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

// writeJSON encodes data as JSON to the response writer.
func writeJSON(w http.ResponseWriter, data interface{}) {
	json.NewEncoder(w).Encode(data)
}

// mockJobsHandler returns an http.HandlerFunc that filters the given jobs
// by git_ref and repo query parameters, matching the daemon's /api/jobs behavior.
func mockJobsHandler(t *testing.T, jobs []storage.ReviewJob) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" || r.Method != "GET" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gitRef := r.URL.Query().Get("git_ref")
		repo := r.URL.Query().Get("repo")
		var matching []storage.ReviewJob
		for _, job := range jobs {
			if (gitRef == "" || job.GitRef == gitRef) && (repo == "" || job.RepoPath == repo) {
				matching = append(matching, job)
			}
		}
		writeJSON(w, map[string]interface{}{"jobs": matching})
	}
}

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
				writeJSON(w, map[string]interface{}{
					"responses": []storage.Response{
						{ID: 1, Responder: "user", Response: "Fixed it"},
						{ID: 2, Responder: "agent", Response: "Verified"},
					},
				})
			} else {
				writeJSON(w, map[string]interface{}{"responses": []storage.Response{}})
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"jobs": []storage.ReviewJob{{ID: 1, Status: storage.JobStatusDone}},
			})
		})
		mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{ID: 1, JobID: 1, Output: "Review complete"})
		})
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			pollCount++
			status := storage.JobStatusQueued
			if pollCount >= 3 {
				status = storage.JobStatusDone
			}
			writeJSON(w, map[string]interface{}{
				"jobs": []storage.ReviewJob{{ID: 1, Status: status}},
			})
		})
		mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{ID: 1, JobID: 1, Output: "Review after polling"})
		})
		_, cleanup := setupMockDaemon(t, mux)
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
			writeJSON(w, map[string]interface{}{
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
			writeJSON(w, map[string]interface{}{
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
		repoDir := t.TempDir()
		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: repoDir},
			{ID: 2, GitRef: "def456", RepoPath: repoDir},
		}
		_, cleanup := setupMockDaemon(t, mockJobsHandler(t, allJobs))
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
		_, cleanup := setupMockDaemon(t, mockJobsHandler(t, allJobs))
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
		otherRepo := t.TempDir()
		anotherRepo := t.TempDir()
		queryRepo := t.TempDir()
		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: otherRepo},
			{ID: 2, GitRef: "abc123", RepoPath: anotherRepo},
		}
		_, cleanup := setupMockDaemon(t, mockJobsHandler(t, allJobs))
		defer cleanup()

		job, err := findJobForCommit(queryRepo, "abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job != nil {
			t.Error("expected nil job when fallback only has jobs from different repos")
		}
	})

	t.Run("fallback matches job with same normalized path", func(t *testing.T) {
		tmpDir := t.TempDir()
		repoPath := filepath.Join(tmpDir, "repo")
		if err := os.MkdirAll(repoPath, 0755); err != nil {
			t.Fatal(err)
		}

		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: repoPath},
		}
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/jobs" || r.Method != "GET" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			gitRef := r.URL.Query().Get("git_ref")
			repo := r.URL.Query().Get("repo")
			var matching []storage.ReviewJob
			for _, job := range allJobs {
				if repo != "" && job.RepoPath != repo {
					continue
				}
				if gitRef == "" || job.GitRef == gitRef {
					matching = append(matching, job)
				}
			}
			writeJSON(w, map[string]interface{}{"jobs": matching})
		}))
		defer cleanup()

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
		repoDir := t.TempDir()
		allJobs := []storage.ReviewJob{
			{ID: 1, GitRef: "abc123", RepoPath: ""},
			{ID: 2, GitRef: "abc123", RepoPath: "relative/path"},
			{ID: 3, GitRef: "abc123", RepoPath: repoDir},
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

			if repo != "" {
				writeJSON(w, map[string]interface{}{"jobs": []storage.ReviewJob{}})
				return
			}

			var matching []storage.ReviewJob
			for _, job := range allJobs {
				if gitRef == "" || job.GitRef == gitRef {
					matching = append(matching, job)
				}
			}
			writeJSON(w, map[string]interface{}{"jobs": matching})
		}))
		defer cleanup()

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
		if requestCount != 2 {
			t.Errorf("expected 2 requests (primary + fallback), got %d", requestCount)
		}
	})
}
