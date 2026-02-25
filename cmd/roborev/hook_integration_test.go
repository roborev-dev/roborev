//go:build integration

package main

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/githook"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func setupHookTest(t *testing.T) *testutil.TestRepo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	repo := testutil.NewTestRepo(t)

	t.Cleanup(testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n"))
	t.Cleanup(repo.Chdir())

	return repo
}

func runInitCmd(t *testing.T) {
	t.Helper()
	cmd := initCmd()
	cmd.SetArgs([]string{"--agent", "test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command failed: %v", err)
	}
}

func readHookContent(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read hook at %s: %v", path, err)
	}
	return string(content)
}

const legacyV2HookSnippet = `
# roborev post-commit hook v2 - auto-reviews every commit
ROBOREV="/usr/local/bin/roborev"
if [ ! -x "$ROBOREV" ]; then
    ROBOREV=$(command -v roborev 2>/dev/null)
    [ -z "$ROBOREV" ] || [ ! -x "$ROBOREV" ] && exit 0
fi
"$ROBOREV" enqueue --quiet 2>/dev/null
`

func assertNotContains(t *testing.T, content, substr, msg string) {
	t.Helper()
	if strings.Contains(content, substr) {
		t.Errorf("%s. Expected NOT to find %q in:\n%s", msg, substr, content)
	}
}

func TestInitCmdCreatesHooksDirectory(t *testing.T) {
	repo := setupHookTest(t)
	repo.RemoveHooksDir()

	if _, err := os.Stat(repo.HooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	runInitCmd(t)

	if _, err := os.Stat(repo.HooksDir); os.IsNotExist(err) {
		t.Error("hooks directory was not created")
	}

	if _, err := os.Stat(repo.HookPath); os.IsNotExist(err) {
		t.Error("post-commit hook was not created")
	}

	info, err := os.Stat(repo.HookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("post-commit hook is not executable")
	}
}

func TestInitCmdUpgradesOutdatedHook(t *testing.T) {
	repo := setupHookTest(t)

	// Write a realistic old-style hook (v2 format)
	oldHook := "#!/bin/sh\n" + legacyV2HookSnippet
	repo.WriteHook(oldHook)

	runInitCmd(t)

	contentStr := readHookContent(t, repo.HookPath)
	assertContains(t, contentStr, githook.PostCommitVersionMarker, "upgraded hook should contain v3 marker")
	assertNotContains(t, contentStr, "hook v2", "upgraded hook should not contain old v2 marker")
	assertContains(t, contentStr, "enqueue --quiet", "upgraded hook should have enqueue line")
}

func TestInitCmdPreservesOtherHooksOnUpgrade(t *testing.T) {
	repo := setupHookTest(t)

	// Mixed hook: user content + old v2 roborev snippet
	oldHook := "#!/bin/sh\n" +
		"echo 'my custom hook'\n" +
		legacyV2HookSnippet
	repo.WriteHook(oldHook)

	runInitCmd(t)

	contentStr := readHookContent(t, repo.HookPath)

	assertContains(t, contentStr, "echo 'my custom hook'", "upgrade should preserve non-roborev lines")
	assertContains(t, contentStr, githook.PostCommitVersionMarker, "upgrade should contain v3 marker")
	assertNotContains(t, contentStr, "hook v2", "upgrade should remove old v2 marker")
}

func TestInitCmdEarlyExitHookStillRunsRoborev(t *testing.T) {
	repo := setupHookTest(t)

	// Husky-style hook with exit 0 at the end
	huskyHook := "#!/bin/sh\n" +
		". \"$(dirname \"$0\")/_/husky.sh\"\n" +
		"npx lint-staged\n" +
		"exit 0\n"
	repo.WriteHook(huskyHook)

	runInitCmd(t)

	contentStr := readHookContent(t, repo.HookPath)

	// Roborev snippet should appear before exit 0
	snippetIdx := strings.Index(contentStr, "_roborev_hook")
	exitIdx := strings.Index(contentStr, "exit 0")
	if snippetIdx < 0 {
		t.Fatal("roborev snippet should be present")
	}
	if exitIdx < 0 {
		t.Fatal("exit 0 should be preserved")
	}
	if snippetIdx > exitIdx {
		t.Error("roborev snippet should appear before exit 0")
	}

	// All original content should be preserved
	assertContains(t, contentStr, "husky.sh", "husky.sh reference should be preserved")
	assertContains(t, contentStr, "npx lint-staged", "lint-staged command should be preserved")

	// Post-rewrite hook should also be installed
	prContent := readHookContent(t, repo.GetHookPath("post-rewrite"))
	assertContains(t, prContent, githook.PostRewriteVersionMarker, "post-rewrite hook should have version marker")
}
