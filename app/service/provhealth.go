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
	mu       sync.RWMutex
	bad      map[string]provErr  // id -> error terakhir
	testing  map[string]bool     // id -> sedang di-test (kuning)
	progress map[string]provProg // id -> fase + progres langkah (kuning)
}

type provErr struct {
	Msg string
	At  int64 // unix terakhir SetError
}

// provProg melacak operasi multi-langkah yang sedang berjalan (models:auto):
// label fase ("fetching"/"identifying") dan hitungan langkah done/total.
type provProg struct {
	Phase string
	Done  int
	Total int
}

// Fase progres provider (dipakai UI untuk teks status models).
const (
	ProvPhaseFetching    = "fetching"
	ProvPhaseIdentifying = "identifying"
)

// ProvKeyID membangun identity key: slug|namaKey (sama dengan CooldownKey).
func ProvKeyID(providerSlug, keyName string) string {
	return CooldownKey(providerSlug, keyName)
}

// ProvModelID membangun identity model: slug|modelSlug.
func ProvModelID(providerSlug, modelSlug string) string {
	return providerSlug + "|" + modelSlug
}

// ProvProviderID membangun identity provider-level (untuk mode models:auto):
// "provider:"+slug. Prefix berbeda dari key/model agar tidak tabrakan dengan
// CooldownKey(slug,keyName) maupun ProvModelID.
func ProvProviderID(providerSlug string) string {
	return "provider:" + providerSlug
}

func NewProvHealth() *ProvHealth {
	return &ProvHealth{bad: map[string]provErr{}, testing: map[string]bool{}, progress: map[string]provProg{}}
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
	delete(p.progress, id) // mulai bersih: fase akan di-set ulang bila berprogres
	p.mu.Unlock()
}

// SetProgress menandai id sedang berjalan (kuning) dengan label fase dan
// hitungan langkah (done/total). Total 0 = tak diketahui.
func (p *ProvHealth) SetProgress(id, phase string, done, total int) {
	if p == nil || id == "" {
		return
	}
	if done < 0 {
		done = 0
	}
	p.mu.Lock()
	if p.testing == nil {
		p.testing = map[string]bool{}
	}
	if p.progress == nil {
		p.progress = map[string]provProg{}
	}
	p.testing[id] = true
	p.progress[id] = provProg{Phase: phase, Done: done, Total: total}
	p.mu.Unlock()
}

// Progress mengembalikan fase + done/total untuk id (ok=false bila tak ada).
func (p *ProvHealth) Progress(id string) (phase string, done, total int, ok bool) {
	if p == nil || id == "" {
		return "", 0, 0, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	pr, found := p.progress[id]
	return pr.Phase, pr.Done, pr.Total, found
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
	delete(p.testing, id)  // hasil sudah keluar: kuning -> merah
	delete(p.progress, id) // progres selesai
	p.mu.Unlock()
}

// SetOK menghapus tanda error (kembali aktif/hijau).
func (p *ProvHealth) SetOK(id string) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	delete(p.bad, id)
	delete(p.testing, id)  // hasil sudah keluar: kuning -> hijau
	delete(p.progress, id) // progres selesai
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
