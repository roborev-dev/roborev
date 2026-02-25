// Package testenv provides environment isolation helpers for tests.
// This package intentionally has no dependencies on other internal packages
// to avoid import cycles.
package testenv

import (
	"fmt"
	"os"
	"testing"
)

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

	origEnv, hasEnv := os.LookupEnv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if hasEnv {
			os.Setenv("ROBOREV_DATA_DIR", origEnv)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

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
