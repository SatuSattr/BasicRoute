package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Pool struct {
	mu      sync.RWMutex
	clients map[string]*http.Client
}

func NewPool() *Pool {
	return &Pool{clients: map[string]*http.Client{}}
}

func (p *Pool) ClientFor(proxyURL string) (*http.Client, error) {
	key := strings.TrimSpace(proxyURL)

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
	tr, err := transportFor(key)
	if err != nil {
		return nil, err
	}
	c := &http.Client{Transport: tr}
	p.clients[key] = c
	slog.Debug("upstream: http client created", "direct", key == "")
	return c, nil
}

func (p *Pool) CloseIdleConnections() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, c := range p.clients {
		if tr, ok := c.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	}
}

func transportFor(proxyURL string) (*http.Transport, error) {
	var proxyFn func(*http.Request) (*url.URL, error)
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("upstream: invalid proxy URL %q: %w", proxyURL, err)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			if u.Host == "" {
				return nil, fmt.Errorf("upstream: proxy URL %q has no host", proxyURL)
			}
			proxyFn = http.ProxyURL(u)
		case "socks5", "socks5h":
			return nil, fmt.Errorf("upstream: socks5 proxy %q unsupported (stdlib only) — configure http proxy or direct", proxyURL)
		default:
			return nil, fmt.Errorf("upstream: proxy scheme %q unsupported (want http/https), got %q", u.Scheme, proxyURL)
		}
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy:                 proxyFn,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, nil
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
