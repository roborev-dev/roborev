package main

import (
	"fmt"
	"os"

	"github.com/roborev-dev/roborev/internal/git"
)

// currentRepoRoots captures the worktree root used for local git/file
// operations and the main repo root used for daemon/API queries.
type currentRepoRoots struct {
	worktreeRoot string
	mainRepoRoot string
}

// resolveCurrentRepoRoots resolves repo roots from the current working
// directory. Outside a git repo, both roots fall back to the working
// directory so existing fix/compact behavior stays unchanged.
func resolveCurrentRepoRoots() (currentRepoRoots, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return currentRepoRoots{}, fmt.Errorf("get working directory: %w", err)
	}

	roots := currentRepoRoots{
		worktreeRoot: workDir,
		mainRepoRoot: workDir,
	}
	if root, err := git.GetRepoRoot(workDir); err == nil {
		roots.worktreeRoot = root
		roots.mainRepoRoot = root
	}
	if root, err := git.GetMainRepoRoot(workDir); err == nil {
		roots.mainRepoRoot = root
	}

	return roots, nil
}

func resolveCurrentBranchFilter(worktreeRoot, branch string, allBranches bool) string {
	if allBranches {
		return ""
	}
	if branch != "" {
		return branch
	}
	return git.GetCurrentBranch(worktreeRoot)
}
