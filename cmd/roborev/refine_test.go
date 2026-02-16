package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
)

// createMockExecutable creates a platform-appropriate executable script in dir
// that exits with the given exit code.
func createMockExecutable(t *testing.T, dir, name string, exitCode int) string {
	t.Helper()
	var path string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, name+".bat")
		if err := os.WriteFile(path, []byte(fmt.Sprintf("@exit /b %d\r\n", exitCode)), 0755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	} else {
		path = filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)), 0755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	return path
}

// installGitHook writes a shell script as the named git hook
// (e.g. "pre-commit", "post-checkout") and makes it executable.
func installGitHook(t *testing.T, repoDir, name, script string) {
	t.Helper()
	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, name)
	if err := os.WriteFile(hookPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

// gitCommitFile writes a file, stages it, commits, and returns the new HEAD SHA.
func gitCommitFile(t *testing.T, repoDir string, runGit func(...string) string, filename, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", filename)
	runGit("commit", "-m", msg)
	return runGit("rev-parse", "HEAD")
}

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
	tmpDir := t.TempDir()
	createMockExecutable(t, tmpDir, "codex", 0)

	t.Setenv("PATH", tmpDir)

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
	tmpDir := t.TempDir()
	createMockExecutable(t, tmpDir, "codex", 0)

	t.Setenv("PATH", tmpDir)

	// Request an unavailable agent (claude), codex should be used as fallback
	selected, err := selectRefineAgent("claude", agent.ReasoningThorough, "")
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

func TestFindFailedReviewForBranch_OldestFirst(t *testing.T) {
	client := newMockDaemonClient()

	// Mock reviews: oldest commit passes (output="No issues found."),
	// middle and newest fail (output contains actual findings).
	client.
		WithReview("oldest123", 100, "No issues found.", false).
		WithReview("middle456", 200, "Found a bug in the code.", false).
		WithReview("newest789", 300, "Security vulnerability detected.", false)

	// Commits in chronological order (oldest first, as returned by git log --reverse)
	commits := []string{"oldest123", "middle456", "newest789"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	if found == nil {
		t.Fatal("expected to find a failed review")
	}

	// Should return oldest failure (job 200), not newest (job 300)
	if found.JobID != 200 {
		t.Errorf("expected oldest failed review (job 200), got job %d", found.JobID)
	}
}

func TestFindFailedReviewForBranch_SkipsAddressed(t *testing.T) {
	client := newMockDaemonClient()

	client.
		WithReview("commit1", 100, "Bug found.", false).
		WithReview("commit2", 200, "Another bug.", true).
		WithReview("commit3", 300, "More issues.", false)

	commits := []string{"commit1", "commit2", "commit3"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return commit1 (oldest unaddressed failure), skipping addressed commit2
	if found == nil || found.JobID != 100 {
		t.Errorf("expected oldest unaddressed failure (job 100), got %v", found)
	}
}

func TestFindFailedReviewForBranch_SkipsGivenUpReviews(t *testing.T) {
	client := newMockDaemonClient()

	client.
		WithReview("commit1", 100, "Bug found.", false).
		WithReview("commit2", 200, "Another bug.", false).
		WithReview("commit3", 300, "No issues found.", false)

	commits := []string{"commit1", "commit2", "commit3"}

	// Skip review ID 1 (simulates "giving up" after 3 failed attempts)
	skip := map[int64]bool{1: true}

	found, err := findFailedReviewForBranch(client, commits, skip)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return commit2 (job 200), skipping commit1 which is in the skip set
	if found == nil || found.JobID != 200 {
		t.Errorf("expected job 200 (skipping given-up review), got %v", found)
	}
}

func TestFindFailedReviewForBranch_AllSkippedReturnsNil(t *testing.T) {
	client := newMockDaemonClient()

	client.
		WithReview("commit1", 100, "Bug found.", false).
		WithReview("commit2", 200, "Another.", false)

	commits := []string{"commit1", "commit2"}

	// Skip both reviews (simulates giving up on all failed reviews)
	skip := map[int64]bool{1: true, 2: true}

	found, err := findFailedReviewForBranch(client, commits, skip)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return nil since all failures are in skip set
	if found != nil {
		t.Errorf("expected nil (all skipped), got job %d", found.JobID)
	}
}

func TestFindFailedReviewForBranch_AllPass(t *testing.T) {
	client := newMockDaemonClient()

	client.
		WithReview("commit1", 100, "No issues found.", false).
		WithReview("commit2", 200, "No findings.", false)

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	if found != nil {
		t.Errorf("expected no failed reviews, got job %d", found.JobID)
	}
}

func TestSubmoduleRequiresFileProtocol(t *testing.T) {
	tpl := `[submodule "test"]
	path = test
	%s = %s
`
	tests := []struct {
		name     string
		key      string
		url      string
		expected bool
	}{
		{name: "file-scheme", key: "url", url: "file:///tmp/repo", expected: true},
		{name: "file-scheme-quoted", key: "url", url: `"file:///tmp/repo"`, expected: true},
		{name: "file-scheme-mixed-case-key", key: "URL", url: "file:///tmp/repo", expected: true},
		{name: "file-single-slash", key: "url", url: "file:/tmp/repo", expected: true},
		{name: "unix-absolute", key: "url", url: "/tmp/repo", expected: true},
		{name: "relative-dot", key: "url", url: "./repo", expected: true},
		{name: "relative-dotdot", key: "url", url: "../repo", expected: true},
		{name: "windows-drive-slash", key: "url", url: "C:/repo", expected: true},
		{name: "windows-drive-backslash", key: "url", url: `C:\repo`, expected: true},
		{name: "windows-unc", key: "url", url: `\\server\share\repo`, expected: true},
		{name: "https", key: "url", url: "https://example.com/repo.git", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitmodules := filepath.Join(dir, ".gitmodules")
			if err := os.WriteFile(gitmodules, []byte(fmt.Sprintf(tpl, tc.key, tc.url)), 0644); err != nil {
				t.Fatalf("write .gitmodules: %v", err)
			}
			if got := submoduleRequiresFileProtocol(dir); got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestSubmoduleRequiresFileProtocolNested(t *testing.T) {
	tpl := `[submodule "test"]
	path = test
	url = %s
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte(fmt.Sprintf(tpl, "https://example.com/repo.git")), 0644); err != nil {
		t.Fatalf("write root .gitmodules: %v", err)
	}
	nestedPath := filepath.Join(dir, "sub", ".gitmodules")
	if err := os.MkdirAll(filepath.Dir(nestedPath), 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(nestedPath, []byte(fmt.Sprintf(tpl, "file:///tmp/repo")), 0644); err != nil {
		t.Fatalf("write nested .gitmodules: %v", err)
	}

	if !submoduleRequiresFileProtocol(dir) {
		t.Fatalf("expected nested file URL to require file protocol")
	}
}

func TestFindFailedReviewForBranch_NoReviews(t *testing.T) {
	client := newMockDaemonClient()
	// No reviews set - GetReviewBySHA will return nil for all commits

	commits := []string{"unreviewed1", "unreviewed2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	if found != nil {
		t.Errorf("expected nil when no reviews exist, got job %d", found.JobID)
	}
}

func TestFindFailedReviewForBranch_MarksPassingAsAddressed(t *testing.T) {
	client := newMockDaemonClient()

	// Two passing reviews that are NOT yet addressed
	client.
		WithReview("commit1", 100, "No issues found.", false).
		WithReview("commit2", 200, "No findings.", false)

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// No failed reviews should be found
	if found != nil {
		t.Errorf("expected no failed reviews, got job %d", found.JobID)
	}

	// Both passing reviews should be marked as addressed (by job ID)
	if len(client.addressedJobIDs) != 2 {
		t.Errorf("expected 2 reviews to be marked addressed, got %d", len(client.addressedJobIDs))
	}

	// Verify the specific job IDs were marked
	addressed := make(map[int64]bool)
	for _, id := range client.addressedJobIDs {
		addressed[id] = true
	}
	if !addressed[100] || !addressed[200] {
		t.Errorf("expected jobs 100 and 200 to be marked addressed, got %v", client.addressedJobIDs)
	}
}

func TestFindFailedReviewForBranch_MarksPassingBeforeFailure(t *testing.T) {
	client := newMockDaemonClient()

	// First commit passes (unaddressed), second fails
	client.
		WithReview("commit1", 100, "No issues found.", false).
		WithReview("commit2", 200, "Bug found.", false)

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return the failing review
	if found == nil || found.JobID != 200 {
		t.Errorf("expected failed review (job 200), got %v", found)
	}

	// Passing review before the failure should be marked addressed (by job ID)
	if len(client.addressedJobIDs) != 1 {
		t.Errorf("expected 1 review to be marked addressed, got %d", len(client.addressedJobIDs))
	}
	if len(client.addressedJobIDs) > 0 && client.addressedJobIDs[0] != 100 {
		t.Errorf("expected job 100 to be marked addressed, got %v", client.addressedJobIDs)
	}
}

func TestFindFailedReviewForBranch_DoesNotMarkAlreadyAddressed(t *testing.T) {
	client := newMockDaemonClient()

	// Passing review already addressed - should not be marked again
	client.
		WithReview("commit1", 100, "No issues found.", true).
		WithReview("commit2", 200, "Bug found.", false)

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	if found == nil || found.JobID != 200 {
		t.Errorf("expected failed review (job 200), got %v", found)
	}

	// Already-addressed review should NOT be marked again
	if len(client.addressedJobIDs) != 0 {
		t.Errorf("expected no reviews to be marked addressed (already addressed), got %v", client.addressedJobIDs)
	}
}

func TestFindFailedReviewForBranch_MixedScenario(t *testing.T) {
	client := newMockDaemonClient()

	// Complex scenario:
	// commit1: pass (unaddressed) - should be marked
	// commit2: pass (already addressed) - should NOT be marked
	// commit3: fail (addressed) - should be skipped
	// commit4: pass (unaddressed) - should be marked
	// commit5: fail (unaddressed) - should be returned
	client.
		WithReview("commit1", 100, "No issues found.", false).
		WithReview("commit2", 200, "No issues.", true).
		WithReview("commit3", 300, "Bug found.", true).
		WithReview("commit4", 400, "No findings detected.", false).
		WithReview("commit5", 500, "Critical error.", false)

	commits := []string{"commit1", "commit2", "commit3", "commit4", "commit5"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return the first unaddressed failure (commit5)
	if found == nil || found.JobID != 500 {
		t.Errorf("expected failed review (job 500), got %v", found)
	}

	// commit1 and commit4 should be marked as addressed (unaddressed passing reviews)
	if len(client.addressedJobIDs) != 2 {
		t.Errorf("expected 2 reviews to be marked addressed, got %d: %v", len(client.addressedJobIDs), client.addressedJobIDs)
	}

	addressed := make(map[int64]bool)
	for _, id := range client.addressedJobIDs {
		addressed[id] = true
	}
	if !addressed[100] || !addressed[400] {
		t.Errorf("expected jobs 100 and 400 to be marked addressed, got %v", client.addressedJobIDs)
	}
}

func TestFindFailedReviewForBranch_StopsAtFirstFailure(t *testing.T) {
	client := newMockDaemonClient()

	// Multiple failures - should stop at the first (oldest) one
	// and not process subsequent commits
	client.
		WithReview("commit1", 100, "Bug found.", false).
		WithReview("commit2", 200, "No issues found.", false).
		WithReview("commit3", 300, "Another bug.", false)

	commits := []string{"commit1", "commit2", "commit3"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return the first failure
	if found == nil || found.JobID != 100 {
		t.Errorf("expected first failed review (job 100), got %v", found)
	}

	// No reviews should be marked as addressed (we stopped at first failure)
	if len(client.addressedJobIDs) != 0 {
		t.Errorf("expected no reviews to be marked addressed, got %v", client.addressedJobIDs)
	}
}

func TestFindFailedReviewForBranch_MarkAddressedError(t *testing.T) {
	client := newMockDaemonClient()

	// A passing review that will trigger MarkReviewAddressed
	client.WithReview("commit1", 100, "No issues found.", false)

	// Configure the mock to return an error when marking as addressed
	client.markAddressedErr = fmt.Errorf("daemon connection failed")

	commits := []string{"commit1"}

	found, err := findFailedReviewForBranch(client, commits, nil)

	// Should return an error and not continue processing
	if err == nil {
		t.Fatal("expected error when MarkReviewAddressed fails, got nil")
	}
	if found != nil {
		t.Errorf("expected nil review when error occurs, got job %d", found.JobID)
	}

	// Error message should indicate which job failed
	expectedMsg := "marking review (job 100) as addressed"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("error should mention job ID, got: %v", err)
	}
}

func TestFindFailedReviewForBranch_GetReviewBySHAError(t *testing.T) {
	client := newMockDaemonClient()

	// Configure the mock to return an error when fetching reviews
	client.getReviewBySHAErr = fmt.Errorf("daemon connection failed")

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)

	// Should return an error and not continue processing
	if err == nil {
		t.Fatal("expected error when GetReviewBySHA fails, got nil")
	}
	if found != nil {
		t.Errorf("expected nil review when error occurs, got job %d", found.JobID)
	}

	// Error message should indicate which commit failed
	if !strings.Contains(err.Error(), "commit1") {
		t.Errorf("error should mention commit SHA, got: %v", err)
	}
	if !strings.Contains(err.Error(), "fetching review") {
		t.Errorf("error should mention 'fetching review', got: %v", err)
	}
}

func TestFindPendingJobForBranch_FindsRunningJob(t *testing.T) {
	client := newMockDaemonClient()

	// Jobs: first is done, second is running
	client.
		WithJob(100, "commit1", storage.JobStatusDone).
		WithJob(200, "commit2", storage.JobStatusRunning)

	commits := []string{"commit1", "commit2"}

	pending, err := findPendingJobForBranch(client, "/repo", commits)
	if err != nil {
		t.Fatalf("findPendingJobForBranch failed: %v", err)
	}

	if pending == nil {
		t.Fatal("expected to find a pending job")
	}
	if pending.ID != 200 {
		t.Errorf("expected running job 200, got %d", pending.ID)
	}
}

func TestFindPendingJobForBranch_FindsQueuedJob(t *testing.T) {
	client := newMockDaemonClient()

	// Jobs: first is queued
	client.WithJob(100, "commit1", storage.JobStatusQueued)

	commits := []string{"commit1"}

	pending, err := findPendingJobForBranch(client, "/repo", commits)
	if err != nil {
		t.Fatalf("findPendingJobForBranch failed: %v", err)
	}

	if pending == nil {
		t.Fatal("expected to find a pending job")
	}
	if pending.ID != 100 {
		t.Errorf("expected queued job 100, got %d", pending.ID)
	}
}

func TestFindPendingJobForBranch_NoPendingJobs(t *testing.T) {
	client := newMockDaemonClient()

	// All jobs are done
	client.
		WithJob(100, "commit1", storage.JobStatusDone).
		WithJob(200, "commit2", storage.JobStatusDone)

	commits := []string{"commit1", "commit2"}

	pending, err := findPendingJobForBranch(client, "/repo", commits)
	if err != nil {
		t.Fatalf("findPendingJobForBranch failed: %v", err)
	}

	if pending != nil {
		t.Errorf("expected no pending jobs, got job %d", pending.ID)
	}
}

func TestFindPendingJobForBranch_NoJobsForCommits(t *testing.T) {
	client := newMockDaemonClient()
	// No jobs in the map

	commits := []string{"unreviewed1", "unreviewed2"}

	pending, err := findPendingJobForBranch(client, "/repo", commits)
	if err != nil {
		t.Fatalf("findPendingJobForBranch failed: %v", err)
	}

	if pending != nil {
		t.Errorf("expected nil when no jobs exist, got job %d", pending.ID)
	}
}

func TestFindPendingJobForBranch_OldestFirst(t *testing.T) {
	client := newMockDaemonClient()

	// Two running jobs - should return oldest (commit1)
	client.
		WithJob(100, "commit1", storage.JobStatusRunning).
		WithJob(200, "commit2", storage.JobStatusRunning)

	commits := []string{"commit1", "commit2"}

	pending, err := findPendingJobForBranch(client, "/repo", commits)
	if err != nil {
		t.Fatalf("findPendingJobForBranch failed: %v", err)
	}

	if pending == nil {
		t.Fatal("expected to find a pending job")
	}
	// Should return oldest pending job (commit1 is first in list)
	if pending.ID != 100 {
		t.Errorf("expected oldest pending job 100, got %d", pending.ID)
	}
}

// setupTestGitRepo creates a git repo for testing branch/--since behavior.
// Returns the repo directory, base commit SHA, and a helper to run git commands.
func setupTestGitRepo(t *testing.T) (repoDir string, baseSHA string, runGit func(args ...string) string) {
	t.Helper()

	repoDir = t.TempDir()
	runGit = func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Use git init + symbolic-ref for compatibility with Git < 2.28 (which lacks -b flag)
	runGit("init")
	runGit("symbolic-ref", "HEAD", "refs/heads/main")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")

	baseSHA = gitCommitFile(t, repoDir, runGit, "base.txt", "base", "base commit")

	return repoDir, baseSHA, runGit
}

func TestValidateRefineContext_RefusesMainBranchWithoutSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoDir, _, _ := setupTestGitRepo(t)

	// Stay on main branch (don't create feature branch)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Validating without --since on main should fail
	_, _, _, _, err = validateRefineContext("")
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

	repoDir, baseSHA, runGit := setupTestGitRepo(t)

	// Add another commit on main
	gitCommitFile(t, repoDir, runGit, "second.txt", "second", "second commit")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Validating with --since on main should pass
	repoPath, currentBranch, _, mergeBase, err := validateRefineContext(baseSHA)
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

	repoDir, baseSHA, runGit := setupTestGitRepo(t)

	// Create feature branch with commits
	runGit("checkout", "-b", "feature")
	gitCommitFile(t, repoDir, runGit, "feature.txt", "feature", "feature commit")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// --since should work on feature branch
	repoPath, currentBranch, _, mergeBase, err := validateRefineContext(baseSHA)
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

	repoDir, _, _ := setupTestGitRepo(t)

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Invalid --since ref should fail with clear error
	_, _, _, _, err = validateRefineContext("nonexistent-ref-abc123")
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

	repoDir, _, runGit := setupTestGitRepo(t)

	// Create a commit on a separate branch that diverges from main
	runGit("checkout", "-b", "other-branch")
	otherBranchSHA := gitCommitFile(t, repoDir, runGit, "other.txt", "other", "commit on other branch")

	// Go back to main and create a different commit
	runGit("checkout", "main")
	gitCommitFile(t, repoDir, runGit, "main2.txt", "main2", "second commit on main")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Using --since with a commit from a different branch (not ancestor of HEAD) should fail
	_, _, _, _, err = validateRefineContext(otherBranchSHA)
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

	repoDir, baseSHA, runGit := setupTestGitRepo(t)

	// Create feature branch
	runGit("checkout", "-b", "feature")
	gitCommitFile(t, repoDir, runGit, "feature.txt", "feature", "feature commit")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Feature branch without --since should pass validation (uses merge-base)
	repoPath, currentBranch, _, mergeBase, err := validateRefineContext("")
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

func TestWorktreeCleanupBetweenIterations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoDir, _, _ := setupTestGitRepo(t)

	// Simulate the refine loop pattern: create a worktree, then clean it up
	// before the next iteration. Verify the directory is removed each time.
	var prevPath string
	for i := 0; i < 3; i++ {
		worktreePath, cleanup, err := createTempWorktree(repoDir)
		if err != nil {
			t.Fatalf("iteration %d: createTempWorktree failed: %v", i, err)
		}

		// Verify previous worktree was cleaned up
		if prevPath != "" {
			if _, err := os.Stat(prevPath); !os.IsNotExist(err) {
				t.Fatalf("iteration %d: previous worktree %s still exists after cleanup", i, prevPath)
			}
		}

		// Verify current worktree exists
		if _, err := os.Stat(worktreePath); err != nil {
			t.Fatalf("iteration %d: worktree %s should exist: %v", i, worktreePath, err)
		}

		// Simulate the explicit cleanup call (as done on error/no-change paths)
		cleanup()
		prevPath = worktreePath
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

	repoDir, _, runGit := setupTestGitRepo(t)

	installGitHook(t, repoDir, "post-checkout", "#!/bin/sh\nexit 1\n")

	// Verify the hook is active (a normal worktree add would fail)
	failDir := t.TempDir()
	cmd := exec.Command("git", "-C", repoDir, "worktree", "add", "--detach", failDir, "HEAD")
	if out, err := cmd.CombinedOutput(); err == nil {
		// Clean up the worktree before failing
		exec.Command("git", "-C", repoDir, "worktree", "remove", "--force", failDir).Run()
		// Some git versions don't fail on post-checkout hook errors.
		// In that case, verify our approach still succeeds.
		_ = out
	}

	// createTempWorktree should succeed because it suppresses hooks
	worktreePath, cleanup, err := createTempWorktree(repoDir)
	if err != nil {
		t.Fatalf("createTempWorktree should succeed with failing hook: %v", err)
	}
	defer cleanup()

	// Verify the worktree directory exists and has the file from the repo
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree directory should exist: %v", err)
	}

	baseFile := filepath.Join(worktreePath, "base.txt")
	content, err := os.ReadFile(baseFile)
	if err != nil {
		t.Fatalf("expected base.txt in worktree: %v", err)
	}
	if string(content) != "base" {
		t.Errorf("expected content 'base', got %q", string(content))
	}

	_ = runGit // used by setupTestGitRepo
}

func TestCommitWithHookRetrySucceeds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoDir, _, runGit := setupTestGitRepo(t)

	// Install a pre-commit hook that fails on the first 2 calls and
	// succeeds on the 3rd+. The hook runs twice before a retry: once
	// by git commit, once by the hook probe. A counter file tracks calls.
	installGitHook(t, repoDir, "pre-commit", `#!/bin/sh
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
	if err := os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	testAgent := agent.NewTestAgent()
	sha, err := commitWithHookRetry(repoDir, "test commit", testAgent, true)
	if err != nil {
		t.Fatalf("commitWithHookRetry should succeed: %v", err)
	}

	if sha == "" {
		t.Fatal("expected non-empty SHA")
	}

	// Verify the commit exists
	commitSHA := runGit("rev-parse", "HEAD")
	if commitSHA != sha {
		t.Errorf("expected HEAD=%s, got %s", sha, commitSHA)
	}
}

func TestCommitWithHookRetryExhausted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoDir, _, _ := setupTestGitRepo(t)

	installGitHook(t, repoDir, "pre-commit",
		"#!/bin/sh\necho 'always fails' >&2\nexit 1\n")

	// Make a file change
	if err := os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repoDir, "test commit", testAgent, true)
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

	repoDir, _, _ := setupTestGitRepo(t)

	// No pre-commit hook installed. Commit with no changes will fail
	// for a non-hook reason ("nothing to commit").
	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repoDir, "empty commit", testAgent, true)
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

	repoDir, _, _ := setupTestGitRepo(t)

	installGitHook(t, repoDir, "pre-commit", "#!/bin/sh\nexit 0\n")

	// Make a change so there's something to commit
	if err := os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create index.lock to make git add fail (non-hook failure)
	lockFile := filepath.Join(repoDir, ".git", "index.lock")
	if err := os.WriteFile(lockFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lockFile)

	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repoDir, "test commit", testAgent, true)
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

	repoDir, _, _ := setupTestGitRepo(t)

	installGitHook(t, repoDir, "pre-commit", "#!/bin/sh\nexit 0\n")

	// No changes to commit — "nothing to commit" is a commit-phase
	// failure, but the hook passes, so HookFailed should be false.
	testAgent := agent.NewTestAgent()
	_, err := commitWithHookRetry(repoDir, "empty commit", testAgent, true)
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
