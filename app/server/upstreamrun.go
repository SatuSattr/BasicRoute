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
	s             *Server
	w             http.ResponseWriter
	r             *http.Request
	lg            *slog.Logger
	ref           *config.ModelRef
	client        *http.Client
	tgt           service.ProxyTarget
	upURL         string
	upBody        []byte
	timeout       time.Duration
	stream        bool
	singleTry     bool
	ring          []*config.KeyEntry
	healthy       bool
	healthModelID string
	callerHash    string
	callerName    string
	lastErr       error // kegagalan terakhir (untuk pesan 502 terakhir)
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
	upReq.Header.Set("Authorization", "Bearer "+ke.Key)

	u.lg.Info("up>", "target", u.upURL, "provider", u.ref.ProviderSlug, "proxy", u.proxyLabel(), "key", ke.Name, "masked", MaskKey(ke.Key))
	t0 := time.Now()
	resp, err := u.client.Do(upReq)
	if err != nil {
		el := time.Since(t0)
		cancel()
		u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, el.Milliseconds(), true)
		if service.IsProxyFailure(0, err) && u.tgt.Name != "" {
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
		return attemptOutcome{kind: attemptProxyFail, err: fmt.Errorf("upstream HTTP %d", resp.StatusCode)}
	}

	// Status transien (429/5xx): cooldown key, coba key berikutnya.
	if !u.singleTry && service.IsRetryable(resp.StatusCode, nil) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		cancel()
		u.s.st.MarkCooldown(keyID, 60*time.Second)
		u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, ttft.Milliseconds(), true)
		u.lg.Warn("up<", "provider", u.ref.ProviderSlug, "key", ke.Name, "status", resp.StatusCode, "retry", true)
		return attemptOutcome{kind: attemptUpstreamRetry}
	}

	// Teruskan respons apa adanya (streaming atau buffered).
	u.serve(resp, ke, keyID, ttft, t0)
	cancel()
	return attemptOutcome{kind: attemptServed}
}

// serve meneruskan respons upstream ke client sambil mencatat usage/health.
func (u *upstreamRun) serve(resp *http.Response, ke *config.KeyEntry, keyID string, ttft time.Duration, t0 time.Time) {
	sse := u.stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if sse {
		u.w.Header().Set("Content-Type", "text/event-stream")
		u.w.Header().Set("Cache-Control", "no-cache")
		u.w.Header().Set("Connection", "keep-alive")
		u.w.Header().Set("X-Accel-Buffering", "no")
	} else if ct := resp.Header.Get("Content-Type"); ct != "" {
		u.w.Header().Set("Content-Type", ct)
	}
	u.w.WriteHeader(resp.StatusCode)
	cap := newCapture(4 << 20) // simpan ekor s/d 4MB untuk ekstrak usage
	var n int64
	if sse {
		if fl, ok := u.w.(http.Flusher); ok {
			fl.Flush()
			n, _ = io.Copy(flushWriter{w: io.MultiWriter(u.w, cap), f: fl}, resp.Body)
		} else {
			n, _ = io.Copy(io.MultiWriter(u.w, cap), resp.Body)
		}
	} else {
		n, _ = io.Copy(io.MultiWriter(u.w, cap), resp.Body)
	}
	resp.Body.Close()
	el := time.Since(t0)
	ok2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
	u.s.st.Record(u.ref.ProviderSlug, u.ref.FullID, el.Milliseconds(), !ok2xx)
	if ok2xx && u.s.st.Usage != nil && u.callerHash != "" {
		// Add selalu dipanggil (walau toks==0) agar waktu terakhir request
		// tercatat; tokens hanya diakumulasi bila ada.
		u.s.st.Usage.Add(u.callerHash, u.callerName, extractTotalTokens(cap.Bytes(), sse))
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
