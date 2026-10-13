package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"basicroute/app/config"
)

// ProxyTarget adalah satu kandidat proxy untuk satu request provider.
// Name kosong berarti koneksi langsung (direct) tanpa pool.
type ProxyTarget struct {
	Name string
	Type string
	URL  string
}

// ProxyMode menormalkan mode proxy ("auto"/"round"). ok=false bila nilai
// tersebut bukan mode (berarti nama pool biasa atau direct).
func ProxyMode(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case config.ProxyModeAuto:
		return config.ProxyModeAuto, true
	case config.ProxyModeRound:
		return config.ProxyModeRound, true
	}
	return "", false
}

// ResolveProxies mengembalikan daftar kandidat proxy terurut untuk provider:
//   - proxy kosong   -> satu target direct (Name "")
//   - nama pool      -> pool itu saja (harus ada dan enabled)
//   - "auto"/"round" -> semua pool enabled terurut Snapshot.ProxyOrder
//
// Mode round robin (rotasi urutan awal) diterapkan pemanggil (server) karena
// butuh state counter; di sini urutannya selalu deterministik.
func ResolveProxies(snap *config.Snapshot, prov *config.Provider) ([]ProxyTarget, error) {
	if prov == nil || strings.TrimSpace(prov.Proxy) == "" {
		return []ProxyTarget{{}}, nil
	}
	if snap == nil || snap.Raw == nil {
		return nil, fmt.Errorf("proxy %q: snapshot not ready", prov.Proxy)
	}
	name := strings.TrimSpace(prov.Proxy)
	if mode, ok := ProxyMode(name); ok {
		out := make([]ProxyTarget, 0, len(snap.ProxyOrder))
		for _, n := range snap.ProxyOrder {
			p := snap.Raw.ProxyPools[n]
			if p == nil || !bool(p.Enabled) {
				continue
			}
			out = append(out, ProxyTarget{Name: n, Type: p.Type, URL: strings.TrimSpace(p.URL)})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no enabled proxy pools (mode %s)", mode)
		}
		return out, nil
	}
	p, ok := snap.Raw.ProxyPools[name]
	if !ok || p == nil {
		return nil, fmt.Errorf("proxy pool %q not found", name)
	}
	if !bool(p.Enabled) {
		return nil, fmt.Errorf("proxy pool %q disabled", name)
	}
	return []ProxyTarget{{Name: name, Type: p.Type, URL: strings.TrimSpace(p.URL)}}, nil
}

// ResolveKeyProxies mengembalikan kandidat proxy efektif untuk satu key:
//   - ke == nil atau ke.Proxy == nil -> pakai proxy provider (inherited)
//   - ke.Proxy == ""                 -> direct
//   - ke.Proxy == nama pool          -> pool itu saja
//
// noFallback=true berarti key dipatok ke proxy sendiri (direct/pool): bila
// koneksi proxy itu mati, request tidak boleh dilanjutkan ke kandidat lain.
func ResolveKeyProxies(snap *config.Snapshot, ke *config.KeyEntry, inherited []ProxyTarget) ([]ProxyTarget, bool, error) {
	if ke == nil || ke.Proxy == nil {
		return inherited, false, nil
	}
	name := strings.TrimSpace(*ke.Proxy)
	if name == "" {
		return []ProxyTarget{{}}, true, nil
	}
	if snap == nil || snap.Raw == nil {
		return nil, true, fmt.Errorf("key proxy %q: snapshot not ready", name)
	}
	p := snap.Raw.ProxyPools[name]
	if p == nil {
		return nil, true, fmt.Errorf("key proxy pool %q not found", name)
	}
	if !bool(p.Enabled) {
		return nil, true, fmt.Errorf("key proxy pool %q disabled", name)
	}
	return []ProxyTarget{{Name: name, Type: p.Type, URL: strings.TrimSpace(p.URL)}}, true, nil
}

// KeyProxyLabel merangkum proxy efektif satu key untuk tampilan tabel:
// "direct", nama pool, atau label provider (singkat) bila key mengikuti
// provider. inherited=true berarti nilainya diwarisi dari provider.
func KeyProxyLabel(snap *config.Snapshot, prov *config.Provider, proxy *string) (label string, inherited bool) {
	if proxy == nil {
		return ProxyModeShort(snap, prov), true
	}
	if v := strings.TrimSpace(*proxy); v != "" {
		return v, false
	}
	return "direct", false
}

// IsProxyFailure melaporkan apakah satu kegagalan percobaan menandakan
// masalah proxy (transport error apa pun, atau HTTP 407 Proxy Auth Required),
// bukan masalah upstream. Context.Canceled dikecualikan karena berarti
// pemanggil pergi, bukan proxy rusak.
func IsProxyFailure(status int, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false
		}
		return true
	}
	return status == http.StatusProxyAuthRequired
}

// ProxyModeLabel merangkum nilai Provider.Proxy untuk tampilan:
// "direct", nama pool, atau "<mode> (n): a → b" untuk mode auto/round.
// Daftar pool mengikuti urutan pemakaian (order), bukan alfabetis.
func ProxyModeLabel(snap *config.Snapshot, prov *config.Provider) string {
	if prov == nil {
		return "direct"
	}
	name := strings.TrimSpace(prov.Proxy)
	if name == "" {
		return "direct"
	}
	mode, isMode := ProxyMode(name)
	if !isMode {
		return name
	}
	targets, err := ResolveProxies(snap, prov)
	if err != nil || len(targets) == 0 {
		return mode
	}
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	return mode + " (" + itoa(len(names)) + "): " + strings.Join(names, " → ")
}

// ProxyModeShort merangkum nilai Provider.Proxy secara ringkas:
// "direct", nama pool, atau "<mode> (n)".
func ProxyModeShort(snap *config.Snapshot, prov *config.Provider) string {
	if prov == nil {
		return "direct"
	}
	name := strings.TrimSpace(prov.Proxy)
	if name == "" {
		return "direct"
	}
	mode, isMode := ProxyMode(name)
	if !isMode {
		return name
	}
	targets, err := ResolveProxies(snap, prov)
	if err != nil || len(targets) == 0 {
		return mode
	}
	return mode + " (" + itoa(len(targets)) + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
