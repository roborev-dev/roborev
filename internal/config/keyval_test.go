package config

import (
	"strings"
	"testing"
)

func toMap(kvs []KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

func toOriginMap(kvos []KeyValueOrigin) map[string]KeyValueOrigin {
	m := make(map[string]KeyValueOrigin, len(kvos))
	for _, kvo := range kvos {
		m[kvo.Key] = kvo
	}
	return m
}

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
			GitHubAppConfig: GitHubAppConfig{
				GitHubAppID:         12345,
				GitHubAppPrivateKey: "test-private-key",
			},
		},
	}

	tests := []struct {
		key  string
		want string
	}{
		{"sync.enabled", "true"},
		{"sync.postgres_url", "postgres://localhost/test"},
		{"ci.poll_interval", "10m"},
		{"ci.github_app_id", "12345"},
		{"ci.github_app_private_key", "test-private-key"},
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
	tests := []struct {
		name   string
		key    string
		val    string
		verify func(*Config) bool
	}{
		{
			name:   "set string field",
			key:    "default_agent",
			val:    "claude-code",
			verify: func(c *Config) bool { return c.DefaultAgent == "claude-code" },
		},
		{
			name:   "set int field",
			key:    "max_workers",
			val:    "8",
			verify: func(c *Config) bool { return c.MaxWorkers == 8 },
		},
		{
			name:   "set nested bool",
			key:    "sync.enabled",
			val:    "true",
			verify: func(c *Config) bool { return c.Sync.Enabled },
		},
		{
			name:   "set embedded github app id",
			key:    "ci.github_app_id",
			val:    "98765",
			verify: func(c *Config) bool { return c.CI.GitHubAppID == 98765 },
		},
		{
			name:   "set embedded github app private key",
			key:    "ci.github_app_private_key",
			val:    "private-key-data",
			verify: func(c *Config) bool { return c.CI.GitHubAppPrivateKey == "private-key-data" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			err := SetConfigValue(cfg, tt.key, tt.val)
			if err != nil {
				t.Fatalf("SetConfigValue(%q, %q) error: %v", tt.key, tt.val, err)
			}
			if !tt.verify(cfg) {
				t.Errorf("verification failed for key %q value %q", tt.key, tt.val)
			}
		})
	}
}

func TestSetConfigValueMultipleKeys(t *testing.T) {
	cfg := &Config{}
	updates := []struct {
		key string
		val string
	}{
		{key: "default_agent", val: "claude-code"},
		{key: "max_workers", val: "8"},
		{key: "sync.enabled", val: "true"},
		{key: "ci.github_app_id", val: "98765"},
		{key: "ci.github_app_private_key", val: "private-key-data"},
	}

	for _, update := range updates {
		if err := SetConfigValue(cfg, update.key, update.val); err != nil {
			t.Fatalf("SetConfigValue(%q, %q) error: %v", update.key, update.val, err)
		}
	}

	found := toMap(ListConfigKeys(cfg))
	want := map[string]string{
		"default_agent":             "claude-code",
		"max_workers":               "8",
		"sync.enabled":              "true",
		"ci.github_app_id":          "98765",
		"ci.github_app_private_key": "private-key-data",
	}

	for key, value := range want {
		if found[key] != value {
			t.Errorf("key %q = %q, want %q", key, found[key], value)
		}
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

func TestSetConfigValueSliceEmpty(t *testing.T) {
	cfg := &Config{
		CI: CIConfig{
			Repos: []string{"org/repo1"},
		},
	}
	if err := SetConfigValue(cfg, "ci.repos", ""); err != nil {
		t.Fatalf("SetConfigValue error: %v", err)
	}
	if len(cfg.CI.Repos) != 0 {
		t.Errorf("CI.Repos = %v, want empty slice", cfg.CI.Repos)
	}
}

func TestListConfigKeys(t *testing.T) {
	cfg := &Config{
		DefaultAgent: "codex",
		MaxWorkers:   4,
		Sync: SyncConfig{
			Enabled: true,
		},
		CI: CIConfig{
			GitHubAppConfig: GitHubAppConfig{
				GitHubAppID:         12345,
				GitHubAppPrivateKey: "private-key-data",
			},
		},
	}

	kvs := ListConfigKeys(cfg)
	if len(kvs) == 0 {
		t.Fatal("expected non-empty list")
	}

	found := toMap(kvs)

	if found["default_agent"] != "codex" {
		t.Errorf("missing or wrong default_agent: %q", found["default_agent"])
	}
	if found["max_workers"] != "4" {
		t.Errorf("missing or wrong max_workers: %q", found["max_workers"])
	}
	if found["sync.enabled"] != "true" {
		t.Errorf("missing or wrong sync.enabled: %q", found["sync.enabled"])
	}
	if found["ci.github_app_id"] != "12345" {
		t.Errorf("missing or wrong ci.github_app_id: %q", found["ci.github_app_id"])
	}
	if found["ci.github_app_private_key"] != "private-key-data" {
		t.Errorf("missing or wrong ci.github_app_private_key: %q", found["ci.github_app_private_key"])
	}
}

func TestListConfigKeysRepo(t *testing.T) {
	cfg := &RepoConfig{
		Agent:            "claude-code",
		ReviewGuidelines: "Be thorough",
	}

	kvs := ListConfigKeys(cfg)
	found := toMap(kvs)

	if found["agent"] != "claude-code" {
		t.Errorf("missing or wrong agent: %q", found["agent"])
	}
	if found["review_guidelines"] != "Be thorough" {
		t.Errorf("missing or wrong review_guidelines: %q", found["review_guidelines"])
	}
}

func TestListConfigKeysIncludesComplexNonZeroFields(t *testing.T) {
	cfg := &Config{
		Hooks: []HookConfig{
			{
				Event:   "review.failed",
				Command: "echo failed",
				Type:    "command",
			},
		},
		Sync: SyncConfig{
			RepoNames: map[string]string{
				"org/repo": "my-project",
			},
		},
		CI: CIConfig{
			GitHubAppConfig: GitHubAppConfig{
				GitHubAppInstallations: map[string]int64{
					"org": 1234,
				},
			},
		},
	}

	found := toMap(ListConfigKeys(cfg))

	if got, ok := found["sync.repo_names"]; !ok || !strings.Contains(got, "org/repo:my-project") {
		t.Errorf("missing or wrong sync.repo_names: %q", got)
	}
	if got, ok := found["ci.github_app_installations"]; !ok || !strings.Contains(got, "org:1234") {
		t.Errorf("missing or wrong ci.github_app_installations: %q", got)
	}
	if got, ok := found["hooks"]; !ok || !strings.Contains(got, "review.failed") {
		t.Errorf("missing or wrong hooks: %q", got)
	}
}

func TestMergedConfigWithOrigin(t *testing.T) {
	global := DefaultConfig()
	global.DefaultAgent = "gemini"

	repo := &RepoConfig{
		Agent: "claude-code",
	}

	rawGlobal := map[string]interface{}{"default_agent": "gemini"}
	rawRepo := map[string]interface{}{"agent": "claude-code"}

	kvos := MergedConfigWithOrigin(global, repo, rawGlobal, rawRepo)
	if len(kvos) == 0 {
		t.Fatal("expected non-empty list")
	}

	found := toOriginMap(kvos)

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

func TestIsConfigValueSet(t *testing.T) {
	cfg := &Config{
		DefaultAgent: "codex",
		MaxWorkers:   4,
	}

	if !IsConfigValueSet(cfg, "default_agent") {
		t.Error("expected default_agent to be set")
	}
	if !IsConfigValueSet(cfg, "max_workers") {
		t.Error("expected max_workers to be set")
	}
	if IsConfigValueSet(cfg, "cursor_cmd") {
		t.Error("expected cursor_cmd to not be set")
	}
	if IsConfigValueSet(cfg, "nonexistent") {
		t.Error("expected nonexistent to not be set")
	}
}

func TestMergedConfigWithOriginLocalOverridesGlobal(t *testing.T) {
	global := DefaultConfig()
	global.ReviewContextCount = 5

	repo := &RepoConfig{
		ReviewContextCount: 10,
	}

	rawGlobal := map[string]interface{}{"review_context_count": int64(5)}
	rawRepo := map[string]interface{}{"review_context_count": int64(10)}

	kvos := MergedConfigWithOrigin(global, repo, rawGlobal, rawRepo)
	found := toOriginMap(kvos)

	// review_context_count should be overridden by local
	if kvo, ok := found["review_context_count"]; ok {
		if kvo.Value != "10" || kvo.Origin != "local" {
			t.Errorf("review_context_count = {%q, %q}, want {10, local}", kvo.Value, kvo.Origin)
		}
	} else {
		t.Error("missing review_context_count in merged output")
	}
}

func TestMergedConfigWithOriginShowsAllOrigins(t *testing.T) {
	global := DefaultConfig()
	global.DefaultAgent = "gemini" // override from default

	rawGlobal := map[string]interface{}{"default_agent": "gemini"}
	kvos := MergedConfigWithOrigin(global, nil, rawGlobal, nil)
	found := toOriginMap(kvos)

	// default_agent was changed from default
	if found["default_agent"].Origin != "global" {
		t.Errorf("default_agent origin = %q, want global", found["default_agent"].Origin)
	}
	// max_workers should be at default
	if found["max_workers"].Origin != "default" {
		t.Errorf("max_workers origin = %q, want default", found["max_workers"].Origin)
	}
}

func TestMergedConfigWithOriginIncludesComplexFields(t *testing.T) {
	global := DefaultConfig()
	global.Sync.RepoNames = map[string]string{
		"org/repo": "my-project",
	}
	global.CI.GitHubAppInstallations = map[string]int64{
		"org": 1234,
	}

	rawGlobal := map[string]interface{}{
		"sync": map[string]interface{}{
			"repo_names": map[string]interface{}{
				"org/repo": "my-project",
			},
		},
		"ci": map[string]interface{}{
			"github_app_installations": map[string]interface{}{
				"org": int64(1234),
			},
		},
	}

	kvos := MergedConfigWithOrigin(global, nil, rawGlobal, nil)
	found := toOriginMap(kvos)

	if kvo, ok := found["sync.repo_names"]; !ok {
		t.Error("missing sync.repo_names in merged output")
	} else if !strings.Contains(kvo.Value, "org/repo:my-project") {
		t.Errorf("sync.repo_names value = %q, want to contain org/repo:my-project", kvo.Value)
	} else if kvo.Origin != "global" {
		t.Errorf("sync.repo_names origin = %q, want global", kvo.Origin)
	}

	if kvo, ok := found["ci.github_app_installations"]; !ok {
		t.Error("missing ci.github_app_installations in merged output")
	} else if !strings.Contains(kvo.Value, "org:1234") {
		t.Errorf("ci.github_app_installations value = %q, want to contain org:1234", kvo.Value)
	} else if kvo.Origin != "global" {
		t.Errorf("ci.github_app_installations origin = %q, want global", kvo.Origin)
	}
}

func TestFormatMapDeterministic(t *testing.T) {
	cfg := &Config{
		Sync: SyncConfig{
			RepoNames: map[string]string{
				"b/repo": "bravo",
				"a/repo": "alpha",
				"c/repo": "charlie",
			},
		},
	}

	// Run multiple times to verify determinism
	var prev string
	for i := 0; i < 10; i++ {
		kvs := ListConfigKeys(cfg)
		found := toMap(kvs)
		got := found["sync.repo_names"]
		if prev != "" && got != prev {
			t.Fatalf("non-deterministic map output: %q vs %q", prev, got)
		}
		prev = got
	}

	// Verify sorted order
	kvs := ListConfigKeys(cfg)
	found := toMap(kvs)
	want := "a/repo:alpha,b/repo:bravo,c/repo:charlie"
	if found["sync.repo_names"] != want {
		t.Errorf("sync.repo_names = %q, want %q", found["sync.repo_names"], want)
	}
}

func TestIsValidKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"default_agent", true},             // Config only
		{"agent", true},                     // RepoConfig only
		{"max_workers", true},               // Config only
		{"sync.enabled", true},              // nested Config
		{"ci.github_app_id", true},          // inline embedded config
		{"ci.github_app_private_key", true}, // inline embedded sensitive config
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

func TestIsSensitiveKey(t *testing.T) {
	if !IsSensitiveKey("ci.github_app_private_key") {
		t.Error("expected ci.github_app_private_key to be sensitive")
	}
	if IsSensitiveKey("ci.github_app_id") {
		t.Error("expected ci.github_app_id to not be sensitive")
	}
}
