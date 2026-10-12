package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"basicroute/app/config"
	"basicroute/app/model"
	"basicroute/app/service"
)

type Server struct {
	st      *service.Store
	pool    *service.Pool
	log     *slog.Logger
	maxBody int64
	start   time.Time
}

func New(st *service.Store, pool *service.Pool, log *slog.Logger, maxBodyMB int) *Server {
	if log == nil {
		log = slog.Default()
	}
	if st == nil {
		st = service.NewStore(nil)
	}
	if pool == nil {
		pool = service.NewPool()
	}
	if maxBodyMB <= 0 {
		maxBodyMB = 10
	}
	return &Server{
		st:      st,
		pool:    pool,
		log:     log,
		maxBody: int64(maxBodyMB) * 1024 * 1024,
		start:   time.Now(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/", s.handleModelOne) // GET /v1/models/{id}
	mux.HandleFunc("/v1/chat/completions", s.handleProxy)
	mux.HandleFunc("/v1/responses", s.handleProxy)
	mux.HandleFunc("/api/", s.apiAlias(mux))
	mux.HandleFunc("/", s.handleNotFound) // catch-all 404 JSON
	return mux
}

func (s *Server) apiAlias(mux *http.ServeMux) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api")
		if rest == "" {
			rest = "/"
		}
		if rest != "/health" && rest != "/v1" && !strings.HasPrefix(rest, "/v1/") {
			rest = "/v1" + rest
		}
		r2 := r.Clone(r.Context())
		u := *r.URL
		u.Path = rest
		r2.URL = &u
		r2.RequestURI = ""
		mux.ServeHTTP(w, r2)
	}
}


func writeErr(w http.ResponseWriter, status int, msg, typ, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.NewError(msg, typ, code))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}


func realIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isLoopback(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); first != "" {
				return first
			}
		}
	}
	return host
}

func isLoopback(h string) bool {
	h = strings.Trim(strings.TrimSpace(h), "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	scheme, tok, _ := strings.Cut(h, " ")
	if !strings.EqualFold(strings.TrimSpace(scheme), "bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}

func (s *Server) authorized(r *http.Request, snap *config.Snapshot) bool {
	if !snap.Raw.System.RequireAuth {
		return true
	}
	tok := bearerToken(r)
	if tok == "" {
		return false
	}
	for _, k := range snap.Raw.System.Keys {
		if k != nil && bool(k.Enabled) && subtle.ConstantTimeCompare([]byte(tok), []byte(k.Key)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) draining() bool {
	if s.st == nil {
		return false
	}
	return s.st.IsDraining()
}


func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}
	var gen uint64
	if snap := s.st.Get(); snap != nil {
		gen = snap.Gen
	}
	var stats map[string]any
	if s.st != nil {
		stats = s.st.Stats()
	} else {
		stats = map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"generation": gen,
		"inFlight":   s.st.InFlight(),
		"draining":   s.draining(),
		"uptimeSecs": int64(time.Since(s.start).Seconds()),
		"stats":      stats,
	})
}


func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}
	snap := s.st.Get()
	if snap == nil || snap.Raw == nil {
		writeErr(w, http.StatusServiceUnavailable, "no snapshot, retry shortly", "server_busy", "")
		return
	}
	if !s.authorized(r, snap) {
		writeErr(w, http.StatusUnauthorized, "invalid bearer token", "invalid_request_error", "invalid_api_key")
		return
	}
	ids := make([]string, 0, len(snap.ModelMap))
	for id := range snap.ModelMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data := make([]model.ModelObject, 0, len(ids))
	for _, id := range ids {
		ref := snap.ModelMap[id]
		if ref == nil || ref.Provider == nil || ref.Model == nil {
			continue
		}
		data = append(data, model.ModelObject{
			ID:      id,
			Object:  "model",
			Created: int64(ref.Model.Created),
			OwnedBy: ref.Provider.Prefix,
		})
	}
	writeJSON(w, http.StatusOK, model.ModelsListResponse{Object: "list", Data: data})
}


func (s *Server) handleModelOne(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}
	snap := s.st.Get()
	if snap == nil || snap.Raw == nil {
		writeErr(w, http.StatusServiceUnavailable, "no snapshot, retry shortly", "server_busy", "")
		return
	}
	if !s.authorized(r, snap) {
		writeErr(w, http.StatusUnauthorized, "invalid bearer token", "invalid_request_error", "invalid_api_key")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	if strings.TrimSpace(id) == "" {
		writeErr(w, http.StatusNotFound, "not found", "invalid_request_error", "not_found")
		return
	}
	ref := snap.ModelMap[id]
	if ref == nil || ref.Provider == nil || ref.Model == nil || !bool(ref.Provider.Enabled) {
		writeErr(w, http.StatusNotFound, "model not found: "+id, "invalid_request_error", "model_not_found")
		return
	}
	writeJSON(w, http.StatusOK, model.ModelObject{
		ID:      id,
		Object:  "model",
		Created: int64(ref.Model.Created),
		OwnedBy: ref.Provider.Prefix,
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotFound, "not found", "invalid_request_error", "not_found")
}


func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	reqID := ReqID()
	ip := realIP(r)
	w.Header().Set("X-Request-ID", reqID)
	lg := s.log.With("reqID", reqID, "ip", ip, "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "method_not_allowed")
		lg.Info("req<", "status", 405)
		return
	}
	snap := s.st.Get()
	if snap == nil || snap.Raw == nil {
		writeErr(w, http.StatusServiceUnavailable, "no snapshot, retry shortly", "server_busy", "")
		lg.Info("req<", "status", 503)
		return
	}
	if !s.authorized(r, snap) {
		writeErr(w, http.StatusUnauthorized, "invalid bearer token", "invalid_request_error", "invalid_api_key")
		lg.Info("req<", "status", 401)
		return
	}
	if !s.st.Begin() {
		writeErr(w, http.StatusServiceUnavailable, "reloading, retry shortly", "server_busy", "")
		lg.Info("req<", "status", 503, "draining", true)
		return
	}
	defer s.st.Done()

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "body_too_large")
			lg.Info("req<", "status", 413)
			return
		}
		writeErr(w, http.StatusBadRequest, "cannot read request body", "invalid_request_error", "read_error")
		lg.Info("req<", "status", 400)
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body", "invalid_request_error", "invalid_json")
		lg.Info("req<", "status", 400)
		return
	}
	modelID, _ := body["model"].(string)
	if strings.TrimSpace(modelID) == "" {
		writeErr(w, http.StatusBadRequest, `field "model" is required`, "invalid_request_error", "missing_model")
		lg.Info("req<", "status", 400)
		return
	}
	stream, _ := body["stream"].(bool)

	ref, ok := snap.ModelMap[modelID]
	if !ok || ref == nil || ref.Provider == nil || !bool(ref.Provider.Enabled) {
		writeErr(w, http.StatusNotFound, "model not found: "+modelID, "invalid_request_error", "model_not_found")
		lg.Info("req<", "status", 404, "model", modelID)
		return
	}
	lg.Info("req>", "model", modelID, "stream", stream)

	ring := snap.KeyRings[ref.ProviderSlug]
	if len(ring) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "no enabled keys for provider", "server_busy", "")
		lg.Info("req<", "status", 503, "provider", ref.ProviderSlug)
		return
	}
	body["model"] = ref.ModelSlug
	upBody, err := json.Marshal(body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot encode upstream body", "server_error", "")
		lg.Info("req<", "status", 500)
		return
	}

	target := service.TargetURL(ref.Provider.BaseURL, ref.Provider.Interface)

	proxyURL, err := resolveProxyURL(snap, ref.Provider)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error(), "server_error", "bad_gateway")
		lg.Warn("proxy resolve failed", "provider", ref.ProviderSlug, "proxy", ref.Provider.Proxy, "err", err.Error())
		return
	}
	client, err := s.pool.ClientFor(proxyURL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("proxy %q: %v", ref.Provider.Proxy, err), "server_error", "bad_gateway")
		lg.Warn("proxy client failed", "provider", ref.ProviderSlug, "proxy", ref.Provider.Proxy, "err", err.Error())
		return
	}

	timeout := time.Duration(ref.Provider.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	tried := 0
	for i, ke := range ring {
		if ke == nil {
			continue
		}
		keyID := service.CooldownKey(ref.ProviderSlug, ke.Name)
		if s.st.Cooldown(keyID) {
			continue
		}
		tried++

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(upBody))
		if err != nil {
			cancel()
			writeErr(w, http.StatusInternalServerError, "cannot build upstream request", "server_error", "")
			lg.Info("req<", "status", 500)
			return
		}
		service.CopyHeaders(upReq.Header, r.Header)
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set("Authorization", "Bearer "+ke.Key)

		lg.Info("up>", "target", target, "provider", ref.ProviderSlug, "key", ke.Name, "masked", MaskKey(ke.Key))
		t0 := time.Now()
		resp, err := client.Do(upReq)
		if err != nil {
			el := time.Since(t0)
			cancel()
			s.st.MarkCooldown(keyID, 60*time.Second)
			s.st.Record(ref.ProviderSlug, ref.FullID, el.Milliseconds(), true)
			lg.Warn("up<", "provider", ref.ProviderSlug, "key", ke.Name, "err", err.Error(), "latencyMs", el.Milliseconds())
			if service.IsRetryable(0, err) && hasRemaining(s.st, ring, i, ref.ProviderSlug) {
				continue
			}
			if service.IsRetryable(0, err) && tried < len(ring) {
				continue
			}
			writeErr(w, http.StatusBadGateway, "all upstreams failed: "+err.Error(), "server_error", "bad_gateway")
			lg.Info("req<", "status", 502)
			return
		}
		ttft := time.Since(t0) // header upstream diterima (TTFT aproksimasi)
		if service.IsRetryable(resp.StatusCode, nil) && hasRemaining(s.st, ring, i, ref.ProviderSlug) {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			cancel()
			s.st.MarkCooldown(keyID, 60*time.Second)
			s.st.Record(ref.ProviderSlug, ref.FullID, ttft.Milliseconds(), true)
			lg.Warn("up<", "provider", ref.ProviderSlug, "key", ke.Name, "status", resp.StatusCode, "retry", true)
			continue
		}
		sse := stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
		} else if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		var n int64
		if sse {
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
				n, _ = io.Copy(flushWriter{w: w, f: fl}, resp.Body)
			} else {
				n, _ = io.Copy(w, resp.Body)
			}
		} else {
			n, _ = io.Copy(w, resp.Body)
		}
		resp.Body.Close()
		cancel()
		el := time.Since(t0)
		ok2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
		s.st.Record(ref.ProviderSlug, ref.FullID, el.Milliseconds(), !ok2xx)
		lg.Info("req<", "status", resp.StatusCode, "bytes", n, "latencyMs", el.Milliseconds(), "ttftMs", ttft.Milliseconds(), "key", ke.Name)
		return
	}
	if tried == 0 {
		writeErr(w, http.StatusServiceUnavailable, "all keys cooling down, retry shortly", "server_error", "all_keys_cooling_down")
		lg.Info("req<", "status", 503, "provider", ref.ProviderSlug)
		return
	}
	writeErr(w, http.StatusBadGateway, "all upstreams failed", "server_error", "bad_gateway")
	lg.Info("req<", "status", 502)
}

func resolveProxyURL(snap *config.Snapshot, prov *config.Provider) (string, error) {
	if prov == nil || strings.TrimSpace(prov.Proxy) == "" {
		return "", nil
	}
	name := strings.TrimSpace(prov.Proxy)
	p, ok := snap.Raw.ProxyPools[name]
	if !ok || p == nil {
		return "", fmt.Errorf("proxy pool %q not found", name)
	}
	if !p.Enabled {
		return "", fmt.Errorf("proxy pool %q disabled", name)
	}
	return strings.TrimSpace(p.URL), nil
}

func hasRemaining(st *service.Store, ring []*config.KeyEntry, cur int, provSlug string) bool {
	for _, ke := range ring[cur+1:] {
		if ke == nil {
			continue
		}
		if !st.Cooldown(service.CooldownKey(provSlug, ke.Name)) {
			return true
		}
	}
	return false
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.f.Flush()
	return n, err
}
