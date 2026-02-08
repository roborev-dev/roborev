package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
	"github.com/roborev-dev/roborev/internal/version"
)

// setupGitRepoInDir initializes a git repository in the given directory and returns the commit SHA.
// It creates an initial commit with a test file.
// Additional environment variables can be provided via extraEnv (e.g., "HOME=/path").
func setupGitRepoInDir(t *testing.T, tmpDir string, extraEnv ...string) string {
	t.Helper()

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		env := append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		env = append(env, extraEnv...)
		cmd.Env = env
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

	// Get the commit SHA
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = tmpDir
	shaBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get commit SHA: %v", err)
	}
	return strings.TrimSpace(string(shaBytes))
}

// TestOllamaE2E_ReviewWithMockServer tests the full review flow with a mocked Ollama server
func TestOllamaE2E_ReviewWithMockServer(t *testing.T) {
	// Skip if not in a git repo (CI might not have one)
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	db, tmpDir := testutil.OpenTestDBWithDir(t)

	// Initialize git repo and get commit SHA
	commitSHA := setupGitRepoInDir(t, tmpDir)

	// Create mock Ollama server
	var receivedRequest []byte
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			receivedRequest, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/x-ndjson")
			// Simulate streaming response
			chunks := []string{
				`{"model":"test-model","message":{"role":"assistant","content":"This"},"done":false}` + "\n",
				`{"model":"test-model","message":{"role":"assistant","content":" commit"},"done":false}` + "\n",
				`{"model":"test-model","message":{"role":"assistant","content":" looks"},"done":false}` + "\n",
				`{"model":"test-model","message":{"role":"assistant","content":" good."},"done":true}` + "\n",
			}
			for _, chunk := range chunks {
				_, _ = w.Write([]byte(chunk))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return
		}
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ollamaServer.Close()

	// Setup config with Ollama agent and custom base URL
	cfg := config.DefaultConfig()
	cfg.DefaultAgent = "ollama"
	cfg.DefaultModel = "test-model"
	cfg.OllamaBaseURL = ollamaServer.URL

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, commitSHA, "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue a job with Ollama agent
	job, err := db.EnqueueJob(repo.ID, commit.ID, commitSHA, "main", "ollama", "test-model", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create and start worker pool
	broadcaster := daemon.NewBroadcaster()
	pool := daemon.NewWorkerPool(db, daemon.NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to complete (with timeout)
	deadline := time.Now().Add(10 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalJob.Status != storage.JobStatusDone {
		t.Fatalf("Job should be done, got status %s", finalJob.Status)
	}

	// Verify review was stored
	review, err := db.GetReviewByCommitSHA(commitSHA)
	if err != nil {
		t.Fatalf("GetReviewByCommitSHA failed: %v", err)
	}
	if review.Agent != "ollama" {
		t.Errorf("Expected agent 'ollama', got '%s'", review.Agent)
	}
	if !strings.Contains(review.Output, "This commit looks good.") {
		t.Errorf("Review output should contain expected text, got: %q", review.Output)
	}

	// Verify request was sent to Ollama with correct format
	var reqBody struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(receivedRequest, &reqBody); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}
	if reqBody.Model != "test-model" {
		t.Errorf("Expected model 'test-model', got %q", reqBody.Model)
	}
	if len(reqBody.Messages) != 1 || reqBody.Messages[0].Role != "user" {
		t.Errorf("Expected single user message, got %+v", reqBody.Messages)
	}
	if reqBody.Stream == nil || !*reqBody.Stream {
		t.Errorf("Expected stream=true, got %v", reqBody.Stream)
	}
}

// TestOllamaE2E_ConfigResolution tests config resolution through full stack
func TestOllamaE2E_ConfigResolution(t *testing.T) {
	// Skip if not in a git repo
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	db, tmpDir := testutil.OpenTestDBWithDir(t)

	// Initialize git repo and get commit SHA
	commitSHA := setupGitRepoInDir(t, tmpDir)

	// Create mock Ollama server
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"model":"repo-model","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
			return
		}
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ollamaServer.Close()

	// Setup global config
	cfg := config.DefaultConfig()
	cfg.DefaultAgent = "ollama"
	cfg.DefaultModel = "global-model"
	cfg.OllamaBaseURL = ollamaServer.URL

	// Create repo config with different model (repo config should take precedence)
	repoConfig := filepath.Join(tmpDir, ".roborev.toml")
	if err := os.WriteFile(repoConfig, []byte(`model = "repo-model"`), 0644); err != nil {
		t.Fatalf("Failed to write repo config: %v", err)
	}

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, commitSHA, "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue job without explicit model (should use repo config)
	job, err := db.EnqueueJob(repo.ID, commit.ID, commitSHA, "main", "ollama", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create and start worker pool
	broadcaster := daemon.NewBroadcaster()
	pool := daemon.NewWorkerPool(db, daemon.NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to complete
	deadline := time.Now().Add(10 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalJob.Status != storage.JobStatusDone {
		t.Fatalf("Job should be done, got status %s", finalJob.Status)
	}

	// Verify the model from repo config was used (check via review output or job model field)
	if finalJob.Model != "" && finalJob.Model != "repo-model" {
		t.Errorf("Expected model 'repo-model' from repo config, got %q", finalJob.Model)
	}
}

// TestOllamaE2E_ModelRequired tests error when model not configured
func TestOllamaE2E_ModelRequired(t *testing.T) {
	// Skip if not in a git repo
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	db, tmpDir := testutil.OpenTestDBWithDir(t)

	// Initialize git repo and get commit SHA
	commitSHA := setupGitRepoInDir(t, tmpDir)

	// Create mock Ollama server (should not be called)
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Ollama server should not be called when model is missing")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ollamaServer.Close()

	// Setup config with Ollama agent but no model
	cfg := config.DefaultConfig()
	cfg.DefaultAgent = "ollama"
	cfg.DefaultModel = "" // No model configured
	cfg.OllamaBaseURL = ollamaServer.URL

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, commitSHA, "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue job without model
	job, err := db.EnqueueJob(repo.ID, commit.ID, commitSHA, "main", "ollama", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create and start worker pool
	broadcaster := daemon.NewBroadcaster()
	pool := daemon.NewWorkerPool(db, daemon.NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to fail (with timeout)
	deadline := time.Now().Add(10 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalJob.Status != storage.JobStatusFailed {
		t.Fatalf("Job should be failed, got status %s", finalJob.Status)
	}

	// Verify error message mentions model configuration
	if !strings.Contains(finalJob.Error, "model not configured") && !strings.Contains(finalJob.Error, "model") {
		t.Errorf("Error message should mention model configuration, got: %q", finalJob.Error)
	}
}

// TestOllamaE2E_BaseURLOverride tests custom base URL from config
func TestOllamaE2E_BaseURLOverride(t *testing.T) {
	// Skip if not in a git repo
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	db, tmpDir := testutil.OpenTestDBWithDir(t)

	// Initialize git repo and get commit SHA
	commitSHA := setupGitRepoInDir(t, tmpDir)

	// Create mock Ollama server
	// Note: We can't actually test a different URL with httptest, but we can verify
	// the config is passed through correctly by checking the request
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
			return
		}
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ollamaServer.Close()

	// Setup config with custom base URL (using test server URL)
	cfg := config.DefaultConfig()
	cfg.DefaultAgent = "ollama"
	cfg.DefaultModel = "test-model"
	cfg.OllamaBaseURL = ollamaServer.URL // Use test server URL as "custom"

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, commitSHA, "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue job
	job, err := db.EnqueueJob(repo.ID, commit.ID, commitSHA, "main", "ollama", "test-model", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create and start worker pool
	broadcaster := daemon.NewBroadcaster()
	pool := daemon.NewWorkerPool(db, daemon.NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to complete
	deadline := time.Now().Add(10 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalJob.Status != storage.JobStatusDone {
		t.Fatalf("Job should be done, got status %s", finalJob.Status)
	}

	// Verify custom base URL was used (indirectly - job completed successfully)
	// The actual URL verification happens at the agent level
}

// TestOllamaE2E_StreamingOutput tests streaming output in full flow
func TestOllamaE2E_StreamingOutput(t *testing.T) {
	// Skip if not in a git repo
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	db, tmpDir := testutil.OpenTestDBWithDir(t)

	// Initialize git repo and get commit SHA
	commitSHA := setupGitRepoInDir(t, tmpDir)

	// Create mock Ollama server with streaming response
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			// Send multiple chunks to test streaming
			chunks := []string{
				`{"model":"test-model","message":{"role":"assistant","content":"Chunk"},"done":false}` + "\n",
				`{"model":"test-model","message":{"role":"assistant","content":" 1"},"done":false}` + "\n",
				`{"model":"test-model","message":{"role":"assistant","content":" Chunk"},"done":false}` + "\n",
				`{"model":"test-model","message":{"role":"assistant","content":" 2"},"done":true}` + "\n",
			}
			for _, chunk := range chunks {
				_, _ = w.Write([]byte(chunk))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				time.Sleep(10 * time.Millisecond) // Small delay to simulate streaming
			}
			return
		}
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ollamaServer.Close()

	// Setup config
	cfg := config.DefaultConfig()
	cfg.DefaultAgent = "ollama"
	cfg.DefaultModel = "test-model"
	cfg.OllamaBaseURL = ollamaServer.URL

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, commitSHA, "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue job
	job, err := db.EnqueueJob(repo.ID, commit.ID, commitSHA, "main", "ollama", "test-model", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create and start worker pool
	broadcaster := daemon.NewBroadcaster()
	pool := daemon.NewWorkerPool(db, daemon.NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to complete
	deadline := time.Now().Add(10 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalJob.Status != storage.JobStatusDone {
		t.Fatalf("Job should be done, got status %s", finalJob.Status)
	}

	// Verify streaming output was accumulated correctly
	review, err := db.GetReviewByCommitSHA(commitSHA)
	if err != nil {
		t.Fatalf("GetReviewByCommitSHA failed: %v", err)
	}
	expected := "Chunk 1 Chunk 2"
	if review.Output != expected {
		t.Errorf("Expected output %q, got %q", expected, review.Output)
	}
}

// TestOllamaE2E_ErrorHandling tests error propagation through full stack
func TestOllamaE2E_ErrorHandling(t *testing.T) {
	// Skip if not in a git repo
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	db, tmpDir := testutil.OpenTestDBWithDir(t)

	// Initialize git repo and get commit SHA
	commitSHA := setupGitRepoInDir(t, tmpDir)

	// Create mock Ollama server that returns errors
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model 'missing-model' not found"}`))
			return
		}
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ollamaServer.Close()

	// Setup config
	cfg := config.DefaultConfig()
	cfg.DefaultAgent = "ollama"
	cfg.DefaultModel = "missing-model"
	cfg.OllamaBaseURL = ollamaServer.URL

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, commitSHA, "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue job
	job, err := db.EnqueueJob(repo.ID, commit.ID, commitSHA, "main", "ollama", "missing-model", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create and start worker pool
	broadcaster := daemon.NewBroadcaster()
	pool := daemon.NewWorkerPool(db, daemon.NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to fail
	deadline := time.Now().Add(10 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalJob.Status != storage.JobStatusFailed {
		t.Fatalf("Job should be failed, got status %s", finalJob.Status)
	}

	// Verify error message contains useful information
	if !strings.Contains(finalJob.Error, "not found") && !strings.Contains(finalJob.Error, "missing-model") {
		t.Errorf("Error message should mention model not found, got: %q", finalJob.Error)
	}
}

// TestOllamaE2E_ServerNotRunning tests error when Ollama server is not reachable
func TestOllamaE2E_ServerNotRunning(t *testing.T) {
	// Skip if not in a git repo (CI might not have one)
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	db, tmpDir := testutil.OpenTestDBWithDir(t)

	// Use a URL that will fail to connect (nothing listening on this port)
	unreachableURL := "http://127.0.0.1:19999"

	// Setup config with unreachable server
	cfg := config.DefaultConfig()
	cfg.DefaultAgent = "ollama"
	cfg.DefaultModel = "test-model"
	cfg.OllamaBaseURL = unreachableURL

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, "testsha-unreachable", "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue job
	job, err := db.EnqueueJob(repo.ID, commit.ID, "testsha-unreachable", "main", "ollama", "test-model", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create and start worker pool
	broadcaster := daemon.NewBroadcaster()
	pool := daemon.NewWorkerPool(db, daemon.NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to fail (with shorter timeout since connection will fail quickly)
	deadline := time.Now().Add(15 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalJob.Status != storage.JobStatusFailed {
		t.Fatalf("Job should be failed, got status %s", finalJob.Status)
	}

	// Verify error message mentions connection issue
	if !strings.Contains(finalJob.Error, "connection") && !strings.Contains(finalJob.Error, "not reachable") && !strings.Contains(finalJob.Error, "refused") {
		t.Errorf("Error message should mention connection issue, got: %q", finalJob.Error)
	}
}

// TestOllamaE2E_CLIEnqueue tests CLI enqueue with Ollama agent
func TestOllamaE2E_CLIEnqueue(t *testing.T) {
	// Skip if not in a git repo
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err != nil {
		t.Skip("Not in a git repo, skipping e2e test")
	}

	// Save and restore serverAddr
	origServerAddr := serverAddr
	t.Cleanup(func() { serverAddr = origServerAddr })

	// Override HOME to prevent reading real daemon.json
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpHome)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// Create a temp git repo with custom HOME
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir, "HOME="+tmpHome)

	// Create mock daemon server
	var receivedAgent string
	var receivedModel string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/enqueue" {
			var req struct {
				Agent string `json:"agent"`
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			receivedAgent = req.Agent
			receivedModel = req.Model

			job := storage.ReviewJob{ID: 1, GitRef: "HEAD", Agent: req.Agent, Status: "queued"}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(job)
			return
		}
		if r.URL.Path == "/api/status" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"version": version.Version,
			})
			return
		}
	}))
	defer ts.Close()

	// Write fake daemon.json
	roborevDir := filepath.Join(tmpHome, ".roborev")
	_ = os.MkdirAll(roborevDir, 0755)
	mockAddr := ts.URL[7:] // remove "http://"
	daemonInfo := daemon.RuntimeInfo{Addr: mockAddr, PID: os.Getpid(), Version: version.Version}
	data, _ := json.Marshal(daemonInfo)
	_ = os.WriteFile(filepath.Join(roborevDir, "daemon.json"), data, 0644)

	serverAddr = ts.URL

	// Test: CLI enqueue with Ollama agent and model
	t.Run("enqueue with Ollama agent and model", func(t *testing.T) {
		receivedAgent = ""
		receivedModel = ""

		cmd := reviewCmd()
		cmd.SetArgs([]string{"--repo", tmpDir, "--agent", "ollama", "--model", "test-model"})
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}

		if receivedAgent != "ollama" {
			t.Errorf("Expected agent 'ollama', got %q", receivedAgent)
		}
		if receivedModel != "test-model" {
			t.Errorf("Expected model 'test-model', got %q", receivedModel)
		}
	})
}
