package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type ProviderKind string

const (
	ProviderOpenAI ProviderKind = "openai"
	ProviderGemini ProviderKind = "gemini"
)

type Config struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"`
	Model     string `json:"model"`
	WorkerURL string `json:"workerUrl"`
	QuicEnable bool   `json:"quicEnable"`
}

func Default() Config {
	return Config{
		Provider: string(ProviderGemini),
		BaseURL:  "https://generativelanguage.googleapis.com/v1beta/openai",
		Model:    "gemini-2.5-flash",
	}
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := Default()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (s *Store) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg.applyDefaults()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (c *Config) applyDefaults() {
	if c.Provider == "" {
		c.Provider = string(ProviderGemini)
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	}
	if c.Model == "" {
		c.Model = "gemini-2.5-flash"
	}
}
