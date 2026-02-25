package config

import (
	"fmt"
	"os"
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
		t.Setenv("ROBOREV_DATA_DIR", "") // DataDir() treats empty the same as unset

		dir := DataDir()
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".roborev")
		if dir != expected {
			t.Errorf("Expected %s, got %s", expected, dir)
		}
	})

	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv("ROBOREV_DATA_DIR", "/custom/data/dir")

		dir := DataDir()
		if dir != "/custom/data/dir" {
			t.Errorf("Expected /custom/data/dir, got %s", dir)
		}
	})

	t.Run("GlobalConfigPath uses DataDir", func(t *testing.T) {
		testDir := filepath.Join(os.TempDir(), "roborev-test")
		t.Setenv("ROBOREV_DATA_DIR", testDir)

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
	writeRepoConfigStr(t, tmpDir, `agent = "claude-code"`)

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
	tmpDir := newTempRepo(t, `
agent = "claude-code"
review_guidelines = """
We are not doing database migrations because there are no production databases yet.
Prefer composition over inheritance.
All public APIs must have documentation comments.
"""
`)

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
	tmpDir := newTempRepo(t, `agent = "codex"`)

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
			if got != tt.want {
				t.Errorf("ResolveJobTimeout() = %v, want %v", got, tt.want)
			}
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
					if (err != nil) != tt.wantErr {
						t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
					}
					if !tt.wantErr && got != tt.want {
						t.Errorf("got %q, want %q", got, tt.want)
					}
				})
			}
		})
	}

	runTests(t, "Review", ResolveReviewReasoning, "review_reasoning", "thorough", "standard")
	runTests(t, "Refine", ResolveRefineReasoning, "refine_reasoning", "standard", "thorough")
	runTests(t, "Fix", ResolveFixReasoning, "fix_reasoning", "standard", "thorough")
}

func TestFixEmptyReasoningSelectsStandardAgent(t *testing.T) {
	// End-to-end: empty --reasoning resolves to "standard" via ResolveFixReasoning,
	// then ResolveAgentForWorkflow selects fix_agent_standard over fix_agent.
	tmpDir := t.TempDir()
	writeRepoConfig(t, tmpDir, M{
		"fix_agent":          "codex",
		"fix_agent_standard": "claude",
		"fix_agent_fast":     "gemini",
	})

	reasoning, err := ResolveFixReasoning("", tmpDir)
	if err != nil {
		t.Fatalf("ResolveFixReasoning: %v", err)
	}
	if reasoning != "standard" {
		t.Fatalf("expected default reasoning 'standard', got %q", reasoning)
	}

	agent := ResolveAgentForWorkflow("", tmpDir, nil, "fix", reasoning)
	if agent != "claude" {
		t.Errorf("expected fix_agent_standard 'claude', got %q", agent)
	}

	model := ResolveModelForWorkflow("", tmpDir, nil, "fix", reasoning)
	if model != "" {
		t.Errorf("expected empty model (none configured), got %q", model)
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
			// For "no config file", we just don't write anything.

			got := IsBranchExcluded(tmpDir, tt.branch)
			if got != tt.want {
				t.Errorf("IsBranchExcluded(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
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
		t.Setenv("TEST_PG_PASS", "secret123")

		cfg := SyncConfig{PostgresURL: "postgres://user:${TEST_PG_PASS}@localhost:5432/db"}
		expected := "postgres://user:secret123@localhost:5432/db"
		if got := cfg.PostgresURLExpanded(); got != expected {
			t.Errorf("Expected %q, got %q", expected, got)
		}
	})

	t.Run("missing env var becomes empty", func(t *testing.T) {
		t.Setenv("NONEXISTENT_VAR", "")
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
		t.Setenv("MISSING_VAR", "")
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
		t.Setenv("TEST_PG_PASS2", "secret")

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
	if err := os.WriteFile(configPath, []byte(`
default_agent = "codex"

[sync]
enabled = true
postgres_url = "postgres://roborev:pass@localhost:5432/roborev"
interval = "10m"
machine_name = "test-machine"
connect_timeout = "10s"
`), 0644); err != nil {
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
		tmpDir := newTempRepo(t, `agent = "codex"`)
		name := GetDisplayName(tmpDir)
		if name != "" {
			t.Errorf("Expected empty display name when not set, got '%s'", name)
		}
	})

	t.Run("display_name is set", func(t *testing.T) {
		tmpDir := newTempRepo(t, `display_name = "My Cool Project"`)
		name := GetDisplayName(tmpDir)
		if name != "My Cool Project" {
			t.Errorf("Expected display name 'My Cool Project', got '%s'", name)
		}
	})

	t.Run("display_name with other config", func(t *testing.T) {
		tmpDir := newTempRepo(t, `
agent = "claude-code"
display_name = "Backend Service"
excluded_branches = ["wip"]
`)
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
		execGit(t, tmpDir, "init")
		execGit(t, tmpDir, "remote", "add", "origin", "https://github.com/test/repo.git")

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
			if got != tt.want {
				t.Errorf("ResolveModel() = %q, want %q", got, tt.want)
			}
		})
	}
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
		tmpDir := newTempRepo(t, `max_prompt_size = 300000`)
		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 300000 {
			t.Errorf("Expected 300000 from repo config, got %d", size)
		}
	})

	t.Run("repo config zero falls through to global", func(t *testing.T) {
		tmpDir := newTempRepo(t, `max_prompt_size = 0`)
		cfg := &Config{DefaultMaxPromptSize: 500 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 500*1024 {
			t.Errorf("Expected 500KB from global (repo is 0), got %d", size)
		}
	})

	t.Run("repo config without max_prompt_size falls through to global", func(t *testing.T) {
		tmpDir := newTempRepo(t, `agent = "codex"`)
		cfg := &Config{DefaultMaxPromptSize: 600 * 1024}
		size := ResolveMaxPromptSize(tmpDir, cfg)
		if size != 600*1024 {
			t.Errorf("Expected 600KB from global (repo has no max_prompt_size), got %d", size)
		}
	})

	t.Run("malformed repo config falls through to global", func(t *testing.T) {
		tmpDir := newTempRepo(t, `this is not valid toml {{{`)
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
		global   *Config
		workflow string
		level    string
		expect   string
	}{
		// Defaults
		{"empty config", "", nil, nil, "review", "fast", "codex"},
		{"global default only", "", nil, &Config{DefaultAgent: "claude"}, "review", "fast", "claude"},

		// Global specificity ladder
		{"global workflow > global default", "", nil, &Config{DefaultAgent: "codex", ReviewAgent: "claude"}, "review", "fast", "claude"},
		{"global level > global workflow", "", nil, &Config{ReviewAgent: "codex", ReviewAgentFast: "claude"}, "review", "fast", "claude"},
		{"global level ignored for wrong level", "", nil, &Config{ReviewAgent: "codex", ReviewAgentFast: "claude"}, "review", "thorough", "codex"},

		// Repo specificity ladder
		{"repo generic only", "", M{"agent": "claude"}, nil, "review", "fast", "claude"},
		{"repo workflow > repo generic", "", M{"agent": "codex", "review_agent": "claude"}, nil, "review", "fast", "claude"},
		{"repo level > repo workflow", "", M{"review_agent": "codex", "review_agent_fast": "claude"}, nil, "review", "fast", "claude"},

		// Layer beats specificity (Option A)
		{"repo generic > global level-specific", "", M{"agent": "claude"}, &Config{ReviewAgentFast: "gemini"}, "review", "fast", "claude"},
		{"repo generic > global workflow-specific", "", M{"agent": "claude"}, &Config{ReviewAgent: "gemini"}, "review", "fast", "claude"},
		{"repo workflow > global level-specific", "", M{"review_agent": "claude"}, &Config{ReviewAgentFast: "gemini"}, "review", "fast", "claude"},

		// CLI wins all
		{"cli > repo level-specific", "droid", M{"review_agent_fast": "claude"}, nil, "review", "fast", "droid"},
		{"cli > everything", "droid", M{"review_agent_fast": "claude"}, &Config{ReviewAgentFast: "gemini"}, "review", "fast", "droid"},

		// Refine workflow isolation
		{"refine uses refine_agent not review_agent", "", M{"review_agent": "claude", "refine_agent": "gemini"}, nil, "refine", "fast", "gemini"},
		{"refine level-specific", "", M{"refine_agent": "codex", "refine_agent_fast": "claude"}, nil, "refine", "fast", "claude"},
		{"review config ignored for refine", "", M{"review_agent_fast": "claude"}, &Config{DefaultAgent: "codex"}, "refine", "fast", "codex"},

		// Level isolation
		{"fast config ignored for standard", "", M{"review_agent_fast": "claude", "review_agent": "codex"}, nil, "review", "standard", "codex"},
		{"standard config used for standard", "", M{"review_agent_standard": "claude"}, nil, "review", "standard", "claude"},
		{"thorough config used for thorough", "", M{"review_agent_thorough": "claude"}, nil, "review", "thorough", "claude"},

		// Mixed layers
		{"repo workflow + global level (repo wins)", "", M{"review_agent": "claude"}, &Config{ReviewAgentFast: "gemini", ReviewAgentThorough: "droid"}, "review", "fast", "claude"},
		{"global fills gaps repo doesn't set", "", M{"agent": "codex"}, &Config{ReviewAgentFast: "claude"}, "review", "standard", "codex"},

		// Fix workflow
		{"fix uses fix_agent", "", M{"fix_agent": "claude"}, nil, "fix", "fast", "claude"},
		{"fix level-specific", "", M{"fix_agent": "codex", "fix_agent_fast": "claude"}, nil, "fix", "fast", "claude"},
		{"fix falls back to generic agent", "", M{"agent": "claude"}, nil, "fix", "fast", "claude"},
		{"fix falls back to global fix_agent", "", nil, &Config{FixAgent: "claude"}, "fix", "fast", "claude"},
		{"fix global level-specific", "", nil, &Config{FixAgent: "codex", FixAgentFast: "claude"}, "fix", "fast", "claude"},
		{"fix standard level selects fix_agent_standard", "", M{"fix_agent_standard": "claude", "fix_agent": "codex"}, nil, "fix", "standard", "claude"},
		{"fix default reasoning (standard) selects level-specific", "", nil, &Config{FixAgentStandard: "claude", FixAgent: "codex"}, "fix", "standard", "claude"},
		{"fix isolated from review", "", M{"review_agent": "claude"}, &Config{DefaultAgent: "codex"}, "fix", "fast", "codex"},
		{"fix isolated from refine", "", M{"refine_agent": "claude"}, &Config{DefaultAgent: "codex"}, "fix", "fast", "codex"},

		// Design workflow
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
		global   *Config
		workflow string
		level    string
		expect   string
	}{
		// Defaults (model defaults to empty, not "codex")
		{"empty config", "", nil, nil, "review", "fast", ""},
		{"global default only", "", nil, &Config{DefaultModel: "gpt-4"}, "review", "fast", "gpt-4"},

		// Global specificity ladder
		{"global workflow > global default", "", nil, &Config{DefaultModel: "gpt-4", ReviewModel: "claude-3"}, "review", "fast", "claude-3"},
		{"global level > global workflow", "", nil, &Config{ReviewModel: "gpt-4", ReviewModelFast: "claude-3"}, "review", "fast", "claude-3"},

		// Repo specificity ladder
		{"repo generic only", "", M{"model": "gpt-4"}, nil, "review", "fast", "gpt-4"},
		{"repo workflow > repo generic", "", M{"model": "gpt-4", "review_model": "claude-3"}, nil, "review", "fast", "claude-3"},
		{"repo level > repo workflow", "", M{"review_model": "gpt-4", "review_model_fast": "claude-3"}, nil, "review", "fast", "claude-3"},

		// Layer beats specificity (Option A)
		{"repo generic > global level-specific", "", M{"model": "gpt-4"}, &Config{ReviewModelFast: "claude-3"}, "review", "fast", "gpt-4"},

		// CLI wins all
		{"cli > everything", "o1", M{"review_model_fast": "gpt-4"}, &Config{ReviewModelFast: "claude-3"}, "review", "fast", "o1"},

		// Refine workflow isolation
		{"refine uses refine_model", "", M{"review_model": "gpt-4", "refine_model": "claude-3"}, nil, "refine", "fast", "claude-3"},

		// Fix workflow
		{"fix uses fix_model", "", M{"fix_model": "gpt-4"}, nil, "fix", "fast", "gpt-4"},
		{"fix level-specific model", "", M{"fix_model": "gpt-4", "fix_model_fast": "claude-3"}, nil, "fix", "fast", "claude-3"},
		{"fix falls back to generic model", "", M{"model": "gpt-4"}, nil, "fix", "fast", "gpt-4"},
		{"fix isolated from review model", "", M{"review_model": "gpt-4"}, nil, "fix", "fast", ""},

		// Design workflow
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
		// No backup configured
		{"empty config", nil, nil, "review", ""},
		{"only primary agent configured", M{"review_agent": "claude"}, nil, "review", ""},

		// Global backup agent
		{"global backup only", nil, &Config{ReviewBackupAgent: "test"}, "review", "test"},
		{"global backup for refine", nil, &Config{RefineBackupAgent: "claude"}, "refine", "claude"},
		{"global backup for fix", nil, &Config{FixBackupAgent: "codex"}, "fix", "codex"},
		{"global backup for security", nil, &Config{SecurityBackupAgent: "gemini"}, "security", "gemini"},
		{"global backup for design", nil, &Config{DesignBackupAgent: "droid"}, "design", "droid"},

		// Repo backup agent overrides global
		{"repo overrides global", M{"review_backup_agent": "repo-test"}, &Config{ReviewBackupAgent: "global-test"}, "review", "repo-test"},
		{"repo backup only", M{"review_backup_agent": "test"}, nil, "review", "test"},

		// Different workflows resolve independently
		{"review backup doesn't affect refine", M{"review_backup_agent": "claude"}, nil, "refine", ""},
		{"each workflow has own backup", M{"review_backup_agent": "claude", "refine_backup_agent": "codex"}, nil, "review", "claude"},
		{"each workflow has own backup - refine", M{"review_backup_agent": "claude", "refine_backup_agent": "codex"}, nil, "refine", "codex"},

		// Unknown workflow returns empty
		{"unknown workflow", M{"review_backup_agent": "test"}, nil, "unknown", ""},

		// No reasoning level support for backup agents
		{"no level variants recognized", M{"review_backup_agent_fast": "claude"}, nil, "review", ""},
		{"backup agent doesn't use levels", M{"review_backup_agent": "claude"}, nil, "review", "claude"},

		// Default/generic backup agent fallback
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
			// Create temp dir for repo config
			repoDir := t.TempDir()

			// Write repo config if provided
			if tt.repo != nil {
				writeRepoConfig(t, repoDir, tt.repo)
			}

			// Test the function
			result := ResolveBackupAgentForWorkflow(repoDir, tt.global, tt.workflow)

			if result != tt.expect {
				t.Errorf("ResolveBackupAgentForWorkflow(%q, global, %q) = %q, want %q",
					repoDir, tt.workflow, result, tt.expect)
			}
		})
	}
}

func TestResolvedReviewTypes(t *testing.T) {
	t.Run("uses configured types", func(t *testing.T) {
		ci := CIConfig{ReviewTypes: []string{"security", "review"}}
		got := ci.ResolvedReviewTypes()
		if len(got) != 2 || got[0] != "security" || got[1] != "review" {
			t.Errorf("got %v, want [security review]", got)
		}
	})

	t.Run("defaults to security", func(t *testing.T) {
		ci := CIConfig{}
		got := ci.ResolvedReviewTypes()
		if len(got) != 1 || got[0] != "security" {
			t.Errorf("got %v, want [security]", got)
		}
	})
}

func TestResolvedAgents(t *testing.T) {
	t.Run("uses configured agents", func(t *testing.T) {
		ci := CIConfig{Agents: []string{"codex", "gemini"}}
		got := ci.ResolvedAgents()
		if len(got) != 2 || got[0] != "codex" || got[1] != "gemini" {
			t.Errorf("got %v, want [codex gemini]", got)
		}
	})

	t.Run("defaults to auto-detect", func(t *testing.T) {
		ci := CIConfig{}
		got := ci.ResolvedAgents()
		if len(got) != 1 || got[0] != "" {
			t.Errorf("got %v, want [\"\"]", got)
		}
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
			if got != tt.want {
				t.Errorf("ResolvedMaxRepos() = %d, want %d", got, tt.want)
			}
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
			t.Fatal(err)
		}

		cfg, err := LoadGlobalFrom(configPath)
		if err != nil {
			t.Fatalf("LoadGlobalFrom: %v", err)
		}

		if len(cfg.CI.Repos) != 2 {
			t.Errorf("got %d repos, want 2", len(cfg.CI.Repos))
		}
		if len(cfg.CI.ExcludeRepos) != 2 {
			t.Errorf("got %d exclude_repos, want 2", len(cfg.CI.ExcludeRepos))
		}
		if cfg.CI.MaxRepos != 50 {
			t.Errorf("got max_repos %d, want 50", cfg.CI.MaxRepos)
		}
		if cfg.CI.ResolvedMaxRepos() != 50 {
			t.Errorf("ResolvedMaxRepos() = %d, want 50", cfg.CI.ResolvedMaxRepos())
		}
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
				t.Fatalf("NormalizeMinSeverity(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("NormalizeMinSeverity(%q) = %q, want %q", tt.input, got, tt.want)
			}
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
		if err != nil {
			t.Fatalf("LoadRepoConfig: %v", err)
		}
		if len(cfg.CI.Agents) != 2 || cfg.CI.Agents[0] != "gemini" || cfg.CI.Agents[1] != "claude" {
			t.Errorf("got agents %v, want [gemini claude]", cfg.CI.Agents)
		}
		if len(cfg.CI.ReviewTypes) != 2 || cfg.CI.ReviewTypes[0] != "security" || cfg.CI.ReviewTypes[1] != "review" {
			t.Errorf("got review_types %v, want [security review]", cfg.CI.ReviewTypes)
		}
		if cfg.CI.Reasoning != "standard" {
			t.Errorf("got reasoning %q, want %q", cfg.CI.Reasoning, "standard")
		}
	})

	t.Run("empty CI section", func(t *testing.T) {
		tmpDir := newTempRepo(t, `agent = "codex"`)
		cfg, err := LoadRepoConfig(tmpDir)
		if err != nil {
			t.Fatalf("LoadRepoConfig: %v", err)
		}
		if len(cfg.CI.Agents) != 0 {
			t.Errorf("got agents %v, want empty", cfg.CI.Agents)
		}
		if len(cfg.CI.ReviewTypes) != 0 {
			t.Errorf("got review_types %v, want empty", cfg.CI.ReviewTypes)
		}
		if cfg.CI.Reasoning != "" {
			t.Errorf("got reasoning %q, want empty", cfg.CI.Reasoning)
		}
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
		if got := ci.InstallationIDForOwner("wesm"); got != 111111 {
			t.Errorf("got %d, want 111111", got)
		}
		if got := ci.InstallationIDForOwner("roborev-dev"); got != 222222 {
			t.Errorf("got %d, want 222222", got)
		}
	})

	t.Run("falls back to singular", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations:  map[string]int64{"wesm": 111111},
			GitHubAppInstallationID: 999999,
		}}
		if got := ci.InstallationIDForOwner("unknown-org"); got != 999999 {
			t.Errorf("got %d, want 999999", got)
		}
	})

	t.Run("zero when unset", func(t *testing.T) {
		ci := CIConfig{}
		if got := ci.InstallationIDForOwner("wesm"); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("zero mapped value falls back to singular", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations:  map[string]int64{"wesm": 0},
			GitHubAppInstallationID: 999999,
		}}
		if got := ci.InstallationIDForOwner("wesm"); got != 999999 {
			t.Errorf("got %d, want 999999 (fallback to singular)", got)
		}
	})

	t.Run("case-insensitive lookup after normalization", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations: map[string]int64{"Wesm": 111111, "RoboRev-Dev": 222222},
		}}
		if err := ci.NormalizeInstallations(); err != nil {
			t.Fatalf("NormalizeInstallations: %v", err)
		}
		if got := ci.InstallationIDForOwner("wesm"); got != 111111 {
			t.Errorf("got %d, want 111111", got)
		}
		if got := ci.InstallationIDForOwner("WESM"); got != 111111 {
			t.Errorf("got %d, want 111111", got)
		}
		if got := ci.InstallationIDForOwner("roborev-dev"); got != 222222 {
			t.Errorf("got %d, want 222222", got)
		}
	})
}

func TestNormalizeInstallations(t *testing.T) {
	t.Run("lowercases keys", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations: map[string]int64{"Wesm": 111111, "RoboRev-Dev": 222222},
		}}
		if err := ci.NormalizeInstallations(); err != nil {
			t.Fatalf("NormalizeInstallations: %v", err)
		}
		if _, ok := ci.GitHubAppInstallations["wesm"]; !ok {
			t.Error("expected lowercase key 'wesm' after normalization")
		}
		if _, ok := ci.GitHubAppInstallations["roborev-dev"]; !ok {
			t.Error("expected lowercase key 'roborev-dev' after normalization")
		}
		if _, ok := ci.GitHubAppInstallations["Wesm"]; ok {
			t.Error("original mixed-case key 'Wesm' should not exist after normalization")
		}
	})

	t.Run("noop on nil map", func(t *testing.T) {
		ci := CIConfig{}
		if err := ci.NormalizeInstallations(); err != nil {
			t.Fatalf("NormalizeInstallations on nil map: %v", err)
		}
	})

	t.Run("case-colliding keys returns error", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppInstallations: map[string]int64{"wesm": 111111, "Wesm": 222222},
		}}
		err := ci.NormalizeInstallations()
		if err == nil {
			t.Fatal("expected error for case-colliding keys")
		}
		if !strings.Contains(err.Error(), "case-colliding") {
			t.Errorf("expected case-colliding error, got: %v", err)
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
		t.Fatal(err)
	}

	cfg, err := LoadGlobalFrom(configPath)
	if err != nil {
		t.Fatalf("LoadGlobalFrom: %v", err)
	}

	if got := cfg.CI.InstallationIDForOwner("wesm"); got != 111111 {
		t.Errorf("got %d, want 111111 for normalized 'wesm'", got)
	}
	if got := cfg.CI.InstallationIDForOwner("roborev-dev"); got != 222222 {
		t.Errorf("got %d, want 222222 for normalized 'roborev-dev'", got)
	}
}

func TestGitHubAppConfigured_MultiInstall(t *testing.T) {
	t.Run("configured with map only", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:            12345,
			GitHubAppPrivateKey:    "~/.roborev/app.pem",
			GitHubAppInstallations: map[string]int64{"wesm": 111111},
		}}
		if !ci.GitHubAppConfigured() {
			t.Error("expected GitHubAppConfigured() == true with map only")
		}
	})

	t.Run("configured with singular only", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:             12345,
			GitHubAppPrivateKey:     "~/.roborev/app.pem",
			GitHubAppInstallationID: 111111,
		}}
		if !ci.GitHubAppConfigured() {
			t.Error("expected GitHubAppConfigured() == true with singular only")
		}
	})

	t.Run("not configured without any installation", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:         12345,
			GitHubAppPrivateKey: "~/.roborev/app.pem",
		}}
		if ci.GitHubAppConfigured() {
			t.Error("expected GitHubAppConfigured() == false without any installation ID")
		}
	})

	t.Run("not configured without private key", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{
			GitHubAppID:             12345,
			GitHubAppInstallationID: 111111,
		}}
		if ci.GitHubAppConfigured() {
			t.Error("expected GitHubAppConfigured() == false without private key")
		}
	})
}

func TestGitHubAppPrivateKeyResolved_TildeExpansion(t *testing.T) {
	// Create a temp PEM file
	dir := t.TempDir()
	pemFile := filepath.Join(dir, "test.pem")
	pemContent := "-----BEGIN RSA PRIVATE KEY-----\nfakekey\n-----END RSA PRIVATE KEY-----"
	if err := os.WriteFile(pemFile, []byte(pemContent), 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("inline PEM returned directly", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: pemContent}}
		got, err := ci.GitHubAppPrivateKeyResolved()
		if err != nil {
			t.Fatal(err)
		}
		if got != pemContent {
			t.Errorf("got %q, want inline PEM", got)
		}
	})

	t.Run("absolute path reads file", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: pemFile}}
		got, err := ci.GitHubAppPrivateKeyResolved()
		if err != nil {
			t.Fatal(err)
		}
		if got != pemContent {
			t.Errorf("got %q, want %q", got, pemContent)
		}
	})

	t.Run("tilde path expands to home", func(t *testing.T) {
		// Use a fake HOME so we don't touch the real home directory
		fakeHome := t.TempDir()
		t.Setenv("HOME", fakeHome)
		t.Setenv("USERPROFILE", fakeHome) // Windows compatibility

		fakePem := filepath.Join(fakeHome, ".roborev", "test.pem")
		if err := os.MkdirAll(filepath.Dir(fakePem), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fakePem, []byte(pemContent), 0600); err != nil {
			t.Fatal(err)
		}

		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: "~/.roborev/test.pem"}}
		got, err := ci.GitHubAppPrivateKeyResolved()
		if err != nil {
			t.Fatalf("tilde expansion failed: %v", err)
		}
		if got != pemContent {
			t.Errorf("got %q, want %q", got, pemContent)
		}
	})

	t.Run("empty after expansion returns error", func(t *testing.T) {
		ci := CIConfig{GitHubAppConfig: GitHubAppConfig{GitHubAppPrivateKey: ""}}
		_, err := ci.GitHubAppPrivateKeyResolved()
		if err == nil {
			t.Error("expected error for empty key")
		}
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
			if result != tt.expected {
				t.Errorf("stripURLCredentials(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestHideAddressedDefaultPersistence(t *testing.T) {
	testenv.SetDataDir(t)

	// Test saving preference as true
	cfg := &Config{HideAddressedByDefault: true}
	err := SaveGlobal(cfg)
	if err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	// Load and verify it persisted
	loaded, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal failed: %v", err)
	}
	if !loaded.HideAddressedByDefault {
		t.Error("Expected HideAddressedByDefault to be true")
	}

	// Toggle to false and verify
	loaded.HideAddressedByDefault = false
	err = SaveGlobal(loaded)
	if err != nil {
		t.Fatalf("SaveGlobal failed: %v", err)
	}

	reloaded, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal failed: %v", err)
	}
	if reloaded.HideAddressedByDefault {
		t.Error("Expected HideAddressedByDefault to be false")
	}
}

func TestIsDefaultReviewType(t *testing.T) {
	defaults := []string{"", "default", "general", "review"}
	for _, rt := range defaults {
		if !IsDefaultReviewType(rt) {
			t.Errorf("expected %q to be default review type", rt)
		}
	}
	nonDefaults := []string{"security", "design", "bogus"}
	for _, rt := range nonDefaults {
		if IsDefaultReviewType(rt) {
			t.Errorf("expected %q to NOT be default review type", rt)
		}
	}
}

func TestLoadRepoConfigFromRef(t *testing.T) {
	// Create a real git repo with .roborev.toml at a commit
	dir := t.TempDir()
	execGit(t, dir, "init")
	execGit(t, dir, "config", "user.email", "test@test.com")
	execGit(t, dir, "config", "user.name", "Test")

	// Write .roborev.toml and commit
	configContent := `review_guidelines = "Use descriptive variable names."` + "\n"
	writeTestFile(t, dir, ".roborev.toml", configContent)
	execGit(t, dir, "add", ".roborev.toml")
	execGit(t, dir, "commit", "-m", "add config")

	// Get the commit SHA
	sha := execGit(t, dir, "rev-parse", "HEAD")

	t.Run("loads config from ref", func(t *testing.T) {
		cfg, err := LoadRepoConfigFromRef(dir, sha)
		if err != nil {
			t.Fatalf("LoadRepoConfigFromRef: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.ReviewGuidelines != "Use descriptive variable names." {
			t.Errorf("got %q", cfg.ReviewGuidelines)
		}
	})

	t.Run("returns nil for nonexistent ref", func(t *testing.T) {
		cfg, err := LoadRepoConfigFromRef(dir, "0000000000000000000000000000000000000000")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Error("expected nil config for nonexistent ref")
		}
	})

	t.Run("returns nil when file missing from ref", func(t *testing.T) {
		// Remove .roborev.toml and commit
		execGit(t, dir, "rm", ".roborev.toml")
		execGit(t, dir, "commit", "-m", "remove config")
		headSHA := execGit(t, dir, "rev-parse", "HEAD")

		cfg, err := LoadRepoConfigFromRef(dir, headSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Error("expected nil config when file removed from ref")
		}
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
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf(
						"got[%d] = %q, want %q",
						i, got[i], tt.want[i])
				}
			}
		})
	}
}
