package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
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

	// Guard silent re-test key error (satu jalan per provider + interval).
	retestMu   sync.Mutex
	retestLast map[string]time.Time
	retestRun  map[string]bool

	// Guard silent re-test proxy error (satu jalan per pool + interval).
	proxyRetestLast map[string]time.Time
	proxyRetestRun  map[string]bool
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
		st:              st,
		pool:            pool,
		log:             log,
		maxBody:         int64(maxBodyMB) * 1024 * 1024,
		start:           time.Now(),
		retestLast:      map[string]time.Time{},
		retestRun:       map[string]bool{},
		proxyRetestLast: map[string]time.Time{},
		proxyRetestRun:  map[string]bool{},
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

func (s *Server) authorize(r *http.Request, snap *config.Snapshot) (name string, ok bool) {
	if !snap.Raw.System.RequireAuth {
		return "", true
	}
	tok := bearerToken(r)
	if tok == "" {
		return "", false
	}
	for n, k := range snap.Raw.System.Keys {
		if k != nil && bool(k.Enabled) && subtle.ConstantTimeCompare([]byte(tok), []byte(k.Key)) == 1 {
			return n, true
		}
	}
	return "", false
}

func (s *Server) authorized(r *http.Request, snap *config.Snapshot) bool {
	_, ok := s.authorize(r, snap)
	return ok
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
	callerName, okAuth := s.authorize(r, snap)
	if !okAuth {
		writeErr(w, http.StatusUnauthorized, "invalid bearer token", "invalid_request_error", "invalid_api_key")
		lg.Info("req<", "status", 401)
		return
	}
	callerHash := ""
	if tok := bearerToken(r); tok != "" {
		callerHash = service.HashSysKey(tok)
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

	upURL := service.TargetURL(ref.Provider.BaseURL, ref.Provider.Interface)

	// Kandidat proxy: direct / pool / auto / round (urutan + prioritas sehat).
	targets, err := s.proxyCandidates(snap, ref.ProviderSlug, ref.Provider)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error(), "server_error", "bad_gateway")
		lg.Warn("proxy resolve failed", "provider", ref.ProviderSlug, "proxy", ref.Provider.Proxy, "err", err.Error())
		return
	}

	timeout := time.Duration(ref.Provider.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	healthModelID := service.ProvModelID(ref.ProviderSlug, ref.ModelSlug)
	// Model flagged: satu percobaan saja (key sehat pertama), tanpa failover.
	// Gagal -> flag dipertahankan, error upstream diteruskan ke requester.
	singleTry := s.st.ProvHealth.IsError(healthModelID)

	// Key flagged di-skip bila masih ada alternatif sehat; bila semua
	// flagged (last-resort) tetap dicoba agar tidak outage total.
	hasHealthy := false
	for _, ke := range ring {
		if ke == nil {
			continue
		}
		id := service.CooldownKey(ref.ProviderSlug, ke.Name)
		if s.st.Cooldown(id) || s.st.ProvHealth.IsError(id) {
			continue
		}
		hasHealthy = true
		break
	}

	// Request masuk: silent re-test key & proxy ber-flag di background
	// (self-heal), tanpa mengganggu response ini.
	s.maybeRetestKeys(snap, ref.ProviderSlug, ref.Provider)
	s.maybeRetestProxies(snap, targets)

	// Driver: proxy di loop luar (failover antar pool), key di loop dalam.
	tried := 0
	var lastErr error
	for _, tgt := range targets {
		client, cerr := s.pool.ClientFor(tgt.Type, tgt.URL)
		if cerr != nil {
			lg.Warn("proxy client failed", "provider", ref.ProviderSlug, "proxy", tgt.Name, "err", cerr.Error())
			if tgt.Name != "" {
				s.st.ProxyHealth.SetError(tgt.Name, cerr.Error())
			}
			lastErr = cerr
			continue
		}
		run := &upstreamRun{
			s: s, w: w, r: r, lg: lg, ref: ref, client: client, tgt: tgt,
			upURL: upURL, upBody: upBody, timeout: timeout, stream: stream,
			singleTry: singleTry, ring: ring, healthy: hasHealthy,
			healthModelID: healthModelID, callerHash: callerHash, callerName: callerName,
		}
		if run.exec(&tried) == runServed {
			return
		}
		if run.lastErr != nil {
			lastErr = run.lastErr
		}
	}

	if tried == 0 {
		writeErr(w, http.StatusServiceUnavailable, "all keys cooling down, retry shortly", "server_error", "all_keys_cooling_down")
		lg.Info("req<", "status", 503, "provider", ref.ProviderSlug)
		return
	}
	msg := "all upstreams failed"
	if lastErr != nil {
		msg += ": " + lastErr.Error()
	}
	writeErr(w, http.StatusBadGateway, msg, "server_error", "bad_gateway")
	lg.Info("req<", "status", 502)
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

// capture menyimpan ekor byte (tail) hingga limit agar ekstraksi usage
// tetap dapat chunk terakhir SSE walau body besar.
type capture struct {
	buf   []byte
	limit int
}

func newCapture(limit int) *capture {
	if limit <= 0 {
		limit = 4 << 20
	}
	return &capture{limit: limit}
}

func (c *capture) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	if len(c.buf) > c.limit {
		c.buf = c.buf[len(c.buf)-c.limit:]
	}
	return len(p), nil
}

func (c *capture) Bytes() []byte {
	if c == nil {
		return nil
	}
	return c.buf
}

type usagePayload struct {
	Usage struct {
		TotalTokens      int64 `json:"total_tokens"`
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// extractTotalTokens mengambil total_tokens dari body upstream.
// sse=true: pindai tiap baris data: dari belakang, ambil usage terakhir.
// sse=false: unmarshal JSON penuh, fallback ke pindai substring.
func extractTotalTokens(data []byte, sse bool) uint64 {
	if len(data) == 0 {
		return 0
	}
	if sse {
		lines := bytes.Split(data, []byte("\n"))
		for i := len(lines) - 1; i >= 0; i-- {
			ln := bytes.TrimSpace(lines[i])
			if len(ln) == 0 {
				continue
			}
			if bytes.HasPrefix(ln, []byte("data:")) {
				ln = bytes.TrimSpace(ln[len("data:"):])
			} else if !(len(ln) > 0 && ln[0] == '{') {
				continue
			}
			if bytes.Equal(ln, []byte("[DONE]")) {
				continue
			}
			var u usagePayload
			if err := json.Unmarshal(ln, &u); err != nil {
				continue
			}
			if t := usageTotal(u); t > 0 {
				return uint64(t)
			}
		}
		// Fallback: body SSE mungkin terpotong di tengah JSON,
		// pindai angka total_tokens terakhir yang utuh.
		return scanLastTotalTokens(data)
	}
	var u usagePayload
	if err := json.Unmarshal(data, &u); err == nil {
		if t := usageTotal(u); t > 0 {
			return uint64(t)
		}
	}
	return scanLastTotalTokens(data)
}

func usageTotal(u usagePayload) int64 {
	if u.Usage.TotalTokens > 0 {
		return u.Usage.TotalTokens
	}
	if u.Usage.PromptTokens > 0 || u.Usage.CompletionTokens > 0 {
		return u.Usage.PromptTokens + u.Usage.CompletionTokens
	}
	return 0
}

// scanLastTotalTokens mencari `"total_tokens":123` terakhir di blob.
func scanLastTotalTokens(data []byte) uint64 {
	var last uint64
	for i := 0; i < len(data); i++ {
		if !hasKeyAt(data, i, `"total_tokens"`) {
			continue
		}
		j := i + len(`"total_tokens"`)
		for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\r' || data[j] == '\n' || data[j] == '"' || data[j] == ':' || data[j] == '=') {
			j++
		}
		var n uint64
		digits := 0
		for j < len(data) && data[j] >= '0' && data[j] <= '9' {
			n = n*10 + uint64(data[j]-'0')
			j++
			digits++
		}
		if digits > 0 {
			last = n
		}
	}
	return last
}

func hasKeyAt(data []byte, i int, key string) bool {
	if i+len(key) > len(data) {
		return false
	}
	for k := 0; k < len(key); k++ {
		if data[i+k] != key[k] {
			return false
		}
	}
	return true
}
