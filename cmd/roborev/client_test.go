package main

// Tests for daemon client functions (getCommentsForJob, waitForReview, findJobForCommit)

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestGetCommentsForJob(t *testing.T) {
	t.Run("returns responses for job", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/comments" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			jobID := r.URL.Query().Get("job_id")
			if jobID == "42" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"responses": []storage.Response{
						{ID: 1, Responder: "user", Response: "Fixed it"},
						{ID: 2, Responder: "agent", Response: "Verified"},
					},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]interface{}{"responses": []storage.Response{}})
			}
		}))
		defer cleanup()

		responses, err := getCommentsForJob(42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(responses) != 2 {
			t.Errorf("expected 2 responses, got %d", len(responses))
		}
	})

	t.Run("returns error on non-200", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer cleanup()

		_, err := getCommentsForJob(42)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestWaitForReview(t *testing.T) {
	t.Run("returns review when job completes", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" && r.Method == "GET" {
				// Return done immediately to avoid slow poll sleep
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusDone}},
				})
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 1, Output: "Review complete",
				})
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer cleanup()

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
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" && r.Method == "GET" {
				pollCount++
				if pollCount < 3 {
					// Return queued for first 2 polls
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusQueued}},
					})
				} else {
					// Return done on 3rd poll
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusDone}},
					})
				}
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 1, Output: "Review after polling",
				})
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer cleanup()

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
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusFailed, Error: "agent crashed"}},
			})
		}))
		defer cleanup()

		_, err := waitForReview(1)
		if err == nil {
			t.Fatal("expected error for failed job")
		}
		if !strings.Contains(err.Error(), "agent crashed") {
			t.Errorf("expected error to mention agent crashed: %v", err)
		}
	})

	t.Run("returns error on job canceled", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusCanceled}},
			})
		}))
		defer cleanup()

		_, err := waitForReview(1)
		if err == nil {
			t.Fatal("expected error for canceled job")
		}
		if !strings.Contains(err.Error(), "canceled") {
			t.Errorf("expected error to mention canceled: %v", err)
		}
	})
}

func TestFindJobForCommit(t *testing.T) {
	t.Run("finds matching job", func(t *testing.T) {
		// Use a real temp dir so path normalization works cross-platform
		repoDir := t.TempDir()
		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: repoDir},
			{ID: 2, GitRef: "def456", RepoPath: repoDir},
		}
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Filter by git_ref and repo query parameters
			gitRef := r.URL.Query().Get("git_ref")
			repo := r.URL.Query().Get("repo")
			var matchingJobs []storage.ReviewJob
			for _, job := range allJobs {
				if (gitRef == "" || job.GitRef == gitRef) && (repo == "" || job.RepoPath == repo) {
					matchingJobs = append(matchingJobs, job)
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": matchingJobs,
			})
		}))
		defer cleanup()

		job, err := findJobForCommit(repoDir, "def456")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job == nil {
			t.Fatal("expected to find job")
		}
		if job.ID != 2 {
			t.Errorf("expected job ID 2, got %d", job.ID)
		}
	})

	t.Run("returns nil when not found", func(t *testing.T) {
		repoDir := t.TempDir()
		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: repoDir},
		}
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Filter by git_ref and repo query parameters
			gitRef := r.URL.Query().Get("git_ref")
			repo := r.URL.Query().Get("repo")
			var matchingJobs []storage.ReviewJob
			for _, job := range allJobs {
				if (gitRef == "" || job.GitRef == gitRef) && (repo == "" || job.RepoPath == repo) {
					matchingJobs = append(matchingJobs, job)
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": matchingJobs,
			})
		}))
		defer cleanup()

		job, err := findJobForCommit(repoDir, "notfound")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job != nil {
			t.Error("expected nil job for non-matching SHA")
		}
	})

	t.Run("fallback skips jobs from different repo", func(t *testing.T) {
		// Primary query returns empty (repo mismatch), fallback returns job from different repo
		otherRepo := t.TempDir()
		anotherRepo := t.TempDir()
		queryRepo := t.TempDir()
		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: otherRepo},
			{ID: 2, GitRef: "abc123", RepoPath: anotherRepo},
		}
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			gitRef := r.URL.Query().Get("git_ref")
			repo := r.URL.Query().Get("repo")
			var matchingJobs []storage.ReviewJob
			for _, job := range allJobs {
				if (gitRef == "" || job.GitRef == gitRef) && (repo == "" || job.RepoPath == repo) {
					matchingJobs = append(matchingJobs, job)
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": matchingJobs,
			})
		}))
		defer cleanup()

		// Request job for queryRepo, but all jobs are for different repos
		job, err := findJobForCommit(queryRepo, "abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job != nil {
			t.Error("expected nil job when fallback only has jobs from different repos")
		}
	})

	t.Run("fallback matches job with same normalized path", func(t *testing.T) {
		// Use temp dir for path normalization testing
		tmpDir := t.TempDir()
		repoPath := filepath.Join(tmpDir, "repo")
		if err := os.MkdirAll(repoPath, 0755); err != nil {
			t.Fatal(err)
		}

		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: repoPath}, // Stored with absolute path
		}
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			gitRef := r.URL.Query().Get("git_ref")
			repo := r.URL.Query().Get("repo")
			var matchingJobs []storage.ReviewJob
			for _, job := range allJobs {
				// Primary query with repo filter - simulate mismatch due to path format
				if repo != "" && job.RepoPath != repo {
					continue
				}
				if gitRef == "" || job.GitRef == gitRef {
					matchingJobs = append(matchingJobs, job)
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": matchingJobs,
			})
		}))
		defer cleanup()

		// Request with same path - should find via fallback normalization
		job, err := findJobForCommit(repoPath, "abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job == nil {
			t.Fatal("expected to find job via fallback with normalized path")
		}
		if job.ID != 1 {
			t.Errorf("expected job ID 1, got %d", job.ID)
		}
	})

	t.Run("returns error on non-200 response", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer cleanup()

		_, err := findJobForCommit("/test/repo", "abc123")
		if err == nil {
			t.Fatal("expected error on non-200 response")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("expected error to mention status code, got: %v", err)
		}
	})

	t.Run("returns error on invalid JSON", func(t *testing.T) {
		repoDir := t.TempDir()
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not json"))
		}))
		defer cleanup()

		_, err := findJobForCommit(repoDir, "abc123")
		if err == nil {
			t.Fatal("expected error on invalid JSON")
		}
		if !strings.Contains(err.Error(), "decode") {
			t.Errorf("expected decode error, got: %v", err)
		}
	})

	t.Run("fallback skips jobs with empty or relative paths", func(t *testing.T) {
		// Jobs with empty or relative paths should be skipped in fallback to avoid
		// false matches from cwd resolution. Job 3 has correct SHA but path stored
		// differently (simulating path normalization mismatch).
		repoDir := t.TempDir()
		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: ""},              // Empty path - skip
			{ID: 2, GitRef: "abc123", RepoPath: "relative/path"}, // Relative path - skip
			{ID: 3, GitRef: "abc123", RepoPath: repoDir},         // Absolute path - match via fallback
		}
		requestCount := 0
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			requestCount++
			gitRef := r.URL.Query().Get("git_ref")
			repo := r.URL.Query().Get("repo")

			// Primary query has repo filter - simulate mismatch by returning empty
			// (e.g., client normalized path differently than daemon stored it)
			if repo != "" {
				// Primary query: return empty to force fallback
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs": []storage.ReviewJob{},
				})
				return
			}

			// Fallback query (no repo filter): return all jobs with matching gitRef
			var matchingJobs []storage.ReviewJob
			for _, job := range allJobs {
				if gitRef == "" || job.GitRef == gitRef {
					matchingJobs = append(matchingJobs, job)
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": matchingJobs,
			})
		}))
		defer cleanup()

		// Request job for repoDir - primary query returns empty, fallback should
		// skip empty/relative paths and find job ID 3 via path normalization
		job, err := findJobForCommit(repoDir, "abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job == nil {
			t.Fatal("expected to find job via fallback")
		}
		if job.ID != 3 {
			t.Errorf("expected job ID 3, got %d", job.ID)
		}
		// Verify both primary and fallback queries were made
		if requestCount != 2 {
			t.Errorf("expected 2 requests (primary + fallback), got %d", requestCount)
		}
	})
}
