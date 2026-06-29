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
	"time"
)

type OpenAIAdapter struct {
	Client *http.Client
}

func NewOpenAIAdapter(client *http.Client) *OpenAIAdapter {
	return &OpenAIAdapter{Client: client}
}

func (a *OpenAIAdapter) Chat(ctx context.Context, cfg Config, messages []Message, onDelta StreamHandler) error {
	if err := requireConfig(cfg); err != nil {
		return err
	}

	targetURL, err := joinURL(cfg.BaseURL, "chat/completions")
	if err != nil {
		return err
	}
	start := logRequestStart("openai", targetURL)
	log.Printf("[provider=openai] model=%s base_url=%s worker_url_set=%t", cfg.Model, cfg.BaseURL, cfg.WorkerURL != "")

	payload := map[string]any{
		"model":    cfg.Model,
		"messages": convertOpenAIMessages(messages),
		"stream":   true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := makeRequest(ctx, http.MethodPost, targetURL, bytes.NewReader(body), buildOpenAIHeaders(cfg.APIKey))
	if err != nil {
		return err
	}
	trace := newRequestTrace("openai", targetURL, start)
	req = req.WithContext(trace.withContext(req.Context()))
	req, err = maybeWrapWithWorker(cfg.WorkerURL, targetURL, req)
	if err != nil {
		return err
	}
	log.Printf("[provider=openai] outbound_url=%s target_url=%s via_worker=%t", req.URL.String(), targetURL, cfg.WorkerURL != "")

	resp, err := a.Client.Do(req)
	if err != nil {
		logStreamDone("openai", start, time.Time{}, time.Time{}, err)
		return err
	}
	defer resp.Body.Close()
	responseAt := logResponseHeaders("openai", start, resp.Status)

	if resp.StatusCode >= 300 {
		raw, _ := readBodyWithLimit(resp.Body, 1<<20)
		err = fmt.Errorf("openai-compatible upstream error: %s: %s", resp.Status, string(raw))
		log.Printf("[provider=openai] upstream_error target=%s status=%s body=%q", targetURL, resp.Status, truncateForLog(raw, 1024))
		logStreamDone("openai", start, responseAt, time.Time{}, err)
		return err
	}

	firstDeltaAt := time.Time{}
	err = streamOpenAISSE(resp.Body, func(delta string) error {
		if firstDeltaAt.IsZero() {
			firstDeltaAt = logFirstDelta("openai", responseAt, delta)
		}
		return onDelta(delta)
	})
	logStreamDone("openai", start, responseAt, firstDeltaAt, err)
	return err
}

func convertOpenAIMessages(messages []Message) []map[string]string {
	converted := make([]map[string]string, 0, len(messages))
	for _, message := range messages {
		converted = append(converted, map[string]string{
			"role":    normalizeRole(message.Role),
			"content": message.Content,
		})
	}
	return converted
}

func streamOpenAISSE(body io.Reader, onDelta StreamHandler) error {
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
