package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultDataFile is the default path of the unified document store.
const DefaultDataFile = "/app/data/data.json"

// DefaultDataSaveIntervalSecs is the default flush cadence in seconds.
const DefaultDataSaveIntervalSecs = 180

// DataVersion is the schema version written to data.json.
const DataVersion = 1

// legacyUsageFile is the historical usage.json path, read once for migration.
const legacyUsageFile = "/app/data/usage.json"

// UsageEntry is one row of the "usage" section: cumulative token counters for
// a single system key (identified by the sha256 hash of the raw key).
type UsageEntry struct {
	Name        string `json:"name"`
	TotalTokens uint64 `json:"totalTokens"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

// dataDoc is the on-disk schema of data.json.
type dataDoc struct {
	Version int                    `json:"version"`
	Usage   map[string]*UsageEntry `json:"usage"`
}

// DataStore is the single in-memory owner of data.json. All mutations touch
// memory only; one background worker is the only goroutine that writes the
// file, coalescing edits and flushing on an interval (and on exit).
type DataStore struct {
	path     string
	interval time.Duration

	mu      sync.RWMutex
	usage   map[string]*UsageEntry

	dirty atomic.Bool

	wake  chan struct{}
	reset chan time.Duration
	stop  chan struct{}
	done  chan struct{}

	startMu sync.Mutex
	started bool
	stopped bool
}

// NewDataStore builds a store for path; interval <= 0 falls back to the default.
func NewDataStore(path string, interval time.Duration) *DataStore {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultDataFile
	}
	if interval <= 0 {
		interval = time.Duration(DefaultDataSaveIntervalSecs) * time.Second
	}
	return &DataStore{
		path:     path,
		interval: interval,
		usage:    map[string]*UsageEntry{},
		wake:     make(chan struct{}, 1),
		reset:    make(chan time.Duration, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Path returns the file path held by the store ("" for a nil store).
func (d *DataStore) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// HashSysKey uses the sha256 hex of the raw key as a stable identifier, since
// system keys have no id of their own.
func HashSysKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Load reads data.json once. A missing file triggers a best-effort one-time
// migration from a legacy usage.json; a corrupt file is reported to the caller
// (which may continue with an empty store).
func (d *DataStore) Load() error {
	if d == nil {
		return nil
	}
	b, err := os.ReadFile(d.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			d.migrateLegacy()
			return nil
		}
		return err
	}
	var doc dataDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	d.mu.Lock()
	if doc.Usage != nil {
		d.usage = doc.Usage
	}
	d.mu.Unlock()
	return nil
}

// migrateLegacy imports a legacy usage.json located next to data.json (or the
// historical default path) into the usage section, once, when data.json is
// absent. Missing/invalid legacy files are silently ignored.
func (d *DataStore) migrateLegacy() {
	candidates := []string{
		filepath.Join(filepath.Dir(d.path), "usage.json"),
		legacyUsageFile,
	}
	for _, p := range candidates {
		if p == "" || p == d.path {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f struct {
			Version int                    `json:"version"`
			Keys    map[string]*UsageEntry `json:"keys"`
		}
		if json.Unmarshal(b, &f) != nil || len(f.Keys) == 0 {
			continue
		}
		d.mu.Lock()
		d.usage = f.Keys
		d.mu.Unlock()
		d.dirty.Store(true)
		return
	}
}

// AddUsage accumulates tokens for a system key and refreshes its last-seen
// timestamp. tokens == 0 still records the timestamp. Memory only.
func (d *DataStore) AddUsage(hash, name string, tokens uint64) {
	if d == nil || hash == "" {
		return
	}
	now := time.Now().Unix()
	d.mu.Lock()
	if d.usage == nil {
		d.usage = map[string]*UsageEntry{}
	}
	e := d.usage[hash]
	if e == nil {
		e = &UsageEntry{}
		d.usage[hash] = e
	}
	if tokens > 0 {
		e.TotalTokens += tokens
	}
	e.UpdatedAt = now
	if name != "" {
		e.Name = name
	}
	d.mu.Unlock()
	d.dirty.Store(true)
}

// UsageTotal returns the accumulated tokens for one hash.
func (d *DataStore) UsageTotal(hash string) uint64 {
	if d == nil || hash == "" {
		return 0
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if e := d.usage[hash]; e != nil {
		return e.TotalTokens
	}
	return 0
}

// UsageSnapshot returns hash -> total tokens. Used by the UI/CLI.
func (d *DataStore) UsageSnapshot() map[string]uint64 {
	if d == nil {
		return map[string]uint64{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]uint64, len(d.usage))
	for h, e := range d.usage {
		if e != nil {
			out[h] = e.TotalTokens
		}
	}
	return out
}

// UsageLastSeen returns the unix time of the last request for one hash.
func (d *DataStore) UsageLastSeen(hash string) int64 {
	if d == nil || hash == "" {
		return 0
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if e := d.usage[hash]; e != nil {
		return e.UpdatedAt
	}
	return 0
}

// UsageSnapshotLast returns hash -> last request unix time. Used by the UI/CLI.
func (d *DataStore) UsageSnapshotLast() map[string]int64 {
	if d == nil {
		return map[string]int64{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]int64, len(d.usage))
	for h, e := range d.usage {
		if e != nil && e.UpdatedAt > 0 {
			out[h] = e.UpdatedAt
		}
	}
	return out
}

// Flush writes the current document to disk atomically. No-op when clean.
func (d *DataStore) Flush() error {
	if d == nil {
		return nil
	}
	if !d.dirty.Load() {
		return nil
	}
	d.mu.Lock()
	doc := dataDoc{Version: DataVersion, Usage: d.usage}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err == nil {
		b = append(b, '\n')
		d.dirty.Store(false)
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if err := writeAtomic(d.path, b); err != nil {
		d.dirty.Store(true)
		return err
	}
	return nil
}

// RequestFlush asks the worker to flush soon; coalesced, never blocking.
func (d *DataStore) RequestFlush() {
	if d == nil {
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Start launches the single writer worker. Idempotent.
func (d *DataStore) Start() {
	if d == nil {
		return
	}
	d.startMu.Lock()
	if d.started || d.stopped {
		d.startMu.Unlock()
		return
	}
	d.started = true
	d.startMu.Unlock()
	go d.run()
}

// Stop performs a final flush and stops the worker. Idempotent.
func (d *DataStore) Stop() {
	if d == nil {
		return
	}
	d.startMu.Lock()
	if d.stopped {
		d.startMu.Unlock()
		return
	}
	d.stopped = true
	started := d.started
	d.startMu.Unlock()
	if !started {
		_ = d.Flush()
		return
	}
	close(d.stop)
	<-d.done
}

// SetInterval changes the flush cadence and resets the running timer.
func (d *DataStore) SetInterval(iv time.Duration) {
	if d == nil || iv <= 0 {
		return
	}
	d.mu.Lock()
	d.interval = iv
	d.mu.Unlock()
	select {
	case d.reset <- iv:
	default:
	}
}

func (d *DataStore) currentInterval() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.interval <= 0 {
		return time.Duration(DefaultDataSaveIntervalSecs) * time.Second
	}
	return d.interval
}

// run is the sole writer: it flushes on an interval, on demand, and on stop.
func (d *DataStore) run() {
	timer := time.NewTimer(d.currentInterval())
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			_ = d.Flush()
			timer.Reset(d.currentInterval())
		case iv := <-d.reset:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if iv <= 0 {
				iv = d.currentInterval()
			}
			timer.Reset(iv)
		case <-d.wake:
			_ = d.Flush()
		case <-d.stop:
			_ = d.Flush()
			close(d.done)
			return
		}
	}
}

// writeAtomic writes b to path via a temp file + rename.
func writeAtomic(path string, b []byte) error {
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
