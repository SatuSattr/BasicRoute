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
	case 429, 500, 502, 503, 504:
		return true
	}
	if err == nil {
		return false
	}
	return isRetryableNetErr(err)
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
