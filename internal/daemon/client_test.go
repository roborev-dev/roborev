package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

// createTestGitRepo creates a temporary git repo with an initial commit and
// returns the repo path (symlink-resolved) and a helper to run git commands in it.
func createTestGitRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	tmpDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(tmpDir); err == nil {
		tmpDir = resolved
	}
	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	testFile := filepath.Join(repoDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	return repoDir, run
}

// mockAPI creates an httptest.Server with the given handler and returns
// an HTTPClient pointing at it. The server is closed when the test finishes.
func mockAPI(t *testing.T, handler http.HandlerFunc) *HTTPClient {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return NewHTTPClient(s.URL)
}

func assertRequest(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method || r.URL.Path != path {
		t.Errorf("expected %s %s, got %s %s", method, path, r.Method, r.URL.Path)
	}
}

func assertQuery(t *testing.T, r *http.Request, key, expected string) {
	t.Helper()
	if got := r.URL.Query().Get(key); got != expected {
		t.Errorf("expected %s query param %q, got %q", key, expected, got)
	}
}

func decodeJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestHTTPClientAddComment(t *testing.T) {
	var received struct {
		JobID     int    `json:"job_id"`
		Commenter string `json:"commenter"`
		Comment   string `json:"comment"`
	}

	client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodPost, "/api/comment")
		decodeJSON(t, r, &received)
		w.WriteHeader(http.StatusCreated)
	})
	if err := client.AddComment(42, "test-agent", "Fixed the issue"); err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	if received.JobID != 42 {
		t.Errorf("expected job_id 42, got %v", received.JobID)
	}
	if received.Commenter != "test-agent" {
		t.Errorf("expected commenter test-agent, got %v", received.Commenter)
	}
	if received.Comment != "Fixed the issue" {
		t.Errorf("expected comment to match, got %v", received.Comment)
	}
}

func TestHTTPClientMarkReviewAddressed(t *testing.T) {
	var received struct {
		JobID     int  `json:"job_id"`
		Addressed bool `json:"addressed"`
	}

	client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodPost, "/api/review/address")
		decodeJSON(t, r, &received)
		w.WriteHeader(http.StatusOK)
	})
	if err := client.MarkReviewAddressed(99); err != nil {
		t.Fatalf("MarkReviewAddressed failed: %v", err)
	}

	if received.JobID != 99 {
		t.Errorf("expected job_id 99, got %v", received.JobID)
	}
	if received.Addressed != true {
		t.Errorf("expected addressed true, got %v", received.Addressed)
	}
}

func TestHTTPClientWaitForReviewUsesJobID(t *testing.T) {
	var reviewCalls int32

	client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/jobs" && r.Method == http.MethodGet:
			writeTestJSON(t, w, map[string]any{
				"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusDone, GitRef: "commit1"}},
			})
			return
		case r.URL.Path == "/api/review" && r.Method == http.MethodGet:
			assertQuery(t, r, "job_id", "1")
			assertQuery(t, r, "sha", "")
			if atomic.AddInt32(&reviewCalls, 1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeTestJSON(t, w, storage.Review{ID: 1, JobID: 1, Output: "Review complete"})
			return
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client.SetPollInterval(1 * time.Millisecond)
	review, err := client.WaitForReview(1)
	if err != nil {
		t.Fatalf("WaitForReview failed: %v", err)
	}
	if review.Output != "Review complete" {
		t.Errorf("unexpected output: %s", review.Output)
	}
	if atomic.LoadInt32(&reviewCalls) < 2 {
		t.Errorf("expected review to be retried after 404")
	}
}

func TestFindJobForCommit(t *testing.T) {
	// Skip if git is not available (minimal CI environments)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a real git repo and worktree to test path normalization
	mainRepo, runGit := createTestGitRepo(t)
	worktreeDir := filepath.Join(filepath.Dir(mainRepo), "worktree")
	runGit("worktree", "add", worktreeDir, "HEAD")

	sha := "abc123"

	tests := []struct {
		name           string
		queryRepo      string
		setupMock      func(t *testing.T) func(t *testing.T, w http.ResponseWriter, r *http.Request)
		expectedJobID  int64
		expectNotFound bool
	}{
		{
			name:      "worktree path normalized to main repo",
			queryRepo: worktreeDir,
			setupMock: func(t *testing.T) func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				return func(t *testing.T, w http.ResponseWriter, r *http.Request) {
					assertRequest(t, r, http.MethodGet, "/api/jobs")

					repo := r.URL.Query().Get("repo")
					if repo == "" {
						t.Error("expected server to receive a repo path, got empty")
					}
					if repo == worktreeDir {
						t.Errorf("expected server to NOT receive worktree path %q", worktreeDir)
					}

					normalizedReceived := repo
					if resolved, err := filepath.EvalSymlinks(repo); err == nil {
						normalizedReceived = resolved
					}

					if normalizedReceived == mainRepo {
						writeTestJSON(t, w, map[string]any{
							"jobs": []storage.ReviewJob{
								{ID: 1, GitRef: sha, RepoPath: mainRepo, Status: storage.JobStatusDone},
							},
						})
						return
					}
					writeTestJSON(t, w, map[string]any{"jobs": []storage.ReviewJob{}})
				}
			},
			expectedJobID: 1,
		},
		{
			name:      "main repo path works directly",
			queryRepo: mainRepo,
			setupMock: func(t *testing.T) func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				return func(t *testing.T, w http.ResponseWriter, r *http.Request) {
					assertRequest(t, r, http.MethodGet, "/api/jobs")

					repo := r.URL.Query().Get("repo")
					normalizedReceived := repo
					if resolved, err := filepath.EvalSymlinks(repo); err == nil {
						normalizedReceived = resolved
					}

					if normalizedReceived == mainRepo {
						writeTestJSON(t, w, map[string]any{
							"jobs": []storage.ReviewJob{
								{ID: 1, GitRef: sha, RepoPath: mainRepo, Status: storage.JobStatusDone},
							},
						})
						return
					}
					writeTestJSON(t, w, map[string]any{"jobs": []storage.ReviewJob{}})
				}
			},
			expectedJobID: 1,
		},
		{
			name:      "fallback when primary query returns no results",
			queryRepo: mainRepo,
			setupMock: func(t *testing.T) func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				var calls int
				t.Cleanup(func() {
					if calls != 2 {
						t.Errorf("expected 2 calls, got %d", calls)
					}
				})
				return func(t *testing.T, w http.ResponseWriter, r *http.Request) {
					assertRequest(t, r, http.MethodGet, "/api/jobs")
					calls++

					repo := r.URL.Query().Get("repo")
					if calls == 1 {
						if repo == "" {
							t.Error("expected first call to have non-empty repo")
						}
						// Return empty to trigger fallback
						writeTestJSON(t, w, map[string]any{"jobs": []storage.ReviewJob{}})
						return
					}

					if calls == 2 {
						if repo != "" {
							t.Errorf("expected second call to have empty repo, got %q", repo)
						}
						// Fallback query (no repo filter)
						writeTestJSON(t, w, map[string]any{
							"jobs": []storage.ReviewJob{
								{ID: 1, GitRef: sha, RepoPath: mainRepo, Status: storage.JobStatusDone},
							},
						})
						return
					}
					t.Errorf("unexpected call %d", calls)
				}
			},
			expectedJobID: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.setupMock(t)
			client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				handler(t, w, r)
			})
			job, err := client.FindJobForCommit(tc.queryRepo, sha)
			if err != nil {
				t.Fatalf("FindJobForCommit failed: %v", err)
			}

			if tc.expectNotFound {
				if job != nil {
					t.Errorf("expected no job, got ID %d", job.ID)
				}
			} else {
				if job == nil {
					t.Fatalf("expected to find job, got nil")
				}
				if job.ID != tc.expectedJobID {
					t.Errorf("expected job ID %d, got %d", tc.expectedJobID, job.ID)
				}
			}
		})
	}
}

func TestFindPendingJobForRef(t *testing.T) {
	tests := []struct {
		name                 string
		mockResponses        map[string][]storage.ReviewJob
		expectJobID          int64
		expectStatus         storage.JobStatus
		expectNotFound       bool
		expectedRequestOrder []string
	}{
		{
			name: "returns running job",
			mockResponses: map[string][]storage.ReviewJob{
				"running": {{ID: 1, GitRef: "abc123..def456", Status: storage.JobStatusRunning}},
			},
			expectJobID:          1,
			expectStatus:         storage.JobStatusRunning,
			expectedRequestOrder: []string{"queued", "running"},
		},
		{
			name:                 "returns nil when no pending jobs",
			mockResponses:        map[string][]storage.ReviewJob{},
			expectNotFound:       true,
			expectedRequestOrder: []string{"queued", "running"},
		},
		{
			name: "returns queued job before checking running",
			mockResponses: map[string][]storage.ReviewJob{
				"queued": {{ID: 1, GitRef: "abc123..def456", Status: storage.JobStatusQueued}},
			},
			expectJobID:          1,
			expectStatus:         storage.JobStatusQueued,
			expectedRequestOrder: []string{"queued"},
		},
		{
			name: "queries both queued and running when needed",
			mockResponses: map[string][]storage.ReviewJob{
				"running": {{ID: 2, GitRef: "abc123..def456", Status: storage.JobStatusRunning}},
			},
			expectJobID:          2,
			expectStatus:         storage.JobStatusRunning,
			expectedRequestOrder: []string{"queued", "running"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requestedStatuses []string
			var mu sync.Mutex

			client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				assertRequest(t, r, http.MethodGet, "/api/jobs")

				assertQuery(t, r, "git_ref", "abc123..def456")

				expectedRepo, err := filepath.Abs("/test/repo")
				if err != nil {
					t.Fatalf("filepath.Abs failed: %v", err)
				}
				assertQuery(t, r, "repo", expectedRepo)

				status := r.URL.Query().Get("status")
				mu.Lock()
				requestedStatuses = append(requestedStatuses, status)
				mu.Unlock()

				jobs, ok := tc.mockResponses[status]
				if !ok {
					jobs = []storage.ReviewJob{}
				}
				writeTestJSON(t, w, map[string]any{"jobs": jobs})
			})

			job, err := client.FindPendingJobForRef("/test/repo", "abc123..def456")
			if err != nil {
				t.Fatalf("FindPendingJobForRef failed: %v", err)
			}

			if tc.expectNotFound {
				if job != nil {
					t.Errorf("expected nil job, got ID %d", job.ID)
				}
			} else {
				if job == nil {
					t.Fatal("expected to find job")
				}
				if job.ID != tc.expectJobID {
					t.Errorf("expected job ID %d, got %d", tc.expectJobID, job.ID)
				}
				if tc.expectStatus != "" && job.Status != tc.expectStatus {
					t.Errorf("expected status %s, got %s", tc.expectStatus, job.Status)
				}
			}

			if len(requestedStatuses) != len(tc.expectedRequestOrder) {
				t.Errorf("expected %d requests, got %d: %v", len(tc.expectedRequestOrder), len(requestedStatuses), requestedStatuses)
			} else {
				for i, want := range tc.expectedRequestOrder {
					if requestedStatuses[i] != want {
						t.Errorf("request %d: expected status %q, got %q", i, want, requestedStatuses[i])
					}
				}
			}
		})
	}
}
