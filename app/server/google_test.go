package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"basicroute/app/config"
)

func googleSnapshot(baseURL string) *config.Snapshot {
	prov := &config.Provider{
		Name:      "G",
		Prefix:    "g",
		Type:      config.ProviderTypeGoogle,
		Interface: config.InterfaceInteractions,
		BaseURL:   baseURL,
		Enabled:   config.FlexBool(true),
		Keys: map[string]*config.ProvKey{
			"k1": {Key: "g-key", Order: config.FlexInt(1), Enabled: config.FlexBool(true)},
		},
		Models: map[string]*config.Model{
			"m": {Name: "M", Enabled: config.FlexBool(true)},
		},
	}
	return &config.Snapshot{
		Raw: &config.Config{
			System:     config.System{ProxyTestURL: "http://127.0.0.1:1/none"},
			Providers:  map[string]*config.Provider{"g": prov},
			ProxyPools: map[string]*config.ProxyPool{},
		},
		ModelMap: map[string]*config.ModelRef{
			"g/m": {ProviderSlug: "g", Provider: prov, ModelSlug: "m", Model: prov.Models["m"], FullID: "g/m"},
		},
		KeyRings: map[string][]*config.KeyEntry{
			"g": {{Name: "k1", Key: "g-key", Order: 1}},
		},
	}
}

func googleRequest(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGoogleChatNonStreaming(t *testing.T) {
	var gotPath, gotKey, gotRevision, gotQuery string
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotKey = r.Header.Get("x-goog-api-key")
		gotRevision = r.Header.Get("Api-Revision")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"int_1","created":"2026-05-12T18:44:51Z","status":"completed",
			"output_text":"hello","usage":{"total_tokens":9,"total_input_tokens":4,"total_output_tokens":5}}`))
	}))
	defer up.Close()

	srv := newTestServer(googleSnapshot(up.URL + "/v1beta"))
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/m","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1beta/interactions" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q, want empty", gotQuery)
	}
	if gotKey != "g-key" || gotRevision == "" {
		t.Fatalf("auth headers: key=%q revision=%q", gotKey, gotRevision)
	}
	if gotBody["model"] != "m" || gotBody["store"] != false {
		t.Fatalf("upstream body = %v", gotBody)
	}
	gc, _ := gotBody["generation_config"].(map[string]any)
	if gc["max_output_tokens"] != float64(10) {
		t.Fatalf("generation_config = %v", gc)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response decode: %v", err)
	}
	if resp["model"] != "g/m" || resp["object"] != "chat.completion" {
		t.Fatalf("response = %v", resp)
	}
	choice := resp["choices"].([]any)[0].(map[string]any)
	if choice["message"].(map[string]any)["content"] != "hello" {
		t.Fatalf("choices = %v", resp["choices"])
	}
}

func TestGoogleChatStreaming(t *testing.T) {
	var gotQuery string
	var gotStream any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotStream = body["stream"]

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, chunk := range []string{
			"event: interaction.created\ndata: {\"interaction\":{\"id\":\"int_1\",\"status\":\"in_progress\"},\"event_type\":\"interaction.created\"}\n\n",
			"event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"model_output\"},\"event_type\":\"step.start\"}\n\n",
			"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"Hel\"},\"event_type\":\"step.delta\"}\n\n",
			"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"lo\"},\"event_type\":\"step.delta\"}\n\n",
			"event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n",
			"event: interaction.completed\ndata: {\"interaction\":{\"id\":\"int_1\",\"status\":\"completed\",\"usage\":{\"total_tokens\":5,\"total_input_tokens\":2,\"total_output_tokens\":3}},\"event_type\":\"interaction.completed\"}\n\n",
			"event: done\ndata: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, chunk)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer up.Close()

	srv := newTestServer(googleSnapshot(up.URL + "/v1beta"))
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotQuery != "alt=sse" || gotStream != true {
		t.Fatalf("query=%q stream=%v", gotQuery, gotStream)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"object":"chat.completion.chunk"`,
		`"content":"Hel"`,
		`"content":"lo"`,
		`"finish_reason":"stop"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

func TestGoogleResponsesNonStreaming(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"int_2","created":"2026-05-12T18:44:51Z","status":"completed",
			"output_text":"hi there","usage":{"total_tokens":7,"total_input_tokens":3,"total_output_tokens":4}}`))
	}))
	defer up.Close()

	srv := newTestServer(googleSnapshot(up.URL + "/v1beta"))
	rec := googleRequest(t, srv, "/v1/responses", `{"model":"g/m","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["object"] != "response" || resp["output_text"] != "hi there" {
		t.Fatalf("response = %v", resp)
	}
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(4) {
		t.Fatalf("usage = %v", usage)
	}
}

func TestGoogleErrorTranslation(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"bad input","status":"INVALID_ARGUMENT"}}`)
	}))
	defer up.Close()

	srv := newTestServer(googleSnapshot(up.URL + "/v1beta"))
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	e := resp["error"].(map[string]any)
	if e["message"] != "bad input" || e["code"] != "invalid_argument" {
		t.Fatalf("error = %v", e)
	}
}

func TestGoogleInvalidKeyFailsOver(t *testing.T) {
	var keys []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-goog-api-key")
		keys = append(keys, key)
		w.Header().Set("Content-Type", "application/json")
		if key == "bad-key" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"int_4","output_text":"ok"}`)
	}))
	defer up.Close()

	snap := googleSnapshot(up.URL + "/v1beta")
	prov := snap.Raw.Providers["g"]
	prov.Keys["k0"] = &config.ProvKey{Key: "bad-key", Order: config.FlexInt(0), Enabled: config.FlexBool(true)}
	snap.KeyRings["g"] = []*config.KeyEntry{
		{Name: "k0", Key: "bad-key", Order: 0},
		{Name: "k1", Key: "g-key", Order: 1},
	}

	srv := newTestServer(snap)
	rec := googleRequest(t, srv, "/v1/chat/completions",
		`{"model":"g/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(keys) != 2 || keys[0] != "bad-key" || keys[1] != "g-key" {
		t.Fatalf("keys used = %v", keys)
	}
}

func TestGoogleUpstreamKeyHeaderNotForwarded(t *testing.T) {
	var sawKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"int_3","output_text":"ok"}`)
	}))
	defer up.Close()

	srv := newTestServer(googleSnapshot(up.URL + "/v1beta"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"g/m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-goog-api-key", "client-supplied")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if sawKey != "g-key" {
		t.Fatalf("upstream key = %q, want provider key", sawKey)
	}
}
