//go:build integration

package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestValidateRefineContext(t *testing.T) {
	requireGit(t)

	// Helper to create a standard test repo
	// Returns repo, baseSHA
	createStandardRepo := func(t *testing.T) (*testutil.TestRepo, string) {
		repo := testutil.InitTestRepo(t)
		baseSHA := repo.RevParse("HEAD")
		return repo, baseSHA
	}

	type refineTestSetup struct {
		repo              *testutil.TestRepo
		since             string
		expectedMergeBase string
	}

	tests := []struct {
		name      string
		setup     func(t *testing.T) refineTestSetup
		sinceArg  string // Overrides setup if not empty
		branchArg string
		wantErr   string
		wantBr    string
	}{
		{
			name: "refuses main without since",
			setup: func(t *testing.T) refineTestSetup {
				repo, _ := createStandardRepo(t)
				return refineTestSetup{repo: repo, since: "", expectedMergeBase: ""}
			},
			wantErr: "refusing to refine on main branch without --since flag",
		},
		{
			name: "allows main with since",
			setup: func(t *testing.T) refineTestSetup {
				repo, baseSHA := createStandardRepo(t)
				repo.CommitFile("second.txt", "second", "second commit")
				return refineTestSetup{repo: repo, since: baseSHA, expectedMergeBase: baseSHA}
			},
			wantBr: "main",
		},
		{
			name: "since works on feature branch",
			setup: func(t *testing.T) refineTestSetup {
				repo, baseSHA := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feature.txt", "feature", "feature commit")
				return refineTestSetup{repo: repo, since: baseSHA, expectedMergeBase: baseSHA}
			},
			wantBr: "feature",
		},
		{
			name: "invalid since ref",
			setup: func(t *testing.T) refineTestSetup {
				repo, _ := createStandardRepo(t)
				return refineTestSetup{repo: repo, since: "nonexistent-ref-abc123", expectedMergeBase: ""}
			},
			wantErr: "cannot resolve --since",
		},
		{
			name: "since not ancestor of HEAD",
			setup: func(t *testing.T) refineTestSetup {
				repo, _ := createStandardRepo(t)
				repo.Checkout("-b", "other-branch")
				otherBranchSHA := repo.CommitFile("other.txt", "other", "commit on other branch")
				repo.Checkout("main")
				repo.CommitFile("main2.txt", "main2", "second commit on main")
				return refineTestSetup{repo: repo, since: otherBranchSHA, expectedMergeBase: ""}
			},
			wantErr: "is not an ancestor of HEAD",
		},
		{
			name: "feature branch without since works",
			setup: func(t *testing.T) refineTestSetup {
				repo, baseSHA := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feature.txt", "feature", "feature commit")
				return refineTestSetup{repo: repo, since: "", expectedMergeBase: baseSHA}
			},
			wantBr: "feature",
		},
		{
			name: "branch mismatch",
			setup: func(t *testing.T) refineTestSetup {
				repo, _ := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feat.txt", "f", "feat")
				return refineTestSetup{repo: repo, since: "", expectedMergeBase: ""}
			},
			branchArg: "other",
			wantErr:   "not on branch",
		},
		{
			name: "branch match",
			setup: func(t *testing.T) refineTestSetup {
				repo, baseSHA := createStandardRepo(t)
				repo.Checkout("-b", "feature")
				repo.CommitFile("feat.txt", "f", "feat")
				return refineTestSetup{repo: repo, since: "", expectedMergeBase: baseSHA}
			},
			branchArg: "feature",
			wantBr:    "feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup := tt.setup(t)
			repo := setup.repo
			expectedBase := setup.expectedMergeBase
			since := setup.since
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
