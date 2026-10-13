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
	apiUsageAt time.Time
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

// testProxiesCmd menjalankan test berurutan (async) ke pool yang enabled.
func (m Model) testProxiesCmd(names []string) tea.Cmd {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
	}
	testURL := snap.Raw.System.ProxyTestURL
	type job struct{ name, typ, url string }
	jobs := make([]job, 0, len(names))
	for _, n := range names {
		p := snap.Raw.ProxyPools[n]
		if p == nil || !bool(p.Enabled) {
			continue
		}
		jobs = append(jobs, job{name: n, typ: p.Type, url: strings.TrimSpace(p.URL)})
	}
	if len(jobs) == 0 {
		return nil
	}
	return func() tea.Msg {
		out := make([]proxyTestResult, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, proxyTestResult{name: j.name, err: service.TestProxy(j.typ, j.url, testURL, proxyTestTimeout)})
		}
		return proxyTestResultMsg{results: out}
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

// poolNames mengembalikan nama proxy pool terurut (live dari Store).
func (m Model) poolNames() []string {
	snap := m.deps.Store.Get()
	if snap == nil || snap.Raw == nil {
		return nil
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
	if m.deps.Store == nil || m.deps.Store.Usage == nil {
		m.apiUsage = map[string]uint64{}
		m.apiUsageAt = time.Now()
		return
	}
	m.apiUsage = m.deps.Store.Usage.Snapshot()
	m.apiUsageAt = time.Now()
}

// apiUsageTotal membaca dari cache 3-detik (hash -> total).
func (m Model) apiUsageTotal(hash string) uint64 {
	if hash == "" {
		return 0
	}
	return m.apiUsage[hash]
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
		m.applyProxyTestResults(msg.results)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applyProxyTestResults menyimpan status error/ok ke memory dan melapor.
func (m *Model) applyProxyTestResults(results []proxyTestResult) {
	if m.deps.Store != nil && m.deps.Store.ProxyHealth != nil {
		for _, r := range results {
			if r.err != nil {
				m.deps.Store.ProxyHealth.SetError(r.name)
			} else {
				m.deps.Store.ProxyHealth.SetOK(r.name)
			}
		}
	}
	var okN, errN int
	var firstErr string
	for _, r := range results {
		if r.err != nil {
			errN++
			if firstErr == "" {
				firstErr = r.name + ": " + r.err.Error()
			}
		} else {
			okN++
		}
	}
	total := len(results)
	switch {
	case total == 0:
		m.say("nothing to test", true)
	case errN == 0:
		m.say(fmt.Sprintf("proxy test ok: %d/%d", okN, total), false)
	default:
		m.say(fmt.Sprintf("proxy test: %d ok, %d error — %s", okN, errN, firstErr), true)
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
		cols := m.provCols(n)
		switch key {
		case "left", "h":
			if n > 0 {
				f.cursor = (f.cursor + n - 1) % n
				m.setCur(f)
				m.ensureCardVisible()
			}
		case "right", "l":
			if n > 0 {
				f.cursor = (f.cursor + 1) % n
				m.setCur(f)
				m.ensureCardVisible()
			}
		case "up", "k":
			if n > 0 {
				c := f.cursor
				if c-cols >= 0 {
					c -= cols
				} else {
					for c+cols < n {
						c += cols
					}
				}
				f.cursor = c
				m.setCur(f)
				m.ensureCardVisible()
			}
		case "down", "j":
			if n > 0 {
				c := f.cursor
				if c+cols < n {
					c += cols
				} else {
					c = c % cols
					if c >= n {
						c = n - 1
					}
				}
				f.cursor = c
				m.setCur(f)
				m.ensureCardVisible()
			}
		case "enter", " ":
			if f.cursor >= 0 && f.cursor < n {
				m.push(frame{pg: pgProvider, arg: cards[f.cursor].slug})
			}
		case "pgup":
			f.scroll -= m.provVisRows()
			m.setCur(f)
		case "pgdown":
			f.scroll += m.provVisRows()
			m.setCur(f)
		case "home", "g":
			f.scroll = 0
			m.setCur(f)
		case "end", "G":
			f.scroll = 1 << 30
			m.setCur(f)
		}
	case pgAPIKeys:
		names := m.sysKeyNames()
		switch key {
		case "up", "k":
			if len(names) > 0 {
				f.cursor = (f.cursor + len(names) - 1) % len(names)
				m.setCur(f)
				m.ensureApiKeysVisible()
			}
		case "down", "j":
			if len(names) > 0 {
				f.cursor = (f.cursor + 1) % len(names)
				m.setCur(f)
				m.ensureApiKeysVisible()
			}
		case "x", "X":
			f.revealKeys = !f.revealKeys
			if f.revealKeys {
				m.say("all keys shown in plain text (x: mask)", false)
			} else {
				m.say("keys masked (x: reveal)", false)
			}
			m.setCur(f)
		case "pgup":
			f.scroll -= m.viewportH() - 1
			m.setCur(f)
		case "pgdown":
			f.scroll += m.viewportH() - 1
			m.setCur(f)
		case "home", "g":
			f.scroll = 0
			m.setCur(f)
		case "end", "G":
			f.scroll = 1 << 30
			m.setCur(f)
		}
	case pgProxies:
		cards := m.proxyCards()
		n := len(cards)
		cols := m.proxyCols(n)
		switch key {
		case "left", "h", "right", "l", "up", "k", "down", "j":
			if n > 0 {
				f.cursor = moveProxyCursor(f.cursor, key, n, cols)
				f.sel = nil // navigasi biasa = reset seleksi
				m.setCur(f)
				m.ensureCardVisible()
			}
		case "shift+left", "shift+right", "shift+up", "shift+down":
			if n > 0 {
				prev := f.cursor
				f.cursor = moveProxyCursor(f.cursor, strings.TrimPrefix(key, "shift+"), n, cols)
				if f.sel == nil {
					f.sel = map[int]bool{}
				}
				f.sel[prev] = true
				f.sel[f.cursor] = true
				m.setCur(f)
				m.ensureCardVisible()
			}
		case "ctrl+a":
			if n > 0 {
				f.sel = make(map[int]bool, n)
				for i := 0; i < n; i++ {
					f.sel[i] = true
				}
				m.setCur(f)
				m.say(fmt.Sprintf("%d proxy selected", n), false)
			}
		case "t":
			if f.cursor >= 0 && f.cursor < n {
				c := cards[f.cursor]
				if !c.enabled {
					m.say("proxy "+c.name+" is disabled — cannot test", true)
					return m, nil
				}
				m.say("testing "+c.name+"…", false)
				return m, m.testProxiesCmd([]string{c.name})
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
			msg := fmt.Sprintf("testing %d proxy…", len(names))
			if skipped > 0 {
				msg = fmt.Sprintf("testing %d proxy (%d disabled skipped)…", len(names), skipped)
			}
			m.say(msg, false)
			return m, m.testProxiesCmd(names)
		case "enter", " ":
			if f.cursor >= 0 && f.cursor < n {
				m.push(frame{pg: pgProxy, arg: cards[f.cursor].name})
			}
		case "pgup":
			f.scroll -= m.proxyVisRows()
			m.setCur(f)
		case "pgdown":
			f.scroll += m.proxyVisRows()
			m.setCur(f)
		case "home", "g":
			f.scroll = 0
			m.setCur(f)
		case "end", "G":
			f.scroll = 1 << 30
			m.setCur(f)
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
	default:
		// Halaman konten (detail provider/proxy, output, apikeys): panah scroll.
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
		rows := provRows(n, m.provCols(n))
		f.scroll = clamp(f.scroll, 0, imax(rows-m.provVisRows(), 0))
	case pgProxies:
		n := len(m.proxyCards())
		if n == 0 {
			f.cursor = 0
		} else {
			f.cursor = clamp(f.cursor, 0, n-1)
		}
		// Buang seleksi yang di luar jangkauan setelah reload/resize.
		for i := range f.sel {
			if i < 0 || i >= n {
				delete(f.sel, i)
			}
		}
		rows := provRows(n, m.proxyCols(n))
		f.scroll = clamp(f.scroll, 0, imax(rows-m.proxyVisRows(), 0))
	case pgAPIKeys:
		n := len(m.sysKeyNames())
		if n == 0 {
			f.cursor = 0
		} else {
			f.cursor = clamp(f.cursor, 0, n-1)
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
			n = len(m.providerDetail(f))
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
