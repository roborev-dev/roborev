package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// mockDaemonClient is a test implementation of daemon.Client
type mockDaemonClient struct {
	reviews   map[string]*storage.Review // keyed by SHA
	jobs      map[int64]*storage.ReviewJob
	responses map[int64][]storage.Response

	// Track calls for assertions
	addressedJobIDs []int64
	addedComments   []addedComment
	enqueuedReviews []enqueuedReview

	// Auto-incrementing review ID counter for WithReview
	nextReviewID int64

	// Configurable errors for testing error paths
	markAddressedErr  error
	getReviewBySHAErr error
}

type addedComment struct {
	JobID     int64
	Commenter string
	Comment   string
}

type enqueuedReview struct {
	RepoPath  string
	GitRef    string
	AgentName string
}

func newMockDaemonClient() *mockDaemonClient {
	return &mockDaemonClient{
		reviews:   make(map[string]*storage.Review),
		jobs:      make(map[int64]*storage.ReviewJob),
		responses: make(map[int64][]storage.Response),
	}
}

func (m *mockDaemonClient) GetReviewBySHA(sha string) (*storage.Review, error) {
	if m.getReviewBySHAErr != nil {
		return nil, m.getReviewBySHAErr
	}
	review, ok := m.reviews[sha]
	if !ok {
		return nil, nil
	}
	return review, nil
}

func (m *mockDaemonClient) GetReviewByJobID(jobID int64) (*storage.Review, error) {
	job, ok := m.jobs[jobID]
	if !ok {
		return nil, nil
	}
	return m.reviews[job.GitRef], nil
}

func (m *mockDaemonClient) MarkReviewAddressed(jobID int64) error {
	if m.markAddressedErr != nil {
		return m.markAddressedErr
	}
	m.addressedJobIDs = append(m.addressedJobIDs, jobID)
	return nil
}

func (m *mockDaemonClient) AddComment(jobID int64, commenter, comment string) error {
	m.addedComments = append(m.addedComments, addedComment{jobID, commenter, comment})
	return nil
}

func (m *mockDaemonClient) EnqueueReview(repoPath, gitRef, agentName string) (int64, error) {
	m.enqueuedReviews = append(m.enqueuedReviews, enqueuedReview{repoPath, gitRef, agentName})
	return int64(len(m.enqueuedReviews)), nil
}

func (m *mockDaemonClient) WaitForReview(jobID int64) (*storage.Review, error) {
	job, ok := m.jobs[jobID]
	if !ok {
		return nil, nil
	}
	return m.reviews[job.GitRef], nil
}

func (m *mockDaemonClient) FindJobForCommit(repoPath, sha string) (*storage.ReviewJob, error) {
	for _, job := range m.jobs {
		if job.GitRef == sha {
			return job, nil
		}
	}
	return nil, nil
}

func (m *mockDaemonClient) FindPendingJobForRef(repoPath, gitRef string) (*storage.ReviewJob, error) {
	for _, job := range m.jobs {
		if job.GitRef == gitRef {
			if job.Status == storage.JobStatusQueued || job.Status == storage.JobStatusRunning {
				return job, nil
			}
		}
	}
	return nil, nil
}

func (m *mockDaemonClient) GetCommentsForJob(jobID int64) ([]storage.Response, error) {
	return m.responses[jobID], nil
}

func (m *mockDaemonClient) Remap(req daemon.RemapRequest) (*daemon.RemapResult, error) {
	return &daemon.RemapResult{}, nil
}

// WithReview adds a review to the mock client, returning the client for chaining.
func (m *mockDaemonClient) WithReview(sha string, jobID int64, output string, addressed bool) *mockDaemonClient {
	m.nextReviewID++
	m.reviews[sha] = &storage.Review{
		ID:        m.nextReviewID,
		JobID:     jobID,
		Output:    output,
		Addressed: addressed,
	}
	return m
}

// WithJob adds a job to the mock client, returning the client for chaining.
func (m *mockDaemonClient) WithJob(id int64, gitRef string, status storage.JobStatus) *mockDaemonClient {
	m.jobs[id] = &storage.ReviewJob{
		ID:     id,
		GitRef: gitRef,
		Status: status,
	}
	return m
}

// Verify mockDaemonClient implements daemon.Client
var _ daemon.Client = (*mockDaemonClient)(nil)

func TestSelectRefineAgentCodexFallback(t *testing.T) {
	// With an empty PATH, no real agents are available and the test agent
	// is excluded from production fallback, so we expect an error.
	t.Setenv("PATH", "")

	_, err := selectRefineAgent("codex", agent.ReasoningFast, "")
	if err == nil {
		t.Fatal("expected error when no agents are available")
	}
	if !strings.Contains(err.Error(), "no agents available") {
		t.Fatalf("expected 'no agents available' error, got: %v", err)
	}
}

func TestResolveAllowUnsafeAgents(t *testing.T) {
	// Note: refine defaults to true because it requires file modifications to work.
	// Priority: CLI flag > config > default (true for refine).
	boolTrue := true
	boolFalse := false

	tests := []struct {
		name        string
		flag        bool
		flagChanged bool
		cfg         *config.Config
		expected    bool
	}{
		{
			name:        "config enabled, flag not changed - uses config",
			flag:        false,
			flagChanged: false,
			cfg:         &config.Config{AllowUnsafeAgents: &boolTrue},
			expected:    true,
		},
		{
			name:        "config disabled, flag not changed - honors config",
			flag:        false,
			flagChanged: false,
			cfg:         &config.Config{AllowUnsafeAgents: &boolFalse},
			expected:    false, // Now honors config
		},
		{
			name:        "flag explicitly enabled - uses flag over config",
			flag:        true,
			flagChanged: true,
			cfg:         &config.Config{AllowUnsafeAgents: &boolFalse},
			expected:    true,
		},
		{
			name:        "flag explicitly disabled - uses flag over config",
			flag:        false,
			flagChanged: true,
			cfg:         &config.Config{AllowUnsafeAgents: &boolTrue},
			expected:    false,
		},
		{
			name:        "nil config, flag not changed - defaults to true",
			flag:        false,
			flagChanged: false,
			cfg:         nil,
			expected:    true,
		},
		{
			name:        "nil config, flag explicitly enabled - uses flag",
			flag:        true,
			flagChanged: true,
			cfg:         nil,
			expected:    true,
		},
		{
			name:        "config not set (nil pointer), flag not changed - defaults to true",
			flag:        false,
			flagChanged: false,
			cfg:         &config.Config{AllowUnsafeAgents: nil},
			expected:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := resolveAllowUnsafeAgents(tc.flag, tc.flagChanged, tc.cfg)
			if result != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestSelectRefineAgentCodexUsesRequestedReasoning(t *testing.T) {
	t.Cleanup(testutil.MockExecutable(t, "codex", 0))

	selected, err := selectRefineAgent("codex", agent.ReasoningFast, "")
	if err != nil {
		t.Fatalf("selectRefineAgent failed: %v", err)
	}

	codexAgent, ok := selected.(*agent.CodexAgent)
	if !ok {
		t.Fatalf("expected codex agent, got %T", selected)
	}
	if codexAgent.Reasoning != agent.ReasoningFast {
		t.Fatalf("expected codex to use requested reasoning (fast), got %q", codexAgent.Reasoning)
	}
}

func TestSelectRefineAgentCodexFallbackUsesRequestedReasoning(t *testing.T) {
	t.Cleanup(testutil.MockExecutableIsolated(t, "codex", 0))

	// Request an unavailable agent, codex should be used as fallback
	selected, err := selectRefineAgent("nonexistent-agent", agent.ReasoningThorough, "")
	if err != nil {
		t.Fatalf("selectRefineAgent failed: %v", err)
	}

	codexAgent, ok := selected.(*agent.CodexAgent)
	if !ok {
		t.Fatalf("expected codex fallback agent, got %T", selected)
	}
	if codexAgent.Reasoning != agent.ReasoningThorough {
		t.Fatalf("expected codex fallback to use requested reasoning (thorough), got %q", codexAgent.Reasoning)
	}
}

func TestFindFailedReviewForBranch(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(*mockDaemonClient)
		commits          []string
		skip             map[int64]bool
		wantJobID        int64 // 0 if nil expected
		wantErrs         []string
		wantAddressedIDs []int64
	}{
		{
			name: "oldest first",
			setup: func(c *mockDaemonClient) {
				c.WithReview("oldest123", 100, "No issues found.", false).
					WithReview("middle456", 200, "Found a bug in the code.", false).
					WithReview("newest789", 300, "Security vulnerability detected.", false)
			},
			commits:          []string{"oldest123", "middle456", "newest789"},
			wantJobID:        200,
			wantAddressedIDs: []int64{100},
		},
		{
			name: "skips addressed",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "Another bug.", true).
					WithReview("commit3", 300, "More issues.", false)
			},
			commits:   []string{"commit1", "commit2", "commit3"},
			wantJobID: 100,
		},
		{
			name: "skips given up reviews",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "Another bug.", false).
					WithReview("commit3", 300, "No issues found.", false)
			},
			commits:   []string{"commit1", "commit2", "commit3"},
			skip:      map[int64]bool{1: true},
			wantJobID: 200,
		},
		{
			name: "all skipped returns nil",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "Another.", false)
			},
			commits:   []string{"commit1", "commit2"},
			skip:      map[int64]bool{1: true, 2: true},
			wantJobID: 0,
		},
		{
			name: "all pass",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "No findings.", false)
			},
			commits:          []string{"commit1", "commit2"},
			wantJobID:        0,
			wantAddressedIDs: []int64{100, 200},
		},
		{
			name:      "no reviews",
			setup:     func(c *mockDaemonClient) {},
			commits:   []string{"unreviewed1", "unreviewed2"},
			wantJobID: 0,
		},
		{
			name: "marks passing as addressed",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "No findings.", false)
			},
			commits:          []string{"commit1", "commit2"},
			wantJobID:        0,
			wantAddressedIDs: []int64{100, 200},
		},
		{
			name: "marks passing before failure",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "Bug found.", false)
			},
			commits:          []string{"commit1", "commit2"},
			wantJobID:        200,
			wantAddressedIDs: []int64{100},
		},
		{
			name: "does not mark already addressed",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", true).
					WithReview("commit2", 200, "Bug found.", false)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 200,
		},
		{
			name: "mixed scenario",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "No issues.", true).
					WithReview("commit3", 300, "Bug found.", true).
					WithReview("commit4", 400, "No findings detected.", false).
					WithReview("commit5", 500, "Critical error.", false)
			},
			commits:          []string{"commit1", "commit2", "commit3", "commit4", "commit5"},
			wantJobID:        500,
			wantAddressedIDs: []int64{100, 400},
		},
		{
			name: "stops at first failure",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "No issues found.", false).
					WithReview("commit3", 300, "Another bug.", false)
			},
			commits:   []string{"commit1", "commit2", "commit3"},
			wantJobID: 100,
		},
		{
			name: "mark addressed error",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false)
				c.markAddressedErr = fmt.Errorf("daemon connection failed")
			},
			commits:  []string{"commit1"},
			wantErrs: []string{"marking review (job 100) as addressed"},
		},
		{
			name: "get review by sha error",
			setup: func(c *mockDaemonClient) {
				c.getReviewBySHAErr = fmt.Errorf("daemon connection failed")
			},
			commits:  []string{"commit1", "commit2"},
			wantErrs: []string{"fetching review", "commit1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockDaemonClient()
			tt.setup(client)

			found, err := findFailedReviewForBranch(client, tt.commits, tt.skip)

			if len(tt.wantErrs) > 0 {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrs)
				}
				for _, wantErr := range tt.wantErrs {
					if !strings.Contains(err.Error(), wantErr) {
						t.Errorf("expected error containing %q, got: %v", wantErr, err)
					}
				}
				if found != nil {
					t.Errorf("expected nil review when error occurs, got job %d", found.JobID)
				}
				return
			}

			if err != nil {
				t.Fatalf("findFailedReviewForBranch failed: %v", err)
			}

			if tt.wantJobID == 0 {
				if found != nil {
					t.Errorf("expected no failed reviews, got job %d", found.JobID)
				}
			} else {
				if found == nil {
					t.Fatalf("expected to find a failed review (job %d)", tt.wantJobID)
				}
				if found.JobID != tt.wantJobID {
					t.Errorf("expected job %d, got job %d", tt.wantJobID, found.JobID)
				}
			}

			if len(tt.wantAddressedIDs) > 0 {
				if len(client.addressedJobIDs) != len(tt.wantAddressedIDs) {
					t.Errorf("expected %d reviews to be marked addressed, got %d", len(tt.wantAddressedIDs), len(client.addressedJobIDs))
				}
				addressed := make(map[int64]bool)
				for _, id := range client.addressedJobIDs {
					addressed[id] = true
				}
				for _, id := range tt.wantAddressedIDs {
					if !addressed[id] {
						t.Errorf("expected job %d to be marked addressed, got %v", id, client.addressedJobIDs)
					}
				}
			} else if len(client.addressedJobIDs) > 0 {
				t.Errorf("expected no reviews to be marked addressed, got %v", client.addressedJobIDs)
			}
		})
	}
}

func TestFindPendingJobForBranch(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*mockDaemonClient)
		commits   []string
		wantJobID int64 // 0 if nil expected
	}{
		{
			name: "finds running job",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusDone).
					WithJob(200, "commit2", storage.JobStatusRunning)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 200,
		},
		{
			name: "finds queued job",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusQueued)
			},
			commits:   []string{"commit1"},
			wantJobID: 100,
		},
		{
			name: "no pending jobs",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusDone).
					WithJob(200, "commit2", storage.JobStatusDone)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 0,
		},
		{
			name:      "no jobs for commits",
			setup:     func(c *mockDaemonClient) {},
			commits:   []string{"unreviewed1", "unreviewed2"},
			wantJobID: 0,
		},
		{
			name: "oldest first",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusRunning).
					WithJob(200, "commit2", storage.JobStatusRunning)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockDaemonClient()
			tt.setup(client)

			pending, err := findPendingJobForBranch(client, "/repo", tt.commits)
			if err != nil {
				t.Fatalf("findPendingJobForBranch failed: %v", err)
			}

			if tt.wantJobID == 0 {
				if pending != nil {
					t.Errorf("expected no pending jobs, got job %d", pending.ID)
				}
			} else {
				if pending == nil {
					t.Fatalf("expected to find a pending job (job %d)", tt.wantJobID)
				}
				if pending.ID != tt.wantJobID {
					t.Errorf("expected job %d, got job %d", tt.wantJobID, pending.ID)
				}
			}
		})
	}
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

func TestValidateRefineContext_RefusesMainBranchWithoutSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	// Stay on main branch (don't create feature branch)
	chdirForTest(t, repo.Root)

	// Validating without --since on main should fail
	_, _, _, _, err := validateRefineContext("", "", "")
	if err == nil {
		t.Fatal("expected error when validating on main without --since")
	}
	if !strings.Contains(err.Error(), "refusing to refine on main") {
		t.Errorf("expected 'refusing to refine on main' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--since") {
		t.Errorf("expected error to mention --since flag, got: %v", err)
	}
}

func TestValidateRefineContext_AllowsMainBranchWithSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)
	baseSHA := repo.RevParse("HEAD")

	// Add another commit on main
	repo.CommitFile("second.txt", "second", "second commit")

	chdirForTest(t, repo.Root)

	// Validating with --since on main should pass
	repoPath, currentBranch, _, mergeBase, err := validateRefineContext("", baseSHA, "")
	if err != nil {
		t.Fatalf("validation should pass with --since on main, got: %v", err)
	}
	if repoPath == "" {
		t.Error("expected non-empty repoPath")
	}
	if currentBranch != "main" {
		t.Errorf("expected currentBranch=main, got %s", currentBranch)
	}
	if mergeBase != baseSHA {
		t.Errorf("expected mergeBase=%s, got %s", baseSHA, mergeBase)
	}
}

func TestValidateRefineContext_SinceWorksOnFeatureBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)
	baseSHA := repo.RevParse("HEAD")

	// Create feature branch with commits
	repo.RunGit("checkout", "-b", "feature")
	repo.CommitFile("feature.txt", "feature", "feature commit")

	chdirForTest(t, repo.Root)

	// --since should work on feature branch
	repoPath, currentBranch, _, mergeBase, err := validateRefineContext("", baseSHA, "")
	if err != nil {
		t.Fatalf("--since should work on feature branch, got: %v", err)
	}
	if repoPath == "" {
		t.Error("expected non-empty repoPath")
	}
	if currentBranch != "feature" {
		t.Errorf("expected currentBranch=feature, got %s", currentBranch)
	}
	if mergeBase != baseSHA {
		t.Errorf("expected mergeBase=%s, got %s", baseSHA, mergeBase)
	}
}

func TestValidateRefineContext_InvalidSinceRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	chdirForTest(t, repo.Root)

	// Invalid --since ref should fail with clear error
	_, _, _, _, err := validateRefineContext("", "nonexistent-ref-abc123", "")
	if err == nil {
		t.Fatal("expected error for invalid --since ref")
	}
	if !strings.Contains(err.Error(), "cannot resolve --since") {
		t.Errorf("expected 'cannot resolve --since' error, got: %v", err)
	}
}

func TestValidateRefineContext_SinceNotAncestorOfHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	// Create a commit on a separate branch that diverges from main
	repo.RunGit("checkout", "-b", "other-branch")
	repo.CommitFile("other.txt", "other", "commit on other branch")
	otherBranchSHA := repo.RevParse("HEAD")

	// Go back to main and create a different commit
	repo.RunGit("checkout", "main")
	repo.CommitFile("main2.txt", "main2", "second commit on main")

	chdirForTest(t, repo.Root)

	// Using --since with a commit from a different branch (not ancestor of HEAD) should fail
	_, _, _, _, err := validateRefineContext("", otherBranchSHA, "")
	if err == nil {
		t.Fatal("expected error when --since is not an ancestor of HEAD")
	}
	if !strings.Contains(err.Error(), "not an ancestor of HEAD") {
		t.Errorf("expected 'not an ancestor of HEAD' error, got: %v", err)
	}
}

func TestValidateRefineContext_FeatureBranchWithoutSinceStillWorks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)
	baseSHA := repo.RevParse("HEAD")

	// Create feature branch
	repo.RunGit("checkout", "-b", "feature")
	repo.CommitFile("feature.txt", "feature", "feature commit")

	chdirForTest(t, repo.Root)

	// Feature branch without --since should pass validation (uses merge-base)
	repoPath, currentBranch, _, mergeBase, err := validateRefineContext("", "", "")
	if err != nil {
		t.Fatalf("feature branch without --since should work, got: %v", err)
	}
	if repoPath == "" {
		t.Error("expected non-empty repoPath")
	}
	if currentBranch != "feature" {
		t.Errorf("expected currentBranch=feature, got %s", currentBranch)
	}
	// mergeBase should be the base commit (merge-base of feature and main)
	if mergeBase != baseSHA {
		t.Errorf("expected mergeBase=%s (base commit), got %s", baseSHA, mergeBase)
	}
}

func TestCommitWithHookRetrySucceeds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	// Install a pre-commit hook that fails on the first 2 calls and
	// succeeds on the 3rd+. The hook runs twice before a retry: once
	// by git commit, once by the hook probe. A counter file tracks calls.
	repo.WriteNamedHook("pre-commit", `#!/bin/sh
COUNT_FILE=".git/hook-count"
COUNT=0
if [ -f "$COUNT_FILE" ]; then
    COUNT=$(cat "$COUNT_FILE")
fi
COUNT=$((COUNT + 1))
echo "$COUNT" > "$COUNT_FILE"
if [ "$COUNT" -le 2 ]; then
    echo "lint error: trailing whitespace" >&2
    exit 1
fi
exit 0
`)

	// Make a file change to commit
	if err := os.WriteFile(filepath.Join(repo.Root, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	testAgent := agent.NewTestAgent()
	sha, err := commitWithHookRetry(repo.Root, "test commit", testAgent, true)
	if err != nil {
		t.Fatalf("commitWithHookRetry should succeed: %v", err)
	}

	if sha == "" {
		t.Fatal("expected non-empty SHA")
	}

	// Verify the commit exists
	commitSHA := repo.RevParse("HEAD")
	if commitSHA != sha {
		t.Errorf("expected HEAD=%s, got %s", sha, commitSHA)
	}
}

func TestCommitWithHookRetryExhausted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	repo.WriteNamedHook("pre-commit",
		"#!/bin/sh\necho 'always fails' >&2\nexit 1\n")

	// Make a file change
	if err := os.WriteFile(filepath.Join(repo.Root, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repo.Root, "test commit", testAgent, true)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("expected error mentioning '3 attempts', got: %v", err)
	}
}

func TestCommitWithHookRetrySkipsNonHookError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	// No pre-commit hook installed. Commit with no changes will fail
	// for a non-hook reason ("nothing to commit").
	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repo.Root, "empty commit", testAgent, true)
	if err == nil {
		t.Fatal("expected error for empty commit without hook")
	}

	// Should return the raw git error, not a hook-retry error
	if strings.Contains(err.Error(), "pre-commit hook failed") {
		t.Errorf("non-hook error should not be reported as hook failure, got: %v", err)
	}
	if strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("non-hook error should not trigger retries, got: %v", err)
	}
}

func TestCommitWithHookRetrySkipsAddPhaseError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	repo.WriteNamedHook("pre-commit", "#!/bin/sh\nexit 0\n")

	// Make a change so there's something to commit
	if err := os.WriteFile(filepath.Join(repo.Root, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create index.lock to make git add fail (non-hook failure)
	lockFile := filepath.Join(repo.Root, ".git", "index.lock")
	if err := os.WriteFile(lockFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lockFile)

	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repo.Root, "test commit", testAgent, true)
	if err == nil {
		t.Fatal("expected error with index.lock present")
	}

	// Should NOT retry despite hook being present (add-phase failure)
	if strings.Contains(err.Error(), "pre-commit hook failed") {
		t.Errorf("add-phase error should not be reported as hook failure, got: %v", err)
	}
	if strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("add-phase error should not trigger retries, got: %v", err)
	}
}

func TestCommitWithHookRetrySkipsCommitPhaseNonHookError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := testutil.InitTestRepo(t)

	repo.WriteNamedHook("pre-commit", "#!/bin/sh\nexit 0\n")

	// No changes to commit — "nothing to commit" is a commit-phase
	// failure, but the hook passes, so HookFailed should be false.
	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repo.Root, "empty commit", testAgent, true)
	if err == nil {
		t.Fatal("expected error for empty commit")
	}

	// Should NOT retry despite hook being present (hook is passing)
	if strings.Contains(err.Error(), "pre-commit hook failed") {
		t.Errorf("non-hook commit error should not be reported as hook failure, got: %v", err)
	}
	if strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("non-hook commit error should not trigger retries, got: %v", err)
	}
}

func TestResolveReasoningWithFast(t *testing.T) {
	tests := []struct {
		name                   string
		reasoning              string
		fast                   bool
		reasoningExplicitlySet bool
		want                   string
	}{
		{
			name:                   "fast flag sets reasoning to fast",
			reasoning:              "",
			fast:                   true,
			reasoningExplicitlySet: false,
			want:                   "fast",
		},
		{
			name:                   "explicit reasoning takes precedence over fast",
			reasoning:              "thorough",
			fast:                   true,
			reasoningExplicitlySet: true,
			want:                   "thorough",
		},
		{
			name:                   "no fast flag preserves reasoning",
			reasoning:              "standard",
			fast:                   false,
			reasoningExplicitlySet: true,
			want:                   "standard",
		},
		{
			name:                   "no flags returns empty",
			reasoning:              "",
			fast:                   false,
			reasoningExplicitlySet: false,
			want:                   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveReasoningWithFast(tt.reasoning, tt.fast, tt.reasoningExplicitlySet)
			if got != tt.want {
				t.Errorf("resolveReasoningWithFast(%q, %v, %v) = %q, want %q",
					tt.reasoning, tt.fast, tt.reasoningExplicitlySet, got, tt.want)
			}
		})
	}
}

func TestRefineFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "all-branches and branch mutually exclusive",
			args:    []string{"--all-branches", "--branch", "main"},
			wantErr: "--all-branches and --branch are mutually exclusive",
		},
		{
			name:    "all-branches and since mutually exclusive",
			args:    []string{"--all-branches", "--since", "abc123"},
			wantErr: "--all-branches and --since are mutually exclusive",
		},
		{
			name:    "newest-first requires all-branches or list",
			args:    []string{"--newest-first"},
			wantErr: "--newest-first requires --all-branches or --list",
		},
		{
			name: "newest-first with list is accepted",
			args: []string{"--newest-first", "--list"},
			// This will fail for other reasons (no daemon), but
			// flag validation itself should pass.
			wantErr: "",
		},
		{
			name:    "newest-first with all-branches is accepted",
			args:    []string{"--newest-first", "--all-branches"},
			wantErr: "",
		},
		{
			name:    "list and since mutually exclusive",
			args:    []string{"--list", "--since", "abc123"},
			wantErr: "--list and --since are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := refineCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf(
						"expected error containing %q, got: %v",
						tt.wantErr, err,
					)
				}
			} else if err != nil {
				msg := err.Error()
				isValidationErr := strings.Contains(msg, "mutually exclusive") ||
					strings.Contains(msg, "requires --")
				if isValidationErr {
					t.Errorf("unexpected flag validation error: %v", err)
				}
			}
		})
	}
}
