package worktree

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err, "git %v failed: %v\n%s", args, err, out)
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		require.NoError(t, err, "failed to write %s: %v", name, err)
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
	require.NoError(t, err, "Create failed: %v", err)

	// Worktree dir should exist and contain the file
	if _, err := os.Stat(filepath.Join(wt.Dir, "hello.txt")); err != nil {
		require.NoError(t, err, "expected hello.txt in worktree: %v", err)
	}

	wtDir := wt.Dir
	wt.Close()

	// After Close, the directory should be removed
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		require.NoError(t, err, "worktree dir should be removed after Close, got: %v", err)
	}
}

func TestCapturePatchNoChanges(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	require.NoError(t, err, "Create failed: %v", err)
	defer wt.Close()

	patch, err := wt.CapturePatch()
	require.NoError(t, err, "CapturePatch failed: %v", err)
	if patch != "" {
		require.NoError(t, err, "expected empty patch for unchanged worktree, got %d bytes", len(patch))
	}
}

func TestCapturePatchWithChanges(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	require.NoError(t, err, "Create failed: %v", err)
	defer wt.Close()

	// Modify a file and add a new one
	writeTestFile(t, wt.Dir, "hello.txt", "modified")
	writeTestFile(t, wt.Dir, "new.txt", "new file")

	patch, err := wt.CapturePatch()
	require.NoError(t, err, "CapturePatch failed: %v", err)
	if patch == "" {
		require.NoError(t, err)
	}
	assert.Contains(t, patch, "hello.txt", "patch should reference hello.txt")
	assert.Contains(t, patch, "new.txt", "patch should reference new.txt")
}

func TestCapturePatchCommittedChanges(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	require.NoError(t, err, "Create failed: %v", err)
	defer wt.Close()

	// Simulate an agent that commits changes (instead of just staging)
	writeTestFile(t, wt.Dir, "hello.txt", "committed-change")
	runGit(t, wt.Dir, "add", "-A")
	runGit(t, wt.Dir, "commit", "-m", "agent commit")

	// CapturePatch should still capture the committed changes
	patch, err := wt.CapturePatch()
	require.NoError(t, err, "CapturePatch failed: %v", err)
	if patch == "" {
		require.NoError(t, err)
	}
	assert.Contains(t, patch, "hello.txt", "patch should reference hello.txt")
	assert.Contains(t, patch, "committed-change", "patch should contain the committed content")
}

func TestApplyPatchEmpty(t *testing.T) {
	// Empty patch should be a no-op
	if err := ApplyPatch("/nonexistent", ""); err != nil {
		require.NoError(t, err, "ApplyPatch with empty patch should succeed: %v", err)
	}
}

func TestApplyPatchRoundTrip(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	require.NoError(t, err, "Create failed: %v", err)

	// Make changes in worktree
	writeTestFile(t, wt.Dir, "hello.txt", "changed")
	writeTestFile(t, wt.Dir, "added.txt", "added")

	patch, err := wt.CapturePatch()
	require.NoError(t, err, "CapturePatch failed: %v", err)
	wt.Close()

	// Apply the patch back to the original repo
	if err := ApplyPatch(repo, patch); err != nil {
		require.NoError(t, err, "ApplyPatch failed: %v", err)
	}

	// Verify the changes were applied
	content, err := os.ReadFile(filepath.Join(repo, "hello.txt"))
	require.NoError(t, err, "failed to read hello.txt: %v", err)
	if string(content) != "changed" {
		assert.Equalf(t, "changed", string(content), "expected 'changed', got %q", string(content))
	}
	content, err = os.ReadFile(filepath.Join(repo, "added.txt"))
	require.NoError(t, err, "failed to read added.txt: %v", err)
	assert.Equalf(t, "added", string(content), "expected 'added', got %q", string(content))
}

func TestCheckPatchClean(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	require.NoError(t, err, "Create failed: %v", err)
	writeTestFile(t, wt.Dir, "hello.txt", "changed")
	patch, err := wt.CapturePatch()
	require.NoError(t, err, "CapturePatch failed: %v", err)
	wt.Close()

	// Check should pass on unmodified repo
	if err := CheckPatch(repo, patch); err != nil {
		require.NoError(t, err, "CheckPatch should succeed on clean repo: %v", err)
	}
}

func TestCheckPatchEmpty(t *testing.T) {
	if err := CheckPatch("/nonexistent", ""); err != nil {
		require.NoError(t, err, "CheckPatch with empty patch should succeed: %v", err)
	}
}

func TestCheckPatchConflict(t *testing.T) {
	repo := setupGitRepo(t)

	// Create a patch that modifies hello.txt
	wt, err := Create(repo, "HEAD")
	require.NoError(t, err, "Create failed: %v", err)
	writeTestFile(t, wt.Dir, "hello.txt", "from-worktree")
	patch, err := wt.CapturePatch()
	require.NoError(t, err, "CapturePatch failed: %v", err)
	wt.Close()

	// Now modify hello.txt in the original repo to create a conflict
	writeTestFile(t, repo, "hello.txt", "conflicting-change")

	// CheckPatch should fail
	if err := CheckPatch(repo, patch); err == nil {
		require.NoError(t, err)
	}
}

func TestApplyPatchConflictFails(t *testing.T) {
	repo := setupGitRepo(t)

	wt, err := Create(repo, "HEAD")
	require.NoError(t, err, "Create failed: %v", err)
	writeTestFile(t, wt.Dir, "hello.txt", "from-worktree")
	patch, err := wt.CapturePatch()
	require.NoError(t, err, "CapturePatch failed: %v", err)
	wt.Close()

	// Create a conflict
	writeTestFile(t, repo, "hello.txt", "different")

	// ApplyPatch should fail
	if err := ApplyPatch(repo, patch); err == nil {
		require.NoError(t, err)
	}
}

func TestCreateEmptyRef(t *testing.T) {
	repo := setupGitRepo(t)
	_, err := Create(repo, "")
	require.Error(t, err)
	assert.ErrorContains(t, err, "ref must not be empty")
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
	require.NoError(t, err, "Create with specific SHA failed: %v", err)
	defer wt.Close()

	// Worktree should have the original content, not "updated"
	content, err := os.ReadFile(filepath.Join(wt.Dir, "hello.txt"))
	require.NoError(t, err, "read hello.txt: %v", err)
	assert.Equal(t, "hello", string(content), "expected 'hello' at initial SHA, got %q", string(content))
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
			require.Equal(t, tc.expected, submoduleRequiresFileProtocol(dir), "expected %v", tc.expected)
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
		require.NoError(t, err, "mkdir nested: %v", err)
	}
	writeTestFile(t, dir, "sub/.gitmodules", string(fmt.Appendf(nil, tpl, "file:///tmp/repo")))

	require.True(t, submoduleRequiresFileProtocol(dir), "expected nested file URL to require file protocol")
}
