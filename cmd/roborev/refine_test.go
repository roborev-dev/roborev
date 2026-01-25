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

// mockDaemonClient is a test implementation of daemon.Client
type mockDaemonClient struct {
	reviews   map[string]*storage.Review // keyed by SHA
	jobs      map[int64]*storage.ReviewJob
	responses map[int64][]storage.Response

	// Track calls for assertions
	addressedReviews []int64
	addedComments   []addedComment
	enqueuedReviews  []enqueuedReview

	// Configurable errors for testing error paths
	markAddressedErr   error
	getReviewBySHAErr  error
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

func (m *mockDaemonClient) MarkReviewAddressed(reviewID int64) error {
	if m.markAddressedErr != nil {
		return m.markAddressedErr
	}
	m.addressedReviews = append(m.addressedReviews, reviewID)
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

// Verify mockDaemonClient implements daemon.Client
var _ daemon.Client = (*mockDaemonClient)(nil)

func TestSelectRefineAgentCodexFallback(t *testing.T) {
	t.Setenv("PATH", "")

	selected, err := selectRefineAgent("codex", agent.ReasoningFast)
	if err != nil {
		t.Fatalf("selectRefineAgent failed: %v", err)
	}

	if selected.Name() == "codex" {
		t.Fatalf("expected fallback agent when codex is unavailable")
	}

	if ta, ok := selected.(*agent.TestAgent); ok {
		if ta.Reasoning != agent.ReasoningFast {
			t.Fatalf("expected fallback agent to use requested reasoning, got %q", ta.Reasoning)
		}
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
	var codexPath string
	if runtime.GOOS == "windows" {
		// On Windows, create a batch file that exits successfully
		codexPath = filepath.Join(tmpDir, "codex.bat")
		if err := os.WriteFile(codexPath, []byte("@exit /b 0\r\n"), 0755); err != nil {
			t.Fatalf("write codex stub: %v", err)
		}
	} else {
		codexPath = filepath.Join(tmpDir, "codex")
		if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatalf("write codex stub: %v", err)
		}
	}

	t.Setenv("PATH", tmpDir)

	selected, err := selectRefineAgent("codex", agent.ReasoningFast)
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
	var codexPath string
	if runtime.GOOS == "windows" {
		// On Windows, create a batch file that exits successfully
		codexPath = filepath.Join(tmpDir, "codex.bat")
		if err := os.WriteFile(codexPath, []byte("@exit /b 0\r\n"), 0755); err != nil {
			t.Fatalf("write codex stub: %v", err)
		}
	} else {
		codexPath = filepath.Join(tmpDir, "codex")
		if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatalf("write codex stub: %v", err)
		}
	}

	t.Setenv("PATH", tmpDir)

	// Request an unavailable agent (claude), codex should be used as fallback
	selected, err := selectRefineAgent("claude", agent.ReasoningThorough)
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

	// Mock reviews: oldest commit passes, middle fails, newest fails
	client.reviews = map[string]*storage.Review{
		"oldest123": {ID: 1, JobID: 100, Output: "No issues found."},                           // Pass
		"middle456": {ID: 2, JobID: 200, Output: "Found a bug in the code.", Addressed: false}, // Fail (oldest failure)
		"newest789": {ID: 3, JobID: 300, Output: "Security vulnerability detected."},           // Fail (newest)
	}

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

	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "Bug found."},                    // Fail, not addressed (oldest)
		"commit2": {ID: 2, JobID: 200, Output: "Another bug.", Addressed: true}, // Fail, but addressed
		"commit3": {ID: 3, JobID: 300, Output: "More issues."},                  // Fail, not addressed (newest)
	}

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

	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "Bug found."},       // Fail (oldest, but in skip set)
		"commit2": {ID: 2, JobID: 200, Output: "Another bug."},     // Fail (should be returned)
		"commit3": {ID: 3, JobID: 300, Output: "No issues found."}, // Pass
	}

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

	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "Bug found."}, // Fail (in skip set)
		"commit2": {ID: 2, JobID: 200, Output: "Another."},   // Fail (in skip set)
	}

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

	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "No issues found."},
		"commit2": {ID: 2, JobID: 200, Output: "No findings."},
	}

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
	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "No issues found.", Addressed: false},
		"commit2": {ID: 2, JobID: 200, Output: "No findings.", Addressed: false},
	}

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// No failed reviews should be found
	if found != nil {
		t.Errorf("expected no failed reviews, got job %d", found.JobID)
	}

	// Both passing reviews should be marked as addressed
	if len(client.addressedReviews) != 2 {
		t.Errorf("expected 2 reviews to be marked addressed, got %d", len(client.addressedReviews))
	}

	// Verify the specific review IDs were marked
	addressed := make(map[int64]bool)
	for _, id := range client.addressedReviews {
		addressed[id] = true
	}
	if !addressed[1] || !addressed[2] {
		t.Errorf("expected reviews 1 and 2 to be marked addressed, got %v", client.addressedReviews)
	}
}

func TestFindFailedReviewForBranch_MarksPassingBeforeFailure(t *testing.T) {
	client := newMockDaemonClient()

	// First commit passes (unaddressed), second fails
	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "No issues found.", Addressed: false}, // Pass
		"commit2": {ID: 2, JobID: 200, Output: "Bug found."},                          // Fail
	}

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return the failing review
	if found == nil || found.JobID != 200 {
		t.Errorf("expected failed review (job 200), got %v", found)
	}

	// Passing review before the failure should be marked addressed
	if len(client.addressedReviews) != 1 {
		t.Errorf("expected 1 review to be marked addressed, got %d", len(client.addressedReviews))
	}
	if len(client.addressedReviews) > 0 && client.addressedReviews[0] != 1 {
		t.Errorf("expected review 1 to be marked addressed, got %v", client.addressedReviews)
	}
}

func TestFindFailedReviewForBranch_DoesNotMarkAlreadyAddressed(t *testing.T) {
	client := newMockDaemonClient()

	// Passing review already addressed - should not be marked again
	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "No issues found.", Addressed: true},
		"commit2": {ID: 2, JobID: 200, Output: "Bug found."},
	}

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits, nil)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	if found == nil || found.JobID != 200 {
		t.Errorf("expected failed review (job 200), got %v", found)
	}

	// Already-addressed review should NOT be marked again
	if len(client.addressedReviews) != 0 {
		t.Errorf("expected no reviews to be marked addressed (already addressed), got %v", client.addressedReviews)
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
	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "No issues found.", Addressed: false},
		"commit2": {ID: 2, JobID: 200, Output: "No issues.", Addressed: true},
		"commit3": {ID: 3, JobID: 300, Output: "Bug found.", Addressed: true},
		"commit4": {ID: 4, JobID: 400, Output: "No findings detected.", Addressed: false},
		"commit5": {ID: 5, JobID: 500, Output: "Critical error."},
	}

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
	if len(client.addressedReviews) != 2 {
		t.Errorf("expected 2 reviews to be marked addressed, got %d: %v", len(client.addressedReviews), client.addressedReviews)
	}

	addressed := make(map[int64]bool)
	for _, id := range client.addressedReviews {
		addressed[id] = true
	}
	if !addressed[1] || !addressed[4] {
		t.Errorf("expected reviews 1 and 4 to be marked addressed, got %v", client.addressedReviews)
	}
}

func TestFindFailedReviewForBranch_StopsAtFirstFailure(t *testing.T) {
	client := newMockDaemonClient()

	// Multiple failures - should stop at the first (oldest) one
	// and not process subsequent commits
	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "Bug found."},       // Fail - should be returned
		"commit2": {ID: 2, JobID: 200, Output: "No issues found."}, // Pass - should NOT be marked (not reached)
		"commit3": {ID: 3, JobID: 300, Output: "Another bug."},     // Fail - should NOT be processed
	}

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
	if len(client.addressedReviews) != 0 {
		t.Errorf("expected no reviews to be marked addressed, got %v", client.addressedReviews)
	}
}

func TestFindFailedReviewForBranch_MarkAddressedError(t *testing.T) {
	client := newMockDaemonClient()

	// A passing review that will trigger MarkReviewAddressed
	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "No issues found.", Addressed: false},
	}

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

	// Error message should indicate which review failed
	expectedMsg := "marking review 1 as addressed"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("error should mention review ID, got: %v", err)
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
	client.jobs = map[int64]*storage.ReviewJob{
		100: {ID: 100, GitRef: "commit1", Status: storage.JobStatusDone},
		200: {ID: 200, GitRef: "commit2", Status: storage.JobStatusRunning},
	}

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
	client.jobs = map[int64]*storage.ReviewJob{
		100: {ID: 100, GitRef: "commit1", Status: storage.JobStatusQueued},
	}

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
	client.jobs = map[int64]*storage.ReviewJob{
		100: {ID: 100, GitRef: "commit1", Status: storage.JobStatusDone},
		200: {ID: 200, GitRef: "commit2", Status: storage.JobStatusDone},
	}

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
	client.jobs = map[int64]*storage.ReviewJob{
		100: {ID: 100, GitRef: "commit1", Status: storage.JobStatusRunning},
		200: {ID: 200, GitRef: "commit2", Status: storage.JobStatusRunning},
	}

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

	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "base.txt")
	runGit("commit", "-m", "base commit")
	baseSHA = runGit("rev-parse", "HEAD")

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
	if err := os.WriteFile(filepath.Join(repoDir, "second.txt"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "second.txt")
	runGit("commit", "-m", "second commit")

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
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "feature.txt")
	runGit("commit", "-m", "feature commit")

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
	if err := os.WriteFile(filepath.Join(repoDir, "other.txt"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "other.txt")
	runGit("commit", "-m", "commit on other branch")
	otherBranchSHA := runGit("rev-parse", "HEAD")

	// Go back to main and create a different commit
	runGit("checkout", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "main2.txt"), []byte("main2"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "main2.txt")
	runGit("commit", "-m", "second commit on main")

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
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "feature.txt")
	runGit("commit", "-m", "feature commit")

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
