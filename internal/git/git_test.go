package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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

func TestNormalizeMSYSPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string // Expected on Windows; on other platforms we just check FromSlash behavior
	}{
		{"forward slash path", "C:/Users/test", "C:" + string(filepath.Separator) + "Users" + string(filepath.Separator) + "test"},
		{"MSYS lowercase drive", "/c/Users/test", ""},    // Platform-specific expected
		{"MSYS uppercase drive", "/C/Users/test", ""},    // Platform-specific expected
		{"Unix absolute path", "/home/user/repo", ""},    // Platform-specific expected
		{"relative path", "some/path", "some" + string(filepath.Separator) + "path"},
		{"with trailing newline", "C:/Users/test\n", "C:" + string(filepath.Separator) + "Users" + string(filepath.Separator) + "test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeMSYSPath(tt.input)

			switch tt.name {
			case "MSYS lowercase drive":
				if runtime.GOOS == "windows" {
					if result != "C:\\Users\\test" {
						t.Errorf("Expected C:\\Users\\test, got %s", result)
					}
				} else {
					// On non-Windows, /c/Users/test stays as-is (just separator change)
					if result != "/c/Users/test" {
						t.Errorf("Expected /c/Users/test, got %s", result)
					}
				}
			case "MSYS uppercase drive":
				if runtime.GOOS == "windows" {
					if result != "C:\\Users\\test" {
						t.Errorf("Expected C:\\Users\\test, got %s", result)
					}
				} else {
					if result != "/C/Users/test" {
						t.Errorf("Expected /C/Users/test, got %s", result)
					}
				}
			case "Unix absolute path":
				if runtime.GOOS == "windows" {
					// /home is not a drive letter pattern, so stays as \home
					if result != "\\home\\user\\repo" {
						t.Errorf("Expected \\home\\user\\repo, got %s", result)
					}
				} else {
					if result != "/home/user/repo" {
						t.Errorf("Expected /home/user/repo, got %s", result)
					}
				}
			default:
				if result != tt.expected {
					t.Errorf("Expected %s, got %s", tt.expected, result)
				}
			}
		})
	}
}

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

func TestGetDefaultBranchOriginHead(t *testing.T) {
	bareRepo := t.TempDir()
	runGit(t, bareRepo, "init", "--bare")
	runGit(t, bareRepo, "symbolic-ref", "HEAD", "refs/heads/main")

	seedRepo := t.TempDir()
	runGit(t, seedRepo, "init")
	runGit(t, seedRepo, "checkout", "-b", "main")
	runGit(t, seedRepo, "config", "user.email", "test@test.com")
	runGit(t, seedRepo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seedRepo, "file.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedRepo, "add", "file.txt")
	runGit(t, seedRepo, "commit", "-m", "initial")
	runGit(t, seedRepo, "remote", "add", "origin", bareRepo)
	runGit(t, seedRepo, "push", "-u", "origin", "main")

	t.Run("missing local branch uses origin ref", func(t *testing.T) {
		cloneRepo := t.TempDir()
		runGit(t, "", "clone", bareRepo, cloneRepo)
		runGit(t, cloneRepo, "remote", "set-head", "origin", "-a")
		runGit(t, cloneRepo, "checkout", "--detach")
		runGit(t, cloneRepo, "branch", "-D", "main")

		branch, err := GetDefaultBranch(cloneRepo)
		if err != nil {
			t.Fatalf("GetDefaultBranch failed: %v", err)
		}
		if branch != "origin/main" {
			t.Fatalf("expected origin/main, got %s", branch)
		}
	})

	t.Run("stale local branch uses origin ref", func(t *testing.T) {
		cloneRepo := t.TempDir()
		runGit(t, "", "clone", bareRepo, cloneRepo)
		runGit(t, cloneRepo, "remote", "set-head", "origin", "-a")

		if err := os.WriteFile(filepath.Join(seedRepo, "file2.txt"), []byte("new"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, seedRepo, "add", "file2.txt")
		runGit(t, seedRepo, "commit", "-m", "update")
		runGit(t, seedRepo, "push")
		runGit(t, cloneRepo, "fetch", "origin")

		branch, err := GetDefaultBranch(cloneRepo)
		if err != nil {
			t.Fatalf("GetDefaultBranch failed: %v", err)
		}
		if branch != "origin/main" {
			t.Fatalf("expected origin/main, got %s", branch)
		}
	})

	t.Run("origin/HEAD points to missing remote ref, falls back to local branch", func(t *testing.T) {
		cloneRepo := t.TempDir()
		runGit(t, "", "clone", bareRepo, cloneRepo)
		runGit(t, cloneRepo, "remote", "set-head", "origin", "-a")

		// Delete the remote-tracking branch while keeping origin/HEAD symbolic ref intact
		// This simulates a scenario where origin/HEAD exists but points to a missing ref
		runGit(t, cloneRepo, "update-ref", "-d", "refs/remotes/origin/main")

		// Local main branch should still exist
		localBranchOut := runGit(t, cloneRepo, "rev-parse", "--verify", "main")
		if localBranchOut == "" {
			t.Fatal("expected local main branch to exist")
		}

		// GetDefaultBranch should fall back to the local branch
		branch, err := GetDefaultBranch(cloneRepo)
		if err != nil {
			t.Fatalf("GetDefaultBranch failed: %v", err)
		}
		if branch != "main" {
			t.Fatalf("expected main (local branch fallback), got %s", branch)
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

		// Original commit should still return just the branch name (suffix stripped)
		branch := GetBranchName(tmpDir, commitSHA)
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
		branch := GetBranchName(tmpDir, "0000000000000000000000000000000000000000")
		if branch != "" {
			t.Errorf("expected empty string, got %s", branch)
		}
	})
}

func TestGetCurrentBranch(t *testing.T) {
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

	// Create initial commit
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", tmpDir, "add", ".").Run()
	exec.Command("git", "-C", tmpDir, "commit", "-m", "initial").Run()

	t.Run("returns current branch", func(t *testing.T) {
		// Get expected branch name
		branchCmd := exec.Command("git", "-C", tmpDir, "rev-parse", "--abbrev-ref", "HEAD")
		branchOut, err := branchCmd.Output()
		if err != nil {
			t.Fatalf("git rev-parse --abbrev-ref HEAD failed: %v", err)
		}
		expectedBranch := strings.TrimSpace(string(branchOut))

		branch := GetCurrentBranch(tmpDir)
		if branch != expectedBranch {
			t.Errorf("expected %s, got %s", expectedBranch, branch)
		}
	})

	t.Run("returns branch after checkout", func(t *testing.T) {
		// Create and checkout a new branch
		exec.Command("git", "-C", tmpDir, "checkout", "-b", "feature-branch").Run()

		branch := GetCurrentBranch(tmpDir)
		if branch != "feature-branch" {
			t.Errorf("expected 'feature-branch', got %s", branch)
		}
	})

	t.Run("returns empty for detached HEAD", func(t *testing.T) {
		// Get current commit SHA
		shaCmd := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD")
		shaOut, _ := shaCmd.Output()
		sha := strings.TrimSpace(string(shaOut))

		// Detach HEAD
		exec.Command("git", "-C", tmpDir, "checkout", sha).Run()

		branch := GetCurrentBranch(tmpDir)
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
	tmpDir := t.TempDir()

	// Initialize git repo
	exec.Command("git", "-C", tmpDir, "init").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	// Create initial commit
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", tmpDir, "add", ".").Run()
	exec.Command("git", "-C", tmpDir, "commit", "-m", "initial").Run()

	t.Run("no changes", func(t *testing.T) {
		hasChanges, err := HasUncommittedChanges(tmpDir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if hasChanges {
			t.Error("expected no uncommitted changes")
		}
	})

	t.Run("staged changes", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("modified"), 0644); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", tmpDir, "add", ".").Run()
		defer func() {
			exec.Command("git", "-C", tmpDir, "checkout", "file.txt").Run()
		}()

		hasChanges, err := HasUncommittedChanges(tmpDir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if !hasChanges {
			t.Error("expected uncommitted changes for staged file")
		}
	})

	t.Run("unstaged changes", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("unstaged"), 0644); err != nil {
			t.Fatal(err)
		}
		defer func() {
			exec.Command("git", "-C", tmpDir, "checkout", "file.txt").Run()
		}()

		hasChanges, err := HasUncommittedChanges(tmpDir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if !hasChanges {
			t.Error("expected uncommitted changes for unstaged file")
		}
	})

	t.Run("untracked file", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tmpDir, "untracked.txt"), []byte("new"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(tmpDir, "untracked.txt"))

		hasChanges, err := HasUncommittedChanges(tmpDir)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if !hasChanges {
			t.Error("expected uncommitted changes for untracked file")
		}
	})
}

func TestGetDirtyDiff(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize git repo
	exec.Command("git", "-C", tmpDir, "init").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	// Create initial commit
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", tmpDir, "add", ".").Run()
	exec.Command("git", "-C", tmpDir, "commit", "-m", "initial").Run()

	t.Run("includes tracked file changes", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("modified\n"), 0644); err != nil {
			t.Fatal(err)
		}
		defer func() {
			exec.Command("git", "-C", tmpDir, "checkout", "file.txt").Run()
		}()

		diff, err := GetDirtyDiff(tmpDir)
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
		if err := os.WriteFile(filepath.Join(tmpDir, "newfile.txt"), []byte("new content\n"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(tmpDir, "newfile.txt"))

		diff, err := GetDirtyDiff(tmpDir)
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
		// Modify tracked file
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("changed\n"), 0644); err != nil {
			t.Fatal(err)
		}
		// Create untracked file
		if err := os.WriteFile(filepath.Join(tmpDir, "another.txt"), []byte("another\n"), 0644); err != nil {
			t.Fatal(err)
		}
		defer func() {
			exec.Command("git", "-C", tmpDir, "checkout", "file.txt").Run()
			os.Remove(filepath.Join(tmpDir, "another.txt"))
		}()

		diff, err := GetDirtyDiff(tmpDir)
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
		// Create a binary file (contains null byte)
		if err := os.WriteFile(filepath.Join(tmpDir, "binary.bin"), []byte("hello\x00world"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(tmpDir, "binary.bin"))

		diff, err := GetDirtyDiff(tmpDir)
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
	tmpDir := t.TempDir()

	// Initialize git repo WITHOUT any commits
	exec.Command("git", "-C", tmpDir, "init").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	// Add a file but don't commit
	if err := os.WriteFile(filepath.Join(tmpDir, "newfile.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", tmpDir, "add", ".").Run()

	// Also create an untracked file
	if err := os.WriteFile(filepath.Join(tmpDir, "untracked.txt"), []byte("untracked\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diff, err := GetDirtyDiff(tmpDir)
	if err != nil {
		t.Fatalf("GetDirtyDiff failed on repo with no commits: %v", err)
	}

	// Should include the staged file
	if !strings.Contains(diff, "newfile.txt") {
		t.Error("expected diff to contain newfile.txt (staged)")
	}

	// Should include untracked file
	if !strings.Contains(diff, "untracked.txt") {
		t.Error("expected diff to contain untracked.txt")
	}
}

func TestGetDirtyDiffStagedThenDeleted(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize git repo WITHOUT any commits
	exec.Command("git", "-C", tmpDir, "init").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	// Create and stage a file
	filePath := filepath.Join(tmpDir, "staged.txt")
	if err := os.WriteFile(filePath, []byte("staged content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", tmpDir, "add", "staged.txt").Run()

	// Delete the file from the working tree (but keep it staged)
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	diff, err := GetDirtyDiff(tmpDir)
	if err != nil {
		t.Fatalf("GetDirtyDiff failed: %v", err)
	}

	// Should still show the staged file (index-only entry)
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
	tmpDir := setupTestGitRepo(t)

	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".beads", "notes.md"), []byte("beads\n"), 0644); err != nil {
		t.Fatalf("write .beads file failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "uv.lock"), []byte("lock\n"), 0644); err != nil {
		t.Fatalf("write uv.lock failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "go.sum"), []byte("sum\n"), 0644); err != nil {
		t.Fatalf("write go.sum failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "keep.txt"), []byte("keep\n"), 0644); err != nil {
		t.Fatalf("write keep.txt failed: %v", err)
	}

	if out, err := exec.Command("git", "-C", tmpDir, "add", ".").CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", tmpDir, "commit", "-m", "add files").CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	shaOut, err := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse failed: %v", err)
	}
	sha := strings.TrimSpace(string(shaOut))

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
		diff, err := GetDiff(tmpDir, sha)
		if err != nil {
			t.Fatalf("GetDiff failed: %v", err)
		}
		assertExcluded(t, diff)
	})

	t.Run("GetRangeDiff", func(t *testing.T) {
		diff, err := GetRangeDiff(tmpDir, "HEAD~1..HEAD")
		if err != nil {
			t.Fatalf("GetRangeDiff failed: %v", err)
		}
		assertExcluded(t, diff)
	})
}

// setupTestGitRepo creates a git repo with an initial commit and returns the path.
// It fatals on any setup error to ensure test reliability.
func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Configure git identity
	if out, err := exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").CombinedOutput(); err != nil {
		t.Fatalf("git config user.email failed: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatalf("git config user.name failed: %v\n%s", err, out)
	}

	// Create initial commit so we have HEAD
	testFile := filepath.Join(tmpDir, "initial.txt")
	if err := os.WriteFile(testFile, []byte("initial content"), 0644); err != nil {
		t.Fatalf("write initial file failed: %v", err)
	}
	if out, err := exec.Command("git", "-C", tmpDir, "add", ".").CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", tmpDir, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	return tmpDir
}

func TestIsWorkingTreeClean(t *testing.T) {
	t.Run("clean tree returns true", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t)

		if !IsWorkingTreeClean(tmpDir) {
			t.Error("expected clean tree to return true")
		}
	})

	t.Run("dirty tree with modified file returns false", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t)

		// Modify the file
		testFile := filepath.Join(tmpDir, "initial.txt")
		if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
			t.Fatal(err)
		}

		if IsWorkingTreeClean(tmpDir) {
			t.Error("expected dirty tree with modified file to return false")
		}
	})

	t.Run("dirty tree with untracked file returns false", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t)

		// Add untracked file
		untrackedFile := filepath.Join(tmpDir, "untracked.txt")
		if err := os.WriteFile(untrackedFile, []byte("untracked"), 0644); err != nil {
			t.Fatal(err)
		}

		if IsWorkingTreeClean(tmpDir) {
			t.Error("expected dirty tree with untracked file to return false")
		}
	})
}

func TestResetWorkingTree(t *testing.T) {
	t.Run("resets modified files", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t)

		// Modify the file
		testFile := filepath.Join(tmpDir, "initial.txt")
		if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
			t.Fatal(err)
		}

		// Verify dirty
		if IsWorkingTreeClean(tmpDir) {
			t.Fatal("expected tree to be dirty before reset")
		}

		// Reset
		if err := ResetWorkingTree(tmpDir); err != nil {
			t.Fatalf("ResetWorkingTree failed: %v", err)
		}

		// Should be clean now
		if !IsWorkingTreeClean(tmpDir) {
			t.Error("expected tree to be clean after reset")
		}

		// Verify content was restored
		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "initial content" {
			t.Errorf("expected file content 'initial content', got %q", string(content))
		}
	})

	t.Run("removes untracked files", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t)

		// Add untracked file
		untrackedFile := filepath.Join(tmpDir, "untracked.txt")
		if err := os.WriteFile(untrackedFile, []byte("untracked"), 0644); err != nil {
			t.Fatal(err)
		}

		// Verify dirty
		if IsWorkingTreeClean(tmpDir) {
			t.Fatal("expected tree to be dirty before reset")
		}

		// Reset
		if err := ResetWorkingTree(tmpDir); err != nil {
			t.Fatalf("ResetWorkingTree failed: %v", err)
		}

		// Should be clean now
		if !IsWorkingTreeClean(tmpDir) {
			t.Error("expected tree to be clean after reset")
		}

		// Verify untracked file was removed
		if _, err := os.Stat(untrackedFile); !os.IsNotExist(err) {
			t.Error("expected untracked file to be removed")
		}
	})

	t.Run("resets staged changes", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t)

		// Modify and stage
		testFile := filepath.Join(tmpDir, "initial.txt")
		if err := os.WriteFile(testFile, []byte("staged changes"), 0644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", tmpDir, "add", ".").CombinedOutput(); err != nil {
			t.Fatalf("git add failed: %v\n%s", err, out)
		}

		// Verify dirty
		if IsWorkingTreeClean(tmpDir) {
			t.Fatal("expected tree to be dirty before reset")
		}

		// Reset
		if err := ResetWorkingTree(tmpDir); err != nil {
			t.Fatalf("ResetWorkingTree failed: %v", err)
		}

		// Should be clean now
		if !IsWorkingTreeClean(tmpDir) {
			t.Error("expected tree to be clean after reset")
		}

		// Verify content was restored
		content, err := os.ReadFile(testFile)
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

func TestIsAncestor(t *testing.T) {
	tmpDir := t.TempDir()

	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create base commit
	if err := os.WriteFile(filepath.Join(tmpDir, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", "base.txt")
	runGit(t, tmpDir, "commit", "-m", "base commit")
	baseSHA := runGit(t, tmpDir, "rev-parse", "HEAD")

	// Create second commit on main
	if err := os.WriteFile(filepath.Join(tmpDir, "second.txt"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", "second.txt")
	runGit(t, tmpDir, "commit", "-m", "second commit")
	secondSHA := runGit(t, tmpDir, "rev-parse", "HEAD")

	// Create a divergent branch from base
	runGit(t, tmpDir, "checkout", baseSHA)
	runGit(t, tmpDir, "checkout", "-b", "divergent")
	if err := os.WriteFile(filepath.Join(tmpDir, "divergent.txt"), []byte("divergent"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", "divergent.txt")
	runGit(t, tmpDir, "commit", "-m", "divergent commit")
	divergentSHA := runGit(t, tmpDir, "rev-parse", "HEAD")

	t.Run("base is ancestor of second", func(t *testing.T) {
		isAnc, err := IsAncestor(tmpDir, baseSHA, secondSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isAnc {
			t.Error("expected base to be ancestor of second")
		}
	})

	t.Run("second is not ancestor of base", func(t *testing.T) {
		isAnc, err := IsAncestor(tmpDir, secondSHA, baseSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isAnc {
			t.Error("expected second to NOT be ancestor of base")
		}
	})

	t.Run("divergent is not ancestor of second", func(t *testing.T) {
		isAnc, err := IsAncestor(tmpDir, divergentSHA, secondSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isAnc {
			t.Error("expected divergent to NOT be ancestor of second (different branches)")
		}
	})

	t.Run("base is ancestor of divergent", func(t *testing.T) {
		isAnc, err := IsAncestor(tmpDir, baseSHA, divergentSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isAnc {
			t.Error("expected base to be ancestor of divergent")
		}
	})

	t.Run("commit is ancestor of itself", func(t *testing.T) {
		isAnc, err := IsAncestor(tmpDir, baseSHA, baseSHA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isAnc {
			t.Error("expected commit to be ancestor of itself")
		}
	})

	t.Run("bad object returns error", func(t *testing.T) {
		_, err := IsAncestor(tmpDir, "badbadbadbadbadbadbadbadbadbadbadbadbad", "HEAD")
		if err == nil {
			t.Error("expected error for bad object")
		}
	})
}
