package server

import (
	"encoding/json"
	"io"
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

	payload := `{"activeProfileId":"openai-main","profiles":[{"id":"openai-main","name":"OpenAI Main","provider":"openai","baseUrl":"https://api.openai.com/v1","apiKey":"test","model":"gpt-4o-mini","routeMode":"direct","workerUrl":"","http3Enabled":false}]}`
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
	profile, ok := cfg.ActiveProfile()
	if !ok || profile.Model != "gpt-4o-mini" || profile.Provider != "openai" {
		t.Fatalf("config not saved: %+v", cfg)
	}

	req, err = http.NewRequest(http.MethodGet, "/v1/models", nil)
	if err != nil {
		t.Fatalf("new models request: %v", err)
	}
	rec = &responseRecorder{header: http.Header{}}
	mux.ServeHTTP(rec, req)
	if rec.code != http.StatusOK || !strings.Contains(rec.body.String(), "gpt-4o-mini") {
		t.Fatalf("models response invalid status=%d body=%s", rec.code, rec.body.String())
	}
}

func TestV1ChatCompletionsNonStream(t *testing.T) {
	mux := newTestMux(t, `{"activeProfileId":"main","profiles":[{"id":"main","name":"Main","provider":"openai","baseUrl":"https://api.example/v1","apiKey":"upstream-key","model":"gpt-test","routeMode":"direct"}]}`, `data: {"choices":[{"delta":{"content":"hello"}}]}`+"\n\n"+"data: [DONE]\n\n")

	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}
	rec := &responseRecorder{header: http.Header{}}
	mux.ServeHTTP(rec, req)
	if rec.code != http.StatusOK {
		t.Fatalf("unexpected status=%d body=%s", rec.code, rec.body.String())
	}
	if !strings.Contains(rec.body.String(), `"chat.completion"`) || !strings.Contains(rec.body.String(), "hello") {
		t.Fatalf("unexpected response body: %s", rec.body.String())
	}
}

func TestV1ChatCompletionsStream(t *testing.T) {
	mux := newTestMux(t, `{"activeProfileId":"main","profiles":[{"id":"main","name":"Main","provider":"openai","baseUrl":"https://api.example/v1","apiKey":"upstream-key","model":"gpt-test","routeMode":"direct"}]}`, `data: {"choices":[{"delta":{"content":"he"}}]}`+"\n\n"+`data: {"choices":[{"delta":{"content":"llo"}}]}`+"\n\n"+"data: [DONE]\n\n")

	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}
	rec := &responseRecorder{header: http.Header{}}
	mux.ServeHTTP(rec, req)
	if rec.code != http.StatusOK {
		t.Fatalf("unexpected status=%d body=%s", rec.code, rec.body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("unexpected content type: %s", got)
	}
	if !strings.Contains(rec.body.String(), "data: [DONE]") || !strings.Contains(rec.body.String(), `"content":"he"`) || !strings.Contains(rec.body.String(), `"content":"llo"`) {
		t.Fatalf("unexpected stream body: %s", rec.body.String())
	}
}

func newTestMux(t *testing.T, cfgJSON string, upstreamBody string) *http.ServeMux {
	t.Helper()
	store := config.NewStore(t.TempDir() + "/config.json")
	if err := store.Save(mustLoadConfig(t, cfgJSON)); err != nil {
		t.Fatalf("save test config: %v", err)
	}
	srv, err := New(store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	srv.gateway.ClientFactory = func(profile config.Profile) (*http.Client, func(), error) {
		client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
				t.Fatalf("unexpected upstream authorization: %s", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(upstreamBody)),
				Request:    r,
			}, nil
		})}
		return client, func() {}, nil
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	return mux
}

func mustLoadConfig(t *testing.T, payload string) config.Config {
	t.Helper()
	var cfg config.Config
	if err := json.NewDecoder(strings.NewReader(payload)).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
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

func (r *responseRecorder) Flush() {}
