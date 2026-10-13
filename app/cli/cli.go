package cli

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"basicroute/app/config"
	"basicroute/app/service"
)

// Deps adalah dependensi read-only untuk CLI interaktif.
// OnReload / OnQuit boleh nil (perintah terkait jadi no-op dengan pesan).
type Deps struct {
	Store    *service.Store
	Addr     string
	Start    time.Time
	Stdin    io.Reader // default os.Stdin
	Stdout   io.Writer // default os.Stdout
	OnReload func(reason string)
	OnQuit   func()
}

// Run menjalankan loop interaktif hingga EOF/interrupt.
// Hanya membaca Store; tidak pernah memutasi config.
func Run(d Deps) {
	in := d.Stdin
	if in == nil {
		in = os.Stdin
	}
	out := d.Stdout
	if out == nil {
		out = os.Stdout
	}
	if d.Store == nil {
		fmt.Fprintln(out, "cli: store not ready")
		return
	}

	sc := bufio.NewScanner(in)
	// Izinkan baris panjang (mis. paste URL/parameter).
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Fprint(out, "br> ")
		if !sc.Scan() {
			fmt.Fprintln(out)
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		cmd, arg := splitCmd(line)
		switch cmd {
		case "h", "help", "?":
			printHelp(out)
		case "status", "st":
			printStatus(out, d)
	case "system", "sys", "info":
		printSystem(out, d)
	case "apikey", "key":
		if arg != "" {
			printSysKeyDetail(out, d, arg)
		} else {
			printSystem(out, d)
		}
		case "providers", "provider", "prov", "ls":
			if arg != "" {
				printProviderDetail(out, d, arg)
			} else {
				printProviders(out, d)
			}
		case "show":
			if arg == "" {
				fmt.Fprintln(out, "usage: show <provider-slug|prefix>")
			} else {
				printProviderDetail(out, d, arg)
			}
		case "proxies", "proxy", "pools", "pool":
			if arg != "" {
				printProxyDetail(out, d, arg)
			} else if cmd == "proxy" {
				// "proxy" tanpa arg = daftar semua (read-only, konsisten).
				printProxies(out, d)
			} else {
				printProxies(out, d)
			}
		case "models", "model", "m":
			printModels(out, d, arg)
		case "stats", "stat":
			printStats(out, d)
		case "r", "reload":
			if d.OnReload != nil {
				d.OnReload("stdin")
			} else {
				fmt.Fprintln(out, "reload unavailable")
			}
		case "q", "quit", "exit":
			if d.OnQuit != nil {
				d.OnQuit()
			} else {
				fmt.Fprintln(out, "press Ctrl+C to stop")
			}
			return
		default:
			fmt.Fprintf(out, "unknown command: %q — type \"help\"\n", line)
		}
	}
}

func splitCmd(line string) (cmd, arg string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", ""
	}
	cmd = strings.ToLower(fields[0])
	arg = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	arg = strings.TrimSpace(arg)
	// dukung "provider:slug", "provider=slug"
	arg = strings.TrimPrefix(arg, ":")
	arg = strings.TrimPrefix(arg, "=")
	return strings.TrimSpace(cmd), strings.TrimSpace(arg)
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, "read-only commands (no config changes):")
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  help\tshow this help")
	fmt.Fprintln(tw, "  status\tServer summary (addr, gen, uptime, counts)")
	fmt.Fprintln(tw, "  system\tSystem details (host/port/auth/limits/keys)")
	fmt.Fprintln(tw, "  apikey [name]\tShow 1 system key (no arg = same as system)")
	fmt.Fprintln(tw, "  providers\tTable of all providers")
	fmt.Fprintln(tw, "  provider <slug|prefix>\tShow 1 provider (keys+models)")
	fmt.Fprintln(tw, "  models [filter]\tList models by FullID prefix/slug (optional substring filter)")
	fmt.Fprintln(tw, "  proxies\tTable of all proxy pools + users")
	fmt.Fprintln(tw, "  proxy <name>\tShow 1 proxy pool")
	fmt.Fprintln(tw, "  stats\tHits/errors/latency per provider|model")
	fmt.Fprintln(tw, "  r / reload\tReload config (as before)")
	fmt.Fprintln(tw, "  q / quit\tExit CLI + graceful shutdown")
	_ = tw.Flush()
	fmt.Fprintln(out, "note: all keys & proxy passwords are shown masked.")
}

func printStatus(out io.Writer, d Deps) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "status: snapshot not ready")
		return
	}
	provTotal := len(snap.Raw.Providers)
	provOn := 0
	modelTotal := 0
	modelOn := len(snap.ModelMap)
	for _, p := range snap.Raw.Providers {
		if p != nil && bool(p.Enabled) {
			provOn++
		}
		if p != nil {
			modelTotal += len(p.Models)
		}
	}
	poolTotal := len(snap.Raw.ProxyPools)
	poolOn := 0
	for _, p := range snap.Raw.ProxyPools {
		if p != nil && bool(p.Enabled) {
			poolOn++
		}
	}
	uptime := time.Since(d.Start).Round(time.Second)
	fmt.Fprintln(out, "status:")
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  listen\t%s\n", d.Addr)
	fmt.Fprintf(tw, "  gen\t%d\n", d.Store.Gen())
	fmt.Fprintf(tw, "  uptime\t%s\n", uptime)
	fmt.Fprintf(tw, "  inFlight\t%d\n", d.Store.InFlight())
	fmt.Fprintf(tw, "  draining\t%v\n", d.Store.IsDraining())
	fmt.Fprintf(tw, "  providers\t%d on / %d total\n", provOn, provTotal)
	fmt.Fprintf(tw, "  models\t%d enabled / %d total\n", modelOn, modelTotal)
	fmt.Fprintf(tw, "  proxyPools\t%d on / %d total\n", poolOn, poolTotal)
	_ = tw.Flush()
}

func printSystem(out io.Writer, d Deps) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "system: snapshot not ready")
		return
	}
	sys := snap.Raw.System
	fmt.Fprintln(out, "system:")
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  host\t%s\n", sys.Host)
	fmt.Fprintf(tw, "  port\t%d\n", int(sys.Port))
	fmt.Fprintf(tw, "  requireAuth\t%v\n", bool(sys.RequireAuth))
	fmt.Fprintf(tw, "  maxBodyMB\t%v\n", float64(sys.Limits.MaxBodyMB))
	fmt.Fprintf(tw, "  gen\t%d\n", d.Store.Gen())
	_ = tw.Flush()

	names := make([]string, 0, len(sys.Keys))
	for n := range sys.Keys {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(out, "  keys (%d):\n", len(names))
	tw2 := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw2, "    NAME\tSTATUS\tKEY\tTOTAL TOKENS")
	usage := map[string]uint64{}
	if d.Store != nil && d.Store.Usage != nil {
		usage = d.Store.Usage.Snapshot()
	}
	for _, n := range names {
		k := sys.Keys[n]
		if k == nil {
			fmt.Fprintf(tw2, "    %s\t(null)\t-\t%s\n", n, FormatTokens(0))
			continue
		}
		fmt.Fprintf(tw2, "    %s\t%s\t%s\t%s\n", n, onOff(bool(k.Enabled)), maskKey(k.Key), FormatTokens(usage[service.HashSysKey(k.Key)]))
	}
	_ = tw2.Flush()
}

// printSysKeyDetail menampilkan detail 1 system key (read-only, termasking).
func printSysKeyDetail(out io.Writer, d Deps, arg string) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "apikey: snapshot not ready")
		return
	}
	name := strings.TrimSpace(arg)
	k := snap.Raw.System.Keys[name]
	if k == nil {
		found := ""
		for n := range snap.Raw.System.Keys {
			if strings.EqualFold(n, name) {
				found = n
				break
			}
		}
		if found != "" {
			name = found
			k = snap.Raw.System.Keys[name]
		}
	}
	if k == nil {
		fmt.Fprintf(out, "api key %q not found\n", arg)
		return
	}
	fmt.Fprintf(out, "apikey %s:\n", name)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  status\t%s\n", onOff(bool(k.Enabled)))
	fmt.Fprintf(tw, "  key\t%s\n", maskKey(k.Key))
	var toks uint64
	if d.Store != nil && d.Store.Usage != nil {
		toks = d.Store.Usage.Total(service.HashSysKey(k.Key))
	}
	fmt.Fprintf(tw, "  totalTokens\t%s\n", FormatTokens(toks))
	_ = tw.Flush()
}

func printProviders(out io.Writer, d Deps) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "providers: snapshot not ready")
		return
	}
	slugs := make([]string, 0, len(snap.Raw.Providers))
	for s := range snap.Raw.Providers {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tPREFIX\tNAME\tST\tIFACE\tPROXY\tKEYS\tMODELS\tTIMEOUT")
	for _, slug := range slugs {
		p := snap.Raw.Providers[slug]
		if p == nil {
			fmt.Fprintf(tw, "%s\t-\t(null)\t-\t-\t-\t-\t-\t-\n", slug)
			continue
		}
		keysOn, keysTotal := countKeys(p.Keys)
		modelsOn, modelsTotal := countModels(p.Models)
		proxy := strings.TrimSpace(p.Proxy)
		if proxy == "" {
			proxy = "direct"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\t%d/%d\t%ds\n",
			slug, p.Prefix, p.Name, onOff(bool(p.Enabled)), p.Interface, proxy,
			keysOn, keysTotal, modelsOn, modelsTotal, int(p.TimeoutSecs))
	}
	_ = tw.Flush()
	fmt.Fprintln(out, "tip: \"provider <slug|prefix>\" for details.")
}

func printProviderDetail(out io.Writer, d Deps, arg string) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "provider: snapshot not ready")
		return
	}
	slug, p := lookupProvider(snap, arg)
	if p == nil {
		fmt.Fprintf(out, "provider %q not found — \"providers\" for the list\n", arg)
		return
	}
	proxy := strings.TrimSpace(p.Proxy)
	if proxy == "" {
		proxy = "direct"
	}
	fmt.Fprintf(out, "provider %s:\n", slug)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  name\t%s\n", p.Name)
	fmt.Fprintf(tw, "  prefix\t%s\n", p.Prefix)
	fmt.Fprintf(tw, "  status\t%s\n", onOff(bool(p.Enabled)))
	fmt.Fprintf(tw, "  type\t%s\n", p.Type)
	fmt.Fprintf(tw, "  interface\t%s\n", p.Interface)
	fmt.Fprintf(tw, "  baseUrl\t%s\n", p.BaseURL)
	fmt.Fprintf(tw, "  proxy\t%s\n", proxy)
	fmt.Fprintf(tw, "  timeout\t%ds\n", int(p.TimeoutSecs))
	_ = tw.Flush()

	// Keys terurut order.
	type krow struct {
		name string
		ord  int
		on   bool
		mask string
		cool bool
	}
	var rows []krow
	for n, k := range p.Keys {
		if k == nil {
			rows = append(rows, krow{name: n})
			continue
		}
		cool := d.Store.Cooldown(service.CooldownKey(slug, n))
		rows = append(rows, krow{name: n, ord: int(k.Order), on: bool(k.Enabled), mask: maskKey(k.Key), cool: cool})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ord != rows[j].ord {
			return rows[i].ord < rows[j].ord
		}
		return rows[i].name < rows[j].name
	})
	fmt.Fprintf(out, "  keys (%d):\n", len(rows))
	tw2 := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw2, "    NAME\tORDER\tSTATUS\tCOOLDOWN\tKEY")
	for _, r := range rows {
		cool := "-"
		if r.cool {
			cool = "cooling"
		}
		st := onOff(r.on)
		if r.mask == "" {
			st = "(null)"
		}
		fmt.Fprintf(tw2, "    %s\t%d\t%s\t%s\t%s\n", r.name, r.ord, st, cool, r.mask)
	}
	_ = tw2.Flush()

	// Models terurut slug.
	mslugs := make([]string, 0, len(p.Models))
	for m := range p.Models {
		mslugs = append(mslugs, m)
	}
	sort.Strings(mslugs)
	fmt.Fprintf(out, "  models (%d):\n", len(mslugs))
	tw3 := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw3, "    SLUG\tFULL ID\tSTATUS\tCONTEXT\tOUTPUT\tCREATED")
	for _, m := range mslugs {
		md := p.Models[m]
		if md == nil {
			fmt.Fprintf(tw3, "    %s\t%s/%s\t(null)\t-\t-\t-\n", m, p.Prefix, m)
			continue
		}
		fmt.Fprintf(tw3, "    %s\t%s/%s\t%s\t%d\t%d\t%s\n",
			m, p.Prefix, m, onOff(bool(md.Enabled)),
			int(md.Limit.Context), int(md.Limit.Output), formatCreated(int64(md.Created)))
	}
	_ = tw3.Flush()
}

func printProxies(out io.Writer, d Deps) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "proxies: snapshot not ready")
		return
	}
	users := reverseProxyUsers(snap)
	names := make([]string, 0, len(snap.Raw.ProxyPools))
	for n := range snap.Raw.ProxyPools {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(out, "proxyPools: (empty — all providers direct)")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSTATUS\tHEALTH\tURL\tUSED BY")
	for _, n := range names {
		pp := snap.Raw.ProxyPools[n]
		if pp == nil {
			fmt.Fprintf(tw, "%s\t(null)\t-\t-\t-\n", n)
			continue
		}
		used := strings.Join(users[n], ", ")
		if used == "" {
			used = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", n, proxyTypeLabel(pp.Type), onOff(bool(pp.Enabled)), proxyHealthLabel(d, n), maskProxyURL(pp.URL), used)
	}
	_ = tw.Flush()
	fmt.Fprintln(out, "tip: \"proxy <name>\" for details.")
}

func printProxyDetail(out io.Writer, d Deps, arg string) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "proxy: snapshot not ready")
		return
	}
	name := strings.TrimSpace(arg)
	pp := snap.Raw.ProxyPools[name]
	if pp == nil {
		// coba case-insensitive
		found := ""
		for n := range snap.Raw.ProxyPools {
			if strings.EqualFold(n, name) {
				found = n
				break
			}
		}
		if found != "" {
			name = found
			pp = snap.Raw.ProxyPools[name]
		}
	}
	if pp == nil {
		fmt.Fprintf(out, "proxy pool %q not found — \"proxies\" for the list\n", arg)
		return
	}
	fmt.Fprintf(out, "proxy %s:\n", name)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  type\t%s\n", proxyTypeLabel(pp.Type))
	fmt.Fprintf(tw, "  status\t%s\n", onOff(bool(pp.Enabled)))
	fmt.Fprintf(tw, "  health\t%s\n", proxyHealthLabel(d, name))
	fmt.Fprintf(tw, "  url\t%s\n", maskProxyURL(pp.URL))
	_ = tw.Flush()
	users := reverseProxyUsers(snap)[name]
	if len(users) == 0 {
		fmt.Fprintln(out, "  used by: (no provider uses this pool)")
		return
	}
	sort.Strings(users)
	fmt.Fprintf(out, "  used by (%d): %s\n", len(users), strings.Join(users, ", "))
}

func printModels(out io.Writer, d Deps, filter string) {
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		fmt.Fprintln(out, "models: snapshot not ready")
		return
	}
	filter = strings.ToLower(strings.TrimSpace(filter))
	type mrow struct {
		full, slug, prov string
		on               bool
		ctx, out         int
	}
	var rows []mrow
	for slug, p := range snap.Raw.Providers {
		if p == nil {
			continue
		}
		// filter boleh cocok ke slug provider / prefix / fullID.
		for mslug, md := range p.Models {
			full := p.Prefix + "/" + mslug
			if filter != "" &&
				!strings.Contains(strings.ToLower(full), filter) &&
				!strings.Contains(strings.ToLower(slug), filter) &&
				!strings.Contains(strings.ToLower(p.Prefix), filter) {
				continue
			}
			r := mrow{full: full, slug: mslug, prov: slug}
			if md != nil {
				r.on = bool(md.Enabled) && bool(p.Enabled)
				r.ctx = int(md.Limit.Context)
				r.out = int(md.Limit.Output)
			}
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].full < rows[j].full })
	if len(rows) == 0 {
		if filter != "" {
			fmt.Fprintf(out, "models: no match for %q\n", filter)
		} else {
			fmt.Fprintln(out, "models: (empty)")
		}
		return
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FULL ID\tPROVIDER\tSTATUS\tCONTEXT\tOUTPUT")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n", r.full, r.prov, onOff(r.on), r.ctx, r.out)
	}
	_ = tw.Flush()
}

func printStats(out io.Writer, d Deps) {
	stats := d.Store.Stats()
	hits, _ := stats["hits"].(map[string]uint64)
	errs, _ := stats["errors"].(map[string]uint64)
	avg, _ := stats["avgLatencyMs"].(map[string]float64)
	count, _ := stats["count"].(map[string]uint64)
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	for k := range hits {
		if _, ok := count[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		fmt.Fprintln(out, "stats: no traffic yet (empty hits)")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER|MODEL\tHITS\tERRORS\tAVG MS")
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f\n", k, hits[k], errs[k], avg[k])
	}
	_ = tw.Flush()
}

// ---- helpers (read-only, tanpa dependensi ke server agar tak import cycle) ----

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func maskKey(k string) string {
	if len(k) > 8 {
		return "****" + k[len(k)-4:]
	}
	if k == "" {
		return "-"
	}
	return "****"
}

// proxyTypeLabel menormalkan tipe pool untuk tampilan ("http"/"relay").
func proxyTypeLabel(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), config.ProxyTypeRelay) {
		return config.ProxyTypeRelay
	}
	return config.ProxyTypeHTTP
}

// proxyHealthLabel mengembalikan status health proxy dari memory ("ok"/"error").
func proxyHealthLabel(d Deps, name string) string {
	if d.Store == nil || d.Store.ProxyHealth == nil {
		return "ok"
	}
	if d.Store.ProxyHealth.IsError(name) {
		return "error"
	}
	return "ok"
}

func maskProxyURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "-"
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.User == nil {
		return u
	}
	user := parsed.User.Username()
	if _, hasPass := parsed.User.Password(); hasPass {
		parsed.User = url.UserPassword(user, "****")
	}
	return parsed.String()
}

func formatCreated(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	return time.Unix(unix, 0).Format("2006-01-02")
}

func countKeys(m map[string]*config.ProvKey) (on, total int) {
	for _, k := range m {
		total++
		if k != nil && bool(k.Enabled) {
			on++
		}
	}
	return on, total
}

func countModels(m map[string]*config.Model) (on, total int) {
	for _, md := range m {
		total++
		if md != nil && bool(md.Enabled) {
			on++
		}
	}
	return on, total
}

func lookupProvider(snap *config.Snapshot, arg string) (string, *config.Provider) {
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

func reverseProxyUsers(snap *config.Snapshot) map[string][]string {
	out := map[string][]string{}
	if snap == nil || snap.Raw == nil {
		return out
	}
	for slug, p := range snap.Raw.Providers {
		if p == nil {
			continue
		}
		name := strings.TrimSpace(p.Proxy)
		if name == "" {
			continue
		}
		out[name] = append(out[name], slug)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}
