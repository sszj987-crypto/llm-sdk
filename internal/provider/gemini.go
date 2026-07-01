package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type GeminiAdapter struct {
	Client    *http.Client
	LastTrace *requestTrace // ponytail: test access to timing data
}

func NewGeminiAdapter(client *http.Client) *GeminiAdapter {
	return &GeminiAdapter{Client: client}
}

func (a *GeminiAdapter) Chat(ctx context.Context, cfg Config, messages []Message, onDelta StreamHandler) error {
	if err := requireConfig(cfg); err != nil {
		return err
	}

	baseURL := normalizeGeminiBaseURL(cfg.BaseURL)
	targetURL, err := joinURL(baseURL, "chat/completions")
	if err != nil {
		return err
	}
	start := logRequestStart("gemini", targetURL)
	log.Printf("[provider=gemini] model=%s base_url=%s resolved_base_url=%s worker_url_set=%t", cfg.Model, cfg.BaseURL, baseURL, cfg.WorkerURL != "")

	payload := map[string]any{
		"model":    cfg.Model,
		"messages": convertOpenAIMessages(messages),
		"stream":   true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := makeRequest(ctx, http.MethodPost, targetURL, bytes.NewReader(body), buildGeminiHeaders(cfg.APIKey))
	if err != nil {
		return err
	}
	trace := newRequestTrace("gemini", targetURL, start)
	a.LastTrace = trace
	req = req.WithContext(trace.withContext(req.Context()))
	req, err = maybeWrapWithWorker(cfg.WorkerURL, targetURL, req)
	if err != nil {
		return err
	}
	log.Printf("[provider=gemini] outbound_url=%s target_url=%s via_worker=%t", req.URL.String(), targetURL, cfg.WorkerURL != "")

	resp, err := a.Client.Do(req)
	if err != nil {
		logStreamDone("gemini", start, time.Time{}, time.Time{}, err)
		return err
	}
	defer resp.Body.Close()
	responseAt := logResponseHeaders("gemini", start, resp.Status)

	if resp.StatusCode >= 300 {
		raw, _ := readBodyWithLimit(resp.Body, 1<<20)
		err = fmt.Errorf("gemini upstream error: %s target=%s: %s", resp.Status, targetURL, string(raw))
		log.Printf("[provider=gemini] upstream_error target=%s status=%s body=%q", targetURL, resp.Status, truncateForLog(raw, 1024))
		logStreamDone("gemini", start, responseAt, time.Time{}, err)
		return err
	}

	firstDeltaAt := time.Time{}
	err = streamGeminiSSE(resp.Body, func(delta string) error {
		if firstDeltaAt.IsZero() {
			firstDeltaAt = logFirstDelta("gemini", responseAt, delta)
		}
		return onDelta(delta)
	})
	logStreamDone("gemini", start, responseAt, firstDeltaAt, err)
	return err
}

func normalizeGeminiBaseURL(baseURL string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	if strings.HasSuffix(baseURL, "/openai") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/interactions") {
		return strings.TrimSuffix(baseURL, "/interactions") + "/openai"
	}
	return baseURL + "/openai"
}

func streamGeminiSSE(body io.Reader, onDelta StreamHandler) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event map[string]any
		if err := decodeJSONLine(line, &event); err != nil {
			continue
		}
		if len(event) == 0 {
			continue
		}
		if delta := extractTextFromAny(event); delta != "" {
			if err := onDelta(delta); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
