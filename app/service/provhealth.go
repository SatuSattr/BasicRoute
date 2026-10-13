package service

import (
	"sync"
	"time"
)

// ProvHealth menandai key/model provider yang gagal di memory saja
// (tidak persisten), beserta pesan error terakhirnya untuk ditampilkan
// di TUI. Identity key memakai format CooldownKey(slug, keyName);
// identity model memakai slug|modelSlug (slug map, bukan display Name).
// Selain test manual TUI, request yang sukses me-reset flag (self-heal)
// dan request yang gagal beruntun bisa menaikkan flag (auto-flag).
type ProvHealth struct {
	mu      sync.RWMutex
	bad     map[string]provErr // id -> error terakhir
	testing map[string]bool    // id -> sedang di-test (kuning)
}

type provErr struct {
	Msg string
	At  int64 // unix terakhir SetError
}

// ProvKeyID membangun identity key: slug|namaKey (sama dengan CooldownKey).
func ProvKeyID(providerSlug, keyName string) string {
	return CooldownKey(providerSlug, keyName)
}

// ProvModelID membangun identity model: slug|modelSlug.
func ProvModelID(providerSlug, modelSlug string) string {
	return providerSlug + "|" + modelSlug
}

func NewProvHealth() *ProvHealth {
	return &ProvHealth{bad: map[string]provErr{}, testing: map[string]bool{}}
}

// SetTesting menandai id sedang di-test (indikator kuning).
func (p *ProvHealth) SetTesting(id string) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	if p.testing == nil {
		p.testing = map[string]bool{}
	}
	p.testing[id] = true
	p.mu.Unlock()
}

// ClearTesting menghapus tanda sedang di-test tanpa mengubah error.
func (p *ProvHealth) ClearTesting(id string) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	delete(p.testing, id)
	p.mu.Unlock()
}

// IsTesting melaporkan apakah id sedang di-test.
func (p *ProvHealth) IsTesting(id string) bool {
	if p == nil || id == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.testing[id]
}

// SetError menandai id sedang error beserta pesannya.
func (p *ProvHealth) SetError(id, msg string) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	if p.bad == nil {
		p.bad = map[string]provErr{}
	}
	p.bad[id] = provErr{Msg: msg, At: time.Now().Unix()}
	delete(p.testing, id) // hasil sudah keluar: kuning -> merah
	p.mu.Unlock()
}

// SetOK menghapus tanda error (kembali aktif/hijau).
func (p *ProvHealth) SetOK(id string) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	delete(p.bad, id)
	delete(p.testing, id) // hasil sudah keluar: kuning -> hijau
	p.mu.Unlock()
}

// IsError melaporkan apakah id ditandai error.
func (p *ProvHealth) IsError(id string) bool {
	if p == nil || id == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.bad[id]
	return ok
}

// ErrMsg mengembalikan pesan error terakhir untuk id ("" bila tidak flagged).
func (p *ProvHealth) ErrMsg(id string) string {
	if p == nil || id == "" {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bad[id].Msg
}

// Snapshot menyalin id -> pesan error untuk pembaca lain.
func (p *ProvHealth) Snapshot() map[string]string {
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
