package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyIsolatedProcessEnvOverridesAndRestores(t *testing.T) {
	t.Setenv("ROBOREV_DATA_DIR", "/orig/data")
	t.Setenv("HOME", "/orig/home")
	t.Setenv("GIT_CONFIG_GLOBAL", "/orig/gitconfig")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")

	dataDir := t.TempDir()
	restore, err := applyIsolatedProcessEnv(dataDir)
	if err != nil {
		t.Fatalf("applyIsolatedProcessEnv: %v", err)
	}

	homeDir := filepath.Join(dataDir, "home")
	if got := os.Getenv("ROBOREV_DATA_DIR"); got != dataDir {
		t.Fatalf("ROBOREV_DATA_DIR = %q, want %q", got, dataDir)
	}
	if got := os.Getenv("HOME"); got != homeDir {
		t.Fatalf("HOME = %q, want %q", got, homeDir)
	}
	if got := os.Getenv("GIT_CONFIG_GLOBAL"); got != filepath.Join(homeDir, ".gitconfig") {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q", got)
	}
	if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q, want %q", got, "1")
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".gitconfig")); err != nil {
		t.Fatalf("stat isolated git config: %v", err)
	}

	restore()

	if got := os.Getenv("ROBOREV_DATA_DIR"); got != "/orig/data" {
		t.Fatalf("ROBOREV_DATA_DIR after restore = %q", got)
	}
	if got := os.Getenv("HOME"); got != "/orig/home" {
		t.Fatalf("HOME after restore = %q", got)
	}
	if got := os.Getenv("GIT_CONFIG_GLOBAL"); got != "/orig/gitconfig" {
		t.Fatalf("GIT_CONFIG_GLOBAL after restore = %q", got)
	}
	if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "0" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM after restore = %q", got)
	}
}
