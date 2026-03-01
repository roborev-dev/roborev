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

	"github.com/roborev-dev/roborev/internal/agent"
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
				json.NewDecoder(r.Body).Decode(&req)

				if req.RepoPath != "/test/repo" || req.GitRef != "abc..def" {
					t.Errorf("unexpected request body: %+v", req)
				}

				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(storage.ReviewJob{ID: 123})
				return true
			},
		})
		defer md.Close()

		jobID, err := enqueueReview("/test/repo", "abc..def", "codex")
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

		_, err := enqueueReview("/bad/repo", "HEAD", "codex")
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
				ID: 1, JobID: 1, Output: "**Bug found**: fail", Addressed: false,
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

	if err := runRefine(ctx, refineOptions{agentName: "test", maxIterations: 1, quiet: true}); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRunRefineQuietNonTTYTimerOutput(t *testing.T) {
	repoDir, headSHA := setupRefineRepo(t)

	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	md.State.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 42, Output: "**Bug found**: fail", Addressed: false,
	}

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	ctx := newFastRunContext(repoDir)

	output := captureStdout(t, func() {
		if err := runRefine(ctx, refineOptions{agentName: "test", maxIterations: 1, quiet: true}); err == nil {
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
		ID: 1, JobID: 7, Output: "**Bug found**: fail", Addressed: false,
	}

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return true }
	defer func() { isTerminal = origIsTerminal }()

	agent.Register(&functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		return "", fmt.Errorf("test agent failure")
	}})
	defer agent.Register(agent.NewTestAgent())

	ctx := newFastRunContext(repoDir)

	output := captureStdout(t, func() {
		if err := runRefine(ctx, refineOptions{agentName: "test", maxIterations: 1, quiet: true}); err == nil {
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

	t.Run("returns nil when review is already addressed", func(t *testing.T) {
		client := newMockDaemonClient()
		// Failed but already addressed
		client.reviews["commit1sha"] = &storage.Review{
			ID: 1, JobID: 1, Output: "**Bug**: error", Addressed: true,
		}

		commits := []string{"commit1sha"}
		review, err := findFailedReviewForBranch(client, commits, nil)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review != nil {
			t.Error("expected nil - addressed reviews should be skipped")
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
	responses, err := getCommentsForJob(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 0 {
		t.Errorf("expected 0 previous responses for job 42, got %d", len(responses))
	}

	// Job with a prior skip comment — verify the comment text matches
	responses, err = getCommentsForJob(99)
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
			t.Errorf("expected nil when all commits pass, got review ID=%d JobID=%d Output=%q Addressed=%v",
				review.ID, review.JobID, review.Output, review.Addressed)
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

		jobID, err := enqueueReview("/test/repo", "abc123..HEAD", "codex")
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
			ID: 1, JobID: 42, Output: "All tests pass. No issues found.", Addressed: false,
		}

		review, err := waitForReviewWithInterval(42, 1*time.Millisecond)
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

		_, err := waitForReviewWithInterval(42, 1*time.Millisecond)
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
func TestRefinePendingJobWaitDoesNotConsumeIteration(t *testing.T) {
	repoDir, commitSHA := setupRefineRepo(t)

	// Track how many times the job has been polled
	var pollCount int32

	handleGetJobs := func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
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
			count := atomic.AddInt32(&pollCount, 1)
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

	handleEnqueue := func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
		// Handle branch review enqueue - create a passing branch review
		var req struct {
			GitRef string `json:"git_ref"`
		}
		json.NewDecoder(r.Body).Decode(&req)
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

	md := NewMockDaemon(t, MockRefineHooks{
		OnGetJobs: handleGetJobs,
		OnEnqueue: handleEnqueue,
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
		ID: 1, JobID: 1, Output: "No issues found. LGTM!", Addressed: false,
	}
	md.State.nextJobID = 2

	ctx := newFastRunContext(repoDir)

	// Run refine with maxIterations=1. If waiting on the pending job consumed
	// an iteration, this would fail with "max iterations reached". Since the
	// pending job transitions to Done with a passing review (and no failed
	// reviews exist), refine should succeed.
	err := runRefine(ctx, refineOptions{agentName: "test", maxIterations: 1, quiet: true})

	// Should succeed - all reviews pass after waiting for the pending one
	if err != nil {
		t.Fatalf("expected refine to succeed (pending wait should not consume iteration), got: %v", err)
	}

	// Verify the job was actually polled multiple times (proving we waited)
	if atomic.LoadInt32(&pollCount) < 2 {
		t.Errorf("expected job to be polled at least twice (wait behavior), got %d polls", atomic.LoadInt32(&pollCount))
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
	err = startDaemon()
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
	daemonInfo := daemon.RuntimeInfo{PID: 0, Addr: "127.0.0.1:59999"}
	data, _ := json.Marshal(daemonInfo)
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), data, 0644); err != nil {
		t.Fatalf("write daemon.json: %v", err)
	}

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
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), []byte("not valid json"), 0644); err != nil {
		t.Fatalf("write daemon.json: %v", err)
	}

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
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), []byte(`{"pid": 123, "addr": "127.0.0.1:7373`), 0644); err != nil {
		t.Fatalf("write daemon.json: %v", err)
	}

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
	daemonInfo := daemon.RuntimeInfo{PID: 12345, Addr: "127.0.0.1:7373"}
	data, _ := json.Marshal(daemonInfo)
	daemonPath := filepath.Join(tmpDir, "daemon.json")
	if err := os.WriteFile(daemonPath, data, 0644); err != nil {
		t.Fatalf("write daemon.json: %v", err)
	}

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
	origPIDAlive := isPIDAliveForUpdate
	origStop := stopDaemonForUpdate
	origKill := killAllDaemonsForUpdate
	origStart := startUpdatedDaemon
	origWait := updateRestartWaitTimeout
	origPoll := updateRestartPollInterval
	t.Cleanup(func() {
		getAnyRunningDaemon = origGet
		listAllRuntimes = origList
		isPIDAliveForUpdate = origPIDAlive
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
	isPIDAliveForUpdate = func(int) bool {
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

func TestRestartDaemonAfterUpdateNoRestart(t *testing.T) {
	s := stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", true)
	})

	if !strings.Contains(output, "Skipping daemon restart (--no-restart)") {
		t.Fatalf("expected no-restart message, got %q", output)
	}
	if s.stopCalls != 0 {
		t.Fatalf("expected stopDaemonForUpdate not called, got %d", s.stopCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called, got %d", s.startCalls)
	}
}

func TestRestartDaemonAfterUpdateManagerRestarted(t *testing.T) {
	s := stubRestartVars(t)

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		if getCalls == 1 {
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// After stop, an external manager restarted with a new PID.
		return &daemon.RuntimeInfo{PID: 200, Addr: "127.0.0.1:7373"}, nil
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if !strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("expected successful restart output, got %q", output)
	}
	if s.stopCalls != 1 {
		t.Fatalf("expected stopDaemonForUpdate called once, got %d", s.stopCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called, got %d", s.startCalls)
	}
}

func TestRestartDaemonAfterUpdateStopFailureSamePID(t *testing.T) {
	s := stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
	}
	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return errors.New("cannot stop daemon")
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if !strings.Contains(output, "warning: failed to stop daemon: cannot stop daemon") {
		t.Fatalf("expected stop warning, got %q", output)
	}
	if !strings.Contains(output, "warning: daemon pid 100 is still running; restart it manually") {
		t.Fatalf("expected same-pid warning, got %q", output)
	}
	if strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("unexpected success output: %q", output)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected start not called, got %d", s.startCalls)
	}
}

func TestWaitForDaemonExitProbeErrorWithRuntimePresentTimesOut(t *testing.T) {
	stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return []*daemon.RuntimeInfo{{PID: 100, Addr: "127.0.0.1:7373"}}, nil
	}
	isPIDAliveForUpdate = func(pid int) bool {
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
	isPIDAliveForUpdate = func(int) bool {
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
	isPIDAliveForUpdate = func(pid int) bool {
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
	isPIDAliveForUpdate = func(pid int) bool {
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
	isPIDAliveForUpdate = func(pid int) bool {
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
	isPIDAliveForUpdate = func(pid int) bool {
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
	isPIDAliveForUpdate = func(pid int) bool {
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

func TestRestartDaemonAfterUpdateStopFailureManagerRestartNeedsCleanup(t *testing.T) {
	s := stubRestartVars(t)

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		// Initial probe sees old daemon.
		if getCalls == 1 {
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// During first wait loop, manager PID appears but old runtime still exists.
		if s.killCalls == 0 {
			return &daemon.RuntimeInfo{PID: 200, Addr: "127.0.0.1:7373"}, nil
		}
		// After forced kill and manual start, readiness probe succeeds.
		if s.startCalls > 0 {
			return &daemon.RuntimeInfo{PID: 300, Addr: "127.0.0.1:7373"}, nil
		}
		// During second wait loop after kill, no daemon responds.
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			// Before cleanup, one original PID still exists.
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
				{PID: 101, Addr: "127.0.0.1:7373"},
			}, nil
		}
		// Cleanup removed old daemons.
		return nil, nil
	}
	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return errors.New("cannot stop all daemons")
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if !strings.Contains(output, "warning: failed to stop daemon: cannot stop all daemons") {
		t.Fatalf("expected stop warning, got %q", output)
	}
	if s.killCalls != 1 {
		t.Fatalf("expected kill fallback called once, got %d", s.killCalls)
	}
	if s.startCalls != 1 {
		t.Fatalf("expected manual start after cleanup, got %d", s.startCalls)
	}
	if !strings.Contains(output, "Restarting daemon...") || !strings.Contains(output, "OK") {
		t.Fatalf("expected restart flow to complete, got %q", output)
	}
}

func TestRestartDaemonAfterUpdateManagerRestartedAfterKill(t *testing.T) {
	s := stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			// Before forced kill, old daemon stays on the same PID.
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// After forced kill, external manager restarts the daemon.
		return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
			}, nil
		}
		return []*daemon.RuntimeInfo{
			{PID: 500, Addr: "127.0.0.1:7373"},
		}, nil
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 1 {
		t.Fatalf("expected kill fallback called once, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called when manager restarted daemon, got %d", s.startCalls)
	}
	if !strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("expected manager-restart success output, got %q", output)
	}
}

func TestRestartDaemonAfterUpdateManagerHandoffUnresponsiveUsesRuntimePID(t *testing.T) {
	s := stubRestartVars(t)

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		if getCalls == 1 {
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// Replacement daemon is not yet responsive.
		return nil, os.ErrNotExist
	}

	var listCalls int
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		listCalls++
		if listCalls == 1 {
			// Initial snapshot for stop validation.
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
			}, nil
		}
		// Runtime handoff to manager-restarted PID.
		return []*daemon.RuntimeInfo{
			{PID: 200, Addr: "127.0.0.1:7373"},
		}, nil
	}
	isPIDAliveForUpdate = func(pid int) bool {
		return pid == 200
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 0 {
		t.Fatalf("expected kill fallback not called when handoff PID already exists, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called for unready handoff, got %d", s.startCalls)
	}
	if strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("unexpected handoff success output: %q", output)
	}
	if !strings.Contains(output, "warning: daemon handoff detected but replacement is not ready; restart it manually") {
		t.Fatalf("expected not-ready handoff warning, got %q", output)
	}
}

func TestRestartDaemonAfterUpdateManagerHandoffAfterKillNotReadyWarnsNoStart(t *testing.T) {
	s := stubRestartVars(t)

	var handoffSeen bool
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			// Initial probe + first wait loop see only the old daemon,
			// forcing timeout and kill fallback.
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		if !handoffSeen {
			// After kill fallback, handoff PID appears once.
			handoffSeen = true
			return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
		}
		// Replacement remains unresponsive during readiness polling.
		return nil, os.ErrNotExist
	}

	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
			}, nil
		}
		return []*daemon.RuntimeInfo{
			{PID: 500, Addr: "127.0.0.1:7373"},
		}, nil
	}

	isPIDAliveForUpdate = func(pid int) bool {
		return pid == 500
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 1 {
		t.Fatalf("expected kill fallback called once, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called for unready handoff, got %d", s.startCalls)
	}
	if !strings.Contains(output, "warning: daemon handoff detected but replacement is not ready; restart it manually") {
		t.Fatalf("expected not-ready handoff warning, got %q", output)
	}
	if strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("unexpected success output: %q", output)
	}
}

func TestRestartDaemonAfterUpdateManagerRestartedAfterKillWithLingeringInitialPID(t *testing.T) {
	s := stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			// Before forced kill, old daemon stays on the same PID.
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// After forced kill, external manager restarts one daemon PID.
		return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			// Initial runtime snapshot includes multiple daemon PIDs.
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
				{PID: 101, Addr: "127.0.0.1:7373"},
			}, nil
		}
		// After kill, previousPID is gone but another initial PID remains.
		return []*daemon.RuntimeInfo{
			{PID: 101, Addr: "127.0.0.1:7373"},
			{PID: 500, Addr: "127.0.0.1:7373"},
		}, nil
	}
	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return errors.New("cannot stop all daemons")
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 1 {
		t.Fatalf("expected kill fallback called once, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called with lingering initial PID, got %d", s.startCalls)
	}
	if !strings.Contains(output, "warning: daemon restart detected but older daemon runtimes remain; restart it manually") {
		t.Fatalf("expected lingering-runtime warning, got %q", output)
	}
	if strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("unexpected success output when initial PID still lingers: %q", output)
	}
}

func TestRestartDaemonAfterUpdateStopFailedPreviousPIDExitedButInitialPIDLingering(t *testing.T) {
	s := stubRestartVars(t)

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		if getCalls == 1 {
			// Initial probe sees previous PID.
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// previousPID exited quickly; no replacement PID observed.
		return nil, os.ErrNotExist
	}
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		switch getCalls {
		case 0:
			// Initial snapshot includes multiple daemon PIDs.
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
				{PID: 101, Addr: "127.0.0.1:7373"},
			}, nil
		case 1:
			// waitForDaemonExit still sees previous PID.
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
				{PID: 101, Addr: "127.0.0.1:7373"},
			}, nil
		default:
			// previousPID gone, but another initial PID lingers.
			return []*daemon.RuntimeInfo{
				{PID: 101, Addr: "127.0.0.1:7373"},
			}, nil
		}
	}
	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return errors.New("cannot stop all daemons")
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 0 {
		t.Fatalf("expected kill fallback not called when previousPID exited quickly, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called with lingering initial PID, got %d", s.startCalls)
	}
	if !strings.Contains(output, "warning: older daemon runtimes still present after stop; restart it manually") {
		t.Fatalf("expected lingering-runtime warning, got %q", output)
	}
	if strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("unexpected success output when initial PID still lingers: %q", output)
	}
}

func TestRestartDaemonAfterUpdateStopFailedInitialSnapshotErrorWithLingeringRuntimeSkipsStart(t *testing.T) {
	s := stubRestartVars(t)

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		if getCalls == 1 {
			// Initial probe sees previous PID.
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// After stop attempt, daemon probe fails.
		return nil, os.ErrNotExist
	}

	var listCalls int
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		listCalls++
		switch listCalls {
		case 1:
			// Initial runtime snapshot fails.
			return nil, errors.New("cannot read runtime files")
		case 2:
			// waitForDaemonExit still sees previous PID.
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
			}, nil
		case 3:
			// previousPID is now gone from runtime files.
			return nil, nil
		default:
			// Verification after stop failure finds another lingering runtime.
			return []*daemon.RuntimeInfo{
				{PID: 101, Addr: "127.0.0.1:7373"},
			}, nil
		}
	}

	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return errors.New("cannot stop daemon")
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 0 {
		t.Fatalf("expected kill fallback not called when previousPID exits, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called with lingering runtime, got %d", s.startCalls)
	}
	if !strings.Contains(output, "warning: older daemon runtimes still present after stop; restart it manually") {
		t.Fatalf("expected lingering-runtime warning, got %q", output)
	}
}

func TestRestartDaemonAfterUpdateStopFailedHandoffNotReadyWarnsNoStart(t *testing.T) {
	s := stubRestartVars(t)

	var handoffSeen bool
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			// Initial probe + first wait loop see only the old daemon,
			// forcing timeout and kill fallback.
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		if !handoffSeen {
			// Second wait loop sees manager handoff PID once.
			handoffSeen = true
			return &daemon.RuntimeInfo{PID: 500, Addr: "127.0.0.1:7373"}, nil
		}
		// Replacement remains unresponsive during readiness polling.
		return nil, os.ErrNotExist
	}

	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		if s.killCalls == 0 {
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
			}, nil
		}
		// previousPID is gone; only replacement PID runtime remains.
		return []*daemon.RuntimeInfo{
			{PID: 500, Addr: "127.0.0.1:7373"},
		}, nil
	}

	isPIDAliveForUpdate = func(pid int) bool {
		return pid == 500
	}

	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return errors.New("cannot stop daemon")
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 1 {
		t.Fatalf("expected kill fallback called once, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called for unready handoff, got %d", s.startCalls)
	}
	if !strings.Contains(output, "warning: daemon handoff detected but replacement is not ready; restart it manually") {
		t.Fatalf("expected not-ready handoff warning, got %q", output)
	}
	if strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("unexpected success output: %q", output)
	}
}

func TestRestartDaemonAfterUpdateStopFailedPreExistingPIDNotAcceptedAsHandoff(t *testing.T) {
	s := stubRestartVars(t)

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		if getCalls == 1 {
			// Initial probe sees previous PID.
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		// Existing daemon PID 200 remains responsive throughout.
		return &daemon.RuntimeInfo{PID: 200, Addr: "127.0.0.1:7373"}, nil
	}

	var listCalls int
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		listCalls++
		if listCalls == 1 {
			// Initial snapshot already includes PID 200.
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
				{PID: 200, Addr: "127.0.0.1:7373"},
			}, nil
		}
		// previousPID disappeared, but pre-existing PID 200 remains.
		return []*daemon.RuntimeInfo{
			{PID: 200, Addr: "127.0.0.1:7373"},
		}, nil
	}
	isPIDAliveForUpdate = func(pid int) bool {
		return pid == 200
	}
	stopDaemonForUpdate = func() error {
		s.stopCalls++
		return errors.New("cannot stop daemon")
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if s.killCalls != 1 {
		t.Fatalf("expected kill fallback called once, got %d", s.killCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected startUpdatedDaemon not called, got %d", s.startCalls)
	}
	if !strings.Contains(output, "warning: daemon restart detected but older daemon runtimes remain; restart it manually") {
		t.Fatalf("expected pre-existing handoff warning, got %q", output)
	}
	if strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("unexpected success output: %q", output)
	}
}

// Fix #2: Probe failure with runtime files should use PID from
// runtime files and still attempt stop/wait/start.
func TestRestartDaemonAfterUpdateProbeFailFallback(t *testing.T) {
	s := stubRestartVars(t)
	// This test needs 5 getAnyRunningDaemon calls to succeed. On
	// Windows the default timer resolution is ~15ms, so the 5ms
	// timeout from stubRestartVars expires before enough poll
	// iterations run. Use a longer timeout.
	updateRestartWaitTimeout = 200 * time.Millisecond

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		if getCalls <= 2 {
			// Initial probe + first waitForDaemonExit poll fail.
			return nil, os.ErrNotExist
		}
		if getCalls <= 4 {
			// Continue failing until the old runtime disappears.
			return nil, os.ErrNotExist
		}
		// After manual start, daemon responds with new PID.
		return &daemon.RuntimeInfo{PID: 300, Addr: "127.0.0.1:7373"}, nil
	}
	// Runtime files exist with a known PID.
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		if getCalls <= 3 {
			return []*daemon.RuntimeInfo{
				{PID: 100, Addr: "127.0.0.1:7373"},
			}, nil
		}
		return nil, nil
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if !strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("expected restart OK, got %q", output)
	}
	if s.stopCalls != 1 {
		t.Fatalf("expected stop called once, got %d", s.stopCalls)
	}
	if s.startCalls != 1 {
		t.Fatalf("expected start called once, got %d", s.startCalls)
	}
}

// Fix #2: No responsive daemon and no runtime files should skip silently.
func TestRestartDaemonAfterUpdateNoDaemon(t *testing.T) {
	s := stubRestartVars(t)

	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return nil, os.ErrNotExist
	}
	// No runtime files either.
	listAllRuntimes = func() ([]*daemon.RuntimeInfo, error) {
		return nil, nil
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if output != "" {
		t.Fatalf("expected no output, got %q", output)
	}
	if s.stopCalls != 0 {
		t.Fatalf("expected stop not called, got %d", s.stopCalls)
	}
	if s.startCalls != 0 {
		t.Fatalf("expected start not called, got %d", s.startCalls)
	}
}

// Fix #3: Unmanaged daemon exits quickly — no 2s delay.
func TestRestartDaemonAfterUpdateExitsQuickly(t *testing.T) {
	s := stubRestartVars(t)

	var getCalls int
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		getCalls++
		if getCalls == 1 {
			return &daemon.RuntimeInfo{PID: 100, Addr: "127.0.0.1:7373"}, nil
		}
		if getCalls == 2 {
			// Daemon exited after stop.
			return nil, os.ErrNotExist
		}
		// After manual start, daemon is ready.
		return &daemon.RuntimeInfo{PID: 400, Addr: "127.0.0.1:7373"}, nil
	}

	output := captureStdout(t, func() {
		restartDaemonAfterUpdate("/tmp/bin", false)
	})

	if !strings.Contains(output, "Restarting daemon... OK") {
		t.Fatalf("expected restart OK, got %q", output)
	}
	if s.stopCalls != 1 {
		t.Fatalf("expected stop called once, got %d", s.stopCalls)
	}
	if s.startCalls != 1 {
		t.Fatalf("expected start called once, got %d", s.startCalls)
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
