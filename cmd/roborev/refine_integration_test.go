//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
	"github.com/roborev-dev/roborev/internal/worktree"
)

func TestValidateRefineContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Helper to create a standard test repo
	// Returns repo, baseSHA
	createStandardRepo := func(t *testing.T) (*testutil.TestRepo, string) {
		repo := testutil.NewTestRepo(t)
		repo.RunGit("init")
		repo.SymbolicRef("HEAD", "refs/heads/main")
		repo.Config("user.email", "test@test.com")
		repo.Config("user.name", "Test")
		baseSHA := repo.CommitFile("base.txt", "base", "base commit")
		return repo, baseSHA
	}

	tests := []struct {
		name      string
		setup     func(t *testing.T) (*testutil.TestRepo, string, string) // Returns repo, since, expectedMergeBase
		sinceArg  string                                                  // Overrides setup if not empty
		branchArg string
		wantErr   string
		wantBr    string
	}{
		{
			name: "refuses main without since",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, _ := createStandardRepo(t)
				return repo, "", ""
			},
			wantErr: "refusing to refine on main branch without --since flag",
		},
		{
			name: "allows main with since",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, baseSHA := createStandardRepo(t)
				repo.CommitFile("second.txt", "second", "second commit")
				return repo, baseSHA, baseSHA
			},
			wantBr: "main",
		},
		{
			name: "since works on feature branch",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, baseSHA := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feature.txt", "feature", "feature commit")
				return repo, baseSHA, baseSHA
			},
			wantBr: "feature",
		},
		{
			name: "invalid since ref",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, _ := createStandardRepo(t)
				return repo, "nonexistent-ref-abc123", ""
			},
			wantErr: "cannot resolve --since",
		},
		{
			name: "since not ancestor of HEAD",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, _ := createStandardRepo(t)
				repo.Checkout("-b", "other-branch")
				otherBranchSHA := repo.CommitFile("other.txt", "other", "commit on other branch")
				repo.Checkout("main")
				repo.CommitFile("main2.txt", "main2", "second commit on main")
				return repo, otherBranchSHA, ""
			},
			wantErr: "is not an ancestor of HEAD",
		},
		{
			name: "feature branch without since works",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, baseSHA := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feature.txt", "feature", "feature commit")
				return repo, "", baseSHA
			},
			wantBr: "feature",
		},
		{
			name: "branch mismatch",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, _ := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feat.txt", "f", "feat")
				return repo, "", ""
			},
			branchArg: "other",
			wantErr:   "not on branch",
		},
		{
			name: "branch match",
			setup: func(t *testing.T) (*testutil.TestRepo, string, string) {
				repo, baseSHA := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feat.txt", "f", "feat")
				return repo, "", baseSHA
			},
			branchArg: "feature",
			wantBr:    "feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, sinceSetup, expectedBase := tt.setup(t)
			since := sinceSetup
			if tt.sinceArg != "" {
				since = tt.sinceArg
			}

			repoPath, currentBranch, _, mergeBase, err := validateRefineContext(repo.Root, since, tt.branchArg)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if repoPath == "" {
				t.Error("expected non-empty repoPath")
			}
			if tt.wantBr != "" && currentBranch != tt.wantBr {
				t.Errorf("expected branch %q, got %q", tt.wantBr, currentBranch)
			}
			if expectedBase != "" && mergeBase != expectedBase {
				t.Errorf("expected merge base %q, got %q", expectedBase, mergeBase)
			}
		})
	}
}

func TestWorktreeCleanupBetweenIterations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.NewTestRepo(t)
	repo.RunGit("init")
	repo.Config("user.email", "test@test.com")
	repo.Config("user.name", "Test")
	repo.CommitFile("base.txt", "base", "base commit")

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

	repo := testutil.NewTestRepo(t)
	repo.RunGit("init")
	repo.Config("user.email", "test@test.com")
	repo.Config("user.name", "Test")
	repo.CommitFile("base.txt", "base", "base commit")

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
	submoduleRepo := t.TempDir()
	runSubGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = submoduleRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runSubGit("init")
	runSubGit("symbolic-ref", "HEAD", "refs/heads/main")
	runSubGit("config", "user.email", "test@test.com")
	runSubGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(submoduleRepo, "sub.txt"), []byte("sub"), 0644); err != nil {
		t.Fatal(err)
	}
	runSubGit("add", "sub.txt")
	runSubGit("commit", "-m", "submodule commit")

	mainRepo := t.TempDir()
	runMainGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runMainGit("init")
	runMainGit("symbolic-ref", "HEAD", "refs/heads/main")
	runMainGit("config", "user.email", "test@test.com")
	runMainGit("config", "user.name", "Test")
	runMainGit("config", "protocol.file.allow", "always")
	runMainGit("-c", "protocol.file.allow=always", "submodule", "add", submoduleRepo, "deps/sub")
	runMainGit("commit", "-m", "add submodule")

	wt, err := worktree.Create(mainRepo, "HEAD")
	if err != nil {
		t.Fatalf("worktree.Create failed: %v", err)
	}
	defer wt.Close()

	if _, err := os.Stat(filepath.Join(wt.Dir, "deps", "sub", "sub.txt")); err != nil {
		t.Fatalf("expected submodule file in worktree: %v", err)
	}
}
