package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	require.Equal(t, "127.0.0.1:7373", cfg.ServerAddr, "Expected ServerAddr '127.0.0.1:7373'")
	require.Equal(t, 4, cfg.MaxWorkers, "Expected MaxWorkers 4")
	require.Equal(t, "codex", cfg.DefaultAgent, "Expected DefaultAgent 'codex'")
	require.True(t, cfg.MouseEnabled, "Expected MouseEnabled to default to true")
}

func TestDataDir(t *testing.T) {
	t.Run("default uses home directory", func(t *testing.T) {
		t.Setenv("ROBOREV_DATA_DIR", "")

		dir := DataDir()
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".roborev")
		require.Equal(t, expected, dir, "Expected %s, got %s", expected, dir)
	})

	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv("ROBOREV_DATA_DIR", "/custom/data/dir")

		dir := DataDir()
		require.Equal(t, "/custom/data/dir", dir, "Expected /custom/data/dir, got %s", dir)
	})

	t.Run("GlobalConfigPath uses DataDir", func(t *testing.T) {
		testDir := filepath.Join(os.TempDir(), "roborev-test")
		t.Setenv("ROBOREV_DATA_DIR", testDir)

		path := GlobalConfigPath()
		expected := filepath.Join(testDir, "config.toml")
		require.Equal(t, expected, path, "Expected %s, got %s", expected, path)
	})
}

func TestResolveAgent(t *testing.T) {
	cfg := DefaultConfig()
	tmpDir := t.TempDir()

	agent := ResolveAgent("claude-code", tmpDir, cfg)
	require.Equal(t, "claude-code", agent, "Expected 'claude-code'")

	agent = ResolveAgent("", tmpDir, cfg)
	require.Equal(t, "codex", agent, "Expected 'codex' (from global)")

	writeRepoConfigStr(t, tmpDir, `agent = "claude-code"`)

	agent = ResolveAgent("", tmpDir, cfg)
	require.Equal(t, "claude-code", agent, "Expected 'claude-code' (from repo config)")

	agent = ResolveAgent("codex", tmpDir, cfg)
	require.Equal(t, "codex", agent, "Expected 'codex' (explicit)")
}

func TestSaveAndLoadGlobal(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.DefaultAgent = "claude-code"
	cfg.MaxWorkers = 8

	err := SaveGlobal(cfg)
	require.NoError(t, err, "SaveGlobal failed: %v")

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")
	assert.Equal(t, "claude-code", loaded.DefaultAgent, "Expected DefaultAgent 'claude-code'")
	assert.Equal(t, 8, loaded.MaxWorkers, "Expected MaxWorkers 8")

}

func TestSaveAndLoadGlobalAutoFilterBranch(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.AutoFilterBranch = true

	if err := SaveGlobal(cfg); err != nil {
		require.NoError(t, err, "SaveGlobal failed: %v")
	}

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.True(t, loaded.AutoFilterBranch, "AutoFilterBranch should be true after round-trip")

}

func TestLoadGlobalAutoFilterBranchFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("auto_filter_branch = true\n"), 0644); err != nil {
		require.NoError(t, err, "write config: %v")
	}

	cfg, err := LoadGlobalFrom(path)
	require.NoError(t, err, "LoadGlobalFrom failed: %v")

	assert.True(t, cfg.AutoFilterBranch, "AutoFilterBranch should be true when loaded from TOML")
}

func TestSaveAndLoadGlobalMouseEnabled(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.MouseEnabled = false

	if err := SaveGlobal(cfg); err != nil {
		require.NoError(t, err, "SaveGlobal failed: %v")
	}

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.False(t, loaded.MouseEnabled, "MouseEnabled should be false after round-trip")
}

func TestLoadGlobalMouseEnabledFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("mouse_enabled = false\n"), 0644); err != nil {
		require.NoError(t, err, "write config: %v")
	}

	cfg, err := LoadGlobalFrom(path)
	require.NoError(t, err, "LoadGlobalFrom failed: %v")

	assert.False(t, cfg.MouseEnabled, "MouseEnabled should be false when loaded from TOML")
}

func TestSaveAndLoadGlobalAutoFilterBranch(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.AutoFilterBranch = true

	if err := SaveGlobal(cfg); err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	loaded, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal failed: %v", err)
	}

	if !loaded.AutoFilterBranch {
		t.Error("AutoFilterBranch should be true after round-trip")
	}
}

func TestLoadGlobalAutoFilterBranchFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("auto_filter_branch = true\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadGlobalFrom(path)
	if err != nil {
		t.Fatalf("LoadGlobalFrom failed: %v", err)
	}

	if !cfg.AutoFilterBranch {
		t.Error("AutoFilterBranch should be true when loaded from TOML")
	}
}

func TestSaveAndLoadGlobalMouseEnabled(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.MouseEnabled = false

	if err := SaveGlobal(cfg); err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	loaded, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal failed: %v", err)
	}

	if loaded.MouseEnabled {
		t.Error("MouseEnabled should be false after round-trip")
	}
}

func TestLoadGlobalMouseEnabledFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("mouse_enabled = false\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadGlobalFrom(path)
	if err != nil {
		t.Fatalf("LoadGlobalFrom failed: %v", err)
	}

	if cfg.MouseEnabled {
		t.Error("MouseEnabled should be false when loaded from TOML")
	}
}

func TestLoadRepoConfigWithGuidelines(t *testing.T) {
	tmpDir := newTempRepo(t, `
agent = "claude-code"
review_guidelines = """
We are not doing database migrations because there are no production databases yet.
Prefer composition over inheritance.
All public APIs must have documentation comments.
"""
`)

	cfg, err := LoadRepoConfig(tmpDir)
	require.NoError(t, err, "LoadRepoConfig failed: %v")
	assert.NotNil(t, cfg, "Expected non-nil config")
	assert.Equal(t, "claude-code", cfg.Agent, "Expected agent 'claude-code', got '%s'", cfg.Agent)

	assert.Contains(t, cfg.ReviewGuidelines, "database migrations", "Expected guidelines to contain 'database migrations', got '%s'", cfg.ReviewGuidelines)
	assert.Contains(t, cfg.ReviewGuidelines, "composition over inheritance", "Expected guidelines to contain 'composition over inheritance'")

}

func TestLoadRepoConfigNoGuidelines(t *testing.T) {
	tmpDir := newTempRepo(t, `agent = "codex"`)

	cfg, err := LoadRepoConfig(tmpDir)
	require.NoError(t, err, "LoadRepoConfig failed: %v")
	assert.NotNil(t, cfg, "Expected non-nil config")

	if cfg.ReviewGuidelines != "" {
		assert.Empty(t, cfg.ReviewGuidelines, "Expected empty guidelines, got '%s'", cfg.ReviewGuidelines)
	}
}

func TestLoadRepoConfigMissing(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := LoadRepoConfig(tmpDir)
	require.NoError(t, err, "LoadRepoConfig failed: %v")

	assert.Nil(t, cfg, "Expected nil config when file doesn't exist")
}

func TestResolveJobTimeout(t *testing.T) {
	tests := []struct {
		name         string
		repoConfig   string
		globalConfig *Config
		want         int
	}{
		{
			name: "default when no config",
			want: 30,
		},
		{
			name:         "default when global config has zero",
			globalConfig: &Config{JobTimeoutMinutes: 0},
			want:         30,
		},
		{
			name:         "negative global config falls through to default",
			globalConfig: &Config{JobTimeoutMinutes: -10},
			want:         30,
		},
		{
			name:         "global config takes precedence over default",
			globalConfig: &Config{JobTimeoutMinutes: 45},
			want:         45,
		},
		{
			name:         "repo config takes precedence over global",
			repoConfig:   `job_timeout_minutes = 15`,
			globalConfig: &Config{JobTimeoutMinutes: 45},
			want:         15,
		},
		{
			name:         "repo config zero falls through to global",
			repoConfig:   `job_timeout_minutes = 0`,
			globalConfig: &Config{JobTimeoutMinutes: 45},
			want:         45,
		},
		{
			name:         "repo config negative falls through to global",
			repoConfig:   `job_timeout_minutes = -5`,
			globalConfig: &Config{JobTimeoutMinutes: 45},
			want:         45,
		},
		{
			name:         "repo config without timeout falls through to global",
			repoConfig:   `agent = "codex"`,
			globalConfig: &Config{JobTimeoutMinutes: 60},
			want:         60,
		},
		{
			name:         "malformed repo config falls through to global",
			repoConfig:   `this is not valid toml {{{`,
			globalConfig: &Config{JobTimeoutMinutes: 45},
			want:         45,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newTempRepo(t, tt.repoConfig)
			got := ResolveJobTimeout(tmpDir, tt.globalConfig)
			assert.Equal(t, tt.want, got, "unexpected condition")
		})
	}
}

func TestResolveReasoning(t *testing.T) {
	type resolverFunc func(explicit string, dir string) (string, error)

	runTests := func(t *testing.T, name string, fn resolverFunc, configKey, defaultVal, repoVal string) {
		t.Run(name, func(t *testing.T) {
			tests := []struct {
				testName   string
				explicit   string
				repoConfig string
				want       string
				wantErr    bool
			}{
				{"default when no config", "", "", defaultVal, false},
				{"repo config when explicit empty", "", fmt.Sprintf(`%s = "%s"`, configKey, repoVal), repoVal, false},
				{"explicit overrides repo config", "fast", fmt.Sprintf(`%s = "%s"`, configKey, repoVal), "fast", false},
				{"explicit normalization", "FAST", "", "fast", false},
				{"invalid explicit", "unknown", "", "", true},
				{"invalid repo config", "", fmt.Sprintf(`%s = "invalid"`, configKey), "", true},
			}

			for _, tt := range tests {
				t.Run(tt.testName, func(t *testing.T) {
					tmpDir := newTempRepo(t, tt.repoConfig)
					got, err := fn(tt.explicit, tmpDir)
					assert.Equal(t, tt.wantErr, (err != nil), "unexpected condition")
					assert.False(t, !tt.wantErr && got != tt.want, "unexpected condition")
				})
			}
		})
	}

	runTests(t, "Review", ResolveReviewReasoning, "review_reasoning", "thorough", "standard")
	runTests(t, "Refine", ResolveRefineReasoning, "refine_reasoning", "standard", "thorough")
	runTests(t, "Fix", ResolveFixReasoning, "fix_reasoning", "standard", "thorough")
}

func TestFixEmptyReasoningSelectsStandardAgent(t *testing.T) {

	tmpDir := t.TempDir()
	writeRepoConfig(t, tmpDir, M{
		"fix_agent":          "codex",
		"fix_agent_standard": "claude",
		"fix_agent_fast":     "gemini",
	})

	reasoning, err := ResolveFixReasoning("", tmpDir)
	require.NoError(t, err, "ResolveFixReasoning: %v")
	assert.Equal(t, "standard", reasoning, "expected default reasoning 'standard', got %q", reasoning)

	agent := ResolveAgentForWorkflow("", tmpDir, nil, "fix", reasoning)
	assert.Equal(t, "claude", agent, "expected fix_agent_standard 'claude', got %q", agent)

	model := ResolveModelForWorkflow("", tmpDir, nil, "fix", reasoning)
	if model != "" {
		assert.Empty(t, model, "expected empty model (none configured), got %q", model)
	}
}

func TestIsBranchExcluded(t *testing.T) {
	tests := []struct {
		name       string
		repoConfig string
		branch     string
		want       bool
	}{
		{
			name:   "no config file",
			branch: "main",
			want:   false,
		},
		{
			name:       "empty excluded_branches",
			repoConfig: `agent = "codex"`,
			branch:     "main",
			want:       false,
		},
		{
			name:       "branch is excluded (wip)",
			repoConfig: `excluded_branches = ["wip", "scratch", "test-branch"]`,
			branch:     "wip",
			want:       true,
		},
		{
			name:       "branch is excluded (scratch)",
			repoConfig: `excluded_branches = ["wip", "scratch", "test-branch"]`,
			branch:     "scratch",
			want:       true,
		},
		{
			name:       "branch is excluded (test-branch)",
			repoConfig: `excluded_branches = ["wip", "scratch", "test-branch"]`,
			branch:     "test-branch",
			want:       true,
		},
		{
			name:       "branch is not excluded",
			repoConfig: `excluded_branches = ["wip", "scratch"]`,
			branch:     "main",
			want:       false,
		},
		{
			name:       "branch is not excluded (feature/foo)",
			repoConfig: `excluded_branches = ["wip", "scratch"]`,
			branch:     "feature/foo",
			want:       false,
		},
		{
			name:       "exact match required (prefix mismatch)",
			repoConfig: `excluded_branches = ["wip"]`,
			branch:     "wip-feature",
			want:       false,
		},
		{
			name:       "exact match required (suffix mismatch)",
			repoConfig: `excluded_branches = ["wip"]`,
			branch:     "my-wip",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newTempRepo(t, tt.repoConfig)

			got := IsBranchExcluded(tmpDir, tt.branch)
			assert.Equal(t, tt.want, got, "unexpected condition")
		})
	}
}

func TestIsCommitMessageExcluded(t *testing.T) {
	tests := []struct {
		name       string
		repoConfig string
		message    string
		want       bool
	}{
		{
			name:    "no config file",
			message: "fix: update handler",
			want:    false,
		},
		{
			name:       "empty excluded_commit_patterns",
			repoConfig: `agent = "codex"`,
			message:    "fix: update handler",
			want:       false,
		},
		{
			name:       "message matches pattern",
			repoConfig: `excluded_commit_patterns = ["[skip review]"]`,
			message:    "wip: quick fix [skip review]",
			want:       true,
		},
		{
			name:       "message matches one of several patterns",
			repoConfig: `excluded_commit_patterns = ["[skip review]", "[wip]", "[no review]"]`,
			message:    "checkpoint [wip]",
			want:       true,
		},
		{
			name:       "message does not match",
			repoConfig: `excluded_commit_patterns = ["[skip review]", "[wip]"]`,
			message:    "feat: add new endpoint",
			want:       false,
		},
		{
			name:       "case insensitive match",
			repoConfig: `excluded_commit_patterns = ["[Skip Review]"]`,
			message:    "wip: quick fix [SKIP REVIEW]",
			want:       true,
		},
		{
			name:       "pattern in body not just subject",
			repoConfig: `excluded_commit_patterns = ["[skip review]"]`,
			message:    "feat: add feature\n\nsome details [skip review]",
			want:       true,
		},
		{
			name:       "empty pattern is ignored",
			repoConfig: `excluded_commit_patterns = [""]`,
			message:    "any commit message",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newTempRepo(t, tt.repoConfig)
			got := IsCommitMessageExcluded(tmpDir, tt.message)
			assert.Equal(t, tt.want, got, "unexpected condition")
		})
	}
}

func TestAllCommitMessagesExcluded(t *testing.T) {
	tests := []struct {
		name       string
		repoConfig string
		messages   []string
		want       bool
	}{
		{
			name:     "empty messages returns false",
			messages: nil,
			want:     false,
		},
		{
			name:       "all match",
			repoConfig: `excluded_commit_patterns = ["[wip]"]`,
			messages: []string{
				"[wip] checkpoint 1",
				"[wip] checkpoint 2",
			},
			want: true,
		},
		{
			name:       "one does not match",
			repoConfig: `excluded_commit_patterns = ["[wip]"]`,
			messages: []string{
				"[wip] checkpoint",
				"feat: real work",
			},
			want: false,
		},
		{
			name:     "no config file",
			messages: []string{"[wip] anything"},
			want:     false,
		},
		{
			name:       "no patterns configured",
			repoConfig: `agent = "codex"`,
			messages:   []string{"[wip] anything"},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newTempRepo(t, tt.repoConfig)
			got := AllCommitMessagesExcluded(tmpDir, tt.messages)
			assert.Equal(t, tt.want, got, "AllCommitMessagesExcluded() = %v, want %v", got, tt.want)
		})
	}
}

func TestIsCommitMessageExcluded(t *testing.T) {
	tests := []struct {
		name       string
		repoConfig string
		message    string
		want       bool
	}{
		{
			name:    "no config file",
			message: "fix: update handler",
			want:    false,
		},
		{
			name:       "empty excluded_commit_patterns",
			repoConfig: `agent = "codex"`,
			message:    "fix: update handler",
			want:       false,
		},
		{
			name:       "message matches pattern",
			repoConfig: `excluded_commit_patterns = ["[skip review]"]`,
			message:    "wip: quick fix [skip review]",
			want:       true,
		},
		{
			name:       "message matches one of several patterns",
			repoConfig: `excluded_commit_patterns = ["[skip review]", "[wip]", "[no review]"]`,
			message:    "checkpoint [wip]",
			want:       true,
		},
		{
			name:       "message does not match",
			repoConfig: `excluded_commit_patterns = ["[skip review]", "[wip]"]`,
			message:    "feat: add new endpoint",
			want:       false,
		},
		{
			name:       "case insensitive match",
			repoConfig: `excluded_commit_patterns = ["[Skip Review]"]`,
			message:    "wip: quick fix [SKIP REVIEW]",
			want:       true,
		},
		{
			name:       "pattern in body not just subject",
			repoConfig: `excluded_commit_patterns = ["[skip review]"]`,
			message:    "feat: add feature\n\nsome details [skip review]",
			want:       true,
		},
		{
			name:       "empty pattern is ignored",
			repoConfig: `excluded_commit_patterns = [""]`,
			message:    "any commit message",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newTempRepo(t, tt.repoConfig)
			got := IsCommitMessageExcluded(tmpDir, tt.message)
			if got != tt.want {
				t.Errorf("IsCommitMessageExcluded(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestAllCommitMessagesExcluded(t *testing.T) {
	tests := []struct {
		name       string
		repoConfig string
		messages   []string
		want       bool
	}{
		{
			name:     "empty messages returns false",
			messages: nil,
			want:     false,
		},
		{
			name:       "all match",
			repoConfig: `excluded_commit_patterns = ["[wip]"]`,
			messages: []string{
				"[wip] checkpoint 1",
				"[wip] checkpoint 2",
			},
			want: true,
		},
		{
			name:       "one does not match",
			repoConfig: `excluded_commit_patterns = ["[wip]"]`,
			messages: []string{
				"[wip] checkpoint",
				"feat: real work",
			},
			want: false,
		},
		{
			name:     "no config file",
			messages: []string{"[wip] anything"},
			want:     false,
		},
		{
			name:       "no patterns configured",
			repoConfig: `agent = "codex"`,
			messages:   []string{"[wip] anything"},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newTempRepo(t, tt.repoConfig)
			got := AllCommitMessagesExcluded(tmpDir, tt.messages)
			if got != tt.want {
				t.Errorf(
					"AllCommitMessagesExcluded() = %v, want %v",
					got, tt.want,
				)
			}
		})
	}
}

func TestSyncConfigPostgresURLExpanded(t *testing.T) {
	t.Run("empty URL returns empty", func(t *testing.T) {
		cfg := SyncConfig{}
		assert.Empty(t, cfg.PostgresURLExpanded())
	})

	t.Run("URL without env vars unchanged", func(t *testing.T) {
		cfg := SyncConfig{PostgresURL: "postgres://user:pass@localhost:5432/db"}
		assert.Equal(t, cfg.PostgresURL, cfg.PostgresURLExpanded())
	})

	t.Run("URL with env var is expanded", func(t *testing.T) {
		t.Setenv("TEST_PG_PASS", "secret123")

		cfg := SyncConfig{PostgresURL: "postgres://user:${TEST_PG_PASS}@localhost:5432/db"}
		expected := "postgres://user:secret123@localhost:5432/db"
		assert.Equal(t, expected, cfg.PostgresURLExpanded())
	})

	t.Run("missing env var becomes empty", func(t *testing.T) {
		t.Setenv("NONEXISTENT_VAR", "")
		cfg := SyncConfig{PostgresURL: "postgres://user:${NONEXISTENT_VAR}@localhost:5432/db"}
		expected := "postgres://user:@localhost:5432/db"
		assert.Equal(t, expected, cfg.PostgresURLExpanded())
	})
}

func TestSyncConfigGetRepoDisplayName(t *testing.T) {
	t.Run("nil receiver returns empty", func(t *testing.T) {
		var cfg *SyncConfig
		assert.Empty(t, cfg.GetRepoDisplayName("any"))
	})

	t.Run("nil map returns empty", func(t *testing.T) {
		cfg := &SyncConfig{}
		assert.Empty(t, cfg.GetRepoDisplayName("any"), "expected empty display name for missing key")
	})

	t.Run("missing key returns empty", func(t *testing.T) {
		cfg := &SyncConfig{
			RepoNames: map[string]string{
				"git@github.com:org/repo.git": "my-repo",
			},
		}
		assert.Empty(t, cfg.GetRepoDisplayName("unknown"))
	})

	t.Run("returns configured name", func(t *testing.T) {
		cfg := &SyncConfig{
			RepoNames: map[string]string{
				"git@github.com:org/repo.git": "my-custom-name",
			},
		}
		expected := "my-custom-name"
		assert.Equal(t, expected, cfg.GetRepoDisplayName("git@github.com:org/repo.git"))
	})
}

func TestSyncConfigValidate(t *testing.T) {
	t.Run("disabled returns no warnings", func(t *testing.T) {
		cfg := SyncConfig{Enabled: false}
		warnings := cfg.Validate()
		assert.Empty(t, warnings, "unexpected condition")
	})

	t.Run("enabled without URL warns", func(t *testing.T) {
		cfg := SyncConfig{Enabled: true, PostgresURL: ""}
		warnings := cfg.Validate()
		assert.Len(t, warnings, 1, "unexpected condition")
		assert.Contains(t, warnings[0], "postgres_url is not set", "unexpected condition")
	})

	t.Run("valid config no warnings", func(t *testing.T) {
		cfg := SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://user:pass@localhost:5432/db",
		}
		warnings := cfg.Validate()
		assert.Empty(t, warnings, "unexpected condition")
	})

	t.Run("unexpanded env var warns", func(t *testing.T) {
		t.Setenv("MISSING_VAR", "")
		cfg := SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://user:${MISSING_VAR}@localhost:5432/db",
		}
		warnings := cfg.Validate()
		assert.Len(t, warnings, 1, "unexpected condition")
		assert.Contains(t, warnings[0], "unexpanded", "unexpected condition")
	})

	t.Run("expanded env var no warning", func(t *testing.T) {
		t.Setenv("TEST_PG_PASS2", "secret")

		cfg := SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://user:${TEST_PG_PASS2}@localhost:5432/db",
		}
		warnings := cfg.Validate()
		assert.Empty(t, warnings, "unexpected condition")
	})
}

func TestLoadGlobalWithSyncConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
default_agent = "codex"

[sync]
enabled = true
postgres_url = "postgres://roborev:pass@localhost:5432/roborev"
interval = "10m"
machine_name = "test-machine"
connect_timeout = "10s"
`), 0644); err != nil {
		require.NoError(t, err, "Failed to write config: %v")
	}

	cfg, err := LoadGlobalFrom(configPath)
	require.NoError(t, err, "LoadGlobalFrom failed: %v")
	assert.True(t, cfg.Sync.Enabled, "Expected Sync.Enabled to be true")

	assert.Equal(t, "postgres://roborev:pass@localhost:5432/roborev", cfg.Sync.PostgresURL, "unexpected condition")
	assert.Equal(t, "10m", cfg.Sync.Interval, "unexpected condition")
	assert.Equal(t, "test-machine", cfg.Sync.MachineName, "unexpected condition")
	assert.Equal(t, "10s", cfg.Sync.ConnectTimeout, "unexpected condition")
}

func TestGetDisplayName(t *testing.T) {
	t.Run("no config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		name := GetDisplayName(tmpDir)
		assert.Empty(t, name, "unexpected condition")
	})

	t.Run("display_name not set", func(t *testing.T) {
		tmpDir := newTempRepo(t, `agent = "codex"`)
		name := GetDisplayName(tmpDir)
		assert.Empty(t, name, "unexpected condition")
	})

	t.Run("display_name is set", func(t *testing.T) {
		tmpDir := newTempRepo(t, `display_name = "My Cool Project"`)
		name := GetDisplayName(tmpDir)
		assert.Equal(t, "My Cool Project", name, "unexpected condition")
	})

	t.Run("display_name with other config", func(t *testing.T) {
		tmpDir := newTempRepo(t, `
agent = "claude-code"
display_name = "Backend Service"
excluded_branches = ["wip"]
`)
		name := GetDisplayName(tmpDir)
		assert.Equal(t, "Backend Service", name, "unexpected condition")
	})
}

func TestValidateRoborevID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid simple", "my-project", false},
		{"valid with dots", "my.project.name", false},
		{"valid with underscores", "my_project_name", false},
		{"valid with colons", "org:my-project", false},
		{"valid with slashes", "org/repo/name", false},
		{"valid with at", "user@host", false},
		{"valid URL-like", "github.com/user/repo", false},
		{"valid numeric start", "123project", false},
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"starts with dot", ".hidden", true},
		{"starts with dash", "-invalid", true},
		{"starts with underscore", "_invalid", true},
		{"contains spaces", "my project", true},
		{"contains newline", "my\nproject", true},
		{"too long", strings.Repeat("a", 257), true},
		{"max length", strings.Repeat("a", 256), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errMsg := ValidateRoborevID(tt.id)
			gotErr := errMsg != ""
			assert.Equal(t, tt.wantErr, gotErr, "unexpected condition")
		})
	}
}

func TestReadRoborevID(t *testing.T) {
	t.Run("file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		id, err := ReadRoborevID(tmpDir)
		require.NoError(t, err, "unexpected condition")
		assert.Empty(t, id, "unexpected condition")
	})

	t.Run("valid file", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte("my-project\n"), 0644); err != nil {
			require.NoError(t, err)
		}
		id, err := ReadRoborevID(tmpDir)
		require.NoError(t, err, "unexpected condition")
		assert.Equal(t, "my-project", id, "unexpected condition")
	})

	t.Run("valid file with whitespace", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte("  my-project  \n\n"), 0644); err != nil {
			require.NoError(t, err)
		}
		id, err := ReadRoborevID(tmpDir)
		require.NoError(t, err, "unexpected condition")
		assert.Equal(t, "my-project", id, "unexpected condition")
	})

	t.Run("invalid file content", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte(".invalid-start"), 0644); err != nil {
			require.NoError(t, err)
		}
		id, err := ReadRoborevID(tmpDir)
		require.Error(t, err, "Expected error for invalid content")

		if id != "" {
			assert.Empty(t, id, "Expected empty ID on error, got: %q", id)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte(""), 0644); err != nil {
			require.NoError(t, err)
		}
		id, err := ReadRoborevID(tmpDir)
		require.Error(t, err, "Expected error for empty file")

		if id != "" {
			assert.Empty(t, id, "Expected empty ID on error, got: %q", id)
		}
	})
}

func TestResolveRepoIdentity(t *testing.T) {
	t.Run("uses roborev-id when present", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte("my-custom-id"), 0644); err != nil {
			require.NoError(t, err)
		}

		mockRemote := func(repoPath, remoteName string) string {
			return "https://github.com/user/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		assert.Equal(t, "my-custom-id", id, "unexpected condition")
	})

	t.Run("falls back to git remote when no roborev-id", func(t *testing.T) {
		tmpDir := t.TempDir()

		mockRemote := func(repoPath, remoteName string) string {
			return "https://github.com/user/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		assert.Equal(t, "https://github.com/user/repo.git", id, "unexpected condition")
	})

	t.Run("falls back to local path when no remote", func(t *testing.T) {
		tmpDir := t.TempDir()

		mockRemote := func(repoPath, remoteName string) string {
			return ""
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		expected := "local://" + tmpDir
		assert.Equal(t, expected, id, "unexpected condition")
	})

	t.Run("uses default git.GetRemoteURL when getRemoteURL is nil", func(t *testing.T) {
		tmpDir := t.TempDir()

		execGit(t, tmpDir, "init")
		execGit(t, tmpDir, "remote", "add", "origin", "https://github.com/test/repo.git")

		id := ResolveRepoIdentity(tmpDir, nil)
		assert.Equal(t, "https://github.com/test/repo.git", id, "unexpected condition")
	})

	t.Run("falls back to local path when nil and no git remote", func(t *testing.T) {
		tmpDir := t.TempDir()

		id := ResolveRepoIdentity(tmpDir, nil)
		expected := "local://" + tmpDir
		assert.Equal(t, expected, id, "unexpected condition")
	})

	t.Run("skips invalid roborev-id and uses remote", func(t *testing.T) {
		tmpDir := t.TempDir()

		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte(".invalid"), 0644); err != nil {
			require.NoError(t, err)
		}

		mockRemote := func(repoPath, remoteName string) string {
			return "https://github.com/user/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		assert.Equal(t, "https://github.com/user/repo.git", id, "unexpected condition")
	})

	t.Run("strips credentials from remote URL", func(t *testing.T) {
		tmpDir := t.TempDir()

		mockRemote := func(repoPath, remoteName string) string {
			return "https://user:token@github.com/org/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		assert.Equal(t, "https://github.com/org/repo.git", id, "unexpected condition")
	})
}

func TestResolveModel(t *testing.T) {
	tests := []struct {
		name         string
		explicit     string
		repoConfig   string
		globalConfig *Config
		want         string
	}{
		{
			name:         "explicit model takes precedence",
			explicit:     "explicit-model",
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "explicit-model",
		},
		{
			name:         "explicit with whitespace is trimmed",
			explicit:     "  explicit-model  ",
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "explicit-model",
		},
		{
			name:         "empty explicit falls back to repo config",
			explicit:     "",
			repoConfig:   `model = "repo-model"`,
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "repo-model",
		},
		{
			name:       "repo config with whitespace is trimmed",
			repoConfig: `model = "  repo-model  "`,
			want:       "repo-model",
		},
		{
			name:         "no repo config falls back to global config",
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "global-model",
		},
		{
			name:         "global config with whitespace is trimmed",
			globalConfig: &Config{DefaultModel: "  global-model  "},
			want:         "global-model",
		},
		{
			name: "no config returns empty",
			want: "",
		},
		{
			name:         "empty global config returns empty",
			globalConfig: &Config{DefaultModel: ""},
			want:         "",
		},
		{
			name:         "whitespace-only explicit falls through to repo config",
			explicit:     "   ",
			repoConfig:   `model = "repo-model"`,
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "repo-model",
		},
		{
			name:         "whitespace-only repo config falls through to global",
			repoConfig:   `model = "   "`,
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "global-model",
		},
		{
			name:         "explicit overrides repo config",
			explicit:     "explicit-model",
			repoConfig:   `model = "repo-model"`,
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "explicit-model",
		},
		{
			name:         "malformed repo config falls through to global",
			repoConfig:   `this is not valid toml {{{`,
			globalConfig: &Config{DefaultModel: "global-model"},
			want:         "global-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newTempRepo(t, tt.repoConfig)
			got := ResolveModel(tt.explicit, tmpDir, tt.globalConfig)
			assert.Equal(t, tt.want, got, "unexpected condition")
		})
	}
}

func TestResolveMaxPromptSize(t *testing.T) {
	t.Run("default when no config", func(t *testing.T) {
		tmpDir := t.TempDir()
		size := ResolveMaxPromptSize(tmpDir, nil)
		assert.Equal(t, DefaultMaxPromptSize, size, "unexpected condition")
	})

	t.Run("default when global config has zero", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultMaxPromptSize: 0}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		assert.Equal(t, DefaultMaxPromptSize, size, "unexpected condition")
	})

	t.Run("global config takes precedence over default", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		assert.Equal(t, 500*1024, size, "unexpected condition")
	})

	t.Run("repo config takes precedence over global", func(t *testing.T) {
		tmpDir := newTempRepo(t, `max_prompt_size = 300000`)
		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		assert.Equal(t, 300000, size, "unexpected condition")
	})

	t.Run("repo config zero falls through to global", func(t *testing.T) {
		tmpDir := newTempRepo(t, `max_prompt_size = 0`)
		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		assert.Equal(t, 500*1024, size, "unexpected condition")
	})

	t.Run("repo config without max_prompt_size falls through to global", func(t *testing.T) {
		tmpDir := newTempRepo(t, `agent = "codex"`)
		cfg := &Config{DefaultMaxPromptSize: 600 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		assert.Equal(t, 600*1024, size, "unexpected condition")
	})

	t.Run("malformed repo config falls through to global", func(t *testing.T) {
		tmpDir := newTempRepo(t, `this is not valid toml {{{`)
		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		assert.Equal(t, 500*1024, size, "unexpected condition")
	})
}

func TestResolveAgentForWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		cli      string
		repo     map[string]string
		global   *Config
		workflow string
		level    string
		expect   string
	}{

		{"empty config", "", nil, nil, "review", "fast", "codex"},
		{"global default only", "", nil, &Config{DefaultAgent: "claude"}, "review", "fast", "claude"},

		{"global workflow > global default", "", nil, &Config{DefaultAgent: "codex", ReviewAgent: "claude"}, "review", "fast", "claude"},
		{"global level > global workflow", "", nil, &Config{ReviewAgent: "codex", ReviewAgentFast: "claude"}, "review", "fast", "claude"},
		{"global level ignored for wrong level", "", nil, &Config{ReviewAgent: "codex", ReviewAgentFast: "claude"}, "review", "thorough", "codex"},

		{"repo generic only", "", M{"agent": "claude"}, nil, "review", "fast", "claude"},
		{"repo workflow > repo generic", "", M{"agent": "codex", "review_agent": "claude"}, nil, "review", "fast", "claude"},
		{"repo level > repo workflow", "", M{"review_agent": "codex", "review_agent_fast": "claude"}, nil, "review", "fast", "claude"},

		{"repo generic > global level-specific", "", M{"agent": "claude"}, &Config{ReviewAgentFast: "gemini"}, "review", "fast", "claude"},
		{"repo generic > global workflow-specific", "", M{"agent": "claude"}, &Config{ReviewAgent: "gemini"}, "review", "fast", "claude"},
		{"repo workflow > global level-specific", "", M{"review_agent": "claude"}, &Config{ReviewAgentFast: "gemini"}, "review", "fast", "claude"},

		{"cli > repo level-specific", "droid", M{"review_agent_fast": "claude"}, nil, "review", "fast", "droid"},
		{"cli > everything", "droid", M{"review_agent_fast": "claude"}, &Config{ReviewAgentFast: "gemini"}, "review", "fast", "droid"},

		{"refine uses refine_agent not review_agent", "", M{"review_agent": "claude", "refine_agent": "gemini"}, nil, "refine", "fast", "gemini"},
		{"refine level-specific", "", M{"refine_agent": "codex", "refine_agent_fast": "claude"}, nil, "refine", "fast", "claude"},
		{"review config ignored for refine", "", M{"review_agent_fast": "claude"}, &Config{DefaultAgent: "codex"}, "refine", "fast", "codex"},

		{"fast config ignored for standard", "", M{"review_agent_fast": "claude", "review_agent": "codex"}, nil, "review", "standard", "codex"},
		{"standard config used for standard", "", M{"review_agent_standard": "claude"}, nil, "review", "standard", "claude"},
		{"thorough config used for thorough", "", M{"review_agent_thorough": "claude"}, nil, "review", "thorough", "claude"},

		{"repo workflow + global level (repo wins)", "", M{"review_agent": "claude"}, &Config{ReviewAgentFast: "gemini", ReviewAgentThorough: "droid"}, "review", "fast", "claude"},
		{"global fills gaps repo doesn't set", "", M{"agent": "codex"}, &Config{ReviewAgentFast: "claude"}, "review", "standard", "codex"},

		{"fix uses fix_agent", "", M{"fix_agent": "claude"}, nil, "fix", "fast", "claude"},
		{"fix level-specific", "", M{"fix_agent": "codex", "fix_agent_fast": "claude"}, nil, "fix", "fast", "claude"},
		{"fix falls back to generic agent", "", M{"agent": "claude"}, nil, "fix", "fast", "claude"},
		{"fix falls back to global fix_agent", "", nil, &Config{FixAgent: "claude"}, "fix", "fast", "claude"},
		{"fix global level-specific", "", nil, &Config{FixAgent: "codex", FixAgentFast: "claude"}, "fix", "fast", "claude"},
		{"fix standard level selects fix_agent_standard", "", M{"fix_agent_standard": "claude", "fix_agent": "codex"}, nil, "fix", "standard", "claude"},
		{"fix default reasoning (standard) selects level-specific", "", nil, &Config{FixAgentStandard: "claude", FixAgent: "codex"}, "fix", "standard", "claude"},
		{"fix isolated from review", "", M{"review_agent": "claude"}, &Config{DefaultAgent: "codex"}, "fix", "fast", "codex"},
		{"fix isolated from refine", "", M{"refine_agent": "claude"}, &Config{DefaultAgent: "codex"}, "fix", "fast", "codex"},

		{"design uses design_agent", "", M{"design_agent": "claude"}, nil, "design", "fast", "claude"},
		{"design level-specific", "", M{"design_agent": "codex", "design_agent_fast": "claude"}, nil, "design", "fast", "claude"},
		{"design falls back to generic agent", "", M{"agent": "claude"}, nil, "design", "fast", "claude"},
		{"design falls back to global design_agent", "", nil, &Config{DesignAgent: "claude"}, "design", "fast", "claude"},
		{"design global level-specific", "", nil, &Config{DesignAgent: "codex", DesignAgentThorough: "claude"}, "design", "thorough", "claude"},
		{"design isolated from review", "", M{"review_agent": "claude"}, &Config{DefaultAgent: "codex"}, "design", "fast", "codex"},
		{"design isolated from security", "", M{"security_agent": "claude"}, &Config{DefaultAgent: "codex"}, "design", "fast", "codex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeRepoConfig(t, tmpDir, tt.repo)
			got := ResolveAgentForWorkflow(tt.cli, tmpDir, tt.global, tt.workflow, tt.level)
			assert.Equal(t, got, tt.expect, "unexpected condition")
		})
	}
}

func TestResolveModelForWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		cli      string
		repo     map[string]string
		global   *Config
		workflow string
		level    string
		expect   string
	}{

		{"empty config", "", nil, nil, "review", "fast", ""},
		{"global default only", "", nil, &Config{DefaultModel: "gpt-4"}, "review", "fast", "gpt-4"},

		{"global workflow > global default", "", nil, &Config{DefaultModel: "gpt-4", ReviewModel: "claude-3"}, "review", "fast", "claude-3"},
		{"global level > global workflow", "", nil, &Config{ReviewModel: "gpt-4", ReviewModelFast: "claude-3"}, "review", "fast", "claude-3"},

		{"repo generic only", "", M{"model": "gpt-4"}, nil, "review", "fast", "gpt-4"},
		{"repo workflow > repo generic", "", M{"model": "gpt-4", "review_model": "claude-3"}, nil, "review", "fast", "claude-3"},
		{"repo level > repo workflow", "", M{"review_model": "gpt-4", "review_model_fast": "claude-3"}, nil, "review", "fast", "claude-3"},

		{"repo generic > global level-specific", "", M{"model": "gpt-4"}, &Config{ReviewModelFast: "claude-3"}, "review", "fast", "gpt-4"},

		{"cli > everything", "o1", M{"review_model_fast": "gpt-4"}, &Config{ReviewModelFast: "claude-3"}, "review", "fast", "o1"},

		{"refine uses refine_model", "", M{"review_model": "gpt-4", "refine_model": "claude-3"}, nil, "refine", "fast", "claude-3"},

		{"fix uses fix_model", "", M{"fix_model": "gpt-4"}, nil, "fix", "fast", "gpt-4"},
		{"fix level-specific model", "", M{"fix_model": "gpt-4", "fix_model_fast": "claude-3"}, nil, "fix", "fast", "claude-3"},
		{"fix falls back to generic model", "", M{"model": "gpt-4"}, nil, "fix", "fast", "gpt-4"},
		{"fix isolated from review model", "", M{"review_model": "gpt-4"}, nil, "fix", "fast", ""},

		{"design uses design_model", "", M{"design_model": "gpt-4"}, nil, "design", "fast", "gpt-4"},
		{"design level-specific model", "", M{"design_model": "gpt-4", "design_model_fast": "claude-3"}, nil, "design", "fast", "claude-3"},
		{"design falls back to generic model", "", M{"model": "gpt-4"}, nil, "design", "fast", "gpt-4"},
		{"design isolated from review model", "", M{"review_model": "gpt-4"}, nil, "design", "fast", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeRepoConfig(t, tmpDir, tt.repo)
			got := ResolveModelForWorkflow(tt.cli, tmpDir, tt.global, tt.workflow, tt.level)
			assert.Equal(t, got, tt.expect, "unexpected condition")
		})
	}
}

func TestResolveWorkflowModel(t *testing.T) {
	tests := []struct {
		name     string
		repo     map[string]string
		global   *Config
		workflow string
		level    string
		expect   string
	}{

		{
			"empty config",
			nil, nil,
			"fix", "fast", "",
		},

		{
			"skips global default_model",
			nil, &Config{DefaultModel: "gpt-5.4"},
			"fix", "fast", "",
		},

		{
			"skips repo generic model",
			M{"model": "gpt-5.4"}, nil,
			"fix", "fast", "",
		},

		{
			"global fix_model",
			nil, &Config{DefaultModel: "gpt-5.4", FixModel: "gemini-2.5-pro"},
			"fix", "fast", "gemini-2.5-pro",
		},

		{
			"global fix_model_fast",
			nil, &Config{DefaultModel: "gpt-5.4", FixModelFast: "gemini-2.5-flash"},
			"fix", "fast", "gemini-2.5-flash",
		},

		{
			"global level > global workflow",
			nil, &Config{FixModel: "gpt-4", FixModelFast: "claude-3"},
			"fix", "fast", "claude-3",
		},

		{
			"repo fix_model",
			M{"model": "gpt-5.4", "fix_model": "gemini-2.5-pro"}, nil,
			"fix", "fast", "gemini-2.5-pro",
		},

		{
			"repo fix_model_fast",
			M{"fix_model_fast": "claude-3"}, nil,
			"fix", "fast", "claude-3",
		},

		{
			"repo workflow > global workflow",
			M{"fix_model": "repo-model"},
			&Config{FixModel: "global-model"},
			"fix", "fast", "repo-model",
		},

		{
			"review workflow uses review_model",
			M{"fix_model": "fix-only", "review_model": "review-only"}, nil,
			"review", "standard", "review-only",
		},

		{
			"skips both generic defaults",
			M{"model": "repo-generic"},
			&Config{DefaultModel: "global-generic"},
			"fix", "fast", "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeRepoConfig(t, tmpDir, tt.repo)
			got := ResolveWorkflowModel(tmpDir, tt.global, tt.workflow, tt.level)
			assert.Equal(t, got, tt.expect, "unexpected condition")
		})
	}
}

func TestResolveWorkflowModel(t *testing.T) {
	tests := []struct {
		name     string
		repo     map[string]string
		global   *Config
		workflow string
		level    string
		expect   string
	}{
		// Empty config returns empty
		{
			"empty config",
			nil, nil,
			"fix", "fast", "",
		},
		// Skips generic global default_model
		{
			"skips global default_model",
			nil, &Config{DefaultModel: "gpt-5.4"},
			"fix", "fast", "",
		},
		// Skips generic repo model
		{
			"skips repo generic model",
			M{"model": "gpt-5.4"}, nil,
			"fix", "fast", "",
		},
		// Uses workflow-specific model from global config
		{
			"global fix_model",
			nil, &Config{DefaultModel: "gpt-5.4", FixModel: "gemini-2.5-pro"},
			"fix", "fast", "gemini-2.5-pro",
		},
		// Uses level-specific model from global config
		{
			"global fix_model_fast",
			nil, &Config{DefaultModel: "gpt-5.4", FixModelFast: "gemini-2.5-flash"},
			"fix", "fast", "gemini-2.5-flash",
		},
		// Level-specific beats workflow-level in global
		{
			"global level > global workflow",
			nil, &Config{FixModel: "gpt-4", FixModelFast: "claude-3"},
			"fix", "fast", "claude-3",
		},
		// Uses workflow-specific model from repo config
		{
			"repo fix_model",
			M{"model": "gpt-5.4", "fix_model": "gemini-2.5-pro"}, nil,
			"fix", "fast", "gemini-2.5-pro",
		},
		// Uses level-specific model from repo config
		{
			"repo fix_model_fast",
			M{"fix_model_fast": "claude-3"}, nil,
			"fix", "fast", "claude-3",
		},
		// Repo beats global for workflow-specific
		{
			"repo workflow > global workflow",
			M{"fix_model": "repo-model"},
			&Config{FixModel: "global-model"},
			"fix", "fast", "repo-model",
		},
		// Review workflow isolation
		{
			"review workflow uses review_model",
			M{"fix_model": "fix-only", "review_model": "review-only"}, nil,
			"review", "standard", "review-only",
		},
		// Skips both global default_model and repo generic model
		{
			"skips both generic defaults",
			M{"model": "repo-generic"},
			&Config{DefaultModel: "global-generic"},
			"fix", "fast", "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeRepoConfig(t, tmpDir, tt.repo)
			got := ResolveWorkflowModel(tmpDir, tt.global, tt.workflow, tt.level)
			if got != tt.expect {
				t.Errorf("got %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestResolveBackupAgentForWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		repo     map[string]string
		global   *Config
		workflow string
		expect   string
	}{

		{"empty config", nil, nil, "review", ""},
		{"only primary agent configured", M{"review_agent": "claude"}, nil, "review", ""},

		{"global backup only", nil, &Config{ReviewBackupAgent: "test"}, "review", "test"},
		{"global backup for refine", nil, &Config{RefineBackupAgent: "claude"}, "refine", "claude"},
		{"global backup for fix", nil, &Config{FixBackupAgent: "codex"}, "fix", "codex"},
		{"global backup for security", nil, &Config{SecurityBackupAgent: "gemini"}, "security", "gemini"},
		{"global backup for design", nil, &Config{DesignBackupAgent: "droid"}, "design", "droid"},

		{"repo overrides global", M{"review_backup_agent": "repo-test"}, &Config{ReviewBackupAgent: "global-test"}, "review", "repo-test"},
		{"repo backup only", M{"review_backup_agent": "test"}, nil, "review", "test"},

		{"review backup doesn't affect refine", M{"review_backup_agent": "claude"}, nil, "refine", ""},
		{"each workflow has own backup", M{"review_backup_agent": "claude", "refine_backup_agent": "codex"}, nil, "review", "claude"},
		{"each workflow has own backup - refine", M{"review_backup_agent": "claude", "refine_backup_agent": "codex"}, nil, "refine", "codex"},

		{"unknown workflow", M{"review_backup_agent": "test"}, nil, "unknown", ""},

		{"no level variants recognized", M{"review_backup_agent_fast": "claude"}, nil, "review", ""},
		{"backup agent doesn't use levels", M{"review_backup_agent": "claude"}, nil, "review", "claude"},

		{"global default_backup_agent", nil, &Config{DefaultBackupAgent: "test"}, "review", "test"},
		{"global default_backup_agent for any workflow", nil, &Config{DefaultBackupAgent: "test"}, "fix", "test"},
		{"global workflow-specific overrides default", nil, &Config{DefaultBackupAgent: "test", ReviewBackupAgent: "claude"}, "review", "claude"},
		{"global default used when workflow not set", nil, &Config{DefaultBackupAgent: "test", ReviewBackupAgent: "claude"}, "fix", "test"},
		{"repo backup_agent generic", M{"backup_agent": "repo-fallback"}, nil, "review", "repo-fallback"},
		{"repo backup_agent generic for any workflow", M{"backup_agent": "repo-fallback"}, nil, "refine", "repo-fallback"},
		{"repo workflow-specific overrides repo generic", M{"backup_agent": "generic", "review_backup_agent": "specific"}, nil, "review", "specific"},
		{"repo generic overrides global workflow-specific", M{"backup_agent": "repo"}, &Config{ReviewBackupAgent: "global"}, "review", "repo"},
		{"repo generic overrides global default", M{"backup_agent": "repo"}, &Config{DefaultBackupAgent: "global"}, "review", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			repoDir := t.TempDir()

			if tt.repo != nil {
				writeRepoConfig(t, repoDir, tt.repo)
			}

			result := ResolveBackupAgentForWorkflow(repoDir, tt.global, tt.workflow)

			assert.Equal(t, result, tt.expect, "unexpected condition")
		})
	}
}

func TestResolveBackupModelForWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		repo     map[string]string
		global   *Config
		workflow string
		expect   string
	}{

		{"empty config", nil, nil, "review", ""},
		{"only backup agent configured", M{"review_backup_agent": "claude"}, nil, "review", ""},

		{"global backup model only", nil, &Config{ReviewBackupModel: "gpt-4"}, "review", "gpt-4"},
		{"global backup model for refine", nil, &Config{RefineBackupModel: "claude-3"}, "refine", "claude-3"},
		{"global backup model for fix", nil, &Config{FixBackupModel: "o3-mini"}, "fix", "o3-mini"},
		{"global backup model for security", nil, &Config{SecurityBackupModel: "gpt-4"}, "security", "gpt-4"},
		{"global backup model for design", nil, &Config{DesignBackupModel: "claude-3"}, "design", "claude-3"},

		{"repo overrides global", M{"review_backup_model": "repo-model"}, &Config{ReviewBackupModel: "global-model"}, "review", "repo-model"},
		{"repo backup model only", M{"review_backup_model": "gpt-4"}, nil, "review", "gpt-4"},

		{"review backup model doesn't affect refine", M{"review_backup_model": "gpt-4"}, nil, "refine", ""},
		{"each workflow has own backup model", M{"review_backup_model": "gpt-4", "refine_backup_model": "claude-3"}, nil, "review", "gpt-4"},
		{"each workflow has own backup model - refine", M{"review_backup_model": "gpt-4", "refine_backup_model": "claude-3"}, nil, "refine", "claude-3"},

		{"unknown workflow", M{"review_backup_model": "gpt-4"}, nil, "unknown", ""},

		{"global default_backup_model", nil, &Config{DefaultBackupModel: "gpt-4"}, "review", "gpt-4"},
		{"global default_backup_model for any workflow", nil, &Config{DefaultBackupModel: "gpt-4"}, "fix", "gpt-4"},
		{"global workflow-specific overrides default", nil, &Config{DefaultBackupModel: "gpt-4", ReviewBackupModel: "claude-3"}, "review", "claude-3"},
		{"global default used when workflow not set", nil, &Config{DefaultBackupModel: "gpt-4", ReviewBackupModel: "claude-3"}, "fix", "gpt-4"},
		{"repo backup_model generic", M{"backup_model": "repo-model"}, nil, "review", "repo-model"},
		{"repo backup_model generic for any workflow", M{"backup_model": "repo-model"}, nil, "refine", "repo-model"},
		{"repo workflow-specific overrides repo generic", M{"backup_model": "generic", "review_backup_model": "specific"}, nil, "review", "specific"},
		{"repo generic overrides global workflow-specific", M{"backup_model": "repo"}, &Config{ReviewBackupModel: "global"}, "review", "repo"},
		{"repo generic overrides global default", M{"backup_model": "repo"}, &Config{DefaultBackupModel: "global"}, "review", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir := t.TempDir()
			if tt.repo != nil {
				writeRepoConfig(t, repoDir, tt.repo)
			}
			result := ResolveBackupModelForWorkflow(repoDir, tt.global, tt.workflow)
			assert.Equal(t, result, tt.expect, "unexpected condition")
		})
	}
}

func TestResolvedReviewTypes(t *testing.T) {
	t.Run("uses configured types", func(t *testing.T) {
		ci := CIConfig{ReviewTypes: []string{"security", "review"}}
		got := ci.ResolvedReviewTypes()
		assert.False(t, len(got) != 2 || got[0] != "security" || got[1] != "review", "unexpected condition")
	})

	t.Run("defaults to security", func(t *testing.T) {
		ci := CIConfig{}
		got := ci.ResolvedReviewTypes()
		assert.False(t, len(got) != 1 || got[0] != "security", "unexpected condition")
	})
}

func TestResolvedAgents(t *testing.T) {
	t.Run("uses configured agents", func(t *testing.T) {
		ci := CIConfig{Agents: []string{"codex", "gemini"}}
		got := ci.ResolvedAgents()
		assert.False(t, len(got) != 2 || got[0] != "codex" || got[1] != "gemini", "unexpected condition")
	})

	t.Run("defaults to auto-detect", func(t *testing.T) {
		ci := CIConfig{}
		got := ci.ResolvedAgents()
		assert.False(t, len(got) != 1 || got[0] != "", "unexpected condition")
	})
}

func TestResolvedMaxRepos(t *testing.T) {
	tests := []struct {
		name     string
		maxRepos int
		want     int
	}{
		{"default when zero", 0, 100},
		{"default when negative", -5, 100},
		{"custom value", 50, 50},
		{"custom large value", 500, 500},
		{"value of 1", 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ci := CIConfig{MaxRepos: tt.maxRepos}
			got := ci.ResolvedMaxRepos()
			assert.Equal(t, tt.want, got, "unexpected condition")
		})
	}
}

func TestCIConfigNewFields(t *testing.T) {
	t.Run("parses exclude_repos and max_repos", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.toml")
		if err := os.WriteFile(configPath, []byte(`
[ci]
enabled = true
repos = ["myorg/*", "other/repo"]
exclude_repos = ["myorg/archived-*", "myorg/internal-*"]
max_repos = 50
`), 0644); err != nil {
			require.NoError(t, err)
		}

		cfg, err := LoadGlobalFrom(configPath)
		require.NoError(t, err, "LoadGlobalFrom: %v")

		assert.Len(t, cfg.CI.Repos, 2, "unexpected condition")
		assert.Len(t, cfg.CI.ExcludeRepos, 2, "unexpected condition")
		assert.Equal(t, 50, cfg.CI.MaxRepos, "unexpected condition")
		assert.Equal(t, 50, cfg.CI.ResolvedMaxRepos(), "unexpected condition")
	})
}

func TestNormalizeMinSeverity(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"critical", "critical", false},
		{"high", "high", false},
		{"medium", "medium", false},
		{"low", "low", false},
		{"CRITICAL", "critical", false},
		{"  High  ", "high", false},
		{"Medium", "medium", false},
		{"invalid", "", true},
		{"thorough", "", true},
	}

	for _, tt := range tests {
		t.Run("input_"+tt.input, func(t *testing.T) {
			got, err := NormalizeMinSeverity(tt.input)
			if (err != nil) != tt.wantErr {
				require.Errorf(t, err, "NormalizeMinSeverity(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			assert.Equal(t, tt.want, got, "NormalizeMinSeverity(%q) = %q, want %q", tt.input, got, tt.want)

		})
	}
}

func TestRepoCIConfig(t *testing.T) {
	t.Run("parses agents and review_types", func(t *testing.T) {
		tmpDir := newTempRepo(t, `
agent = "codex"

[ci]
agents = ["gemini", "claude"]
review_types = ["security", "review"]
reasoning = "standard"
`)
		cfg, err := LoadRepoConfig(tmpDir)
		require.NoError(t, err, "LoadRepoConfig: %v")

		assert.False(t, len(cfg.CI.Agents) != 2 || cfg.CI.Agents[0] != "gemini" || cfg.CI.Agents[1] != "claude", "unexpected condition")
		assert.False(t, len(cfg.CI.ReviewTypes) != 2 || cfg.CI.ReviewTypes[0] != "security" || cfg.CI.ReviewTypes[1] != "review", "unexpected condition")
		assert.Equal(t, "standard", cfg.CI.Reasoning, "unexpected condition")
	})

	t.Run("empty CI section", func(t *testing.T) {
		tmpDir := newTempRepo(t, `agent = "codex"`)
		cfg, err := LoadRepoConfig(tmpDir)
		require.NoError(t, err, "LoadRepoConfig: %v")

		assert.Empty(t, cfg.CI.Agents, "unexpected condition")
		assert.Empty(t, cfg.CI.ReviewTypes, "unexpected condition")
		assert.Empty(t, cfg.CI.Reasoning, "unexpected condition")
	})
}

func TestInstallationIDForOwner(t *testing.T) {
	t.Run("map lookup", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations: map[string]int64{
				"wesm":        111111,
				"roborev-dev": 222222,
			},
			GitHubAppInstallationID: 999999,
		}}
		assert.Equal(t, int64(111111), ci.InstallationIDForOwner("wesm"))
		assert.Equal(t, int64(222222), ci.InstallationIDForOwner("roborev-dev"))
	})

	t.Run("falls back to singular", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations:  map[string]int64{"wesm": 111111},
			GitHubAppInstallationID: 999999,
		}}
		assert.Equal(t, int64(999999), ci.InstallationIDForOwner("unknown-org"))
	})

	t.Run("zero when unset", func(t *testing.T) {
		ci := CIConfig{}
		assert.Equal(t, int64(0), ci.InstallationIDForOwner("wesm"))
	})

	t.Run("zero mapped value falls back to singular", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations:  map[string]int64{"wesm": 0},
			GitHubAppInstallationID: 999999,
		}}
		assert.Equal(t, int64(999999), ci.InstallationIDForOwner("wesm"))
	})

	t.Run("case-insensitive lookup after normalization", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations: map[string]int64{"Wesm": 111111, "RoboRev-Dev": 222222},
		}}
		if err := ci.NormalizeInstallations(); err != nil {
			require.NoError(t, err, "NormalizeInstallations: %v")
		}
		assert.Equal(t, int64(111111), ci.InstallationIDForOwner("wesm"))
		assert.Equal(t, int64(111111), ci.InstallationIDForOwner("WESM"))
		assert.Equal(t, int64(222222), ci.InstallationIDForOwner("roborev-dev"))
	})
}

func TestNormalizeInstallations(t *testing.T) {
	t.Run("lowercases keys", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations: map[string]int64{"Wesm": 111111, "RoboRev-Dev": 222222},
		}}
		if err := ci.NormalizeInstallations(); err != nil {
			require.NoError(t, err, "NormalizeInstallations: %v")
		}
		_, ok := ci.GitHubAppInstallations["wesm"]
		assert.True(t, ok, "expected lowercase key 'wesm' after normalization")
		_, ok = ci.GitHubAppInstallations["roborev-dev"]
		assert.True(t, ok, "expected lowercase key 'roborev-dev' after normalization")
		_, ok = ci.GitHubAppInstallations["Wesm"]
		assert.False(t, ok, "original mixed-case key 'Wesm' should not exist after normalization")
	})

	t.Run("noop on nil map", func(t *testing.T) {
		ci := CIConfig{}
		if err := ci.NormalizeInstallations(); err != nil {
			require.NoError(t, err, "NormalizeInstallations on nil map: %v")
		}
	})

	t.Run("case-colliding keys returns error", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations: map[string]int64{"wesm": 111111, "Wesm": 222222},
		}}
		err := ci.NormalizeInstallations()
		require.Error(t, err, "expected error for case-colliding keys")

		if !strings.Contains(err.Error(), "case-colliding") {
			assert.Contains(t, err.Error(), "case-colliding", "expected case-colliding error, got: %v", err)
		}
	})
}

func TestLoadGlobalFrom_NormalizesInstallations(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[ci]
github_app_id = 12345
github_app_private_key = "~/.roborev/app.pem"

[ci.github_app_installations]
Wesm = 111111
RoboRev-Dev = 222222
`), 0644); err != nil {
		require.NoError(t, err)
	}

	cfg, err := LoadGlobalFrom(configPath)
	require.NoError(t, err, "LoadGlobalFrom: %v")

	if got := cfg.CI.InstallationIDForOwner("wesm"); got != 111111 {
		assert.Equal(t, 111111, got, "got %d, want 111111 for normalized 'wesm'", got)
	}
	if got := cfg.CI.InstallationIDForOwner("roborev-dev"); got != 222222 {
		assert.Equal(t, 222222, got, "got %d, want 222222 for normalized 'wesm'", got)
	}
}

func TestGitHubAppConfigured_MultiInstall(t *testing.T) {
	t.Run("configured with map only", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:            12345,
			GitHubAppPrivateKey:    "~/.roborev/app.pem",
			GitHubAppInstallations: map[string]int64{"wesm": 111111},
		}}
		assert.True(t, ci.GitHubAppConfigured(), "expected GitHubAppConfigured() == true with map only")

	})

	t.Run("configured with singular only", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:             12345,
			GitHubAppPrivateKey:     "~/.roborev/app.pem",
			GitHubAppInstallationID: 111111,
		}}
		assert.True(t, ci.GitHubAppConfigured(), "expected GitHubAppConfigured() == true with singular only")

	})

	t.Run("not configured without any installation", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:         12345,
			GitHubAppPrivateKey: "~/.roborev/app.pem",
		}}
		if ci.GitHubAppConfigured() {
			assert.False(t, ci.GitHubAppConfigured(), "expected GitHubAppConfigured() == false without any installation ID")
		}
	})

	t.Run("not configured without private key", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:             12345,
			GitHubAppInstallationID: 111111,
		}}
		if ci.GitHubAppConfigured() {
			assert.False(t, ci.GitHubAppConfigured(), "expected GitHubAppConfigured() == false without private key")
		}
	})
}

func TestGitHubAppPrivateKeyResolved_TildeExpansion(t *testing.T) {

	dir := t.TempDir()
	pemFile := filepath.Join(dir, "assertion_failed.pem")
	pemContent := "-----BEGIN RSA PRIVATE KEY-----\nfakekey\n-----END RSA PRIVATE KEY-----"
	if err := os.WriteFile(pemFile, []byte(pemContent), 0600); err != nil {
		require.NoError(t, err)
	}

	t.Run("inline PEM returned directly", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: pemContent}}
		got, err := ci.GitHubAppPrivateKeyResolved()
		require.NoError(t, err)
		assert.Equal(t, got, pemContent, "unexpected condition")
	})

	t.Run("absolute path reads file", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: pemFile}}
		got, err := ci.GitHubAppPrivateKeyResolved()
		require.NoError(t, err)
		assert.Equal(t, got, pemContent, "unexpected condition")
	})

	t.Run("tilde path expands to home", func(t *testing.T) {

		fakeHome := t.TempDir()
		t.Setenv("HOME", fakeHome)
		t.Setenv("USERPROFILE", fakeHome)

		fakePem := filepath.Join(fakeHome, ".roborev", "test.pem")
		if err := os.MkdirAll(filepath.Dir(fakePem), 0700); err != nil {
			require.NoError(t, err)
		}
		if err := os.WriteFile(fakePem, []byte(pemContent), 0600); err != nil {
			require.NoError(t, err)
		}

		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: "~/.roborev/test.pem"}}
		got, err := ci.GitHubAppPrivateKeyResolved()
		require.NoError(t, err, "tilde expansion failed: %v")

		assert.Equal(t, got, pemContent, "unexpected condition")
	})

	t.Run("empty after expansion returns error", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: ""}}
		_, err := ci.GitHubAppPrivateKeyResolved()
		require.Error(t, err, "expected error for empty key")

	})
}

func TestStripURLCredentials(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "HTTPS URL with user:password",
			input:    "https://user:password@github.com/org/repo.git",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "HTTPS URL with token only",
			input:    "https://token@github.com/org/repo.git",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "HTTPS URL without credentials",
			input:    "https://github.com/org/repo.git",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "SSH URL unchanged",
			input:    "git@github.com:org/repo.git",
			expected: "git@github.com:org/repo.git",
		},
		{
			name:     "HTTP URL with credentials",
			input:    "http://user:pass@gitlab.example.com/project.git",
			expected: "http://gitlab.example.com/project.git",
		},
		{
			name:     "URL with only username",
			input:    "https://user@bitbucket.org/team/repo.git",
			expected: "https://bitbucket.org/team/repo.git",
		},
		{
			name:     "Local path unchanged",
			input:    "/path/to/repo",
			expected: "/path/to/repo",
		},
		{
			name:     "File URL unchanged",
			input:    "file:///path/to/repo",
			expected: "file:///path/to/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripURLCredentials(tt.input)
			assert.Equal(t, tt.expected, result, "unexpected condition")
		})
	}
}

func TestHideClosedDefaultPersistence(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := &Config{HideClosedByDefault: true}
	err := SaveGlobal(cfg)
	require.NoError(t, err, "SaveGlobal failed: %v")

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.True(t, loaded.HideClosedByDefault, "Expected HideClosedByDefault to be true")

	loaded.HideClosedByDefault = false
	err = SaveGlobal(loaded)
	require.NoError(t, err, "SaveGlobal failed: %v")

	reloaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.False(t, reloaded.HideClosedByDefault, "Expected HideClosedByDefault to be false")

}

func TestAdvancedTasksEnabledPersistence(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := &Config{}
	cfg.Advanced.TasksEnabled = true
	if err := SaveGlobal(cfg); err != nil {
		require.NoError(t, err, "SaveGlobal failed: %v")
	}

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.True(t, loaded.Advanced.TasksEnabled, "Expected Advanced.TasksEnabled to be true")

	loaded.Advanced.TasksEnabled = false
	if err := SaveGlobal(loaded); err != nil {
		require.NoError(t, err, "SaveGlobal failed: %v")
	}

	reloaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.False(t, reloaded.Advanced.TasksEnabled, "Expected Advanced.TasksEnabled to be false")

}

func TestAdvancedTasksEnabledPersistence(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := &Config{}
	cfg.Advanced.TasksEnabled = true
	if err := SaveGlobal(cfg); err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	loaded, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal failed: %v", err)
	}
	if !loaded.Advanced.TasksEnabled {
		t.Error("Expected Advanced.TasksEnabled to be true")
	}

	loaded.Advanced.TasksEnabled = false
	if err := SaveGlobal(loaded); err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	reloaded, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal failed: %v", err)
	}
	if reloaded.Advanced.TasksEnabled {
		t.Error("Expected Advanced.TasksEnabled to be false")
	}
}

func TestSaveGlobalWritesDocumentedSettings(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.DefaultAgent = "claude-code"
	cfg.HideClosedByDefault = true
	cfg.MouseEnabled = false
	cfg.Advanced.TasksEnabled = true

	if err := SaveGlobal(cfg); err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	data, err := os.ReadFile(GlobalConfigPath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	got := string(data)

	for _, want := range []string{
		"default_agent = 'claude-code'",
		"# Hide closed reviews by default in the TUI queue.\nhide_closed_by_default = true",
		"# Enable mouse support in the TUI.\nmouse_enabled = false",
		"[advanced]\n# Enable the advanced Tasks workflow in the TUI.\ntasks_enabled = true",
		"max_workers = 4",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("saved config missing documented setting %q:\n%s", want, got)
		}
	}
}

func TestHideAddressedDeprecatedMigration(t *testing.T) {
	testenv.SetDataDir(t)

	cfgPath := GlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		require.NoError(t, err, "mkdir failed: %v")
	}
	if err := os.WriteFile(cfgPath, []byte("hide_addressed_by_default = true\n"), 0644); err != nil {
		require.NoError(t, err, "WriteFile failed: %v")
	}

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.True(t, loaded.HideClosedByDefault, "Expected deprecated hide_addressed_by_default to migrate to HideClosedByDefault")

	if loaded.HideAddressedByDefault {
		assert.Empty(t, loaded.HideAddressedByDefault, "Expected HideAddressedByDefault to be cleared after migration")
	}
}

func TestHideAddressedDoesNotOverrideExplicitNewKey(t *testing.T) {
	testenv.SetDataDir(t)

	cfgPath := GlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		require.NoError(t, err, "mkdir failed: %v")
	}
	content := "hide_addressed_by_default = true\nhide_closed_by_default = false\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		require.NoError(t, err, "WriteFile failed: %v")
	}

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")

	assert.False(t, loaded.HideClosedByDefault, "Deprecated key should not override explicit hide_closed_by_default = false")
	if loaded.HideAddressedByDefault {
		assert.Empty(t, loaded.HideAddressedByDefault, "Expected HideAddressedByDefault to be cleared after migration")
	}
}

func TestIsDefaultReviewType(t *testing.T) {
	defaults := []string{"", "default", "general", "review"}
	for _, rt := range defaults {
		assert.True(t, IsDefaultReviewType(rt), "expected %q to be default review type", rt)
	}
	nonDefaults := []string{"security", "design", "bogus"}
	for _, rt := range nonDefaults {
		assert.False(t, IsDefaultReviewType(rt), "expected %q to NOT be default review type", rt)
	}
}

func TestLoadRepoConfigFromRef(t *testing.T) {

	dir := t.TempDir()
	execGit(t, dir, "init")
	execGit(t, dir, "config", "user.email", "test@test.com")
	execGit(t, dir, "config", "user.name", "Test")

	configContent := `review_guidelines = "Use descriptive variable names."` + "\n"
	writeTestFile(t, dir, ".roborev.toml", configContent)
	execGit(t, dir, "add", ".roborev.toml")
	execGit(t, dir, "commit", "-m", "add config")

	sha := execGit(t, dir, "rev-parse", "HEAD")

	t.Run("loads config from ref", func(t *testing.T) {
		cfg, err := LoadRepoConfigFromRef(dir, sha)
		require.NoError(t, err, "LoadRepoConfigFromRef: %v")
		assert.NotNil(t, cfg, "expected non-nil config")
		assert.Equal(t, "Use descriptive variable names.", cfg.ReviewGuidelines, "got %q, cfg.ReviewGuidelines", cfg.ReviewGuidelines)

	})

	t.Run("returns nil for nonexistent ref", func(t *testing.T) {
		cfg, err := LoadRepoConfigFromRef(dir, "0000000000000000000000000000000000000000")
		require.NoError(t, err)

		assert.Nil(t, cfg, "expected nil config for nonexistent ref")
	})

	t.Run("returns nil when file missing from ref", func(t *testing.T) {

		execGit(t, dir, "rm", ".roborev.toml")
		execGit(t, dir, "commit", "-m", "remove config")
		headSHA := execGit(t, dir, "rev-parse", "HEAD")

		cfg, err := LoadRepoConfigFromRef(dir, headSHA)
		require.NoError(t, err)

		assert.Nil(t, cfg, "expected nil config when file removed from ref")
	})
}

func TestValidateReviewTypes(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{
			name:  "valid types pass through",
			input: []string{"default", "security", "design"},
			want:  []string{"default", "security", "design"},
		},
		{
			name:  "alias review canonicalizes",
			input: []string{"review"},
			want:  []string{"default"},
		},
		{
			name:  "alias general canonicalizes",
			input: []string{"general"},
			want:  []string{"default"},
		},
		{
			name:  "duplicates removed",
			input: []string{"default", "review", "general"},
			want:  []string{"default"},
		},
		{
			name:  "mixed valid with dedup",
			input: []string{"security", "review", "security"},
			want:  []string{"security", "default"},
		},
		{
			name:    "invalid type returns error",
			input:   []string{"typo"},
			wantErr: true,
		},
		{
			name:    "empty string returns error",
			input:   []string{""},
			wantErr: true,
		},
		{
			name:    "invalid among valid",
			input:   []string{"security", "bogus"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateReviewTypes(tt.input)
			if tt.wantErr {
				require.Error(t, err, "expected error")

				return
			}
			require.NoError(t, err)

			if len(got) != len(tt.want) {
				assert.Len(t, got, len(tt.want), "got %v, want %v", got, tt.want)
			}
			for i := range got {
				assert.Equal(t, tt.want[i], got[i], "got[%d] = %q, want %q", i, got[i], tt.want[i])

			}
		})
	}
}

func TestResolvedReviewMatrix(t *testing.T) {
	t.Run("falls back to cross-product", func(t *testing.T) {
		ci := CIConfig{
			Agents:      []string{"codex", "gemini"},
			ReviewTypes: []string{"security", "default"},
		}
		matrix := ci.ResolvedReviewMatrix()
		if len(matrix) != 4 {
			assert.Len(t, matrix, 4, "got %d entries, want 4, len(matrix)")
		}

		want := []AgentReviewType{
			{"codex", "security"},
			{"gemini", "security"},
			{"codex", "default"},
			{"gemini", "default"},
		}
		for i, got := range matrix {
			assert.Equal(t, want[i], got, "matrix[%d] = %v, want %v", i, got, want[i])

		}
	})

	t.Run("uses reviews map when set", func(t *testing.T) {
		ci := CIConfig{
			Reviews: map[string][]string{
				"codex":  {"security", "default"},
				"gemini": {"default"},
			},

			Agents:      []string{"ignored"},
			ReviewTypes: []string{"ignored"},
		}
		matrix := ci.ResolvedReviewMatrix()
		if len(matrix) != 3 {
			assert.Len(t, matrix, 3, "got %d entries, want 3, len(matrix)")
		}

		sort.Slice(matrix, func(i, j int) bool {
			if matrix[i].Agent != matrix[j].Agent {
				return matrix[i].Agent < matrix[j].Agent
			}
			return matrix[i].ReviewType < matrix[j].ReviewType
		})
		want := []AgentReviewType{
			{"codex", "default"},
			{"codex", "security"},
			{"gemini", "default"},
		}
		for i, got := range matrix {
			assert.Equal(t, want[i], got, "matrix[%d] = %v, want %v", i, got, want[i])

		}
	})

	t.Run("defaults when empty", func(t *testing.T) {
		ci := CIConfig{}
		matrix := ci.ResolvedReviewMatrix()

		if len(matrix) != 1 {
			assert.Len(t, matrix, 1, "got %d entries, want 1, len(matrix)")
		}
		if matrix[0].Agent != "" || matrix[0].ReviewType != "security" {
			assert.Equal(t, AgentReviewType{Agent: "", ReviewType: "security"}, matrix[0], "got %v, want {\"\" security}", matrix[0])
		}
	})
}

func TestRepoCIConfigResolvedReviewMatrix(t *testing.T) {
	t.Run("returns nil when Reviews not set", func(t *testing.T) {
		ci := RepoCIConfig{
			Agents:      []string{"codex"},
			ReviewTypes: []string{"security"},
		}
		if matrix := ci.ResolvedReviewMatrix(); matrix != nil {
			assert.Nil(t, matrix, "expected nil, got %v", matrix)
		}
	})

	t.Run("returns matrix from Reviews", func(t *testing.T) {
		ci := RepoCIConfig{
			Reviews: map[string][]string{
				"codex": {"security"},
			},
		}
		matrix := ci.ResolvedReviewMatrix()
		if len(matrix) != 1 {
			assert.Len(t, matrix, 1, "got %d entries, want 1, len(matrix)")
		}
		if matrix[0].Agent != "codex" ||
			matrix[0].ReviewType != "security" {
			assert.Equal(t, AgentReviewType{Agent: "codex", ReviewType: "security"}, matrix[0], "got %v, matrix[0]")
		}
	})

	t.Run("empty Reviews disables reviews", func(t *testing.T) {
		ci := RepoCIConfig{
			Reviews: map[string][]string{},
		}
		matrix := ci.ResolvedReviewMatrix()
		assert.NotNil(t, matrix, "expected non-nil empty slice, got nil")

		if len(matrix) != 0 {
			assert.Empty(t, matrix, "expected 0 entries, got %d", len(matrix))
		}
	})

	t.Run("Reviews with all empty lists disables reviews", func(t *testing.T) {
		ci := RepoCIConfig{
			Reviews: map[string][]string{"codex": {}},
		}
		matrix := ci.ResolvedReviewMatrix()
		assert.NotNil(t, matrix, "expected non-nil empty slice, got nil")

		if len(matrix) != 0 {
			assert.Empty(t, matrix, "expected 0 entries, got %d", len(matrix))
		}
	})
}

func TestResolvePostCommitReview(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{"no config file", "", "commit"},
		{"field not set", `agent = "claude-code"`, "commit"},
		{"explicit commit", `post_commit_review = "commit"`, "commit"},
		{"branch", `post_commit_review = "branch"`, "branch"},
		{"unknown value falls back to commit", `post_commit_review = "auto"`, "commit"},
		{"empty string falls back to commit", `post_commit_review = ""`, "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dir string
			if tt.config == "" {
				dir = t.TempDir()
			} else {
				dir = newTempRepo(t, tt.config)
			}
			got := ResolvePostCommitReview(dir)
			assert.Equal(t, tt.want, got, "unexpected condition")
		})
	}
}

func TestResolvedThrottleInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"empty defaults to 1h", "", time.Hour},
		{"zero disables", "0", 0},
		{"valid duration", "30m", 30 * time.Minute},
		{"valid seconds", "3600s", time.Hour},
		{"invalid falls back to 1h", "not-a-duration", time.Hour},
		{"negative falls back to 1h", "-5m", time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ci := CIConfig{ThrottleInterval: tt.value}
			got := ci.ResolvedThrottleInterval()
			assert.Equal(t, tt.want, got, "ResolvedThrottleInterval() = %v, want %v", got, tt.want)

		})
	}
}

func TestCIConfigReviewsFieldParsing(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[ci]
enabled = true
throttle_interval = "30m"

[ci.reviews]
codex = ["security", "default"]
gemini = ["default"]
`), 0644); err != nil {
		require.NoError(t, err)
	}

	cfg, err := LoadGlobalFrom(configPath)
	require.NoError(t, err, "LoadGlobalFrom: %v")
	assert.Equal(t, "30m", cfg.CI.ThrottleInterval, "got throttle_interval %q, want %q", cfg.CI.ThrottleInterval, "30m")

	if len(cfg.CI.Reviews) != 2 {
		assert.Len(t, cfg.CI.Reviews, 2, "assertion failed, got %d review entries, want 2", len(cfg.CI.Reviews))
	}
	codexTypes := cfg.CI.Reviews["codex"]
	if len(codexTypes) != 2 ||
		codexTypes[0] != "security" ||
		codexTypes[1] != "default" {
		assert.Equal(t, []string{"security", "default"}, codexTypes, "got codex types %v, codexTypes")
	}
}

func TestIsThrottleBypassed(t *testing.T) {
	ci := CIConfig{
		ThrottleBypassUsers: []string{"wesm", "mariusvniekerk"},
	}

	tests := []struct {
		login string
		want  bool
	}{
		{"wesm", true},
		{"mariusvniekerk", true},
		{"Wesm", true},
		{"WESM", true},
		{"MariusVNiekerk", true},
		{"someone-else", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			got := ci.IsThrottleBypassed(tt.login)
			assert.Equal(t, tt.want, got, "IsThrottleBypassed(%q) = %v, want %v", tt.login, got, tt.want)

		})
	}

	t.Run("empty list", func(t *testing.T) {
		empty := CIConfig{}
		if empty.IsThrottleBypassed("wesm") {
			assert.False(t, empty.IsThrottleBypassed("wesm"), "expected false for empty bypass list")
		}
	})
}

func TestRepoCIConfigReviewsFieldParsing(t *testing.T) {
	tmpDir := newTempRepo(t, `
agent = "codex"

[ci]
agents = ["codex"]

[ci.reviews]
codex = ["security"]
gemini = ["default"]
`)
	cfg, err := LoadRepoConfig(tmpDir)
	require.NoError(t, err, "LoadRepoConfig: %v")

	if len(cfg.CI.Reviews) != 2 {
		assert.Len(t, cfg.CI.Reviews, 2, "assertion failed, got %d review entries, want 2", len(cfg.CI.Reviews))
	}
}

func TestResolveMinSeverity(t *testing.T) {
	type resolverFunc func(explicit string, dir string) (string, error)

	runTests := func(
		t *testing.T, name string, fn resolverFunc,
		configKey string,
	) {
		t.Run(name, func(t *testing.T) {
			tests := []struct {
				testName   string
				explicit   string
				repoConfig string
				want       string
				wantErr    bool
			}{
				{
					"default when no config",
					"", "", "", false,
				},
				{
					"repo config when explicit empty",
					"",
					fmt.Sprintf(`%s = "high"`, configKey),
					"high", false,
				},
				{
					"explicit overrides repo config",
					"critical",
					fmt.Sprintf(`%s = "medium"`, configKey),
					"critical", false,
				},
				{
					"explicit normalization",
					"HIGH", "", "high", false,
				},
				{
					"invalid explicit",
					"bogus", "", "", true,
				},
				{
					"invalid repo config",
					"",
					fmt.Sprintf(`%s = "invalid"`, configKey),
					"", true,
				},
			}

			for _, tt := range tests {
				t.Run(tt.testName, func(t *testing.T) {
					tmpDir := newTempRepo(t, tt.repoConfig)
					got, err := fn(tt.explicit, tmpDir)
					if (err != nil) != tt.wantErr {
						t.Errorf(
							"error = %v, wantErr %v",
							err, tt.wantErr,
						)
					}
					if !tt.wantErr && got != tt.want {
						t.Errorf("got %q, want %q", got, tt.want)
					}
				})
			}
		})
	}

	runTests(t, "Fix", ResolveFixMinSeverity, "fix_min_severity")
	runTests(t, "Refine", ResolveRefineMinSeverity, "refine_min_severity")
}

func TestSeverityInstruction(t *testing.T) {
	tests := []struct {
		name        string
		minSeverity string
		wantEmpty   bool
		wantSubstr  string
	}{
		{
			name:        "empty returns empty",
			minSeverity: "",
			wantEmpty:   true,
		},
		{
			name:        "low returns empty",
			minSeverity: "low",
			wantEmpty:   true,
		},
		{
			name:        "unknown returns empty",
			minSeverity: "bogus",
			wantEmpty:   true,
		},
		{
			name:        "medium",
			minSeverity: "medium",
			wantSubstr:  "Medium, High, and Critical",
		},
		{
			name:        "high",
			minSeverity: "high",
			wantSubstr:  "High and Critical",
		},
		{
			name:        "critical",
			minSeverity: "critical",
			wantSubstr:  "Only include Critical",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeverityInstruction(tt.minSeverity)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf(
						"SeverityInstruction(%q) = %q, want empty",
						tt.minSeverity, got,
					)
				}
				return
			}
			if got == "" {
				t.Fatalf(
					"SeverityInstruction(%q) = empty, want non-empty",
					tt.minSeverity,
				)
			}
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf(
					"SeverityInstruction(%q) = %q, missing %q",
					tt.minSeverity, got, tt.wantSubstr,
				)
			}
			if !strings.Contains(got, SeverityThresholdMarker) {
				t.Errorf(
					"SeverityInstruction(%q) missing threshold marker %q",
					tt.minSeverity, SeverityThresholdMarker,
				)
			}
		})
	}
}
