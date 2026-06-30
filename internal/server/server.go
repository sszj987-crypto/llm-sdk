package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"time"

	assets "llm-sdk"
	"llm-sdk/internal/config"
	"llm-sdk/internal/dialer"
	"llm-sdk/internal/ipdb"
	"llm-sdk/internal/provider"
)

type Server struct {
	store  *config.Store
	client *http.Client
	echDialer *dialer.Dialer
	ipdbCli   *ipdb.Client
	openAI *provider.OpenAIAdapter
	gemini *provider.GeminiAdapter
}

func New(store *config.Store) (*Server, error) {
	echDialer := dialer.New()

	// Start IPDB client for auto-fetching preferred CF IPs.
	ipdbCli := ipdb.NewClient()
	if ipdbCli.Count() > 0 {
		echDialer.SetIPProvider(ipdbCli)
		log.Printf("[server] ipdb_initialized preferred_ips=%d", ipdbCli.Count())
	} else {
		log.Printf("[server] ipdb_no_ips — falling back to DNS resolution")
	}

	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := netSplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if port != "" {
				echDialer.Port = port
			}
			return echDialer.DialTLSContext(ctx, network, host)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
		},
		ForceAttemptHTTP2: true,
	}

	client := &http.Client{
		Timeout:   0,
		Transport: transport,
	}
	return &Server{
		store:  store,
		client: client,
		echDialer: echDialer,
		ipdbCli:   ipdbCli,
		openAI: provider.NewOpenAIAdapter(client),
		gemini: provider.NewGeminiAdapter(client),
	}, nil
}

// Shutdown gracefully stops background loops (e.g. IPDB refresh).
func (s *Server) Shutdown() {
	if s.ipdbCli != nil {
		s.ipdbCli.Stop()
	}
}

func netSplitHostPort(hostport string) (host, port string, err error) {
	for i := len(hostport) - 1; i >= 0; i-- {
		if hostport[i] == ':' {
			if hostport[0] == '[' {
				end := stringsIndexByte(hostport, ']')
				if end < 0 {
					return "", "", fmt.Errorf("missing ']' in address")
				}
				return hostport[1:end], hostport[end+2:], nil
			}
			return hostport[:i], hostport[i+1:], nil
		}
	}
	return hostport, "", nil
}

func stringsIndexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func (s *Server) Register(mux *http.ServeMux) {
	webSubFS, err := fs.Sub(assets.WebFS, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(webSubFS)))
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/ipdb/status", s.handleIpdbStatus)
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
	Provider string             `json:"provider"`
	Messages []provider.Message `json:"messages"`
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

	providerName := req.Provider
	if providerName == "" {
		providerName = cfg.Provider
	}
	selected := provider.Config{
		BaseURL:   cfg.BaseURL,
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		WorkerURL: cfg.WorkerURL,
	}
	log.Printf("[request_id=%s] provider_config base_url=%s model=%s worker_url_set=%t", requestID, selected.BaseURL, selected.Model, selected.WorkerURL != "")

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

	err = s.chatStream(r.Context(), providerName, selected, req.Messages, func(delta string) error {
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

func (s *Server) chatStream(ctx context.Context, providerName string, cfg provider.Config, messages []provider.Message, onDelta provider.StreamHandler) error {
	switch providerName {
	case string(config.ProviderOpenAI):
		return s.openAI.Chat(ctx, cfg, messages, onDelta)
	case string(config.ProviderGemini):
		return s.gemini.Chat(ctx, cfg, messages, onDelta)
	default:
		return fmt.Errorf("unsupported provider: %s", providerName)
	}
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
