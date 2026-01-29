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
