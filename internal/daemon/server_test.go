package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	gitpkg "github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// safeRecorder wraps httptest.ResponseRecorder with mutex protection for concurrent access
type safeRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newSafeRecorder() *safeRecorder {
	return &safeRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (s *safeRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Write(p)
}

func (s *safeRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResponseRecorder.WriteHeader(code)
}

func (s *safeRecorder) Header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Header()
}

func (s *safeRecorder) bodyString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Body.String()
}

// waitForSubscriberIncrease polls until subscriber count increases from initialCount
func waitForSubscriberIncrease(b Broadcaster, initialCount int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b.SubscriberCount() > initialCount {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// waitForEvents polls until the response body contains at least minEvents newline-delimited events
func waitForEvents(w *safeRecorder, minEvents int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body := w.bodyString()
		count := strings.Count(body, "\n")
		if count >= minEvents {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// newTestServer creates a Server with a test DB and default config.
// Returns the server, DB (for seeding/assertions), and temp directory.
// setJobStatus is a test helper to update a job's status.
func setJobStatus(t *testing.T, db *storage.DB, jobID int64, status storage.JobStatus) {
	t.Helper()
	var query string
	switch status {
	case storage.JobStatusRunning:
		query = `UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`
	case storage.JobStatusDone:
		query = `UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`
	default:
		t.Fatalf("unsupported status in helper: %v", status)
	}
	res, err := db.Exec(query, jobID)
	if err != nil {
		t.Fatalf("failed to set job status: %v", err)
	}
	if rows, err := res.RowsAffected(); err != nil {
		t.Fatalf("failed to get rows affected: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected 1 row affected when setting job status, got %d", rows)
	}
}

// createTestJob is a helper to reduce boilerplate for creating a test repo, commit, and job.
func createTestJob(t *testing.T, db *storage.DB, dir, gitRef, agent string) *storage.ReviewJob {
	t.Helper()
	repo, err := db.GetOrCreateRepo(dir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, gitRef, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: gitRef, Agent: agent})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	return job
}

// testInvalidIDParsing is a generic helper to test invalid ID query parameters.
func testInvalidIDParsing(t *testing.T, handler http.HandlerFunc, urlTemplate string) {
	t.Helper()
	tests := []string{"abc", "10abc", "1.5"}
	for _, invalidID := range tests {
		t.Run("invalid_id_"+invalidID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf(urlTemplate, invalidID), nil)
			w := httptest.NewRecorder()
			handler(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected status %d for id %s, got %d", http.StatusBadRequest, invalidID, w.Code)
			}
		})
	}
}

func newTestServer(t *testing.T) (*Server, *storage.DB, string) {
	t.Helper()
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")
	return server, db, tmpDir
}

func TestNewServerAllowUnsafeAgents(t *testing.T) {
	boolTrue := true
	boolFalse := false

	t.Run("config nil keeps agent state false", func(t *testing.T) {
		// Save and restore global state
		prev := agent.AllowUnsafeAgents()
		t.Cleanup(func() { agent.SetAllowUnsafeAgents(prev) })

		// Pre-set to true to verify it gets reset
		agent.SetAllowUnsafeAgents(true)

		db, _ := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		cfg.AllowUnsafeAgents = nil // not set

		_ = NewServer(db, cfg, "")

		if agent.AllowUnsafeAgents() {
			t.Error("expected AllowUnsafeAgents to be false when config is nil")
		}
	})

	t.Run("config false sets agent state false", func(t *testing.T) {
		prev := agent.AllowUnsafeAgents()
		t.Cleanup(func() { agent.SetAllowUnsafeAgents(prev) })

		// Pre-set to true to verify it gets reset
		agent.SetAllowUnsafeAgents(true)

		db, _ := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		cfg.AllowUnsafeAgents = &boolFalse

		_ = NewServer(db, cfg, "")

		if agent.AllowUnsafeAgents() {
			t.Error("expected AllowUnsafeAgents to be false when config is false")
		}
	})

	t.Run("config true sets agent state true", func(t *testing.T) {
		prev := agent.AllowUnsafeAgents()
		t.Cleanup(func() { agent.SetAllowUnsafeAgents(prev) })

		// Pre-set to false to verify it gets set
		agent.SetAllowUnsafeAgents(false)

		db, _ := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		cfg.AllowUnsafeAgents = &boolTrue

		_ = NewServer(db, cfg, "")

		if !agent.AllowUnsafeAgents() {
			t.Error("expected AllowUnsafeAgents to be true when config is true")
		}
	})
}

// seedRepoWithJobs creates a repo and enqueues a number of jobs with predictable SHAs.
// shaPrefix is used to generate SHAs like "{prefix}sha{a,b,c...}".
func seedRepoWithJobs(t *testing.T, db *storage.DB, repoPath string, jobCount int, shaPrefix string) (*storage.Repo, []*storage.ReviewJob) {
	t.Helper()
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	var jobs []*storage.ReviewJob
	for i := range jobCount {
		sha := fmt.Sprintf("%ssha%c", shaPrefix, 'a'+i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Agent: "test"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		jobs = append(jobs, job)
	}
	return repo, jobs
}

// setJobBranch directly updates a job's branch in the DB (for test setup).
func setJobBranch(t *testing.T, db *storage.DB, jobID int64, branch string) {
	t.Helper()
	if _, err := db.Exec("UPDATE review_jobs SET branch = ? WHERE id = ?", branch, jobID); err != nil {
		t.Fatalf("setJobBranch failed: %v", err)
	}
}

func TestHandleListRepos(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	t.Run("empty database", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		// repos can be nil when empty
		var reposLen int
		if repos, ok := response["repos"].([]any); ok && repos != nil {
			reposLen = len(repos)
		}
		totalCount := int(response["total_count"].(float64))

		if reposLen != 0 {
			t.Errorf("Expected 0 repos, got %d", reposLen)
		}
		if totalCount != 0 {
			t.Errorf("Expected total_count 0, got %d", totalCount)
		}
	})

	// Create repos and jobs
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	t.Run("repos with jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		repos := response["repos"].([]any)
		totalCount := int(response["total_count"].(float64))

		if len(repos) != 2 {
			t.Errorf("Expected 2 repos, got %d", len(repos))
		}
		if totalCount != 5 {
			t.Errorf("Expected total_count 5, got %d", totalCount)
		}

		// Verify individual repo counts
		repoMap := make(map[string]int)
		for _, r := range repos {
			repoObj := r.(map[string]any)
			repoMap[repoObj["name"].(string)] = int(repoObj["count"].(float64))
		}

		if repoMap["repo1"] != 3 {
			t.Errorf("Expected repo1 count 3, got %d", repoMap["repo1"])
		}
		if repoMap["repo2"] != 2 {
			t.Errorf("Expected repo2 count 2, got %d", repoMap["repo2"])
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for POST, got %d", w.Code)
		}
	})
}

func TestHandleListReposWithBranchFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	// Set branches: repo1 jobs 1,2 = main, job 3 = feature; repo2 jobs 4,5 = main
	setJobBranch(t, db, 1, "main")
	setJobBranch(t, db, 2, "main")
	setJobBranch(t, db, 4, "main")
	setJobBranch(t, db, 5, "main")
	setJobBranch(t, db, 3, "feature")

	t.Run("filter by main branch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=main", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		repos := response["repos"].([]any)
		totalCount := int(response["total_count"].(float64))

		if len(repos) != 2 {
			t.Errorf("Expected 2 repos with main branch, got %d", len(repos))
		}
		if totalCount != 4 {
			t.Errorf("Expected total_count 4, got %d", totalCount)
		}
	})

	t.Run("filter by feature branch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=feature", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		repos := response["repos"].([]any)
		totalCount := int(response["total_count"].(float64))

		if len(repos) != 1 {
			t.Errorf("Expected 1 repo with feature branch, got %d", len(repos))
		}
		if totalCount != 1 {
			t.Errorf("Expected total_count 1, got %d", totalCount)
		}
	})

	t.Run("nonexistent branch returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=nonexistent", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		totalCount := int(response["total_count"].(float64))
		if totalCount != 0 {
			t.Errorf("Expected total_count 0 for nonexistent branch, got %d", totalCount)
		}
	})
}

func TestHandleListBranches(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	// Set branches: jobs 1,2,4 = main, job 3 = feature, job 5 = no branch
	setJobBranch(t, db, 1, "main")
	setJobBranch(t, db, 2, "main")
	setJobBranch(t, db, 4, "main")
	setJobBranch(t, db, 3, "feature")
	// job 5 left empty

	t.Run("list all branches", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/branches", nil)
		w := httptest.NewRecorder()

		server.handleListBranches(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		branches := response["branches"].([]any)
		totalCount := int(response["total_count"].(float64))
		nullsRemaining := int(response["nulls_remaining"].(float64))

		if len(branches) != 3 {
			t.Errorf("Expected 3 branches, got %d", len(branches))
		}
		if totalCount != 5 {
			t.Errorf("Expected total_count 5, got %d", totalCount)
		}
		if nullsRemaining != 1 {
			t.Errorf("Expected nulls_remaining 1, got %d", nullsRemaining)
		}
	})

	t.Run("filter by repo", func(t *testing.T) {
		repoPath := filepath.Join(tmpDir, "repo1")
		req := httptest.NewRequest(http.MethodGet, "/api/branches?repo="+repoPath, nil)
		w := httptest.NewRecorder()

		server.handleListBranches(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		branches := response["branches"].([]any)
		totalCount := int(response["total_count"].(float64))

		if len(branches) != 2 {
			t.Errorf("Expected 2 branches for repo1, got %d", len(branches))
		}
		if totalCount != 3 {
			t.Errorf("Expected total_count 3 for repo1, got %d", totalCount)
		}
	})

	t.Run("filter by multiple repos", func(t *testing.T) {
		repo1Path := filepath.Join(tmpDir, "repo1")
		repo2Path := filepath.Join(tmpDir, "repo2")
		req := httptest.NewRequest(http.MethodGet, "/api/branches?repo="+repo1Path+"&repo="+repo2Path, nil)
		w := httptest.NewRecorder()

		server.handleListBranches(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		branches := response["branches"].([]any)
		totalCount := int(response["total_count"].(float64))

		if len(branches) != 3 {
			t.Errorf("Expected 3 branches for both repos, got %d", len(branches))
		}
		if totalCount != 5 {
			t.Errorf("Expected total_count 5 for both repos, got %d", totalCount)
		}
	})

	t.Run("empty repo param treated as no filter", func(t *testing.T) {
		// Empty repo param should be ignored, returning all branches
		req := httptest.NewRequest(http.MethodGet, "/api/branches?repo=", nil)
		w := httptest.NewRecorder()

		server.handleListBranches(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		branches := response["branches"].([]any)
		totalCount := int(response["total_count"].(float64))

		// Should return all 3 branches and 5 jobs, not zero
		if len(branches) != 3 {
			t.Errorf("Expected 3 branches (empty repo = no filter), got %d", len(branches))
		}
		if totalCount != 5 {
			t.Errorf("Expected total_count 5 (empty repo = no filter), got %d", totalCount)
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/branches", nil)
		w := httptest.NewRecorder()

		server.handleListBranches(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for POST, got %d", w.Code)
		}
	})
}

func TestHandleListJobsWithFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	repo1, _ := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	t.Run("no filter returns all jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs, got %d", len(response.Jobs))
		}
	})

	t.Run("repo filter returns only matching jobs", func(t *testing.T) {
		// Filter by root_path (not name) since repos with same name could exist at different paths
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?repo="+url.QueryEscape(repo1.RootPath), nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 3 {
			t.Errorf("Expected 3 jobs for repo1, got %d", len(response.Jobs))
		}

		// Verify all jobs are from repo1
		for _, job := range response.Jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("limit parameter works", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=2", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 2 {
			t.Errorf("Expected 2 jobs with limit=2, got %d", len(response.Jobs))
		}
	})

	t.Run("limit=0 returns all jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=0", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs with limit=0 (no limit), got %d", len(response.Jobs))
		}
	})

	t.Run("repo filter with limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?repo="+url.QueryEscape(repo1.RootPath)+"&limit=2", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 2 {
			t.Errorf("Expected 2 jobs with repo filter and limit=2, got %d", len(response.Jobs))
		}

		// Verify all jobs are from repo1
		for _, job := range response.Jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("negative limit treated as unlimited", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=-1", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Negative clamped to 0 (unlimited), should return all 5 jobs
		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs with limit=-1 (clamped to unlimited), got %d", len(response.Jobs))
		}
	})

	t.Run("very large limit capped to max", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=999999", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Large limit capped to 10000, but we only have 5 jobs
		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs (all available), got %d", len(response.Jobs))
		}
	})

	t.Run("invalid limit uses default", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=abc", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Invalid limit uses default (50), we have 5 jobs
		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs with invalid limit (uses default), got %d", len(response.Jobs))
		}
	})
}

func TestHandleStatus(t *testing.T) {
	server, _, _ := newTestServer(t)

	t.Run("returns status with version", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var status storage.DaemonStatus
		testutil.DecodeJSON(t, w, &status)

		// Version should be set (non-empty)
		if status.Version == "" {
			t.Error("Expected Version to be set in status response")
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for POST, got %d", w.Code)
		}
	})

	t.Run("returns max_workers from pool not config", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		var status storage.DaemonStatus
		testutil.DecodeJSON(t, w, &status)

		// MaxWorkers should match the pool size (config default), not a potentially reloaded config value
		expectedWorkers := config.DefaultConfig().MaxWorkers
		if status.MaxWorkers != expectedWorkers {
			t.Errorf("Expected MaxWorkers %d from pool, got %d", expectedWorkers, status.MaxWorkers)
		}
	})

	t.Run("config_reloaded_at empty initially", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		var status storage.DaemonStatus
		testutil.DecodeJSON(t, w, &status)

		// ConfigReloadedAt should be empty when no reload has occurred
		if status.ConfigReloadedAt != "" {
			t.Errorf("Expected ConfigReloadedAt to be empty initially, got %q", status.ConfigReloadedAt)
		}
	})
}

func TestHandleCancelJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a repo and job
	job := createTestJob(t, db, tmpDir, "canceltest", "test")

	t.Run("cancel queued job", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusCanceled {
			t.Errorf("Expected status 'canceled', got '%s'", updated.Status)
		}
	})

	t.Run("cancel already canceled job fails", func(t *testing.T) {
		// Job is already canceled from previous test
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for already canceled job, got %d", w.Code)
		}
	})

	t.Run("cancel nonexistent job fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: 99999})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for nonexistent job, got %d", w.Code)
		}
	})

	t.Run("cancel with missing job_id fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", map[string]any{})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400 for missing job_id, got %d", w.Code)
		}
	})

	t.Run("cancel with wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/job/cancel", nil)
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for GET, got %d", w.Code)
		}
	})

	t.Run("cancel running job", func(t *testing.T) {
		// Create a new job and claim it
		commit2, err := db.GetOrCreateCommit(job.RepoID, "cancelrunning", "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job2, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: job.RepoID, CommitID: commit2.ID, GitRef: "cancelrunning", Agent: "test"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		if _, err := db.ClaimJob("worker-1"); err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: job2.ID})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job2.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusCanceled {
			t.Errorf("Expected status 'canceled', got '%s'", updated.Status)
		}
	})
}

func TestListJobsPagination(t *testing.T) {
	server, db, _ := newTestServer(t)

	// Create test repo and 10 jobs
	seedRepoWithJobs(t, db, "/test/repo", 10, "")

	t.Run("has_more true when more jobs exist", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?limit=5", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 5 {
			t.Errorf("Expected 5 jobs, got %d", len(result.Jobs))
		}
		if !result.HasMore {
			t.Error("Expected has_more=true when more jobs exist")
		}
	})

	t.Run("has_more false when no more jobs", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?limit=50", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 10 {
			t.Errorf("Expected 10 jobs, got %d", len(result.Jobs))
		}
		if result.HasMore {
			t.Error("Expected has_more=false when all jobs returned")
		}
	})

	t.Run("offset skips jobs", func(t *testing.T) {
		// First page
		req1 := httptest.NewRequest("GET", "/api/jobs?limit=3&offset=0", nil)
		w1 := httptest.NewRecorder()
		server.handleListJobs(w1, req1)

		var result1 struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w1, &result1)

		// Second page
		req2 := httptest.NewRequest("GET", "/api/jobs?limit=3&offset=3", nil)
		w2 := httptest.NewRecorder()
		server.handleListJobs(w2, req2)

		var result2 struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w2, &result2)

		// Ensure no overlap
		for _, j1 := range result1.Jobs {
			for _, j2 := range result2.Jobs {
				if j1.ID == j2.ID {
					t.Errorf("Job %d appears in both pages", j1.ID)
				}
			}
		}
	})

	t.Run("offset ignored when limit=0", func(t *testing.T) {
		// limit=0 means unlimited, offset should be ignored
		req := httptest.NewRequest("GET", "/api/jobs?limit=0&offset=5", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		// Should return all 10 jobs since offset is ignored with limit=0
		if len(result.Jobs) != 10 {
			t.Errorf("Expected 10 jobs (offset ignored with limit=0), got %d", len(result.Jobs))
		}
	})
}

func TestListJobsWithGitRefFilter(t *testing.T) {
	server, db, _ := newTestServer(t)

	// Create repo and jobs with different git refs
	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	refs := []string{"abc123", "def456", "abc123..def456"}
	for _, ref := range refs {
		commit, _ := db.GetOrCreateCommit(repo.ID, ref, "A", "S", time.Now())
		db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: ref, Agent: "codex"})
	}

	t.Run("git_ref filter returns matching job", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?git_ref=abc123", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 1 {
			t.Errorf("Expected 1 job, got %d", len(result.Jobs))
		}
		if len(result.Jobs) > 0 && result.Jobs[0].GitRef != "abc123" {
			t.Errorf("Expected GitRef 'abc123', got '%s'", result.Jobs[0].GitRef)
		}
	})

	t.Run("git_ref filter with no match returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?git_ref=nonexistent", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 0 {
			t.Errorf("Expected 0 jobs, got %d", len(result.Jobs))
		}
	})

	t.Run("git_ref filter with range ref", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?git_ref="+url.QueryEscape("abc123..def456"), nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 1 {
			t.Errorf("Expected 1 job with range ref, got %d", len(result.Jobs))
		}
	})
}

func TestHandleListJobsAddressedFilter(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	repo, _ := db.GetOrCreateRepo("/tmp/repo-addr-filter")
	commit, _ := db.GetOrCreateCommit(repo.ID, "aaa", "A", "S", time.Now())
	job1, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "aaa", Branch: "main", Agent: "codex"})
	db.ClaimJob("w")
	db.CompleteJob(job1.ID, "codex", "", "output1")

	commit2, _ := db.GetOrCreateCommit(repo.ID, "bbb", "A", "S2", time.Now())
	job2, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit2.ID, GitRef: "bbb", Branch: "main", Agent: "codex"})
	db.ClaimJob("w")
	db.CompleteJob(job2.ID, "codex", "", "output2")
	db.MarkReviewAddressedByJobID(job2.ID, true)

	t.Run("addressed=false", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?addressed=false", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)
		if len(result.Jobs) != 1 {
			t.Errorf("Expected 1 unaddressed job, got %d", len(result.Jobs))
		}
	})

	t.Run("branch filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?branch=main", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)
		if len(result.Jobs) != 2 {
			t.Errorf("Expected 2 jobs on main, got %d", len(result.Jobs))
		}
	})
}

// startStreamHandler starts the stream handler in a goroutine and waits for subscription.
// Returns a cancel function, the recorder, and a done channel.
func startStreamHandler(t *testing.T, server *Server, url string) (context.CancelFunc, *safeRecorder, chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, url, nil).WithContext(ctx)
	w := newSafeRecorder()

	initialCount := server.broadcaster.SubscriberCount()

	// Run handler in goroutine
	done := make(chan struct{})
	go func() {
		server.handleStreamEvents(w, req)
		close(done)
	}()

	// Wait for handler to subscribe
	if !waitForSubscriberIncrease(server.broadcaster, initialCount, time.Second) {
		cancel() // Clean up context if timeout
		t.Fatal("Timed out waiting for subscriber")
	}

	return cancel, w, done
}

func TestHandleStreamEvents(t *testing.T) {
	server, _, _ := newTestServer(t)

	t.Run("returns correct headers", func(t *testing.T) {
		cancel, w, done := startStreamHandler(t, server, "/api/stream/events")

		// Cancel request to stop the handler
		cancel()
		<-done

		// Check headers
		if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
			t.Errorf("Expected Content-Type 'application/x-ndjson', got '%s'", ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Expected Cache-Control 'no-cache', got '%s'", cc)
		}
		if conn := w.Header().Get("Connection"); conn != "keep-alive" {
			t.Errorf("Expected Connection 'keep-alive', got '%s'", conn)
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/stream/events", nil)
		w := httptest.NewRecorder()

		server.handleStreamEvents(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for POST, got %d", w.Code)
		}
	})

	t.Run("streams events as JSONL", func(t *testing.T) {
		cancel, w, done := startStreamHandler(t, server, "/api/stream/events")

		// Broadcast test events
		event1 := Event{
			Type:     "review.completed",
			TS:       time.Now(),
			JobID:    1,
			Repo:     "/test/repo1",
			RepoName: "repo1",
			SHA:      "abc123",
			Agent:    "test",
			Verdict:  "pass",
		}
		event2 := Event{
			Type:     "review.failed",
			TS:       time.Now(),
			JobID:    2,
			Repo:     "/test/repo2",
			RepoName: "repo2",
			SHA:      "def456",
			Agent:    "test",
			Error:    "agent timeout",
		}
		server.broadcaster.Broadcast(event1)
		server.broadcaster.Broadcast(event2)

		// Wait for events to be written
		if !waitForEvents(w, 2, time.Second) {
			cancel()
			t.Fatal("Timed out waiting for events")
		}

		// Cancel and wait for handler to finish
		cancel()
		<-done

		// Parse JSONL output (safe to read body after handler done)
		body := w.bodyString()
		scanner := bufio.NewScanner(bytes.NewBufferString(body))
		var events []Event
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var ev Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("Failed to parse JSONL line: %v, line: %s", err, line)
			}
			events = append(events, ev)
		}

		if len(events) != 2 {
			t.Fatalf("Expected 2 events, got %d", len(events))
		}

		if events[0].Type != "review.completed" || events[0].JobID != 1 {
			t.Errorf("First event mismatch: %+v", events[0])
		}
		if events[0].RepoName != "repo1" {
			t.Errorf("Expected RepoName 'repo1', got '%s'", events[0].RepoName)
		}
		if events[0].Verdict != "pass" {
			t.Errorf("Expected Verdict 'pass', got '%s'", events[0].Verdict)
		}
		if events[1].Type != "review.failed" || events[1].JobID != 2 {
			t.Errorf("Second event mismatch: %+v", events[1])
		}
		if events[1].Error != "agent timeout" {
			t.Errorf("Expected Error 'agent timeout', got '%s'", events[1].Error)
		}
	})

	t.Run("all event types are supported", func(t *testing.T) {
		cancel, w, done := startStreamHandler(t, server, "/api/stream/events")

		// Broadcast all event types
		server.broadcaster.Broadcast(Event{
			Type:     "review.started",
			JobID:    1,
			Repo:     "/test/repo",
			RepoName: "repo",
			SHA:      "abc",
			Agent:    "codex",
		})
		server.broadcaster.Broadcast(Event{
			Type:     "review.completed",
			JobID:    1,
			Repo:     "/test/repo",
			RepoName: "repo",
			SHA:      "abc",
			Agent:    "codex",
			Verdict:  "pass",
		})
		server.broadcaster.Broadcast(Event{
			Type:     "review.failed",
			JobID:    2,
			Repo:     "/test/repo",
			RepoName: "repo",
			SHA:      "def",
			Agent:    "codex",
			Error:    "connection refused",
		})
		server.broadcaster.Broadcast(Event{
			Type:     "review.canceled",
			JobID:    3,
			Repo:     "/test/repo",
			RepoName: "repo",
			SHA:      "ghi",
			Agent:    "codex",
		})

		if !waitForEvents(w, 4, time.Second) {
			cancel()
			t.Fatal("Timed out waiting for events")
		}
		cancel()
		<-done

		body := w.bodyString()
		lines := strings.Split(strings.TrimSpace(body), "\n")

		// Parse into maps to check actual key presence (not just empty values)
		var rawEvents []map[string]any
		for _, line := range lines {
			if line == "" {
				continue
			}
			var raw map[string]any
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				t.Fatalf("Failed to parse JSONL: %v", err)
			}
			rawEvents = append(rawEvents, raw)
		}

		expectedTypes := []string{"review.started", "review.completed", "review.failed", "review.canceled"}
		if len(rawEvents) != len(expectedTypes) {
			t.Fatalf("Expected %d events, got %d", len(expectedTypes), len(rawEvents))
		}
		for i, exp := range expectedTypes {
			if rawEvents[i]["type"] != exp {
				t.Errorf("Event %d: expected type '%s', got '%v'", i, exp, rawEvents[i]["type"])
			}
		}

		// Verify error field is present (key exists) for failed event and absent for others
		for i, raw := range rawEvents {
			eventType, ok := raw["type"].(string)
			if !ok {
				t.Fatalf("Event %d: 'type' key missing or not a string", i)
			}
			errorVal, hasError := raw["error"]
			if eventType == "review.failed" {
				if !hasError {
					t.Error("Expected 'error' key present in review.failed event")
				} else if errorStr, ok := errorVal.(string); !ok || errorStr != "connection refused" {
					t.Errorf("Expected error 'connection refused', got %v", errorVal)
				}
			} else {
				if hasError {
					t.Errorf("Unexpected 'error' key in %s event", eventType)
				}
			}
		}

		// Verify verdict field is present (key exists) only in completed event
		for i, raw := range rawEvents {
			eventType, ok := raw["type"].(string)
			if !ok {
				t.Fatalf("Event %d: 'type' key missing or not a string", i)
			}
			verdictVal, hasVerdict := raw["verdict"]
			if eventType == "review.completed" {
				if !hasVerdict {
					t.Error("Expected 'verdict' key present in review.completed event")
				} else if verdictStr, ok := verdictVal.(string); !ok || verdictStr != "pass" {
					t.Errorf("Expected verdict 'pass', got %v", verdictVal)
				}
			} else {
				if hasVerdict {
					t.Errorf("Unexpected 'verdict' key in %s event", eventType)
				}
			}
		}
	})

	t.Run("omitempty fields are excluded when empty", func(t *testing.T) {
		cancel, w, done := startStreamHandler(t, server, "/api/stream/events")

		// Event with no optional fields set
		server.broadcaster.Broadcast(Event{
			Type:     "review.started",
			JobID:    1,
			Repo:     "/test/repo",
			RepoName: "repo",
			SHA:      "abc",
			// Agent, Verdict, Error all empty
		})

		if !waitForEvents(w, 1, time.Second) {
			cancel()
			t.Fatal("Timed out waiting for events")
		}
		cancel()
		<-done

		body := w.bodyString()
		// Check that empty fields are not in the JSON output
		if bytes.Contains([]byte(body), []byte(`"verdict"`)) {
			t.Error("Expected 'verdict' to be omitted when empty")
		}
		if bytes.Contains([]byte(body), []byte(`"error"`)) {
			t.Error("Expected 'error' to be omitted when empty")
		}
	})

	t.Run("repo filter only sends matching events", func(t *testing.T) {
		// Filter for repo1 only
		cancel, w, done := startStreamHandler(t, server, "/api/stream/events?repo="+url.QueryEscape("/test/repo1"))

		// Broadcast events for different repos
		server.broadcaster.Broadcast(Event{
			Type:     "review.completed",
			JobID:    10,
			Repo:     "/test/repo1",
			RepoName: "repo1",
			SHA:      "aaa",
		})
		server.broadcaster.Broadcast(Event{
			Type:     "review.completed",
			JobID:    11,
			Repo:     "/test/repo2", // Should be filtered out
			RepoName: "repo2",
			SHA:      "bbb",
		})
		server.broadcaster.Broadcast(Event{
			Type:     "review.completed",
			JobID:    12,
			Repo:     "/test/repo1",
			RepoName: "repo1",
			SHA:      "ccc",
		})

		// Wait for events to be written (expect 2 for repo1, repo2 event is filtered)
		if !waitForEvents(w, 2, time.Second) {
			cancel()
			t.Fatal("Timed out waiting for events")
		}

		// Cancel and wait
		cancel()
		<-done

		// Parse JSONL output
		body := w.bodyString()
		scanner := bufio.NewScanner(bytes.NewBufferString(body))
		var events []Event
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var ev Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("Failed to parse JSONL line: %v", err)
			}
			events = append(events, ev)
		}

		// Should only have 2 events (repo1), not the repo2 event
		if len(events) != 2 {
			t.Fatalf("Expected 2 events (filtered), got %d", len(events))
		}

		for _, ev := range events {
			if ev.Repo != "/test/repo1" {
				t.Errorf("Expected all events for /test/repo1, got event for %s", ev.Repo)
			}
		}
	})

	t.Run("special characters in repo filter are handled", func(t *testing.T) {
		repoPath := "/test/my repo with spaces"
		cancel, w, done := startStreamHandler(t, server, "/api/stream/events?repo="+url.QueryEscape(repoPath))

		// Broadcast event for the repo with spaces
		server.broadcaster.Broadcast(Event{
			Type:     "review.completed",
			JobID:    20,
			Repo:     repoPath,
			RepoName: "my repo with spaces",
			SHA:      "xyz",
		})
		// Broadcast event for different repo (should be filtered)
		server.broadcaster.Broadcast(Event{
			Type:     "review.completed",
			JobID:    21,
			Repo:     "/test/other",
			RepoName: "other",
			SHA:      "zzz",
		})

		// Wait for the event that passes filter
		if !waitForEvents(w, 1, time.Second) {
			cancel()
			t.Fatal("Timed out waiting for events")
		}
		cancel()
		<-done

		// Parse output
		body := w.bodyString()
		scanner := bufio.NewScanner(bytes.NewBufferString(body))
		var events []Event
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var ev Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("Failed to parse JSONL line: %v", err)
			}
			events = append(events, ev)
		}

		if len(events) != 1 {
			t.Fatalf("Expected 1 event for repo with spaces, got %d", len(events))
		}
		if events[0].Repo != repoPath {
			t.Errorf("Expected repo '%s', got '%s'", repoPath, events[0].Repo)
		}
	})
}

func TestHandleRerunJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a repo
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("rerun failed job", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-failed", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-failed", Agent: "test"})
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "some error")

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun canceled job", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-canceled", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-canceled", Agent: "test"})
		db.CancelJob(job.ID)

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun done job", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-done", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-done", Agent: "test"})
		// Claim and complete job
		var claimed *storage.ReviewJob
		for {
			claimed, _ = db.ClaimJob("worker-1")
			if claimed == nil {
				t.Fatal("No job to claim")
			}
			if claimed.ID == job.ID {
				break
			}
			db.CompleteJob(claimed.ID, "test", "prompt", "output")
		}
		db.CompleteJob(job.ID, "test", "prompt", "output")

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun queued job fails", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-queued", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-queued", Agent: "test"})

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for queued job, got %d", w.Code)
		}
	})

	t.Run("rerun nonexistent job fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: 99999})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for nonexistent job, got %d", w.Code)
		}
	})

	t.Run("rerun with missing job_id fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", map[string]any{})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400 for missing job_id, got %d", w.Code)
		}
	})

	t.Run("rerun with invalid method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/job/rerun", nil)
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for GET, got %d", w.Code)
		}
	})
}

func TestHandleRegisterRepo(t *testing.T) {
	t.Run("GET returns 405", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/api/repos/register", nil)
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected 405, got %d", w.Code)
		}
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("non-git path returns 400", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		plainDir := t.TempDir()
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{
			"repo_path": plainDir,
		})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("valid git repo returns 200 and persists", func(t *testing.T) {
		server, db, tmpDir := newTestServer(t)
		repoDir := filepath.Join(tmpDir, "testrepo")
		testutil.InitTestGitRepo(t, repoDir)

		// Add a remote so identity resolves to something meaningful
		remoteCmd := exec.Command("git", "-C", repoDir, "remote", "add", "origin", "https://github.com/test/testrepo.git")
		if out, err := remoteCmd.CombinedOutput(); err != nil {
			t.Fatalf("git remote add failed: %v\n%s", err, out)
		}

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{
			"repo_path": repoDir,
		})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var repo storage.Repo
		testutil.DecodeJSON(t, w, &repo)
		if repo.ID == 0 {
			t.Error("Expected non-zero repo ID")
		}
		if repo.Identity == "" {
			t.Error("Expected non-empty identity")
		}

		// Verify repo is in the DB
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		if len(repos) != 1 {
			t.Fatalf("Expected 1 repo in DB, got %d", len(repos))
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		server, db, tmpDir := newTestServer(t)
		repoDir := filepath.Join(tmpDir, "testrepo")
		testutil.InitTestGitRepo(t, repoDir)

		body := map[string]string{"repo_path": repoDir}

		// First call
		req1 := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", body)
		w1 := httptest.NewRecorder()
		server.handleRegisterRepo(w1, req1)
		if w1.Code != http.StatusOK {
			t.Fatalf("First call: expected 200, got %d: %s", w1.Code, w1.Body.String())
		}

		// Second call
		req2 := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", body)
		w2 := httptest.NewRecorder()
		server.handleRegisterRepo(w2, req2)
		if w2.Code != http.StatusOK {
			t.Fatalf("Second call: expected 200, got %d: %s", w2.Code, w2.Body.String())
		}

		// Still only one repo in DB
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		if len(repos) != 1 {
			t.Fatalf("Expected 1 repo in DB after two calls, got %d", len(repos))
		}
	})
}

func TestHandleEnqueueExcludedBranch(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "testrepo")
	testutil.InitTestGitRepo(t, repoDir)

	// Switch to excluded branch
	checkoutCmd := exec.Command("git", "-C", repoDir, "checkout", "-b", "wip-feature")
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout failed: %v\n%s", err, out)
	}

	// Create .roborev.toml with excluded_branches
	repoConfig := filepath.Join(repoDir, ".roborev.toml")
	configContent := `excluded_branches = ["wip-feature", "draft"]`
	if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write repo config: %v", err)
	}

	t.Run("enqueue on excluded branch returns skipped", func(t *testing.T) {
		reqData := EnqueueRequest{RepoPath: repoDir, GitRef: "HEAD", Agent: "test"}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for skipped enqueue, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		if skipped, ok := response["skipped"].(bool); !ok || !skipped {
			t.Errorf("Expected skipped=true, got %v", response)
		}

		if reason, ok := response["reason"].(string); !ok || !strings.Contains(reason, "wip-feature") {
			t.Errorf("Expected reason to mention branch name, got %v", response)
		}

		// Verify no job was created
		queued, _, _, _, _, _, _, _ := db.GetJobCounts()
		if queued != 0 {
			t.Errorf("Expected 0 queued jobs, got %d", queued)
		}
	})

	t.Run("enqueue on non-excluded branch succeeds", func(t *testing.T) {
		// Switch to a non-excluded branch
		checkoutCmd := exec.Command("git", "checkout", "-b", "feature-ok")
		checkoutCmd.Dir = repoDir
		if out, err := checkoutCmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout failed: %v\n%s", err, out)
		}

		reqData := EnqueueRequest{RepoPath: repoDir, GitRef: "HEAD", Agent: "test"}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("Expected status 201 for successful enqueue, got %d: %s", w.Code, w.Body.String())
		}

		// Verify job was created
		queued, _, _, _, _, _, _, _ := db.GetJobCounts()
		if queued != 1 {
			t.Errorf("Expected 1 queued job, got %d", queued)
		}
	})
}

func TestHandleEnqueueBranchFallback(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "testrepo")
	testutil.InitTestGitRepo(t, repoDir)

	// Switch to a named branch
	branchCmd := exec.Command("git", "-C", repoDir, "checkout", "-b", "my-feature")
	if out, err := branchCmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout failed: %v\n%s", err, out)
	}

	// Enqueue with empty branch field
	reqData := EnqueueRequest{
		RepoPath: repoDir,
		GitRef:   "HEAD",
		Agent:    "test",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
	w := httptest.NewRecorder()
	server.handleEnqueue(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var respJob storage.ReviewJob
	testutil.DecodeJSON(t, w, &respJob)

	// Verify the job has the detected branch, not empty
	job, err := db.GetJobByID(respJob.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Branch != "my-feature" {
		t.Errorf("expected branch %q, got %q", "my-feature", job.Branch)
	}
}

func TestHandleEnqueueBodySizeLimit(t *testing.T) {
	server, _, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "testrepo")
	testutil.InitTestGitRepo(t, repoDir)

	t.Run("rejects oversized request body", func(t *testing.T) {
		// Create a request body larger than the default limit (200KB + 50KB overhead)
		largeDiff := strings.Repeat("a", 300*1024) // 300KB
		reqData := EnqueueRequest{
			RepoPath:    repoDir,
			GitRef:      "dirty",
			Agent:       "test",
			DiffContent: largeDiff,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("Expected status 413, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		if errMsg, ok := response["error"].(string); !ok || !strings.Contains(errMsg, "too large") {
			t.Errorf("Expected error about body size, got %v", response)
		}
	})

	t.Run("rejects dirty review with empty diff_content", func(t *testing.T) {
		// git_ref="dirty" with empty diff_content should return a clear error
		reqData := EnqueueRequest{
			RepoPath: repoDir,
			GitRef:   "dirty",
			Agent:    "test",
			// diff_content intentionally omitted/empty
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]any
		testutil.DecodeJSON(t, w, &response)

		if errMsg, ok := response["error"].(string); !ok || !strings.Contains(errMsg, "diff_content required") {
			t.Errorf("Expected error about diff_content required, got %v", response)
		}
	})

	t.Run("accepts valid size dirty request", func(t *testing.T) {
		// Create a valid-sized diff (under 200KB)
		validDiff := strings.Repeat("a", 100*1024) // 100KB
		reqData := EnqueueRequest{
			RepoPath:    repoDir,
			GitRef:      "dirty",
			Agent:       "test",
			DiffContent: validDiff,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestHandleListJobsByID(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	_, jobs := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "testrepo"), 3, "repo1")
	job1ID := jobs[0].ID
	job2ID := jobs[1].ID
	job3ID := jobs[2].ID

	t.Run("fetches specific job by ID", func(t *testing.T) {
		// Request job 1 specifically
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/jobs?id=%d", job1ID), nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 1 {
			t.Errorf("Expected exactly 1 job, got %d", len(response.Jobs))
		}
		if response.Jobs[0].ID != job1ID {
			t.Errorf("Expected job ID %d, got %d", job1ID, response.Jobs[0].ID)
		}
	})

	t.Run("fetches middle job correctly", func(t *testing.T) {
		// Request job 2 specifically (the middle job)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/jobs?id=%d", job2ID), nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 1 {
			t.Errorf("Expected exactly 1 job, got %d", len(response.Jobs))
		}
		if response.Jobs[0].ID != job2ID {
			t.Errorf("Expected job ID %d, got %d", job2ID, response.Jobs[0].ID)
		}
	})

	t.Run("returns empty for non-existent job ID", func(t *testing.T) {
		// Request a job ID that doesn't exist
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?id=99999", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 0 {
			t.Errorf("Expected 0 jobs for non-existent ID, got %d", len(response.Jobs))
		}
	})

	t.Run("returns error for invalid job ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?id=notanumber", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("without id param returns all jobs", func(t *testing.T) {
		// Request without id param should return all jobs
		req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Should have all 3 jobs
		if len(response.Jobs) != 3 {
			t.Errorf("Expected 3 jobs, got %d", len(response.Jobs))
		}

		// Verify all job IDs are present (order may vary due to same-second timestamps)
		foundIDs := make(map[int64]bool)
		for _, job := range response.Jobs {
			foundIDs[job.ID] = true
		}
		if !foundIDs[job1ID] || !foundIDs[job2ID] || !foundIDs[job3ID] {
			t.Errorf("Expected jobs %d, %d, %d but found %v", job1ID, job2ID, job3ID, foundIDs)
		}
	})
}

func TestHandleEnqueuePromptJob(t *testing.T) {
	repoDir := t.TempDir()
	testutil.InitTestGitRepo(t, repoDir)

	server, _, _ := newTestServer(t)

	t.Run("enqueues prompt job successfully", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			CustomPrompt: "Explain this codebase",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if job.GitRef != "prompt" {
			t.Errorf("Expected git_ref 'prompt', got '%s'", job.GitRef)
		}
		if job.Agent != "test" {
			t.Errorf("Expected agent 'test', got '%s'", job.Agent)
		}
		if job.Status != storage.JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", job.Status)
		}
	})

	t.Run("git_ref prompt without custom_prompt is treated as branch review", func(t *testing.T) {
		// With no custom_prompt, git_ref="prompt" is treated as trying to review
		// a branch/commit named "prompt" (not a prompt job). This allows reviewing
		// branches literally named "prompt" without collision.
		reqData := EnqueueRequest{
			RepoPath: repoDir,
			GitRef:   "prompt",
			Agent:    "test",
			// no custom_prompt - should try to resolve "prompt" as a git ref
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		// Should fail because there's no branch named "prompt", not because
		// custom_prompt is missing
		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 (invalid commit), got %d: %s", w.Code, w.Body.String())
		}

		if strings.Contains(w.Body.String(), "custom_prompt required") {
			t.Errorf("Should NOT require custom_prompt for git_ref=prompt, got: %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "invalid commit") {
			t.Errorf("Expected 'invalid commit' error, got: %s", w.Body.String())
		}
	})

	t.Run("prompt job with reasoning level", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			Reasoning:    "fast",
			CustomPrompt: "Quick analysis",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if job.Reasoning != "fast" {
			t.Errorf("Expected reasoning 'fast', got '%s'", job.Reasoning)
		}
	})

	t.Run("prompt job with agentic flag", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			CustomPrompt: "Fix all bugs",
			Agentic:      true,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if !job.Agentic {
			t.Error("Expected Agentic to be true")
		}
	})

	t.Run("prompt job without agentic defaults to false", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			CustomPrompt: "Read-only review",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if job.Agentic {
			t.Error("Expected Agentic to be false by default")
		}
	})
}

func TestGetMachineID_CachingBehavior(t *testing.T) {
	t.Run("caches valid machine ID", func(t *testing.T) {
		server, _, _ := newTestServer(t)

		// First call should fetch from DB and cache
		id1 := server.getMachineID()
		if id1 == "" {
			t.Fatal("Expected non-empty machine ID on first call")
		}

		// Second call should return cached value
		id2 := server.getMachineID()
		if id2 != id1 {
			t.Errorf("Expected cached value %q, got %q", id1, id2)
		}

		// Verify internal state is cached
		server.machineIDMu.Lock()
		cachedID := server.machineID
		server.machineIDMu.Unlock()
		if cachedID != id1 {
			t.Errorf("Expected internal machineID to be %q, got %q", id1, cachedID)
		}
	})

	t.Run("error then success caches on success", func(t *testing.T) {
		// This tests the error→success retry path:
		// 1. First call fails (DB closed) → returns empty, not cached
		// 2. DB replaced with working one
		// 3. Second call succeeds → returns valid ID and caches it

		server, db, tmpDir := newTestServer(t)

		// Close the DB to simulate error condition
		db.Close()

		// First call should return empty since DB is closed
		id1 := server.getMachineID()
		if id1 != "" {
			t.Fatalf("Expected empty machine ID on error, got %q", id1)
		}

		// Verify nothing was cached
		server.machineIDMu.Lock()
		if server.machineID != "" {
			server.machineIDMu.Unlock()
			t.Fatal("Should not cache on error")
		}
		server.machineIDMu.Unlock()

		// "Fix" the error by opening a new DB and replacing it
		newDB, err := storage.Open(filepath.Join(tmpDir, "reviews.db"))
		if err != nil {
			t.Fatalf("Failed to reopen DB: %v", err)
		}
		t.Cleanup(func() { newDB.Close() })
		server.db = newDB

		// Second call should succeed and cache
		id2 := server.getMachineID()
		if id2 == "" {
			t.Fatal("Expected non-empty machine ID after DB recovery")
		}

		// Verify it's now cached
		server.machineIDMu.Lock()
		cachedID := server.machineID
		server.machineIDMu.Unlock()
		if cachedID != id2 {
			t.Errorf("Expected cached ID %q, got %q", id2, cachedID)
		}

		// Third call should return cached value
		id3 := server.getMachineID()
		if id3 != id2 {
			t.Errorf("Expected cached ID %q on third call, got %q", id2, id3)
		}
	})
}

// TestHandleAddCommentToJobStates tests that comments can be added to jobs
// in any state: queued, running, done, failed, and canceled.
func TestHandleAddCommentToJobStates(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repo and commit
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "test-repo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	testCases := []struct {
		name       string
		setupQuery string // SQL to set job to specific state
	}{
		{"queued job", ""},
		{"running job", `UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`},
		{"completed job", `UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`},
		{"failed job", `UPDATE review_jobs SET status = 'failed', started_at = datetime('now'), finished_at = datetime('now'), error = 'test error' WHERE id = ?`},
		{"canceled job", `UPDATE review_jobs SET status = 'canceled', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a job
			job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "abc123", Agent: "test-agent"})
			if err != nil {
				t.Fatalf("EnqueueJob failed: %v", err)
			}

			// Set job to desired state
			if tc.setupQuery != "" {
				if _, err := db.Exec(tc.setupQuery, job.ID); err != nil {
					t.Fatalf("Failed to set job state: %v", err)
				}
			}

			// Add comment via API
			reqData := map[string]any{
				"job_id":    job.ID,
				"commenter": "test-user",
				"comment":   "Test comment for " + tc.name,
			}
			req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/comment", reqData)
			w := httptest.NewRecorder()

			server.handleAddComment(w, req)

			if w.Code != http.StatusCreated {
				t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
			}

			// Verify response contains the comment
			var resp storage.Response
			testutil.DecodeJSON(t, w, &resp)
			if resp.Responder != "test-user" {
				t.Errorf("Expected responder 'test-user', got %q", resp.Responder)
			}
		})
	}
}

// TestHandleAddCommentToNonExistentJob tests that adding a comment to a
// non-existent job returns 404.
func TestHandleAddCommentToNonExistentJob(t *testing.T) {
	server, _, _ := newTestServer(t)

	reqData := map[string]any{
		"job_id":    99999,
		"commenter": "test-user",
		"comment":   "This should fail",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/comment", reqData)
	w := httptest.NewRecorder()

	server.handleAddComment(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "job not found") {
		t.Errorf("Expected 'job not found' error, got: %s", w.Body.String())
	}
}

// TestHandleAddCommentWithoutReview tests that comments can be added to jobs
// that don't have a review yet (job exists but hasn't completed).
func TestHandleAddCommentWithoutReview(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repo, commit, and job (but NO review)
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")

	// Set job to running (no review exists yet)
	setJobStatus(t, db, job.ID, storage.JobStatusRunning)

	// Verify no review exists
	if _, err := db.GetReviewByJobID(job.ID); err == nil {
		t.Fatal("Expected no review to exist for job")
	}

	// Add comment - should succeed even without a review
	reqData := map[string]any{
		"job_id":    job.ID,
		"commenter": "test-user",
		"comment":   "Comment on in-progress job without review",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/comment", reqData)
	w := httptest.NewRecorder()

	server.handleAddComment(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify comment was stored
	comments, err := db.GetCommentsForJob(job.ID)
	if err != nil {
		t.Fatalf("GetCommentsForJob failed: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("Expected 1 comment, got %d", len(comments))
	}
	if comments[0].Response != "Comment on in-progress job without review" {
		t.Errorf("Unexpected comment: %q", comments[0].Response)
	}
}

// TestHandleJobOutput_InvalidJobID tests that invalid job_id returns 400.
func TestHandleJobOutput_InvalidJobID(t *testing.T) {
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/job/output?job_id=notanumber", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleJobOutput_NonExistentJob tests that non-existent job returns 404.
func TestHandleJobOutput_NonExistentJob(t *testing.T) {
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/job/output?job_id=99999", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleJobOutput_PollingRunningJob tests polling mode for a running job.
func TestHandleJobOutput_PollingRunningJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a running job
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")
	setJobStatus(t, db, job.ID, storage.JobStatusRunning)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/job/output?job_id=%d", job.ID), nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		JobID  int64  `json:"job_id"`
		Status string `json:"status"`
		Lines  []struct {
			TS       string `json:"ts"`
			Text     string `json:"text"`
			LineType string `json:"line_type"`
		} `json:"lines"`
		HasMore bool `json:"has_more"`
	}
	testutil.DecodeJSON(t, w, &resp)

	if resp.JobID != job.ID {
		t.Errorf("Expected job_id %d, got %d", job.ID, resp.JobID)
	}
	if resp.Status != "running" {
		t.Errorf("Expected status 'running', got %q", resp.Status)
	}
	if !resp.HasMore {
		t.Error("Expected has_more=true for running job")
	}
}

// TestHandleJobOutput_PollingCompletedJob tests polling mode for a completed job.
func TestHandleJobOutput_PollingCompletedJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a completed job
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")
	setJobStatus(t, db, job.ID, storage.JobStatusDone)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/job/output?job_id=%d", job.ID), nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		JobID   int64  `json:"job_id"`
		Status  string `json:"status"`
		HasMore bool   `json:"has_more"`
	}
	testutil.DecodeJSON(t, w, &resp)

	if resp.Status != "done" {
		t.Errorf("Expected status 'done', got %q", resp.Status)
	}
	if resp.HasMore {
		t.Error("Expected has_more=false for completed job")
	}
}

// TestHandleJobOutput_StreamingCompletedJob tests that streaming mode for a
// completed job returns an immediate complete response instead of hanging.
func TestHandleJobOutput_StreamingCompletedJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a completed job
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")
	setJobStatus(t, db, job.ID, storage.JobStatusDone)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/job/output?job_id=%d&stream=1", job.ID), nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	// Should return immediately with complete message, not hang
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	testutil.DecodeJSON(t, w, &resp)

	if resp.Type != "complete" {
		t.Errorf("Expected type 'complete', got %q", resp.Type)
	}
	if resp.Status != "done" {
		t.Errorf("Expected status 'done', got %q", resp.Status)
	}
}

func TestHandleEnqueueAgentAvailability(t *testing.T) {
	// Shared read-only git repo created once (all subtests use different servers for DB isolation)
	repoDir := filepath.Join(t.TempDir(), "repo")
	testutil.InitTestGitRepo(t, repoDir)
	headSHA := testutil.GetHeadSHA(t, repoDir)

	// Create an isolated dir containing only a wrapper for git.
	// We can't just use git's parent dir because it may contain real agent
	// binaries (e.g. codex, claude) that would defeat the PATH isolation.
	// Symlinks don't work reliably on Windows, so we use wrapper scripts.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("git not found in PATH")
	}
	gitOnlyDir := t.TempDir()
	if runtime.GOOS == "windows" {
		wrapper := fmt.Sprintf("@\"%s\" %%*\r\n", gitPath)
		if err := os.WriteFile(filepath.Join(gitOnlyDir, "git.cmd"), []byte(wrapper), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		wrapper := fmt.Sprintf("#!/bin/sh\nexec '%s' \"$@\"\n", gitPath)
		if err := os.WriteFile(filepath.Join(gitOnlyDir, "git"), []byte(wrapper), 0755); err != nil {
			t.Fatal(err)
		}
	}

	mockScript := "#!/bin/sh\nexit 0\n"

	tests := []struct {
		name          string
		requestAgent  string
		mockBinaries  []string // binary names to place in PATH
		expectedAgent string   // expected agent stored in job
		expectedCode  int      // expected HTTP status code
	}{
		{
			name:          "explicit test agent preserved",
			requestAgent:  "test",
			mockBinaries:  nil,
			expectedAgent: "test",
			expectedCode:  http.StatusCreated,
		},
		{
			name:          "unavailable codex falls back to claude-code",
			requestAgent:  "codex",
			mockBinaries:  []string{"claude"},
			expectedAgent: "claude-code",
			expectedCode:  http.StatusCreated,
		},
		{
			name:          "default agent falls back when codex not installed",
			requestAgent:  "",
			mockBinaries:  []string{"claude"},
			expectedAgent: "claude-code",
			expectedCode:  http.StatusCreated,
		},
		{
			name:          "explicit codex kept when available",
			requestAgent:  "codex",
			mockBinaries:  []string{"codex"},
			expectedAgent: "codex",
			expectedCode:  http.StatusCreated,
		},
		{
			name:         "no agents available returns 503",
			requestAgent: "codex",
			mockBinaries: nil,
			expectedCode: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each subtest gets its own server/DB to avoid SHA dedup conflicts
			server, _, _ := newTestServer(t)

			// Isolate PATH: only mock binaries + git (no real agent CLIs)
			origPath := os.Getenv("PATH")
			mockDir := t.TempDir()
			for _, bin := range tt.mockBinaries {
				name := bin
				content := mockScript
				if runtime.GOOS == "windows" {
					name = bin + ".cmd"
					content = "@exit /b 0\r\n"
				}
				if err := os.WriteFile(filepath.Join(mockDir, name), []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			os.Setenv("PATH", mockDir+string(os.PathListSeparator)+gitOnlyDir)
			t.Cleanup(func() { os.Setenv("PATH", origPath) })

			reqData := EnqueueRequest{
				RepoPath:  repoDir,
				CommitSHA: headSHA,
			}
			if tt.requestAgent != "" {
				reqData.Agent = tt.requestAgent
			}
			req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
			w := httptest.NewRecorder()

			server.handleEnqueue(w, req)

			if w.Code != tt.expectedCode {
				t.Fatalf("Expected status %d, got %d: %s", tt.expectedCode, w.Code, w.Body.String())
			}

			if tt.expectedCode != http.StatusCreated {
				return
			}

			var job storage.ReviewJob
			testutil.DecodeJSON(t, w, &job)

			if job.Agent != tt.expectedAgent {
				t.Errorf("Expected agent %q, got %q", tt.expectedAgent, job.Agent)
			}
		})
	}
}

func TestHandleJobOutput_MissingJobID(t *testing.T) {
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/job/output", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestServerStop_StopsCIPoller(t *testing.T) {
	server, db, _ := newTestServer(t)

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	cfg.CI.PollInterval = "1h"

	poller := NewCIPoller(db, NewStaticConfig(cfg), server.Broadcaster())
	if err := poller.Start(); err != nil {
		t.Fatalf("Start poller: %v", err)
	}

	healthy, _ := poller.HealthCheck()
	if !healthy {
		t.Fatal("expected poller running after Start")
	}

	server.SetCIPoller(poller)
	if err := server.Stop(); err != nil {
		t.Fatalf("Server.Stop: %v", err)
	}

	healthy, msg := poller.HealthCheck()
	if healthy {
		t.Fatalf("expected poller stopped after Server.Stop, got (%v, %q)", healthy, msg)
	}
}

// TestHandleEnqueueWorktreeGitDirIsolation verifies that a leaked GIT_DIR
// environment variable (as set by git hooks) does not cause the daemon to
// resolve HEAD from the wrong worktree.
//
// Reproduces the bug reported in issue #230:
//  1. Create a main repo with commit A
//  2. Create a worktree and advance it to commit B
//  3. Set GIT_DIR to point to the main repo's .git dir (simulating a hook)
//  4. Show that handleEnqueue resolves the wrong commit (the bug)
//  5. Clear GIT_DIR (as daemonRunCmd does at startup) and verify correct resolution
func TestHandleEnqueueWorktreeGitDirIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping worktree test on Windows due to path differences")
	}

	tmpDir := t.TempDir()

	// Create main repo with initial commit (commit A)
	mainRepo := filepath.Join(tmpDir, "main-repo")
	testutil.InitTestGitRepo(t, mainRepo)
	commitA := testutil.GetHeadSHA(t, mainRepo)

	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	wtCmd := exec.Command("git", "-C", mainRepo, "worktree", "add", "-b", "wt-branch", worktreeDir)
	if out, err := wtCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}

	// Make a new commit in the worktree so HEAD differs (commit B)
	wtFile := filepath.Join(worktreeDir, "worktree-file.txt")
	if err := os.WriteFile(wtFile, []byte("worktree content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for _, args := range [][]string{
		{"git", "-C", worktreeDir, "add", "."},
		{"git", "-C", worktreeDir, "commit", "-m", "worktree commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	commitB := testutil.GetHeadSHA(t, worktreeDir)

	if commitA == commitB {
		t.Fatal("test setup error: commits A and B should differ")
	}

	enqueue := func(t *testing.T) storage.ReviewJob {
		t.Helper()
		server, _, _ := newTestServer(t)
		reqData := EnqueueRequest{
			RepoPath: worktreeDir,
			GitRef:   "HEAD",
			Agent:    "test",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()
		server.handleEnqueue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)
		return job
	}

	t.Run("leaked GIT_DIR resolves wrong commit", func(t *testing.T) {
		// Set GIT_DIR to the main repo's .git dir, simulating a post-commit hook.
		// t.Setenv restores the original value after the subtest.
		mainGitDir := filepath.Join(mainRepo, ".git")
		t.Setenv("GIT_DIR", mainGitDir)

		job := enqueue(t)

		// With GIT_DIR leaked, git resolves HEAD from the main repo (commit A)
		// instead of the worktree (commit B). This is the bug.
		if job.GitRef != commitA {
			t.Errorf("expected leaked GIT_DIR to resolve commit A (%s), got %s", commitA, job.GitRef)
		}
	})

	t.Run("cleared GIT_DIR resolves correct commit", func(t *testing.T) {
		// Simulate the daemon startup fix: clear GIT_DIR before handling requests.
		// This is what daemonRunCmd() does with os.Unsetenv.
		t.Setenv("GIT_DIR", "")
		os.Unsetenv("GIT_DIR")

		job := enqueue(t)

		// Without GIT_DIR, git uses cmd.Dir correctly and resolves the worktree's HEAD.
		if job.GitRef != commitB {
			t.Errorf("expected worktree commit B (%s), got %s", commitB, job.GitRef)
		}
	})
}

// TestHandleEnqueueRangeFromRootCommit verifies that a range review starting
// from the root commit (which has no parent) succeeds by falling back to the
// empty tree SHA.
func TestHandleEnqueueRangeFromRootCommit(t *testing.T) {
	repoDir := t.TempDir()
	testutil.InitTestGitRepo(t, repoDir)

	// Get the root commit SHA
	rootSHA, err := gitpkg.ResolveSHA(repoDir, "HEAD")
	if err != nil {
		t.Fatalf("resolve root SHA: %v", err)
	}

	// Add a second commit so we have a range
	testFile := filepath.Join(repoDir, "second.txt")
	if err := os.WriteFile(testFile, []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "-C", repoDir, "add", "."},
		{"git", "-C", repoDir, "commit", "-m", "second"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	endSHA, err := gitpkg.ResolveSHA(repoDir, "HEAD")
	if err != nil {
		t.Fatalf("resolve end SHA: %v", err)
	}

	server, _, _ := newTestServer(t)

	// Send range starting from root commit's parent (rootSHA^..endSHA)
	// This is what the CLI sends for "roborev review <root> <end>"
	rangeRef := rootSHA + "^.." + endSHA
	reqData := EnqueueRequest{
		RepoPath: repoDir,
		GitRef:   rangeRef,
		Agent:    "test",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
	w := httptest.NewRecorder()

	server.handleEnqueue(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var job storage.ReviewJob
	testutil.DecodeJSON(t, w, &job)

	// The stored range should use the empty tree SHA as the start
	expectedRef := gitpkg.EmptyTreeSHA + ".." + endSHA
	if job.GitRef != expectedRef {
		t.Errorf("expected git_ref %q, got %q", expectedRef, job.GitRef)
	}
}

// TestHandleEnqueueRangeNonCommitObjectRejects verifies that the root-commit
// fallback does not trigger for non-commit objects (e.g. blobs).
func TestHandleEnqueueRangeNonCommitObjectRejects(t *testing.T) {
	repoDir := t.TempDir()
	testutil.InitTestGitRepo(t, repoDir)

	endSHA, err := gitpkg.ResolveSHA(repoDir, "HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}

	// Get a blob SHA (the test.txt file created by InitTestGitRepo)
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD:test.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("get blob SHA: %v", err)
	}
	blobSHA := strings.TrimSpace(string(out))

	server, _, _ := newTestServer(t)

	// A blob^ should not fall back to EmptyTreeSHA — it should return 400
	rangeRef := blobSHA + "^.." + endSHA
	reqData := EnqueueRequest{
		RepoPath: repoDir,
		GitRef:   rangeRef,
		Agent:    "test",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
	w := httptest.NewRecorder()

	server.handleEnqueue(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid start commit") {
		t.Errorf("expected 'invalid start commit' error, got: %s", w.Body.String())
	}
}

func TestHandleBatchJobs(t *testing.T) {
	server, db, _ := newTestServer(t)

	repo, err := db.GetOrCreateRepo("/tmp/repo", "")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Job 1: with review
	job1, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, GitRef: "abc123", Agent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	setJobStatus(t, db, job1.ID, storage.JobStatusRunning)
	if err := db.CompleteJob(job1.ID, "test", "p1", "o1"); err != nil {
		t.Fatalf("CompleteJob failed for job1: %v", err)
	}

	// Job 2: no review
	job2, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, GitRef: "def456", Agent: "test"})
	if err != nil {
		t.Fatal(err)
	}

	nonExistentJobID := int64(9999)

	t.Run("fetches jobs with and without reviews", func(t *testing.T) {
		reqBody := map[string][]int64{"job_ids": {job1.ID, job2.ID, nonExistentJobID}}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", reqBody)
		w := httptest.NewRecorder()

		server.handleBatchJobs(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var resp struct {
			Results map[int64]storage.JobWithReview `json:"results"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Results) != 2 {
			t.Fatalf("Expected 2 results, got %d", len(resp.Results))
		}

		// Check job 1 (with review)
		res1, ok := resp.Results[job1.ID]
		if !ok {
			t.Errorf("Result for job %d not found", job1.ID)
		}
		if res1.Review == nil {
			t.Error("Expected review for job 1")
		} else if res1.Review.Output != "o1" {
			t.Errorf("Expected output 'o1', got %q", res1.Review.Output)
		}

		// Check job 2 (no review)
		res2, ok := resp.Results[job2.ID]
		if !ok {
			t.Errorf("Result for job %d not found", job2.ID)
		}
		if res2.Review != nil {
			t.Error("Expected nil review for job 2")
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs/batch", nil)
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusMethodNotAllowed)
	})

	t.Run("empty job_ids fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", map[string][]int64{"job_ids": {}})
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
	})

	t.Run("missing job_ids fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", map[string]string{})
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
	})

	t.Run("batch size limit enforced", func(t *testing.T) {
		ids := make([]int64, 101)
		for i := range ids {
			ids[i] = int64(i)
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", map[string][]int64{"job_ids": ids})
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
		if !strings.Contains(w.Body.String(), "too many job IDs") {
			t.Errorf("Expected error message about batch size, got: %s", w.Body.String())
		}
	})
}

func TestHandleRemap(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Set up a git repo so handleRemap can resolve paths
	repoDir := filepath.Join(tmpDir, "remap-repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	// Resolve symlinks to get the canonical path
	// (macOS /var -> /private/var symlink)
	resolvedDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		resolvedDir = repoDir
	}

	repo, err := db.GetOrCreateRepo(resolvedDir)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "oldsha111", "Test", "old commit", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "oldsha111",
		Agent:    "test",
		PatchID:  "patchXYZ",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("remap updates job", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: repoDir,
			Mappings: []RemapMapping{
				{
					OldSHA:    "oldsha111",
					NewSHA:    "newsha222",
					PatchID:   "patchXYZ",
					Author:    "Test",
					Subject:   "new commit",
					Timestamp: time.Now().Format(time.RFC3339),
				},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var result map[string]int
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["remapped"] != 1 {
			t.Errorf("expected remapped=1, got %d", result["remapped"])
		}
	})

	t.Run("remap with non-git path returns 400", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: "/nonexistent/repo",
			Mappings: []RemapMapping{
				{OldSHA: "a", NewSHA: "b", PatchID: "c", Author: "x", Subject: "y", Timestamp: time.Now().Format(time.RFC3339)},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("remap with unregistered repo returns 404", func(t *testing.T) {
		// Create a valid git repo that is NOT registered in the DB
		unregistered := filepath.Join(tmpDir, "unregistered-repo")
		if err := os.MkdirAll(unregistered, 0755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "init")
		cmd.Dir = unregistered
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %s: %v", out, err)
		}

		reqData := RemapRequest{
			RepoPath: unregistered,
			Mappings: []RemapMapping{
				{OldSHA: "a", NewSHA: "b", PatchID: "c", Author: "x", Subject: "y", Timestamp: time.Now().Format(time.RFC3339)},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("remap with invalid timestamp returns 400", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: repoDir,
			Mappings: []RemapMapping{
				{
					OldSHA:    "oldsha111",
					NewSHA:    "newsha333",
					PatchID:   "patchXYZ",
					Author:    "Test",
					Subject:   "bad ts",
					Timestamp: "not-a-timestamp",
				},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("remap with empty repo_path returns 400", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: "",
			Mappings: []RemapMapping{
				{
					OldSHA:    "a",
					NewSHA:    "b",
					PatchID:   "c",
					Author:    "x",
					Subject:   "y",
					Timestamp: time.Now().Format(time.RFC3339),
				},
			},
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/remap", reqData,
		)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("remap rejects too many mappings", func(t *testing.T) {
		mappings := make([]RemapMapping, 1001)
		for i := range mappings {
			mappings[i] = RemapMapping{
				OldSHA: "a", NewSHA: "b", PatchID: "c",
				Author: "x", Subject: "y",
				Timestamp: time.Now().Format(time.RFC3339),
			}
		}
		reqData := RemapRequest{
			RepoPath: repoDir,
			Mappings: mappings,
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/remap", reqData,
		)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("remap rejects oversized body", func(t *testing.T) {
		// Build a payload larger than 1MB.
		// JSON-encode repoDir so Windows backslashes are escaped.
		escapedDir, _ := json.Marshal(repoDir)
		body := []byte(`{"repo_path":` + string(escapedDir) + `,"mappings":[`)
		entry := []byte(`{"old_sha":"a","new_sha":"b","patch_id":"c","author":"x","subject":"y","timestamp":"2026-01-01T00:00:00Z"},`)
		for len(body) < 1<<20+1 {
			body = append(body, entry...)
		}
		body = append(body[:len(body)-1], []byte(`]}`)...)

		req := httptest.NewRequest(
			http.MethodPost, "/api/remap",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d: %s",
				w.Code, w.Body.String())
		}
	})
}

func TestHandleGetReviewJobIDParsing(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoPath := filepath.Join(tmpDir, "repo-review-parse")
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	commit, err := db.GetOrCreateCommit(
		repo.ID, "abc123", "msg", "Author", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "abc123",
		Branch:   "main",
		Agent:    "test",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, err := db.ClaimJob("test-worker")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if claimed == nil || claimed.ID != job.ID {
		t.Fatalf("ClaimJob: expected job %d", job.ID)
	}
	if err := db.CompleteJob(
		job.ID, "test", "prompt", "review output",
	); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{
			"valid job_id",
			fmt.Sprintf("job_id=%d", job.ID),
			http.StatusOK,
		},
		{
			"missing params",
			"",
			http.StatusBadRequest,
		},
		{
			"non-numeric job_id",
			"job_id=abc",
			http.StatusBadRequest,
		},
		{
			"partial numeric job_id",
			"job_id=10abc",
			http.StatusBadRequest,
		},
		{
			"not found job_id",
			"job_id=999999",
			http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/review"
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			server.handleGetReview(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf(
					"expected status %d, got %d: %s",
					tt.wantStatus, w.Code, w.Body.String(),
				)
			}
		})
	}
}

func TestHandleListJobsIDParsing(t *testing.T) {
	server, _, _ := newTestServer(t)
	testInvalidIDParsing(t, server.handleListJobs, "/api/jobs?id=%s")
}

func TestHandleJobOutputIDParsing(t *testing.T) {
	server, _, _ := newTestServer(t)
	testInvalidIDParsing(t, server.handleJobOutput, "/api/job-output?job_id=%s")
}

func TestHandleListCommentsJobIDParsing(t *testing.T) {
	server, _, _ := newTestServer(t)
	testInvalidIDParsing(t, server.handleListComments, "/api/comments?job_id=%s")
}

func TestHandleListJobsJobTypeFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "repo-jt")
	testutil.InitTestGitRepo(t, repoDir)
	repo, _ := db.GetOrCreateRepo(repoDir)
	commit, _ := db.GetOrCreateCommit(
		repo.ID, "jt-abc", "Author", "Subject", time.Now(),
	)

	// Create a review job
	reviewJob, _ := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "jt-abc",
		Agent:    "test",
	})

	// Create a fix job parented to the review
	db.EnqueueJob(storage.EnqueueOpts{
		RepoID:      repo.ID,
		CommitID:    commit.ID,
		GitRef:      "jt-abc",
		Agent:       "test",
		JobType:     storage.JobTypeFix,
		ParentJobID: reviewJob.ID,
	})

	t.Run("job_type=fix returns only fix jobs", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/jobs?job_type=fix", nil,
		)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Jobs) != 1 {
			t.Fatalf("Expected 1 fix job, got %d", len(resp.Jobs))
		}
		if resp.Jobs[0].JobType != storage.JobTypeFix {
			t.Errorf(
				"Expected job_type 'fix', got %q", resp.Jobs[0].JobType,
			)
		}
	})

	t.Run("no job_type returns all jobs", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/jobs", nil,
		)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Jobs) != 2 {
			t.Errorf("Expected 2 jobs total, got %d", len(resp.Jobs))
		}
	})

	t.Run("exclude_job_type=fix returns only non-fix jobs", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/jobs?exclude_job_type=fix", nil,
		)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Jobs) != 1 {
			t.Fatalf("Expected 1 non-fix job, got %d", len(resp.Jobs))
		}
		if resp.Jobs[0].JobType == storage.JobTypeFix {
			t.Error("Expected non-fix job, got fix")
		}
	})
}

func TestHandleFixJobStaleValidation(t *testing.T) {
	server, db, tmpDir := newTestServer(t)
	server.configWatcher.Config().FixAgent = "test"

	repoDir := filepath.Join(tmpDir, "repo-fix-val")
	testutil.InitTestGitRepo(t, repoDir)
	repo, _ := db.GetOrCreateRepo(repoDir)
	commit, _ := db.GetOrCreateCommit(
		repo.ID, "fix-val-abc", "Author", "Subject", time.Now(),
	)

	// Create a review job and complete it with output
	reviewJob, _ := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "fix-val-abc",
		Agent:    "test",
	})
	db.ClaimJob("w1")
	db.CompleteJob(reviewJob.ID, "test", "prompt", "FAIL: issues found")

	t.Run("fix job as parent is rejected", func(t *testing.T) {
		// Create a fix job and try to use it as a parent
		fixJob, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo.ID,
			CommitID:    commit.ID,
			GitRef:      "fix-val-abc",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: reviewJob.ID,
		})
		db.ClaimJob("w-fix-parent")
		db.CompleteJob(fixJob.ID, "test", "prompt", "done")

		body := map[string]any{
			"parent_job_id": fixJob.ID,
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix", body,
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf(
				"Expected 400 for fix-job parent, got %d: %s",
				w.Code, w.Body.String(),
			)
		}
	})

	t.Run("stale job that is not a fix job is rejected", func(t *testing.T) {
		// reviewJob is a review, not a fix job
		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"stale_job_id":  reviewJob.ID,
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix", body,
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for non-fix stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("stale job with wrong parent is rejected", func(t *testing.T) {
		// Create a second review + fix in the SAME repo, linked to the other review
		review2, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fix-val-def",
			Agent:    "test",
		})
		db.ClaimJob("w3")
		db.CompleteJob(review2.ID, "test", "prompt", "FAIL: other issues")

		wrongParentFix, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo.ID,
			CommitID:    commit.ID,
			GitRef:      "fix-val-def",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: review2.ID,
		})
		// Complete it so it has terminal status + patch
		db.ClaimJob("w4")
		db.CompleteJob(wrongParentFix.ID, "test", "prompt", "done")
		db.SaveJobPatch(wrongParentFix.ID, "--- a/f\n+++ b/f\n")

		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"stale_job_id":  wrongParentFix.ID,
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix", body,
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for wrong-parent stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("stale job without patch is rejected", func(t *testing.T) {
		// Create a fix job linked to reviewJob but with no patch
		noPatchFix, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo.ID,
			CommitID:    commit.ID,
			GitRef:      "fix-val-abc",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: reviewJob.ID,
		})
		// Complete it (terminal status) but don't set a patch
		db.ClaimJob("w5")
		db.CompleteJob(noPatchFix.ID, "test", "prompt", "done but no diff")

		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"stale_job_id":  noPatchFix.ID,
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix", body,
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for patchless stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("non-terminal stale job statuses are rejected", func(t *testing.T) {
		for _, status := range []storage.JobStatus{
			storage.JobStatusQueued,
			storage.JobStatusRunning,
			storage.JobStatusFailed,
			storage.JobStatusCanceled,
		} {
			t.Run(string(status), func(t *testing.T) {
				fixJob, err := db.EnqueueJob(storage.EnqueueOpts{
					RepoID:      repo.ID,
					CommitID:    commit.ID,
					GitRef:      "fix-val-abc",
					Agent:       "test",
					JobType:     storage.JobTypeFix,
					ParentJobID: reviewJob.ID,
				})
				if err != nil {
					t.Fatalf("EnqueueJob: %v", err)
				}

				// Set status directly to avoid ClaimJob picking
				// a different queued job from an earlier iteration.
				if status != storage.JobStatusQueued {
					_, err = db.Exec(
						`UPDATE review_jobs SET status = ? WHERE id = ?`,
						string(status), fixJob.ID,
					)
					if err != nil {
						t.Fatalf("Set status to %s: %v", status, err)
					}
				}

				// Verify the job is in the expected state
				got, err := db.GetJobByID(fixJob.ID)
				if err != nil {
					t.Fatalf("GetJobByID: %v", err)
				}
				if got.Status != status {
					t.Fatalf(
						"Expected job %d status %s, got %s",
						fixJob.ID, status, got.Status,
					)
				}

				body := map[string]any{
					"parent_job_id": reviewJob.ID,
					"stale_job_id":  fixJob.ID,
				}
				req := testutil.MakeJSONRequest(
					t, http.MethodPost, "/api/job/fix", body,
				)
				w := httptest.NewRecorder()
				server.handleFixJob(w, req)

				if w.Code != http.StatusBadRequest {
					t.Errorf(
						"Expected 400 for %s stale job, got %d: %s",
						status, w.Code, w.Body.String(),
					)
				}
			})
		}
	})

	t.Run("compact parent with empty git_ref uses branch as fallback", func(t *testing.T) {
		// Compact jobs are stored with an empty git_ref (the label is stored separately).
		// Fixing a compact job must not pass "" to worktree.Create — it should fall back
		// to the branch, then "HEAD".
		compactJob, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:  repo.ID,
			GitRef:  "", // compact jobs have no real git ref
			Branch:  "main",
			Agent:   "test",
			JobType: storage.JobTypeCompact,
			Prompt:  "consolidated findings",
			Label:   "compact-all-20240101-120000",
		})
		// Force to running so CompleteJob can transition it to done.
		setJobStatus(t, db, compactJob.ID, storage.JobStatusRunning)
		db.CompleteJob(compactJob.ID, "test", "consolidated findings", "FAIL: issues found")

		body := map[string]any{
			"parent_job_id": compactJob.ID,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/fix", body)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 for compact parent fix, got %d: %s", w.Code, w.Body.String())
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)

		if fixJob.GitRef == "" {
			t.Errorf("Fix job git_ref must not be empty for compact parent")
		}
		if fixJob.GitRef != "main" {
			t.Errorf("Expected fix job git_ref 'main' (branch fallback), got %q", fixJob.GitRef)
		}
	})

	t.Run("range parent uses branch instead of range ref for fix worktree", func(t *testing.T) {
		// Range git refs like "sha1..sha2" are not valid for git worktree add.
		// Fixing a range review should use the branch instead.
		rangeJob, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID: repo.ID,
			GitRef: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa..bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Branch: "feature/foo",
			Agent:  "test",
		})
		setJobStatus(t, db, rangeJob.ID, storage.JobStatusRunning)
		db.CompleteJob(rangeJob.ID, "test", "prompt", "FAIL: issues found")

		body := map[string]any{
			"parent_job_id": rangeJob.ID,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/fix", body)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 for range parent fix, got %d: %s", w.Code, w.Body.String())
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)

		if strings.Contains(fixJob.GitRef, "..") {
			t.Errorf("Fix job git_ref must not be a range, got %q", fixJob.GitRef)
		}
		if fixJob.GitRef != "feature/foo" {
			t.Errorf("Expected fix job git_ref 'feature/foo' (branch fallback), got %q", fixJob.GitRef)
		}
	})

	t.Run("rejects git_ref starting with dash", func(t *testing.T) {
		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"git_ref":       "--option-injection",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/fix", body)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 for dash-prefixed git_ref, got %d", w.Code)
		}
	})

	t.Run("rejects git_ref with control chars", func(t *testing.T) {
		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"git_ref":       "main\x00injected",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/fix", body)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 for control-char git_ref, got %d", w.Code)
		}
	})

	t.Run("rejects whitespace-padded dash ref", func(t *testing.T) {
		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"git_ref":       " --option-injection",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/fix", body)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf(
				"Expected 400 for space+dash git_ref, got %d",
				w.Code,
			)
		}
	})

	t.Run("treats whitespace-only git_ref as empty", func(t *testing.T) {
		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"git_ref":       "   ",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/fix", body)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		// Whitespace-only trims to empty, so it's treated as
		// no user-provided ref — the server falls through to
		// the parent ref/branch/HEAD resolution chain.
		if w.Code != http.StatusCreated {
			t.Fatalf(
				"Expected 201 for whitespace-only git_ref (treated as empty), got %d: %s",
				w.Code, w.Body.String(),
			)
		}
	})

	t.Run("accepts valid git_ref from request", func(t *testing.T) {
		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"git_ref":       "feature/my-branch",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/fix", body)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 for valid git_ref, got %d: %s", w.Code, w.Body.String())
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)
		if fixJob.GitRef != "feature/my-branch" {
			t.Errorf("Expected git_ref 'feature/my-branch', got %q", fixJob.GitRef)
		}
	})

	t.Run("stale job from different repo is rejected", func(t *testing.T) {
		// Create a fix job in a different repo
		repo2Dir := filepath.Join(tmpDir, "repo-fix-val-2")
		testutil.InitTestGitRepo(t, repo2Dir)
		repo2, _ := db.GetOrCreateRepo(repo2Dir)
		commit2, _ := db.GetOrCreateCommit(
			repo2.ID, "other-sha", "Author", "Subject", time.Now(),
		)
		otherReview, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:   repo2.ID,
			CommitID: commit2.ID,
			GitRef:   "other-sha",
			Agent:    "test",
		})
		db.ClaimJob("w2")
		db.CompleteJob(otherReview.ID, "test", "prompt", "FAIL")

		otherFix, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo2.ID,
			CommitID:    commit2.ID,
			GitRef:      "other-sha",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: otherReview.ID,
		})

		body := map[string]any{
			"parent_job_id": reviewJob.ID,
			"stale_job_id":  otherFix.ID,
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix", body,
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for cross-repo stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})
}

func TestHandleJobLog(t *testing.T) {
	server, db, tmpDir := newTestServer(t)
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Create a repo and a job
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "testrepo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID: repo.ID,
		GitRef: "abc123",
		Agent:  "test",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	t.Run("missing job_id returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/job/log", nil)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("nonexistent job returns 404", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/job/log?job_id=99999", nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("no log file returns 404", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/job/log?job_id=%d", job.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("returns log content with headers", func(t *testing.T) {
		// Create a log file
		logDir := JobLogDir()
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		logContent := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
		if err := os.WriteFile(
			JobLogPath(job.ID), []byte(logContent), 0644,
		); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/job/log?job_id=%d", job.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
			t.Errorf("expected Content-Type application/x-ndjson, got %q", ct)
		}
		if js := w.Header().Get("X-Job-Status"); js != "queued" {
			t.Errorf("expected X-Job-Status queued, got %q", js)
		}
		if w.Body.String() != logContent {
			t.Errorf("expected log content %q, got %q", logContent, w.Body.String())
		}
	})

	t.Run("running job with no log returns empty 200", func(t *testing.T) {
		// Claim the existing queued job to move it to "running"
		claimed, err := db.ClaimJob("worker-test")
		if err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}
		// Remove any log file to simulate startup race
		os.Remove(JobLogPath(claimed.ID))

		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/job/log?job_id=%d", claimed.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if js := w.Header().Get("X-Job-Status"); js != "running" {
			t.Errorf("expected X-Job-Status running, got %q", js)
		}
		if w.Body.Len() != 0 {
			t.Errorf("expected empty body, got %q", w.Body.String())
		}
	})

	t.Run("POST returns 405", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			fmt.Sprintf("/api/job/log?job_id=%d", job.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", w.Code)
		}
	})
}

func TestHandleJobLogOffset(t *testing.T) {
	server, db, tmpDir := newTestServer(t)
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	repo, err := db.GetOrCreateRepo(
		filepath.Join(tmpDir, "testrepo"),
	)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID: repo.ID,
		GitRef: "def456",
		Agent:  "test",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Create log file with two JSONL lines.
	logDir := JobLogDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	line1 := `{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}` + "\n"
	line2 := `{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}` + "\n"
	logContent := line1 + line2
	if err := os.WriteFile(
		JobLogPath(job.ID), []byte(logContent), 0644,
	); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Run("offset=0 returns full content", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=0", job.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != logContent {
			t.Errorf(
				"expected full content, got %q",
				w.Body.String(),
			)
		}

		// X-Log-Offset should equal file size.
		offsetStr := w.Header().Get("X-Log-Offset")
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			t.Fatalf("parse X-Log-Offset %q: %v", offsetStr, err)
		}
		if offset != int64(len(logContent)) {
			t.Errorf(
				"X-Log-Offset = %d, want %d",
				offset, len(logContent),
			)
		}
	})

	t.Run("offset returns partial content", func(t *testing.T) {
		off := len(line1)
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=%d",
				job.ID, off,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != line2 {
			t.Errorf(
				"expected second line only, got %q",
				w.Body.String(),
			)
		}
	})

	t.Run("offset at end returns empty", func(t *testing.T) {
		off := len(logContent)
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=%d",
				job.ID, off,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if w.Body.Len() != 0 {
			t.Errorf(
				"expected empty body, got %q",
				w.Body.String(),
			)
		}
	})

	t.Run("negative offset returns 400", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=-1", job.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("offset beyond file resets to 0", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=999999",
				job.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		// Should return full content since offset was clamped.
		if w.Body.String() != logContent {
			t.Errorf(
				"expected full content after clamp, got %q",
				w.Body.String(),
			)
		}
	})

	t.Run("running job snaps to newline boundary", func(t *testing.T) {
		// Claim the existing queued job first so the next
		// ClaimJob picks up job2.
		if _, err := db.ClaimJob("worker-drain"); err != nil {
			t.Fatalf("ClaimJob (drain): %v", err)
		}

		// Create a new running job with a partial line at end.
		job2, err := db.EnqueueJob(storage.EnqueueOpts{
			RepoID: repo.ID,
			GitRef: "ghi789",
			Agent:  "test",
		})
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		if _, err := db.ClaimJob("worker-test2"); err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}

		// Write a complete line + partial line.
		completeLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}` + "\n"
		partialLine := `{"type":"assistant","message":{"content":`
		if err := os.WriteFile(
			JobLogPath(job2.ID),
			[]byte(completeLine+partialLine),
			0644,
		); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d", job2.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		// Should only return up to the newline, not the partial.
		body := w.Body.String()
		if body != completeLine {
			t.Errorf(
				"expected only complete line, got %q",
				body,
			)
		}

		// X-Log-Offset should point past the newline.
		offsetStr := w.Header().Get("X-Log-Offset")
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			t.Fatalf("parse X-Log-Offset: %v", err)
		}
		if offset != int64(len(completeLine)) {
			t.Errorf(
				"X-Log-Offset = %d, want %d",
				offset, len(completeLine),
			)
		}
	})
}

func TestJobLogSafeEnd(t *testing.T) {
	t.Run("empty file", func(t *testing.T) {
		f := writeTempFile(t, []byte{})
		if got := jobLogSafeEnd(f, 0); got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})

	t.Run("ends with newline", func(t *testing.T) {
		data := []byte("line1\nline2\n")
		f := writeTempFile(t, data)
		if got := jobLogSafeEnd(f, int64(len(data))); got != int64(len(data)) {
			t.Errorf("expected %d, got %d", len(data), got)
		}
	})

	t.Run("partial line at end", func(t *testing.T) {
		data := []byte("line1\npartial")
		f := writeTempFile(t, data)
		got := jobLogSafeEnd(f, int64(len(data)))
		if got != 6 { // "line1\n" is 6 bytes
			t.Errorf("expected 6, got %d", got)
		}
	})

	t.Run("no newlines at all", func(t *testing.T) {
		data := []byte("no-newlines-here")
		f := writeTempFile(t, data)
		if got := jobLogSafeEnd(f, int64(len(data))); got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})

	t.Run("large partial beyond 64KB", func(t *testing.T) {
		// A complete line followed by a partial line > 64KB.
		// The chunked backward scan should still find the newline.
		completeLine := "line1\n"
		partial := strings.Repeat("x", 100*1024) // 100KB
		data := []byte(completeLine + partial)
		f := writeTempFile(t, data)
		got := jobLogSafeEnd(f, int64(len(data)))
		want := int64(len(completeLine))
		if got != want {
			t.Errorf("expected %d, got %d", want, got)
		}
	})
}

func writeTempFile(t *testing.T, data []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "logtest-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return f
}
