package config

import "testing"

func googleCfg(providerBody string) string {
	return `{
  "system": {"host": "0.0.0.0", "port": 21172, "requireAuth": false,
    "keys": {}, "limits": {"maxBodyMB": 10}},
  "providers": {"g": ` + providerBody + `},
  "proxyPools": {}
}`
}

func TestGoogleProviderDefaults(t *testing.T) {
	snap, err := LoadWithEnv(writeCfg(t, googleCfg(`{
    "name": "G", "prefix": "g", "type": "google",
    "enabled": true, "timeoutSecs": 30,
    "keys": {"k1": {"key": "x", "order": 1, "enabled": true}},
    "models": {"gemini-3.8-flash": {"name": "Flash", "enabled": true,
      "limit": {"context": 1000, "output": 100}}}}`)), "-")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := snap.Raw.Providers["g"]
	if p.Interface != InterfaceInteractions {
		t.Fatalf("interface = %q, want %q", p.Interface, InterfaceInteractions)
	}
	if p.BaseURL != DefaultGoogleBaseURL {
		t.Fatalf("baseUrl = %q, want %q", p.BaseURL, DefaultGoogleBaseURL)
	}
	if !p.IsGoogle() {
		t.Fatal("IsGoogle() = false")
	}
}

func TestGoogleProviderRejectsOtherInterface(t *testing.T) {
	_, err := LoadWithEnv(writeCfg(t, googleCfg(`{
    "name": "G", "prefix": "g", "type": "google", "interface": "chat-completions",
    "enabled": true, "timeoutSecs": 30,
    "keys": {"k1": {"key": "x", "order": 1, "enabled": true}},
    "models": {"m": {"name": "M", "enabled": true,
      "limit": {"context": 1, "output": 1}}}}`)), "-")
	if err == nil {
		t.Fatal("expected error for interface != interactions")
	}
}
