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

func TestHTTPClientAddComment(t *testing.T) {
	var received struct {
		JobID     int    `json:"job_id"`
		Commenter string `json:"commenter"`
		Comment   string `json:"comment"`
	}

	client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodPost, "/api/comment")
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
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
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
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
			json.NewEncoder(w).Encode(map[string]any{
				"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusDone, GitRef: "commit1"}},
			})
			return
		case r.URL.Path == "/api/review" && r.Method == http.MethodGet:
			if r.URL.Query().Get("job_id") != "1" {
				t.Errorf("expected job_id query param, got %q", r.URL.RawQuery)
			}
			if r.URL.Query().Get("sha") != "" {
				t.Errorf("did not expect sha query param, got %q", r.URL.RawQuery)
			}
			if atomic.AddInt32(&reviewCalls, 1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Output: "Review complete"})
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

func TestFindJobForCommitWorktree(t *testing.T) {
	// Skip if git is not available (minimal CI environments)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a real git repo and worktree to test path normalization
	mainRepo, runGit := createTestGitRepo(t)
	worktreeDir := filepath.Join(filepath.Dir(mainRepo), "worktree")
	runGit("worktree", "add", worktreeDir, "HEAD")

	sha := "abc123"

	// Track which repo path the server receives (use mutex to avoid data race)
	var mu sync.Mutex
	var receivedRepo string

	client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodGet, "/api/jobs")

		repo := r.URL.Query().Get("repo")
		mu.Lock()
		receivedRepo = repo
		mu.Unlock()

		// Return job if repo matches main repo path (normalize for comparison)
		normalizedReceived := repo
		if resolved, err := filepath.EvalSymlinks(repo); err == nil {
			normalizedReceived = resolved
		}
		if normalizedReceived == mainRepo {
			json.NewEncoder(w).Encode(map[string]any{
				"jobs": []storage.ReviewJob{
					{ID: 1, GitRef: sha, RepoPath: mainRepo, Status: storage.JobStatusDone},
				},
			})
			return
		}

		json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{}})
	})

	t.Run("worktree path normalized to main repo", func(t *testing.T) {
		// Query using worktree path - should be normalized to main repo path
		job, err := client.FindJobForCommit(worktreeDir, sha)
		if err != nil {
			t.Fatalf("FindJobForCommit failed: %v", err)
		}

		// Verify the server received a path (not worktree path)
		mu.Lock()
		gotRepo := receivedRepo
		mu.Unlock()
		if gotRepo == "" {
			t.Error("expected server to receive a repo path, got empty")
		}
		if gotRepo == worktreeDir {
			t.Errorf("expected server to NOT receive worktree path %q", worktreeDir)
		}

		// Should find the job
		if job == nil {
			t.Error("expected to find job, got nil")
		} else if job.ID != 1 {
			t.Errorf("expected job ID 1, got %d", job.ID)
		}
	})

	t.Run("main repo path works directly", func(t *testing.T) {
		// Query using main repo path should also work
		job, err := client.FindJobForCommit(mainRepo, sha)
		if err != nil {
			t.Fatalf("FindJobForCommit failed: %v", err)
		}

		if job == nil {
			t.Error("expected to find job, got nil")
		}
	})
}

func TestFindJobForCommitFallback(t *testing.T) {
	// Test fallback behavior when primary query returns no results
	sha := "abc123"
	mainRepo := t.TempDir() // Use real temp dir for cross-platform path normalization

	var primaryCalled, fallbackCalled int32

	client := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		repo := r.URL.Query().Get("repo")

		if repo != "" {
			atomic.StoreInt32(&primaryCalled, 1)
			// Return empty to trigger fallback
			json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{}})
			return
		}

		// Fallback query (no repo filter)
		atomic.StoreInt32(&fallbackCalled, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"jobs": []storage.ReviewJob{
				{ID: 1, GitRef: sha, RepoPath: mainRepo, Status: storage.JobStatusDone},
			},
		})
	})

	// Use the same temp dir so paths match after normalization
	job, err := client.FindJobForCommit(mainRepo, sha)
	if err != nil {
		t.Fatalf("FindJobForCommit failed: %v", err)
	}

	if atomic.LoadInt32(&primaryCalled) == 0 {
		t.Error("expected primary query to be called")
	}
	if atomic.LoadInt32(&fallbackCalled) == 0 {
		t.Error("expected fallback query to be called")
	}

	// Fallback should match because normalized path equals job.RepoPath
	if job == nil {
		t.Error("expected to find job via fallback")
	} else if job.ID != 1 {
		t.Errorf("expected job ID 1, got %d", job.ID)
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

				if got := r.URL.Query().Get("git_ref"); got != "abc123..def456" {
					t.Errorf("expected git_ref=abc123..def456, got %q", got)
				}
				expectedRepo, _ := filepath.Abs("/test/repo")
				if got := r.URL.Query().Get("repo"); got != expectedRepo {
					t.Errorf("expected repo=%q, got %q", expectedRepo, got)
				}

				status := r.URL.Query().Get("status")
				mu.Lock()
				requestedStatuses = append(requestedStatuses, status)
				mu.Unlock()

				jobs, ok := tc.mockResponses[status]
				if !ok {
					jobs = []storage.ReviewJob{}
				}
				json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
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
