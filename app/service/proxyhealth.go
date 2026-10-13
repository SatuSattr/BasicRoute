package service

import "sync"

// ProxyHealth menandai proxy pool yang gagal di memory saja (tidak persisten).
// Dipakai indikator UI: error = merah, selain itu hijau/abu sesuai enabled.
// Hanya proxy yang di-test manual atau yang baru sukses dilewati request
// yang mengubah status di sini.
type ProxyHealth struct {
	mu  sync.RWMutex
	bad map[string]bool // nama pool -> true bila diketahui error
}

func NewProxyHealth() *ProxyHealth {
	return &ProxyHealth{bad: map[string]bool{}}
}

// SetError menandai pool sedang error.
func (p *ProxyHealth) SetError(name string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	if p.bad == nil {
		p.bad = map[string]bool{}
	}
	p.bad[name] = true
	p.mu.Unlock()
}

// SetOK menghapus tanda error (pool kembali aktif/hijau).
func (p *ProxyHealth) SetOK(name string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	delete(p.bad, name)
	p.mu.Unlock()
}

// IsError melaporkan apakah pool ditandai error.
func (p *ProxyHealth) IsError(name string) bool {
	if p == nil || name == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bad[name]
}

// Snapshot menyalin status error (nama -> true) untuk pembaca lain.
func (p *ProxyHealth) Snapshot() map[string]bool {
	if p == nil {
		return map[string]bool{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]bool, len(p.bad))
	for k, v := range p.bad {
		out[k] = v
	}
	return out
}
