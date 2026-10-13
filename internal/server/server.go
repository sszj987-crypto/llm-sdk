package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	assets "llm-sdk"
	"llm-sdk/internal/config"
	"llm-sdk/internal/gateway"
	"llm-sdk/internal/ipdb"
	"llm-sdk/internal/provider"
)

type Server struct {
	store   *config.Store
	ipdbCli *ipdb.Client
	gateway *gateway.Gateway
}

func New(store *config.Store) (*Server, error) {
	ipdbCli := ipdb.NewClient()
	if ipdbCli.Count() == 0 {
		log.Printf("[server] ipdb_no_ips falling_back_to_dns=true")
	}

	return &Server{
		store:   store,
		ipdbCli: ipdbCli,
		gateway: gateway.New(ipdbCli),
	}, nil
}

// Shutdown gracefully stops background loops (e.g. IPDB refresh).
func (s *Server) Shutdown() {
	if s.ipdbCli != nil {
		s.ipdbCli.Stop()
	}
}

func (s *Server) Register(mux *http.ServeMux) {
	webSubFS, err := fs.Sub(assets.WebFS, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(webSubFS)))
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/diagnose/profile/", s.handleDiagnoseProfile)
	mux.HandleFunc("/api/ipdb/status", s.handleIpdbStatus)
	mux.HandleFunc("/v1/models", s.handleV1Models)
	mux.HandleFunc("/v1/chat/completions", s.handleV1ChatCompletions)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.store.Load()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeJSON(w, cfg)
	case http.MethodPost:
		var cfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.store.Save(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeJSON(w, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type chatRequest struct {
	Provider  string             `json:"provider"`
	ProfileID string             `json:"profileId"`
	Model     string             `json:"model"`
	Messages  []provider.Message `json:"messages"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	requestID := fmt.Sprintf("chat-%d", start.UnixNano())

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[request_id=%s] chat_received ts=%s provider=%s messages=%d", requestID, start.Format(time.RFC3339Nano), req.Provider, len(req.Messages))

	cfg, err := s.store.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	profile, ok := selectProfile(cfg, req.ProfileID, req.Model, req.Provider)
	if !ok {
		http.Error(w, "no matching profile", http.StatusBadRequest)
		return
	}
	log.Printf("[request_id=%s] profile_config profile=%s provider=%s base_url=%s model=%s route=%s http3=%t worker_url_set=%t", requestID, profile.ID, profile.Provider, profile.BaseURL, profile.Model, profile.RouteMode, profile.HTTP3Enabled, profile.WorkerURL != "")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	writeEvent := func(eventType, content string) error {
		payload := map[string]string{"type": eventType}
		if content != "" {
			payload["content"] = content
		}
		return s.writeLine(w, payload, flusher)
	}

	chatReq := gateway.ChatRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   true,
		Raw: map[string]any{
			"model":    profile.UpstreamModel(),
			"messages": req.Messages,
			"stream":   true,
		},
	}
	_, err = s.gateway.Chat(r.Context(), profile, chatReq, func(delta string) error {
		return writeEvent("delta", delta)
	})
	if err != nil {
		log.Printf("[request_id=%s] chat_error ts=%s elapsed=%s err=%v", requestID, time.Now().Format(time.RFC3339Nano), time.Since(start).Round(time.Millisecond), err)
		_ = writeEvent("error", err.Error())
		return
	}
	_ = writeEvent("done", "")
	log.Printf("[request_id=%s] chat_done ts=%s elapsed=%s", requestID, time.Now().Format(time.RFC3339Nano), time.Since(start).Round(time.Millisecond))
}

func (s *Server) writeLine(w http.ResponseWriter, payload any, flusher http.Flusher) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, bytes.NewReader(append(data, '\n'))); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) handleV1Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cfg, err := s.store.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := make([]map[string]any, 0, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		data = append(data, map[string]any{
			"id":       profile.Model,
			"object":   "model",
			"owned_by": profile.Provider,
			"profile":  profile.ID,
		})
	}
	s.writeJSON(w, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (s *Server) handleV1ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	requestID := fmt.Sprintf("v1chat-%d", start.UnixNano())

	chatReq, err := gateway.ParseChatRequest(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := s.store.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	profile, ok := cfg.FindProfileForModel(chatReq.Model)
	if !ok {
		http.Error(w, "no profile configured", http.StatusBadRequest)
		return
	}
	log.Printf("[request_id=%s] v1_chat_received profile=%s requested_model=%s stream=%t messages=%d", requestID, profile.ID, chatReq.Model, chatReq.Stream, len(chatReq.Messages))

	if chatReq.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		result, err := s.gateway.Chat(r.Context(), profile, chatReq, func(delta string) error {
			return writeOpenAISSE(w, flusher, profile.UpstreamModel(), delta)
		})
		if err != nil {
			_ = writeOpenAIErrorSSE(w, flusher, err)
			return
		}
		_ = writeOpenAISSEDone(w, flusher)
		log.Printf("[request_id=%s] v1_chat_done elapsed=%s trace=%v", requestID, time.Since(start).Round(time.Millisecond), result.Trace)
		return
	}

	result, err := s.gateway.Chat(r.Context(), profile, chatReq, nil)
	if err != nil {
		writeOpenAIErrorJSON(w, err)
		return
	}
	s.writeJSON(w, openAICompletionResponse(profile.UpstreamModel(), result.Text))
	log.Printf("[request_id=%s] v1_chat_done elapsed=%s trace=%v", requestID, time.Since(start).Round(time.Millisecond), result.Trace)
}

func (s *Server) handleDiagnoseProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	profileID := strings.TrimPrefix(r.URL.Path, "/api/diagnose/profile/")
	if profileID == "" {
		http.Error(w, "profile id is required", http.StatusBadRequest)
		return
	}
	cfg, err := s.store.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	profile := cfg.FindProfile(profileID)
	if profile == nil {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	result := s.gateway.Diagnose(r.Context(), *profile)
	s.writeJSON(w, result)
}

func selectProfile(cfg config.Config, profileID string, model string, providerName string) (config.Profile, bool) {
	if profileID != "" {
		if profile := cfg.FindProfile(profileID); profile != nil {
			return *profile, true
		}
		return config.Profile{}, false
	}
	if model != "" {
		return cfg.FindProfileForModel(model)
	}
	if providerName != "" {
		for _, profile := range cfg.Profiles {
			if profile.Provider == providerName {
				return profile, true
			}
		}
	}
	return cfg.ActiveProfile()
}

func writeOpenAISSE(w http.ResponseWriter, flusher http.Flusher, model string, delta string) error {
	payload := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]string{
					"content": delta,
				},
				"finish_reason": nil,
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeOpenAIErrorSSE(w http.ResponseWriter, flusher http.Flusher, err error) error {
	payload := map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "gateway_error",
		},
	}
	data, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return marshalErr
	}
	if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", data); writeErr != nil {
		return writeErr
	}
	return writeOpenAISSEDone(w, flusher)
}

func writeOpenAISSEDone(w http.ResponseWriter, flusher http.Flusher) error {
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func openAICompletionResponse(model string, content string) map[string]any {
	now := time.Now()
	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", now.UnixNano()),
		"object":  "chat.completion",
		"created": now.Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	}
}

func writeOpenAIErrorJSON(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if upstreamErr, ok := err.(*gateway.UpstreamError); ok && upstreamErr.StatusCode >= 400 && upstreamErr.StatusCode < 500 {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "gateway_error",
		},
	})
}

func (s *Server) handleIpdbStatus(w http.ResponseWriter, r *http.Request) {
	if s.ipdbCli == nil {
		s.writeJSON(w, map[string]any{"enabled": false, "count": 0})
		return
	}
	ips := s.ipdbCli.GetAllIPs()
	s.writeJSON(w, map[string]any{
		"enabled": s.ipdbCli.Count() > 0,
		"count":   len(ips),
		"sample":  sampleIPs(ips, 5),
	})
}

func sampleIPs(ips []string, n int) []string {
	if len(ips) <= n {
		return ips
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		idx := i * (len(ips) - 1) / (n - 1)
		out[i] = ips[idx]
	}
	return out
}
