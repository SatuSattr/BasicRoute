package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"basicroute/app/config"
	"basicroute/app/service"
)

func uiTestModel() Model {
	prov := &config.Provider{
		Name:      "Acme",
		Prefix:    "acme",
		Type:      "openai",
		Interface: "chat-completions",
		BaseURL:   "https://api.example.com/v1",
		Proxy:     "auto",
		Enabled:   config.FlexBool(true),
		Keys: map[string]*config.ProvKey{
			"k1": {Key: "sk-1", Order: config.FlexInt(1), Enabled: config.FlexBool(true)},
		},
		Models: map[string]*config.Model{
			"m1": {Name: "M1", Enabled: config.FlexBool(true)},
		},
	}
	snap := &config.Snapshot{
		Raw: &config.Config{
			Providers: map[string]*config.Provider{"acme": prov},
			ProxyPools: map[string]*config.ProxyPool{
				"eu": {Type: config.ProxyTypeHTTP, URL: "http://u:p@eu.example:8080", Order: 1, Enabled: true},
				"us": {Type: config.ProxyTypeHTTP, URL: "http://us.example:8080", Order: 2, Enabled: true},
			},
		},
		ProxyOrder: []string{"eu", "us"},
	}
	return Model{deps: Deps{Store: service.NewStore(snap)}, width: 100, height: 30}
}

func TestProxyCardsFollowOrder(t *testing.T) {
	m := uiTestModel()
	cards := m.proxyCards()
	if len(cards) != 2 {
		t.Fatalf("cards = %d", len(cards))
	}
	if cards[0].name != "eu" || cards[1].name != "us" {
		t.Fatalf("order = %s, %s", cards[0].name, cards[1].name)
	}
	if cards[0].order != 1 || cards[1].order != 2 {
		t.Fatalf("order values = %d, %d", cards[0].order, cards[1].order)
	}
	// Password harus termasking, bukan %2A%2A.
	if strings.Contains(cards[0].url, "%2A") || !strings.Contains(cards[0].url, "****") {
		t.Fatalf("masked url = %q", cards[0].url)
	}
}

func TestProxyTableHasOrderColumn(t *testing.T) {
	m := uiTestModel()
	out := m.proxiesTableView(frame{pg: pgProxies})
	for _, want := range []string{"NAME", "TYPE", "ORDER", "CLIENTS", "URL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table header missing %q:\n%s", want, out)
		}
	}
}

func TestProviderDetailKeyProxyColumn(t *testing.T) {
	m := uiTestModel()

	// Key tanpa override: kolom PROXY menampilkan label proxy provider.
	all := providerDetailText(m)
	if !strings.Contains(all, "PROXY") {
		t.Fatalf("keys table missing PROXY column:\n%s", all)
	}
	if !strings.Contains(all, "auto (2)") {
		t.Fatalf("inherited key must show provider proxy label:\n%s", all)
	}

	// Key dengan proxy "" (direct) eksplisit.
	snap := m.deps.Store.Get()
	empty := ""
	snap.Raw.Providers["acme"].Keys["k1"].Proxy = &empty
	all = providerDetailText(m)
	if !strings.Contains(all, "direct") {
		t.Fatalf("explicit-direct key must show \"direct\":\n%s", all)
	}
}

// providerDetailText menggabung semua segmen teks halaman detail provider.
func providerDetailText(m Model) string {
	lines, _, _ := m.providerDetail(frame{pg: pgProvider, arg: "acme"})
	var sb strings.Builder
	for _, l := range lines {
		for _, s := range l.segs {
			sb.WriteString(s.text)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// dotColors mengumpulkan warna foreground setiap indikator "• " pada baris
// detail provider (judul, baris key, baris model).
func dotColors(lines []dline) []lipgloss.TerminalColor {
	var out []lipgloss.TerminalColor
	for _, l := range lines {
		if len(l.segs) > 0 && l.segs[0].text == "• " {
			out = append(out, l.segs[0].st.GetForeground())
		}
	}
	return out
}

// Provider disabled: semua dot key & model ikut abu-abu, walaupun key/model
// sendiri enabled, sedang di-test, atau flagged error.
func TestDisabledProviderGraysAllKeyAndModelDots(t *testing.T) {
	m := uiTestModel()

	// Kontrol: provider enabled + key/model enabled -> semua dot hijau.
	lines, keyLines, modelLines := m.providerDetail(frame{pg: pgProvider, arg: "acme"})
	if len(keyLines) == 0 || len(modelLines) == 0 {
		t.Fatalf("expected key and model rows: keys=%v models=%v", keyLines, modelLines)
	}
	dots := dotColors(lines)
	if len(dots) < 3 {
		t.Fatalf("expected title + key + model dots, got %d", len(dots))
	}
	for _, c := range dots {
		if c != colGreen {
			t.Fatalf("enabled provider dot color = %v, want green", c)
		}
	}

	// Precedence tetap: key flagged merah, model sedang di-test kuning.
	m.deps.Store.ProvHealth.SetError(service.ProvKeyID("acme", "k1"), "boom")
	m.deps.Store.ProvHealth.SetTesting(service.ProvModelID("acme", "m1"))
	lines, _, _ = m.providerDetail(frame{pg: pgProvider, arg: "acme"})
	seen := map[lipgloss.TerminalColor]bool{}
	for _, c := range dotColors(lines) {
		seen[c] = true
	}
	if !seen[colRed] || !seen[colAmber] {
		t.Fatalf("flagged/testing dots must stay red/amber: %v", seen)
	}

	// Provider disabled -> semua dot abu-abu, menimpa merah/kuning.
	m.deps.Store.Get().Raw.Providers["acme"].Enabled = config.FlexBool(false)
	lines, keyLines, modelLines = m.providerDetail(frame{pg: pgProvider, arg: "acme"})
	if len(keyLines) == 0 || len(modelLines) == 0 {
		t.Fatal("key and model rows must still be listed when the provider is disabled")
	}
	for _, c := range dotColors(lines) {
		if c != colDim {
			t.Fatalf("disabled provider dot color = %v, want gray", c)
		}
	}
	// Baris detail error di dasar container juga tidak ditampilkan.
	if el := m.provErrLine("acme", 0); el != "" {
		t.Fatalf("disabled provider must not show the error line, got %q", el)
	}
}

func TestProvidersTableHasTypeColumn(t *testing.T) {
	m := uiTestModel()
	out := m.providersTableView(frame{pg: pgProviders})
	for _, want := range []string{"NAME", "TYPE", "KEYS", "MODELS", "PROXY"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table header missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PREFIX") {
		t.Fatalf("table must not show a PREFIX column:\n%s", out)
	}
	if !strings.Contains(out, "openai") {
		t.Fatalf("table must show the provider type:\n%s", out)
	}
}

func TestProviderCardProxyLabel(t *testing.T) {
	m := uiTestModel()
	cards := m.provCards()
	if len(cards) != 1 {
		t.Fatalf("cards = %d", len(cards))
	}
	if cards[0].proxy != "auto (2)" {
		t.Fatalf("card proxy = %q", cards[0].proxy)
	}
}

func TestProviderDetailShowsProxyMode(t *testing.T) {
	m := uiTestModel()
	lines, _, _ := m.providerDetail(frame{pg: pgProvider, arg: "acme"})
	var joined []string
	for _, l := range lines {
		for _, s := range l.segs {
			joined = append(joined, s.text)
		}
	}
	all := strings.Join(joined, "")
	if !strings.Contains(all, "auto (2): eu → us") {
		t.Fatalf("basic info proxy line missing mode: %q", all)
	}
}
