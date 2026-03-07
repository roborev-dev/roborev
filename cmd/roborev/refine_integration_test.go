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

	tests := []struct {
		name           string
		setup          func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (since string, expectedBase string)
		branchArg      string
		wantErr        string
		expectedBranch string
	}{
		{
			name: "refuses main without since",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				return "", ""
			},
			wantErr: "refusing to refine on main branch without --since flag",
		},
		{
			name: "allows main with since",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				repo.CommitFile("second.txt", "second", "second commit")
				return baseSHA, baseSHA
			},
			expectedBranch: "main",
		},
		{
			name: "since works on feature branch",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				repo.Checkout("-b", "feature")
				repo.CommitFile("feature.txt", "feature", "feature commit")
				return baseSHA, baseSHA
			},
			expectedBranch: "feature",
		},
		{
			name: "invalid since ref",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				return "nonexistent-ref-abc123", ""
			},
			wantErr: "cannot resolve --since",
		},
		{
			name: "since not ancestor of HEAD",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				repo.Checkout("-b", "other-branch")
				otherBranchSHA := repo.CommitFile("other.txt", "other", "commit on other branch")
				repo.Checkout("main")
				repo.CommitFile("main2.txt", "main2", "second commit on main")
				return otherBranchSHA, ""
			},
			wantErr: "is not an ancestor of HEAD",
		},
		{
			name: "feature branch without since works",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				repo.Checkout("-b", "feature")
				repo.CommitFile("feature.txt", "feature", "feature commit")
				return "", baseSHA
			},
			expectedBranch: "feature",
		},
		{
			name: "branch mismatch",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				repo.Checkout("-b", "feature")
				repo.CommitFile("feat.txt", "f", "feat")
				return "", ""
			},
			branchArg: "other",
			wantErr:   "not on branch",
		},
		{
			name: "branch match",
			setup: func(t *testing.T, repo *testutil.TestRepo, baseSHA string) (string, string) {
				repo.Checkout("-b", "feature")
				repo.CommitFile("feat.txt", "f", "feat")
				return "", baseSHA
			},
			branchArg:      "feature",
			expectedBranch: "feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.InitTestRepo(t)
			baseSHA := repo.RevParse("HEAD")

			since, expectedBase := tt.setup(t, repo, baseSHA)

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
			if tt.expectedBranch != "" && currentBranch != tt.expectedBranch {
				t.Errorf("expected branch %q, got %q", tt.expectedBranch, currentBranch)
			}
			if expectedBase != "" && mergeBase != expectedBase {
				t.Errorf("expected merge base %q, got %q", expectedBase, mergeBase)
			}
		})
	}
}
