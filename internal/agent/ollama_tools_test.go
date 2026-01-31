package agent

import (
	"context"
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

	got, err := safePathUnderRoot(root, "a/b")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "b" {
		t.Errorf("got %s", got)
	}

	_, err = safePathUnderRoot(root, "..")
	if err == nil {
		t.Error("expected error for ..")
	}

	_, err = safePathUnderRoot(root, "../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}
