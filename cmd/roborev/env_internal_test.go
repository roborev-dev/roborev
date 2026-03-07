package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGitTestEnvDedup(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/inherited/global/config")
	t.Setenv("git_config_nosystem", "0")
	t.Setenv("home", "/inherited/home")

	dir := t.TempDir()
	env := gitTestEnv(dir)

	counts := make(map[string]int)
	envMap := make(map[string]string)

	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			key := strings.ToUpper(parts[0]) // Case insensitive map
			counts[key]++
			envMap[key] = parts[1]
		}
	}

	for _, k := range []string{"HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
		if counts[k] != 1 {
			t.Errorf("Expected exactly 1 instance of %s, got %d", k, counts[k])
		}
	}

	if got := envMap["GIT_CONFIG_GLOBAL"]; got != filepath.Join(dir, ".gitconfig") {
		t.Errorf("GIT_CONFIG_GLOBAL = %q; want %q", got, filepath.Join(dir, ".gitconfig"))
	}
	if got := envMap["GIT_CONFIG_NOSYSTEM"]; got != "1" {
		t.Errorf("GIT_CONFIG_NOSYSTEM = %q; want %q", got, "1")
	}
	if got := envMap["HOME"]; got != dir {
		t.Errorf("HOME = %q; want %q", got, dir)
	}
}
