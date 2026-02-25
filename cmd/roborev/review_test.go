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

func executeReviewCmd(args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer

	cmd := reviewCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func setupTestEnvironment(t *testing.T) (*TestGitRepo, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	daemonFromHandler(t, mux)
	return newTestGitRepo(t), mux
}

func TestEnqueueCmdPositionalArg(t *testing.T) {
	t.Run("positional arg overrides default HEAD", func(t *testing.T) {
		var receivedSHA string
		repo, mux := setupTestEnvironment(t)
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				GitRef string `json:"git_ref"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			receivedSHA = req.GitRef

			job := storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"}
			respondJSON(w, http.StatusCreated, job)
		})

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		shortFirstSHA := firstSHA[:7]
		_, _, err := executeReviewCmd("--repo", repo.Dir, shortFirstSHA)
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

	t.Run("sha flag works", func(t *testing.T) {
		var receivedSHA string
		repo, mux := setupTestEnvironment(t)
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				GitRef string `json:"git_ref"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			receivedSHA = req.GitRef

			job := storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"}
			respondJSON(w, http.StatusCreated, job)
		})

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		shortFirstSHA := firstSHA[:7]
		_, _, err := executeReviewCmd("--repo", repo.Dir, "--sha", shortFirstSHA)
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		if receivedSHA != shortFirstSHA {
			t.Errorf("Expected SHA %s, got %s", shortFirstSHA, receivedSHA)
		}
	})

	t.Run("defaults to HEAD", func(t *testing.T) {
		var receivedSHA string
		repo, mux := setupTestEnvironment(t)
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				GitRef string `json:"git_ref"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			receivedSHA = req.GitRef

			job := storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"}
			respondJSON(w, http.StatusCreated, job)
		})

		repo.CommitFile("file1.txt", "first", "first commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir)
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		if receivedSHA != "HEAD" {
			t.Errorf("Expected HEAD, got %s", receivedSHA)
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
		repo, mux := setupTestEnvironment(t)

		gitRefChan := make(chan string, 1)
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

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", firstSHA[:7])
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

	t.Run("since and branch are mutually exclusive", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", "abc123", "--branch")
		assertErrorContains(t, err, "cannot use --branch with --since")
	})

	t.Run("since and dirty are mutually exclusive", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", "abc123", "--dirty")
		assertErrorContains(t, err, "cannot use --since with --dirty")
	})

	t.Run("since with positional args fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", "abc123", "def456")
		assertErrorContains(t, err, "cannot specify commits with --since")
	})
}

func TestReviewBranchFlag(t *testing.T) {
	t.Run("branch and dirty are mutually exclusive", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch", "--dirty")
		assertErrorContains(t, err, "cannot use --branch with --dirty")
	})

	t.Run("branch with positional args fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch", "abc123")
		assertErrorContains(t, err, "cannot specify commits with --branch")
		assertErrorContains(t, err, "--branch=<name>")
	})

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

		var receivedGitRef string
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				GitRef string `json:"git_ref"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			receivedGitRef = req.GitRef
			respondJSON(w, http.StatusCreated, storage.ReviewJob{ID: 1, GitRef: req.GitRef, Agent: "test"})
		})

		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		mainSHA := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch")
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
		repo, mux := setupTestEnvironment(t)

		reasoningChan := make(chan string, 1)
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

		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--fast")
		if err != nil {
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
		repo, mux := setupTestEnvironment(t)

		reasoningChan := make(chan string, 1)
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

		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--fast", "--reasoning", "thorough")
		if err != nil {
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
