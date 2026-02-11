//go:build integration

package main

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestInitCmdCreatesHooksDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}

	// Override HOME to prevent reading real daemon.json
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := testutil.NewTestRepo(t)
	repo.RemoveHooksDir()

	// Verify hooks directory doesn't exist
	if _, err := os.Stat(repo.HooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	// Create a fake roborev binary in PATH
	defer testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n")()

	defer repo.Chdir()()

	// Build init command
	initCommand := initCmd()
	initCommand.SetArgs([]string{"--agent", "test"})
	err := initCommand.Execute()

	// Should succeed (not fail with "no such file or directory")
	if err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	// Verify hooks directory was created
	if _, err := os.Stat(repo.HooksDir); os.IsNotExist(err) {
		t.Error("hooks directory was not created")
	}

	// Verify hook file was created
	if _, err := os.Stat(repo.HookPath); os.IsNotExist(err) {
		t.Error("post-commit hook was not created")
	}

	// Verify hook is executable
	info, err := os.Stat(repo.HookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("post-commit hook is not executable")
	}
}

func TestInitCmdUpgradesOutdatedHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}

	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := testutil.NewTestRepo(t)

	// Write a realistic old-style hook (no version marker, has &, includes if/fi block)
	oldHook := "#!/bin/sh\n# roborev post-commit hook - auto-reviews every commit\nROBOREV=\"/usr/local/bin/roborev\"\nif [ ! -x \"$ROBOREV\" ]; then\n    ROBOREV=$(command -v roborev 2>/dev/null)\n    [ -z \"$ROBOREV\" ] || [ ! -x \"$ROBOREV\" ] && exit 0\nfi\n\"$ROBOREV\" enqueue --quiet 2>/dev/null &\n"
	repo.WriteHook(oldHook)

	defer testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n")()
	defer repo.Chdir()()

	initCommand := initCmd()
	initCommand.SetArgs([]string{"--agent", "test"})
	err := initCommand.Execute()
	if err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	content, err := os.ReadFile(repo.HookPath)
	if err != nil {
		t.Fatalf("Failed to read hook: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "hook v2") {
		t.Error("upgraded hook should contain version marker")
	}
	if strings.Contains(contentStr, "2>/dev/null &") {
		t.Error("upgraded hook should not have backgrounded enqueue")
	}
	if !strings.Contains(contentStr, "2>/dev/null\n") {
		t.Error("upgraded hook should still have enqueue line (without &)")
	}
	// Verify the if/fi block is preserved intact (no stray fi)
	if !strings.Contains(contentStr, "if [ ! -x") {
		t.Error("upgraded hook should preserve the if block")
	}
}

func TestInitCmdPreservesOtherHooksOnUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}

	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := testutil.NewTestRepo(t)

	oldHook := "#!/bin/sh\necho 'my custom hook'\n# roborev post-commit hook - auto-reviews every commit\nROBOREV=\"/usr/local/bin/roborev\"\nif [ ! -x \"$ROBOREV\" ]; then\n    ROBOREV=$(command -v roborev 2>/dev/null)\n    [ -z \"$ROBOREV\" ] || [ ! -x \"$ROBOREV\" ] && exit 0\nfi\n\"$ROBOREV\" enqueue --quiet 2>/dev/null &\n"
	repo.WriteHook(oldHook)

	defer testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n")()
	defer repo.Chdir()()

	initCommand := initCmd()
	initCommand.SetArgs([]string{"--agent", "test"})
	err := initCommand.Execute()
	if err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	content, err := os.ReadFile(repo.HookPath)
	if err != nil {
		t.Fatalf("Failed to read hook: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "echo 'my custom hook'") {
		t.Error("upgrade should preserve non-roborev lines")
	}
	if !strings.Contains(contentStr, "hook v2") {
		t.Error("upgrade should contain version marker")
	}
	if strings.Contains(contentStr, "2>/dev/null &") {
		t.Error("upgrade should remove trailing &")
	}
}
