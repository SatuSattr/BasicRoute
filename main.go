package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"basicroute/app/cli"
	"basicroute/app/config"
	"basicroute/app/server"
	"basicroute/app/service"
	"basicroute/app/ui"
)

const (
	drainTimeout    = 30 * time.Second
	shutdownTimeout = 30 * time.Second
)

func main() {
	cfgPath := flag.String("config", "config.jsonc", "path to JSONC config file")
	envPath := flag.String("env", "", "path to .env file for <ENV:...> placeholders (\"-\" = disabled; empty = auto-find <config-dir>/.env then ./.env)")
	debug := flag.Bool("v", false, "enable debug logs")
	flag.Parse()

	// Mode TUI bila stdin adalah terminal interaktif; bila di-pipe,
	// pakai CLI baris-perintah lama agar tetap bisa di-script.
	useUI := isTerminal()
	var log *slog.Logger
	var ring *ui.LogRing
	if useUI {
		ring = ui.NewLogRing(600)
		log = server.SetupTo(*debug, ring)
	} else {
		log = server.Setup(*debug)
	}

	if _, err := os.Stat(*cfgPath); errors.Is(err, os.ErrNotExist) {
		log.Error("config.jsonc not found", "path", *cfgPath)
		log.Error("copy config.example.jsonc to config.jsonc, then set the basics (host, port, keys, providers)")
		fmt.Fprintln(os.Stderr, "config.jsonc not found: "+*cfgPath)
		os.Exit(1)
	}

	snap, err := config.LoadWithEnv(*cfgPath, *envPath)
	if err != nil {
		log.Error("config load failed", "path", *cfgPath, "err", err.Error())
		fmt.Fprintln(os.Stderr, "config load failed: "+err.Error())
		os.Exit(1)
	}

	st := service.NewStore(snap)
	st.Usage = service.NewUsageTracker(snap.Raw.System.UsageFile)
	st.Usage.Load()
	usageStop := make(chan struct{})
	go st.Usage.AutoFlush(service.FlushInterval, usageStop)
	defer close(usageStop)
	pool := service.NewPool()
	srv := server.New(st, pool, log, int(snap.Raw.System.Limits.MaxBodyMB))
	handler := srv.Handler()
	go st.RefreshAutoModels(log)

	addr := fmt.Sprintf("%s:%d", snap.Raw.System.Host, snap.Raw.System.Port)
	boot := time.Now()
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		WriteTimeout:      0, // SSE streaming panjang tidak boleh diputus total-timeout
	}

	reload := func(reason string) {
		log.Info("reload requested", "reason", reason)
		staged, err := config.LoadWithEnv(*cfgPath, *envPath)
		if err != nil {
			log.Warn("reload rejected: config invalid", "err", err.Error())
			return
		}
		st.SetDraining(true)
		log.Info("reload draining", "inFlight", st.InFlight())
		st.WaitIdle(drainTimeout)
		// Flush usage sebelum ganti snapshot; bila path berubah, pindah tracker.
		if st.Usage != nil {
			_ = st.Usage.Flush()
			if staged.Raw.System.UsageFile != "" && staged.Raw.System.UsageFile != st.Usage.Path() {
				st.Usage = service.NewUsageTracker(staged.Raw.System.UsageFile)
				st.Usage.Load()
			}
		}
		st.Swap(staged)
		st.SetDraining(false)
		log.Info("reload ok", "gen", st.Gen())
		logModels(log, staged)
		go st.RefreshAutoModels(log)
	}

	var shutdownOnce sync.Once
	shutdown := func(reason string) {
		shutdownOnce.Do(func() {
			log.Info("shutdown requested, draining", "reason", reason, "inFlight", st.InFlight())
			st.SetDraining(true)
			st.WaitIdle(drainTimeout)
			if st.Usage != nil {
				if err := st.Usage.Flush(); err != nil {
					log.Warn("usage flush failed", "err", err.Error())
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := httpSrv.Shutdown(ctx); err != nil {
				log.Warn("shutdown error", "err", err.Error())
			}
		})
	}

	if !useUI {
		go cli.Run(cli.Deps{
			Store: st,
			Addr:  addr,
			Start: boot,
			OnReload: func(reason string) {
				reload(reason)
			},
			OnQuit: func() {
				shutdown("cli-quit")
			},
		})
	}

	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		for range hupCh {
			reload("sighup")
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		shutdown("signal")
	}()

	if useUI {
		runUI(log, ring, st, addr, boot, httpSrv, snap, reload, shutdown)
		return
	}

	log.Info("boot",
		"listen", addr,
		"gen", st.Gen(),
		"providers", len(snap.Raw.Providers),
		"models", len(snap.ModelMap),
		"pools", len(snap.Raw.ProxyPools),
	)
	logModels(log, snap)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("listen failed", "addr", addr, "err", err.Error())
		os.Exit(1)
	}
	log.Info("stopped")
}

// runTUI menjalankan HTTP server di background dan TUI fullscreen di foreground.
// Semua slog sudah mengarah ke ring sehingga tampil di window Logs.
func runUI(log *slog.Logger, ring *ui.LogRing, st *service.Store, addr string, boot time.Time, httpSrv *http.Server, snap *config.Snapshot, reload func(string), shutdown func(string)) {
	go func() {
		log.Info("boot",
			"listen", addr,
			"gen", st.Gen(),
			"providers", len(snap.Raw.Providers),
			"models", len(snap.ModelMap),
			"pools", len(snap.Raw.ProxyPools),
		)
		logModels(log, snap)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("listen failed", "addr", addr, "err", err.Error())
		}
	}()
	if err := ui.Run(ui.Deps{
		Store:   st,
		BaseURL: "http://" + addr,
		Start:   boot,
		Logs:    ring,
		OnReload: func(reason string) {
			reload(reason)
		},
		OnQuit: func() {
			shutdown("ui-quit")
		},
	}); err != nil {
		log.Error("ui failed", "err", err.Error())
		fmt.Fprintln(os.Stderr, "ui failed: "+err.Error())
	}
	shutdown("ui-exit")
	log.Info("stopped")
}

// isTerminal melaporkan apakah stdin adalah terminal interaktif.
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func logModels(log *slog.Logger, snap *config.Snapshot) {
	if snap == nil || snap.Raw == nil {
		return
	}
	slugs := make([]string, 0, len(snap.Raw.Providers))
	for slug := range snap.Raw.Providers {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		prov := snap.Raw.Providers[slug]
		if prov == nil {
			continue
		}
		ids := make([]string, 0)
		for id, ref := range snap.ModelMap {
			if ref != nil && ref.ProviderSlug == slug {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		status := "on"
		if !prov.Enabled {
			status = "off"
		}
		log.Info("models", "provider", fmt.Sprintf("%s (%s)", slug, prov.Name), "status", status, "list", strings.Join(ids, ", "))
	}
}
