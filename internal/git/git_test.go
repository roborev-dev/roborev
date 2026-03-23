package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/roborev-dev/roborev/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolatedMain(m))
}

type TestRepo struct {
	T   *testing.T
	Dir string
}

func NewTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	return NewTestRepoWithAuthor(t, "Test")
}

func NewTestRepoWithAuthor(t *testing.T, author string) *TestRepo {
	t.Helper()
	dir := t.TempDir()
	r := &TestRepo{T: t, Dir: dir}
	r.Run("init")
	r.Run("config", "user.email", "test@test.com")
	r.Run("config", "user.name", author)
	return r
}

func NewTestRepoWithCommit(t *testing.T) *TestRepo {
	t.Helper()
	repo := NewTestRepo(t)
	repo.CommitFile("initial.txt", "initial content", "initial commit")
	return repo
}

func NewBareTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	dir := t.TempDir()
	r := &TestRepo{T: t, Dir: dir}
	r.Run("init", "--bare")
	return r
}

func (r *TestRepo) Run(args ...string) string {
	r.T.Helper()
	return runGit(r.T, r.Dir, args...)
}

func (r *TestRepo) CommitFile(filename, content, msg string) {
	r.T.Helper()
	r.WriteFile(filename, content)
	r.Run("add", filename)
	r.Run("commit", "-m", msg)
}

func (r *TestRepo) CommitAll(msg string) {
	r.T.Helper()
	r.Run("add", ".")
	r.Run("commit", "-m", msg)
}

func (r *TestRepo) WriteFile(filename, content string) {
	r.T.Helper()
	path := filepath.Join(r.Dir, filename)
	err := os.MkdirAll(filepath.Dir(path), 0755)
	require.NoError(r.T, err)
	err = os.WriteFile(path, []byte(content), 0644)
	require.NoError(r.T, err)
}

func (r *TestRepo) HeadSHA() string {
	r.T.Helper()
	return r.Run("rev-parse", "HEAD")
}

func (r *TestRepo) AddWorktree(branchName string) *TestRepo {
	r.T.Helper()
	wtDir := r.T.TempDir()
	r.Run("worktree", "add", wtDir, "-b", branchName)
	r.T.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", wtDir)
		cmd.Dir = r.Dir
		_ = cmd.Run()
	})
	return &TestRepo{T: r.T, Dir: wtDir}
}

func (r *TestRepo) InstallHook(name, script string) {
	r.T.Helper()
	hooksDir := filepath.Join(r.Dir, ".git", "hooks")
	err := os.MkdirAll(hooksDir, 0755)
	require.NoError(r.T, err)
	hookPath := filepath.Join(hooksDir, name)
	err = os.WriteFile(hookPath, []byte(script), 0755)
	require.NoError(r.T, err)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %v\n%s", args, err, out)
	return strings.TrimSpace(string(out))
}

func TestIsUnbornHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("true for empty repo", func(t *testing.T) {
		repo := NewTestRepo(t)
		assert.True(t, IsUnbornHead(repo.Dir), "expected IsUnbornHead=true for empty repo")
	})

	t.Run("false after first commit", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		assert.False(t, IsUnbornHead(repo.Dir), "expected IsUnbornHead=false after commit")
	})

	t.Run("false for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		assert.False(t, IsUnbornHead(dir), "expected IsUnbornHead=false for non-git dir")
	})

	t.Run("false for corrupt ref", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		headRef := strings.TrimSpace(repo.Run("symbolic-ref", "HEAD"))
		repo.WriteFile(filepath.Join(".git", headRef), "0000000000000000000000000000000000000000\n")

		assert.False(t, IsUnbornHead(repo.Dir), "expected IsUnbornHead=false for corrupt ref (ref exists but object is missing)")
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
			assert.Equal(t, tt.expected, result, "Expected %s, got %s", tt.expected, result)

		})
	}
}

func TestGetHooksPath(t *testing.T) {
	t.Run("default hooks path", func(t *testing.T) {
		repo := NewTestRepo(t)

		hooksPath, err := GetHooksPath(repo.Dir)
		require.NoError(t, err, "GetHooksPath failed: %v", err)
		assert.True(t, filepath.IsAbs(hooksPath),
			"hooks path should be absolute, got: %s", hooksPath)

		cleanPath := filepath.Clean(hooksPath)
		expectedSuffix := filepath.Join(".git", "hooks")
		assert.True(t, strings.HasSuffix(cleanPath, expectedSuffix),
			"hooks path should end with %s, got: %s",
			expectedSuffix, cleanPath)
	})

	t.Run("custom core.hooksPath absolute", func(t *testing.T) {
		repo := NewTestRepo(t)
		customHooksDir := filepath.Join(repo.Dir, "my-hooks")
		err := os.MkdirAll(customHooksDir, 0o755)
		require.NoError(t, err)

		repo.Run("config", "core.hooksPath", customHooksDir)

		hooksPath, err := GetHooksPath(repo.Dir)
		require.NoError(t, err, "GetHooksPath failed: %v", err)

		assert.Equal(t, customHooksDir, hooksPath, "expected hooksPath=%s, got %s", customHooksDir, hooksPath)
	})

	t.Run("custom core.hooksPath relative", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.Run("config", "core.hooksPath", "custom-hooks")

		hooksPath, err := GetHooksPath(repo.Dir)
		require.NoError(t, err, "GetHooksPath failed: %v", err)

		assert.True(t, filepath.IsAbs(hooksPath),
			"hooks path should be absolute, got: %s", hooksPath)
		// GetMainRepoRoot resolves symlinks, so compare
		// against the resolved dir.
		resolvedDir, err := filepath.EvalSymlinks(repo.Dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(resolvedDir, "custom-hooks"),
			hooksPath)
	})

	t.Run("relative hooksPath resolves to main repo from worktree",
		func(t *testing.T) {
			repo := NewTestRepo(t)
			repo.Run("commit", "--allow-empty", "-m", "init")
			repo.Run("config", "core.hooksPath", ".githooks")

			wtDir := t.TempDir()
			resolved, err := filepath.EvalSymlinks(wtDir)
			require.NoError(t, err)
			repo.Run("worktree", "add", resolved, "-b", "wt")

			hooksPath, err := GetHooksPath(resolved)
			require.NoError(t, err)

			resolvedMain, err := filepath.EvalSymlinks(repo.Dir)
			require.NoError(t, err)
			assert.Equal(t,
				filepath.Join(resolvedMain, ".githooks"),
				hooksPath,
				"should resolve against main repo, not worktree",
			)
		})

	t.Run("default hooksPath resolves to main repo from worktree",
		func(t *testing.T) {
			repo := NewTestRepo(t)
			repo.Run("commit", "--allow-empty", "-m", "init")

			wtDir := t.TempDir()
			resolved, err := filepath.EvalSymlinks(wtDir)
			require.NoError(t, err)
			repo.Run("worktree", "add", resolved, "-b", "wt")

			hooksPath, err := GetHooksPath(resolved)
			require.NoError(t, err)

			resolvedMain, err := filepath.EvalSymlinks(repo.Dir)
			require.NoError(t, err)
			assert.Equal(t,
				filepath.Join(resolvedMain, ".git", "hooks"),
				hooksPath,
				"default hooks path from worktree should point "+
					"at main repo .git/hooks",
			)
		})
}

func TestEnsureAbsoluteHooksPath(t *testing.T) {
	t.Run("noop when not set", func(t *testing.T) {
		repo := NewTestRepo(t)
		err := EnsureAbsoluteHooksPath(repo.Dir)
		require.NoError(t, err)

		// Verify core.hooksPath is still unset.
		cmd := exec.Command(
			"git", "config", "--local", "core.hooksPath",
		)
		cmd.Dir = repo.Dir
		assert.Error(t, cmd.Run(), "core.hooksPath should remain unset")
	})

	t.Run("noop when already absolute", func(t *testing.T) {
		repo := NewTestRepo(t)
		absPath := filepath.Join(repo.Dir, "my-hooks")
		repo.Run("config", "core.hooksPath", absPath)

		err := EnsureAbsoluteHooksPath(repo.Dir)
		require.NoError(t, err)

		got := repo.Run("config", "--local", "core.hooksPath")
		assert.Equal(t, absPath, got)
	})

	t.Run("noop for tilde home path", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.Run("config", "core.hooksPath", "~/my-hooks")

		err := EnsureAbsoluteHooksPath(repo.Dir)
		require.NoError(t, err)

		got := repo.Run("config", "--local", "core.hooksPath")
		assert.Equal(t, "~/my-hooks", got,
			"~/path should be left for git to expand")
	})

	t.Run("noop for bare tilde", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.Run("config", "core.hooksPath", "~")

		err := EnsureAbsoluteHooksPath(repo.Dir)
		require.NoError(t, err)

		got := repo.Run("config", "--local", "core.hooksPath")
		assert.Equal(t, "~", got,
			"bare ~ should be left for git to expand")
	})

	t.Run("converts relative to absolute", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.Run("config", "core.hooksPath", ".githooks")

		err := EnsureAbsoluteHooksPath(repo.Dir)
		require.NoError(t, err)

		got := repo.Run("config", "--local", "core.hooksPath")
		assert.True(t, filepath.IsAbs(got),
			"expected absolute path, got: %s", got)
		// GetMainRepoRoot resolves symlinks (e.g. macOS
		// /var → /private/var), so compare against the
		// resolved repo dir.
		resolvedDir, err := filepath.EvalSymlinks(repo.Dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(resolvedDir, ".githooks"), got)
	})

	t.Run("resolves against main repo root from worktree", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.Run("commit", "--allow-empty", "-m", "init")
		repo.Run("config", "core.hooksPath", ".githooks")

		wtDir := t.TempDir()
		resolved, err := filepath.EvalSymlinks(wtDir)
		require.NoError(t, err)
		repo.Run("worktree", "add", resolved, "-b", "wt-branch")

		// Run from the linked worktree, not the main repo.
		err = EnsureAbsoluteHooksPath(resolved)
		require.NoError(t, err)

		// The rewritten path must point at the main repo's
		// .githooks, not the worktree's.
		resolvedMain, err := filepath.EvalSymlinks(repo.Dir)
		require.NoError(t, err)
		wt := &TestRepo{T: t, Dir: resolved}
		got := wt.Run("config", "--local", "core.hooksPath")
		assert.Equal(t, filepath.Join(resolvedMain, ".githooks"), got,
			"should resolve against main repo root, not worktree")
	})

	t.Run("overrides relative global config with local absolute",
		func(t *testing.T) {
			// Simulate a global ~/.gitconfig with relative
			// core.hooksPath (no local config set).
			fakeHome := t.TempDir()
			t.Setenv("HOME", fakeHome)
			t.Setenv("USERPROFILE", fakeHome)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(
				fakeHome, ".config",
			))
			globalCfg := filepath.Join(fakeHome, ".gitconfig")
			t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
			err := os.WriteFile(globalCfg, []byte(
				"[core]\n\thooksPath = .githooks\n",
			), 0644)
			require.NoError(t, err)

			repo := NewTestRepo(t)

			// Verify no local override exists yet.
			check := exec.Command(
				"git", "config", "--local", "core.hooksPath",
			)
			check.Dir = repo.Dir
			require.Error(t, check.Run(),
				"should have no local core.hooksPath")

			err = EnsureAbsoluteHooksPath(repo.Dir)
			require.NoError(t, err)

			// Should now have a local absolute override.
			got := repo.Run(
				"config", "--local", "core.hooksPath",
			)
			assert.True(t, filepath.IsAbs(got),
				"expected absolute path, got: %s", got)
			resolvedDir, err := filepath.EvalSymlinks(repo.Dir)
			require.NoError(t, err)
			assert.Equal(t,
				filepath.Join(resolvedDir, ".githooks"), got,
			)
		})
}

func TestIsRebaseInProgress(t *testing.T) {
	t.Run("no rebase", func(t *testing.T) {
		repo := NewTestRepo(t)
		assert.False(t, IsRebaseInProgress(repo.Dir), "expected no rebase in progress")
	})

	t.Run("rebase-merge directory", func(t *testing.T) {
		repo := NewTestRepo(t)
		rebaseMerge := filepath.Join(repo.Dir, ".git", "rebase-merge")
		err := os.MkdirAll(rebaseMerge, 0o755)
		require.NoError(t, err)
		assert.True(t, IsRebaseInProgress(repo.Dir), "should detect rebase-merge")
	})

	t.Run("rebase-apply directory", func(t *testing.T) {
		repo := NewTestRepo(t)
		rebaseApply := filepath.Join(repo.Dir, ".git", "rebase-apply")
		err := os.MkdirAll(rebaseApply, 0o755)
		require.NoError(t, err)
		assert.True(t, IsRebaseInProgress(repo.Dir), "should detect rebase-apply")
	})

	t.Run("non-repo returns false", func(t *testing.T) {
		nonRepo := t.TempDir()
		assert.False(t, IsRebaseInProgress(nonRepo), "non-repo should not be in rebase")
	})

	t.Run("worktree with rebase", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		wt := repo.AddWorktree("test-branch")

		gitPath := filepath.Join(wt.Dir, ".git")
		info, err := os.Stat(gitPath)
		require.NoError(t, err, "worktree .git not found: %v", err)
		if info.IsDir() {
			t.Skip("worktree has .git directory instead of file - older git version")
		}
		require.False(t, IsRebaseInProgress(wt.Dir), "worktree should not be in rebase")

		worktreeGitDir := strings.TrimSpace(wt.Run("rev-parse", "--git-dir"))
		if !filepath.IsAbs(worktreeGitDir) {
			worktreeGitDir = filepath.Join(wt.Dir, worktreeGitDir)
		}

		rebaseMerge := filepath.Join(worktreeGitDir, "rebase-merge")
		err = os.MkdirAll(rebaseMerge, 0o755)
		require.NoError(t, err)
		require.True(t, IsRebaseInProgress(wt.Dir), "worktree should detect rebase")
	})
}

func TestGetCommitInfo(t *testing.T) {
	t.Run("commit with subject only", func(t *testing.T) {
		repo := NewTestRepoWithAuthor(t, "Test Author")

		repo.CommitFile("file1.txt", "content", "Simple subject")

		commitSHA := repo.HeadSHA()

		info, err := GetCommitInfo(repo.Dir, commitSHA)
		require.NoError(t, err, "GetCommitInfo failed: %v", err)
		assert.Equal(t, "Simple subject", info.Subject, "expected subject 'Simple subject', got '%s'", info.Subject)

		assert.Empty(t, info.Body, "expected empty body, got '%s'", info.Body)
		assert.Equal(t, "Test Author", info.Author, "expected author 'Test Author', got '%s'", info.Author)

	})

	t.Run("commit with subject and body", func(t *testing.T) {
		repo := NewTestRepoWithAuthor(t, "Test Author")
		repo.WriteFile("file2.txt", "content2")
		repo.Run("add", ".")

		commitMsg := "Subject line\n\nThis is the body.\nIt has multiple lines.\n\nAnd paragraphs."
		repo.Run("commit", "-m", commitMsg)

		commitSHA := repo.HeadSHA()

		info, err := GetCommitInfo(repo.Dir, commitSHA)
		require.NoError(t, err, "GetCommitInfo failed: %v", err)
		assert.Equal(t, "Subject line", info.Subject, "expected subject 'Subject line', got '%s'", info.Subject)

		assert.Contains(t, info.Body, "This is the body", "expected body to contain 'This is the body', got '%s'", info.Body)
		assert.Contains(t, info.Body, "multiple lines", "expected body to contain 'multiple lines', got '%s'", info.Body)
	})

	t.Run("commit with pipe in message", func(t *testing.T) {
		repo := NewTestRepoWithAuthor(t, "Test Author")
		repo.WriteFile("file3.txt", "content3")
		repo.Run("add", ".")

		commitMsg := "Fix bug | important\n\nDetails: foo | bar | baz"
		repo.Run("commit", "-m", commitMsg)

		commitSHA := repo.HeadSHA()

		info, err := GetCommitInfo(repo.Dir, commitSHA)
		require.NoError(t, err, "GetCommitInfo failed: %v", err)

		assert.Contains(t, info.Subject, "|", "expected subject to contain pipe, got '%s'", info.Subject)
		assert.Contains(t, info.Body, "foo | bar", "expected body to contain 'foo | bar', got '%s'", info.Body)
	})
}

func TestGetBranchName(t *testing.T) {
	t.Run("valid commit on branch", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		commitSHA := repo.HeadSHA()
		expectedBranch := repo.Run("rev-parse", "--abbrev-ref", "HEAD")

		branch := GetBranchName(repo.Dir, commitSHA)
		assert.Equal(t, expectedBranch, branch, "expected %s, got %s", expectedBranch, branch)
	})

	t.Run("commit behind branch head", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		commitSHA := repo.HeadSHA()
		expectedBranch := repo.Run("rev-parse", "--abbrev-ref", "HEAD")

		repo.CommitFile("file2.txt", "content2", "second")

		branch := GetBranchName(repo.Dir, commitSHA)
		assert.Equal(t, expectedBranch, branch, "expected %s (suffix stripped), got %s", expectedBranch, branch)
	})

	t.Run("non-existent repo returns empty", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		commitSHA := repo.HeadSHA()

		nonRepo := t.TempDir()
		branch := GetBranchName(nonRepo, commitSHA)
		assert.Empty(t, branch, "expected empty string, got %s", branch)
	})

	t.Run("invalid SHA returns empty", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		branch := GetBranchName(repo.Dir, "0000000000000000000000000000000000000000")
		assert.Empty(t, branch, "expected empty string, got %s", branch)
	})
}

func TestGetCurrentBranch(t *testing.T) {
	t.Run("returns current branch", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		branch := GetCurrentBranch(repo.Dir)
		assert.NotEmpty(t, branch)
		assert.NotContains(t, branch, "heads/",
			"branch should not have heads/ prefix")
	})

	t.Run("returns branch after checkout", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.Run("checkout", "-b", "feature-branch")

		branch := GetCurrentBranch(repo.Dir)
		assert.Equal(t, "feature-branch", branch)
	})

	t.Run("returns empty for detached HEAD", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		sha := repo.HeadSHA()
		repo.Run("checkout", sha)

		branch := GetCurrentBranch(repo.Dir)
		assert.Empty(t, branch)
	})

	t.Run("returns empty for non-repo", func(t *testing.T) {
		nonRepo := t.TempDir()
		branch := GetCurrentBranch(nonRepo)
		assert.Empty(t, branch)
	})

	t.Run("no heads prefix with ambiguous remote ref", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		// Create a branch and a remote-tracking ref that share
		// the same suffix. rev-parse --abbrev-ref and symbolic-ref
		// --short both add "heads/" to disambiguate.
		repo.Run("checkout", "-b", "user/feat")
		sha := repo.HeadSHA()
		repo.Run("update-ref", "refs/remotes/user/feat", sha)

		branch := GetCurrentBranch(repo.Dir)
		assert.Equal(t, "user/feat", branch,
			"should return clean branch name with ambiguous remote ref")
	})

	t.Run("no heads prefix with ambiguous tag", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		// A tag with the same name as the branch causes
		// symbolic-ref --short to return "heads/user/feat".
		repo.Run("checkout", "-b", "user/feat")
		repo.Run("tag", "user/feat")

		branch := GetCurrentBranch(repo.Dir)
		assert.Equal(t, "user/feat", branch,
			"should return clean branch name with ambiguous tag")
	})

	t.Run("no heads prefix in linked worktree with ambiguous refs", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		// Create a worktree on "user/feat/view", then add a remote
		// ref that shares the suffix. Both rev-parse --abbrev-ref
		// and symbolic-ref --short return "heads/user/feat/view"
		// in linked worktrees to disambiguate.
		wt := repo.AddWorktree("user/feat/view")
		sha := repo.HeadSHA()
		repo.Run(
			"update-ref",
			"refs/remotes/user/feat/view",
			sha,
		)

		branch := GetCurrentBranch(wt.Dir)
		assert.Equal(t, "user/feat/view", branch,
			"linked worktree should return clean branch name")
	})
}

func TestHasUncommittedChanges(t *testing.T) {
	t.Run("no changes", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		hasChanges, err := HasUncommittedChanges(repo.Dir)
		require.NoError(t, err, "HasUncommittedChanges failed: %v", err)
		assert.False(t, hasChanges, "no changes should not report uncommitted changes")
	})

	t.Run("staged changes", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("initial.txt", "modified")
		repo.Run("add", ".")

		hasChanges, err := HasUncommittedChanges(repo.Dir)
		require.NoError(t, err, "HasUncommittedChanges failed: %v", err)
		assert.True(t, hasChanges, "expected staged changes to be reported as dirty")
	})

	t.Run("unstaged changes", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("initial.txt", "unstaged")

		hasChanges, err := HasUncommittedChanges(repo.Dir)
		require.NoError(t, err, "HasUncommittedChanges failed: %v", err)
		assert.True(t, hasChanges, "expected unstaged changes to be reported as dirty")
	})

	t.Run("untracked file", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("untracked.txt", "new")

		hasChanges, err := HasUncommittedChanges(repo.Dir)
		require.NoError(t, err, "HasUncommittedChanges failed: %v", err)
		assert.True(t, hasChanges, "expected untracked file to be reported as dirty")
	})
}

func TestGetDirtyDiff(t *testing.T) {
	t.Run("includes tracked file changes", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("initial.txt", "modified\n")

		diff, err := GetDirtyDiff(repo.Dir)
		require.NoError(t, err, "GetDirtyDiff failed: %v", err)
		assert.Contains(t, diff, "initial.txt", "expected dirty diff to include initial.txt")
		assert.Contains(t, diff, "+modified", "expected diff to contain +modified")
	})

	t.Run("includes untracked files", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("newfile.txt", "new content\n")

		diff, err := GetDirtyDiff(repo.Dir)
		require.NoError(t, err, "GetDirtyDiff failed: %v", err)
		assert.Contains(t, diff, "newfile.txt", "expected dirty diff to include newfile.txt")
		assert.Contains(t, diff, "+new content", "expected diff to contain +new content")
		assert.Contains(t, diff, "new file mode", "expected diff to contain 'new file mode' header")
	})

	t.Run("includes both tracked and untracked", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("initial.txt", "changed\n")
		repo.WriteFile("another.txt", "another\n")

		diff, err := GetDirtyDiff(repo.Dir)
		require.NoError(t, err, "GetDirtyDiff failed: %v", err)
		assert.Contains(t, diff, "initial.txt", "expected dirty diff to include initial.txt")
		assert.Contains(t, diff, "another.txt", "expected dirty diff to include another.txt")
	})

	t.Run("handles binary files", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("binary.bin", "hello\x00world")

		diff, err := GetDirtyDiff(repo.Dir)
		require.NoError(t, err, "GetDirtyDiff failed: %v", err)
		assert.Contains(t, diff, "binary.bin", "expected dirty diff to include binary.bin")
		assert.Contains(t, diff, "Binary file", "expected dirty diff to include binary file marker")
	})
}

func TestGetDirtyDiffNoCommits(t *testing.T) {
	repo := NewTestRepo(t)

	repo.WriteFile("newfile.txt", "content\n")
	repo.Run("add", ".")

	repo.WriteFile("untracked.txt", "untracked\n")

	diff, err := GetDirtyDiff(repo.Dir)
	require.NoError(t, err, "GetDirtyDiff failed on repo with no commits: %v", err)
	assert.Contains(t, diff, "newfile.txt", "expected diff to contain newfile.txt (staged)")
	assert.Contains(t, diff, "untracked.txt", "expected diff to contain untracked file marker")
}

func TestGetDirtyDiffStagedThenDeleted(t *testing.T) {
	repo := NewTestRepo(t)

	repo.WriteFile("staged.txt", "staged content\n")
	repo.Run("add", "staged.txt")

	err := os.Remove(filepath.Join(repo.Dir, "staged.txt"))
	require.NoError(t, err, "failed to remove staged.txt")

	diff, err := GetDirtyDiff(repo.Dir)
	require.NoError(t, err, "GetDirtyDiff failed: %v", err)
	assert.Contains(t, diff, "staged.txt", "expected diff to contain staged.txt (staged but deleted from working tree)")
	assert.Contains(t, diff, "staged content", "expected staged diff to include content")
}

func TestFormatExcludeArgs(t *testing.T) {
	assert.Nil(t, FormatExcludeArgs(nil))
	assert.Nil(t, FormatExcludeArgs([]string{}))

	// Plain names get both file and directory forms
	assert.Equal(t,
		[]string{
			":(exclude,glob)**/foo.lock",
			":(exclude,glob)**/foo.lock/**",
			":(exclude,glob)**/*.min.js",
			":(exclude,glob)**/*.min.js/**",
		},
		FormatExcludeArgs([]string{"foo.lock", "*.min.js"}),
	)

	// Patterns with path separators get both exact and subtree forms
	assert.Equal(t,
		[]string{
			":(exclude,glob)vendor/dist",
			":(exclude,glob)vendor/dist/**",
		},
		FormatExcludeArgs([]string{"vendor/dist"}),
	)

	// Whitespace-only patterns are skipped
	assert.Equal(t,
		[]string{
			":(exclude,glob)**/keep",
			":(exclude,glob)**/keep/**",
		},
		FormatExcludeArgs([]string{" ", "keep", "  "}),
	)

	// Leading slash = root-anchored (no **/ prefix)
	assert.Equal(t,
		[]string{
			":(exclude,glob)vendor",
			":(exclude,glob)vendor/**",
		},
		FormatExcludeArgs([]string{"/vendor"}),
	)
}

func TestGetDiffExtraExcludes(t *testing.T) {
	repo := NewTestRepoWithCommit(t)
	repo.WriteFile("keep.txt", "keep\n")
	repo.WriteFile("custom.lock", "lockdata\n")
	repo.CommitAll("add files")

	sha := repo.HeadSHA()

	diff, err := GetDiff(repo.Dir, sha, "custom.lock")
	require.NoError(t, err)
	assert.Contains(t, diff, "keep.txt")
	assert.NotContains(t, diff, "custom.lock")
}

func TestGetDiffExcludesNestedFiles(t *testing.T) {
	// Verify that built-in and extra excludes work at any depth
	repo := NewTestRepoWithCommit(t)
	repo.WriteFile("keep.txt", "keep\n")
	repo.WriteFile("sub/uv.lock", "nested builtin\n")
	repo.WriteFile("sub/deep/custom.lock", "nested custom\n")
	repo.CommitAll("add nested files")

	sha := repo.HeadSHA()

	diff, err := GetDiff(repo.Dir, sha, "custom.lock")
	require.NoError(t, err)
	assert.Contains(t, diff, "keep.txt")
	assert.NotContains(t, diff, "uv.lock",
		"built-in exclude should match nested uv.lock")
	assert.NotContains(t, diff, "custom.lock",
		"extra exclude should match nested custom.lock")
}

func setupDiffExcludesGeneratedFilesTest(t *testing.T) (*TestRepo, string) {
	t.Helper()
	repo := NewTestRepoWithCommit(t)

	repo.WriteFile(".beads/notes.md", "beads\n")
	repo.WriteFile("uv.lock", "lock\n")
	repo.WriteFile("go.sum", "sum\n")
	repo.WriteFile("keep.txt", "keep\n")

	repo.CommitAll("add files")

	sha := repo.HeadSHA()
	return repo, sha
}

func TestGetDiffExcludesGeneratedFiles(t *testing.T) {
	assertExcluded := func(t *testing.T, diff string) {
		t.Helper()
		require.Contains(t, diff, "keep.txt", "expected generated files filter to retain keep.txt")
		require.NotContains(t, diff, "uv.lock", "expected generated files filter to exclude uv.lock")
		require.NotContains(t, diff, "go.sum", "expected generated files filter to exclude go.sum")
		require.NotContains(t, diff, ".beads/", "expected generated files filter to exclude .beads files")
	}

	t.Run("GetDiff", func(t *testing.T) {
		repo, sha := setupDiffExcludesGeneratedFilesTest(t)
		diff, err := GetDiff(repo.Dir, sha)
		require.NoError(t, err, "GetDiff failed: %v", err)
		assertExcluded(t, diff)
	})

	t.Run("GetRangeDiff", func(t *testing.T) {
		repo, _ := setupDiffExcludesGeneratedFilesTest(t)
		diff, err := GetRangeDiff(repo.Dir, "HEAD~1..HEAD")
		require.NoError(t, err, "GetRangeDiff failed: %v", err)
		assertExcluded(t, diff)
	})
}

func TestGetDiffExcludesSlashedDirectory(t *testing.T) {
	repo := NewTestRepoWithCommit(t)
	repo.WriteFile("keep.txt", "keep\n")
	repo.WriteFile("vendor/dist/bundle.js", "bundled\n")
	repo.WriteFile("vendor/dist/deep/util.js", "util\n")
	repo.CommitAll("add vendor/dist files")

	sha := repo.HeadSHA()

	t.Run("GetDiff", func(t *testing.T) {
		diff, err := GetDiff(repo.Dir, sha, "vendor/dist")
		require.NoError(t, err)
		assert.Contains(t, diff, "keep.txt")
		assert.NotContains(t, diff, "bundle.js",
			"slashed exclude should filter tracked dir contents")
		assert.NotContains(t, diff, "util.js",
			"slashed exclude should filter nested tracked files")
	})

	t.Run("GetRangeDiff", func(t *testing.T) {
		diff, err := GetRangeDiff(repo.Dir, "HEAD~1..HEAD", "vendor/dist")
		require.NoError(t, err)
		assert.Contains(t, diff, "keep.txt")
		assert.NotContains(t, diff, "bundle.js",
			"slashed exclude should filter tracked dir contents")
		assert.NotContains(t, diff, "util.js",
			"slashed exclude should filter nested tracked files")
	})
}

func TestGetDiffLimited(t *testing.T) {
	repo := NewTestRepoWithCommit(t)
	repo.WriteFile("large.txt", strings.Repeat("line\n", 20000))
	repo.CommitAll("large change")

	diff, truncated, err := GetDiffLimited(repo.Dir, repo.HeadSHA(), 1024)
	require.NoError(t, err)
	assert.True(t, truncated, "expected limited diff read to report truncation")
	assert.LessOrEqual(t, len(diff), 1024)
}

func TestGetRangeDiffLimited(t *testing.T) {
	repo := NewTestRepoWithCommit(t)
	repo.WriteFile("large.txt", strings.Repeat("line\n", 20000))
	repo.CommitAll("large change")

	diff, truncated, err := GetRangeDiffLimited(repo.Dir, "HEAD~1..HEAD", 1024)
	require.NoError(t, err)
	assert.True(t, truncated, "expected limited range diff read to report truncation")
	assert.LessOrEqual(t, len(diff), 1024)
}

func TestGetDiffLimitedTruncatesUTF8Safely(t *testing.T) {
	repo := NewTestRepoWithCommit(t)
	repo.WriteFile("large.txt", strings.Repeat("世界\n", 20000))
	repo.CommitAll("large unicode change")

	fullDiff, err := GetDiff(repo.Dir, repo.HeadSHA())
	require.NoError(t, err)

	splitAt := -1
	for i := 0; i < len(fullDiff); i++ {
		if fullDiff[i] >= utf8.RuneSelf {
			splitAt = i + 1
			break
		}
	}
	require.Positive(t, splitAt, "expected diff to contain multibyte UTF-8 content")

	diff, truncated, err := GetDiffLimited(repo.Dir, repo.HeadSHA(), splitAt)
	require.NoError(t, err)
	assert.True(t, truncated, "expected limited diff read to report truncation")
	assert.LessOrEqual(t, len(diff), splitAt)
	assert.True(t, utf8.ValidString(diff), "limited diff output should remain valid UTF-8")
}

func TestGetDiffLimitedPreservesPrefixWithEarlierInvalidBytes(t *testing.T) {
	repo := NewTestRepoWithCommit(t)
	path := filepath.Join(repo.Dir, "legacy.txt")
	content := append([]byte("latin1: \xe9\n"), []byte(strings.Repeat("世界\n", 20000))...)
	err := os.WriteFile(path, content, 0o644)
	require.NoError(t, err)
	repo.Run("add", "legacy.txt")
	repo.Run("commit", "-m", "large legacy-encoded change")

	fullDiff, err := GetDiff(repo.Dir, repo.HeadSHA())
	require.NoError(t, err)

	diffBytes := []byte(fullDiff)
	invalidIdx := strings.Index(fullDiff, "\xe9")
	require.Positive(t, invalidIdx, "expected diff to contain earlier invalid UTF-8 bytes")

	splitAt := -1
	for i := invalidIdx + 1; i < len(diffBytes); i++ {
		if diffBytes[i] >= utf8.RuneSelf && utf8.RuneStart(diffBytes[i]) {
			splitAt = i + 1
			break
		}
	}
	require.Positive(t, splitAt, "expected diff to contain multibyte UTF-8 content after invalid bytes")

	diff, truncated, err := GetDiffLimited(repo.Dir, repo.HeadSHA(), splitAt)
	require.NoError(t, err)
	assert.True(t, truncated, "expected limited diff read to report truncation")
	assert.True(t, utf8.ValidString(diff), "limited diff output should remain valid UTF-8")
	assert.Contains(t, diff, "latin1:", "sanitized limited diff should preserve the earlier valid prefix")
	assert.NotEmpty(t, diff, "sanitized limited diff should not collapse to an empty string")
}

func TestSanitizeToValidUTF8PreservesEarlierBytesWhileRepairingTail(t *testing.T) {
	input := []byte("latin1: \xe9\nunicode: ")
	input = append(input, []byte("世界")...)
	input = input[:len(input)-1] // split the final multibyte rune

	output := sanitizeToValidUTF8(input)

	assert.True(t, utf8.ValidString(output), "sanitized output should be valid UTF-8")
	assert.Contains(t, output, "latin1:", "sanitized output should preserve the earlier valid prefix")
	assert.NotEmpty(t, output, "sanitized output should not collapse to an empty string")
}

func TestGetDirtyDiffExcludesUntrackedFiles(t *testing.T) {
	t.Run("plain directory exclude", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		repo.WriteFile("new.go", "package main\n")
		repo.WriteFile("vendor/dep.go", "vendored\n")
		repo.WriteFile("vendor/sub/util.go", "util\n")

		diff, err := GetDirtyDiff(repo.Dir, "vendor")
		require.NoError(t, err)
		assert.Contains(t, diff, "new.go")
		assert.NotContains(t, diff, "vendor/dep.go")
		assert.NotContains(t, diff, "vendor/sub/util.go")
	})

	t.Run("builtin lockfiles", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		repo.WriteFile("keep.go", "package main\n")
		repo.WriteFile("sub/uv.lock", "lock\n")
		repo.WriteFile("package-lock.json", "lock\n")
		repo.WriteFile("deep/Cargo.lock", "lock\n")
		repo.WriteFile("sub/cargo.lock", "lock\n")
		repo.WriteFile("go.sum", "sum\n")

		diff, err := GetDirtyDiff(repo.Dir)
		require.NoError(t, err)
		assert.Contains(t, diff, "keep.go")
		assert.NotContains(t, diff, "uv.lock")
		assert.NotContains(t, diff, "package-lock.json")
		assert.NotContains(t, diff, "Cargo.lock")
		assert.NotContains(t, diff, "cargo.lock")
		assert.NotContains(t, diff, "go.sum")
	})

	t.Run("basename glob pattern", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		repo.WriteFile("keep.js", "ok\n")
		repo.WriteFile("app.min.js", "minified\n")
		repo.WriteFile("sub/lib.min.js", "nested\n")

		diff, err := GetDirtyDiff(repo.Dir, "*.min.js")
		require.NoError(t, err)
		assert.Contains(t, diff, "keep.js")
		assert.NotContains(t, diff, "app.min.js")
		assert.NotContains(t, diff, "lib.min.js")
	})

	t.Run("slashed rooted pattern", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		repo.WriteFile("keep.txt", "ok\n")
		repo.WriteFile("vendor/dist/bundle.js", "bundled\n")
		repo.WriteFile("vendor/dist/deep/util.js", "util\n")

		diff, err := GetDirtyDiff(repo.Dir, "vendor/dist")
		require.NoError(t, err)
		assert.Contains(t, diff, "keep.txt")
		assert.NotContains(t, diff, "bundle.js")
		assert.NotContains(t, diff, "util.js")
	})
}

func TestIsWorkingTreeClean(t *testing.T) {
	t.Run("clean tree returns true", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		require.True(t, IsWorkingTreeClean(repo.Dir), "expected clean tree for clean tree case")
	})

	t.Run("dirty tree with modified file returns false", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("initial.txt", "modified")

		require.False(t, IsWorkingTreeClean(repo.Dir), "expected modified file to make tree dirty")
	})

	t.Run("dirty tree with untracked file returns false", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("untracked.txt", "untracked")

		require.False(t, IsWorkingTreeClean(repo.Dir), "expected untracked file to make tree dirty")
	})
}

func TestResetWorkingTree(t *testing.T) {
	t.Run("resets modified files", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("initial.txt", "modified")
		assert.False(t, IsWorkingTreeClean(repo.Dir), "expected tree to be dirty before reset")

		err := ResetWorkingTree(repo.Dir)
		require.NoError(t, err, "ResetWorkingTree failed: %v", err)
		assert.True(t, IsWorkingTreeClean(repo.Dir), "expected tree to be clean after reset")

		content, err := os.ReadFile(filepath.Join(repo.Dir, "initial.txt"))
		require.NoError(t, err)
		assert.Equal(t, "initial content", string(content), "expected file content 'initial content', got %q", string(content))
	})

	t.Run("removes untracked files", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		untrackedFile := filepath.Join(repo.Dir, "untracked.txt")
		repo.WriteFile("untracked.txt", "untracked")
		assert.False(t, IsWorkingTreeClean(repo.Dir), "expected tree to be dirty before reset")

		err := ResetWorkingTree(repo.Dir)
		require.NoError(t, err, "ResetWorkingTree failed: %v", err)

		require.True(t, IsWorkingTreeClean(repo.Dir), "expected tree to be clean after reset")

		_, err = os.Stat(untrackedFile)
		require.Error(t, err, "expected untracked file to be removed after reset")
	})

	t.Run("resets staged changes", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		repo.WriteFile("initial.txt", "staged changes")
		repo.Run("add", ".")
		assert.False(t, IsWorkingTreeClean(repo.Dir), "expected tree to be dirty before reset")

		err := ResetWorkingTree(repo.Dir)
		require.NoError(t, err, "ResetWorkingTree failed: %v", err)
		assert.True(t, IsWorkingTreeClean(repo.Dir), "expected tree to be clean after reset")

		content, err := os.ReadFile(filepath.Join(repo.Dir, "initial.txt"))
		require.NoError(t, err)
		assert.Equal(t, "initial content", string(content), "expected file content 'initial content', got %q", string(content))
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
			assert.Equal(t, tt.want, got, "LocalBranchName(%q) = %q, want %q", tt.input, got, tt.want)
		})
	}
}

func setupRangeFilesChangedTest(t *testing.T) (*TestRepo, string) {
	t.Helper()
	repo := NewTestRepo(t)
	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
	repo.CommitFile("base.txt", "base", "base commit")
	baseSHA := repo.HeadSHA()

	repo.Run("checkout", "-b", "feature")
	repo.CommitFile("new.go", "package main", "add go file")
	repo.CommitFile("docs.md", "# Docs", "add docs")
	repo.CommitFile("config.yml", "key: val", "add config")

	return repo, baseSHA
}

func TestGetRangeFilesChanged(t *testing.T) {
	t.Run("returns changed files in range", func(t *testing.T) {
		repo, baseSHA := setupRangeFilesChangedTest(t)
		files, err := GetRangeFilesChanged(repo.Dir, baseSHA+"..HEAD")
		require.NoError(t, err, "GetRangeFilesChanged failed: %v", err)
		require.NoError(t, err, "expected 3 files, got %d: %v", len(files), files)

		found := map[string]bool{}
		for _, f := range files {
			found[f] = true
		}
		for _, want := range []string{"new.go", "docs.md", "config.yml"} {
			assert.True(t, found[want], "expected %s in changed files, got %v", want, files)
		}
	})

	t.Run("empty range returns nil", func(t *testing.T) {
		repo, _ := setupRangeFilesChangedTest(t)
		files, err := GetRangeFilesChanged(repo.Dir, "HEAD..HEAD")
		require.NoError(t, err, "GetRangeFilesChanged failed: %v", err)
		assert.Empty(t, files, "expected 0 files for empty range, got %d: %v", len(files), files)
	})
}

func TestCreateCommitPreCommitHookOutput(t *testing.T) {
	repo := NewTestRepoWithCommit(t)

	repo.InstallHook("pre-commit",
		"#!/bin/sh\necho 'error: trailing whitespace on line 42' >&2\nexit 1\n")

	repo.WriteFile("new.txt", "content")
	repo.Run("add", "new.txt")

	_, err := CreateCommit(repo.Dir, "should fail")
	require.Error(t, err, "expected CreateCommit to fail with pre-commit hook")

	assert.
		Contains(t, err.Error(), "trailing whitespace on line 42", "expected error to contain hook output, got: %v", err)

	var commitErr *CommitError
	require.ErrorAs(t, err, &commitErr, "expected CommitError type")
	assert.True(t, commitErr.HookFailed, "expected HookFailed=true for pre-commit hook rejection")
}

func TestCommitErrorHookFailedFalseWhenNothingToCommit(t *testing.T) {
	repo := NewTestRepoWithCommit(t)

	repo.InstallHook("pre-commit", "#!/bin/sh\nexit 0\n")

	_, err := CreateCommit(repo.Dir, "empty commit")
	require.Error(t, err, "expected CreateCommit to fail")

	var commitErr *CommitError
	require.ErrorAs(t, err, &commitErr, "expected CommitError type")
	assert.False(t, commitErr.HookFailed, "expected HookFailed=false for dry-run")
}

func TestCommitErrorHookFailedCommitMsgHook(t *testing.T) {
	repo := NewTestRepoWithCommit(t)

	repo.InstallHook("commit-msg",
		"#!/bin/sh\necho 'bad commit message format' >&2\nexit 1\n")

	repo.WriteFile("new.txt", "content")
	repo.Run("add", "new.txt")

	_, err := CreateCommit(repo.Dir, "should fail")
	require.Error(t, err, "expected CreateCommit to fail with commit-msg hook")

	var commitErr *CommitError
	require.ErrorAs(t, err, &commitErr, "expected CommitError type")
	assert.True(t, commitErr.HookFailed, "expected HookFailed=true for commit-msg hook rejection")
}

func TestCommitErrorHookFailedFalseForGPGSigningFailure(t *testing.T) {
	repo := NewTestRepoWithCommit(t)

	repo.Run("config", "commit.gpgsign", "true")

	dummyGPG := filepath.Join(repo.Dir, "fail-gpg")
	if runtime.GOOS == "windows" {
		dummyGPG += ".bat"
		repo.WriteFile("fail-gpg.bat", "@echo off\nexit /b 1\n")
	} else {
		repo.WriteFile("fail-gpg", "#!/bin/sh\nexit 1\n")
		err := os.Chmod(dummyGPG, 0755)
		require.NoError(t, err, "failed to chmod fail-gpg")
	}

	repo.Run("config", "gpg.program", dummyGPG)
	repo.Run("config", "user.signingkey", "DEADBEEF00000000")

	repo.WriteFile("new.txt", "content")
	repo.Run("add", "new.txt")

	_, err := CreateCommit(repo.Dir, "should fail from gpg")
	require.Error(t, err, "expected commit to fail due to gpg.program=false")

	var commitErr *CommitError
	require.ErrorAs(t, err, &commitErr, "expected CommitError type, got: %T", err)
	assert.False(t, commitErr.HookFailed, "HookFailed should be false for GPG signing failure (no hooks installed)")
}

func TestHasCommitHooksDetectsInstalledHooks(t *testing.T) {
	repo := NewTestRepoWithCommit(t)
	require.False(t, hasCommitHooks(repo.Dir), "expected pre-commit hook to be detected after install")

	repo.InstallHook("pre-commit", "#!/bin/sh\nexit 0\n")
	assert.True(t, hasCommitHooks(repo.Dir), "expected hasCommitHooks=true after installing pre-commit")
}

func TestHasCommitHooksIgnoresDirectories(t *testing.T) {
	repo := NewTestRepoWithCommit(t)

	hooksDir, err := GetHooksPath(repo.Dir)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(hooksDir, "pre-commit"), 0o755)
	require.NoError(t, err, "failed to create pre-commit directory")
	require.False(t, hasCommitHooks(repo.Dir), "expected directory named pre-commit not to be treated as hook file")

}

func setupAncestorTest(t *testing.T) (*TestRepo, string, string, string) {
	t.Helper()
	repo := NewTestRepo(t)
	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")

	repo.CommitFile("base.txt", "base", "base commit")
	baseSHA := repo.HeadSHA()

	repo.CommitFile("second.txt", "second", "second commit")
	secondSHA := repo.HeadSHA()

	repo.Run("checkout", baseSHA)
	repo.Run("checkout", "-b", "divergent")
	repo.CommitFile("divergent.txt", "divergent", "divergent commit")
	divergentSHA := repo.HeadSHA()

	return repo, baseSHA, secondSHA, divergentSHA
}

func TestIsAncestor(t *testing.T) {
	t.Run("base is ancestor of second", func(t *testing.T) {
		repo, baseSHA, secondSHA, _ := setupAncestorTest(t)
		isAnc, err := IsAncestor(repo.Dir, baseSHA, secondSHA)
		require.NoError(t, err, "unexpected error: %v", err)
		require.True(t, isAnc, "base is ancestor of divergent")
	})

	t.Run("second is not ancestor of base", func(t *testing.T) {
		repo, baseSHA, secondSHA, _ := setupAncestorTest(t)
		isAnc, err := IsAncestor(repo.Dir, secondSHA, baseSHA)
		require.NoError(t, err, "unexpected error: %v", err)
		assert.False(t, isAnc, "second should not be ancestor of base")
	})

	t.Run("divergent is not ancestor of second", func(t *testing.T) {
		repo, _, secondSHA, divergentSHA := setupAncestorTest(t)
		isAnc, err := IsAncestor(repo.Dir, divergentSHA, secondSHA)
		require.NoError(t, err, "unexpected error: %v", err)
		assert.False(t, isAnc, "expected divergent to NOT be ancestor of second (different branches)")
	})

	t.Run("base is ancestor of divergent", func(t *testing.T) {
		repo, baseSHA, _, divergentSHA := setupAncestorTest(t)
		isAnc, err := IsAncestor(repo.Dir, baseSHA, divergentSHA)
		require.NoError(t, err, "unexpected error: %v", err)
		require.True(t, isAnc, "commit should be ancestor of itself")
	})

	t.Run("commit is ancestor of itself", func(t *testing.T) {
		repo, baseSHA, _, _ := setupAncestorTest(t)
		isAnc, err := IsAncestor(repo.Dir, baseSHA, baseSHA)
		require.NoError(t, err, "unexpected error: %v", err)
		require.True(t, isAnc, "commit should be ancestor of itself")
	})

	t.Run("bad object returns error", func(t *testing.T) {
		repo, _, _, _ := setupAncestorTest(t)
		_, err := IsAncestor(repo.Dir, "badbadbadbadbadbadbadbadbadbadbadbadbad", "HEAD")
		require.Error(t, err, "bad object should return error")

	})
}

func TestGetPatchID(t *testing.T) {
	t.Run("stable across rebase", func(t *testing.T) {
		repo := NewTestRepo(t)

		repo.Run("checkout", "-b", "main")
		repo.CommitFile("base.txt", "base", "initial")

		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "hello", "add feature")
		sha1 := repo.HeadSHA()
		patchID1 := GetPatchID(repo.Dir, sha1)

		assert.NotEmpty(t, patchID1, "expected non-empty patch-id")

		repo.Run("checkout", "main")
		repo.CommitFile("other.txt", "other", "another commit")
		repo.Run("checkout", "feature")
		repo.Run("rebase", "main")
		sha2 := repo.HeadSHA()
		patchID2 := GetPatchID(repo.Dir, sha2)

		assert.NotEqual(t, sha1, sha2, "SHAs should differ after rebase")
		assert.Equal(t, patchID1, patchID2, "patch-ids should match: %s != %s", patchID1, patchID2)

	})

	t.Run("different for modified commits", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("a.txt", "content-a", "commit a")
		sha1 := repo.HeadSHA()

		repo.CommitFile("b.txt", "content-b", "commit b")
		sha2 := repo.HeadSHA()

		pid1 := GetPatchID(repo.Dir, sha1)
		pid2 := GetPatchID(repo.Dir, sha2)

		assert.NotEmpty(t, pid1, "expected non-empty patch-id")
		assert.NotEmpty(t, pid2, "expected non-empty patch-id")
		assert.NotEqual(t, pid1, pid2, "expected distinct patch-ids for different commits")
	})

	t.Run("empty for empty commit", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("a.txt", "content", "first")
		repo.Run("commit", "--allow-empty", "-m", "empty")
		sha := repo.HeadSHA()

		pid := GetPatchID(repo.Dir, sha)
		assert.Empty(t, pid, "expected empty patch-id for empty commit, got %s", pid)
	})
}

func TestShortRef(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"range ref", "abc1234def5678..99887766aabbcc", "abc1234..9988776"},
		{"single sha", "abc1234def5678", "abc1234"},
		{"short single", "abc", "abc"},
		{"empty", "", ""},
		{"range with short sides", "abc..def", "abc..def"},
		{"triple dot splits on first pair", "abc1234def5678...99887766aabbcc", "abc1234...99887766aabbcc"},
		{"task label passthrough", "run", "run"},
		{"dirty ref passthrough", "dirty", "dirty"},
		{"branch name passthrough", "feature/very-long-name", "feature/very-long-name"},
		{"analysis label passthrough", "duplication", "duplication"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortRef(tt.in)
			assert.Equal(t, tt.want, got, "ShortRef(%q) = %q, want %q", tt.in, got, tt.want)
		})
	}
}

func TestShortSHA(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"full sha", "abc1234def5678", "abc1234"},
		{"exactly 7", "abc1234", "abc1234"},
		{"shorter", "abc", "abc"},
		{"empty", "", ""},
		{"8 chars", "abc12345", "abc1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortSHA(tt.in)
			assert.Equal(t, tt.want, got, "ShortSHA(%q) = %q, want %q", tt.in, got, tt.want)
		})
	}
}

func TestWorktreePathForBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	evalSymlinks := func(t *testing.T, path string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(path)
		require.NoError(t, err, "EvalSymlinks(%q): %v", path, err)
		return resolved
	}

	t.Run("returns worktree dir for branch checked out in worktree", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		wt := repo.AddWorktree("feature-x")

		got, checkedOut, err := WorktreePathForBranch(repo.Dir, "feature-x")
		require.NoError(t, err, "unexpected error: %v", err)
		got = evalSymlinks(t, got)
		want := evalSymlinks(t, wt.Dir)
		assert.Equal(t, want, got, "WorktreePathForBranch() path = %q, want %q", got, want)
		assert.True(t, checkedOut, "WorktreePathForBranch() checkedOut = false, want true")
	})

	t.Run("returns repoPath and false when branch has no worktree", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		repo.Run("branch", "other-branch")

		got, checkedOut, err := WorktreePathForBranch(repo.Dir, "other-branch")
		require.NoError(t, err, "unexpected error: %v", err)
		assert.Equal(t, repo.Dir, got, "WorktreePathForBranch() path = %q, want %q", got, repo.Dir)
		assert.False(t, checkedOut, "WorktreePathForBranch() checkedOut = true, want false")
	})

	t.Run("returns repoPath and true for empty branch", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)

		got, checkedOut, err := WorktreePathForBranch(repo.Dir, "")
		require.NoError(t, err, "unexpected error: %v", err)
		assert.Equal(t, repo.Dir, got, "WorktreePathForBranch() path = %q, want %q", got, repo.Dir)
		assert.True(t, checkedOut, "WorktreePathForBranch() checkedOut = false, want true")
	})

	t.Run("returns main repo dir for branch checked out in main worktree", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		branch := GetCurrentBranch(repo.Dir)

		got, checkedOut, err := WorktreePathForBranch(repo.Dir, branch)
		require.NoError(t, err, "unexpected error: %v", err)
		got = evalSymlinks(t, got)
		want := evalSymlinks(t, repo.Dir)
		assert.Equal(t, want, got, "WorktreePathForBranch() path = %q, want %q", got, want)
		assert.True(t, checkedOut, "WorktreePathForBranch() checkedOut = false, want true")
	})

	t.Run("returns error for invalid repo path", func(t *testing.T) {
		_, _, err := WorktreePathForBranch("/nonexistent/repo", "main")
		require.Error(t, err, "expected error for invalid repo path, got nil")

	})

	t.Run("git worktree add succeeds on pre-existing empty directory", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		repo.Run("branch", "wt-preexist")

		wtDir := t.TempDir()
		repo.Run("worktree", "add", wtDir, "wt-preexist")
		t.Cleanup(func() {
			rmCmd := exec.Command("git", "-C", repo.Dir, "worktree", "remove", wtDir)
			_ = rmCmd.Run()
		})

		_, statErr := os.Stat(filepath.Join(wtDir, "initial.txt"))
		require.NoError(t, statErr, "expected initial.txt in worktree")
	})

	t.Run("skips stale worktree whose directory was deleted", func(t *testing.T) {
		repo := NewTestRepoWithCommit(t)
		wt := repo.AddWorktree("stale-branch")
		wtDir := wt.Dir

		os.RemoveAll(wtDir)

		got, checkedOut, err := WorktreePathForBranch(repo.Dir, "stale-branch")
		require.NoError(t, err, "unexpected error: %v", err)

		if checkedOut {
			_, statErr := os.Stat(got)
			require.NoError(t, statErr, "WorktreePathForBranch() returned checkedOut=true for stale path %q that doesn't exist", got)
		}
	})
}
