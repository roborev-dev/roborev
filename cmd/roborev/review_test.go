package main

// Tests for the review command (enqueue, wait, branch mode)

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

func TestEnqueueCmdPositionalArg(t *testing.T) {
	// Track what SHA was sent to the server
	var receivedSHA string

	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/enqueue" {
			var req struct {
				GitRef string `json:"git_ref"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			receivedSHA = req.GitRef

			job := storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(job)
			return
		}
	}))
	defer cleanup()

	// Create a temp git repo with two commits
	repo := newTestGitRepo(t)
	firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
	repo.CommitFile("file2.txt", "second", "second commit")

	// Test: positional arg should be used instead of HEAD
	t.Run("positional arg overrides default HEAD", func(t *testing.T) {
		receivedSHA = ""
		shortFirstSHA := firstSHA[:7]
		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, shortFirstSHA})
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		if receivedSHA != shortFirstSHA {
			t.Errorf("Expected SHA %s, got %s", shortFirstSHA, receivedSHA)
		}
		if receivedSHA == "HEAD" {
			t.Error("Received HEAD instead of positional arg - bug not fixed!")
		}
	})

	// Test: --sha flag still works
	t.Run("sha flag works", func(t *testing.T) {
		receivedSHA = ""
		shortFirstSHA := firstSHA[:7]
		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--sha", shortFirstSHA})
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		if receivedSHA != shortFirstSHA {
			t.Errorf("Expected SHA %s, got %s", shortFirstSHA, receivedSHA)
		}
	})

	// Test: default to HEAD when no arg provided
	t.Run("defaults to HEAD", func(t *testing.T) {
		receivedSHA = ""
		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir})
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		if receivedSHA != "HEAD" {
			t.Errorf("Expected HEAD, got %s", receivedSHA)
		}
	})
}

func TestEnqueueSkippedBranch(t *testing.T) {
	_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/enqueue" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"skipped": true,
				"reason":  "branch \"wip\" is excluded from reviews",
			})
			return
		}
	}))
	defer cleanup()

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	t.Run("skipped response prints message and exits successfully", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := reviewCmd()
		cmd.SetOut(&stdout)
		cmd.SetArgs([]string{"--repo", repo.Dir})
		err := cmd.Execute()
		if err != nil {
			t.Errorf("enqueue should succeed (exit 0) for skipped branch, got error: %v", err)
		}

		output := stdout.String()
		if !strings.Contains(output, "Skipped") {
			t.Errorf("expected output to contain 'Skipped', got: %q", output)
		}
	})

	t.Run("skipped response in quiet mode suppresses output", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := reviewCmd()
		cmd.SetOut(&stdout)
		cmd.SetArgs([]string{"--repo", repo.Dir, "--quiet"})
		err := cmd.Execute()
		if err != nil {
			t.Errorf("enqueue --quiet should succeed for skipped branch, got error: %v", err)
		}

		output := stdout.String()
		if output != "" {
			t.Errorf("expected no output in quiet mode, got: %q", output)
		}
	})
}

func TestWaitQuietVerdictExitCode(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	t.Run("passing review exits 0 with no output", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/jobs" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "done"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{job}, "has_more": false})
				return
			}
			if r.URL.Path == "/api/review" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."})
				return
			}
		}))
		defer cleanup()

		var stdout, stderr bytes.Buffer
		cmd := reviewCmd()
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--repo", repo.Dir, "--wait", "--quiet"})
		err := cmd.Execute()

		if err != nil {
			t.Errorf("expected exit 0 for passing review, got error: %v", err)
		}
		if stdout.String() != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout.String())
		}
		if stderr.String() != "" {
			t.Errorf("expected no stderr in quiet mode, got: %q", stderr.String())
		}
	})

	t.Run("failing review exits 1 with no output", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/jobs" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "done"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{job}, "has_more": false})
				return
			}
			if r.URL.Path == "/api/review" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "Found 2 issues:\n1. Bug in foo.go\n2. Missing error handling"})
				return
			}
		}))
		defer cleanup()

		var stdout, stderr bytes.Buffer
		cmd := reviewCmd()
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--repo", repo.Dir, "--wait", "--quiet"})
		err := cmd.Execute()

		if err == nil {
			t.Error("expected exit 1 for failing review, got success")
		} else {
			exitErr, ok := err.(*exitError)
			if !ok {
				t.Errorf("expected exitError, got: %T %v", err, err)
			} else if exitErr.code != 1 {
				t.Errorf("expected exit code 1, got: %d", exitErr.code)
			}
		}
		if stdout.String() != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout.String())
		}
		if stderr.String() != "" {
			t.Errorf("expected no stderr in quiet mode, got: %q", stderr.String())
		}
	})
}

func TestWaitForJobUnknownStatus(t *testing.T) {
	setupFastPolling(t)

	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	t.Run("unknown status exceeds max retries", func(t *testing.T) {
		callCount := 0
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/jobs" {
				callCount++
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "future_status"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{job},
					"has_more": false,
				})
				return
			}
		}))
		defer cleanup()

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--wait", "--quiet"})
		err := cmd.Execute()

		if err == nil {
			t.Fatal("expected error for unknown status after max retries")
		}
		if !strings.Contains(err.Error(), "unknown status") {
			t.Errorf("error should mention unknown status, got: %v", err)
		}
		if !strings.Contains(err.Error(), "daemon may be newer than CLI") {
			t.Errorf("error should mention daemon version, got: %v", err)
		}
		if callCount != 10 {
			t.Errorf("expected 10 retries, got %d", callCount)
		}
	})

	t.Run("counter resets on known status", func(t *testing.T) {
		callCount := 0
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/jobs" {
				callCount++
				var status string
				switch {
				case callCount <= 5:
					status = "future_status"
				case callCount == 6:
					status = "running"
				case callCount <= 11:
					status = "future_status"
				default:
					status = "done"
				}
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: storage.JobStatus(status)}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{job},
					"has_more": false,
				})
				return
			}
			if r.URL.Path == "/api/review" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(storage.Review{
					ID:     1,
					JobID:  1,
					Agent:  "test",
					Output: "No issues found. LGTM!",
				})
				return
			}
		}))
		defer cleanup()

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--wait", "--quiet"})
		err := cmd.Execute()

		if err != nil {
			t.Errorf("expected success (counter should reset on known status), got error: %v", err)
		}
		if callCount != 12 {
			t.Errorf("expected 12 calls, got %d", callCount)
		}
	})
}

func TestReviewSinceFlag(t *testing.T) {
	t.Run("since with valid ref succeeds", func(t *testing.T) {
		gitRefChan := make(chan string, 1)
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				var req struct {
					GitRef string `json:"git_ref"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				gitRefChan <- req.GitRef
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"})
				return
			}
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		reviewCmd := reviewCmd()
		reviewCmd.SetArgs([]string{"--repo", repo.Dir, "--since", firstSHA[:7]})
		err := reviewCmd.Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		receivedGitRef := <-gitRefChan
		if !strings.Contains(receivedGitRef, firstSHA) {
			t.Errorf("expected git_ref to contain first SHA %s, got %s", firstSHA, receivedGitRef)
		}
		if !strings.HasSuffix(receivedGitRef, "..HEAD") {
			t.Errorf("expected git_ref to end with ..HEAD, got %s", receivedGitRef)
		}
	})

	t.Run("since with invalid ref fails", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--since", "nonexistent123"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for invalid --since ref")
		}
		if !strings.Contains(err.Error(), "invalid --since commit") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("since with no commits ahead fails", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--since", "HEAD"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when no commits since ref")
		}
		if !strings.Contains(err.Error(), "no commits since") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("since and branch are mutually exclusive", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--since", "abc123", "--branch"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --since with --branch")
		}
		if !strings.Contains(err.Error(), "cannot use --branch with --since") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("since and dirty are mutually exclusive", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--since", "abc123", "--dirty"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --since with --dirty")
		}
		if !strings.Contains(err.Error(), "cannot use --since with --dirty") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("since with positional args fails", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--since", "abc123", "def456"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --since with positional arg")
		}
		if !strings.Contains(err.Error(), "cannot specify commits with --since") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestReviewBranchFlag(t *testing.T) {
	t.Run("branch and dirty are mutually exclusive", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--branch", "--dirty"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --branch with --dirty")
		}
		if !strings.Contains(err.Error(), "cannot use --branch with --dirty") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("branch with positional args fails", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--branch", "abc123"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --branch with positional arg")
		}
		if !strings.Contains(err.Error(), "cannot specify commits with --branch") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("branch on default branch fails", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--branch"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when on default branch")
		}
		if !strings.Contains(err.Error(), "already on main") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("branch with no commits fails", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		repo.Run("checkout", "-b", "feature")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--branch"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when no commits on branch")
		}
		if !strings.Contains(err.Error(), "no commits on branch") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("branch review succeeds with commits", func(t *testing.T) {
		var receivedGitRef string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				var req struct {
					GitRef string `json:"git_ref"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				receivedGitRef = req.GitRef
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"})
				return
			}
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		mainSHA := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")

		reviewCmd := reviewCmd()
		reviewCmd.SetArgs([]string{"--repo", repo.Dir, "--branch"})
		err := reviewCmd.Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(receivedGitRef, mainSHA) {
			t.Errorf("expected git_ref to contain main SHA %s, got %s", mainSHA, receivedGitRef)
		}
		if !strings.HasSuffix(receivedGitRef, "..HEAD") {
			t.Errorf("expected git_ref to end with ..HEAD, got %s", receivedGitRef)
		}
	})
}

func TestReviewFastFlag(t *testing.T) {
	t.Run("fast flag sets reasoning to fast", func(t *testing.T) {
		reasoningChan := make(chan string, 1)
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				var req struct {
					Reasoning string `json:"reasoning"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				reasoningChan <- req.Reasoning
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(storage.ReviewJob{ID: 1, Agent: "test"})
				return
			}
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--fast"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case reasoning := <-reasoningChan:
			if reasoning != "fast" {
				t.Errorf("expected reasoning 'fast', got %q", reasoning)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for enqueue request")
		}
	})

	t.Run("explicit reasoning takes precedence over fast", func(t *testing.T) {
		reasoningChan := make(chan string, 1)
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				var req struct {
					Reasoning string `json:"reasoning"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				reasoningChan <- req.Reasoning
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(storage.ReviewJob{ID: 1, Agent: "test"})
				return
			}
		}))
		defer cleanup()

		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--fast", "--reasoning", "thorough"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case reasoning := <-reasoningChan:
			if reasoning != "thorough" {
				t.Errorf("expected reasoning 'thorough' (explicit flag should win), got %q", reasoning)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for enqueue request")
		}
	})
}
