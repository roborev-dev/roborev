package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/prompt/analyze"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestExpandAndReadFiles(t *testing.T) {
	tmpDir := writeTestFiles(t, map[string]string{
		"main.go":            "package main\n",
		"util.go":            "package main\n// util\n",
		"sub/helper.go":      "package sub\n",
		"sub/helper_test.go": "package sub\n// test\n",
		"data.json":          `{"key": "value"}`,
		"README.md":          "# README\n",
		".hidden/secret.go":  "package hidden\n",
	})

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
	tmpDir := writeTestFiles(t, map[string]string{
		"main.go":           "package main",
		"cmd/app/app.go":    "package app",
		"internal/util.go":  "package internal",
		"vendor/dep/dep.go": "package dep", // Should be skipped
		".git/config":       "[core]",      // Should be skipped
	})

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
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)

	cmd, output := newTestCmd(t)

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
	cmd, output := newTestCmd(t)

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
	cmd, output := newTestCmd(t)

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
	cmd, _ := newTestCmd(t)

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
	ts, state := newMockServer(t, MockServerOpts{})
	patchServerAddr(t, ts.URL)

	tmpDir := writeTestFiles(t, map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n",
		"c.go": "package c\n",
	})

	files, err := expandAndReadFiles(tmpDir, tmpDir, []string{"*.go"})
	if err != nil {
		t.Fatalf("expandAndReadFiles: %v", err)
	}

	cmd, output := newTestCmd(t)

	analysisType := analyze.GetType("refactor")
	err = runPerFileAnalysis(cmd, tmpDir, analysisType, files, analyzeOptions{quiet: false}, config.DefaultMaxPromptSize)
	if err != nil {
		t.Fatalf("runPerFileAnalysis: %v", err)
	}

	// Should have created 3 jobs (one per file)
	if atomic.LoadInt32(&state.EnqueueCount) != 3 {
		t.Errorf("expected 3 jobs, got %d", atomic.LoadInt32(&state.EnqueueCount))
	}

	// Output should mention all job IDs
	outputStr := output.String()
	if !strings.Contains(outputStr, "Created 3 jobs") {
		t.Error("output should mention 3 jobs created")
	}
}

func TestEnqueueAnalysisJob(t *testing.T) {
	ts, _ := newMockServer(t, MockServerOpts{
		JobIDStart: 42,
		OnEnqueue: func(w http.ResponseWriter, r *http.Request) {
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)

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
		},
	})
	patchServerAddr(t, ts.URL)

	job, err := enqueueAnalysisJob("/repo", "test prompt", "", "test-fixtures", analyzeOptions{agentName: "test"})
	if err != nil {
		t.Fatalf("enqueueAnalysisJob: %v", err)
	}

	if job.ID != 42 {
		t.Errorf("job.ID = %d, want 42", job.ID)
	}
}

func TestAnalyzeJSONOutput(t *testing.T) {
	t.Run("single analysis JSON output", func(t *testing.T) {
		ts, _ := newMockServer(t, MockServerOpts{Agent: "test-agent"})
		patchServerAddr(t, ts.URL)
		tmpDir := writeTestFiles(t, map[string]string{"test.go": "package main"})

		files := map[string]string{"test.go": "package main"}
		analysisType := analyze.GetType("refactor")

		cmd, output := newTestCmd(t)

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
		ts, _ := newMockServer(t, MockServerOpts{Agent: "test-agent"})
		patchServerAddr(t, ts.URL)
		files := map[string]string{
			"a.go": "package a",
			"b.go": "package b",
			"c.go": "package c",
		}
		tmpDir := writeTestFiles(t, files)

		analysisType := analyze.GetType("complexity")

		cmd, output := newTestCmd(t)

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
