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

// GitTestRepo encapsulates a temporary git repository for tests.
type GitTestRepo struct {
	Dir string
	t   *testing.T
}

// NewGitTestRepo creates a new temporary git repository.
func NewGitTestRepo(t *testing.T) *GitTestRepo {
	t.Helper()
	dir := t.TempDir()
	r := &GitTestRepo{Dir: dir, t: t}
	r.Run("init")
	r.Run("symbolic-ref", "HEAD", "refs/heads/main")
	r.Run("config", "user.email", "test@test.com")
	r.Run("config", "user.name", "Test")
	return r
}

// Run executes a git command in the repository.
func (r *GitTestRepo) Run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
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

// NewMockDaemon creates a new mock daemon.
func NewMockDaemon(t *testing.T, hooks MockRefineHooks) *MockDaemon {
	t.Helper()
	state := newMockRefineState()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mockMethodNotAllowed(w)
			return
		}
		if hooks.OnGetJobs != nil && hooks.OnGetJobs(w, r, state) {
			return
		}
		state.handleJobs(w, r)
	})

	mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			mockMethodNotAllowed(w)
			return
		}
		if hooks.OnEnqueue != nil && hooks.OnEnqueue(w, r, state) {
			return
		}
		state.handleEnqueue(w, r)
	})

	mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mockMethodNotAllowed(w)
			return
		}
		if hooks.OnReview != nil && hooks.OnReview(w, r, state) {
			return
		}
		state.handleReview(w, r)
	})

	mux.HandleFunc("/api/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mockMethodNotAllowed(w)
			return
		}
		if hooks.OnComments != nil && hooks.OnComments(w, r, state) {
			return
		}
		state.handleComments(w, r)
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mockMethodNotAllowed(w)
			return
		}
		if hooks.OnStatus != nil && hooks.OnStatus(w, r, state) {
			return
		}
		state.handleStatus(w, r)
	})

	mux.HandleFunc("/api/comment", func(w http.ResponseWriter, r *http.Request) {
		if hooks.OnComment != nil && hooks.OnComment(w, r, state) {
			return
		}
		state.handleComment(w, r)
	})

	mux.HandleFunc("/api/review/address", func(w http.ResponseWriter, r *http.Request) {
		if hooks.OnReviewAddress != nil && hooks.OnReviewAddress(w, r, state) {
			return
		}
		state.handleReviewAddress(w, r)
	})

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

	origServerAddr := serverAddr
	serverAddr = ts.URL

	t.Cleanup(func() {
		ts.Close()
		serverAddr = origServerAddr
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
func (f *functionalMockAgent) CommandLine() string                                  { return "" }

// MockRefineHooks allows overriding specific endpoints in the mock refine handler.
type MockRefineHooks struct {
	OnGetJobs       func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool // Return true if handled
	OnEnqueue       func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnReview        func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnComments      func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnStatus        func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnComment       func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnReviewAddress func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
	OnUnhandled     func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool
}

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

// mockMethodNotAllowed writes a 405 JSON error matching daemon behavior.
func mockMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "method not allowed",
	})
}

func (state *mockRefineState) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mockMethodNotAllowed(w)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": version.Version,
	})
}

func (state *mockRefineState) handleReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mockMethodNotAllowed(w)
		return
	}
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
	if r.Method != http.MethodGet {
		mockMethodNotAllowed(w)
		return
	}
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
	if r.Method != http.MethodPost {
		mockMethodNotAllowed(w)
		return
	}
	var req struct {
		JobID     int64  `json:"job_id"`
		Responder string `json:"responder"`
		Response  string `json:"response"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
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

func (state *mockRefineState) handleReviewAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mockMethodNotAllowed(w)
		return
	}
	var req struct {
		ReviewID  int64 `json:"review_id"`
		Addressed bool  `json:"addressed"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
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
}

func (state *mockRefineState) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mockMethodNotAllowed(w)
		return
	}
	var req struct {
		RepoPath string `json:"repo_path"`
		GitRef   string `json:"git_ref"`
		Agent    string `json:"agent"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
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
	if r.Method != http.MethodGet {
		mockMethodNotAllowed(w)
		return
	}
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
	mux.HandleFunc("/api/status", state.handleStatus)
	mux.HandleFunc("/api/review", state.handleReview)
	mux.HandleFunc("/api/comments", state.handleComments)
	mux.HandleFunc("/api/comment", state.handleComment)
	mux.HandleFunc("/api/review/address", state.handleReviewAddress)
	mux.HandleFunc("/api/enqueue", state.handleEnqueue)
	mux.HandleFunc("/api/jobs", state.handleJobs)
	return mux
}

// daemonFromHandler wraps a legacy http.Handler in a MockDaemon.
func daemonFromHandler(t *testing.T, handler http.Handler) *MockDaemon {
	t.Helper()
	return NewMockDaemon(t, MockRefineHooks{
		OnGetJobs: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			handler.ServeHTTP(w, r)
			return true
		},
		OnEnqueue: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			handler.ServeHTTP(w, r)
			return true
		},
		OnReview: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			handler.ServeHTTP(w, r)
			return true
		},
		OnComments: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			handler.ServeHTTP(w, r)
			return true
		},
		OnComment: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			handler.ServeHTTP(w, r)
			return true
		},
		OnReviewAddress: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			handler.ServeHTTP(w, r)
			return true
		},
		OnUnhandled: func(w http.ResponseWriter, r *http.Request, state *mockRefineState) bool {
			handler.ServeHTTP(w, r)
			return true
		},
	})
}

var chdirMutex sync.Mutex

// runWithOutput runs a cobra command within a specific directory and returns its output.
func runWithOutput(t *testing.T, dir string, fn func(cmd *cobra.Command) error) (string, error) {
	t.Helper()

	chdirMutex.Lock()
	defer chdirMutex.Unlock()

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current directory: %v", err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to change directory to %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Errorf("failed to restore directory to %s: %v", oldWd, err)
		}
	}()

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	err = fn(cmd)
	return output.String(), err
}
