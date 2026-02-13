package main

// Tests for the wait command

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestWaitJobFlagRequiresArgument(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cleanup()

	chdir(t, repo.Dir)

	var stderr bytes.Buffer
	cmd := waitCmd()
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--job"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error when --job is used without argument")
	}
	if !strings.Contains(err.Error(), "--job requires a job ID argument") {
		t.Errorf("expected '--job requires a job ID argument' error, got: %v", err)
	}
}

func TestWaitSHAAndPositionalArgConflict(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD", "42"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error when both --sha and positional arg are given")
	}
	if !strings.Contains(err.Error(), "cannot use both") {
		t.Errorf("expected 'cannot use both' error, got: %v", err)
	}
}

func TestWaitExitCode4WhenNoJobFound(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{},
				"has_more": false,
			})
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	t.Run("no job for SHA exits 4", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := waitCmd()
		cmd.SetOut(&stdout)
		cmd.SetArgs([]string{"--sha", "HEAD"})
		err := cmd.Execute()

		if err == nil {
			t.Fatal("expected exit error for missing job")
		}
		exitErr, ok := err.(*exitError)
		if !ok {
			t.Fatalf("expected exitError, got: %T %v", err, err)
		}
		if exitErr.code != 4 {
			t.Errorf("expected exit code 4, got: %d", exitErr.code)
		}
		if !strings.Contains(stdout.String(), "No job found") {
			t.Errorf("expected 'No job found' message, got: %q", stdout.String())
		}
	})

	t.Run("no job for SHA quiet exits 4 with no output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cmd := waitCmd()
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--sha", "HEAD", "--quiet"})
		err := cmd.Execute()

		if err == nil {
			t.Fatal("expected exit error for missing job")
		}
		exitErr, ok := err.(*exitError)
		if !ok {
			t.Fatalf("expected exitError, got: %T %v", err, err)
		}
		if exitErr.code != 4 {
			t.Errorf("expected exit code 4, got: %d", exitErr.code)
		}
		if stdout.String() != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout.String())
		}
	})
}

func TestWaitExitCode4WhenJobIDNotFound(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			// Return empty jobs list for any query
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{},
				"has_more": false,
			})
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	var stdout bytes.Buffer
	cmd := waitCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--job", "99999"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected exit error for missing job ID")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("expected exitError, got: %T %v", err, err)
	}
	if exitErr.code != 4 {
		t.Errorf("expected exit code 4, got: %d", exitErr.code)
	}
}

func TestWaitExitCode3OnTimeout(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			// If querying by git_ref, return the job
			if r.URL.Query().Get("git_ref") != "" {
				job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "running"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{job},
					"has_more": false,
				})
				return
			}
			// If querying by id, always return running
			job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "running"}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD", "--timeout", "1", "--quiet"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected exit error for timeout")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("expected exitError, got: %T %v", err, err)
	}
	if exitErr.code != 3 {
		t.Errorf("expected exit code 3 for timeout, got: %d", exitErr.code)
	}
}

func TestWaitPassingReview(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			if r.URL.Query().Get("git_ref") != "" {
				job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{job},
					"has_more": false,
				})
				return
			}
			job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
			return
		}
		if r.URL.Path == "/api/review" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."})
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD", "--quiet"})
	err := cmd.Execute()

	if err != nil {
		t.Errorf("expected exit 0 for passing review, got error: %v", err)
	}
}

func TestWaitFailingReview(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			if r.URL.Query().Get("git_ref") != "" {
				job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{job},
					"has_more": false,
				})
				return
			}
			job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
			return
		}
		if r.URL.Path == "/api/review" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "Found 2 issues:\n1. Bug\n2. Missing check"})
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD", "--quiet"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected exit 1 for failing review")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("expected exitError, got: %T %v", err, err)
	}
	if exitErr.code != 1 {
		t.Errorf("expected exit code 1, got: %d", exitErr.code)
	}
}

func TestWaitReviewErrorNotRemappedToCode4(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			if r.URL.Query().Get("git_ref") != "" {
				job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{job},
					"has_more": false,
				})
				return
			}
			job := storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
			return
		}
		if r.URL.Path == "/api/review" {
			// Return 404 — "no review found" should NOT become exit code 4
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD", "--quiet"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error when review not found")
	}
	// This should NOT be exit code 4 — it's a "no review found" error,
	// not a "no job found" error
	if exitErr, ok := err.(*exitError); ok && exitErr.code == 4 {
		t.Error("'no review found' error should not be remapped to exit code 4")
	}
}

func TestWaitWorktreeResolvesRefFromWorktreeAndRepoFromMain(t *testing.T) {
	setupFastPolling(t)

	// Create main repo with initial commit
	repo := newTestGitRepo(t)
	mainSHA := repo.CommitFile("file.txt", "content", "initial on main")

	// Create a worktree on a new branch with a different commit
	worktreeDir := t.TempDir()
	os.Remove(worktreeDir) // git worktree add needs to create it
	repo.Run("worktree", "add", "-b", "wt-branch", worktreeDir)

	// Make a new commit in the worktree so HEAD differs from main
	wtRepo := &TestGitRepo{Dir: worktreeDir, t: t}
	wtSHA := wtRepo.CommitFile("wt-file.txt", "worktree content", "commit in worktree")

	// Sanity check: SHAs should differ
	if mainSHA == wtSHA {
		t.Fatal("expected different SHAs for main and worktree commits")
	}

	var lookupQuery string
	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			// Capture only the lookup query (has git_ref), not the poll query (has id)
			if r.URL.Query().Get("git_ref") != "" {
				lookupQuery = r.URL.RawQuery
			}
			// Return a job so we can proceed to waitForJob
			job := storage.ReviewJob{ID: 1, GitRef: wtSHA, Agent: "test", Status: "done"}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
			return
		}
		if r.URL.Path == "/api/review" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."})
			return
		}
	}))
	defer cleanup()

	// Run wait from the worktree directory
	chdir(t, worktreeDir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD", "--quiet"})
	err := cmd.Execute()

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if lookupQuery == "" {
		t.Fatal("expected a lookup query with git_ref, but none was captured")
	}

	// git_ref should be the worktree's HEAD (wtSHA), not the main repo's HEAD
	if !strings.Contains(lookupQuery, "git_ref="+url.QueryEscape(wtSHA)) {
		t.Errorf("expected git_ref=%s (worktree HEAD) in query, got: %s", wtSHA, lookupQuery)
	}
	if strings.Contains(lookupQuery, "git_ref="+url.QueryEscape(mainSHA)) {
		t.Errorf("git_ref should NOT be main repo HEAD %s, got: %s", mainSHA, lookupQuery)
	}

	// repo should be the main repo path, not the worktree path
	if strings.Contains(lookupQuery, url.QueryEscape(worktreeDir)) {
		t.Errorf("repo param should be main repo path, not worktree path; got: %s", lookupQuery)
	}
	if !strings.Contains(lookupQuery, url.QueryEscape(repo.Dir)) {
		t.Errorf("expected repo=%s (main repo) in query, got: %s", repo.Dir, lookupQuery)
	}
}
