//go:build integration

package git

import (
	"os"
	"path/filepath"
	"testing"
)

func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved == "" {
		return path
	}
	return resolved
}

func assertNoErrorMsg(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

func assertEqual(t *testing.T, expected, actual, msg string) {
	t.Helper()
	if expected != actual {
		t.Fatalf("%s: expected %s, got %s", msg, expected, actual)
	}
}

func TestGetMainRepoRoot(t *testing.T) {
	t.Run("regular repo returns same as GetRepoRoot", func(t *testing.T) {
		repo := NewTestRepo(t)

		mainRoot, err := GetMainRepoRoot(repo.Dir)
		assertNoErrorMsg(t, err, "GetMainRepoRoot failed")

		repoRoot, err := GetRepoRoot(repo.Dir)
		assertNoErrorMsg(t, err, "GetRepoRoot failed")

		assertEqual(t, repoRoot, mainRoot, "GetMainRepoRoot should be same as GetRepoRoot")
	})

	t.Run("worktree returns main repo root", func(t *testing.T) {
		repo := NewTestRepo(t)
		repo.CommitFile("file.txt", "content", "initial")

		wt := repo.AddWorktree("worktree-branch")

		// GetRepoRoot from worktree returns the worktree path
		worktreeRoot, err := GetRepoRoot(wt.Dir)
		assertNoErrorMsg(t, err, "GetRepoRoot on worktree failed")

		mainRepoRoot, err := GetRepoRoot(repo.Dir)
		assertNoErrorMsg(t, err, "GetRepoRoot on main repo failed")

		if worktreeRoot == mainRepoRoot {
			t.Skip("worktree root equals main repo root - older git version")
		}

		mainRoot, err := GetMainRepoRoot(wt.Dir)
		assertNoErrorMsg(t, err, "GetMainRepoRoot on worktree failed")

		assertEqual(t, mainRepoRoot, mainRoot, "GetMainRepoRoot on worktree mismatch")
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
		parentRepo.CommitFile("parent.txt", "parent", "parent initial")

		subSource := NewTestRepo(t)
		subSource.CommitFile("sub.txt", "sub", "sub initial")

		// Add submodule to parent
		parentRepo.AddSubmodule(subSource.Dir, "mysub")

		submoduleDir := filepath.Join(parentRepo.Dir, "mysub")
		submoduleDirResolved := resolvePath(t, submoduleDir)

		subRoot, err := GetMainRepoRoot(submoduleDir)
		assertNoErrorMsg(t, err, "GetMainRepoRoot on submodule failed")

		parentRoot, err := GetMainRepoRoot(parentRepo.Dir)
		assertNoErrorMsg(t, err, "GetMainRepoRoot on parent failed")

		if subRoot == parentRoot {
			t.Errorf("submodule root should be distinct from parent: sub=%s parent=%s", subRoot, parentRoot)
		}

		subRootResolved := resolvePath(t, subRoot)
		assertEqual(t, submoduleDirResolved, subRootResolved, "GetMainRepoRoot on submodule mismatch")
	})

	t.Run("worktree from submodule returns submodule root", func(t *testing.T) {
		parentRepo := NewTestRepo(t)
		parentRepo.CommitFile("parent.txt", "parent", "parent initial")

		subSource := NewTestRepo(t)
		subSource.CommitFile("sub.txt", "sub", "sub initial")

		// Add submodule to parent
		parentRepo.AddSubmodule(subSource.Dir, "mysub")
		parentRepo.CommitAll("add submodule")

		submoduleDir := filepath.Join(parentRepo.Dir, "mysub")

		// Create a worktree from within the submodule
		subRepo := &TestRepo{T: t, Dir: submoduleDir}
		wt := subRepo.AddWorktree("sub-wt-branch")
		worktreeDir := wt.Dir

		wtRoot, err := GetMainRepoRoot(worktreeDir)
		assertNoErrorMsg(t, err, "GetMainRepoRoot on submodule worktree failed")

		subRoot, err := GetMainRepoRoot(submoduleDir)
		assertNoErrorMsg(t, err, "GetMainRepoRoot on submodule failed")

		assertEqual(t, subRoot, wtRoot, "worktree from submodule should return submodule root")

		parentRoot, err := GetMainRepoRoot(parentRepo.Dir)
		assertNoErrorMsg(t, err, "GetMainRepoRoot on parent failed")

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
		assertNoErrorMsg(t, err, "ResolveSHA main HEAD failed")

		wt := mainRepo.AddWorktree("wt-branch")

		// Make a new commit in the worktree
		wt.CommitFile("file.txt", "v2", "commit2")

		wtHead, err := ResolveSHA(wt.Dir, "HEAD")
		assertNoErrorMsg(t, err, "ResolveSHA worktree HEAD failed")

		if wtHead == mainHead {
			t.Error("worktree HEAD should differ from main HEAD after new commit")
		}

		mainHeadAgain, err := ResolveSHA(mainRepo.Dir, "HEAD")
		assertNoErrorMsg(t, err, "ResolveSHA main HEAD again failed")
		assertEqual(t, mainHead, mainHeadAgain, "main HEAD changed unexpectedly")

		mainRoot, _ := GetMainRepoRoot(mainRepo.Dir)
		wtRoot, _ := GetMainRepoRoot(wt.Dir)
		assertEqual(t, mainRoot, wtRoot, "GetMainRepoRoot should return same root")
	})
}

func setupBranchOriginTest(t *testing.T) (*TestRepo, *TestRepo) {
	t.Helper()
	bareRepo := NewBareTestRepo(t)
	bareRepo.Run("symbolic-ref", "HEAD", "refs/heads/main")

	seedRepo := NewTestRepo(t)
	seedRepo.Run("checkout", "-b", "main")
	seedRepo.CommitFile("file.txt", "base", "initial")
	seedRepo.Run("remote", "add", "origin", bareRepo.Dir)
	seedRepo.Run("push", "-u", "origin", "main")

	return bareRepo, seedRepo
}

func setupBranchOriginClone(t *testing.T) (*TestRepo, *TestRepo, *TestRepo) {
	t.Helper()
	bareRepo, seedRepo := setupBranchOriginTest(t)
	clone := CloneTestRepo(t, bareRepo.Dir)
	clone.Run("remote", "set-head", "origin", "-a")
	return bareRepo, seedRepo, clone
}

func TestGetDefaultBranchOriginHead(t *testing.T) {
	t.Run("missing local branch uses origin ref", func(t *testing.T) {
		_, _, clone := setupBranchOriginClone(t)
		clone.Run("checkout", "--detach")
		clone.Run("branch", "-D", "main")

		branch, err := GetDefaultBranch(clone.Dir)
		assertNoErrorMsg(t, err, "GetDefaultBranch failed")
		assertEqual(t, "origin/main", branch, "branch mismatch")
	})

	t.Run("stale local branch uses origin ref", func(t *testing.T) {
		_, seedRepo, clone := setupBranchOriginClone(t)

		seedRepo.CommitFile("file2.txt", "new", "update")
		seedRepo.Run("push")
		clone.Run("fetch", "origin")

		branch, err := GetDefaultBranch(clone.Dir)
		assertNoErrorMsg(t, err, "GetDefaultBranch failed")
		assertEqual(t, "origin/main", branch, "branch mismatch")
	})

	t.Run("origin/HEAD points to missing remote ref, falls back to local branch", func(t *testing.T) {
		_, _, clone := setupBranchOriginClone(t)

		// Delete the remote-tracking branch while keeping origin/HEAD symbolic ref intact
		clone.Run("update-ref", "-d", "refs/remotes/origin/main")

		// Local main branch should still exist
		localBranchOut := clone.Run("rev-parse", "--verify", "main")
		if localBranchOut == "" {
			t.Fatal("expected local main branch to exist")
		}

		branch, err := GetDefaultBranch(clone.Dir)
		assertNoErrorMsg(t, err, "GetDefaultBranch failed")
		assertEqual(t, "main", branch, "branch mismatch")
	})
}
