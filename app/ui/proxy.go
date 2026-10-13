package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"basicroute/app/config"
)

// proxyTypeLabel menormalkan tipe pool untuk tampilan ("http"/"relay").
func proxyTypeLabel(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), config.ProxyTypeRelay) {
		return config.ProxyTypeRelay
	}
	return config.ProxyTypeHTTP
}

// ---- daftar proxy sebagai kartu (grid responsif, mirip provider) ----

const (
	proxyCardW   = 26 // lebar total kartu, termasuk border
	proxyCardGap = 2  // spasi antar kartu
	proxyCardH   = 4  // tinggi kartu: 2 baris isi + 2 border
)

// proxyCard adalah ringkasan satu proxy pool untuk kartu.
type proxyCard struct {
	name    string // nama pool
	clients int    // jumlah provider yang memakai pool ini
	enabled bool   // pool enabled
	err     bool   // ditandai error dari hasil test (memory)
	typ     string // tipe pool: "http" (forward) atau "relay"
}

// proxyCards membaca ringkasan pool live dari Store, terurut nama.
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
		if name := strings.TrimSpace(p.Proxy); name != "" {
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
		if m.deps.Store.ProxyHealth != nil {
			errFlag = m.deps.Store.ProxyHealth.IsError(n)
		}
		out = append(out, proxyCard{
			name:    n,
			clients: used[n],
			enabled: bool(pp.Enabled),
			err:     errFlag,
			typ:     proxyTypeLabel(pp.Type),
		})
	}
	return out
}

// proxyDot memilih style dot: abu (disabled), merah (error), hijau (sehat).
func proxyDot(c proxyCard) lipgloss.Style {
	switch {
	case !c.enabled:
		return styleDotOff
	case c.err:
		return styleDotErr
	default:
		return styleDotOn
	}
}

// proxyCols menghitung jumlah kolom dari lebar terminal (responsif).
func (m Model) proxyCols(n int) int {
	if n <= 0 {
		return 1
	}
	cols := (m.innerW() + proxyCardGap) / (proxyCardW + proxyCardGap)
	if cols < 1 {
		cols = 1
	}
	if cols > n {
		cols = n
	}
	return cols
}

// proxyVisRows: baris kartu yang muat di viewport (sisakan 2 baris info).
func (m Model) proxyVisRows() int {
	vr := (m.viewportH() - 1) / (proxyCardH + 1)
	if vr < 1 {
		vr = 1
	}
	return vr
}

// renderProxyCard menggambar satu kartu (lebar tetap proxyCardW).
// sel = kartu terpilih sebagai cursor (nama underline), picked = ikut terseleksi.
func (m Model) renderProxyCard(c proxyCard, sel, picked bool) string {
	w := proxyCardW - 4 // isi: border 2 + padding 2

	ns := styleCardName
	if !c.enabled && !sel {
		ns = styleCardNameOD
	}
	if sel {
		ns = styleCardNameSl
	}
	ds := proxyDot(c)
	l1 := dline{[]dseg{{"• ", ds}, {c.name, ns}}}.render(w)
	l2 := dline{[]dseg{
		{"Clients: ", styleCardLbl},
		{fmt.Sprint(c.clients), styleCardVal},
	}}.render(w)

	body := strings.Join([]string{l1, l2}, "\n")
	st := styleCard.Width(proxyCardW - 2)
	switch {
	case sel:
		st = styleCardSel.Width(proxyCardW - 2)
	case picked:
		st = styleCardSel2.Width(proxyCardW - 2)
	}
	_ = m
	return st.Render(body)
}

// proxiesGridView: kartu-kartu dalam window, navigasi 2D.
func (m Model) proxiesGridView(f frame) string {
	cards := m.proxyCards()
	vp := m.viewportH()
	var body []string

	if len(cards) == 0 {
		body = []string{styleBody.Render("(empty — all providers direct)")}
	} else {
		cols := m.proxyCols(len(cards))
		rows := provRows(len(cards), cols)
		vis := m.proxyVisRows()
		start := clamp(f.scroll, 0, imax(rows-vis, 0))
		end := start + vis
		if end > rows {
			end = rows
		}
		gap := strings.Repeat(" ", proxyCardGap)
		for r := start; r < end; r++ {
			var parts []string
			for c := 0; c < cols; c++ {
				idx := r*cols + c
				if idx >= len(cards) {
					break
				}
				if len(parts) > 0 {
					parts = append(parts, gap)
				}
				parts = append(parts, m.renderProxyCard(cards[idx], idx == f.cursor, f.sel[idx]))
			}
			body = append(body, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, parts...), "\n")...)
			if r < end-1 {
				body = append(body, "")
			}
		}
		if f.cursor >= 0 && f.cursor < len(cards) {
			sel := cards[f.cursor]
			info := "▸ " + sel.name + "  ·  " + sel.typ + "  ·  Clients: " + fmt.Sprint(sel.clients)
			switch {
			case !sel.enabled:
				info += "  ·  disabled"
			case sel.err:
				info += "  ·  error"
			default:
				info += "  ·  ok"
			}
			body = append(body, "")
			body = append(body, styleFooter.Render(info))
		}
	}

	for len(body) < vp {
		body = append(body, "")
	}
	return styleWin.Width(m.width - 2).Render(strings.Join(body, "\n")) + "\n"
}

// moveProxyCursor memindahkan cursor grid ala navigasi kartu (wrap-around).
func moveProxyCursor(cursor int, key string, n, cols int) int {
	if n <= 0 {
		return 0
	}
	switch key {
	case "left", "h":
		return (cursor + n - 1) % n
	case "right", "l":
		return (cursor + 1) % n
	case "up", "k":
		c := cursor
		if c-cols >= 0 {
			c -= cols
		} else {
			for c+cols < n {
				c += cols
			}
		}
		return c
	case "down", "j":
		c := cursor
		if c+cols < n {
			c += cols
		} else {
			c = c % cols
			if c >= n {
				c = n - 1
			}
		}
		return c
	}
	return cursor
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
