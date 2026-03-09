package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// M is a shorthand type for map[string]string to keep test tables compact
type M = map[string]string

// writeRepoConfigStr writes a TOML string to .roborev.toml in the given directory.
func writeRepoConfigStr(t *testing.T, dir, content string) {
	t.Helper()
	writeTestFile(t, dir, ".roborev.toml", content)
}

// writeTestFile writes content to a file in the given directory.
func writeTestFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644)
	require.NoError(t, err, "failed to write %s", filename)
}
