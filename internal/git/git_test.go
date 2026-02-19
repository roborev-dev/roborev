package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRepo wraps a temporary git repository for testing.
type TestRepo struct {
	T   *testing.T
	Dir string
}

// NewTestRepo creates a temp dir, initializes git, and configures user identity.
func NewTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	return NewTestRepoWithAuthor(t, "Test")
}

// NewTestRepoWithAuthor creates a test repo with a custom author name.
func NewTestRepoWithAuthor(t *testing.T, author string) *TestRepo {
	t.Helper()
	dir := t.TempDir()
	r := &TestRepo{T: t, Dir: dir}
	r.Run("init")
	r.Run("config", "user.email", "test@test.com")
	r.Run("config", "user.name", author)
	return r
}

// NewBareTestRepo creates a bare git repository.
func NewBareTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	dir := t.TempDir()
	r := &TestRepo{T: t, Dir: dir}
	r.Run("init", "--bare")
	return r
}

// Run executes a git command in the repo and fails the test on error.
func (r *TestRepo) Run(args ...string) string {
	r.T.Helper()
	return runGit(r.T, r.Dir, args...)
}

// CommitFile writes a file and commits it.
func (r *TestRepo) CommitFile(filename, content, msg string) {
	r.T.Helper()
	r.WriteFile(filename, content)
	r.Run("add", filename)
	r.Run("commit", "-m", msg)
}

// CommitAll stages all changes and commits with the given message.
func (r *TestRepo) CommitAll(msg string) {
	r.T.Helper()
	r.Run("add", ".")
	r.Run("commit", "-m", msg)
}

// WriteFile writes a file without committing it.
func (r *TestRepo) WriteFile(filename, content string) {
	r.T.Helper()
	path := filepath.Join(r.Dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		r.T.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		r.T.Fatal(err)
	}
}

// HeadSHA returns the SHA of HEAD.
func (r *TestRepo) HeadSHA() string {
	r.T.Helper()
	return r.Run("rev-parse", "HEAD")
}

// AddWorktree creates a worktree on a new branch and returns a TestRepo for it.
func (r *TestRepo) AddWorktree(branchName string) *TestRepo {
	r.T.Helper()
	wtDir := r.T.TempDir()
	r.Run("worktree", "add", wtDir, "-b", branchName)
	r.T.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", wtDir)
		cmd.Dir = r.Dir
		_ = cmd.Run() // best-effort cleanup; not worth failing the test
	})
	return &TestRepo{T: r.T, Dir: wtDir}
}

// InstallHook writes a shell script as the named git hook
// (e.g. "pre-commit") and makes it executable.
func (r *TestRepo) InstallHook(name, script string) {
	r.T.Helper()
	hooksDir := filepath.Join(r.Dir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		r.T.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, name)
	if err := os.WriteFile(hookPath, []byte(script), 0755); err != nil {
		r.T.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestIsUnbornHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("true for empty repo", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init")
		if !IsUnbornHead(dir) {
			t.Error("expected IsUnbornHead=true for empty repo")
		}
	})

	t.Run("false after first commit", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init")
		runGit(t, dir, "config", "user.email", "test@test.com")
		runGit(t, dir, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-m", "init")
		if IsUnbornHead(dir) {
			t.Error("expected IsUnbornHead=false after commit")
		}
	})

	t.Run("false for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		if IsUnbornHead(dir) {
			t.Error("expected IsUnbornHead=false for non-git dir")
		}
	})

	t.Run("false for corrupt ref", func(t *testing.T) {
		// Simulate a repo where HEAD's target branch exists but points to
		// a missing object — this is NOT unborn, it's corrupt.
		dir := t.TempDir()
		runGit(t, dir, "init")
		runGit(t, dir, "config", "user.email", "test@test.com")
		runGit(t, dir, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-m", "init")

		// Corrupt the branch ref by writing a bogus SHA.
		// Read the actual HEAD target to avoid hardcoding main/master.
		headRef := strings.TrimSpace(runGit(t, dir, "symbolic-ref", "HEAD"))
		refPath := filepath.Join(dir, ".git", headRef)
		if err := os.WriteFile(refPath, []byte("0000000000000000000000000000000000000000\n"), 0644); err != nil {
			t.Fatal(err)
		}

		if IsUnbornHead(dir) {
			t.Error("expected IsUnbornHead=false for corrupt ref (ref exists but object is missing)")
		}
	})
}

func TestNormalizeMSYSPath(t *testing.T) {
	expectedCUsers := "/c/Users/test"
	expectedCapCUsers := "/C/Users/test"
	expectedUnix := "/home/user/repo"

	if runtime.GOOS == "windows" {
		expectedCUsers = "C:\\Users\\test"
		expectedCapCUsers = "C:\\Users\\test"
		expectedUnix = "\\home\\user\\repo"
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"forward slash path", "C:/Users/test", "C:" + string(filepath.Separator) + "Users" + string(filepath.Separator) + "test"},
		{"MSYS lowercase drive", "/c/Users/test", expectedCUsers},
		{"MSYS uppercase drive", "/C/Users/test", expectedCapCUsers},
		{"Unix absolute path", "/home/user/repo", expectedUnix},
		{"relative path", "some/path", "some" + string(filepath.Separator) + "path"},
		{"with trailing newline", "C:/Users/test\n", "C:" + string(filepath.Separator) + "Users" + string(filepath.Separator) + "test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeMSYSPath(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestGetHooksPath(t *testing.T) {
	t.Run("default hooks path", func(t *testing.T) {
		repo := NewTestRepo(t)

		hooksPath, err := GetHooksPath(repo.Dir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		if !filepath.IsAbs(hooksPath) {
			t.Errorf("hooks path should be absolute, got: %s", hooksPath)
		}

		cleanPath := filepath.Clean(hooksPath)
		expectedSuffix := filepath.Join(".git", "hooks")
		if !strings.HasSuffix(cleanPath, expectedSuffix) {
			t.Errorf("hooks path should end with %s, got: %s", expectedSuffix, cleanPath)
		}

		rel, err := filepath.Rel(repo.Dir, hooksPath)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			t.Errorf("hooks path should be under %s, got: %s", repo.Dir, hooksPath)
		}
	})

	t.Run("custom core.hooksPath absolute", func(t *testing.T) {
		repo := NewTestRepo(t)
		customHooksDir := filepath.Join(repo.Dir, "my-hooks")
		if err := os.MkdirAll(customHooksDir, 0755); err != nil {
			t.Fatal(err)
		}

		repo.Run("config", "core.hooksPath", customHooksDir)

		hooksPath, err := GetHooksPath(repo.Dir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		if hooksPath != customHooksDir {
			t.Errorf("expected %s, got %s", customHooksDir, hooksPath)
		}
	})

	t.Run("custom core.hooksPath relative", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.Run("config", "core.hooksPath", "custom-hooks")

		hooksPath, err := GetHooksPath(repo.Dir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		if !filepath.IsAbs(hooksPath) {
			t.Errorf("hooks path should be absolute, got: %s", hooksPath)
		}

		expected := filepath.Join(repo.Dir, "custom-hooks")
		if hooksPath != expected {
			t.Errorf("expected %s, got %s", expected, hooksPath)
		}
	})
}

func TestIsRebaseInProgress(t *testing.T) {
	t.Run("no rebase", func(t *testing.T) {
		repo := NewTestRepo(t)
		if IsRebaseInProgress(repo.Dir) {
			t.Error("expected no rebase in progress")
		}
	})

	t.Run("rebase-merge directory", func(t *testing.T) {
		repo := NewTestRepo(t)
		rebaseMerge := filepath.Join(repo.Dir, ".git", "rebase-merge")
		if err := os.MkdirAll(rebaseMerge, 0755); err != nil {
			t.Fatal(err)
		}
		// No defer needed; t.TempDir() handles cleanup automatically

		if !IsRebaseInProgress(repo.Dir) {
			t.Error("expected rebase in progress with rebase-merge")
		}
	})

	t.Run("rebase-apply directory", func(t *testing.T) {
		repo := NewTestRepo(t)
		rebaseApply := filepath.Join(repo.Dir, ".git", "rebase-apply")
		if err := os.MkdirAll(rebaseApply, 0755); err != nil {
			t.Fatal(err)
		}

		if !IsRebaseInProgress(repo.Dir) {
			t.Error("expected rebase in progress with rebase-apply")
		}
	})

	t.Run("non-repo returns false", func(t *testing.T) {
		nonRepo := t.TempDir()
		if IsRebaseInProgress(nonRepo) {
			t.Error("expected false for non-repo")
		}
	})

	t.Run("worktree with rebase", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("file.txt", "content", "initial")

		wt := repo.AddWorktree("test-branch")

		// Verify worktree has .git file (not directory)
		gitPath := filepath.Join(wt.Dir, ".git")
		info, err := os.Stat(gitPath)
		if err != nil {
			t.Fatalf("worktree .git not found: %v", err)
		}
		if info.IsDir() {
			t.Skip("worktree has .git directory instead of file - older git version")
		}

		// No rebase in worktree
		if IsRebaseInProgress(wt.Dir) {
			t.Error("expected no rebase in worktree")
		}

		// Get the actual gitdir for the worktree to simulate rebase
		gitDirCmd := exec.Command("git", "-C", wt.Dir, "rev-parse", "--git-dir")
		gitDirOut, err := gitDirCmd.Output()
		if err != nil {
			t.Fatalf("git rev-parse --git-dir failed: %v", err)
		}
		worktreeGitDir := strings.TrimSpace(string(gitDirOut))
		if !filepath.IsAbs(worktreeGitDir) {
			worktreeGitDir = filepath.Join(wt.Dir, worktreeGitDir)
		}

		// Simulate rebase in worktree
		rebaseMerge := filepath.Join(worktreeGitDir, "rebase-merge")
		if err := os.MkdirAll(rebaseMerge, 0755); err != nil {
			t.Fatal(err)
		}

		if !IsRebaseInProgress(wt.Dir) {
			t.Error("expected rebase in progress in worktree")
		}
	})
}

func TestGetCommitInfo(t *testing.T) {
	repo := NewTestRepoWithAuthor(t, "Test Author")

	t.Run("commit with subject only", func(t *testing.T) {
		repo.CommitFile("file1.txt", "content", "Simple subject")

		commitSHA := repo.HeadSHA()

		info, err := GetCommitInfo(repo.Dir, commitSHA)
		if err != nil {
			t.Fatalf("GetCommitInfo failed: %v", err)
		}

		if info.Subject != "Simple subject" {
			t.Errorf("expected subject 'Simple subject', got '%s'", info.Subject)
		}
		if info.Body != "" {
			t.Errorf("expected empty body, got '%s'", info.Body)
		}
		if info.Author != "Test Author" {
			t.Errorf("expected author 'Test Author', got '%s'", info.Author)
		}
	})

	t.Run("commit with subject and body", func(t *testing.T) {
		repo.WriteFile("file2.txt", "content2")
		repo.Run("add", ".")

		commitMsg := "Subject line\n\nThis is the body.\nIt has multiple lines.\n\nAnd paragraphs."
		cmd := exec.Command("git", "-C", repo.Dir, "commit", "-m", commitMsg)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit failed: %v\n%s", err, out)
		}

		commitSHA := repo.HeadSHA()

		info, err := GetCommitInfo(repo.Dir, commitSHA)
		if err != nil {
			t.Fatalf("GetCommitInfo failed: %v", err)
		}

		if info.Subject != "Subject line" {
			t.Errorf("expected subject 'Subject line', got '%s'", info.Subject)
		}
		if !strings.Contains(info.Body, "This is the body") {
			t.Errorf("expected body to contain 'This is the body', got '%s'", info.Body)
		}
		if !strings.Contains(info.Body, "multiple lines") {
			t.Errorf("expected body to contain 'multiple lines', got '%s'", info.Body)
		}
	})

	t.Run("commit with pipe in message", func(t *testing.T) {
		repo.WriteFile("file3.txt", "content3")
		repo.Run("add", ".")

		commitMsg := "Fix bug | important\n\nDetails: foo | bar | baz"
		cmd := exec.Command("git", "-C", repo.Dir, "commit", "-m", commitMsg)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit failed: %v\n%s", err, out)
		}

		commitSHA := repo.HeadSHA()

		info, err := GetCommitInfo(repo.Dir, commitSHA)
		if err != nil {
			t.Fatalf("GetCommitInfo failed: %v", err)
		}

		if !strings.Contains(info.Subject, "|") {
			t.Errorf("expected subject to contain pipe, got '%s'", info.Subject)
		}
		if !strings.Contains(info.Body, "foo | bar") {
			t.Errorf("expected body to contain 'foo | bar', got '%s'", info.Body)
		}
	})
}

func TestGetBranchName(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("file.txt", "content", "initial")

	commitSHA := repo.HeadSHA()
	expectedBranch := repo.Run("rev-parse", "--abbrev-ref", "HEAD")

	t.Run("valid commit on branch", func(t *testing.T) {
		branch := GetBranchName(repo.Dir, commitSHA)
		if branch != expectedBranch {
			t.Errorf("expected %s, got %s", expectedBranch, branch)
		}
	})

	t.Run("commit behind branch head", func(t *testing.T) {
		repo.CommitFile("file2.txt", "content2", "second")

		branch := GetBranchName(repo.Dir, commitSHA)
		if branch != expectedBranch {
			t.Errorf("expected %s (suffix stripped), got %s", expectedBranch, branch)
		}
	})

	t.Run("non-existent repo returns empty", func(t *testing.T) {
		nonRepo := t.TempDir()
		branch := GetBranchName(nonRepo, commitSHA)
		if branch != "" {
			t.Errorf("expected empty string, got %s", branch)
		}
	})

	t.Run("invalid SHA returns empty", func(t *testing.T) {
		branch := GetBranchName(repo.Dir, "0000000000000000000000000000000000000000")
		if branch != "" {
			t.Errorf("expected empty string, got %s", branch)
		}
	})
}

func TestGetCurrentBranch(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("file.txt", "content", "initial")

	t.Run("returns current branch", func(t *testing.T) {
		expectedBranch := repo.Run("rev-parse", "--abbrev-ref", "HEAD")

		branch := GetCurrentBranch(repo.Dir)
		if branch != expectedBranch {
			t.Errorf("expected %s, got %s", expectedBranch, branch)
		}
	})

	t.Run("returns branch after checkout", func(t *testing.T) {
		repo.Run("checkout", "-b", "feature-branch")

		branch := GetCurrentBranch(repo.Dir)
		if branch != "feature-branch" {
			t.Errorf("expected 'feature-branch', got %s", branch)
		}
	})

	t.Run("returns empty for detached HEAD", func(t *testing.T) {
		sha := repo.HeadSHA()
		repo.Run("checkout", sha)

		branch := GetCurrentBranch(repo.Dir)
		if branch != "" {
			t.Errorf("expected empty string for detached HEAD, got %s", branch)
		}
	})

	t.Run("returns empty for non-repo", func(t *testing.T) {
		nonRepo := t.TempDir()
		branch := GetCurrentBranch(nonRepo)
		if branch != "" {
			t.Errorf("expected empty string for non-repo, got %s", branch)
		}
	})
}

func TestHasUncommittedChanges(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("file.txt", "initial", "initial")

	t.Run("no changes", func(t *testing.T) {
		hasChanges, err := HasUncommittedChanges(repo.Dir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if hasChanges {
			t.Error("expected no uncommitted changes")
		}
	})

	t.Run("staged changes", func(t *testing.T) {
		repo.WriteFile("file.txt", "modified")
		repo.Run("add", ".")
		defer func() {
			repo.Run("checkout", "file.txt")
		}()

		hasChanges, err := HasUncommittedChanges(repo.Dir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if !hasChanges {
			t.Error("expected uncommitted changes for staged file")
		}
	})

	t.Run("unstaged changes", func(t *testing.T) {
		repo.WriteFile("file.txt", "unstaged")
		defer func() {
			repo.Run("checkout", "file.txt")
		}()

		hasChanges, err := HasUncommittedChanges(repo.Dir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if !hasChanges {
			t.Error("expected uncommitted changes for unstaged file")
		}
	})

	t.Run("untracked file", func(t *testing.T) {
		repo.WriteFile("untracked.txt", "new")
		defer os.Remove(filepath.Join(repo.Dir, "untracked.txt"))

		hasChanges, err := HasUncommittedChanges(repo.Dir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if !hasChanges {
			t.Error("expected uncommitted changes for untracked file")
		}
	})
}

func TestGetDirtyDiff(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("file.txt", "initial\n", "initial")

	t.Run("includes tracked file changes", func(t *testing.T) {
		repo.WriteFile("file.txt", "modified\n")
		defer func() {
			repo.Run("checkout", "file.txt")
		}()

		diff, err := GetDirtyDiff(repo.Dir)
		if err != nil {
			t.Fatalf("GetDirtyDiff failed: %v", err)
		}
		if !strings.Contains(diff, "file.txt") {
			t.Error("expected diff to contain file.txt")
		}
		if !strings.Contains(diff, "+modified") {
			t.Error("expected diff to contain +modified")
		}
	})

	t.Run("includes untracked files", func(t *testing.T) {
		repo.WriteFile("newfile.txt", "new content\n")
		defer os.Remove(filepath.Join(repo.Dir, "newfile.txt"))

		diff, err := GetDirtyDiff(repo.Dir)
		if err != nil {
			t.Fatalf("GetDirtyDiff failed: %v", err)
		}
		if !strings.Contains(diff, "newfile.txt") {
			t.Error("expected diff to contain newfile.txt")
		}
		if !strings.Contains(diff, "+new content") {
			t.Error("expected diff to contain +new content")
		}
		if !strings.Contains(diff, "new file mode") {
			t.Error("expected diff to contain 'new file mode' header")
		}
	})

	t.Run("includes both tracked and untracked", func(t *testing.T) {
		repo.WriteFile("file.txt", "changed\n")
		repo.WriteFile("another.txt", "another\n")
		defer func() {
			repo.Run("checkout", "file.txt")
			os.Remove(filepath.Join(repo.Dir, "another.txt"))
		}()

		diff, err := GetDirtyDiff(repo.Dir)
		if err != nil {
			t.Fatalf("GetDirtyDiff failed: %v", err)
		}
		if !strings.Contains(diff, "file.txt") {
			t.Error("expected diff to contain file.txt")
		}
		if !strings.Contains(diff, "another.txt") {
			t.Error("expected diff to contain another.txt")
		}
	})

	t.Run("handles binary files", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(repo.Dir, "binary.bin"), []byte("hello\x00world"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(repo.Dir, "binary.bin"))

		diff, err := GetDirtyDiff(repo.Dir)
		if err != nil {
			t.Fatalf("GetDirtyDiff failed: %v", err)
		}
		if !strings.Contains(diff, "binary.bin") {
			t.Error("expected diff to contain binary.bin")
		}
		if !strings.Contains(diff, "Binary file") {
			t.Error("expected diff to indicate binary file")
		}
	})
}

func TestGetDirtyDiffNoCommits(t *testing.T) {
	repo := NewTestRepo(t)

	// Add a file but don't commit
	repo.WriteFile("newfile.txt", "content\n")
	repo.Run("add", ".")

	// Also create an untracked file
	repo.WriteFile("untracked.txt", "untracked\n")

	diff, err := GetDirtyDiff(repo.Dir)
	if err != nil {
		t.Fatalf("GetDirtyDiff failed on repo with no commits: %v", err)
	}

	if !strings.Contains(diff, "newfile.txt") {
		t.Error("expected diff to contain newfile.txt (staged)")
	}

	if !strings.Contains(diff, "untracked.txt") {
		t.Error("expected diff to contain untracked.txt")
	}
}

func TestGetDirtyDiffStagedThenDeleted(t *testing.T) {
	repo := NewTestRepo(t)

	// Create and stage a file
	repo.WriteFile("staged.txt", "staged content\n")
	repo.Run("add", "staged.txt")

	// Delete the file from the working tree (but keep it staged)
	if err := os.Remove(filepath.Join(repo.Dir, "staged.txt")); err != nil {
		t.Fatal(err)
	}

	diff, err := GetDirtyDiff(repo.Dir)
	if err != nil {
		t.Fatalf("GetDirtyDiff failed: %v", err)
	}

	if !strings.Contains(diff, "staged.txt") {
		t.Error("expected diff to contain staged.txt (staged but deleted from working tree)")
	}
	if !strings.Contains(diff, "staged content") {
		t.Error("expected diff to contain staged file content")
	}
}

func TestIsExcludedFile(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		excluded bool
	}{
		// Lock files should be excluded
		{"uv.lock at root", "uv.lock", true},
		{"package-lock.json at root", "package-lock.json", true},
		{"yarn.lock at root", "yarn.lock", true},
		{"cargo.lock lowercase", "cargo.lock", true},
		{"Cargo.lock uppercase", "Cargo.lock", true}, // Rust uses capital C
		{"go.sum at root", "go.sum", true},

		// .beads directory should be excluded (including nested)
		{".beads file", ".beads/issues.md", true},
		{".beads nested", ".beads/foo/bar.md", true},
		{".beads deeply nested", ".beads/a/b/c/d.md", true},

		// Lock files in subdirs should also be excluded (git pathspec matches anywhere)
		{"uv.lock in subdir", "vendor/uv.lock", true},
		{"nested cargo.lock", "subdir/cargo.lock", true},
		{"nested Cargo.lock uppercase", "rust-crate/Cargo.lock", true},

		// Normal files should NOT be excluded
		{"go source file", "main.go", false},
		{"nested source file", "internal/git/git.go", false},
		{"readme", "README.md", false},
		{"similar but not lock", "package.json", false},
		{"lock in name but not exact", "mylock.lock", false},

		// Directories named like lockfiles should not be excluded
		{"uv.lock directory contents", "uv.lock/readme.md", false},
		{"go.sum directory contents", "go.sum/checksums.txt", false},

		// Files with similar prefixes should NOT be excluded
		{".beadsnotes.md", ".beadsnotes.md", false}, // Not in .beads directory
		{".beads-backup", ".beads-backup", false},   // Not in .beads directory
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isExcludedFile(tt.file)
			if got != tt.excluded {
				t.Errorf("isExcludedFile(%q) = %v, want %v", tt.file, got, tt.excluded)
			}
		})
	}
}

func TestGetDiffExcludesGeneratedFiles(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial content", "initial")

	repo.WriteFile(".beads/notes.md", "beads\n")
	repo.WriteFile("uv.lock", "lock\n")
	repo.WriteFile("go.sum", "sum\n")
	repo.WriteFile("keep.txt", "keep\n")

	repo.CommitAll("add files")

	sha := repo.HeadSHA()

	assertExcluded := func(t *testing.T, diff string) {
		t.Helper()
		if !strings.Contains(diff, "keep.txt") {
			t.Error("expected diff to contain keep.txt")
		}
		if strings.Contains(diff, "uv.lock") {
			t.Error("expected diff to exclude uv.lock")
		}
		if strings.Contains(diff, "go.sum") {
			t.Error("expected diff to exclude go.sum")
		}
		if strings.Contains(diff, ".beads/") {
			t.Error("expected diff to exclude .beads contents")
		}
	}

	t.Run("GetDiff", func(t *testing.T) {
		diff, err := GetDiff(repo.Dir, sha)
		if err != nil {
			t.Fatalf("GetDiff failed: %v", err)
		}
		assertExcluded(t, diff)
	})

	t.Run("GetRangeDiff", func(t *testing.T) {
		diff, err := GetRangeDiff(repo.Dir, "HEAD~1..HEAD")
		if err != nil {
			t.Fatalf("GetRangeDiff failed: %v", err)
		}
		assertExcluded(t, diff)
	})
}

func TestIsWorkingTreeClean(t *testing.T) {
	t.Run("clean tree returns true", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("initial.txt", "initial content", "initial")

		if !IsWorkingTreeClean(repo.Dir) {
			t.Error("expected clean tree to return true")
		}
	})

	t.Run("dirty tree with modified file returns false", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("initial.txt", "initial content", "initial")

		repo.WriteFile("initial.txt", "modified")

		if IsWorkingTreeClean(repo.Dir) {
			t.Error("expected dirty tree with modified file to return false")
		}
	})

	t.Run("dirty tree with untracked file returns false", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("initial.txt", "initial content", "initial")

		repo.WriteFile("untracked.txt", "untracked")

		if IsWorkingTreeClean(repo.Dir) {
			t.Error("expected dirty tree with untracked file to return false")
		}
	})
}

func TestResetWorkingTree(t *testing.T) {
	t.Run("resets modified files", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("initial.txt", "initial content", "initial")

		repo.WriteFile("initial.txt", "modified")

		if IsWorkingTreeClean(repo.Dir) {
			t.Fatal("expected tree to be dirty before reset")
		}

		if err := ResetWorkingTree(repo.Dir); err != nil {
			t.Fatalf("ResetWorkingTree failed: %v", err)
		}

		if !IsWorkingTreeClean(repo.Dir) {
			t.Error("expected tree to be clean after reset")
		}

		content, err := os.ReadFile(filepath.Join(repo.Dir, "initial.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "initial content" {
			t.Errorf("expected file content 'initial content', got %q", string(content))
		}
	})

	t.Run("removes untracked files", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("initial.txt", "initial content", "initial")

		untrackedFile := filepath.Join(repo.Dir, "untracked.txt")
		repo.WriteFile("untracked.txt", "untracked")

		if IsWorkingTreeClean(repo.Dir) {
			t.Fatal("expected tree to be dirty before reset")
		}

		if err := ResetWorkingTree(repo.Dir); err != nil {
			t.Fatalf("ResetWorkingTree failed: %v", err)
		}

		if !IsWorkingTreeClean(repo.Dir) {
			t.Error("expected tree to be clean after reset")
		}

		if _, err := os.Stat(untrackedFile); !os.IsNotExist(err) {
			t.Error("expected untracked file to be removed")
		}
	})

	t.Run("resets staged changes", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("initial.txt", "initial content", "initial")

		repo.WriteFile("initial.txt", "staged changes")
		repo.Run("add", ".")

		if IsWorkingTreeClean(repo.Dir) {
			t.Fatal("expected tree to be dirty before reset")
		}

		if err := ResetWorkingTree(repo.Dir); err != nil {
			t.Fatalf("ResetWorkingTree failed: %v", err)
		}

		if !IsWorkingTreeClean(repo.Dir) {
			t.Error("expected tree to be clean after reset")
		}

		content, err := os.ReadFile(filepath.Join(repo.Dir, "initial.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "initial content" {
			t.Errorf("expected file content 'initial content', got %q", string(content))
		}
	})
}

func TestLocalBranchName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"main", "main"},
		{"origin/main", "main"},
		{"origin/master", "master"},
		{"feature/foo", "feature/foo"},
		{"origin/feature/foo", "feature/foo"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := LocalBranchName(tt.input)
			if got != tt.want {
				t.Errorf("LocalBranchName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetRangeFilesChanged(t *testing.T) {
	repo := NewTestRepo(t)
	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
	repo.CommitFile("base.txt", "base", "base commit")
	baseSHA := repo.HeadSHA()

	// Create branch with some changes
	repo.Run("checkout", "-b", "feature")
	repo.CommitFile("new.go", "package main", "add go file")
	repo.CommitFile("docs.md", "# Docs", "add docs")
	repo.CommitFile("config.yml", "key: val", "add config")

	t.Run("returns changed files in range", func(t *testing.T) {
		files, err := GetRangeFilesChanged(repo.Dir, baseSHA+"..HEAD")
		if err != nil {
			t.Fatalf("GetRangeFilesChanged failed: %v", err)
		}
		if len(files) != 3 {
			t.Fatalf("expected 3 files, got %d: %v", len(files), files)
		}
		found := map[string]bool{}
		for _, f := range files {
			found[f] = true
		}
		for _, want := range []string{"new.go", "docs.md", "config.yml"} {
			if !found[want] {
				t.Errorf("expected %s in changed files, got %v", want, files)
			}
		}
	})

	t.Run("empty range returns nil", func(t *testing.T) {
		files, err := GetRangeFilesChanged(repo.Dir, "HEAD..HEAD")
		if err != nil {
			t.Fatalf("GetRangeFilesChanged failed: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("expected 0 files for empty range, got %d: %v", len(files), files)
		}
	})
}

func TestCreateCommitPreCommitHookOutput(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial", "initial commit")

	repo.InstallHook("pre-commit",
		"#!/bin/sh\necho 'error: trailing whitespace on line 42' >&2\nexit 1\n")

	// Make a change so there's something to commit
	repo.WriteFile("new.txt", "content")
	repo.Run("add", "new.txt")

	_, err := CreateCommit(repo.Dir, "should fail")
	if err == nil {
		t.Fatal("expected CreateCommit to fail with pre-commit hook")
	}

	// The error should contain the hook's stderr output
	if !strings.Contains(err.Error(), "trailing whitespace on line 42") {
		t.Errorf(
			"expected error to contain hook output, got: %v", err,
		)
	}

	// HookFailed should be true since the hook caused the failure
	var commitErr *CommitError
	if !errors.As(err, &commitErr) {
		t.Fatal("expected CommitError type")
	}
	if !commitErr.HookFailed {
		t.Error("expected HookFailed=true for pre-commit hook rejection")
	}
}

func TestCommitErrorHookFailedFalseWhenNothingToCommit(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial", "initial commit")

	// Install a passing pre-commit hook. The commit should still fail
	// because there are no staged changes ("nothing to commit").
	// The dry-run probe (--no-verify --dry-run) also fails for the
	// same reason, so HookFailed must be false.
	repo.InstallHook("pre-commit", "#!/bin/sh\nexit 0\n")

	// No staged changes — commit fails for non-hook reason
	_, err := CreateCommit(repo.Dir, "empty commit")
	if err == nil {
		t.Fatal("expected CreateCommit to fail")
	}

	var commitErr *CommitError
	if !errors.As(err, &commitErr) {
		t.Fatal("expected CommitError type")
	}

	// Dry-run without hooks also fails, so HookFailed must be false
	if commitErr.HookFailed {
		t.Error("HookFailed should be false when commit fails for non-hook reasons")
	}
}

func TestCommitErrorHookFailedCommitMsgHook(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial", "initial commit")

	// Install a commit-msg hook that rejects. The dry-run probe
	// bypasses all hooks (--no-verify), so it should succeed and
	// HookFailed should be true.
	repo.InstallHook("commit-msg",
		"#!/bin/sh\necho 'bad commit message format' >&2\nexit 1\n")

	repo.WriteFile("new.txt", "content")
	repo.Run("add", "new.txt")

	_, err := CreateCommit(repo.Dir, "should fail")
	if err == nil {
		t.Fatal("expected CreateCommit to fail with commit-msg hook")
	}

	var commitErr *CommitError
	if !errors.As(err, &commitErr) {
		t.Fatal("expected CommitError type")
	}
	if !commitErr.HookFailed {
		t.Error("expected HookFailed=true for commit-msg hook rejection")
	}
}

func TestCommitErrorHookFailedFalseForGPGSigningFailure(t *testing.T) {
	// GPG signing failure: commit fails because there's no usable
	// key, but --dry-run --no-verify passes (dry-run doesn't sign).
	// Without the hasCommitHooks guard, this would be a false
	// positive (HookFailed=true with no hooks installed).
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial", "initial commit")

	// Force GPG signing with a non-existent key
	repo.Run("config", "commit.gpgsign", "true")
	repo.Run("config", "user.signingkey", "DEADBEEF00000000")

	repo.WriteFile("new.txt", "content")
	repo.Run("add", "new.txt")

	_, err := CreateCommit(repo.Dir, "should fail from gpg")
	if err == nil {
		// Some CI environments may have gpg configured in a way
		// that doesn't fail. Skip rather than give a false pass.
		t.Skip("commit succeeded unexpectedly (gpg may be configured)")
	}

	var commitErr *CommitError
	if !errors.As(err, &commitErr) {
		t.Fatalf("expected CommitError type, got: %T", err)
	}
	if commitErr.HookFailed {
		t.Error(
			"HookFailed should be false for GPG signing " +
				"failure (no hooks installed)",
		)
	}
}

func TestHasCommitHooksDetectsInstalledHooks(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial", "initial commit")

	if hasCommitHooks(repo.Dir) {
		t.Error("expected no commit hooks in fresh repo")
	}

	repo.InstallHook("pre-commit", "#!/bin/sh\nexit 0\n")
	if !hasCommitHooks(repo.Dir) {
		t.Error("expected hasCommitHooks=true after installing pre-commit")
	}
}

func TestHasCommitHooksIgnoresDirectories(t *testing.T) {
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial", "initial commit")

	// Create a directory named like a hook — should not count.
	hooksDir, err := GetHooksPath(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hooksDir, "pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}

	if hasCommitHooks(repo.Dir) {
		t.Error("directory named pre-commit should not count as installed hook")
	}
}

func TestIsAncestor(t *testing.T) {
	repo := NewTestRepo(t)
	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")

	repo.CommitFile("base.txt", "base", "base commit")
	baseSHA := repo.HeadSHA()

	repo.CommitFile("second.txt", "second", "second commit")
	secondSHA := repo.HeadSHA()

	// Create a divergent branch from base
	repo.Run("checkout", baseSHA)
	repo.Run("checkout", "-b", "divergent")
	repo.CommitFile("divergent.txt", "divergent", "divergent commit")
	divergentSHA := repo.HeadSHA()

	t.Run("base is ancestor of second", func(t *testing.T) {
		isAnc, err := IsAncestor(repo.Dir, baseSHA, secondSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isAnc {
			t.Error("expected base to be ancestor of second")
		}
	})

	t.Run("second is not ancestor of base", func(t *testing.T) {
		isAnc, err := IsAncestor(repo.Dir, secondSHA, baseSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isAnc {
			t.Error("expected second to NOT be ancestor of base")
		}
	})

	t.Run("divergent is not ancestor of second", func(t *testing.T) {
		isAnc, err := IsAncestor(repo.Dir, divergentSHA, secondSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isAnc {
			t.Error("expected divergent to NOT be ancestor of second (different branches)")
		}
	})

	t.Run("base is ancestor of divergent", func(t *testing.T) {
		isAnc, err := IsAncestor(repo.Dir, baseSHA, divergentSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isAnc {
			t.Error("expected base to be ancestor of divergent")
		}
	})

	t.Run("commit is ancestor of itself", func(t *testing.T) {
		isAnc, err := IsAncestor(repo.Dir, baseSHA, baseSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isAnc {
			t.Error("expected commit to be ancestor of itself")
		}
	})

	t.Run("bad object returns error", func(t *testing.T) {
		_, err := IsAncestor(repo.Dir, "badbadbadbadbadbadbadbadbadbadbadbadbad", "HEAD")
		if err == nil {
			t.Error("expected error for bad object")
		}
	})
}

func TestGetPatchID(t *testing.T) {
	t.Run("stable across rebase", func(t *testing.T) {
		repo := NewTestRepo(t)
		// Use -b to name the initial branch explicitly
		repo.Run("checkout", "-b", "main")
		repo.CommitFile("base.txt", "base", "initial")

		// Create a commit on a branch
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "hello", "add feature")
		sha1 := repo.HeadSHA()
		patchID1 := GetPatchID(repo.Dir, sha1)

		if patchID1 == "" {
			t.Fatal("expected non-empty patch-id")
		}

		// Rebase onto a new base commit
		repo.Run("checkout", "main")
		repo.CommitFile("other.txt", "other", "another commit")
		repo.Run("checkout", "feature")
		repo.Run("rebase", "main")
		sha2 := repo.HeadSHA()
		patchID2 := GetPatchID(repo.Dir, sha2)

		if sha1 == sha2 {
			t.Fatal("SHAs should differ after rebase")
		}
		if patchID1 != patchID2 {
			t.Errorf("patch-ids should match: %s != %s", patchID1, patchID2)
		}
	})

	t.Run("different for modified commits", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("a.txt", "content-a", "commit a")
		sha1 := repo.HeadSHA()

		repo.CommitFile("b.txt", "content-b", "commit b")
		sha2 := repo.HeadSHA()

		pid1 := GetPatchID(repo.Dir, sha1)
		pid2 := GetPatchID(repo.Dir, sha2)

		if pid1 == "" || pid2 == "" {
			t.Fatal("expected non-empty patch-ids")
		}
		if pid1 == pid2 {
			t.Error("different diffs should produce different patch-ids")
		}
	})

	t.Run("empty for empty commit", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("a.txt", "content", "first")
		repo.Run("commit", "--allow-empty", "-m", "empty")
		sha := repo.HeadSHA()

		pid := GetPatchID(repo.Dir, sha)
		if pid != "" {
			t.Errorf("expected empty patch-id for empty commit, got %s", pid)
		}
	})
}
