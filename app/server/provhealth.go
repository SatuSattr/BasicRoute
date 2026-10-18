package server

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"basicroute/app/config"
	"basicroute/app/service"
)

// provRetestInterval adalah jeda minimum antar silent re-test per provider.
const provRetestInterval = 60 * time.Second

// noteUpstreamFailure mencatat response upstream non-2xx ke health:
// 401/403 = key tidak valid -> flag key; 429 = rate-limit transien dan
// 4xx lain (kecuali 404) = kemungkinan salah requester -> diabaikan;
// 5xx dan 404 (model tidak ada) -> counter consecutive model, capai
// ambang -> auto-flag model. Model yang sudah flagged tidak dihitung ulang.
func (s *Server) noteUpstreamFailure(lg *slog.Logger, ref *config.ModelRef, modelID, keyID, keyName string, status int) {
	if s == nil || s.st == nil || ref == nil {
		return
	}
	switch {
	case status == 401 || status == 403:
		msg := fmt.Sprintf("upstream HTTP %d for model %q", status, ref.ModelSlug)
		s.st.ProvHealth.SetError(keyID, msg)
		s.log.Warn("key flagged invalid", "provider", ref.ProviderSlug, "key", keyName, "status", status)
		return
	case status == 404 || status >= 500:
		// 404 = model tidak ada; 5xx = upstream rusak. Keduanya
		// indikasi masalah model → masuk hitungan.
	default:
		// 3xx, 429 rate-limit, dan 4xx lain (bad request dari caller)
		// bukan indikasi model rusak — jangan dihitung.
		return
	}
	n := s.st.NoteModelResult(ref.FullID, false, fmt.Sprintf("upstream HTTP %d", status))
	if n >= service.ModelAutoFlagThreshold && !s.st.ProvHealth.IsError(modelID) {
		msg := fmt.Sprintf("auto-flagged after %d consecutive errors (last: upstream HTTP %d for model %q)", n, status, ref.ModelSlug)
		s.st.ProvHealth.SetError(modelID, msg)
		lg.Warn("model auto-flagged", "provider", ref.ProviderSlug, "model", ref.ModelSlug, "status", status, "consecutive", n)
	}
}

// maybeRetestKeys memicu silent re-test (background) untuk key provider yang
// sedang flagged error: bila pulih, flag dihapus sehingga key bisa dipakai
// lagi di request berikutnya. Max satu goroutine per provider dan
// max sekali per provRetestInterval. Tidak mengubah response yang berjalan.
func (s *Server) maybeRetestKeys(snap *config.Snapshot, slug string, prov *config.Provider) {
	if s == nil || s.st == nil || s.st.ProvHealth == nil || snap == nil || prov == nil {
		return
	}
	if strings.TrimSpace(slug) == "" {
		return
	}
	type kk struct{ name, val string }
	var flagged []kk
	for n, k := range prov.Keys {
		if k == nil || !bool(k.Enabled) {
			continue
		}
		if s.st.ProvHealth.IsError(service.ProvKeyID(slug, n)) {
			flagged = append(flagged, kk{name: n, val: k.Key})
		}
	}
	if len(flagged) == 0 {
		return
	}
	sort.Slice(flagged, func(i, j int) bool { return flagged[i].name < flagged[j].name })

	s.retestMu.Lock()
	if s.retestRun[slug] || time.Since(s.retestLast[slug]) < provRetestInterval {
		s.retestMu.Unlock()
		return
	}
	s.retestRun[slug] = true
	s.retestLast[slug] = time.Now()
	s.retestMu.Unlock()

	ptype, purl, perr := service.ResolveProxy(snap, prov)
	base := strings.TrimSpace(prov.BaseURL)
	timeout := service.TestTimeout(int(prov.TimeoutSecs))
	go func() {
		defer func() {
			s.retestMu.Lock()
			s.retestRun[slug] = false
			s.retestMu.Unlock()
		}()
		if perr != nil {
			s.log.Warn("silent key retest skipped", "provider", slug, "err", perr.Error())
			return
		}
		for _, k := range flagged {
			if rerr := service.TestProvKey(base, prov.Type, ptype, purl, k.val, timeout); rerr != nil {
				s.log.Warn("silent key retest still failing", "provider", slug, "key", k.name, "err", rerr.Error())
				continue
			}
			s.st.ProvHealth.SetOK(service.ProvKeyID(slug, k.name))
			s.log.Info("silent key retest recovered", "provider", slug, "key", k.name)
		}
	}()
}
