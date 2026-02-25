package main

// Tests for daemon client functions (getCommentsForJob, waitForReview, findJobForCommit)

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

// writeJSON encodes data as JSON to the response writer.
func writeJSON(w http.ResponseWriter, data any) {
	json.NewEncoder(w).Encode(data)
}

// newMockHandler creates a handler that asserts method and path, then writes a response.
// response can be a byte slice (for raw output) or any other type (encoded as JSON).
func newMockHandler(t *testing.T, method, path string, response any, status int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			t.Errorf("expected method %s, got %s", method, r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != path {
			t.Errorf("expected path %s, got %s", path, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		if response != nil {
			if b, ok := response.([]byte); ok {
				_, _ = w.Write(b)
			} else {
				writeJSON(w, response)
			}
		}
	}
}

// MockStep defines a single expected request/response in a sequence.
type MockStep struct {
	Jobs          []storage.ReviewJob
	ExpectedQuery map[string]string
}

// mockSequenceHandler returns different responses for sequential calls to /api/jobs.
// This allows testing fallback logic where multiple calls are made.
func mockSequenceHandler(t *testing.T, steps ...MockStep) http.HandlerFunc {
	t.Helper()
	var (
		mu   sync.Mutex
		call int
	)

	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if !t.Failed() && call != len(steps) {
			t.Errorf("expected %d calls to sequence handler, got %d", len(steps), call)
		}
	})

	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.URL.Path != "/api/jobs" || r.Method != "GET" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if call >= len(steps) {
			t.Errorf("unexpected extra call to %s (expected %d calls)", r.URL.Path, len(steps))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		step := steps[call]
		call++

		query := r.URL.Query()
		for k, v := range step.ExpectedQuery {
			if got := query.Get(k); got != v {
				t.Errorf("call %d: expected query param %s=%s, got %s", call, k, v, got)
			}
		}

		writeJSON(w, map[string]any{"jobs": step.Jobs})
	}
}

func TestGetCommentsForJob(t *testing.T) {
	t.Run("returns responses for job", func(t *testing.T) {
		mockResp := map[string]any{
			"responses": []storage.Response{
				{ID: 1, Responder: "user", Response: "Fixed it"},
				{ID: 2, Responder: "agent", Response: "Verified"},
			},
		}

		// Wrap newMockHandler to verify query params
		baseHandler := newMockHandler(t, "GET", "/api/comments", mockResp, http.StatusOK)
		handler := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("job_id") != "42" {
				t.Errorf("expected job_id=42, got %s", r.URL.Query().Get("job_id"))
			}
			baseHandler(w, r)
		}

		daemonFromHandler(t, http.HandlerFunc(handler))

		responses, err := getCommentsForJob(42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(responses) != 2 {
			t.Errorf("expected 2 responses, got %d", len(responses))
		}
	})

	t.Run("returns error on non-200", func(t *testing.T) {
		handler := newMockHandler(t, "GET", "/api/comments", nil, http.StatusInternalServerError)
		daemonFromHandler(t, handler)

		_, err := getCommentsForJob(42)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestWaitForReview(t *testing.T) {
	t.Run("returns review when job completes", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.Handle("/api/jobs", newMockHandler(t, "GET", "/api/jobs",
			map[string]any{"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusDone}}}, 0))
		mux.Handle("/api/review", newMockHandler(t, "GET", "/api/review",
			storage.Review{ID: 1, JobID: 1, Output: "Review complete"}, 0))

		daemonFromHandler(t, mux)

		review, err := waitForReview(1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review.Output != "Review complete" {
			t.Errorf("unexpected output: %s", review.Output)
		}
	})

	t.Run("polls until job transitions from queued to done", func(t *testing.T) {
		pollCount := 0
		mux := http.NewServeMux()
		mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			pollCount++
			status := storage.JobStatusQueued
			if pollCount >= 3 {
				status = storage.JobStatusDone
			}
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{ID: 1, Status: status}},
			})
		})
		mux.Handle("/api/review", newMockHandler(t, "GET", "/api/review",
			storage.Review{ID: 1, JobID: 1, Output: "Review after polling"}, 0))

		daemonFromHandler(t, mux)

		review, err := waitForReviewWithInterval(1, 1*time.Millisecond)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review.Output != "Review after polling" {
			t.Errorf("unexpected output: %s", review.Output)
		}
		if pollCount < 3 {
			t.Errorf("expected at least 3 polls, got %d", pollCount)
		}
	})

	t.Run("returns error on job failure", func(t *testing.T) {
		resp := map[string]any{
			"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusFailed, Error: "agent crashed"}},
		}
		daemonFromHandler(t, newMockHandler(t, "GET", "/api/jobs", resp, 0))

		_, err := waitForReview(1)
		if err == nil {
			t.Fatal("expected error for failed job")
		}
		if !strings.Contains(err.Error(), "agent crashed") {
			t.Errorf("expected error to mention agent crashed: %v", err)
		}
	})

	t.Run("returns error on job canceled", func(t *testing.T) {
		resp := map[string]any{
			"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusCanceled}},
		}
		daemonFromHandler(t, newMockHandler(t, "GET", "/api/jobs", resp, 0))

		_, err := waitForReview(1)
		if err == nil {
			t.Fatal("expected error for canceled job")
		}
		if !strings.Contains(err.Error(), "canceled") {
			t.Errorf("expected error to mention canceled: %v", err)
		}
	})
}

// normalizeTestPath resolves symlinks and returns the absolute path
func normalizeTestPath(t *testing.T, path string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

func TestFindJobForCommit(t *testing.T) {
	tests := []struct {
		name        string
		setupTest   func(t *testing.T) (string, []MockStep)                    // Returns the repo path and mock steps
		mockHandler func(t *testing.T, w http.ResponseWriter, r *http.Request) // Optional custom handler override
		commitSHA   string
		expectedID  int64
		expectFound bool
		expectError string
	}{
		{
			name:      "finds matching job",
			commitSHA: "def456",
			setupTest: func(t *testing.T) (string, []MockStep) {
				repoDir := normalizeTestPath(t, t.TempDir())
				return repoDir, []MockStep{
					{
						Jobs:          []storage.ReviewJob{{ID: 2, GitRef: "def456", RepoPath: repoDir}},
						ExpectedQuery: map[string]string{"git_ref": "def456", "repo": repoDir, "limit": "1"},
					},
				}
			},
			expectedID:  2,
			expectFound: true,
		},
		{
			name:      "returns nil when not found",
			commitSHA: "notfound",
			setupTest: func(t *testing.T) (string, []MockStep) {
				repoDir := normalizeTestPath(t, t.TempDir())
				return repoDir, []MockStep{
					{
						Jobs:          []storage.ReviewJob{},
						ExpectedQuery: map[string]string{"git_ref": "notfound", "repo": repoDir, "limit": "1"},
					},
					{
						Jobs:          []storage.ReviewJob{},
						ExpectedQuery: map[string]string{"git_ref": "notfound", "limit": "100"},
					},
				}
			},
			expectFound: false,
		},
		{
			name:      "fallback skips jobs from different repo",
			commitSHA: "abc123",
			setupTest: func(t *testing.T) (string, []MockStep) {
				repoDir := normalizeTestPath(t, t.TempDir())
				return repoDir, []MockStep{
					{
						Jobs:          []storage.ReviewJob{},
						ExpectedQuery: map[string]string{"git_ref": "abc123", "repo": repoDir, "limit": "1"},
					},
					{
						Jobs: []storage.ReviewJob{
							{ID: 1, GitRef: "abc123", RepoPath: "/other/repo"},
							{ID: 2, GitRef: "abc123", RepoPath: "/another/repo"},
						},
						ExpectedQuery: map[string]string{"git_ref": "abc123", "limit": "100"},
					},
				}
			},
			expectFound: false,
		},
		{
			name:      "fallback matches job with same normalized path",
			commitSHA: "abc123",
			setupTest: func(t *testing.T) (string, []MockStep) {
				tmpDir := normalizeTestPath(t, t.TempDir())
				repoPath := filepath.Join(tmpDir, "repo")
				if err := os.MkdirAll(repoPath, 0755); err != nil {
					t.Fatal(err)
				}
				return repoPath, []MockStep{
					{
						Jobs:          []storage.ReviewJob{},
						ExpectedQuery: map[string]string{"git_ref": "abc123", "repo": repoPath, "limit": "1"},
					},
					{
						Jobs: []storage.ReviewJob{
							{ID: 1, GitRef: "abc123", RepoPath: repoPath},
						},
						ExpectedQuery: map[string]string{"git_ref": "abc123", "limit": "100"},
					},
				}
			},
			expectedID:  1,
			expectFound: true,
		},
		{
			name:      "returns error on non-200 response",
			commitSHA: "abc123",
			setupTest: func(t *testing.T) (string, []MockStep) {
				return normalizeTestPath(t, "/test/repo"), nil
			},
			mockHandler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			expectError: "500",
		},
		{
			name:      "returns error on invalid JSON",
			commitSHA: "abc123",
			setupTest: func(t *testing.T) (string, []MockStep) {
				return normalizeTestPath(t, t.TempDir()), nil
			},
			mockHandler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("not json"))
			},
			expectError: "decode",
		},
		{
			name:      "fallback skips jobs with empty or relative paths",
			commitSHA: "abc123",
			setupTest: func(t *testing.T) (string, []MockStep) {
				repoDir := normalizeTestPath(t, t.TempDir())
				return repoDir, []MockStep{
					{
						Jobs:          []storage.ReviewJob{},
						ExpectedQuery: map[string]string{"git_ref": "abc123", "repo": repoDir, "limit": "1"},
					},
					{
						Jobs: []storage.ReviewJob{
							{ID: 1, GitRef: "abc123", RepoPath: ""},
							{ID: 2, GitRef: "abc123", RepoPath: "relative/path"},
							{ID: 3, GitRef: "abc123", RepoPath: repoDir},
						},
						ExpectedQuery: map[string]string{"git_ref": "abc123", "limit": "100"},
					},
				}
			},
			expectedID:  3,
			expectFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir, steps := tt.setupTest(t)

			var handler http.HandlerFunc
			if tt.mockHandler != nil {
				handler = func(w http.ResponseWriter, r *http.Request) {
					tt.mockHandler(t, w, r)
				}
			} else {
				handler = mockSequenceHandler(t, steps...)
			}

			daemonFromHandler(t, handler)

			job, err := findJobForCommit(repoDir, tt.commitSHA)

			if tt.expectError != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.expectError) {
					t.Errorf("expected error to contain %q, got: %v", tt.expectError, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.expectFound {
				if job == nil {
					t.Fatal("expected to find job")
				}
				if job.ID != tt.expectedID {
					t.Errorf("expected job ID %d, got %d", tt.expectedID, job.ID)
				}
			} else {
				if job != nil {
					t.Errorf("expected nil job, got ID %d", job.ID)
				}
			}
		})
	}
}
