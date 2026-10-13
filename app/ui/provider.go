package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"basicroute/app/cli"
	"basicroute/app/config"
	"basicroute/app/service"
)

// ---- daftar provider sebagai kartu (grid responsif ala flexbox) ----

const (
	provCardW   = 26 // lebar total kartu, termasuk border
	provCardGap = 2  // spasi antar kartu
	provCardH   = 5  // tinggi kartu: 3 baris isi + 2 border
)

// provCard adalah ringkasan satu provider untuk kartu.
type provCard struct {
	slug                 string
	name                 string
	prefix               string
	baseURL              string
	proxy                string
	enabled              bool
	keysOn, keysTotal    int
	modelsOn, modelsTotal int
}

// provCards membaca ringkasan provider live dari Store, terurut slug.
func (m Model) provCards() []provCard {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	slugs := m.providerSlugs()
	out := make([]provCard, 0, len(slugs))
	for _, slug := range slugs {
		p := snap.Raw.Providers[slug]
		if p == nil {
			continue
		}
		c := provCard{
			slug:    slug,
			name:    p.Name,
			prefix:  p.Prefix,
			baseURL: p.BaseURL,
			enabled: bool(p.Enabled),
		}
		for _, k := range p.Keys {
			c.keysTotal++
			if k != nil && bool(k.Enabled) {
				c.keysOn++
			}
		}
		for _, md := range p.Models {
			c.modelsTotal++
			if md != nil && bool(md.Enabled) {
				c.modelsOn++
			}
		}
		c.proxy = strings.TrimSpace(p.Proxy)
		if c.proxy == "" {
			c.proxy = "direct"
		}
		out = append(out, c)
	}
	return out
}

// provCols menghitung jumlah kolom dari lebar terminal (responsif).
func (m Model) provCols(n int) int {
	if n <= 0 {
		return 1
	}
	cols := (m.innerW() + provCardGap) / (provCardW + provCardGap)
	if cols < 1 {
		cols = 1
	}
	if cols > n {
		cols = n
	}
	return cols
}

func provRows(n, cols int) int {
	if n <= 0 || cols <= 0 {
		return 0
	}
	return (n + cols - 1) / cols
}

// provVisRows: baris kartu yang muat di viewport (sisakan 2 baris info).
func (m Model) provVisRows() int {
	vr := (m.viewportH() - 1) / (provCardH + 1)
	if vr < 1 {
		vr = 1
	}
	return vr
}

// renderProvCard menggambar satu kartu (lebar tetap provCardW).
func (m Model) renderProvCard(c provCard, sel bool) string {
	w := provCardW - 4 // isi: border 2 + padding 2

	ns := styleCardName
	ds := styleDotOn
	if !c.enabled {
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
		{"Keys: ", styleCardLbl},
		{fmt.Sprintf("%d/%d", c.keysOn, c.keysTotal), styleCardVal},
		{"  Models: ", styleCardLbl},
		{fmt.Sprintf("%d/%d", c.modelsOn, c.modelsTotal), styleCardVal},
	}}.render(w)
	l3 := dline{[]dseg{{"Proxy: ", styleCardLbl}, {c.proxy, styleCardVal}}}.render(w)

	body := strings.Join([]string{l1, l2, l3}, "\n")
	st := styleCard.Width(provCardW - 2)
	if sel {
		st = styleCardSel.Width(provCardW - 2)
	}
	_ = m
	return st.Render(body)
}

// providersGridView: kartu-kartu dalam window, navigasi 2D.
func (m Model) providersGridView(f frame) string {
	cards := m.provCards()
	vp := m.viewportH()
	var body []string

	if len(cards) == 0 {
		body = []string{styleBody.Render("(no providers yet)")}
	} else {
		cols := m.provCols(len(cards))
		rows := provRows(len(cards), cols)
		vis := m.provVisRows()
		start := clamp(f.scroll, 0, imax(rows-vis, 0))
		end := start + vis
		if end > rows {
			end = rows
		}
		gap := strings.Repeat(" ", provCardGap)
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
				parts = append(parts, m.renderProvCard(cards[idx], idx == f.cursor))
			}
			body = append(body, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, parts...), "\n")...)
			if r < end-1 {
				body = append(body, "")
			}
		}
		if f.cursor >= 0 && f.cursor < len(cards) {
			sel := cards[f.cursor]
			body = append(body, "")
			body = append(body, styleFooter.Render("▸ "+sel.slug+"  ·  "+sel.prefix+"  ·  "+sel.proxy))
		}
	}

	for len(body) < vp {
		body = append(body, "")
	}
	return styleWin.Width(m.width - 2).Render(strings.Join(body, "\n")) + "\n"
}

// ensureCardVisible menggeser scroll baris agar cursor terlihat.
func (m *Model) ensureCardVisible() {
	f := m.cur()
	var n, cols, vis int
	switch f.pg {
	case pgProviders:
		n = len(m.provCards())
		cols = m.provCols(n)
		vis = m.provVisRows()
	case pgProxies:
		n = len(m.proxyCards())
		cols = m.proxyCols(n)
		vis = m.proxyVisRows()
	default:
		return
	}
	if n == 0 {
		return
	}
	row := f.cursor / cols
	if row < f.scroll {
		f.scroll = row
	} else if row >= f.scroll+vis {
		f.scroll = row - vis + 1
	}
	m.setCur(f)
}

// ---- halaman detail provider (berseksi, tidy) ----

// dseg adalah satu segmen teks dengan stylenya sendiri.
type dseg struct {
	text string
	st   lipgloss.Style
}

// dline adalah satu baris visual: beberapa segmen, segmen terakhir
// dipotong otomatis bila melebihi lebar.
type dline struct {
	segs []dseg
}

func (l dline) render(w int) string {
	total := 0
	for _, s := range l.segs {
		total += len([]rune(s.text))
	}
	var sb strings.Builder
	for i, s := range l.segs {
		t := s.text
		if i == len(l.segs)-1 && total > w && w > 0 {
			t = trunc(t, len([]rune(t))-(total-w))
		}
		sb.WriteString(s.st.Render(t))
	}
	return sb.String()
}

func runeLen(s string) int { return len([]rune(s)) }

func padRight(s string, n int) string {
	if k := runeLen(s); k >= n {
		return s
	} else {
		return s + strings.Repeat(" ", n-k)
	}
}

// lookupProv mencari provider by slug/prefix (case-insensitive), seperti CLI.
func lookupProv(snap *config.Snapshot, arg string) (string, *config.Provider) {
	a := strings.TrimSpace(arg)
	if a == "" || snap == nil || snap.Raw == nil {
		return "", nil
	}
	if p, ok := snap.Raw.Providers[a]; ok && p != nil {
		return a, p
	}
	for slug, p := range snap.Raw.Providers {
		if p == nil {
			continue
		}
		if strings.EqualFold(slug, a) || strings.EqualFold(p.Prefix, a) {
			return slug, p
		}
	}
	return "", nil
}

type pkeyRow struct {
	name string
	ord  int
	on   bool
	mask string
	cool bool
}

// providerDetail membangun baris-baris halaman detail (seksi rapi).
func (m Model) providerDetail(f frame) []dline {
	inner := m.innerW()
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return []dline{{[]dseg{{"(snapshot not ready)", styleBody}}}}
	}
	slug, p := lookupProv(snap, f.arg)
	if p == nil {
		return []dline{
			{[]dseg{{"provider " + fmt.Sprintf("%q", f.arg) + " not found", styleErr}}},
			{[]dseg{{"Esc to go back", styleFooter}}},
		}
	}

	var out []dline

	// Judul: indikator status di depan nama.
	ds := styleDotOn
	if !bool(p.Enabled) {
		ds = styleDotOff
	}
	out = append(out, dline{[]dseg{{"• ", ds}, {p.Name, styleDetName}}})
	out = append(out, dline{[]dseg{{"", styleBody}}})

	// Seksi 1: informasi dasar.
	out = append(out, dline{[]dseg{{"Basic info", styleSecHead}}})
	proxy := strings.TrimSpace(p.Proxy)
	if proxy == "" {
		proxy = "direct"
	}
	info := [][2]string{
		{"prefix", p.Prefix},
		{"type", p.Type},
		{"interface", p.Interface},
		{"base-url", p.BaseURL},
		{"proxy", proxy},
		{"timeout", fmt.Sprintf("%ds", int(p.TimeoutSecs))},
	}
	labW := 0
	for _, kv := range info {
		if runeLen(kv[0]) > labW {
			labW = runeLen(kv[0])
		}
	}
	labW += 2
	for _, kv := range info {
		out = append(out, dline{[]dseg{
			{padRight(kv[0], labW), styleDetLbl},
			{kv[1], styleDetVal},
		}})
	}

	// Seksi 2: keys (bisa di-toggle dengan x).
	var krows []pkeyRow
	for n, k := range p.Keys {
		if k == nil {
			krows = append(krows, pkeyRow{name: n})
			continue
		}
		krows = append(krows, pkeyRow{
			name: n,
			ord:  int(k.Order),
			on:   bool(k.Enabled),
			mask: cli.MaskKey(k.Key),
			cool: m.deps.Store.Cooldown(service.CooldownKey(slug, n)),
		})
	}
	sort.Slice(krows, func(i, j int) bool {
		if krows[i].ord != krows[j].ord {
			return krows[i].ord < krows[j].ord
		}
		return krows[i].name < krows[j].name
	})

	out = append(out, dline{[]dseg{{"", styleBody}}})
	out = append(out, dline{[]dseg{{strings.Repeat("─", inner), styleSep}}})
	out = append(out, dline{[]dseg{{fmt.Sprintf("Keys (%d)", len(krows)), styleSecHead}}})
	if len(krows) == 0 {
		out = append(out, dline{[]dseg{{"(no keys)", styleDetLbl}}})
	} else {
		nW, oW, sW, cW := runeLen("NAME"), runeLen("ORDER"), runeLen("STATUS"), runeLen("COOLDOWN")
		for _, r := range krows {
			nW = max(nW, runeLen(r.name))
			oW = max(oW, runeLen(fmt.Sprint(r.ord)))
			if r.cool {
				cW = max(cW, runeLen("cooling"))
			}
		}
		nW += 2
		oW += 2
		sW += 2
		cW += 2
		out = append(out, dline{[]dseg{
			{padRight("NAME", nW) + padRight("ORDER", oW) + padRight("STATUS", sW) + padRight("COOLDOWN", cW) + "KEY", styleDetLbl},
		}})
		for _, r := range krows {
			st, sts := "on", styleDetHi
			if !r.on {
				st, sts = "off", styleDetLbl
			}
			if r.mask == "" || r.mask == "-" {
				st, sts = "(null)", styleDetLbl
			}
			cool, cools := "-", styleDetLbl
			if r.cool {
				cool, cools = "cooling", styleLogWarn
			}
			out = append(out, dline{[]dseg{
				{padRight(r.name, nW), styleDetVal},
				{padRight(fmt.Sprint(r.ord), oW), styleDetLbl},
				{padRight(st, sW), sts},
				{padRight(cool, cW), cools},
				{r.mask, styleDetVal},
			}})
		}
	}

	// Seksi 3: models.
	mslugs := make([]string, 0, len(p.Models))
	for ms := range p.Models {
		mslugs = append(mslugs, ms)
	}
	sort.Strings(mslugs)

	out = append(out, dline{[]dseg{{"", styleBody}}})
	out = append(out, dline{[]dseg{{strings.Repeat("─", inner), styleSep}}})
	out = append(out, dline{[]dseg{{fmt.Sprintf("Models (%d)", len(mslugs)), styleSecHead}}})
	if len(mslugs) == 0 {
		out = append(out, dline{[]dseg{{"(no models)", styleDetLbl}}})
	} else {
		sW, fW, stW, cW, oW := runeLen("SLUG"), runeLen("FULL ID"), runeLen("STATUS"), runeLen("CTX"), runeLen("OUT")
		type mrow struct {
			slug, full, st, ctx, out, created string
			on                               bool
		}
		var mrows []mrow
		for _, ms := range mslugs {
			md := p.Models[ms]
			r := mrow{slug: ms, full: p.Prefix + "/" + ms, st: "off"}
			if md != nil {
				if bool(md.Enabled) {
					r.on = true
					r.st = "on"
				}
				r.ctx = fmt.Sprint(int(md.Limit.Context))
				r.out = fmt.Sprint(int(md.Limit.Output))
				if int64(md.Created) > 0 {
					r.created = time.Unix(int64(md.Created), 0).Format("2006-01-02")
				} else {
					r.created = "-"
				}
			} else {
				r.ctx, r.out, r.created = "-", "-", "-"
			}
			mrows = append(mrows, r)
			sW = max(sW, runeLen(r.slug))
			fW = max(fW, runeLen(r.full))
			cW = max(cW, runeLen(r.ctx))
			oW = max(oW, runeLen(r.out))
		}
		sW += 2
		fW += 2
		stW += 2
		cW += 2
		oW += 2
		out = append(out, dline{[]dseg{
			{padRight("SLUG", sW) + padRight("FULL ID", fW) + padRight("STATUS", stW) + padRight("CTX", cW) + padRight("OUT", oW) + "CREATED", styleDetLbl},
		}})
		for _, r := range mrows {
			sts := styleDetLbl
			if r.on {
				sts = styleDetHi
			}
			out = append(out, dline{[]dseg{
				{padRight(r.slug, sW), styleDetVal},
				{padRight(r.full, fW), styleDetLbl},
				{padRight(r.st, stW), sts},
				{padRight(r.ctx, cW), styleDetVal},
				{padRight(r.out, oW), styleDetVal},
				{r.created, styleDetLbl},
			}})
		}
	}
	return out
}

// providerDetailView: seksi detail dalam window, di-scroll + stabil.
func (m Model) providerDetailView(f frame) string {
	inner := m.innerW()
	vp := m.viewportH()
	lines := m.providerDetail(f)

	start := clamp(f.scroll, 0, maxScroll(len(lines), vp))
	end := start + vp
	if end > len(lines) {
		end = len(lines)
	}
	vis := make([]string, 0, vp)
	for _, l := range lines[start:end] {
		vis = append(vis, l.render(inner))
	}
	for len(vis) < vp {
		vis = append(vis, "")
	}
	return styleWin.Width(m.width - 2).Render(strings.Join(vis, "\n")) + "\n"
}
