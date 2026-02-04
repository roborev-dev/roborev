package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestListCommand(t *testing.T) {
	now := time.Now()
	started := now.Add(-10 * time.Second)
	finished := now.Add(-5 * time.Second)
	testJobs := []storage.ReviewJob{
		{
			ID:        1,
			GitRef:    "abc1234567890",
			RepoName:  "myrepo",
			Agent:     "test",
			Status:    storage.JobStatusDone,
			StartedAt: &started,
			FinishedAt: &finished,
		},
		{
			ID:       2,
			GitRef:   "def4567890123",
			RepoName: "myrepo",
			Agent:    "codex",
			Status:   storage.JobStatusQueued,
		},
	}

	t.Run("tabular output shows jobs", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     testJobs,
					"has_more": false,
				})
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		output := captureStdout(t, func() {
			cmd := listCmd()
			cmd.SetArgs([]string{})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		if !strings.Contains(output, "abc1234") {
			t.Errorf("expected short SHA abc1234 in output, got: %s", output)
		}
		if !strings.Contains(output, "myrepo") {
			t.Errorf("expected repo name in output, got: %s", output)
		}
		if !strings.Contains(output, "test") {
			t.Errorf("expected agent name in output, got: %s", output)
		}
		if !strings.Contains(output, "done") {
			t.Errorf("expected status in output, got: %s", output)
		}
	})

	t.Run("json output passes through raw response", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     testJobs,
					"has_more": true,
				})
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		output := captureStdout(t, func() {
			cmd := listCmd()
			cmd.SetArgs([]string{"--json"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		var parsed []storage.ReviewJob
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatalf("json output not valid JSON: %v\noutput: %s", err, output)
		}
		if len(parsed) != 2 {
			t.Errorf("expected 2 jobs, got %d", len(parsed))
		}
	})

	t.Run("has_more shows hint in tabular mode", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     testJobs,
					"has_more": true,
				})
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		output := captureStdout(t, func() {
			cmd := listCmd()
			cmd.SetArgs([]string{})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		if !strings.Contains(output, "more results available") {
			t.Errorf("expected 'more results available' hint, got: %s", output)
		}
	})

	t.Run("empty results shows message", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		output := captureStdout(t, func() {
			cmd := listCmd()
			cmd.SetArgs([]string{})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		if !strings.Contains(output, "No jobs found") {
			t.Errorf("expected 'No jobs found' message, got: %s", output)
		}
	})

	t.Run("non-2xx response returns error", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("database locked"))
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		cmd := listCmd()
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for 500 response")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("expected status code in error, got: %v", err)
		}
	})

	t.Run("non-2xx response returns error with --json", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("invalid status filter"))
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		cmd := listCmd()
		cmd.SetArgs([]string{"--json"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for 400 response with --json")
		}
		if !strings.Contains(err.Error(), "400") {
			t.Errorf("expected status code in error, got: %v", err)
		}
	})

	t.Run("query params pass through correctly", func(t *testing.T) {
		var receivedQuery string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				receivedQuery = r.URL.RawQuery
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		captureStdout(t, func() {
			cmd := listCmd()
			cmd.SetArgs([]string{"--status", "done", "--limit", "10"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		if !strings.Contains(receivedQuery, "status=done") {
			t.Errorf("expected status=done in query, got: %s", receivedQuery)
		}
		if !strings.Contains(receivedQuery, "limit=10") {
			t.Errorf("expected limit=10 in query, got: %s", receivedQuery)
		}
	})

	t.Run("explicit --repo to non-git path sends no branch", func(t *testing.T) {
		var receivedQuery string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				receivedQuery = r.URL.RawQuery
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
		}))
		t.Cleanup(cleanup)

		// cwd is in a git repo with a branch, but --repo points to a non-git path
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		captureStdout(t, func() {
			cmd := listCmd()
			cmd.SetArgs([]string{"--repo", "/some/other/repo"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		if strings.Contains(receivedQuery, "branch=") {
			t.Errorf("expected no branch param when --repo is non-git path, got: %s", receivedQuery)
		}
		if !strings.Contains(receivedQuery, "repo=%2Fsome%2Fother%2Frepo") {
			t.Errorf("expected explicit repo in query, got: %s", receivedQuery)
		}
	})

	t.Run("explicit --repo to cwd repo still auto-resolves branch", func(t *testing.T) {
		var receivedQuery string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				receivedQuery = r.URL.RawQuery
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
		}))
		t.Cleanup(cleanup)

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")
		chdir(t, repo.Dir)

		captureStdout(t, func() {
			cmd := listCmd()
			cmd.SetArgs([]string{"--repo", repo.Dir})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		if !strings.Contains(receivedQuery, "branch=") {
			t.Errorf("expected branch param when --repo is a valid git repo, got: %s", receivedQuery)
		}
	})
}

func TestShowJSONOutput(t *testing.T) {
	t.Run("--json outputs valid JSON", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		review := storage.Review{
			ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
		}
		mockReviewDaemon(t, review)

		chdir(t, repo.Dir)

		cmd := showCmd()
		var buf strings.Builder
		cmd.SetOut(&buf)
		cmd.SetArgs([]string{"--job", "42", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := buf.String()
		var parsed storage.Review
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatalf("--json output not valid JSON: %v\noutput: %s", err, output)
		}
		if parsed.JobID != 42 {
			t.Errorf("expected job_id=42, got %d", parsed.JobID)
		}
		if parsed.Output != "LGTM" {
			t.Errorf("expected output=LGTM, got %q", parsed.Output)
		}
		if parsed.Agent != "test" {
			t.Errorf("expected agent=test, got %q", parsed.Agent)
		}
	})

	t.Run("--json skips formatted header", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
		})

		chdir(t, repo.Dir)

		cmd := showCmd()
		var buf strings.Builder
		cmd.SetOut(&buf)
		cmd.SetArgs([]string{"--job", "42", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := buf.String()
		if strings.Contains(output, "Review for") {
			t.Errorf("--json should not contain formatted header, got: %s", output)
		}
		if strings.Contains(output, "---") {
			t.Errorf("--json should not contain separator, got: %s", output)
		}
	})
}
