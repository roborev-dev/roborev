package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func writeTempCommand(t *testing.T, script string) string {
	t.Helper()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cmd")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write temp command: %v", err)
	}
	return path
}
