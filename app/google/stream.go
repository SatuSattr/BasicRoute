package google

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// TranslateStream consumes Google Interactions SSE events from r and writes
// [OI]-compatible SSE events to w. It returns when the upstream stream ends.
func TranslateStream(r io.Reader, w io.Writer, iface, modelID string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	t := &streamTranslator{
		w:         w,
		iface:     iface,
		model:     modelID,
		created:   time.Now().Unix(),
		toolIndex: map[int]int{},
	}

	var event string
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			event, data = "", nil
			return nil
		}
		payload := strings.Join(data, "\n")
		ev := event
		event, data = "", nil
		return t.handle(ev, payload)
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if err := dispatch(); err != nil {
		return err
	}
	return t.finish()
}

type streamTranslator struct {
	w     io.Writer
	iface string
	model string

	id      string
	created int64
	seq     int
	usage   map[string]any

	// chat-completions state
	chatDone  bool
	sentRole  bool
	toolCount int
	toolIndex map[int]int

	// responses-api state
	createdSent bool
	completed   bool
	output      []any
	allText     strings.Builder
	curKind     string
	curIndex    int
	itemID      string
	callID      string
	funcName    string
	text        strings.Builder
	args        strings.Builder
}

func (t *streamTranslator) handle(event, payload string) error {
	if strings.TrimSpace(payload) == "[DONE]" {
		return t.finish()
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return nil // abaikan event yang tidak bisa diparse
	}
	if event == "" {
		event = str(m["event_type"])
	}
	if strings.EqualFold(strings.TrimSpace(t.iface), InterfaceResponses) {
		t.handleResponses(event, m)
		return nil
	}
	t.handleChat(event, m)
	return nil
}

func (t *streamTranslator) finish() error {
	if strings.EqualFold(strings.TrimSpace(t.iface), InterfaceResponses) {
		t.finishCurrent()
		t.completeResponse("completed")
		return nil
	}
	t.finishChat()
	return nil
}

// ---- chat completions ----

func (t *streamTranslator) handleChat(event string, m map[string]any) {
	switch event {
	case "interaction.created":
		if in := asMap(m["interaction"]); in != nil {
			t.setInteraction(in)
		}
	case "step.start":
		step := asMap(m["step"])
		if step == nil || str(step["type"]) != "function_call" {
			return
		}
		idx := int(num(m["index"]))
		ti := t.toolCount
		t.toolCount++
		t.toolIndex[idx] = ti
		t.emitChatDelta(map[string]any{
			"tool_calls": []any{map[string]any{
				"index": ti,
				"id":    str(step["id"]),
				"type":  "function",
				"function": map[string]any{
					"name":      str(step["name"]),
					"arguments": "",
				},
			}},
		})
	case "step.delta":
		delta := asMap(m["delta"])
		if delta == nil {
			return
		}
		switch str(delta["type"]) {
		case "text":
			t.emitChatDelta(map[string]any{"content": str(delta["text"])})
		case "arguments_delta":
			ti := t.toolIndex[int(num(m["index"]))]
			t.emitChatDelta(map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    ti,
					"function": map[string]any{"arguments": str(delta["arguments"])},
				}},
			})
		}
	case "interaction.completed":
		if in := asMap(m["interaction"]); in != nil {
			t.setInteraction(in)
			t.usage = asMap(in["usage"])
		}
		t.finishChat()
	case "error":
		t.emitChatError(m)
	}
}

func (t *streamTranslator) emitChatDelta(delta map[string]any) {
	if t.chatDone {
		return
	}
	if !t.sentRole {
		delta["role"] = "assistant"
		t.sentRole = true
	}
	writeSSE(t.w, "", map[string]any{
		"id":      t.chatID(),
		"object":  "chat.completion.chunk",
		"created": t.created,
		"model":   t.model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}},
	})
}

func (t *streamTranslator) finishChat() {
	if t.chatDone {
		return
	}
	t.chatDone = true
	finish := "stop"
	if t.toolCount > 0 {
		finish = "tool_calls"
	}
	in, out, total := usageNumbers(map[string]any{"usage": t.usage})
	writeSSE(t.w, "", map[string]any{
		"id":      t.chatID(),
		"object":  "chat.completion.chunk",
		"created": t.created,
		"model":   t.model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     in,
			"completion_tokens": out,
			"total_tokens":      total,
		},
	})
	_, _ = io.WriteString(t.w, "data: [DONE]\n\n")
}

func (t *streamTranslator) emitChatError(m map[string]any) {
	if t.chatDone {
		return
	}
	t.chatDone = true
	e := asMap(m["error"])
	msg, code := "upstream error", "upstream_error"
	if e != nil {
		if s := str(e["message"]); s != "" {
			msg = s
		}
		if s := str(e["code"]); s != "" {
			code = s
		}
	}
	writeSSE(t.w, "", map[string]any{
		"error": map[string]any{"message": msg, "type": "server_error", "code": code},
	})
	_, _ = io.WriteString(t.w, "data: [DONE]\n\n")
}

func (t *streamTranslator) chatID() string {
	if t.id == "" {
		return "chatcmpl-google"
	}
	return "chatcmpl-" + t.id
}

// ---- responses api ----

func (t *streamTranslator) handleResponses(event string, m map[string]any) {
	switch event {
	case "interaction.created":
		if in := asMap(m["interaction"]); in != nil {
			t.setInteraction(in)
		}
		t.emitResponseCreated()
	case "step.start":
		step := asMap(m["step"])
		if step == nil {
			return
		}
		switch str(step["type"]) {
		case "model_output":
			t.startMessage()
		case "function_call":
			t.startFunction(step)
		}
	case "step.delta":
		delta := asMap(m["delta"])
		if delta == nil {
			return
		}
		switch str(delta["type"]) {
		case "text":
			t.text.WriteString(str(delta["text"]))
			t.emitResponseEvent("response.output_text.delta", map[string]any{
				"item_id":       t.itemID,
				"output_index":  t.curIndex,
				"content_index": 0,
				"delta":         str(delta["text"]),
			})
		case "arguments_delta":
			t.args.WriteString(str(delta["arguments"]))
			t.emitResponseEvent("response.function_call_arguments.delta", map[string]any{
				"item_id":      t.itemID,
				"output_index": t.curIndex,
				"delta":        str(delta["arguments"]),
			})
		}
	case "step.stop":
		t.finishCurrent()
	case "interaction.completed":
		if in := asMap(m["interaction"]); in != nil {
			t.setInteraction(in)
			t.usage = asMap(in["usage"])
		}
		t.finishCurrent()
		t.completeResponse("completed")
	case "error":
		t.finishCurrent()
		t.failedResponse(m)
	}
}

func (t *streamTranslator) startMessage() {
	t.curKind = "message"
	t.curIndex = len(t.output)
	t.text.Reset()
	t.itemID = fmt.Sprintf("msg_%s_%d", t.shortID(), t.curIndex)
	t.emitResponseEvent("response.output_item.added", map[string]any{
		"output_index": t.curIndex,
		"item": map[string]any{
			"id":      t.itemID,
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})
	t.emitResponseEvent("response.content_part.added", map[string]any{
		"item_id":       t.itemID,
		"output_index":  t.curIndex,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	})
}

func (t *streamTranslator) startFunction(step map[string]any) {
	t.curKind = "function_call"
	t.curIndex = len(t.output)
	t.args.Reset()
	t.callID = str(step["id"])
	if t.callID == "" {
		t.callID = fmt.Sprintf("call_%d", t.curIndex)
	}
	t.funcName = str(step["name"])
	t.itemID = "fc_" + t.callID
	t.emitResponseEvent("response.output_item.added", map[string]any{
		"output_index": t.curIndex,
		"item": map[string]any{
			"id":        t.itemID,
			"type":      "function_call",
			"status":    "in_progress",
			"call_id":   t.callID,
			"name":      t.funcName,
			"arguments": "",
		},
	})
}

func (t *streamTranslator) finishCurrent() {
	switch t.curKind {
	case "message":
		text := t.text.String()
		t.emitResponseEvent("response.output_text.done", map[string]any{
			"item_id":       t.itemID,
			"output_index":  t.curIndex,
			"content_index": 0,
			"text":          text,
		})
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
		t.emitResponseEvent("response.content_part.done", map[string]any{
			"item_id":       t.itemID,
			"output_index":  t.curIndex,
			"content_index": 0,
			"part":          part,
		})
		item := responsesMessageItem(t.itemID, text)
		t.emitResponseEvent("response.output_item.done", map[string]any{
			"output_index": t.curIndex,
			"item":         item,
		})
		t.output = append(t.output, item)
		t.allText.WriteString(text)
	case "function_call":
		args := t.args.String()
		if args == "" {
			args = "{}"
		}
		t.emitResponseEvent("response.function_call_arguments.done", map[string]any{
			"item_id":      t.itemID,
			"output_index": t.curIndex,
			"arguments":    args,
		})
		item := map[string]any{
			"id":        t.itemID,
			"type":      "function_call",
			"status":    "completed",
			"call_id":   t.callID,
			"name":      t.funcName,
			"arguments": args,
		}
		t.emitResponseEvent("response.output_item.done", map[string]any{
			"output_index": t.curIndex,
			"item":         item,
		})
		t.output = append(t.output, item)
	}
	t.curKind = ""
}

func (t *streamTranslator) emitResponseCreated() {
	if t.createdSent {
		return
	}
	t.createdSent = true
	t.emitResponseEvent("response.created", map[string]any{
		"response": t.responseObject("in_progress", false),
	})
}

func (t *streamTranslator) completeResponse(status string) {
	if t.completed {
		return
	}
	t.completed = true
	t.emitResponseEvent("response.completed", map[string]any{
		"response": t.responseObject(status, true),
	})
}

func (t *streamTranslator) failedResponse(m map[string]any) {
	if t.completed {
		return
	}
	t.completed = true
	e := asMap(m["error"])
	msg, code := "upstream error", "upstream_error"
	if e != nil {
		if s := str(e["message"]); s != "" {
			msg = s
		}
		if s := str(e["code"]); s != "" {
			code = s
		}
	}
	resp := t.responseObject("failed", true)
	resp["error"] = map[string]any{"code": code, "message": msg}
	t.emitResponseEvent("response.failed", map[string]any{"response": resp})
}

func (t *streamTranslator) responseObject(status string, withUsage bool) map[string]any {
	resp := map[string]any{
		"id":          "resp_" + t.shortID(),
		"object":      "response",
		"created_at":  t.created,
		"status":      status,
		"model":       t.model,
		"output":      t.output,
		"output_text": t.allText.String(),
	}
	if withUsage {
		in, out, total := usageNumbers(map[string]any{"usage": t.usage})
		resp["usage"] = map[string]any{
			"input_tokens":  in,
			"output_tokens": out,
			"total_tokens":  total,
		}
	}
	return resp
}

func (t *streamTranslator) emitResponseEvent(typ string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["type"] = typ
	payload["sequence_number"] = t.seq
	t.seq++
	writeSSE(t.w, typ, payload)
}

func (t *streamTranslator) shortID() string {
	if t.id == "" {
		return "google"
	}
	return t.id
}

func (t *streamTranslator) setInteraction(in map[string]any) {
	if id := str(in["id"]); id != "" {
		t.id = id
	}
	if created := interactionCreated(in); created > 0 {
		t.created = created
	}
}

func writeSSE(w io.Writer, event string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}
