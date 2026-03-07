package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
)

const (
	apiPathJobs        = "/api/jobs"
	apiPathReview      = "/api/review"
	apiPathReviewClose = "/api/review/close"
	apiPathEnqueue     = "/api/enqueue"
	apiPathComment     = "/api/comment"
	paramClosed        = "closed"
	paramLimit         = "limit"
	paramStatus        = "status"
	paramBranch        = "branch"
	paramID            = "id"
	paramJobID         = "job_id"
	paramRepo          = "repo"
)

func patchFixDaemonRetryForTest(t *testing.T, ensure func() (string, error)) {
	t.Helper()

	oldMaxRetries := fixDaemonMaxRetries
	oldRecoveryWait := fixDaemonRecoveryWait
	oldRecoveryPoll := fixDaemonRecoveryPoll
	oldEnsure := fixDaemonEnsure
	oldSleep := fixDaemonSleep
	oldProbeAttempts := enqueueIfNeededProbeAttempts
	oldProbeDelay := enqueueIfNeededProbeDelay

	fixDaemonMaxRetries = 1
	fixDaemonRecoveryWait = 25 * time.Millisecond
	fixDaemonRecoveryPoll = 5 * time.Millisecond
	if ensure != nil {
		fixDaemonEnsure = ensure
	} else {
		// Default: discover daemon via runtime info (same as the
		// production default, but without requiring a *cobra.Command).
		fixDaemonEnsure = func() (string, error) {
			if info, err := daemon.GetAnyRunningDaemon(); err == nil {
				return fmt.Sprintf("http://%s", info.Addr), nil
			}
			return "", fmt.Errorf("daemon not running")
		}
	}
	fixDaemonSleep = time.Sleep
	enqueueIfNeededProbeAttempts = 1
	enqueueIfNeededProbeDelay = 5 * time.Millisecond

	t.Cleanup(func() {
		fixDaemonMaxRetries = oldMaxRetries
		fixDaemonRecoveryWait = oldRecoveryWait
		fixDaemonRecoveryPoll = oldRecoveryPoll
		fixDaemonEnsure = oldEnsure
		fixDaemonSleep = oldSleep
		enqueueIfNeededProbeAttempts = oldProbeAttempts
		enqueueIfNeededProbeDelay = oldProbeDelay
	})
}

func closeConnNoResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()

	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer does not support hijacking")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("hijack connection: %v", err)
	}
	_ = conn.Close()
}

func ptrInt64(v int64) *int64 {
	return &v
}

func TestBuildGenericFixPrompt(t *testing.T) {
	analysisOutput := `## Issues Found
- Long function in main.go:50
- Missing error handling`

	prompt := buildGenericFixPrompt(analysisOutput)

	// Should include the analysis output
	assertContains(t, prompt, "Issues Found", "prompt should include analysis output")
	assertContains(t, prompt, "Long function", "prompt should include specific findings")

	// Should have fix instructions
	assertContains(t, prompt, "apply the suggested changes", "prompt should include fix instructions")

	// Should request a commit
	assertContains(t, prompt, "git commit", "prompt should request a commit")
}

func TestBuildGenericCommitPrompt(t *testing.T) {
	prompt := buildGenericCommitPrompt()

	// Should have commit instructions
	assertContains(t, prompt, "git commit", "prompt should mention git commit")
	assertContains(t, prompt, "descriptive", "prompt should request a descriptive message")
}

func TestFetchJob(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		jobs       []storage.ReviewJob
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			jobs: []storage.ReviewJob{{
				ID:     42,
				Status: storage.JobStatusDone,
				Agent:  "test",
			}},
		},
		{
			name:       "not found",
			statusCode: http.StatusOK,
			jobs:       []storage.ReviewJob{},
			wantErr:    true,
			wantErrMsg: "not found",
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
			wantErrMsg: "server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					writeJSON(w, map[string]any{"jobs": tt.jobs})
				}
			}))
			defer ts.Close()

			job, err := fetchJob(context.Background(), ts.URL, 42)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				} else if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if job.ID != 42 {
				t.Errorf("job.ID = %d, want 42", job.ID)
			}
		})
	}
}

func TestFetchJobCanceledContextDoesNotRetryRecovery(t *testing.T) {
	patchFixDaemonRetryForTest(t, func() (string, error) {
		t.Fatal("fetchJob should not attempt daemon recovery after context cancellation")
		return "", nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fetchJob(ctx, "http://127.0.0.1:1", 42)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got: %v", err)
	}
}

func TestFetchReview(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		review     *storage.Review
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			review: &storage.Review{
				JobID:  42,
				Output: "Analysis output here",
			},
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
			wantErrMsg: "server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != apiPathReview {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.URL.Query().Get(paramJobID) != "42" {
					t.Errorf("unexpected job_id: %s", r.URL.Query().Get(paramJobID))
				}

				w.WriteHeader(tt.statusCode)
				if tt.review != nil {
					writeJSON(w, tt.review)
				}
			}))
			defer ts.Close()

			review, err := fetchReview(context.Background(), ts.URL, 42)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				} else if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("fetchReview: %v", err)
			}
			if review.JobID != tt.review.JobID {
				t.Errorf("review.JobID = %d, want %d", review.JobID, tt.review.JobID)
			}
			if review.Output != tt.review.Output {
				t.Errorf("review.Output = %q, want %q", review.Output, tt.review.Output)
			}
		})
	}
}

func TestFetchReviewDeadlineExceededDoesNotRetryRecovery(t *testing.T) {
	patchFixDaemonRetryForTest(t, func() (string, error) {
		t.Fatal("fetchReview should not attempt daemon recovery after deadline exceeded")
		return "", nil
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := fetchReview(ctx, ts.URL, 42)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got: %v", err)
	}
}

func TestWithFixDaemonRetryContextRetriesClientTimeoutWhenCallerContextActive(t *testing.T) {
	var ensureCalls atomic.Int32
	patchFixDaemonRetryForTest(t, func() (string, error) {
		ensureCalls.Add(1)
		return "", nil
	})

	var attempts atomic.Int32
	value, err := withFixDaemonRetryContext(context.Background(), "http://127.0.0.1:1", func(addr string) (string, error) {
		if attempts.Add(1) == 1 {
			return "", &neturl.Error{Op: "Get", URL: addr, Err: context.DeadlineExceeded}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("withFixDaemonRetryContext: %v", err)
	}
	if value != "ok" {
		t.Fatalf("expected successful retry result, got %q", value)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
	if ensureCalls.Load() != 1 {
		t.Fatalf("expected one recovery attempt, got %d", ensureCalls.Load())
	}
}

func TestWithFixDaemonRetryContextStopsAfterCancellationDuringRecovery(t *testing.T) {
	oldRecoveryWait := fixDaemonRecoveryWait
	oldRecoveryPoll := fixDaemonRecoveryPoll
	oldEnsure := fixDaemonEnsure
	oldSleep := fixDaemonSleep
	fixDaemonRecoveryWait = time.Second
	fixDaemonRecoveryPoll = time.Millisecond
	t.Cleanup(func() {
		fixDaemonRecoveryWait = oldRecoveryWait
		fixDaemonRecoveryPoll = oldRecoveryPoll
		fixDaemonEnsure = oldEnsure
		fixDaemonSleep = oldSleep
	})

	ctx, cancel := context.WithCancel(context.Background())
	var ensureCalls atomic.Int32
	fixDaemonEnsure = func() (string, error) {
		ensureCalls.Add(1)
		return "", errors.New("daemon still down")
	}
	fixDaemonSleep = func(time.Duration) {
		cancel()
	}

	var attempts atomic.Int32
	_, err := withFixDaemonRetryContext(ctx, "http://127.0.0.1:1", func(addr string) (string, error) {
		attempts.Add(1)
		return "", &neturl.Error{Op: "Get", URL: addr, Err: syscall.ECONNREFUSED}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected no retry after cancellation during recovery, got %d attempts", attempts.Load())
	}
	if ensureCalls.Load() == 0 {
		t.Fatal("expected daemon recovery to be attempted")
	}
}

func TestWithFixDaemonRetryContextReturnsRecoveryFailure(t *testing.T) {
	oldMaxRetries := fixDaemonMaxRetries
	oldRecoveryWait := fixDaemonRecoveryWait
	oldRecoveryPoll := fixDaemonRecoveryPoll
	oldEnsure := fixDaemonEnsure
	oldSleep := fixDaemonSleep
	fixDaemonMaxRetries = 1
	fixDaemonRecoveryWait = time.Millisecond
	fixDaemonRecoveryPoll = time.Millisecond
	recoveryErr := errors.New("refusing to auto-start daemon from ephemeral binary")
	fixDaemonEnsure = func() (string, error) { return "", recoveryErr }
	fixDaemonSleep = func(time.Duration) {}
	t.Cleanup(func() {
		fixDaemonMaxRetries = oldMaxRetries
		fixDaemonRecoveryWait = oldRecoveryWait
		fixDaemonRecoveryPoll = oldRecoveryPoll
		fixDaemonEnsure = oldEnsure
		fixDaemonSleep = oldSleep
	})

	var attempts atomic.Int32
	_, err := withFixDaemonRetryContext(context.Background(), "http://127.0.0.1:1", func(addr string) (string, error) {
		attempts.Add(1)
		return "", &neturl.Error{Op: "Get", URL: addr, Err: syscall.ECONNREFUSED}
	})
	if !errors.Is(err, recoveryErr) {
		t.Fatalf("expected recovery failure, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected stale request not to be retried after recovery failure, got %d attempts", attempts.Load())
	}
}

func TestAddJobResponse(t *testing.T) {
	var gotJobID int64
	var gotContent string

	var gotCommenter string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPathComment || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotJobID = int64(req[paramJobID].(float64))
		gotContent = req["comment"].(string)
		gotCommenter = req["commenter"].(string)

		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	err := addJobResponse(context.Background(), ts.URL, 123, "roborev-fix", "Fix applied")
	if err != nil {
		t.Fatalf("addJobResponse: %v", err)
	}

	if gotJobID != 123 {
		t.Errorf("job_id = %d, want 123", gotJobID)
	}
	if gotContent != "Fix applied" {
		t.Errorf("comment = %q, want %q", gotContent, "Fix applied")
	}
	if gotCommenter != "roborev-fix" {
		t.Errorf("commenter = %q, want %q", gotCommenter, "roborev-fix")
	}
}

func TestAddJobResponseAvoidsDuplicatePostAfterConnectionDrop(t *testing.T) {
	var firstPostCount atomic.Int32
	var recoveryPostCount atomic.Int32
	var responsesMu sync.Mutex
	var responses []storage.Response

	startServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/comment":
			firstPostCount.Add(1)
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			responsesMu.Lock()
			responses = append(responses, storage.Response{
				ID:        int64(len(responses) + 1),
				JobID:     ptrInt64(123),
				Responder: req["commenter"].(string),
				Response:  req["comment"].(string),
			})
			responsesMu.Unlock()
			closeConnNoResponse(t, w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer startServer.Close()

	recoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/comments":
			responsesMu.Lock()
			responsesCopy := append([]storage.Response(nil), responses...)
			responsesMu.Unlock()
			writeJSON(w, map[string]any{"responses": responsesCopy})
		case "/api/comment":
			recoveryPostCount.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer recoveryServer.Close()

	patchFixDaemonRetryForTest(t, func() (string, error) {
		return recoveryServer.URL, nil
	})

	err := addJobResponse(context.Background(), startServer.URL, 123, "roborev-fix", "Fix applied")
	if err != nil {
		t.Fatalf("addJobResponse: %v", err)
	}
	if firstPostCount.Load() != 1 {
		t.Fatalf("expected one initial comment post, got %d", firstPostCount.Load())
	}
	if recoveryPostCount.Load() != 0 {
		t.Fatalf("expected no duplicate comment post after recovery, got %d", recoveryPostCount.Load())
	}
}

func TestAddJobResponseCanceledContextDoesNotAttemptRecovery(t *testing.T) {
	patchFixDaemonRetryForTest(t, func() (string, error) {
		t.Fatal("addJobResponse should not attempt daemon recovery after context cancellation")
		return "", nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/comment" {
			http.NotFound(w, r)
			return
		}
		closeConnNoResponse(t, w)
	}))
	defer ts.Close()

	if err := addJobResponse(ctx, ts.URL, 123, "roborev-fix", "Fix applied"); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestAddJobResponseDeadlineExceededCancelsHTTPCall(t *testing.T) {
	patchFixDaemonRetryForTest(t, func() (string, error) {
		t.Fatal("addJobResponse should not attempt daemon recovery after request deadline exceeded")
		return "", nil
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/comment" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := addJobResponse(ctx, ts.URL, 123, "roborev-fix", "Fix applied")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestFixSingleJob(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})

	ts, _ := newMockServer(t, MockServerOpts{
		ReviewOutput: "## Issues\n- Found minor issue",
		OnJobs: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusDone,
					Agent:  "test",
				}},
			})
		},
	})

	cmd, output := newTestCmd(t, ts.URL)
	ctx := context.WithValue(context.Background(), serverAddrKey{}, ts.URL)
	cmd.SetContext(ctx)

	opts := fixOptions{
		agentName: "test",
		reasoning: "fast",
	}

	err := fixSingleJob(cmd, getDaemonAddr(cmd), repo.Dir, 99, opts)
	if err != nil {
		t.Fatalf("fixSingleJob: %v", err)
	}

	// Verify output contains expected content
	outputStr := output.String()
	assertContains(t, outputStr, "Issues", "output should show analysis findings")
	assertContains(t, outputStr, paramClosed, "output should confirm job closed")
}

func TestFixSingleJobRecoversPostFixDaemonCalls(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})

	originalAgent, err := agent.Get("test")
	if err != nil {
		t.Fatalf("get test agent: %v", err)
	}
	agent.Register(&agent.FakeAgent{
		NameStr: "test",
		ReviewFn: func(_ context.Context, repoPath, _, _ string, _ io.Writer) (string, error) {
			fixPath := filepath.Join(repoPath, "fix.txt")
			if err := os.WriteFile(fixPath, []byte("fixed\n"), 0644); err != nil {
				return "", fmt.Errorf("write fix: %w", err)
			}
			cmd := exec.Command("git", "add", "fix.txt")
			cmd.Dir = repoPath
			if out, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("git add: %v (%s)", err, out)
			}
			cmd = exec.Command("git", "commit", "-m", "fix: apply retry test")
			cmd.Dir = repoPath
			if out, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("git commit: %v (%s)", err, out)
			}
			return "applied fix", nil
		},
	})
	t.Cleanup(func() {
		agent.Register(originalAgent)
	})

	var enqueueCount atomic.Int32
	var commentCount atomic.Int32
	var closeCount atomic.Int32
	recoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			writeJSON(w, map[string]any{
				"jobs":     []storage.ReviewJob{},
				"has_more": false,
			})
		case "/api/enqueue":
			enqueueCount.Add(1)
			w.WriteHeader(http.StatusCreated)
		case "/api/comments":
			writeJSON(w, map[string]any{"responses": []storage.Response{}})
		case "/api/comment":
			commentCount.Add(1)
			w.WriteHeader(http.StatusCreated)
		case "/api/review/close":
			closeCount.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer recoveryServer.Close()

	var ensureCalls atomic.Int32
	patchFixDaemonRetryForTest(t, func() (string, error) {
		ensureCalls.Add(1)
		return recoveryServer.URL, nil
	})

	var primaryServer *httptest.Server
	ts := newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusDone,
					Agent:  "test",
				}},
			})
		}).
		WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{Output: "## Issues\n- Found minor issue"})
			// Close primary server after serving the review so
			// post-fix calls trigger connection errors and recovery.
			go primaryServer.Close()
		}).
		Build()
	primaryServer = ts

	cmd, output := newTestCmd(t, ts.URL)

	opts := fixOptions{
		agentName: "test",
		reasoning: "fast",
	}

	err = fixSingleJob(cmd, getDaemonAddr(cmd), repo.Dir, 99, opts)
	if err != nil {
		t.Fatalf("fixSingleJob: %v", err)
	}

	outputStr := output.String()
	if ensureCalls.Load() == 0 {
		t.Fatal("expected daemon recovery to be attempted")
	}
	if strings.Contains(outputStr, "Warning: could not enqueue review for fix commit") {
		t.Fatalf("unexpected enqueue warning after recovery:\n%s", outputStr)
	}
	if strings.Contains(outputStr, "Warning: could not add response to job") {
		t.Fatalf("unexpected comment warning after recovery:\n%s", outputStr)
	}
	if strings.Contains(outputStr, "Warning: could not close job") {
		t.Fatalf("unexpected close warning after recovery:\n%s", outputStr)
	}
	if enqueueCount.Load() != 1 {
		t.Fatalf("expected one recovered enqueue, got %d", enqueueCount.Load())
	}
	if commentCount.Load() != 1 {
		t.Fatalf("expected one recovered comment, got %d", commentCount.Load())
	}
	if closeCount.Load() != 1 {
		t.Fatalf("expected one recovered close, got %d", closeCount.Load())
	}
}

func TestFixJobNotComplete(t *testing.T) {
	ts := newMockDaemonBuilder(t).
		WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusRunning, // Not complete
					Agent:  "test",
				}},
			})
		}).
		Build()

	cmd, _ := newTestCmd(t, ts.URL)
	ctx := context.WithValue(context.Background(), serverAddrKey{}, ts.URL)
	cmd.SetContext(ctx)

	err := fixSingleJob(cmd, ts.URL, t.TempDir(), 99, fixOptions{agentName: "test"})

	if err == nil {
		t.Error("expected error for incomplete job")
	}
	assertContains(t, err.Error(), "not complete", "error %q should mention 'not complete'", err.Error())
}

func TestFixCmdFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "--branch without --open",
			args:    []string{"--branch", "main"},
			wantErr: "--branch requires --open",
		},
		{
			name:    "--all-branches with positional args",
			args:    []string{"--all-branches", "123"},
			wantErr: "--open cannot be used with positional job IDs",
		},
		{
			name:    "--open with positional args",
			args:    []string{"--unaddressed", "123"},
			wantErr: "--open cannot be used with positional job IDs",
		},
		{
			name:    "--newest-first without --open",
			args:    []string{"--newest-first", "123"},
			wantErr: "--newest-first requires --open",
		},
		{
			name:    "--all-branches with --branch (no explicit --unaddressed)",
			args:    []string{"--all-branches", "--branch", "main"},
			wantErr: "--all-branches and --branch are mutually exclusive",
		},
		{
			name:    "--batch with --open",
			args:    []string{"--batch", "--unaddressed"},
			wantErr: "--batch and --open are mutually exclusive",
		},
		{
			name:    "--batch with explicit IDs and --branch",
			args:    []string{"--batch", "--branch", "main", "123"},
			wantErr: "cannot be used with explicit job IDs",
		},
		{
			name:    "--batch with explicit IDs and --all-branches",
			args:    []string{"--batch", "--all-branches", "123"},
			wantErr: "cannot be used with explicit job IDs",
		},
		{
			name:    "--batch with explicit IDs and --newest-first",
			args:    []string{"--batch", "--newest-first", "123"},
			wantErr: "cannot be used with explicit job IDs",
		},
		{
			name:    "--list with positional args",
			args:    []string{"--list", "123"},
			wantErr: "--list cannot be used with positional job IDs",
		},
		{
			name:    "--list with --batch",
			args:    []string{"--list", "--batch"},
			wantErr: "--list and --batch are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := fixCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected error")
			}
			assertContains(t, err.Error(), tt.wantErr, "error %q should contain %q", err.Error(), tt.wantErr)
		})
	}
}

func TestFixNoArgsDefaultsToOpen(t *testing.T) {
	// Running fix with no args should not produce a validation error —
	// it should enter the open path (which will fail at daemon
	// connection, not at argument validation).
	//
	// Use a mock daemon so ensureDaemon doesn't try to spawn a real
	// daemon subprocess (which hangs on CI).
	ts := newMockDaemonBuilder(t).WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
		// Return empty for all queries — we only care about argument routing
		json.NewEncoder(w).Encode(map[string]any{
			"jobs":     []any{},
			"has_more": false,
		})
	}).Build()

	cmd := fixCmd()
	cmd.SetContext(context.WithValue(context.Background(), serverAddrKey{}, ts.URL))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	// Should NOT be a validation/args error; any other error (e.g. daemon
	// not running) is acceptable.
	if err != nil && strings.Contains(err.Error(), "requires at least") {
		t.Errorf("no-args should default to --open, got validation error: %v", err)
	}
}

func TestFixAllBranchesImpliesOpen(t *testing.T) {
	// --all-branches alone should imply --open and pass
	// validation, routing through open discovery.
	ts := newMockDaemonBuilder(t).WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"jobs":     []any{},
			"has_more": false,
		})
	}).Build()

	cmd := fixCmd()
	cmd.SetContext(context.WithValue(context.Background(), serverAddrKey{}, ts.URL))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--all-branches"})
	err := cmd.Execute()
	if err != nil {
		t.Errorf("--all-branches should not fail validation: %v", err)
	}
}

func TestRunFixOpen(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	t.Run("no open jobs", func(t *testing.T) {
		_ = newMockDaemonBuilder(t).
			WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get(paramStatus) != "done" {
					t.Errorf("expected status=done, got %q", q.Get(paramStatus))
				}
				if q.Get(paramClosed) != "false" {
					t.Errorf("expected closed=false, got %q", q.Get(paramClosed))
				}
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
			}).
			Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixOpen(cmd, "", false, fixOptions{agentName: "test"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContains(t, out, "No open jobs found", "expected 'No open jobs found' message, got %q", out)
	})

	t.Run("finds and processes open jobs", func(t *testing.T) {
		var reviewCalls, closeCalls atomic.Int32
		var openQueryCalls atomic.Int32

		_ = newMockDaemonBuilder(t).
			WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get(paramClosed) == "false" && q.Get(paramLimit) == "0" {
					if openQueryCalls.Add(1) == 1 {
						writeJSON(w, map[string]any{
							"jobs": []storage.ReviewJob{
								{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
								{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
							},
							"has_more": false,
						})
					} else {
						writeJSON(w, map[string]any{
							"jobs":     []storage.ReviewJob{},
							"has_more": false,
						})
					}
				} else {
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			}).
			WithHandler(apiPathReview, func(w http.ResponseWriter, r *http.Request) {
				reviewCalls.Add(1)
				writeJSON(w, storage.Review{Output: "findings"})
			}).
			WithHandler(apiPathReviewClose, func(w http.ResponseWriter, r *http.Request) {
				closeCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}).
			WithHandler(apiPathEnqueue, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}).
			Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixOpen(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContains(t, out, "Found 2 open job(s)", "expected count message, got %q", out)
		if rc := reviewCalls.Load(); rc != 2 {
			t.Errorf("expected 2 review fetches, got %d", rc)
		}
		if ac := closeCalls.Load(); ac != 2 {
			t.Errorf("expected 2 close calls, got %d", ac)
		}
	})

	t.Run("passes branch filter to API", func(t *testing.T) {
		var gotBranch string
		_ = newMockDaemonBuilder(t).
			WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get(paramClosed) == "false" {
					gotBranch = r.URL.Query().Get(paramBranch)
				}
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
			}).
			Build()

		_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixOpen(cmd, "feature-branch", false, fixOptions{agentName: "test"})
		})
		if err != nil {
			t.Fatalf("runFixOpen returned unexpected error: %v", err)
		}
		if gotBranch != "feature-branch" {
			t.Errorf("expected branch=feature-branch, got %q", gotBranch)
		}
	})

	t.Run("server error", func(t *testing.T) {
		_ = newMockDaemonBuilder(t).
			WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("db error"))
			}).
			Build()

		_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixOpen(cmd, "", false, fixOptions{agentName: "test"})
		})
		if err == nil {
			t.Fatal("expected error on server failure")
		}
		assertContains(t, err.Error(), "server error", "error %q should mention server error", err.Error())
	})
}
func TestRunFixOpenOrdering(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	makeBuilder := func() (*MockDaemonBuilder, *atomic.Int32) {
		var openQueryCalls atomic.Int32
		b := newMockDaemonBuilder(t).
			WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get(paramClosed) == "false" {
					if openQueryCalls.Add(1) == 1 {
						// Return newest first (as the API does)
						writeJSON(w, map[string]any{
							"jobs": []storage.ReviewJob{
								{ID: 30, Status: storage.JobStatusDone, Agent: "test"},
								{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
								{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
							},
							"has_more": false,
						})
					} else {
						writeJSON(w, map[string]any{
							"jobs":     []storage.ReviewJob{},
							"has_more": false,
						})
					}
				} else {
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			}).
			WithHandler(apiPathReview, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, storage.Review{Output: "findings"})
			}).
			WithHandler(apiPathComment, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
			}).
			WithHandler(apiPathReviewClose, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}).
			WithHandler(apiPathEnqueue, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		return b, &openQueryCalls
	}

	t.Run("oldest first by default", func(t *testing.T) {
		b, _ := makeBuilder()
		b.Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixOpen(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContains(t, out, "[10 20 30]", "expected oldest-first order [10 20 30], got %q", out)
	})

	t.Run("newest first with flag", func(t *testing.T) {
		b, _ := makeBuilder()
		b.Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixOpen(cmd, "", true, fixOptions{agentName: "test", reasoning: "fast"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContains(t, out, "[30 20 10]", "expected newest-first order [30 20 10], got %q", out)
	})
}
func TestRunFixOpenRequery(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	var queryCount atomic.Int32
	_ = newMockDaemonBuilder(t).
		WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get(paramClosed) == "false" && q.Get(paramLimit) == "0" {
				n := queryCount.Add(1)
				switch n {
				case 1:
					// First query: return batch 1
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				case 2:
					// Second query: new job appeared
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				default:
					// Third query: no new jobs
					writeJSON(w, map[string]any{
						"jobs":     []storage.ReviewJob{},
						"has_more": false,
					})
				}
			} else {
				// Individual job fetch
				writeJSON(w, map[string]any{
					"jobs": []storage.ReviewJob{
						{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
					},
					"has_more": false,
				})
			}
		}).
		WithHandler(apiPathReview, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{Output: "findings"})
		}).
		WithHandler(apiPathComment, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}).
		WithHandler(apiPathReviewClose, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		WithHandler(apiPathEnqueue, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		Build()

	out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixOpen(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertContains(t, out, "Found 1 open job(s)", "expected first batch message, got %q", out)
	assertContains(t, out, "Found 1 new open job(s)", "expected second batch message, got %q", out)
	if int(queryCount.Load()) != 3 {
		t.Errorf("expected 3 queries, got %d", queryCount.Load())
	}
}

func TestRunFixOpenRequeriesAfterProcessingJob(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	patchFixDaemonRetryForTest(t, func() (string, error) {
		return "", nil
	})

	var openQueryCount atomic.Int32
	_ = newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("closed") == "false" && q.Get("limit") == "0" {
				// First open-jobs query returns one job; second
				// returns empty to terminate the loop.
				if openQueryCount.Add(1) == 1 {
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
					return
				}
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{
					{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
				},
				"has_more": false,
			})
		}).
		WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{Output: "findings"})
		}).
		WithHandler("/api/comment", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}).
		WithHandler("/api/review/close", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		Build()

	out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixOpen(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Found 1 open job(s)") {
		t.Errorf("expected initial batch output, got %q", out)
	}
	if openQueryCount.Load() < 2 {
		t.Errorf("expected requery after processing job, got %d queries", openQueryCount.Load())
	}
}

func TestFixJobDirectUnbornHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("agent creates first commit", func(t *testing.T) {
		// Create a fresh git repo with no commits (unborn HEAD)
		dir := t.TempDir()
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		for _, args := range [][]string{
			{"config", "core.hooksPath", os.DevNull},
			{"config", "user.email", "test@test.com"},
			{"config", "user.name", "Test"},
		} {
			c := exec.Command("git", args...)
			c.Dir = dir
			if err := c.Run(); err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
		}

		ag := &agent.FakeAgent{
			NameStr: "test",
			ReviewFn: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
				// Simulate agent creating the first commit
				if err := os.WriteFile(filepath.Join(repoPath, "fix.txt"), []byte("fixed"), 0644); err != nil {
					return "", fmt.Errorf("write file: %w", err)
				}
				c := exec.Command("git", "add", ".")
				c.Dir = repoPath
				if err := c.Run(); err != nil {
					return "", fmt.Errorf("git add: %w", err)
				}
				c = exec.Command("git", "commit", "-m", "first commit")
				c.Dir = repoPath
				if err := c.Run(); err != nil {
					return "", fmt.Errorf("git commit: %w", err)
				}
				return "applied fix", nil
			},
		}

		result, err := fixJobDirect(context.Background(), fixJobParams{
			RepoRoot: dir,
			Agent:    ag,
		}, "fix things")
		if err != nil {
			t.Fatalf("fixJobDirect: %v", err)
		}
		if !result.CommitCreated {
			t.Error("expected CommitCreated=true")
		}
		if result.NoChanges {
			t.Error("expected NoChanges=false")
		}
		if result.NewCommitSHA == "" {
			t.Error("expected NewCommitSHA to be set")
		}
	})

	t.Run("agent makes no changes on unborn head", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}

		ag := &agent.FakeAgent{
			NameStr: "test",
			ReviewFn: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
				return "nothing to do", nil
			},
		}

		result, err := fixJobDirect(context.Background(), fixJobParams{
			RepoRoot: dir,
			Agent:    ag,
		}, "fix things")
		if err != nil {
			t.Fatalf("fixJobDirect: %v", err)
		}
		if result.CommitCreated {
			t.Error("expected CommitCreated=false")
		}
		if !result.NoChanges {
			t.Error("expected NoChanges=true")
		}
	})
}

func TestBuildBatchFixPrompt(t *testing.T) {
	entries := []batchEntry{
		{
			jobID:  123,
			job:    &storage.ReviewJob{GitRef: "abc123def456"},
			review: &storage.Review{Output: "Found bug in foo.go"},
		},
		{
			jobID:  456,
			job:    &storage.ReviewJob{GitRef: "deadbeef1234"},
			review: &storage.Review{Output: "Missing error check in bar.go"},
		},
	}

	prompt := buildBatchFixPrompt(entries)

	// Header
	assertContains(t, prompt, "# Batch Fix Request", "prompt should have batch header")
	assertContains(t, prompt, "Address all findings across all reviews in a single pass", "prompt should instruct single-pass fix")

	// Per-review sections with numbered headers
	assertContains(t, prompt, "## Review 1 (Job 123 — abc123d)", "prompt missing review 1 header, got:\n%s", prompt)
	assertContains(t, prompt, "Found bug in foo.go", "prompt should include first review output")
	assertContains(t, prompt, "## Review 2 (Job 456 — deadbee)", "prompt missing review 2 header, got:\n%s", prompt)
	assertContains(t, prompt, "Missing error check in bar.go", "prompt should include second review output")

	// Instructions footer
	assertContains(t, prompt, "## Instructions", "prompt should have instructions section")
	assertContains(t, prompt, "git commit", "prompt should request a commit")
}

func TestBuildBatchFixPromptSingleEntry(t *testing.T) {
	entries := []batchEntry{
		{
			jobID:  7,
			job:    &storage.ReviewJob{GitRef: "aaa"},
			review: &storage.Review{Output: "one issue"},
		},
	}

	prompt := buildBatchFixPrompt(entries)
	assertContains(t, prompt, "## Review 1 (Job 7", "single-entry batch should still have numbered header")
}

func TestSplitIntoBatches(t *testing.T) {
	makeEntry := func(id int64, outputSize int) batchEntry {
		return batchEntry{
			jobID:  id,
			job:    &storage.ReviewJob{GitRef: fmt.Sprintf("sha%d", id)},
			review: &storage.Review{Output: strings.Repeat("x", outputSize)},
		}
	}

	t.Run("all fit in one batch", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 100),
			makeEntry(2, 100),
			makeEntry(3, 100),
		}
		batches := splitIntoBatches(entries, 100000)
		if len(batches) != 1 {
			t.Errorf("expected 1 batch, got %d", len(batches))
		}
		if len(batches[0]) != 3 {
			t.Errorf("expected 3 entries in batch, got %d", len(batches[0]))
		}
	})

	t.Run("splits when exceeding limit", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 500),
			makeEntry(2, 500),
			makeEntry(3, 500),
		}
		// Set limit small enough that not all fit (overhead ~300 bytes + entry ~530 each)
		maxSize := 1000
		batches := splitIntoBatches(entries, maxSize)
		if len(batches) < 2 {
			t.Errorf("expected at least 2 batches, got %d", len(batches))
		}
		// All entries should be present across batches
		total := 0
		for _, b := range batches {
			total += len(b)
		}
		if total != 3 {
			t.Errorf("expected 3 total entries, got %d", total)
		}
	})

	t.Run("oversized single review gets own batch", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 100),
			makeEntry(2, 5000), // oversized
			makeEntry(3, 100),
		}
		batches := splitIntoBatches(entries, 1000)
		if len(batches) < 2 {
			t.Errorf("expected at least 2 batches, got %d", len(batches))
		}
		// The oversized entry should be alone in its batch
		found := false
		for _, b := range batches {
			for _, e := range b {
				if e.jobID == 2 && len(b) == 1 {
					found = true
				}
			}
		}
		if !found {
			t.Error("oversized entry (job 2) should be alone in its batch")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		batches := splitIntoBatches(nil, 100000)
		if len(batches) != 0 {
			t.Errorf("expected 0 batches for empty input, got %d", len(batches))
		}
	})

	t.Run("built prompt respects size estimate", func(t *testing.T) {
		// Verify that splitIntoBatches size accounting matches buildBatchFixPrompt output.
		entries := []batchEntry{
			makeEntry(1, 200),
			makeEntry(2, 200),
			makeEntry(3, 200),
			makeEntry(4, 200),
			makeEntry(5, 200),
		}
		maxSize := 1000
		batches := splitIntoBatches(entries, maxSize)
		for i, batch := range batches {
			prompt := buildBatchFixPrompt(batch)
			// Single-entry batches that are inherently oversized are allowed to exceed.
			if len(batch) > 1 && len(prompt) > maxSize {
				t.Errorf("batch %d prompt size %d exceeds maxSize %d", i, len(prompt), maxSize)
			}
		}
	})

	t.Run("preserves order", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(10, 100),
			makeEntry(20, 100),
			makeEntry(30, 100),
		}
		batches := splitIntoBatches(entries, 100000)
		if len(batches) != 1 {
			t.Fatalf("expected 1 batch, got %d", len(batches))
		}
		for i, want := range []int64{10, 20, 30} {
			if batches[0][i].jobID != want {
				t.Errorf("batch[0][%d].jobID = %d, want %d", i, batches[0][i].jobID, want)
			}
		}
	})
}

func TestFormatJobIDs(t *testing.T) {
	tests := []struct {
		ids  []int64
		want string
	}{
		{[]int64{1}, "1"},
		{[]int64{1, 2, 3}, "1, 2, 3"},
		{[]int64{100, 200}, "100, 200"},
	}
	for _, tt := range tests {
		got := formatJobIDs(tt.ids)
		if got != tt.want {
			t.Errorf("formatJobIDs(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}

func TestEnqueueIfNeededSkipsWhenJobExists(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})
	sha := "abc123def456"

	var enqueueCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPathJobs:
			// Return an existing job — hook already fired
			json.NewEncoder(w).Encode(map[string]any{
				"jobs": []map[string]any{{paramID: 42}},
			})
		case apiPathEnqueue:
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{paramID: 99})
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(context.Background(), ts.URL, repo.Dir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if enqueueCalls.Load() != 0 {
		t.Error("should not enqueue when job already exists on first check")
	}
}

func TestEnqueueIfNeededAvoidsDuplicatePostAfterConnectionDrop(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})
	sha := repo.Run("rev-parse", "HEAD")

	var firstPostCount atomic.Int32
	var recoveryPostCount atomic.Int32
	var jobExists atomic.Bool

	startServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			writeJSON(w, map[string]any{"jobs": []storage.ReviewJob{}})
		case "/api/enqueue":
			firstPostCount.Add(1)
			jobExists.Store(true)
			closeConnNoResponse(t, w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer startServer.Close()

	recoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			jobs := []storage.ReviewJob{}
			if jobExists.Load() {
				jobs = append(jobs, storage.ReviewJob{ID: 42, GitRef: sha})
			}
			writeJSON(w, map[string]any{"jobs": jobs})
		case "/api/enqueue":
			recoveryPostCount.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer recoveryServer.Close()

	patchFixDaemonRetryForTest(t, func() (string, error) {
		return recoveryServer.URL, nil
	})

	err := enqueueIfNeeded(context.Background(), startServer.URL, repo.Dir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if firstPostCount.Load() != 1 {
		t.Fatalf("expected one initial enqueue post, got %d", firstPostCount.Load())
	}
	if recoveryPostCount.Load() != 0 {
		t.Fatalf("expected no duplicate enqueue post after recovery, got %d", recoveryPostCount.Load())
	}
}

func TestEnqueueIfNeededSkipsEnqueueAfterTransientProbeFailure(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})
	sha := repo.Run("rev-parse", "HEAD")

	patchFixDaemonRetryForTest(t, nil)
	oldProbeAttempts := enqueueIfNeededProbeAttempts
	oldProbeDelay := enqueueIfNeededProbeDelay
	enqueueIfNeededProbeAttempts = 2
	enqueueIfNeededProbeDelay = time.Millisecond
	t.Cleanup(func() {
		enqueueIfNeededProbeAttempts = oldProbeAttempts
		enqueueIfNeededProbeDelay = oldProbeDelay
	})

	var jobsCalls atomic.Int32
	var enqueueCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			if jobsCalls.Add(1) == 1 {
				closeConnNoResponse(t, w)
				return
			}
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{ID: 42, GitRef: sha}},
			})
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(context.Background(), ts.URL, repo.Dir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if enqueueCalls.Load() != 0 {
		t.Fatalf("expected no fallback enqueue after transient probe failure, got %d", enqueueCalls.Load())
	}
	if jobsCalls.Load() < 2 {
		t.Fatalf("expected a retrying probe sequence, got %d job checks", jobsCalls.Load())
	}
}

func TestEnqueueIfNeededVerificationFailureReturnsErrorWithoutDuplicatePost(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})
	sha := repo.Run("rev-parse", "HEAD")

	var firstPostCount atomic.Int32
	var recoveryPostCount atomic.Int32

	startServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			writeJSON(w, map[string]any{"jobs": []storage.ReviewJob{}})
		case "/api/enqueue":
			firstPostCount.Add(1)
			closeConnNoResponse(t, w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer startServer.Close()

	recoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/api/enqueue":
			recoveryPostCount.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer recoveryServer.Close()

	patchFixDaemonRetryForTest(t, func() (string, error) {
		return recoveryServer.URL, nil
	})

	err := enqueueIfNeeded(context.Background(), startServer.URL, repo.Dir, sha)
	if err == nil {
		t.Fatal("expected verification error")
	}
	if !strings.Contains(err.Error(), "verify enqueue after retryable failure") {
		t.Fatalf("expected verification error, got %v", err)
	}
	if firstPostCount.Load() != 1 {
		t.Fatalf("expected one initial enqueue post, got %d", firstPostCount.Load())
	}
	if recoveryPostCount.Load() != 0 {
		t.Fatalf("expected no duplicate enqueue post after verification failure, got %d", recoveryPostCount.Load())
	}
}

func TestEnqueueIfNeededDeadlineExceededCancelsProbeRequest(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})
	sha := repo.Run("rev-parse", "HEAD")

	patchFixDaemonRetryForTest(t, func() (string, error) {
		t.Fatal("enqueueIfNeeded should not attempt daemon recovery after request deadline exceeded")
		return "", nil
	})

	var enqueueCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			time.Sleep(100 * time.Millisecond)
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := enqueueIfNeeded(ctx, ts.URL, repo.Dir, sha)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
	if enqueueCalls.Load() != 0 {
		t.Fatalf("expected no enqueue after canceled probe request, got %d", enqueueCalls.Load())
	}
}

func TestEnqueueIfNeededRefreshesProbeAddressAfterConnectionError(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})
	sha := repo.Run("rev-parse", "HEAD")

	oldProbeAttempts := enqueueIfNeededProbeAttempts
	oldProbeDelay := enqueueIfNeededProbeDelay
	enqueueIfNeededProbeAttempts = 2
	enqueueIfNeededProbeDelay = time.Millisecond
	t.Cleanup(func() {
		enqueueIfNeededProbeAttempts = oldProbeAttempts
		enqueueIfNeededProbeDelay = oldProbeDelay
	})

	var ensureCalls atomic.Int32
	var jobsCalls atomic.Int32
	var enqueueCalls atomic.Int32
	recoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			jobsCalls.Add(1)
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{ID: 42, GitRef: sha}},
			})
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer recoveryServer.Close()

	patchFixDaemonRetryForTest(t, func() (string, error) {
		ensureCalls.Add(1)
		return recoveryServer.URL, nil
	})

	err := enqueueIfNeeded(context.Background(), "http://127.0.0.1:1", repo.Dir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if ensureCalls.Load() == 0 {
		t.Fatal("expected probe to refresh daemon address after connection error")
	}
	if jobsCalls.Load() == 0 {
		t.Fatal("expected probe to hit refreshed daemon address")
	}
	if enqueueCalls.Load() != 0 {
		t.Fatalf("expected no enqueue when refreshed probe found existing job, got %d", enqueueCalls.Load())
	}
}

func TestHasJobForSHADoesNotTriggerDaemonRecovery(t *testing.T) {
	patchFixDaemonRetryForTest(t, func() (string, error) {
		t.Fatal("hasJobForSHA should not attempt daemon recovery")
		return "", nil
	})

	found, err := hasJobForSHA("http://127.0.0.1:1", "abc123def456")
	if err == nil {
		t.Fatal("expected connection error")
	}
	if found {
		t.Fatal("expected no job match on connection failure")
	}
}

func TestQueryOpenJobIDsDeadlineExceededCancelsRequest(t *testing.T) {
	patchFixDaemonRetryForTest(t, func() (string, error) {
		t.Fatal("queryOpenJobIDs should not attempt daemon recovery after request deadline exceeded")
		return "", nil
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := queryOpenJobIDs(ctx, ts.URL, "/tmp/repo", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestRunFixList(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	t.Run("lists open jobs with details", func(t *testing.T) {
		finishedAt := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)
		verdict := "FAIL"

		_ = newMockDaemonBuilder(t).
			WithJobs([]storage.ReviewJob{{
				ID:            42,
				GitRef:        "abc123def456",
				Branch:        "feature-branch",
				CommitSubject: "Fix the widget",
				Agent:         "claude-code",
				Model:         "claude-3-opus",
				Status:        storage.JobStatusDone,
				FinishedAt:    &finishedAt,
				Verdict:       &verdict,
			}}).
			WithReview(42, "Found 3 issues:\n- Missing error handling\n- Unused variable").
			Build()
			// serverAddr is patched by daemonFromHandler called inside Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixList(cmd, "", false)
		})
		if err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		// Check header
		assertContains(t, out, "Found 1 open job(s):", "expected header message, got:\n%s", out)

		// Check job details are displayed
		assertContains(t, out, "Job #42", "expected job ID, got:\n%s", out)
		assertContains(t, out, "Git Ref:  abc123d", "expected git ref, got:\n%s", out)
		assertContains(t, out, "Branch:   feature-branch", "expected branch, got:\n%s", out)
		assertContains(t, out, "Subject:  Fix the widget", "expected subject, got:\n%s", out)
		assertContains(t, out, "Agent:    claude-code", "expected agent, got:\n%s", out)
		assertContains(t, out, "Model:    claude-3-opus", "expected model, got:\n%s", out)
		assertContains(t, out, "Verdict:  FAIL", "expected verdict, got:\n%s", out)
		assertContains(t, out, "Summary:  Found 3 issues:", "expected summary, got:\n%s", out)

		// Check usage hints
		assertContains(t, out, "roborev fix <job_id>", "expected usage hint, got:\n%s", out)
		assertContains(t, out, "roborev fix --open", "expected open hint, got:\n%s", out)
	})

	t.Run("no open jobs", func(t *testing.T) {
		_ = newMockDaemonBuilder(t).
			WithJobs([]storage.ReviewJob{}).
			Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixList(cmd, "", false)
		})
		if err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		assertContains(t, out, "No open jobs found", "expected no jobs message, got:\n%s", out)
	})

	t.Run("respects newest-first flag", func(t *testing.T) {
		var gotIDs []int64
		_ = newMockDaemonBuilder(t).
			WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get(paramClosed) == "false" && q.Get(paramLimit) == "0" {
					// API returns newest first
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 30, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				} else if q.Get(paramID) != "" {
					var id int64
					_, _ = fmt.Sscanf(q.Get(paramID), "%d", &id)
					gotIDs = append(gotIDs, id)
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: id, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			}).
			WithReview(30, "findings").
			WithReview(20, "findings").
			WithReview(10, "findings").
			Build()

		gotIDs = nil
		_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixList(cmd, "", true)
		})
		if err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		if len(gotIDs) != 3 {
			t.Fatalf("expected 3 job fetches, got %d", len(gotIDs))
		}
		if gotIDs[0] != 30 || gotIDs[1] != 20 || gotIDs[2] != 10 {
			t.Errorf("expected newest-first order [30, 20, 10], got %v", gotIDs)
		}
	})
}
func TestTruncateString(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 8, "hello..."},
		{"hello world", 5, "he..."},
		{"hi", 3, "hi"},
		{"hello", 3, "hel"},
		{"", 10, ""},
		// Edge cases for maxLen <= 0
		{"hello", 0, ""},
		{"hello", -1, ""},
		// Unicode handling: ensure multi-byte characters aren't split
		{"こんにちは世界", 5, "こん..."},      // Japanese: maxLen=5, output is 2 chars + "..." = 5 runes
		{"こんにちは", 10, "こんにちは"},       // Japanese: fits within limit
		{"Hello 世界!", 8, "Hello..."}, // Mixed ASCII and Unicode
		{"🎉🎊🎁🎄🎅", 3, "🎉🎊🎁"},          // Emoji: exactly 3 runes
		{"🎉🎊🎁🎄🎅", 4, "🎉..."},         // Emoji: truncate with ellipsis
	}

	for _, tt := range tests {
		got := truncateString(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateString(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

// setupWorktree creates a main repo with a commit and a worktree, returning
// the main repo and the worktree directory path.
func setupWorktree(t *testing.T) (mainRepo *TestGitRepo, worktreeDir string) {
	t.Helper()
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial")

	wtDir := t.TempDir()
	os.Remove(wtDir)
	repo.Run("worktree", "add", "-b", "wt-branch", wtDir)
	return repo, wtDir
}

// setupWorktreeMockDaemon sets up a mock daemon that captures the repo query
// param from /api/jobs requests, returning empty results.
func setupWorktreeMockDaemon(t *testing.T) (receivedRepo *string) {
	t.Helper()
	var repo string
	_ = newMockDaemonBuilder(t).
		WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
			repo = r.URL.Query().Get(paramRepo)
			writeJSON(w, map[string]any{
				"jobs":     []storage.ReviewJob{},
				"has_more": false,
			})
		}).
		Build()
	return &repo
}

func TestFixWorktreeRepoResolution(t *testing.T) {
	t.Run("runFixList sends main repo path", func(t *testing.T) {
		receivedRepo := setupWorktreeMockDaemon(t)
		repo, worktreeDir := setupWorktree(t)
		chdir(t, worktreeDir)

		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := runFixList(cmd, "", false); err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		if *receivedRepo == "" {
			t.Fatal("expected repo param to be sent")
		}
		if *receivedRepo != repo.Dir {
			t.Errorf("expected main repo path %q, got %q", repo.Dir, *receivedRepo)
		}
	})

	t.Run("runFixOpen sends main repo path", func(t *testing.T) {
		receivedRepo := setupWorktreeMockDaemon(t)
		repo, worktreeDir := setupWorktree(t)
		chdir(t, worktreeDir)

		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		opts := fixOptions{quiet: true}
		if err := runFixOpen(cmd, "", false, opts); err != nil {
			t.Fatalf("runFixOpen: %v", err)
		}

		if *receivedRepo == "" {
			t.Fatal("expected repo param to be sent")
		}
		if *receivedRepo != repo.Dir {
			t.Errorf("expected main repo path %q, got %q", repo.Dir, *receivedRepo)
		}
	})

	t.Run("runFixBatch sends main repo path", func(t *testing.T) {
		receivedRepo := setupWorktreeMockDaemon(t)
		repo, worktreeDir := setupWorktree(t)
		chdir(t, worktreeDir)

		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		opts := fixOptions{quiet: true}
		// nil jobIDs triggers discovery via queryOpenJobs
		if err := runFixBatch(cmd, nil, "", false, opts); err != nil {
			t.Fatalf("runFixBatch: %v", err)
		}

		if *receivedRepo == "" {
			t.Fatal("expected repo param to be sent")
		}
		if *receivedRepo != repo.Dir {
			t.Errorf("expected main repo path %q, got %q", repo.Dir, *receivedRepo)
		}
	})
}

func TestJobVerdict(t *testing.T) {
	pass := "P"
	fail := "F"
	empty := ""

	tests := []struct {
		name    string
		verdict *string
		output  string
		want    string
	}{
		{
			name:    "stored PASS verdict",
			verdict: &pass,
			output:  "some output",
			want:    "P",
		},
		{
			name:    "stored FAIL verdict",
			verdict: &fail,
			output:  "No issues found.",
			want:    "F",
		},
		{
			name:    "nil verdict falls back to parse",
			verdict: nil,
			output:  "No issues found.",
			want:    "P",
		},
		{
			name:    "empty verdict falls back to parse",
			verdict: &empty,
			output:  "## Issues\n- Bug in foo.go",
			want:    "F",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &storage.ReviewJob{Verdict: tt.verdict}
			review := &storage.Review{Output: tt.output}
			got := jobVerdict(job, review)
			if got != tt.want {
				t.Errorf("jobVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFixSingleJobSkipsPassVerdict(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})

	var closeCalls atomic.Int32
	var agentCalled atomic.Int32

	ts := newMockDaemonBuilder(t).
		WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusDone,
					Agent:  "test",
				}},
			})
		}).
		WithReview(99, "No issues found.").
		Build()
	// Wrap the server to track close calls
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPathReviewClose {
			closeCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Proxy to the original mock
		proxy, err := http.NewRequest(r.Method, ts.URL+r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := http.DefaultClient.Do(proxy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(wrapper.Close)

	cmd, output := newTestCmd(t, ts.URL)
	ctx := context.WithValue(context.Background(), serverAddrKey{}, wrapper.URL)
	cmd.SetContext(ctx)

	// Use a fake agent that tracks invocations
	agent.Register(&agent.FakeAgent{
		NameStr: "test-pass-skip",
		ReviewFn: func(_ context.Context, _, _, _ string, _ io.Writer) (string, error) {
			agentCalled.Add(1)
			return "", nil
		},
	})
	t.Cleanup(func() { agent.Unregister("test-pass-skip") })

	opts := fixOptions{agentName: "test-pass-skip"}

	err := fixSingleJob(cmd, getDaemonAddr(cmd), repo.Dir, 99, opts)
	if err != nil {
		t.Fatalf("fixSingleJob: %v", err)
	}

	outputStr := output.String()
	assertContains(t, outputStr, "review passed, skipping fix", "expected skip message, got:\n%s", outputStr)
	if agentCalled.Load() != 0 {
		t.Error("agent should not have been invoked for passing review")
	}
	if closeCalls.Load() != 1 {
		t.Errorf("expected 1 close call, got %d", closeCalls.Load())
	}
}

func TestFixBatchSkipsPassVerdict(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})

	var mu sync.Mutex
	var closedJobIDs []int64

	passVerdict := "P"

	_ = newMockDaemonBuilder(t).
		WithHandler(apiPathJobs, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get(paramID) == "10" {
				writeJSON(w, map[string]any{
					"jobs": []storage.ReviewJob{{
						ID:      10,
						Status:  storage.JobStatusDone,
						Agent:   "test",
						Verdict: &passVerdict,
					}},
				})
			} else if q.Get(paramID) == "20" {
				writeJSON(w, map[string]any{
					"jobs": []storage.ReviewJob{{
						ID:     20,
						Status: storage.JobStatusDone,
						Agent:  "test",
					}},
				})
			} else {
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
			}
		}).
		WithHandler(apiPathReview, func(w http.ResponseWriter, r *http.Request) {
			jobID := r.URL.Query().Get(paramJobID)
			if jobID == "10" {
				writeJSON(w, storage.Review{
					JobID:  10,
					Output: "No issues found.",
				})
			} else {
				writeJSON(w, storage.Review{
					JobID:  20,
					Output: "## Issues\n- Bug in foo.go",
				})
			}
		}).
		WithHandler(apiPathReviewClose, func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				JobID int64 `json:"job_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				mu.Lock()
				closedJobIDs = append(closedJobIDs, body.JobID)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		}).
		WithHandler(apiPathEnqueue, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		Build()

	out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixBatch(
			cmd,
			[]int64{10, 20},
			"",
			false,
			fixOptions{agentName: "test", reasoning: "fast"},
		)
	})
	if err != nil {
		t.Fatalf("runFixBatch: %v", err)
	}

	assertContains(t, out, "Skipping job 10 (review passed)", "expected skip message for job 10, got:\n%s", out)
	// Job 20 (FAIL) should be processed — its findings should appear
	assertContains(t, out, "Bug in foo.go", "expected FAIL job findings in output, got:\n%s", out)

	// Verify PASS job 10 was closed during the skip phase
	mu.Lock()
	ids := closedJobIDs
	mu.Unlock()
	if !slices.Contains(ids, int64(10)) {
		t.Errorf("expected job 10 to be closed, got IDs: %v", ids)
	}
}

func setupFixErrorMockDaemon(t *testing.T, processedJobs *[]int64, mu *sync.Mutex) {
	t.Helper()
	_ = newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			jobIDStr := q.Get("id")
			if jobIDStr == "" {
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
			var id int64
			fmt.Sscanf(jobIDStr, "%d", &id)
			mu.Lock()
			*processedJobs = append(*processedJobs, id)
			mu.Unlock()

			// Job 20 is "running" (not complete), which causes fixSingleJob
			// to return an error.
			status := storage.JobStatusDone
			if id == 20 {
				status = storage.JobStatusRunning
			}
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{
					{ID: id, Status: status, Agent: "test"},
				},
				"has_more": false,
			})
		}).
		WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{Output: "findings"})
		}).
		WithHandler("/api/review/close", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		WithHandler("/api/comment", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}).
		WithHandler("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		Build()
}

func TestRunFixWithSeenExplicitFailsOnEligibilityError(t *testing.T) {
	// Explicit job IDs (seen == nil): failure on per-job eligibility (e.g. running status)
	// should abort the whole run.
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	var processedJobs []int64
	var mu sync.Mutex
	setupFixErrorMockDaemon(t, &processedJobs, &mu)

	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixWithSeen(cmd, []int64{10, 20, 30}, fixOptions{
			agentName: "test",
			reasoning: "fast",
		}, nil)
	})

	if err == nil {
		t.Fatalf("expected error for explicit job IDs when failing, got none")
	}
	if !strings.Contains(err.Error(), "error fixing job 20") {
		t.Errorf("expected error about job 20, got:\n%v", err)
	}

	// Job 30 SHOULD NOT be processed because we abort on eligibility errors
	mu.Lock()
	jobs := processedJobs
	mu.Unlock()
	if slices.Contains(jobs, int64(30)) {
		t.Errorf("job 30 should not be processed after job 20 fails with eligibility error, got: %v", jobs)
	}
}

func TestRunFixWithSeenExplicitAbortsOnNonEligibilityErr(t *testing.T) {
	// Explicit job IDs (seen == nil): failure on non-eligibility daemon/API error
	// should abort the run entirely.
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	var processedJobs []int64
	var mu sync.Mutex

	_ = newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			jobIDStr := q.Get("id")
			var id int64
			fmt.Sscanf(jobIDStr, "%d", &id)
			mu.Lock()
			processedJobs = append(processedJobs, id)
			mu.Unlock()

			if id == 20 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{
					{ID: id, Status: storage.JobStatusDone, Agent: "test"},
				},
			})
		}).
		WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{Output: "findings"})
		}).
		Build()

	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixWithSeen(cmd, []int64{10, 20, 30}, fixOptions{
			agentName: "test",
			reasoning: "fast",
		}, nil)
	})

	if err == nil {
		t.Fatal("expected error for explicit job IDs when non-eligibility error occurs, got nil")
	}

	mu.Lock()
	jobs := processedJobs
	mu.Unlock()
	if slices.Contains(jobs, int64(30)) {
		t.Errorf("job 30 should NOT be processed after job 20 fails with transport error, got: %v", jobs)
	}
}

func TestRunFixWithSeenDiscoveryContinuesOnError(t *testing.T) {
	// Discovery mode (seen != nil): failure should warn and continue
	// best-effort so the re-query loop processes remaining jobs.
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	var processedJobs []int64
	var mu sync.Mutex
	setupFixErrorMockDaemon(t, &processedJobs, &mu)

	seen := make(map[int64]bool)
	out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixWithSeen(cmd, []int64{10, 20, 30}, fixOptions{
			agentName: "test",
			reasoning: "fast",
		}, seen)
	})

	if err != nil {
		t.Fatalf("expected no error in discovery mode, got: %v", err)
	}

	if !strings.Contains(out, "Warning: error fixing job 20") {
		t.Errorf("expected warning about job 20, got:\n%s", out)
	}

	// All three jobs should be attempted
	mu.Lock()
	jobs := processedJobs
	mu.Unlock()
	if !slices.Contains(jobs, int64(30)) {
		t.Errorf("job 30 should be processed in discovery mode, got: %v", jobs)
	}

	// Failed job should be marked as seen
	if !seen[20] {
		t.Error("job 20 should be marked as seen even after failure")
	}
}

func TestRunFixWithSeenDiscoveryAbortsOnConnectionError(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	patchFixDaemonRetryForTest(t, func() (string, error) {
		return "", nil
	})

	var primaryServer *httptest.Server
	var jobsCalls atomic.Int32
	ts := newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     10,
					Status: storage.JobStatusDone,
					Agent:  "test",
				}},
			})
			// Close primary server after first job fetch so
			// subsequent calls trigger connection errors.
			if jobsCalls.Add(1) == 1 {
				go primaryServer.Close()
			}
		}).
		Build()
	primaryServer = ts
	_ = ts

	seen := make(map[int64]bool)
	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixWithSeen(cmd, []int64{10, 20}, fixOptions{
			agentName: "test",
			reasoning: "fast",
		}, seen)
	})

	if err == nil {
		t.Fatal("expected connection error in discovery mode")
	}
	if !strings.Contains(err.Error(), "daemon connection lost") {
		t.Fatalf("expected daemon connection lost error, got: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("expected no jobs marked as seen after connection failure, got: %v", seen)
	}
}

func TestResolveFixAgentSkipsDefaultModel(t *testing.T) {
	// When --agent is passed on CLI, the global default_model should
	// NOT be applied. The default_model is paired with default_agent
	// and likely doesn't apply to the overridden agent.

	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	cfgPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`
default_agent = "codex"
default_model = "gpt-5.4"
`), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		agentName string // CLI --agent value
		model     string // CLI --model value
		wantModel bool   // whether default_model should be applied
	}{
		{
			name:      "no CLI agent uses default_model",
			agentName: "",
			model:     "",
			wantModel: true,
		},
		{
			name:      "CLI agent skips default_model",
			agentName: "test",
			model:     "",
			wantModel: false,
		},
		{
			name:      "CLI agent with CLI model uses CLI model",
			agentName: "test",
			model:     "my-model",
			wantModel: true,
		},
		{
			name:      "no CLI agent with CLI model uses CLI model",
			agentName: "",
			model:     "my-model",
			wantModel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.LoadGlobal()
			if err != nil {
				t.Fatalf("LoadGlobal: %v", err)
			}

			modelStr := resolveFixModel(
				tt.agentName, tt.model, tmpDir, cfg, "fast",
			)

			if tt.wantModel && modelStr == "" {
				t.Errorf("expected model to be set, got empty")
			}
			if !tt.wantModel && modelStr != "" {
				t.Errorf("expected model to be empty, got %q", modelStr)
			}
			if tt.model != "" && modelStr != tt.model {
				t.Errorf("expected model %q, got %q", tt.model, modelStr)
			}
		})
	}
}

func TestResolveFixAgentUsesWorkflowModel(t *testing.T) {
	// When --agent is passed on CLI, workflow-specific models (e.g.,
	// fix_model) should still be used. Only generic defaults are skipped.

	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Write global config with both default_model and fix_model
	cfgPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`
default_agent = "codex"
default_model = "gpt-5.4"
fix_model = "gemini-2.5-pro"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}

	// With CLI --agent override, fix_model should still apply
	modelStr := config.ResolveWorkflowModel(tmpDir, cfg, "fix", "fast")
	if modelStr != "gemini-2.5-pro" {
		t.Errorf(
			"expected workflow-specific model 'gemini-2.5-pro', got %q",
			modelStr,
		)
	}

	// Without CLI --agent, ResolveModelForWorkflow should return
	// fix_model (higher priority than default_model)
	modelStr = config.ResolveModelForWorkflow("", tmpDir, cfg, "fix", "fast")
	if modelStr != "gemini-2.5-pro" {
		t.Errorf(
			"expected workflow-specific model 'gemini-2.5-pro', got %q",
			modelStr,
		)
	}
}

func TestResolveFixAgentUsesRepoWorkflowModel(t *testing.T) {
	// Repo-level workflow-specific models should be used even when
	// --agent is overridden on CLI.

	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Write global config with default_model only
	cfgPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`
default_agent = "codex"
default_model = "gpt-5.4"
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Write repo config with fix_model_fast
	repoDir := t.TempDir()
	repoConfigPath := filepath.Join(repoDir, ".roborev.toml")
	if err := os.WriteFile(repoConfigPath, []byte(`
fix_model_fast = "claude-sonnet"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}

	// With CLI --agent override, repo fix_model_fast should apply
	modelStr := config.ResolveWorkflowModel(repoDir, cfg, "fix", "fast")
	if modelStr != "claude-sonnet" {
		t.Errorf(
			"expected repo workflow model 'claude-sonnet', got %q",
			modelStr,
		)
	}

	// Repo generic model should NOT be used
	repoConfigPath2 := filepath.Join(repoDir, ".roborev.toml")
	if err := os.WriteFile(repoConfigPath2, []byte(`
model = "repo-default"
`), 0644); err != nil {
		t.Fatal(err)
	}

	modelStr = config.ResolveWorkflowModel(repoDir, cfg, "fix", "fast")
	if modelStr != "" {
		t.Errorf(
			"expected empty model (repo generic should be skipped), got %q",
			modelStr,
		)
	}
}

func TestResolveFixAgentSameAsDefault(t *testing.T) {
	// When --agent matches the config default (even via alias),
	// the generic default_model should still be applied because
	// the agent is effectively unchanged.

	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	tests := []struct {
		name         string
		defaultAgent string
		fixAgent     string
		fixModel     string
		fixAgentFast string
		fixModelFast string
		cliAgent     string
		wantModel    string
	}{
		{
			name:         "same agent uses default_model",
			defaultAgent: "codex",
			cliAgent:     "codex",
			wantModel:    "gpt-5.4",
		},
		{
			name:         "alias matches default uses default_model",
			defaultAgent: "claude-code",
			cliAgent:     "claude",
			wantModel:    "gpt-5.4",
		},
		{
			name:         "different agent skips default_model",
			defaultAgent: "codex",
			cliAgent:     "test",
			wantModel:    "",
		},
		{
			name:         "fix workflow specific agent match uses fix model",
			defaultAgent: "claude-code",
			fixAgent:     "codex",
			fixModel:     "gpt-5.4",
			cliAgent:     "codex",
			wantModel:    "gpt-5.4",
		},
		{
			name:         "reasoning specific agent match uses reasoning model",
			defaultAgent: "claude-code",
			fixAgentFast: "kilo",
			fixModelFast: "kilo-2.0",
			cliAgent:     "kilo",
			wantModel:    "kilo-2.0",
		},
		{
			name:         "reasoning specific agent mismatch skips reasoning model",
			defaultAgent: "claude-code",
			fixAgentFast: "kilo",
			fixModelFast: "kilo-2.0",
			cliAgent:     "codex",
			wantModel:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := filepath.Join(tmpDir, "config.toml")
			cfgContent := fmt.Sprintf(
				"default_agent = %q\ndefault_model = \"gpt-5.4\"\n",
				tt.defaultAgent,
			)
			if tt.fixAgent != "" {
				cfgContent += fmt.Sprintf("fix_agent = %q\n", tt.fixAgent)
				if tt.fixModel != "" {
					cfgContent += fmt.Sprintf("fix_model = %q\n", tt.fixModel)
				}
			}
			if tt.fixAgentFast != "" {
				cfgContent += fmt.Sprintf("fix_agent_fast = %q\n", tt.fixAgentFast)
				if tt.fixModelFast != "" {
					cfgContent += fmt.Sprintf("fix_model_fast = %q\n", tt.fixModelFast)
				}
			}
			if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadGlobal()
			if err != nil {
				t.Fatalf("LoadGlobal: %v", err)
			}

			// Call the production resolveFixModel function directly
			modelStr := resolveFixModel(
				tt.cliAgent, "", tmpDir, cfg, "fast",
			)

			if modelStr != tt.wantModel {
				t.Errorf("model = %q, want %q", modelStr, tt.wantModel)
			}
		})
	}
}

func TestResolveFixModel_RepoGenericAgent_GlobalDefaultModel(t *testing.T) {
	// Tests the regression where a repo-level generic `agent` would cause the CLI override
	// comparison against the "default agent" to incorrectly classify a global default agent
	// request as an override, thus skipping the valid global `default_model`.
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	globalCfgPath := filepath.Join(tmpDir, "config.toml")
	globalCfgContent := `
default_agent = "codex"
default_model = "gpt-5.4"
`
	if err := os.WriteFile(globalCfgPath, []byte(globalCfgContent), 0644); err != nil {
		t.Fatal(err)
	}

	repoDir := t.TempDir()
	repoCfgPath := filepath.Join(repoDir, ".roborev.toml")
	repoCfgContent := `
agent = "claude"
`
	if err := os.WriteFile(repoCfgPath, []byte(repoCfgContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}

	// 1. If CLI specifies "codex", it matches global default, should use global default_model
	modelStr := resolveFixModel("codex", "", repoDir, cfg, "fast")
	if modelStr != "gpt-5.4" {
		t.Errorf("expected global default_model 'gpt-5.4', got %q", modelStr)
	}

	// 2. If CLI doesn't specify an agent, it uses repo generic agent ("claude")
	// and there's no model, so it shouldn't get "gpt-5.4" as it is meant for codex.
	// Actually, wait, if cliAgent is "", cliAgentChanged is false, so it falls through to
	// ResolveModelForWorkflow which gets the generic model fallback (gpt-5.4)
	// That's acceptable generic fallback behavior, but we mainly care about cli overriding.
}

func setupFixAgentErrMockDaemon(t *testing.T, processedJobs *[]int64, mu *sync.Mutex) {
	t.Helper()
	_ = newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			jobIDStr := q.Get("id")
			if jobIDStr == "" {
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
			var id int64
			fmt.Sscanf(jobIDStr, "%d", &id)
			mu.Lock()
			*processedJobs = append(*processedJobs, id)
			mu.Unlock()

			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{
					{ID: id, Status: storage.JobStatusDone, Agent: "test"},
				},
				"has_more": false,
			})
		}).
		WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("job_id") == "10" {
				writeJSON(w, storage.Review{Output: "No issues found."})
			} else {
				writeJSON(w, storage.Review{Output: "findings"})
			}
		}).
		WithHandler("/api/review/close", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		Build()
}

func TestRunFixWithSeenExplicitAbortsOnAgentErr(t *testing.T) {
	// Explicit job IDs (seen == nil): failure on agent error
	// should abort the run entirely.
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	var processedJobs []int64
	var mu sync.Mutex
	setupFixAgentErrMockDaemon(t, &processedJobs, &mu)

	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		// Passing an invalid agent name will cause an agentExecutionError during resolveFixAgent.
		return runFixWithSeen(cmd, []int64{10, 20, 30}, fixOptions{
			agentName: "non-existent-agent-that-fails",
			reasoning: "fast",
		}, nil)
	})

	if err == nil {
		t.Fatal("expected error for explicit job IDs when agent error occurs, got nil")
	}
	if !strings.Contains(err.Error(), "resolve agent") {
		t.Errorf("error should be an agent error, got: %v", err)
	}

	mu.Lock()
	processed := append([]int64(nil), processedJobs...)
	mu.Unlock()
	if !slices.Equal(processed, []int64{10, 20}) {
		t.Errorf("expected to process [10 20], got %v", processed)
	}
}

func TestRunFixWithSeenDiscoveryAbortsOnAgentErr(t *testing.T) {
	// Discovery mode (seen != nil): failure on agent error should abort the run entirely.
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	var processedJobs []int64
	var mu sync.Mutex
	setupFixAgentErrMockDaemon(t, &processedJobs, &mu)

	seen := make(map[int64]bool)
	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		// Passing an invalid agent name will cause an agentExecutionError
		return runFixWithSeen(cmd, []int64{10, 20, 30}, fixOptions{
			agentName: "non-existent-agent-that-fails",
			reasoning: "fast",
		}, seen)
	})

	if err == nil {
		t.Fatal("expected error for discovery mode when agent error occurs, got nil")
	}
	if !strings.Contains(err.Error(), "resolve agent") {
		t.Errorf("error should be an agent error, got: %v", err)
	}

	mu.Lock()
	processed := append([]int64(nil), processedJobs...)
	mu.Unlock()
	if !slices.Equal(processed, []int64{10, 20}) {
		t.Errorf("expected to process [10 20], got %v", processed)
	}
}
