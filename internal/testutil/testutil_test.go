package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func findBaselineCommand(t *testing.T) string {
	t.Helper()
	// Find a command that exists before isolation so we can
	// verify it becomes unreachable afterward. Try several
	// candidates to handle both Unix and Windows.
	candidates := []string{"ls", "cat", "echo", "cmd", "whoami"}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	t.Log("no baseline command found in PATH; cannot verify isolation")
	t.SkipNow()
	return "" // unreachable, but required by compiler
}

func TestMockExecutableIsolated(t *testing.T) {
	baseline := findBaselineCommand(t)

	MockExecutableIsolated(t, "my-mock-tool", 0)

	if _, err := exec.LookPath("my-mock-tool"); err != nil {
		t.Errorf("expected to find my-mock-tool in PATH, got: %v", err)
	}

	if _, err := exec.LookPath(baseline); err == nil {
		t.Errorf("expected %q to be absent from isolated PATH, but it was found", baseline)
	}
}

func TestNewGitRepoSuppressesHooks(t *testing.T) {
	repo := NewGitRepo(t)

	out, err := newIsolatedGitCmd(repo.dir, "config", "--local", "--get", "core.hooksPath").Output()
	if err != nil {
		t.Fatalf("git config --local --get core.hooksPath: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != os.DevNull {
		t.Fatalf("core.hooksPath = %q, want %q", got, os.DevNull)
	}

	repo.CommitFile("file.txt", "content", "initial commit")
}

func TestGitPropagationBlocked(t *testing.T) {
	// Setup a malicious hook that writes to a known file
	hookDir := t.TempDir()
	flagFile := hookDir + "/hook_ran"

	// Create a pre-commit hook
	hookPath := hookDir + "/pre-commit"
	var hookContent string
	if os.PathSeparator == '\\' {
		// Windows
		hookPath += ".bat"
		hookContent = "@echo off\r\necho ran > " + flagFile + "\r\n"
	} else {
		// Unix
		hookContent = "#!/bin/sh\ntouch " + flagFile + "\n"
	}
	if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
		t.Fatalf("failed to create hook: %v", err)
	}

	// Set git config propagation variables to try and force our hook dir
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", hookDir)

	// Create repo and do a commit
	repo := NewGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	// Verify the hook did not run
	if _, err := os.Stat(flagFile); err == nil {
		t.Fatalf("hook was executed, GIT_CONFIG_* variables were not stripped")
	}
}
