package service

import (
	"sync"
	"sync/atomic"
	"time"

	"basicroute/app/config"
)

func CooldownKey(providerSlug, keyName string) string {
	return providerSlug + "|" + keyName
}

// ModelAutoFlagThreshold: jumlah gagal response beruntun (non-2xx non-auth,
// non-429) sebelum model di-flag error otomatis.
const ModelAutoFlagThreshold = 3

// ProxyFailThreshold: jumlah gagal proxy beruntun (transport error / 407)
// sebelum pool di-flag error dan request pindah ke pool berikutnya.
const ProxyFailThreshold = 3

// ProxyFailWindow: jendela akumulasi kegagalan proxy. Counter gagal
// di-reset bila kegagalan sebelumnya lebih tua dari jendela ini.
const ProxyFailWindow = 30 * time.Second

// proxyFailWindow menghitung kegagalan proxy dalam jendela waktu.
type proxyFailWindow struct {
	n     int
	since time.Time
}

// modelFailState menghitung consecutive failures satu model (FullID).
type modelFailState struct {
	n    int
	last string
}

type Store struct {
	snap atomic.Pointer[config.Snapshot]

	// Usage melacak total token per system key (hash -> total).
	// Diisi di main, boleh nil pada test lama.
	Usage *UsageTracker

	// ProxyHealth menandai proxy pool yang error (memory only).
	ProxyHealth *ProxyHealth

	// ProvHealth menandai key/model provider yang error + pesan terakhir
	// (memory only). Dipakai indikator merah + pesan detail di TUI.
	ProvHealth *ProvHealth

	mu       sync.RWMutex
	hits     map[string]uint64
	errs     map[string]uint64
	latSum   map[string]int64
	latCount map[string]uint64

	draining atomic.Bool
	wg       sync.WaitGroup
	inflight atomic.Int32

	cooldownMu sync.Mutex
	cooldown   map[string]time.Time

	// modelFails menghitung consecutive gagal per model (FullID) untuk
	// auto-flag. Sukses me-reset counter (lihat NoteModelResult).
	modelFailMu sync.Mutex
	modelFails  map[string]modelFailState

	// proxyFails menghitung consecutive gagal per pool proxy (nama) untuk
	// auto-flag. Sukses me-reset counter (lihat NoteProxyResult).
	proxyFailMu sync.Mutex
	proxyFails  map[string]proxyFailWindow

	// roundRobin menyimpan kursor rotasi per provider untuk mode "round".
	rrMu       sync.Mutex
	roundRobin map[string]uint64

	gen atomic.Uint64
}

func NewStore(s *config.Snapshot) *Store {
	st := &Store{
		cooldown:    map[string]time.Time{},
		hits:        map[string]uint64{},
		errs:        map[string]uint64{},
		latSum:      map[string]int64{},
		latCount:    map[string]uint64{},
		modelFails:  map[string]modelFailState{},
		proxyFails:  map[string]proxyFailWindow{},
		roundRobin:  map[string]uint64{},
		ProxyHealth: NewProxyHealth(),
		ProvHealth:  NewProvHealth(),
	}
	if s != nil {
		st.snap.Store(s)
		st.gen.Store(s.Gen)
	}
	return st
}

func (st *Store) Get() *config.Snapshot {
	return st.snap.Load()
}

func (st *Store) Swap(s *config.Snapshot) {
	n := st.gen.Add(1)
	if s != nil {
		s.Gen = n
		st.snap.Store(s)
	}
}

func (st *Store) Gen() uint64 {
	return st.gen.Load()
}

func (st *Store) Begin() bool {
	if st.draining.Load() {
		return false
	}
	st.wg.Add(1)
	st.inflight.Add(1)
	if st.draining.Load() {
		st.wg.Done()
		st.inflight.Add(-1)
		return false
	}
	return true
}

func (st *Store) Done() {
	st.wg.Done()
	st.inflight.Add(-1)
}

func (st *Store) SetDraining(d bool) {
	st.draining.Store(d)
}

func (st *Store) IsDraining() bool {
	return st.draining.Load()
}

func (st *Store) InFlight() int {
	return int(st.inflight.Load())
}

func (st *Store) WaitIdle(timeout time.Duration) bool {
	if st.inflight.Load() <= 0 {
		return true
	}
	if timeout <= 0 {
		return false
	}
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		if st.inflight.Load() <= 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return st.inflight.Load() <= 0
		}
	}
	return false
}

func (st *Store) Cooldown(key string) bool {
	if key == "" {
		return false
	}
	now := time.Now()
	st.cooldownMu.Lock()
	defer st.cooldownMu.Unlock()
	if st.cooldown == nil {
		return false
	}
	deadline, ok := st.cooldown[key]
	if !ok {
		return false
	}
	if !now.Before(deadline) {
		delete(st.cooldown, key)
		return false
	}
	return true
}

func (st *Store) MarkCooldown(key string, d time.Duration) {
	if key == "" {
		return
	}
	st.cooldownMu.Lock()
	defer st.cooldownMu.Unlock()
	if st.cooldown == nil {
		st.cooldown = map[string]time.Time{}
	}
	st.cooldown[key] = time.Now().Add(d)
}

// NoteModelResult mencatat hasil satu response upstream untuk model
// (FullID): ok=true mereset counter, ok=false menaikkan counter.
// Mengembalikan jumlah consecutive failures saat ini (0 bila ok).
func (st *Store) NoteModelResult(fullID string, ok bool, lastErr string) int {
	if st == nil || fullID == "" {
		if ok {
			return 0
		}
		return 1
	}
	st.modelFailMu.Lock()
	defer st.modelFailMu.Unlock()
	if st.modelFails == nil {
		st.modelFails = map[string]modelFailState{}
	}
	if ok {
		delete(st.modelFails, fullID)
		return 0
	}
	fs := st.modelFails[fullID]
	fs.n++
	if lastErr != "" {
		fs.last = lastErr
	}
	st.modelFails[fullID] = fs
	return fs.n
}

// NoteProxyResult mencatat hasil satu percobaan lewat pool proxy:
// ok=true mereset counter, ok=false menaikkan counter. Mengembalikan
// jumlah consecutive failures saat ini (0 bila ok).
func (st *Store) NoteProxyResult(name string, ok bool) int {
	if st == nil || name == "" {
		if ok {
			return 0
		}
		return 1
	}
	st.proxyFailMu.Lock()
	defer st.proxyFailMu.Unlock()
	if st.proxyFails == nil {
		st.proxyFails = map[string]proxyFailWindow{}
	}
	if ok {
		delete(st.proxyFails, name)
		return 0
	}
	now := time.Now()
	fw := st.proxyFails[name]
	if fw.n == 0 || now.Sub(fw.since) > ProxyFailWindow {
		fw = proxyFailWindow{n: 1, since: now}
	} else {
		fw.n++
	}
	st.proxyFails[name] = fw
	return fw.n
}

// NextRoundRobin mengembalikan index awal (0..n-1) untuk mode "round" dan
// memajukan kursor rotasi provider. n<=0 -> 0.
func (st *Store) NextRoundRobin(providerSlug string, n int) int {
	if st == nil || n <= 0 {
		return 0
	}
	st.rrMu.Lock()
	defer st.rrMu.Unlock()
	if st.roundRobin == nil {
		st.roundRobin = map[string]uint64{}
	}
	cur := st.roundRobin[providerSlug]
	st.roundRobin[providerSlug] = cur + 1
	return int(cur % uint64(n))
}

// NextKeyIndex identik dengan NextRoundRobin tetapi memakai namespace
// "keys:"+slug agar kursor rotasi key terpisah dari kursor proxy.
func (st *Store) NextKeyIndex(slug string, n int) int {
	if st == nil || n <= 0 {
		return 0
	}
	st.rrMu.Lock()
	defer st.rrMu.Unlock()
	if st.roundRobin == nil {
		st.roundRobin = map[string]uint64{}
	}
	key := "keys:" + slug
	cur := st.roundRobin[key]
	st.roundRobin[key] = cur + 1
	return int(cur % uint64(n))
}

func statKey(provider, model string) string {
	return provider + "|" + model
}
func (st *Store) Record(provider, model string, ms int64, err bool) {
	if ms < 0 {
		ms = 0
	}
	k := statKey(provider, model)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.hits == nil {
		st.hits = map[string]uint64{}
	}
	if st.errs == nil {
		st.errs = map[string]uint64{}
	}
	if st.latSum == nil {
		st.latSum = map[string]int64{}
	}
	if st.latCount == nil {
		st.latCount = map[string]uint64{}
	}
	st.hits[k]++
	if err {
		st.errs[k]++
	}
	st.latSum[k] += ms
	st.latCount[k]++
}

func (st *Store) Stats() map[string]any {
	st.mu.RLock()
	hits := make(map[string]uint64, len(st.hits))
	for k, v := range st.hits {
		hits[k] = v
	}
	errs := make(map[string]uint64, len(st.errs))
	for k, v := range st.errs {
		errs[k] = v
	}
	avg := make(map[string]float64, len(st.latCount))
	count := make(map[string]uint64, len(st.latCount))
	for k, n := range st.latCount {
		count[k] = n
		if n > 0 {
			avg[k] = float64(st.latSum[k]) / float64(n)
		}
	}
	st.mu.RUnlock()

	return map[string]any{
		"gen":          st.gen.Load(),
		"inflight":     int(st.inflight.Load()),
		"draining":     st.draining.Load(),
		"hits":         hits,
		"errors":       errs,
		"avgLatencyMs": avg,
		"count":        count,
	}
}
