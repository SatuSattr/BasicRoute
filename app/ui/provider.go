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

// ---- provider list as a table (like the Api Key / Proxy pages) ----

// provCard is one provider row summary.
type provCard struct {
	slug                  string
	name                  string
	typ                   string
	proxy                 string
	enabled               bool
	err                   bool // provider-level flagged (models:auto fetch gagal)
	testing               bool // provider-level fetching (models:auto)
	auto                  bool // models:auto
	keysOn, keysTotal     int
	modelsOn, modelsTotal int
}

// provCards reads the live provider summary from the Store, sorted by slug.
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
			typ:     p.Type,
			enabled: bool(p.Enabled),
			auto:    p.IsModelsAuto(),
		}
		if m.deps.Store != nil && m.deps.Store.ProvHealth != nil {
			pid := service.ProvProviderID(slug)
			c.err = m.deps.Store.ProvHealth.IsError(pid)
			c.testing = m.deps.Store.ProvHealth.IsTesting(pid)
		}
		for _, k := range p.Keys {
			c.keysTotal++
			if k != nil && bool(k.Enabled) {
				c.keysOn++
			}
		}
		if p.IsModelsAuto() {
			n := 0
			for _, ref := range snap.ModelMap {
				if ref != nil && ref.ProviderSlug == slug {
					n++
				}
			}
			c.modelsOn, c.modelsTotal = n, n
		} else {
			for _, md := range p.Models {
				c.modelsTotal++
				if md != nil && bool(md.Enabled) {
					c.modelsOn++
				}
			}
		}
		c.proxy = service.ProxyModeShort(snap, p)
		out = append(out, c)
	}
	return out
}

// provLevelDot memilih style dot untuk baris provider di tabel list:
// kuning (fetching auto), merah (fetch gagal), abu (disabled), hijau (sehat).
func provLevelDot(c provCard) lipgloss.Style {
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

// provLevelErr mengembalikan pesan error provider-level ("" bila sehat).
func (m Model) provLevelErr(slug string) string {
	if m.deps.Store == nil || m.deps.Store.ProvHealth == nil {
		return ""
	}
	return m.deps.Store.ProvHealth.ErrMsg(service.ProvProviderID(slug))
}

// provTypeStyle returns the text style for the TYPE column.
func provTypeStyle(_ string) lipgloss.Style {
	return styleProvType
}

// providersTableView renders NAME | TYPE | KEYS | MODELS | PROXY.
// Fixed header, scrollable body, single-cursor selection.
func (m Model) providersTableView(f frame) string {
	inner := m.innerW()
	vp := m.viewportH()
	cards := m.provCards()
	if len(cards) == 0 {
		body := []string{styleBody.Render("(no providers yet)")}
		for len(body) < vp {
			body = append(body, "")
		}
		return styleWin.Width(m.width-2).Render(strings.Join(body, "\n")) + "\n"
	}
	nW, tW, kW, mW := runeLen("NAME"), runeLen("TYPE"), runeLen("KEYS"), runeLen("MODELS")
	for _, c := range cards {
		nW = max(nW, runeLen(c.name))
		tW = max(tW, runeLen(c.typ))
		kW = max(kW, runeLen(fmt.Sprintf("%d/%d", c.keysOn, c.keysTotal)))
		mW = max(mW, runeLen(fmt.Sprintf("%d/%d", c.modelsOn, c.modelsTotal)))
	}
	nW += 2
	tW += 2
	kW += 2
	mW += 2

	header := dline{[]dseg{
		{padRight("NAME", nW+2) + padRight("TYPE", tW) + padRight("KEYS", kW) + padRight("MODELS", mW) + "PROXY", styleDetLbl},
	}}.render(inner)

	start := clamp(f.scroll, 0, maxScroll(len(cards), vp-1))
	end := start + (vp - 1)
	if end > len(cards) {
		end = len(cards)
	}
	vis := make([]string, 0, vp)
	vis = append(vis, header)
	for idx := start; idx < end; idx++ {
		c := cards[idx]
		ds := provLevelDot(c)
		plain := "• " + padRight(c.name, nW) + padRight(c.typ, tW) +
			padRight(fmt.Sprintf("%d/%d", c.keysOn, c.keysTotal), kW) +
			padRight(fmt.Sprintf("%d/%d", c.modelsOn, c.modelsTotal), mW) + c.proxy
		plain = trunc(plain, inner)
		if idx == f.cursor {
			vis = append(vis, ds.Render("• ")+styleItemSel.Width(inner-2).Render(strings.TrimPrefix(plain, "• ")))
			continue
		}
		vis = append(vis, dline{[]dseg{
			{"• ", ds},
			{padRight(c.name, nW), styleDetVal},
			{padRight(c.typ, tW), provTypeStyle(c.typ)},
			{padRight(fmt.Sprintf("%d/%d", c.keysOn, c.keysTotal), kW), styleDetVal},
			{padRight(fmt.Sprintf("%d/%d", c.modelsOn, c.modelsTotal), mW), styleDetVal},
			{c.proxy, styleDetVal},
		}}.render(inner))
	}
	for len(vis) < vp {
		vis = append(vis, "")
	}
	// Baris dasar: keterangan error provider-level yang sedang di-select
	// (fetch auto gagal), agar terlihat seperti error key/model di detail.
	if len(vis) > 0 && f.cursor >= 0 && f.cursor < len(cards) {
		if msg := m.provLevelErr(cards[f.cursor].slug); msg != "" {
			vis[len(vis)-1] = styleErr.Render(trunc(fmt.Sprintf("provider %q error: %s", cards[f.cursor].slug, msg), inner))
		}
	}
	return styleWin.Width(m.width-2).Render(strings.Join(vis, "\n")) + "\n"
}

// ensureProvidersVisible keeps the cursor row inside the table viewport.
func (m *Model) ensureProvidersVisible() {
	f := m.cur()
	if f.pg != pgProviders {
		return
	}
	n := len(m.provCards())
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

// provDot memilih style dot baris key/model: abu (provider disabled),
// kuning (sedang di-test), merah (flagged), hijau (enabled), abu (disabled).
// Provider yang disabled membuat semua dot key & model-nya abu-abu, karena
// provider itu tidak melayani request apa pun.
func (m Model) provDot(slug string, isKey bool, name string, on, provOn bool) lipgloss.Style {
	switch {
	case !provOn:
		return styleDotOff
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
// Provider disabled tidak menampilkan flag sama sekali (dot-nya juga abu-abu),
// karena flag lama jadi tidak relevan.
func (m Model) provErrLine(slug string, cursor int) string {
	if m.deps.Store == nil || m.deps.Store.ProvHealth == nil {
		return ""
	}
	if _, p := lookupProv(m.deps.Store.Get(), slug); p == nil || !bool(p.Enabled) {
		return ""
	}
	// Provider-level dulu (models:auto fetch gagal): tampil tanpa peduli cursor.
	if msg := m.deps.Store.ProvHealth.ErrMsg(service.ProvProviderID(slug)); msg != "" {
		return fmt.Sprintf("provider %q error: %s", slug, msg)
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

	// Judul: indikator status di depan nama (provider-level auto diutamakan).
	ds := styleDotOn
	if !bool(p.Enabled) {
		ds = styleDotOff
	} else if m.deps.Store != nil && m.deps.Store.ProvHealth != nil {
		pid := service.ProvProviderID(slug)
		switch {
		case m.deps.Store.ProvHealth.IsTesting(pid):
			ds = styleDotTest
		case m.deps.Store.ProvHealth.IsError(pid):
			ds = styleDotErr
		}
	}
	out = append(out, dline{[]dseg{{"• ", ds}, {p.Name, styleDetName}}})
	out = append(out, dline{[]dseg{{"", styleBody}}})

	// Seksi 1: informasi dasar.
	out = append(out, dline{[]dseg{{"Basic info", styleSecHead}}})
	proxy := service.ProxyModeLabel(snap, p)
	modelsInfo := fmt.Sprintf("%d models", len(p.Models))
	if p.IsModelsAuto() {
		n := 0
		for _, ref := range snap.ModelMap {
			if ref != nil && ref.ProviderSlug == slug {
				n++
			}
		}
		modelsInfo = fmt.Sprintf("auto (%d fetched)", n)
		if ph := m.deps.Store.ProvHealth; ph != nil {
			pid := service.ProvProviderID(slug)
			if phase, done, total, ok := ph.Progress(pid); ok {
				// Dua fase: fetching (unduh daftar) lalu identifying (probe
				// endpoint per model free, dengan progres done/total).
				if phase == service.ProvPhaseIdentifying {
					modelsInfo = fmt.Sprintf("identifying (%d/%d)", done, total)
				} else {
					modelsInfo = "fetching"
				}
			} else if ph.IsError(pid) {
				modelsInfo = "auto (error)"
			}
		}
	}
	info := [][2]string{
		{"prefix", p.Prefix},
		{"type", p.Type},
		{"interface", p.Interface},
		{"base-url", p.BaseURL},
		{"proxy", proxy},
		{"models", modelsInfo},
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
	provOn := bool(p.Enabled)
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
			ds := m.provDot(slug, true, r.name, r.on, provOn)
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

	// Seksi 3: models (auto: dari ModelMap hasil fetch, limit tampil "-").
	mslugs := m.provModelSlugs(snap, slug, p)

	out = append(out, dline{[]dseg{{"", styleBody}}})
	out = append(out, dline{[]dseg{{strings.Repeat("─", inner), styleSep}}})
	modelsHead := fmt.Sprintf("Models (%d)", len(mslugs))
	if p.IsModelsAuto() {
		if ph := m.deps.Store.ProvHealth; ph != nil {
			if phase, _, total, ok := ph.Progress(service.ProvProviderID(slug)); ok && phase == service.ProvPhaseIdentifying && total > 0 {
				modelsHead = fmt.Sprintf("Models (%d/%d)", len(mslugs), total)
			}
		}
	}
	out = append(out, dline{[]dseg{{modelsHead, styleSecHead}}})
	if len(mslugs) == 0 {
		if p.IsModelsAuto() {
			ph := m.deps.Store.ProvHealth
			pid := service.ProvProviderID(slug)
			switch {
			case ph != nil && ph.IsTesting(pid):
				if phase, done, total, ok := ph.Progress(pid); ok && phase == service.ProvPhaseIdentifying {
					out = append(out, dline{[]dseg{{fmt.Sprintf("(identifying models… %d/%d)", done, total), styleDetLbl}}})
				} else {
					out = append(out, dline{[]dseg{{"(fetching models…)", styleDetLbl}}})
				}
			case ph != nil && ph.IsError(pid):
				out = append(out, dline{[]dseg{{"(fetch failed — see error below)", styleDetLbl}}})
			default:
				out = append(out, dline{[]dseg{{"(no models fetched yet)", styleDetLbl}}})
			}
		} else {
			out = append(out, dline{[]dseg{{"(no models)", styleDetLbl}}})
		}
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
			if p.IsModelsAuto() {
				// Hasil fetch: nama=id, limit unknown ("-"), created dari upstream.
				r.on = true
				r.ctx, r.out = "-", "-"
				if ref := snap.ModelMap[p.Prefix+"/"+ms]; ref != nil && ref.Model != nil {
					if strings.TrimSpace(ref.Model.Name) != "" {
						r.name = ref.Model.Name
					}
					if int64(ref.Model.Created) > 0 {
						r.created = time.Unix(int64(ref.Model.Created), 0).Format("2006-01-02")
					} else {
						r.created = "-"
					}
				} else {
					r.created = "-"
				}
			} else if md != nil {
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
			ds := m.provDot(slug, false, r.slug, r.on, provOn)
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

// provModelSlugs mengembalikan slug model terurut untuk detail/rows:
// auto → dari ModelMap hasil fetch; statis → dari p.Models.
func (m Model) provModelSlugs(snap *config.Snapshot, slug string, p *config.Provider) []string {
	if p == nil {
		return nil
	}
	if p.IsModelsAuto() {
		var out []string
		for _, ref := range snap.ModelMap {
			if ref != nil && ref.ProviderSlug == slug {
				out = append(out, ref.ModelSlug)
			}
		}
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(p.Models))
	for ms := range p.Models {
		out = append(out, ms)
	}
	sort.Strings(out)
	return out
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
	mslugs := m.provModelSlugs(snap, slug, p)
	for _, ms := range mslugs {
		if p.IsModelsAuto() {
			out = append(out, provRowID{name: ms, label: ms, on: true})
			continue
		}
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
