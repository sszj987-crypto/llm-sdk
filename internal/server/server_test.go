package server

import (
	"net/http"
	"strings"
	"testing"

	"llm-sdk/internal/config"
)

func TestServerServesIndexAndConfig(t *testing.T) {
	store := config.NewStore(t.TempDir() + "/config.json")
	srv, err := New(store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	mux := http.NewServeMux()
	srv.Register(mux)

	req, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	rec := &responseRecorder{header: http.Header{}}
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.body.String(), "LLM Agent") {
		t.Fatalf("index missing title: %s", rec.body.String())
	}

	payload := `{"provider":"openai","baseUrl":"https://api.openai.com/v1","apiKey":"test","model":"gpt-4o-mini","workerUrl":""}`
	req, err = http.NewRequest(http.MethodPost, "/api/config", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("new post request: %v", err)
	}
	rec = &responseRecorder{header: http.Header{}}
	mux.ServeHTTP(rec, req)
	if rec.code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.code)
	}

	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Model != "gpt-4o-mini" || cfg.Provider != "openai" {
		t.Fatalf("config not saved: %+v", cfg)
	}
}

type responseRecorder struct {
	header http.Header
	body   strings.Builder
	code   int
}

func (r *responseRecorder) Header() http.Header {
	return r.header
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(data)
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.code = statusCode
}
