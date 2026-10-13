package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mock 9router-style relay: meneruskan ke x-relay-target + x-relay-path.
func newMockRelay(t *testing.T) (*httptest.Server, *map[string]string) {
	t.Helper()
	got := &map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("x-relay-target")
		path := r.Header.Get("x-relay-path")
		if target == "" {
			http.Error(w, `{"error":"Missing x-relay-target header"}`, http.StatusBadRequest)
			return
		}
		(*got)["target"] = target
		(*got)["path"] = path
		body, _ := io.ReadAll(r.Body)
		upReq, err := http.NewRequest(r.Method, strings.TrimRight(target, "/")+path, strings.NewReader(string(body)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vv := range r.Header {
			lk := strings.ToLower(k)
			if lk == "x-relay-target" || lk == "x-relay-path" || lk == "host" {
				continue
			}
			for _, v := range vv {
				upReq.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestRelayTransportHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"total_tokens":42}`))
	}))
	t.Cleanup(upstream.Close)

	relay, got := newMockRelay(t)

	pool := NewPool()
	client, err := pool.ClientFor("relay", relay.URL)
	if err != nil {
		t.Fatalf("ClientFor relay: %v", err)
	}
	resp, err := client.Get(upstream.URL + "/v1/chat/completions?x=1")
	if err != nil {
		t.Fatalf("GET via relay: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("unexpected relay response: %d %s", resp.StatusCode, body)
	}
	if (*got)["target"] != upstream.URL {
		t.Fatalf("x-relay-target = %q, want %q", (*got)["target"], upstream.URL)
	}
	if (*got)["path"] != "/v1/chat/completions?x=1" {
		t.Fatalf("x-relay-path = %q", (*got)["path"])
	}
}

func TestRelayTestProxy(t *testing.T) {
	relay, _ := newMockRelay(t)
	// testURL bebas: relay akan fetch target+path sungguhan, jadi pakai
	// upstream lokal agar tidak tergantung internet.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	if err := TestProxy("relay", relay.URL, upstream.URL+"/generate_204", 10*time.Second); err != nil {
		t.Fatalf("TestProxy relay: %v", err)
	}
}

func TestForwardProxyURLWithCreds(t *testing.T) {
	// Konstruksi client forward proxy dengan format user:pass@host:port
	// (mis. 711Proxy/9Proxy) tidak boleh error; dial terjadi saat request.
	pool := NewPool()
	if _, err := pool.ClientFor("http", "http://customer123:your_password@proxy.example.com:8080"); err != nil {
		t.Fatalf("ClientFor http creds: %v", err)
	}
	if _, err := pool.ClientFor("", "http://127.0.0.1:8080"); err != nil {
		t.Fatalf("ClientFor default type: %v", err)
	}
	if _, err := pool.ClientFor("relay", "http://user:pass@relay.example.com"); err == nil {
		t.Fatalf("ClientFor relay with userinfo should fail")
	}
}
