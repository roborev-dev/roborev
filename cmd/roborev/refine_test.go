package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesm/roborev/internal/agent"
	"github.com/wesm/roborev/internal/config"
	"github.com/wesm/roborev/internal/daemon"
	"github.com/wesm/roborev/internal/storage"
)

// mockDaemonClient is a test implementation of daemon.Client
type mockDaemonClient struct {
	reviews   map[string]*storage.Review // keyed by SHA
	jobs      map[int64]*storage.ReviewJob
	responses map[int64][]storage.Response

	// Track calls for assertions
	addressedReviews []int64
	addedResponses   []addedResponse
	enqueuedReviews  []enqueuedReview

	// Configurable errors for testing error paths
	markAddressedErr error
}

type addedResponse struct {
	JobID     int64
	Responder string
	Response  string
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

func (m *mockDaemonClient) AddResponse(jobID int64, responder, response string) error {
	m.addedResponses = append(m.addedResponses, addedResponse{jobID, responder, response})
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

func (m *mockDaemonClient) GetResponsesForJob(jobID int64) ([]storage.Response, error) {
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
	cfg := &config.Config{AllowUnsafeAgents: true}
	if !resolveAllowUnsafeAgents(false, cfg) {
		t.Fatal("expected config to enable unsafe agents")
	}

	cfg.AllowUnsafeAgents = false
	if resolveAllowUnsafeAgents(false, cfg) {
		t.Fatal("expected unsafe agents to remain disabled")
	}
	if !resolveAllowUnsafeAgents(true, cfg) {
		t.Fatal("expected CLI flag to enable unsafe agents")
	}
	if resolveAllowUnsafeAgents(false, nil) {
		t.Fatal("expected unsafe agents to remain disabled without config")
	}
}

func TestSelectRefineAgentCodexUsesRequestedReasoning(t *testing.T) {
	tmpDir := t.TempDir()
	codexPath := filepath.Join(tmpDir, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write codex stub: %v", err)
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
	codexPath := filepath.Join(tmpDir, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write codex stub: %v", err)
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

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)
	if err != nil {
		t.Fatalf("findFailedReviewForBranch failed: %v", err)
	}

	// Should return commit1 (oldest unaddressed failure), skipping addressed commit2
	if found == nil || found.JobID != 100 {
		t.Errorf("expected oldest unaddressed failure (job 100), got %v", found)
	}
}

func TestFindFailedReviewForBranch_AllPass(t *testing.T) {
	client := newMockDaemonClient()

	client.reviews = map[string]*storage.Review{
		"commit1": {ID: 1, JobID: 100, Output: "No issues found."},
		"commit2": {ID: 2, JobID: 200, Output: "No findings."},
	}

	commits := []string{"commit1", "commit2"}

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)
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

	found, err := findFailedReviewForBranch(client, commits)

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
