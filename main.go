package main

import (
	"bufio"
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
	"syscall"
	"time"

	"basicroute/app/config"
	"basicroute/app/server"
	"basicroute/app/service"
)

const (
	drainTimeout    = 30 * time.Second
	shutdownTimeout = 30 * time.Second
)

func main() {
	cfgPath := flag.String("config", "config.jsonc", "path file config JSONC")
	envPath := flag.String("env", "", "path file .env untuk placeholder <ENV:...> (\"-\" = nonaktif; kosong = otomatis cari <dir-config>/.env lalu ./.env)")
	debug := flag.Bool("v", false, "aktifkan log debug")
	flag.Parse()

	log := server.Setup(*debug)

	if _, err := os.Stat(*cfgPath); errors.Is(err, os.ErrNotExist) {
		log.Error("config.jsonc not found", "path", *cfgPath)
		log.Error("copy config.example.jsonc menjadi config.jsonc lalu lakukan konfigurasi dasar (host, port, keys, providers)")
		os.Exit(1)
	}

	snap, err := config.LoadWithEnv(*cfgPath, *envPath)
	if err != nil {
		log.Error("config load failed", "path", *cfgPath, "err", err.Error())
		os.Exit(1)
	}

	st := service.NewStore(snap)
	pool := service.NewPool()
	srv := server.New(st, pool, log, int(snap.Raw.System.Limits.MaxBodyMB))
	handler := srv.Handler()

	addr := fmt.Sprintf("%s:%d", snap.Raw.System.Host, snap.Raw.System.Port)
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
		st.Swap(staged)
		st.SetDraining(false)
		log.Info("reload ok", "gen", st.Gen())
		logModels(log, staged)
	}

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "r" || strings.TrimSpace(sc.Text()) == "R" {
				reload("stdin")
			}
		}
	}()

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
		log.Info("shutdown requested, draining", "inFlight", st.InFlight())
		st.SetDraining(true)
		st.WaitIdle(drainTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			log.Warn("shutdown error", "err", err.Error())
		}
	}()

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
