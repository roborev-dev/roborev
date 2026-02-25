package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
}

// setupGitRepo creates a minimal git repo with one commit and returns its path.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "test")
	writeTestFile(t, dir, "hello.txt", "hello")
	runGit(t, dir, "add", "hello.txt")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func TestCreateAndClose(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Worktree dir should exist and contain the file
	if _, err := os.Stat(filepath.Join(wt.Dir, "hello.txt")); err != nil {
		t.Fatalf("expected hello.txt in worktree: %v", err)
	}

	wtDir := wt.Dir
	wt.Close()

	// After Close, the directory should be removed
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be removed after Close, got: %v", err)
	}
}

func TestCapturePatchNoChanges(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer wt.Close()

	patch, err := wt.CapturePatch()
	if err != nil {
		t.Fatalf("CapturePatch failed: %v", err)
	}
	if patch != "" {
		t.Fatalf("expected empty patch for unchanged worktree, got %d bytes", len(patch))
	}
}

func TestCapturePatchWithChanges(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer wt.Close()

	// Modify a file and add a new one
	writeTestFile(t, wt.Dir, "hello.txt", "modified")
	writeTestFile(t, wt.Dir, "new.txt", "new file")

	patch, err := wt.CapturePatch()
	if err != nil {
		t.Fatalf("CapturePatch failed: %v", err)
	}
	if patch == "" {
		t.Fatal("expected non-empty patch")
	}
	if !strings.Contains(patch, "hello.txt") {
		t.Error("patch should reference hello.txt")
	}
	if !strings.Contains(patch, "new.txt") {
		t.Error("patch should reference new.txt")
	}
}

func TestCapturePatchCommittedChanges(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer wt.Close()

	// Simulate an agent that commits changes (instead of just staging)
	writeTestFile(t, wt.Dir, "hello.txt", "committed-change")
	runGit(t, wt.Dir, "add", "-A")
	runGit(t, wt.Dir, "commit", "-m", "agent commit")

	// CapturePatch should still capture the committed changes
	patch, err := wt.CapturePatch()
	if err != nil {
		t.Fatalf("CapturePatch failed: %v", err)
	}
	if patch == "" {
		t.Fatal("expected non-empty patch for committed changes")
	}
	if !strings.Contains(patch, "hello.txt") {
		t.Error("patch should reference hello.txt")
	}
	if !strings.Contains(patch, "committed-change") {
		t.Error("patch should contain the committed content")
	}
}

func TestApplyPatchEmpty(t *testing.T) {
	// Empty patch should be a no-op
	if err := ApplyPatch("/nonexistent", ""); err != nil {
		t.Fatalf("ApplyPatch with empty patch should succeed: %v", err)
	}
}

func TestApplyPatchRoundTrip(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Make changes in worktree
	writeTestFile(t, wt.Dir, "hello.txt", "changed")
	writeTestFile(t, wt.Dir, "added.txt", "added")

	patch, err := wt.CapturePatch()
	if err != nil {
		t.Fatalf("CapturePatch failed: %v", err)
	}
	wt.Close()

	// Apply the patch back to the original repo
	if err := ApplyPatch(repo, patch); err != nil {
		t.Fatalf("ApplyPatch failed: %v", err)
	}

	// Verify the changes were applied
	content, err := os.ReadFile(filepath.Join(repo, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read hello.txt: %v", err)
	}
	if string(content) != "changed" {
		t.Errorf("expected 'changed', got %q", content)
	}
	content, err = os.ReadFile(filepath.Join(repo, "added.txt"))
	if err != nil {
		t.Fatalf("failed to read added.txt: %v", err)
	}
	if string(content) != "added" {
		t.Errorf("expected 'added', got %q", content)
	}
}

func TestCheckPatchClean(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	writeTestFile(t, wt.Dir, "hello.txt", "changed")
	patch, err := wt.CapturePatch()
	if err != nil {
		t.Fatalf("CapturePatch failed: %v", err)
	}
	wt.Close()

	// Check should pass on unmodified repo
	if err := CheckPatch(repo, patch); err != nil {
		t.Fatalf("CheckPatch should succeed on clean repo: %v", err)
	}
}

func TestCheckPatchEmpty(t *testing.T) {
	if err := CheckPatch("/nonexistent", ""); err != nil {
		t.Fatalf("CheckPatch with empty patch should succeed: %v", err)
	}
}

func TestCheckPatchConflict(t *testing.T) {
	repo := setupGitRepo(t)

	// Create a patch that modifies hello.txt
	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	writeTestFile(t, wt.Dir, "hello.txt", "from-worktree")
	patch, err := wt.CapturePatch()
	if err != nil {
		t.Fatalf("CapturePatch failed: %v", err)
	}
	wt.Close()

	// Now modify hello.txt in the original repo to create a conflict
	writeTestFile(t, repo, "hello.txt", "conflicting-change")

	// CheckPatch should fail
	if err := CheckPatch(repo, patch); err == nil {
		t.Fatal("CheckPatch should fail when patch conflicts with working tree")
	}
}

func TestApplyPatchConflictFails(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	writeTestFile(t, wt.Dir, "hello.txt", "from-worktree")
	patch, err := wt.CapturePatch()
	if err != nil {
		t.Fatalf("CapturePatch failed: %v", err)
	}
	wt.Close()

	// Create a conflict
	writeTestFile(t, repo, "hello.txt", "different")

	// ApplyPatch should fail
	if err := ApplyPatch(repo, patch); err == nil {
		t.Fatal("ApplyPatch should fail when patch conflicts")
	}
}

func TestCreateEmptyRef(t *testing.T) {
	repo := setupGitRepo(t)
	_, err := Create(repo, "")
	if err == nil {
		t.Fatal("expected error for empty ref")
	}
	if !strings.Contains(err.Error(), "ref must not be empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCreateWithSpecificSHA(t *testing.T) {
	repo := setupGitRepo(t)

	// Record the SHA of the initial commit
	initialSHA := runGit(t, repo, "rev-parse", "HEAD")

	// Add a second commit that changes hello.txt
	writeTestFile(t, repo, "hello.txt", "updated")
	runGit(t, repo, "add", "hello.txt")
	runGit(t, repo, "commit", "-m", "second commit")

	// HEAD now has "updated", but create worktree at the initial SHA
	wt, err := Create(repo, initialSHA)
	if err != nil {
		t.Fatalf("Create with specific SHA failed: %v", err)
	}
	defer wt.Close()

	// Worktree should have the original content, not "updated"
	content, err := os.ReadFile(filepath.Join(wt.Dir, "hello.txt"))
	if err != nil {
		t.Fatalf("read hello.txt: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("expected 'hello' at initial SHA, got %q", content)
	}
}

func TestSubmoduleRequiresFileProtocol(t *testing.T) {
	tpl := `[submodule "test"]
	path = test
	%s = %s
`
	tests := []struct {
		name     string
		key      string
		url      string
		expected bool
	}{
		{name: "file-scheme", key: "url", url: "file:///tmp/repo", expected: true},
		{name: "file-scheme-quoted", key: "url", url: `"file:///tmp/repo"`, expected: true},
		{name: "file-scheme-mixed-case-key", key: "URL", url: "file:///tmp/repo", expected: true},
		{name: "file-single-slash", key: "url", url: "file:/tmp/repo", expected: true},
		{name: "unix-absolute", key: "url", url: "/tmp/repo", expected: true},
		{name: "relative-dot", key: "url", url: "./repo", expected: true},
		{name: "relative-dotdot", key: "url", url: "../repo", expected: true},
		{name: "windows-drive-slash", key: "url", url: "C:/repo", expected: true},
		{name: "windows-drive-backslash", key: "url", url: `C:\repo`, expected: true},
		{name: "windows-unc", key: "url", url: `\\server\share\repo`, expected: true},
		{name: "https", key: "url", url: "https://example.com/repo.git", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitmodules := ".gitmodules"
			writeTestFile(t, dir, gitmodules, string(fmt.Appendf(nil, tpl, tc.key, tc.url)))
			if got := submoduleRequiresFileProtocol(dir); got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestSubmoduleRequiresFileProtocolNested(t *testing.T) {
	tpl := `[submodule "test"]
	path = test
	url = %s
`
	dir := t.TempDir()
	writeTestFile(t, dir, ".gitmodules", string(fmt.Appendf(nil, tpl, "https://example.com/repo.git")))

	nestedPath := filepath.Join(dir, "sub", ".gitmodules")
	if err := os.MkdirAll(filepath.Dir(nestedPath), 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writeTestFile(t, dir, "sub/.gitmodules", string(fmt.Appendf(nil, tpl, "file:///tmp/repo")))

	if !submoduleRequiresFileProtocol(dir) {
		t.Fatalf("expected nested file URL to require file protocol")
	}
}
