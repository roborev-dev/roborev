package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetHooksPath(t *testing.T) {
	// Create a temp git repo
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	t.Run("default hooks path", func(t *testing.T) {
		hooksPath, err := GetHooksPath(tmpDir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		// Should be absolute
		if !filepath.IsAbs(hooksPath) {
			t.Errorf("hooks path should be absolute, got: %s", hooksPath)
		}

		// Should end with .git/hooks (normalize for cross-platform)
		cleanPath := filepath.Clean(hooksPath)
		expectedSuffix := filepath.Join(".git", "hooks")
		if !strings.HasSuffix(cleanPath, expectedSuffix) {
			t.Errorf("hooks path should end with %s, got: %s", expectedSuffix, cleanPath)
		}

		// Should be under tmpDir (use filepath.Rel for robust check)
		rel, err := filepath.Rel(tmpDir, hooksPath)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			t.Errorf("hooks path should be under %s, got: %s", tmpDir, hooksPath)
		}
	})

	t.Run("custom core.hooksPath absolute", func(t *testing.T) {
		// Create a custom hooks directory
		customHooksDir := filepath.Join(tmpDir, "my-hooks")
		if err := os.MkdirAll(customHooksDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Set core.hooksPath to absolute path
		cmd := exec.Command("git", "config", "core.hooksPath", customHooksDir)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config failed: %v\n%s", err, out)
		}

		hooksPath, err := GetHooksPath(tmpDir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		// Should return the custom absolute path
		if hooksPath != customHooksDir {
			t.Errorf("expected %s, got %s", customHooksDir, hooksPath)
		}

		// Reset for other tests
		cmd = exec.Command("git", "config", "--unset", "core.hooksPath")
		cmd.Dir = tmpDir
		cmd.Run() // ignore error if not set
	})

	t.Run("custom core.hooksPath relative", func(t *testing.T) {
		// Set core.hooksPath to relative path
		cmd := exec.Command("git", "config", "core.hooksPath", "custom-hooks")
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config failed: %v\n%s", err, out)
		}

		hooksPath, err := GetHooksPath(tmpDir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		// Should be made absolute
		if !filepath.IsAbs(hooksPath) {
			t.Errorf("hooks path should be absolute, got: %s", hooksPath)
		}

		// Should resolve to tmpDir/custom-hooks
		expected := filepath.Join(tmpDir, "custom-hooks")
		if hooksPath != expected {
			t.Errorf("expected %s, got %s", expected, hooksPath)
		}
	})
}

func TestIsRebaseInProgress(t *testing.T) {
	// Create a temp git repo
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Configure git user for commits
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	t.Run("no rebase", func(t *testing.T) {
		if IsRebaseInProgress(tmpDir) {
			t.Error("expected no rebase in progress")
		}
	})

	t.Run("rebase-merge directory", func(t *testing.T) {
		// Simulate interactive rebase by creating rebase-merge directory
		rebaseMerge := filepath.Join(tmpDir, ".git", "rebase-merge")
		if err := os.MkdirAll(rebaseMerge, 0755); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(rebaseMerge)

		if !IsRebaseInProgress(tmpDir) {
			t.Error("expected rebase in progress with rebase-merge")
		}
	})

	t.Run("rebase-apply directory", func(t *testing.T) {
		// Simulate git am / regular rebase by creating rebase-apply directory
		rebaseApply := filepath.Join(tmpDir, ".git", "rebase-apply")
		if err := os.MkdirAll(rebaseApply, 0755); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(rebaseApply)

		if !IsRebaseInProgress(tmpDir) {
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
		// Create initial commit so we can create a worktree
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", tmpDir, "add", ".").Run()
		exec.Command("git", "-C", tmpDir, "commit", "-m", "initial").Run()

		// Create a worktree
		worktreeDir := t.TempDir()
		cmd := exec.Command("git", "-C", tmpDir, "worktree", "add", worktreeDir, "-b", "test-branch")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add failed: %v\n%s", err, out)
		}
		defer exec.Command("git", "-C", tmpDir, "worktree", "remove", worktreeDir).Run()

		// Verify worktree has .git file (not directory)
		gitPath := filepath.Join(worktreeDir, ".git")
		info, err := os.Stat(gitPath)
		if err != nil {
			t.Fatalf("worktree .git not found: %v", err)
		}
		if info.IsDir() {
			t.Skip("worktree has .git directory instead of file - older git version")
		}

		// No rebase in worktree
		if IsRebaseInProgress(worktreeDir) {
			t.Error("expected no rebase in worktree")
		}

		// Get the actual gitdir for the worktree to simulate rebase
		gitDirCmd := exec.Command("git", "-C", worktreeDir, "rev-parse", "--git-dir")
		gitDirOut, err := gitDirCmd.Output()
		if err != nil {
			t.Fatalf("git rev-parse --git-dir failed: %v", err)
		}
		worktreeGitDir := strings.TrimSpace(string(gitDirOut))
		if !filepath.IsAbs(worktreeGitDir) {
			worktreeGitDir = filepath.Join(worktreeDir, worktreeGitDir)
		}

		// Simulate rebase in worktree
		rebaseMerge := filepath.Join(worktreeGitDir, "rebase-merge")
		if err := os.MkdirAll(rebaseMerge, 0755); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(rebaseMerge)

		if !IsRebaseInProgress(worktreeDir) {
			t.Error("expected rebase in progress in worktree")
		}
	})
}

func TestGetMainRepoRoot(t *testing.T) {
	// Create a temp git repo
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Configure git user for commits
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	t.Run("regular repo returns same as GetRepoRoot", func(t *testing.T) {
		mainRoot, err := GetMainRepoRoot(tmpDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot failed: %v", err)
		}

		repoRoot, err := GetRepoRoot(tmpDir)
		if err != nil {
			t.Fatalf("GetRepoRoot failed: %v", err)
		}

		if mainRoot != repoRoot {
			t.Errorf("GetMainRepoRoot returned %s, expected %s (same as GetRepoRoot)", mainRoot, repoRoot)
		}
	})

	t.Run("worktree returns main repo root", func(t *testing.T) {
		// Create initial commit so we can create a worktree
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", tmpDir, "add", ".").Run()
		exec.Command("git", "-C", tmpDir, "commit", "-m", "initial").Run()

		// Create a worktree
		worktreeDir := t.TempDir()
		cmd := exec.Command("git", "-C", tmpDir, "worktree", "add", worktreeDir, "-b", "worktree-branch")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add failed: %v\n%s", err, out)
		}
		defer exec.Command("git", "-C", tmpDir, "worktree", "remove", worktreeDir).Run()

		// GetRepoRoot from worktree returns the worktree path
		worktreeRoot, err := GetRepoRoot(worktreeDir)
		if err != nil {
			t.Fatalf("GetRepoRoot on worktree failed: %v", err)
		}

		// Worktree root should be different from main repo
		mainRepoRoot, err := GetRepoRoot(tmpDir)
		if err != nil {
			t.Fatalf("GetRepoRoot on main repo failed: %v", err)
		}

		if worktreeRoot == mainRepoRoot {
			t.Skip("worktree root equals main repo root - older git version")
		}

		// GetMainRepoRoot from worktree should return the main repo root
		mainRoot, err := GetMainRepoRoot(worktreeDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on worktree failed: %v", err)
		}

		if mainRoot != mainRepoRoot {
			t.Errorf("GetMainRepoRoot on worktree returned %s, expected %s", mainRoot, mainRepoRoot)
		}
	})

	t.Run("non-repo returns error", func(t *testing.T) {
		nonRepo := t.TempDir()
		_, err := GetMainRepoRoot(nonRepo)
		if err == nil {
			t.Error("expected error for non-repo")
		}
	})

	t.Run("submodule stays distinct from parent", func(t *testing.T) {
		// Create parent repo
		parentDir := t.TempDir()
		cmd := exec.Command("git", "init")
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init parent failed: %v\n%s", err, out)
		}
		exec.Command("git", "-C", parentDir, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", parentDir, "config", "user.name", "Test").Run()
		// Allow file:// protocol for submodule clone in test environment
		exec.Command("git", "-C", parentDir, "config", "protocol.file.allow", "always").Run()

		// Create initial commit in parent
		if err := os.WriteFile(filepath.Join(parentDir, "parent.txt"), []byte("parent"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", parentDir, "add", ".").Run()
		exec.Command("git", "-C", parentDir, "commit", "-m", "parent initial").Run()

		// Create a separate repo to use as submodule source
		subSourceDir := t.TempDir()
		cmd = exec.Command("git", "init")
		cmd.Dir = subSourceDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init sub source failed: %v\n%s", err, out)
		}
		exec.Command("git", "-C", subSourceDir, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", subSourceDir, "config", "user.name", "Test").Run()
		if err := os.WriteFile(filepath.Join(subSourceDir, "sub.txt"), []byte("sub"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", subSourceDir, "add", ".").Run()
		exec.Command("git", "-C", subSourceDir, "commit", "-m", "sub initial").Run()

		// Add submodule to parent
		cmd = exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", subSourceDir, "mysub")
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git submodule add failed: %v\n%s", err, out)
		}

		submoduleDir := filepath.Join(parentDir, "mysub")
		// Resolve symlinks for comparison (macOS: /var -> /private/var)
		submoduleDirResolved, _ := filepath.EvalSymlinks(submoduleDir)
		if submoduleDirResolved == "" {
			submoduleDirResolved = submoduleDir
		}

		// GetMainRepoRoot on submodule should return submodule path, not parent
		subRoot, err := GetMainRepoRoot(submoduleDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on submodule failed: %v", err)
		}

		parentRoot, err := GetMainRepoRoot(parentDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on parent failed: %v", err)
		}

		if subRoot == parentRoot {
			t.Errorf("submodule root should be distinct from parent: sub=%s parent=%s", subRoot, parentRoot)
		}

		// Submodule root should be the submodule directory (compare resolved paths)
		subRootResolved, _ := filepath.EvalSymlinks(subRoot)
		if subRootResolved == "" {
			subRootResolved = subRoot
		}
		if subRootResolved != submoduleDirResolved {
			t.Errorf("GetMainRepoRoot on submodule returned %s, expected %s", subRoot, submoduleDir)
		}
	})

	t.Run("worktree from submodule returns submodule root", func(t *testing.T) {
		// This tests the scenario where a worktree is created from within a submodule.
		// The worktree should resolve to the submodule's root, not the parent repo.

		// Create parent repo
		parentDir := t.TempDir()
		cmd := exec.Command("git", "init")
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init parent failed: %v\n%s", err, out)
		}
		exec.Command("git", "-C", parentDir, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", parentDir, "config", "user.name", "Test").Run()
		exec.Command("git", "-C", parentDir, "config", "protocol.file.allow", "always").Run()

		// Create initial commit in parent
		if err := os.WriteFile(filepath.Join(parentDir, "parent.txt"), []byte("parent"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", parentDir, "add", ".").Run()
		exec.Command("git", "-C", parentDir, "commit", "-m", "parent initial").Run()

		// Create a separate repo to use as submodule source
		subSourceDir := t.TempDir()
		cmd = exec.Command("git", "init")
		cmd.Dir = subSourceDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init sub source failed: %v\n%s", err, out)
		}
		exec.Command("git", "-C", subSourceDir, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", subSourceDir, "config", "user.name", "Test").Run()
		if err := os.WriteFile(filepath.Join(subSourceDir, "sub.txt"), []byte("sub"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", subSourceDir, "add", ".").Run()
		exec.Command("git", "-C", subSourceDir, "commit", "-m", "sub initial").Run()

		// Add submodule to parent
		cmd = exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", subSourceDir, "mysub")
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git submodule add failed: %v\n%s", err, out)
		}
		exec.Command("git", "-C", parentDir, "add", ".").Run()
		exec.Command("git", "-C", parentDir, "commit", "-m", "add submodule").Run()

		submoduleDir := filepath.Join(parentDir, "mysub")

		// Create a worktree from within the submodule
		worktreeDir := t.TempDir()
		cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "sub-wt-branch")
		cmd.Dir = submoduleDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add from submodule failed: %v\n%s", err, out)
		}
		defer exec.Command("git", "-C", submoduleDir, "worktree", "remove", worktreeDir).Run()

		// GetMainRepoRoot from the submodule's worktree should return the submodule root
		wtRoot, err := GetMainRepoRoot(worktreeDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on submodule worktree failed: %v", err)
		}

		// It should match the submodule directory, not parent or .git/modules path
		subRoot, err := GetMainRepoRoot(submoduleDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on submodule failed: %v", err)
		}

		if wtRoot != subRoot {
			t.Errorf("worktree from submodule should return submodule root: wt=%s sub=%s", wtRoot, subRoot)
		}

		// It should NOT return the parent repo root
		parentRoot, err := GetMainRepoRoot(parentDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on parent failed: %v", err)
		}

		if wtRoot == parentRoot {
			t.Errorf("worktree from submodule should NOT return parent root: wt=%s parent=%s", wtRoot, parentRoot)
		}

		// The root should be an actual directory that exists
		if info, err := os.Stat(wtRoot); err != nil || !info.IsDir() {
			t.Errorf("GetMainRepoRoot returned invalid path: %s", wtRoot)
		}
	})

	t.Run("worktree HEAD resolves to worktree branch", func(t *testing.T) {
		// Create main repo with initial commit
		mainDir := t.TempDir()
		cmd := exec.Command("git", "init")
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init failed: %v\n%s", err, out)
		}
		exec.Command("git", "-C", mainDir, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", mainDir, "config", "user.name", "Test").Run()

		if err := os.WriteFile(filepath.Join(mainDir, "file.txt"), []byte("v1"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", mainDir, "add", ".").Run()
		exec.Command("git", "-C", mainDir, "commit", "-m", "commit1").Run()

		// Get main branch HEAD
		mainHead, err := ResolveSHA(mainDir, "HEAD")
		if err != nil {
			t.Fatalf("ResolveSHA main HEAD failed: %v", err)
		}

		// Create worktree on new branch
		worktreeDir := t.TempDir()
		cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "wt-branch")
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add failed: %v\n%s", err, out)
		}
		defer exec.Command("git", "-C", mainDir, "worktree", "remove", worktreeDir).Run()

		// Make a new commit in the worktree
		if err := os.WriteFile(filepath.Join(worktreeDir, "file.txt"), []byte("v2"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", worktreeDir, "add", ".").Run()
		exec.Command("git", "-C", worktreeDir, "commit", "-m", "commit2").Run()

		// Get worktree HEAD
		wtHead, err := ResolveSHA(worktreeDir, "HEAD")
		if err != nil {
			t.Fatalf("ResolveSHA worktree HEAD failed: %v", err)
		}

		// Worktree HEAD should differ from main HEAD
		if wtHead == mainHead {
			t.Error("worktree HEAD should differ from main HEAD after new commit")
		}

		// Resolving HEAD from main dir should still return main HEAD
		mainHeadAgain, err := ResolveSHA(mainDir, "HEAD")
		if err != nil {
			t.Fatalf("ResolveSHA main HEAD again failed: %v", err)
		}
		if mainHeadAgain != mainHead {
			t.Errorf("main HEAD changed unexpectedly: was %s, now %s", mainHead, mainHeadAgain)
		}

		// But GetMainRepoRoot should return the same for both
		mainRoot, _ := GetMainRepoRoot(mainDir)
		wtRoot, _ := GetMainRepoRoot(worktreeDir)
		if mainRoot != wtRoot {
			t.Errorf("GetMainRepoRoot should return same root: main=%s wt=%s", mainRoot, wtRoot)
		}
	})
}

func TestGetCommitInfo(t *testing.T) {
	// Create a temp git repo
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Configure git user for commits
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test Author").Run()

	t.Run("commit with subject only", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", tmpDir, "add", ".").Run()
		exec.Command("git", "-C", tmpDir, "commit", "-m", "Simple subject").Run()

		sha, _ := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD").Output()
		commitSHA := strings.TrimSpace(string(sha))

		info, err := GetCommitInfo(tmpDir, commitSHA)
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
		if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("content2"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", tmpDir, "add", ".").Run()

		// Create commit with multi-line message
		commitMsg := "Subject line\n\nThis is the body.\nIt has multiple lines.\n\nAnd paragraphs."
		cmd := exec.Command("git", "-C", tmpDir, "commit", "-m", commitMsg)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit failed: %v\n%s", err, out)
		}

		sha, _ := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD").Output()
		commitSHA := strings.TrimSpace(string(sha))

		info, err := GetCommitInfo(tmpDir, commitSHA)
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
		if err := os.WriteFile(filepath.Join(tmpDir, "file3.txt"), []byte("content3"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", tmpDir, "add", ".").Run()

		// Create commit with pipe characters (tests delimiter handling)
		commitMsg := "Fix bug | important\n\nDetails: foo | bar | baz"
		cmd := exec.Command("git", "-C", tmpDir, "commit", "-m", commitMsg)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit failed: %v\n%s", err, out)
		}

		sha, _ := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD").Output()
		commitSHA := strings.TrimSpace(string(sha))

		info, err := GetCommitInfo(tmpDir, commitSHA)
		if err != nil {
			t.Fatalf("GetCommitInfo failed: %v", err)
		}

		// Subject should include the pipe
		if !strings.Contains(info.Subject, "|") {
			t.Errorf("expected subject to contain pipe, got '%s'", info.Subject)
		}
		// Body should include pipes
		if !strings.Contains(info.Body, "foo | bar") {
			t.Errorf("expected body to contain 'foo | bar', got '%s'", info.Body)
		}
	})
}

func TestGetBranchName(t *testing.T) {
	// Create a temp git repo
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Configure git user for commits
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	// Create initial commit on main/master
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", tmpDir, "add", ".").Run()
	exec.Command("git", "-C", tmpDir, "commit", "-m", "initial").Run()

	// Get the commit SHA
	shaCmd := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD")
	shaOut, err := shaCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD failed: %v", err)
	}
	commitSHA := strings.TrimSpace(string(shaOut))

	// Get current branch name
	branchCmd := exec.Command("git", "-C", tmpDir, "rev-parse", "--abbrev-ref", "HEAD")
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --abbrev-ref HEAD failed: %v", err)
	}
	expectedBranch := strings.TrimSpace(string(branchOut))

	t.Run("valid commit on branch", func(t *testing.T) {
		branch := GetBranchName(tmpDir, commitSHA)
		if branch != expectedBranch {
			t.Errorf("expected %s, got %s", expectedBranch, branch)
		}
	})

	t.Run("commit behind branch head", func(t *testing.T) {
		// Create another commit
		if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("content2"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", tmpDir, "add", ".").Run()
		exec.Command("git", "-C", tmpDir, "commit", "-m", "second").Run()

		// Original commit should now show as branch~1
		branch := GetBranchName(tmpDir, commitSHA)
		expected := expectedBranch + "~1"
		if branch != expected {
			t.Errorf("expected %s, got %s", expected, branch)
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
		branch := GetBranchName(tmpDir, "0000000000000000000000000000000000000000")
		if branch != "" {
			t.Errorf("expected empty string, got %s", branch)
		}
	})
}
