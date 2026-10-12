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

type Store struct {
	snap atomic.Pointer[config.Snapshot]

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

	gen atomic.Uint64
}

func NewStore(s *config.Snapshot) *Store {
	st := &Store{
		cooldown: map[string]time.Time{},
		hits:     map[string]uint64{},
		errs:     map[string]uint64{},
		latSum:   map[string]int64{},
		latCount: map[string]uint64{},
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
