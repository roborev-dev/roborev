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

	"github.com/roborev-dev/roborev/internal/storage"
)

func respondJSON(
	w http.ResponseWriter, status int, payload any,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func executeReviewCmd(
	args ...string,
) (string, string, error) {
	var stdout, stderr bytes.Buffer

	cmd := reviewCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func setupTestEnvironment(
	t *testing.T,
) (*TestGitRepo, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	daemonFromHandler(t, mux)
	return newTestGitRepo(t), mux
}

type capturedEnqueue struct {
	GitRef    string `json:"git_ref"`
	Reasoning string `json:"reasoning"`
}

func mockEnqueue(
	t *testing.T, mux *http.ServeMux,
) <-chan capturedEnqueue {
	t.Helper()
	ch := make(chan capturedEnqueue, 1)
	mux.HandleFunc("/api/enqueue", func(
		w http.ResponseWriter, r *http.Request,
	) {
		var req capturedEnqueue
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("mockEnqueue: decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		select {
		case ch <- req:
		default:
			t.Errorf("mockEnqueue: unexpected extra enqueue request")
			http.Error(w, "duplicate request", http.StatusConflict)
			return
		}
		respondJSON(w, http.StatusCreated, storage.ReviewJob{
			ID: 1, GitRef: req.GitRef, Agent: "test",
		})
	})
	return ch
}

func mockWaitableReview(
	t *testing.T, mux *http.ServeMux, output string,
) {
	t.Helper()
	mux.HandleFunc("/api/enqueue", func(
		w http.ResponseWriter, r *http.Request,
	) {
		respondJSON(w, http.StatusCreated, storage.ReviewJob{
			ID: 1, GitRef: "abc123", Agent: "test",
			Status: "queued",
		})
	})
	mux.HandleFunc("/api/jobs", func(
		w http.ResponseWriter, r *http.Request,
	) {
		job := storage.ReviewJob{
			ID: 1, GitRef: "abc123", Agent: "test",
			Status: "done",
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"jobs": []storage.ReviewJob{job}, "has_more": false,
		})
	})
	mux.HandleFunc("/api/review", func(
		w http.ResponseWriter, r *http.Request,
	) {
		respondJSON(w, http.StatusOK, storage.Review{
			ID: 1, JobID: 1, Agent: "test", Output: output,
		})
	})
}

func TestEnqueueCmdPositionalArg(t *testing.T) {
	t.Run("positional arg overrides default HEAD", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		shortFirstSHA := firstSHA[:7]
		_, _, err := executeReviewCmd("--repo", repo.Dir, shortFirstSHA)
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		req := <-reqCh
		if req.GitRef != shortFirstSHA {
			t.Errorf("Expected SHA %s, got %s", shortFirstSHA, req.GitRef)
		}
		if req.GitRef == "HEAD" {
			t.Error("Received HEAD instead of positional arg - bug not fixed!")
		}
	})

	t.Run("sha flag works", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		shortFirstSHA := firstSHA[:7]
		_, _, err := executeReviewCmd("--repo", repo.Dir, "--sha", shortFirstSHA)
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		req := <-reqCh
		if req.GitRef != shortFirstSHA {
			t.Errorf("Expected SHA %s, got %s", shortFirstSHA, req.GitRef)
		}
	})

	t.Run("defaults to HEAD", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.CommitFile("file1.txt", "first", "first commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir)
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		req := <-reqCh
		if req.GitRef != "HEAD" {
			t.Errorf("Expected HEAD, got %s", req.GitRef)
		}
	})
}

func TestEnqueueSkippedBranch(t *testing.T) {
	t.Run("skipped response prints message and exits successfully", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, map[string]any{
				"skipped": true,
				"reason":  "branch \"wip\" is excluded from reviews",
			})
		})

		repo.CommitFile("file.txt", "content", "initial commit")

		stdout, _, err := executeReviewCmd("--repo", repo.Dir)
		if err != nil {
			t.Errorf("enqueue should succeed (exit 0) for skipped branch, got error: %v", err)
		}

		if !strings.Contains(stdout, "Skipped") {
			t.Errorf("expected output to contain 'Skipped', got: %q", stdout)
		}
	})

	t.Run("skipped response in quiet mode suppresses output", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, map[string]any{
				"skipped": true,
				"reason":  "branch \"wip\" is excluded from reviews",
			})
		})

		repo.CommitFile("file.txt", "content", "initial commit")

		stdout, _, err := executeReviewCmd("--repo", repo.Dir, "--quiet")
		if err != nil {
			t.Errorf("enqueue --quiet should succeed for skipped branch, got error: %v", err)
		}

		if stdout != "" {
			t.Errorf("expected no output in quiet mode, got: %q", stdout)
		}
	})
}

func TestWaitQuietVerdictExitCode(t *testing.T) {
	setupFastPolling(t)

	t.Run("passing review exits 0 with no output", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")
		mockWaitableReview(t, mux, "No issues found.")

		stdout, stderr, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

		if err != nil {
			t.Errorf("expected exit 0 for passing review, got error: %v", err)
		}
		if stdout != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout)
		}
		if stderr != "" {
			t.Errorf("expected no stderr in quiet mode, got: %q", stderr)
		}
	})

	t.Run("failing review exits 1 with no output", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")
		mockWaitableReview(t, mux,
			"Found 2 issues:\n1. Bug in foo.go\n2. Missing error handling",
		)

		stdout, stderr, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

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
		if stdout != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout)
		}
		if stderr != "" {
			t.Errorf("expected no stderr in quiet mode, got: %q", stderr)
		}
	})
}

func TestWaitForJobUnknownStatus(t *testing.T) {
	setupFastPolling(t)

	t.Run("unknown status exceeds max retries", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		callCount := 0
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

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

		assertErrorContains(t, err, "unknown status")
		assertErrorContains(t, err, "daemon may be newer than CLI")

		if callCount != 10 {
			t.Errorf("expected 10 retries, got %d", callCount)
		}
	})

	t.Run("counter resets on known status", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		poller := &mockJobPoller{}

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

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

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

func (m *mockJobPoller) HandleJobs(
	w http.ResponseWriter, r *http.Request,
) {
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
	job := storage.ReviewJob{
		ID: 1, GitRef: "abc123", Agent: "test",
		Status: storage.JobStatus(status),
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"jobs":     []storage.ReviewJob{job},
		"has_more": false,
	})
}

func TestReviewFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			"since and branch exclusive",
			[]string{"--since", "abc123", "--branch"},
			[]string{"cannot use --branch with --since"},
		},
		{
			"since and dirty exclusive",
			[]string{"--since", "abc123", "--dirty"},
			[]string{"cannot use --since with --dirty"},
		},
		{
			"since with positional args",
			[]string{"--since", "abc123", "def456"},
			[]string{"cannot specify commits with --since"},
		},
		{
			"branch and dirty exclusive",
			[]string{"--branch", "--dirty"},
			[]string{"cannot use --branch with --dirty"},
		},
		{
			"branch with positional args",
			[]string{"--branch", "abc123"},
			[]string{
				"cannot specify commits with --branch",
				"--branch=<name>",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _ := setupTestEnvironment(t)
			_, _, err := executeReviewCmd(
				append([]string{"--repo", repo.Dir}, tt.args...)...,
			)
			for _, w := range tt.want {
				assertErrorContains(t, err, w)
			}
		})
	}
}

func TestReviewSinceFlag(t *testing.T) {
	t.Run("since with valid ref succeeds", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", firstSHA[:7])
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		req := <-reqCh
		if !strings.Contains(req.GitRef, firstSHA) {
			t.Errorf("expected git_ref to contain first SHA %s, got %s", firstSHA, req.GitRef)
		}
		if !strings.HasSuffix(req.GitRef, "..HEAD") {
			t.Errorf("expected git_ref to end with ..HEAD, got %s", req.GitRef)
		}
	})

	t.Run("since with invalid ref fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", "nonexistent123")
		assertErrorContains(t, err, "invalid --since commit")
	})

	t.Run("since with no commits ahead fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", "HEAD")
		assertErrorContains(t, err, "no commits since")
	})
}

func TestReviewBranchFlag(t *testing.T) {
	t.Run("branch on default branch fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch")
		assertErrorContains(t, err, "already on main")
	})

	t.Run("branch with no commits fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		repo.Run("checkout", "-b", "feature")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch")
		assertErrorContains(t, err, "no commits on branch")
	})

	t.Run("branch review succeeds with commits", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		mainSHA := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		req := <-reqCh
		if !strings.Contains(req.GitRef, mainSHA) {
			t.Errorf("expected git_ref to contain main SHA %s, got %s", mainSHA, req.GitRef)
		}
		if !strings.HasSuffix(req.GitRef, "..HEAD") {
			t.Errorf("expected git_ref to end with ..HEAD, got %s", req.GitRef)
		}
	})
}

func TestReviewFastFlag(t *testing.T) {
	t.Run("fast flag sets reasoning to fast", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--fast")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		req := <-reqCh
		if req.Reasoning != "fast" {
			t.Errorf("expected reasoning 'fast', got %q", req.Reasoning)
		}
	})

	t.Run("explicit reasoning takes precedence over fast", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--fast", "--reasoning", "thorough")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		req := <-reqCh
		if req.Reasoning != "thorough" {
			t.Errorf("expected reasoning 'thorough' (explicit flag should win), got %q", req.Reasoning)
		}
	})
}

func TestReviewInvalidArgsNoSideEffects(t *testing.T) {
	repo, mux := setupTestEnvironment(t)

	// Catch-all handler to fail the test if any request is made
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Error("daemon should not be contacted on invalid args")
		w.WriteHeader(http.StatusOK)
	})

	hooksDir := filepath.Join(repo.Dir, ".git", "hooks")

	_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch", "--dirty")
	assertErrorContains(t, err, "cannot use --branch with --dirty")

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
