package main

import (
	"bytes"
	"encoding/json"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestGitRepo wraps a temporary git repository for test use.
type TestGitRepo struct {
	Dir string
	t   *testing.T
}

// newTestGitRepo creates and initializes a temporary git repository.
func newTestGitRepo(t *testing.T) *TestGitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("Failed to resolve symlinks: %v", err)
	}
	r := &TestGitRepo{Dir: resolved, t: t}
	r.Run("init")
	r.Run("config", "user.email", "test@test.com")
	r.Run("config", "user.name", "Test")
	return r
}

// chdir changes to dir and registers a t.Cleanup to restore the original directory.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

// Run executes a git command in the repo directory and returns trimmed output.
// It isolates git from the user's global config to prevent flaky tests.
func (r *TestGitRepo) Run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	// Build a clean environment with only the variables git needs,
	// avoiding conflicts from inherited duplicates.
	gitEnv := []string{
		"HOME=" + r.Dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	}
	overridden := map[string]bool{
		"HOME":                true,
		"GIT_CONFIG_NOSYSTEM": true,
		"GIT_AUTHOR_NAME":     true,
		"GIT_AUTHOR_EMAIL":    true,
		"GIT_COMMITTER_NAME":  true,
		"GIT_COMMITTER_EMAIL": true,
	}
	for _, env := range os.Environ() {
		if key, _, ok := strings.Cut(env, "="); ok && !overridden[key] {
			gitEnv = append(gitEnv, env)
		}
	}
	cmd.Env = gitEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// CommitFile creates or overwrites a file, stages it, and commits with the
// given message. Returns the full commit SHA.
func (r *TestGitRepo) CommitFile(name, content, msg string) string {
	r.t.Helper()
	fullPath := filepath.Join(r.Dir, name)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		r.t.Fatal(err)
	}
	r.Run("add", name)
	r.Run("commit", "-m", msg)
	return r.Run("rev-parse", "HEAD")
}

// WriteFiles writes the given files to the repository directory.
func (r *TestGitRepo) WriteFiles(files map[string]string) {
	r.t.Helper()
	writeFiles(r.t, r.Dir, files)
}

// patchServerAddr safely swaps the global serverAddr variable and restores it
// when the test completes.
func patchServerAddr(t *testing.T, newURL string) {
	t.Helper()
	old := serverAddr
	serverAddr = newURL
	t.Cleanup(func() { serverAddr = old })
}

// createTestRepo creates a temporary git repository with the given files
// committed. It returns the TestGitRepo.
func createTestRepo(t *testing.T, files map[string]string) *TestGitRepo {
	t.Helper()

	r := newTestGitRepo(t)
	r.WriteFiles(files)
	r.Run("add", ".")
	r.Run("commit", "-m", "initial")
	return r
}

// writeTestFiles creates files in a directory without git. Returns the directory.
func writeTestFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	return dir
}

// writeFiles is a helper to write files to a directory.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		fullPath := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// mockReviewDaemon sets up a mock daemon that returns the given review on
// GET /api/review. It returns a function to retrieve the last received query
// string.
func mockReviewDaemon(t *testing.T, review storage.Review) func() string {
	t.Helper()
	var mu sync.Mutex
	var receivedQuery string
	daemonFromHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/review" && r.Method == "GET" {
			mu.Lock()
			receivedQuery = r.URL.RawQuery
			mu.Unlock()
			json.NewEncoder(w).Encode(review)
			return
		}
	}))
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return receivedQuery
	}
}

// runShowCmd executes showCmd() with the given args and returns captured stdout.
func runShowCmd(t *testing.T, args ...string) string {
	t.Helper()
	cmd := showCmd()
	cmd.SetArgs(args)
	return captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// newTestCmd creates a cobra.Command with output captured to the returned buffer.
func newTestCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	return cmd, &buf
}

// MockServerState tracks counters for API calls made to a mock server.
type MockServerState struct {
	EnqueueCount int32
	JobsCount    int32
	ReviewCount  int32
	AddressCount int32
	CommentCount int32
}

func (s *MockServerState) Enqueues() int32  { return atomic.LoadInt32(&s.EnqueueCount) }
func (s *MockServerState) Jobs() int32      { return atomic.LoadInt32(&s.JobsCount) }
func (s *MockServerState) Reviews() int32   { return atomic.LoadInt32(&s.ReviewCount) }
func (s *MockServerState) Addresses() int32 { return atomic.LoadInt32(&s.AddressCount) }
func (s *MockServerState) Comments() int32  { return atomic.LoadInt32(&s.CommentCount) }

// MockServerOpts configures the behavior of a mock roborev server.
type MockServerOpts struct {
	// JobIDStart is the starting job ID for enqueue responses (0 defaults to 1).
	JobIDStart int64
	// Agent is the agent name in responses (default "test").
	Agent string
	// DoneAfterPolls is the number of /api/jobs polls before reporting done (default 2).
	DoneAfterPolls int32
	// ReviewOutput is the review text returned by /api/review.
	ReviewOutput string
	// OnEnqueue is an optional callback for /api/enqueue requests.
	OnEnqueue func(w http.ResponseWriter, r *http.Request)
	// OnJobs is an optional callback for /api/jobs requests. If set, overrides default behavior.
	OnJobs func(w http.ResponseWriter, r *http.Request)
}

type mockServerHandler struct {
	opts  MockServerOpts
	state *MockServerState
	jobID int64
}

func (h *mockServerHandler) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.opts.OnEnqueue != nil {
		atomic.AddInt32(&h.state.EnqueueCount, 1)
		h.opts.OnEnqueue(w, r)
		return
	}
	id := atomic.AddInt64(&h.jobID, 1)
	atomic.AddInt32(&h.state.EnqueueCount, 1)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(storage.ReviewJob{
		ID:     id,
		Agent:  h.opts.Agent,
		Status: storage.JobStatusQueued,
	})
}

func (h *mockServerHandler) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.opts.OnJobs != nil {
		atomic.AddInt32(&h.state.JobsCount, 1)
		h.opts.OnJobs(w, r)
		return
	}
	count := atomic.AddInt32(&h.state.JobsCount, 1)
	status := storage.JobStatusQueued
	if count >= h.opts.DoneAfterPolls {
		status = storage.JobStatusDone
	}
	json.NewEncoder(w).Encode(map[string]any{
		"jobs": []storage.ReviewJob{{
			ID:     atomic.LoadInt64(&h.jobID),
			Status: status,
		}},
	})
}

func (h *mockServerHandler) handleReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	atomic.AddInt32(&h.state.ReviewCount, 1)
	output := h.opts.ReviewOutput
	if output == "" {
		output = "review output"
	}
	json.NewEncoder(w).Encode(storage.Review{
		JobID:  atomic.LoadInt64(&h.jobID),
		Agent:  h.opts.Agent,
		Output: output,
	})
}

func (h *mockServerHandler) handleComment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	atomic.AddInt32(&h.state.CommentCount, 1)
	w.WriteHeader(http.StatusCreated)
}

func (h *mockServerHandler) handleAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	atomic.AddInt32(&h.state.AddressCount, 1)
	w.WriteHeader(http.StatusOK)
}

// newMockServer creates an httptest.Server that mimics the roborev daemon API.
// It handles /api/enqueue, /api/jobs, /api/review, and /api/review/address.
func newMockServer(t *testing.T, opts MockServerOpts) (*httptest.Server, *MockServerState) {
	t.Helper()
	state := &MockServerState{}

	if opts.Agent == "" {
		opts.Agent = "test"
	}
	if opts.DoneAfterPolls == 0 {
		opts.DoneAfterPolls = 2
	}
	jobIDStart := opts.JobIDStart
	if jobIDStart <= 0 {
		jobIDStart = 1
	}

	h := &mockServerHandler{
		opts:  opts,
		state: state,
		jobID: jobIDStart - 1,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/enqueue", h.handleEnqueue)
	mux.HandleFunc("/api/jobs", h.handleJobs)
	mux.HandleFunc("/api/review", h.handleReview)
	mux.HandleFunc("/api/comment", h.handleComment)
	mux.HandleFunc("/api/review/address", h.handleAddress)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, state
}
