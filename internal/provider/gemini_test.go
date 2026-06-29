package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGeminiChatUsesOpenAICompatiblePayload(t *testing.T) {
	var receivedBody map[string]any
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.URL.Path; got != "/v1beta/openai/chat/completions" {
				t.Fatalf("unexpected path: %s", got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("unexpected content type: %s", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("unexpected authorization header: %s", got)
			}

			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if err := json.Unmarshal(raw, &receivedBody); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}

			body := io.NopCloser(strings.NewReader(`data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\n" + "data: [DONE]\n\n"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       body,
				Request:    r,
			}, nil
		}),
	}

	adapter := NewGeminiAdapter(client)
	err := adapter.Chat(context.Background(), Config{
		BaseURL: "https://example.invalid/v1beta/openai",
		APIKey:  "test-key",
		Model:   "gemini-3.0-flash",
	}, []Message{{Role: "user", Content: "hi"}}, func(delta string) error {
		if delta != "hello" {
			t.Fatalf("unexpected delta: %s", delta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}

	if got, ok := receivedBody["model"].(string); !ok || got != "gemini-3.0-flash" {
		t.Fatalf("unexpected model field: %#v", receivedBody["model"])
	}
	if got, ok := receivedBody["stream"].(bool); !ok || !got {
		t.Fatalf("unexpected stream field: %#v", receivedBody["stream"])
	}
	if _, ok := receivedBody["contents"]; ok {
		t.Fatalf("unexpected contents field in payload: %#v", receivedBody)
	}

	messages, ok := receivedBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("unexpected messages field: %#v", receivedBody["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message entry: %#v", messages[0])
	}
	if got := message["role"]; got != "user" {
		t.Fatalf("unexpected role: %#v", got)
	}
	if got := message["content"]; got != "hi" {
		t.Fatalf("unexpected content: %#v", got)
	}
}

func TestNormalizeRoleUsesOpenAIRoles(t *testing.T) {
	cases := map[string]string{
		"user":      "user",
		"assistant": "assistant",
		"system":    "system",
		"MODEL":     "user",
	}
	for input, expected := range cases {
		if got := normalizeRole(input); got != expected {
			t.Fatalf("normalizeRole(%q) = %q, want %q", input, got, expected)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
