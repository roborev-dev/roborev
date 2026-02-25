package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// M is a shorthand type for map[string]string to keep test tables compact
type M = map[string]string

// newTempRepo creates a temp directory and writes content to .roborev.toml.
func newTempRepo(t *testing.T, configContent string) string {
	t.Helper()
	dir := t.TempDir()
	if configContent != "" {
		writeRepoConfigStr(t, dir, configContent)
	}
	return dir
}

// writeRepoConfigStr writes a TOML string to .roborev.toml in the given directory.
func writeRepoConfigStr(t *testing.T, dir, content string) {
	t.Helper()
	writeTestFile(t, dir, ".roborev.toml", content)
}

func writeRepoConfig(t *testing.T, dir string, cfg map[string]string) {
	t.Helper()
	if cfg == nil {
		return
	}
	var sb strings.Builder
	if err := toml.NewEncoder(&sb).Encode(cfg); err != nil {
		t.Fatalf("failed to encode repo config: %v", err)
	}
	writeRepoConfigStr(t, dir, sb.String())
}

// writeTestFile writes content to a file in the given directory.
func writeTestFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write %s: %v", filename, err)
	}
}

// execGit executes a git command in the given directory and returns its output.
func execGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			t.Fatalf("git %v failed: %v\nstderr: %s", args, err, exitError.Stderr)
		}
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
