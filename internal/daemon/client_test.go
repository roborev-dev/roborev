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

	"github.com/wesm/roborev/internal/storage"
)

func TestHTTPClientAddResponse(t *testing.T) {
	var received map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/respond" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)
	if err := client.AddResponse(42, "test-agent", "Fixed the issue"); err != nil {
		t.Fatalf("AddResponse failed: %v", err)
	}

	if received["job_id"].(float64) != 42 {
		t.Errorf("expected job_id 42, got %v", received["job_id"])
	}
	if received["responder"] != "test-agent" {
		t.Errorf("expected responder test-agent, got %v", received["responder"])
	}
	if received["response"] != "Fixed the issue" {
		t.Errorf("expected response to match, got %v", received["response"])
	}
}

func TestHTTPClientMarkReviewAddressed(t *testing.T) {
	var received map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review/address" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)
	if err := client.MarkReviewAddressed(99); err != nil {
		t.Fatalf("MarkReviewAddressed failed: %v", err)
	}

	if received["review_id"].(float64) != 99 {
		t.Errorf("expected review_id 99, got %v", received["review_id"])
	}
	if received["addressed"] != true {
		t.Errorf("expected addressed true, got %v", received["addressed"])
	}
}

func TestHTTPClientWaitForReviewUsesJobID(t *testing.T) {
	var reviewCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/jobs" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)
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
	tmpDir := t.TempDir()
	// Resolve symlinks (macOS /var -> /private/var)
	if resolved, err := filepath.EvalSymlinks(tmpDir); err == nil {
		tmpDir = resolved
	}
	mainRepo := filepath.Join(tmpDir, "main")
	worktreeDir := filepath.Join(tmpDir, "worktree")

	// Initialize main repo
	if err := os.MkdirAll(mainRepo, 0755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(mainRepo, "init")
	runGit(mainRepo, "config", "user.email", "test@test.com")
	runGit(mainRepo, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(mainRepo, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "add", ".")
	runGit(mainRepo, "commit", "-m", "initial")

	// Create worktree
	runGit(mainRepo, "worktree", "add", worktreeDir, "HEAD")

	sha := "abc123"

	// Track which repo path the server receives (use mutex to avoid data race)
	var mu sync.Mutex
	var receivedRepo string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

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
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []storage.ReviewJob{
					{ID: 1, GitRef: sha, RepoPath: mainRepo, Status: storage.JobStatusDone},
				},
			})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)

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
	mainRepo := "/some/repo"

	var primaryCalled, fallbackCalled int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		repo := r.URL.Query().Get("repo")

		if repo != "" {
			atomic.StoreInt32(&primaryCalled, 1)
			// Return empty to trigger fallback
			json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
			return
		}

		// Fallback query (no repo filter)
		atomic.StoreInt32(&fallbackCalled, 1)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jobs": []storage.ReviewJob{
				{ID: 1, GitRef: sha, RepoPath: mainRepo, Status: storage.JobStatusDone},
			},
		})
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)

	// Use a non-git path so GetMainRepoRoot fails and we test the fallback path matching
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
