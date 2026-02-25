//go:build integration

package worktree_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
	"github.com/roborev-dev/roborev/internal/worktree"
)

func TestWorktreeCleanupBetweenIterations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

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
			if _, err := os.Stat(prevPath); !os.IsNotExist(err) {
				t.Fatalf("iteration %d: previous worktree %s still exists after cleanup", i, prevPath)
			}
		}

		// Verify current worktree exists
		if _, err := os.Stat(wt.Dir); err != nil {
			t.Fatalf("iteration %d: worktree %s should exist: %v", i, wt.Dir, err)
		}

		// Simulate the explicit cleanup call (as done on error/no-change paths)
		wt.Close()
		prevPath = wt.Dir
	}

	// Verify the last worktree was also cleaned up
	if _, err := os.Stat(prevPath); !os.IsNotExist(err) {
		t.Fatalf("last worktree %s still exists after cleanup", prevPath)
	}
}

func TestCreateTempWorktreeIgnoresHooks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

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
	if _, err := os.Stat(wt.Dir); err != nil {
		t.Fatalf("worktree directory should exist: %v", err)
	}

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

	wt, err := worktree.Create(mainRepo.Root, "HEAD")
	if err != nil {
		t.Fatalf("worktree.Create failed: %v", err)
	}
	defer wt.Close()

	if _, err := os.Stat(filepath.Join(wt.Dir, "deps", "sub", "sub.txt")); err != nil {
		t.Fatalf("expected submodule file in worktree: %v", err)
	}
}
