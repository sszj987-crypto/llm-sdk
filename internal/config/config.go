package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type ProviderKind string

const (
	ProviderOpenAI ProviderKind = "openai"
	ProviderGemini ProviderKind = "gemini"
)

type RouteMode string

const (
	RouteDirect    RouteMode = "direct"
	RouteWorkerSNI RouteMode = "worker_sni"
)

type Config struct {
	ActiveProfileID string    `json:"activeProfileId"`
	Profiles        []Profile `json:"profiles"`

	Provider   string `json:"provider,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
	APIKey     string `json:"apiKey,omitempty"`
	Model      string `json:"model,omitempty"`
	WorkerURL  string `json:"workerUrl,omitempty"`
	QuicEnable bool   `json:"quicEnable,omitempty"`
}

type Profile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	BaseURL      string `json:"baseUrl"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	RouteMode    string `json:"routeMode"`
	WorkerURL    string `json:"workerUrl"`
	HTTP3Enabled bool   `json:"http3Enabled"`
}

func Default() Config {
	return Config{
		ActiveProfileID: "gemini-default",
		Profiles: []Profile{
			DefaultProfile(),
		},
	}
}

func DefaultProfile() Profile {
	return Profile{
		ID:        "gemini-default",
		Name:      "Gemini Default",
		Provider:  string(ProviderGemini),
		BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
		Model:     "gemini-2.5-flash",
		RouteMode: string(RouteDirect),
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

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, err
	}
	var cfg Config
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
	cfg.clearLegacyFields()
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
	if len(c.Profiles) == 0 {
		profile := DefaultProfile()
		if c.Provider != "" {
			profile.Provider = c.Provider
		}
		if c.BaseURL != "" {
			profile.BaseURL = c.BaseURL
		}
		if c.APIKey != "" {
			profile.APIKey = c.APIKey
		}
		if c.Model != "" {
			profile.Model = c.Model
		}
		if c.WorkerURL != "" {
			profile.WorkerURL = c.WorkerURL
			profile.RouteMode = string(RouteWorkerSNI)
		}
		if c.QuicEnable {
			profile.HTTP3Enabled = true
		}
		c.Profiles = []Profile{profile}
	}

	for i := range c.Profiles {
		c.Profiles[i].applyDefaults(i)
	}

	if c.ActiveProfileID == "" {
		c.ActiveProfileID = c.Profiles[0].ID
	}
	if c.FindProfile(c.ActiveProfileID) == nil {
		c.ActiveProfileID = c.Profiles[0].ID
	}
}

func (p *Profile) applyDefaults(index int) {
	defaultProfile := DefaultProfile()
	if p.ID == "" {
		if index == 0 {
			p.ID = defaultProfile.ID
		} else {
			p.ID = fmt.Sprintf("profile-%d", index+1)
		}
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.Provider == "" {
		p.Provider = defaultProfile.Provider
	}
	if p.BaseURL == "" {
		p.BaseURL = defaultProfile.BaseURL
	}
	if p.Model == "" {
		p.Model = defaultProfile.Model
	}
	if p.RouteMode == "" {
		if p.WorkerURL != "" {
			p.RouteMode = string(RouteWorkerSNI)
		} else {
			p.RouteMode = string(RouteDirect)
		}
	}
	if p.RouteMode != string(RouteWorkerSNI) {
		p.RouteMode = string(RouteDirect)
	}
}

func (c Config) ActiveProfile() (Profile, bool) {
	if profile := c.FindProfile(c.ActiveProfileID); profile != nil {
		return *profile, true
	}
	if len(c.Profiles) == 0 {
		return Profile{}, false
	}
	return c.Profiles[0], true
}

func (c Config) FindProfile(id string) *Profile {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			return &c.Profiles[i]
		}
	}
	return nil
}

func (c Config) FindProfileForModel(model string) (Profile, bool) {
	if model != "" {
		for _, profile := range c.Profiles {
			if profile.ID == model || profile.Model == model {
				return profile, true
			}
		}
	}
	return c.ActiveProfile()
}

func (p Profile) UpstreamModel() string {
	if p.Model == "" {
		return DefaultProfile().Model
	}
	return p.Model
}

func (c *Config) clearLegacyFields() {
	c.Provider = ""
	c.BaseURL = ""
	c.APIKey = ""
	c.Model = ""
	c.WorkerURL = ""
	c.QuicEnable = false
}
