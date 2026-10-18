package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// cfgWithModel membungkus satu definisi model ke config minimal yang valid.
func cfgWithModel(modelJSON string) string {
	return fmt.Sprintf(`{
  "system": {"host": "0.0.0.0", "port": 21172, "requireAuth": false,
    "keys": {}, "limits": {"maxBodyMB": 10}},
  "providers": {"p1": {"name": "P", "prefix": "p", "type": "openai",
    "interface": "chat-completions", "baseUrl": "https://api.example.com/v1",
    "enabled": true, "timeoutSecs": 30,
    "keys": {"k1": {"key": "sk-x", "order": 1, "enabled": true}},
    "models": {"m": %s}}},
  "proxyPools": {}
}`, modelJSON)
}

func loadModel(t *testing.T, modelJSON string) (*Snapshot, error) {
	t.Helper()
	return LoadWithEnv(writeCfg(t, cfgWithModel(modelJSON)), "-")
}

func TestModelInputNormalize(t *testing.T) {
	// Case/whitespace dinormalkan ke lowercase + trim; duplikat dibuang.
	snap, err := loadModel(t, `{"name": "M", "enabled": true,
		"input": ["Text", " IMAGE ", "text"],
		"limit": {"context": 1000, "output": 100}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := snap.Raw.Providers["p1"].Models["m"].Input
	want := []string{"text", "image"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Input = %v, want %v", got, want)
	}

	// Semua lima modalitas diterima, urutan dipertahankan.
	snap, err = loadModel(t, `{"name": "M", "enabled": true,
		"input": ["text", "image", "video", "audio", "pdf"],
		"limit": {"context": 1000, "output": 100}}`)
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	got = snap.Raw.Providers["p1"].Models["m"].Input
	want = []string{"text", "image", "video", "audio", "pdf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Input = %v, want %v", got, want)
	}
}

func TestModelInputAbsent(t *testing.T) {
	// Absen -> nil (tak dideklarasikan), tetap valid.
	snap, err := loadModel(t, `{"name": "M", "enabled": true,
		"limit": {"context": 1000, "output": 100}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := snap.Raw.Providers["p1"].Models["m"].Input; got != nil {
		t.Fatalf("absent input must stay nil, got %v", got)
	}
}

func TestModelInputReject(t *testing.T) {
	// Nilai tak dikenal ditolak, pesan menyebut nama field + nilai.
	_, err := loadModel(t, `{"name": "M", "enabled": true, "input": ["text", "weird"],
		"limit": {"context": 1000, "output": 100}}`)
	if err == nil {
		t.Fatal("unknown input type must fail")
	}
	if !strings.Contains(err.Error(), `models["m"].input`) || !strings.Contains(err.Error(), `"weird"`) {
		t.Fatalf("error must name the field and value, got %v", err)
	}

	// String kosong ditolak.
	if _, err := loadModel(t, `{"name": "M", "enabled": true, "input": ["text", ""],
		"limit": {"context": 1000, "output": 100}}`); err == nil {
		t.Fatal("empty input value must fail")
	}

	// Bukan array string: gagal decode.
	if _, err := loadModel(t, `{"name": "M", "enabled": true, "input": ["text", 7],
		"limit": {"context": 1000, "output": 100}}`); err == nil {
		t.Fatal("non-string input element must fail")
	}
}
