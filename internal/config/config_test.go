package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ServerAddr != "127.0.0.1:7373" {
		t.Errorf("Expected ServerAddr '127.0.0.1:7373', got '%s'", cfg.ServerAddr)
	}
	if cfg.MaxWorkers != 4 {
		t.Errorf("Expected MaxWorkers 4, got %d", cfg.MaxWorkers)
	}
	if cfg.DefaultAgent != "codex" {
		t.Errorf("Expected DefaultAgent 'codex', got '%s'", cfg.DefaultAgent)
	}
}

func TestDataDir(t *testing.T) {
	t.Run("default uses home directory", func(t *testing.T) {
		// Clear env var to test default
		origEnv := os.Getenv("ROBOREV_DATA_DIR")
		os.Unsetenv("ROBOREV_DATA_DIR")
		defer func() {
			if origEnv != "" {
				os.Setenv("ROBOREV_DATA_DIR", origEnv)
			}
		}()

		dir := DataDir()
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".roborev")
		if dir != expected {
			t.Errorf("Expected %s, got %s", expected, dir)
		}
	})

	t.Run("env var overrides default", func(t *testing.T) {
		origEnv := os.Getenv("ROBOREV_DATA_DIR")
		os.Setenv("ROBOREV_DATA_DIR", "/custom/data/dir")
		defer func() {
			if origEnv != "" {
				os.Setenv("ROBOREV_DATA_DIR", origEnv)
			} else {
				os.Unsetenv("ROBOREV_DATA_DIR")
			}
		}()

		dir := DataDir()
		if dir != "/custom/data/dir" {
			t.Errorf("Expected /custom/data/dir, got %s", dir)
		}
	})

	t.Run("GlobalConfigPath uses DataDir", func(t *testing.T) {
		origEnv := os.Getenv("ROBOREV_DATA_DIR")
		testDir := filepath.Join(os.TempDir(), "roborev-test")
		os.Setenv("ROBOREV_DATA_DIR", testDir)
		defer func() {
			if origEnv != "" {
				os.Setenv("ROBOREV_DATA_DIR", origEnv)
			} else {
				os.Unsetenv("ROBOREV_DATA_DIR")
			}
		}()

		path := GlobalConfigPath()
		expected := filepath.Join(testDir, "config.toml")
		if path != expected {
			t.Errorf("Expected %s, got %s", expected, path)
		}
	})
}

func TestResolveAgent(t *testing.T) {
	cfg := DefaultConfig()
	tmpDir := t.TempDir()

	// Test explicit agent takes precedence
	agent := ResolveAgent("claude-code", tmpDir, cfg)
	if agent != "claude-code" {
		t.Errorf("Expected 'claude-code', got '%s'", agent)
	}

	// Test empty explicit falls back to global config
	agent = ResolveAgent("", tmpDir, cfg)
	if agent != "codex" {
		t.Errorf("Expected 'codex' (from global), got '%s'", agent)
	}

	// Test per-repo config
	repoConfig := filepath.Join(tmpDir, ".roborev.toml")
	os.WriteFile(repoConfig, []byte(`agent = "claude-code"`), 0644)

	agent = ResolveAgent("", tmpDir, cfg)
	if agent != "claude-code" {
		t.Errorf("Expected 'claude-code' (from repo config), got '%s'", agent)
	}

	// Explicit still takes precedence over repo config
	agent = ResolveAgent("codex", tmpDir, cfg)
	if agent != "codex" {
		t.Errorf("Expected 'codex' (explicit), got '%s'", agent)
	}
}

func TestSaveAndLoadGlobal(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.DefaultAgent = "claude-code"
	cfg.MaxWorkers = 8

	err := SaveGlobal(cfg)
	if err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	loaded, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal failed: %v", err)
	}

	if loaded.DefaultAgent != "claude-code" {
		t.Errorf("Expected DefaultAgent 'claude-code', got '%s'", loaded.DefaultAgent)
	}
	if loaded.MaxWorkers != 8 {
		t.Errorf("Expected MaxWorkers 8, got %d", loaded.MaxWorkers)
	}
}

func TestLoadRepoConfigWithGuidelines(t *testing.T) {
	tmpDir := t.TempDir()

	// Test loading config with review guidelines as multi-line string
	configContent := `
agent = "claude-code"
review_guidelines = """
We are not doing database migrations because there are no production databases yet.
Prefer composition over inheritance.
All public APIs must have documentation comments.
"""
`
	repoConfig := filepath.Join(tmpDir, ".roborev.toml")
	if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadRepoConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadRepoConfig failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("Expected non-nil config")
	}

	if cfg.Agent != "claude-code" {
		t.Errorf("Expected agent 'claude-code', got '%s'", cfg.Agent)
	}

	if !strings.Contains(cfg.ReviewGuidelines, "database migrations") {
		t.Errorf("Expected guidelines to contain 'database migrations', got '%s'", cfg.ReviewGuidelines)
	}

	if !strings.Contains(cfg.ReviewGuidelines, "composition over inheritance") {
		t.Errorf("Expected guidelines to contain 'composition over inheritance'")
	}
}

func TestLoadRepoConfigNoGuidelines(t *testing.T) {
	tmpDir := t.TempDir()

	// Test loading config without review guidelines (backwards compatibility)
	configContent := `agent = "codex"`
	repoConfig := filepath.Join(tmpDir, ".roborev.toml")
	if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadRepoConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadRepoConfig failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("Expected non-nil config")
	}

	if cfg.ReviewGuidelines != "" {
		t.Errorf("Expected empty guidelines, got '%s'", cfg.ReviewGuidelines)
	}
}

func TestLoadRepoConfigMissing(t *testing.T) {
	tmpDir := t.TempDir()

	// Test loading from directory with no config file
	cfg, err := LoadRepoConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadRepoConfig failed: %v", err)
	}

	if cfg != nil {
		t.Error("Expected nil config when file doesn't exist")
	}
}

func TestResolveJobTimeout(t *testing.T) {
	t.Run("default when no config", func(t *testing.T) {
		tmpDir := t.TempDir()
		timeout := ResolveJobTimeout(tmpDir, nil)
		if timeout != 30 {
			t.Errorf("Expected default timeout 30, got %d", timeout)
		}
	})

	t.Run("default when global config has zero", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{JobTimeoutMinutes: 0}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 30 {
			t.Errorf("Expected default timeout 30 when global is 0, got %d", timeout)
		}
	})

	t.Run("negative global config falls through to default", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{JobTimeoutMinutes: -10}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 30 {
			t.Errorf("Expected default timeout 30 when global is negative, got %d", timeout)
		}
	})

	t.Run("global config takes precedence over default", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{JobTimeoutMinutes: 45}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 45 {
			t.Errorf("Expected timeout 45 from global config, got %d", timeout)
		}
	})

	t.Run("repo config takes precedence over global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`job_timeout_minutes = 15`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{JobTimeoutMinutes: 45}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 15 {
			t.Errorf("Expected timeout 15 from repo config, got %d", timeout)
		}
	})

	t.Run("repo config zero falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`job_timeout_minutes = 0`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{JobTimeoutMinutes: 45}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 45 {
			t.Errorf("Expected timeout 45 from global (repo is 0), got %d", timeout)
		}
	})

	t.Run("repo config negative falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`job_timeout_minutes = -5`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{JobTimeoutMinutes: 45}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 45 {
			t.Errorf("Expected timeout 45 from global (repo is negative), got %d", timeout)
		}
	})

	t.Run("repo config without timeout falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`agent = "codex"`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{JobTimeoutMinutes: 60}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 60 {
			t.Errorf("Expected timeout 60 from global (repo has no timeout), got %d", timeout)
		}
	})

	t.Run("malformed repo config falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`this is not valid toml {{{`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{JobTimeoutMinutes: 45}
		timeout := ResolveJobTimeout(tmpDir, cfg)
		if timeout != 45 {
			t.Errorf("Expected timeout 45 from global (repo config malformed), got %d", timeout)
		}
	})
}

func TestResolveReviewReasoning(t *testing.T) {
	t.Run("default when no config", func(t *testing.T) {
		tmpDir := t.TempDir()
		reasoning, err := ResolveReviewReasoning("", tmpDir)
		if err != nil {
			t.Fatalf("ResolveReviewReasoning failed: %v", err)
		}
		if reasoning != "thorough" {
			t.Errorf("Expected default 'thorough', got '%s'", reasoning)
		}
	})

	t.Run("repo config when explicit empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`review_reasoning = "standard"`), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		reasoning, err := ResolveReviewReasoning("", tmpDir)
		if err != nil {
			t.Fatalf("ResolveReviewReasoning failed: %v", err)
		}
		if reasoning != "standard" {
			t.Errorf("Expected 'standard' from repo config, got '%s'", reasoning)
		}
	})

	t.Run("explicit overrides repo config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`review_reasoning = "standard"`), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		reasoning, err := ResolveReviewReasoning("FAST", tmpDir)
		if err != nil {
			t.Fatalf("ResolveReviewReasoning failed: %v", err)
		}
		if reasoning != "fast" {
			t.Errorf("Expected 'fast' from explicit override, got '%s'", reasoning)
		}
	})

	t.Run("invalid explicit", func(t *testing.T) {
		tmpDir := t.TempDir()
		_, err := ResolveReviewReasoning("unknown", tmpDir)
		if err == nil {
			t.Fatal("Expected error for invalid reasoning")
		}
	})

	t.Run("invalid repo config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`review_reasoning = "invalid"`), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		_, err := ResolveReviewReasoning("", tmpDir)
		if err == nil {
			t.Fatal("Expected error for invalid repo reasoning")
		}
	})
}

func TestResolveRefineReasoning(t *testing.T) {
	t.Run("default when no config", func(t *testing.T) {
		tmpDir := t.TempDir()
		reasoning, err := ResolveRefineReasoning("", tmpDir)
		if err != nil {
			t.Fatalf("ResolveRefineReasoning failed: %v", err)
		}
		if reasoning != "standard" {
			t.Errorf("Expected default 'standard', got '%s'", reasoning)
		}
	})

	t.Run("repo config when explicit empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`refine_reasoning = "thorough"`), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		reasoning, err := ResolveRefineReasoning("", tmpDir)
		if err != nil {
			t.Fatalf("ResolveRefineReasoning failed: %v", err)
		}
		if reasoning != "thorough" {
			t.Errorf("Expected 'thorough' from repo config, got '%s'", reasoning)
		}
	})

	t.Run("explicit overrides repo config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`refine_reasoning = "thorough"`), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		reasoning, err := ResolveRefineReasoning("standard", tmpDir)
		if err != nil {
			t.Fatalf("ResolveRefineReasoning failed: %v", err)
		}
		if reasoning != "standard" {
			t.Errorf("Expected 'standard' from explicit override, got '%s'", reasoning)
		}
	})

	t.Run("invalid explicit", func(t *testing.T) {
		tmpDir := t.TempDir()
		_, err := ResolveRefineReasoning("nope", tmpDir)
		if err == nil {
			t.Fatal("Expected error for invalid reasoning")
		}
	})

	t.Run("invalid repo config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`refine_reasoning = "invalid"`), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		_, err := ResolveRefineReasoning("", tmpDir)
		if err == nil {
			t.Fatal("Expected error for invalid repo reasoning")
		}
	})
}

func TestIsBranchExcluded(t *testing.T) {
	t.Run("no config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		if IsBranchExcluded(tmpDir, "main") {
			t.Error("Expected branch not excluded when no config file")
		}
	})

	t.Run("empty excluded_branches", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`agent = "codex"`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		if IsBranchExcluded(tmpDir, "main") {
			t.Error("Expected branch not excluded when excluded_branches not set")
		}
	})

	t.Run("branch is excluded", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		configContent := `
excluded_branches = ["wip", "scratch", "test-branch"]
`
		if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		if !IsBranchExcluded(tmpDir, "wip") {
			t.Error("Expected 'wip' branch to be excluded")
		}
		if !IsBranchExcluded(tmpDir, "scratch") {
			t.Error("Expected 'scratch' branch to be excluded")
		}
		if !IsBranchExcluded(tmpDir, "test-branch") {
			t.Error("Expected 'test-branch' branch to be excluded")
		}
	})

	t.Run("branch is not excluded", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		configContent := `
excluded_branches = ["wip", "scratch"]
`
		if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		if IsBranchExcluded(tmpDir, "main") {
			t.Error("Expected 'main' branch not to be excluded")
		}
		if IsBranchExcluded(tmpDir, "feature/foo") {
			t.Error("Expected 'feature/foo' branch not to be excluded")
		}
	})

	t.Run("exact match required", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		configContent := `
excluded_branches = ["wip"]
`
		if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		// Partial matches should not be excluded
		if IsBranchExcluded(tmpDir, "wip-feature") {
			t.Error("Expected 'wip-feature' not to be excluded (not exact match)")
		}
		if IsBranchExcluded(tmpDir, "my-wip") {
			t.Error("Expected 'my-wip' not to be excluded (not exact match)")
		}
	})
}

func TestSyncConfigPostgresURLExpanded(t *testing.T) {
	t.Run("empty URL returns empty", func(t *testing.T) {
		cfg := SyncConfig{}
		if got := cfg.PostgresURLExpanded(); got != "" {
			t.Errorf("Expected empty string, got %q", got)
		}
	})

	t.Run("URL without env vars unchanged", func(t *testing.T) {
		cfg := SyncConfig{PostgresURL: "postgres://user:pass@localhost:5432/db"}
		if got := cfg.PostgresURLExpanded(); got != cfg.PostgresURL {
			t.Errorf("Expected %q, got %q", cfg.PostgresURL, got)
		}
	})

	t.Run("URL with env var is expanded", func(t *testing.T) {
		os.Setenv("TEST_PG_PASS", "secret123")
		defer os.Unsetenv("TEST_PG_PASS")

		cfg := SyncConfig{PostgresURL: "postgres://user:${TEST_PG_PASS}@localhost:5432/db"}
		expected := "postgres://user:secret123@localhost:5432/db"
		if got := cfg.PostgresURLExpanded(); got != expected {
			t.Errorf("Expected %q, got %q", expected, got)
		}
	})

	t.Run("missing env var becomes empty", func(t *testing.T) {
		os.Unsetenv("NONEXISTENT_VAR")
		cfg := SyncConfig{PostgresURL: "postgres://user:${NONEXISTENT_VAR}@localhost:5432/db"}
		expected := "postgres://user:@localhost:5432/db"
		if got := cfg.PostgresURLExpanded(); got != expected {
			t.Errorf("Expected %q, got %q", expected, got)
		}
	})
}

func TestSyncConfigGetRepoDisplayName(t *testing.T) {
	t.Run("nil receiver returns empty", func(t *testing.T) {
		var cfg *SyncConfig
		if got := cfg.GetRepoDisplayName("any"); got != "" {
			t.Errorf("Expected empty string for nil receiver, got %q", got)
		}
	})

	t.Run("nil map returns empty", func(t *testing.T) {
		cfg := &SyncConfig{}
		if got := cfg.GetRepoDisplayName("any"); got != "" {
			t.Errorf("Expected empty string for nil map, got %q", got)
		}
	})

	t.Run("missing key returns empty", func(t *testing.T) {
		cfg := &SyncConfig{
			RepoNames: map[string]string{
				"git@github.com:org/repo.git": "my-repo",
			},
		}
		if got := cfg.GetRepoDisplayName("unknown"); got != "" {
			t.Errorf("Expected empty string for missing key, got %q", got)
		}
	})

	t.Run("returns configured name", func(t *testing.T) {
		cfg := &SyncConfig{
			RepoNames: map[string]string{
				"git@github.com:org/repo.git": "my-custom-name",
			},
		}
		expected := "my-custom-name"
		if got := cfg.GetRepoDisplayName("git@github.com:org/repo.git"); got != expected {
			t.Errorf("Expected %q, got %q", expected, got)
		}
	})
}

func TestSyncConfigValidate(t *testing.T) {
	t.Run("disabled returns no warnings", func(t *testing.T) {
		cfg := SyncConfig{Enabled: false}
		warnings := cfg.Validate()
		if len(warnings) != 0 {
			t.Errorf("Expected no warnings when disabled, got %v", warnings)
		}
	})

	t.Run("enabled without URL warns", func(t *testing.T) {
		cfg := SyncConfig{Enabled: true, PostgresURL: ""}
		warnings := cfg.Validate()
		if len(warnings) != 1 {
			t.Errorf("Expected 1 warning, got %d", len(warnings))
		}
		if !strings.Contains(warnings[0], "postgres_url is not set") {
			t.Errorf("Expected warning about missing URL, got %q", warnings[0])
		}
	})

	t.Run("valid config no warnings", func(t *testing.T) {
		cfg := SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://user:pass@localhost:5432/db",
		}
		warnings := cfg.Validate()
		if len(warnings) != 0 {
			t.Errorf("Expected no warnings for valid config, got %v", warnings)
		}
	})

	t.Run("unexpanded env var warns", func(t *testing.T) {
		os.Unsetenv("MISSING_VAR")
		cfg := SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://user:${MISSING_VAR}@localhost:5432/db",
		}
		warnings := cfg.Validate()
		if len(warnings) != 1 {
			t.Errorf("Expected 1 warning for unexpanded var, got %d: %v", len(warnings), warnings)
		}
		if !strings.Contains(warnings[0], "unexpanded") {
			t.Errorf("Expected warning about unexpanded vars, got %q", warnings[0])
		}
	})

	t.Run("expanded env var no warning", func(t *testing.T) {
		os.Setenv("TEST_PG_PASS2", "secret")
		defer os.Unsetenv("TEST_PG_PASS2")

		cfg := SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://user:${TEST_PG_PASS2}@localhost:5432/db",
		}
		warnings := cfg.Validate()
		if len(warnings) != 0 {
			t.Errorf("Expected no warnings when env var is set, got %v", warnings)
		}
	})
}

func TestLoadGlobalWithSyncConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	configContent := `
default_agent = "codex"

[sync]
enabled = true
postgres_url = "postgres://roborev:pass@localhost:5432/roborev"
interval = "10m"
machine_name = "test-machine"
connect_timeout = "10s"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadGlobalFrom(configPath)
	if err != nil {
		t.Fatalf("LoadGlobalFrom failed: %v", err)
	}

	if !cfg.Sync.Enabled {
		t.Error("Expected Sync.Enabled to be true")
	}
	if cfg.Sync.PostgresURL != "postgres://roborev:pass@localhost:5432/roborev" {
		t.Errorf("Unexpected PostgresURL: %s", cfg.Sync.PostgresURL)
	}
	if cfg.Sync.Interval != "10m" {
		t.Errorf("Expected Interval '10m', got '%s'", cfg.Sync.Interval)
	}
	if cfg.Sync.MachineName != "test-machine" {
		t.Errorf("Expected MachineName 'test-machine', got '%s'", cfg.Sync.MachineName)
	}
	if cfg.Sync.ConnectTimeout != "10s" {
		t.Errorf("Expected ConnectTimeout '10s', got '%s'", cfg.Sync.ConnectTimeout)
	}
}

func TestGetDisplayName(t *testing.T) {
	t.Run("no config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		name := GetDisplayName(tmpDir)
		if name != "" {
			t.Errorf("Expected empty display name when no config file, got '%s'", name)
		}
	})

	t.Run("display_name not set", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`agent = "codex"`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		name := GetDisplayName(tmpDir)
		if name != "" {
			t.Errorf("Expected empty display name when not set, got '%s'", name)
		}
	})

	t.Run("display_name is set", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		configContent := `
display_name = "My Cool Project"
`
		if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		name := GetDisplayName(tmpDir)
		if name != "My Cool Project" {
			t.Errorf("Expected display name 'My Cool Project', got '%s'", name)
		}
	})

	t.Run("display_name with other config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		configContent := `
agent = "claude-code"
display_name = "Backend Service"
excluded_branches = ["wip"]
`
		if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		name := GetDisplayName(tmpDir)
		if name != "Backend Service" {
			t.Errorf("Expected display name 'Backend Service', got '%s'", name)
		}
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
			if gotErr != tt.wantErr {
				t.Errorf("ValidateRoborevID(%q) error = %q, wantErr = %v", tt.id, errMsg, tt.wantErr)
			}
		})
	}
}

func TestReadRoborevID(t *testing.T) {
	t.Run("file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		id, err := ReadRoborevID(tmpDir)
		if err != nil {
			t.Errorf("Expected no error for missing file, got: %v", err)
		}
		if id != "" {
			t.Errorf("Expected empty ID for missing file, got: %q", id)
		}
	})

	t.Run("valid file", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte("my-project\n"), 0644); err != nil {
			t.Fatal(err)
		}
		id, err := ReadRoborevID(tmpDir)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if id != "my-project" {
			t.Errorf("Expected 'my-project', got: %q", id)
		}
	})

	t.Run("valid file with whitespace", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte("  my-project  \n\n"), 0644); err != nil {
			t.Fatal(err)
		}
		id, err := ReadRoborevID(tmpDir)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if id != "my-project" {
			t.Errorf("Expected 'my-project', got: %q", id)
		}
	})

	t.Run("invalid file content", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte(".invalid-start"), 0644); err != nil {
			t.Fatal(err)
		}
		id, err := ReadRoborevID(tmpDir)
		if err == nil {
			t.Error("Expected error for invalid content")
		}
		if id != "" {
			t.Errorf("Expected empty ID on error, got: %q", id)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		id, err := ReadRoborevID(tmpDir)
		if err == nil {
			t.Error("Expected error for empty file")
		}
		if id != "" {
			t.Errorf("Expected empty ID on error, got: %q", id)
		}
	})
}

func TestResolveRepoIdentity(t *testing.T) {
	t.Run("uses roborev-id when present", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte("my-custom-id"), 0644); err != nil {
			t.Fatal(err)
		}

		mockRemote := func(repoPath, remoteName string) string {
			return "https://github.com/user/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		if id != "my-custom-id" {
			t.Errorf("Expected 'my-custom-id', got: %q", id)
		}
	})

	t.Run("falls back to git remote when no roborev-id", func(t *testing.T) {
		tmpDir := t.TempDir()

		mockRemote := func(repoPath, remoteName string) string {
			return "https://github.com/user/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		if id != "https://github.com/user/repo.git" {
			t.Errorf("Expected git remote URL, got: %q", id)
		}
	})

	t.Run("falls back to local path when no remote", func(t *testing.T) {
		tmpDir := t.TempDir()

		mockRemote := func(repoPath, remoteName string) string {
			return ""
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		expected := "local://" + tmpDir
		if id != expected {
			t.Errorf("Expected %q, got: %q", expected, id)
		}
	})

	t.Run("uses default git.GetRemoteURL when getRemoteURL is nil", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Initialize a git repo with a remote
		cmds := [][]string{
			{"git", "init"},
			{"git", "remote", "add", "origin", "https://github.com/test/repo.git"},
		}
		for _, args := range cmds {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = tmpDir
			if err := cmd.Run(); err != nil {
				t.Fatalf("Failed to run %v: %v", args, err)
			}
		}

		// With nil getRemoteURL, should use git.GetRemoteURL and find the remote
		id := ResolveRepoIdentity(tmpDir, nil)
		if id != "https://github.com/test/repo.git" {
			t.Errorf("Expected 'https://github.com/test/repo.git', got: %q", id)
		}
	})

	t.Run("falls back to local path when nil and no git remote", func(t *testing.T) {
		tmpDir := t.TempDir()

		// With nil getRemoteURL and no git repo, should fall back to local path
		id := ResolveRepoIdentity(tmpDir, nil)
		expected := "local://" + tmpDir
		if id != expected {
			t.Errorf("Expected %q, got: %q", expected, id)
		}
	})

	t.Run("skips invalid roborev-id and uses remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Write invalid content (starts with dot)
		if err := os.WriteFile(filepath.Join(tmpDir, ".roborev-id"), []byte(".invalid"), 0644); err != nil {
			t.Fatal(err)
		}

		mockRemote := func(repoPath, remoteName string) string {
			return "https://github.com/user/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		if id != "https://github.com/user/repo.git" {
			t.Errorf("Expected git remote URL when roborev-id is invalid, got: %q", id)
		}
	})

	t.Run("strips credentials from remote URL", func(t *testing.T) {
		tmpDir := t.TempDir()

		mockRemote := func(repoPath, remoteName string) string {
			return "https://user:token@github.com/org/repo.git"
		}

		id := ResolveRepoIdentity(tmpDir, mockRemote)
		if id != "https://github.com/org/repo.git" {
			t.Errorf("Expected credentials stripped from URL, got: %q", id)
		}
	})
}

func TestResolveModel(t *testing.T) {
	t.Run("explicit model takes precedence", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("explicit-model", tmpDir, cfg)
		if model != "explicit-model" {
			t.Errorf("Expected 'explicit-model', got '%s'", model)
		}
	})

	t.Run("explicit with whitespace is trimmed", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("  explicit-model  ", tmpDir, cfg)
		if model != "explicit-model" {
			t.Errorf("Expected 'explicit-model', got '%s'", model)
		}
	})

	t.Run("empty explicit falls back to repo config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`model = "repo-model"`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("", tmpDir, cfg)
		if model != "repo-model" {
			t.Errorf("Expected 'repo-model' from repo config, got '%s'", model)
		}
	})

	t.Run("repo config with whitespace is trimmed", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`model = "  repo-model  "`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{}
		model := ResolveModel("", tmpDir, cfg)
		if model != "repo-model" {
			t.Errorf("Expected 'repo-model', got '%s'", model)
		}
	})

	t.Run("no repo config falls back to global config", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("", tmpDir, cfg)
		if model != "global-model" {
			t.Errorf("Expected 'global-model' from global config, got '%s'", model)
		}
	})

	t.Run("global config with whitespace is trimmed", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultModel: "  global-model  "}
		model := ResolveModel("", tmpDir, cfg)
		if model != "global-model" {
			t.Errorf("Expected 'global-model', got '%s'", model)
		}
	})

	t.Run("no config returns empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		model := ResolveModel("", tmpDir, nil)
		if model != "" {
			t.Errorf("Expected empty string when no config, got '%s'", model)
		}
	})

	t.Run("empty global config returns empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultModel: ""}
		model := ResolveModel("", tmpDir, cfg)
		if model != "" {
			t.Errorf("Expected empty string when global model is empty, got '%s'", model)
		}
	})

	t.Run("whitespace-only explicit falls through to repo config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`model = "repo-model"`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("   ", tmpDir, cfg)
		if model != "repo-model" {
			t.Errorf("Expected 'repo-model' when explicit is whitespace, got '%s'", model)
		}
	})

	t.Run("whitespace-only repo config falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`model = "   "`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("", tmpDir, cfg)
		if model != "global-model" {
			t.Errorf("Expected 'global-model' when repo model is whitespace, got '%s'", model)
		}
	})

	t.Run("explicit overrides repo config", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`model = "repo-model"`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("explicit-model", tmpDir, cfg)
		if model != "explicit-model" {
			t.Errorf("Expected 'explicit-model', got '%s'", model)
		}
	})

	t.Run("malformed repo config falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`this is not valid toml {{{`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultModel: "global-model"}
		model := ResolveModel("", tmpDir, cfg)
		if model != "global-model" {
			t.Errorf("Expected 'global-model' when repo config is malformed, got '%s'", model)
		}
	})
}

func TestResolveMaxPromptSize(t *testing.T) {
	t.Run("default when no config", func(t *testing.T) {
		tmpDir := t.TempDir()
		size := ResolveMaxPromptSize(tmpDir, nil)
		if size != DefaultMaxPromptSize {
			t.Errorf("Expected default %d, got %d", DefaultMaxPromptSize, size)
		}
	})

	t.Run("default when global config has zero", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultMaxPromptSize: 0}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != DefaultMaxPromptSize {
			t.Errorf("Expected default %d when global is 0, got %d", DefaultMaxPromptSize, size)
		}
	})

	t.Run("global config takes precedence over default", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 500*1024 {
			t.Errorf("Expected 500KB from global config, got %d", size)
		}
	})

	t.Run("repo config takes precedence over global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`max_prompt_size = 300000`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 300000 {
			t.Errorf("Expected 300000 from repo config, got %d", size)
		}
	})

	t.Run("repo config zero falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`max_prompt_size = 0`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 500*1024 {
			t.Errorf("Expected 500KB from global (repo is 0), got %d", size)
		}
	})

	t.Run("repo config without max_prompt_size falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`agent = "codex"`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultMaxPromptSize: 600 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 600*1024 {
			t.Errorf("Expected 600KB from global (repo has no max_prompt_size), got %d", size)
		}
	})

	t.Run("malformed repo config falls through to global", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoConfig := filepath.Join(tmpDir, ".roborev.toml")
		if err := os.WriteFile(repoConfig, []byte(`this is not valid toml {{{`), 0644); err != nil {
			t.Fatalf("Failed to write repo config: %v", err)
		}

		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 500*1024 {
			t.Errorf("Expected 500KB from global (repo config malformed), got %d", size)
		}
	})
}

func TestResolveAgentForWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		cli      string
		repo     map[string]string
		global   map[string]string
		workflow string
		level    string
		expect   string
	}{
		// Defaults
		{"empty config", "", nil, nil, "review", "fast", "codex"},
		{"global default only", "", nil, M{"default_agent": "claude"}, "review", "fast", "claude"},

		// Global specificity ladder
		{"global workflow > global default", "", nil, M{"default_agent": "codex", "review_agent": "claude"}, "review", "fast", "claude"},
		{"global level > global workflow", "", nil, M{"review_agent": "codex", "review_agent_fast": "claude"}, "review", "fast", "claude"},
		{"global level ignored for wrong level", "", nil, M{"review_agent": "codex", "review_agent_fast": "claude"}, "review", "thorough", "codex"},

		// Repo specificity ladder
		{"repo generic only", "", M{"agent": "claude"}, nil, "review", "fast", "claude"},
		{"repo workflow > repo generic", "", M{"agent": "codex", "review_agent": "claude"}, nil, "review", "fast", "claude"},
		{"repo level > repo workflow", "", M{"review_agent": "codex", "review_agent_fast": "claude"}, nil, "review", "fast", "claude"},

		// Layer beats specificity (Option A)
		{"repo generic > global level-specific", "", M{"agent": "claude"}, M{"review_agent_fast": "gemini"}, "review", "fast", "claude"},
		{"repo generic > global workflow-specific", "", M{"agent": "claude"}, M{"review_agent": "gemini"}, "review", "fast", "claude"},
		{"repo workflow > global level-specific", "", M{"review_agent": "claude"}, M{"review_agent_fast": "gemini"}, "review", "fast", "claude"},

		// CLI wins all
		{"cli > repo level-specific", "droid", M{"review_agent_fast": "claude"}, nil, "review", "fast", "droid"},
		{"cli > everything", "droid", M{"review_agent_fast": "claude"}, M{"review_agent_fast": "gemini"}, "review", "fast", "droid"},

		// Refine workflow isolation
		{"refine uses refine_agent not review_agent", "", M{"review_agent": "claude", "refine_agent": "gemini"}, nil, "refine", "fast", "gemini"},
		{"refine level-specific", "", M{"refine_agent": "codex", "refine_agent_fast": "claude"}, nil, "refine", "fast", "claude"},
		{"review config ignored for refine", "", M{"review_agent_fast": "claude"}, M{"default_agent": "codex"}, "refine", "fast", "codex"},

		// Level isolation
		{"fast config ignored for standard", "", M{"review_agent_fast": "claude", "review_agent": "codex"}, nil, "review", "standard", "codex"},
		{"standard config used for standard", "", M{"review_agent_standard": "claude"}, nil, "review", "standard", "claude"},
		{"thorough config used for thorough", "", M{"review_agent_thorough": "claude"}, nil, "review", "thorough", "claude"},

		// Mixed layers
		{"repo workflow + global level (repo wins)", "", M{"review_agent": "claude"}, M{"review_agent_fast": "gemini", "review_agent_thorough": "droid"}, "review", "fast", "claude"},
		{"global fills gaps repo doesn't set", "", M{"agent": "codex"}, M{"review_agent_fast": "claude"}, "review", "standard", "codex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeRepoConfig(t, tmpDir, tt.repo)
			global := buildGlobalConfig(tt.global)
			got := ResolveAgentForWorkflow(tt.cli, tmpDir, global, tt.workflow, tt.level)
			if got != tt.expect {
				t.Errorf("got %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestResolveModelForWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		cli      string
		repo     map[string]string
		global   map[string]string
		workflow string
		level    string
		expect   string
	}{
		// Defaults (model defaults to empty, not "codex")
		{"empty config", "", nil, nil, "review", "fast", ""},
		{"global default only", "", nil, M{"default_model": "gpt-4"}, "review", "fast", "gpt-4"},

		// Global specificity ladder
		{"global workflow > global default", "", nil, M{"default_model": "gpt-4", "review_model": "claude-3"}, "review", "fast", "claude-3"},
		{"global level > global workflow", "", nil, M{"review_model": "gpt-4", "review_model_fast": "claude-3"}, "review", "fast", "claude-3"},

		// Repo specificity ladder
		{"repo generic only", "", M{"model": "gpt-4"}, nil, "review", "fast", "gpt-4"},
		{"repo workflow > repo generic", "", M{"model": "gpt-4", "review_model": "claude-3"}, nil, "review", "fast", "claude-3"},
		{"repo level > repo workflow", "", M{"review_model": "gpt-4", "review_model_fast": "claude-3"}, nil, "review", "fast", "claude-3"},

		// Layer beats specificity (Option A)
		{"repo generic > global level-specific", "", M{"model": "gpt-4"}, M{"review_model_fast": "claude-3"}, "review", "fast", "gpt-4"},

		// CLI wins all
		{"cli > everything", "o1", M{"review_model_fast": "gpt-4"}, M{"review_model_fast": "claude-3"}, "review", "fast", "o1"},

		// Refine workflow isolation
		{"refine uses refine_model", "", M{"review_model": "gpt-4", "refine_model": "claude-3"}, nil, "refine", "fast", "claude-3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writeRepoConfig(t, tmpDir, tt.repo)
			global := buildGlobalConfig(tt.global)
			got := ResolveModelForWorkflow(tt.cli, tmpDir, global, tt.workflow, tt.level)
			if got != tt.expect {
				t.Errorf("got %q, want %q", got, tt.expect)
			}
		})
	}
}

// M is a shorthand type for map[string]string to keep test tables compact
type M = map[string]string

func writeRepoConfig(t *testing.T, dir string, cfg map[string]string) {
	t.Helper()
	if cfg == nil {
		return
	}
	var sb strings.Builder
	for k, v := range cfg {
		sb.WriteString(k + " = \"" + v + "\"\n")
	}
	if err := os.WriteFile(filepath.Join(dir, ".roborev.toml"), []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
}

func buildGlobalConfig(cfg map[string]string) *Config {
	if cfg == nil {
		return nil
	}
	c := &Config{}
	for k, v := range cfg {
		switch k {
		case "default_agent":
			c.DefaultAgent = v
		case "default_model":
			c.DefaultModel = v
		case "review_agent":
			c.ReviewAgent = v
		case "review_agent_fast":
			c.ReviewAgentFast = v
		case "review_agent_standard":
			c.ReviewAgentStandard = v
		case "review_agent_thorough":
			c.ReviewAgentThorough = v
		case "refine_agent":
			c.RefineAgent = v
		case "refine_agent_fast":
			c.RefineAgentFast = v
		case "refine_agent_standard":
			c.RefineAgentStandard = v
		case "refine_agent_thorough":
			c.RefineAgentThorough = v
		case "review_model":
			c.ReviewModel = v
		case "review_model_fast":
			c.ReviewModelFast = v
		case "review_model_standard":
			c.ReviewModelStandard = v
		case "review_model_thorough":
			c.ReviewModelThorough = v
		case "refine_model":
			c.RefineModel = v
		case "refine_model_fast":
			c.RefineModelFast = v
		case "refine_model_standard":
			c.RefineModelStandard = v
		case "refine_model_thorough":
			c.RefineModelThorough = v
		}
	}
	return c
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
			if result != tt.expected {
				t.Errorf("stripURLCredentials(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
