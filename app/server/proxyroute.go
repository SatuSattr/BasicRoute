package server

import (
	"strings"
	"time"

	"basicroute/app/config"
	"basicroute/app/service"
)

// proxyRetestInterval adalah jeda minimum antar silent re-test per pool proxy.
const proxyRetestInterval = 60 * time.Second

// proxyCandidates membangun daftar target proxy untuk satu provider:
//   - ResolveProxies memberi urutan deterministik (direct / nama pool / auto / round)
//   - PARTISI dulu: pool yang sudah di-flag error diturunkan prioritasnya:
//     bila masih ada pool sehat, yang flagged di-skip; bila semua flagged,
//     semuanya dipakai (last-resort, anti-outage) — sama seperti aturan key.
//   - ROTASI kemudian: mode "round" merotasi titik awal (round robin) HANYA
//     di subset sehat memakai kursor per provider, sehingga pool flagged
//     tidak menggeser rotasi pool sehat.
//
// Catatan: round robin KEY tidak ditangani di sini melainkan di
// server.go:handleProxy (kursor namespace "keys:"+slug, aktif hanya bila
// provider mengimplementasikan IsKeyRound() bool).
func (s *Server) proxyCandidates(snap *config.Snapshot, slug string, prov *config.Provider) ([]service.ProxyTarget, error) {
	targets, err := service.ResolveProxies(snap, prov)
	if err != nil {
		return nil, err
	}
	// Partisi dulu: sehat vs flagged (verbatim aturan prioritas lama).
	var healthy, flagged []service.ProxyTarget
	if len(targets) > 1 && s.st.ProxyHealth != nil {
		for _, t := range targets {
			if t.Name != "" && s.st.ProxyHealth.IsError(t.Name) {
				flagged = append(flagged, t)
				continue
			}
			healthy = append(healthy, t)
		}
	} else {
		healthy = targets
	}
	// Rotasi hanya subset sehat agar round tidak pincang saat ada flagged.
	if _, isRound := service.ProxyMode(prov.Proxy); isRound {
		if n := len(healthy); n > 1 {
			start := s.st.NextRoundRobin(slug, n)
			healthy = append(append([]service.ProxyTarget{}, healthy[start:]...), healthy[:start]...)
		}
	}
	// Sehat dulu + flagged di belakang; bila tak ada yang sehat (last-resort)
	// pakai urutan apa adanya.
	if len(healthy) > 0 {
		targets = append(healthy, flagged...)
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
//
// Probe provider-spesifik: pool diuji terhadap upstream provider yang
// memakainya (service.TestProvKey memakai sample key enabled pertama),
// bukan hanya konektivitas generik ke ProxyTestURL/Google-204 (false-green:
// pool→Google OK tapi pool→upstream asli gagal). Bila probe provider gagal,
// fallback ke TestProxy generik; hanya bila KEDUANYA gagal pool dinyatakan
// masih gagal. Bila salah satu sukses pool dinyatakan pulih (recovered).
func (s *Server) maybeRetestProxies(snap *config.Snapshot, prov *config.Provider, keys []*config.KeyEntry) {
	if s == nil || s.st == nil || s.st.ProxyHealth == nil || snap == nil || prov == nil {
		return
	}
	provTargets, err := service.ResolveProxies(snap, prov)
	if err != nil {
		provTargets = nil
	}
	targets := retestTargets(provTargets, snap, keys)
	var flagged []service.ProxyTarget
	seen := map[string]bool{}
	for _, t := range targets {
		if t.Name == "" {
			continue
		}
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		if s.st.ProxyHealth.IsError(t.Name) {
			flagged = append(flagged, t)
		}
	}
	if len(flagged) == 0 {
		return
	}
	testURL := snap.Raw.System.ProxyTestURL
	timeout := service.TestTimeout(15)
	sampleKey := sampleProvKey(prov)
	for _, t := range flagged {
		s.retestProxy(t, prov, sampleKey, testURL, timeout)
	}
}

// sampleProvKey mengambil satu key enabled pertama provider sebagai bahan
// probe provider-spesifik. "" bila tak ada (probe jatuh ke generik saja).
func sampleProvKey(prov *config.Provider) string {
	if prov == nil {
		return ""
	}
	for _, k := range prov.Keys {
		if k != nil && bool(k.Enabled) && strings.TrimSpace(k.Key) != "" {
			return k.Key
		}
	}
	return ""
}

// retestProxy menjalankan satu silent re-test bila guard mengizinkan:
// pertama probe provider-spesifik (TestProvKey ke baseURL provider lewat
// pool), gagal -> fallback TestProxy generik. Salah satu sukses = pulih.
func (s *Server) retestProxy(t service.ProxyTarget, prov *config.Provider, sampleKey, testURL string, timeout time.Duration) {
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
		// Probe utama provider-spesifik (pool -> upstream asli). Key
		// invalid (401/403) ikut gagal di sini, jadi kegagalannya TIDAK
		// dipakai menahan flag: fallback generik yang memutuskan.
		baseURL, provType := "", ""
		if prov != nil {
			baseURL = prov.BaseURL
			provType = prov.Type
		}
		if sampleKey != "" {
			if err := service.TestProvKey(baseURL, provType, typ, raw, sampleKey, timeout); err == nil {
				s.st.ProxyHealth.SetOK(name)
				s.st.NoteProxyResult(name, true)
				s.log.Info("silent proxy retest recovered", "proxy", name, "via", "provider")
				return
			}
		}
		if err := service.TestProxy(typ, raw, testURL, timeout); err != nil {
			s.log.Warn("silent proxy retest still failing", "proxy", name, "err", err.Error())
			return
		}
		s.st.ProxyHealth.SetOK(name)
		s.st.NoteProxyResult(name, true)
		s.log.Info("silent proxy retest recovered", "proxy", name, "via", "generic")
	}()
}
