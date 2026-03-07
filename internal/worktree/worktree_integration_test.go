//go:build integration

package worktree_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
	"github.com/roborev-dev/roborev/internal/worktree"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func assertPathNotExists(t *testing.T, path string, msg string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s: path %s still exists", msg, path)
	}
}

func assertPathExists(t *testing.T, path string, msg string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s: path %s should exist: %v", msg, path, err)
	}
}

func setupRepoWithSubmodule(t *testing.T) *testutil.TestRepo {
	t.Helper()
	subRepo := testutil.NewTestRepo(t)
	subRepo.RunGit("init")
	subRepo.SymbolicRef("HEAD", "refs/heads/main")
	subRepo.Config("user.email", testutil.GitUserEmail)
	subRepo.Config("user.name", testutil.GitUserName)
	subRepo.CommitFile("sub.txt", "sub", "submodule commit")

	mainRepo := testutil.NewTestRepo(t)
	mainRepo.RunGit("init")
	mainRepo.SymbolicRef("HEAD", "refs/heads/main")
	mainRepo.Config("user.email", testutil.GitUserEmail)
	mainRepo.Config("user.name", testutil.GitUserName)
	mainRepo.Config("protocol.file.allow", "always")
	mainRepo.RunGit("-c", "protocol.file.allow=always", "submodule", "add", subRepo.Root, "deps/sub")
	mainRepo.RunGit("commit", "-m", "add submodule")

	return mainRepo
}

func TestWorktreeCleanupBetweenIterations(t *testing.T) {
	requireGit(t)

	repo := testutil.InitTestRepo(t)

	// Simulate the refine loop pattern: create a worktree, then clean it up
	// before the next iteration. Verify the directory is removed each time.
	var prevPath string
	for i := range 3 {
		wt, err := worktree.Create(repo.Root, "HEAD")
		if err != nil {
			t.Fatalf("iteration %d: worktree.Create failed: %v", i, err)
		}

		// Verify previous worktree was cleaned up
		if prevPath != "" {
			assertPathNotExists(t, prevPath, fmt.Sprintf("iteration %d: previous worktree cleanup", i))
		}

		// Verify current worktree exists
		assertPathExists(t, wt.Dir, fmt.Sprintf("iteration %d: current worktree", i))

		// Simulate the explicit cleanup call (as done on error/no-change paths)
		wt.Close()
		prevPath = wt.Dir
	}

	// Verify the last worktree was also cleaned up
	assertPathNotExists(t, prevPath, "last worktree cleanup")
}

func TestCreateTempWorktreeIgnoresHooks(t *testing.T) {
	requireGit(t)

	repo := testutil.InitTestRepo(t)

	repo.WriteNamedHook("post-checkout", "#!/bin/sh\nexit 1\n")

	// Verify the hook is active (a normal worktree add would fail)
	failDir := t.TempDir()
	cmd := exec.Command("git", "-C", repo.Root, "worktree", "add", "--detach", failDir, "HEAD")
	if out, err := cmd.CombinedOutput(); err == nil {
		// Clean up the worktree before failing
		exec.Command("git", "-C", repo.Root, "worktree", "remove", "--force", failDir).Run()
		// Some git versions don't fail on post-checkout hook errors.
		// In that case, verify our approach still succeeds.
		_ = out
	}

	// worktree.Create should succeed because it suppresses hooks
	wt, err := worktree.Create(repo.Root, "HEAD")
	if err != nil {
		t.Fatalf("worktree.Create should succeed with failing hook: %v", err)
	}
	defer wt.Close()

	// Verify the worktree directory exists and has the file from the repo
	assertPathExists(t, wt.Dir, "worktree directory")

	baseFile := filepath.Join(wt.Dir, "base.txt")
	content, err := os.ReadFile(baseFile)
	if err != nil {
		t.Fatalf("expected base.txt in worktree: %v", err)
	}
	if string(content) != "base" {
		t.Errorf("expected content 'base', got %q", string(content))
	}
}

func TestCreateTempWorktreeInitializesSubmodules(t *testing.T) {
	requireGit(t)
	mainRepo := setupRepoWithSubmodule(t)

	wt, err := worktree.Create(mainRepo.Root, "HEAD")
	if err != nil {
		t.Fatalf("worktree.Create failed: %v", err)
	}
	defer wt.Close()

	assertPathExists(t, filepath.Join(wt.Dir, "deps", "sub", "sub.txt"), "submodule file")
}
