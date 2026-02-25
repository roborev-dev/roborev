package main

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

type mockConfig struct {
	Jobs        []storage.ReviewJob
	Review      *storage.Review
	JobsErr     int                   // Optional HTTP error code
	OnJobsQuery func(r *http.Request) // Optional spy callback
}

func newWaitMockHandler(cfg mockConfig) http.HandlerFunc {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		if cfg.OnJobsQuery != nil {
			cfg.OnJobsQuery(r)
		}
		if cfg.JobsErr != 0 {
			w.WriteHeader(cfg.JobsErr)
			// For compatibility with existing tests expecting specific error body
			if cfg.JobsErr == http.StatusInternalServerError {
				w.Write([]byte("database locked"))
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		// Ensure empty slice is encoded as [] not null if initialized but empty
		jobs := cfg.Jobs
		if jobs == nil {
			jobs = []storage.ReviewJob{}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"jobs":     jobs,
			"has_more": false,
		})
	})
	mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
		if cfg.Review != nil {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(cfg.Review)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return mux.ServeHTTP
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

// waitEnv holds common test fixtures for wait command tests.
type waitEnv struct {
	repo *TestGitRepo
	sha  string
}

// newWaitEnv creates a git repo with a commit, starts a mock daemon,
// and chdirs into the repo. Cleanup is automatic via t.Cleanup.
func newWaitEnv(t *testing.T, handler http.Handler) *waitEnv {
	t.Helper()
	repo := newTestGitRepo(t)
	sha := repo.CommitFile("file.txt", "content", "initial commit")
	daemonFromHandler(t, handler)
	chdir(t, repo.Dir)
	return &waitEnv{repo: repo, sha: sha}
}

// runWait executes the wait command with the given args and returns
// captured stdout and the error.
func runWait(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	var out bytes.Buffer
	cmd := waitCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), err
}

func TestWaitArgValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "job flag without argument",
			args:    []string{"--job"},
			wantErr: "--job requires a job ID argument",
		},
		{
			name:    "sha and positional arg conflict",
			args:    []string{"--sha", "HEAD", "42"},
			wantErr: "cannot use both",
		},
		{
			name:    "invalid positional arg",
			args:    []string{"not-a-ref-or-number"},
			wantErr: "not a valid git ref or job ID",
		},
		{
			name:    "positional zero is invalid",
			args:    []string{"0"},
			wantErr: "not a valid git ref or job ID",
		},
		{
			name:    "invalid sha ref",
			args:    []string{"--sha", "not-a-valid-ref"},
			wantErr: "invalid git ref",
		},
		{
			name:    "job flag rejects non-positive ID",
			args:    []string{"--job", "0"},
			wantErr: "invalid job ID",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newWaitEnv(t, newWaitMockHandler(mockConfig{}))
			_, err := runWait(t, tc.args...)
			assertErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestWaitArgValidationWithoutDaemon(t *testing.T) {
	// Validation errors should be returned before contacting the daemon.
	// This test does NOT start a mock daemon.
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")
	chdir(t, repo.Dir)

	_, err := runWait(t, "--job", "0")
	assertErrorContains(t, err, "invalid job ID")

	_, err = runWait(t, "--sha", "not-a-valid-ref")
	assertErrorContains(t, err, "invalid git ref")
}

func TestWait_Scenarios(t *testing.T) {
	setupFastPolling(t)

	cases := []struct {
		name          string
		args          []string
		mock          mockConfig
		expectErr     bool
		expectErrCode int
		expectOut     string
	}{
		{
			name:          "No job for SHA",
			args:          []string{"--sha", "HEAD"},
			mock:          mockConfig{}, // No jobs
			expectErr:     true,
			expectErrCode: 1,
			expectOut:     "No job found",
		},
		{
			name:          "Quiet mode suppresses output",
			args:          []string{"--sha", "HEAD", "--quiet"},
			mock:          mockConfig{},
			expectErr:     true,
			expectErrCode: 1,
			expectOut:     "",
		},
		{
			name:          "Job ID not found",
			args:          []string{"--job", "99999"},
			mock:          mockConfig{},
			expectErr:     true,
			expectErrCode: 1,
			expectOut:     "No job found",
		},
		{
			name: "Passing Review",
			args: []string{"--sha", "HEAD", "--quiet"},
			mock: mockConfig{
				Jobs:   []storage.ReviewJob{{ID: 1, Agent: "test", Status: "done"}},
				Review: &storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."},
			},
			expectErr: false,
		},
		{
			name: "Failing Review",
			args: []string{"--sha", "HEAD", "--quiet"},
			mock: mockConfig{
				Jobs:   []storage.ReviewJob{{ID: 1, Agent: "test", Status: "done"}},
				Review: &storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "Found 2 issues:\n1. Bug\n2. Missing check"},
			},
			expectErr:     true,
			expectErrCode: 1,
		},
		{
			name: "Positional Arg As Git Ref",
			args: []string{"HEAD", "--quiet"},
			mock: mockConfig{
				Jobs:   []storage.ReviewJob{{ID: 1, Agent: "test", Status: "done"}},
				Review: &storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."},
			},
			expectErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newWaitEnv(t, newWaitMockHandler(tc.mock))
			stdout, err := runWait(t, tc.args...)

			if tc.expectErr {
				requireExitCode(t, err, tc.expectErrCode)
			} else if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			if tc.expectOut == "" && stdout != "" {
				t.Errorf("expected no stdout, got: %q", stdout)
			} else if tc.expectOut != "" && !strings.Contains(stdout, tc.expectOut) {
				t.Errorf("expected stdout to contain %q, got: %q", tc.expectOut, stdout)
			}
		})
	}
}

func TestWaitJobIDPollingNon200Response(t *testing.T) {
	setupFastPolling(t)
	newWaitEnv(t, newWaitMockHandler(mockConfig{
		JobsErr: http.StatusInternalServerError,
	}))

	_, err := runWait(t, "--job", "42")
	assertErrorContains(t, err, "500")
}

func TestWaitNumericFallbackToJobID(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	// "42" is not a valid git ref, so the wait command should fall back to
	// treating it as a numeric job ID and query by id (not git_ref).
	var lastJobsQuery string
	mock := mockConfig{
		Jobs: []storage.ReviewJob{{ID: 42, GitRef: "abc", Agent: "test", Status: "done"}},
		Review: &storage.Review{
			ID: 1, JobID: 42, Agent: "test", Output: "No issues found.",
		},
		OnJobsQuery: func(r *http.Request) {
			lastJobsQuery = r.URL.RawQuery
		},
	}

	daemonFromHandler(t, newWaitMockHandler(mock))
	chdir(t, repo.Dir)

	_, err := runWait(t, "42", "--quiet")
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

	// Job completes but /api/review returns 404. This should surface as a
	// plain error (from showReview), not an *exitError. Both map to exit 1
	// in practice, but the distinction matters for error reporting: plain
	// errors print Cobra's "Error:" prefix while exitError silences it.
	mock := mockConfig{
		Jobs: []storage.ReviewJob{{ID: 1, Agent: "test", Status: "done"}},
		// Review is nil, so 404
	}
	newWaitEnv(t, newWaitMockHandler(mock))

	_, err := runWait(t, "--sha", "HEAD", "--quiet")
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
	newWaitEnv(t, newWaitMockHandler(mockConfig{
		JobsErr: http.StatusInternalServerError,
	}))

	_, err := runWait(t, "--sha", "HEAD")
	assertErrorContains(t, err, "500 Internal Server Error")
}

// setupRepoWithWorktree creates a repo, a worktree, and commits in both.
// It returns the repo path, worktree path, main SHA, and worktree SHA.
func setupRepoWithWorktree(t *testing.T) (repoDir, wtDir, mainSHA, wtSHA string) {
	t.Helper()
	// Create main repo with initial commit
	repo := newTestGitRepo(t)
	mainSHA = repo.CommitFile("file.txt", "content", "initial on main")

	// Create a worktree on a new branch with a different commit
	wtDir = t.TempDir()
	os.Remove(wtDir) // git worktree add needs to create it
	repo.Run("worktree", "add", "-b", "wt-branch", wtDir)

	// Make a new commit in the worktree so HEAD differs from main
	wtRepo := &TestGitRepo{Dir: wtDir, t: t}
	wtSHA = wtRepo.CommitFile("wt-file.txt", "worktree content", "commit in worktree")

	if mainSHA == wtSHA {
		t.Fatal("expected different SHAs for main and worktree commits")
	}
	return repo.Dir, wtDir, mainSHA, wtSHA
}

func TestWaitWorktreeResolvesRefFromWorktreeAndRepoFromMain(t *testing.T) {
	setupFastPolling(t)

	repoDir, worktreeDir, mainSHA, wtSHA := setupRepoWithWorktree(t)

	var lookupQuery string
	mock := mockConfig{
		Jobs: []storage.ReviewJob{{ID: 1, GitRef: wtSHA, Agent: "test", Status: "done"}},
		Review: &storage.Review{
			ID: 1, JobID: 1, Agent: "test", Output: "No issues found.",
		},
		OnJobsQuery: func(r *http.Request) {
			// Capture only the lookup query (has git_ref), not the poll query (has id)
			if r.URL.Query().Get("git_ref") != "" {
				lookupQuery = r.URL.RawQuery
			}
		},
	}

	daemonFromHandler(t, newWaitMockHandler(mock))

	chdir(t, worktreeDir)

	_, err := runWait(t, "--sha", "HEAD", "--quiet")
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
	if !strings.Contains(lookupQuery, url.QueryEscape(repoDir)) {
		t.Errorf("expected repo=%s (main repo) in query, got: %s", repoDir, lookupQuery)
	}
}
