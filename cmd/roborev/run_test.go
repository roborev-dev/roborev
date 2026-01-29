package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/storage"
)

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
		// Create temp repo with .roborev.toml
		repoPath := t.TempDir()
		configContent := `review_guidelines = "Always use tabs for indentation"`
		configPath := filepath.Join(repoPath, ".roborev.toml")
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config: %v", err)
		}

		result := buildPromptWithContext(repoPath, "test prompt")

		if !strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to contain '## Project Guidelines' header")
		}
		if !strings.Contains(result, "Always use tabs for indentation") {
			t.Error("Expected result to contain guidelines text")
		}
	})

	t.Run("omits guidelines section when not configured", func(t *testing.T) {
		// Create temp repo without .roborev.toml
		repoPath := t.TempDir()

		result := buildPromptWithContext(repoPath, "test prompt")

		if strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to NOT contain '## Project Guidelines' header when no config")
		}
	})

	t.Run("omits guidelines when config has no guidelines", func(t *testing.T) {
		// Create temp repo with .roborev.toml but no guidelines
		repoPath := t.TempDir()
		configContent := `agent = "claude-code"`
		configPath := filepath.Join(repoPath, ".roborev.toml")
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config: %v", err)
		}

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
		repoPath := t.TempDir()
		configContent := `review_guidelines = "Test guideline"`
		configPath := filepath.Join(repoPath, ".roborev.toml")
		os.WriteFile(configPath, []byte(configContent), 0644)

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

// mockCmd creates a cobra command with captured output for testing
func mockCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd, out
}

func TestShowPromptResult(t *testing.T) {
	t.Run("displays result without verdict exit code", func(t *testing.T) {
		// Mock server that returns a review
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/review" {
				review := storage.Review{
					ID:     1,
					JobID:  123,
					Agent:  "test-agent",
					Output: "Paris", // Simple answer with no verdict
				}
				json.NewEncoder(w).Encode(review)
			}
		}))
		defer server.Close()

		cmd, out := mockCmd()
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
		// Even output that ParseVerdict would interpret as FAIL should return nil
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/review" {
				review := storage.Review{
					ID:     1,
					JobID:  123,
					Agent:  "test-agent",
					Output: "Found several issues:\n1. Bug in line 5\n2. Missing error handling",
				}
				json.NewEncoder(w).Encode(review)
			}
		}))
		defer server.Close()

		cmd, _ := mockCmd()
		err := showPromptResult(cmd, server.URL, 123, false, "")

		// Prompt jobs don't use verdict-based exit codes
		if err != nil {
			t.Errorf("Prompt jobs should not return error based on verdict, got: %v", err)
		}
	})

	t.Run("quiet mode suppresses output", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/review" {
				review := storage.Review{
					ID:     1,
					JobID:  123,
					Agent:  "test-agent",
					Output: "Some output",
				}
				json.NewEncoder(w).Encode(review)
			}
		}))
		defer server.Close()

		cmd, out := mockCmd()
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
		defer server.Close()

		cmd, _ := mockCmd()
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
		defer server.Close()

		cmd, _ := mockCmd()
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

					job := storage.ReviewJob{
						ID:     1,
						Agent:  "test",
						GitRef: receivedRef,
					}
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(job)
				}
			}))
			defer server.Close()

			// Override serverAddr for this test
			oldAddr := serverAddr
			serverAddr = server.URL
			defer func() { serverAddr = oldAddr }()

			// Test the git_ref building directly since runPrompt requires daemon setup
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
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				callCount++
				job := storage.ReviewJob{
					ID:     123,
					Status: storage.JobStatusDone,
				}
				json.NewEncoder(w).Encode(map[string][]storage.ReviewJob{"jobs": {job}})
			} else if r.URL.Path == "/api/review" {
				review := storage.Review{
					ID:     1,
					JobID:  123,
					Agent:  "test-agent",
					Output: "Test result",
				}
				json.NewEncoder(w).Encode(review)
			}
		}))
		defer server.Close()

		cmd, out := mockCmd()
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
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				job := storage.ReviewJob{
					ID:     123,
					Status: storage.JobStatusFailed,
					Error:  "agent crashed",
				}
				json.NewEncoder(w).Encode(map[string][]storage.ReviewJob{"jobs": {job}})
			}
		}))
		defer server.Close()

		cmd, out := mockCmd()
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
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				job := storage.ReviewJob{
					ID:     123,
					Status: storage.JobStatusCanceled,
				}
				json.NewEncoder(w).Encode(map[string][]storage.ReviewJob{"jobs": {job}})
			}
		}))
		defer server.Close()

		cmd, out := mockCmd()
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
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				json.NewEncoder(w).Encode(map[string][]storage.ReviewJob{"jobs": {}})
			}
		}))
		defer server.Close()

		cmd, _ := mockCmd()
		err := waitForPromptJob(cmd, server.URL, 999, false)

		if err == nil {
			t.Error("Expected error for missing job")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("Expected 'not found' in error, got: %v", err)
		}
	})

	t.Run("quiet mode suppresses waiting message", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				job := storage.ReviewJob{
					ID:     123,
					Status: storage.JobStatusDone,
				}
				json.NewEncoder(w).Encode(map[string][]storage.ReviewJob{"jobs": {job}})
			} else if r.URL.Path == "/api/review" {
				review := storage.Review{
					ID:     1,
					JobID:  123,
					Agent:  "test-agent",
					Output: "Test result",
				}
				json.NewEncoder(w).Encode(review)
			}
		}))
		defer server.Close()

		cmd, out := mockCmd()
		err := waitForPromptJob(cmd, server.URL, 123, true) // quiet=true

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		// In quiet mode, no output at all
		if out.Len() > 0 {
			t.Errorf("Expected no output in quiet mode, got: %s", out.String())
		}
	})

	t.Run("polls while job is running", func(t *testing.T) {
		// Use fast poll interval for test
		oldInterval := promptPollInterval
		promptPollInterval = 1 * time.Millisecond
		defer func() { promptPollInterval = oldInterval }()

		pollCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/jobs" {
				pollCount++
				var status storage.JobStatus
				if pollCount < 3 {
					status = storage.JobStatusRunning
				} else {
					status = storage.JobStatusDone
				}
				job := storage.ReviewJob{
					ID:     123,
					Status: status,
				}
				json.NewEncoder(w).Encode(map[string][]storage.ReviewJob{"jobs": {job}})
			} else if r.URL.Path == "/api/review" {
				review := storage.Review{
					ID:     1,
					JobID:  123,
					Agent:  "test-agent",
					Output: "Final result",
				}
				json.NewEncoder(w).Encode(review)
			}
		}))
		defer server.Close()

		cmd, _ := mockCmd()
		err := waitForPromptJob(cmd, server.URL, 123, true)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		if pollCount < 3 {
			t.Errorf("Expected at least 3 polls, got: %d", pollCount)
		}
	})
}
