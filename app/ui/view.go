package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"basicroute/app/cli"
	"basicroute/app/service"
)

func sysKeyHash(raw string) string { return service.HashSysKey(raw) }

// chromeLines adalah jumlah baris tetap di luar konten:
// header(3) + blank(2) + judul halaman(1) + blank(1) + border window(2)
// + command(1) + status(1) + footer(1) = 12.
// Baris command hanya ada saat cmdMode; viewportH() mengkompensasinya.
const chromeLines = 12

// viewportH adalah tinggi area konten agar layout tidak melompat.
func (m Model) viewportH() int {
	h := m.height - chromeLines
	if !m.cmdMode {
		h++ // baris perintah disembunyikan, konten memakai jatahnya
	}
	if h < 3 {
		return 3
	}
	return h
}

// innerW adalah lebar teks di dalam window (border 2 + padding 2x2).
func (m Model) innerW() int {
	w := m.width - 6
	if w < 10 {
		return 10
	}
	return w
}

// trunc memotong string per rune dengan elipsis.
func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// View merakit seluruh layar: header, halaman, command, status, footer.
func (m Model) View() string {
	if m.width == 0 {
		return "loading UI…"
	}
	var sb strings.Builder
	sb.WriteString(m.headerView())
	sb.WriteString("\n")
	f := m.cur()
	switch f.pg {
	case pgHome:
		sb.WriteString(m.homeView())
		sb.WriteString("\n")
	case pgProviders, pgProxies:
		sb.WriteString("\n")
		sb.WriteString(stylePageTitle.Render(m.pageTitle(f)))
		if ind := m.scrollIndicator(f); ind != "" {
			sb.WriteString(" " + styleFooter.Render(ind))
		}
		sb.WriteString("\n\n")
		if f.pg == pgProviders {
			sb.WriteString(m.providersGridView(f))
		} else {
			sb.WriteString(m.proxiesGridView(f))
		}
	case pgProvider:
		sb.WriteString("\n")
		sb.WriteString(stylePageTitle.Render(m.pageTitle(f)))
		if ind := m.scrollIndicator(f); ind != "" {
			sb.WriteString(" " + styleFooter.Render(ind))
		}
		sb.WriteString("\n\n")
		sb.WriteString(m.providerDetailView(f))
	case pgAPIKeys:
		sb.WriteString("\n")
		sb.WriteString(stylePageTitle.Render(m.pageTitle(f)))
		if ind := m.scrollIndicator(f); ind != "" {
			sb.WriteString(" " + styleFooter.Render(ind))
		}
		sb.WriteString("\n\n")
		sb.WriteString(m.apiKeysView(f))
	default:
		sb.WriteString("\n")
		sb.WriteString(stylePageTitle.Render(m.pageTitle(f)))
		if ind := m.scrollIndicator(f); ind != "" {
			sb.WriteString(" " + styleFooter.Render(ind))
		}
		sb.WriteString("\n\n")
		sb.WriteString(m.windowView(f))
	}
	sb.WriteString(m.cmdView())
	sb.WriteString(m.statusView())
	sb.WriteString(m.footerView())
	return sb.String()
}

// headerView: "BasicRoute v1.0", base-url, dan hitungan Providers/Models/Proxies.
func (m Model) headerView() string {
	s := cli.Summarize(m.cliDeps)
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("BasicRoute"))
	sb.WriteString(" ")
	sb.WriteString(styleVersion.Render(Version))
	sb.WriteString("\n")
	sb.WriteString(styleURL.Render(m.deps.BaseURL))
	sb.WriteString("\n")
	sb.WriteString(styleStatLbl.Render("Providers: "))
	sb.WriteString(styleStatNum.Render(fmt.Sprint(s.ProvOn)))
	sb.WriteString(styleStatLbl.Render("/"))
	sb.WriteString(styleStatNum.Render(fmt.Sprint(s.ProvTotal)))
	sb.WriteString(styleStatSep.Render("  |  "))
	sb.WriteString(styleStatLbl.Render("Models: "))
	sb.WriteString(styleStatNum.Render(fmt.Sprint(s.ModelOn)))
	sb.WriteString(styleStatLbl.Render("/"))
	sb.WriteString(styleStatNum.Render(fmt.Sprint(s.ModelTotal)))
	sb.WriteString(styleStatSep.Render("  |  "))
	sb.WriteString(styleStatLbl.Render("Proxies: "))
	sb.WriteString(styleStatNum.Render(fmt.Sprint(s.PoolOn)))
	sb.WriteString(styleStatLbl.Render("/"))
	sb.WriteString(styleStatNum.Render(fmt.Sprint(s.PoolTotal)))
	return sb.String()
}

// homeView: daftar menu bernavigasi panah.
func (m Model) homeView() string {
	f := m.cur()
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(styleMenuHead.Render("Menus"))
	sb.WriteString("\n\n")
	for i, it := range homeMenu {
		row := "●  " + it.label
		if i == f.cursor {
			sb.WriteString(styleItemSel.Width(m.width).Render(row))
		} else {
			sb.WriteString(styleItem.Width(m.width).Render(
				styleBullet.Render("●")+"  "+it.label,
			))
		}
		if i < len(homeMenu)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// pageTitle memberi judul halaman konten.
func (m Model) pageTitle(f frame) string {
	switch f.pg {
	case pgAPIKeys:
		return "Api Key"
	case pgLogs:
		return "Logs"
	case pgProviders:
		return "Provider"
	case pgProvider:
		if f.arg != "" {
			return "Provider · " + f.arg
		}
		return "Provider"
	case pgProxies:
		return "Proxy"
	case pgProxy:
		if f.arg != "" {
			return "Proxy · " + f.arg
		}
		return "Proxy"
	case pgOutput:
		switch f.arg {
		case outHelp:
			return "Help"
		case outStatus:
			return "Status"
		case outSystem:
			return "System"
		case outModels:
			return "Models"
		case outStats:
			return "Stats"
		}
		return "Output"
	}
	return ""
}

// pageKind memberi tahu cara me-render baris konten.
type lineKind int

const (
	lkBody lineKind = iota // teks polos
	lkLog                  // log (warna per level)
	lkSelect               // baris pilihan (ada 1 yang disorot)
)

// pageLines mengembalikan baris konten penuh (plain, belum dipotong).
// selectIdx >= 0 menandai baris yang disorot (khusus lkSelect).
// dots sejajar baris: warna dot per baris (hijau=on, abu=off); nil = bullet biasa.
func (m Model) pageLines(f frame) (lines []string, kind lineKind, selectIdx int, dots []bool) {
	selectIdx = -1
	switch f.pg {
	case pgLogs:
		return m.logLines, lkLog, -1, nil
	case pgAPIKeys:
		rows, ds := m.apiKeyRows(f.revealKeys)
		if len(rows) == 0 {
			return []string{"(no api keys yet)"}, lkBody, -1, nil
		}
		return rows, lkSelect, f.cursor, ds
	case pgProxy:
		return splitLines(cli.RenderProxy(m.cliDeps, f.arg)), lkBody, -1, nil
	case pgOutput:
		switch f.arg {
		case outHelp:
			return splitLines(cli.RenderHelp(m.cliDeps)), lkBody, -1, nil
		case outStatus:
			return splitLines(cli.RenderStatus(m.cliDeps)), lkBody, -1, nil
		case outSystem:
			return splitLines(cli.RenderSystem(m.cliDeps)), lkBody, -1, nil
		case outModels:
			return splitLines(cli.RenderModels(m.cliDeps, f.filter)), lkBody, -1, nil
		case outStats:
			return splitLines(cli.RenderStats(m.cliDeps)), lkBody, -1, nil
		}
	}
	return nil, lkBody, -1, nil
}

// apiKeyRows: baris data per system key untuk dipakai pageLines/scroll.
// Format kolom: nama | keys | total tokens (dipakai juga oleh apiKeysView).
// reveal=true menampilkan key jelas, false termasking.
// Mengembalikan juga status enabled per baris untuk warna dot.
func (m Model) apiKeyRows(reveal bool) ([]string, []bool) {
	names := m.sysKeyNames()
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil, nil
	}
	nW, kW := runeLen("NAMA"), runeLen("KEYS")
	type row struct {
		name, shown, total string
		on                 bool
	}
	rows := make([]row, 0, len(names))
	for _, n := range names {
		k := snap.Raw.System.Keys[n]
		on := k != nil && bool(k.Enabled)
		shown := "-"
		var hash string
		if k != nil {
			if reveal {
				shown = k.Key
			} else {
				shown = cli.MaskKey(k.Key)
			}
			hash = sysKeyHash(k.Key)
		}
		rows = append(rows, row{name: n, shown: shown, total: cli.FormatTokens(m.apiUsageTotal(hash)), on: on})
	}
	for _, r := range rows {
		nW = max(nW, runeLen(r.name))
		kW = max(kW, runeLen(r.shown))
	}
	nW += 2
	kW += 2
	out := make([]string, 0, len(rows))
	dots := make([]bool, 0, len(rows))
	for _, r := range rows {
		out = append(out, "•  "+padRight(r.name, nW)+padRight(r.shown, kW)+r.total)
		dots = append(dots, r.on)
	}
	return out, dots
}

// apiKeysView: table NAMA | KEYS | TOTAL TOKENS ala section Keys/Models
// di halaman detail provider: header label + dot hijau/abu di depan nama.
// Header fixed, body di-scroll. Baris terpilih di-highlight.
func (m Model) apiKeysView(f frame) string {
	inner := m.innerW()
	vp := m.viewportH()
	names := m.sysKeyNames()
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return styleWin.Width(m.width - 2).Render(styleBody.Render("(snapshot not ready)")) + "\n"
	}
	if len(names) == 0 {
		body := []string{styleBody.Render("(no api keys yet)")}
		for len(body) < vp {
			body = append(body, "")
		}
		return styleWin.Width(m.width - 2).Render(strings.Join(body, "\n")) + "\n"
	}
	nW, kW := runeLen("NAMA"), runeLen("KEYS")
	type row struct {
		name, shown, total string
		on                 bool
	}
	data := make([]row, 0, len(names))
	for _, n := range names {
		k := snap.Raw.System.Keys[n]
		on := k != nil && bool(k.Enabled)
		shown := "-"
		var hash string
		if k != nil {
			if f.revealKeys {
				shown = k.Key
			} else {
				shown = cli.MaskKey(k.Key)
			}
			hash = sysKeyHash(k.Key)
		}
		data = append(data, row{name: n, shown: shown, total: cli.FormatTokens(m.apiUsageTotal(hash)), on: on})
	}
	for _, r := range data {
		nW = max(nW, runeLen(r.name))
		kW = max(kW, runeLen(r.shown))
	}
	nW += 2
	kW += 2

	header := dline{[]dseg{
		{padRight("NAMA", nW+2) + padRight("KEYS", kW) + "TOTAL TOKENS", styleDetLbl},
	}}.render(inner)

	// Body scroll atas data saja (header fixed).
	start := clamp(f.scroll, 0, maxScroll(len(data), vp-1))
	end := start + (vp - 1)
	if end > len(data) {
		end = len(data)
	}
	vis := make([]string, 0, vp)
	vis = append(vis, header)
	for idx := start; idx < end; idx++ {
		r := data[idx]
		ds := styleDotOff
		if r.on {
			ds = styleDotOn
		}
		plain := "•  " + padRight(r.name, nW) + padRight(r.shown, kW) + r.total
		plain = trunc(plain, inner)
		if idx == f.cursor {
			vis = append(vis, ds.Render("•")+"  "+styleItemSel.Width(inner-3).Render(strings.TrimPrefix(plain, "•  ")))
			continue
		}
		vis = append(vis, dline{[]dseg{
			{"• ", ds},
			{padRight(r.name, nW), styleDetVal},
			{padRight(r.shown, kW), styleDetVal},
			{r.total, styleDetVal},
		}}.render(inner))
	}
	for len(vis) < vp {
		vis = append(vis, "")
	}
	return styleWin.Width(m.width - 2).Render(strings.Join(vis, "\n")) + "\n"
}

// windowView: konten dalam window border terpisah, di-scroll + dipotong.
func (m Model) windowView(f frame) string {
	vp := m.viewportH()
	inner := m.innerW()
	lines, kind, sel, dots := m.pageLines(f)

	start := f.scroll
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := start + vp
	if end > len(lines) {
		end = len(lines)
	}
	vis := make([]string, 0, vp)
	for _, ln := range lines[start:end] {
		vis = append(vis, trunc(ln, inner))
	}
	// Tinggi stabil: ganjal baris kosong.
	for len(vis) < vp {
		vis = append(vis, "")
	}

	styled := make([]string, 0, len(vis))
	for i, ln := range vis {
		real := start + i
		switch kind {
		case lkLog:
			if ln == "" {
				styled = append(styled, "")
				continue
			}
			styled = append(styled, styleLogLine(ln))
		case lkSelect:
			if ln == "" {
				styled = append(styled, "")
				continue
			}
			withDots := dots != nil && real < len(dots)
			var ds lipgloss.Style
			if withDots {
				ds = styleDotOff
				if dots[real] {
					ds = styleDotOn
				}
			}
			if real == sel {
				if withDots {
					styled = append(styled, ds.Render("•")+"  "+styleItemSel.Width(inner-3).Render(strings.TrimPrefix(ln, "•  ")))
				} else {
					styled = append(styled, styleItemSel.Width(inner).Render("●  "+ln))
				}
			} else if withDots {
				styled = append(styled, ds.Render("•")+"  "+styleItem.Render(strings.TrimPrefix(ln, "•  ")))
			} else {
				styled = append(styled, styleItem.Render(styleBullet.Render("●")+"  "+ln))
			}
		default:
			styled = append(styled, styleBody.Render(ln))
		}
	}
	// Info posisi scroll sebagai indikator di judul (tanpa baris tambahan).
	body := strings.Join(styled, "\n")
	win := styleWin.Width(m.width - 2).Render(body)
	win += "\n"
	return win
}

// scrollIndicator mengembalikan "↑"/"↓" bila konten terpotong viewport.
func (m Model) scrollIndicator(f frame) string {
	var n int
	vp := m.viewportH()
	switch f.pg {
	case pgLogs:
		n = len(m.logLines)
	case pgProviders:
		n = provRows(len(m.provCards()), m.provCols(len(m.provCards())))
		vp = m.provVisRows()
	case pgProxies:
		n = provRows(len(m.proxyCards()), m.proxyCols(len(m.proxyCards())))
		vp = m.proxyVisRows()
	case pgProvider:
		n = len(m.providerDetail(f))
	case pgAPIKeys:
		// Header fixed 1 baris, body = vp-1.
		n = len(m.sysKeyNames()) + 1
	default:
		pl, _, _, _ := m.pageLines(f)
		n = len(pl)
	}
	if n <= vp {
		return ""
	}
	up := f.scroll > 0
	down := f.scroll+vp < n
	switch {
	case up && down:
		return "↑↓"
	case up:
		return "↑"
	default:
		return "↓"
	}
}

// styleLogLine mewarnai baris log per level — info redup, warn terang, error bold.
func styleLogLine(s string) string {
	switch {
	case strings.Contains(s, "level=ERROR"):
		return styleLogErr.Render(s)
	case strings.Contains(s, "level=WARN"):
		return styleLogWarn.Render(s)
	default:
		return styleLogInfo.Render(s)
	}
}

// cmdView: baris perintah ala CLI lama ("br> ").
// Hanya tampil saat cmdMode (tombol "/"); hilang saat perintah
// selesai dijalankan (Enter) atau dibatalkan (Esc).
func (m Model) cmdView() string {
	if !m.cmdMode {
		return ""
	}
	return stylePrompt.Render("br> ") + m.input.View() + "\n"
}

// statusView: selalu 1 baris agar layout stabil; error digarisbawahi.
func (m Model) statusView() string {
	if m.status == "" {
		return "\n"
	}
	if m.isErr {
		return styleErr.Render(m.status) + "\n"
	}
	return styleStatus.Render(m.status) + "\n"
}

// footerView: petunjuk tombol sesuai konteks halaman.
func (m Model) footerView() string {
	if m.cmdMode {
		return hintLine(
			"Enter", "send",
			"Esc", "cancel",
		)
	}
	f := m.cur()
	switch f.pg {
	case pgHome:
		return hintLine(
			"↑↓", "select",
			"Enter", "open",
			"/", "command",
			"r", "reload",
			"q", "quit",
		)
	case pgLogs:
		return hintLine(
			"↑↓", "scroll",
			"PgUp/PgDn", "jump",
			"End", "tail",
			"Esc", "back",
		)
	case pgProviders:
		return hintLine(
			"←→↑↓", "select",
			"Enter", "details",
			"Esc", "back",
			"/", "command",
		)
	case pgAPIKeys:
		return hintLine(
			"↑↓", "select",
			"x", "show/hide",
			"Esc", "back",
			"/", "command",
		)
	case pgProvider:
		return hintLine(
			"↑↓", "scroll",
			"Esc", "back",
			"/", "command",
		)
	case pgProxies:
		return hintLine(
			"←→↑↓", "select",
			"Enter", "details",
			"t", "test",
			"Esc", "back",
			"/", "command",
		)
	default:
		return hintLine(
			"↑↓ PgUp/PgDn", "scroll",
			"Esc", "back",
			"/", "command",
			"r", "reload",
		)
	}
}

// hintLine merakit pasangan tombol + deskripsi dalam satu baris redup.
func hintLine(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, styleFootKey.Render(pairs[i])+" "+styleFooter.Render(pairs[i+1]))
	}
	return strings.Join(parts, styleFooter.Render("   ")) + "\n"
}
