package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"basicroute/app/config"
)

// normalizeProxyType memetakan tipe kosong/tak dikenal ke forward proxy http.
func normalizeProxyType(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), config.ProxyTypeRelay) {
		return config.ProxyTypeRelay
	}
	return config.ProxyTypeHTTP
}

type Pool struct {
	mu      sync.RWMutex
	clients map[string]*http.Client
}

func NewPool() *Pool {
	return &Pool{clients: map[string]*http.Client{}}
}

func (p *Pool) ClientFor(proxyType, proxyURL string) (*http.Client, error) {
	key := normalizeProxyType(proxyType) + "|" + strings.TrimSpace(proxyURL)

	p.mu.RLock()
	if c, ok := p.clients[key]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[key]; ok {
		return c, nil
	}
	tr, err := roundTripperFor(proxyType, strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	c := &http.Client{Transport: tr}
	p.clients[key] = c
	slog.Debug("upstream: http client created", "type", normalizeProxyType(proxyType), "direct", strings.TrimSpace(proxyURL) == "")
	return c, nil
}

func (p *Pool) CloseIdleConnections() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, c := range p.clients {
		closeIdle(c.Transport)
	}
}

type idleCloser interface{ CloseIdleConnections() }

func closeIdle(rt http.RoundTripper) {
	if ic, ok := rt.(idleCloser); ok {
		ic.CloseIdleConnections()
	}
}

// newBaseTransport membuat transport dasar tanpa proxy (dipakai direct maupun
// sebagai lapisan bawah relay).
func newBaseTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// relayTransport meneruskan request ke http relay ala 9router: request dikirim
// ke URL relay, target asli dinyatakan lewat header x-relay-target/x-relay-path,
// lalu relay melakukan fetch() ke upstream dan mengembalikan respons apa adanya.
type relayTransport struct {
	relay *url.URL
	base  *http.Transport
}

func (rt *relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target := req.URL
	out := req.Clone(req.Context())
	u := *rt.relay
	out.URL = &u
	out.Host = u.Host
	if req.Header != nil {
		out.Header = req.Header.Clone()
	} else {
		out.Header = http.Header{}
	}
	// Jangan pernah bocorkan header kontrol relay ke upstream.
	out.Header.Del("X-Relay-Target")
	out.Header.Del("X-Relay-Path")
	out.Header.Set("X-Relay-Target", target.Scheme+"://"+target.Host)
	out.Header.Set("X-Relay-Path", target.RequestURI())
	return rt.base.RoundTrip(out)
}

func (rt *relayTransport) CloseIdleConnections() { rt.base.CloseIdleConnections() }

func roundTripperFor(proxyType, proxyURL string) (http.RoundTripper, error) {
	if strings.TrimSpace(proxyURL) == "" {
		return newBaseTransport(), nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("upstream: invalid proxy URL %q: %w", proxyURL, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("upstream: proxy scheme %q unsupported (want http/https), got %q", u.Scheme, proxyURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("upstream: proxy URL %q has no host", proxyURL)
	}
	if normalizeProxyType(proxyType) == config.ProxyTypeRelay {
		if u.User != nil {
			return nil, fmt.Errorf("upstream: relay proxy %q must not embed user:password", proxyURL)
		}
		return &relayTransport{relay: u, base: newBaseTransport()}, nil
	}
	tr := newBaseTransport()
	tr.Proxy = http.ProxyURL(u)
	return tr, nil
}

func IsRetryable(status int, err error) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	if err == nil {
		return false
	}
	return isRetryableNetErr(err)
}

// ParseRetryAfter mem-parse nilai header Retry-After: detik integer atau
// http-date. Clamp 1s..300s. Return 0 bila kosong/invalid/masa lalu.
func ParseRetryAfter(v string) time.Duration {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0
		}
		if n < 1 {
			n = 1
		}
		if n > 300 {
			n = 300
		}
		return time.Duration(n) * time.Second
	}
	t, err := http.ParseTime(s)
	if err != nil {
		// http.ParseTime hanya menerima "GMT"; coba RFC1123 umum (zona lain).
		t, err = time.Parse(time.RFC1123, s)
	}
	if err != nil {
		return 0
	}
	d := time.Until(t)
	if d <= 0 {
		return 0
	}
	if d < time.Second {
		d = time.Second
	}
	if d > 300*time.Second {
		d = 300 * time.Second
	}
	return d
}

// CooldownForStatus menentukan durasi cooldown untuk status HTTP upstream.
// Status 401/403/408 selalu 60s (abaikan retry-after). Status retryable
// lainnya memakai ParseRetryAfter bila >0, else 60s. Selain itu return 0.
func CooldownForStatus(status int, retryAfter string) time.Duration {
	switch status {
	case 401, 403, 408:
		return 60 * time.Second
	}
	if IsRetryable(status, nil) {
		if d := ParseRetryAfter(retryAfter); d > 0 {
			return d
		}
		return 60 * time.Second
	}
	return 0
}

func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"connection reset",
		"connection refused",
		"broken pipe",
		"unexpected eof",
		"no such host",
		"network is unreachable",
		"timed out",
		"timeout",
		"temporary",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// IsProxyConnectFailure melaporkan apakah error transport jelas terjadi di hop
// pool (bukan upstream). proxyType membedakan dua dunia:
//   - relay: client hanya dial ke relay; fetch upstream terjadi di sisi relay
//     dan kegagalannya kembali sebagai status HTTP, bukan transport error.
//     Maka SEMUA transport error non-cancel adalah salah relay.
//   - http (forward): dial ke proxy, lalu TLS end-to-end ke upstream. Sinyal
//     TLS/x509 dan DNS telanjang adalah ranah upstream; sinyal proxyconnect,
//     host-gated, dan unknown (dial targetnya memang proxy) adalah salah pool.
func IsProxyConnectFailure(proxyType, proxyURL string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if normalizeProxyType(proxyType) == config.ProxyTypeRelay {
		return true
	}
	msg := strings.ToLower(err.Error())

	// Sinyal generik hop-proxy (tanpa perlu host match). Dicek dulu agar
	// hostname pool yang salah ("proxyconnect … no such host") tetap true.
	for _, s := range []string{
		"proxyconnect",
		"proxy connect",
		"proxy auth",
		"proxy error",
		"tunnel connection failed",
		"connect: connection refused",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}

	// Sinyal upstream (TLS end-to-end / DNS telanjang): bukan salah pool.
	for _, s := range []string{
		"no such host",
		"x509",
		"certificate",
		"tls handshake",
		"handshake failure",
	} {
		if strings.Contains(msg, s) {
			return false
		}
	}

	// Sinyal host-gated: msg harus menyebut host proxy DAN jenis gagal konek.
	proxyHost := ""
	if u, perr := url.Parse(proxyURL); perr == nil && u.Host != "" {
		proxyHost = strings.ToLower(u.Hostname())
		if proxyHost == "" {
			proxyHost = strings.ToLower(u.Host)
		}
	}
	if proxyHost != "" && strings.Contains(msg, proxyHost) {
		for _, s := range []string{
			"connection refused",
			"connection reset",
			"timed out",
			"timeout",
			"no route to host",
		} {
			if strings.Contains(msg, s) {
				return true
			}
		}
	}
	// Unknown via forward proxy: dial targetnya adalah proxy itu sendiri,
	// jadi perlakukan sebagai salah pool (pertahankan failover lama).
	return true
}

// ProxyFailureKind true bila ini murni salah pool: status 407, atau error
// transport yang jelas di hop pool (lihat IsProxyConnectFailure).
// Status lain tanpa err (429/5xx/401/...) adalah ranah key/upstream, bukan pool.
func ProxyFailureKind(proxyType, proxyURL string, status int, err error) bool {
	if status == 407 {
		return true
	}
	if err != nil {
		return IsProxyConnectFailure(proxyType, proxyURL, err)
	}
	return false
}

// DefaultProxyTestURL dipakai bila config tidak menentukan proxyTestUrl.
// Endpoint 204 ringan yang umum dipakai untuk cek konektivitas.
const DefaultProxyTestURL = "https://www.gstatic.com/generate_204"

// TestProxy melakukan GET ke testURL lewat proxy (forward proxy atau http
// relay, sesuai proxyType) untuk memastikan proxy bisa meneruskan trafik.
// Return nil bila proxy sehat.
func TestProxy(proxyType, proxyURL, testURL string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if strings.TrimSpace(testURL) == "" {
		testURL = DefaultProxyTestURL
	}
	rt, err := roundTripperFor(proxyType, strings.TrimSpace(proxyURL))
	if err != nil {
		return err
	}
	defer closeIdle(rt)
	switch tr := rt.(type) {
	case *http.Transport:
		tr.ResponseHeaderTimeout = timeout
	case *relayTransport:
		tr.base.ResponseHeaderTimeout = timeout
	}
	client := &http.Client{Transport: rt, Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, testURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("proxy test: upstream HTTP %d", resp.StatusCode)
	}
	return nil
}

func TargetURL(base, iface string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	switch strings.TrimSpace(iface) {
	case "chat-completions":
		if strings.HasSuffix(b, "/chat/completions") {
			return b
		}
		return b + "/chat/completions"
	case "responses-api":
		if strings.HasSuffix(b, "/responses") {
			return b
		}
		return b + "/responses"
	default:
		return b
	}
}

var skipHeaders = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"authorization":       {},
	"host":                {},
	"content-length":      {},
}

func CopyHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	dynamic := map[string]struct{}{}
	for _, v := range src["Connection"] {
		for _, token := range strings.Split(v, ",") {
			if t := strings.ToLower(strings.TrimSpace(token)); t != "" {
				dynamic[t] = struct{}{}
			}
		}
	}
	for k, vv := range src {
		lk := strings.ToLower(k)
		if _, skip := skipHeaders[lk]; skip {
			continue
		}
		if _, skip := dynamic[lk]; skip {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
