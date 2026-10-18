package server

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"basicroute/app/config"
)

const liveModel = "gemini-2.5-flash"

func liveSnapshot(t *testing.T, key, model string) *config.Snapshot {
	t.Helper()
	snap := googleSnapshot("https://generativelanguage.googleapis.com/v1beta")
	prov := snap.Raw.Providers["g"]
	prov.Keys = map[string]*config.ProvKey{
		"k1": {Key: key, Order: config.FlexInt(1), Enabled: config.FlexBool(true)},
	}
	prov.Models = map[string]*config.Model{
		model: {Name: "M", Enabled: config.FlexBool(true)},
	}
	snap.KeyRings["g"] = []*config.KeyEntry{{Name: "k1", Key: key, Order: 1}}
	snap.ModelMap = map[string]*config.ModelRef{
		"g/" + model: {ProviderSlug: "g", Provider: prov, ModelSlug: model, Model: prov.Models[model], FullID: "g/" + model},
	}
	return snap
}

func liveKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("BR_GOOGLE_TEST_KEY")
	if key == "" {
		t.Skip("set BR_GOOGLE_TEST_KEY to run live Google tests")
	}
	return key
}

func TestLiveGoogleChat(t *testing.T) {
	srv := newTestServer(liveSnapshot(t, liveKey(t), liveModel))
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/`+liveModel+`","messages":[{"role":"user","content":"Reply with exactly the word: pong"}],"max_tokens":32}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Logf("chat: %s", rec.Body.String())
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["model"] != "g/"+liveModel || resp["object"] != "chat.completion" {
		t.Fatalf("bad envelope: %v", resp)
	}
	choice := resp["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if s, _ := msg["content"].(string); strings.TrimSpace(s) == "" {
		t.Fatalf("empty content: %v", msg)
	}
	usage := resp["usage"].(map[string]any)
	if usage["total_tokens"].(float64) <= 0 {
		t.Fatalf("usage not populated: %v", usage)
	}
}

func TestLiveGoogleChatStream(t *testing.T) {
	srv := newTestServer(liveSnapshot(t, liveKey(t), liveModel))
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/`+liveModel+`","stream":true,"messages":[{"role":"user","content":"Count from 1 to 5, digits only, separated by spaces"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	t.Logf("stream head: %.400s", body)
	if !strings.Contains(body, `"object":"chat.completion.chunk"`) {
		t.Fatalf("no chunks: %s", body)
	}
	if !strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("no DONE: %s", body)
	}
}

func TestLiveGoogleResponses(t *testing.T) {
	srv := newTestServer(liveSnapshot(t, liveKey(t), liveModel))
	rec := googleRequest(t, srv, "/v1/responses",
		`{"model":"g/`+liveModel+`","input":"Reply with exactly the word: pong","max_output_tokens":32}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Logf("responses: %s", rec.Body.String())
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["object"] != "response" {
		t.Fatalf("bad envelope: %v", resp)
	}
	if s, _ := resp["output_text"].(string); strings.TrimSpace(s) == "" {
		t.Fatalf("empty output_text: %v", resp)
	}
}

func TestLiveGoogleResponsesStream(t *testing.T) {
	srv := newTestServer(liveSnapshot(t, liveKey(t), liveModel))
	rec := googleRequest(t, srv, "/v1/responses",
		`{"model":"g/`+liveModel+`","stream":true,"input":"Count from 1 to 5, digits only, separated by spaces"}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	t.Logf("responses stream head: %.500s", body)
	for _, want := range []string{`event: response.created`, `event: response.output_text.delta`, `event: response.completed`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

func TestLiveGoogleTools(t *testing.T) {
	srv := newTestServer(liveSnapshot(t, liveKey(t), liveModel))
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/`+liveModel+`","messages":[{"role":"user","content":"What is the weather in Paris? Use the get_weather tool."}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],"tool_choice":"auto"}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Logf("tools: %s", rec.Body.String())
	if !strings.Contains(rec.Body.String(), "tool_calls") {
		t.Fatalf("expected tool_calls: %s", rec.Body.String())
	}
}

func TestLiveGoogleToolLoop(t *testing.T) {
	key := liveKey(t)
	model := "gemini-2.5-flash-lite"
	srv := newTestServer(liveSnapshot(t, key, model))
	ask := `{"role":"user","content":"What is the weather in Paris? Use the get_weather tool."}`
	tools := `[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/`+model+`","messages":[`+ask+`],"tools":`+tools+`}`)
	if rec.Code != 200 {
		t.Fatalf("turn1 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	choice := resp["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) == 0 {
		t.Fatalf("no tool_calls: %s", rec.Body.String())
	}
	call := calls[0].(map[string]any)
	id, _ := call["id"].(string)
	fn := call["function"].(map[string]any)
	name, _ := fn["name"].(string)
	args, _ := fn["arguments"].(string)
	assistant := `{"role":"assistant","content":null,"tool_calls":[{"id":` + strconv.Quote(id) +
		`,"type":"function","function":{"name":` + strconv.Quote(name) + `,"arguments":` + strconv.Quote(args) + `}}]}`
	toolMsg := `{"role":"tool","tool_call_id":` + strconv.Quote(id) + `,"content":"22C sunny"}`
	rec2 := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/`+model+`","messages":[`+ask+`,`+assistant+`,`+toolMsg+`],"tools":`+tools+`}`)
	if rec2.Code != 200 {
		t.Fatalf("turn2 status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	t.Logf("tool loop: %s", rec2.Body.String())
	if !strings.Contains(rec2.Body.String(), "22") {
		t.Fatalf("final answer missing tool result: %s", rec2.Body.String())
	}
}

func TestLiveGoogleVision(t *testing.T) {
	srv := newTestServer(liveSnapshot(t, liveKey(t), liveModel))
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/`+liveModel+`","reasoning_effort":"low","messages":[{"role":"user","content":[{"type":"text","text":"What color is this 1x1 image? One word."},{"type":"image_url","image_url":{"url":"data:image/png;base64,`+png+`"}}]}],"max_tokens":128}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Logf("vision: %s", rec.Body.String())
}
