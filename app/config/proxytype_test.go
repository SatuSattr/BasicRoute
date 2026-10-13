package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const cfgBase = `{
  "system": {"host": "0.0.0.0", "port": 21172, "requireAuth": false,
    "keys": {}, "limits": {"maxBodyMB": 10}},
  "providers": {"p1": {"name": "P", "prefix": "p", "type": "openai",
    "interface": "chat-completions", "baseUrl": "https://api.example.com/v1",
    "enabled": true, "timeoutSecs": 30,
    "keys": {"k1": {"key": "sk-x", "order": 1, "enabled": true}},
    "models": {"m": {"name": "M", "enabled": true,
      "limit": {"context": 1000, "output": 100}}}}},
  "proxyPools": %s
}`

func loadPools(t *testing.T, pools string) (*Snapshot, error) {
	t.Helper()
	return LoadWithEnv(writeCfg(t, fmt.Sprintf(cfgBase, pools)), "-")
}

func TestProxyPoolTypes(t *testing.T) {
	// http dengan kredensial ala 711Proxy/9Proxy: valid, tipe dipertahankan.
	snap, err := loadPools(t, `{"a": {"type": "http",
		"url": "http://customer123:your_password@proxy.example.com:8080", "enabled": true}}`)
	if err != nil {
		t.Fatalf("http pool: %v", err)
	}
	if snap.Raw.ProxyPools["a"].Type != ProxyTypeHTTP {
		t.Fatalf("type = %q", snap.Raw.ProxyPools["a"].Type)
	}

	// tipe kosong => default http.
	snap, err = loadPools(t, `{"a": {"url": "http://127.0.0.1:8080", "enabled": true}}`)
	if err != nil {
		t.Fatalf("default type: %v", err)
	}
	if snap.Raw.ProxyPools["a"].Type != ProxyTypeHTTP {
		t.Fatalf("default type = %q", snap.Raw.ProxyPools["a"].Type)
	}

	// relay valid.
	snap, err = loadPools(t, `{"r": {"type": "relay",
		"url": "https://relay-example.vercel.app", "enabled": true}}`)
	if err != nil {
		t.Fatalf("relay pool: %v", err)
	}
	if snap.Raw.ProxyPools["r"].Type != ProxyTypeRelay {
		t.Fatalf("type = %q", snap.Raw.ProxyPools["r"].Type)
	}

	// relay dengan userinfo: ditolak.
	if _, err = loadPools(t, `{"r": {"type": "relay",
		"url": "https://user:pass@relay-example.vercel.app", "enabled": true}}`); err == nil {
		t.Fatal("relay with userinfo should fail")
	}

	// tipe tak dikenal: ditolak.
	if _, err = loadPools(t, `{"x": {"type": "socks",
		"url": "http://127.0.0.1:8080", "enabled": true}}`); err == nil {
		t.Fatal("unknown type should fail")
	}
}
