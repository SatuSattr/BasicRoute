package cli

import (
	"strings"
)

// Render* adalah versi string dari print* di cli.go, agar output yang sama
// bisa dipakai ulang oleh TUI (atau pemanggil lain) tanpa duplikasi logika.
// Semuanya read-only terhadap Store.

// Split mengurai baris perintah menjadi (command, arg) lowercase.
// Diekspor agar TUI memakai parser yang sama dengan loop CLI.
func Split(line string) (cmd, arg string) { return splitCmd(line) }

// MaskProxyURL menyamarkan password dalam URL proxy (diekspor untuk TUI).
func MaskProxyURL(s string) string { return maskProxyURL(s) }

// MaskKey menyamarkan satu API key (diekspor untuk TUI).
func MaskKey(k string) string { return maskKey(k) }

// Summary adalah angka ringkas untuk header (TUI/status).
type Summary struct {
	Gen                 uint64
	ProvOn, ProvTotal   int
	ModelOn, ModelTotal int
	PoolOn, PoolTotal   int
}

// Summarize menghitung Summary dari snapshot aktif (read-only).
func Summarize(d Deps) Summary {
	var s Summary
	if d.Store == nil {
		return s
	}
	s.Gen = d.Store.Gen()
	snap := d.Store.Get()
	if snap == nil || snap.Raw == nil {
		return s
	}
	s.ProvTotal = len(snap.Raw.Providers)
	s.ModelOn = len(snap.ModelMap)
	for _, p := range snap.Raw.Providers {
		if p == nil {
			continue
		}
		if bool(p.Enabled) {
			s.ProvOn++
		}
		s.ModelTotal += len(p.Models)
	}
	s.PoolTotal = len(snap.Raw.ProxyPools)
	for _, p := range snap.Raw.ProxyPools {
		if p != nil && bool(p.Enabled) {
			s.PoolOn++
		}
	}
	return s
}

func finish(sb *strings.Builder) string {
	return strings.TrimRight(sb.String(), "\n")
}

// RenderHelp, RenderStatus, RenderSystem, RenderProviders, RenderProvider,
// RenderProxies, RenderProxy, RenderModels, RenderStats: output polos
// (tanpa warna) untuk dibungkus style oleh TUI.
func RenderHelp(d Deps) string {
	var sb strings.Builder
	printHelp(&sb)
	return finish(&sb)
}

func RenderStatus(d Deps) string {
	var sb strings.Builder
	printStatus(&sb, d)
	return finish(&sb)
}

func RenderSystem(d Deps) string {
	var sb strings.Builder
	printSystem(&sb, d)
	return finish(&sb)
}

func RenderSysKey(d Deps, arg string) string {
	var sb strings.Builder
	printSysKeyDetail(&sb, d, arg)
	return finish(&sb)
}

func RenderProviders(d Deps) string {
	var sb strings.Builder
	printProviders(&sb, d)
	return finish(&sb)
}

func RenderProvider(d Deps, arg string) string {
	var sb strings.Builder
	printProviderDetail(&sb, d, arg)
	return finish(&sb)
}

func RenderProxies(d Deps) string {
	var sb strings.Builder
	printProxies(&sb, d)
	return finish(&sb)
}

func RenderProxy(d Deps, arg string) string {
	var sb strings.Builder
	printProxyDetail(&sb, d, arg)
	return finish(&sb)
}

func RenderModels(d Deps, filter string) string {
	var sb strings.Builder
	printModels(&sb, d, filter)
	return finish(&sb)
}

func RenderStats(d Deps) string {
	var sb strings.Builder
	printStats(&sb, d)
	return finish(&sb)
}
