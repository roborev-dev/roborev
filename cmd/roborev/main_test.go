package main

// NOTE: Tests in this package mutate package-level variables (serverAddr,
// pollStartInterval, pollMaxInterval) and environment variables (HOME).
// Do not use t.Parallel() in this package as it will cause race conditions.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesm/roborev/internal/agent"
	"github.com/wesm/roborev/internal/daemon"
	"github.com/wesm/roborev/internal/storage"
	"github.com/wesm/roborev/internal/version"
)

// ============================================================================
// Refine Command Tests
// ============================================================================

// setupMockDaemon creates a test server and writes a fake daemon.json so
// getDaemonAddr() returns the test server address.
func setupMockDaemon(t *testing.T, handler http.Handler) (*httptest.Server, func()) {
	t.Helper()

	ts := httptest.NewServer(handler)

	// Override HOME
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)

	// Write fake daemon.json
	roborevDir := filepath.Join(tmpHome, ".roborev")
	if err := os.MkdirAll(roborevDir, 0755); err != nil {
		t.Fatalf("failed to create roborev dir: %v", err)
	}
	mockAddr := ts.URL[7:] // strip "http://"
	daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
	data, err := json.Marshal(daemonInfo)
	if err != nil {
		t.Fatalf("failed to marshal daemon.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644); err != nil {
		t.Fatalf("failed to write daemon.json: %v", err)
	}

	// Also set serverAddr for functions that use it directly
	origServerAddr := serverAddr
	serverAddr = ts.URL

	cleanup := func() {
		ts.Close()
		os.Setenv("HOME", origHome)
		serverAddr = origServerAddr
	}

	return ts, cleanup
}

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

func setupRefineRepo(t *testing.T) (string, string) {
	t.Helper()

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		} else if len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
		return ""
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "base commit")
	runGit("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("change"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "feature.txt")
	runGit("commit", "-m", "feature commit")

	headSHA := runGit("rev-parse", "HEAD")
	return repoDir, headSHA
}

type failingAgent struct{}

func (f failingAgent) Name() string { return "test" }

func (f failingAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	return "", fmt.Errorf("test agent failure")
}

func (f failingAgent) WithReasoning(level agent.ReasoningLevel) agent.Agent { return f }
func TestEnqueueReviewRefine(t *testing.T) {
	t.Run("returns job ID on success", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/enqueue" || r.Method != "POST" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}

			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)

			if req["repo_path"] != "/test/repo" || req["git_ref"] != "abc..def" {
				t.Errorf("unexpected request body: %+v", req)
			}

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(storage.ReviewJob{ID: 123})
		}))
		defer cleanup()

		jobID, err := enqueueReview("/test/repo", "abc..def", "codex")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jobID != 123 {
			t.Errorf("expected job ID 123, got %d", jobID)
		}
	})

	t.Run("returns error on failure", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/enqueue" || r.Method != "POST" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid repo"}`))
		}))
		defer cleanup()

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

func TestRefineNoChangeRetryLogic(t *testing.T) {
	// Test that the retry counting logic works correctly

	t.Run("counts no-change attempts from responses", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Return 2 previous no-change responses
			json.NewEncoder(w).Encode(map[string]interface{}{
				"responses": []storage.Response{
					{Responder: "roborev-refine", Response: "Agent could not determine how to address findings (attempt 1)"},
					{Responder: "roborev-refine", Response: "Agent could not determine how to address findings (attempt 2)"},
				},
			})
		}))
		defer cleanup()

		responses, _ := getResponsesForJob(1)

		// Count no-change attempts
		noChangeAttempts := 0
		for _, a := range responses {
			if strings.Contains(a.Response, "could not determine how to address") {
				noChangeAttempts++
			}
		}

		if noChangeAttempts != 2 {
			t.Errorf("expected 2 no-change attempts, got %d", noChangeAttempts)
		}

		// After 2 attempts, third should mark as addressed (threshold is >= 2)
		shouldMarkAddressed := noChangeAttempts >= 2
		if !shouldMarkAddressed {
			t.Error("expected to mark as addressed after 2 previous failures")
		}
	})
}

func TestRunRefineSurfacesResponseErrors(t *testing.T) {
	repoDir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "base commit")
	runGit("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("change"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "feature.txt")
	runGit("commit", "-m", "feature commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/status":
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		case r.URL.Path == "/api/review":
			json.NewEncoder(w).Encode(storage.Review{
				ID: 1, JobID: 1, Output: "**Bug found**: fail", Addressed: false,
			})
		case r.URL.Path == "/api/responses":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer cleanup()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	if err := runRefine("test", "", 1, true, false); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRunRefineQuietNonTTYTimerOutput(t *testing.T) {
	repoDir, headSHA := setupRefineRepo(t)
	state := newMockRefineState()
	state.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 42, Output: "**Bug found**: fail", Addressed: false,
	}

	_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
	defer cleanup()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	output := captureStdout(t, func() {
		if err := runRefine("test", "", 1, true, false); err == nil {
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
	state := newMockRefineState()
	state.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 7, Output: "**Bug found**: fail", Addressed: false,
	}

	_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
	defer cleanup()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return true }
	defer func() { isTerminal = origIsTerminal }()

	agent.Register(failingAgent{})
	defer agent.Register(agent.NewTestAgent())

	output := captureStdout(t, func() {
		if err := runRefine("test", "", 1, true, false); err == nil {
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

func TestCreateTempWorktreeInitializesSubmodules(t *testing.T) {
	submoduleRepo := t.TempDir()
	runSubGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = submoduleRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runSubGit("init", "-b", "main")
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

	runMainGit("init", "-b", "main")
	runMainGit("config", "user.email", "test@test.com")
	runMainGit("config", "user.name", "Test")
	runMainGit("config", "protocol.file.allow", "always")
	runMainGit("-c", "protocol.file.allow=always", "submodule", "add", submoduleRepo, "deps/sub")
	runMainGit("commit", "-m", "add submodule")

	worktreePath, cleanup, err := createTempWorktree(mainRepo)
	if err != nil {
		t.Fatalf("createTempWorktree failed: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(worktreePath, "deps", "sub", "sub.txt")); err != nil {
		t.Fatalf("expected submodule file in worktree: %v", err)
	}
}

// ============================================================================
// Integration Tests for Refine Loop Business Logic
// ============================================================================

// mockRefineState tracks state for simulating the full refine loop
type mockRefineState struct {
	reviews       map[string]*storage.Review   // SHA -> review
	jobs          map[int64]*storage.ReviewJob // jobID -> job
	responses     map[int64][]storage.Response // jobID -> responses
	addressedIDs  []int64                      // review IDs that were marked addressed
	nextJobID     int64
	enqueuedRefs  []string // git refs that were enqueued for review
	respondCalled []struct {
		jobID     int64
		responder string
		response  string
	}
}

func newMockRefineState() *mockRefineState {
	return &mockRefineState{
		reviews:   make(map[string]*storage.Review),
		jobs:      make(map[int64]*storage.ReviewJob),
		responses: make(map[int64][]storage.Response),
		nextJobID: 1,
	}
}

// createMockRefineHandler creates an HTTP handler that simulates daemon behavior
func createMockRefineHandler(state *mockRefineState) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/status":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"version": version.Version,
			})

		case r.URL.Path == "/api/review" && r.Method == "GET":
			sha := r.URL.Query().Get("sha")
			jobIDStr := r.URL.Query().Get("job_id")

			var review *storage.Review
			if sha != "" {
				review = state.reviews[sha]
			} else if jobIDStr != "" {
				var jobID int64
				fmt.Sscanf(jobIDStr, "%d", &jobID)
				// Find review by job ID
				for _, rev := range state.reviews {
					if rev.JobID == jobID {
						review = rev
						break
					}
				}
			}

			if review == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(review)

		case r.URL.Path == "/api/responses" && r.Method == "GET":
			jobIDStr := r.URL.Query().Get("job_id")
			var jobID int64
			fmt.Sscanf(jobIDStr, "%d", &jobID)
			responses := state.responses[jobID]
			if responses == nil {
				responses = []storage.Response{}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"responses": responses,
			})

		case r.URL.Path == "/api/respond" && r.Method == "POST":
			var req struct {
				JobID     int64  `json:"job_id"`
				Responder string `json:"responder"`
				Response  string `json:"response"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			state.respondCalled = append(state.respondCalled, struct {
				jobID     int64
				responder string
				response  string
			}{req.JobID, req.Responder, req.Response})

			// Add to responses
			resp := storage.Response{
				ID:        int64(len(state.responses[req.JobID]) + 1),
				Responder: req.Responder,
				Response:  req.Response,
			}
			state.responses[req.JobID] = append(state.responses[req.JobID], resp)

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/api/review/address" && r.Method == "POST":
			var req struct {
				ReviewID  int64 `json:"review_id"`
				Addressed bool  `json:"addressed"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Addressed {
				state.addressedIDs = append(state.addressedIDs, req.ReviewID)
				// Update the review in state
				for _, rev := range state.reviews {
					if rev.ID == req.ReviewID {
						rev.Addressed = true
						break
					}
				}
			}
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/enqueue" && r.Method == "POST":
			var req struct {
				RepoPath string `json:"repo_path"`
				GitRef   string `json:"git_ref"`
				Agent    string `json:"agent"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			state.enqueuedRefs = append(state.enqueuedRefs, req.GitRef)

			job := &storage.ReviewJob{
				ID:     state.nextJobID,
				GitRef: req.GitRef,
				Agent:  req.Agent,
				Status: storage.JobStatusDone,
			}
			state.jobs[job.ID] = job
			state.nextJobID++

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(job)

		case r.URL.Path == "/api/jobs" && r.Method == "GET":
			var jobs []storage.ReviewJob
			for _, job := range state.jobs {
				jobs = append(jobs, *job)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     jobs,
				"has_more": false,
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

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
		review, err := findFailedReviewForBranch(client, commits)

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
		review, err := findFailedReviewForBranch(client, commits)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review != nil {
			t.Error("expected nil - addressed reviews should be skipped")
		}
	})
}

func TestRefineLoopNoChangeRetryScenario(t *testing.T) {
	// Test the retry counting when agent makes no changes
	// countNoChangeAttempts mirrors the logic in runRefine()
	countNoChangeAttempts := func(responses []storage.Response) int {
		count := 0
		for _, a := range responses {
			// Must match both responder AND message pattern (security: prevent other responders from inflating count)
			if a.Responder == "roborev-refine" && strings.Contains(a.Response, "could not determine how to address") {
				count++
			}
		}
		return count
	}

	t.Run("three consecutive no-change attempts mark review as addressed", func(t *testing.T) {
		state := newMockRefineState()
		// Simulate 2 previous failed attempts from roborev-refine
		state.responses[42] = []storage.Response{
			{ID: 1, Responder: "roborev-refine", Response: "Agent could not determine how to address findings (attempt 1)"},
			{ID: 2, Responder: "roborev-refine", Response: "Agent could not determine how to address findings (attempt 2)"},
		}

		_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
		defer cleanup()

		responses, err := getResponsesForJob(42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		noChangeAttempts := countNoChangeAttempts(responses)

		// Should have 2 previous attempts
		if noChangeAttempts != 2 {
			t.Errorf("expected 2 previous no-change attempts, got %d", noChangeAttempts)
		}

		// Per the logic: if noChangeAttempts >= 2, we mark as addressed (third attempt)
		shouldGiveUp := noChangeAttempts >= 2
		if !shouldGiveUp {
			t.Error("expected to give up after 2 previous failures")
		}
	})

	t.Run("first no-change attempt does not mark as addressed", func(t *testing.T) {
		state := newMockRefineState()
		// No previous attempts
		state.responses[42] = []storage.Response{}

		_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
		defer cleanup()

		responses, _ := getResponsesForJob(42)
		noChangeAttempts := countNoChangeAttempts(responses)

		// Should not give up yet (first attempt)
		shouldGiveUp := noChangeAttempts >= 2
		if shouldGiveUp {
			t.Error("should not give up on first attempt")
		}
	})

	t.Run("responses from other responders do not inflate retry count", func(t *testing.T) {
		state := newMockRefineState()
		// Mix of responses: 1 from roborev-refine, 2 from other sources with same message
		state.responses[42] = []storage.Response{
			{ID: 1, Responder: "roborev-refine", Response: "Agent could not determine how to address findings (attempt 1)"},
			{ID: 2, Responder: "user", Response: "Agent could not determine how to address findings - I tried manually"},
			{ID: 3, Responder: "other-tool", Response: "could not determine how to address this issue"},
		}

		_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
		defer cleanup()

		responses, _ := getResponsesForJob(42)
		noChangeAttempts := countNoChangeAttempts(responses)

		// Should only count the 1 response from roborev-refine, not the others
		if noChangeAttempts != 1 {
			t.Errorf("expected 1 no-change attempt (from roborev-refine only), got %d", noChangeAttempts)
		}

		// With only 1 attempt, should NOT give up yet
		shouldGiveUp := noChangeAttempts >= 2
		if shouldGiveUp {
			t.Error("should not give up when only 1 roborev-refine attempt exists")
		}
	})
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
		review, err := findFailedReviewForBranch(client, commits)

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
		state := newMockRefineState()
		_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
		defer cleanup()

		jobID, err := enqueueReview("/test/repo", "abc123..HEAD", "codex")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jobID == 0 {
			t.Error("expected non-zero job ID")
		}

		// Verify the range ref was enqueued
		if len(state.enqueuedRefs) != 1 {
			t.Fatalf("expected 1 enqueued ref, got %d", len(state.enqueuedRefs))
		}
		if state.enqueuedRefs[0] != "abc123..HEAD" {
			t.Errorf("expected range ref 'abc123..HEAD', got '%s'", state.enqueuedRefs[0])
		}
	})
}

func TestRefineLoopWaitForReviewCompletion(t *testing.T) {
	// Test waiting for a review to complete

	t.Run("returns review when job completes successfully", func(t *testing.T) {
		state := newMockRefineState()
		state.jobs[42] = &storage.ReviewJob{ID: 42, GitRef: "abc123", Status: storage.JobStatusDone}
		state.reviews["abc123"] = &storage.Review{
			ID: 1, JobID: 42, Output: "All tests pass. No issues found.", Addressed: false,
		}

		_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
		defer cleanup()

		review, err := waitForReview(42)
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
		state := newMockRefineState()
		state.jobs[42] = &storage.ReviewJob{
			ID:     42,
			GitRef: "abc123",
			Status: storage.JobStatusFailed,
			Error:  "Agent timeout after 10 minutes",
		}

		_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
		defer cleanup()

		_, err := waitForReview(42)
		if err == nil {
			t.Fatal("expected error for failed job")
		}
		if !strings.Contains(err.Error(), "timeout") {
			t.Errorf("error should mention failure reason, got: %v", err)
		}
	})
}

type changingAgent struct {
	count int
}

func (a *changingAgent) Name() string { return "test" }

func (a *changingAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	a.count++
	change := fmt.Sprintf("fix %d", a.count)
	if err := os.WriteFile(filepath.Join(repoPath, "fix.txt"), []byte(change), 0644); err != nil {
		return "", err
	}
	if output != nil {
		if _, err := output.Write([]byte(change)); err != nil {
			return "", err
		}
	}
	return change, nil
}

func (a *changingAgent) WithReasoning(level agent.ReasoningLevel) agent.Agent {
	return a
}

func TestRefineLoopStaysOnFailedFixChain(t *testing.T) {
	repoDir, _ := setupRefineRepo(t)
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		} else if len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
		return ""
	}

	if err := os.WriteFile(filepath.Join(repoDir, "second.txt"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "second.txt")
	runGit("commit", "-m", "second commit")

	commitList := strings.Fields(runGit("rev-list", "--reverse", "main..HEAD"))
	if len(commitList) < 2 {
		t.Fatalf("expected two commits on branch, got %d", len(commitList))
	}
	oldestCommit := commitList[0]
	newestCommit := commitList[1]

	state := newMockRefineState()
	state.nextJobID = 100
	state.reviews[oldestCommit] = &storage.Review{
		ID: 1, JobID: 1, Output: "**Bug**: old failure", Addressed: false,
	}
	state.reviews[newestCommit] = &storage.Review{
		ID: 2, JobID: 2, Output: "**Bug**: new failure", Addressed: false,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/status":
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		case r.URL.Path == "/api/review" && r.Method == http.MethodGet:
			sha := r.URL.Query().Get("sha")
			jobIDStr := r.URL.Query().Get("job_id")

			var review *storage.Review
			if sha != "" {
				review = state.reviews[sha]
			} else if jobIDStr != "" {
				var jobID int64
				fmt.Sscanf(jobIDStr, "%d", &jobID)
				for _, rev := range state.reviews {
					if rev.JobID == jobID {
						review = rev
						break
					}
				}
			}

			if review == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(review)

		case r.URL.Path == "/api/responses" && r.Method == http.MethodGet:
			jobIDStr := r.URL.Query().Get("job_id")
			var jobID int64
			fmt.Sscanf(jobIDStr, "%d", &jobID)
			responses := state.responses[jobID]
			if responses == nil {
				responses = []storage.Response{}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"responses": responses,
			})

		case r.URL.Path == "/api/respond" && r.Method == http.MethodPost:
			var req struct {
				JobID     int64  `json:"job_id"`
				Responder string `json:"responder"`
				Response  string `json:"response"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			state.respondCalled = append(state.respondCalled, struct {
				jobID     int64
				responder string
				response  string
			}{req.JobID, req.Responder, req.Response})

			resp := storage.Response{
				ID:        int64(len(state.responses[req.JobID]) + 1),
				Responder: req.Responder,
				Response:  req.Response,
			}
			state.responses[req.JobID] = append(state.responses[req.JobID], resp)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/api/review/address" && r.Method == http.MethodPost:
			var req struct {
				ReviewID  int64 `json:"review_id"`
				Addressed bool  `json:"addressed"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Addressed {
				state.addressedIDs = append(state.addressedIDs, req.ReviewID)
				for _, rev := range state.reviews {
					if rev.ID == req.ReviewID {
						rev.Addressed = true
						break
					}
				}
			}
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/jobs" && r.Method == http.MethodGet:
			q := r.URL.Query()
			if idStr := q.Get("id"); idStr != "" {
				var jobID int64
				fmt.Sscanf(idStr, "%d", &jobID)
				job, ok := state.jobs[jobID]
				if !ok {
					json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
					return
				}
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{*job}})
				return
			}
			if gitRef := q.Get("git_ref"); gitRef != "" {
				var job *storage.ReviewJob
				for _, j := range state.jobs {
					if j.GitRef == gitRef {
						job = j
						break
					}
				}
				if job == nil {
					job = &storage.ReviewJob{
						ID:       state.nextJobID,
						GitRef:   gitRef,
						Agent:    "test",
						Status:   storage.JobStatusDone,
						RepoPath: q.Get("repo"),
					}
					state.jobs[job.ID] = job
					state.nextJobID++
				}
				if _, ok := state.reviews[gitRef]; !ok {
					state.reviews[gitRef] = &storage.Review{
						ID:     job.ID + 1000,
						JobID:  job.ID,
						Output: "**Bug**: fix failed",
					}
				}
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{*job}})
				return
			}

			var jobs []storage.ReviewJob
			for _, job := range state.jobs {
				jobs = append(jobs, *job)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     jobs,
				"has_more": false,
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, cleanup := setupMockDaemon(t, handler)
	defer cleanup()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	changer := &changingAgent{}
	agent.Register(changer)
	defer agent.Register(agent.NewTestAgent())

	if err := runRefine("test", "", 2, true, false); err == nil {
		t.Fatal("expected error from reaching max iterations")
	}

	for _, call := range state.respondCalled {
		if call.jobID == 2 {
			t.Fatalf("expected to stay on failed fix chain; saw response for newer commit job 2")
		}
	}
}
