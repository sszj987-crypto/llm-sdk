package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"
)

func joinURL(baseURL, suffix string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	return parsed.String(), nil
}

func maybeWrapWithWorker(workerURL, targetURL string, req *http.Request) (*http.Request, error) {
	if workerURL == "" {
		return req, nil
	}

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()

	wrapped, err := http.NewRequestWithContext(req.Context(), req.Method, workerURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	wrapped.Header = req.Header.Clone()
	wrapped.Header.Set("X-Target-Url", targetURL)
	return wrapped, nil
}

func decodeJSONLine(line []byte, target any) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	if bytes.HasPrefix(line, []byte("data:")) {
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	}
	if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
		return nil
	}
	return json.Unmarshal(line, target)
}

func extractTextFromAny(value map[string]any) string {
	if text := textFromGemini(value); text != "" {
		return text
	}
	if text := textFromOpenAI(value); text != "" {
		return text
	}
	return ""
}

func textFromOpenAI(value map[string]any) string {
	choices, ok := value["choices"].([]any)
	if !ok || len(choices) == 0 {
		return ""
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return ""
	}
	if delta, ok := choice["delta"].(map[string]any); ok {
		if content, ok := delta["content"].(string); ok {
			return content
		}
	}
	if message, ok := choice["message"].(map[string]any); ok {
		if content, ok := message["content"].(string); ok {
			return content
		}
	}
	return ""
}

func textFromGemini(value map[string]any) string {
	candidates, ok := value["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return ""
	}
	candidate, ok := candidates[0].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := candidate["content"].(map[string]any)
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]any)
	if !ok {
		return ""
	}
	var out strings.Builder
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := partMap["text"].(string); ok {
			out.WriteString(text)
		}
	}
	return out.String()
}

func makeRequest(ctx context.Context, method, targetURL string, body io.Reader, headers http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	return req, nil
}

type requestTrace struct {
	mu          sync.Mutex
	provider    string
	targetURL   string
	start       time.Time
	getConnAt   time.Time
	gotConnAt   time.Time
	connectAt   time.Time
	connectDone time.Time
	tlsAt       time.Time
	tlsDone     time.Time
	wroteAt     time.Time
	firstByteAt time.Time
}

// Summary returns key timing milestones relative to request start.
// Fields that were not recorded (e.g. TLS for QUIC) are omitted.
func (t *requestTrace) Summary() map[string]time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	m := make(map[string]time.Duration)
	if !t.getConnAt.IsZero() {
		m["get_conn"] = t.getConnAt.Sub(t.start)
	}
	if !t.gotConnAt.IsZero() {
		m["got_conn"] = t.gotConnAt.Sub(t.start)
		if !t.getConnAt.IsZero() {
			m["conn_wait"] = t.gotConnAt.Sub(t.getConnAt)
		}
	}
	if !t.connectAt.IsZero() && !t.connectDone.IsZero() {
		m["connect"] = t.connectDone.Sub(t.connectAt)
	}
	if !t.tlsAt.IsZero() && !t.tlsDone.IsZero() {
		m["tls"] = t.tlsDone.Sub(t.tlsAt)
	}
	if !t.wroteAt.IsZero() {
		m["wrote_request"] = t.wroteAt.Sub(t.start)
	}
	if !t.firstByteAt.IsZero() {
		m["first_byte"] = t.firstByteAt.Sub(t.start)
	}
	return m
}

func newRequestTrace(providerName, targetURL string, start time.Time) *requestTrace {
	return &requestTrace{
		provider:  providerName,
		targetURL: targetURL,
		start:     start,
	}
}

func (t *requestTrace) withContext(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(hostPort string) {
			now := time.Now()
			t.mu.Lock()
			t.getConnAt = now
			t.mu.Unlock()
			log.Printf("[provider=%s] trace_get_conn ts=%s elapsed=%s host=%s", t.provider, now.Format(time.RFC3339Nano), now.Sub(t.start).Round(time.Millisecond), hostPort)
		},
		GotConn: func(info httptrace.GotConnInfo) {
			now := time.Now()
			t.mu.Lock()
			t.gotConnAt = now
			t.mu.Unlock()
			log.Printf("[provider=%s] trace_got_conn ts=%s elapsed=%s reused=%t idle_time=%s was_idle=%t", t.provider, now.Format(time.RFC3339Nano), now.Sub(t.start).Round(time.Millisecond), info.Reused, info.IdleTime.Round(time.Millisecond), info.WasIdle)
		},
		ConnectStart: func(network, addr string) {
			now := time.Now()
			t.mu.Lock()
			t.connectAt = now
			t.mu.Unlock()
			log.Printf("[provider=%s] trace_connect_start ts=%s elapsed=%s network=%s addr=%s", t.provider, now.Format(time.RFC3339Nano), now.Sub(t.start).Round(time.Millisecond), network, addr)
		},
		ConnectDone: func(network, addr string, err error) {
			now := time.Now()
			t.mu.Lock()
			start := t.connectAt
			t.connectDone = now
			t.mu.Unlock()
			fields := []any{"[provider=" + t.provider + "]", "trace_connect_done", "ts=", now.Format(time.RFC3339Nano), "elapsed=", now.Sub(t.start).Round(time.Millisecond)}
			if !start.IsZero() {
				fields = append(fields, "connect_elapsed=", now.Sub(start).Round(time.Millisecond))
			}
			fields = append(fields, "network=", network, "addr=", addr)
			if err != nil {
				fields = append(fields, "err=", err)
			}
			log.Println(fields...)
		},
		TLSHandshakeStart: func() {
			now := time.Now()
			t.mu.Lock()
			t.tlsAt = now
			t.mu.Unlock()
			log.Printf("[provider=%s] trace_tls_start ts=%s elapsed=%s", t.provider, now.Format(time.RFC3339Nano), now.Sub(t.start).Round(time.Millisecond))
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			now := time.Now()
			t.mu.Lock()
			start := t.tlsAt
			t.tlsDone = now
			t.mu.Unlock()
			fields := []any{"[provider=" + t.provider + "]", "trace_tls_done", "ts=", now.Format(time.RFC3339Nano), "elapsed=", now.Sub(t.start).Round(time.Millisecond)}
			if !start.IsZero() {
				fields = append(fields, "tls_elapsed=", now.Sub(start).Round(time.Millisecond))
			}
			if err != nil {
				fields = append(fields, "err=", err)
			}
			log.Println(fields...)
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			now := time.Now()
			t.mu.Lock()
			t.wroteAt = now
			t.mu.Unlock()
			fields := []any{"[provider=" + t.provider + "]", "trace_wrote_request", "ts=", now.Format(time.RFC3339Nano), "elapsed=", now.Sub(t.start).Round(time.Millisecond)}
			if info.Err != nil {
				fields = append(fields, "err=", info.Err)
			}
			log.Println(fields...)
		},
		GotFirstResponseByte: func() {
			now := time.Now()
			t.mu.Lock()
			t.firstByteAt = now
			t.mu.Unlock()
			log.Printf("[provider=%s] trace_first_response_byte ts=%s elapsed=%s", t.provider, now.Format(time.RFC3339Nano), now.Sub(t.start).Round(time.Millisecond))
		},
	})
}

func buildOpenAIHeaders(apiKey string) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Set("Authorization", "Bearer "+apiKey)
	return headers
}

func buildGeminiHeaders(apiKey string) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Set("Authorization", "Bearer "+apiKey)
	return headers
}

func readBodyWithLimit(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

func truncateForLog(data []byte, max int) string {
	if len(data) <= max {
		return string(data)
	}
	return string(data[:max]) + "...(truncated)"
}

func normalizeRole(role string) string {
	switch strings.ToLower(role) {
	case "system", "user", "assistant":
		return strings.ToLower(role)
	default:
		return "user"
	}
}

func trimConfigValue(value string) string {
	return strings.TrimSpace(value)
}

func requireConfig(cfg Config) error {
	if strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("api key is required")
	}
	return nil
}

func logRequestStart(providerName, targetURL string) time.Time {
	start := time.Now()
	log.Printf("[provider=%s] request_start ts=%s target=%s", providerName, start.Format(time.RFC3339Nano), targetURL)
	return start
}

func logResponseHeaders(providerName string, start time.Time, status string) time.Time {
	now := time.Now()
	log.Printf("[provider=%s] response_headers ts=%s elapsed=%s status=%s", providerName, now.Format(time.RFC3339Nano), now.Sub(start).Round(time.Millisecond), status)
	return now
}

func logFirstDelta(providerName string, responseAt time.Time, delta string) time.Time {
	now := time.Now()
	log.Printf("[provider=%s] first_delta ts=%s elapsed_since_headers=%s delta_bytes=%d", providerName, now.Format(time.RFC3339Nano), now.Sub(responseAt).Round(time.Millisecond), len(delta))
	return now
}

func logStreamDone(providerName string, start time.Time, responseAt time.Time, firstDeltaAt time.Time, err error) {
	now := time.Now()
	fields := []any{
		"[provider=" + providerName + "]",
		"stream_done",
		"ts=", now.Format(time.RFC3339Nano),
		"total_elapsed=", now.Sub(start).Round(time.Millisecond),
	}
	if !responseAt.IsZero() {
		fields = append(fields, "headers_elapsed=", responseAt.Sub(start).Round(time.Millisecond))
	}
	if !firstDeltaAt.IsZero() {
		fields = append(fields, "first_delta_elapsed=", firstDeltaAt.Sub(responseAt).Round(time.Millisecond))
	}
	if err != nil {
		fields = append(fields, "err=", err)
	}
	log.Println(fields...)
}
