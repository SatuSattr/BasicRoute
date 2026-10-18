package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"basicroute/app/config"
)

// Batas timeout test key/model di TUI agar tidak menggantung.
const (
	TestTimeoutMin = 10 * time.Second
	TestTimeoutMax = 30 * time.Second
)

// TestTimeout menormalkan timeoutSecs provider ke rentang test interaktif.
func TestTimeout(timeoutSecs int) time.Duration {
	d := time.Duration(timeoutSecs) * time.Second
	if d < TestTimeoutMin {
		return TestTimeoutMin
	}
	if d > TestTimeoutMax {
		return TestTimeoutMax
	}
	return d
}

// ResolveProxy memetakan proxy provider ke (type, url) kandidat pertama.
// Pool kosong = direct ("", "", nil). Dipakai test manual TUI (key/model)
// dan silent re-test; jalur request memakai ResolveProxies (multi kandidat).
func ResolveProxy(snap *config.Snapshot, prov *config.Provider) (proxyType, proxyURL string, err error) {
	targets, err := ResolveProxies(snap, prov)
	if err != nil {
		return "", "", err
	}
	if len(targets) == 0 {
		return "", "", nil
	}
	return targets[0].Type, targets[0].URL, nil
}

// ModelsURL membangun URL endpoint daftar model dari baseURL provider
// (GET {base}/models; dipakai provider [OI] maupun Google).
func ModelsURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
}

// testClient membangun http client sekali pakai lewat proxy provider.
func testClient(proxyType, proxyURL string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = TestTimeoutMin
	}
	rt, err := roundTripperFor(proxyType, strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	defer closeIdle(rt)
	switch tr := rt.(type) {
	case *http.Transport:
		tr.ResponseHeaderTimeout = timeout
	case *relayTransport:
		tr.base.ResponseHeaderTimeout = timeout
	}
	return &http.Client{Transport: rt, Timeout: timeout}, nil
}

// errBody memformat error HTTP upstream beserta potongan body.
func errBody(what string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		return fmt.Errorf("%s: upstream HTTP %d", what, status)
	}
	return fmt.Errorf("%s: upstream HTTP %d: %s", what, status, msg)
}

// TestProvKey menguji satu provider key via GET {baseURL}/models.
// Return nil bila key valid (HTTP 2xx).
func TestProvKey(baseURL, provType, proxyType, proxyURL, key string, timeout time.Duration) error {
	client, err := testClient(proxyType, proxyURL, timeout)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, ModelsURL(baseURL), nil)
	if err != nil {
		return err
	}
	SetAuth(req.Header, provType, key)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errBody("key test", resp.StatusCode, body)
	}
	return nil
}

// TestProvModel menguji satu model dengan chat minimal memakai key tertentu.
// Isi jawaban AI tidak diparse: HTTP 2xx apa pun = berhasil.
func TestProvModel(baseURL, provType, iface, proxyType, proxyURL, modelSlug, key string, timeout time.Duration) error {
	client, err := testClient(proxyType, proxyURL, timeout)
	if err != nil {
		return err
	}
	prompt := "is 1+1=2?, answer only yes or no"
	var payload map[string]any
	switch {
	case strings.EqualFold(strings.TrimSpace(provType), config.ProviderTypeGoogle):
		payload = map[string]any{
			"model":             modelSlug,
			"input":             prompt,
			"store":             false,
			"generation_config": map[string]any{"max_output_tokens": 8},
		}
	case strings.TrimSpace(iface) == "responses-api":
		payload = map[string]any{
			"model":             modelSlug,
			"input":             prompt,
			"max_output_tokens": 8,
		}
	default: // chat-completions (dan default lain)
		payload = map[string]any{
			"model":      modelSlug,
			"messages":   []map[string]string{{"role": "user", "content": prompt}},
			"max_tokens": 8,
			"stream":     false,
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, TargetURL(baseURL, iface), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	SetAuth(req.Header, provType, key)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errBody("model test", resp.StatusCode, respBody)
	}
	return nil
}

// IsAuthErr melaporkan apakah error test menandakan key tidak valid
// (401/403, atau 400 API_KEY_INVALID khas Google). Key semacam itu dilewati
// ke key berikutnya saat test model.
func IsAuthErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 401") || strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "invalid_api_key") || strings.Contains(msg, "api_key_invalid") ||
		strings.Contains(msg, "api key not valid") || strings.Contains(msg, "invalid api key")
}
