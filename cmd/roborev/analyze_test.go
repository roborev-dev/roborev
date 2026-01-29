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
	"time"

	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/prompt/analyze"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestExpandAndReadFiles(t *testing.T) {
	// Create temp directory with test files
	tmpDir := t.TempDir()

	// Create some test files
	writeFile := func(path, content string) {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeFile("main.go", "package main\n")
	writeFile("util.go", "package main\n// util\n")
	writeFile("sub/helper.go", "package sub\n")
	writeFile("sub/helper_test.go", "package sub\n// test\n")
	writeFile("data.json", `{"key": "value"}`)
	writeFile("README.md", "# README\n")
	writeFile(".hidden/secret.go", "package hidden\n")

	tests := []struct {
		name     string
		patterns []string
		wantKeys []string
		wantErr  bool
	}{
		{
			name:     "single file",
			patterns: []string{"main.go"},
			wantKeys: []string{"main.go"},
		},
		{
			name:     "glob pattern",
			patterns: []string{"*.go"},
			wantKeys: []string{"main.go", "util.go"},
		},
		{
			name:     "subdirectory file",
			patterns: []string{"sub/helper.go"},
			wantKeys: []string{"sub/helper.go"},
		},
		{
			name:     "subdirectory glob",
			patterns: []string{"sub/*.go"},
			wantKeys: []string{"sub/helper.go", "sub/helper_test.go"},
		},
		{
			name:     "multiple patterns",
			patterns: []string{"main.go", "sub/helper.go"},
			wantKeys: []string{"main.go", "sub/helper.go"},
		},
		{
			name:     "non-source file",
			patterns: []string{"data.json"},
			wantKeys: []string{"data.json"},
		},
		{
			name:     "directory as pattern",
			patterns: []string{"sub"},
			wantKeys: []string{"sub/helper.go", "sub/helper_test.go"},
		},
		{
			name:     "no match",
			patterns: []string{"nonexistent.go"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := expandAndReadFiles(tmpDir, tmpDir, tt.patterns)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(files) != len(tt.wantKeys) {
				t.Errorf("got %d files, want %d", len(files), len(tt.wantKeys))
			}

			for _, key := range tt.wantKeys {
				// Convert to native path separator for cross-platform compatibility
				nativeKey := filepath.FromSlash(key)
				if _, ok := files[nativeKey]; !ok {
					t.Errorf("missing expected file %q, got keys: %v", nativeKey, mapKeys(files))
				}
			}
		})
	}
}

func TestExpandAndReadFilesRecursive(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure
	files := map[string]string{
		"main.go":           "package main",
		"cmd/app/app.go":    "package app",
		"internal/util.go":  "package internal",
		"vendor/dep/dep.go": "package dep", // Should be skipped
		".git/config":       "[core]",      // Should be skipped
	}

	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	result, err := expandAndReadFiles(tmpDir, tmpDir, []string{"./..."})
	if err != nil {
		t.Fatalf("expandAndReadFiles error: %v", err)
	}

	// Should include main.go, cmd/app/app.go, internal/util.go
	// Should NOT include vendor/dep/dep.go or .git/config
	want := []string{"main.go", "cmd/app/app.go", "internal/util.go"}
	for _, w := range want {
		nativeW := filepath.FromSlash(w)
		if _, ok := result[nativeW]; !ok {
			t.Errorf("missing expected file %q", nativeW)
		}
	}

	noWant := []string{"vendor/dep/dep.go", ".git/config"}
	for _, nw := range noWant {
		nativeNW := filepath.FromSlash(nw)
		if _, ok := result[nativeNW]; ok {
			t.Errorf("should not include %q", nativeNW)
		}
	}
}

func TestExpandAndReadFiles_ShellExpanded(t *testing.T) {
	// Simulate shell-expanded globs: the shell expands *.go in a subdirectory
	// into individual relative file paths like "helper.go", "helper_test.go".
	// workDir is the subdirectory where the shell ran; repoRoot is the repo root.
	tmpDir := t.TempDir()

	writeFile := func(path, content string) {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeFile("main.go", "package main\n")
	writeFile("sub/helper.go", "package sub\n")
	writeFile("sub/helper_test.go", "package sub\n// test\n")

	repoRoot := tmpDir
	workDir := filepath.Join(tmpDir, "sub")

	// Shell in sub/ expands *.go → ["helper.go", "helper_test.go"]
	files, err := expandAndReadFiles(workDir, repoRoot, []string{"helper.go", "helper_test.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Keys should be relative to repoRoot
	wantKeys := []string{"sub/helper.go", "sub/helper_test.go"}
	if len(files) != len(wantKeys) {
		t.Fatalf("got %d files, want %d: %v", len(files), len(wantKeys), mapKeys(files))
	}
	for _, key := range wantKeys {
		nativeKey := filepath.FromSlash(key)
		if _, ok := files[nativeKey]; !ok {
			t.Errorf("missing expected file %q, got keys: %v", nativeKey, mapKeys(files))
		}
	}
}

func TestExpandAndReadFiles_RecursiveFromSubdir(t *testing.T) {
	// ./... should walk from repoRoot and return all source files,
	// even when workDir is a subdirectory.
	tmpDir := t.TempDir()

	writeFile := func(path, content string) {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeFile("main.go", "package main\n")
	writeFile("sub/helper.go", "package sub\n")

	repoRoot := tmpDir
	workDir := filepath.Join(tmpDir, "sub")

	files, err := expandAndReadFiles(workDir, repoRoot, []string{"./..."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find files across the entire repo, not just the subdirectory
	wantKeys := []string{"main.go", "sub/helper.go"}
	if len(files) != len(wantKeys) {
		t.Fatalf("got %d files, want %d: %v", len(files), len(wantKeys), mapKeys(files))
	}
	for _, key := range wantKeys {
		nativeKey := filepath.FromSlash(key)
		if _, ok := files[nativeKey]; !ok {
			t.Errorf("missing expected file %q, got keys: %v", nativeKey, mapKeys(files))
		}
	}
}

func TestIsSourceFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"script.py", true},
		{"app.js", true},
		{"app.ts", true},
		{"app.tsx", true},
		{"lib.rs", true},
		{"config.yaml", true},
		{"config.yml", true},
		{"data.json", true},
		{"readme.md", true},
		{"binary.exe", false},
		{"image.png", false},
		{"archive.tar.gz", false},
		{".gitignore", false},
		{"Makefile", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isSourceFile(tt.path)
			if got != tt.want {
				t.Errorf("isSourceFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestBuildFixPrompt(t *testing.T) {
	analysisType := &analyze.AnalysisType{
		Name:        "refactor",
		Description: "Suggest refactoring opportunities",
	}
	analysisOutput := `## CODE SMELLS
- Long function in main.go:50
- Duplicated logic in utils.go

## REFACTORING SUGGESTIONS
- Extract method for repeated code`

	prompt := buildFixPrompt(analysisType, analysisOutput)

	// Check that it includes the analysis type
	if !strings.Contains(prompt, "refactor") {
		t.Error("prompt should include analysis type name")
	}

	// Check that it includes the analysis output
	if !strings.Contains(prompt, "CODE SMELLS") {
		t.Error("prompt should include analysis output")
	}
	if !strings.Contains(prompt, "Long function in main.go") {
		t.Error("prompt should include specific findings")
	}

	// Check that it has fix instructions
	if !strings.Contains(prompt, "apply the suggested changes") {
		t.Error("prompt should include fix instructions")
	}

	// Check that it mentions verification steps
	if !strings.Contains(prompt, "compiles") || !strings.Contains(prompt, "tests") {
		t.Error("prompt should mention verification steps")
	}
}

func TestAnalyzeOptionsDefaults(t *testing.T) {
	opts := analyzeOptions{
		agentName: "gemini",
		fix:       true,
	}

	// fixAgent should default to agentName when empty
	fixAgent := opts.fixAgent
	if fixAgent == "" {
		fixAgent = opts.agentName
	}
	if fixAgent != "gemini" {
		t.Errorf("fixAgent should default to agentName, got %q", fixAgent)
	}

	// When fixAgent is set, it should be used
	opts.fixAgent = "claude-code"
	if opts.fixAgent != "claude-code" {
		t.Errorf("fixAgent should be 'claude-code', got %q", opts.fixAgent)
	}
}

func TestWaitForAnalysisJob(t *testing.T) {
	tests := []struct {
		name       string
		responses  []jobResponse // sequence of responses
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "immediate success",
			responses: []jobResponse{
				{status: "done", review: "Analysis complete: found 3 issues"},
			},
		},
		{
			name: "queued then done",
			responses: []jobResponse{
				{status: "queued"},
				{status: "running"},
				{status: "done", review: "All good"},
			},
		},
		{
			name: "job failed",
			responses: []jobResponse{
				{status: "failed", errMsg: "agent crashed"},
			},
			wantErr:    true,
			wantErrMsg: "agent crashed",
		},
		{
			name: "job canceled",
			responses: []jobResponse{
				{status: "canceled"},
			},
			wantErr:    true,
			wantErrMsg: "canceled",
		},
		{
			name: "job not found",
			responses: []jobResponse{
				{notFound: true},
			},
			wantErr:    true,
			wantErrMsg: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var callCount int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				idx := int(atomic.AddInt32(&callCount, 1)) - 1
				if idx >= len(tt.responses) {
					idx = len(tt.responses) - 1
				}
				resp := tt.responses[idx]

				switch {
				case strings.HasPrefix(r.URL.Path, "/api/jobs"):
					if resp.notFound {
						json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []interface{}{}})
						return
					}
					job := storage.ReviewJob{
						ID:     42,
						Status: storage.JobStatus(resp.status),
						Error:  resp.errMsg,
					}
					json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{job}})

				case strings.HasPrefix(r.URL.Path, "/api/review"):
					json.NewEncoder(w).Encode(storage.Review{
						JobID:  42,
						Output: resp.review,
					})
				}
			}))
			defer ts.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			review, err := waitForAnalysisJob(ctx, ts.URL, 42)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if review == nil {
				t.Fatal("expected review, got nil")
			}
			if review.Output != tt.responses[len(tt.responses)-1].review {
				t.Errorf("got review %q, want %q", review.Output, tt.responses[len(tt.responses)-1].review)
			}
		})
	}
}

func TestWaitForAnalysisJob_Timeout(t *testing.T) {
	// Test that context timeout is respected
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return "queued" to force polling
		job := storage.ReviewJob{
			ID:     42,
			Status: storage.JobStatusQueued,
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{job}})
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := waitForAnalysisJob(ctx, ts.URL, 42)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected context deadline error, got: %v", err)
	}
}

type jobResponse struct {
	status   string
	review   string
	errMsg   string
	notFound bool
}

func TestMarkJobAddressed(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"success", http.StatusOK, false},
		{"server error", http.StatusInternalServerError, true},
		{"not found", http.StatusNotFound, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotJobID int64
			var gotAddressed bool

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/review/address" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: %s", r.Method)
				}

				var req map[string]interface{}
				json.NewDecoder(r.Body).Decode(&req)
				gotJobID = int64(req["job_id"].(float64))
				gotAddressed = req["addressed"].(bool)

				w.WriteHeader(tt.statusCode)
			}))
			defer ts.Close()

			err := markJobAddressed(ts.URL, 123)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if gotJobID != 123 {
				t.Errorf("job_id = %d, want 123", gotJobID)
			}
			if !gotAddressed {
				t.Error("addressed should be true")
			}
		})
	}
}

func TestRunFixAgent(t *testing.T) {
	// Create a minimal repo for the test agent
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	// Use the built-in test agent
	err := runFixAgent(cmd, tmpDir, "test", "", "fast", "Fix the issues", false)
	if err != nil {
		t.Fatalf("runFixAgent failed: %v", err)
	}

	// Test agent should produce output
	if output.Len() == 0 {
		t.Error("expected output from test agent")
	}
}

func TestRunAnalyzeAndFix_Integration(t *testing.T) {
	// This tests the full workflow with mocked daemon and test agent

	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping integration test")
	}

	// Create a real git repo (needed for commit verification)
	tmpDir := t.TempDir()
	runGit := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		return cmd.Run()
	}

	if err := runGit("init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644)
	runGit("add", ".")
	if err := runGit("commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	var jobsCount, reviewCount, addressCount int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/enqueue" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(storage.ReviewJob{
				ID:     99,
				Agent:  "test",
				Status: storage.JobStatusQueued,
			})

		case r.URL.Path == "/api/jobs":
			count := atomic.AddInt32(&jobsCount, 1)
			status := storage.JobStatusQueued
			if count >= 2 {
				status = storage.JobStatusDone
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: status,
				}},
			})

		case r.URL.Path == "/api/review":
			atomic.AddInt32(&reviewCount, 1)
			json.NewEncoder(w).Encode(storage.Review{
				JobID:  99,
				Agent:  "test",
				Output: "## CODE SMELLS\n- Found duplicated code in main.go",
			})

		case r.URL.Path == "/api/review/address":
			atomic.AddInt32(&addressCount, 1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	analysisType := analyze.GetType("refactor")
	opts := analyzeOptions{
		agentName: "test",
		fix:       true,
		fixAgent:  "test",
		reasoning: "fast",
	}

	err := runAnalyzeAndFix(cmd, ts.URL, tmpDir, 99, analysisType, opts)
	if err != nil {
		t.Fatalf("runAnalyzeAndFix failed: %v", err)
	}

	// Verify the workflow was executed
	if atomic.LoadInt32(&jobsCount) < 2 {
		t.Error("should have polled for job status")
	}
	if atomic.LoadInt32(&reviewCount) == 0 {
		t.Error("should have fetched the review")
	}
	if atomic.LoadInt32(&addressCount) == 0 {
		t.Error("should have marked job as addressed")
	}

	// Verify output contains analysis result
	outputStr := output.String()
	if !strings.Contains(outputStr, "CODE SMELLS") {
		t.Error("output should contain analysis result")
	}
	if !strings.Contains(outputStr, "marked as addressed") {
		t.Error("output should confirm job was addressed")
	}
}

func TestBuildCommitPrompt(t *testing.T) {
	analysisType := &analyze.AnalysisType{
		Name:        "duplication",
		Description: "Find code duplication",
	}

	prompt := buildCommitPrompt(analysisType)

	// Should mention the analysis type
	if !strings.Contains(prompt, "duplication") {
		t.Error("prompt should reference the analysis type")
	}

	// Should have instructions for committing
	if !strings.Contains(prompt, "git commit") {
		t.Error("prompt should mention git commit")
	}

	// Should ask for a descriptive message
	if !strings.Contains(prompt, "descriptive") {
		t.Error("prompt should request a descriptive message")
	}
}

func TestListAnalysisTypes(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	err := listAnalysisTypes(cmd)
	if err != nil {
		t.Fatalf("listAnalysisTypes failed: %v", err)
	}

	outputStr := output.String()

	// Should have header
	if !strings.Contains(outputStr, "Available analysis types") {
		t.Error("output should contain header")
	}

	// Should list all types
	expectedTypes := []string{
		"test-fixtures",
		"duplication",
		"refactor",
		"complexity",
		"api-design",
		"dead-code",
		"architecture",
	}

	for _, typ := range expectedTypes {
		if !strings.Contains(outputStr, typ) {
			t.Errorf("output should contain %q", typ)
		}
	}
}

func TestShowAnalysisPrompt(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	err := showAnalysisPrompt(cmd, "test-fixtures")
	if err != nil {
		t.Fatalf("showAnalysisPrompt failed: %v", err)
	}

	outputStr := output.String()

	// Should have type name as header
	if !strings.Contains(outputStr, "# test-fixtures") {
		t.Error("output should contain type name header")
	}

	// Should have description
	if !strings.Contains(outputStr, "Description:") {
		t.Error("output should contain description")
	}

	// Should have prompt template section with content after it
	if !strings.Contains(outputStr, "## Prompt Template") {
		t.Error("output should contain prompt template section")
	}

	// Verify there's substantial content after the template header
	// (the prompt templates are all multi-line with instructions)
	idx := strings.Index(outputStr, "## Prompt Template")
	if idx >= 0 {
		afterHeader := outputStr[idx+len("## Prompt Template"):]
		if len(strings.TrimSpace(afterHeader)) < 50 {
			t.Error("prompt template section should have substantial content")
		}
	}
}

func TestShowAnalysisPromptUnknown(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	err := showAnalysisPrompt(cmd, "unknown-type")
	if err == nil {
		t.Error("expected error for unknown type")
	}
	if !strings.Contains(err.Error(), "unknown analysis type") {
		t.Errorf("error should mention 'unknown analysis type': %v", err)
	}
}

func TestShowPromptRequiresType(t *testing.T) {
	cmd := analyzeCmd()
	cmd.SetArgs([]string{"--show-prompt"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when --show-prompt used without type")
	}
	if !strings.Contains(err.Error(), "requires an analysis type") {
		t.Errorf("error should mention 'requires an analysis type', got: %v", err)
	}
}

func TestPerFileAnalysis(t *testing.T) {
	// Test that per-file mode creates multiple jobs
	var jobCount int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/enqueue" && r.Method == http.MethodPost {
			atomic.AddInt32(&jobCount, 1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(storage.ReviewJob{
				ID:     int64(atomic.LoadInt32(&jobCount)),
				Agent:  "test",
				Status: storage.JobStatusQueued,
			})
		}
	}))
	defer ts.Close()

	// Save and restore serverAddr
	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	// Create test files
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package a\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package b\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "c.go"), []byte("package c\n"), 0644)

	files, err := expandAndReadFiles(tmpDir, tmpDir, []string{"*.go"})
	if err != nil {
		t.Fatalf("expandAndReadFiles: %v", err)
	}

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	analysisType := analyze.GetType("refactor")
	err = runPerFileAnalysis(cmd, tmpDir, analysisType, files, analyzeOptions{quiet: false}, config.DefaultMaxPromptSize)
	if err != nil {
		t.Fatalf("runPerFileAnalysis: %v", err)
	}

	// Should have created 3 jobs (one per file)
	if atomic.LoadInt32(&jobCount) != 3 {
		t.Errorf("expected 3 jobs, got %d", atomic.LoadInt32(&jobCount))
	}

	// Output should mention all job IDs
	outputStr := output.String()
	if !strings.Contains(outputStr, "Created 3 jobs") {
		t.Error("output should mention 3 jobs created")
	}
}

func TestEnqueueAnalysisJob(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/enqueue" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)

		// Verify request - agentic is true so agent can read files when prompt is too large
		if req["agentic"] != true {
			t.Error("agentic should be true for analysis")
		}
		if req["custom_prompt"] == nil {
			t.Error("custom_prompt should be set")
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(storage.ReviewJob{
			ID:     42,
			Agent:  "test",
			Status: storage.JobStatusQueued,
		})
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	job, err := enqueueAnalysisJob("/repo", "test prompt", "", "test-fixtures", analyzeOptions{agentName: "test"})
	if err != nil {
		t.Fatalf("enqueueAnalysisJob: %v", err)
	}

	if job.ID != 42 {
		t.Errorf("job.ID = %d, want 42", job.ID)
	}
}

func TestAnalyzeJSONOutput(t *testing.T) {
	// Set up mock server
	var jobCounter int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/enqueue" && r.Method == http.MethodPost {
			jobCounter++
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(storage.ReviewJob{
				ID:     jobCounter,
				Agent:  "test-agent",
				Status: storage.JobStatusQueued,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	t.Run("single analysis JSON output", func(t *testing.T) {
		jobCounter = 0
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main"), 0644); err != nil {
			t.Fatal(err)
		}

		files := map[string]string{"test.go": "package main"}
		analysisType := analyze.GetType("refactor")

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		err := runSingleAnalysis(cmd, tmpDir, analysisType, files, analyzeOptions{jsonOutput: true}, config.DefaultMaxPromptSize)
		if err != nil {
			t.Fatalf("runSingleAnalysis: %v", err)
		}

		var result AnalyzeResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v\nOutput: %s", err, output.String())
		}

		if len(result.Jobs) != 1 {
			t.Errorf("expected 1 job, got %d", len(result.Jobs))
		}
		if result.Jobs[0].ID != 1 {
			t.Errorf("expected job ID 1, got %d", result.Jobs[0].ID)
		}
		if result.Jobs[0].Agent != "test-agent" {
			t.Errorf("expected agent 'test-agent', got %q", result.Jobs[0].Agent)
		}
		if result.AnalysisType != "refactor" {
			t.Errorf("expected analysis type 'refactor', got %q", result.AnalysisType)
		}
		if len(result.Files) != 1 || result.Files[0] != "test.go" {
			t.Errorf("expected files ['test.go'], got %v", result.Files)
		}
	})

	t.Run("per-file analysis JSON output", func(t *testing.T) {
		jobCounter = 0
		tmpDir := t.TempDir()
		files := map[string]string{
			"a.go": "package a",
			"b.go": "package b",
			"c.go": "package c",
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}

		analysisType := analyze.GetType("complexity")

		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)

		err := runPerFileAnalysis(cmd, tmpDir, analysisType, files, analyzeOptions{jsonOutput: true}, config.DefaultMaxPromptSize)
		if err != nil {
			t.Fatalf("runPerFileAnalysis: %v", err)
		}

		var result AnalyzeResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v\nOutput: %s", err, output.String())
		}

		if len(result.Jobs) != 3 {
			t.Errorf("expected 3 jobs, got %d", len(result.Jobs))
		}
		// Jobs should be in sorted file order
		expectedFiles := []string{"a.go", "b.go", "c.go"}
		for i, info := range result.Jobs {
			if info.File != expectedFiles[i] {
				t.Errorf("job %d: expected file %q, got %q", i, expectedFiles[i], info.File)
			}
			if info.ID != int64(i+1) {
				t.Errorf("job %d: expected ID %d, got %d", i, i+1, info.ID)
			}
		}
		if result.AnalysisType != "complexity" {
			t.Errorf("expected analysis type 'complexity', got %q", result.AnalysisType)
		}
		if len(result.Files) != 3 {
			t.Errorf("expected 3 files, got %d", len(result.Files))
		}
	})
}
