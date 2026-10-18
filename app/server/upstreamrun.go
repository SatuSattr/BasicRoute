package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"basicroute/app/config"
	"basicroute/app/google"
	"basicroute/app/service"
)

// proxyRun adalah hasil menjalankan seluruh key ring lewat satu target proxy.
type proxyRun int

const (
	runServed    proxyRun = iota // respons sudah ditulis ke client: selesai
	runProxyDead                 // pool terbukti mati (flag dicapai): coba target berikutnya
	runStop                      // tidak ada lagi yang bisa dicoba: hentikan failover
)

// attemptKind adalah hasil satu percobaan ke upstream.
type attemptKind int

const (
	attemptServed        attemptKind = iota // respons diteruskan ke client
	attemptUpstreamRetry                    // masalah key/upstream: coba key berikutnya
	attemptProxyFail                        // masalah proxy: hitung, mungkin retry pool yang sama
)

type attemptOutcome struct {
	kind attemptKind
	err  error
}

// upstreamRun membawa konteks satu request untuk menembus satu target proxy.
type upstreamRun struct {
	s               *Server
	w               http.ResponseWriter
	r               *http.Request
	lg              *slog.Logger
	ref             *config.ModelRef
	client          *http.Client
	tgt             service.ProxyTarget
	upURL           string
	upBody          []byte
	timeout         time.Duration
	stream          bool
	inboundIface    string
	publicModel     string
	singleTry       bool
	ring            []*config.KeyEntry
	healthy         bool
	healthModelID   string
	callerHash      string
	callerName      string
	lastErr         error  // kegagalan terakhir (untuk pesan 502 terakhir)
	lastStatus      int    // status retryable/auth terakhir untuk driver
	lastRetryAfter  string // nilai header Retry-After terakhir
	lastBodySnippet string // max 300 char body terakhir (untuk pesan error)
}

// snippet memangkas body ke n char: trim spasi, ganti newline jadi spasi.
func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	// Collapse runs of spaces from newline replacement.
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if n <= 0 {
		return ""
	}
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// isProxySideFault mendelegasikan ke service.ProxyFailureKind (klasifikasi
// pool-side vs upstream-side, sadar-tipe relay/http) dengan pool target saat ini.
func isProxySideFault(proxyType, proxyURL string, status int, err error) bool {
	return service.ProxyFailureKind(proxyType, proxyURL, status, err)
}

// proxyLabel mengembalikan nama pool untuk log ("direct" bila tanpa pool).
func (u *upstreamRun) proxyLabel() string {
	if u.tgt.Name == "" {
		return "direct"
	}
	return u.tgt.Name
}

// eligibleKeys menyaring key ring: buang yang cooling down, dan buang yang
// flagged error selama masih ada key sehat (last-resort: semua dipakai).
func (u *upstreamRun) eligibleKeys() []*config.KeyEntry {
	st := u.s.st
	slug := u.ref.ProviderSlug
	out := make([]*config.KeyEntry, 0, len(u.ring))
	for _, ke := range u.ring {
		if ke == nil {
			continue
		}
		id := service.CooldownKey(slug, ke.Name)
		if st.Cooldown(id) {
			continue
		}
		if u.healthy && st.ProvHealth.IsError(id) {
			continue
		}
		out = append(out, ke)
	}
	return out
}

// exec menjalankan key ring lewat target proxy saat ini. Kegagalan proxy-level
// diulang beberapa kali (ProxyFailThreshold) sebelum pool dianggap mati, lalu
// pool di-flag dan caller pindah ke target berikutnya.
func (u *upstreamRun) exec(triedKeys *int) proxyRun {
	keys := u.eligibleKeys()
	if len(keys) == 0 {
		return runStop
	}
	direct := u.tgt.Name == ""
	for _, ke := range keys {
		keyID := service.CooldownKey(u.ref.ProviderSlug, ke.Name)
		attempts := 1
		if !direct {
			attempts = service.ProxyFailThreshold
		}
		advance := false
		for a := 0; a < attempts; a++ {
			*triedKeys++
			out := u.attempt(ke, keyID)
			switch out.kind {
			case attemptServed:
				return runServed
			case attemptUpstreamRetry:
				advance = true
			case attemptProxyFail:
				u.lastErr = out.err
				if out.err != nil && strings.Contains(out.err.Error(), "407") {
					u.s.st.ProxyHealth.SetError(u.tgt.Name, out.err.Error())
					u.lg.Warn("proxy flagged error", "provider", u.ref.ProviderSlug, "proxy", u.tgt.Name, "failFast", "407", "err", out.err.Error())
					return runProxyDead
				}
				n := u.s.st.NoteProxyResult(u.tgt.Name, false)
				if n >= service.ProxyFailThreshold {
					u.s.st.ProxyHealth.SetError(u.tgt.Name, out.err.Error())
					u.lg.Warn("proxy flagged error", "provider", u.ref.ProviderSlug, "proxy", u.tgt.Name, "consecutive", n, "err", out.err.Error())
					return runProxyDead
				}
				// Belum cukup bukti: ulangi pool yang sama.
			}
			if advance {
				break
			}
		}
		if u.singleTry {
			break
		}
	}
	return runStop
}

// attempt melakukan satu percobaan ke upstream memakai key ke.
func (u *upstreamRun) attempt(ke *config.KeyEntry, keyID string) attemptOutcome {
	ctx, cancel := context.WithTimeout(u.r.Context(), u.timeout)
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.upURL, bytes.NewReader(u.upBody))
	if err != nil {
		cancel()
		writeErr(u.w, http.StatusInternalServerError, "cannot build upstream request", "server_error", "")
		u.lg.Info("req<", "status", 500)
		return attemptOutcome{kind: attemptServed}
	}
	service.CopyHeaders(upReq.Header, u.r.Header)
	upReq.Header.Set("Content-Type", "application/json")
	service.SetAuth(upReq.Header, u.ref.Provider.Type, ke.Key)

	u.lg.Info("up>", "target", u.upURL, "provider", u.ref.ProviderSlug, "proxy", u.proxyLabel(), "key", ke.Name, "masked", MaskKey(ke.Key))
	t0 := time.Now()
	resp, err := u.client.Do(upReq)
	if err != nil {
		el := time.Since(t0)
		cancel()
		u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, el.Milliseconds(), true)
		if service.IsProxyFailure(0, err) && u.tgt.Name != "" {
			if !isProxySideFault(u.tgt.Type, u.tgt.URL, 0, err) {
				// Bukan sisi-proxy (mis. DNS/TLS upstream): jangan merahkan pool; perlakukan sebagai UpstreamRetry.
				u.lastErr = err
				u.s.st.MarkCooldown(keyID, 60*time.Second)
				u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "key", ke.Name, "err", err.Error(), "latencyMs", el.Milliseconds())
				return attemptOutcome{kind: attemptUpstreamRetry, err: err}
			}
			u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "proxy", u.tgt.Name, "err", err.Error(), "latencyMs", el.Milliseconds())
			return attemptOutcome{kind: attemptProxyFail, err: err}
		}
		// Direct, atau request dibatalkan pemanggil: kegagalan upstream biasa.
		u.lastErr = err
		u.s.st.MarkCooldown(keyID, 60*time.Second)
		u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "key", ke.Name, "err", err.Error(), "latencyMs", el.Milliseconds())
		return attemptOutcome{kind: attemptUpstreamRetry, err: err}
	}
	ttft := time.Since(t0)

	// 407 Proxy Authentication Required: masalah pool, bukan upstream.
	if resp.StatusCode == http.StatusProxyAuthRequired && u.tgt.Name != "" {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		cancel()
		u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, ttft.Milliseconds(), true)
		u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "proxy", u.tgt.Name, "status", resp.StatusCode)
		return attemptOutcome{kind: attemptProxyFail, err: fmt.Errorf("upstream HTTP %d no-retry", resp.StatusCode)}
	}

	// Google melaporkan API key tidak valid sebagai 400 INVALID_ARGUMENT:
	// perlakukan seperti 401 agar failover antar-key tetap jalan.
	if !u.singleTry && u.ref.Provider.IsGoogle() && resp.StatusCode == http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if google.IsAuthError(body) {
			cancel()
			u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, ttft.Milliseconds(), true)
			msg := fmt.Sprintf("upstream HTTP %d for model %q (invalid api key)", resp.StatusCode, u.ref.ModelSlug)
			u.s.st.ProvHealth.SetError(keyID, msg)
			u.s.st.MarkCooldown(keyID, 60*time.Second)
			u.lastStatus = resp.StatusCode
			u.lastBodySnippet = snippet(body, 300)
			u.lastErr = fmt.Errorf("upstream HTTP %d", resp.StatusCode)
			u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "key", ke.Name, "status", resp.StatusCode, "retry", true)
			return attemptOutcome{kind: attemptUpstreamRetry, err: u.lastErr}
		}
	}

	// 401/403/408: key bermasalah/transien -> coba key berikutnya
	// (kecuali singleTry: hormati circuit-breaker model flagged, serve langsung).
	if !u.singleTry && (resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 408) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		cancel()
		u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, ttft.Milliseconds(), true)
		msg := fmt.Sprintf("upstream HTTP %d for model %q", resp.StatusCode, u.ref.ModelSlug)
		u.s.st.ProvHealth.SetError(keyID, msg)
		if cd := service.CooldownForStatus(resp.StatusCode, resp.Header.Get("Retry-After")); cd > 0 {
			u.s.st.MarkCooldown(keyID, cd)
		} else {
			u.s.st.MarkCooldown(keyID, 60*time.Second)
		}
		u.lastStatus = resp.StatusCode
		u.lastRetryAfter = resp.Header.Get("Retry-After")
		u.lastBodySnippet = snippet(body, 300)
		u.lastErr = fmt.Errorf("upstream HTTP %d", resp.StatusCode)
		u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "key", ke.Name, "status", resp.StatusCode, "retry", true)
		return attemptOutcome{kind: attemptUpstreamRetry, err: u.lastErr}
	}

	// Status transien (429/5xx): cooldown key, coba key berikutnya.
	if !u.singleTry && service.IsRetryable(resp.StatusCode, nil) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		cancel()
		if cd := service.CooldownForStatus(resp.StatusCode, resp.Header.Get("Retry-After")); cd > 0 {
			u.s.st.MarkCooldown(keyID, cd)
		} else {
			u.s.st.MarkCooldown(keyID, 60*time.Second)
		}
		u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, ttft.Milliseconds(), true)
		u.lastStatus = resp.StatusCode
		u.lastRetryAfter = resp.Header.Get("Retry-After")
		u.lastBodySnippet = snippet(body, 300)
		if u.lastErr == nil {
			u.lastErr = fmt.Errorf("upstream HTTP %d", resp.StatusCode)
		}
		u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "key", ke.Name, "status", resp.StatusCode, "retry", true)
		return attemptOutcome{kind: attemptUpstreamRetry, err: u.lastErr}
	}

	// Teruskan respons apa adanya (streaming atau buffered).
	u.serve(resp, ke, keyID, ttft, t0)
	cancel()
	return attemptOutcome{kind: attemptServed}
}

// serve meneruskan respons upstream ke client sambil mencatat usage/health.
func (u *upstreamRun) serve(resp *http.Response, ke *config.KeyEntry, keyID string, ttft time.Duration, t0 time.Time) {
	sse := u.stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	ok2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
	cap := newCapture(4 << 20) // simpan ekor s/d 4MB untuk ekstrak usage
	var n int64
	if u.ref.Provider.IsGoogle() {
		n = u.serveGoogle(resp, sse, ok2xx, cap)
	} else if sse {
		u.w.Header().Set("Content-Type", "text/event-stream")
		u.w.Header().Set("Cache-Control", "no-cache")
		u.w.Header().Set("Connection", "keep-alive")
		u.w.Header().Set("X-Accel-Buffering", "no")
		u.w.WriteHeader(resp.StatusCode)
		if fl, ok := u.w.(http.Flusher); ok {
			fl.Flush()
			n, _ = io.Copy(flushWriter{w: io.MultiWriter(u.w, cap), f: fl}, resp.Body)
		} else {
			n, _ = io.Copy(io.MultiWriter(u.w, cap), resp.Body)
		}
		resp.Body.Close()
	} else {
		// Non-SSE: buffer dulu dengan batas 8MB agar respons non-2xx
		// diteruskan utuh; fallback antar-key sudah ditangani di attempt().
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			u.w.Header().Set("Content-Type", ct)
		}
		u.w.WriteHeader(resp.StatusCode)
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		n, _ = io.Copy(io.MultiWriter(u.w, cap), bytes.NewReader(body))
	}
	el := time.Since(t0)
	u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, el.Milliseconds(), !ok2xx)
	if ok2xx && u.s.st.Data != nil && u.callerHash != "" {
		// AddUsage selalu dipanggil (walau toks==0) agar waktu terakhir request
		// tercatat; tokens hanya diakumulasi bila ada.
		u.s.st.Data.AddUsage(u.callerHash, u.callerName, extractTotalTokens(cap.Bytes(), sse))
	}
	if ok2xx {
		// Sukses = key, model, dan pool pulih: hapus flag + reset counter.
		u.s.st.ProvHealth.SetOK(keyID)
		u.s.st.ProvHealth.SetOK(u.healthModelID)
		u.s.st.NoteModelResult(u.ref.FullID, true, "")
		if u.tgt.Name != "" {
			u.s.st.ProxyHealth.SetOK(u.tgt.Name)
			u.s.st.NoteProxyResult(u.tgt.Name, true)
		}
	} else {
		u.s.noteUpstreamFailure(u.lg, u.ref, u.healthModelID, keyID, ke.Name, resp.StatusCode)
	}
	u.lg.Info("req<", "status", resp.StatusCode, "bytes", n, "latencyMs", el.Milliseconds(), "ttftMs", ttft.Milliseconds(), "key", ke.Name)
}

// serveGoogle menerjemahkan respons Interactions API ke bentuk [OI] lalu
// menuliskannya ke client. Streaming diterjemahkan event-per-event; non-2xx
// memakai envelope error [OI].
func (u *upstreamRun) serveGoogle(resp *http.Response, sse, ok2xx bool, cap *capture) int64 {
	upstreamSSE := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	if sse && ok2xx && upstreamSSE {
		u.w.Header().Set("Content-Type", "text/event-stream")
		u.w.Header().Set("Cache-Control", "no-cache")
		u.w.Header().Set("Connection", "keep-alive")
		u.w.Header().Set("X-Accel-Buffering", "no")
		u.w.WriteHeader(resp.StatusCode)
		var sink io.Writer = u.w
		if fl, ok := u.w.(http.Flusher); ok {
			sink = flushWriter{w: u.w, f: fl}
		}
		cw := &countWriter{w: io.MultiWriter(sink, cap)}
		if err := google.TranslateStream(resp.Body, cw, u.inboundIface, u.publicModel); err != nil {
			u.lg.Warn("google stream translation failed", "provider", u.ref.ProviderSlug, "err", err.Error())
		}
		resp.Body.Close()
		return cw.n
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if ok2xx {
		if translated, err := google.TranslateResponse(body, u.inboundIface, u.publicModel); err == nil {
			body = translated
		} else {
			u.lg.Warn("google response translation failed", "provider", u.ref.ProviderSlug, "err", err.Error())
		}
	} else {
		body = google.TranslateError(body, resp.StatusCode)
	}
	u.w.Header().Set("Content-Type", "application/json")
	u.w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(io.MultiWriter(u.w, cap), bytes.NewReader(body))
	return n
}

// countWriter menghitung byte yang benar-benar ditulis ke client.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
