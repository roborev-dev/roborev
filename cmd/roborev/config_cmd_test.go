package main

import (
	"errors"
	"fmt"
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

func readTOML(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw := make(map[string]interface{})
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		t.Fatalf("read TOML %s: %v", path, err)
	}
	return raw
}

// getNestedValue traverses a dot-separated key path in a nested map.
func getNestedValue(t *testing.T, raw map[string]interface{}, dotKey string) interface{} {
	t.Helper()
	parts := strings.Split(dotKey, ".")
	var current interface{} = raw
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			t.Fatalf("key %q: expected map at %q, got %T", dotKey, part, current)
		}
		current = m[part]
	}
	return current
}

func assertConfigValue(t *testing.T, path, dotKey string, expected interface{}) {
	t.Helper()
	raw := readTOML(t, path)
	val := getNestedValue(t, raw, dotKey)
	if val != expected {
		t.Errorf("%s = %v (%T), want %v (%T)", dotKey, val, val, expected, expected)
	}
}

func stubRepoRootResolution(t *testing.T, gitFn func(string) (string, error), wdFn func() (string, error)) {
	t.Helper()
	oldGit := repoRootFromGit
	oldWD := currentWorkingDir

	if gitFn != nil {
		repoRootFromGit = gitFn
	}
	if wdFn != nil {
		currentWorkingDir = wdFn
	}

	t.Cleanup(func() {
		repoRootFromGit = oldGit
		currentWorkingDir = oldWD
	})
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
		stubRepoRootResolution(t,
			func(path string) (string, error) {
				return "/tmp/from-git", nil
			},
			nil,
		)

		got, err := repoRoot()
		if err != nil {
			t.Fatalf("repoRoot returned error: %v", err)
		}
		if got != "/tmp/from-git" {
			t.Fatalf("repoRoot = %q, want %q", got, "/tmp/from-git")
		}
	})

	t.Run("falls back to filesystem when git resolver fails", func(t *testing.T) {
		repoDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0755); err != nil {
			t.Fatalf("create .git dir: %v", err)
		}
		nestedDir := filepath.Join(repoDir, "nested", "deeper")
		if err := os.MkdirAll(nestedDir, 0755); err != nil {
			t.Fatalf("create nested dir: %v", err)
		}

		stubRepoRootResolution(t,
			func(path string) (string, error) {
				return "", fmt.Errorf("git unavailable")
			},
			func() (string, error) {
				return nestedDir, nil
			},
		)

		got, err := repoRoot()
		if err != nil {
			t.Fatalf("repoRoot returned error: %v", err)
		}
		if got != repoDir {
			t.Fatalf("repoRoot = %q, want %q", got, repoDir)
		}
	})

	t.Run("optional lookup returns empty when not in repo", func(t *testing.T) {
		workDir := t.TempDir()
		stubRepoRootResolution(t,
			func(path string) (string, error) {
				return "", fmt.Errorf("git unavailable")
			},
			func() (string, error) {
				return workDir, nil
			},
		)

		got, err := repoRoot()
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
		workDir := t.TempDir()
		stubRepoRootResolution(t,
			func(path string) (string, error) {
				return "", fmt.Errorf("git unavailable")
			},
			func() (string, error) {
				return workDir, nil
			},
		)

		_, err := requireRepoRoot()
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, errNotGitRepository) {
			t.Fatalf("requireRepoRoot error = %v, want not git repository", err)
		}
	})

	t.Run("surfaces resolver errors", func(t *testing.T) {
		stubRepoRootResolution(t,
			func(path string) (string, error) {
				return "", fmt.Errorf("git unavailable")
			},
			func() (string, error) {
				return "", fmt.Errorf("cwd failed")
			},
		)

		_, err := requireRepoRoot()
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "determine repository root: cwd failed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestGetValueForScopeMergedPrefersLocal(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dataDir)

	globalPath := filepath.Join(dataDir, "config.toml")
	globalTOML := "review_agent = \"global-agent\"\n"
	if err := os.WriteFile(globalPath, []byte(globalTOML), 0644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	repoDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatalf("create .git dir: %v", err)
	}
	localPath := filepath.Join(repoDir, ".roborev.toml")
	localTOML := "review_agent = \"local-agent\"\n"
	if err := os.WriteFile(localPath, []byte(localTOML), 0644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	nestedDir := filepath.Join(repoDir, "a", "b")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}

	stubRepoRootResolution(t,
		func(path string) (string, error) {
			return "", fmt.Errorf("git unavailable")
		},
		func() (string, error) {
			return nestedDir, nil
		},
	)

	got, err := getValueForScope("review_agent", scopeMerged)
	if err != nil {
		t.Fatalf("getValueForScope returned error: %v", err)
	}
	if got != "local-agent" {
		t.Fatalf("getValueForScope = %q, want %q", got, "local-agent")
	}
}

func TestGetValueForScopeMergedRepoResolutionError(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dataDir)

	stubRepoRootResolution(t,
		func(path string) (string, error) {
			return "", fmt.Errorf("git unavailable")
		},
		func() (string, error) {
			return "", fmt.Errorf("cwd failed")
		},
	)

	_, err := getValueForScope("review_agent", scopeMerged)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "determine repository root: cwd failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListMergedConfigRepoResolutionError(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dataDir)

	stubRepoRootResolution(t,
		func(path string) (string, error) {
			return "", fmt.Errorf("git unavailable")
		},
		func() (string, error) {
			return "", fmt.Errorf("cwd failed")
		},
	)

	err := listMergedConfig(false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "determine repository root: cwd failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetConfigKey(t *testing.T) {
	path := setupConfigFile(t)

	t.Run("String", func(t *testing.T) {
		if err := setConfigKey(path, "default_agent", "gemini", true); err != nil {
			t.Fatalf("setConfigKey: %v", err)
		}
		assertConfigValue(t, path, "default_agent", "gemini")
	})

	t.Run("Integer", func(t *testing.T) {
		if err := setConfigKey(path, "max_workers", "8", true); err != nil {
			t.Fatalf("setConfigKey: %v", err)
		}
		assertConfigValue(t, path, "max_workers", int64(8))
	})

	t.Run("Boolean", func(t *testing.T) {
		if err := setConfigKey(path, "sync.enabled", "true", true); err != nil {
			t.Fatalf("setConfigKey: %v", err)
		}
		assertConfigValue(t, path, "sync.enabled", true)
	})

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
	repos, ok := getNestedValue(t, raw, "ci.repos").([]interface{})
	if !ok {
		t.Fatalf("ci.repos is not a slice: %v (%T)", getNestedValue(t, raw, "ci.repos"), getNestedValue(t, raw, "ci.repos"))
	}
	if len(repos) != 2 {
		t.Errorf("ci.repos length = %d, want 2", len(repos))
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
		val  interface{}
		path string // dot-path to check in the resulting map
		want interface{}
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
			m := make(map[string]interface{})
			setRawMapKey(m, tt.key, tt.val)
			got := getNestedValue(t, m, tt.path)
			if got != tt.want {
				t.Errorf("%s = %v (%T), want %v (%T)", tt.path, got, got, tt.want, tt.want)
			}
		})
	}
}
