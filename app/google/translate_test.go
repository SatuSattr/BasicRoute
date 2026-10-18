package google

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	return m
}

func TestTranslateChatRequest(t *testing.T) {
	body := map[string]any{
		"model": "g/m",
		"messages": []any{
			map[string]any{"role": "system", "content": "be nice"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hi"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,QUJD"}},
			}},
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function", "function": map[string]any{
					"name": "f", "arguments": `{"x":1}`,
				}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "42"},
		},
		"max_tokens":      float64(100),
		"temperature":     0.5,
		"stream":          true,
		"tool_choice":     "auto",
		"tools":           []any{map[string]any{"type": "function", "function": map[string]any{"name": "f", "description": "d", "parameters": map[string]any{"type": "object"}}}},
		"response_format": map[string]any{"type": "json_object"},
	}

	out, err := TranslateRequest(body, InterfaceChat, "gemini-x")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if out["model"] != "gemini-x" || out["store"] != false || out["stream"] != true {
		t.Fatalf("top-level fields wrong: %v", out)
	}
	if out["system_instruction"] != "be nice" {
		t.Fatalf("system_instruction = %v", out["system_instruction"])
	}
	steps := out["input"].([]any)
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2: %v", len(steps), steps)
	}
	user := steps[0].(map[string]any)
	if user["type"] != "user_input" {
		t.Fatalf("step 0 = %v", user)
	}
	parts := user["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("user parts = %d", len(parts))
	}
	img := parts[1].(map[string]any)
	if img["type"] != "image" || img["data"] != "QUJD" || img["mime_type"] != "image/png" {
		t.Fatalf("image part = %v", img)
	}
	fr := steps[1].(map[string]any)
	if fr["type"] != "function_result" || fr["call_id"] != "call_1" || fr["name"] != "f" {
		t.Fatalf("function_result = %v", fr)
	}
	if fr["result"] != "42" {
		t.Fatalf("result = %v", fr["result"])
	}
	gc := out["generation_config"].(map[string]any)
	if gc["max_output_tokens"] != int64(100) || gc["temperature"] != 0.5 {
		t.Fatalf("generation_config = %v", gc)
	}
	if _, ok := gc["response_mime_type"]; ok {
		t.Fatalf("response_mime_type must not be in generation_config: %v", gc)
	}
	if _, ok := out["tool_config"]; ok {
		t.Fatalf("tool_config must not be sent: %v", out["tool_config"])
	}
	rf := out["response_format"].(map[string]any)
	if rf["type"] != "object" {
		t.Fatalf("response_format = %v", rf)
	}
	tools := out["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "f" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestTranslateResponsesRequest(t *testing.T) {
	body := map[string]any{
		"model":        "g/m",
		"instructions": "sys",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "hi"},
			}},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "f", "arguments": `{"a":2}`},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "done"},
		},
		"max_output_tokens": float64(50),
		"reasoning":         map[string]any{"effort": "high"},
		"text":              map[string]any{"format": map[string]any{"type": "json_schema", "name": "x", "schema": map[string]any{"type": "object"}}},
		"tools":             []any{map[string]any{"type": "function", "name": "f", "parameters": map[string]any{"type": "object"}}},
	}

	out, err := TranslateRequest(body, InterfaceResponses, "gemini-x")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if out["system_instruction"] != "sys" {
		t.Fatalf("system_instruction = %v", out["system_instruction"])
	}
	steps := out["input"].([]any)
	if len(steps) != 2 {
		t.Fatalf("steps = %d: %v", len(steps), steps)
	}
	fr := steps[1].(map[string]any)
	if fr["type"] != "function_result" || fr["name"] != "f" || fr["result"] != "done" {
		t.Fatalf("function_result = %v", fr)
	}
	gc := out["generation_config"].(map[string]any)
	if gc["max_output_tokens"] != int64(50) || gc["thinking_level"] != "high" {
		t.Fatalf("generation_config = %v", gc)
	}
	rf := out["response_format"].(map[string]any)
	if rf["type"] != "object" {
		t.Fatalf("response_format = %v", rf)
	}
}

func TestTranslateResponseChat(t *testing.T) {
	raw := []byte(`{"id":"int_1","created":"2026-05-12T18:44:51Z","status":"completed",
		"output_text":"hello",
		"steps":[{"type":"model_output","content":[{"type":"text","text":"hello"}]},
		         {"type":"function_call","id":"c1","name":"f","arguments":{"x":1}}],
		"usage":{"total_tokens":9,"total_input_tokens":4,"total_output_tokens":5}}`)
	b, err := TranslateResponse(raw, InterfaceChat, "g/m")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	m := decode(t, b)
	choice := m["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["content"] != "hello" {
		t.Fatalf("content = %v", msg["content"])
	}
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	tc := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tc["name"] != "f" || tc["arguments"] != `{"x":1}` {
		t.Fatalf("tool call = %v", tc)
	}
	usage := m["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(4) || usage["completion_tokens"] != float64(5) || usage["total_tokens"] != float64(9) {
		t.Fatalf("usage = %v", usage)
	}
}

func TestTranslateResponseResponses(t *testing.T) {
	raw := []byte(`{"id":"int_2","created":"2026-05-12T18:44:51Z","status":"completed",
		"output_text":"hi there","usage":{"total_tokens":7,"total_input_tokens":3,"total_output_tokens":4}}`)
	b, err := TranslateResponse(raw, InterfaceResponses, "g/m")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	m := decode(t, b)
	if m["object"] != "response" || m["output_text"] != "hi there" || m["status"] != "completed" {
		t.Fatalf("response = %v", m)
	}
	output := m["output"].([]any)
	if len(output) != 1 || output[0].(map[string]any)["type"] != "message" {
		t.Fatalf("output = %v", output)
	}
	usage := m["usage"].(map[string]any)
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(4) {
		t.Fatalf("usage = %v", usage)
	}
}

func TestTranslateError(t *testing.T) {
	b := TranslateError([]byte(`{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`), 400)
	m := decode(t, b)
	e := m["error"].(map[string]any)
	if e["message"] != "bad" || e["type"] != "invalid_request_error" || e["code"] != "invalid_argument" {
		t.Fatalf("error = %v", e)
	}
}

func TestTranslateErrorInteractionsShape(t *testing.T) {
	b := TranslateError([]byte(`{"error":{"message":"Unknown parameter 'tool_choice'.","code":"invalid_request"}}`), 400)
	m := decode(t, b)
	e := m["error"].(map[string]any)
	if e["message"] != "Unknown parameter 'tool_choice'." || e["code"] != "invalid_request" || e["type"] != "invalid_request_error" {
		t.Fatalf("error = %v", e)
	}
	b = TranslateError([]byte(`{"error":{"message":"bad key","code":"api_key_invalid"}}`), 400)
	e = decode(t, b)["error"].(map[string]any)
	if e["type"] != "authentication_error" {
		t.Fatalf("auth error = %v", e)
	}
}

func TestStreamChat(t *testing.T) {
	in := strings.Join([]string{
		`event: interaction.created`,
		`data: {"interaction":{"id":"int_1","status":"in_progress","model":"gemini-x"},"event_type":"interaction.created"}`,
		``,
		`event: step.start`,
		`data: {"index":0,"step":{"type":"model_output"},"event_type":"step.start"}`,
		``,
		`event: step.delta`,
		`data: {"index":0,"delta":{"type":"text","text":"Hel"},"event_type":"step.delta"}`,
		``,
		`event: step.delta`,
		`data: {"index":0,"delta":{"type":"text","text":"lo"},"event_type":"step.delta"}`,
		``,
		`event: step.stop`,
		`data: {"index":0,"event_type":"step.stop"}`,
		``,
		`event: interaction.completed`,
		`data: {"interaction":{"id":"int_1","status":"completed","usage":{"total_tokens":5,"total_input_tokens":2,"total_output_tokens":3}},"event_type":"interaction.completed"}`,
		``,
		`event: done`,
		`data: [DONE]`,
		``,
	}, "\n")

	var out bytes.Buffer
	if err := TranslateStream(strings.NewReader(in), &out, InterfaceChat, "g/m"); err != nil {
		t.Fatalf("stream: %v", err)
	}
	got := out.String()
	for _, want := range []string{`"content":"Hel"`, `"content":"lo"`, `"finish_reason":"stop"`, `"total_tokens":5`, `data: [DONE]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestTranslateChatIgnoresToolChoice(t *testing.T) {
	body := map[string]any{
		"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
		"tool_choice": map[string]any{"type": "function", "name": "f"},
	}
	out, err := TranslateRequest(body, InterfaceChat, "m")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if _, ok := out["tool_config"]; ok {
		t.Fatalf("tool_config must not be sent: %v", out["tool_config"])
	}
}

func TestTranslateVideoURLType(t *testing.T) {
	body := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example.com/clip"}},
		}}},
	}
	out, err := TranslateRequest(body, InterfaceChat, "m")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	part := out["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if part["type"] != "video" || part["uri"] != "https://cdn.example.com/clip" {
		t.Fatalf("video part = %v", part)
	}
	if _, ok := part["mime_type"]; ok {
		t.Fatalf("wildcard mime must be omitted: %v", part)
	}
}

func TestStreamEventNameWithoutEventType(t *testing.T) {
	in := strings.Join([]string{
		`event: interaction.created`,
		`data: {"interaction":{"id":"int_1"}}`,
		``,
		`event: step.start`,
		`data: {"index":0,"step":{"type":"model_output"}}`,
		``,
		`event: step.delta`,
		`data: {"index":0,"delta":{"type":"text","text":"ok"}}`,
		``,
		`event: interaction.completed`,
		`data: {"interaction":{"id":"int_1","usage":{"total_tokens":2}}}`,
		``,
	}, "\n")

	var out bytes.Buffer
	if err := TranslateStream(strings.NewReader(in), &out, InterfaceChat, "m"); err != nil {
		t.Fatalf("stream: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"content":"ok"`) || !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Fatalf("event names lost: %s", got)
	}
}

func TestStreamResponsesText(t *testing.T) {
	in := strings.Join([]string{
		`event: interaction.created`,
		`data: {"interaction":{"id":"int_1","status":"in_progress"},"event_type":"interaction.created"}`,
		``,
		`event: step.start`,
		`data: {"index":0,"step":{"type":"model_output"},"event_type":"step.start"}`,
		``,
		`event: step.delta`,
		`data: {"index":0,"delta":{"type":"text","text":"Hi"},"event_type":"step.delta"}`,
		``,
		`event: step.stop`,
		`data: {"index":0,"event_type":"step.stop"}`,
		``,
		`event: interaction.completed`,
		`data: {"interaction":{"id":"int_1","status":"completed","usage":{"total_tokens":4,"total_input_tokens":1,"total_output_tokens":3}},"event_type":"interaction.completed"}`,
		``,
		`event: done`,
		`data: [DONE]`,
		``,
	}, "\n")

	var out bytes.Buffer
	if err := TranslateStream(strings.NewReader(in), &out, InterfaceResponses, "g/m"); err != nil {
		t.Fatalf("stream: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`event: response.created`,
		`event: response.output_text.delta`,
		`event: response.output_text.done`,
		`event: response.output_item.done`,
		`event: response.completed`,
		`"output_text":"Hi"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestStreamResponsesFunctionCall(t *testing.T) {
	in := strings.Join([]string{
		`event: interaction.created`,
		`data: {"interaction":{"id":"int_1"},"event_type":"interaction.created"}`,
		``,
		`event: step.start`,
		`data: {"index":0,"step":{"type":"function_call","id":"c1","name":"f"},"event_type":"step.start"}`,
		``,
		`event: step.delta`,
		`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"{\"a\":"},"event_type":"step.delta"}`,
		``,
		`event: step.delta`,
		`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"2}"},"event_type":"step.delta"}`,
		``,
		`event: step.stop`,
		`data: {"index":0,"event_type":"step.stop"}`,
		``,
		`event: interaction.completed`,
		`data: {"interaction":{"id":"int_1","status":"requires_action"},"event_type":"interaction.completed"}`,
		``,
		`event: done`,
		`data: [DONE]`,
		``,
	}, "\n")

	var out bytes.Buffer
	if err := TranslateStream(strings.NewReader(in), &out, InterfaceResponses, "g/m"); err != nil {
		t.Fatalf("stream: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`"type":"function_call"`,
		`event: response.function_call_arguments.delta`,
		`"arguments":"{\"a\":2}"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}
