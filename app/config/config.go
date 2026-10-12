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
	Host        string             `json:"host"`
	Port        FlexInt            `json:"port"`
	RequireAuth FlexBool           `json:"requireAuth"`
	Keys        map[string]*SysKey `json:"keys"`
	Limits      Limits             `json:"limits"`
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
	Enabled     FlexBool            `json:"enabled"`
	TimeoutSecs FlexInt             `json:"timeoutSecs"`
	Keys        map[string]*ProvKey `json:"keys"`
	Models      map[string]*Model   `json:"models"`
}

type ProvKey struct {
	Key     string   `json:"key"`
	Order   FlexInt  `json:"order"` // urutan failover, unik per provider
	Enabled FlexBool `json:"enabled"`
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

type ProxyPool struct {
	URL     string   `json:"url"`
	Enabled FlexBool `json:"enabled"`
}


type Snapshot struct {
	Gen      uint64
	Raw      *Config
	ModelMap map[string]*ModelRef   // key = FullID ("<prefix>/<modelSlug>")
	KeyRings map[string][]*KeyEntry // key = provider slug, value = enabled keys terurut Order
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
		return fmt.Errorf("system.host: harus diisi")
	}
	if cfg.System.Port < 1 || cfg.System.Port > 65535 {
		return fmt.Errorf("system.port: harus 1-65535, dapat %d", cfg.System.Port)
	}
	if cfg.System.Limits.MaxBodyMB <= 0 {
		return fmt.Errorf("system.limits.maxBodyMB: harus > 0, dapat %v", cfg.System.Limits.MaxBodyMB)
	}
	enabledSysKeys := 0
	for name, k := range cfg.System.Keys {
		if k == nil {
			return fmt.Errorf("system.keys[%q]: entry null", name)
		}
		if strings.TrimSpace(k.Key) == "" {
			return fmt.Errorf("system.keys[%q].key: harus diisi", name)
		}
		if k.Enabled {
			enabledSysKeys++
		}
	}
	if cfg.System.RequireAuth && enabledSysKeys == 0 {
		return fmt.Errorf("system.keys: requireAuth=true butuh >=1 system key enabled")
	}

	if cfg.ProxyPools == nil {
		cfg.ProxyPools = map[string]*ProxyPool{}
	}
	for name, p := range cfg.ProxyPools {
		if p == nil {
			return fmt.Errorf("proxyPools[%q]: entry null", name)
		}
		u := strings.TrimSpace(p.URL)
		if u == "" {
			return fmt.Errorf("proxyPools[%q].url: harus diisi", name)
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("proxyPools[%q].url: URL tidak valid %q", name, p.URL)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
		default:
			return fmt.Errorf("proxyPools[%q].url: scheme harus http/https (socks5 belum didukung), dapat %q", name, parsed.Scheme)
		}
		p.URL = u
	}

	if len(cfg.Providers) == 0 {
		return fmt.Errorf("providers: minimal 1 provider")
	}
	seenPrefix := map[string]string{} // prefix -> slug
	seenFullID := map[string]string{} // fullID -> slug
	for slug, prov := range cfg.Providers {
		base := fmt.Sprintf("providers[%q]", slug)
		if prov == nil {
			return fmt.Errorf("%s: entry null", base)
		}
		if strings.TrimSpace(prov.Name) == "" {
			return fmt.Errorf("%s.name: harus diisi", base)
		}
		if prov.Prefix == "" || !prefixRe.MatchString(prov.Prefix) {
			return fmt.Errorf("%s.prefix: harus cocok ^[a-z0-9-]+$, dapat %q", base, prov.Prefix)
		}
		if other, dup := seenPrefix[prov.Prefix]; dup {
			return fmt.Errorf("%s.prefix: duplikat %q sudah dipakai %s", base, prov.Prefix, fmt.Sprintf("providers[%q]", other))
		}
		seenPrefix[prov.Prefix] = slug

		if prov.Type != "openai" {
			return fmt.Errorf("%s.type: harus \"openai\", dapat %q", base, prov.Type)
		}
		if prov.Interface != "chat-completions" && prov.Interface != "responses-api" {
			return fmt.Errorf("%s.interface: harus chat-completions|responses-api, dapat %q", base, prov.Interface)
		}

		norm := strings.TrimRight(strings.TrimSpace(prov.BaseURL), "/")
		if norm == "" || !(strings.HasPrefix(norm, "http://") || strings.HasPrefix(norm, "https://")) {
			return fmt.Errorf("%s.baseUrl: harus http(s) URL, dapat %q", base, prov.BaseURL)
		}
		prov.BaseURL = norm

		if prov.TimeoutSecs <= 0 {
			return fmt.Errorf("%s.timeoutSecs: harus > 0, dapat %d", base, prov.TimeoutSecs)
		}

		if strings.TrimSpace(prov.Proxy) != "" {
			prov.Proxy = strings.TrimSpace(prov.Proxy)
			if _, ok := cfg.ProxyPools[prov.Proxy]; !ok {
				return fmt.Errorf("%s.proxy: pool %q tidak ada di proxyPools", base, prov.Proxy)
			}
		}

		if len(prov.Keys) == 0 {
			return fmt.Errorf("%s.keys: minimal 1 key", base)
		}
		seenOrder := map[int]string{}
		enabledKeys := 0
		for kname, k := range prov.Keys {
			kb := fmt.Sprintf("%s.keys[%q]", base, kname)
			if k == nil {
				return fmt.Errorf("%s: entry null", kb)
			}
			if strings.TrimSpace(k.Key) == "" {
				return fmt.Errorf("%s.key: harus diisi", kb)
			}
			if k.Order < 1 {
				return fmt.Errorf("%s.order: harus >= 1, dapat %d", kb, int(k.Order))
			}
			if other, dup := seenOrder[int(k.Order)]; dup {
				return fmt.Errorf("%s.order: duplikat order %d sudah dipakai %s", kb, int(k.Order), fmt.Sprintf("%s.keys[%q]", base, other))
			}
			seenOrder[int(k.Order)] = kname
			if k.Enabled {
				enabledKeys++
			}
		}

		if len(prov.Models) == 0 {
			return fmt.Errorf("%s.models: minimal 1 model", base)
		}
		enabledModels := 0
		for mslug, m := range prov.Models {
			mb := fmt.Sprintf("%s.models[%q]", base, mslug)
			if m == nil {
				return fmt.Errorf("%s: entry null", mb)
			}
			if strings.TrimSpace(mslug) == "" {
				return fmt.Errorf("%s: slug model kosong", base)
			}
			if m.Limit.Context <= 0 {
				return fmt.Errorf("%s.limit.context: harus > 0, dapat %d", mb, m.Limit.Context)
			}
			if m.Limit.Output <= 0 {
				return fmt.Errorf("%s.limit.output: harus > 0, dapat %d", mb, m.Limit.Output)
			}
			if m.Created < 0 {
				return fmt.Errorf("%s.created: harus >= 0 (unix timestamp, 0 = tidak diekspos), dapat %d", mb, int(m.Created))
			}
			if m.Enabled {
				enabledModels++
			}
			fullID := prov.Prefix + "/" + mslug
			if other, dup := seenFullID[fullID]; dup {
				return fmt.Errorf("%s: FullID duplikat %q sudah dipakai %s", mb, fullID, other)
			}
			seenFullID[fullID] = base
		}

		if prov.Enabled {
			if enabledKeys == 0 {
				return fmt.Errorf("%s.keys: provider enabled butuh >=1 key enabled", base)
			}
			if enabledModels == 0 {
				return fmt.Errorf("%s.models: provider enabled butuh >=1 model enabled", base)
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
	for slug, prov := range cfg.Providers {
		var ring []*KeyEntry
		for kname, k := range prov.Keys {
			if k == nil || !k.Enabled {
				continue
			}
			ring = append(ring, &KeyEntry{Name: kname, Key: k.Key, Order: int(k.Order)})
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
