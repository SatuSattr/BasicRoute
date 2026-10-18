package google

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"basicroute/app/model"
)

// TranslateResponse converts a non-streaming Google Interaction object into
// the [OI]-compatible response shape selected by iface.
func TranslateResponse(body []byte, iface, modelID string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	text := interactionText(m)
	calls := interactionCalls(m)
	created := interactionCreated(m)
	id := str(m["id"])
	if id == "" {
		id = fmt.Sprintf("google-%d", created)
	}

	if strings.EqualFold(strings.TrimSpace(iface), InterfaceResponses) {
		return json.Marshal(responsesResponse(m, text, calls, created, id, modelID))
	}
	return json.Marshal(chatResponse(m, text, calls, created, id, modelID))
}

// IsAuthError reports whether a Google error body indicates an invalid API
// key (Google returns 400 INVALID_ARGUMENT / API_KEY_INVALID for these).
func IsAuthError(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "api_key_invalid") ||
		strings.Contains(s, "api key not valid") ||
		strings.Contains(s, "invalid api key")
}

// TranslateError converts a Google error body into an [OI] error envelope.
// Bentuk error Google bermacam: {code,message,status} klasik, atau
// {message,code:"invalid_request"} ala Interactions.
func TranslateError(body []byte, status int) []byte {
	msg := ""
	code := ""
	var m map[string]any
	if err := json.Unmarshal(body, &m); err == nil {
		if e := asMap(m["error"]); e != nil {
			msg = str(e["message"])
			switch sc := e["status"].(type) {
			case string:
				code = strings.ToLower(strings.TrimSpace(sc))
			}
			if code == "" {
				if cc, ok := e["code"].(string); ok {
					code = strings.ToLower(strings.TrimSpace(cc))
				}
			}
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = "upstream error"
	}
	if code == "" {
		code = "upstream_error"
	}
	out, err := json.Marshal(model.NewError(msg, errorType(status, code), code))
	if err != nil {
		return body
	}
	return out
}

// errorType memilih tipe error [OI] dari status HTTP / kode Google.
func errorType(status int, code string) string {
	switch code {
	case "unauthenticated", "permission_denied", "api_key_invalid":
		return "authentication_error"
	case "not_found":
		return "not_found_error"
	case "too_many_requests", "resource_exhausted", "rate_limit_exceeded":
		return "rate_limit_error"
	}
	if status >= 500 {
		return "server_error"
	}
	return "invalid_request_error"
}

// interactionStatus mengambil status interaksi Google ("" bila tak ada).
func interactionStatus(m map[string]any) string {
	return strings.ToLower(strings.TrimSpace(str(m["status"])))
}

func chatResponse(m map[string]any, text string, calls []funcCall, created int64, id, modelID string) map[string]any {
	msg := map[string]any{"role": "assistant"}
	finish := "stop"
	if interactionStatus(m) == "incomplete" {
		finish = "length"
	}
	if len(calls) > 0 {
		finish = "tool_calls"
		if text == "" {
			msg["content"] = nil
		} else {
			msg["content"] = text
		}
		toolCalls := make([]any, 0, len(calls))
		for _, c := range calls {
			toolCalls = append(toolCalls, map[string]any{
				"id":       c.ID,
				"type":     "function",
				"function": map[string]any{"name": c.Name, "arguments": c.Arguments},
			})
		}
		msg["tool_calls"] = toolCalls
	} else {
		msg["content"] = text
	}

	in, out, total := usageNumbers(m)
	return map[string]any{
		"id":      "chatcmpl-" + id,
		"object":  "chat.completion",
		"created": created,
		"model":   modelID,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     in,
			"completion_tokens": out,
			"total_tokens":      total,
		},
	}
}

func responsesResponse(m map[string]any, text string, calls []funcCall, created int64, id, modelID string) map[string]any {
	output := []any{}
	if text != "" {
		output = append(output, responsesMessageItem("msg_"+id, text))
	}
	for _, c := range calls {
		output = append(output, responsesFunctionItem(c))
	}
	in, out, total := usageNumbers(m)
	status := interactionStatus(m)
	switch status {
	case "completed", "incomplete", "failed":
	default:
		status = "completed"
	}
	return map[string]any{
		"id":          "resp_" + id,
		"object":      "response",
		"created_at":  created,
		"status":      status,
		"model":       modelID,
		"output":      output,
		"output_text": text,
		"usage": map[string]any{
			"input_tokens":  in,
			"output_tokens": out,
			"total_tokens":  total,
		},
	}
}

func responsesMessageItem(id, text string) map[string]any {
	return map[string]any{
		"id":     id,
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{map[string]any{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
		}},
	}
}

func responsesFunctionItem(c funcCall) map[string]any {
	return map[string]any{
		"id":        "fc_" + c.ID,
		"type":      "function_call",
		"status":    "completed",
		"call_id":   c.ID,
		"name":      c.Name,
		"arguments": c.Arguments,
	}
}

type funcCall struct {
	ID        string
	Name      string
	Arguments string
}

func interactionText(m map[string]any) string {
	if s := str(m["output_text"]); s != "" {
		return s
	}
	var b strings.Builder
	for _, sv := range asSlice(m["steps"]) {
		st := asMap(sv)
		if st == nil || str(st["type"]) != "model_output" {
			continue
		}
		for _, cv := range asSlice(st["content"]) {
			c := asMap(cv)
			if c != nil && str(c["type"]) == "text" {
				b.WriteString(str(c["text"]))
			}
		}
	}
	return b.String()
}

func interactionCalls(m map[string]any) []funcCall {
	out := []funcCall{}
	for _, sv := range asSlice(m["steps"]) {
		st := asMap(sv)
		if st == nil || str(st["type"]) != "function_call" {
			continue
		}
		args := str(st["arguments"])
		if args == "" {
			if st["arguments"] != nil {
				if b, err := json.Marshal(st["arguments"]); err == nil {
					args = string(b)
				}
			}
		}
		if args == "" {
			args = "{}"
		}
		id := str(st["id"])
		if id == "" {
			id = fmt.Sprintf("call_%d", len(out)+1)
		}
		out = append(out, funcCall{
			ID:        id,
			Name:      str(st["name"]),
			Arguments: args,
		})
	}
	return out
}

func interactionCreated(m map[string]any) int64 {
	switch v := m["created"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.Unix()
		}
	case float64:
		if v > 0 {
			return int64(v)
		}
	}
	return time.Now().Unix()
}

func usageNumbers(m map[string]any) (in, out, total int64) {
	u := asMap(m["usage"])
	if u == nil {
		return 0, 0, 0
	}
	in = int64(num(u["total_input_tokens"]))
	out = int64(num(u["total_output_tokens"]))
	total = int64(num(u["total_tokens"]))
	if total == 0 {
		total = in + out
	}
	return in, out, total
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
