package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"basicroute/app/config"
)

// AutoModel adalah satu model hasil fetch GET /models (memory only).
type AutoModel struct {
	ID      string
	Created int64
}

type modelsListResp struct {
	Data []modelsListItem `json:"data"`
}

type modelsListItem struct {
	ID      string          `json:"id"`
	Created json.RawMessage `json:"created"`
}

func parseCreated(raw json.RawMessage) int64 {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		if n < 0 {
			return 0
		}
		return n
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		if f <= 0 {
			return 0
		}
		return int64(f)
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		str = strings.TrimSpace(str)
		if v, err := strconv.ParseInt(str, 10, 64); err == nil && v > 0 {
			return v
		}
		if fv, err := strconv.ParseFloat(str, 64); err == nil && fv > 0 {
			return int64(fv)
		}
	}
	return 0
}

// fetchAutoModelsViaTarget melakukan satu GET /models lewat satu proxy target.
func fetchAutoModelsViaTarget(baseURL, proxyType, proxyURL, key string, timeout time.Duration) ([]AutoModel, int, error) {
	client, err := testClient(proxyType, proxyURL, timeout)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(http.MethodGet, ModelsURL(baseURL), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, errBody("auto models fetch", resp.StatusCode, body)
	}
	var list modelsListResp
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("auto models fetch: invalid models response: %w", err)
	}
	seen := map[string]bool{}
	out := make([]AutoModel, 0, len(list.Data))
	for _, it := range list.Data {
		id := strings.TrimSpace(it.ID)
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = strings.TrimSpace(id[i+1:])
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, AutoModel{ID: id, Created: parseCreated(it.Created)})
	}
	return out, resp.StatusCode, nil
}

// autoFetchWithFailover mencoba semua key (urut order) x semua proxy target
// (urut order, full-failover): key pertama dulu; auth-invalid (401/403) lanjut
// ke key berikutnya; proxy-side fault (transport/407) lanjut ke target
// berikutnya dengan key yang sama; sukses pertama langsung dipakai.
// Mengembalikan juga usedKey (key yang berhasil, "" bila gagal) dan badKeys
// (keyName -> pesan auth-invalid) agar pemanggil bisa flag key yang error,
// konsisten dengan test model manual (applyModelTestResult).
func autoFetchWithFailover(snap *config.Snapshot, prov *config.Provider, slug string, keys []*config.KeyEntry, targets []ProxyTarget, timeout time.Duration) (models []AutoModel, usedKey string, badKeys map[string]string, err error) {
	badKeys = map[string]string{}
	var lastErr error
	for _, ke := range keys {
		if ke == nil {
			continue
		}
		for _, tgt := range targets {
			m, status, ferr := fetchAutoModelsViaTarget(strings.TrimSpace(prov.BaseURL), tgt.Type, tgt.URL, ke.Key, timeout)
			if ferr == nil {
				return m, ke.Name, badKeys, nil
			}
			lastErr = ferr
			if IsAuthErr(ferr) {
				badKeys[ke.Name] = ferr.Error()
				break // key invalid -> key berikutnya
			}
			if ProxyFailureKind(tgt.Type, tgt.URL, status, ferr) {
				continue // pool mati -> target berikutnya, key sama
			}
			// upstream-side (5xx/dll): coba key berikutnya juga
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no enabled keys")
	}
	return nil, "", badKeys, lastErr
}

// RefreshAutoModels mem-fetch model untuk semua provider models:auto secara
// async (satu goroutine per provider). Kuning saat fetching, hijau saat ok,
// merah + pesan saat semua key gagal. Hasil basi (reload di tengah jalan)
// dibuang via cek generasi.
func (st *Store) RefreshAutoModels(log *slog.Logger) {
	if st == nil {
		return
	}
	snap := st.Get()
	if snap == nil || snap.Raw == nil {
		return
	}
	gen := st.Gen()
	for slug, prov := range snap.Raw.Providers {
		if prov == nil || !bool(prov.Enabled) || !prov.IsModelsAuto() {
			continue
		}
		go st.refreshOneAutoModel(log, gen, slug)
	}
}

func (st *Store) refreshOneAutoModel(log *slog.Logger, gen uint64, slug string) {
	snap := st.Get()
	if snap == nil || snap.Raw == nil {
		return
	}
	prov := snap.Raw.Providers[slug]
	if prov == nil || !bool(prov.Enabled) || !prov.IsModelsAuto() {
		return
	}
	id := ProvProviderID(slug)
	if st.ProvHealth != nil {
		st.ProvHealth.SetProgress(id, ProvPhaseFetching, 0, 0)
	}
	if log != nil {
		log.Info("auto models fetching", "provider", slug)
	}
	ring := snap.KeyRings[slug]
	if len(ring) == 0 {
		msg := fmt.Sprintf("auto models fetch failed: no enabled keys for provider %q", slug)
		if st.ProvHealth != nil {
			st.ProvHealth.SetError(id, msg)
		}
		if log != nil {
			log.Warn("auto models fetch failed", "provider", slug, "err", msg)
		}
		return
	}
	targets, err := ResolveProxies(snap, prov)
	if err != nil {
		msg := fmt.Sprintf("auto models fetch failed: %s", err.Error())
		if st.ProvHealth != nil {
			st.ProvHealth.SetError(id, msg)
		}
		if log != nil {
			log.Warn("auto models fetch failed", "provider", slug, "err", msg)
		}
		return
	}
	models, usedKey, badKeys, err := autoFetchWithFailover(snap, prov, slug, ring, targets, TestTimeout(int(prov.TimeoutSecs)))
	// Flag key yang terbukti auth-invalid selama fetch (langsung merah,
	// tanpa perlu tekan t). Key yang berhasil di-clear (self-heal).
	if st.ProvHealth != nil {
		for kn, msg := range badKeys {
			st.ProvHealth.SetError(ProvKeyID(slug, kn), msg)
		}
		if usedKey != "" {
			st.ProvHealth.SetOK(ProvKeyID(slug, usedKey))
		}
	}
	if err != nil {
		msg := fmt.Sprintf("auto models fetch failed for provider %q (%d keys tried): %s", slug, len(ring), err.Error())
		if st.ProvHealth != nil {
			st.ProvHealth.SetError(id, msg)
		}
		if log != nil {
			log.Warn("auto models fetch failed", "provider", slug, "err", err.Error())
		}
		return
	}
	if st.Gen() != gen {
		if log != nil {
			log.Info("auto models discarded (stale generation)", "provider", slug)
		}
		return
	}
	added := st.MergeAutoModels(slug, models)
	if st.ProvHealth != nil {
		st.ProvHealth.SetOK(id)
	}
	if log != nil {
		log.Info("auto models fetched", "provider", slug, "models", added)
	}
}

// MergeAutoModels mengganti entry auto provider di ModelMap (memory only,
// Raw tidak diubah) lalu Swap snapshot. Mengembalikan jumlah model baru.
// Entry basi provider yang sama dibuang dulu agar reload/fetch ulang bersih.
// FullID tabrakan dengan provider lain di-skip (log warn).
func (st *Store) MergeAutoModels(slug string, models []AutoModel) int {
	if st == nil {
		return 0
	}
	snap := st.Get()
	if snap == nil || snap.Raw == nil {
		return 0
	}
	prov := snap.Raw.Providers[slug]
	if prov == nil || !prov.IsModelsAuto() {
		return 0
	}
	next := &config.Snapshot{
		Raw:        snap.Raw,
		ModelMap:   make(map[string]*config.ModelRef, len(snap.ModelMap)+len(models)),
		KeyRings:   snap.KeyRings,
		ProxyOrder: snap.ProxyOrder,
	}
	for fullID, ref := range snap.ModelMap {
		if ref != nil && ref.ProviderSlug == slug {
			continue // buang hasil fetch lama provider ini
		}
		next.ModelMap[fullID] = ref
	}
	added := 0
	for _, am := range models {
		id := strings.TrimSpace(am.ID)
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = strings.TrimSpace(id[i+1:])
		}
		if id == "" {
			continue
		}
		fullID := prov.Prefix + "/" + id
		if _, dup := next.ModelMap[fullID]; dup {
			continue
		}
		next.ModelMap[fullID] = &config.ModelRef{
			ProviderSlug: slug,
			Provider:     prov,
			ModelSlug:    id,
			Model: &config.Model{
				Name:    id,
				Enabled: config.FlexBool(true),
				Created: config.FlexInt(am.Created),
				// Limit dibiarkan 0 = unknown, tampil "-" di UI/CLI.
			},
			FullID: fullID,
		}
		added++
	}
	st.Swap(next)
	return added
}
