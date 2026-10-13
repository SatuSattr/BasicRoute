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
	slug                  string
	name                  string
	prefix                string
	baseURL               string
	proxy                 string
	enabled               bool
	keysOn, keysTotal     int
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
		c.proxy = service.ProxyModeShort(snap, p)
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
	}

	for len(body) < vp {
		body = append(body, "")
	}
	return styleWin.Width(m.width-2).Render(strings.Join(body, "\n")) + "\n"
}

// ensureCardVisible menggeser scroll baris agar cursor terlihat (grid provider).
func (m *Model) ensureCardVisible() {
	f := m.cur()
	if f.pg != pgProviders {
		return
	}
	n := len(m.provCards())
	cols := m.provCols(n)
	vis := m.provVisRows()
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

// renderSelected menggambar baris terseleksi ala tabel Api Key: dot tetap
// berwarna, sisa teks digambar polos dengan styleItemSel sehingga terang
// menembus teks (bukan menimpa style redup di atasnya).
func (l dline) renderSelected(w int) string {
	if len(l.segs) == 0 || w <= 0 {
		return styleItemSel.Width(w).Render("")
	}
	dot := l.segs[0]
	var sb strings.Builder
	for _, s := range l.segs[1:] {
		sb.WriteString(s.text)
	}
	rest := sb.String()
	dotW := runeLen(dot.text)
	rest = trunc(rest, w-dotW)
	return dot.st.Render(dot.text) + styleItemSel.Width(w-dotW).Render(rest)
}

func runeLen(s string) int { return len([]rune(s)) }

func padRight(s string, n int) string {
	if k := runeLen(s); k >= n {
		return s
	} else {
		return s + strings.Repeat(" ", n-k)
	}
}

// shortNum memadatkan angka besar agar kolom tetap ramping:
// 125000 -> "125k", 1000000 -> "1M", 2500000000 -> "2.5B".
// Di bawah 1000 tampil apa adanya.
func shortNum(n int) string {
	switch {
	case n >= 1_000_000_000:
		return trimDec(float64(n)/1_000_000_000) + "B"
	case n >= 1_000_000:
		return trimDec(float64(n)/1_000_000) + "M"
	case n >= 1_000:
		return trimDec(float64(n)/1_000) + "k"
	default:
		return fmt.Sprint(n)
	}
}

// trimDec mencetak 1 desimal lalu membuang ".0": 1 -> "1", 1.5 -> "1.5".
func trimDec(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
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
	name    string
	ord     int
	on      bool
	mask    string
	cool    bool
	proxy   string
	inherit bool // proxy diwarisi dari provider (ditampilkan redup)
}

// provKeyErr melaporkan key provider sedang flagged error (dot merah).
func (m Model) provKeyErr(slug, name string) bool {
	return m.deps.Store != nil && m.deps.Store.ProvHealth != nil &&
		m.deps.Store.ProvHealth.IsError(service.ProvKeyID(slug, name))
}

// provModelErr melaporkan model provider sedang flagged error (dot merah).
func (m Model) provModelErr(slug, modelSlug string) bool {
	return m.deps.Store != nil && m.deps.Store.ProvHealth != nil &&
		m.deps.Store.ProvHealth.IsError(service.ProvModelID(slug, modelSlug))
}

// provKeyTesting melaporkan key provider sedang di-test (dot kuning).
func (m Model) provKeyTesting(slug, name string) bool {
	return m.deps.Store != nil && m.deps.Store.ProvHealth != nil &&
		m.deps.Store.ProvHealth.IsTesting(service.ProvKeyID(slug, name))
}

// provModelTesting melaporkan model provider sedang di-test (dot kuning).
func (m Model) provModelTesting(slug, modelSlug string) bool {
	return m.deps.Store != nil && m.deps.Store.ProvHealth != nil &&
		m.deps.Store.ProvHealth.IsTesting(service.ProvModelID(slug, modelSlug))
}

// provDot memilih style dot baris key/model: kuning (sedang di-test),
// merah (flagged), hijau (enabled), abu (disabled).
func (m Model) provDot(slug string, isKey bool, name string, on bool) lipgloss.Style {
	switch {
	case isKey && m.provKeyTesting(slug, name), !isKey && m.provModelTesting(slug, name):
		return styleDotTest
	case isKey && m.provKeyErr(slug, name), !isKey && m.provModelErr(slug, name):
		return styleDotErr
	case on:
		return styleDotOn
	default:
		return styleDotOff
	}
}

// provErrLine mengembalikan baris detail error untuk baris cursor
// (key/model), "" bila baris sehat. Dipakai di dasar container detail.
func (m Model) provErrLine(slug string, cursor int) string {
	if m.deps.Store == nil || m.deps.Store.ProvHealth == nil {
		return ""
	}
	ids := m.provRowIDs(slug)
	if cursor < 0 || cursor >= len(ids) {
		return ""
	}
	id := ids[cursor]
	kind := "model"
	key := service.ProvModelID(slug, id.name)
	if id.isKey {
		kind = "key"
		key = service.ProvKeyID(slug, id.name)
	}
	msg := m.deps.Store.ProvHealth.ErrMsg(key)
	if msg == "" {
		return ""
	}
	return fmt.Sprintf("%s %q error: %s", kind, id.label, msg)
}

// providerDetail membangun baris-baris halaman detail (seksi rapi).
// Mengembalikan juga index baris yang bisa di-select, dipisah per seksi:
// keyLines (keys) lalu modelLines (models), agar multi-select tidak
// menembus antar seksi.
func (m Model) providerDetail(f frame) (lines []dline, keyLines []int, modelLines []int) {
	inner := m.innerW()
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return []dline{{[]dseg{{"(snapshot not ready)", styleBody}}}}, nil, nil
	}
	slug, p := lookupProv(snap, f.arg)
	if p == nil {
		return []dline{
			{[]dseg{{"provider " + fmt.Sprintf("%q", f.arg) + " not found", styleErr}}},
			{[]dseg{{"Esc to go back", styleFooter}}},
		}, nil, nil
	}

	var out []dline
	var keys, models []int

	// Judul: indikator status di depan nama.
	ds := styleDotOn
	if !bool(p.Enabled) {
		ds = styleDotOff
	}
	out = append(out, dline{[]dseg{{"• ", ds}, {p.Name, styleDetName}}})
	out = append(out, dline{[]dseg{{"", styleBody}}})

	// Seksi 1: informasi dasar.
	out = append(out, dline{[]dseg{{"Basic info", styleSecHead}}})
	proxy := service.ProxyModeLabel(snap, p)
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
			krows = append(krows, pkeyRow{name: n, proxy: "-"})
			continue
		}
		pl, inh := service.KeyProxyLabel(snap, p, k.Proxy)
		krows = append(krows, pkeyRow{
			name:    n,
			ord:     int(k.Order),
			on:      bool(k.Enabled),
			mask:    cli.MaskKey(k.Key),
			cool:    m.deps.Store.Cooldown(service.CooldownKey(slug, n)),
			proxy:   pl,
			inherit: inh,
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
		nW, oW, cW, pW := runeLen("NAME"), runeLen("ORDER"), runeLen("COOLDOWN"), runeLen("PROXY")
		for _, r := range krows {
			nW = max(nW, runeLen(r.name))
			oW = max(oW, runeLen(fmt.Sprint(r.ord)))
			pW = max(pW, runeLen(r.proxy))
			if r.cool {
				cW = max(cW, runeLen("cooling"))
			}
		}
		nW += 2
		oW += 2
		cW += 2
		pW += 2
		out = append(out, dline{[]dseg{
			// Header NAME digeser 2 spasi agar sejajar teks nama di bawah dot "• ".
			{padRight("NAME", nW+2) + padRight("ORDER", oW) + padRight("COOLDOWN", cW) + padRight("PROXY", pW) + "KEY", styleDetLbl},
		}})
		for _, r := range krows {
			ds := m.provDot(slug, true, r.name, r.on)
			cool, cools := "-", styleDetLbl
			if r.cool {
				cool, cools = "cooling", styleLogWarn
			}
			ps := styleDetVal
			if r.inherit {
				ps = styleDetLbl
			}
			out = append(out, dline{[]dseg{
				{"• ", ds},
				{padRight(r.name, nW), styleDetVal},
				{padRight(fmt.Sprint(r.ord), oW), styleDetLbl},
				{padRight(cool, cW), cools},
				{padRight(r.proxy, pW), ps},
				{r.mask, styleDetVal},
			}})
			keys = append(keys, len(out)-1)
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
		sW, fW, cW, oW := runeLen("NAME"), runeLen("FULL ID"), runeLen("CTX"), runeLen("OUT")
		type mrow struct {
			slug, name, full, ctx, out, created string
			on                                  bool
		}
		var mrows []mrow
		for _, ms := range mslugs {
			md := p.Models[ms]
			r := mrow{slug: ms, name: ms, full: p.Prefix + "/" + ms}
			if md != nil {
				if strings.TrimSpace(md.Name) != "" {
					r.name = md.Name
				}
				if bool(md.Enabled) {
					r.on = true
				}
				r.ctx = shortNum(int(md.Limit.Context))
				r.out = shortNum(int(md.Limit.Output))
				if int64(md.Created) > 0 {
					r.created = time.Unix(int64(md.Created), 0).Format("2006-01-02")
				} else {
					r.created = "-"
				}
			} else {
				r.ctx, r.out, r.created = "-", "-", "-"
			}
			mrows = append(mrows, r)
			sW = max(sW, runeLen(r.name))
			fW = max(fW, runeLen(r.full))
			cW = max(cW, runeLen(r.ctx))
			oW = max(oW, runeLen(r.out))
		}
		sW += 2
		fW += 2
		cW += 2
		oW += 2
		out = append(out, dline{[]dseg{
			// Header NAME digeser 2 spasi agar sejajar teks nama di bawah dot "• ".
			{padRight("NAME", sW+2) + padRight("FULL ID", fW) + padRight("CTX", cW) + padRight("OUT", oW) + "CREATED", styleDetLbl},
		}})
		for _, r := range mrows {
			ds := m.provDot(slug, false, r.slug, r.on)
			out = append(out, dline{[]dseg{
				{"• ", ds},
				{padRight(r.name, sW), styleDetVal},
				{padRight(r.full, fW), styleDetLbl},
				{padRight(r.ctx, cW), styleDetVal},
				{padRight(r.out, oW), styleDetVal},
				{r.created, styleDetLbl},
			}})
			models = append(models, len(out)-1)
		}
	}
	return out, keys, models
}

// provRowID mengidentifikasi satu baris selectable di domain cursor datar.
type provRowID struct {
	isKey bool
	name  string // nama key, atau slug model (untuk API)
	label string // nama tampil (display Name bila ada)
	on    bool   // enabled
}

// provRowIDs mengembalikan identitas baris seurut cursor datar
// (keys urut order lalu nama, models urut slug) — konsisten dengan
// providerDetail agar cursor selalu memetakan ke baris yang benar.
func (m Model) provRowIDs(slug string) []provRowID {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	_, p := lookupProv(snap, slug)
	if p == nil {
		return nil
	}
	var out []provRowID
	type krow struct {
		name string
		ord  int
		on   bool
	}
	var krows []krow
	for n, k := range p.Keys {
		if k == nil {
			krows = append(krows, krow{name: n})
			continue
		}
		krows = append(krows, krow{name: n, ord: int(k.Order), on: bool(k.Enabled)})
	}
	sort.Slice(krows, func(i, j int) bool {
		if krows[i].ord != krows[j].ord {
			return krows[i].ord < krows[j].ord
		}
		return krows[i].name < krows[j].name
	})
	for _, r := range krows {
		out = append(out, provRowID{isKey: true, name: r.name, label: r.name, on: r.on})
	}
	mslugs := make([]string, 0, len(p.Models))
	for ms := range p.Models {
		mslugs = append(mslugs, ms)
	}
	sort.Strings(mslugs)
	for _, ms := range mslugs {
		md := p.Models[ms]
		label := ms
		on := false
		if md != nil {
			if strings.TrimSpace(md.Name) != "" {
				label = md.Name
			}
			on = bool(md.Enabled)
		}
		out = append(out, provRowID{name: ms, label: label, on: on})
	}
	return out
}

// provFlat menggabung baris selectable keys+models ke domain cursor datar.
func provFlat(keyLines, modelLines []int) []int {
	out := make([]int, 0, len(keyLines)+len(modelLines))
	out = append(out, keyLines...)
	out = append(out, modelLines...)
	return out
}

// provSection mengembalikan batas cursor [lo,hi] untuk seksi cursor saat ini
// (keys: 0..nk-1, models: nk..n-1) agar multi-select tidak menembus seksi.
func provSection(cursor, nk, n int) (lo, hi int) {
	if n <= 0 {
		return 0, -1
	}
	if cursor < nk {
		return 0, nk - 1
	}
	return nk, n - 1
}

// provSelLabel memberi label jumlah multi-select per seksi untuk footer.
func provSelLabel(nk int, sel map[int]bool) string {
	nkSel, mSel := 0, 0
	for p := range sel {
		if p < nk {
			nkSel++
		} else {
			mSel++
		}
	}
	switch {
	case nkSel > 0 && mSel == 0:
		return fmt.Sprintf("%d keys selected", nkSel)
	case mSel > 0 && nkSel == 0:
		return fmt.Sprintf("%d models selected", mSel)
	case nkSel+mSel > 0:
		return fmt.Sprintf("%d selected", nkSel+mSel)
	default:
		return ""
	}
}

// providerDetailView: seksi detail dalam window, seleksi keys/models + auto-scroll.
func (m Model) providerDetailView(f frame) string {
	inner := m.innerW()
	vp := m.viewportH()
	lines, keyLines, modelLines := m.providerDetail(f)
	selectable := provFlat(keyLines, modelLines)

	start := clamp(f.scroll, 0, maxScroll(len(lines), vp))
	end := start + vp
	if end > len(lines) {
		end = len(lines)
	}
	selLine := -1
	if f.cursor >= 0 && f.cursor < len(selectable) {
		selLine = selectable[f.cursor]
	}
	picked := map[int]bool{}
	for pos := range f.sel {
		if pos >= 0 && pos < len(selectable) {
			picked[selectable[pos]] = true
		}
	}
	vis := make([]string, 0, vp)
	for i := start; i < end; i++ {
		if i == selLine || picked[i] {
			vis = append(vis, lines[i].renderSelected(inner))
			continue
		}
		vis = append(vis, lines[i].render(inner))
	}
	for len(vis) < vp {
		vis = append(vis, "")
	}
	// Baris dasar container: detail error baris fokus bila flagged,
	// jika tidak maka padding seperti biasa (layout tidak bergeser).
	if len(vis) > 0 {
		if el := m.provErrLine(f.arg, f.cursor); el != "" {
			vis[len(vis)-1] = styleErr.Render(trunc(el, inner))
		}
	}
	return styleWin.Width(m.width-2).Render(strings.Join(vis, "\n")) + "\n"
}

// ensureProviderVisible menggeser scroll agar baris terseleksi terlihat.
func (m *Model) ensureProviderVisible() {
	f := m.cur()
	if f.pg != pgProvider {
		return
	}
	lines, keyLines, modelLines := m.providerDetail(f)
	selectable := provFlat(keyLines, modelLines)
	if len(selectable) == 0 {
		return
	}
	if f.cursor < 0 {
		f.cursor = 0
	}
	if f.cursor >= len(selectable) {
		f.cursor = len(selectable) - 1
	}
	selLine := selectable[f.cursor]
	vp := m.viewportH()
	if selLine < f.scroll {
		f.scroll = selLine
	} else if selLine >= f.scroll+vp {
		f.scroll = selLine - vp + 1
	}
	maxS := maxScroll(len(lines), vp)
	f.scroll = clamp(f.scroll, 0, maxS)
	m.setCur(f)
}
