package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		"url": "http://customer123:your_password@proxy.example.com:8080", "order": 1, "enabled": true}}`)
	if err != nil {
		t.Fatalf("http pool: %v", err)
	}
	if snap.Raw.ProxyPools["a"].Type != ProxyTypeHTTP {
		t.Fatalf("type = %q", snap.Raw.ProxyPools["a"].Type)
	}

	// tipe kosong => default http.
	snap, err = loadPools(t, `{"a": {"url": "http://127.0.0.1:8080", "order": 1, "enabled": true}}`)
	if err != nil {
		t.Fatalf("default type: %v", err)
	}
	if snap.Raw.ProxyPools["a"].Type != ProxyTypeHTTP {
		t.Fatalf("default type = %q", snap.Raw.ProxyPools["a"].Type)
	}

	// relay valid.
	snap, err = loadPools(t, `{"r": {"type": "relay",
		"url": "https://relay-example.vercel.app", "order": 1, "enabled": true}}`)
	if err != nil {
		t.Fatalf("relay pool: %v", err)
	}
	if snap.Raw.ProxyPools["r"].Type != ProxyTypeRelay {
		t.Fatalf("type = %q", snap.Raw.ProxyPools["r"].Type)
	}

	// relay dengan userinfo: ditolak.
	if _, err = loadPools(t, `{"r": {"type": "relay",
		"url": "https://user:pass@relay-example.vercel.app", "order": 1, "enabled": true}}`); err == nil {
		t.Fatal("relay with userinfo should fail")
	}

	// tipe tak dikenal: ditolak.
	if _, err = loadPools(t, `{"x": {"type": "socks",
		"url": "http://127.0.0.1:8080", "order": 1, "enabled": true}}`); err == nil {
		t.Fatal("unknown type should fail")
	}
}

func TestProxyPoolOrder(t *testing.T) {
	// order wajib.
	if _, err := loadPools(t, `{"a": {"url": "http://127.0.0.1:8080", "enabled": true}}`); err == nil {
		t.Fatal("missing order should fail")
	}
	// order harus >= 1.
	if _, err := loadPools(t, `{"a": {"url": "http://127.0.0.1:8080", "order": 0, "enabled": true}}`); err == nil {
		t.Fatal("order 0 should fail")
	}
	// order duplikat: ditolak.
	if _, err := loadPools(t, `{"a": {"url": "http://127.0.0.1:8080", "order": 1, "enabled": true},
		"b": {"url": "http://127.0.0.1:8081", "order": 1, "enabled": true}}`); err == nil {
		t.Fatal("duplicate order should fail")
	}
	// ProxyOrder terurut order asc, nama asc pada tie.
	snap, err := loadPools(t, `{"c": {"url": "http://127.0.0.1:8083", "order": 3, "enabled": true},
		"a": {"url": "http://127.0.0.1:8081", "order": 1, "enabled": true},
		"b": {"url": "http://127.0.0.1:8082", "order": 2, "enabled": false}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if len(snap.ProxyOrder) != len(want) {
		t.Fatalf("ProxyOrder = %v", snap.ProxyOrder)
	}
	for i := range want {
		if snap.ProxyOrder[i] != want[i] {
			t.Fatalf("ProxyOrder = %v, want %v", snap.ProxyOrder, want)
		}
	}
}

func TestProxyPoolReservedName(t *testing.T) {
	for _, name := range []string{"auto", "round", "AUTO", "Round"} {
		if _, err := loadPools(t, fmt.Sprintf(`{"%s": {"url": "http://127.0.0.1:8080", "order": 1, "enabled": true}}`, name)); err == nil {
			t.Fatalf("reserved pool name %q should fail", name)
		}
	}
}

func TestProviderProxyMode(t *testing.T) {
	// "auto"/"round" diterima sebagai mode, dinormalkan ke lowercase.
	for _, mode := range []string{"auto", "round", "AUTO", "Round"} {
		body := fmt.Sprintf(cfgBase, `{"a": {"url": "http://127.0.0.1:8080", "order": 1, "enabled": true}}`)
		body = strings.Replace(body, `"baseUrl": "https://api.example.com/v1",`,
			`"baseUrl": "https://api.example.com/v1", "proxy": `+strconv.Quote(mode)+`,`, 1)
		snap, err := LoadWithEnv(writeCfg(t, body), "-")
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		got := snap.Raw.Providers["p1"].Proxy
		if got != strings.ToLower(mode) {
			t.Fatalf("mode %q normalized to %q", mode, got)
		}
	}
	// nama pool tak dikenal: ditolak.
	body := fmt.Sprintf(cfgBase, `{"a": {"url": "http://127.0.0.1:8080", "order": 1, "enabled": true}}`)
	body = strings.Replace(body, `"baseUrl": "https://api.example.com/v1",`,
		`"baseUrl": "https://api.example.com/v1", "proxy": "nope",`, 1)
	if _, err := LoadWithEnv(writeCfg(t, body), "-"); err == nil {
		t.Fatal("unknown pool name should fail")
	}

	// mode auto tanpa pool enabled: ditolak (gagalkan run, bukan diam-diam).
	body = fmt.Sprintf(cfgBase, `{}`)
	body = strings.Replace(body, `"baseUrl": "https://api.example.com/v1",`,
		`"baseUrl": "https://api.example.com/v1", "proxy": "auto",`, 1)
	if _, err := LoadWithEnv(writeCfg(t, body), "-"); err == nil {
		t.Fatal("auto mode without enabled pool should fail")
	}

	// mode auto dengan hanya pool disabled: tetap ditolak.
	body = fmt.Sprintf(cfgBase, `{"a": {"url": "http://127.0.0.1:8080", "order": 1, "enabled": false}}`)
	body = strings.Replace(body, `"baseUrl": "https://api.example.com/v1",`,
		`"baseUrl": "https://api.example.com/v1", "proxy": "auto",`, 1)
	if _, err := LoadWithEnv(writeCfg(t, body), "-"); err == nil {
		t.Fatal("auto mode with only disabled pools should fail")
	}
}
