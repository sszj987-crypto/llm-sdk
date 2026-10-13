package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"llm-sdk/internal/config"
	"llm-sdk/internal/provider"
)

func TestChatWorkerSNISetsTargetURL(t *testing.T) {
	profile := config.Profile{
		ID:        "main",
		Provider:  "openai",
		BaseURL:   "https://api.example/v1",
		APIKey:    "upstream-key",
		Model:     "gpt-test",
		RouteMode: string(config.RouteWorkerSNI),
		WorkerURL: "https://worker.example",
	}
	gw := New(nil)
	gw.ClientFactory = func(profile config.Profile) (*http.Client, func(), error) {
		client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://worker.example" {
				t.Fatalf("unexpected outbound url: %s", r.URL.String())
			}
			if got := r.Header.Get("X-Target-Url"); got != "https://api.example/v1/chat/completions" {
				t.Fatalf("unexpected target header: %s", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
				t.Fatalf("unexpected auth header: %s", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n" + "data: [DONE]\n\n")),
				Request:    r,
			}, nil
		})}
		return client, func() {}, nil
	}

	result, err := gw.Chat(context.Background(), profile, ChatRequest{
		Model:    "gpt-test",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Raw:      map[string]any{},
	}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if result.Text != "ok" {
		t.Fatalf("unexpected text: %s", result.Text)
	}
}

func TestDiagnoseReportsWorkerHealthFailure(t *testing.T) {
	profile := config.Profile{
		ID:        "main",
		Provider:  "openai",
		BaseURL:   "https://api.example/v1",
		APIKey:    "upstream-key",
		Model:     "gpt-test",
		RouteMode: string(config.RouteWorkerSNI),
		WorkerURL: "https://worker.example",
	}
	gw := New(nil)
	gw.ClientFactory = func(profile config.Profile) (*http.Client, func(), error) {
		client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Status:     "502 Bad Gateway",
				Body:       io.NopCloser(strings.NewReader("worker down")),
				Request:    r,
			}, nil
		})}
		return client, func() {}, nil
	}

	result := gw.Diagnose(context.Background(), profile)
	if result.ErrorClass != "upstream_5xx" {
		t.Fatalf("unexpected error class: %+v", result)
	}
	if result.WorkerHealth == nil || result.WorkerHealth.OK {
		t.Fatalf("expected failed worker health: %+v", result)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
