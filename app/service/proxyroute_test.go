package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"basicroute/app/config"
)

func proxySnap() *config.Snapshot {
	cfg := &config.Config{
		Providers: map[string]*config.Provider{
			"p1": {Name: "P1", Prefix: "p", Proxy: "auto"},
			"p2": {Name: "P2", Prefix: "q", Proxy: "round"},
			"p3": {Name: "P3", Prefix: "r", Proxy: "b"},
			"p4": {Name: "P4", Prefix: "s"},
		},
		ProxyPools: map[string]*config.ProxyPool{
			"a": {Type: config.ProxyTypeHTTP, URL: "http://127.0.0.1:8081", Order: 1, Enabled: true},
			"b": {Type: config.ProxyTypeHTTP, URL: "http://127.0.0.1:8082", Order: 2, Enabled: true},
			"c": {Type: config.ProxyTypeRelay, URL: "https://relay.example", Order: 3, Enabled: false},
		},
	}
	return &config.Snapshot{
		Raw:        cfg,
		ProxyOrder: []string{"a", "b", "c"},
	}
}

func TestResolveProxiesModes(t *testing.T) {
	snap := proxySnap()

	// direct
	tg, err := ResolveProxies(snap, snap.Raw.Providers["p4"])
	if err != nil || len(tg) != 1 || tg[0].Name != "" {
		t.Fatalf("direct = %+v, err=%v", tg, err)
	}

	// auto: semua pool enabled, urut ProxyOrder (c disabled -> dilewati).
	tg, err = ResolveProxies(snap, snap.Raw.Providers["p1"])
	if err != nil {
		t.Fatal(err)
	}
	if len(tg) != 2 || tg[0].Name != "a" || tg[1].Name != "b" {
		t.Fatalf("auto = %+v", tg)
	}
	if tg[0].URL != "http://127.0.0.1:8081" {
		t.Fatalf("auto url = %q", tg[0].URL)
	}

	// round: set kandidat sama dengan auto (rotasi dilakukan server).
	tg, err = ResolveProxies(snap, snap.Raw.Providers["p2"])
	if err != nil || len(tg) != 2 {
		t.Fatalf("round = %+v, err=%v", tg, err)
	}

	// nama pool
	tg, err = ResolveProxies(snap, snap.Raw.Providers["p3"])
	if err != nil || len(tg) != 1 || tg[0].Name != "b" {
		t.Fatalf("named = %+v, err=%v", tg, err)
	}

	// pool disabled sebagai nama -> error
	pd := &config.Provider{Name: "PD", Prefix: "pd", Proxy: "c"}
	if _, err := ResolveProxies(snap, pd); err == nil {
		t.Fatal("disabled named pool should error")
	}

	// auto tanpa pool enabled -> error
	empty := &config.Snapshot{
		Raw: &config.Config{
			Providers: map[string]*config.Provider{"e": {Name: "E", Prefix: "e", Proxy: "auto"}},
			ProxyPools: map[string]*config.ProxyPool{
				"c": {Type: config.ProxyTypeHTTP, URL: "http://127.0.0.1:8082", Order: 1, Enabled: false},
			},
		},
		ProxyOrder: []string{"c"},
	}
	if _, err := ResolveProxies(empty, empty.Raw.Providers["e"]); err == nil {
		t.Fatal("auto with no enabled pool should error")
	}
}

func TestProxyModeDetection(t *testing.T) {
	for _, s := range []string{"auto", "AUTO", " round ", "Round"} {
		if _, ok := ProxyMode(s); !ok {
			t.Fatalf("%q should be a mode", s)
		}
	}
	for _, s := range []string{"", "a", "auto1", "round2", "direct"} {
		if m, ok := ProxyMode(s); ok {
			t.Fatalf("%q should not be a mode (got %q)", s, m)
		}
	}
}

func TestIsProxyFailure(t *testing.T) {
	if !IsProxyFailure(0, errors.New("dial tcp: connection refused")) {
		t.Fatal("transport error is a proxy failure")
	}
	if !IsProxyFailure(http.StatusProxyAuthRequired, nil) {
		t.Fatal("407 is a proxy failure")
	}
	if IsProxyFailure(0, context.Canceled) {
		t.Fatal("caller cancel is not a proxy failure")
	}
	if IsProxyFailure(http.StatusBadGateway, nil) {
		t.Fatal("502 is upstream, not proxy")
	}
	if IsProxyFailure(0, nil) {
		t.Fatal("no error and 0 status is not a failure")
	}
}

func TestNoteProxyResultAndRoundRobin(t *testing.T) {
	st := NewStore(nil)
	if n := st.NoteProxyResult("a", false); n != 1 {
		t.Fatalf("first fail = %d", n)
	}
	if n := st.NoteProxyResult("a", false); n != 2 {
		t.Fatalf("second fail = %d", n)
	}
	if n := st.NoteProxyResult("a", true); n != 0 {
		t.Fatalf("success resets = %d", n)
	}
	if n := st.NoteProxyResult("a", false); n != 1 {
		t.Fatalf("after reset = %d", n)
	}

	// round robin berotasi 0,1,2,0,1,2
	got := []int{}
	for i := 0; i < 6; i++ {
		got = append(got, st.NextRoundRobin("p", 3))
	}
	want := []int{0, 1, 2, 0, 1, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round robin = %v, want %v", got, want)
		}
	}
	// provider lain punya kursor sendiri
	if v := st.NextRoundRobin("q", 2); v != 0 {
		t.Fatalf("other provider cursor = %d", v)
	}
	// n<=0 aman
	if v := st.NextRoundRobin("p", 0); v != 0 {
		t.Fatalf("n=0 -> %d", v)
	}
}

func TestProxyHealthMessages(t *testing.T) {
	h := NewProxyHealth()
	h.SetTesting("a")
	if !h.IsTesting("a") || h.IsError("a") {
		t.Fatal("testing state wrong")
	}
	h.SetError("a", "boom")
	if h.IsTesting("a") || !h.IsError("a") || h.ErrMsg("a") != "boom" {
		t.Fatal("SetError must clear testing and store msg")
	}
	if snap := h.Snapshot(); snap["a"] != "boom" {
		t.Fatalf("snapshot = %v", snap)
	}
	h.SetOK("a")
	if h.IsError("a") || h.ErrMsg("a") != "" {
		t.Fatal("SetOK must clear")
	}
	// nil receiver aman
	var nilH *ProxyHealth
	nilH.SetError("x", "y")
	if nilH.IsError("x") {
		t.Fatal("nil must be safe")
	}
}

func TestProxyModeLabel(t *testing.T) {
	snap := proxySnap()
	if got := ProxyModeLabel(snap, snap.Raw.Providers["p4"]); got != "direct" {
		t.Fatalf("direct label = %q", got)
	}
	if got := ProxyModeShort(snap, snap.Raw.Providers["p4"]); got != "direct" {
		t.Fatalf("direct short = %q", got)
	}
	if got := ProxyModeLabel(snap, snap.Raw.Providers["p3"]); got != "b" {
		t.Fatalf("named label = %q", got)
	}
	if got := ProxyModeShort(snap, snap.Raw.Providers["p1"]); got != "auto (2)" {
		t.Fatalf("auto short = %q", got)
	}
	if got := ProxyModeLabel(snap, snap.Raw.Providers["p1"]); got != "auto (2): a → b" {
		t.Fatalf("auto label = %q", got)
	}
}
