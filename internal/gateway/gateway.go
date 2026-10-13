package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"llm-sdk/internal/config"
	"llm-sdk/internal/dialer"
	"llm-sdk/internal/ipdb"
	"llm-sdk/internal/provider"
)

type Gateway struct {
	ipdbCli       *ipdb.Client
	ClientFactory func(profile config.Profile) (*http.Client, func(), error)
}

type ChatRequest struct {
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
	Raw      map[string]any     `json:"-"`
}

type ChatResult struct {
	Model    string           `json:"model"`
	Text     string           `json:"text"`
	Trace    map[string]int64 `json:"traceMs"`
	Duration int64            `json:"durationMs"`
}

type DiagnoseResult struct {
	ProfileID    string           `json:"profileId"`
	Provider     string           `json:"provider"`
	RouteMode    string           `json:"routeMode"`
	HTTP3Enabled bool             `json:"http3Enabled"`
	WorkerHealth *HealthResult    `json:"workerHealth,omitempty"`
	StatusCode   int              `json:"statusCode"`
	ErrorClass   string           `json:"errorClass,omitempty"`
	Error        string           `json:"error,omitempty"`
	Trace        map[string]int64 `json:"traceMs"`
	Duration     int64            `json:"durationMs"`
}

type HealthResult struct {
	OK         bool   `json:"ok"`
	StatusCode int    `json:"statusCode"`
	ErrorClass string `json:"errorClass,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"durationMs"`
}

type UpstreamError struct {
	StatusCode int
	Status     string
	TargetURL  string
	Body       string
	Class      string
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s upstream error: %s target=%s: %s", e.Class, e.Status, e.TargetURL, e.Body)
}

func New(ipdbCli *ipdb.Client) *Gateway {
	return &Gateway{ipdbCli: ipdbCli}
}

func ParseChatRequest(body io.Reader) (ChatRequest, error) {
	raw := make(map[string]any)
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return ChatRequest{}, err
	}

	var req ChatRequest
	data, err := json.Marshal(raw)
	if err != nil {
		return ChatRequest{}, err
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return ChatRequest{}, err
	}
	req.Raw = raw
	return req, nil
}

func (g *Gateway) Chat(ctx context.Context, profile config.Profile, req ChatRequest, onDelta func(string) error) (ChatResult, error) {
	start := time.Now()
	trace := newTrace(profile.Provider, start)
	client, cleanup, err := g.clientForProfile(profile)
	if err != nil {
		return ChatResult{}, err
	}
	defer cleanup()

	targetURL, err := targetURLForProfile(profile)
	if err != nil {
		return ChatResult{}, err
	}

	payload := clonePayload(req.Raw)
	payload["model"] = profile.UpstreamModel()
	payload["messages"] = req.Messages
	payload["stream"] = true

	body, err := json.Marshal(payload)
	if err != nil {
		return ChatResult{}, err
	}

	requestURL := targetURL
	headers := buildHeaders(profile)
	if profile.RouteMode == string(config.RouteWorkerSNI) {
		if strings.TrimSpace(profile.WorkerURL) == "" {
			return ChatResult{}, fmt.Errorf("workerUrl is required for worker_sni route")
		}
		requestURL = profile.WorkerURL
		headers.Set("X-Target-Url", targetURL)
	}

	httpReq, err := http.NewRequestWithContext(trace.withContext(ctx), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return ChatResult{}, err
	}
	httpReq.Header = headers

	log.Printf("[gateway] request_start profile=%s provider=%s route=%s http3=%t outbound_url=%s target_url=%s", profile.ID, profile.Provider, profile.RouteMode, profile.HTTP3Enabled, requestURL, targetURL)

	resp, err := client.Do(httpReq)
	if err != nil {
		trace.finish()
		return ChatResult{Model: profile.UpstreamModel(), Trace: trace.Summary(), Duration: msSince(start)}, classifyNetworkError(err)
	}
	defer resp.Body.Close()
	trace.markHeaders(resp.StatusCode)

	if resp.StatusCode >= 300 {
		rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		upstreamErr := &UpstreamError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			TargetURL:  targetURL,
			Body:       string(rawBody),
			Class:      classifyHTTPError(resp.StatusCode, rawBody),
		}
		log.Printf("[gateway] upstream_error profile=%s status=%s class=%s body=%q", profile.ID, resp.Status, upstreamErr.Class, truncate(rawBody, 1024))
		trace.finish()
		return ChatResult{Model: profile.UpstreamModel(), Trace: trace.Summary(), Duration: msSince(start)}, upstreamErr
	}

	var out strings.Builder
	err = readSSE(resp.Body, func(delta string) error {
		if delta == "" {
			return nil
		}
		trace.markFirstToken()
		out.WriteString(delta)
		if onDelta != nil {
			return onDelta(delta)
		}
		return nil
	})
	trace.finish()
	if err != nil {
		return ChatResult{Model: profile.UpstreamModel(), Text: out.String(), Trace: trace.Summary(), Duration: msSince(start)}, err
	}
	log.Printf("[gateway] request_done profile=%s status=%d duration=%dms trace=%v", profile.ID, resp.StatusCode, msSince(start), trace.Summary())
	return ChatResult{Model: profile.UpstreamModel(), Text: out.String(), Trace: trace.Summary(), Duration: msSince(start)}, nil
}

func (g *Gateway) Diagnose(ctx context.Context, profile config.Profile) DiagnoseResult {
	start := time.Now()
	result := DiagnoseResult{
		ProfileID:    profile.ID,
		Provider:     profile.Provider,
		RouteMode:    profile.RouteMode,
		HTTP3Enabled: profile.HTTP3Enabled,
	}

	if profile.RouteMode == string(config.RouteWorkerSNI) {
		health := g.CheckWorkerHealth(ctx, profile)
		result.WorkerHealth = &health
		if !health.OK {
			result.ErrorClass = health.ErrorClass
			result.Error = health.Error
			result.Duration = msSince(start)
			return result
		}
	}

	req := ChatRequest{
		Model:    profile.Model,
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Stream:   false,
		Raw: map[string]any{
			"model": profile.Model,
			"messages": []provider.Message{
				{Role: "user", Content: "hi"},
			},
		},
	}
	chatResult, err := g.Chat(ctx, profile, req, nil)
	result.Trace = chatResult.Trace
	result.Duration = msSince(start)
	if err != nil {
		result.Error = err.Error()
		result.ErrorClass = errorClass(err)
		if upstreamErr, ok := err.(*UpstreamError); ok {
			result.StatusCode = upstreamErr.StatusCode
		}
		return result
	}
	result.StatusCode = http.StatusOK
	return result
}

func (g *Gateway) CheckWorkerHealth(ctx context.Context, profile config.Profile) HealthResult {
	start := time.Now()
	if strings.TrimSpace(profile.WorkerURL) == "" {
		return HealthResult{OK: false, ErrorClass: "worker_missing_url", Error: "workerUrl is empty"}
	}

	client, cleanup, err := g.clientForProfile(profile)
	if err != nil {
		return HealthResult{OK: false, ErrorClass: "transport_error", Error: err.Error(), DurationMS: msSince(start)}
	}
	defer cleanup()

	healthURL, err := joinURL(profile.WorkerURL, "health")
	if err != nil {
		return HealthResult{OK: false, ErrorClass: "worker_bad_url", Error: err.Error(), DurationMS: msSince(start)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return HealthResult{OK: false, ErrorClass: "worker_bad_request", Error: err.Error(), DurationMS: msSince(start)}
	}
	resp, err := client.Do(req)
	if err != nil {
		return HealthResult{OK: false, ErrorClass: "worker_network_error", Error: err.Error(), DurationMS: msSince(start)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return HealthResult{
			OK:         false,
			StatusCode: resp.StatusCode,
			ErrorClass: classifyHTTPError(resp.StatusCode, rawBody),
			Error:      strings.TrimSpace(string(rawBody)),
			DurationMS: msSince(start),
		}
	}
	return HealthResult{OK: true, StatusCode: resp.StatusCode, DurationMS: msSince(start)}
}

func (g *Gateway) clientForProfile(profile config.Profile) (*http.Client, func(), error) {
	if g.ClientFactory != nil {
		return g.ClientFactory(profile)
	}
	if profile.HTTP3Enabled {
		quicDialer := dialer.NewQuicDialer()
		if profile.RouteMode == string(config.RouteWorkerSNI) && g.ipdbCli != nil && g.ipdbCli.Count() > 0 {
			quicDialer.SetIPProvider(g.ipdbCli)
		}
		transport := &http3.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13},
			QUICConfig: &quic.Config{
				HandshakeIdleTimeout:       15 * time.Second,
				Allow0RTT:                  true,
				InitialStreamReceiveWindow: 5 * 1024 * 1024,
				DisablePathMTUDiscovery:    true,
				InitialPacketSize:          1450,
			},
			Dial: quicDialer.Dial,
		}
		return &http.Client{Transport: transport}, func() { transport.Close() }, nil
	}

	if profile.RouteMode == string(config.RouteWorkerSNI) {
		sniDialer := dialer.New()
		if g.ipdbCli != nil && g.ipdbCli.Count() > 0 {
			sniDialer.SetIPProvider(g.ipdbCli)
		}
		transport := &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				if port != "" {
					sniDialer.Port = port
				}
				return sniDialer.DialTLSContext(ctx, network, host)
			},
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13},
			ForceAttemptHTTP2: true,
		}
		return &http.Client{Transport: transport}, transport.CloseIdleConnections, nil
	}

	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: transport}, transport.CloseIdleConnections, nil
}

func targetURLForProfile(profile config.Profile) (string, error) {
	baseURL := strings.TrimRight(profile.BaseURL, "/")
	if profile.Provider == string(config.ProviderGemini) && !strings.HasSuffix(baseURL, "/openai") {
		baseURL += "/openai"
	}
	return joinURL(baseURL, "chat/completions")
}

func buildHeaders(profile config.Profile) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	if profile.APIKey != "" {
		headers.Set("Authorization", "Bearer "+profile.APIKey)
	}
	return headers
}

func clonePayload(raw map[string]any) map[string]any {
	out := make(map[string]any)
	for key, value := range raw {
		out[key] = value
	}
	return out
}

func readSSE(body io.Reader, onDelta func(string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		}
		if bytes.Equal(line, []byte("[DONE]")) {
			break
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if delta := extractDelta(event); delta != "" {
			if err := onDelta(delta); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func extractDelta(event map[string]any) string {
	choices, ok := event["choices"].([]any)
	if !ok || len(choices) == 0 {
		return extractGeminiDelta(event)
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

func extractGeminiDelta(event map[string]any) string {
	candidates, ok := event["candidates"].([]any)
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

type traceRecorder struct {
	mu           sync.Mutex
	provider     string
	start        time.Time
	getConnAt    time.Time
	gotConnAt    time.Time
	connectAt    time.Time
	connectDone  time.Time
	tlsAt        time.Time
	tlsDone      time.Time
	wroteAt      time.Time
	firstByteAt  time.Time
	headersAt    time.Time
	firstTokenAt time.Time
	doneAt       time.Time
	statusCode   int
}

func newTrace(providerName string, start time.Time) *traceRecorder {
	return &traceRecorder{provider: providerName, start: start}
}

func (t *traceRecorder) withContext(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(hostPort string) {
			t.mark("get_conn", func(now time.Time) { t.getConnAt = now })
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mark("got_conn", func(now time.Time) { t.gotConnAt = now })
		},
		ConnectStart: func(network, addr string) {
			t.mark("connect_start", func(now time.Time) { t.connectAt = now })
		},
		ConnectDone: func(network, addr string, err error) {
			t.mark("connect_done", func(now time.Time) { t.connectDone = now })
		},
		TLSHandshakeStart: func() {
			t.mark("tls_start", func(now time.Time) { t.tlsAt = now })
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			t.mark("tls_done", func(now time.Time) { t.tlsDone = now })
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			t.mark("wrote_request", func(now time.Time) { t.wroteAt = now })
		},
		GotFirstResponseByte: func() {
			t.mark("first_byte", func(now time.Time) { t.firstByteAt = now })
		},
	})
}

func (t *traceRecorder) mark(name string, assign func(time.Time)) {
	now := time.Now()
	t.mu.Lock()
	assign(now)
	t.mu.Unlock()
	log.Printf("[gateway] trace_%s provider=%s elapsed=%dms", name, t.provider, now.Sub(t.start).Milliseconds())
}

func (t *traceRecorder) markHeaders(statusCode int) {
	now := time.Now()
	t.mu.Lock()
	t.headersAt = now
	t.statusCode = statusCode
	t.mu.Unlock()
}

func (t *traceRecorder) markFirstToken() {
	now := time.Now()
	t.mu.Lock()
	if t.firstTokenAt.IsZero() {
		t.firstTokenAt = now
	}
	t.mu.Unlock()
}

func (t *traceRecorder) finish() {
	now := time.Now()
	t.mu.Lock()
	t.doneAt = now
	t.mu.Unlock()
}

func (t *traceRecorder) Summary() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make(map[string]int64)
	put := func(name string, at time.Time) {
		if !at.IsZero() {
			out[name] = at.Sub(t.start).Milliseconds()
		}
	}
	put("getConn", t.getConnAt)
	put("gotConn", t.gotConnAt)
	if !t.connectAt.IsZero() && !t.connectDone.IsZero() {
		out["tcpConnect"] = t.connectDone.Sub(t.connectAt).Milliseconds()
	}
	if !t.tlsAt.IsZero() && !t.tlsDone.IsZero() {
		out["tlsHandshake"] = t.tlsDone.Sub(t.tlsAt).Milliseconds()
	}
	put("wroteRequest", t.wroteAt)
	put("firstByte", t.firstByteAt)
	put("headers", t.headersAt)
	put("firstToken", t.firstTokenAt)
	put("done", t.doneAt)
	if !t.wroteAt.IsZero() && !t.firstByteAt.IsZero() {
		out["serverWait"] = t.firstByteAt.Sub(t.wroteAt).Milliseconds()
	}
	return out
}

func classifyNetworkError(err error) error {
	return &UpstreamError{Status: "network error", Body: err.Error(), Class: "network_error"}
}

func classifyHTTPError(statusCode int, body []byte) string {
	text := strings.ToLower(string(body))
	switch {
	case statusCode == 522 || strings.Contains(text, "error code 522"):
		return "cloudflare_522"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || strings.Contains(text, "authorization"):
		return "auth_error"
	case statusCode >= 400 && statusCode < 500:
		return "upstream_4xx"
	case statusCode >= 500:
		return "upstream_5xx"
	default:
		return "upstream_error"
	}
}

func errorClass(err error) string {
	if upstreamErr, ok := err.(*UpstreamError); ok {
		return upstreamErr.Class
	}
	return "runtime_error"
}

func joinURL(baseURL, suffix string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	return parsed.String(), nil
}

func msSince(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

func truncate(data []byte, max int) string {
	if len(data) <= max {
		return string(data)
	}
	return string(data[:max]) + "...(truncated)"
}
