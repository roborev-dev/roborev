package analyze

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGetType(t *testing.T) {
	tests := []struct {
		name     string
		want     string
		wantNil  bool
	}{
		{"test-fixtures", "test-fixtures", false},
		{"duplication", "duplication", false},
		{"refactor", "refactor", false},
		{"complexity", "complexity", false},
		{"api-design", "api-design", false},
		{"dead-code", "dead-code", false},
		{"architecture", "architecture", false},
		{"invalid", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetType(tt.name)
			if tt.wantNil {
				if got != nil {
					t.Errorf("GetType(%q) = %v, want nil", tt.name, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("GetType(%q) = nil, want %q", tt.name, tt.want)
			}
			if got.Name != tt.want {
				t.Errorf("GetType(%q).Name = %q, want %q", tt.name, got.Name, tt.want)
			}
		})
	}
}

func TestAllTypesHavePrompts(t *testing.T) {
	for _, at := range AllTypes {
		t.Run(at.Name, func(t *testing.T) {
			prompt, err := at.GetPrompt()
			if err != nil {
				t.Fatalf("GetPrompt() error = %v", err)
			}
			if prompt == "" {
				t.Error("GetPrompt() returned empty string")
			}
			if len(prompt) < 50 {
				t.Errorf("GetPrompt() returned suspiciously short prompt: %q", prompt)
			}
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	files := map[string]string{
		"b.go": "package b\n",
		"a.go": "package a\n",
	}

	prompt, err := TestFixtures.BuildPrompt(files)
	if err != nil {
		t.Fatalf("BuildPrompt() error = %v", err)
	}

	// Check header
	if !strings.Contains(prompt, "## Analysis Request") {
		t.Error("prompt missing '## Analysis Request' header")
	}
	if !strings.Contains(prompt, "**Type:** test-fixtures") {
		t.Error("prompt missing type")
	}

	// Check files are listed in sorted order
	if !strings.Contains(prompt, "**Files:** a.go, b.go") {
		t.Error("prompt should list files in sorted order")
	}

	// Check file contents appear
	if !strings.Contains(prompt, "### a.go") {
		t.Error("prompt missing a.go section")
	}
	if !strings.Contains(prompt, "### b.go") {
		t.Error("prompt missing b.go section")
	}
	if !strings.Contains(prompt, "package a") {
		t.Error("prompt missing a.go content")
	}
	if !strings.Contains(prompt, "package b") {
		t.Error("prompt missing b.go content")
	}

	// Check instructions section
	if !strings.Contains(prompt, "## Instructions") {
		t.Error("prompt missing '## Instructions' header")
	}

	// Verify a.go comes before b.go in the content (sorted order)
	aIdx := strings.Index(prompt, "### a.go")
	bIdx := strings.Index(prompt, "### b.go")
	if aIdx > bIdx {
		t.Error("files should be in sorted order (a.go before b.go)")
	}
}

func TestBuildPromptAddsMissingNewline(t *testing.T) {
	files := map[string]string{
		"test.go": "package test", // No trailing newline
	}

	prompt, err := TestFixtures.BuildPrompt(files)
	if err != nil {
		t.Fatalf("BuildPrompt() error = %v", err)
	}

	// Should have added a newline before the closing ```
	if !strings.Contains(prompt, "package test\n```") {
		t.Error("prompt should add missing newline before closing code fence")
	}
}

func TestBuildPromptEmptyFiles(t *testing.T) {
	files := map[string]string{}

	prompt, err := TestFixtures.BuildPrompt(files)
	if err != nil {
		t.Fatalf("BuildPrompt() error = %v", err)
	}

	// Should still have headers
	if !strings.Contains(prompt, "## Analysis Request") {
		t.Error("prompt missing header")
	}
	if !strings.Contains(prompt, "**Files:** \n") {
		t.Error("prompt should have empty files list")
	}
}

func TestBuildPromptWithPaths(t *testing.T) {
	repoRoot := t.TempDir()
	filePaths := []string{
		filepath.Join(repoRoot, "b.go"),
		filepath.Join(repoRoot, "a.go"),
	}

	prompt, err := TestFixtures.BuildPromptWithPaths(repoRoot, filePaths)
	if err != nil {
		t.Fatalf("BuildPromptWithPaths() error = %v", err)
	}

	// Check header
	if !strings.Contains(prompt, "## Analysis Request") {
		t.Error("prompt missing '## Analysis Request' header")
	}
	if !strings.Contains(prompt, "**Type:** test-fixtures") {
		t.Error("prompt missing type")
	}
	if !strings.Contains(prompt, "**Repository:** "+repoRoot) {
		t.Error("prompt missing repository path")
	}
	// Files header should list paths (sorted)
	expectedFilesHeader := "**Files:** " + filepath.Join(repoRoot, "a.go") + ", " + filepath.Join(repoRoot, "b.go")
	if !strings.Contains(prompt, expectedFilesHeader) {
		t.Errorf("prompt missing expected files header, want %q", expectedFilesHeader)
	}

	// Check file paths are listed (should be sorted)
	if !strings.Contains(prompt, "- `"+filepath.Join(repoRoot, "a.go")+"`") {
		t.Error("prompt missing a.go path")
	}
	if !strings.Contains(prompt, "- `"+filepath.Join(repoRoot, "b.go")+"`") {
		t.Error("prompt missing b.go path")
	}

	// Check paths are in sorted order
	aIdx := strings.Index(prompt, "a.go")
	bIdx := strings.Index(prompt, "b.go")
	if aIdx > bIdx {
		t.Error("paths should be in sorted order (a.go before b.go)")
	}

	// Check for the "too large" message
	if !strings.Contains(prompt, "too large to embed") {
		t.Error("prompt missing 'too large' explanation")
	}

	// Check instructions section
	if !strings.Contains(prompt, "## Instructions") {
		t.Error("prompt missing '## Instructions' header")
	}
}
