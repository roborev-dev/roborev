package config

import (
	"math"
	"reflect"
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

func assertConfigValues(t *testing.T, actual []KeyValue, expected map[string]string) {
	t.Helper()
	m := toMap(actual)
	for k, want := range expected {
		got, ok := m[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if got != want {
			t.Errorf("key %q = %q, want %q", k, got, want)
		}
	}
}

type expectedOrigin struct {
	Value  string
	Origin string
}

func assertOrigins(t *testing.T, actual []KeyValueOrigin, expected map[string]expectedOrigin) {
	t.Helper()
	m := toOriginMap(actual)
	for k, want := range expected {
		got, ok := m[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if got.Value != want.Value {
			t.Errorf("key %q value = %q, want %q", k, got.Value, want.Value)
		}
		if got.Origin != want.Origin {
			t.Errorf("key %q origin = %q, want %q", k, got.Origin, want.Origin)
		}
	}
}

func assertDeterministic(t *testing.T, fn func() string) {
	t.Helper()

	var prev string
	for i := range 20 {
		got := fn()
		if prev != "" && got != prev {
			t.Fatalf("non-deterministic output on iteration %d: %q vs %q", i, prev, got)
		}
		prev = got
	}
}

func newComplexTestConfig() *Config {
	return &Config{
		DefaultAgent:       "codex",
		MaxWorkers:         4,
		ReviewContextCount: 3,
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
}

func TestGetConfigValue(t *testing.T) {
	cfg := newComplexTestConfig()

	tests := []struct {
		key  string
		want string
	}{
		{"default_agent", "codex"},
		{"max_workers", "4"},
		{"review_context_count", "3"},
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
		init   func() *Config
		verify func(*testing.T, *Config)
	}{
		{
			name: "set string field",
			key:  "default_agent",
			val:  "claude-code",
			verify: func(t *testing.T, c *Config) {
				if c.DefaultAgent != "claude-code" {
					t.Errorf("DefaultAgent = %q, want claude-code", c.DefaultAgent)
				}
			},
		},
		{
			name: "set int field",
			key:  "max_workers",
			val:  "8",
			verify: func(t *testing.T, c *Config) {
				if c.MaxWorkers != 8 {
					t.Errorf("MaxWorkers = %d, want 8", c.MaxWorkers)
				}
			},
		},
		{
			name: "set nested bool",
			key:  "sync.enabled",
			val:  "true",
			verify: func(t *testing.T, c *Config) {
				if !c.Sync.Enabled {
					t.Errorf("Sync.Enabled = %v, want true", c.Sync.Enabled)
				}
			},
		},
		{
			name: "set embedded github app id",
			key:  "ci.github_app_id",
			val:  "98765",
			verify: func(t *testing.T, c *Config) {
				if c.CI.GitHubAppID != 98765 {
					t.Errorf("CI.GitHubAppID = %d, want 98765", c.CI.GitHubAppID)
				}
			},
		},
		{
			name: "set embedded github app private key",
			key:  "ci.github_app_private_key",
			val:  "private-key-data",
			verify: func(t *testing.T, c *Config) {
				if c.CI.GitHubAppPrivateKey != "private-key-data" {
					t.Errorf("CI.GitHubAppPrivateKey = %q, want private-key-data", c.CI.GitHubAppPrivateKey)
				}
			},
		},
		{
			name: "set bool ptr",
			key:  "allow_unsafe_agents",
			val:  "true",
			verify: func(t *testing.T, c *Config) {
				if c.AllowUnsafeAgents == nil || !*c.AllowUnsafeAgents {
					t.Errorf("AllowUnsafeAgents = %v, want true", c.AllowUnsafeAgents)
				}
			},
		},
		{
			name: "set slice",
			key:  "ci.repos",
			val:  "org/repo1,org/repo2",
			verify: func(t *testing.T, c *Config) {
				if len(c.CI.Repos) != 2 || c.CI.Repos[0] != "org/repo1" || c.CI.Repos[1] != "org/repo2" {
					t.Errorf("CI.Repos = %v, want [org/repo1 org/repo2]", c.CI.Repos)
				}
			},
		},
		{
			name: "set slice from nil",
			key:  "ci.repos",
			val:  "org/repo1,org/repo2",
			init: func() *Config { return &Config{} },
			verify: func(t *testing.T, c *Config) {
				if len(c.CI.Repos) != 2 || c.CI.Repos[0] != "org/repo1" || c.CI.Repos[1] != "org/repo2" {
					t.Errorf("CI.Repos = %v, want [org/repo1 org/repo2]", c.CI.Repos)
				}
			},
		},
		{
			name: "set slice empty",
			key:  "ci.repos",
			val:  "",
			verify: func(t *testing.T, c *Config) {
				if len(c.CI.Repos) != 0 {
					t.Errorf("CI.Repos = %v, want empty", c.CI.Repos)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg *Config
			if tt.init != nil {
				cfg = tt.init()
			} else {
				cfg = &Config{
					CI: CIConfig{
						Repos: []string{"initial"},
					},
				}
			}
			err := SetConfigValue(cfg, tt.key, tt.val)
			if err != nil {
				t.Fatalf("SetConfigValue(%q, %q) error: %v", tt.key, tt.val, err)
			}
			tt.verify(t, cfg)
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

	assertConfigValues(t, ListConfigKeys(cfg), map[string]string{
		"default_agent":             "claude-code",
		"max_workers":               "8",
		"sync.enabled":              "true",
		"ci.github_app_id":          "98765",
		"ci.github_app_private_key": "private-key-data",
	})
}

func TestSetConfigValueInvalidType(t *testing.T) {
	cfg := &Config{}
	if err := SetConfigValue(cfg, "max_workers", "notanumber"); err == nil {
		t.Fatal("expected error for invalid integer")
	}
}

func TestListConfigKeys(t *testing.T) {
	cfg := newComplexTestConfig()
	// Set the key so it matches what we expect from the old test.
	cfg.CI.GitHubAppPrivateKey = "private-key-data"

	assertConfigValues(t, ListConfigKeys(cfg), map[string]string{
		"default_agent":             "codex",
		"max_workers":               "4",
		"review_context_count":      "3",
		"sync.enabled":              "true",
		"sync.postgres_url":         "postgres://localhost/test",
		"ci.poll_interval":          "10m",
		"ci.github_app_id":          "12345",
		"ci.github_app_private_key": "private-key-data",
	})
}

func TestListConfigKeysRepo(t *testing.T) {
	cfg := &RepoConfig{
		Agent:            "claude-code",
		ReviewGuidelines: "Be thorough",
	}

	assertConfigValues(t, ListConfigKeys(cfg), map[string]string{
		"agent":             "claude-code",
		"review_guidelines": "Be thorough",
	})
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
	tests := []struct {
		name      string
		global    *Config
		repo      *RepoConfig
		rawGlobal map[string]any
		rawRepo   map[string]any
		want      map[string]expectedOrigin
		verify    func(*testing.T, []KeyValueOrigin)
	}{
		{
			name: "both local and global set",
			global: func() *Config {
				g := DefaultConfig()
				g.DefaultAgent = "gemini"
				return g
			}(),
			repo:      &RepoConfig{Agent: "claude-code"},
			rawGlobal: map[string]any{"default_agent": "gemini"},
			rawRepo:   map[string]any{"agent": "claude-code"},
			want: map[string]expectedOrigin{
				"default_agent": {Value: "gemini", Origin: "global"},
				"agent":         {Value: "claude-code", Origin: "local"},
			},
			verify: func(t *testing.T, kvos []KeyValueOrigin) {
				found := toOriginMap(kvos)
				if kvo, ok := found["max_workers"]; ok {
					if kvo.Origin != "default" {
						t.Errorf("max_workers origin = %q, want default", kvo.Origin)
					}
				}
			},
		},
		{
			name: "local overrides global",
			global: func() *Config {
				g := DefaultConfig()
				g.ReviewContextCount = 5
				return g
			}(),
			repo:      &RepoConfig{ReviewContextCount: 10},
			rawGlobal: map[string]any{"review_context_count": int64(5)},
			rawRepo:   map[string]any{"review_context_count": int64(10)},
			want: map[string]expectedOrigin{
				"review_context_count": {Value: "10", Origin: "local"},
			},
		},
		{
			name: "shows all origins",
			global: func() *Config {
				g := DefaultConfig()
				g.DefaultAgent = "gemini" // override from default
				return g
			}(),
			rawGlobal: map[string]any{"default_agent": "gemini"},
			want: map[string]expectedOrigin{
				"default_agent": {Value: "gemini", Origin: "global"},
			},
			verify: func(t *testing.T, kvos []KeyValueOrigin) {
				found := toOriginMap(kvos)
				if found["max_workers"].Origin != "default" {
					t.Errorf("max_workers origin = %q, want default", found["max_workers"].Origin)
				}
			},
		},
		{
			name: "includes complex fields",
			global: func() *Config {
				g := DefaultConfig()
				g.Sync.RepoNames = map[string]string{"org/repo": "my-project"}
				g.CI.GitHubAppInstallations = map[string]int64{"org": 1234}
				return g
			}(),
			rawGlobal: map[string]any{
				"sync": map[string]any{
					"repo_names": map[string]any{"org/repo": "my-project"},
				},
				"ci": map[string]any{
					"github_app_installations": map[string]any{"org": int64(1234)},
				},
			},
			want: map[string]expectedOrigin{
				"sync.repo_names":             {Value: "org/repo:my-project", Origin: "global"},
				"ci.github_app_installations": {Value: "org:1234", Origin: "global"},
			},
		},
		{
			name:   "omits unset complex fields",
			global: DefaultConfig(),
			verify: func(t *testing.T, kvos []KeyValueOrigin) {
				found := toOriginMap(kvos)
				if _, ok := found["hooks"]; ok {
					t.Error("merged output should not include unset hooks")
				}
				if _, ok := found["sync.repo_names"]; ok {
					t.Error("merged output should not include unset sync.repo_names")
				}
				if _, ok := found["ci.github_app_installations"]; ok {
					t.Error("merged output should not include unset ci.github_app_installations")
				}
			},
		},
		{
			name:      "explicit global matching default shows global origin",
			global:    DefaultConfig(),
			rawGlobal: map[string]any{"max_workers": int64(4)}, // Explicitly set to default value
			verify: func(t *testing.T, kvos []KeyValueOrigin) {
				found := toOriginMap(kvos)
				if kvo, ok := found["max_workers"]; !ok {
					t.Error("expected max_workers in output")
				} else if kvo.Origin != "global" {
					t.Errorf("max_workers origin = %q, want %q", kvo.Origin, "global")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kvos := MergedConfigWithOrigin(tt.global, tt.repo, tt.rawGlobal, tt.rawRepo)
			if tt.want != nil {
				assertOrigins(t, kvos, tt.want)
			}
			if tt.verify != nil {
				tt.verify(t, kvos)
			}
		})
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
	assertDeterministic(t, func() string {
		kvs := ListConfigKeys(cfg)
		found := toMap(kvs)
		return found["sync.repo_names"]
	})

	// Verify sorted order
	kvs := ListConfigKeys(cfg)
	found := toMap(kvs)
	want := "a/repo:alpha,b/repo:bravo,c/repo:charlie"
	if found["sync.repo_names"] != want {
		t.Errorf("sync.repo_names = %q, want %q", found["sync.repo_names"], want)
	}
}

// collidingKey is a custom type whose String() always returns the same value,
// used to test formatMap's tie-breaking behavior with colliding keys.
type collidingKey int

func (k collidingKey) String() string { return "same" }

func TestFormatMapCollidingKeys(t *testing.T) {
	// Build a map[collidingKey]string where all keys stringify to "same"
	m := map[collidingKey]string{
		collidingKey(1): "alpha",
		collidingKey(2): "bravo",
		collidingKey(3): "charlie",
	}

	// Run multiple times to verify determinism despite colliding String() output
	assertDeterministic(t, func() string {
		return formatMap(reflect.ValueOf(m))
	})

	// All three entries must be present
	result := formatMap(reflect.ValueOf(m))
	for _, val := range []string{"alpha", "bravo", "charlie"} {
		if !strings.Contains(result, val) {
			t.Errorf("result %q missing value %q", result, val)
		}
	}

	// Verify exact expected output: keys sorted by %#v tie-breaker
	// collidingKey(1) < collidingKey(2) < collidingKey(3) by %#v
	want := "same:alpha,same:bravo,same:charlie"
	if result != want {
		t.Errorf("formatMap = %q, want %q", result, want)
	}
}

// fullyCollidingKey has both String() and GoString() returning constants,
// so %v and %#v both collide. Only structural comparison can distinguish keys.
type fullyCollidingKey int

func (k fullyCollidingKey) String() string   { return "same" }
func (k fullyCollidingKey) GoString() string { return "same" }

func TestFormatMapFullyCollidingKeys(t *testing.T) {
	m := map[fullyCollidingKey]string{
		fullyCollidingKey(10): "x",
		fullyCollidingKey(20): "y",
		fullyCollidingKey(30): "z",
	}

	// Run multiple times to verify determinism despite colliding %v and %#v
	assertDeterministic(t, func() string {
		return formatMap(reflect.ValueOf(m))
	})

	// Structural comparison orders by underlying int: 10 < 20 < 30
	want := "same:x,same:y,same:z"
	// Note: previous implementation compared prev with want, here we check deterministic result
	got := formatMap(reflect.ValueOf(m))
	if got != want {
		t.Errorf("formatMap = %q, want %q", got, want)
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

func TestIsGlobalKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"default_agent", true},      // Config only
		{"max_workers", true},        // Config only
		{"sync.enabled", true},       // nested Config
		{"agent", false},             // RepoConfig only
		{"review_guidelines", false}, // RepoConfig only
		{"nonexistent", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsGlobalKey(tt.key)
			if got != tt.want {
				t.Errorf("IsGlobalKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestListExplicitKeys(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *Config
		raw    map[string]any
		verify func(*testing.T, []KeyValue)
	}{
		{
			name: "only includes raw keys",
			cfg: func() *Config {
				c := DefaultConfig()
				c.MaxWorkers = 8
				return c
			}(),
			raw: map[string]any{"max_workers": int64(8)},
			verify: func(t *testing.T, kvs []KeyValue) {
				found := toMap(kvs)
				if _, ok := found["max_workers"]; !ok {
					t.Error("expected max_workers to be listed (explicitly in TOML)")
				}
				if _, ok := found["default_agent"]; ok {
					t.Error("default_agent should NOT be listed (not in raw TOML)")
				}
			},
		},
		{
			name: "includes zero values",
			cfg: &Config{
				MaxWorkers: 0,
				Sync:       SyncConfig{Enabled: false},
			},
			raw: map[string]any{
				"max_workers": int64(0),
				"sync":        map[string]any{"enabled": false},
			},
			verify: func(t *testing.T, kvs []KeyValue) {
				found := toMap(kvs)
				if got, ok := found["max_workers"]; !ok {
					t.Error("expected max_workers to be listed (explicit zero in TOML)")
				} else if got != "0" {
					t.Errorf("max_workers = %q, want %q", got, "0")
				}
				if got, ok := found["sync.enabled"]; !ok {
					t.Error("expected sync.enabled to be listed (explicit false in TOML)")
				} else if got != "false" {
					t.Errorf("sync.enabled = %q, want %q", got, "false")
				}
			},
		},
		{
			name: "includes empty values",
			cfg: &Config{
				DefaultModel: "",
				CI:           CIConfig{Repos: []string{}},
				Sync:         SyncConfig{RepoNames: map[string]string{}},
			},
			raw: map[string]any{
				"default_model": "",
				"ci":            map[string]any{"repos": []any{}},
				"sync":          map[string]any{"repo_names": map[string]any{}},
			},
			verify: func(t *testing.T, kvs []KeyValue) {
				found := toMap(kvs)
				if _, ok := found["default_model"]; !ok {
					t.Error("expected default_model to be listed (explicit empty string in TOML)")
				}
				if _, ok := found["ci.repos"]; !ok {
					t.Error("expected ci.repos to be listed (explicit empty slice in TOML)")
				}
				if _, ok := found["sync.repo_names"]; !ok {
					t.Error("expected sync.repo_names to be listed (explicit empty map in TOML)")
				}
			},
		},
		{
			name: "nil raw",
			cfg:  DefaultConfig(),
			raw:  nil,
			verify: func(t *testing.T, kvs []KeyValue) {
				if len(kvs) != 0 {
					t.Errorf("expected empty result for nil raw, got %d entries", len(kvs))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kvs := ListExplicitKeys(tt.cfg, tt.raw)
			tt.verify(t, kvs)
		})
	}
}

func TestDetermineOriginExplicitGlobalMatchingDefault(t *testing.T) {
	// When a key is explicitly set in global TOML to the same value as the
	// default, origin should be "global", not "default".
	rawGlobal := map[string]any{
		"max_workers": int64(4),
	}
	origin, ok := determineOrigin("max_workers", "4", "4", rawGlobal)
	if !ok {
		t.Fatal("expected key to be included in output")
	}
	if origin != "global" {
		t.Errorf("origin = %q, want %q", origin, "global")
	}
}

func TestFormatMapNilInterfaceKeys(t *testing.T) {
	// Map with interface keys where some are nil.
	// This previously panicked because compareKeys called Elem() on nil interfaces.
	m := map[any]string{
		nil:   "null-val",
		"abc": "string-val",
		42:    "int-val",
	}

	assertDeterministic(t, func() string {
		return formatMap(reflect.ValueOf(m))
	})

	// All entries must be present (nil interface key should not panic)
	result := formatMap(reflect.ValueOf(m))
	for _, val := range []string{"null-val", "string-val", "int-val"} {
		if !strings.Contains(result, val) {
			t.Errorf("result %q missing value %q", result, val)
		}
	}
	if !strings.Contains(result, "<nil>:null-val") {
		t.Errorf("result %q missing nil key entry", result)
	}
}

func TestFormatMapNaNFloatKeys(t *testing.T) {
	// Map with NaN float keys. Different NaN bit patterns should produce
	// deterministic ordering via bit-pattern comparison.
	nan1 := math.NaN()
	nan2 := math.Float64frombits(math.Float64bits(nan1) ^ 1) // different NaN payload

	m := map[float64]string{
		nan1: "first",
		nan2: "second",
		1.0:  "one",
	}

	assertDeterministic(t, func() string {
		return formatMap(reflect.ValueOf(m))
	})

	// All entries must be present
	result := formatMap(reflect.ValueOf(m))
	for _, val := range []string{"first", "second", "one"} {
		if !strings.Contains(result, val) {
			t.Errorf("result %q missing value %q", result, val)
		}
	}
}

func TestCompareKeysNilInterfaces(t *testing.T) {
	// Direct unit tests for compareKeys nil-interface handling.

	// Two nil interfaces are equal.
	var i1, i2 any
	v1 := reflect.ValueOf(&i1).Elem()
	v2 := reflect.ValueOf(&i2).Elem()
	if c := compareKeys(v1, v2); c != 0 {
		t.Errorf("compareKeys(nil, nil) = %d, want 0", c)
	}

	// nil < non-nil
	i2 = 42
	v2 = reflect.ValueOf(&i2).Elem()
	if c := compareKeys(v1, v2); c != -1 {
		t.Errorf("compareKeys(nil, 42) = %d, want -1", c)
	}

	// non-nil > nil
	if c := compareKeys(v2, v1); c != 1 {
		t.Errorf("compareKeys(42, nil) = %d, want 1", c)
	}
}
