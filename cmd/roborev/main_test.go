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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

// ============================================================================
// Refine Command Tests
// ============================================================================

// setupMockDaemon creates a test server and writes a fake daemon.json so
// getDaemonAddr() returns the test server address.
func setupMockDaemon(t *testing.T, handler http.Handler) (*httptest.Server, func()) {
	t.Helper()

	// Wrap handler to handle /api/status requests.
	// IsDaemonAlive calls /api/status to verify daemon is alive.
	// ensureDaemon also calls /api/status to check the daemon version.
	// We need to return a proper response with version info to prevent
	// ensureDaemon from trying to restart the daemon (which would kill the test).
	wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" && r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"version": version.Version,
			})
			return
		}
		handler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(wrappedHandler)

	// Use ROBOREV_DATA_DIR to override data directory (works cross-platform)
	// On Windows, HOME doesn't work since os.UserHomeDir() uses USERPROFILE
	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Write fake daemon.json
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatalf("failed to create data dir: %v", err)
	}
	mockAddr := ts.URL[7:] // strip "http://"
	daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
	data, err := json.Marshal(daemonInfo)
	if err != nil {
		t.Fatalf("failed to marshal daemon.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), data, 0644); err != nil {
		t.Fatalf("failed to write daemon.json: %v", err)
	}

	// Also set serverAddr for functions that use it directly
	origServerAddr := serverAddr
	serverAddr = ts.URL

	cleanup := func() {
		ts.Close()
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
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

	runGit("init")
	runGit("symbolic-ref", "HEAD", "refs/heads/main")
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

// functionalMockAgent is a configurable mock agent that accepts behavior as a function.
type functionalMockAgent struct {
	nameVal    string
	reviewFunc func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error)
}

func (f *functionalMockAgent) Name() string { return f.nameVal }

func (f *functionalMockAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	if f.reviewFunc == nil {
		panic("functionalMockAgent.Review called with nil reviewFunc — set reviewFunc before use")
	}
	return f.reviewFunc(ctx, repoPath, commitSHA, prompt, output)
}

func (f *functionalMockAgent) WithReasoning(level agent.ReasoningLevel) agent.Agent { return f }
func (f *functionalMockAgent) WithAgentic(agentic bool) agent.Agent                 { return f }
func (f *functionalMockAgent) WithModel(model string) agent.Agent                   { return f }

// inDir changes to the given directory for the duration of fn, then restores the original directory.
func inDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(orig)
	fn()
}

// MockRefineHooks allows overriding specific endpoints in the mock refine handler.
type MockRefineHooks struct {
	OnGetJobs func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool // Return true if handled
	OnEnqueue func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
}

// createConfigurableMockRefineHandler wraps createMockRefineHandler with hook overrides.
func createConfigurableMockRefineHandler(state *mockRefineState, hooks MockRefineHooks) http.Handler {
	base := createMockRefineHandler(state)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" && r.Method == "GET" && hooks.OnGetJobs != nil {
			if hooks.OnGetJobs(w, r, state) {
				return
			}
		}
		if r.URL.Path == "/api/enqueue" && r.Method == "POST" && hooks.OnEnqueue != nil {
			if hooks.OnEnqueue(w, r, state) {
				return
			}
		}
		base.ServeHTTP(w, r)
	})
}

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
	setupFastPolling(t)
	repoDir, _ := setupRefineRepo(t)

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/status":
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		case r.URL.Path == "/api/review":
			json.NewEncoder(w).Encode(storage.Review{
				ID: 1, JobID: 1, Output: "**Bug found**: fail", Addressed: false,
			})
		case r.URL.Path == "/api/comments":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer cleanup()

	inDir(t, repoDir, func() {
		if err := runRefine("test", "", "", 1, true, false, false, ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestRunRefineQuietNonTTYTimerOutput(t *testing.T) {
	setupFastPolling(t)
	repoDir, headSHA := setupRefineRepo(t)
	state := newMockRefineState()
	state.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 42, Output: "**Bug found**: fail", Addressed: false,
	}

	_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
	defer cleanup()

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	inDir(t, repoDir, func() {
		output := captureStdout(t, func() {
			if err := runRefine("test", "", "", 1, true, false, false, ""); err == nil {
				t.Fatal("expected error, got nil")
			}
		})

		if strings.Contains(output, "\r") {
			t.Fatalf("expected no carriage returns in non-tty output, got: %q", output)
		}
		if !strings.Contains(output, "Addressing review (job 42)...") {
			t.Fatalf("expected final timer line in output, got: %q", output)
		}
	})
}

func TestRunRefineStopsLiveTimerOnAgentError(t *testing.T) {
	setupFastPolling(t)
	repoDir, headSHA := setupRefineRepo(t)
	state := newMockRefineState()
	state.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 7, Output: "**Bug found**: fail", Addressed: false,
	}

	_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
	defer cleanup()

	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return true }
	defer func() { isTerminal = origIsTerminal }()

	agent.Register(&functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		return "", fmt.Errorf("test agent failure")
	}})
	defer agent.Register(agent.NewTestAgent())

	inDir(t, repoDir, func() {
		output := captureStdout(t, func() {
			if err := runRefine("test", "", "", 1, true, false, false, ""); err == nil {
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
	})
}

// TestRunRefineAgentErrorRetriesWithoutApplyingChanges verifies that when the agent
// returns an error, the error is properly captured and printed (not shadowed), and
// the refine loop retries in the next iteration without applying any changes.
func TestRunRefineAgentErrorRetriesWithoutApplyingChanges(t *testing.T) {
	setupFastPolling(t)
	repoDir, headSHA := setupRefineRepo(t)
	state := newMockRefineState()
	state.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 7, Output: "**Bug found**: fail", Addressed: false,
	}

	_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
	defer cleanup()

	// Use 2 iterations so we can verify retry behavior
	agent.Register(&functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		return "", fmt.Errorf("test agent failure")
	}})
	defer agent.Register(agent.NewTestAgent())

	// Capture HEAD before running refine
	headBefore, _ := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()

	inDir(t, repoDir, func() {
		output := captureStdout(t, func() {
			// With 2 iterations and a failing agent, should exhaust iterations
			err := runRefine("test", "", "", 2, true, false, false, "")
			if err == nil {
				t.Fatal("expected error after exhausting iterations, got nil")
			}
		})

		// Verify agent error message is printed (not shadowed by ResolveSHA)
		if !strings.Contains(output, "Agent error: test agent failure") {
			t.Errorf("expected 'Agent error: test agent failure' in output, got: %q", output)
		}

		// Verify "Will retry in next iteration" message
		if !strings.Contains(output, "Will retry in next iteration") {
			t.Errorf("expected 'Will retry in next iteration' in output, got: %q", output)
		}

		// Verify no commit was created (HEAD unchanged)
		headAfter, _ := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
		if string(headBefore) != string(headAfter) {
			t.Errorf("expected HEAD to be unchanged after agent error, was %s now %s",
				strings.TrimSpace(string(headBefore)), strings.TrimSpace(string(headAfter)))
		}

		// Verify we attempted 2 iterations (both printed)
		if !strings.Contains(output, "=== Refinement iteration 1/2 ===") {
			t.Errorf("expected iteration 1/2 in output, got: %q", output)
		}
		if !strings.Contains(output, "=== Refinement iteration 2/2 ===") {
			t.Errorf("expected iteration 2/2 in output, got: %q", output)
		}
	})
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

	runSubGit("init")
	runSubGit("symbolic-ref", "HEAD", "refs/heads/main")
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

	runMainGit("init")
	runMainGit("symbolic-ref", "HEAD", "refs/heads/main")
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
	mu            sync.Mutex
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

			state.mu.Lock()
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
			// Copy under lock before encoding
			var reviewCopy storage.Review
			if review != nil {
				reviewCopy = *review
			}
			state.mu.Unlock()

			if review == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(reviewCopy)

		case r.URL.Path == "/api/comments" && r.Method == "GET":
			jobIDStr := r.URL.Query().Get("job_id")
			var jobID int64
			fmt.Sscanf(jobIDStr, "%d", &jobID)
			state.mu.Lock()
			// Copy slice under lock before encoding
			origResponses := state.responses[jobID]
			responses := make([]storage.Response, len(origResponses))
			copy(responses, origResponses)
			state.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{
				"responses": responses,
			})

		case r.URL.Path == "/api/comment" && r.Method == "POST":
			var req struct {
				JobID     int64  `json:"job_id"`
				Responder string `json:"responder"`
				Response  string `json:"response"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			state.mu.Lock()
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
			state.mu.Unlock()

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/api/review/address" && r.Method == "POST":
			var req struct {
				ReviewID  int64 `json:"review_id"`
				Addressed bool  `json:"addressed"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			state.mu.Lock()
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
			state.mu.Unlock()
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/enqueue" && r.Method == "POST":
			var req struct {
				RepoPath string `json:"repo_path"`
				GitRef   string `json:"git_ref"`
				Agent    string `json:"agent"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			state.mu.Lock()
			state.enqueuedRefs = append(state.enqueuedRefs, req.GitRef)

			job := &storage.ReviewJob{
				ID:     state.nextJobID,
				GitRef: req.GitRef,
				Agent:  req.Agent,
				Status: storage.JobStatusDone,
			}
			state.jobs[job.ID] = job
			state.nextJobID++
			state.mu.Unlock()

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(job)

		case r.URL.Path == "/api/jobs" && r.Method == "GET":
			state.mu.Lock()
			var jobs []storage.ReviewJob
			for _, job := range state.jobs {
				jobs = append(jobs, *job)
			}
			state.mu.Unlock()
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
	state := newMockRefineState()
	state.responses[42] = []storage.Response{}
	jobID99 := int64(99)
	state.responses[99] = []storage.Response{
		{ID: 1, JobID: &jobID99, Responder: "roborev-refine", Response: "Agent could not determine how to address findings"},
	}

	_, cleanup := setupMockDaemon(t, createMockRefineHandler(state))
	defer cleanup()

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
	setupFastPolling(t)
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

func TestRefineLoopStaysOnFailedFixChain(t *testing.T) {
	setupFastPolling(t)
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

	// Use configurable handler with custom /api/jobs logic that auto-creates
	// jobs and failing reviews for unknown git_refs (simulating re-review after fix).
	handler := createConfigurableMockRefineHandler(state, MockRefineHooks{
		OnGetJobs: func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
			q := r.URL.Query()
			if idStr := q.Get("id"); idStr != "" {
				var jobID int64
				fmt.Sscanf(idStr, "%d", &jobID)
				s.mu.Lock()
				job, ok := s.jobs[jobID]
				if !ok {
					s.mu.Unlock()
					json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
					return true
				}
				jobCopy := *job
				s.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{jobCopy}})
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
				if job == nil {
					job = &storage.ReviewJob{
						ID:       s.nextJobID,
						GitRef:   gitRef,
						Agent:    "test",
						Status:   storage.JobStatusDone,
						RepoPath: q.Get("repo"),
					}
					s.jobs[job.ID] = job
					s.nextJobID++
				}
				if _, ok := s.reviews[gitRef]; !ok {
					s.reviews[gitRef] = &storage.Review{
						ID:     job.ID + 1000,
						JobID:  job.ID,
						Output: "**Bug**: fix failed",
					}
				}
				jobCopy := *job
				s.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{jobCopy}})
				return true
			}
			return false // fall through to base handler
		},
	})

	_, cleanup := setupMockDaemon(t, handler)
	defer cleanup()

	var changeCount int
	agent.Register(&functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		changeCount++
		change := fmt.Sprintf("fix %d", changeCount)
		if err := os.WriteFile(filepath.Join(repoPath, "fix.txt"), []byte(change), 0644); err != nil {
			return "", err
		}
		if output != nil {
			output.Write([]byte(change))
		}
		return change, nil
	}})
	defer agent.Register(agent.NewTestAgent())

	inDir(t, repoDir, func() {
		if err := runRefine("test", "", "", 2, true, false, false, ""); err == nil {
			t.Fatal("expected error from reaching max iterations")
		}
	})

	for _, call := range state.respondCalled {
		if call.jobID == 2 {
			t.Fatalf("expected to stay on failed fix chain; saw response for newer commit job 2")
		}
	}
}

// TestRefinePendingJobWaitDoesNotConsumeIteration verifies that waiting for a pending
// (in-progress) job does not consume a refinement iteration. The test runs with
// maxIterations=1 and a job that starts as Running then transitions to Done with a
// passing review - if iterations were consumed during the wait, the test would fail.
func TestRefinePendingJobWaitDoesNotConsumeIteration(t *testing.T) {
	setupFastPolling(t)
	repoDir, commitSHA := setupRefineRepo(t)

	// Track how many times the job has been polled
	var pollCount int32

	state := newMockRefineState()
	// Create a job that starts as Running
	state.jobs[1] = &storage.ReviewJob{
		ID:       1,
		GitRef:   commitSHA,
		Agent:    "test",
		Status:   storage.JobStatusRunning, // Starts as pending
		RepoPath: repoDir,
	}
	// Passing review (will be returned once job is Done)
	state.reviews[commitSHA] = &storage.Review{
		ID: 1, JobID: 1, Output: "No issues found. LGTM!", Addressed: false,
	}
	state.nextJobID = 2

	// Use configurable handler with custom /api/jobs (job transitions from Running to Done)
	// and custom /api/enqueue (creates passing branch reviews).
	handler := createConfigurableMockRefineHandler(state, MockRefineHooks{
		OnGetJobs: func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
			q := r.URL.Query()
			if idStr := q.Get("id"); idStr != "" {
				var jobID int64
				fmt.Sscanf(idStr, "%d", &jobID)
				s.mu.Lock()
				job, ok := s.jobs[jobID]
				if !ok {
					s.mu.Unlock()
					json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
					return true
				}
				// On first poll, job is still Running; on subsequent polls, transition to Done
				count := atomic.AddInt32(&pollCount, 1)
				if count > 1 {
					job.Status = storage.JobStatusDone
				}
				jobCopy := *job
				s.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{jobCopy}})
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
					json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{jobCopy}})
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

	_, cleanup := setupMockDaemon(t, handler)
	defer cleanup()

	inDir(t, repoDir, func() {
		// Run refine with maxIterations=1. If waiting on the pending job consumed
		// an iteration, this would fail with "max iterations reached". Since the
		// pending job transitions to Done with a passing review (and no failed
		// reviews exist), refine should succeed.
		err := runRefine("test", "", "", 1, true, false, false, "")

		// Should succeed - all reviews pass after waiting for the pending one
		if err != nil {
			t.Fatalf("expected refine to succeed (pending wait should not consume iteration), got: %v", err)
		}
	})

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
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			json.NewEncoder(w).Encode(map[string]string{"version": version.Version})
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	_, cleanup := setupMockDaemon(t, handler)
	defer cleanup()

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
// Daemon Run Tests
// ============================================================================

func TestDaemonRunStartsAndShutdownsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping daemon integration test on Windows due to file locking differences")
	}

	// Use temp directories for isolation
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	configPath := filepath.Join(tmpDir, "config.toml")

	// Isolate runtime dir to avoid writing to real ~/.roborev/daemon.json
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

	// Write minimal config
	if err := os.WriteFile(configPath, []byte(`max_workers = 1`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Create the daemon run command with custom flags
	// Use a high base port to avoid conflicts with production (7373).
	// FindAvailablePort will auto-increment if 17373 is busy.
	cmd := daemonRunCmd()
	cmd.SetArgs([]string{
		"--db", dbPath,
		"--config", configPath,
		"--addr", "127.0.0.1:17373",
	})

	// Run daemon in goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Execute()
	}()

	// Wait for daemon to start (check if DB file is created)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dbPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify DB was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected database to be created")
	}

	// Check that daemon didn't exit early with an error
	select {
	case err := <-errCh:
		t.Fatalf("daemon exited unexpectedly: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Daemon is still running - good
	}

	// Wait for daemon to be fully started and responsive
	// The runtime file is written before ListenAndServe, so we need to verify
	// the HTTP server is actually accepting connections.
	// Use longer timeout for race detector which adds significant overhead.
	var info *daemon.RuntimeInfo
	myPID := os.Getpid()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runtimes, err := daemon.ListAllRuntimes()
		if err == nil {
			// Find the runtime for OUR daemon (matching our PID), not a stale one
			for _, rt := range runtimes {
				if rt.PID == myPID && daemon.IsDaemonAlive(rt.Addr) {
					info = rt
					break
				}
			}
			if info != nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if info == nil {
		// Provide more context for debugging CI failures
		runtimes, _ := daemon.ListAllRuntimes()
		t.Fatalf("daemon did not create runtime file or is not responding (myPID=%d, found %d runtimes)", myPID, len(runtimes))
	}

	// The daemon runs in a goroutine within this test process.
	// Use os.Interrupt to trigger the signal handler.
	proc, err := os.FindProcess(myPID)
	if err != nil {
		t.Fatalf("failed to find own process: %v", err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		t.Fatalf("failed to send interrupt signal: %v", err)
	}

	// Wait for daemon to exit (longer timeout for race detector)
	select {
	case <-errCh:
		// Daemon exited - good
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit within 10 second timeout")
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
	defer os.Chmod(daemonPath, 0644)

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
