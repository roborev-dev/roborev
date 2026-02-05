package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

// createRepoWithConfig creates a temp directory with a .roborev.toml file.
// If configContent is empty, no config file is written.
func createRepoWithConfig(t *testing.T, configContent string) string {
	t.Helper()
	repoPath := t.TempDir()
	if configContent != "" {
		if err := os.WriteFile(filepath.Join(repoPath, ".roborev.toml"), []byte(configContent), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return repoPath
}

// newMockReviewServer creates a test server that returns the given review on /api/review.
func newMockReviewServer(t *testing.T, review storage.Review) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/review" {
			writeJSON(w, review)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(s.Close)
	return s
}

// newMockJobsServer creates a test server that returns the given jobs on /api/jobs
// and optionally serves /api/review with the provided review.
func newMockJobsServer(t *testing.T, jobs []storage.ReviewJob, review *storage.Review) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			writeJSON(w, map[string][]storage.ReviewJob{"jobs": jobs})
		case "/api/review":
			if review != nil {
				writeJSON(w, *review)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// stubReview creates a storage.Review with common defaults.
var nextStubReviewID atomic.Int64

func stubReview(jobID int64, agent, output string) storage.Review {
	return storage.Review{
		ID:     nextStubReviewID.Add(1),
		JobID:  jobID,
		Agent:  agent,
		Output: output,
	}
}

// patchPromptPollInterval sets promptPollInterval to a fast value and restores it on cleanup.
func patchPromptPollInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := promptPollInterval
	promptPollInterval = d
	t.Cleanup(func() { promptPollInterval = old })
}

func TestBuildPromptWithContext(t *testing.T) {
	t.Run("includes repo name and path", func(t *testing.T) {
		repoPath := "/path/to/my-project"
		userPrompt := "Explain this code"

		result := buildPromptWithContext(repoPath, userPrompt)

		if !strings.Contains(result, "my-project") {
			t.Error("Expected result to contain repo name 'my-project'")
		}
		if !strings.Contains(result, repoPath) {
			t.Error("Expected result to contain repo path")
		}
		if !strings.Contains(result, "## Context") {
			t.Error("Expected result to contain '## Context' header")
		}
		if !strings.Contains(result, "## Request") {
			t.Error("Expected result to contain '## Request' header")
		}
		if !strings.Contains(result, userPrompt) {
			t.Error("Expected result to contain user prompt")
		}
	})

	t.Run("includes project guidelines when present", func(t *testing.T) {
		repoPath := createRepoWithConfig(t, `review_guidelines = "Always use tabs for indentation"`)

		result := buildPromptWithContext(repoPath, "test prompt")

		if !strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to contain '## Project Guidelines' header")
		}
		if !strings.Contains(result, "Always use tabs for indentation") {
			t.Error("Expected result to contain guidelines text")
		}
	})

	t.Run("omits guidelines section when not configured", func(t *testing.T) {
		repoPath := createRepoWithConfig(t, "")

		result := buildPromptWithContext(repoPath, "test prompt")

		if strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to NOT contain '## Project Guidelines' header when no config")
		}
	})

	t.Run("omits guidelines when config has no guidelines", func(t *testing.T) {
		repoPath := createRepoWithConfig(t, `agent = "claude-code"`)

		result := buildPromptWithContext(repoPath, "test prompt")

		if strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to NOT contain '## Project Guidelines' when guidelines empty")
		}
	})

	t.Run("preserves user prompt exactly", func(t *testing.T) {
		repoPath := "/tmp/test"
		userPrompt := "Find all TODO comments\nand list them"

		result := buildPromptWithContext(repoPath, userPrompt)

		if !strings.Contains(result, userPrompt) {
			t.Error("Expected user prompt to be preserved exactly")
		}
	})

	t.Run("correct section order", func(t *testing.T) {
		repoPath := createRepoWithConfig(t, `review_guidelines = "Test guideline"`)

		result := buildPromptWithContext(repoPath, "test prompt")

		contextPos := strings.Index(result, "## Context")
		guidelinesPos := strings.Index(result, "## Project Guidelines")
		requestPos := strings.Index(result, "## Request")

		if contextPos == -1 || guidelinesPos == -1 || requestPos == -1 {
			t.Fatal("Missing expected sections")
		}

		if contextPos > guidelinesPos {
			t.Error("Context should come before Guidelines")
		}
		if guidelinesPos > requestPos {
			t.Error("Guidelines should come before Request")
		}
	})
}

func TestShowPromptResult(t *testing.T) {
	t.Run("displays result without verdict exit code", func(t *testing.T) {
		review := stubReview(123, "test-agent", "Paris")
		server := newMockReviewServer(t, review)

		cmd, out := newTestCmd(t)
		err := showPromptResult(cmd, server.URL, 123, false, "")

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "Result (by test-agent)") {
			t.Error("Expected result header with agent name")
		}
		if !strings.Contains(output, "Paris") {
			t.Error("Expected output to contain 'Paris'")
		}
	})

	t.Run("returns nil for output that would be FAIL verdict in review", func(t *testing.T) {
		review := stubReview(123, "test-agent", "Found several issues:\n1. Bug in line 5\n2. Missing error handling")
		server := newMockReviewServer(t, review)

		cmd, _ := newTestCmd(t)
		err := showPromptResult(cmd, server.URL, 123, false, "")

		if err != nil {
			t.Errorf("Prompt jobs should not return error based on verdict, got: %v", err)
		}
	})

	t.Run("quiet mode suppresses output", func(t *testing.T) {
		review := stubReview(123, "test-agent", "Some output")
		server := newMockReviewServer(t, review)

		cmd, out := newTestCmd(t)
		err := showPromptResult(cmd, server.URL, 123, true, "") // quiet=true

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		if out.Len() > 0 {
			t.Errorf("Expected no output in quiet mode, got: %s", out.String())
		}
	})

	t.Run("handles not found error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)

		cmd, _ := newTestCmd(t)
		err := showPromptResult(cmd, server.URL, 999, false, "")

		if err == nil {
			t.Error("Expected error for not found")
		}
		if !strings.Contains(err.Error(), "no result found") {
			t.Errorf("Expected 'no result found' error, got: %v", err)
		}
	})

	t.Run("handles server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
		}))
		t.Cleanup(server.Close)

		cmd, _ := newTestCmd(t)
		err := showPromptResult(cmd, server.URL, 123, false, "")

		if err == nil {
			t.Error("Expected error for server error")
		}
		if !strings.Contains(err.Error(), "server error") {
			t.Errorf("Expected 'server error' in message, got: %v", err)
		}
	})
}

func TestRunLabelFlag(t *testing.T) {
	tests := []struct {
		name        string
		label       string
		expectedRef string
	}{
		{
			name:        "no label defaults to run",
			label:       "",
			expectedRef: "run",
		},
		{
			name:        "custom label is used",
			label:       "my-task",
			expectedRef: "my-task",
		},
		{
			name:        "analyze label",
			label:       "analyze",
			expectedRef: "analyze",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedRef string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/enqueue" {
					var req map[string]interface{}
					json.NewDecoder(r.Body).Decode(&req)
					receivedRef = req["git_ref"].(string)

					w.WriteHeader(http.StatusCreated)
					writeJSON(w, storage.ReviewJob{
						ID:     1,
						Agent:  "test",
						GitRef: receivedRef,
					})
				}
			}))
			t.Cleanup(server.Close)

			patchServerAddr(t, server.URL)

			gitRef := "run"
			if tt.label != "" {
				gitRef = tt.label
			}

			reqBody, _ := json.Marshal(map[string]interface{}{
				"repo_path":     "/test",
				"git_ref":       gitRef,
				"custom_prompt": "test prompt",
			})

			resp, err := http.Post(server.URL+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			resp.Body.Close()

			if receivedRef != tt.expectedRef {
				t.Errorf("Expected git_ref %q, got %q", tt.expectedRef, receivedRef)
			}
		})
	}
}

func TestWaitForPromptJob(t *testing.T) {
	t.Run("returns success when job completes", func(t *testing.T) {
		review := stubReview(123, "test-agent", "Test result")
		doneJob := storage.ReviewJob{ID: 123, Status: storage.JobStatusDone}
		server := newMockJobsServer(t, []storage.ReviewJob{doneJob}, &review)

		cmd, out := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, false)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "done!") {
			t.Error("Expected 'done!' in output")
		}
		if !strings.Contains(output, "Test result") {
			t.Error("Expected result in output")
		}
	})

	t.Run("returns error when job fails", func(t *testing.T) {
		failedJob := storage.ReviewJob{ID: 123, Status: storage.JobStatusFailed, Error: "agent crashed"}
		server := newMockJobsServer(t, []storage.ReviewJob{failedJob}, nil)

		cmd, out := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, false)

		if err == nil {
			t.Error("Expected error for failed job")
		}
		if !strings.Contains(err.Error(), "agent crashed") {
			t.Errorf("Expected error message to contain reason, got: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "failed!") {
			t.Error("Expected 'failed!' in output")
		}
	})

	t.Run("returns error when job canceled", func(t *testing.T) {
		canceledJob := storage.ReviewJob{ID: 123, Status: storage.JobStatusCanceled}
		server := newMockJobsServer(t, []storage.ReviewJob{canceledJob}, nil)

		cmd, out := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, false)

		if err == nil {
			t.Error("Expected error for canceled job")
		}
		if !strings.Contains(err.Error(), "canceled") {
			t.Errorf("Expected 'canceled' in error, got: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "canceled!") {
			t.Error("Expected 'canceled!' in output")
		}
	})

	t.Run("returns error when job not found", func(t *testing.T) {
		server := newMockJobsServer(t, []storage.ReviewJob{}, nil)

		cmd, _ := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 999, false)

		if err == nil {
			t.Error("Expected error for missing job")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("Expected 'not found' in error, got: %v", err)
		}
	})

	t.Run("quiet mode suppresses waiting message", func(t *testing.T) {
		review := stubReview(123, "test-agent", "Test result")
		doneJob := storage.ReviewJob{ID: 123, Status: storage.JobStatusDone}
		server := newMockJobsServer(t, []storage.ReviewJob{doneJob}, &review)

		cmd, out := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, true) // quiet=true

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		if out.Len() > 0 {
			t.Errorf("Expected no output in quiet mode, got: %s", out.String())
		}
	})

	t.Run("polls while job is running", func(t *testing.T) {
		patchPromptPollInterval(t, 1*time.Millisecond)

		pollCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/jobs":
				pollCount++
				status := storage.JobStatusRunning
				if pollCount >= 3 {
					status = storage.JobStatusDone
				}
				writeJSON(w, map[string][]storage.ReviewJob{
					"jobs": {{ID: 123, Status: status}},
				})
			case "/api/review":
				writeJSON(w, stubReview(123, "test-agent", "Final result"))
			}
		}))
		t.Cleanup(server.Close)

		cmd, _ := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, true)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		if pollCount < 3 {
			t.Errorf("Expected at least 3 polls, got: %d", pollCount)
		}
	})
}
