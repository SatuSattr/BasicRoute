package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultUsageFile sesuai permintaan: /app/data/usage.json.
const DefaultUsageFile = "/app/data/usage.json"

// FlushInterval adalah jeda tulis periodik ke disk.
const FlushInterval = 3 * time.Minute

// UsageEntry adalah satu baris di file usage.json.
type UsageEntry struct {
	Name        string `json:"name"`
	TotalTokens uint64 `json:"totalTokens"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

type usageFile struct {
	Version int                    `json:"version"`
	Keys    map[string]*UsageEntry `json:"keys"`
}

// HashSysKey memakai hash sha256 hex dari raw key sebagai identifier.
// System key tidak punya id, jadi hash ini yang jadi kunci stabil.
func HashSysKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// UsageTracker menyimpan akumulasi token di memori dan flush ke file
// setiap FlushInterval. Add() tidak pernah menyentuh disk sehingga aman
// saat traffic tinggi.
type UsageTracker struct {
	path string

	mu    sync.Mutex
	base  map[string]*UsageEntry // terakhir dari disk / flush
	delta map[string]uint64      // akumulasi sejak flush terakhir
	names map[string]string      // hash -> nama terakhir (untuk flush)

	dirty atomic.Bool
}

func NewUsageTracker(path string) *UsageTracker {
	if path == "" {
		path = DefaultUsageFile
	}
	return &UsageTracker{
		path:  path,
		base:  map[string]*UsageEntry{},
		delta: map[string]uint64{},
		names: map[string]string{},
	}
}

func (u *UsageTracker) Path() string {
	if u == nil {
		return ""
	}
	return u.path
}

// Load membaca file bila ada. File hilang/corrupt = mulai kosong (tidak fatal).
func (u *UsageTracker) Load() {
	if u == nil {
		return
	}
	b, err := os.ReadFile(u.path)
	if err != nil {
		return
	}
	var f usageFile
	if err := json.Unmarshal(b, &f); err != nil {
		return
	}
	if f.Keys == nil {
		f.Keys = map[string]*UsageEntry{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.base = f.Keys
	for h, e := range f.Keys {
		if e != nil {
			u.names[h] = e.Name
		}
	}
}

// Add menambah tokens ke akumulasi memori. Tidak menyentuh disk.
func (u *UsageTracker) Add(hash, name string, tokens uint64) {
	if u == nil || hash == "" || tokens == 0 {
		return
	}
	u.mu.Lock()
	u.delta[hash] += tokens
	if name != "" {
		u.names[hash] = name
	}
	u.mu.Unlock()
	u.dirty.Store(true)
}

// Total mengembalikan base + delta untuk satu hash.
func (u *UsageTracker) Total(hash string) uint64 {
	if u == nil || hash == "" {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	var t uint64
	if e := u.base[hash]; e != nil {
		t += e.TotalTokens
	}
	t += u.delta[hash]
	return t
}

// Snapshot mengembalikan total semua hash (base+delta). Dipakai UI/CLI.
func (u *UsageTracker) Snapshot() map[string]uint64 {
	if u == nil {
		return map[string]uint64{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make(map[string]uint64, len(u.base)+len(u.delta))
	for h, e := range u.base {
		if e != nil {
			out[h] = e.TotalTokens
		}
	}
	for h, d := range u.delta {
		out[h] += d
	}
	return out
}

// Dirty melaporkan ada perubahan yang belum di-flush.
func (u *UsageTracker) Dirty() bool {
	if u == nil {
		return false
	}
	return u.dirty.Load()
}

// Flush menggabung delta ke base lalu tulis atomic (tmp + rename).
// No-op bila tidak dirty.
func (u *UsageTracker) Flush() error {
	if u == nil {
		return nil
	}
	if !u.dirty.Load() {
		return nil
	}
	u.mu.Lock()
	now := time.Now().Unix()
	merged := make(map[string]*UsageEntry, len(u.base)+len(u.delta))
	for h, e := range u.base {
		if e == nil {
			continue
		}
		cp := *e
		merged[h] = &cp
	}
	for h, d := range u.delta {
		if d == 0 {
			continue
		}
		e := merged[h]
		if e == nil {
			e = &UsageEntry{}
			merged[h] = e
		}
		e.TotalTokens += d
		e.UpdatedAt = now
		if n := u.names[h]; n != "" {
			e.Name = n
		}
	}
	u.mu.Unlock()

	if err := writeAtomicJSON(u.path, usageFile{Version: 1, Keys: merged}); err != nil {
		return err
	}

	u.mu.Lock()
	u.base = merged
	clear(u.delta)
	u.mu.Unlock()
	u.dirty.Store(false)
	return nil
}

// AutoFlush menulis tiap interval selama stop belum ditutup.
func (u *UsageTracker) AutoFlush(interval time.Duration, stop <-chan struct{}) {
	if u == nil {
		return
	}
	if interval <= 0 {
		interval = FlushInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = u.Flush()
		case <-stop:
			return
		}
	}
}

func writeAtomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
