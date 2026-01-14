package main

// Tests for the review command (enqueue, wait, branch mode)

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesm/roborev/internal/daemon"
	"github.com/wesm/roborev/internal/storage"
	"github.com/wesm/roborev/internal/version"
)

func TestEnqueueCmdPositionalArg(t *testing.T) {
	// Override HOME to prevent reading real daemon.json
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create a temp git repo with a known commit
	tmpDir := t.TempDir()

	// Initialize git repo
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"HOME="+tmpHome,
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")

	// Create two commits so we can distinguish them
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file1.txt")
	runGit("commit", "-m", "first commit")

	// Get first commit SHA
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = tmpDir
	firstSHABytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get first commit SHA: %v", err)
	}
	firstSHA := string(firstSHABytes[:len(firstSHABytes)-1]) // trim newline

	// Create second commit (this becomes HEAD)
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file2.txt")
	runGit("commit", "-m", "second commit")

	// Second commit is now HEAD (we don't need to track its SHA since CLI sends "HEAD" unresolved)

	// Track what SHA was sent to the server
	var receivedSHA string

	// Create mock server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		// Status endpoint for ensureDaemon check - must include version
		if r.URL.Path == "/api/status" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"version": version.Version,
			})
			return
		}
	}))
	defer ts.Close()

	// Write fake daemon.json pointing to our mock server
	roborevDir := filepath.Join(tmpHome, ".roborev")
	os.MkdirAll(roborevDir, 0755)
	// Extract host:port from ts.URL (strip http://)
	mockAddr := ts.URL[7:] // remove "http://"
	daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
	data, _ := json.Marshal(daemonInfo)
	os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644)

	// Test: positional arg should be used instead of HEAD
	t.Run("positional arg overrides default HEAD", func(t *testing.T) {
		receivedSHA = ""
		serverAddr = ts.URL

		shortFirstSHA := firstSHA[:7]
		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, shortFirstSHA}) // Use short SHA as positional arg
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		// Should have received the first commit SHA (short form as entered), not HEAD
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
		serverAddr = ts.URL

		shortFirstSHA := firstSHA[:7]
		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--sha", shortFirstSHA})
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
		serverAddr = ts.URL

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir})
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		// When no arg provided, CLI sends "HEAD" which gets resolved server-side
		if receivedSHA != "HEAD" {
			t.Errorf("Expected HEAD, got %s", receivedSHA)
		}
	})
}

func TestEnqueueSkippedBranch(t *testing.T) {
	// Save and restore serverAddr to avoid leaking state to other tests
	origServerAddr := serverAddr
	t.Cleanup(func() { serverAddr = origServerAddr })

	// Override HOME to prevent reading real daemon.json
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create a temp git repo
	tmpDir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"HOME="+tmpHome,
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial commit")

	// Create mock server that returns skipped response
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/enqueue" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"skipped": true,
				"reason":  "branch \"wip\" is excluded from reviews",
			})
			return
		}
		if r.URL.Path == "/api/status" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"version": version.Version,
			})
			return
		}
	}))
	defer ts.Close()

	// Write fake daemon.json pointing to our mock server
	roborevDir := filepath.Join(tmpHome, ".roborev")
	if err := os.MkdirAll(roborevDir, 0755); err != nil {
		t.Fatalf("Failed to create roborev dir: %v", err)
	}
	mockAddr := ts.URL[7:] // remove "http://"
	daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
	data, err := json.Marshal(daemonInfo)
	if err != nil {
		t.Fatalf("Failed to marshal daemon info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644); err != nil {
		t.Fatalf("Failed to write daemon.json: %v", err)
	}

	serverAddr = ts.URL

	t.Run("skipped response prints message and exits successfully", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := reviewCmd()
		cmd.SetOut(&stdout)
		cmd.SetArgs([]string{"--repo", tmpDir})
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
		cmd.SetArgs([]string{"--repo", tmpDir, "--quiet"})
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
	// Save and restore serverAddr to avoid leaking state to other tests
	origServerAddr := serverAddr
	t.Cleanup(func() { serverAddr = origServerAddr })

	// Speed up polling for tests
	origPollStart := pollStartInterval
	origPollMax := pollMaxInterval
	pollStartInterval = 1 * time.Millisecond
	pollMaxInterval = 1 * time.Millisecond
	t.Cleanup(func() {
		pollStartInterval = origPollStart
		pollMaxInterval = origPollMax
	})

	// Override HOME to prevent reading real daemon.json
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create a temp git repo
	tmpDir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"HOME="+tmpHome,
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial commit")

	t.Run("passing review exits 0 with no output", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/status" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/jobs" {
				// Return done immediately
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "done"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{job}, "has_more": false})
				return
			}
			if r.URL.Path == "/api/review" {
				// Passing review - "No issues" triggers pass verdict
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "No issues found."})
				return
			}
		}))
		defer ts.Close()

		// Write fake daemon.json
		roborevDir := filepath.Join(tmpHome, ".roborev")
		if err := os.MkdirAll(roborevDir, 0755); err != nil {
			t.Fatalf("failed to create roborev dir: %v", err)
		}
		mockAddr := ts.URL[7:]
		daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
		data, err := json.Marshal(daemonInfo)
		if err != nil {
			t.Fatalf("failed to marshal daemon info: %v", err)
		}
		if err := os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644); err != nil {
			t.Fatalf("failed to write daemon.json: %v", err)
		}

		serverAddr = ts.URL

		var stdout, stderr bytes.Buffer
		cmd := reviewCmd()
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--repo", tmpDir, "--wait", "--quiet"})
		err = cmd.Execute()

		// Should succeed with exit 0
		if err != nil {
			t.Errorf("expected exit 0 for passing review, got error: %v", err)
		}
		// Should have no output in quiet mode (stdout and stderr)
		if stdout.String() != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout.String())
		}
		if stderr.String() != "" {
			t.Errorf("expected no stderr in quiet mode, got: %q", stderr.String())
		}
	})

	t.Run("failing review exits 1 with no output", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/status" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/jobs" {
				// Return done immediately
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "done"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{job}, "has_more": false})
				return
			}
			if r.URL.Path == "/api/review" {
				// Failing review - findings present
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 1, Agent: "test", Output: "Found 2 issues:\n1. Bug in foo.go\n2. Missing error handling"})
				return
			}
		}))
		defer ts.Close()

		// Write fake daemon.json
		roborevDir := filepath.Join(tmpHome, ".roborev")
		if err := os.MkdirAll(roborevDir, 0755); err != nil {
			t.Fatalf("failed to create roborev dir: %v", err)
		}
		mockAddr := ts.URL[7:]
		daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
		data, err := json.Marshal(daemonInfo)
		if err != nil {
			t.Fatalf("failed to marshal daemon info: %v", err)
		}
		if err := os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644); err != nil {
			t.Fatalf("failed to write daemon.json: %v", err)
		}

		serverAddr = ts.URL

		var stdout, stderr bytes.Buffer
		cmd := reviewCmd()
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--repo", tmpDir, "--wait", "--quiet"})
		err = cmd.Execute()

		// Should fail with exit 1
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
		// Should have no output in quiet mode (stdout and stderr)
		if stdout.String() != "" {
			t.Errorf("expected no stdout in quiet mode, got: %q", stdout.String())
		}
		if stderr.String() != "" {
			t.Errorf("expected no stderr in quiet mode, got: %q", stderr.String())
		}
	})
}

func TestWaitForJobUnknownStatus(t *testing.T) {
	// Save and restore serverAddr to avoid leaking state to other tests
	origServerAddr := serverAddr
	t.Cleanup(func() { serverAddr = origServerAddr })

	// Speed up polling for tests
	origPollStart := pollStartInterval
	origPollMax := pollMaxInterval
	pollStartInterval = 1 * time.Millisecond
	pollMaxInterval = 1 * time.Millisecond
	t.Cleanup(func() {
		pollStartInterval = origPollStart
		pollMaxInterval = origPollMax
	})

	// Override HOME to prevent reading real daemon.json
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create a temp git repo
	tmpDir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"HOME="+tmpHome,
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial commit")

	t.Run("unknown status exceeds max retries", func(t *testing.T) {
		callCount := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/status" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"version": version.Version,
				})
				return
			}
			if r.URL.Path == "/api/jobs" {
				callCount++
				// Always return unknown status
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "future_status"}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs":     []storage.ReviewJob{job},
					"has_more": false,
				})
				return
			}
		}))
		defer ts.Close()

		// Write fake daemon.json
		roborevDir := filepath.Join(tmpHome, ".roborev")
		if err := os.MkdirAll(roborevDir, 0755); err != nil {
			t.Fatalf("failed to create roborev dir: %v", err)
		}
		mockAddr := ts.URL[7:]
		daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
		data, err := json.Marshal(daemonInfo)
		if err != nil {
			t.Fatalf("failed to marshal daemon info: %v", err)
		}
		if err := os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644); err != nil {
			t.Fatalf("failed to write daemon.json: %v", err)
		}

		serverAddr = ts.URL

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--wait", "--quiet"})
		err = cmd.Execute()

		if err == nil {
			t.Fatal("expected error for unknown status after max retries")
		}
		if !strings.Contains(err.Error(), "unknown status") {
			t.Errorf("error should mention unknown status, got: %v", err)
		}
		if !strings.Contains(err.Error(), "daemon may be newer than CLI") {
			t.Errorf("error should mention daemon version, got: %v", err)
		}
		// Should have tried exactly 10 times
		if callCount != 10 {
			t.Errorf("expected 10 retries, got %d", callCount)
		}
	})

	t.Run("counter resets on known status", func(t *testing.T) {
		callCount := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/enqueue" {
				job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(job)
				return
			}
			if r.URL.Path == "/api/status" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"version": version.Version,
				})
				return
			}
			if r.URL.Path == "/api/jobs" {
				callCount++
				var status string
				// Unknown 5 times, then "running" to reset counter,
				// then unknown 5 more times, then "done"
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
				// Return a review so showReview succeeds
				// Output must contain "No issues" or similar for ParseVerdict to return "P"
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
		defer ts.Close()

		// Write fake daemon.json
		roborevDir := filepath.Join(tmpHome, ".roborev")
		if err := os.MkdirAll(roborevDir, 0755); err != nil {
			t.Fatalf("failed to create roborev dir: %v", err)
		}
		mockAddr := ts.URL[7:]
		daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
		data, err := json.Marshal(daemonInfo)
		if err != nil {
			t.Fatalf("failed to marshal daemon info: %v", err)
		}
		if err := os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644); err != nil {
			t.Fatalf("failed to write daemon.json: %v", err)
		}

		serverAddr = ts.URL

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--wait", "--quiet"})
		err = cmd.Execute()

		// Should succeed because counter was reset
		if err != nil {
			t.Errorf("expected success (counter should reset on known status), got error: %v", err)
		}
		// Should have called 12 times total (5 unknown + 1 running + 5 unknown + 1 done)
		if callCount != 12 {
			t.Errorf("expected 12 calls, got %d", callCount)
		}
	})
}

func TestReviewBranchFlag(t *testing.T) {
	t.Run("branch and dirty are mutually exclusive", func(t *testing.T) {
		// Set up mock server that accepts any request
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		// Create a minimal git repo
		tmpDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}
		runGit("init")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--branch", "--dirty"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --branch with --dirty")
		}
		if !strings.Contains(err.Error(), "cannot use --branch with --dirty") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("branch with positional args fails", func(t *testing.T) {
		// Set up mock server
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		// Create a minimal git repo
		tmpDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}
		runGit("init")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--branch", "abc123"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --branch with positional arg")
		}
		if !strings.Contains(err.Error(), "cannot specify commits with --branch") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("branch on default branch fails", func(t *testing.T) {
		// Set up mock server
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		// Create a git repo on main branch
		tmpDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}
		runGit("init", "-b", "main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial")

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--branch"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when on default branch")
		}
		if !strings.Contains(err.Error(), "already on main") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("branch with no commits fails", func(t *testing.T) {
		// Set up mock server
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		// Create a git repo with main and feature branch (no commits on feature)
		tmpDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}
		runGit("init", "-b", "main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial")
		runGit("checkout", "-b", "feature")
		// No new commits on feature branch

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--branch"})
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
		// Set up mock server
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
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

		// Create a git repo with main and feature branch with commits
		tmpDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}
		runGit("init", "-b", "main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial")

		// Get main commit SHA for verification
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = tmpDir
		mainSHABytes, _ := cmd.Output()
		mainSHA := strings.TrimSpace(string(mainSHABytes))

		// Create feature branch with a commit
		runGit("checkout", "-b", "feature")
		if err := os.WriteFile(filepath.Join(tmpDir, "feature.txt"), []byte("feature"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "feature.txt")
		runGit("commit", "-m", "feature commit")

		reviewCmd := reviewCmd()
		reviewCmd.SetArgs([]string{"--repo", tmpDir, "--branch"})
		err := reviewCmd.Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the git_ref is a range from main to HEAD
		if !strings.Contains(receivedGitRef, mainSHA) {
			t.Errorf("expected git_ref to contain main SHA %s, got %s", mainSHA, receivedGitRef)
		}
		if !strings.HasSuffix(receivedGitRef, "..HEAD") {
			t.Errorf("expected git_ref to end with ..HEAD, got %s", receivedGitRef)
		}
	})
}
