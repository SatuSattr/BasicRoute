package service

import (
	"testing"
	"time"
)

func TestProxyFailWindow(t *testing.T) {
	st := NewStore(nil)

	// 3 fail cepat -> 3 (threshold semantics).
	if got := st.NoteProxyResult("pool-a", false); got != 1 {
		t.Fatalf("fail 1 = %d, want 1", got)
	}
	if got := st.NoteProxyResult("pool-a", false); got != 2 {
		t.Fatalf("fail 2 = %d, want 2", got)
	}
	if got := st.NoteProxyResult("pool-a", false); got != 3 {
		t.Fatalf("fail 3 = %d, want 3", got)
	}
	if got := st.NoteProxyResult("pool-a", false); got != 4 {
		t.Fatalf("fail 4 = %d, want 4", got)
	}

	// Sukses -> reset 0.
	if got := st.NoteProxyResult("pool-a", true); got != 0 {
		t.Fatalf("ok reset = %d, want 0", got)
	}

	// Fail setelah reset -> 1 lagi.
	if got := st.NoteProxyResult("pool-a", false); got != 1 {
		t.Fatalf("fail after reset = %d, want 1", got)
	}

	// Window kedaluwarsa: geser since ke masa lalu, fail berikutnya harus reset ke 1.
	st.proxyFailMu.Lock()
	fw := st.proxyFails["pool-a"]
	fw.since = time.Now().Add(-ProxyFailWindow - time.Second)
	st.proxyFails["pool-a"] = fw
	st.proxyFailMu.Unlock()

	if got := st.NoteProxyResult("pool-a", false); got != 1 {
		t.Fatalf("fail after window expiry = %d, want 1", got)
	}

	// Threshold semantics: 3 fail cepat mencapai threshold.
	st2 := NewStore(nil)
	for i := 1; i <= ProxyFailThreshold; i++ {
		got := st2.NoteProxyResult("pool-b", false)
		if got != i {
			t.Fatalf("pool-b fail %d = %d, want %d", i, got, i)
		}
	}
	if got := st2.NoteProxyResult("pool-b", false); got != ProxyFailThreshold+1 {
		t.Fatalf("pool-b fail over threshold = %d, want %d", got, ProxyFailThreshold+1)
	}
}

func TestProxyHealthErrAge(t *testing.T) {
	p := NewProxyHealth()

	// Belum flagged -> 0.
	if got := p.ErrAt("pool-x"); got != 0 {
		t.Fatalf("ErrAt unflagged = %d, want 0", got)
	}
	if got := p.ErrAge("pool-x"); got != 0 {
		t.Fatalf("ErrAge unflagged = %v, want 0", got)
	}

	p.SetError("pool-x", "boom")
	at := p.ErrAt("pool-x")
	if at == 0 {
		t.Fatalf("ErrAt flagged = 0, want non-zero")
	}
	age := p.ErrAge("pool-x")
	if age <= 0 {
		t.Fatalf("ErrAge flagged = %v, want > 0", age)
	}
	if age >= 5*time.Second {
		t.Fatalf("ErrAge flagged = %v, want < 5s", age)
	}

	p.SetOK("pool-x")
	if got := p.ErrAge("pool-x"); got != 0 {
		t.Fatalf("ErrAge after SetOK = %v, want 0", got)
	}
	if got := p.ErrAt("pool-x"); got != 0 {
		t.Fatalf("ErrAt after SetOK = %d, want 0", got)
	}

	// Nil receiver aman.
	var nilP *ProxyHealth
	if got := nilP.ErrAt("a"); got != 0 {
		t.Fatalf("nil ErrAt = %d, want 0", got)
	}
	if got := nilP.ErrAge("a"); got != 0 {
		t.Fatalf("nil ErrAge = %v, want 0", got)
	}
}
