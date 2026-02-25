package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
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

type mockServerConfig struct {
	jobs        []storage.ReviewJob
	review      *storage.Review
	status      int // Default 200
	receivedRef *string
}

// newRunTestServer creates a unified test server for run command tests.
func newRunTestServer(t *testing.T, cfg mockServerConfig) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.status != 0 && cfg.status != 200 {
			w.WriteHeader(cfg.status)
			if cfg.status == http.StatusInternalServerError {
				w.Write([]byte("server error"))
			}
			return
		}

		switch r.URL.Path {
		case "/api/status":
			// Required for ensureDaemon()
			writeJSON(w, map[string]string{"version": version.Version})
		case "/api/jobs":
			writeJSON(w, map[string][]storage.ReviewJob{"jobs": cfg.jobs})
		case "/api/review":
			if cfg.review != nil {
				writeJSON(w, *cfg.review)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "/api/enqueue":
			if r.Method == "POST" {
				var req daemon.EnqueueRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if cfg.receivedRef != nil {
					*cfg.receivedRef = req.GitRef
				}
				w.WriteHeader(http.StatusCreated)
				writeJSON(w, storage.ReviewJob{
					ID:     1,
					Agent:  "test",
					GitRef: req.GitRef,
				})
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

func TestBuildPromptWithContext(t *testing.T) {
	t.Run("includes repo name and path", func(t *testing.T) {
		repoPath := "/path/to/my-project"
		userPrompt := "Explain this code"

		result := buildPromptWithContext(repoPath, userPrompt)

		expectedStrings := []string{"my-project", repoPath, "## Context", "## Request", userPrompt}
		for _, s := range expectedStrings {
			if !strings.Contains(result, s) {
				t.Errorf("Expected result to contain %q", s)
			}
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
		server := newRunTestServer(t, mockServerConfig{review: &review})

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
		server := newRunTestServer(t, mockServerConfig{review: &review})

		cmd, _ := newTestCmd(t)
		err := showPromptResult(cmd, server.URL, 123, false, "")

		if err != nil {
			t.Errorf("Prompt jobs should not return error based on verdict, got: %v", err)
		}
	})

	t.Run("quiet mode suppresses output", func(t *testing.T) {
		review := stubReview(123, "test-agent", "Some output")
		server := newRunTestServer(t, mockServerConfig{review: &review})

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
		server := newRunTestServer(t, mockServerConfig{status: http.StatusInternalServerError})
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
			server := newRunTestServer(t, mockServerConfig{receivedRef: &receivedRef})
			patchServerAddr(t, server.URL)

			cmd := runCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			args := []string{"test prompt"}
			if tt.label != "" {
				args = append(args, "--label", tt.label)
			}
			cmd.SetArgs(args)

			err := cmd.Execute()
			if err != nil {
				t.Fatalf("Unexpected error executing command: %v", err)
			}

			if receivedRef != tt.expectedRef {
				t.Errorf("Expected git_ref %q, got %q", tt.expectedRef, receivedRef)
			}
		})
	}
}

func newPollingTestServer(t *testing.T, statuses []storage.JobStatus) (*httptest.Server, *int) {
	t.Helper()
	pollCount := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			status := statuses[len(statuses)-1] // Default to last
			if pollCount < len(statuses) {
				status = statuses[pollCount]
			}
			pollCount++

			writeJSON(w, map[string][]storage.ReviewJob{"jobs": {{ID: 123, Status: status}}})
		case "/api/review":
			writeJSON(w, stubReview(123, "test-agent", "Final result"))
		}
	}))
	t.Cleanup(s.Close)
	return s, &pollCount
}

func TestWaitForPromptJob(t *testing.T) {
	review := stubReview(123, "test-agent", "Result")

	tests := []struct {
		name        string
		jobs        []storage.ReviewJob
		review      *storage.Review
		quiet       bool
		expectError string
		expectOut   []string
	}{
		{
			name:      "success",
			jobs:      []storage.ReviewJob{{ID: 123, Status: storage.JobStatusDone}},
			review:    &review,
			expectOut: []string{"done!", "Result"},
		},
		{
			name:        "failed job",
			jobs:        []storage.ReviewJob{{ID: 123, Status: storage.JobStatusFailed, Error: "oops"}},
			expectError: "oops",
			expectOut:   []string{"failed!"},
		},
		{
			name:        "canceled job",
			jobs:        []storage.ReviewJob{{ID: 123, Status: storage.JobStatusCanceled}},
			expectError: "canceled",
			expectOut:   []string{"canceled!"},
		},
		{
			name:        "job not found",
			jobs:        []storage.ReviewJob{},
			expectError: "not found",
		},
		{
			name:      "quiet mode success",
			jobs:      []storage.ReviewJob{{ID: 123, Status: storage.JobStatusDone}},
			review:    &review,
			quiet:     true,
			expectOut: []string{}, // Should be empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newRunTestServer(t, mockServerConfig{jobs: tt.jobs, review: tt.review})
			cmd, out := newTestCmd(t)

			// Use small poll interval for tests
			err := waitForPromptJob(cmd, server.URL, 123, tt.quiet, 1*time.Millisecond)

			if tt.expectError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.expectError) {
					t.Errorf("Expected error containing %q, got %v", tt.expectError, err)
				}
			} else if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			output := out.String()
			if tt.quiet {
				if output != "" {
					t.Errorf("Expected no output in quiet mode, got: %q", output)
				}
			} else {
				for _, s := range tt.expectOut {
					if !strings.Contains(output, s) {
						t.Errorf("Expected output to contain %q, got: %q", s, output)
					}
				}
			}
		})
	}

	t.Run("polls while job is running", func(t *testing.T) {
		server, pollCount := newPollingTestServer(t, []storage.JobStatus{
			storage.JobStatusRunning,
			storage.JobStatusRunning,
			storage.JobStatusDone,
		})

		cmd, _ := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, true, 1*time.Millisecond)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		if *pollCount < 3 {
			t.Errorf("Expected at least 3 polls, got: %d", *pollCount)
		}
	})

	t.Run("retries on unknown status", func(t *testing.T) {
		server, pollCount := newPollingTestServer(t, []storage.JobStatus{
			"unknown_status",
			"unknown_status",
			storage.JobStatusDone,
		})

		cmd, _ := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, true, 1*time.Millisecond)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		if *pollCount < 3 {
			t.Errorf("Expected at least 3 polls, got: %d", *pollCount)
		}
	})

	t.Run("fails after max unknown retries", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/jobs":
				writeJSON(w, map[string][]storage.ReviewJob{
					"jobs": {{ID: 123, Status: "unknown_status"}},
				})
			}
		}))
		t.Cleanup(server.Close)

		cmd, _ := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, true, 1*time.Millisecond)

		if err == nil {
			t.Fatal("Expected error for max unknown retries")
		}
		if !strings.Contains(err.Error(), "giving up") {
			t.Errorf("Expected 'giving up' error, got: %v", err)
		}
	})

	for _, tt := range []struct {
		name     string
		interval time.Duration
	}{
		{"zero", 0},
		{"negative", -1 * time.Millisecond},
	} {
		t.Run("falls back to default interval when pollInterval is "+tt.name, func(t *testing.T) {
			// Override the fallback interval to a known value
			origInterval := promptPollInterval
			promptPollInterval = 5 * time.Millisecond
			t.Cleanup(func() { promptPollInterval = origInterval })

			var pollTimes []time.Time
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/jobs":
					pollTimes = append(pollTimes, time.Now())
					status := storage.JobStatusRunning
					if len(pollTimes) >= 3 {
						status = storage.JobStatusDone
					}
					writeJSON(w, map[string][]storage.ReviewJob{
						"jobs": {{ID: 123, Status: status}},
					})
				case "/api/review":
					writeJSON(w, stubReview(123, "test-agent", "Result"))
				}
			}))
			t.Cleanup(server.Close)

			cmd, _ := newTestCmd(t)
			err := waitForPromptJob(cmd, server.URL, 123, true, tt.interval)

			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}
			if len(pollTimes) < 3 {
				t.Fatalf("Expected at least 3 polls, got: %d", len(pollTimes))
			}
			// Verify polls are spaced by at least 3ms (fallback is 5ms;
			// allow slack for scheduling jitter but reject busy-polling)
			for i := 1; i < len(pollTimes); i++ {
				gap := pollTimes[i].Sub(pollTimes[i-1])
				if gap < 3*time.Millisecond {
					t.Errorf(
						"Poll gap %d→%d was %v, expected ≥3ms (fallback interval)",
						i-1, i, gap,
					)
				}
			}
		})
	}

	t.Run("resets unknown counter on known status", func(t *testing.T) {
		statuses := []storage.JobStatus{
			"unknown_status", "unknown_status", "unknown_status", "unknown_status", "unknown_status",
			storage.JobStatusRunning,
			"unknown_status", "unknown_status", "unknown_status", "unknown_status", "unknown_status", "unknown_status",
			storage.JobStatusDone,
		}
		server, pollCount := newPollingTestServer(t, statuses)

		cmd, _ := newTestCmd(t)
		err := waitForPromptJob(cmd, server.URL, 123, true, 1*time.Millisecond)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		if *pollCount < 13 {
			t.Errorf("Expected at least 13 polls, got: %d", *pollCount)
		}
	})
}
