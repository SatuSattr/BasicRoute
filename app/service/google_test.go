package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basicroute/app/config"
)

func TestSetAuthGoogle(t *testing.T) {
	h := http.Header{}
	SetAuth(h, config.ProviderTypeGoogle, "g-key")
	if h.Get("x-goog-api-key") != "g-key" {
		t.Fatalf("x-goog-api-key = %q", h.Get("x-goog-api-key"))
	}
	if h.Get("Api-Revision") != GoogleAPIRevision {
		t.Fatalf("Api-Revision = %q", h.Get("Api-Revision"))
	}
	if h.Get("Authorization") != "" {
		t.Fatalf("Authorization must be empty, got %q", h.Get("Authorization"))
	}
}

func TestSetAuthBearer(t *testing.T) {
	h := http.Header{}
	SetAuth(h, "openai", "sk-1")
	if h.Get("Authorization") != "Bearer sk-1" {
		t.Fatalf("Authorization = %q", h.Get("Authorization"))
	}
	if h.Get("x-goog-api-key") != "" {
		t.Fatalf("x-goog-api-key must be empty, got %q", h.Get("x-goog-api-key"))
	}
}

func TestTargetURLInteractions(t *testing.T) {
	if got := TargetURL("https://g.example.com/v1beta", config.InterfaceInteractions); got != "https://g.example.com/v1beta/interactions" {
		t.Fatalf("TargetURL = %q", got)
	}
	if got := TargetURL("https://g.example.com/v1beta/interactions/", config.InterfaceInteractions); got != "https://g.example.com/v1beta/interactions" {
		t.Fatalf("already suffixed = %q", got)
	}
}

func TestIsAuthErrGoogleInvalidKey(t *testing.T) {
	err := fmt.Errorf(`model test: HTTP 400: {"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`)
	if !IsAuthErr(err) {
		t.Fatal("Google API_KEY_INVALID must be treated as auth error")
	}
	if IsAuthErr(fmt.Errorf("model test: HTTP 400: bad request")) {
		t.Fatal("generic 400 must not be treated as auth error")
	}
}

func TestFetchAutoModelsGoogle(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent","countTokens"]},
			{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]},
			{"name":"models/gemini-live","supportedGenerationMethods":["interactions"]}
		]}`))
	}))
	defer srv.Close()

	models, status, err := fetchAutoModelsViaTarget(srv.URL+"/v1beta", config.ProviderTypeGoogle, "", "", "g-key", 5*time.Second)
	if err != nil || status != http.StatusOK {
		t.Fatalf("fetch: status=%d err=%v", status, err)
	}
	if gotKey != "g-key" {
		t.Fatalf("key header = %q", gotKey)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != "gemini-3.8-flash" || ids[1] != "gemini-live" {
		t.Fatalf("models = %v", ids)
	}
}

func TestFetchAutoModelsGoogleAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	_, status, err := fetchAutoModelsViaTarget(srv.URL+"/v1beta", config.ProviderTypeGoogle, "", "", "bad", 5*time.Second)
	if err == nil || status != http.StatusBadRequest {
		t.Fatalf("expected 400 error, got status=%d err=%v", status, err)
	}
	if !IsAuthErr(err) {
		t.Fatalf("expected auth error, got %v", err)
	}
	if !strings.Contains(err.Error(), "API key not valid") {
		t.Fatalf("error must carry upstream message: %v", err)
	}
}
