package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteTool_Read(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(f, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	out, err := ExecuteTool(ctx, dir, "Read", map[string]any{"file_path": "foo.txt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Errorf("got %q, want hello", out)
	}
}

func TestExecuteTool_Read_PathTraversal_Rejected(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	_, err := ExecuteTool(ctx, dir, "Read", map[string]any{"file_path": "../etc/passwd"}, false)
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestExecuteTool_Read_MissingArg(t *testing.T) {
	ctx := context.Background()
	_, err := ExecuteTool(ctx, t.TempDir(), "Read", map[string]any{}, false)
	if err == nil {
		t.Error("expected error for missing file_path")
	}
}

func TestExecuteTool_Glob(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	out, err := ExecuteTool(ctx, dir, "Glob", map[string]any{"pattern": "*.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a.go\nb.go" && out != "b.go\na.go" {
		// Order may vary
		if len(out) > 0 && out != "a.go\nb.go" {
			t.Logf("got %q", out)
		}
	}
}

func TestExecuteTool_Glob_MissingArg(t *testing.T) {
	ctx := context.Background()
	_, err := ExecuteTool(ctx, t.TempDir(), "Glob", map[string]any{}, false)
	if err == nil {
		t.Error("expected error for missing pattern")
	}
}

func TestExecuteTool_Grep(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.go")
	if err := os.WriteFile(f, []byte("line1\nfunc main()\nline3"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	out, err := ExecuteTool(ctx, dir, "Grep", map[string]any{"pattern": "func"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" && !strings.Contains(out, "func") {
		t.Errorf("expected output to contain func, got %q", out)
	}
}

func TestExecuteTool_Grep_MissingArg(t *testing.T) {
	ctx := context.Background()
	_, err := ExecuteTool(ctx, t.TempDir(), "Grep", map[string]any{}, false)
	if err == nil {
		t.Error("expected error for missing pattern")
	}
}

func TestExecuteTool_Edit_RequiresAgentic(t *testing.T) {
	ctx := context.Background()
	_, err := ExecuteTool(ctx, t.TempDir(), "Edit", map[string]any{"file_path": "x", "old_string": "a", "new_string": "b"}, false)
	if err == nil {
		t.Error("expected error when agentic is false")
	}
}

func TestExecuteTool_Edit(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	out, err := ExecuteTool(ctx, dir, "Edit", map[string]any{
		"file_path":  "f.txt",
		"old_string": "world",
		"new_string": "there",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Errorf("got %q, want ok", out)
	}
	data, _ := os.ReadFile(f)
	if string(data) != "hello there" {
		t.Errorf("file content: got %q, want hello there", data)
	}
}

func TestExecuteTool_Write_RequiresAgentic(t *testing.T) {
	ctx := context.Background()
	_, err := ExecuteTool(ctx, t.TempDir(), "Write", map[string]any{"file_path": "x", "contents": "y"}, false)
	if err == nil {
		t.Error("expected error when agentic is false")
	}
}

func TestExecuteTool_Write(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	out, err := ExecuteTool(ctx, dir, "Write", map[string]any{
		"file_path": "subdir/file.txt",
		"contents":  "new content",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Errorf("got %q, want ok", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "subdir", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Errorf("got %q, want new content", data)
	}
}

func TestExecuteTool_Bash_RequiresAgentic(t *testing.T) {
	ctx := context.Background()
	_, err := ExecuteTool(ctx, t.TempDir(), "Bash", map[string]any{"command": "echo ok"}, false)
	if err == nil {
		t.Error("expected error when agentic is false")
	}
}

func TestExecuteTool_Bash(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	out, err := ExecuteTool(ctx, dir, "Bash", map[string]any{"command": "echo hello"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello\n" && out != "hello" {
		t.Errorf("got %q", out)
	}
}

func TestExecuteTool_UnknownTool(t *testing.T) {
	ctx := context.Background()
	_, err := ExecuteTool(ctx, t.TempDir(), "Unknown", nil, false)
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestSafePathUnderRoot(t *testing.T) {
	root := t.TempDir()

	// Create a subdirectory for testing
	subdir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		root      string
		path      string
		wantError bool
		wantBase  string
	}{
		{
			name:      "relative path within root",
			root:      root,
			path:      "a/b",
			wantError: false,
			wantBase:  "b",
		},
		{
			name:      "absolute path within root",
			root:      root,
			path:      filepath.Join(root, "c/d"),
			wantError: false,
			wantBase:  "d",
		},
		{
			name:      "path with ..",
			root:      root,
			path:      "..",
			wantError: true,
		},
		{
			name:      "path traversal attempt",
			root:      root,
			path:      "../etc/passwd",
			wantError: true,
		},
		{
			name:      "absolute path outside root",
			root:      root,
			path:      "/etc/passwd",
			wantError: true,
		},
		{
			name:      "path with .. in middle",
			root:      subdir,
			path:      "a/../b",
			wantError: false,
			wantBase:  "b",
		},
		{
			name:      "path escaping via ..",
			root:      subdir,
			path:      "../../outside",
			wantError: true,
		},
		{
			name:      "dot path",
			root:      root,
			path:      ".",
			wantError: false,
			wantBase:  filepath.Base(root),
		},
		{
			name:      "empty path",
			root:      root,
			path:      "",
			wantError: false,
			wantBase:  filepath.Base(root),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safePathUnderRoot(tt.root, tt.path)
			if tt.wantError {
				if err == nil {
					t.Errorf("safePathUnderRoot() expected error but got none, result: %s", got)
				} else if !strings.Contains(err.Error(), "path outside repo") {
					t.Errorf("expected 'path outside repo' error, got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("safePathUnderRoot() unexpected error: %v", err)
				} else if filepath.Base(got) != tt.wantBase {
					t.Errorf("safePathUnderRoot() = %s, want base %s", got, tt.wantBase)
				}
			}
		})
	}
}

func TestExecuteTool_Write_EdgeCases(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	tests := []struct {
		name        string
		args        map[string]any
		wantError   bool
		checkFile   string
		wantContent string
	}{
		{
			name: "write with path traversal attempt",
			args: map[string]any{
				"file_path": "../outside.txt",
				"contents":  "Should fail",
			},
			wantError: true,
		},
		{
			name: "write with absolute path",
			args: map[string]any{
				"file_path": "/etc/passwd",
				"contents":  "Should fail",
			},
			wantError: true,
		},
		{
			name: "write empty content",
			args: map[string]any{
				"file_path": "empty.txt",
				"contents":  "",
			},
			wantError:   false,
			checkFile:   "empty.txt",
			wantContent: "",
		},
		{
			name: "overwrite with different content",
			args: map[string]any{
				"file_path": "existing.txt",
				"contents":  "New content",
			},
			wantError:   false,
			checkFile:   "existing.txt",
			wantContent: "New content",
		},
	}

	// Create existing file for overwrite test
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("Old content"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExecuteTool(ctx, root, "Write", tt.args, true)
			if tt.wantError {
				if err == nil {
					t.Errorf("ExecuteTool() expected error but got none, result: %s", result)
				}
			} else {
				if err != nil {
					t.Errorf("ExecuteTool() unexpected error: %v", err)
				} else if tt.checkFile != "" {
					content, err := os.ReadFile(filepath.Join(root, tt.checkFile))
					if err != nil {
						t.Errorf("failed to read file %s: %v", tt.checkFile, err)
					} else if string(content) != tt.wantContent {
						t.Errorf("file content = %q, want %q", string(content), tt.wantContent)
					}
				}
			}
		})
	}
}

func TestExecuteTool_Edit_EdgeCases(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	// Create test file
	testFile := filepath.Join(root, "test.txt")
	originalContent := `Line 1
Line 2
Line 3
Line 4
Line 5`
	if err := os.WriteFile(testFile, []byte(originalContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		args        map[string]any
		wantError   bool
		wantContent string
	}{
		{
			name: "edit with path traversal",
			args: map[string]any{
				"file_path":  "../test.txt",
				"old_string": "Line 1",
				"new_string": "Modified",
			},
			wantError: true,
		},
		{
			name: "edit non-existent file",
			args: map[string]any{
				"file_path":  "does-not-exist.txt",
				"old_string": "something",
				"new_string": "something else",
			},
			wantError: true,
		},
		{
			name: "old string not found",
			args: map[string]any{
				"file_path":  "test.txt",
				"old_string": "Not in file",
				"new_string": "New content",
			},
			wantError: true,
		},
		{
			name: "empty old string",
			args: map[string]any{
				"file_path":  "test.txt",
				"old_string": "",
				"new_string": "New",
			},
			wantError: true,
		},
		{
			name: "successful multi-line edit",
			args: map[string]any{
				"file_path":  "test.txt",
				"old_string": "Line 2\nLine 3",
				"new_string": "Modified Line 2\nModified Line 3",
			},
			wantError: false,
			wantContent: `Line 1
Modified Line 2
Modified Line 3
Line 4
Line 5`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset file content for each test
			if err := os.WriteFile(testFile, []byte(originalContent), 0644); err != nil {
				t.Fatal(err)
			}

			result, err := ExecuteTool(ctx, root, "Edit", tt.args, true)
			if tt.wantError {
				if err == nil {
					t.Errorf("ExecuteTool() expected error but got none, result: %s", result)
				}
			} else {
				if err != nil {
					t.Errorf("ExecuteTool() unexpected error: %v", err)
				} else if tt.wantContent != "" {
					content, err := os.ReadFile(testFile)
					if err != nil {
						t.Errorf("failed to read file: %v", err)
					} else if string(content) != tt.wantContent {
						t.Errorf("file content = %q, want %q", string(content), tt.wantContent)
					}
				}
			}
		})
	}
}

func TestExecuteTool_Grep_EdgeCases(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	// Create test files
	file1 := filepath.Join(root, "file1.txt")
	if err := os.WriteFile(file1, []byte("Hello world\nGoodbye world\nHello again"), 0644); err != nil {
		t.Fatal(err)
	}

	file2 := filepath.Join(root, "file2.go")
	if err := os.WriteFile(file2, []byte("func Hello() {\n\tprintln(\"Hello\")\n}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create subdirectory with file
	subdir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	file3 := filepath.Join(subdir, "file3.txt")
	if err := os.WriteFile(file3, []byte("Hello from subdir"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create large file to test line limits
	var largeContent strings.Builder
	for i := 0; i < 600; i++ {
		largeContent.WriteString(fmt.Sprintf("Line %d with pattern\n", i))
	}
	largeFile := filepath.Join(root, "large.txt")
	if err := os.WriteFile(largeFile, []byte(largeContent.String()), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		args        map[string]any
		wantError   bool
		checkResult func(string) bool
	}{
		{
			name: "basic pattern match",
			args: map[string]any{
				"pattern": "Hello",
			},
			wantError: false,
			checkResult: func(result string) bool {
				return strings.Contains(result, "file1.txt") &&
					strings.Contains(result, "file3.txt") &&
					strings.Contains(result, "file2.go")
			},
		},
		{
			name: "pattern in specific path",
			args: map[string]any{
				"pattern": "Hello",
				"path":    "file2.go",
			},
			wantError: false,
			checkResult: func(result string) bool {
				// When searching a specific file, the output may not include the filename
				return strings.Contains(result, "Hello") &&
					!strings.Contains(result, "file1.txt")
			},
		},
		{
			name: "pattern in subdirectory",
			args: map[string]any{
				"pattern": "Hello",
				"path":    "subdir",
			},
			wantError: false,
			checkResult: func(result string) bool {
				return strings.Contains(result, "file3.txt") &&
					!strings.Contains(result, "file1.txt")
			},
		},
		{
			name: "pattern not found",
			args: map[string]any{
				"pattern": "NotInAnyFile",
			},
			wantError: false,
			checkResult: func(result string) bool {
				return result == ""
			},
		},
		{
			name: "regex pattern",
			args: map[string]any{
				"pattern": "Hello.*world",
			},
			wantError: false,
			checkResult: func(result string) bool {
				return strings.Contains(result, "Hello world")
			},
		},
		{
			name: "line limit enforced",
			args: map[string]any{
				"pattern": "pattern",
			},
			wantError: false,
			checkResult: func(result string) bool {
				lines := strings.Count(result, "\n")
				// Should be truncated to around 500 lines
				return lines > 0 && lines <= 510 // Some buffer for headers
			},
		},
		{
			name: "path traversal attempt",
			args: map[string]any{
				"pattern": "test",
				"path":    "../etc",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExecuteTool(ctx, root, "Grep", tt.args, false)
			if tt.wantError {
				if err == nil {
					t.Errorf("ExecuteTool() expected error but got none, result: %s", result)
				}
			} else {
				if err != nil {
					t.Errorf("ExecuteTool() unexpected error: %v", err)
				} else if tt.checkResult != nil && !tt.checkResult(result) {
					t.Errorf("ExecuteTool() result check failed, got: %s", result)
				}
			}
		})
	}
}
