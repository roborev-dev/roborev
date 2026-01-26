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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
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

func TestHandleListRepos(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	t.Run("empty database", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		// repos can be nil when empty
		var reposLen int
		if repos, ok := response["repos"].([]interface{}); ok && repos != nil {
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
	repo1, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo1"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	repo2, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo2"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Add 3 jobs to repo1
	for i := 0; i < 3; i++ {
		sha := "repo1sha" + string(rune('a'+i))
		commit, err := db.GetOrCreateCommit(repo1.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		if _, err := db.EnqueueJob(repo1.ID, commit.ID, sha, "", "test", "", ""); err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
	}

	// Add 2 jobs to repo2
	for i := 0; i < 2; i++ {
		sha := "repo2sha" + string(rune('a'+i))
		commit, err := db.GetOrCreateCommit(repo2.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		if _, err := db.EnqueueJob(repo2.ID, commit.ID, sha, "", "test", "", ""); err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
	}

	t.Run("repos with jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		repos := response["repos"].([]interface{})
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
			repoObj := r.(map[string]interface{})
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
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create repos
	repo1, _ := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo1"))
	repo2, _ := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo2"))

	// Add jobs to repos
	for i := 0; i < 3; i++ {
		sha := "repo1sha" + string(rune('a'+i))
		commit, _ := db.GetOrCreateCommit(repo1.ID, sha, "Author", "Subject", time.Now())
		db.EnqueueJob(repo1.ID, commit.ID, sha, "", "test", "", "")
	}
	for i := 0; i < 2; i++ {
		sha := "repo2sha" + string(rune('a'+i))
		commit, _ := db.GetOrCreateCommit(repo2.ID, sha, "Author", "Subject", time.Now())
		db.EnqueueJob(repo2.ID, commit.ID, sha, "", "test", "", "")
	}

	// Set branches: repo1 jobs 1,2 = main, job 3 = feature; repo2 jobs 4,5 = main
	db.Exec("UPDATE review_jobs SET branch = 'main' WHERE id IN (1, 2, 4, 5)")
	db.Exec("UPDATE review_jobs SET branch = 'feature' WHERE id = 3")

	t.Run("filter by main branch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=main", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		repos := response["repos"].([]interface{})
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

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		repos := response["repos"].([]interface{})
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

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		totalCount := int(response["total_count"].(float64))
		if totalCount != 0 {
			t.Errorf("Expected total_count 0 for nonexistent branch, got %d", totalCount)
		}
	})
}

func TestHandleListBranches(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create repos
	repo1, _ := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo1"))
	repo2, _ := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo2"))

	// Add jobs to repos
	for i := 0; i < 3; i++ {
		sha := "repo1sha" + string(rune('a'+i))
		commit, _ := db.GetOrCreateCommit(repo1.ID, sha, "Author", "Subject", time.Now())
		db.EnqueueJob(repo1.ID, commit.ID, sha, "", "test", "", "")
	}
	for i := 0; i < 2; i++ {
		sha := "repo2sha" + string(rune('a'+i))
		commit, _ := db.GetOrCreateCommit(repo2.ID, sha, "Author", "Subject", time.Now())
		db.EnqueueJob(repo2.ID, commit.ID, sha, "", "test", "", "")
	}

	// Set branches: jobs 1,2,4 = main, job 3 = feature, job 5 = no branch
	db.Exec("UPDATE review_jobs SET branch = 'main' WHERE id IN (1, 2, 4)")
	db.Exec("UPDATE review_jobs SET branch = 'feature' WHERE id = 3")

	t.Run("list all branches", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/branches", nil)
		w := httptest.NewRecorder()

		server.handleListBranches(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		branches := response["branches"].([]interface{})
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

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		branches := response["branches"].([]interface{})
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

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		branches := response["branches"].([]interface{})
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

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		branches := response["branches"].([]interface{})
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
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create repos and jobs
	repo1, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo1"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	repo2, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo2"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Add 3 jobs to repo1
	for i := 0; i < 3; i++ {
		sha := "repo1sha" + string(rune('a'+i))
		commit, err := db.GetOrCreateCommit(repo1.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		if _, err := db.EnqueueJob(repo1.ID, commit.ID, sha, "", "test", "", ""); err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
	}

	// Add 2 jobs to repo2
	for i := 0; i < 2; i++ {
		sha := "repo2sha" + string(rune('a'+i))
		commit, err := db.GetOrCreateCommit(repo2.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		if _, err := db.EnqueueJob(repo2.ID, commit.ID, sha, "", "test", "", ""); err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
	}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		// Invalid limit uses default (50), we have 5 jobs
		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs with invalid limit (uses default), got %d", len(response.Jobs))
		}
	})
}

func TestHandleStatus(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	t.Run("returns status with version", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var status storage.DaemonStatus
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

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
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		// MaxWorkers should match the pool size (config default), not a potentially reloaded config value
		if status.MaxWorkers != cfg.MaxWorkers {
			t.Errorf("Expected MaxWorkers %d from pool, got %d", cfg.MaxWorkers, status.MaxWorkers)
		}
	})

	t.Run("config_reloaded_at empty initially", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		var status storage.DaemonStatus
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		// ConfigReloadedAt should be empty when no reload has occurred
		if status.ConfigReloadedAt != "" {
			t.Errorf("Expected ConfigReloadedAt to be empty initially, got %q", status.ConfigReloadedAt)
		}
	})
}

func TestHandleCancelJob(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a repo and job
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "canceltest", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "canceltest", "", "test", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	t.Run("cancel queued job", func(t *testing.T) {
		reqBody, _ := json.Marshal(CancelJobRequest{JobID: job.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/job/cancel", bytes.NewReader(reqBody))
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
		reqBody, _ := json.Marshal(CancelJobRequest{JobID: job.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/job/cancel", bytes.NewReader(reqBody))
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for already canceled job, got %d", w.Code)
		}
	})

	t.Run("cancel nonexistent job fails", func(t *testing.T) {
		reqBody, _ := json.Marshal(CancelJobRequest{JobID: 99999})
		req := httptest.NewRequest(http.MethodPost, "/api/job/cancel", bytes.NewReader(reqBody))
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for nonexistent job, got %d", w.Code)
		}
	})

	t.Run("cancel with missing job_id fails", func(t *testing.T) {
		reqBody, _ := json.Marshal(map[string]interface{}{})
		req := httptest.NewRequest(http.MethodPost, "/api/job/cancel", bytes.NewReader(reqBody))
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
		commit2, err := db.GetOrCreateCommit(repo.ID, "cancelrunning", "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job2, err := db.EnqueueJob(repo.ID, commit2.ID, "cancelrunning", "", "test", "", "")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		if _, err := db.ClaimJob("worker-1"); err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}

		reqBody, _ := json.Marshal(CancelJobRequest{JobID: job2.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/job/cancel", bytes.NewReader(reqBody))
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
	db := testutil.OpenTestDB(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create test repo and jobs
	repo, err := db.GetOrCreateRepo("/test/repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create 10 jobs
	for i := 0; i < 10; i++ {
		commit, err := db.GetOrCreateCommit(repo.ID, fmt.Sprintf("sha%d", i), "author", fmt.Sprintf("subject%d", i), time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		_, err = db.EnqueueJob(repo.ID, commit.ID, fmt.Sprintf("sha%d", i), "", "test", "", "")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
	}

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
		if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
		if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
		json.NewDecoder(w1.Body).Decode(&result1)

		// Second page
		req2 := httptest.NewRequest("GET", "/api/jobs?limit=3&offset=3", nil)
		w2 := httptest.NewRecorder()
		server.handleListJobs(w2, req2)

		var result2 struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		json.NewDecoder(w2.Body).Decode(&result2)

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
		if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		// Should return all 10 jobs since offset is ignored with limit=0
		if len(result.Jobs) != 10 {
			t.Errorf("Expected 10 jobs (offset ignored with limit=0), got %d", len(result.Jobs))
		}
	})
}

func TestListJobsWithGitRefFilter(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create repo and jobs with different git refs
	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	refs := []string{"abc123", "def456", "abc123..def456"}
	for _, ref := range refs {
		commit, _ := db.GetOrCreateCommit(repo.ID, ref, "A", "S", time.Now())
		db.EnqueueJob(repo.ID, commit.ID, ref, "", "codex", "", "")
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
		if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
		json.NewDecoder(w.Body).Decode(&result)

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
		json.NewDecoder(w.Body).Decode(&result)

		if len(result.Jobs) != 1 {
			t.Errorf("Expected 1 job with range ref, got %d", len(result.Jobs))
		}
	})
}

func TestHandleStreamEvents(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	t.Run("returns correct headers", func(t *testing.T) {
		// Create a request with a context that we can cancel
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/api/stream/events", nil).WithContext(ctx)
		w := newSafeRecorder()

		// Capture initial subscriber count to detect when this handler subscribes
		initialCount := server.broadcaster.SubscriberCount()

		// Run handler in goroutine since it blocks
		done := make(chan struct{})
		go func() {
			server.handleStreamEvents(w, req)
			close(done)
		}()

		// Wait for handler to subscribe
		if !waitForSubscriberIncrease(server.broadcaster, initialCount, time.Second) {
			t.Fatal("Timed out waiting for subscriber")
		}

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
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/api/stream/events", nil).WithContext(ctx)
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
			t.Fatal("Timed out waiting for subscriber")
		}

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
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/api/stream/events", nil).WithContext(ctx)
		w := newSafeRecorder()

		initialCount := server.broadcaster.SubscriberCount()

		done := make(chan struct{})
		go func() {
			server.handleStreamEvents(w, req)
			close(done)
		}()

		if !waitForSubscriberIncrease(server.broadcaster, initialCount, time.Second) {
			t.Fatal("Timed out waiting for subscriber")
		}

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
			t.Fatal("Timed out waiting for events")
		}
		cancel()
		<-done

		body := w.bodyString()
		lines := strings.Split(strings.TrimSpace(body), "\n")

		// Parse into maps to check actual key presence (not just empty values)
		var rawEvents []map[string]interface{}
		for _, line := range lines {
			if line == "" {
				continue
			}
			var raw map[string]interface{}
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
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/api/stream/events", nil).WithContext(ctx)
		w := newSafeRecorder()

		initialCount := server.broadcaster.SubscriberCount()

		done := make(chan struct{})
		go func() {
			server.handleStreamEvents(w, req)
			close(done)
		}()

		if !waitForSubscriberIncrease(server.broadcaster, initialCount, time.Second) {
			t.Fatal("Timed out waiting for subscriber")
		}

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
		ctx, cancel := context.WithCancel(context.Background())
		// Filter for repo1 only
		req := httptest.NewRequest(http.MethodGet, "/api/stream/events?repo="+url.QueryEscape("/test/repo1"), nil).WithContext(ctx)
		w := newSafeRecorder()

		initialCount := server.broadcaster.SubscriberCount()

		// Run handler in goroutine
		done := make(chan struct{})
		go func() {
			server.handleStreamEvents(w, req)
			close(done)
		}()

		if !waitForSubscriberIncrease(server.broadcaster, initialCount, time.Second) {
			t.Fatal("Timed out waiting for subscriber")
		}

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
		ctx, cancel := context.WithCancel(context.Background())
		// Repo path with spaces
		repoPath := "/test/my repo with spaces"
		req := httptest.NewRequest(http.MethodGet, "/api/stream/events?repo="+url.QueryEscape(repoPath), nil).WithContext(ctx)
		w := newSafeRecorder()

		initialCount := server.broadcaster.SubscriberCount()

		// Run handler in goroutine
		done := make(chan struct{})
		go func() {
			server.handleStreamEvents(w, req)
			close(done)
		}()

		if !waitForSubscriberIncrease(server.broadcaster, initialCount, time.Second) {
			t.Fatal("Timed out waiting for subscriber")
		}

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
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a repo
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("rerun failed job", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-failed", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(repo.ID, commit.ID, "rerun-failed", "", "test", "", "")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "some error")

		reqBody, _ := json.Marshal(RerunJobRequest{JobID: job.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/job/rerun", bytes.NewReader(reqBody))
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
		job, _ := db.EnqueueJob(repo.ID, commit.ID, "rerun-canceled", "", "test", "", "")
		db.CancelJob(job.ID)

		reqBody, _ := json.Marshal(RerunJobRequest{JobID: job.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/job/rerun", bytes.NewReader(reqBody))
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
		job, _ := db.EnqueueJob(repo.ID, commit.ID, "rerun-done", "", "test", "", "")
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

		reqBody, _ := json.Marshal(RerunJobRequest{JobID: job.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/job/rerun", bytes.NewReader(reqBody))
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
		job, _ := db.EnqueueJob(repo.ID, commit.ID, "rerun-queued", "", "test", "", "")

		reqBody, _ := json.Marshal(RerunJobRequest{JobID: job.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/job/rerun", bytes.NewReader(reqBody))
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for queued job, got %d", w.Code)
		}
	})

	t.Run("rerun nonexistent job fails", func(t *testing.T) {
		reqBody, _ := json.Marshal(RerunJobRequest{JobID: 99999})
		req := httptest.NewRequest(http.MethodPost, "/api/job/rerun", bytes.NewReader(reqBody))
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for nonexistent job, got %d", w.Code)
		}
	})

	t.Run("rerun with missing job_id fails", func(t *testing.T) {
		reqBody, _ := json.Marshal(map[string]interface{}{})
		req := httptest.NewRequest(http.MethodPost, "/api/job/rerun", bytes.NewReader(reqBody))
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

func TestHandleEnqueueExcludedBranch(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a git repo
	repoDir := filepath.Join(tmpDir, "testrepo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("Failed to create repo dir: %v", err)
	}

	// Initialize git repo
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "checkout", "-b", "wip-feature"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Create a commit so we have a valid SHA
	testFile := filepath.Join(repoDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = repoDir
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commitCmd := exec.Command("git", "commit", "-m", "initial commit")
	commitCmd.Dir = repoDir
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	// Create .roborev.toml with excluded_branches
	repoConfig := filepath.Join(repoDir, ".roborev.toml")
	configContent := `excluded_branches = ["wip-feature", "draft"]`
	if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write repo config: %v", err)
	}

	t.Run("enqueue on excluded branch returns skipped", func(t *testing.T) {
		reqData := map[string]string{"repo_path": repoDir, "git_ref": "HEAD", "agent": "test"}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for skipped enqueue, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if skipped, ok := response["skipped"].(bool); !ok || !skipped {
			t.Errorf("Expected skipped=true, got %v", response)
		}

		if reason, ok := response["reason"].(string); !ok || !strings.Contains(reason, "wip-feature") {
			t.Errorf("Expected reason to mention branch name, got %v", response)
		}

		// Verify no job was created
		queued, _, _, _, _, _ := db.GetJobCounts()
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

		reqData := map[string]string{"repo_path": repoDir, "git_ref": "HEAD", "agent": "test"}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("Expected status 201 for successful enqueue, got %d: %s", w.Code, w.Body.String())
		}

		// Verify job was created
		queued, _, _, _, _, _ := db.GetJobCounts()
		if queued != 1 {
			t.Errorf("Expected 1 queued job, got %d", queued)
		}
	})
}

func TestHandleEnqueueBodySizeLimit(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a git repo
	repoDir := filepath.Join(tmpDir, "testrepo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("Failed to create repo dir: %v", err)
	}

	// Initialize git repo with a commit
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	testFile := filepath.Join(repoDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	addCmd := exec.Command("git", "-C", repoDir, "add", ".")
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commitCmd := exec.Command("git", "-C", repoDir, "commit", "-m", "init")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	t.Run("rejects oversized request body", func(t *testing.T) {
		// Create a request body larger than 250KB
		largeDiff := strings.Repeat("a", 300*1024) // 300KB
		reqData := map[string]string{
			"repo_path":    repoDir,
			"git_ref":      "dirty",
			"agent":        "test",
			"diff_content": largeDiff,
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("Expected status 413, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if errMsg, ok := response["error"].(string); !ok || !strings.Contains(errMsg, "too large") {
			t.Errorf("Expected error about body size, got %v", response)
		}
	})

	t.Run("rejects dirty review with empty diff_content", func(t *testing.T) {
		// git_ref="dirty" with empty diff_content should return a clear error
		reqData := map[string]string{
			"repo_path": repoDir,
			"git_ref":   "dirty",
			"agent":     "test",
			// diff_content intentionally omitted/empty
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if errMsg, ok := response["error"].(string); !ok || !strings.Contains(errMsg, "diff_content required") {
			t.Errorf("Expected error about diff_content required, got %v", response)
		}
	})

	t.Run("accepts valid size dirty request", func(t *testing.T) {
		// Create a valid-sized diff (under 200KB)
		validDiff := strings.Repeat("a", 100*1024) // 100KB
		reqData := map[string]string{
			"repo_path":    repoDir,
			"git_ref":      "dirty",
			"agent":        "test",
			"diff_content": validDiff,
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestHandleListJobsByID(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a git repo
	repoDir := filepath.Join(tmpDir, "testrepo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("Failed to create repo dir: %v", err)
	}

	// Initialize git repo with a commit
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	testFile := filepath.Join(repoDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	addCmd := exec.Command("git", "-C", repoDir, "add", ".")
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commitCmd := exec.Command("git", "-C", repoDir, "commit", "-m", "init")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	// Create multiple jobs
	var job1ID, job2ID, job3ID int64

	// Enqueue job 1
	reqData := map[string]string{"repo_path": repoDir, "git_ref": "HEAD", "agent": "test"}
	reqBody, _ := json.Marshal(reqData)
	req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleEnqueue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Failed to create job 1: %d %s", w.Code, w.Body.String())
	}
	var respJob storage.ReviewJob
	json.NewDecoder(w.Body).Decode(&respJob)
	job1ID = respJob.ID

	// Enqueue job 2
	reqBody, _ = json.Marshal(reqData)
	req = httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.handleEnqueue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Failed to create job 2: %d %s", w.Code, w.Body.String())
	}
	json.NewDecoder(w.Body).Decode(&respJob)
	job2ID = respJob.ID

	// Enqueue job 3
	reqBody, _ = json.Marshal(reqData)
	req = httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.handleEnqueue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Failed to create job 3: %d %s", w.Code, w.Body.String())
	}
	json.NewDecoder(w.Body).Decode(&respJob)
	job3ID = respJob.ID

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
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
	// Create a test git repo
	repoDir := t.TempDir()
	initCmd := exec.Command("git", "init", repoDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	configCmd := exec.Command("git", "-C", repoDir, "config", "user.email", "test@test.com")
	configCmd.Run()
	configCmd = exec.Command("git", "-C", repoDir, "config", "user.name", "Test")
	configCmd.Run()

	// Create initial commit
	testFile := filepath.Join(repoDir, "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)
	addCmd := exec.Command("git", "-C", repoDir, "add", ".")
	addCmd.Run()
	commitCmd := exec.Command("git", "-C", repoDir, "commit", "-m", "init")
	commitCmd.Run()

	db, _ := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	t.Run("enqueues prompt job successfully", func(t *testing.T) {
		reqData := map[string]string{
			"repo_path":     repoDir,
			"git_ref":       "prompt",
			"agent":         "test",
			"custom_prompt": "Explain this codebase",
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		if err := json.NewDecoder(w.Body).Decode(&job); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

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
		reqData := map[string]string{
			"repo_path": repoDir,
			"git_ref":   "prompt",
			"agent":     "test",
			// no custom_prompt - should try to resolve "prompt" as a git ref
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
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
		reqData := map[string]string{
			"repo_path":     repoDir,
			"git_ref":       "prompt",
			"agent":         "test",
			"reasoning":     "fast",
			"custom_prompt": "Quick analysis",
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&job)

		if job.Reasoning != "fast" {
			t.Errorf("Expected reasoning 'fast', got '%s'", job.Reasoning)
		}
	})

	t.Run("prompt job with agentic flag", func(t *testing.T) {
		reqData := map[string]interface{}{
			"repo_path":     repoDir,
			"git_ref":       "prompt",
			"agent":         "test",
			"custom_prompt": "Fix all bugs",
			"agentic":       true,
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&job)

		if !job.Agentic {
			t.Error("Expected Agentic to be true")
		}
	})

	t.Run("prompt job without agentic defaults to false", func(t *testing.T) {
		reqData := map[string]string{
			"repo_path":     repoDir,
			"git_ref":       "prompt",
			"agent":         "test",
			"custom_prompt": "Read-only review",
		}
		reqBody, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&job)

		if job.Agentic {
			t.Error("Expected Agentic to be false by default")
		}
	})
}

func TestGetMachineID_CachingBehavior(t *testing.T) {
	t.Run("caches valid machine ID", func(t *testing.T) {
		db, _ := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		server := NewServer(db, cfg, "")

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

		db, tmpDir := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		server := NewServer(db, cfg, "")

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
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

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
			job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "test-agent", "", "")
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
			reqData := map[string]interface{}{
				"job_id":    job.ID,
				"commenter": "test-user",
				"comment":   "Test comment for " + tc.name,
			}
			reqBody, _ := json.Marshal(reqData)
			req := httptest.NewRequest(http.MethodPost, "/api/comment", bytes.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.handleAddComment(w, req)

			if w.Code != http.StatusCreated {
				t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
			}

			// Verify response contains the comment
			var resp storage.Response
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("Failed to unmarshal response: %v", err)
			}
			if resp.Responder != "test-user" {
				t.Errorf("Expected responder 'test-user', got %q", resp.Responder)
			}
		})
	}
}

// TestHandleAddCommentToNonExistentJob tests that adding a comment to a
// non-existent job returns 404.
func TestHandleAddCommentToNonExistentJob(t *testing.T) {
	db, _ := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	reqData := map[string]interface{}{
		"job_id":    99999,
		"commenter": "test-user",
		"comment":   "This should fail",
	}
	reqBody, _ := json.Marshal(reqData)
	req := httptest.NewRequest(http.MethodPost, "/api/comment", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
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
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create repo, commit, and job (but NO review)
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "test-repo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "test-agent", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Set job to running (no review exists yet)
	if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("Failed to set job to running: %v", err)
	}

	// Verify no review exists
	if _, err := db.GetReviewByJobID(job.ID); err == nil {
		t.Fatal("Expected no review to exist for job")
	}

	// Add comment - should succeed even without a review
	reqData := map[string]interface{}{
		"job_id":    job.ID,
		"commenter": "test-user",
		"comment":   "Comment on in-progress job without review",
	}
	reqBody, _ := json.Marshal(reqData)
	req := httptest.NewRequest(http.MethodPost, "/api/comment", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
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
	db, _ := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	req := httptest.NewRequest(http.MethodGet, "/api/job/output?job_id=notanumber", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleJobOutput_NonExistentJob tests that non-existent job returns 404.
func TestHandleJobOutput_NonExistentJob(t *testing.T) {
	db, _ := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	req := httptest.NewRequest(http.MethodGet, "/api/job/output?job_id=99999", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleJobOutput_PollingRunningJob tests polling mode for a running job.
func TestHandleJobOutput_PollingRunningJob(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a running job
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "test-repo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Test", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "test-agent", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	result, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("Expected 1 row affected, got %d", n)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/job/output?job_id=%d", job.ID), nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		JobID   int64 `json:"job_id"`
		Status  string `json:"status"`
		Lines   []struct {
			TS       string `json:"ts"`
			Text     string `json:"text"`
			LineType string `json:"line_type"`
		} `json:"lines"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

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
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a completed job
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "test-repo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Test", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "test-agent", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	result, err := db.Exec(`UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`, job.ID)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("Expected 1 row affected, got %d", n)
	}

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
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

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
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	// Create a completed job
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "test-repo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Test", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "test-agent", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	result, err := db.Exec(`UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`, job.ID)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("Expected 1 row affected, got %d", n)
	}

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
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if resp.Type != "complete" {
		t.Errorf("Expected type 'complete', got %q", resp.Type)
	}
	if resp.Status != "done" {
		t.Errorf("Expected status 'done', got %q", resp.Status)
	}
}

// TestHandleJobOutput_MissingJobID tests that missing job_id returns 400.
func TestHandleJobOutput_MissingJobID(t *testing.T) {
	db, _ := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	req := httptest.NewRequest(http.MethodGet, "/api/job/output", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}
