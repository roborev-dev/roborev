// Package testenv provides environment isolation helpers for tests.
// This package intentionally has no dependencies on other internal packages
// to avoid import cycles.
package testenv

import (
	"os"
	"testing"
)

// SetDataDir sets ROBOREV_DATA_DIR to a temp directory to isolate tests
// from production ~/.roborev. This is preferred over setting HOME because
// ROBOREV_DATA_DIR takes precedence in config.DataDir(). Returns the temp
// directory path. Cleanup is automatic via t.Cleanup.
func SetDataDir(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)

	t.Cleanup(func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	})

	return tmpDir
}
