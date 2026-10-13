package ui

import (
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"basicroute/app/cli"
	"basicroute/app/service"
)

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
	arg        string // provider slug / pool name / output kind
	filter     string // filter models (output kind models)
	cursor     int    // selection index (home menu / cards / list)
	scroll     int    // content scroll offset (card pages: row index)
	tail       bool   // logs: follow new lines
	revealKeys bool   // api keys: show keys in plain text without masking
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

func tickCmd() tea.Msg {
	return tickMsg(time.Now())
}

func (m Model) Init() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) cur() frame { return m.stack[len(m.stack)-1] }

func (m *Model) setCur(f frame) { m.stack[len(m.stack)-1] = f }

func (m *Model) push(f frame) { m.stack = append(m.stack, f) }

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
		m.clampAll()
		return m, tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })

	case reloadDoneMsg:
		m.say("reload done — see Logs for details", false)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
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
			}
		case "down", "j":
			if len(names) > 0 {
				f.cursor = (f.cursor + 1) % len(names)
				m.setCur(f)
			}
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
		cols := m.proxyCols(n)
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
		rows := provRows(n, m.proxyCols(n))
		f.scroll = clamp(f.scroll, 0, imax(rows-m.proxyVisRows(), 0))
	case pgAPIKeys:
		n := len(m.sysKeyNames())
		if n == 0 {
			f.cursor = 0
		} else {
			f.cursor = clamp(f.cursor, 0, n-1)
		}
		pl, _, _, _ := m.pageLines(f)
		f.scroll = clamp(f.scroll, 0, maxScroll(len(pl), m.viewportH()))
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
