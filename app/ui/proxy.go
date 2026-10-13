package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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
	active  bool   // pool enabled (hijau) atau tidak (abu-abu)
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
		out = append(out, proxyCard{
			name:    n,
			clients: used[n],
			active:  bool(pp.Enabled),
		})
	}
	return out
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
func (m Model) renderProxyCard(c proxyCard, sel bool) string {
	w := proxyCardW - 4 // isi: border 2 + padding 2

	ns := styleCardName
	ds := styleDotOn
	if !c.active {
		ds = styleDotOff
		if !sel {
			ns = styleCardNameOD
		}
	}
	if sel {
		ns = styleCardNameSl
	}
	l1 := dline{[]dseg{{"• ", ds}, {c.name, ns}}}.render(w)
	l2 := dline{[]dseg{
		{"Clients: ", styleCardLbl},
		{fmt.Sprint(c.clients), styleCardVal},
	}}.render(w)

	body := strings.Join([]string{l1, l2}, "\n")
	st := styleCard.Width(proxyCardW - 2)
	if sel {
		st = styleCardSel.Width(proxyCardW - 2)
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
				parts = append(parts, m.renderProxyCard(cards[idx], idx == f.cursor))
			}
			body = append(body, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, parts...), "\n")...)
			if r < end-1 {
				body = append(body, "")
			}
		}
		if f.cursor >= 0 && f.cursor < len(cards) {
			sel := cards[f.cursor]
			body = append(body, "")
			body = append(body, styleFooter.Render("▸ "+sel.name+"  ·  Clients: "+fmt.Sprint(sel.clients)))
		}
	}

	for len(body) < vp {
		body = append(body, "")
	}
	return styleWin.Width(m.width - 2).Render(strings.Join(body, "\n")) + "\n"
}
