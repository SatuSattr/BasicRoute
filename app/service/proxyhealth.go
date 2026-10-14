package service

import (
	"sync"
	"time"
)

// ProxyHealth menandai proxy pool yang gagal di memory saja (tidak persisten),
// beserta pesan error terakhirnya untuk ditampilkan di TUI/CLI. Dipakai
// indikator UI: error = merah, selain itu hijau/abu sesuai enabled. Status
// berubah dari test manual maupun dari kegagalan/keberhasilan request nyata.
type ProxyHealth struct {
	mu      sync.RWMutex
	bad     map[string]proxyErr // nama pool -> error terakhir
	testing map[string]bool     // nama pool -> sedang di-test (kuning)
}

type proxyErr struct {
	Msg string
	At  int64 // unix terakhir SetError
}

func NewProxyHealth() *ProxyHealth {
	return &ProxyHealth{bad: map[string]proxyErr{}, testing: map[string]bool{}}
}

// SetTesting menandai pool sedang di-test (indikator kuning).
func (p *ProxyHealth) SetTesting(name string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	if p.testing == nil {
		p.testing = map[string]bool{}
	}
	p.testing[name] = true
	p.mu.Unlock()
}

// ClearTesting menghapus tanda sedang di-test tanpa mengubah error.
func (p *ProxyHealth) ClearTesting(name string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	delete(p.testing, name)
	p.mu.Unlock()
}

// IsTesting melaporkan apakah pool sedang di-test.
func (p *ProxyHealth) IsTesting(name string) bool {
	if p == nil || name == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.testing[name]
}

// SetError menandai pool sedang error beserta pesannya.
func (p *ProxyHealth) SetError(name, msg string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	if p.bad == nil {
		p.bad = map[string]proxyErr{}
	}
	p.bad[name] = proxyErr{Msg: msg, At: time.Now().Unix()}
	delete(p.testing, name) // hasil sudah keluar: kuning -> merah
	p.mu.Unlock()
}

// SetOK menghapus tanda error (pool kembali aktif/hijau).
func (p *ProxyHealth) SetOK(name string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	delete(p.bad, name)
	delete(p.testing, name) // hasil sudah keluar: kuning -> hijau
	p.mu.Unlock()
}

// IsError melaporkan apakah pool ditandai error.
func (p *ProxyHealth) IsError(name string) bool {
	if p == nil || name == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.bad[name]
	return ok
}

// ErrMsg mengembalikan pesan error terakhir untuk pool ("" bila tidak flagged).
func (p *ProxyHealth) ErrMsg(name string) string {
	if p == nil || name == "" {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bad[name].Msg
}

// ErrAt mengembalikan unix timestamp terakhir SetError (0 bila tidak flagged/nil).
func (p *ProxyHealth) ErrAt(name string) int64 {
	if p == nil || name == "" {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.bad[name]
	if !ok {
		return 0
	}
	return e.At
}

// ErrAge mengembalikan umur error (now - At, 0 bila tidak flagged).
func (p *ProxyHealth) ErrAge(name string) time.Duration {
	if p == nil || name == "" {
		return 0
	}
	p.mu.RLock()
	e, ok := p.bad[name]
	p.mu.RUnlock()
	if !ok {
		return 0
	}
	d := time.Since(time.Unix(e.At, 0))
	if d < 0 {
		return 0
	}
	return d
}

// Snapshot menyalin status error (nama -> pesan) untuk pembaca lain.
func (p *ProxyHealth) Snapshot() map[string]string {
	if p == nil {
		return map[string]string{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]string, len(p.bad))
	for k, v := range p.bad {
		out[k] = v.Msg
	}
	return out
}
