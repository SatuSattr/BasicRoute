package ui

import (
	"strings"
	"testing"

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
