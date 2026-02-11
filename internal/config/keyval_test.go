package config

import (
	"testing"
)

func TestGetConfigValue(t *testing.T) {
	cfg := &Config{
		DefaultAgent:       "codex",
		MaxWorkers:         4,
		ReviewContextCount: 3,
	}

	tests := []struct {
		key  string
		want string
	}{
		{"default_agent", "codex"},
		{"max_workers", "4"},
		{"review_context_count", "3"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := GetConfigValue(cfg, tt.key)
			if err != nil {
				t.Fatalf("GetConfigValue(%q) error: %v", tt.key, err)
			}
			if got != tt.want {
				t.Errorf("GetConfigValue(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestGetConfigValueNested(t *testing.T) {
	cfg := &Config{
		Sync: SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://localhost/test",
		},
		CI: CIConfig{
			PollInterval: "10m",
		},
	}

	tests := []struct {
		key  string
		want string
	}{
		{"sync.enabled", "true"},
		{"sync.postgres_url", "postgres://localhost/test"},
		{"ci.poll_interval", "10m"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := GetConfigValue(cfg, tt.key)
			if err != nil {
				t.Fatalf("GetConfigValue(%q) error: %v", tt.key, err)
			}
			if got != tt.want {
				t.Errorf("GetConfigValue(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestGetConfigValueUnknownKey(t *testing.T) {
	cfg := &Config{}
	_, err := GetConfigValue(cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestSetConfigValue(t *testing.T) {
	cfg := &Config{}

	if err := SetConfigValue(cfg, "default_agent", "claude-code"); err != nil {
		t.Fatalf("SetConfigValue error: %v", err)
	}
	if cfg.DefaultAgent != "claude-code" {
		t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, "claude-code")
	}

	if err := SetConfigValue(cfg, "max_workers", "8"); err != nil {
		t.Fatalf("SetConfigValue error: %v", err)
	}
	if cfg.MaxWorkers != 8 {
		t.Errorf("MaxWorkers = %d, want 8", cfg.MaxWorkers)
	}

	if err := SetConfigValue(cfg, "sync.enabled", "true"); err != nil {
		t.Fatalf("SetConfigValue error: %v", err)
	}
	if !cfg.Sync.Enabled {
		t.Error("Sync.Enabled = false, want true")
	}
}

func TestSetConfigValueBoolPtr(t *testing.T) {
	cfg := &Config{}
	if err := SetConfigValue(cfg, "allow_unsafe_agents", "true"); err != nil {
		t.Fatalf("SetConfigValue error: %v", err)
	}
	if cfg.AllowUnsafeAgents == nil || !*cfg.AllowUnsafeAgents {
		t.Error("AllowUnsafeAgents should be true")
	}
}

func TestSetConfigValueInvalidType(t *testing.T) {
	cfg := &Config{}
	if err := SetConfigValue(cfg, "max_workers", "notanumber"); err == nil {
		t.Fatal("expected error for invalid integer")
	}
}

func TestSetConfigValueSlice(t *testing.T) {
	cfg := &Config{
		CI: CIConfig{},
	}
	if err := SetConfigValue(cfg, "ci.repos", "org/repo1,org/repo2"); err != nil {
		t.Fatalf("SetConfigValue error: %v", err)
	}
	if len(cfg.CI.Repos) != 2 || cfg.CI.Repos[0] != "org/repo1" || cfg.CI.Repos[1] != "org/repo2" {
		t.Errorf("CI.Repos = %v, want [org/repo1 org/repo2]", cfg.CI.Repos)
	}
}

func TestListConfigKeys(t *testing.T) {
	cfg := &Config{
		DefaultAgent: "codex",
		MaxWorkers:   4,
		Sync: SyncConfig{
			Enabled: true,
		},
	}

	kvs := ListConfigKeys(cfg)
	if len(kvs) == 0 {
		t.Fatal("expected non-empty list")
	}

	found := make(map[string]string)
	for _, kv := range kvs {
		found[kv.Key] = kv.Value
	}

	if found["default_agent"] != "codex" {
		t.Errorf("missing or wrong default_agent: %q", found["default_agent"])
	}
	if found["max_workers"] != "4" {
		t.Errorf("missing or wrong max_workers: %q", found["max_workers"])
	}
	if found["sync.enabled"] != "true" {
		t.Errorf("missing or wrong sync.enabled: %q", found["sync.enabled"])
	}
}

func TestListConfigKeysRepo(t *testing.T) {
	cfg := &RepoConfig{
		Agent:            "claude-code",
		ReviewGuidelines: "Be thorough",
	}

	kvs := ListConfigKeys(cfg)
	found := make(map[string]string)
	for _, kv := range kvs {
		found[kv.Key] = kv.Value
	}

	if found["agent"] != "claude-code" {
		t.Errorf("missing or wrong agent: %q", found["agent"])
	}
	if found["review_guidelines"] != "Be thorough" {
		t.Errorf("missing or wrong review_guidelines: %q", found["review_guidelines"])
	}
}

func TestMergedConfigWithOrigin(t *testing.T) {
	global := DefaultConfig()
	global.DefaultAgent = "gemini"

	repo := &RepoConfig{
		Agent: "claude-code",
	}

	kvos := MergedConfigWithOrigin(global, repo)
	if len(kvos) == 0 {
		t.Fatal("expected non-empty list")
	}

	found := make(map[string]KeyValueOrigin)
	for _, kvo := range kvos {
		found[kvo.Key] = kvo
	}

	// default_agent is set in global (overrides default "codex")
	if kvo, ok := found["default_agent"]; ok {
		if kvo.Value != "gemini" || kvo.Origin != "global" {
			t.Errorf("default_agent = {%q, %q}, want {gemini, global}", kvo.Value, kvo.Origin)
		}
	} else {
		t.Error("missing default_agent in merged output")
	}

	// max_workers is at default value
	if kvo, ok := found["max_workers"]; ok {
		if kvo.Origin != "default" {
			t.Errorf("max_workers origin = %q, want default", kvo.Origin)
		}
	}

	// repo agent key won't appear in global config merged view since it's a RepoConfig key
	// But "agent" from repo should appear
	if kvo, ok := found["agent"]; ok {
		if kvo.Value != "claude-code" || kvo.Origin != "local" {
			t.Errorf("agent = {%q, %q}, want {claude-code, local}", kvo.Value, kvo.Origin)
		}
	}
}

func TestIsValidKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"default_agent", true}, // Config only
		{"agent", true},         // RepoConfig only
		{"max_workers", true},   // Config only
		{"sync.enabled", true},  // nested Config
		{"nonexistent", false},
		{"fake.key", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsValidKey(tt.key)
			if got != tt.want {
				t.Errorf("IsValidKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}
