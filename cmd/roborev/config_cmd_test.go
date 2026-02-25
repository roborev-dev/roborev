package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func setupConfigFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.toml")
}

const (
	errGitStub = "git unavailable stub"
	errCwdStub = "cwd failed stub"
)

func captureOutput(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	defer func() { os.Stdout = old }()

	if err := fn(); err != nil {
		t.Fatalf("function failed: %v", err)
	}

	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return string(out)
}

func readTOML(t *testing.T, path string) map[string]any {
	t.Helper()
	raw := make(map[string]any)
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		t.Fatalf("read TOML %s: %v", path, err)
	}
	return raw
}

// getNestedValue traverses a dot-separated key path in a nested map.
func getNestedValue(t *testing.T, raw map[string]any, dotKey string) any {
	t.Helper()
	parts := strings.Split(dotKey, ".")
	var current any = raw
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("key %q: expected map at %q, got %T", dotKey, part, current)
		}
		current = m[part]
	}
	return current
}

func assertConfigValue(t *testing.T, path, dotKey string, expected any) {
	t.Helper()
	raw := readTOML(t, path)
	val := getNestedValue(t, raw, dotKey)
	if val != expected {
		t.Errorf("%s = %v (%T), want %v (%T)", dotKey, val, val, expected, expected)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

// stubRepoResolver implements RepoResolver for testing.
type stubRepoResolver struct {
	gitRoot    string
	gitErr     error
	workingDir string
	workingErr error
}

func (s *stubRepoResolver) RepoRoot() (string, error) {
	return s.gitRoot, s.gitErr
}

func (s *stubRepoResolver) WorkingDir() (string, error) {
	return s.workingDir, s.workingErr
}

func (s *stubRepoResolver) SetGitRoot(path string) {
	s.gitRoot = path
	s.gitErr = nil
}

func (s *stubRepoResolver) SetGitError(err error) {
	s.gitErr = err
}

func (s *stubRepoResolver) SetWorkingDir(path string) {
	s.workingDir = path
	s.workingErr = nil
}

func (s *stubRepoResolver) SetWorkingDirError(err error) {
	s.workingErr = err
}

// createFakeGitRepo creates a temporary directory with a .git subdirectory,
// simulating a repository root without running git init.
func createFakeGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatalf("create .git dir: %v", err)
	}
	return dir
}

type configEnv struct {
	DataDir  string
	RepoDir  string
	Resolver *stubRepoResolver
}

func setupConfigEnv(t *testing.T, globalTOML, localTOML string) configEnv {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dataDir)

	if globalTOML != "" {
		globalPath := filepath.Join(dataDir, "config.toml")
		if err := os.WriteFile(globalPath, []byte(globalTOML), 0644); err != nil {
			t.Fatalf("write global config: %v", err)
		}
	}

	repoDir := createFakeGitRepo(t)
	if localTOML != "" {
		localPath := filepath.Join(repoDir, ".roborev.toml")
		if err := os.WriteFile(localPath, []byte(localTOML), 0644); err != nil {
			t.Fatalf("write local config: %v", err)
		}
	}

	resolver := &stubRepoResolver{}
	resolver.SetGitRoot(repoDir)

	return configEnv{DataDir: dataDir, RepoDir: repoDir, Resolver: resolver}
}

func TestDetermineScope(t *testing.T) {
	tests := []struct {
		name       string
		globalFlag bool
		localFlag  bool
		want       configScope
		wantErr    bool
	}{
		{
			name:       "merged default",
			globalFlag: false,
			localFlag:  false,
			want:       scopeMerged,
		},
		{
			name:       "global",
			globalFlag: true,
			localFlag:  false,
			want:       scopeGlobal,
		},
		{
			name:       "local",
			globalFlag: false,
			localFlag:  true,
			want:       scopeLocal,
		},
		{
			name:       "conflicting flags",
			globalFlag: true,
			localFlag:  true,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := determineScope(tt.globalFlag, tt.localFlag)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("determineScope returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("determineScope = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRepoRoot(t *testing.T) {
	t.Run("uses git resolver when available", func(t *testing.T) {
		resolver := &stubRepoResolver{}
		resolver.SetGitRoot("/tmp/from-git")

		got, err := repoRoot(resolver)
		if err != nil {
			t.Fatalf("repoRoot returned error: %v", err)
		}
		if got != "/tmp/from-git" {
			t.Fatalf("repoRoot = %q, want %q", got, "/tmp/from-git")
		}
	})

	t.Run("falls back to filesystem when git resolver fails", func(t *testing.T) {
		repoDir := createFakeGitRepo(t)
		nestedDir := filepath.Join(repoDir, "nested", "deeper")
		if err := os.MkdirAll(nestedDir, 0755); err != nil {
			t.Fatalf("create nested dir: %v", err)
		}

		resolver := &stubRepoResolver{}
		resolver.SetGitError(errors.New(errGitStub))
		resolver.SetWorkingDir(nestedDir)

		got, err := repoRoot(resolver)
		if err != nil {
			t.Fatalf("repoRoot returned error: %v", err)
		}
		if got != repoDir {
			t.Fatalf("repoRoot = %q, want %q", got, repoDir)
		}
	})

	t.Run("optional lookup returns empty when not in repo", func(t *testing.T) {
		resolver := &stubRepoResolver{}
		resolver.SetGitError(errors.New(errGitStub))
		resolver.SetWorkingDir(t.TempDir())

		got, err := repoRoot(resolver)
		if err != nil {
			t.Fatalf("repoRoot returned error: %v", err)
		}
		if got != "" {
			t.Fatalf("repoRoot = %q, want empty string", got)
		}
	})
}
func TestRequireRepoRoot(t *testing.T) {
	t.Run("returns not repo error when required and missing", func(t *testing.T) {
		resolver := &stubRepoResolver{}
		resolver.SetGitError(errors.New(errGitStub))
		resolver.SetWorkingDir(t.TempDir())

		_, err := requireRepoRoot(resolver)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, errNotGitRepository) {
			t.Fatalf("requireRepoRoot error = %v, want not git repository", err)
		}
	})

	t.Run("surfaces resolver errors", func(t *testing.T) {
		resolver := &stubRepoResolver{}
		resolver.SetGitError(errors.New(errGitStub))
		resolver.SetWorkingDirError(errors.New(errCwdStub))

		_, err := requireRepoRoot(resolver)
		assertErrorContains(t, err, "determine repository root: "+errCwdStub)
	})
}
func TestGetValueForScopeMergedPrefersLocal(t *testing.T) {
	env := setupConfigEnv(t, "review_agent = \"global-agent\"\n", "review_agent = \"local-agent\"\n")

	nestedDir := filepath.Join(env.RepoDir, "a", "b")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}

	env.Resolver.SetGitError(errors.New(errGitStub))
	env.Resolver.SetWorkingDir(nestedDir)

	got, err := getValueForScope(env.Resolver, "review_agent", scopeMerged)
	if err != nil {
		t.Fatalf("getValueForScope returned error: %v", err)
	}
	if got != "local-agent" {
		t.Fatalf("getValueForScope = %q, want %q", got, "local-agent")
	}
}
func TestGetValueForScopeMergedRepoResolutionError(t *testing.T) {
	env := setupConfigEnv(t, "", "")

	env.Resolver.SetGitError(errors.New(errGitStub))
	env.Resolver.SetWorkingDirError(errors.New(errCwdStub))

	_, err := getValueForScope(env.Resolver, "review_agent", scopeMerged)
	assertErrorContains(t, err, "determine repository root: "+errCwdStub)
}
func TestListMergedConfigRepoResolutionError(t *testing.T) {
	env := setupConfigEnv(t, "", "")

	env.Resolver.SetGitError(errors.New(errGitStub))
	env.Resolver.SetWorkingDirError(errors.New(errCwdStub))

	err := listMergedConfig(env.Resolver, false)
	assertErrorContains(t, err, "determine repository root: "+errCwdStub)
}
func TestSetConfigKey(t *testing.T) {
	path := setupConfigFile(t)

	tests := []struct {
		name     string
		key      string
		val      string
		expected any
	}{
		{"String", "default_agent", "gemini", "gemini"},
		{"Integer", "max_workers", "8", int64(8)},
		{"Boolean", "sync.enabled", "true", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := setConfigKey(path, tt.key, tt.val, true); err != nil {
				t.Fatalf("setConfigKey: %v", err)
			}
			assertConfigValue(t, path, tt.key, tt.expected)
		})
	}

	t.Run("Persistence", func(t *testing.T) {
		// Previous values should still be present after multiple sets.
		assertConfigValue(t, path, "default_agent", "gemini")
		assertConfigValue(t, path, "max_workers", int64(8))
		assertConfigValue(t, path, "sync.enabled", true)
	})
}

func TestSetConfigKeyNestedCreation(t *testing.T) {
	path := setupConfigFile(t)

	if err := setConfigKey(path, "ci.poll_interval", "10m", true); err != nil {
		t.Fatalf("setConfigKey nested: %v", err)
	}
	assertConfigValue(t, path, "ci.poll_interval", "10m")
}

func TestSetConfigKeyInvalidKey(t *testing.T) {
	path := setupConfigFile(t)

	err := setConfigKey(path, "nonexistent_key", "value", true)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestSetConfigKeySlice(t *testing.T) {
	path := setupConfigFile(t)

	if err := setConfigKey(path, "ci.repos", "org/repo1,org/repo2", true); err != nil {
		t.Fatalf("setConfigKey slice: %v", err)
	}

	raw := readTOML(t, path)
	repos, ok := getNestedValue(t, raw, "ci.repos").([]any)
	if !ok {
		t.Fatalf("ci.repos is not a slice: %v (%T)", getNestedValue(t, raw, "ci.repos"), getNestedValue(t, raw, "ci.repos"))
	}
	if len(repos) != 2 {
		t.Errorf("ci.repos length = %d, want 2", len(repos))
	}
}

func TestSetConfigKeySliceEmpty(t *testing.T) {
	path := setupConfigFile(t)

	// Seed with a non-empty slice first.
	if err := setConfigKey(path, "ci.repos", "org/repo1,org/repo2", true); err != nil {
		t.Fatalf("setConfigKey seed: %v", err)
	}

	// Clear the slice by setting it to an empty string.
	if err := setConfigKey(path, "ci.repos", "", true); err != nil {
		t.Fatalf("setConfigKey empty: %v", err)
	}

	raw := readTOML(t, path)
	repos := getNestedValue(t, raw, "ci.repos")
	slice, ok := repos.([]any)
	if !ok {
		t.Fatalf("ci.repos is not a slice after clearing: %v (%T)", repos, repos)
	}
	if len(slice) != 0 {
		t.Errorf("ci.repos length = %d, want 0", len(slice))
	}
}

func TestSetConfigKeyRepoConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".roborev.toml")

	if err := setConfigKey(path, "agent", "claude-code", false); err != nil {
		t.Fatalf("setConfigKey repo: %v", err)
	}
	assertConfigValue(t, path, "agent", "claude-code")
}

func TestSetRawMapKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  any
		path string // dot-path to check in the resulting map
		want any
	}{
		{
			name: "SimpleKey",
			key:  "foo",
			val:  "bar",
			path: "foo",
			want: "bar",
		},
		{
			name: "NestedKey",
			key:  "a.b.c",
			val:  42,
			path: "a.b.c",
			want: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := make(map[string]any)
			setRawMapKey(m, tt.key, tt.val)
			got := getNestedValue(t, m, tt.path)
			if got != tt.want {
				t.Errorf("%s = %v (%T), want %v (%T)", tt.path, got, got, tt.want, tt.want)
			}
		})
	}
}

func TestGetValueForScopeMergedMalformedLocalConfig(t *testing.T) {
	env := setupConfigEnv(t, "review_agent = \"global-agent\"\n", "invalid toml [[[")

	_, err := getValueForScope(env.Resolver, "review_agent", scopeMerged)
	assertErrorContains(t, err, "load repo config")
}
func TestListMergedConfigMalformedLocalConfig(t *testing.T) {
	env := setupConfigEnv(t, "", "invalid toml [[[")

	err := listMergedConfig(env.Resolver, false)
	assertErrorContains(t, err, "load repo config")
}
func TestListGlobalConfigExplicitKeys(t *testing.T) {
	setupConfigEnv(t, strings.Join([]string{
		`max_workers = 4`,
		`review_context_count = 0`,
		``,
		`[sync]`,
		`enabled = false`,
	}, "\n")+"\n", "")

	// Capture stdout
	output := captureOutput(t, listGlobalConfig)
	// Explicit default-valued key should be shown
	if !strings.Contains(output, "max_workers=4") {
		t.Errorf("output should include explicit default-valued key max_workers=4, got:\n%s", output)
	}

	// Explicit zero key should be shown
	if !strings.Contains(output, "review_context_count=0") {
		t.Errorf("output should include explicit zero key review_context_count=0, got:\n%s", output)
	}

	// Explicit false key should be shown
	if !strings.Contains(output, "sync.enabled=false") {
		t.Errorf("output should include explicit false key sync.enabled=false, got:\n%s", output)
	}

	// Non-explicit default key (default_agent) should NOT be shown
	if strings.Contains(output, "default_agent=") {
		t.Errorf("output should NOT include non-explicit default_agent, got:\n%s", output)
	}
}

func TestListLocalConfigExplicitKeys(t *testing.T) {
	env := setupConfigEnv(t, "", strings.Join([]string{
		`agent = "claude-code"`,
		`review_context_count = 0`,
	}, "\n")+"\n")

	// Capture stdout
	output := captureOutput(t, func() error {
		return listLocalConfig(env.Resolver)
	})
	// Explicit key should be shown
	if !strings.Contains(output, "agent=claude-code") {
		t.Errorf("output should include explicit agent=claude-code, got:\n%s", output)
	}

	// Explicit zero key should be shown
	if !strings.Contains(output, "review_context_count=0") {
		t.Errorf("output should include explicit zero review_context_count=0, got:\n%s", output)
	}

	// Non-explicit keys should NOT be shown (review_guidelines was not set)
	if strings.Contains(output, "review_guidelines=") {
		t.Errorf("output should NOT include non-explicit review_guidelines, got:\n%s", output)
	}
}

func TestGetValueForScopeMergedRepoOnlyKeyNotSet(t *testing.T) {
	env := setupConfigEnv(t, "default_agent = \"codex\"\n", "")

	// No repo (no .git dir)
	env.Resolver.SetGitError(errors.New(errGitStub))
	env.Resolver.SetWorkingDir(t.TempDir())

	// "agent" is a repo-only key — should not fall through to global config
	_, err := getValueForScope(env.Resolver, "agent", scopeMerged)
	assertErrorContains(t, err, "not set in local config")
}
