package server

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"basicroute/app/config"
	"basicroute/app/service"
)

// deadProxy membuat TCP listener yang langsung menutup koneksi, meniru proxy
// mati (transport error), bukan upstream yang membalas error HTTP.
func deadProxy(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() {
		close(done)
		_ = ln.Close()
	})
	return "http://" + ln.Addr().String()
}

// relayServer membuat relay httptest yang membalas 200 dengan body penanda.
func relayServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"relay":"` + tag + `"}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func testSnap(proxyMode string, pools map[string]*config.ProxyPool, order []string) *config.Snapshot {
	prov := &config.Provider{
		Name:      "P1",
		Prefix:    "p",
		Type:      "openai",
		Interface: "chat-completions",
		BaseURL:   "https://api.example.com/v1",
		Proxy:     proxyMode,
		Enabled:   config.FlexBool(true),
		Keys: map[string]*config.ProvKey{
			"k1": {Key: "sk-1", Order: config.FlexInt(1), Enabled: config.FlexBool(true)},
		},
		Models: map[string]*config.Model{
			"m": {Name: "M", Enabled: config.FlexBool(true)},
		},
	}
	md := prov.Models["m"]
	return &config.Snapshot{
		Raw: &config.Config{
			System:     config.System{ProxyTestURL: "http://127.0.0.1:1/none"},
			Providers:  map[string]*config.Provider{"p1": prov},
			ProxyPools: pools,
		},
		ModelMap: map[string]*config.ModelRef{
			"p/m": {ProviderSlug: "p1", Provider: prov, ModelSlug: "m", Model: md, FullID: "p/m"},
		},
		KeyRings: map[string][]*config.KeyEntry{
			"p1": {{Name: "k1", Key: "sk-1", Order: 1}},
		},
		ProxyOrder: order,
	}
}

func chatRequest(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func newTestServer(snap *config.Snapshot) *Server {
	st := service.NewStore(snap)
	return New(st, service.NewPool(), slog.New(slog.NewTextHandler(io.Discard, nil)), 10)
}

// Auto: pool pertama mati -> 3x coba -> di-flag merah, lalu pindah ke pool
// kedua yang sehat -> request tetap sukses.
func TestProxyAutoFailoverFlagsDeadPool(t *testing.T) {
	dead := deadProxy(t)
	good := relayServer(t, "good")
	snap := testSnap("auto", map[string]*config.ProxyPool{
		"dead": {Type: config.ProxyTypeRelay, URL: dead, Order: 1, Enabled: true},
		"good": {Type: config.ProxyTypeRelay, URL: good.URL, Order: 2, Enabled: true},
	}, []string{"dead", "good"})
	srv := newTestServer(snap)

	rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "good") {
		t.Fatalf("expected good relay body, got %s", rec.Body.String())
	}
	ph := srv.st.ProxyHealth
	if !ph.IsError("dead") {
		t.Fatal("dead pool must be flagged after 3 consecutive failures")
	}
	if ph.IsError("good") {
		t.Fatal("good pool must not be flagged")
	}
	if msg := ph.ErrMsg("dead"); msg == "" {
		t.Fatal("dead pool must carry an error message")
	}
}

// Auto dengan pool pertama sehat: langsung dipakai, tidak ada flag.
func TestProxyAutoFirstPoolWins(t *testing.T) {
	a := relayServer(t, "a")
	b := relayServer(t, "b")
	snap := testSnap("auto", map[string]*config.ProxyPool{
		"a": {Type: config.ProxyTypeRelay, URL: a.URL, Order: 1, Enabled: true},
		"b": {Type: config.ProxyTypeRelay, URL: b.URL, Order: 2, Enabled: true},
	}, []string{"a", "b"})
	srv := newTestServer(snap)

	rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"a"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if srv.st.ProxyHealth.IsError("a") || srv.st.ProxyHealth.IsError("b") {
		t.Fatal("no pool should be flagged")
	}
}

// Round: titik awal berotasi tiap request (round robin).
func TestProxyRoundRotates(t *testing.T) {
	a := relayServer(t, "a")
	b := relayServer(t, "b")
	snap := testSnap("round", map[string]*config.ProxyPool{
		"a": {Type: config.ProxyTypeRelay, URL: a.URL, Order: 1, Enabled: true},
		"b": {Type: config.ProxyTypeRelay, URL: b.URL, Order: 2, Enabled: true},
	}, []string{"a", "b"})
	srv := newTestServer(snap)

	got := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		got = append(got, rec.Body.String())
	}
	if !strings.Contains(got[0], `"a"`) || !strings.Contains(got[1], `"b"`) {
		t.Fatalf("round robin must rotate start: %v", got)
	}
}

// Pool yang sudah di-flag turun prioritas: request berikutnya langsung pakai
// pool sehat tanpa menyentuh pool mati lagi.
func TestProxyFlaggedPoolDeprioritized(t *testing.T) {
	dead := deadProxy(t)
	good := relayServer(t, "good")
	snap := testSnap("auto", map[string]*config.ProxyPool{
		"dead": {Type: config.ProxyTypeRelay, URL: dead, Order: 1, Enabled: true},
		"good": {Type: config.ProxyTypeRelay, URL: good.URL, Order: 2, Enabled: true},
	}, []string{"dead", "good"})
	srv := newTestServer(snap)
	srv.st.ProxyHealth.SetError("dead", "pre-flagged")

	rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "good") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Semua kandidat mati: request gagal (502) tapi tidak menggantung.
func TestProxyAllDead(t *testing.T) {
	d1 := deadProxy(t)
	d2 := deadProxy(t)
	snap := testSnap("auto", map[string]*config.ProxyPool{
		"d1": {Type: config.ProxyTypeRelay, URL: d1, Order: 1, Enabled: true},
		"d2": {Type: config.ProxyTypeRelay, URL: d2, Order: 2, Enabled: true},
	}, []string{"d1", "d2"})
	srv := newTestServer(snap)

	rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !srv.st.ProxyHealth.IsError("d1") || !srv.st.ProxyHealth.IsError("d2") {
		t.Fatal("both dead pools must be flagged")
	}
}

// Direct (tanpa pool) tidak menyentuh ProxyHealth sama sekali.
func TestProxyDirectUnaffected(t *testing.T) {
	snap := testSnap("", nil, nil)
	srv := newTestServer(snap)
	if srv.st.ProxyHealth.Snapshot() == nil {
		t.Fatal("proxy health must exist")
	}
	if len(srv.st.ProxyHealth.Snapshot()) != 0 {
		t.Fatal("direct provider must not flag any pool")
	}
}

// Key dengan proxy spesifik meng-override proxy provider: request lewat pool
// milik key, bukan pool provider.
func TestKeyProxyOverridesProvider(t *testing.T) {
	dead := deadProxy(t)
	good := relayServer(t, "good")
	snap := testSnap("pool-dead", map[string]*config.ProxyPool{
		"pool-dead": {Type: config.ProxyTypeRelay, URL: dead, Order: 1, Enabled: true},
		"pool-good": {Type: config.ProxyTypeRelay, URL: good.URL, Order: 2, Enabled: true},
	}, []string{"pool-dead", "pool-good"})
	key := "pool-good"
	snap.KeyRings["p1"][0].Proxy = &key
	srv := newTestServer(snap)

	rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "good") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if srv.st.ProxyHealth.IsError("pool-dead") {
		t.Fatal("provider pool must not be touched when the key overrides it")
	}
}

// Key dipatok ke pool mati: request gagal (502) tanpa fallback ke pool provider.
func TestKeyProxyDeadNoFallback(t *testing.T) {
	dead := deadProxy(t)
	good := relayServer(t, "good")
	snap := testSnap("pool-good", map[string]*config.ProxyPool{
		"pool-dead": {Type: config.ProxyTypeRelay, URL: dead, Order: 1, Enabled: true},
		"pool-good": {Type: config.ProxyTypeRelay, URL: good.URL, Order: 2, Enabled: true},
	}, []string{"pool-dead", "pool-good"})
	key := "pool-dead"
	snap.KeyRings["p1"][0].Proxy = &key
	srv := newTestServer(snap)

	rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !srv.st.ProxyHealth.IsError("pool-dead") {
		t.Fatal("dead key pool must be flagged")
	}
	if srv.st.ProxyHealth.IsError("pool-good") {
		t.Fatal("provider pool must not be used as fallback")
	}
}

// Key dengan proxy "" (direct) tidak memakai pool provider.
func TestKeyProxyDirectOverridesProvider(t *testing.T) {
	good := relayServer(t, "good")
	snap := testSnap("pool-good", map[string]*config.ProxyPool{
		"pool-good": {Type: config.ProxyTypeRelay, URL: good.URL, Order: 1, Enabled: true},
	}, []string{"pool-good"})
	snap.Raw.Providers["p1"].BaseURL = "http://127.0.0.1:1" // upstream mati
	empty := ""
	snap.KeyRings["p1"][0].Proxy = &empty
	srv := newTestServer(snap)

	rec := chatRequest(t, srv, `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code == http.StatusOK {
		t.Fatal("direct key must not go through the provider relay")
	}
	if srv.st.ProxyHealth.IsError("pool-good") {
		t.Fatal("provider pool must not be used for a direct key")
	}
}

// Sanity: kandidat round robin benar-benar berotasi di level Store.
func TestProxyCandidatesRoundRobin(t *testing.T) {
	snap := testSnap("round", map[string]*config.ProxyPool{
		"a": {Type: config.ProxyTypeRelay, URL: "http://127.0.0.1:1", Order: 1, Enabled: true},
		"b": {Type: config.ProxyTypeRelay, URL: "http://127.0.0.1:2", Order: 2, Enabled: true},
		"c": {Type: config.ProxyTypeRelay, URL: "http://127.0.0.1:3", Order: 3, Enabled: true},
	}, []string{"a", "b", "c"})
	srv := newTestServer(snap)
	prov := snap.Raw.Providers["p1"]

	var mu sync.Mutex
	first := []string{}
	for i := 0; i < 3; i++ {
		tg, err := srv.proxyCandidates(snap, "p1", prov)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		first = append(first, tg[0].Name)
		mu.Unlock()
	}
	want := []string{"a", "b", "c"}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("rotating start = %v, want %v", first, want)
		}
	}
}
