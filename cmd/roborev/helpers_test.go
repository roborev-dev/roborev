package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testenv"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
// Hooks are suppressed via core.hooksPath to prevent the user's
// global post-commit hook from leaking test data into the
// production daemon.
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
	r.Run("config", "core.hooksPath", filepath.Join(resolved, ".git", "hooks"))
	return r
}

// chdir changes to dir and registers a t.Cleanup to restore the original directory.
// WARNING: This mutates global process state. Tests using this MUST NOT use t.Parallel().
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
	cmd.Env = append(os.Environ(),
		"HOME="+r.Dir,
		"XDG_CONFIG_HOME="+filepath.Join(r.Dir, ".config"),
		"GIT_CONFIG_GLOBAL="+filepath.Join(r.Dir, ".gitconfig"),
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
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
			b, err := json.Marshal(review)
			if err != nil {
				t.Errorf("failed to encode review: %v", err)
				return
			}
			w.Write(b)
			return
		}
	}))
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return receivedQuery
	}
}

// runShowCmd executes showCmd() with the given args and returns captured stdout and error.
func runShowCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := showCmd()
	cmd.SetArgs(args)
	var err error
	output := captureStdout(t, func() {
		// Suppress cobra's default error printing so it doesn't leak into tests,
		// we're capturing the returned error instead.
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		err = cmd.Execute()
	})
	return output, err
}

// newTestCmd creates a cobra.Command with output captured to the returned buffer.
func newTestCmd(t *testing.T, serverURL string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if serverURL != "" {
		ctx := context.WithValue(context.Background(), serverAddrKey{}, serverURL)
		cmd.SetContext(ctx)
	}
	return cmd, &buf
}

// MockServerState tracks counters for API calls made to a mock server.
type MockServerState struct {
	EnqueueCount atomic.Int32
	JobsCount    atomic.Int32
	ReviewCount  atomic.Int32
	CloseCount   atomic.Int32
	CommentCount atomic.Int32
}

func (s *MockServerState) Enqueues() int32 { return s.EnqueueCount.Load() }
func (s *MockServerState) Jobs() int32     { return s.JobsCount.Load() }
func (s *MockServerState) Reviews() int32  { return s.ReviewCount.Load() }
func (s *MockServerState) Closes() int32   { return s.CloseCount.Load() }
func (s *MockServerState) Comments() int32 { return s.CommentCount.Load() }

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
	// JobStatusSequence is an optional sequence of job statuses to return on successive /api/jobs polls.
	JobStatusSequence []storage.JobStatus
	// JobError is an optional error string to return in the job response.
	JobError string
	// JobNotFound, if true, simulates a missing job (returns empty jobs array).
	JobNotFound bool
	// OnEnqueue is an optional callback for /api/enqueue requests.
	OnEnqueue func(w http.ResponseWriter, r *http.Request)
	// OnJobs is an optional callback for /api/jobs requests. If set, overrides default behavior.
	OnJobs func(w http.ResponseWriter, r *http.Request)
	// OnReview is an optional callback for /api/review requests.
	OnReview func(w http.ResponseWriter, r *http.Request)
	// OnComment is an optional callback for /api/comment requests.
	OnComment func(w http.ResponseWriter, r *http.Request)
	// OnClose is an optional callback for /api/review/close requests.
	OnClose func(w http.ResponseWriter, r *http.Request)
}

type mockServerHandler struct {
	t     *testing.T
	opts  MockServerOpts
	state *MockServerState
	jobID atomic.Int64
}

func (h *mockServerHandler) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.opts.OnEnqueue != nil {
		h.state.EnqueueCount.Add(1)
		h.opts.OnEnqueue(w, r)
		return
	}
	id := h.jobID.Add(1)
	h.state.EnqueueCount.Add(1)
	b, err := json.Marshal(storage.ReviewJob{
		ID:     id,
		Agent:  h.opts.Agent,
		Status: storage.JobStatusQueued,
	})
	if err != nil {
		h.t.Errorf("failed to encode enqueue response: %v", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	w.Write(b)
}

func (h *mockServerHandler) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.opts.OnJobs != nil {
		h.state.JobsCount.Add(1)
		h.opts.OnJobs(w, r)
		return
	}
	count := h.state.JobsCount.Add(1)

	if h.opts.JobNotFound {
		b, err := json.Marshal(map[string]any{
			"jobs": []storage.ReviewJob{},
		})
		if err != nil {
			h.t.Errorf("failed to encode empty jobs response: %v", err)
			return
		}
		w.Write(b)
		return
	}

	var status storage.JobStatus
	if len(h.opts.JobStatusSequence) > 0 {
		idx := int(count) - 1
		if idx >= len(h.opts.JobStatusSequence) {
			idx = len(h.opts.JobStatusSequence) - 1
		}
		status = h.opts.JobStatusSequence[idx]
	} else {
		status = storage.JobStatusQueued
		if count >= h.opts.DoneAfterPolls {
			status = storage.JobStatusDone
		}
	}

	b, err := json.Marshal(map[string]any{
		"jobs": []storage.ReviewJob{{
			ID:     h.jobID.Load(),
			Status: status,
			Error:  h.opts.JobError,
		}},
	})
	if err != nil {
		h.t.Errorf("failed to encode jobs response: %v", err)
		return
	}
	w.Write(b)
}

func (h *mockServerHandler) handleReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.opts.OnReview != nil {
		h.state.ReviewCount.Add(1)
		h.opts.OnReview(w, r)
		return
	}
	h.state.ReviewCount.Add(1)
	output := h.opts.ReviewOutput
	if output == "" {
		output = "review output"
	}
	b, err := json.Marshal(storage.Review{
		JobID:  h.jobID.Load(),
		Agent:  h.opts.Agent,
		Output: output,
	})
	if err != nil {
		h.t.Errorf("failed to encode review response: %v", err)
		return
	}
	w.Write(b)
}

func (h *mockServerHandler) handleComment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.opts.OnComment != nil {
		h.state.CommentCount.Add(1)
		h.opts.OnComment(w, r)
		return
	}
	h.state.CommentCount.Add(1)
	w.WriteHeader(http.StatusCreated)
}

func (h *mockServerHandler) handleClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h.state.CloseCount.Add(1)
	if h.opts.OnClose != nil {
		h.opts.OnClose(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// newMockServer creates an httptest.Server that mimics the roborev daemon API.
// It handles /api/enqueue, /api/jobs, /api/review, and /api/review/close.
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
		t:     t,
		opts:  opts,
		state: state,
	}
	h.jobID.Store(jobIDStart - 1)

	mux := http.NewServeMux()

	mux.HandleFunc("/api/enqueue", h.handleEnqueue)
	mux.HandleFunc("/api/jobs", h.handleJobs)
	mux.HandleFunc("/api/review", h.handleReview)
	mux.HandleFunc("/api/comment", h.handleComment)
	mux.HandleFunc("/api/review/close", h.handleClose)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, state
}

func assertContains(t *testing.T, s, substr string, format string, args ...any) {
	t.Helper()
	if !strings.Contains(s, substr) {
		msg := fmt.Sprintf(format, args...)
		t.Errorf("%s: expected string to contain %q\nDocument content:\n%s", msg, substr, s)
	}
}

func assertEqual[T any](t *testing.T, want, got T, msg string) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%s: want %+v, got %+v", msg, want, got)
	}
}

func TestTestGitRepo_Run_Isolation(t *testing.T) {
	fakeHome := t.TempDir()

	badConfig := filepath.Join(fakeHome, ".gitconfig")
	if err := os.WriteFile(badConfig, []byte("[user]\n\tname = Malicious Global\n\temail = bad@global.com\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", fakeHome)
	t.Setenv("GIT_CONFIG_GLOBAL", badConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")

	r := newTestGitRepo(t)

	// 1. Verify HOME isolation: --global should write to r.Dir, not fakeHome
	r.Run("config", "--global", "test.isolated", "true")

	content, err := os.ReadFile(r.Dir + "/.gitconfig")
	if err != nil {
		t.Fatalf("HOME was not isolated properly: %v", err)
	}
	if !strings.Contains(string(content), "isolated = true") {
		t.Errorf("Expected config in repo dir, got: %s", content)
	}

	// 2. Verify GIT_CONFIG_NOSYSTEM is 1 despite inherited 0
	r.Run("config", "alias.echoenv", "!echo $GIT_CONFIG_NOSYSTEM")
	out := r.Run("echoenv")
	if out != "1" {
		t.Errorf("Expected GIT_CONFIG_NOSYSTEM=1, got: %q", out)
	}

	// 3. Verify it didn't pick up the global config from the malicious GIT_CONFIG_GLOBAL
	cmd := exec.Command("git", "config", "--global", "user.name")
	cmd.Dir = r.Dir
	cmd.Env = testenv.BuildIsolatedGitEnv(os.Environ(), r.Dir)
	globalOut, err := cmd.CombinedOutput()

	// The command should actually fail because the mock global config in r.Dir
	// doesn't have user.name. If it prints anything, it shouldn't be the malicious value.
	if err == nil || strings.Contains(string(globalOut), "Malicious Global") {
		t.Errorf("isolation failed: git picked up global config from original environment: %s", string(globalOut))
	}
}
