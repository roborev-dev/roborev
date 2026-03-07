package main

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
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

func TestGitTestEnvStripsPropagation(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/some/path")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.autocrlf=true'")

	env := gitTestEnv(t.TempDir())
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") ||
			strings.HasPrefix(entry, "GIT_CONFIG_KEY_0=") ||
			strings.HasPrefix(entry, "GIT_CONFIG_VALUE_0=") ||
			strings.HasPrefix(entry, "GIT_CONFIG_PARAMETERS=") {
			t.Errorf("gitTestEnv leaked config propagation variable: %s", entry)
		}
	}
}

// GitTestRepo encapsulates a temporary git repository for tests.
type GitTestRepo struct {
	Dir string
	t   *testing.T
}

func gitTestEnv(dir string) []string {
	overrides := []struct {
		key   string
		value string
	}{
		{"HOME", dir},
		{"GIT_CONFIG_GLOBAL", filepath.Join(dir, ".gitconfig")},
		{"GIT_CONFIG_NOSYSTEM", "1"},
		{"GIT_AUTHOR_NAME", "Test"},
		{"GIT_AUTHOR_EMAIL", "test@test.com"},
		{"GIT_COMMITTER_NAME", "Test"},
		{"GIT_COMMITTER_EMAIL", "test@test.com"},
	}
	skip := make(map[string]struct{}, len(overrides))
	for _, override := range overrides {
		skip[strings.ToUpper(override.key)] = struct{}{}
	}

	env := os.Environ()
	filtered := make([]string, 0, len(env)+len(overrides))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if _, ok := skip[upperKey]; ok {
			continue
		}
		if upperKey == "GIT_CONFIG_PARAMETERS" || upperKey == "GIT_CONFIG_COUNT" || strings.HasPrefix(upperKey, "GIT_CONFIG_KEY_") || strings.HasPrefix(upperKey, "GIT_CONFIG_VALUE_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	for _, override := range overrides {
		filtered = append(filtered, override.key+"="+override.value)
	}
	return filtered
}

// NewGitTestRepo creates a new temporary git repository.
func NewGitTestRepo(t *testing.T) *GitTestRepo {
	t.Helper()
	dir := t.TempDir()
	r := &GitTestRepo{Dir: dir, t: t}
	r.Run("init")
	r.Run("symbolic-ref", "HEAD", "refs/heads/main")
	r.Run("config", "core.hooksPath", os.DevNull)
	r.Run("config", "user.email", "test@test.com")
	r.Run("config", "user.name", "Test")
	return r
}

// Run executes a git command in the repository.
func (r *GitTestRepo) Run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = gitTestEnv(r.Dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// CommitFile writes a file and commits it.
func (r *GitTestRepo) CommitFile(name, content, msg string) string {
	r.t.Helper()
	path := filepath.Join(r.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		r.t.Fatal(err)
	}
	r.Run("add", name)
	r.Run("commit", "-m", msg)
	return r.Run("rev-parse", "HEAD")
}

// MockDaemon encapsulates a mock daemon server and its state.
type MockDaemon struct {
	Server *httptest.Server
	State  *mockRefineState
	hooks  MockRefineHooks
	t      *testing.T
}

func registerRoute(mux *http.ServeMux, path, method string, state *mockRefineState, hook func(http.ResponseWriter, *http.Request, *mockRefineState) bool, handler func(http.ResponseWriter, *http.Request)) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			mockMethodNotAllowed(w)
			return
		}
		if hook != nil && hook(w, r, state) {
			return
		}
		handler(w, r)
	})
}

// NewMockDaemon creates a new mock daemon.
func NewMockDaemon(t *testing.T, hooks MockRefineHooks) *MockDaemon {
	t.Helper()
	state := newMockRefineState()

	mux := http.NewServeMux()

	registerRoute(mux, "/api/jobs", http.MethodGet, state, hooks.OnGetJobs, state.handleJobs)
	registerRoute(mux, "/api/enqueue", http.MethodPost, state, hooks.OnEnqueue, state.handleEnqueue)
	registerRoute(mux, "/api/review", http.MethodGet, state, hooks.OnReview, state.handleReview)
	registerRoute(mux, "/api/comments", http.MethodGet, state, hooks.OnComments, state.handleComments)
	registerRoute(mux, "/api/status", http.MethodGet, state, hooks.OnStatus, state.handleStatus)
	registerRoute(mux, "/api/ping", http.MethodGet, state, hooks.OnPing, state.handlePing)
	registerRoute(mux, "/api/comment", http.MethodPost, state, hooks.OnComment, state.handleComment)
	registerRoute(mux, "/api/review/close", http.MethodPost, state, hooks.OnReviewClose, state.handleReviewClose)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if hooks.OnUnhandled != nil && hooks.OnUnhandled(w, r, state) {
			return
		}
		http.NotFound(w, r)
	})

	ts := httptest.NewServer(mux)

	// Setup environment
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

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

	t.Cleanup(func() {
		ts.Close()
	})

	m := &MockDaemon{
		Server: ts,
		State:  state,
		hooks:  hooks,
		t:      t,
	}

	return m
}

// Close shuts down the mock daemon's HTTP server immediately.
// Full cleanup of the environment and variables is also handled automatically by t.Cleanup.
func (m *MockDaemon) Close() {
	if m.Server != nil {
		m.Server.Close()
	}
}

// functionalMockAgent is a configurable mock agent that accepts behavior as a function.
type functionalMockAgent struct {
	nameVal        string
	modelVal       string
	withModelCalls []string
	reviewFunc     func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error)
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
func (f *functionalMockAgent) WithModel(model string) agent.Agent {
	f.modelVal = model
	f.withModelCalls = append(f.withModelCalls, model)
	return f
}
func (f *functionalMockAgent) CommandLine() string { return "" }

// MockRefineHooks allows overriding specific endpoints in the mock refine handler.
type MockRefineHooks struct {
	OnGetJobs     func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool // Return true if handled
	OnEnqueue     func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnReview      func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnComments    func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnPing        func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnStatus      func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnComment     func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnReviewClose func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnUnhandled   func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
}

// mockRefineState tracks state for simulating the full refine loop
type mockRefineState struct {
	mu            sync.Mutex
	reviews       map[string]*storage.Review   // SHA -> review
	jobs          map[int64]*storage.ReviewJob // jobID -> job
	responses     map[int64][]storage.Response // jobID -> responses
	closedIDs     []int64                      // job IDs that were closed
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

// mockMethodNotAllowed writes a 405 JSON error matching daemon behavior.
func mockMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "method not allowed",
	})
}

func (state *mockRefineState) handleStatus(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": version.Version,
	})
}

func (state *mockRefineState) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mockMethodNotAllowed(w)
		return
	}
	_ = json.NewEncoder(w).Encode(daemon.PingInfo{
		Service: "roborev",
		Version: version.Version,
		PID:     os.Getpid(),
	})
}

func (state *mockRefineState) handleReview(w http.ResponseWriter, r *http.Request) {
	sha := r.URL.Query().Get("sha")
	jobIDStr := r.URL.Query().Get("job_id")

	state.mu.Lock()
	var review *storage.Review
	if sha != "" {
		review = state.reviews[sha]
	} else if jobIDStr != "" {
		var jobID int64
		_, _ = fmt.Sscanf(jobIDStr, "%d", &jobID)
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
	_ = json.NewEncoder(w).Encode(reviewCopy)
}

func (state *mockRefineState) handleComments(w http.ResponseWriter, r *http.Request) {
	jobIDStr := r.URL.Query().Get("job_id")
	var jobID int64
	_, _ = fmt.Sscanf(jobIDStr, "%d", &jobID)
	state.mu.Lock()
	// Copy slice under lock before encoding
	origResponses := state.responses[jobID]
	responses := make([]storage.Response, len(origResponses))
	copy(responses, origResponses)
	state.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"responses": responses,
	})
}

func (state *mockRefineState) handleComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID     int64  `json:"job_id"`
		Responder string `json:"responder"`
		Response  string `json:"response"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
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
	_ = json.NewEncoder(w).Encode(resp)
}

func (state *mockRefineState) handleReviewClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID  int64 `json:"job_id"`
		Closed bool  `json:"closed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	state.mu.Lock()
	if req.Closed {
		state.closedIDs = append(state.closedIDs, req.JobID)
		// Update the review in state
		for _, rev := range state.reviews {
			if rev.JobID == req.JobID {
				rev.Closed = true
				break
			}
		}
	}
	state.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (state *mockRefineState) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath string `json:"repo_path"`
		GitRef   string `json:"git_ref"`
		Agent    string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
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
	_ = json.NewEncoder(w).Encode(job)
}

func (state *mockRefineState) handleJobs(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	var jobs []storage.ReviewJob
	for _, job := range state.jobs {
		jobs = append(jobs, *job)
	}
	state.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jobs":     jobs,
		"has_more": false,
	})
}

// createMockRefineHandler creates an HTTP handler that simulates daemon behavior
func createMockRefineHandler(state *mockRefineState) http.Handler {
	mux := http.NewServeMux()
	registerRoute(mux, "/api/ping", http.MethodGet, state, nil, state.handlePing)
	registerRoute(mux, "/api/status", http.MethodGet, state, nil, state.handleStatus)
	registerRoute(mux, "/api/review", http.MethodGet, state, nil, state.handleReview)
	registerRoute(mux, "/api/comments", http.MethodGet, state, nil, state.handleComments)
	registerRoute(mux, "/api/comment", http.MethodPost, state, nil, state.handleComment)
	registerRoute(mux, "/api/review/close", http.MethodPost, state, nil, state.handleReviewClose)
	registerRoute(mux, "/api/enqueue", http.MethodPost, state, nil, state.handleEnqueue)
	registerRoute(mux, "/api/jobs", http.MethodGet, state, nil, state.handleJobs)
	return mux
}

// daemonFromHandler wraps a legacy http.Handler in a MockDaemon.
func daemonFromHandler(t *testing.T, handler http.Handler) *MockDaemon {
	t.Helper()
	delegate := func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
		handler.ServeHTTP(w, r)
		return true
	}
	return NewMockDaemon(t, MockRefineHooks{
		OnGetJobs:     delegate,
		OnEnqueue:     delegate,
		OnReview:      delegate,
		OnComments:    delegate,
		OnComment:     delegate,
		OnReviewClose: delegate,
		OnUnhandled:   delegate,
	})
}

// runWithOutput runs a cobra command with the given directory set in its context and returns its output.
func runWithOutput(t *testing.T, dir string, fn func(cmd *cobra.Command) error) (string, error) {
	t.Helper()

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	ctx := context.WithValue(context.Background(), workDirKey{}, dir)
	cmd.SetContext(ctx)

	err := fn(cmd)
	return output.String(), err
}
