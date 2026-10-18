package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"basicroute/app/cli"
	"basicroute/app/service"
)

// proxyTestTimeout adalah batas waktu satu test proxy.
const proxyTestTimeout = 15 * time.Second

// Version is shown in the header after the app name.
const Version = "v1.0"

// Deps are read-only dependencies for the TUI.
type Deps struct {
	Store    *service.Store
	BaseURL  string // e.g. http://0.0.0.0:21172
	Start    time.Time
	Logs     *LogRing
	OnReload func(reason string)
	OnQuit   func()
}

type page int

const (
	pgHome page = iota
	pgAPIKeys
	pgLogs
	pgProviders
	pgProvider
	pgProxies
	pgProxy
	pgOutput
)

// output kinds for the generic page.
const (
	outHelp   = "help"
	outStatus = "status"
	outSystem = "system"
	outModels = "models"
	outStats  = "stats"
)

// frame is one navigation level. Esc goes back to the previous frame.
type frame struct {
	pg         page
	arg        string       // provider slug / pool name / output kind
	filter     string       // filter models (output kind models)
	cursor     int          // selection index (home menu / cards / list)
	scroll     int          // content scroll offset (card pages: row index)
	tail       bool         // logs: follow new lines
	revealKeys bool         // api keys: show keys in plain text without masking
	sel        map[int]bool // proxy: index kartu yang terseleksi (multi)
	anchor     int          // provider detail: jangkar shift-select ala Explorer
}

var homeMenu = []struct {
	label string
	pg    page
}{
	{"Api Key", pgAPIKeys},
	{"Logs", pgLogs},
	{"Provider", pgProviders},
	{"Proxy", pgProxies},
}

// Model is the bubbletea state. All data is read live from the Store.
type Model struct {
	deps    Deps
	cliDeps cli.Deps
	stack   []frame
	input   textinput.Model
	cmdMode bool
	status  string
	isErr   bool
	statusT time.Time
	width   int
	height  int
	// cache log
	logLines []string
	logVer   uint64
	// cache usage api keys: refresh max tiap 3 detik dan hanya saat
	// halaman Api Key dibuka, agar tidak poll terus di halaman lain.
	apiUsage   map[string]uint64
	apiLast    map[string]int64
	apiUsageAt time.Time
	// antrean test berurutan (bulk test ala anchor): satu job dijalankan,
	// hasilnya diterapkan, lalu lanjut job berikutnya.
	testQueue    []testJob
	testKind     string
	testPending  int
	testOK       int
	testErr      int
	testFirstErr string
	testReported bool
}

// New membangun Model awal (halaman home).
func New(d Deps) Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "type a command…  (\"help\" for the list)"
	ti.CharLimit = 200
	m := Model{
		deps:    d,
		cliDeps: cli.Deps{Store: d.Store, Addr: d.BaseURL, Start: d.Start},
		stack:   []frame{{pg: pgHome, tail: true}},
		input:   ti,
	}
	if d.Logs != nil {
		m.logLines, m.logVer = d.Logs.Snapshot()
	}
	return m
}

// Run menjalankan TUI fullscreen (alt-screen) hingga quit.
func Run(d Deps) error {
	p := tea.NewProgram(New(d), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type tickMsg time.Time

type reloadDoneMsg struct{}

// proxyTestResult adalah hasil test satu proxy.
type proxyTestResult struct {
	name string
	err  error
}

// proxyTestResultMsg dikirim setelah sekumpulan test proxy selesai.
type proxyTestResultMsg struct {
	results []proxyTestResult
}

// ---- antrean test berurutan (bulk test ala anchor) ----
//
// Satu job dijalankan, hasilnya diterapkan (kuning -> hijau/merah), lalu
// job berikutnya. Karena hasil tiap job masuk sebagai pesan terpisah,
// indikator kuning terlihat bergerak satu per satu.

// testJob adalah satu item yang akan di-test berurutan.
type testJob struct {
	kind  string // "proxy" | "key" | "model"
	slug  string // provider slug (khusus key/model)
	name  string // nama pool / nama key / slug model
	label string // label tampil untuk pesan
}

// testKindPlural mengembalikan kata benda jamak untuk pesan ringkas.
func testKindPlural(kind string, n int) string {
	if n == 1 {
		return kind
	}
	switch kind {
	case "proxy":
		return "proxies"
	default:
		return kind + "s"
	}
}

// startTests memulai antrean test: semua job ditandai kuning lebih dulu,
// lalu dijalankan satu per satu. msg kosong = pesan status default.
func (m *Model) startTests(jobs []testJob, msg string) tea.Cmd {
	if len(jobs) == 0 {
		m.say("nothing to test", true)
		return nil
	}
	if m.testPending > 0 {
		m.say("a test is already running…", true)
		return nil
	}
	m.testQueue = jobs
	m.testKind = jobs[0].kind
	m.testPending = len(jobs)
	m.testOK, m.testErr, m.testFirstErr = 0, 0, ""
	m.testReported = false
	if msg == "" {
		if len(jobs) == 1 {
			msg = fmt.Sprintf("testing %s %q…", jobs[0].kind, jobs[0].label)
		} else {
			msg = fmt.Sprintf("testing %d %s…", len(jobs), testKindPlural(jobs[0].kind, len(jobs)))
		}
	}
	m.say(msg, false)
	if cmd := m.nextTest(); cmd != nil {
		return cmd
	}
	m.finishTests()
	return nil
}

// nextTest mengambil job berikutnya, menandainya kuning (sedang di-test),
// lalu mengembalikan cmd-nya; job yang tak bisa dijalankan (mis. key hilang
// setelah reload) dilewati sebagai error.
func (m *Model) nextTest() tea.Cmd {
	for len(m.testQueue) > 0 {
		j := m.testQueue[0]
		m.testQueue = m.testQueue[1:]
		m.setTesting(j, true) // kuning hanya untuk item yang sedang berjalan
		if cmd := m.runTest(j); cmd != nil {
			return cmd
		}
		m.setTesting(j, false)
		m.tallyTest(fmt.Errorf("cannot test (missing or invalid)"), j.label)
	}
	return nil
}

// runTest memilih cmd test sesuai jenis job.
func (m Model) runTest(j testJob) tea.Cmd {
	switch j.kind {
	case "proxy":
		return m.testProxyCmd(j.name)
	case "key":
		return m.testProvKeyCmd(j.slug, j.name)
	case "model":
		return m.testProvModelCmd(j.slug, j.name)
	}
	return nil
}

// setTesting menandai/melepas indikator kuning untuk satu job.
func (m *Model) setTesting(j testJob, on bool) {
	if m.deps.Store == nil {
		return
	}
	switch j.kind {
	case "proxy":
		if m.deps.Store.ProxyHealth == nil {
			return
		}
		if on {
			m.deps.Store.ProxyHealth.SetTesting(j.name)
		} else {
			m.deps.Store.ProxyHealth.ClearTesting(j.name)
		}
	case "key":
		if m.deps.Store.ProvHealth == nil {
			return
		}
		id := service.ProvKeyID(j.slug, j.name)
		if on {
			m.deps.Store.ProvHealth.SetTesting(id)
		} else {
			m.deps.Store.ProvHealth.ClearTesting(id)
		}
	case "model":
		if m.deps.Store.ProvHealth == nil {
			return
		}
		id := service.ProvModelID(j.slug, j.name)
		if on {
			m.deps.Store.ProvHealth.SetTesting(id)
		} else {
			m.deps.Store.ProvHealth.ClearTesting(id)
		}
	}
}

// tallyTest mengumpulkan statistik hasil test untuk ringkasan batch.
func (m *Model) tallyTest(err error, label string) {
	if err != nil {
		m.testErr++
		if m.testFirstErr == "" {
			m.testFirstErr = label + ": " + err.Error()
		}
		return
	}
	m.testOK++
}

// afterTest melanjutkan antrean; bila habis, menampilkan ringkasan.
func (m *Model) afterTest() tea.Cmd {
	if cmd := m.nextTest(); cmd != nil {
		return cmd
	}
	m.finishTests()
	return nil
}

// finishTests menutup batch berjalan dan melaporkan ringkasannya.
func (m *Model) finishTests() {
	if m.testPending <= 0 {
		return
	}
	n := m.testOK + m.testErr
	switch {
	case n == 0:
		m.say("nothing to test", true)
	case m.testPending == 1 && m.testReported:
		// pesan per-item sudah cukup untuk test tunggal
	case m.testErr == 0:
		m.say(fmt.Sprintf("%s test ok: %d/%d", m.testKind, m.testOK, n), false)
	default:
		m.say(fmt.Sprintf("%s test: %d ok, %d error — %s", m.testKind, m.testOK, m.testErr, m.testFirstErr), true)
	}
	m.testPending = 0
}

// testProxyCmd menguji satu pool proxy (async) dan mengirim hasilnya.
func (m Model) testProxyCmd(name string) tea.Cmd {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	p := snap.Raw.ProxyPools[name]
	if p == nil || !bool(p.Enabled) {
		return nil
	}
	testURL := snap.Raw.System.ProxyTestURL
	typ := p.Type
	raw := strings.TrimSpace(p.URL)
	return func() tea.Msg {
		return proxyTestResultMsg{results: []proxyTestResult{{
			name: name,
			err:  service.TestProxy(typ, raw, testURL, proxyTestTimeout),
		}}}
	}
}

// keyTestResult adalah hasil test satu provider key.
type keyTestResult struct {
	slug, keyName string
	err           error
}

// keyTestResultMsg dikirim setelah test satu key selesai.
type keyTestResultMsg struct {
	res keyTestResult
}

// modelTestResult adalah hasil test satu model: iterasi key provider,
// key yang auth-invalid ikut dilaporkan agar di-flag juga.
type modelTestResult struct {
	slug, modelSlug string
	err             error
	flagModel       bool              // false bila gagal karena semua key invalid
	usedKey         string            // key yang berhasil (saat err == nil)
	badKeys         map[string]string // keyName -> pesan error auth
}

// modelTestResultMsg dikirim setelah test satu model selesai.
type modelTestResultMsg struct {
	res modelTestResult
}

// provTestParams merangkum parameter test untuk satu provider.
type provTestParams struct {
	slug, base, provType, iface, ptype, purl string
	timeout                                  time.Duration
	ok                                       bool
}

// provTestParamsFor membaca parameter test provider dari snapshot live.
func (m Model) provTestParamsFor(slug string) (provTestParams, error) {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return provTestParams{}, fmt.Errorf("snapshot not ready")
	}
	_, p := lookupProv(snap, slug)
	if p == nil {
		return provTestParams{}, fmt.Errorf("provider %q not found", slug)
	}
	ptype, purl, err := service.ResolveProxy(snap, p)
	if err != nil {
		return provTestParams{}, err
	}
	return provTestParams{
		slug:     slug,
		base:     strings.TrimSpace(p.BaseURL),
		provType: p.Type,
		iface:    strings.TrimSpace(p.Interface),
		ptype:    ptype,
		purl:     purl,
		timeout:  service.TestTimeout(int(p.TimeoutSecs)),
		ok:       true,
	}, nil
}

// testProvKeyCmd menguji satu provider key (async) via GET /models.
func (m Model) testProvKeyCmd(slug, keyName string) tea.Cmd {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	_, p := lookupProv(snap, slug)
	if p == nil {
		return nil
	}
	k := p.Keys[keyName]
	if k == nil {
		return nil
	}
	prm, err := m.provTestParamsFor(slug)
	if err != nil || !prm.ok {
		return nil
	}
	keyVal := k.Key
	return func() tea.Msg {
		return keyTestResultMsg{res: keyTestResult{
			slug:    slug,
			keyName: keyName,
			err:     service.TestProvKey(prm.base, prm.provType, prm.ptype, prm.purl, keyVal, prm.timeout),
		}}
	}
}

// testProvModelCmd menguji satu model (async): coba key provider berurutan,
// key yang auth-invalid dilewati (ikut di-flag), sukses 2xx apa pun = ok.
func (m Model) testProvModelCmd(slug, modelSlug string) tea.Cmd {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	prm, err := m.provTestParamsFor(slug)
	if err != nil || !prm.ok {
		return nil
	}
	ring := snap.KeyRings[slug]
	type kk struct{ name, val string }
	var keys []kk
	for _, ke := range ring {
		if ke == nil {
			continue
		}
		keys = append(keys, kk{name: ke.Name, val: ke.Key})
	}
	if len(keys) == 0 {
		return func() tea.Msg {
			return modelTestResultMsg{res: modelTestResult{
				slug: slug, modelSlug: modelSlug,
				err: fmt.Errorf("no enabled keys for provider %q", slug),
			}}
		}
	}
	return func() tea.Msg {
		res := modelTestResult{slug: slug, modelSlug: modelSlug, badKeys: map[string]string{}}
		for _, k := range keys {
			err := service.TestProvModel(prm.base, prm.provType, prm.iface, prm.ptype, prm.purl, modelSlug, k.val, prm.timeout)
			if err == nil {
				res.usedKey = k.name
				return modelTestResultMsg{res: res}
			}
			if service.IsAuthErr(err) {
				res.badKeys[k.name] = err.Error()
				continue
			}
			res.err = err
			res.flagModel = true
			return modelTestResultMsg{res: res}
		}
		res.err = fmt.Errorf("no api key is valid for provider %q (%d tried)", slug, len(keys))
		return modelTestResultMsg{res: res}
	}
}

// applyKeyTestResult menyimpan hasil test key ke memory dan (bila report)
// melapor per-item. report=false saat bagian dari batch (ringkasan di akhir).
func (m *Model) applyKeyTestResult(r keyTestResult, report bool) {
	if m.deps.Store != nil && m.deps.Store.ProvHealth != nil {
		id := service.ProvKeyID(r.slug, r.keyName)
		if r.err != nil {
			m.deps.Store.ProvHealth.SetError(id, r.err.Error())
		} else {
			m.deps.Store.ProvHealth.SetOK(id)
		}
	}
	if !report {
		return
	}
	m.testReported = true
	if r.err != nil {
		m.say(fmt.Sprintf("key test %q error — %s", r.keyName, r.err.Error()), true)
	} else {
		m.say(fmt.Sprintf("key test ok: %q", r.keyName), false)
	}
}

// applyModelTestResult menyimpan hasil test model (+ key invalid) dan
// (bila report) melapor per-item.
func (m *Model) applyModelTestResult(r modelTestResult, report bool) {
	if m.deps.Store != nil && m.deps.Store.ProvHealth != nil {
		ph := m.deps.Store.ProvHealth
		for kn, msg := range r.badKeys {
			ph.SetError(service.ProvKeyID(r.slug, kn), msg)
		}
		mid := service.ProvModelID(r.slug, r.modelSlug)
		switch {
		case r.err == nil:
			ph.SetOK(mid)
			if r.usedKey != "" {
				ph.SetOK(service.ProvKeyID(r.slug, r.usedKey))
			}
		case r.flagModel:
			ph.SetError(mid, r.err.Error())
		}
	}
	if !report {
		return
	}
	m.testReported = true
	switch {
	case r.err == nil:
		if len(r.badKeys) > 0 {
			m.say(fmt.Sprintf("model test ok: %q (via %q, %d invalid key skipped)", r.modelSlug, r.usedKey, len(r.badKeys)), false)
		} else {
			m.say(fmt.Sprintf("model test ok: %q", r.modelSlug), false)
		}
	default:
		m.say(fmt.Sprintf("model test %q error — %s", r.modelSlug, r.err.Error()), true)
	}
}

func tickCmd() tea.Msg {
	return tickMsg(time.Now())
}

func (m Model) Init() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) cur() frame { return m.stack[len(m.stack)-1] }

func (m *Model) setCur(f frame) { m.stack[len(m.stack)-1] = f }

func (m *Model) push(f frame) {
	m.stack = append(m.stack, f)
	if f.pg == pgAPIKeys {
		m.refreshApiUsage()
	}
}

func (m *Model) pop() {
	if len(m.stack) > 1 {
		m.stack = m.stack[:len(m.stack)-1]
	}
}

func (m *Model) say(msg string, isErr bool) {
	m.status = msg
	m.isErr = isErr
	m.statusT = time.Now()
}

// providerSlugs mengembalikan slug provider terurut (live dari Store).
func (m Model) providerSlugs() []string {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	out := make([]string, 0, len(snap.Raw.Providers))
	for s := range snap.Raw.Providers {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// poolNames mengembalikan nama proxy pool terurut urutan pemakaian
// (order asc, nama asc) — live dari Store, konsisten dengan mode auto/round.
func (m Model) poolNames() []string {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	if len(snap.ProxyOrder) > 0 {
		out := make([]string, 0, len(snap.ProxyOrder))
		for _, n := range snap.ProxyOrder {
			if snap.Raw.ProxyPools[n] != nil {
				out = append(out, n)
			}
		}
		return out
	}
	out := make([]string, 0, len(snap.Raw.ProxyPools))
	for n := range snap.Raw.ProxyPools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// sysKeyNames mengembalikan nama system key terurut (live dari Store).
func (m Model) sysKeyNames() []string {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	out := make([]string, 0, len(snap.Raw.System.Keys))
	for n := range snap.Raw.System.Keys {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// refreshApiUsage menarik snapshot usage dari memori (bukan file).
func (m *Model) refreshApiUsage() {
	if m.deps.Store == nil || m.deps.Store.Data == nil {
		m.apiUsage = map[string]uint64{}
		m.apiLast = map[string]int64{}
		m.apiUsageAt = time.Now()
		return
	}
	m.apiUsage = m.deps.Store.Data.UsageSnapshot()
	m.apiLast = m.deps.Store.Data.UsageSnapshotLast()
	m.apiUsageAt = time.Now()
}

// apiUsageTotal membaca dari cache 3-detik (hash -> total).
func (m Model) apiUsageTotal(hash string) uint64 {
	if hash == "" {
		return 0
	}
	return m.apiUsage[hash]
}

// apiUsageLastUnix membaca waktu terakhir request dari cache 3-detik.
// 0 = belum pernah tercatat.
func (m Model) apiUsageLastUnix(hash string) int64 {
	if hash == "" {
		return 0
	}
	return m.apiLast[hash]
}

// ensureApiKeysVisible menggeser scroll agar cursor terlihat (header fixed).
func (m *Model) ensureApiKeysVisible() {
	f := m.cur()
	if f.pg != pgAPIKeys {
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

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Update adalah mesin navigasi: panah untuk memilih, Enter membuka halaman.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = max(msg.Width-8, 20)
		m.clampAll()
		return m, nil

	case tickMsg:
		// Status transien kedaluwarsa setelah 5 detik.
		if m.status != "" && time.Since(m.statusT) > 5*time.Second {
			m.status = ""
			m.isErr = false
		}
		// Tarik log baru dari ring.
		if m.deps.Logs != nil {
			lines, ver := m.deps.Logs.Snapshot()
			if ver != m.logVer {
				m.logLines = lines
				m.logVer = ver
				f := m.cur()
				if f.pg == pgLogs && f.tail {
					f.scroll = 1 << 30 // dijepit ke bawah di bawah
					m.setCur(f)
				}
			}
		}
		// Refresh usage api keys max tiap 3 detik dan hanya saat halamannya dibuka.
		if m.cur().pg == pgAPIKeys && time.Since(m.apiUsageAt) >= 3*time.Second {
			m.refreshApiUsage()
		}
		m.clampAll()
		return m, tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })

	case reloadDoneMsg:
		m.say("reload done — see Logs for details", false)
		return m, nil

	case proxyTestResultMsg:
		for _, r := range msg.results {
			m.applyProxyTestResult(r, m.testPending <= 1)
			m.tallyTest(r.err, r.name)
		}
		return m, m.afterTest()

	case keyTestResultMsg:
		m.applyKeyTestResult(msg.res, m.testPending <= 1)
		m.tallyTest(msg.res.err, msg.res.keyName)
		return m, m.afterTest()

	case modelTestResultMsg:
		m.applyModelTestResult(msg.res, m.testPending <= 1)
		m.tallyTest(msg.res.err, msg.res.modelSlug)
		return m, m.afterTest()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applyProxyTestResult menyimpan status error/ok satu pool ke memory dan
// (bila report) melapor per-item.
func (m *Model) applyProxyTestResult(r proxyTestResult, report bool) {
	if m.deps.Store != nil && m.deps.Store.ProxyHealth != nil {
		if r.err != nil {
			m.deps.Store.ProxyHealth.SetError(r.name, r.err.Error())
			m.deps.Store.NoteProxyResult(r.name, false)
		} else {
			m.deps.Store.ProxyHealth.SetOK(r.name)
			m.deps.Store.NoteProxyResult(r.name, true)
		}
	}
	if !report {
		return
	}
	m.testReported = true
	if r.err != nil {
		m.say(fmt.Sprintf("proxy test %q error — %s", r.name, r.err.Error()), true)
	} else {
		m.say(fmt.Sprintf("proxy test ok: %q", r.name), false)
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Mode ketik perintah: semua tombol milik input, kecuali Enter/Esc/Ctrl+C.
	if m.cmdMode {
		switch key {
		case "ctrl+c":
			return m.quit()
		case "esc":
			m.cmdMode = false
			m.input.Blur()
			m.input.Reset()
			return m, nil
		case "enter":
			line := strings.TrimSpace(m.input.Value())
			m.cmdMode = false
			m.input.Blur()
			m.input.Reset()
			if line != "" {
				return m.execCommand(line)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// Mode navigasi.
	switch key {
	case "ctrl+c", "q":
		return m.quit()
	case "esc":
		m.pop()
		m.status = ""
		return m, nil
	case "/":
		m.cmdMode = true
		return m, m.input.Focus()
	case "r":
		m.say("reloading…", false)
		return m, func() tea.Msg {
			if m.deps.OnReload != nil {
				m.deps.OnReload("ui")
			}
			return reloadDoneMsg{}
		}
	}

	f := m.cur()
	switch f.pg {
	case pgHome:
		switch key {
		case "up", "k":
			f.cursor = (f.cursor + len(homeMenu) - 1) % len(homeMenu)
			m.setCur(f)
		case "down", "j":
			f.cursor = (f.cursor + 1) % len(homeMenu)
			m.setCur(f)
		case "enter", " ":
			m.push(frame{pg: homeMenu[f.cursor].pg, tail: true})
		}
	case pgProviders:
		cards := m.provCards()
		n := len(cards)
		if n > 0 {
			f.cursor = clamp(f.cursor, 0, n-1)
		}
		// Linear table navigation, single cursor.
		step := m.viewportH() - 1
		if step < 1 {
			step = 1
		}
		switch key {
		case "up", "k":
			if n > 0 && f.cursor > 0 {
				f.cursor--
				m.setCur(f)
				m.ensureProvidersVisible()
			}
		case "down", "j":
			if n > 0 && f.cursor < n-1 {
				f.cursor++
				m.setCur(f)
				m.ensureProvidersVisible()
			}
		case "enter", " ":
			if f.cursor >= 0 && f.cursor < n {
				m.push(frame{pg: pgProvider, arg: cards[f.cursor].slug})
			}
		case "pgup":
			if n > 0 {
				f.cursor = clamp(f.cursor-step, 0, n-1)
				m.setCur(f)
				m.ensureProvidersVisible()
			}
		case "pgdown":
			if n > 0 {
				f.cursor = clamp(f.cursor+step, 0, n-1)
				m.setCur(f)
				m.ensureProvidersVisible()
			}
		case "home", "g":
			if n > 0 {
				f.cursor = 0
				m.setCur(f)
				m.ensureProvidersVisible()
			}
		case "end", "G":
			if n > 0 {
				f.cursor = n - 1
				m.setCur(f)
				m.ensureProvidersVisible()
			}
		}
	case pgAPIKeys:
		names := m.sysKeyNames()
		n := len(names)
		if n > 0 {
			f.cursor = clamp(f.cursor, 0, n-1)
			f.anchor = clamp(f.anchor, 0, n-1)
		}
		// Navigasi tabel model anchor Explorer (satu seksi: 0..n-1).
		nav := func() bool {
			switch key {
			case "up", "k":
				if f.cursor <= 0 {
					return true
				}
				selCollapse(&f, f.cursor-1, n)
			case "down", "j":
				if f.cursor >= n-1 {
					return true
				}
				selCollapse(&f, f.cursor+1, n)
			case "shift+up", "K":
				if f.cursor <= 0 {
					return true
				}
				selExtend(&f, f.cursor-1, 0, n-1)
			case "shift+down", "J":
				if f.cursor >= n-1 {
					return true
				}
				selExtend(&f, f.cursor+1, 0, n-1)
			case "shift+home":
				selExtend(&f, 0, 0, n-1)
			case "shift+end":
				selExtend(&f, n-1, 0, n-1)
			case "ctrl+up", "ctrl+k":
				selCollapse(&f, 0, n)
			case "ctrl+down", "ctrl+j":
				selCollapse(&f, n-1, n)
			case "ctrl+shift+up":
				selExtend(&f, 0, 0, n-1)
			case "ctrl+shift+down":
				selExtend(&f, n-1, 0, n-1)
			case "ctrl+a":
				selSectionAll(&f, 0, n-1)
				m.setCur(f)
				return true
			case " ":
				selCollapse(&f, f.cursor, n)
			case "pgup":
				selCollapse(&f, f.cursor-(m.viewportH()-1), n)
			case "pgdown":
				selCollapse(&f, f.cursor+(m.viewportH()-1), n)
			case "home", "g":
				selCollapse(&f, 0, n)
			case "end", "G":
				selCollapse(&f, n-1, n)
			default:
				return false
			}
			m.setCur(f)
			m.ensureApiKeysVisible()
			return true
		}
		if n > 0 && nav() {
			break
		}
		switch key {
		case "x", "X":
			f.revealKeys = !f.revealKeys
			if f.revealKeys {
				m.say("all keys shown in plain text (x: mask)", false)
			} else {
				m.say("keys masked (x: reveal)", false)
			}
			m.setCur(f)
		}
	case pgProxies:
		cards := m.proxyCards()
		n := len(cards)
		if n > 0 {
			f.cursor = clamp(f.cursor, 0, n-1)
			f.anchor = clamp(f.anchor, 0, n-1)
		}
		// Navigasi tabel model anchor Explorer (satu seksi: 0..n-1).
		nav := func() bool {
			switch key {
			case "up", "k":
				if f.cursor <= 0 {
					return true
				}
				selCollapse(&f, f.cursor-1, n)
			case "down", "j":
				if f.cursor >= n-1 {
					return true
				}
				selCollapse(&f, f.cursor+1, n)
			case "shift+up", "K":
				if f.cursor <= 0 {
					return true
				}
				selExtend(&f, f.cursor-1, 0, n-1)
			case "shift+down", "J":
				if f.cursor >= n-1 {
					return true
				}
				selExtend(&f, f.cursor+1, 0, n-1)
			case "shift+home":
				selExtend(&f, 0, 0, n-1)
			case "shift+end":
				selExtend(&f, n-1, 0, n-1)
			case "ctrl+up", "ctrl+k":
				selCollapse(&f, 0, n)
			case "ctrl+down", "ctrl+j":
				selCollapse(&f, n-1, n)
			case "ctrl+shift+up":
				selExtend(&f, 0, 0, n-1)
			case "ctrl+shift+down":
				selExtend(&f, n-1, 0, n-1)
			case "ctrl+a":
				selSectionAll(&f, 0, n-1)
				m.setCur(f)
				return true
			case " ":
				selCollapse(&f, f.cursor, n)
			case "pgup":
				selCollapse(&f, f.cursor-(m.viewportH()-1), n)
			case "pgdown":
				selCollapse(&f, f.cursor+(m.viewportH()-1), n)
			case "home", "g":
				selCollapse(&f, 0, n)
			case "end", "G":
				selCollapse(&f, n-1, n)
			default:
				return false
			}
			m.setCur(f)
			m.ensureProxyVisible()
			return true
		}
		if n > 0 && nav() {
			break
		}
		switch key {
		case "t":
			if f.cursor >= 0 && f.cursor < n {
				c := cards[f.cursor]
				if !c.enabled {
					m.say("proxy "+c.name+" is disabled — cannot test", true)
					return m, nil
				}
				return m, m.startTests([]testJob{{kind: "proxy", name: c.name, label: c.name}}, "")
			}
		case "y":
			names, skipped := selectedProxyNames(cards, f)
			if len(names) == 0 {
				if skipped > 0 {
					m.say("selected proxy is disabled — cannot test", true)
				} else {
					m.say("no proxy selected", true)
				}
				return m, nil
			}
			jobs := make([]testJob, 0, len(names))
			for _, nm := range names {
				jobs = append(jobs, testJob{kind: "proxy", name: nm, label: nm})
			}
			msg := ""
			if skipped > 0 {
				msg = fmt.Sprintf("testing %d %s (%d disabled skipped)…", len(names), testKindPlural("proxy", len(names)), skipped)
			}
			return m, m.startTests(jobs, msg)
		case "enter":
			if f.cursor >= 0 && f.cursor < n {
				m.push(frame{pg: pgProxy, arg: cards[f.cursor].name})
			}
		}
	case pgLogs:
		switch key {
		case "up", "k":
			f.tail = false
			f.scroll--
			m.setCur(f)
		case "down", "j":
			f.scroll++
			m.setCur(f)
			// jika mencapai bawah, kunci tail lagi
			m.snapTail()
		case "pgup":
			f.tail = false
			f.scroll -= m.viewportH()
			m.setCur(f)
		case "pgdown":
			f.scroll += m.viewportH()
			m.setCur(f)
			m.snapTail()
		case "home", "g":
			f.tail = false
			f.scroll = 0
			m.setCur(f)
		case "end", "G":
			f.tail = true
			f.scroll = 1 << 30
			m.setCur(f)
		}
	case pgProvider:
		lines, keyLines, modelLines := m.providerDetail(f)
		nk := len(keyLines)
		n := nk + len(modelLines)
		if n == 0 {
			// Fallback (mis. not found): tetap bisa scroll biasa.
			f = scrollFrame(f, key, m.viewportH())
			m.setCur(f)
			break
		}
		f.cursor = clamp(f.cursor, 0, n-1)
		f.anchor = clamp(f.anchor, 0, n-1)
		lo, hi := provSection(f.cursor, nk, n)
		move := func() {
			m.setCur(f)
			m.ensureProviderVisible()
		}
		switch key {
		case "up", "k":
			if f.cursor <= 0 {
				// Mentok baris paling atas (row 1 Keys): geser container
				// ke atas agar seksi Basic info bisa terlihat lagi.
				// Tanpa ini scroll terkunci setelah scroll down.
				if f.scroll > 0 {
					f.scroll--
					m.setCur(f)
				}
				break
			}
			selCollapse(&f, f.cursor-1, n) // tembus antar seksi
			move()
		case "down", "j":
			if f.cursor >= n-1 {
				// Simetris: mentok baris paling bawah, geser container
				// ke bawah satu baris (dijepit ke maxScroll).
				maxS := maxScroll(len(lines), m.viewportH())
				if f.scroll < maxS {
					f.scroll++
					m.setCur(f)
				}
				break
			}
			selCollapse(&f, f.cursor+1, n)
			move()
		case "shift+up", "K":
			if f.cursor <= lo {
				// Hanya di ujung absolut atas (lo==0) boleh scroll;
				// batas antar-seksi tetap diam agar seleksi tak hilang.
				if lo == 0 && f.scroll > 0 {
					f.scroll--
					m.setCur(f)
				}
				break // ujung seksi: diam, jangan tembus/pindah
			}
			selExtend(&f, f.cursor-1, lo, hi)
			move()
		case "shift+down", "J":
			if f.cursor >= hi {
				if hi == n-1 {
					maxS := maxScroll(len(lines), m.viewportH())
					if f.scroll < maxS {
						f.scroll++
						m.setCur(f)
					}
				}
				break
			}
			selExtend(&f, f.cursor+1, lo, hi)
			move()
		case "shift+home":
			selExtend(&f, lo, lo, hi)
			move()
		case "shift+end":
			selExtend(&f, hi, lo, hi)
			move()
		case "ctrl+up", "ctrl+k":
			selCollapse(&f, lo, n) // awal seksi
			move()
		case "ctrl+down", "ctrl+j":
			selCollapse(&f, hi, n) // akhir seksi
			move()
		case "ctrl+shift+up":
			selExtend(&f, lo, lo, hi)
			move()
		case "ctrl+shift+down":
			selExtend(&f, hi, lo, hi)
			move()
		case "ctrl+a":
			selSectionAll(&f, lo, hi)
			m.setCur(f)
		case " ":
			selCollapse(&f, f.cursor, n) // space = seleksi yang terfokus saja
			move()
		case "t":
			// Test baris fokus, atau semua baris terseleksi (bulk, berurutan).
			// Seleksi tidak pernah menembus seksi, jadi key & model tak tercampur.
			ids := m.provRowIDs(f.arg)
			if len(ids) == 0 {
				break
			}
			sel := f.sel
			if len(sel) == 0 {
				sel = map[int]bool{f.cursor: true}
			}
			var jobs []testJob
			disabled := 0
			kinds := map[string]bool{}
			for i := 0; i < len(ids); i++ {
				if !sel[i] {
					continue
				}
				id := ids[i]
				if !id.on {
					disabled++
					continue
				}
				kind := "model"
				if id.isKey {
					kind = "key"
				}
				kinds[kind] = true
				jobs = append(jobs, testJob{kind: kind, slug: f.arg, name: id.name, label: id.label})
			}
			if len(jobs) == 0 {
				if disabled > 0 {
					m.say("selected row is disabled — cannot test", true)
				} else {
					m.say("nothing to test", true)
				}
				return m, nil
			}
			if len(kinds) > 1 {
				m.say("cannot mix key and model selection — test one section at a time", true)
				return m, nil
			}
			if _, perr := m.provTestParamsFor(f.arg); perr != nil {
				m.say("cannot test: "+perr.Error(), true)
				return m, nil
			}
			return m, m.startTests(jobs, "")
		case "pgup":
			selCollapse(&f, f.cursor-m.viewportH(), n)
			move()
		case "pgdown":
			selCollapse(&f, f.cursor+m.viewportH(), n)
			move()
		case "home", "g":
			selCollapse(&f, 0, n)
			move()
		case "end", "G":
			selCollapse(&f, n-1, n)
			move()
		}
	default:
		// Halaman konten (detail proxy, output): panah scroll.
		f = scrollFrame(f, key, m.viewportH())
		m.setCur(f)
	}
	m.clampAll()
	return m, nil
}

// scrollFrame menggeser scroll frame konten per tombol.
func scrollFrame(f frame, key string, vp int) frame {
	switch key {
	case "up", "k":
		f.scroll--
	case "down", "j":
		f.scroll++
	case "pgup":
		f.scroll -= vp
	case "pgdown":
		f.scroll += vp
	case "home", "g":
		f.scroll = 0
	case "end", "G":
		f.scroll = 1 << 30
	}
	return f
}

// snapTail mengunci tail bila scroll sudah di paling bawah.
func (m *Model) snapTail() {
	f := m.cur()
	if f.pg != pgLogs {
		return
	}
	max := maxScroll(len(m.logLines), m.viewportH())
	if f.scroll >= max {
		f.tail = true
		m.setCur(f)
	}
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.deps.OnQuit != nil {
		m.deps.OnQuit()
	}
	return m, tea.Quit
}

// clampAll menjaga cursor/scroll dalam batas setelah resize/reload.
func (m *Model) clampAll() {
	f := m.cur()
	if f.cursor < 0 {
		f.cursor = 0
	}
	switch f.pg {
	case pgHome:
		f.cursor = clamp(f.cursor, 0, len(homeMenu)-1)
	case pgProviders:
		n := len(m.provCards())
		if n == 0 {
			f.cursor = 0
		} else {
			f.cursor = clamp(f.cursor, 0, n-1)
		}
		// Fixed 1-line header, body is viewport-1 (like the Api Key table).
		vp := m.viewportH() - 1
		if vp < 1 {
			vp = 1
		}
		f.scroll = clamp(f.scroll, 0, maxScroll(n, vp))
	case pgProxies:
		n := len(m.proxyCards())
		if n == 0 {
			f.cursor = 0
			f.anchor = 0
		} else {
			f.cursor = clamp(f.cursor, 0, n-1)
			f.anchor = clamp(f.anchor, 0, n-1)
		}
		// Buang seleksi yang di luar jangkauan setelah reload/resize.
		for i := range f.sel {
			if i < 0 || i >= n {
				delete(f.sel, i)
			}
		}
		// Header fixed 1 baris, body = viewport-1 (seperti tabel Api Key).
		vp := m.viewportH() - 1
		if vp < 1 {
			vp = 1
		}
		f.scroll = clamp(f.scroll, 0, maxScroll(n, vp))
	case pgAPIKeys:
		n := len(m.sysKeyNames())
		if n == 0 {
			f.cursor = 0
			f.anchor = 0
		} else {
			f.cursor = clamp(f.cursor, 0, n-1)
			f.anchor = clamp(f.anchor, 0, n-1)
		}
		for i := range f.sel {
			if i < 0 || i >= n {
				delete(f.sel, i)
			}
		}
		// Header fixed 1 baris, body = viewport-1.
		vp := m.viewportH() - 1
		if vp < 1 {
			vp = 1
		}
		f.scroll = clamp(f.scroll, 0, maxScroll(n, vp))
	case pgLogs:
		if f.tail {
			f.scroll = maxScroll(len(m.logLines), m.viewportH())
		} else {
			f.scroll = clamp(f.scroll, 0, maxScroll(len(m.logLines), m.viewportH()))
		}
	default:
		var n int
		if f.pg == pgProvider {
			lines, keyLines, modelLines := m.providerDetail(f)
			n = len(lines)
			total := len(keyLines) + len(modelLines)
			if total == 0 {
				f.cursor = 0
				f.anchor = 0
			} else {
				f.cursor = clamp(f.cursor, 0, total-1)
				f.anchor = clamp(f.anchor, 0, total-1)
			}
			for i := range f.sel {
				if i < 0 || i >= total {
					delete(f.sel, i)
				}
			}
		} else {
			pl, _, _, _ := m.pageLines(f)
			n = len(pl)
		}
		f.scroll = clamp(f.scroll, 0, maxScroll(n, m.viewportH()))
	}
	m.setCur(f)
}

// execCommand memetakan perintah ala CLI lama ke halaman TUI.
func (m Model) execCommand(line string) (tea.Model, tea.Cmd) {
	cmd, arg := cli.Split(line)
	switch cmd {
	case "h", "help", "?":
		m.push(frame{pg: pgOutput, arg: outHelp})
	case "status", "st":
		m.push(frame{pg: pgOutput, arg: outStatus})
	case "system", "sys", "info", "apikeys", "apikey", "key", "keys":
		m.push(frame{pg: pgAPIKeys})
	case "providers", "prov", "ls":
		if arg != "" {
			m.push(frame{pg: pgProvider, arg: arg})
		} else {
			m.push(frame{pg: pgProviders})
		}
	case "provider", "show":
		if arg == "" {
			m.say(`usage: provider <slug|prefix>`, true)
		} else {
			m.push(frame{pg: pgProvider, arg: arg})
		}
	case "proxies", "pools", "pool":
		if arg != "" {
			m.push(frame{pg: pgProxy, arg: arg})
		} else {
			m.push(frame{pg: pgProxies})
		}
	case "proxy":
		if arg != "" {
			m.push(frame{pg: pgProxy, arg: arg})
		} else {
			m.push(frame{pg: pgProxies})
		}
	case "models", "model", "m":
		m.push(frame{pg: pgOutput, arg: outModels, filter: arg})
	case "stats", "stat":
		m.push(frame{pg: pgOutput, arg: outStats})
	case "logs", "log":
		m.push(frame{pg: pgLogs, tail: true})
	case "home", "menu", "back":
		m.stack = []frame{{pg: pgHome}}
	case "r", "reload":
		m.say("reloading…", false)
		return m, func() tea.Msg {
			if m.deps.OnReload != nil {
				m.deps.OnReload("ui")
			}
			return reloadDoneMsg{}
		}
	case "q", "quit", "exit":
		return m.quit()
	default:
		m.say("unknown command: "+line, true)
	}
	return m, nil
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxScroll(lines, viewport int) int {
	if lines <= viewport {
		return 0
	}
	return lines - viewport
}
