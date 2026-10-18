package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"basicroute/app/config"
)

// bodySink menyimpan body terakhir yang diterima upstream (aman race).
type bodySink struct {
	mu  sync.Mutex
	buf []byte
}

func (b *bodySink) set(p []byte) {
	b.mu.Lock()
	b.buf = append([]byte(nil), p...)
	b.mu.Unlock()
}

func (b *bodySink) get() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf
}

var hopHeaders = map[string]bool{
	"host": true, "content-length": true, "connection": true,
	"proxy-connection": true, "proxy-authorization": true,
	"x-relay-target": true, "x-relay-path": true,
}

func copyReqHeaders(dst, src http.Header) {
	for k, vv := range src {
		if hopHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// newCaptureUpstream membalas 200 dan merekam body request yang diterima.
func newCaptureUpstream(t *testing.T, sink *bodySink) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sink.set(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(s.Close)
	return s
}

// newForwardProxy meniru forward proxy http (absolute-form request).
func newForwardProxy(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		copyReqHeaders(out.Header, r.Header)
		resp, err := http.DefaultTransport.RoundTrip(out)
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
	t.Cleanup(s.Close)
	return s
}

// newRelay meniru relay ala 9router: forward ke x-relay-target + x-relay-path.
func newRelay(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("x-relay-target")
		path := r.Header.Get("x-relay-path")
		if target == "" || path == "" {
			http.Error(w, `{"error":"missing relay headers"}`, http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(r.Body)
		out, err := http.NewRequest(r.Method, target+path, bytes.NewReader(b))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		copyReqHeaders(out.Header, r.Header)
		resp, err := http.DefaultTransport.RoundTrip(out)
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
	t.Cleanup(s.Close)
	return s
}

// mediaCase menyiapkan server dengan upstream yang merekam body, dialirkan
// lewat jalur proxy sesuai kind ("direct", "http", "relay").
func mediaCase(t *testing.T, kind string) (*Server, *bodySink) {
	t.Helper()
	sink := &bodySink{}
	up := newCaptureUpstream(t, sink)

	var snap *config.Snapshot
	switch kind {
	case "direct":
		snap = testSnap("", nil, nil)
	case "http":
		fp := newForwardProxy(t)
		snap = testSnap("fp", map[string]*config.ProxyPool{
			"fp": {Type: config.ProxyTypeHTTP, URL: fp.URL, Order: 1, Enabled: true},
		}, []string{"fp"})
	case "relay":
		rl := newRelay(t)
		snap = testSnap("rl", map[string]*config.ProxyPool{
			"rl": {Type: config.ProxyTypeRelay, URL: rl.URL, Order: 1, Enabled: true},
		}, []string{"rl"})
	default:
		t.Fatalf("unknown proxy kind %q", kind)
	}
	snap.Raw.Providers["p1"].BaseURL = up.URL + "/v1"
	return newTestServer(snap), sink
}

// Marker base64 (tak berubah setelah round-trip JSON) untuk membuktikan isi
// media tidak dirusak saat diteruskan.
const mediaMarker = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="

func mediaBody() string {
	return fmt.Sprintf(`{"model":"p/m","stream":false,"messages":[{"role":"user","content":[`+
		`{"type":"text","text":"describe this"},`+
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}},`+
		`{"type":"input_audio","input_audio":{"data":"%s","format":"wav"}},`+
		`{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4"}}]}]}`, mediaMarker, mediaMarker)
}

// TestMediaPassthrough membuktikan body bermedia (image/audio/video) sampai ke
// upstream utuh lewat direct, forward proxy http, maupun relay -- yang berubah
// hanya field "model".
func TestMediaPassthrough(t *testing.T) {
	for _, kind := range []string{"direct", "http", "relay"} {
		t.Run(kind, func(t *testing.T) {
			srv, sink := mediaCase(t, kind)
			rec := chatRequest(t, srv, mediaBody())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			got := string(sink.get())
			if got == "" {
				t.Fatal("upstream received an empty body")
			}
			// Model di-rewrite ke slug upstream.
			if !strings.Contains(got, `"model":"m"`) {
				t.Fatalf("model not rewritten to upstream slug: %s", got)
			}
			// Isi media bertahan apa adanya.
			for _, needle := range []string{mediaMarker, "image_url", "input_audio", "video_url", "clip.mp4"} {
				if !strings.Contains(got, needle) {
					t.Fatalf("body lost %q: %s", needle, got)
				}
			}
			if n := strings.Count(got, mediaMarker); n != 2 {
				t.Fatalf("marker count = %d, want 2 (%s)", n, got)
			}
		})
	}
}
