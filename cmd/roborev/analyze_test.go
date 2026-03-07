package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/prompt/analyze"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
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

			assertFilesExist(t, files, tt.wantKeys)
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
	assertFilesExist(t, result, []string{"main.go", "cmd/app/app.go", "internal/util.go"})

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
	tmpDir := writeTestFiles(t, map[string]string{
		"main.go":            "package main\n",
		"sub/helper.go":      "package sub\n",
		"sub/helper_test.go": "package sub\n// test\n",
	})

	repoRoot := tmpDir
	workDir := filepath.Join(tmpDir, "sub")

	// Shell in sub/ expands *.go → ["helper.go", "helper_test.go"]
	files, err := expandAndReadFiles(workDir, repoRoot, []string{"helper.go", "helper_test.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Keys should be relative to repoRoot
	assertFilesExist(t, files, []string{"sub/helper.go", "sub/helper_test.go"})
}

func TestExpandAndReadFiles_RecursiveFromSubdir(t *testing.T) {
	// ./... should walk from repoRoot and return all source files,
	// even when workDir is a subdirectory.
	tmpDir := writeTestFiles(t, map[string]string{
		"main.go":       "package main\n",
		"sub/helper.go": "package sub\n",
	})

	repoRoot := tmpDir
	workDir := filepath.Join(tmpDir, "sub")

	files, err := expandAndReadFiles(workDir, repoRoot, []string{"./..."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find files across the entire repo, not just the subdirectory
	assertFilesExist(t, files, []string{"main.go", "sub/helper.go"})
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

	assertContains(t, prompt, "refactor", "prompt should include analysis type name")
	assertContains(t, prompt, "CODE SMELLS", "prompt should include analysis output")
	assertContains(t, prompt, "Long function in main.go", "prompt should include specific findings")
	assertContains(t, prompt, "apply the suggested changes", "prompt should include fix instructions")
	assertContains(t, prompt, "compiles", "prompt should mention verification steps")
	assertContains(t, prompt, "tests", "prompt should mention verification steps")
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
	// JobIDStart: 43 means the initial job ID (without enqueue) is 42.
	// DoneAfterPolls: 999 ensures it stays "queued" long enough for timeout.
	ts, _ := newMockServer(t, MockServerOpts{
		JobIDStart:     43,
		DoneAfterPolls: 999,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := waitForAnalysisJob(ctx, ts.URL, 42)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	assertContains(t, err.Error(), "context deadline exceeded", "expected context deadline error")
}

func TestMarkJobClosed(t *testing.T) {
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
			ts, _ := newMockServer(t, MockServerOpts{
				OnClose: func(w http.ResponseWriter, r *http.Request) {
					var req map[string]any
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Errorf("failed to decode body: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					gotJobID := int64(req["job_id"].(float64))
					gotClosed := req["closed"].(bool)

					if gotJobID != 123 {
						t.Errorf("job_id = %d, want %d", gotJobID, 123)
					}
					if !gotClosed {
						t.Error("closed should be true")
					}

					w.WriteHeader(tt.statusCode)
				},
			})

			err := markJobClosed(context.Background(), ts.URL, 123)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRunFixAgent(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cmd, output := newTestCmd(t, "")

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
	assertContains(t, prompt, "duplication", "prompt should reference the analysis type")

	// Should have instructions for committing
	assertContains(t, prompt, "git commit", "prompt should mention git commit")

	// Should ask for a descriptive message
	assertContains(t, prompt, "descriptive", "prompt should request a descriptive message")
}

func TestListAnalysisTypes(t *testing.T) {
	cmd, output := newTestCmd(t, "")

	err := listAnalysisTypes(cmd)
	if err != nil {
		t.Fatalf("listAnalysisTypes failed: %v", err)
	}

	outputStr := output.String()

	assertContains(t, outputStr, "Available analysis types", "output should contain header")

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
		assertContains(t, outputStr, typ, "output should contain analysis type")
	}
}

func TestShowAnalysisPrompt(t *testing.T) {
	cmd, output := newTestCmd(t, "")

	err := showAnalysisPrompt(cmd, "test-fixtures")
	if err != nil {
		t.Fatalf("showAnalysisPrompt failed: %v", err)
	}

	outputStr := output.String()

	assertContains(t, outputStr, "# test-fixtures", "output should contain type name header")
	assertContains(t, outputStr, "Description:", "output should contain description")
	assertContains(t, outputStr, "## Prompt Template", "output should contain prompt template section")

	// Verify there's substantial content after the template header
	// (the prompt templates are all multi-line with instructions)
	_, after, ok := strings.Cut(outputStr, "## Prompt Template")
	if ok {
		afterHeader := after
		if len(strings.TrimSpace(afterHeader)) < 50 {
			t.Error("prompt template section should have substantial content")
		}
	}
}

func TestShowAnalysisPromptUnknown(t *testing.T) {
	cmd, _ := newTestCmd(t, "")

	err := showAnalysisPrompt(cmd, "unknown-type")
	if err == nil {
		t.Error("expected error for unknown type")
	}
	assertContains(t, err.Error(), "unknown analysis type", "error should mention 'unknown analysis type'")
}

func TestShowPromptRequiresType(t *testing.T) {
	cmd := analyzeCmd()
	cmd.SetArgs([]string{"--show-prompt"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when --show-prompt used without type")
	}
	assertContains(t, err.Error(), "requires an analysis type", "error should mention 'requires an analysis type'")
}

func TestPerFileAnalysis(t *testing.T) {
	ts, state := newMockServer(t, MockServerOpts{})

	tmpDir := writeTestFiles(t, map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n",
		"c.go": "package c\n",
	})

	files, err := expandAndReadFiles(tmpDir, tmpDir, []string{"*.go"})
	if err != nil {
		t.Fatalf("expandAndReadFiles: %v", err)
	}

	cmd, output := newTestCmd(t, "")

	analysisType := analyze.GetType("refactor")
	err = runPerFileAnalysis(cmd, ts.URL, tmpDir, analysisType, files, analyzeOptions{quiet: false}, config.DefaultMaxPromptSize)
	if err != nil {
		t.Fatalf("runPerFileAnalysis: %v", err)
	}

	// Should have created one job per file
	expected := int32(len(files))
	if state.EnqueueCount.Load() != expected {
		t.Errorf("Expected %d enqueue, got %d", expected, state.EnqueueCount.Load())
	}

	// Output should mention all job IDs
	outputStr := output.String()
	assertContains(t, outputStr, "Created 3 jobs", "output should mention 3 jobs created")
}

func TestEnqueueAnalysisJob(t *testing.T) {
	ts, _ := newMockServer(t, MockServerOpts{
		JobIDStart: 42,
		OnEnqueue: func(w http.ResponseWriter, r *http.Request) {
			var req daemon.EnqueueRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			if req.Agentic != true {
				t.Error("agentic should be true for analysis")
			}
			if req.CustomPrompt == "" {
				t.Error("custom_prompt should be set")
			}

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(storage.ReviewJob{
				ID:     42,
				Agent:  "test",
				Status: storage.JobStatusQueued,
			})
		},
	})

	job, err := enqueueAnalysisJob(ts.URL, "/repo", "test prompt", "", "test-fixtures", analyzeOptions{agentName: "test"})
	if err != nil {
		t.Fatalf("enqueueAnalysisJob: %v", err)
	}

	if job.ID != 42 {
		t.Errorf("job.ID = %d, want 42", job.ID)
	}
}

func TestEnqueueAnalysisJobBranchName(t *testing.T) {
	// Create a real git repo so GetCurrentBranch returns a known value
	repo := newTestGitRepo(t)
	repo.Run("checkout", "-b", "test-current")
	repo.CommitFile("init.go", "package main", "initial")

	captureBranch := func(t *testing.T) (mock *httptest.Server, gotBranch *string) {
		t.Helper()
		var branch string
		ts, _ := newMockServer(t, MockServerOpts{
			OnEnqueue: func(w http.ResponseWriter, r *http.Request) {
				var req daemon.EnqueueRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				branch = req.Branch
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(storage.ReviewJob{ID: 1, Agent: "test", Status: storage.JobStatusQueued})
			},
		})
		return ts, &branch
	}

	t.Run("no branch flag uses current branch", func(t *testing.T) {
		ts, gotBranch := captureBranch(t)

		_, err := enqueueAnalysisJob(ts.URL, repo.Dir, "prompt", "", "refactor", analyzeOptions{})
		if err != nil {
			t.Fatalf("enqueueAnalysisJob: %v", err)
		}
		if *gotBranch != "test-current" {
			t.Errorf("expected branch 'test-current', got %q", *gotBranch)
		}
	})

	t.Run("branch=HEAD uses current branch", func(t *testing.T) {
		ts, gotBranch := captureBranch(t)

		_, err := enqueueAnalysisJob(ts.URL, repo.Dir, "prompt", "", "refactor", analyzeOptions{branch: "HEAD"})
		if err != nil {
			t.Fatalf("enqueueAnalysisJob: %v", err)
		}
		if *gotBranch != "test-current" {
			t.Errorf("expected branch 'test-current', got %q", *gotBranch)
		}
	})

	t.Run("named branch overrides current branch", func(t *testing.T) {
		ts, gotBranch := captureBranch(t)

		_, err := enqueueAnalysisJob(ts.URL, repo.Dir, "prompt", "", "refactor", analyzeOptions{branch: "feature-xyz"})
		if err != nil {
			t.Fatalf("enqueueAnalysisJob: %v", err)
		}
		if *gotBranch != "feature-xyz" {
			t.Errorf("expected branch 'feature-xyz', got %q", *gotBranch)
		}
	})
}

func setupBranchTestRepo(t *testing.T) *TestGitRepo {
	repo := newTestGitRepo(t)
	repo.Run("checkout", "-b", "main")
	repo.CommitFile("base.go", "package main", "base")

	repo.Run("checkout", "-b", "feature")
	repo.CommitFile("new.go", "package main\nfunc New() {}", "add go file")
	repo.CommitFile("docs.md", "# Docs", "add docs")
	repo.CommitFile("config.yml", "key: val", "add config")
	return repo
}

func TestGetBranchFiles(t *testing.T) {
	cmd, _ := newTestCmd(t, "")

	t.Run("filters to code files only", func(t *testing.T) {
		repo := setupBranchTestRepo(t)
		files, err := getBranchFiles(cmd, repo.Dir, analyzeOptions{
			branch:     "HEAD",
			baseBranch: "main",
		})
		if err != nil {
			t.Fatalf("getBranchFiles: %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("expected 1 code file, got %d: %v", len(files), testutil.MapKeys(files))
		}
		if _, ok := files["new.go"]; !ok {
			t.Errorf("expected new.go in results, got %v", testutil.MapKeys(files))
		}
	})

	t.Run("reads from git not working tree", func(t *testing.T) {
		repo := setupBranchTestRepo(t)
		// Modify working tree — should NOT affect branch analysis
		_ = os.WriteFile(filepath.Join(repo.Dir, "new.go"), []byte("DIRTY"), 0644)

		files, err := getBranchFiles(cmd, repo.Dir, analyzeOptions{
			branch:     "HEAD",
			baseBranch: "main",
		})
		if err != nil {
			t.Fatalf("getBranchFiles: %v", err)
		}
		content := files["new.go"]
		if strings.Contains(content, "DIRTY") {
			t.Error("getBranchFiles should read from git HEAD, not working tree")
		}
		if !strings.Contains(content, "func New()") {
			t.Errorf("expected committed content, got %q", content)
		}
	})

	t.Run("named branch reads from that branch", func(t *testing.T) {
		repo := setupBranchTestRepo(t)
		// Switch back to main, analyze feature branch by name
		repo.Run("checkout", "main")

		files, err := getBranchFiles(cmd, repo.Dir, analyzeOptions{
			branch:     "feature",
			baseBranch: "main",
		})
		if err != nil {
			t.Fatalf("getBranchFiles: %v", err)
		}
		if _, ok := files["new.go"]; !ok {
			t.Errorf("expected new.go from feature branch, got %v", testutil.MapKeys(files))
		}
	})

	t.Run("no code files returns error", func(t *testing.T) {
		repo := setupBranchTestRepo(t)
		repo.Run("checkout", "-b", "docs-only", "main")
		repo.CommitFile("readme.md", "# Hello", "add readme")

		_, err := getBranchFiles(cmd, repo.Dir, analyzeOptions{
			branch:     "HEAD",
			baseBranch: "main",
		})
		if err == nil {
			t.Fatal("expected error for no code files")
		}
		assertContains(t, err.Error(), "no code files changed", "unexpected error")
	})

	t.Run("on base branch returns error", func(t *testing.T) {
		repo := setupBranchTestRepo(t)
		repo.Run("checkout", "main")

		_, err := getBranchFiles(cmd, repo.Dir, analyzeOptions{
			branch:     "HEAD",
			baseBranch: "main",
		})
		if err == nil {
			t.Fatal("expected error when on base branch")
		}
		assertContains(t, err.Error(), "already on main", "unexpected error")
	})
}

func TestAnalyzeBranchSpaceSeparated(t *testing.T) {
	// Verify that `--branch feature-xyz` (space-separated) gives a helpful error
	// because NoOptDefVal causes "feature-xyz" to become a positional arg
	cmd := analyzeCmd()
	cmd.SetArgs([]string{"refactor", "--branch", "feature-xyz"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err.Error(), "--branch=<name>", "error should suggest --branch=<name> syntax")
}

func TestAnalyzeJSONOutput(t *testing.T) {
	t.Run("single analysis JSON output", func(t *testing.T) {
		ts, _ := newMockServer(t, MockServerOpts{Agent: "test-agent"})

		tmpDir := writeTestFiles(t, map[string]string{"test.go": "package main"})

		files := map[string]string{"test.go": "package main"}
		analysisType := analyze.GetType("refactor")

		cmd, output := newTestCmd(t, "")

		err := runSingleAnalysis(cmd, ts.URL, tmpDir, analysisType, files, analyzeOptions{jsonOutput: true}, config.DefaultMaxPromptSize)
		if err != nil {
			t.Fatalf("runSingleAnalysis: %v", err)
		}

		var result AnalyzeResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v\nOutput: %s", err, output.String())
		}

		want := AnalyzeResult{
			AnalysisType: "refactor",
			Files:        []string{"test.go"},
			Jobs:         []AnalyzeJobInfo{{ID: 1, Agent: "test-agent"}},
		}
		assertEqual(t, want, result, "AnalyzeResult mismatch")
	})

	t.Run("per-file analysis JSON output", func(t *testing.T) {
		ts, _ := newMockServer(t, MockServerOpts{Agent: "test-agent"})

		files := map[string]string{
			"a.go": "package a",
			"b.go": "package b",
			"c.go": "package c",
		}
		tmpDir := writeTestFiles(t, files)

		analysisType := analyze.GetType("complexity")

		cmd, output := newTestCmd(t, "")

		err := runPerFileAnalysis(cmd, ts.URL, tmpDir, analysisType, files, analyzeOptions{jsonOutput: true}, config.DefaultMaxPromptSize)
		if err != nil {
			t.Fatalf("runPerFileAnalysis: %v", err)
		}

		var result AnalyzeResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v\nOutput: %s", err, output.String())
		}

		want := AnalyzeResult{
			AnalysisType: "complexity",
			Files:        []string{"a.go", "b.go", "c.go"},
			Jobs: []AnalyzeJobInfo{
				{ID: 1, File: "a.go", Agent: "test-agent"},
				{ID: 2, File: "b.go", Agent: "test-agent"},
				{ID: 3, File: "c.go", Agent: "test-agent"},
			},
		}
		assertEqual(t, want, result, "AnalyzeResult mismatch")
	})
}

func TestIsCodeFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Code files
		{"main.go", true},
		{"script.py", true},
		{"app.js", true},
		{"app.ts", true},
		{"app.tsx", true},
		{"lib.rs", true},
		{"main.c", true},
		{"main.cpp", true},
		{"App.java", true},
		{"run.sh", true},
		{"query.sql", true},
		{"schema.proto", true},

		// Non-code files (excluded from branch analysis)
		{"README.md", false},
		{"notes.txt", false},
		{"config.yml", false},
		{"config.yaml", false},
		{"data.json", false},
		{"pyproject.toml", false},
		{"index.html", false},
		{"style.css", false},
		{"style.scss", false},

		// Not source at all
		{"image.png", false},
		{"binary.exe", false},
		{".gitignore", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isCodeFile(tt.path)
			if got != tt.want {
				t.Errorf("isCodeFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestAnalyzeBranchFlagValidation(t *testing.T) {
	t.Run("branch requires analysis type", func(t *testing.T) {
		cmd := analyzeCmd()
		cmd.SetArgs([]string{"--branch"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "--branch requires an analysis type", "unexpected error")
	})

	t.Run("branch cannot be combined with file patterns", func(t *testing.T) {
		cmd := analyzeCmd()
		cmd.SetArgs([]string{"--branch", "refactor", "*.go"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "cannot specify file patterns with --branch", "unexpected error")
	})

	t.Run("branch with only analysis type is accepted by arg validation", func(t *testing.T) {
		// Validate args directly instead of cmd.Execute() which would
		// try to connect to a daemon and waste ~3s on timeout.
		cmd := analyzeCmd()
		cmd.SetArgs([]string{"--branch", "refactor"})
		// ParseFlags + ValidateArgs exercises the Args validator without RunE
		if err := cmd.ParseFlags([]string{"--branch"}); err != nil {
			t.Fatalf("parse flags: %v", err)
		}
		if err := cmd.ValidateArgs([]string{"refactor"}); err != nil {
			t.Errorf("arg validation should pass with --branch and analysis type, got: %v", err)
		}
	})
}

func assertFilesExist(t *testing.T, files map[string]string, wantKeys []string) {
	t.Helper()
	if len(files) != len(wantKeys) {
		t.Errorf("got %d files, want %d: %v", len(files), len(wantKeys), testutil.MapKeys(files))
	}
	for _, key := range wantKeys {
		nativeKey := filepath.FromSlash(key)
		if _, ok := files[nativeKey]; !ok {
			t.Errorf("missing expected file %q, got keys: %v", nativeKey, testutil.MapKeys(files))
		}
	}
}
