package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

type Config struct {
	System     System                `json:"system"`
	Providers  map[string]*Provider  `json:"providers"`
	ProxyPools map[string]*ProxyPool `json:"proxyPools"`
}

type System struct {
	Host         string             `json:"host"`
	Port         FlexInt            `json:"port"`
	RequireAuth  FlexBool           `json:"requireAuth"`
	Keys         map[string]*SysKey `json:"keys"`
	Limits       Limits             `json:"limits"`
	UsageFile    string             `json:"usageFile,omitempty"`
	ProxyTestURL string             `json:"proxyTestUrl,omitempty"`
}

type SysKey struct {
	Key     string   `json:"key"`
	Enabled FlexBool `json:"enabled"`
}

type Limits struct {
	MaxBodyMB FlexFloat `json:"maxBodyMB"`
}

type Provider struct {
	Name        string              `json:"name"`
	Prefix      string              `json:"prefix"` // namespace publik, unik, ^[a-z0-9-]+$
	Type        string              `json:"type"`   // v1: hanya "openai"
	Interface   string              `json:"interface"`
	BaseURL     string              `json:"baseUrl"`
	Proxy       string              `json:"proxy,omitempty"` // nama pool di ProxyPools, "" = direct
	KeyStrategy string              `json:"keyStrategy,omitempty"` // ""/ordered = failover urutan Order, "round" = rotasi start per-request
	Enabled     FlexBool            `json:"enabled"`
	TimeoutSecs FlexInt             `json:"timeoutSecs"`
	Keys        map[string]*ProvKey `json:"keys"`
	Models      map[string]*Model   `json:"models"`
	// ModelsAuto true bila config memakai "models": "auto" (fetch GET /models
	// upstream, memory only). Tidak di-marshal dari JSON secara langsung;
	// diisi oleh UnmarshalJSON custom di bawah.
	ModelsAuto bool `json:"-"`
}

// IsModelsAuto melaporkan apakah provider memakai mode fetch otomatis.
func (p *Provider) IsModelsAuto() bool {
	return p != nil && p.ModelsAuto
}

// rawProvider adalah bayangan Provider untuk parsing polimorfik field models.
type rawProvider struct {
	Name        string              `json:"name"`
	Prefix      string              `json:"prefix"`
	Type        string              `json:"type"`
	Interface   string              `json:"interface"`
	BaseURL     string              `json:"baseUrl"`
	Proxy       string              `json:"proxy,omitempty"`
	KeyStrategy string              `json:"keyStrategy,omitempty"`
	Enabled     FlexBool            `json:"enabled"`
	TimeoutSecs FlexInt             `json:"timeoutSecs"`
	Keys        map[string]*ProvKey `json:"keys"`
	Models      json.RawMessage     `json:"models"`
}

var allowedProviderFields = map[string]bool{
	"name": true, "prefix": true, "type": true, "interface": true,
	"baseUrl": true, "proxy": true, "keyStrategy": true, "enabled": true,
	"timeoutSecs": true, "keys": true, "models": true,
}

// UnmarshalJSON menerima dua bentuk "models": object map (statis, seperti
// dulu) atau string "auto" (fetch otomatis). Unknown field tetap ditolak agar
// perilaku strict DisallowUnknownFields tidak hilang.
func (p *Provider) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for k := range fields {
		if !allowedProviderFields[k] {
			return fmt.Errorf("providers: unknown field %q", k)
		}
	}
	var raw rawProvider
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Name = raw.Name
	p.Prefix = raw.Prefix
	p.Type = raw.Type
	p.Interface = raw.Interface
	p.BaseURL = raw.BaseURL
	p.Proxy = raw.Proxy
	p.KeyStrategy = raw.KeyStrategy
	p.Enabled = raw.Enabled
	p.TimeoutSecs = raw.TimeoutSecs
	p.Keys = raw.Keys
	p.ModelsAuto = false
	p.Models = nil
	if len(raw.Models) == 0 || strings.TrimSpace(string(raw.Models)) == "null" {
		return nil // validasi (required) ditangani validateAndNormalize
	}
	trimmed := strings.TrimSpace(string(raw.Models))
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw.Models, &s); err != nil {
			return fmt.Errorf("models: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(s), "auto") {
			p.ModelsAuto = true
			p.Models = map[string]*Model{}
			return nil
		}
		return fmt.Errorf("models: must be \"auto\" or an object map, got %q", s)
	}
	var m map[string]*Model
	if err := json.Unmarshal(raw.Models, &m); err != nil {
		return fmt.Errorf("models: %w", err)
	}
	p.Models = m
	return nil
}

// MarshalJSON mengembalikan bentuk semula: string "auto" bila ModelsAuto,
// object map bila statis.
func (p Provider) MarshalJSON() ([]byte, error) {
	type outProvider struct {
		Name        string              `json:"name"`
		Prefix      string              `json:"prefix"`
		Type        string              `json:"type"`
		Interface   string              `json:"interface"`
		BaseURL     string              `json:"baseUrl"`
		Proxy       string              `json:"proxy,omitempty"`
		KeyStrategy string              `json:"keyStrategy,omitempty"`
		Enabled     FlexBool            `json:"enabled"`
		TimeoutSecs FlexInt             `json:"timeoutSecs"`
		Keys        map[string]*ProvKey `json:"keys"`
		Models      any                 `json:"models"`
	}
	o := outProvider{
		Name: p.Name, Prefix: p.Prefix, Type: p.Type, Interface: p.Interface,
		BaseURL: p.BaseURL, Proxy: p.Proxy, KeyStrategy: p.KeyStrategy,
		Enabled: p.Enabled, TimeoutSecs: p.TimeoutSecs, Keys: p.Keys,
	}
	if p.ModelsAuto {
		o.Models = "auto"
	} else {
		o.Models = p.Models
	}
	return json.Marshal(o)
}

type ProvKey struct {
	Key     string   `json:"key"`
	Order   FlexInt  `json:"order"` // urutan failover, unik per provider
	Enabled FlexBool `json:"enabled"`
	// Proxy meng-override proxy provider untuk key ini (opsional):
	// nil/absen = ikut proxy provider, "" = direct, nama pool = pool spesifik.
	Proxy *string `json:"proxy,omitempty"`
}

type Model struct {
	Name    string   `json:"name"`
	Enabled FlexBool `json:"enabled"`
	Limit   Limit    `json:"limit"`
	Created FlexInt  `json:"created,omitempty"`
}

type Limit struct {
	Context FlexInt `json:"context"`
	Output  FlexInt `json:"output"`
}

// Tipe proxy pool yang didukung.
const (
	ProxyTypeHTTP  = "http"  // forward proxy standard (mis. 711Proxy/9Proxy): http(s)://user:pass@host:port
	ProxyTypeRelay = "relay" // http relay (mis. 9router): https://relay.example.com + header x-relay-*
)

// Mode proxy khusus pada Provider.Proxy (selain nama pool biasa).
const (
	ProxyModeAuto  = "auto"  // pakai semua pool enabled berurutan order, failover ke pool berikutnya
	ProxyModeRound = "round" // sama seperti auto tapi urutan awal berotasi (round robin)
)

// Strategi pemilihan key per-provider.
const (
	KeyStrategyOrdered = "ordered" // failover urutan Order (default)
	KeyStrategyRound   = "round"   // rotasi start per-request (bagi beban)
)

type ProxyPool struct {
	Type    string   `json:"type,omitempty"` // "" / "http" = forward proxy, "relay" = http relay
	URL     string   `json:"url"`
	Order   FlexInt  `json:"order"` // urutan pemakaian mode auto/round, unik antar pool
	Enabled FlexBool `json:"enabled"`
}

type Snapshot struct {
	Gen        uint64
	Raw        *Config
	ModelMap   map[string]*ModelRef   // key = FullID ("<prefix>/<modelSlug>")
	KeyRings   map[string][]*KeyEntry // key = provider slug, value = enabled keys terurut Order
	ProxyOrder []string               // nama semua pool terurut (order asc, nama asc)
}

type ModelRef struct {
	ProviderSlug string
	Provider     *Provider
	ModelSlug    string
	Model        *Model
	FullID       string // Provider.Prefix + "/" + ModelSlug
}

type KeyEntry struct {
	Name  string
	Key   string
	Order int
	// Proxy meng-override proxy provider untuk key ini: nil = ikut provider,
	// "" = direct, nama pool = pool spesifik (tanpa failover).
	Proxy *string
}

func StripJSONC(src []byte) []byte {
	out := make([]byte, len(src))
	inString := false
	escaped := false
	i := 0
	for i < len(src) {
		c := src[i]
		if inString {
			out[i] = c
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}
		if c == '"' {
			inString = true
			out[i] = c
			i++
			continue
		}
		if c == '/' && i+1 < len(src) {
			nxt := src[i+1]
			if nxt == '/' {
				out[i] = ' '
				out[i+1] = ' '
				i += 2
				for i < len(src) && src[i] != '\n' {
					if src[i] == '\r' {
						out[i] = '\r'
					} else {
						out[i] = ' '
					}
					i++
				}
				continue
			}
			if nxt == '*' {
				out[i] = ' '
				out[i+1] = ' '
				i += 2
				for i < len(src) {
					if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
						out[i] = ' '
						out[i+1] = ' '
						i += 2
						break
					}
					if src[i] == '\n' || src[i] == '\r' {
						out[i] = src[i]
					} else {
						out[i] = ' '
					}
					i++
				}
				continue
			}
		}
		out[i] = c
		i++
	}
	return stripTrailingCommas(out)
}

func stripTrailingCommas(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	inString := false
	escaped := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c != ',' {
			continue
		}
		j := i + 1
		for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
			j++
		}
		if j < len(out) && (out[j] == '}' || out[j] == ']') {
			out[i] = ' '
		}
	}
	return out
}

var prefixRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// isReservedProxyName melaporkan apakah nama pool memakai kata kunci mode
// proxy (auto/round) — nama seperti itu dilarang agar tidak ambigu.
func isReservedProxyName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ProxyModeAuto, ProxyModeRound:
		return true
	}
	return false
}

// IsProxyMode melaporkan apakah nilai Provider.Proxy adalah mode khusus
// ("auto"/"round", case-insensitive) alih-alih nama pool.
func IsProxyMode(s string) bool {
	_, ok := ProxyModeOf(s)
	return ok
}

// ProxyModeOf mengembalikan mode proxy (lowercase) bila s adalah kata kunci
// mode; ok=false berarti nama pool biasa atau direct.
func ProxyModeOf(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ProxyModeAuto:
		return ProxyModeAuto, true
	case ProxyModeRound:
		return ProxyModeRound, true
	}
	return "", false
}

// IsKeyRound melaporkan apakah provider memakai strategi key round-robin.
// Nil-safe: nil → false. Hanya true bila KeyStrategy (trim+lower) == "round".
func (p *Provider) IsKeyRound() bool {
	if p == nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(p.KeyStrategy)) == KeyStrategyRound
}

func Load(path string) (*Snapshot, error) {
	return LoadWithEnv(path, "")
}

func LoadWithEnv(path, envPath string) (*Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("system: read %s: %w", path, err)
	}
	extra := map[string]string{}
	for _, f := range collectEnvFiles(path, envPath) {
		b, err := os.ReadFile(f)
		if err != nil {
			if envPath != "" && envPath != "-" {
				return nil, fmt.Errorf("config: read env %s: %w", f, err)
			}
			continue // auto-discovery: file yang tidak ada dilewati
		}
		for k, v := range parseDotEnv(string(b)) {
			extra[k] = v
		}
	}
	stripped := StripJSONC(raw)
	expanded, err := expandEnvPlaceholders(stripped, makeLookup(extra))
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(expanded))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}
	if err := validateAndNormalize(&cfg); err != nil {
		return nil, err
	}
	return buildSnapshot(&cfg), nil
}

func validateAndNormalize(cfg *Config) error {
	if strings.TrimSpace(cfg.System.Host) == "" {
		return fmt.Errorf("system.host: required")
	}
	if cfg.System.Port < 1 || cfg.System.Port > 65535 {
		return fmt.Errorf("system.port: must be 1-65535, got %d", cfg.System.Port)
	}
	if cfg.System.Limits.MaxBodyMB <= 0 {
		return fmt.Errorf("system.limits.maxBodyMB: must be > 0, got %v", cfg.System.Limits.MaxBodyMB)
	}
	enabledSysKeys := 0
	for name, k := range cfg.System.Keys {
		if k == nil {
			return fmt.Errorf("system.keys[%q]: entry null", name)
		}
		if strings.TrimSpace(k.Key) == "" {
			return fmt.Errorf("system.keys[%q].key: required", name)
		}
		if k.Enabled {
			enabledSysKeys++
		}
	}
	if cfg.System.RequireAuth && enabledSysKeys == 0 {
		return fmt.Errorf("system.keys: requireAuth=true needs >=1 enabled system key")
	}
	if strings.TrimSpace(cfg.System.UsageFile) == "" {
		cfg.System.UsageFile = "/app/data/usage.json"
	} else {
		cfg.System.UsageFile = strings.TrimSpace(cfg.System.UsageFile)
	}
	if strings.TrimSpace(cfg.System.ProxyTestURL) == "" {
		cfg.System.ProxyTestURL = "https://www.gstatic.com/generate_204"
	} else {
		cfg.System.ProxyTestURL = strings.TrimSpace(cfg.System.ProxyTestURL)
	}

	if cfg.ProxyPools == nil {
		cfg.ProxyPools = map[string]*ProxyPool{}
	}
	seenPoolOrder := map[int]string{}
	for name, p := range cfg.ProxyPools {
		if p == nil {
			return fmt.Errorf("proxyPools[%q]: entry null", name)
		}
		if isReservedProxyName(name) {
			return fmt.Errorf("proxyPools[%q]: reserved name (proxy mode keyword %q/%q); rename this pool", name, ProxyModeAuto, ProxyModeRound)
		}
		p.Type = strings.ToLower(strings.TrimSpace(p.Type))
		if p.Type == "" {
			p.Type = ProxyTypeHTTP
		}
		switch p.Type {
		case ProxyTypeHTTP, ProxyTypeRelay:
		default:
			return fmt.Errorf("proxyPools[%q].type: must be %q or %q, got %q", name, ProxyTypeHTTP, ProxyTypeRelay, p.Type)
		}
		if p.Order < 1 {
			return fmt.Errorf("proxyPools[%q].order: must be >= 1, got %d", name, int(p.Order))
		}
		if other, dup := seenPoolOrder[int(p.Order)]; dup {
			return fmt.Errorf("proxyPools[%q].order: duplicate order %d already used by %s", name, int(p.Order), fmt.Sprintf("proxyPools[%q]", other))
		}
		seenPoolOrder[int(p.Order)] = name
		u := strings.TrimSpace(p.URL)
		if u == "" {
			return fmt.Errorf("proxyPools[%q].url: required", name)
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("proxyPools[%q].url: invalid URL %q", name, p.URL)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
		default:
			return fmt.Errorf("proxyPools[%q].url: scheme must be http/https (socks5 not supported), got %q", name, parsed.Scheme)
		}
		if p.Type == ProxyTypeRelay && parsed.User != nil {
			return fmt.Errorf("proxyPools[%q].url: type %q takes no user:password@ (use type %q for credentialed forward proxies)", name, ProxyTypeRelay, ProxyTypeHTTP)
		}
		p.URL = u
	}
	enabledPools := 0
	for _, p := range cfg.ProxyPools {
		if p != nil && bool(p.Enabled) {
			enabledPools++
		}
	}

	if len(cfg.Providers) == 0 {
		return fmt.Errorf("providers: at least 1 provider required")
	}
	seenPrefix := map[string]string{} // prefix -> slug
	seenFullID := map[string]string{} // fullID -> slug
	for slug, prov := range cfg.Providers {
		base := fmt.Sprintf("providers[%q]", slug)
		if prov == nil {
			return fmt.Errorf("%s: entry null", base)
		}
		if strings.TrimSpace(prov.Name) == "" {
			return fmt.Errorf("%s.name: required", base)
		}
		if prov.Prefix == "" || !prefixRe.MatchString(prov.Prefix) {
			return fmt.Errorf("%s.prefix: must match ^[a-z0-9-]+$, got %q", base, prov.Prefix)
		}
		if other, dup := seenPrefix[prov.Prefix]; dup {
			return fmt.Errorf("%s.prefix: duplicate %q already used by %s", base, prov.Prefix, fmt.Sprintf("providers[%q]", other))
		}
		seenPrefix[prov.Prefix] = slug

		if prov.Type != "openai" {
			return fmt.Errorf("%s.type: must be \"openai\", got %q", base, prov.Type)
		}
		if prov.Interface != "chat-completions" && prov.Interface != "responses-api" {
			return fmt.Errorf("%s.interface: must be chat-completions|responses-api, got %q", base, prov.Interface)
		}

		norm := strings.TrimRight(strings.TrimSpace(prov.BaseURL), "/")
		if norm == "" || !(strings.HasPrefix(norm, "http://") || strings.HasPrefix(norm, "https://")) {
			return fmt.Errorf("%s.baseUrl: must be an http(s) URL, got %q", base, prov.BaseURL)
		}
		prov.BaseURL = norm

		if prov.TimeoutSecs <= 0 {
			return fmt.Errorf("%s.timeoutSecs: must be > 0, got %d", base, prov.TimeoutSecs)
		}

		if strings.TrimSpace(prov.Proxy) != "" {
			prov.Proxy = strings.TrimSpace(prov.Proxy)
			if mode, isMode := ProxyModeOf(prov.Proxy); isMode {
				if enabledPools == 0 {
					return fmt.Errorf("%s.proxy: mode %q needs >=1 enabled proxy pool", base, mode)
				}
				prov.Proxy = mode
			} else if _, ok := cfg.ProxyPools[prov.Proxy]; !ok {
				return fmt.Errorf("%s.proxy: pool %q not found in proxyPools (use a pool name, %q, or %q)", base, prov.Proxy, ProxyModeAuto, ProxyModeRound)
			}
		}

		prov.KeyStrategy = strings.ToLower(strings.TrimSpace(prov.KeyStrategy))
		switch prov.KeyStrategy {
		case "", KeyStrategyOrdered, KeyStrategyRound:
		default:
			return fmt.Errorf("%s.keyStrategy: must be \"ordered\" or \"round\"", base)
		}

		if len(prov.Keys) == 0 {
			return fmt.Errorf("%s.keys: at least 1 key required", base)
		}
		seenOrder := map[int]string{}
		enabledKeys := 0
		for kname, k := range prov.Keys {
			kb := fmt.Sprintf("%s.keys[%q]", base, kname)
			if k == nil {
				return fmt.Errorf("%s: entry null", kb)
			}
			if strings.TrimSpace(k.Key) == "" {
				return fmt.Errorf("%s.key: required", kb)
			}
			if k.Order < 1 {
				return fmt.Errorf("%s.order: must be >= 1, got %d", kb, int(k.Order))
			}
			if other, dup := seenOrder[int(k.Order)]; dup {
				return fmt.Errorf("%s.order: duplicate order %d already used by %s", kb, int(k.Order), fmt.Sprintf("%s.keys[%q]", base, other))
			}
			seenOrder[int(k.Order)] = kname
			if k.Proxy != nil {
				v := strings.TrimSpace(*k.Proxy)
				if _, isMode := ProxyModeOf(v); isMode {
					return fmt.Errorf("%s.proxy: proxy modes are not supported for keys (use %q for direct or a pool name)", kb, "")
				}
				if v != "" {
					if _, ok := cfg.ProxyPools[v]; !ok {
						return fmt.Errorf("%s.proxy: pool %q not found in proxyPools (use %q for direct or a pool name)", kb, v, "")
					}
				}
				k.Proxy = &v
			}
			if k.Enabled {
				enabledKeys++
			}
		}

		if len(prov.Models) == 0 && !prov.IsModelsAuto() {
			return fmt.Errorf("%s.models: at least 1 model required (or \"auto\")", base)
		}
		enabledModels := 0
		for mslug, m := range prov.Models {
			if prov.IsModelsAuto() {
				break // auto: tidak ada model statis untuk divalidasi
			}
			mb := fmt.Sprintf("%s.models[%q]", base, mslug)
			if m == nil {
				return fmt.Errorf("%s: entry null", mb)
			}
			if strings.TrimSpace(mslug) == "" {
				return fmt.Errorf("%s: empty model slug", base)
			}
			if m.Limit.Context <= 0 {
				return fmt.Errorf("%s.limit.context: must be > 0, got %d", mb, m.Limit.Context)
			}
			if m.Limit.Output <= 0 {
				return fmt.Errorf("%s.limit.output: must be > 0, got %d", mb, m.Limit.Output)
			}
			if m.Created < 0 {
				return fmt.Errorf("%s.created: must be >= 0 (unix timestamp, 0 = omitted), got %d", mb, int(m.Created))
			}
			if m.Enabled {
				enabledModels++
			}
			fullID := prov.Prefix + "/" + mslug
			if other, dup := seenFullID[fullID]; dup {
				return fmt.Errorf("%s: duplicate FullID %q already used by %s", mb, fullID, other)
			}
			seenFullID[fullID] = base
		}

		if prov.Enabled {
			if enabledKeys == 0 {
				return fmt.Errorf("%s.keys: enabled provider needs >=1 enabled key", base)
			}
			if !prov.IsModelsAuto() && enabledModels == 0 {
				return fmt.Errorf("%s.models: enabled provider needs >=1 enabled model", base)
			}
		}
	}
	return nil
}

func buildSnapshot(cfg *Config) *Snapshot {
	snap := &Snapshot{
		Gen:      1,
		Raw:      cfg,
		ModelMap: map[string]*ModelRef{},
		KeyRings: map[string][]*KeyEntry{},
	}
	// ProxyOrder: semua pool terurut order asc, nama asc (dasar mode auto/round).
	for name := range cfg.ProxyPools {
		snap.ProxyOrder = append(snap.ProxyOrder, name)
	}
	sort.Slice(snap.ProxyOrder, func(i, j int) bool {
		a, b := cfg.ProxyPools[snap.ProxyOrder[i]], cfg.ProxyPools[snap.ProxyOrder[j]]
		ao, bo := 0, 0
		if a != nil {
			ao = int(a.Order)
		}
		if b != nil {
			bo = int(b.Order)
		}
		if ao != bo {
			return ao < bo
		}
		return snap.ProxyOrder[i] < snap.ProxyOrder[j]
	})
	for slug, prov := range cfg.Providers {
		var ring []*KeyEntry
		for kname, k := range prov.Keys {
			if k == nil || !k.Enabled {
				continue
			}
			ring = append(ring, &KeyEntry{Name: kname, Key: k.Key, Order: int(k.Order), Proxy: k.Proxy})
		}
		sort.Slice(ring, func(i, j int) bool {
			if ring[i].Order != ring[j].Order {
				return ring[i].Order < ring[j].Order
			}
			return ring[i].Name < ring[j].Name
		})
		snap.KeyRings[slug] = ring

		if !prov.Enabled {
			continue
		}
		for mslug, m := range prov.Models {
			if m == nil || !m.Enabled {
				continue
			}
			fullID := prov.Prefix + "/" + mslug
			snap.ModelMap[fullID] = &ModelRef{
				ProviderSlug: slug,
				Provider:     prov,
				ModelSlug:    mslug,
				Model:        m,
				FullID:       fullID,
			}
		}
	}
	return snap
}
