//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
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
			ID:         1,
			GitRef:     "abc1234567890",
			RepoName:   "myrepo",
			Agent:      "test",
			Status:     storage.JobStatusDone,
			StartedAt:  &started,
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

	// Helper to create a handler that returns a specific list of jobs
	jobsHandler := func(jobs []storage.ReviewJob, hasMore bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				json.NewEncoder(w).Encode(map[string]any{
					"jobs":     jobs,
					"has_more": hasMore,
				})
			}
		}
	}

	// Helper to create a handler that returns an error
	errorHandler := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				w.WriteHeader(code)
				w.Write([]byte(body))
			}
		}
	}

	tests := []struct {
		name          string
		args          []string
		handler       http.HandlerFunc
		repoSetup     func(t *testing.T) (string, *TestGitRepo, []string) // returns wd, repo, extraArgs
		check         func(t *testing.T, output string, query string, repo *TestGitRepo, wd string)
		wantOutput    []string
		notWantOutput []string
		wantError     string
		wantQuery     []string
		notWantQuery  []string
	}{
		{
			name:       "tabular output shows jobs",
			args:       []string{},
			handler:    jobsHandler(testJobs, false),
			wantOutput: []string{"abc1234", "myrepo", "test", "done"},
		},
		{
			name:    "json output passes through raw response",
			args:    []string{"--json"},
			handler: jobsHandler(testJobs, true),
			check: func(t *testing.T, output string, query string, repo *TestGitRepo, wd string) {
				var parsed []storage.ReviewJob
				if err := json.Unmarshal([]byte(output), &parsed); err != nil {
					t.Fatalf("json output not valid JSON: %v\noutput: %s", err, output)
				}
				if len(parsed) != 2 {
					t.Errorf("expected 2 jobs, got %d", len(parsed))
				}
			},
		},
		{
			name:       "has_more shows hint in tabular mode",
			args:       []string{},
			handler:    jobsHandler(testJobs, true),
			wantOutput: []string{"more results available"},
		},
		{
			name:       "empty results shows message",
			args:       []string{},
			handler:    jobsHandler([]storage.ReviewJob{}, false),
			wantOutput: []string{"No jobs found"},
		},
		{
			name:      "non-2xx response returns error",
			args:      []string{},
			handler:   errorHandler(http.StatusInternalServerError, "database locked"),
			wantError: "500",
		},
		{
			name:      "non-2xx response returns error with --json",
			args:      []string{"--json"},
			handler:   errorHandler(http.StatusBadRequest, "invalid status filter"),
			wantError: "400",
		},
		{
			name:      "query params pass through correctly",
			args:      []string{"--status", "done", "--limit", "10"},
			handler:   jobsHandler([]storage.ReviewJob{}, false),
			wantQuery: []string{"status=done", "limit=10"},
		},
		{
			name:    "explicit --repo to non-git path sends no branch",
			args:    []string{"--repo", "/some/other/repo"},
			handler: jobsHandler([]storage.ReviewJob{}, false),
			wantQuery: []string{
				"repo=%2Fsome%2Fother%2Frepo",
			},
			notWantQuery: []string{"branch="},
		},
		{
			name: "explicit --repo to cwd repo still auto-resolves branch",
			repoSetup: func(t *testing.T) (string, *TestGitRepo, []string) {
				repo := newTestGitRepo(t)
				repo.CommitFile("file.txt", "content", "initial")
				// We want to pass repo.Dir as --repo arg
				return repo.Dir, repo, []string{"--repo", repo.Dir}
			},
			handler:   jobsHandler([]storage.ReviewJob{}, false),
			wantQuery: []string{"branch="},
		},
		{
			name: "worktree sends main repo path as repo param",
			repoSetup: func(t *testing.T) (string, *TestGitRepo, []string) {
				repo := newTestGitRepo(t)
				repo.CommitFile("file.txt", "content", "initial")
				worktreeDir := t.TempDir()
				os.Remove(worktreeDir)
				repo.Run("worktree", "add", "-b", "wt-branch", worktreeDir)
				return worktreeDir, repo, nil
			},
			handler: jobsHandler([]storage.ReviewJob{}, false),
			check: func(t *testing.T, output string, query string, repo *TestGitRepo, wd string) {
				if !strings.Contains(query, url.QueryEscape(repo.Dir)) {
					t.Errorf("expected main repo path %q in query, got: %s", repo.Dir, query)
				}
				if !strings.Contains(query, "branch=wt-branch") {
					t.Errorf("expected branch=wt-branch in query, got: %s", query)
				}
				if strings.Contains(query, url.QueryEscape(wd)) {
					t.Errorf("expected worktree path %q NOT in query, got: %s", wd, query)
				}
			},
		},
		{
			name: "explicit --repo with worktree path normalizes to main repo",
			repoSetup: func(t *testing.T) (string, *TestGitRepo, []string) {
				repo := newTestGitRepo(t)
				repo.CommitFile("file.txt", "content", "initial")
				worktreeDir := t.TempDir()
				os.Remove(worktreeDir)
				repo.Run("worktree", "add", "-b", "wt-branch", worktreeDir)
				return worktreeDir, repo, []string{"--repo", worktreeDir}
			},
			handler: jobsHandler([]storage.ReviewJob{}, false),
			check: func(t *testing.T, output string, query string, repo *TestGitRepo, wd string) {
				if !strings.Contains(query, url.QueryEscape(repo.Dir)) {
					t.Errorf("expected main repo path %q in query, got: %s", repo.Dir, query)
				}
				if strings.Contains(query, url.QueryEscape(wd)) {
					t.Errorf("expected worktree path %q NOT in query, got: %s", wd, query)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock daemon to capture query and handle request
			var capturedQuery string
			wrapperHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/jobs" {
					capturedQuery = r.URL.RawQuery
				}
				if tt.handler != nil {
					tt.handler(w, r)
				}
			})
			_, cleanup := setupMockDaemon(t, wrapperHandler)
			t.Cleanup(cleanup)

			// Setup repo and cwd
			var wd string
			var repo *TestGitRepo
			var extraArgs []string
			if tt.repoSetup != nil {
				wd, repo, extraArgs = tt.repoSetup(t)
			} else {
				repo = newTestGitRepo(t)
				repo.CommitFile("file.txt", "content", "initial")
				wd = repo.Dir
			}
			chdir(t, wd)

			// Execute command
			var cmdErr error
			output := captureStdout(t, func() {
				cmd := listCmd()
				args := append(tt.args, extraArgs...)
				cmd.SetArgs(args)
				cmdErr = cmd.Execute()
			})

			// Verify error
			if tt.wantError != "" {
				if cmdErr == nil {
					t.Fatal("expected error but got none")
				}
				if !strings.Contains(cmdErr.Error(), tt.wantError) {
					t.Errorf("expected error containing %q, got %q", tt.wantError, cmdErr.Error())
				}
			} else if cmdErr != nil {
				t.Fatalf("unexpected error: %v", cmdErr)
			}

			// Verify string output
			for _, want := range tt.wantOutput {
				if !strings.Contains(output, want) {
					t.Errorf("expected output to contain %q, got: %s", want, output)
				}
			}
			for _, notWant := range tt.notWantOutput {
				if strings.Contains(output, notWant) {
					t.Errorf("expected output NOT to contain %q, got: %s", notWant, output)
				}
			}

			// Verify query parameters
			for _, want := range tt.wantQuery {
				if !strings.Contains(capturedQuery, want) {
					t.Errorf("expected query to contain %q, got: %s", want, capturedQuery)
				}
			}
			for _, notWant := range tt.notWantQuery {
				if strings.Contains(capturedQuery, notWant) {
					t.Errorf("expected query NOT to contain %q, got: %s", notWant, capturedQuery)
				}
			}

			// Custom checks
			if tt.check != nil {
				tt.check(t, output, capturedQuery, repo, wd)
			}
		})
	}
}
