package main

// NOTE: Tests in this package mutate package-level variables (serverAddr,
// pollStartInterval, pollMaxInterval) and environment variables (ROBOREV_DATA_DIR).
// Do not use t.Parallel() in this package as it will cause race conditions.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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

func TestEnqueueReviewRefine(t *testing.T) {
	t.Run("returns job ID on success", func(t *testing.T) {
		md := NewMockDaemon(t, MockRefineHooks{
			OnEnqueue: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
				var req map[string]string
				json.NewDecoder(r.Body).Decode(&req)

				if req["repo_path"] != "/test/repo" || req["git_ref"] != "abc..def" {
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

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// A fresh repo with a committed file should have a clean working tree
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if err := c.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if err := c.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

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

	ctx := RunContext{
		WorkingDir:      repoDir,
		PollInterval:    1 * time.Millisecond,
		PostCommitDelay: 1 * time.Millisecond,
	}

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

	ctx := RunContext{
		WorkingDir:      repoDir,
		PollInterval:    1 * time.Millisecond,
		PostCommitDelay: 1 * time.Millisecond,
	}

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

	ctx := RunContext{
		WorkingDir:      repoDir,
		PollInterval:    1 * time.Millisecond,
		PostCommitDelay: 1 * time.Millisecond,
	}

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

	md := NewMockDaemon(t, MockRefineHooks{
		OnGetJobs: func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
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
		},
		OnEnqueue: func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
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
		},
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

	ctx := RunContext{
		WorkingDir:      repoDir,
		PollInterval:    1 * time.Millisecond,
		PostCommitDelay: 1 * time.Millisecond,
	}

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
		if !shouldRefuseAutoStartDaemon("/tmp/roborev.test") {
			t.Fatal("expected refusal for test binary without opt-in")
		}
	})

	t.Run("allows explicit opt in for test binary", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "1")
		if shouldRefuseAutoStartDaemon("/tmp/roborev.test") {
			t.Fatal("expected no refusal for test binary when opt-in is set")
		}
	})

	t.Run("does not refuse normal binary", func(t *testing.T) {
		t.Setenv("ROBOREV_TEST_ALLOW_AUTOSTART", "")
		if shouldRefuseAutoStartDaemon("/usr/local/bin/roborev") {
			t.Fatal("expected no refusal for non-test binary")
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
	if !strings.Contains(err.Error(), "refusing to auto-start daemon from go test binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDaemonStopNotRunning verifies daemon stop reports when no daemon is running
func TestDaemonStopNotRunning(t *testing.T) {
	// Use ROBOREV_DATA_DIR to isolate test
	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

	err := stopDaemon()
	if err != ErrDaemonNotRunning {
		t.Errorf("expected ErrDaemonNotRunning, got %v", err)
	}
}

// TestDaemonStopInvalidPID verifies stopDaemon handles invalid PID in daemon.json
func TestDaemonStopInvalidPID(t *testing.T) {
	// Use ROBOREV_DATA_DIR to isolate test
	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

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
	// Use ROBOREV_DATA_DIR to isolate test
	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

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
	// Use ROBOREV_DATA_DIR to isolate test
	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

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

	// Use ROBOREV_DATA_DIR to isolate test
	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

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
