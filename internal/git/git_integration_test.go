//go:build integration

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGetMainRepoRoot(t *testing.T) {
	repo := NewTestRepo(t)

	t.Run("regular repo returns same as GetRepoRoot", func(t *testing.T) {
		mainRoot, err := GetMainRepoRoot(repo.Dir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot failed: %v", err)
		}

		repoRoot, err := GetRepoRoot(repo.Dir)
		if err != nil {
			t.Fatalf("GetRepoRoot failed: %v", err)
		}

		if mainRoot != repoRoot {
			t.Errorf("GetMainRepoRoot returned %s, expected %s (same as GetRepoRoot)", mainRoot, repoRoot)
		}
	})

	t.Run("worktree returns main repo root", func(t *testing.T) {
		repo.CommitFile("file.txt", "content", "initial")

		wt := repo.AddWorktree("worktree-branch")

		// GetRepoRoot from worktree returns the worktree path
		worktreeRoot, err := GetRepoRoot(wt.Dir)
		if err != nil {
			t.Fatalf("GetRepoRoot on worktree failed: %v", err)
		}

		mainRepoRoot, err := GetRepoRoot(repo.Dir)
		if err != nil {
			t.Fatalf("GetRepoRoot on main repo failed: %v", err)
		}

		if worktreeRoot == mainRepoRoot {
			t.Skip("worktree root equals main repo root - older git version")
		}

		mainRoot, err := GetMainRepoRoot(wt.Dir)
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
		parentRepo := NewTestRepo(t)
		parentRepo.Run("config", "protocol.file.allow", "always")
		parentRepo.CommitFile("parent.txt", "parent", "parent initial")

		subSource := NewTestRepo(t)
		subSource.CommitFile("sub.txt", "sub", "sub initial")

		// Add submodule to parent
		cmd := exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", subSource.Dir, "mysub")
		cmd.Dir = parentRepo.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git submodule add failed: %v\n%s", err, out)
		}

		submoduleDir := filepath.Join(parentRepo.Dir, "mysub")
		submoduleDirResolved, _ := filepath.EvalSymlinks(submoduleDir)
		if submoduleDirResolved == "" {
			submoduleDirResolved = submoduleDir
		}

		subRoot, err := GetMainRepoRoot(submoduleDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on submodule failed: %v", err)
		}

		parentRoot, err := GetMainRepoRoot(parentRepo.Dir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on parent failed: %v", err)
		}

		if subRoot == parentRoot {
			t.Errorf("submodule root should be distinct from parent: sub=%s parent=%s", subRoot, parentRoot)
		}

		subRootResolved, _ := filepath.EvalSymlinks(subRoot)
		if subRootResolved == "" {
			subRootResolved = subRoot
		}
		if subRootResolved != submoduleDirResolved {
			t.Errorf("GetMainRepoRoot on submodule returned %s, expected %s", subRoot, submoduleDir)
		}
	})

	t.Run("worktree from submodule returns submodule root", func(t *testing.T) {
		parentRepo := NewTestRepo(t)
		parentRepo.Run("config", "protocol.file.allow", "always")
		parentRepo.CommitFile("parent.txt", "parent", "parent initial")

		subSource := NewTestRepo(t)
		subSource.CommitFile("sub.txt", "sub", "sub initial")

		// Add submodule to parent
		cmd := exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", subSource.Dir, "mysub")
		cmd.Dir = parentRepo.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git submodule add failed: %v\n%s", err, out)
		}
		parentRepo.CommitAll("add submodule")

		submoduleDir := filepath.Join(parentRepo.Dir, "mysub")

		// Create a worktree from within the submodule
		worktreeDir := t.TempDir()
		cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "sub-wt-branch")
		cmd.Dir = submoduleDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add from submodule failed: %v\n%s", err, out)
		}
		defer exec.Command("git", "-C", submoduleDir, "worktree", "remove", worktreeDir).Run()

		wtRoot, err := GetMainRepoRoot(worktreeDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on submodule worktree failed: %v", err)
		}

		subRoot, err := GetMainRepoRoot(submoduleDir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on submodule failed: %v", err)
		}

		if wtRoot != subRoot {
			t.Errorf("worktree from submodule should return submodule root: wt=%s sub=%s", wtRoot, subRoot)
		}

		parentRoot, err := GetMainRepoRoot(parentRepo.Dir)
		if err != nil {
			t.Fatalf("GetMainRepoRoot on parent failed: %v", err)
		}

		if wtRoot == parentRoot {
			t.Errorf("worktree from submodule should NOT return parent root: wt=%s parent=%s", wtRoot, parentRoot)
		}

		if info, err := os.Stat(wtRoot); err != nil || !info.IsDir() {
			t.Errorf("GetMainRepoRoot returned invalid path: %s", wtRoot)
		}
	})

	t.Run("worktree HEAD resolves to worktree branch", func(t *testing.T) {
		mainRepo := NewTestRepo(t)
		mainRepo.CommitFile("file.txt", "v1", "commit1")

		mainHead, err := ResolveSHA(mainRepo.Dir, "HEAD")
		if err != nil {
			t.Fatalf("ResolveSHA main HEAD failed: %v", err)
		}

		wt := mainRepo.AddWorktree("wt-branch")

		// Make a new commit in the worktree
		wt.CommitFile("file.txt", "v2", "commit2")

		wtHead, err := ResolveSHA(wt.Dir, "HEAD")
		if err != nil {
			t.Fatalf("ResolveSHA worktree HEAD failed: %v", err)
		}

		if wtHead == mainHead {
			t.Error("worktree HEAD should differ from main HEAD after new commit")
		}

		mainHeadAgain, err := ResolveSHA(mainRepo.Dir, "HEAD")
		if err != nil {
			t.Fatalf("ResolveSHA main HEAD again failed: %v", err)
		}
		if mainHeadAgain != mainHead {
			t.Errorf("main HEAD changed unexpectedly: was %s, now %s", mainHead, mainHeadAgain)
		}

		mainRoot, _ := GetMainRepoRoot(mainRepo.Dir)
		wtRoot, _ := GetMainRepoRoot(wt.Dir)
		if mainRoot != wtRoot {
			t.Errorf("GetMainRepoRoot should return same root: main=%s wt=%s", mainRoot, wtRoot)
		}
	})
}

func TestGetDefaultBranchOriginHead(t *testing.T) {
	bareRepo := NewBareTestRepo(t)
	bareRepo.Run("symbolic-ref", "HEAD", "refs/heads/main")

	seedRepo := NewTestRepo(t)
	seedRepo.Run("checkout", "-b", "main")
	seedRepo.CommitFile("file.txt", "base", "initial")
	seedRepo.Run("remote", "add", "origin", bareRepo.Dir)
	seedRepo.Run("push", "-u", "origin", "main")

	t.Run("missing local branch uses origin ref", func(t *testing.T) {
		cloneDir := t.TempDir()
		runGit(t, "", "clone", bareRepo.Dir, cloneDir)
		clone := &TestRepo{T: t, Dir: cloneDir}
		clone.Run("remote", "set-head", "origin", "-a")
		clone.Run("checkout", "--detach")
		clone.Run("branch", "-D", "main")

		branch, err := GetDefaultBranch(clone.Dir)
		if err != nil {
			t.Fatalf("GetDefaultBranch failed: %v", err)
		}
		if branch != "origin/main" {
			t.Fatalf("expected origin/main, got %s", branch)
		}
	})

	t.Run("stale local branch uses origin ref", func(t *testing.T) {
		cloneDir := t.TempDir()
		runGit(t, "", "clone", bareRepo.Dir, cloneDir)
		clone := &TestRepo{T: t, Dir: cloneDir}
		clone.Run("remote", "set-head", "origin", "-a")

		seedRepo.CommitFile("file2.txt", "new", "update")
		seedRepo.Run("push")
		clone.Run("fetch", "origin")

		branch, err := GetDefaultBranch(clone.Dir)
		if err != nil {
			t.Fatalf("GetDefaultBranch failed: %v", err)
		}
		if branch != "origin/main" {
			t.Fatalf("expected origin/main, got %s", branch)
		}
	})

	t.Run("origin/HEAD points to missing remote ref, falls back to local branch", func(t *testing.T) {
		cloneDir := t.TempDir()
		runGit(t, "", "clone", bareRepo.Dir, cloneDir)
		clone := &TestRepo{T: t, Dir: cloneDir}
		clone.Run("remote", "set-head", "origin", "-a")

		// Delete the remote-tracking branch while keeping origin/HEAD symbolic ref intact
		clone.Run("update-ref", "-d", "refs/remotes/origin/main")

		// Local main branch should still exist
		localBranchOut := clone.Run("rev-parse", "--verify", "main")
		if localBranchOut == "" {
			t.Fatal("expected local main branch to exist")
		}

		branch, err := GetDefaultBranch(clone.Dir)
		if err != nil {
			t.Fatalf("GetDefaultBranch failed: %v", err)
		}
		if branch != "main" {
			t.Fatalf("expected main (local branch fallback), got %s", branch)
		}
	})
}
