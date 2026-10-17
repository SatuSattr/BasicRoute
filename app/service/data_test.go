package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDataStoreFlushReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	d := NewDataStore(path, time.Second)
	d.AddUsage("h1", "Acme", 123)
	d.AddUsage("h1", "Acme", 77) // accumulates
	if err := d.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	d2 := NewDataStore(path, time.Second)
	if err := d2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := d2.UsageTotal("h1"); got != 200 {
		t.Errorf("usage total: got %d, want 200", got)
	}
	if got := d2.UsageLastSeen("h1"); got == 0 {
		t.Errorf("usage last seen not persisted")
	}
	if snap := d2.UsageSnapshot(); snap["h1"] != 200 {
		t.Errorf("usage snapshot: %v", snap)
	}
}

func TestDataStoreMigrateLegacyUsage(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "usage.json")
	body := `{"version":1,"keys":{"h9":{"name":"Old","totalTokens":42,"updatedAt":111}}}`
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "data.json")
	d := NewDataStore(path, time.Second)
	if err := d.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := d.UsageTotal("h9"); got != 42 {
		t.Fatalf("migrated usage: got %d, want 42", got)
	}
	// First flush must materialize data.json; a later Load reads it (not legacy).
	if err := d.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("data.json not written: %v", err)
	}
	d2 := NewDataStore(path, time.Second)
	if err := d2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := d2.UsageTotal("h9"); got != 42 {
		t.Errorf("reload usage: got %d, want 42", got)
	}
}

func TestDataStoreWorkerFlushOnStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	d := NewDataStore(path, time.Hour) // long interval: rely on Stop flush
	d.Start()
	d.AddUsage("abc", "K", 9)
	d.Stop()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after stop: %v", err)
	}
	var doc dataDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Usage["abc"] == nil || doc.Usage["abc"].TotalTokens != 9 {
		t.Errorf("usage not flushed on stop: %+v", doc.Usage)
	}
	// Idempotent.
	d.Stop()
}

func TestDataStoreNoWriteWhenClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	d := NewDataStore(path, time.Second)
	if err := d.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("clean store should not write a file, err=%v", err)
	}
}
