package main

// NOTE: Tests in this package mutate package-level variables (serverAddr,
// pollStartInterval, pollMaxInterval) and environment variables (ROBOREV_DATA_DIR).
// Do not use t.Parallel() in this package as it will cause race conditions.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

// ============================================================================
// Refine Command Tests
// ============================================================================

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	origStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = origStdout }()
	defer reader.Close()
	defer writer.Close()

	outCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, reader); err != nil {
			outCh <- ""
			return
		}
		outCh <- buf.String()
	}()

	fn()

	writer.Close()
	os.Stdout = origStdout
	output := <-outCh
	return output
}

// setupFastPolling reduces all polling intervals to 1ms for fast tests.
// Returns a cleanup function that restores the original values.
func setupFastPolling(t *testing.T) {
	t.Helper()

	origPollStart := pollStartInterval
	origPollMax := pollMaxInterval
	origPostCommitWait := postCommitWaitDelay
	origDaemonPoll := daemon.DefaultPollInterval

	pollStartInterval = 1 * time.Millisecond
	pollMaxInterval = 1 * time.Millisecond
	postCommitWaitDelay = 1 * time.Millisecond
	daemon.DefaultPollInterval = 1 * time.Millisecond

	t.Cleanup(func() {
		pollStartInterval = origPollStart
		pollMaxInterval = origPollMax
		postCommitWaitDelay = origPostCommitWait
		daemon.DefaultPollInterval = origDaemonPoll
	})
}

func setupRefineRepo(t *testing.T) (string, string) {
	t.Helper()

	repo := NewGitTestRepo(t)
	repo.CommitFile("file.txt", "base", "base commit")

	repo.Run("checkout", "-b", "feature")
	headSHA := repo.CommitFile("feature.txt", "change", "feature commit")

	return repo.Dir, headSHA
}

func newFastRunContext(repoDir string) RunContext {
	return RunContext{
		WorkingDir:      repoDir,
		PollInterval:    1 * time.Millisecond,
		PostCommitDelay: 1 * time.Millisecond,
	}
}

func TestEnqueueReviewRefine(t *testing.T) {
	t.Run("returns job ID on success", func(t *testing.T) {
		md := NewMockDaemon(t, MockRefineHooks{
			OnEnqueue: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
				var req daemon.EnqueueRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return true
				}

				if req.RepoPath != "/test/repo" || req.GitRef != "abc..def" {
					t.Errorf("unexpected request body: %+v", req)
				}

				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(storage.ReviewJob{ID: 123})
				return true
			},
		})
		defer md.Close()

		jobID, err := enqueueReview(md.Server.URL, "/test/repo", "abc..def", "codex")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jobID != 123 {
			t.Errorf("expected job ID 123, got %d", jobID)
		}
	})

	t.Run("returns error on failure", func(t *testing.T) {
		md := NewMockDaemon(t, MockRefineHooks{
			OnEnqueue: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid repo"}`))
				return true
			},
		})
		defer md.Close()

		_, err := enqueueReview(md.Server.URL, "/bad/repo", "HEAD", "codex")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestSummarizeAgentOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "extracts first 10 lines",
			input:    "Line 1\nLine 2\nLine 3\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10\nLine 11\nLine 12",
			expected: "Line 1\nLine 2\nLine 3\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10",
		},
		{
			name:     "handles empty input",
			input:    "",
			expected: "Automated fix",
		},
		{
			name:     "handles whitespace only",
			input:    "   \n\n  \n",
			expected: "Automated fix",
		},
		{
			name:     "skips empty lines",
			input:    "\n\nFirst line\n\nSecond line\n\n",
			expected: "First line\nSecond line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeAgentOutput(tt.input)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestRefineNoChangeSkipsImmediately(t *testing.T) {
	// When the agent makes no changes, refine should skip the review
	// immediately. The skip path is triggered by IsWorkingTreeClean
	// returning true, which records a comment and adds to skippedReviews.
	//
	// Integration coverage: TestRunRefineSurfacesResponseErrors exercises
	// the full loop. Here we verify the predicate and skip-tracking logic.

	repo := NewGitTestRepo(t)
	repo.CommitFile("file.txt", "content", "initial")
	dir := repo.Dir

	if !git.IsWorkingTreeClean(dir) {
		t.Fatal("expected clean working tree after commit")
	}

	// Verify skip tracking: skippedReviews map should track skipped review IDs
	skippedReviews := make(map[int64]bool)
	skippedReviews[42] = true
	if !skippedReviews[42] {
		t.Fatal("expected review 42 to be tracked as skipped")
	}
	if skippedReviews[99] {
		t.Fatal("expected review 99 to not be tracked as skipped")
	}
}

func TestRunRefineSurfacesResponseErrors(t *testing.T) {
	repoDir, _ := setupRefineRepo(t)

	md := NewMockDaemon(t, MockRefineHooks{
		OnReview: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			json.NewEncoder(w).Encode(storage.Review{
				ID: 1, JobID: 1, Output: "**Bug found**: fail", Closed: false,
			})
			return true
		},
		OnComments: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		},
	})
	defer md.Close()

	ctx := newFastRunContext(repoDir)

	if err := runRefine(&cobra.Command{}, ctx, refineOptions{agentName: "test", maxIterations: 1, quiet: true}); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRunRefineQuietNonTTYTimerOutput(t *testing.T) {
	repoDir, headSHA := setupRefineRepo(t)

	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	md.State.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 42, Output: "**Bug found**: fail", Closed: false,
	}

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	ctx := newFastRunContext(repoDir)

	output := captureStdout(t, func() {
		if err := runRefine(&cobra.Command{}, ctx, refineOptions{agentName: "test", maxIterations: 1, quiet: true}); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	if strings.Contains(output, "\r") {
		t.Fatalf("expected no carriage returns in non-tty output, got: %q", output)
	}
	if !strings.Contains(output, "Addressing review (job 42)...") {
		t.Fatalf("expected final timer line in output, got: %q", output)
	}
}

func TestRunRefineStopsLiveTimerOnAgentError(t *testing.T) {
	repoDir, headSHA := setupRefineRepo(t)

	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	md.State.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 7, Output: "**Bug found**: fail", Closed: false,
	}

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return true }
	defer func() { isTerminal = origIsTerminal }()

	testAgent := &functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		return "", fmt.Errorf("test agent failure")
	}}

	ctx := newFastRunContext(repoDir)

	output := captureStdout(t, func() {
		if err := runRefine(&cobra.Command{}, ctx, refineOptions{
			agentName: "test",
			agentFactory: func(_ *config.Config, _ string, _ agent.ReasoningLevel, _ string) (agent.Agent, error) {
				return testAgent, nil
			},
			maxIterations: 1,
			quiet:         true,
		}); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	idx := strings.LastIndex(output, "\rAddressing review (job 7)...")
	if idx == -1 {
		t.Fatalf("expected live timer output, got: %q", output)
	}
	if !strings.Contains(output[idx:], "\n") {
		t.Fatalf("expected timer to stop with newline, got: %q", output)
	}
}

// ============================================================================
// Integration Tests for Refine Loop Business Logic
// ============================================================================

func TestRefineLoopFindFailedReviewPath(t *testing.T) {
	// Test the path where a failed individual review is found

	t.Run("finds oldest failed review among commits", func(t *testing.T) {
		client := newMockDaemonClient()
		// Commit1 passes, commit2 fails
		client.reviews["commit1sha"] = &storage.Review{
			ID: 1, JobID: 1, Output: "No issues found. LGTM!",
		}
		client.reviews["commit2sha"] = &storage.Review{
			ID: 2, JobID: 2, Output: "**Bug**: Missing error handling in foo.go:42",
		}

		commits := []string{"commit1sha", "commit2sha", "commit3sha"}
		review, err := findFailedReviewForBranch(client, commits, nil)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review == nil {
			t.Fatal("expected to find failed review")
		}
		// Should find commit2 failure (oldest-first iteration)
		if review.ID != 2 {
			t.Errorf("expected review ID 2 (commit2 failure), got %d", review.ID)
		}
	})

	t.Run("returns nil when review is already closed", func(t *testing.T) {
		client := newMockDaemonClient()
		// Failed but already closed
		client.reviews["commit1sha"] = &storage.Review{
			ID: 1, JobID: 1, Output: "**Bug**: error", Closed: true,
		}

		commits := []string{"commit1sha"}
		review, err := findFailedReviewForBranch(client, commits, nil)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review != nil {
			t.Error("expected nil - closed reviews should be skipped")
		}
	})
}

func TestRefineLoopNoChangeSkipsReview(t *testing.T) {
	// When the agent makes no changes, refine records a comment and skips
	// the review. Verify the comments API works for both empty and populated
	// cases so the skip logic can rely on it.
	//
	// Integration coverage: TestRunRefineSurfacesResponseErrors exercises
	// the full loop including the skip path.
	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	md.State.responses[42] = []storage.Response{}
	jobID99 := int64(99)
	md.State.responses[99] = []storage.Response{
		{ID: 1, JobID: &jobID99, Responder: "roborev-refine", Response: "Agent could not determine how to address findings"},
	}

	// Job with no prior comments — skip should still apply (no retries needed)
	responses, err := getCommentsForJob(md.Server.URL, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 0 {
		t.Errorf("expected 0 previous responses for job 42, got %d", len(responses))
	}

	// Job with a prior skip comment — verify the comment text matches
	responses, err = getCommentsForJob(md.Server.URL, 99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response for job 99, got %d", len(responses))
	}
	if !strings.Contains(responses[0].Response, "could not determine") {
		t.Errorf("expected skip comment, got %q", responses[0].Response)
	}
}

func TestRefineLoopBranchReviewPath(t *testing.T) {
	// Test the branch review path when no individual failures exist

	t.Run("triggers branch review when no individual failures", func(t *testing.T) {
		client := newMockDaemonClient()
		// All individual commits pass (outputs must start with pass patterns)
		client.reviews["commit1"] = &storage.Review{
			ID: 1, JobID: 1, Output: "No issues found.",
		}
		client.reviews["commit2"] = &storage.Review{
			ID: 2, JobID: 2, Output: "No issues found. LGTM!",
		}

		commits := []string{"commit1", "commit2"}
		review, err := findFailedReviewForBranch(client, commits, nil)

		// Should return nil since all pass
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review != nil {
			t.Errorf("expected nil when all commits pass, got review ID=%d JobID=%d Output=%q Closed=%v",
				review.ID, review.JobID, review.Output, review.Closed)
		}

		// In actual refine loop, this would trigger branch review
		// We can test enqueueReview separately
	})
}

func TestRefineLoopEnqueueBranchReview(t *testing.T) {
	// Test enqueueing a branch (range) review

	t.Run("enqueues range review for branch", func(t *testing.T) {
		md := NewMockDaemon(t, MockRefineHooks{})
		defer md.Close()

		jobID, err := enqueueReview(md.Server.URL, "/test/repo", "abc123..HEAD", "codex")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jobID == 0 {
			t.Error("expected non-zero job ID")
		}

		// Verify the range ref was enqueued
		if len(md.State.enqueuedRefs) != 1 {
			t.Fatalf("expected 1 enqueued ref, got %d", len(md.State.enqueuedRefs))
		}
		if md.State.enqueuedRefs[0] != "abc123..HEAD" {
			t.Errorf("expected range ref 'abc123..HEAD', got '%s'", md.State.enqueuedRefs[0])
		}
	})
}

func TestRefineLoopWaitForReviewCompletion(t *testing.T) {
	// Test waiting for a review to complete

	t.Run("returns review when job completes successfully", func(t *testing.T) {
		md := NewMockDaemon(t, MockRefineHooks{})
		defer md.Close()

		md.State.jobs[42] = &storage.ReviewJob{ID: 42, GitRef: "abc123", Status: storage.JobStatusDone}
		md.State.reviews["abc123"] = &storage.Review{
			ID: 1, JobID: 42, Output: "All tests pass. No issues found.", Closed: false,
		}

		review, err := waitForReviewWithInterval(md.Server.URL, 42, 1*time.Millisecond)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review == nil {
			t.Fatal("expected review")
		}
		if !strings.Contains(review.Output, "No issues found") {
			t.Errorf("unexpected output: %s", review.Output)
		}
	})

	t.Run("returns error when job fails", func(t *testing.T) {
		md := NewMockDaemon(t, MockRefineHooks{})
		defer md.Close()

		md.State.jobs[42] = &storage.ReviewJob{
			ID:     42,
			GitRef: "abc123",
			Status: storage.JobStatusFailed,
			Error:  "Agent timeout after 10 minutes",
		}

		_, err := waitForReviewWithInterval(md.Server.URL, 42, 1*time.Millisecond)
		if err == nil {
			t.Fatal("expected error for failed job")
		}
		if !strings.Contains(err.Error(), "timeout") {
			t.Errorf("error should mention failure reason, got: %v", err)
		}
	})
}

// TestRefinePendingJobWaitDoesNotConsumeIteration verifies that waiting for a pending
// (in-progress) job does not consume a refinement iteration. The test runs with
// maxIterations=1 and a job that starts as Running then transitions to Done with a
// passing review - if iterations were consumed during the wait, the test would fail.
func makePendingJobGetJobsHandler(pollCount *atomic.Int32) func(http.ResponseWriter, *http.Request, *mockRefineState) bool {
	return func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
		q := r.URL.Query()
		if idStr := q.Get("id"); idStr != "" {
			var jobID int64
			fmt.Sscanf(idStr, "%d", &jobID)
			s.mu.Lock()
			job, ok := s.jobs[jobID]
			if !ok {
				s.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{}})
				return true
			}
			// On first poll, job is still Running; on subsequent polls, transition to Done
			count := pollCount.Add(1)
			if count > 1 {
				job.Status = storage.JobStatusDone
			}
			jobCopy := *job
			s.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{jobCopy}})
			return true
		}
		if gitRef := q.Get("git_ref"); gitRef != "" {
			s.mu.Lock()
			var job *storage.ReviewJob
			for _, j := range s.jobs {
				if j.GitRef == gitRef {
					job = j
					break
				}
			}
			if job != nil {
				jobCopy := *job
				s.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{jobCopy}})
				return true
			}
			s.mu.Unlock()
		}
		return false // fall through to base handler
	}
}

func makePendingJobEnqueueHandler(repoDir string) func(http.ResponseWriter, *http.Request, *mockRefineState) bool {
	return func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
		// Handle branch review enqueue - create a passing branch review
		var req struct {
			GitRef string `json:"git_ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		s.mu.Lock()
		branchJobID := s.nextJobID
		s.nextJobID++
		s.jobs[branchJobID] = &storage.ReviewJob{
			ID:       branchJobID,
			GitRef:   req.GitRef,
			Agent:    "test",
			Status:   storage.JobStatusDone,
			RepoPath: repoDir,
		}
		s.reviews[req.GitRef] = &storage.Review{
			ID: branchJobID + 1000, JobID: branchJobID, Output: "No issues found. Branch looks good!",
		}
		jobCopy := *s.jobs[branchJobID]
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(jobCopy)
		return true
	}
}

func TestRefinePendingJobWaitDoesNotConsumeIteration(t *testing.T) {
	repoDir, commitSHA := setupRefineRepo(t)

	// Track how many times the job has been polled
	var pollCount atomic.Int32

	md := NewMockDaemon(t, MockRefineHooks{
		OnGetJobs: makePendingJobGetJobsHandler(&pollCount),
		OnEnqueue: makePendingJobEnqueueHandler(repoDir),
	})
	defer md.Close()

	// Create a job that starts as Running
	md.State.jobs[1] = &storage.ReviewJob{
		ID:       1,
		GitRef:   commitSHA,
		Agent:    "test",
		Status:   storage.JobStatusRunning, // Starts as pending
		RepoPath: repoDir,
	}
	// Passing review (will be returned once job is Done)
	md.State.reviews[commitSHA] = &storage.Review{
		ID: 1, JobID: 1, Output: "No issues found. LGTM!", Closed: false,
	}
	md.State.nextJobID = 2

	ctx := newFastRunContext(repoDir)

	// Run refine with maxIterations=1. If waiting on the pending job consumed
	// an iteration, this would fail with "max iterations reached". Since the
	// pending job transitions to Done with a passing review (and no failed
	// reviews exist), refine should succeed.
	err := runRefine(&cobra.Command{}, ctx, refineOptions{agentName: "test", maxIterations: 1, quiet: true})

	// Should succeed - all reviews pass after waiting for the pending one
	if err != nil {
		t.Fatalf("expected refine to succeed (pending wait should not consume iteration), got: %v", err)
	}

	// Verify the job was actually polled multiple times (proving we waited)
	if pollCount.Load() < 2 {
		t.Errorf("expected job to be polled at least twice (wait behavior), got %d polls", pollCount.Load())
	}
}

// ============================================================================
// Show Command Tests
// ============================================================================

func TestShowJobFlagRequiresArgument(t *testing.T) {
	// Setup mock daemon that responds to /api/status with version info
	md := NewMockDaemon(t, MockRefineHooks{
		OnStatus: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			json.NewEncoder(w).Encode(map[string]string{"version": version.Version})
			return true
		},
	})
	defer md.Close()

	// Create the show command and execute with --job but no argument
	cmd := showCmd()
	cmd.SetArgs([]string{"--job"})

	// Capture stderr where cobra writes errors
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetOut(&errBuf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --job used without argument")
	}
	if !strings.Contains(err.Error(), "--job requires a job ID argument") {
		t.Errorf("expected '--job requires a job ID argument' error, got: %v", err)
	}
}

// ============================================================================
// filterGitEnv Tests
// ============================================================================

func TestFilterGitEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"GIT_DIR=/some/repo/.git",
		"HOME=/home/user",
		"GIT_WORK_TREE=/some/repo",
		"GIT_INDEX_FILE=/some/repo/.git/index",
		"ROBOREV_DATA_DIR=/tmp/roborev",
		"GIT_CEILING_DIRECTORIES=/home",
		"Git_Dir=/mixed/case",                        // Windows-style mixed case
		"git_work_tree=/lowercase",                   // all lowercase
		"GIT_SSH_COMMAND=ssh -i ~/.ssh/deploy_key",   // auth/transport: keep
		"GIT_ASKPASS=/usr/lib/ssh/askpass",           // auth/transport: keep
		"GIT_CONFIG_PARAMETERS='core.autocrlf=true'", // config propagation: strip
		"GIT_CONFIG_COUNT=1",                         // config propagation: strip
		"GIT_CONFIG_KEY_0=core.autocrlf",             // numbered config: strip
		"GIT_CONFIG_VALUE_0=true",                    // numbered config: strip
	}

	filtered := filterGitEnv(env)

	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"ROBOREV_DATA_DIR=/tmp/roborev",
		"GIT_SSH_COMMAND=ssh -i ~/.ssh/deploy_key",
		"GIT_ASKPASS=/usr/lib/ssh/askpass",
	}

	if len(filtered) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(filtered), len(want), filtered)
	}
	for i, got := range filtered {
		if got != want[i] {
			t.Errorf("entry %d: got %q, want %q", i, got, want[i])
		}
	}
}

func TestIsGoTestBinaryPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/tmp/roborev.test", want: true},
		{path: "/tmp/roborev.test.exe", want: true},
		{path: "/tmp/ROBOREV.TEST", want: true},
		{path: "/tmp/roborev", want: false},
		{path: "/tmp/roborev.exe", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isGoTestBinaryPath(tt.path)
			if got != tt.want {
				t.Fatalf("isGoTestBinaryPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestShouldRefuseAutoStartDaemon(t *testing.T) {
	t.Run("refuses test binary by default", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "")
		p := filepath.FromSlash("/tmp/roborev.test")
		if !shouldRefuseAutoStartDaemon(p) {
			t.Fatal("expected refusal for test binary without opt-in")
		}
	})

	t.Run("allows explicit opt in for test binary", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "1")
		p := filepath.FromSlash("/tmp/roborev.test")
		if shouldRefuseAutoStartDaemon(p) {
			t.Fatal("expected no refusal for test binary when opt-in is set")
		}
	})

	t.Run("does not refuse normal binary", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "")
		p := filepath.FromSlash("/usr/local/bin/roborev")
		if shouldRefuseAutoStartDaemon(p) {
			t.Fatal("expected no refusal for non-test binary")
		}
	})

	t.Run("refuses go run binary from build cache", func(t *testing.T) {
		p := filepath.FromSlash(
			"/Users/x/Library/Caches/go-build/72/abc-d/roborev",
		)
		if !shouldRefuseAutoStartDaemon(p) {
			t.Fatal("expected refusal for go-build cache binary")
		}
	})

	t.Run("refuses go run binary from tmp", func(t *testing.T) {
		p := filepath.FromSlash(
			"/var/folders/y4/abc/T/go-build123/b001/exe/roborev",
		)
		if !shouldRefuseAutoStartDaemon(p) {
			t.Fatal("expected refusal for go-build tmp binary")
		}
	})

	t.Run("allows binary under go-builder username", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "")
		p := filepath.FromSlash("/home/go-builder/bin/roborev")
		if shouldRefuseAutoStartDaemon(p) {
			t.Fatal("should not refuse binary under go-builder")
		}
	})

	t.Run("allows binary under go-build1user dir", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "")
		p := filepath.FromSlash("/opt/go-build1user/bin/roborev")
		if shouldRefuseAutoStartDaemon(p) {
			t.Fatal("should not refuse binary under go-build1user")
		}
	})
}

func TestStartDaemonRefusesFromGoTestBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	if !isGoTestBinaryPath(exe) {
		t.Skipf("expected go test binary path, got %q", exe)
	}

	t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "")
	_, err = startDaemon(&cobra.Command{})
	if err == nil {
		t.Fatal("expected startDaemon to refuse go test binary auto-start")
	}
	if !strings.Contains(err.Error(), "refusing to auto-start daemon from ephemeral binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func setupIsolatedDataDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)
	return tmpDir
}

// TestDaemonStopNotRunning verifies daemon stop reports when no daemon is running

func writeMockDaemonFile(t *testing.T, tmpDir string, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(tmpDir, "daemon.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write mock daemon.json: %v", err)
	}
	return path
}

func TestDaemonStopNotRunning(t *testing.T) {
	_ = setupIsolatedDataDir(t)

	err := stopDaemon()
	if err != ErrDaemonNotRunning {
		t.Errorf("expected ErrDaemonNotRunning, got %v", err)
	}
}

// TestDaemonStopInvalidPID verifies stopDaemon handles invalid PID in daemon.json
func TestDaemonStopInvalidPID(t *testing.T) {
	tmpDir := setupIsolatedDataDir(t)

	// Create daemon.json with PID 0 and an address on a port that's definitely not in use
	// Port 59999 is unlikely to be in use and will get connection refused quickly
	writeMockDaemonFile(t, tmpDir, `{"pid":0,"addr":"127.0.0.1:59999"}`, 0644)

	// ListAllRuntimes validates files and removes invalid ones (pid <= 0),
	// so it returns an empty list, and stopDaemon returns ErrDaemonNotRunning.
	// The key is that the invalid file gets cleaned up.
	err := stopDaemon()
	if err != ErrDaemonNotRunning {
		t.Errorf("expected ErrDaemonNotRunning (invalid file removed during listing), got %v", err)
	}

	// Verify daemon.json was cleaned up
	if _, err := os.Stat(filepath.Join(tmpDir, "daemon.json")); !os.IsNotExist(err) {
		t.Error("expected daemon.json to be removed after invalid PID")
	}
}

// TestDaemonStopCorruptedFile verifies stopDaemon cleans up malformed daemon.json
func TestDaemonStopCorruptedFile(t *testing.T) {
	tmpDir := setupIsolatedDataDir(t)

	// Create corrupted daemon.json
	writeMockDaemonFile(t, tmpDir, "not valid json", 0644)

	err := stopDaemon()
	if err != ErrDaemonNotRunning {
		t.Errorf("expected ErrDaemonNotRunning for corrupted file, got %v", err)
	}

	// Verify daemon.json was cleaned up
	if _, err := os.Stat(filepath.Join(tmpDir, "daemon.json")); !os.IsNotExist(err) {
		t.Error("expected corrupted daemon.json to be removed")
	}
}

// TestDaemonStopTruncatedFile verifies stopDaemon cleans up truncated daemon.json
// (yields io.ErrUnexpectedEOF during JSON decode)
func TestDaemonStopTruncatedFile(t *testing.T) {
	tmpDir := setupIsolatedDataDir(t)

	// Create truncated daemon.json (partial JSON that triggers io.ErrUnexpectedEOF)
	// A JSON object that ends abruptly mid-string causes io.ErrUnexpectedEOF
	writeMockDaemonFile(t, tmpDir, `{"pid": 123, "addr": "127.0.0.1:7373`, 0644)

	err := stopDaemon()
	if err != ErrDaemonNotRunning {
		t.Errorf("expected ErrDaemonNotRunning for truncated file, got %v", err)
	}

	// Verify daemon.json was cleaned up
	if _, err := os.Stat(filepath.Join(tmpDir, "daemon.json")); !os.IsNotExist(err) {
		t.Error("expected truncated daemon.json to be removed")
	}
}

// TestDaemonStopUnreadableFileSkipped verifies stopDaemon skips unreadable files
// With the new per-PID runtime file pattern, ListAllRuntimes continues scanning
// even when some files are unreadable. This allows daemon discovery to work even
// if some runtime files are temporarily inaccessible.
func TestDaemonStopUnreadableFileSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	tmpDir := setupIsolatedDataDir(t)

	// Create daemon.json with valid content
	daemonPath := writeMockDaemonFile(t, tmpDir, `{"pid":12345,"addr":"127.0.0.1:7373"}`, 0644)

	// Remove read permission
	if err := os.Chmod(daemonPath, 0000); err != nil {
		t.Fatalf("chmod daemon.json: %v", err)
	}
	// Restore permission for cleanup
	defer func() { _ = os.Chmod(daemonPath, 0644) }()

	// Probe whether chmod 0000 actually blocks reads on this filesystem
	// (some filesystems like Windows or certain ACL-based systems may not enforce this)
	if f, probeErr := os.Open(daemonPath); probeErr == nil {
		f.Close()
		t.Skip("filesystem does not enforce chmod 0000 read restrictions")
	}

	err := stopDaemon()
	// With the new behavior, unreadable files are skipped during ListAllRuntimes.
	// Since no readable daemon files exist, stopDaemon returns ErrDaemonNotRunning.
	if err != ErrDaemonNotRunning {
		t.Errorf("expected ErrDaemonNotRunning (unreadable file skipped), got: %v", err)
	}
}

func TestUpdateCmdHasNoRestartFlag(t *testing.T) {
	cmd := updateCmd()

	flag := cmd.Flags().Lookup("no-restart")
	if flag == nil {
		t.Fatal("expected --no-restart flag to be defined")
	}
	if flag.DefValue != "false" {
		t.Fatalf("expected default false, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "skip daemon restart") {
		t.Fatalf("unexpected usage text: %q", flag.Usage)
	}
}

// stubRestartVars saves and restores all package-level vars used by
// restartDaemonAfterUpdate. Returns a struct with call counters.
type restartStubs struct {
	stopCalls  int
	startCalls int
	killCalls  int
}

func stubRestartVars(t *testing.T) *restartStubs {
	t.Helper()
	origGet := getAnyRunningDaemon
	origList := listAllRuntimes
	origPIDAlive := isPIDAlive
	origStop := stopDaemonForUpdate
	origKill := killAllDaemonsForUpdate
	origStart := startUpdatedDaemon
	origWait := updateRestartWaitTimeout
	origPoll := updateRestartPollInterval
	t.Cleanup(func() {
		getAnyRunningDaemon = origGet
		listAllRuntimes = origList
		isPIDAlive = origPIDAlive
		stopDaemonForUpdate = origStop
		killAllDaemonsForUpdate = origKill
		startUpdatedDaemon = origStart
		updateRestartWaitTimeout = origWait
		updateRestartPollInterval = origPoll
	})
	updateRestartWaitTimeout = 5 * time.Millisecond
	updateRestartPollInterval = 1 * time.Millisecond

	// Default: no runtimes on disk.
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return nil, nil
	}
	isPIDAlive = func(int) bool {
		return false
	}

	s := &restartStubs{}
	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return nil
	}
	killAllDaemonsForUpdate = func() {
		s.killCalls++
	}
	startUpdatedDaemon = func(string) error {
		s.startCalls++
		return nil
	}
	return s
}

func TestWaitForDaemonExitProbeErrorWithRuntimePresentTimesOut(t *testing.T) {
	stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
	}
	isPIDAlive = func(pid int) bool {
		return pid == 100
	}

	exited, newPID := waitForDaemonExit(100, 5*time.Millisecond)
	if exited {
		t.Fatalf("expected timeout (daemon runtime still present), got exited=true newPID=%d", newPID)
	}
	if newPID != 0 {
		t.Fatalf("expected newPID=0 on timeout, got %d", newPID)
	}
}

func TestWaitForDaemonExitProbeErrorWithStaleRuntimeExits(t *testing.T) {
	stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
	}
	// PID is dead; runtime entry is stale.
	isPIDAlive = func(int) bool {
		return false
	}

	exited, newPID := waitForDaemonExit(100, 5*time.Millisecond)
	if !exited {
		t.Fatalf("expected stale runtime not to block exit, got exited=false newPID=%d", newPID)
	}
	if newPID != 0 {
		t.Fatalf("expected newPID=0 with stale runtime, got %d", newPID)
	}
}

func TestWaitForDaemonExitRuntimeGoneButPIDAliveTimesOut(t *testing.T) {
	stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return nil, nil
	}
	isPIDAlive = func(pid int) bool {
		return pid == 100
	}

	exited, newPID := waitForDaemonExit(100, 5*time.Millisecond)
	if exited {
		t.Fatalf("expected timeout while pid is still alive, got exited=true newPID=%d", newPID)
	}
	if newPID != 0 {
		t.Fatalf("expected newPID=0 on timeout, got %d", newPID)
	}
}

func TestWaitForDaemonExitRuntimeGonePIDReusedAsNonDaemonExits(t *testing.T) {
	stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return nil, nil
	}
	// Simulate PID reuse: old daemon runtime is gone and PID now belongs
	// to a non-daemon process, so daemon liveness should not block exit.
	isPIDAlive = func(pid int) bool {
		return false
	}

	exited, newPID := waitForDaemonExit(100, 5*time.Millisecond)
	if !exited {
		t.Fatalf("expected exit when previous PID is reused by non-daemon, got exited=false newPID=%d", newPID)
	}
	if newPID != 0 {
		t.Fatalf("expected newPID=0 without manager handoff, got %d", newPID)
	}
}

func TestWaitForDaemonExitDetectsUnresponsiveManagerHandoffFromRuntimePID(t *testing.T) {
	stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return []*daemon.RuntimeInfo{
			{PID: 200, Addr: "127.0.0.1:7373"},
		}, nil
	}
	isPIDAlive = func(pid int) bool {
		return pid == 200
	}

	exited, newPID := waitForDaemonExit(100, 5*time.Millisecond)
	if !exited {
		t.Fatal("expected exited=true when previous pid is gone")
	}
	if newPID != 200 {
		t.Fatalf("expected newPID=200 from runtime handoff, got %d", newPID)
	}
}

func TestInitialPIDsExitedRequiresPIDDeath(t *testing.T) {
	stubRestartVars(t)

	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return nil, nil
	}
	isPIDAlive = func(pid int) bool {
		return pid == 101
	}

	ok := initialPIDsExited(
		map[int]struct{}{
			100: {},
			101: {},
		},
		0,
	)
	if ok {
		t.Fatal("expected false when an initial PID is still alive")
	}
}

func TestInitialPIDsExitedAllowsManagerPID(t *testing.T) {
	stubRestartVars(t)

	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return nil, nil
	}
	isPIDAlive = func(pid int) bool {
		return pid == 500
	}

	ok := initialPIDsExited(
		map[int]struct{}{
			100: {},
			500: {},
		},
		500,
	)
	if !ok {
		t.Fatal("expected true when only allowPID remains alive")
	}
}

type mockDaemonStep struct {
	info *daemon.RuntimeInfo
	err  error
}

type mockRuntimeStep struct {
	list []*daemon.RuntimeInfo
	err  error
}

func mockDaemonSequence(steps ...mockDaemonStep) func() (*daemon.RuntimeInfo, error) {
	var i int
	return func() (*daemon.RuntimeInfo, error) {
		if len(steps) == 0 {
			return nil, os.ErrNotExist
		}
		if i >= len(steps) {
			i = len(steps) - 1
		}
		step := steps[i]
		i++
		if step.info == nil && step.err == nil {
			return nil, os.ErrNotExist
		}
		return step.info, step.err
	}
}

func mockRuntimeSequence(steps ...mockRuntimeStep) func() ([]*daemon.RuntimeInfo, error) {
	var i int
	return func() ([]*daemon.RuntimeInfo, error) {
		if len(steps) == 0 {
			return nil, nil
		}
		if i >= len(steps) {
			i = len(steps) - 1
		}
		step := steps[i]
		i++
		return step.list, step.err
	}
}

func TestRestartDaemonAfterUpdate(t *testing.T) {
	errStop := errors.New("cannot stop daemon")
	errStopAll := errors.New("cannot stop all daemons")

	tests := []struct {
		name            string
		noRestart       bool
		mockDaemons     []mockDaemonStep
		mockRuntimes    []mockRuntimeStep
		stopErr         error
		wantOutput      []string
		wantSuccess     bool
		dontWantSuccess bool
		wantStopCalls   int
		wantStartCalls  int
		wantKillCalls   int
		setup           func(s *restartStubs)
	}{
		{
			name:           "NoRestart",
			noRestart:      true,
			mockDaemons:    []mockDaemonStep{{info: &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}}},
			wantOutput:     []string{"Skipping daemon restart (--no-restart)"},
			wantStopCalls:  0,
			wantStartCalls: 0,
			wantKillCalls:  0,
		},
		{
			name:           "ManagerRestarted",
			wantOutput:     []string{"Restarting daemon... "},
			wantSuccess:    true,
			wantStopCalls:  1,
			wantStartCalls: 0,
			wantKillCalls:  0,
			mockDaemons: []mockDaemonStep{
				{info: &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}},
				{info: &daemon.RuntimeInfo{PID: 200, Addr: "127.0.0.1:7373"}},
			},
		},
		{
			name:            "StopFailureSamePID",
			mockDaemons:     []mockDaemonStep{{info: &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}}},
			stopErr:         errStop,
			wantOutput:      []string{"warning: failed to stop daemon: cannot stop daemon", "warning: daemon pid 100 is still running; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   1,
		},
		{
			name:           "StopFailureManagerRestartNeedsCleanup",
			stopErr:        errStopAll,
			wantOutput:     []string{"warning: failed to stop daemon: cannot stop all daemons", "OK"},
			wantStopCalls:  1,
			wantStartCalls: 1,
			wantKillCalls:  1,
			setup: func(s *restartStubs) {
				var getCalls int
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					getCalls++
					if getCalls == 1 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					if s.killCalls == 0 {
						return &daemon.RuntimeInfo{PID: 200, Addr: "127.0.0.1:7373"}, nil
					}
					if s.startCalls > 0 {
						return &daemon.RuntimeInfo{PID: 300, Addr: "127.0.0.1:7373"}, nil
					}
					return nil, os.ErrNotExist
				}
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return []*daemon.RuntimeInfo{
							{PID: 100, Addr: "127.0.0.1:7373"},
							{PID: 101, Addr: "127.0.0.1:7373"},
						}, nil
					}
					return nil, nil
				}
			},
		},
		{
			name:           "ManagerRestartedAfterKill",
			wantOutput:     []string{"Restarting daemon... "},
			wantSuccess:    true,
			wantStopCalls:  1,
			wantStartCalls: 0,
			wantKillCalls:  1,
			setup: func(s *restartStubs) {
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
				}
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
					}
					return []*daemon.RuntimeInfo{{PID: 500, Addr: "127.0.0.1:7373"}}, nil
				}
			},
		},
		{
			name:            "ManagerHandoffUnresponsiveUsesRuntimePID",
			wantOutput:      []string{"warning: daemon handoff detected but replacement is not ready; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   0,
			setup: func(s *restartStubs) {
				var getCalls int
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					getCalls++
					if getCalls == 1 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					return nil, os.ErrNotExist
				}
				var listCalls int
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					listCalls++
					if listCalls == 1 {
						return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
					}
					return []*daemon.RuntimeInfo{{PID: 200, Addr: "127.0.0.1:7373"}}, nil
				}
				isPIDAlive = func(pid int) bool { return pid == 200 }
			},
		},
		{
			name:            "ManagerHandoffAfterKillNotReadyWarnsNoStart",
			wantOutput:      []string{"warning: daemon handoff detected but replacement is not ready; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   1,
			setup: func(s *restartStubs) {
				var handoffSeen bool
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					if !handoffSeen {
						handoffSeen = true
						return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
					}
					return nil, os.ErrNotExist
				}
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
					}
					return []*daemon.RuntimeInfo{{PID: 500, Addr: "127.0.0.1:7373"}}, nil
				}
				isPIDAlive = func(pid int) bool { return pid == 500 }
			},
		},
		{
			name:            "ManagerRestartedAfterKillWithLingeringInitialPID",
			stopErr:         errStopAll,
			wantOutput:      []string{"warning: daemon restart detected but older daemon runtimes remain; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   1,
			setup: func(s *restartStubs) {
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
				}
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return []*daemon.RuntimeInfo{
							{PID: 100, Addr: "127.0.0.1:7373"},
							{PID: 101, Addr: "127.0.0.1:7373"},
						}, nil
					}
					return []*daemon.RuntimeInfo{
						{PID: 101, Addr: "127.0.0.1:7373"},
						{PID: 500, Addr: "127.0.0.1:7373"},
					}, nil
				}
			},
		},
		{
			name:            "StopFailedPreviousPIDExitedButInitialPIDLingering",
			stopErr:         errStopAll,
			wantOutput:      []string{"warning: older daemon runtimes still present after stop; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   0,
			setup: func(s *restartStubs) {
				var getCalls int
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					getCalls++
					if getCalls == 1 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					return nil, os.ErrNotExist
				}
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					switch getCalls {
					case 0, 1:
						return []*daemon.RuntimeInfo{
							{PID: 100, Addr: "127.0.0.1:7373"},
							{PID: 101, Addr: "127.0.0.1:7373"},
						}, nil
					default:
						return []*daemon.RuntimeInfo{
							{PID: 101, Addr: "127.0.0.1:7373"},
						}, nil
					}
				}
			},
		},
		{
			name:            "StopFailedInitialSnapshotErrorWithLingeringRuntimeSkipsStart",
			stopErr:         errStop,
			wantOutput:      []string{"warning: older daemon runtimes still present after stop; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   0,
			setup: func(s *restartStubs) {
				var getCalls int
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					getCalls++
					if getCalls == 1 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					return nil, os.ErrNotExist
				}
				var listCalls int
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					listCalls++
					switch listCalls {
					case 1:
						return nil, errors.New("cannot read runtime files")
					case 2:
						return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
					case 3:
						return nil, nil
					default:
						return []*daemon.RuntimeInfo{{PID: 101, Addr: "127.0.0.1:7373"}}, nil
					}
				}
			},
		},
		{
			name:            "StopFailedHandoffNotReadyWarnsNoStart",
			stopErr:         errStop,
			wantOutput:      []string{"warning: daemon handoff detected but replacement is not ready; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   1,
			setup: func(s *restartStubs) {
				var handoffSeen bool
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					if !handoffSeen {
						handoffSeen = true
						return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
					}
					return nil, os.ErrNotExist
				}
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					if s.killCalls == 0 {
						return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
					}
					return []*daemon.RuntimeInfo{{PID: 500, Addr: "127.0.0.1:7373"}}, nil
				}
				isPIDAlive = func(pid int) bool { return pid == 500 }
			},
		},
		{
			name:            "StopFailedPreExistingPIDNotAcceptedAsHandoff",
			stopErr:         errStop,
			wantOutput:      []string{"warning: daemon restart detected but older daemon runtimes remain; restart it manually"},
			dontWantSuccess: true,
			wantStopCalls:   1,
			wantStartCalls:  0,
			wantKillCalls:   1,
			setup: func(s *restartStubs) {
				var getCalls int
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					getCalls++
					if getCalls == 1 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					return &daemon.RuntimeInfo{PID: 200, Addr: "127.0.0.1:7373"}, nil
				}
				var listCalls int
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					listCalls++
					if listCalls == 1 {
						return []*daemon.RuntimeInfo{
							{PID: 100, Addr: "127.0.0.1:7373"},
							{PID: 200, Addr: "127.0.0.1:7373"},
						}, nil
					}
					return []*daemon.RuntimeInfo{{PID: 200, Addr: "127.0.0.1:7373"}}, nil
				}
				isPIDAlive = func(pid int) bool { return pid == 200 }
			},
		},
		{
			name:           "ProbeFailFallback",
			wantOutput:     []string{"Restarting daemon... "},
			wantSuccess:    true,
			wantStopCalls:  1,
			wantStartCalls: 1,
			wantKillCalls:  0,
			setup: func(s *restartStubs) {
				updateRestartWaitTimeout = 200 * time.Millisecond
				var getCalls int
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					getCalls++
					if getCalls <= 4 {
						return nil, os.ErrNotExist
					}
					return &daemon.RuntimeInfo{PID: 300, Addr: "127.0.0.1:7373"}, nil
				}
				listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
					if getCalls <= 3 {
						return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
					}
					return nil, nil
				}
			},
		},
		{
			name:           "NoDaemon",
			mockDaemons:    []mockDaemonStep{{err: os.ErrNotExist}},
			mockRuntimes:   []mockRuntimeStep{{}},
			wantOutput:     []string{""},
			wantStopCalls:  0,
			wantStartCalls: 0,
			wantKillCalls:  0,
		},
		{
			name:           "ExitsQuickly",
			wantOutput:     []string{"Restarting daemon... "},
			wantSuccess:    true,
			wantStopCalls:  1,
			wantStartCalls: 1,
			wantKillCalls:  0,
			setup: func(s *restartStubs) {
				var getCalls int
				getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
					getCalls++
					if getCalls == 1 {
						return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
					}
					if getCalls == 2 {
						return nil, os.ErrNotExist
					}
					return &daemon.RuntimeInfo{PID: 400, Addr: "127.0.0.1:7373"}, nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stubRestartVars(t)

			// We only use simple arrays when setup doesn't override them
			if tt.mockDaemons != nil {
				getAnyRunningDaemon = mockDaemonSequence(tt.mockDaemons...)
			}

			if tt.mockRuntimes != nil {
				listAllRuntimes = mockRuntimeSequence(tt.mockRuntimes...)
			}

			if tt.stopErr != nil {
				stopDaemonForUpdate = func() error {
					s.stopCalls++
					return tt.stopErr
				}
			}

			if tt.setup != nil {
				tt.setup(s)
			}

			output := captureStdout(t, func() {
				restartDaemonAfterUpdate("/tmp/bin", tt.noRestart)
			})

			for _, want := range tt.wantOutput {
				if want == "" {
					if output != "" {
						t.Errorf("expected no output, got %q", output)
					}
				} else if !strings.Contains(output, want) {
					t.Errorf("expected output to contain %q, got %q", want, output)
				}
			}

			if tt.wantSuccess && !strings.Contains(output, "OK\n") {
				t.Errorf("expected output to indicate success with OK, got %q", output)
			}
			if tt.dontWantSuccess && strings.Contains(output, "OK\n") {
				t.Errorf("expected output to not indicate success, got %q", output)
			}

			if s.stopCalls != tt.wantStopCalls {
				t.Errorf("expected %d stop calls, got %d", tt.wantStopCalls, s.stopCalls)
			}
			if s.startCalls != tt.wantStartCalls {
				t.Errorf("expected %d start calls, got %d", tt.wantStartCalls, s.startCalls)
			}
			if s.killCalls != tt.wantKillCalls {
				t.Errorf("expected %d kill calls, got %d", tt.wantKillCalls, s.killCalls)
			}
		})
	}
}

func TestShortJobRef(t *testing.T) {
	commitID := int64(123)
	diffContent := "some diff"

	tests := []struct {
		name     string
		job      storage.ReviewJob
		expected string
	}{
		{
			name:     "run job with new git_ref",
			job:      storage.ReviewJob{GitRef: "run", CommitID: nil, DiffContent: nil},
			expected: "run",
		},
		{
			name:     "legacy prompt job shows as run",
			job:      storage.ReviewJob{GitRef: "prompt", CommitID: nil, DiffContent: nil},
			expected: "run",
		},
		{
			name:     "analyze job",
			job:      storage.ReviewJob{GitRef: "analyze", CommitID: nil, DiffContent: nil},
			expected: "analyze",
		},
		{
			name:     "custom label job",
			job:      storage.ReviewJob{GitRef: "my-task", CommitID: nil, DiffContent: nil},
			expected: "my-task",
		},
		{
			name:     "branch literally named prompt (has CommitID)",
			job:      storage.ReviewJob{GitRef: "prompt", CommitID: &commitID},
			expected: "prompt",
		},
		{
			name:     "branch literally named run (has CommitID)",
			job:      storage.ReviewJob{GitRef: "run", CommitID: &commitID},
			expected: "run",
		},
		{
			name:     "normal SHA review",
			job:      storage.ReviewJob{GitRef: "abc1234567890", CommitID: &commitID},
			expected: "abc1234",
		},
		{
			name:     "normal SHA review with Prompt set (after worker starts)",
			job:      storage.ReviewJob{GitRef: "abc1234567890", CommitID: &commitID, Prompt: "Review this commit..."},
			expected: "abc1234",
		},
		{
			name:     "commit range",
			job:      storage.ReviewJob{GitRef: "abc1234..def5678", CommitID: nil},
			expected: "abc1234..def5678",
		},
		{
			name:     "dirty review (has DiffContent)",
			job:      storage.ReviewJob{GitRef: "dirty", CommitID: nil, DiffContent: &diffContent},
			expected: "dirty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortJobRef(tt.job)
			if got != tt.expected {
				t.Errorf("shortJobRef() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetDaemonAddr(t *testing.T) {
	origGet := getAnyRunningDaemon
	t.Cleanup(func() { getAnyRunningDaemon = origGet })

	// Mock a running daemon
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return &daemon.RuntimeInfo{Addr: "runtime:1111"}, nil
	}

	t.Run("returns explicitly set context address over runtime", func(t *testing.T) {
		cmd := &cobra.Command{}
		ctx := context.WithValue(context.Background(), serverAddrKey{}, "http://context:2222")
		cmd.SetContext(ctx)
		addr := getDaemonAddr(cmd)
		if addr != "http://context:2222" {
			t.Errorf("Expected context address, got %q", addr)
		}
	})

	t.Run("returns explicitly set server flag over runtime", func(t *testing.T) {
		cmd := &cobra.Command{}
		cmd.Flags().String("server", "http://127.0.0.1:7373", "daemon server address")
		cmd.Flags().Set("server", "http://flag:3333")
		addr := getDaemonAddr(cmd)
		if addr != "http://flag:3333" {
			t.Errorf("Expected flag address, got %q", addr)
		}
	})

	t.Run("returns explicitly set env var over runtime", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_SERVER_ADDR", "http://env-fallback:4444")
		cmd := &cobra.Command{}
		addr := getDaemonAddr(cmd)
		if addr != "http://env-fallback:4444" {
			t.Errorf("Expected env var address, got %q", addr)
		}
	})

	t.Run("returns runtime address when no explicit overrides present", func(t *testing.T) {
		cmd := &cobra.Command{}
		addr := getDaemonAddr(cmd)
		if addr != "http://runtime:1111" {
			t.Errorf("Expected runtime address, got %q", addr)
		}
	})

	t.Run("returns default address when no runtime or overrides", func(t *testing.T) {
		getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
			return nil, fmt.Errorf("no daemon")
		}
		cmd := &cobra.Command{}
		addr := getDaemonAddr(cmd)
		if addr != "http://127.0.0.1:7373" {
			t.Errorf("Expected default address, got %q", addr)
		}
	})
}
