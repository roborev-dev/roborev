package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestBuildGenericFixPrompt(t *testing.T) {
	analysisOutput := `## Issues Found
- Long function in main.go:50
- Missing error handling`

	prompt := buildGenericFixPrompt(analysisOutput)

	// Should include the analysis output
	if !strings.Contains(prompt, "Issues Found") {
		t.Error("prompt should include analysis output")
	}
	if !strings.Contains(prompt, "Long function") {
		t.Error("prompt should include specific findings")
	}

	// Should have fix instructions
	if !strings.Contains(prompt, "apply the suggested changes") {
		t.Error("prompt should include fix instructions")
	}

	// Should request a commit
	if !strings.Contains(prompt, "git commit") {
		t.Error("prompt should request a commit")
	}
}

func TestBuildGenericCommitPrompt(t *testing.T) {
	prompt := buildGenericCommitPrompt()

	// Should have commit instructions
	if !strings.Contains(prompt, "git commit") {
		t.Error("prompt should mention git commit")
	}
	if !strings.Contains(prompt, "descriptive") {
		t.Error("prompt should request a descriptive message")
	}
}

func TestFetchJob(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		jobs       []storage.ReviewJob
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			jobs: []storage.ReviewJob{{
				ID:     42,
				Status: storage.JobStatusDone,
				Agent:  "test",
			}},
		},
		{
			name:       "not found",
			statusCode: http.StatusOK,
			jobs:       []storage.ReviewJob{},
			wantErr:    true,
			wantErrMsg: "not found",
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
			wantErrMsg: "server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					json.NewEncoder(w).Encode(map[string]interface{}{"jobs": tt.jobs})
				}
			}))
			defer ts.Close()

			job, err := fetchJob(context.Background(), ts.URL, 42)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				} else if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if job.ID != 42 {
				t.Errorf("job.ID = %d, want 42", job.ID)
			}
		})
	}
}

func TestFetchReview(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("job_id") != "42" {
			t.Errorf("unexpected job_id: %s", r.URL.Query().Get("job_id"))
		}

		json.NewEncoder(w).Encode(storage.Review{
			JobID:  42,
			Output: "Analysis output here",
		})
	}))
	defer ts.Close()

	review, err := fetchReview(context.Background(), ts.URL, 42)
	if err != nil {
		t.Fatalf("fetchReview: %v", err)
	}

	if review.JobID != 42 {
		t.Errorf("review.JobID = %d, want 42", review.JobID)
	}
	if review.Output != "Analysis output here" {
		t.Errorf("review.Output = %q, want %q", review.Output, "Analysis output here")
	}
}

func TestAddJobResponse(t *testing.T) {
	var gotJobID int64
	var gotContent string

	var gotCommenter string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/comment" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		gotJobID = int64(req["job_id"].(float64))
		gotContent = req["comment"].(string)
		gotCommenter = req["commenter"].(string)

		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	err := addJobResponse(ts.URL, 123, "Fix applied")
	if err != nil {
		t.Fatalf("addJobResponse: %v", err)
	}

	if gotJobID != 123 {
		t.Errorf("job_id = %d, want 123", gotJobID)
	}
	if gotContent != "Fix applied" {
		t.Errorf("comment = %q, want %q", gotContent, "Fix applied")
	}
	if gotCommenter != "roborev-fix" {
		t.Errorf("commenter = %q, want %q", gotCommenter, "roborev-fix")
	}
}

func TestFixSingleJob(t *testing.T) {
	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping test")
	}

	// Create a real git repo
	tmpDir := t.TempDir()
	runGit := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		return cmd.Run()
	}

	if err := runGit("init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644)
	runGit("add", ".")
	if err := runGit("commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Mock daemon
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/jobs":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusDone,
					Agent:  "test",
				}},
			})
		case r.URL.Path == "/api/review":
			json.NewEncoder(w).Encode(storage.Review{
				JobID:  99,
				Output: "## Issues\n- Found minor issue",
			})
		case r.URL.Path == "/api/comment":
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/api/review/address":
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	opts := fixOptions{
		agentName: "test",
		reasoning: "fast",
	}

	err := fixSingleJob(cmd, tmpDir, 99, opts)
	if err != nil {
		t.Fatalf("fixSingleJob: %v", err)
	}

	// Verify output contains expected content
	outputStr := output.String()
	if !strings.Contains(outputStr, "Issues") {
		t.Error("output should show analysis findings")
	}
	if !strings.Contains(outputStr, "marked as addressed") {
		t.Error("output should confirm job addressed")
	}
}

func TestFixJobNotComplete(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jobs": []storage.ReviewJob{{
				ID:     99,
				Status: storage.JobStatusRunning, // Not complete
				Agent:  "test",
			}},
		})
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	err := fixSingleJob(cmd, t.TempDir(), 99, fixOptions{agentName: "test"})

	if err == nil {
		t.Error("expected error for incomplete job")
	}
	if !strings.Contains(err.Error(), "not complete") {
		t.Errorf("error %q should mention 'not complete'", err.Error())
	}
}

func TestFixCmdFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "no args and no --unaddressed",
			args:    []string{},
			wantErr: "requires at least 1 arg",
		},
		{
			name:    "--branch without --unaddressed",
			args:    []string{"--branch", "main"},
			wantErr: "--branch requires --unaddressed",
		},
		{
			name:    "--all-branches without --unaddressed",
			args:    []string{"--all-branches"},
			wantErr: "--all-branches requires --unaddressed",
		},
		{
			name:    "--unaddressed with positional args",
			args:    []string{"--unaddressed", "123"},
			wantErr: "--unaddressed cannot be used with positional job IDs",
		},
		{
			name:    "--newest-first without --unaddressed",
			args:    []string{"--newest-first", "123"},
			wantErr: "--newest-first requires --unaddressed",
		},
		{
			name:    "--all-branches with --branch",
			args:    []string{"--unaddressed", "--all-branches", "--branch", "main"},
			wantErr: "--all-branches and --branch are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := fixCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestRunFixUnaddressed(t *testing.T) {
	tmpDir := initTestGitRepo(t)

	t.Run("no unaddressed jobs", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("status") != "done" {
				t.Errorf("expected status=done, got %q", q.Get("status"))
			}
			if q.Get("addressed") != "false" {
				t.Errorf("expected addressed=false, got %q", q.Get("addressed"))
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{},
				"has_more": false,
			})
		}))
		defer cleanup()

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		oldWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(oldWd)

		err := runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output.String(), "No unaddressed jobs found") {
			t.Errorf("expected 'No unaddressed jobs found' message, got %q", output.String())
		}
	})

	t.Run("finds and processes unaddressed jobs", func(t *testing.T) {
		var reviewCalls, addressCalls atomic.Int32
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/jobs":
				q := r.URL.Query()
				if q.Get("addressed") == "false" && q.Get("limit") == "0" {
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				} else {
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			case "/api/review":
				reviewCalls.Add(1)
				json.NewEncoder(w).Encode(storage.Review{Output: "findings"})
			case "/api/comment":
				w.WriteHeader(http.StatusCreated)
			case "/api/review/address":
				addressCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			case "/api/enqueue":
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer cleanup()

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		oldWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(oldWd)

		err := runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output.String(), "Found 2 unaddressed job(s)") {
			t.Errorf("expected count message, got %q", output.String())
		}
		if rc := reviewCalls.Load(); rc != 2 {
			t.Errorf("expected 2 review fetches, got %d", rc)
		}
		if ac := addressCalls.Load(); ac != 2 {
			t.Errorf("expected 2 address calls, got %d", ac)
		}
	})

	t.Run("passes branch filter to API", func(t *testing.T) {
		var gotBranch string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" && r.URL.Query().Get("addressed") == "false" {
				gotBranch = r.URL.Query().Get("branch")
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs":     []storage.ReviewJob{},
				"has_more": false,
			})
		}))
		defer cleanup()

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		oldWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(oldWd)

		runFixUnaddressed(cmd, "feature-branch", false, fixOptions{agentName: "test"})
		if gotBranch != "feature-branch" {
			t.Errorf("expected branch=feature-branch, got %q", gotBranch)
		}
	})

	t.Run("server error", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("db error"))
			}
		}))
		defer cleanup()

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		oldWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(oldWd)

		err := runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test"})
		if err == nil {
			t.Fatal("expected error on server failure")
		}
		if !strings.Contains(err.Error(), "server error") {
			t.Errorf("error %q should mention server error", err.Error())
		}
	})
}

func TestRunFixUnaddressedOrdering(t *testing.T) {
	tmpDir := initTestGitRepo(t)

	makeHandler := func() http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/jobs":
				q := r.URL.Query()
				if q.Get("addressed") == "false" {
					// Return newest first (as the API does)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jobs": []storage.ReviewJob{
							{ID: 30, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				} else {
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			case "/api/review":
				json.NewEncoder(w).Encode(storage.Review{Output: "findings"})
			case "/api/comment":
				w.WriteHeader(http.StatusCreated)
			case "/api/review/address":
				w.WriteHeader(http.StatusOK)
			case "/api/enqueue":
				w.WriteHeader(http.StatusOK)
			}
		})
	}

	t.Run("oldest first by default", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, makeHandler())
		defer cleanup()

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		oldWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(oldWd)

		err := runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output.String(), "[10 20 30]") {
			t.Errorf("expected oldest-first order [10 20 30], got %q", output.String())
		}
	})

	t.Run("newest first with flag", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, makeHandler())
		defer cleanup()

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		oldWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(oldWd)

		err := runFixUnaddressed(cmd, "", true, fixOptions{agentName: "test", reasoning: "fast"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output.String(), "[30 20 10]") {
			t.Errorf("expected newest-first order [30 20 10], got %q", output.String())
		}
	})
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmpDir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Run()
	}
	os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = tmpDir
	cmd.Run()
	return tmpDir
}

func TestEnqueueIfNeededSkipsWhenJobExists(t *testing.T) {
	tmpDir := initTestGitRepo(t)
	sha := "abc123def456"

	var enqueueCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			// Return an existing job — hook already fired
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []map[string]interface{}{{"id": 42}},
			})
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(ts.URL, tmpDir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if enqueueCalls.Load() != 0 {
		t.Error("should not enqueue when job already exists on first check")
	}
}

func TestEnqueueIfNeededSkipsWhenJobAppearsAfterWait(t *testing.T) {
	tmpDir := initTestGitRepo(t)
	sha := "abc123def456"

	var jobCheckCalls atomic.Int32
	var enqueueCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			n := jobCheckCalls.Add(1)
			if n == 1 {
				// First check: no jobs yet
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs": []map[string]interface{}{},
				})
			} else {
				// Second check: hook has fired
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jobs": []map[string]interface{}{{"id": 42}},
				})
			}
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(ts.URL, tmpDir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if jobCheckCalls.Load() != 2 {
		t.Errorf("expected 2 job checks, got %d", jobCheckCalls.Load())
	}
	if enqueueCalls.Load() != 0 {
		t.Error("should not enqueue when job appears on second check")
	}
}

func TestEnqueueIfNeededEnqueuesWhenNoJobExists(t *testing.T) {
	tmpDir := initTestGitRepo(t)
	sha := "abc123def456"

	var enqueueCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []map[string]interface{}{},
			})
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(ts.URL, tmpDir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if enqueueCalls.Load() != 1 {
		t.Errorf("should have enqueued exactly once, got %d", enqueueCalls.Load())
	}
}
