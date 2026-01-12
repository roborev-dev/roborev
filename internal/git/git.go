package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommitInfo holds metadata about a commit
type CommitInfo struct {
	SHA       string
	Author    string
	Subject   string
	Body      string // Full commit message body (excluding subject)
	Timestamp time.Time
}

// GetCommitInfo retrieves commit metadata
func GetCommitInfo(repoPath, sha string) (*CommitInfo, error) {
	// Use record separator (ASCII 30) to delimit fields - won't appear in commit messages
	const rs = "\x1e"
	cmd := exec.Command("git", "log", "-1", "--format=%H"+rs+"%an"+rs+"%s"+rs+"%aI"+rs+"%b", sha)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	parts := strings.SplitN(strings.TrimSuffix(string(out), "\n"), rs, 5)
	if len(parts) < 4 {
		return nil, fmt.Errorf("unexpected git log output: %s", out)
	}

	ts, err := time.Parse(time.RFC3339, parts[3])
	if err != nil {
		ts = time.Now() // Fallback
	}

	var body string
	if len(parts) >= 5 {
		body = strings.TrimSpace(parts[4])
	}

	return &CommitInfo{
		SHA:       parts[0],
		Author:    parts[1],
		Subject:   parts[2],
		Body:      body,
		Timestamp: ts,
	}, nil
}

// GetDiff returns the full diff for a commit
func GetDiff(repoPath, sha string) (string, error) {
	cmd := exec.Command("git", "show", sha, "--format=")
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show: %w", err)
	}

	return string(out), nil
}

// GetFilesChanged returns the list of files changed in a commit
func GetFilesChanged(repoPath, sha string) ([]string, error) {
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var files []string
	for _, line := range lines {
		if line != "" {
			files = append(files, line)
		}
	}

	return files, nil
}

// GetStat returns the stat summary for a commit
func GetStat(repoPath, sha string) (string, error) {
	cmd := exec.Command("git", "show", "--stat", sha, "--format=")
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show --stat: %w", err)
	}

	return string(out), nil
}

// ResolveSHA resolves a ref (like HEAD) to a full SHA
func ResolveSHA(repoPath, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

// GetRepoRoot returns the root directory of the git repository
func GetRepoRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

// GetMainRepoRoot returns the main repository root, resolving through worktrees.
// For a regular repository or submodule, this returns the same as GetRepoRoot.
// For a worktree, this returns the main repository's root path.
func GetMainRepoRoot(path string) (string, error) {
	// Get both --git-dir and --git-common-dir to detect worktrees
	// For regular repos: both return ".git" (or absolute path)
	// For submodules: both return the same path (e.g., "../.git/modules/sub")
	// For worktrees: --git-dir returns worktree-specific dir, --git-common-dir returns main repo's .git
	gitDirCmd := exec.Command("git", "rev-parse", "--git-dir")
	gitDirCmd.Dir = path
	gitDirOut, err := gitDirCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(gitDirOut))

	commonDirCmd := exec.Command("git", "rev-parse", "--git-common-dir")
	commonDirCmd.Dir = path
	commonDirOut, err := commonDirCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(commonDirOut))

	// Make paths absolute for comparison
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(path, commonDir)
	}
	gitDir = filepath.Clean(gitDir)
	commonDir = filepath.Clean(commonDir)

	// Only apply worktree resolution if git-dir differs from common-dir
	// This ensures submodules (where both are the same) use GetRepoRoot
	if gitDir != commonDir {
		// This is a worktree. For regular worktrees, commonDir ends with ".git"
		// and the main repo is its parent. For submodule worktrees, commonDir
		// is inside .git/modules/ and we need to read the core.worktree config.
		if filepath.Base(commonDir) == ".git" {
			// Regular worktree - parent of .git is the repo root
			return filepath.Dir(commonDir), nil
		}

		// Submodule worktree - read core.worktree from config
		cmd := exec.Command("git", "config", "--file", filepath.Join(commonDir, "config"), "core.worktree")
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git config core.worktree for submodule worktree: %w", err)
		}
		worktree := strings.TrimSpace(string(out))
		if !filepath.IsAbs(worktree) {
			worktree = filepath.Join(commonDir, worktree)
		}
		return filepath.Clean(worktree), nil
	}

	// Regular repo or submodule - use standard resolution
	return GetRepoRoot(path)
}

// ReadFile reads a file at a specific commit
func ReadFile(repoPath, sha, filePath string) ([]byte, error) {
	cmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", sha, filePath))
	cmd.Dir = repoPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %s:%s: %s", sha, filePath, stderr.String())
	}

	return stdout.Bytes(), nil
}

// GetParentCommits returns the N commits before the given commit (not including it)
// Returns commits in reverse chronological order (most recent parent first)
func GetParentCommits(repoPath, sha string, count int) ([]string, error) {
	// Use git log to get parent commits, skipping the commit itself
	cmd := exec.Command("git", "log", "--format=%H", "-n", fmt.Sprintf("%d", count), "--skip=1", sha)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []string
	for _, line := range lines {
		if line != "" {
			commits = append(commits, line)
		}
	}

	return commits, nil
}

// IsRange returns true if the ref is a range (contains "..")
func IsRange(ref string) bool {
	return strings.Contains(ref, "..")
}

// ParseRange splits a range ref into start and end
func ParseRange(ref string) (start, end string, ok bool) {
	parts := strings.SplitN(ref, "..", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// GetRangeCommits returns all commits in a range (oldest first)
func GetRangeCommits(repoPath, rangeRef string) ([]string, error) {
	cmd := exec.Command("git", "log", "--format=%H", "--reverse", rangeRef)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log range: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []string
	for _, line := range lines {
		if line != "" {
			commits = append(commits, line)
		}
	}

	return commits, nil
}

// GetRangeDiff returns the combined diff for a range
func GetRangeDiff(repoPath, rangeRef string) (string, error) {
	cmd := exec.Command("git", "diff", rangeRef)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff range: %w", err)
	}

	return string(out), nil
}

// GetRangeStart returns the start commit (first parent before range) for context lookup
func GetRangeStart(repoPath, rangeRef string) (string, error) {
	start, _, ok := ParseRange(rangeRef)
	if !ok {
		return "", fmt.Errorf("invalid range: %s", rangeRef)
	}

	// Resolve the start ref
	return ResolveSHA(repoPath, start)
}

// IsRebaseInProgress returns true if a rebase operation is in progress
func IsRebaseInProgress(repoPath string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return false
	}

	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoPath, gitDir)
	}

	// Check for rebase-merge (interactive rebase) or rebase-apply (git am, regular rebase)
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if info, err := os.Stat(filepath.Join(gitDir, dir)); err == nil && info.IsDir() {
			return true
		}
	}

	return false
}

// GetBranchName returns a human-readable branch reference for a commit.
// Returns something like "main", "feature/foo", or "main~3" depending on
// where the commit is relative to branch heads. Returns empty string on error
// or timeout (2 second limit to avoid blocking UI).
func GetBranchName(repoPath, sha string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "name-rev", "--name-only", "--refs=refs/heads/*", sha)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	name := strings.TrimSpace(string(out))
	// name-rev returns "undefined" if commit isn't reachable from any branch
	if name == "" || name == "undefined" {
		return ""
	}

	return name
}

// GetHooksPath returns the path to the hooks directory, respecting core.hooksPath
func GetHooksPath(repoPath string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-path", "hooks")
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-path hooks: %w", err)
	}

	hooksPath := strings.TrimSpace(string(out))

	// If the path is relative, make it absolute relative to repoPath
	if !filepath.IsAbs(hooksPath) {
		hooksPath = filepath.Join(repoPath, hooksPath)
	}

	return hooksPath, nil
}
