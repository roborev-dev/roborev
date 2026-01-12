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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesm/roborev/internal/config"
	"github.com/wesm/roborev/internal/storage"
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

func TestHandleListRepos(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)

	t.Run("empty database", func(t *testing.T) {
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
		if _, err := db.EnqueueJob(repo1.ID, commit.ID, sha, "test"); err != nil {
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
		if _, err := db.EnqueueJob(repo2.ID, commit.ID, sha, "test"); err != nil {
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

func TestHandleListJobsWithFilter(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)

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
		if _, err := db.EnqueueJob(repo1.ID, commit.ID, sha, "test"); err != nil {
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
		if _, err := db.EnqueueJob(repo2.ID, commit.ID, sha, "test"); err != nil {
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
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)

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
}

func TestHandleCancelJob(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)

	// Create a repo and job
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "canceltest", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "canceltest", "test")
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
		job2, err := db.EnqueueJob(repo.ID, commit2.ID, "cancelrunning", "test")
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
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)

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
		_, err = db.EnqueueJob(repo.ID, commit.ID, fmt.Sprintf("sha%d", i), "test")
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

func TestHandleStreamEvents(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)

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
