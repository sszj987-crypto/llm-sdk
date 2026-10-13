package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigratesLegacySingleProfileConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"provider":"openai","baseUrl":"https://api.openai.com/v1","apiKey":"test","model":"gpt-4o-mini","workerUrl":"https://worker.example","quicEnable":true}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	profile, ok := cfg.ActiveProfile()
	if !ok {
		t.Fatal("expected active profile")
	}
	if profile.Provider != "openai" || profile.Model != "gpt-4o-mini" {
		t.Fatalf("unexpected migrated profile: %+v", profile)
	}
	if profile.RouteMode != string(RouteWorkerSNI) || !profile.HTTP3Enabled {
		t.Fatalf("legacy route settings not migrated: %+v", profile)
	}
}

func TestSaveWritesProfileConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{
		ActiveProfileID: "main",
		Profiles: []Profile{
			{ID: "main", Provider: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", APIKey: "test", Model: "gemini-2.5-flash"},
		},
	}
	if err := NewStore(path).Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	loaded, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	profile, ok := loaded.ActiveProfile()
	if !ok || profile.ID != "main" || profile.RouteMode != string(RouteDirect) {
		t.Fatalf("unexpected saved profile: %+v", loaded)
	}
}
