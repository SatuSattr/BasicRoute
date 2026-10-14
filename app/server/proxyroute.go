package server

import (
	"time"

	"basicroute/app/config"
	"basicroute/app/service"
)

// proxyRetestInterval adalah jeda minimum antar silent re-test per pool proxy.
const proxyRetestInterval = 60 * time.Second

// proxyCandidates membangun daftar target proxy untuk satu provider:
//   - ResolveProxies memberi urutan deterministik (direct / nama pool / auto / round)
//   - mode "round" merotasi titik awal (round robin) memakai kursor per provider
//   - pool yang sudah di-flag error diturunkan prioritasnya: bila masih ada
//     pool sehat, yang flagged di-skip; bila semua flagged, semuanya dipakai
//     (last-resort, anti-outage) — sama seperti aturan key.
//
// Catatan: round robin KEY tidak ditangani di sini melainkan di
// server.go:handleProxy (kursor namespace "keys:"+slug, aktif hanya bila
// provider mengimplementasikan IsKeyRound() bool). File ini sengaja tidak
// mengubah logic proxy (no-op aman untuk workstream driver).
func (s *Server) proxyCandidates(snap *config.Snapshot, slug string, prov *config.Provider) ([]service.ProxyTarget, error) {
	targets, err := service.ResolveProxies(snap, prov)
	if err != nil {
		return nil, err
	}
	if _, isRound := service.ProxyMode(prov.Proxy); isRound {
		if n := len(targets); n > 1 {
			start := s.st.NextRoundRobin(slug, n)
			targets = append(append([]service.ProxyTarget{}, targets[start:]...), targets[:start]...)
		}
	}
	// Prioritas: pool sehat dulu; flagged di belakang. Urutan relatif tetap.
	if len(targets) > 1 && s.st.ProxyHealth != nil {
		var healthy, flagged []service.ProxyTarget
		for _, t := range targets {
			if t.Name != "" && s.st.ProxyHealth.IsError(t.Name) {
				flagged = append(flagged, t)
				continue
			}
			healthy = append(healthy, t)
		}
		if len(healthy) > 0 {
			targets = append(healthy, flagged...)
		}
	}
	return targets, nil
}

// retestTargets mengumpulkan kandidat proxy untuk silent re-test: proxy
// provider plus proxy spesifik yang dipakai key (dedup tidak perlu, guard
// per-pool di retestProxy sudah menahan duplikasi).
func retestTargets(provTargets []service.ProxyTarget, snap *config.Snapshot, keys []*config.KeyEntry) []service.ProxyTarget {
	out := append([]service.ProxyTarget{}, provTargets...)
	for _, ke := range keys {
		tg, _, err := service.ResolveKeyProxies(snap, ke, nil)
		if err != nil {
			continue
		}
		out = append(out, tg...)
	}
	return out
}

// proxyFailMsg merangkum kegagalan pool proxy untuk respons error.
func proxyFailMsg(t service.ProxyTarget, err error) string {
	label := t.Name
	if label == "" {
		label = "direct"
	}
	if err != nil {
		return "proxy " + label + " failed: " + err.Error()
	}
	return "proxy " + label + " failed"
}

// maybeRetestProxies memicu silent re-test (background) untuk pool proxy yang
// sedang flagged error: bila pulih, flag dihapus sehingga pool bisa dipakai
// lagi. Max satu goroutine per pool dan max sekali per proxyRetestInterval.
// Tidak mengubah response yang sedang berjalan.
func (s *Server) maybeRetestProxies(snap *config.Snapshot, targets []service.ProxyTarget) {
	if s == nil || s.st == nil || s.st.ProxyHealth == nil || snap == nil {
		return
	}
	var flagged []service.ProxyTarget
	for _, t := range targets {
		if t.Name == "" {
			continue
		}
		if s.st.ProxyHealth.IsError(t.Name) {
			flagged = append(flagged, t)
		}
	}
	if len(flagged) == 0 {
		return
	}
	testURL := snap.Raw.System.ProxyTestURL
	timeout := service.TestTimeout(15)
	for _, t := range flagged {
		s.retestProxy(t, testURL, timeout)
	}
}

// retestProxy menjalankan satu silent re-test bila guard mengizinkan.
func (s *Server) retestProxy(t service.ProxyTarget, testURL string, timeout time.Duration) {
	name := t.Name
	s.retestMu.Lock()
	if s.proxyRetestRun[name] || time.Since(s.proxyRetestLast[name]) < proxyRetestInterval {
		s.retestMu.Unlock()
		return
	}
	s.proxyRetestRun[name] = true
	s.proxyRetestLast[name] = time.Now()
	s.retestMu.Unlock()

	typ, raw := t.Type, t.URL
	go func() {
		defer func() {
			s.retestMu.Lock()
			s.proxyRetestRun[name] = false
			s.retestMu.Unlock()
		}()
		if err := service.TestProxy(typ, raw, testURL, timeout); err != nil {
			s.log.Warn("silent proxy retest still failing", "proxy", name, "err", err.Error())
			return
		}
		s.st.ProxyHealth.SetOK(name)
		s.st.NoteProxyResult(name, true)
		s.log.Info("silent proxy retest recovered", "proxy", name)
	}()
}
