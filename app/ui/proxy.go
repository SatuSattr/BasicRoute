package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"basicroute/app/cli"
	"basicroute/app/config"
)

// proxyTypeLabel menormalkan tipe pool untuk tampilan ("http"/"relay").
func proxyTypeLabel(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), config.ProxyTypeRelay) {
		return config.ProxyTypeRelay
	}
	return config.ProxyTypeHTTP
}

// ---- daftar proxy sebagai tabel (seperti halaman Api Key) ----

// proxyCard adalah ringkasan satu proxy pool untuk satu baris tabel.
type proxyCard struct {
	name    string // nama pool
	order   int    // urutan pemakaian mode auto/round
	clients int    // jumlah provider yang memakai pool ini
	enabled bool   // pool enabled
	err     bool   // ditandai error dari hasil test (memory)
	testing bool   // sedang di-test (memory, indikator kuning)
	typ     string // tipe pool: "http" (forward) atau "relay"
	url     string // URL termasking (password disamarkan)
}

// proxyCards membaca ringkasan pool live dari Store, terurut urutan pemakaian.
func (m Model) proxyCards() []proxyCard {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	used := map[string]int{}
	for _, p := range snap.Raw.Providers {
		if p == nil {
			continue
		}
		if name := strings.TrimSpace(p.Proxy); name != "" && !config.IsProxyMode(name) {
			used[name]++
		}
	}
	out := make([]proxyCard, 0, len(snap.Raw.ProxyPools))
	for _, n := range m.poolNames() {
		pp := snap.Raw.ProxyPools[n]
		if pp == nil {
			continue
		}
		errFlag := false
		testing := false
		if m.deps.Store.ProxyHealth != nil {
			errFlag = m.deps.Store.ProxyHealth.IsError(n)
			testing = m.deps.Store.ProxyHealth.IsTesting(n)
		}
		out = append(out, proxyCard{
			name:    n,
			order:   int(pp.Order),
			clients: used[n],
			enabled: bool(pp.Enabled),
			err:     errFlag,
			testing: testing,
			typ:     proxyTypeLabel(pp.Type),
			url:     cli.MaskProxyURL(strings.TrimSpace(pp.URL)),
		})
	}
	return out
}

// proxyDot memilih style dot: kuning (sedang di-test), abu (disabled),
// merah (error), hijau (sehat).
func proxyDot(c proxyCard) lipgloss.Style {
	switch {
	case c.testing:
		return styleDotTest
	case !c.enabled:
		return styleDotOff
	case c.err:
		return styleDotErr
	default:
		return styleDotOn
	}
}

// proxiesTableView: tabel NAME | TYPE | ORDER | CLIENTS | URL ala halaman Api Key.
// Header fixed, body di-scroll. Baris cursor + multi-select di-highlight
// (dot hijau/abu/merah tetap terlihat). Seleksi model anchor Explorer.
func (m Model) proxiesTableView(f frame) string {
	inner := m.innerW()
	vp := m.viewportH()
	cards := m.proxyCards()
	if len(cards) == 0 {
		body := []string{styleBody.Render("(empty — all providers direct)")}
		for len(body) < vp {
			body = append(body, "")
		}
		return styleWin.Width(m.width-2).Render(strings.Join(body, "\n")) + "\n"
	}
	nW, tW, oW, cW, uW := runeLen("NAME"), runeLen("TYPE"), runeLen("ORDER"), runeLen("CLIENTS"), runeLen("URL")
	for _, c := range cards {
		nW = max(nW, runeLen(c.name))
		tW = max(tW, runeLen(c.typ))
		oW = max(oW, runeLen(fmt.Sprint(c.order)))
		cW = max(cW, runeLen(fmt.Sprint(c.clients)))
		uW = max(uW, runeLen(c.url))
	}
	nW += 2
	tW += 2
	oW += 2
	cW += 2

	header := dline{[]dseg{
		{padRight("NAME", nW+2) + padRight("TYPE", tW) + padRight("ORDER", oW) + padRight("CLIENTS", cW) + "URL", styleDetLbl},
	}}.render(inner)

	// Body scroll atas data saja (header fixed).
	start := clamp(f.scroll, 0, maxScroll(len(cards), vp-1))
	end := start + (vp - 1)
	if end > len(cards) {
		end = len(cards)
	}
	vis := make([]string, 0, vp)
	vis = append(vis, header)
	for idx := start; idx < end; idx++ {
		c := cards[idx]
		ds := proxyDot(c)
		plain := "• " + padRight(c.name, nW) + padRight(c.typ, tW) + padRight(fmt.Sprint(c.order), oW) + padRight(fmt.Sprint(c.clients), cW) + c.url
		plain = trunc(plain, inner)
		if idx == f.cursor || f.sel[idx] {
			vis = append(vis, ds.Render("• ")+styleItemSel.Width(inner-2).Render(strings.TrimPrefix(plain, "• ")))
			continue
		}
		vis = append(vis, dline{[]dseg{
			{"• ", ds},
			{padRight(c.name, nW), styleDetVal},
			{padRight(c.typ, tW), styleDetVal},
			{padRight(fmt.Sprint(c.order), oW), styleDetVal},
			{padRight(fmt.Sprint(c.clients), cW), styleDetVal},
			{c.url, styleDetVal},
		}}.render(inner))
	}
	if nsel := countSel(f.sel); nsel > 0 {
		vis = append(vis, styleFooter.Render(fmt.Sprintf("%d proxy selected", nsel)))
	}
	for len(vis) < vp {
		vis = append(vis, "")
	}
	return styleWin.Width(m.width-2).Render(strings.Join(vis, "\n")) + "\n"
}

// ensureProxyVisible menggeser scroll tabel agar cursor terlihat.
func (m *Model) ensureProxyVisible() {
	f := m.cur()
	if f.pg != pgProxies {
		return
	}
	n := len(m.proxyCards())
	if n == 0 {
		return
	}
	vp := m.viewportH() - 1
	if vp < 1 {
		vp = 1
	}
	if f.cursor < f.scroll {
		f.scroll = f.cursor
	} else if f.cursor >= f.scroll+vp {
		f.scroll = f.cursor - vp + 1
	}
	m.setCur(f)
}

// countSel menghitung jumlah kartu yang terseleksi (multi-select).
func countSel(sel map[int]bool) int {
	n := 0
	for _, v := range sel {
		if v {
			n++
		}
	}
	return n
}

// selectedProxyNames mengumpulkan nama pool terseleksi (terurut index) yang
// enabled, plus jumlah yang di-skip karena disabled. Bila tak ada seleksi,
// fallback ke kartu di bawah cursor.
func selectedProxyNames(cards []proxyCard, f frame) (names []string, skipped int) {
	if len(cards) == 0 {
		return nil, 0
	}
	if len(f.sel) == 0 {
		if f.cursor >= 0 && f.cursor < len(cards) {
			if cards[f.cursor].enabled {
				return []string{cards[f.cursor].name}, 0
			}
			return nil, 1
		}
		return nil, 0
	}
	for i := 0; i < len(cards); i++ {
		if !f.sel[i] {
			continue
		}
		if cards[i].enabled {
			names = append(names, cards[i].name)
		} else {
			skipped++
		}
	}
	return names, skipped
}
