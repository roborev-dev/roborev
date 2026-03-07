// Package testenv provides environment isolation helpers for tests.
// This package intentionally has no dependencies on other internal packages
// to avoid import cycles.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type envSnapshot struct {
	value string
	ok    bool
}

func snapshotEnv(keys ...string) map[string]envSnapshot {
	snapshot := make(map[string]envSnapshot, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		snapshot[key] = envSnapshot{value: value, ok: ok}
	}
	return snapshot
}

func restoreEnv(snapshot map[string]envSnapshot) {
	for key, state := range snapshot {
		if state.ok {
			_ = os.Setenv(key, state.value)
			continue
		}
		_ = os.Unsetenv(key)
	}
}

func applyIsolatedProcessEnv(dataDir string) (func(), error) {
	homeDir := filepath.Join(dataDir, "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		return nil, err
	}

	gitConfigPath := filepath.Join(homeDir, ".gitconfig")
	if err := os.WriteFile(gitConfigPath, nil, 0644); err != nil {
		return nil, err
	}

	updates := map[string]string{
		"ROBOREV_DATA_DIR":    dataDir,
		"HOME":                homeDir,
		"GIT_CONFIG_GLOBAL":   gitConfigPath,
		"GIT_CONFIG_NOSYSTEM": "1",
	}
	snapshot := snapshotEnv(
		"ROBOREV_DATA_DIR",
		"HOME",
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_NOSYSTEM",
	)
	for key, value := range updates {
		if err := os.Setenv(key, value); err != nil {
			restoreEnv(snapshot)
			return nil, err
		}
	}

	return func() { restoreEnv(snapshot) }, nil
}

// RunIsolatedMain provides a standardized TestMain execution wrapper that
// isolates tests from the production ~/.roborev directory. It safely manages
// environment variables and preserves the original test exit code.
func RunIsolatedMain(m *testing.M) int {
	// Snapshot prod log state BEFORE overriding ROBOREV_DATA_DIR.
	barrier := NewProdLogBarrier(DefaultProdDataDir())

	tmpDir, err := os.MkdirTemp("", "roborev-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	restore, err := applyIsolatedProcessEnv(tmpDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to isolate test environment: %v\n", err)
		return 1
	}
	defer restore()

	code := m.Run()

	// Hard barrier: fail if tests polluted production logs.
	if msg := barrier.Check(); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		if code == 0 {
			return 1
		}
	}
	return code
}

// SetDataDir sets ROBOREV_DATA_DIR to a temp directory to isolate tests
// from production ~/.roborev. This is preferred over setting HOME because
// ROBOREV_DATA_DIR takes precedence in config.DataDir(). Returns the temp
// directory path. Cleanup is automatic via t.Setenv.
//
// Note: t.Setenv marks the test as incompatible with t.Parallel(), which is
// appropriate since environment variables are process-global state.
func SetDataDir(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	return tmpDir
}

// SetDataDirWithPath sets ROBOREV_DATA_DIR to a specific directory to isolate tests
// from production ~/.roborev. Cleanup is automatic via t.Setenv.
// Returns the directory path.
func SetDataDirWithPath(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("ROBOREV_DATA_DIR", dir)
	return dir
}
