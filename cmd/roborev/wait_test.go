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

// waitMockHandler builds an HTTP handler for wait command tests.
// If job is non-nil it is returned for every /api/jobs query; otherwise an
// empty list is returned. If review is non-nil it is served on /api/review.
func waitMockHandler(job *storage.ReviewJob, review *storage.Review) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			var jobs []storage.ReviewJob
			if job != nil {
				jobs = []storage.ReviewJob{*job}
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     jobs,
				"has_more": false,
			})
			return
		}
		if r.URL.Path == "/api/review" && review != nil {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(review)
			return
		}
	}
}

// requireExitCode asserts err is an *exitError with the expected code.
func requireExitCode(t *testing.T, err error, code int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected exit code %d, got nil error", code)
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("expected *exitError with code %d, got %T: %v", code, err, err)
	}
	if exitErr.code != code {
		t.Errorf("expected exit code %d, got: %d", code, exitErr.code)
	}
}

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

func TestWaitInvalidPositionalArg(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"not-a-ref-or-number"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for invalid argument")
	}
	if !strings.Contains(err.Error(), "not a valid git ref or job ID") {
		t.Errorf("expected 'not a valid git ref or job ID' error, got: %v", err)
	}
}

func TestWaitPositionalZeroIsInvalid(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"0"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for positional arg 0")
	}
	if !strings.Contains(err.Error(), "not a valid git ref or job ID") {
		t.Errorf("expected 'not a valid git ref or job ID' error, got: %v", err)
	}
}

func TestWaitInvalidSHARef(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "not-a-valid-ref"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for invalid git ref")
	}
	if !strings.Contains(err.Error(), "invalid git ref") {
		t.Errorf("expected 'invalid git ref' error, got: %v", err)
	}
}

func TestWaitJobIDRejectsNonPositive(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--job", "0"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for --job 0")
	}
	if !strings.Contains(err.Error(), "invalid job ID") {
		t.Errorf("expected 'invalid job ID' error, got: %v", err)
	}
}

func TestWaitExitsWhenNoJobFound(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, waitMockHandler(nil, nil))
	defer cleanup()

	chdir(t, repo.Dir)

	t.Run("No job for SHA", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := waitCmd()
		cmd.SetOut(&stdout)
		cmd.SetArgs([]string{"--sha", "HEAD"})
		err := cmd.Execute()

		requireExitCode(t, err, 1)
		if !strings.Contains(stdout.String(), "No job found") {
			t.Errorf("expected 'No job found' message, got: %q", stdout.String())
		}
	})

	t.Run("No job for SHA quiet mode suppresses output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cmd := waitCmd()
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--sha", "HEAD", "--quiet"})
		err := cmd.Execute()

		requireExitCode(t, err, 1)
		if stdout.String() != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout.String())
		}
	})
}

func TestWaitExitsWhenJobIDNotFound(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, waitMockHandler(nil, nil))
	defer cleanup()

	chdir(t, repo.Dir)

	var stdout bytes.Buffer
	cmd := waitCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--job", "99999"})
	err := cmd.Execute()

	requireExitCode(t, err, 1)
}

func TestWaitPassingReview(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")

	job := &storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
	review := &storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."}
	_, cleanup := setupMockDaemon(t, waitMockHandler(job, review))
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

	job := &storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
	review := &storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "Found 2 issues:\n1. Bug\n2. Missing check"}
	_, cleanup := setupMockDaemon(t, waitMockHandler(job, review))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD", "--quiet"})
	err := cmd.Execute()

	requireExitCode(t, err, 1)
}

func TestWaitPositionalArgAsGitRef(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")

	job := &storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
	review := &storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."}
	_, cleanup := setupMockDaemon(t, waitMockHandler(job, review))
	defer cleanup()

	chdir(t, repo.Dir)

	// Pass HEAD as a positional arg (not --sha flag) to exercise the
	// git-ref-first resolution path.
	cmd := waitCmd()
	cmd.SetArgs([]string{"HEAD", "--quiet"})
	err := cmd.Execute()

	if err != nil {
		t.Errorf("expected exit 0 for positional git ref, got error: %v", err)
	}
}

func TestWaitNumericFallbackToJobID(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	// "42" is not a valid git ref, so the wait command should fall back to
	// treating it as a numeric job ID and query by id (not git_ref).
	var lastJobsQuery string
	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			lastJobsQuery = r.URL.RawQuery
			job := storage.ReviewJob{ID: 42, GitRef: "abc", Agent: "test", Status: "done"}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
			return
		}
		if r.URL.Path == "/api/review" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 42, Agent: "test", Output: "No issues found."})
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"42", "--quiet"})
	err := cmd.Execute()

	if err != nil {
		t.Errorf("expected exit 0 for numeric arg as job ID, got: %v", err)
	}
	// The final poll should query by id=42, not git_ref=42
	if !strings.Contains(lastJobsQuery, "id=42") {
		t.Errorf("expected query by id=42, got: %s", lastJobsQuery)
	}
}

func TestWaitReviewFetchErrorIsPlainError(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")

	// Job completes but /api/review returns 404. This should surface as a
	// plain error (from showReview), not an *exitError. Both map to exit 1
	// in practice, but the distinction matters for error reporting: plain
	// errors print Cobra's "Error:" prefix while exitError silences it.
	job := &storage.ReviewJob{ID: 1, GitRef: sha, Agent: "test", Status: "done"}
	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{*job},
				"has_more": false,
			})
			return
		}
		if r.URL.Path == "/api/review" {
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
	// Should be a plain error, not an *exitError
	if _, ok := err.(*exitError); ok {
		t.Error("review fetch failure should be a plain error, not exitError")
	}
}

func TestWaitLookupNon200Response(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("database locked"))
			return
		}
	}))
	defer cleanup()

	chdir(t, repo.Dir)

	cmd := waitCmd()
	cmd.SetArgs([]string{"--sha", "HEAD"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !strings.Contains(err.Error(), "500 Internal Server Error") {
		t.Errorf("expected error to contain status line, got: %v", err)
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
