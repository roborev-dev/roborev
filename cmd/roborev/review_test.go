package main

// Tests for the review command (enqueue, wait, branch mode)

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func TestEnqueueCmdPositionalArg(t *testing.T) {
	// Track what SHA was sent to the server
	var receivedSHA string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			GitRef string `json:"git_ref"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		receivedSHA = req.GitRef

		job := storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"}
		respondJSON(w, http.StatusCreated, job)
	})

	_, cleanup := setupMockDaemon(t, mux)
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
	mux := http.NewServeMux()
	mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]any{
			"skipped": true,
			"reason":  "branch \"wip\" is excluded from reviews",
		})
	})

	_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
			respondJSON(w, http.StatusCreated, job)
		})
		mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "done"}
			respondJSON(w, http.StatusOK, map[string]any{"jobs": []storage.ReviewJob{job}, "has_more": false})
		})
		mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."})
		})

		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
			respondJSON(w, http.StatusCreated, job)
		})
		mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "done"}
			respondJSON(w, http.StatusOK, map[string]any{"jobs": []storage.ReviewJob{job}, "has_more": false})
		})
		mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "Found 2 issues:\n1. Bug in foo.go\n2. Missing error handling"})
		})

		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
			respondJSON(w, http.StatusCreated, job)
		})
		mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			callCount++
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "future_status"}
			respondJSON(w, http.StatusOK, map[string]any{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
		})

		_, cleanup := setupMockDaemon(t, mux)
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
		poller := &mockJobPoller{}

		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
			respondJSON(w, http.StatusCreated, job)
		})
		mux.HandleFunc("/api/jobs", poller.HandleJobs)
		mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, storage.Review{
				ID:     1,
				JobID:  1,
				Agent:  "test",
				Output: "No issues found. LGTM!",
			})
		})

		_, cleanup := setupMockDaemon(t, mux)
		defer cleanup()

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", repo.Dir, "--wait", "--quiet"})
		err := cmd.Execute()

		if err != nil {
			t.Errorf("expected success (counter should reset on known status), got error: %v", err)
		}
		if poller.callCount != 12 {
			t.Errorf("expected 12 calls, got %d", poller.callCount)
		}
	})
}

type mockJobPoller struct {
	callCount int
}

func (m *mockJobPoller) HandleJobs(w http.ResponseWriter, r *http.Request) {
	m.callCount++
	var status string
	switch {
	case m.callCount <= 5:
		status = "future_status"
	case m.callCount == 6:
		status = "running"
	case m.callCount <= 11:
		status = "future_status"
	default:
		status = "done"
	}
	job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: storage.JobStatus(status)}
	respondJSON(w, http.StatusOK, map[string]any{
		"jobs":     []storage.ReviewJob{job},
		"has_more": false,
	})
}

func TestReviewSinceFlag(t *testing.T) {
	t.Run("since with valid ref succeeds", func(t *testing.T) {
		gitRefChan := make(chan string, 1)
		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				GitRef string `json:"git_ref"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			gitRefChan <- req.GitRef
			respondJSON(w, http.StatusCreated, storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"})
		})
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		// no handler needed for enqueue as it shouldn't be called, but we need to setup mock daemon
		// to allow version check if any (though typically version check hits /api/status which is handled by wrapper)
		// but setupMockDaemon wrapper handles /api/status.
		// The original code was returning version on any request.
		// Let's just pass empty mux, as /api/status is handled by setupMockDaemon wrapper.

		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		if !strings.Contains(err.Error(), "--branch=<name>") {
			t.Errorf("error should suggest --branch=<name> syntax, got: %v", err)
		}
	})

	t.Run("branch on default branch fails", func(t *testing.T) {
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				GitRef string `json:"git_ref"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			receivedGitRef = req.GitRef
			respondJSON(w, http.StatusCreated, storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"})
		})
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Reasoning string `json:"reasoning"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			reasoningChan <- req.Reasoning
			respondJSON(w, http.StatusCreated, storage.ReviewJob{ID: 1, Agent: "test"})
		})
		_, cleanup := setupMockDaemon(t, mux)
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
		mux := http.NewServeMux()
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Reasoning string `json:"reasoning"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			reasoningChan <- req.Reasoning
			respondJSON(w, http.StatusCreated, storage.ReviewJob{ID: 1, Agent: "test"})
		})
		_, cleanup := setupMockDaemon(t, mux)
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

func TestReviewInvalidArgsNoSideEffects(t *testing.T) {
	mux := http.NewServeMux()
	// Catch-all handler to fail the test if any request is made
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Error("daemon should not be contacted on invalid args")
		w.WriteHeader(http.StatusOK)
	})

	_, cleanup := setupMockDaemon(t, mux)
	defer cleanup()

	repo := newTestGitRepo(t)
	hooksDir := filepath.Join(repo.Dir, ".git", "hooks")

	cmd := reviewCmd()
	cmd.SetArgs([]string{
		"--repo", repo.Dir, "--branch", "--dirty",
	})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for --branch with --dirty")
	}

	// Hooks directory should have no roborev-generated files.
	for _, name := range []string{"post-commit", "post-rewrite"} {
		if _, statErr := os.Stat(
			filepath.Join(hooksDir, name),
		); statErr == nil {
			t.Errorf(
				"%s should not exist after invalid args", name,
			)
		}
	}
}
