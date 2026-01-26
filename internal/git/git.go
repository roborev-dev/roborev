package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// normalizeMSYSPath converts MSYS-style paths (e.g., /c/Users/...) to Windows paths (C:\Users\...).
// On non-Windows systems, it just applies filepath.FromSlash.
func normalizeMSYSPath(path string) string {
	path = strings.TrimSpace(path)
	// On Windows, MSYS paths like /c/Users/... need to be converted to C:\Users\...
	// Regular paths like C:/Users/... just need slash conversion
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' {
		// Check for MSYS-style drive letter: /c/ or /C/
		if (path[1] >= 'a' && path[1] <= 'z' || path[1] >= 'A' && path[1] <= 'Z') && path[2] == '/' {
			// Convert /c/... to C:/...
			path = strings.ToUpper(string(path[1])) + ":" + path[2:]
		}
	}
	return filepath.FromSlash(path)
}

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

// GetCurrentBranch returns the current branch name, or empty string if detached HEAD
func GetCurrentBranch(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		// Detached HEAD state
		return ""
	}
	return branch
}

// LocalBranchName strips the "origin/" prefix from a branch name if present.
// This normalizes branch names for comparison since GetDefaultBranch may return
// "origin/main" while GetCurrentBranch returns "main".
func LocalBranchName(branch string) string {
	return strings.TrimPrefix(branch, "origin/")
}

// GetDiff returns the full diff for a commit, excluding generated files like lock files
func GetDiff(repoPath, sha string) (string, error) {
	args := []string{"show", sha, "--format=", "--"}
	args = append(args, ".")
	args = append(args, excludedPathPatterns...)

	cmd := exec.Command("git", args...)
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

// IsAncestor checks if ancestor is an ancestor of descendant.
// Returns (true, nil) if ancestor is reachable from descendant via the commit graph.
// Returns (false, nil) if ancestor is not an ancestor (git exits with status 1).
// Returns (false, error) for git errors (e.g., bad object, repo issues).
func IsAncestor(repoPath, ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = repoPath
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// Exit code 1 means "not ancestor", which is not an error
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	// Any other error (exit code 128, etc.) is a real git error
	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

// GetRepoRoot returns the root directory of the git repository
func GetRepoRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}

	// Git on Windows can return MSYS-style paths (/c/Users/...) or forward-slash paths (C:/...).
	// Convert to native Windows paths for consistency with Go's filepath.
	return normalizeMSYSPath(string(out)), nil
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

// GetRangeDiff returns the combined diff for a range, excluding generated files like lock files
func GetRangeDiff(repoPath, rangeRef string) (string, error) {
	args := []string{"diff", rangeRef, "--"}
	args = append(args, ".")
	args = append(args, excludedPathPatterns...)

	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff range: %w", err)
	}

	return string(out), nil
}

// HasUncommittedChanges returns true if there are uncommitted changes (staged, unstaged, or untracked files)
func HasUncommittedChanges(repoPath string) (bool, error) {
	// Check for staged or unstaged changes to tracked files
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}

	return len(strings.TrimSpace(string(out))) > 0, nil
}

// emptyTreeSHA is the SHA of an empty tree in git, used for diffing repos with no commits
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// GetDirtyDiff returns a diff of all uncommitted changes including untracked files.
// The diff includes both tracked file changes (via git diff HEAD) and untracked files
// formatted as new-file diff entries. Excludes generated files like lock files.
func GetDirtyDiff(repoPath string) (string, error) {
	var result strings.Builder

	// Build diff args with exclusions
	diffArgs := func(baseArgs ...string) []string {
		args := append(baseArgs, "--")
		args = append(args, ".")
		args = append(args, excludedPathPatterns...)
		return args
	}

	// 1. Get diff of tracked files (staged + unstaged)
	cmd := exec.Command("git", diffArgs("diff", "HEAD")...)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		// If HEAD doesn't exist (no commits yet), we need to combine:
		// - git diff --cached <empty-tree>: shows staged files (index vs empty)
		// - git diff: shows unstaged changes (working tree vs index)
		// This covers the edge case where a file is staged but then removed from working tree

		// Get staged changes vs empty tree
		cmd = exec.Command("git", diffArgs("diff", "--cached", emptyTreeSHA)...)
		cmd.Dir = repoPath
		stagedOut, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git diff --cached: %w", err)
		}
		if len(stagedOut) > 0 {
			result.Write(stagedOut)
		}

		// Get unstaged changes (working tree vs index)
		cmd = exec.Command("git", diffArgs("diff")...)
		cmd.Dir = repoPath
		unstagedOut, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git diff: %w", err)
		}
		if len(unstagedOut) > 0 {
			result.Write(unstagedOut)
		}
	} else {
		if len(out) > 0 {
			result.Write(out)
		}
	}

	// 2. Get list of untracked files
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = repoPath

	untrackedOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}

	// 3. For each untracked file, create a diff-style "new file" entry
	untrackedFiles := strings.Split(strings.TrimSpace(string(untrackedOut)), "\n")
	for _, file := range untrackedFiles {
		if file == "" {
			continue
		}

		// Skip excluded files
		if isExcludedFile(file) {
			continue
		}

		// Read file content
		filePath := filepath.Join(repoPath, file)
		content, err := os.ReadFile(filePath)
		if err != nil {
			// Skip files we can't read (permissions, etc.)
			continue
		}

		// Check if file is binary
		if isBinaryContent(content) {
			result.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", file, file))
			result.WriteString("new file mode 100644\n")
			result.WriteString("Binary file (not shown)\n")
			continue
		}

		// Format as diff "new file" entry
		result.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", file, file))
		result.WriteString("new file mode 100644\n")
		result.WriteString("--- /dev/null\n")
		result.WriteString(fmt.Sprintf("+++ b/%s\n", file))

		lines := strings.Split(string(content), "\n")
		// Add line count header
		lineCount := len(lines)
		if lineCount > 0 && lines[lineCount-1] == "" {
			lineCount-- // Don't count trailing empty line from split
		}
		result.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", lineCount))

		// Add each line with + prefix
		for i, line := range lines {
			if i == len(lines)-1 && line == "" {
				// Skip trailing empty line from split
				continue
			}
			result.WriteString("+")
			result.WriteString(line)
			result.WriteString("\n")
		}
	}

	return result.String(), nil
}

// excludedPathPatterns contains pathspec patterns for files that should be excluded from diffs.
// These are typically generated files that add noise to code reviews.
// Uses :(exclude) long form since :! shorthand doesn't work reliably with git show/diff.
var excludedPathPatterns = []string{
	":(exclude)uv.lock",
	":(exclude)package-lock.json",
	":(exclude)yarn.lock",
	":(exclude)pnpm-lock.yaml",
	":(exclude)Cargo.lock", // Rust uses capital C
	":(exclude)cargo.lock", // Include lowercase for case-insensitive filesystems
	":(exclude)Gemfile.lock",
	":(exclude)poetry.lock",
	":(exclude)composer.lock",
	":(exclude)go.sum",
	":(exclude).beads",   // Excludes entire directory tree
	":(exclude).gocache", // Go build cache (sometimes created by agents)
	":(exclude).cache",   // Generic cache directory (pip, pre-commit, etc.)
}

var excludedDirPatterns = map[string]struct{}{
	".beads":   {},
	".gocache": {},
	".cache":   {},
}

// isExcludedFile checks if a file path matches any of the excluded patterns.
// Used for filtering untracked files in GetDirtyDiff.
func isExcludedFile(filePath string) bool {
	// Check each exclusion pattern
	for _, pattern := range excludedPathPatterns {
		// Remove the ":(exclude)" prefix to get the actual pattern
		p := strings.TrimPrefix(pattern, ":(exclude)")

		if _, ok := excludedDirPatterns[p]; ok {
			// Directory patterns match any file within that directory
			if filePath == p || strings.HasPrefix(filePath, p+"/") {
				return true
			}
			continue
		}

		// Exact filename match (like uv.lock) - matches at root or in subdirs
		if filePath == p || strings.HasSuffix(filePath, "/"+p) {
			return true
		}
	}
	return false
}

// isBinaryContent checks if content appears to be binary (contains null bytes in first 8KB)
func isBinaryContent(content []byte) bool {
	// Check first 8KB for null bytes
	checkLen := len(content)
	if checkLen > 8192 {
		checkLen = 8192
	}
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			return true
		}
	}
	return false
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

	// Strip ~N or ^N suffix (e.g., "main~12" -> "main")
	// These indicate the commit is N commits behind the branch tip
	if idx := strings.IndexAny(name, "~^"); idx != -1 {
		name = name[:idx]
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

// GetDefaultBranch detects the default branch (from origin/HEAD, or main/master locally)
func GetDefaultBranch(repoPath string) (string, error) {
	// Prefer origin/HEAD as the authoritative source for the default branch
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err == nil {
		// Returns refs/remotes/origin/main -> extract "main"
		ref := strings.TrimSpace(string(out))
		branchName := strings.TrimPrefix(ref, "refs/remotes/origin/")
		if branchName != "" {
			// Verify the remote-tracking ref exists before using it
			checkCmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branchName)
			checkCmd.Dir = repoPath
			if checkCmd.Run() == nil {
				return "origin/" + branchName, nil
			}
			// Remote-tracking ref doesn't exist, fall back to local branch
			checkCmd = exec.Command("git", "rev-parse", "--verify", "--quiet", branchName)
			checkCmd.Dir = repoPath
			if checkCmd.Run() == nil {
				return branchName, nil
			}
		}
	}

	// Fall back to common local branch names (for repos without origin)
	for _, branch := range []string{"main", "master"} {
		cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", branch)
		cmd.Dir = repoPath
		if err := cmd.Run(); err == nil {
			return branch, nil
		}
	}

	return "", fmt.Errorf("could not detect default branch (tried origin/HEAD, main, master)")
}

// GetMergeBase returns the merge-base (common ancestor) between two refs
func GetMergeBase(repoPath, ref1, ref2 string) (string, error) {
	cmd := exec.Command("git", "merge-base", ref1, ref2)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

// GetCommitsSince returns all commits from mergeBase to HEAD (exclusive of mergeBase)
// Returns commits in chronological order (oldest first)
func GetCommitsSince(repoPath, mergeBase string) ([]string, error) {
	rangeRef := mergeBase + "..HEAD"
	return GetRangeCommits(repoPath, rangeRef)
}

// CreateCommit stages all changes and creates a commit with the given message
// Returns the SHA of the new commit
func CreateCommit(repoPath, message string) (string, error) {
	// Stage all changes (respects .gitignore)
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git add: %w: %s", err, stderr.String())
	}

	// Create commit
	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoPath
	stderr.Reset()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, stderr.String())
	}

	// Get the SHA of the new commit
	sha, err := ResolveSHA(repoPath, "HEAD")
	if err != nil {
		return "", fmt.Errorf("get new commit SHA: %w", err)
	}

	return sha, nil
}

// IsWorkingTreeClean returns true if the working tree has no uncommitted or untracked changes
func IsWorkingTreeClean(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false // Assume dirty if we can't check
	}
	return len(strings.TrimSpace(string(output))) == 0
}

// ResetWorkingTree discards all uncommitted changes (staged and unstaged)
func ResetWorkingTree(repoPath string) error {
	// Reset staged changes
	resetCmd := exec.Command("git", "-C", repoPath, "reset", "--hard", "HEAD")
	if err := resetCmd.Run(); err != nil {
		return fmt.Errorf("git reset --hard: %w", err)
	}
	// Clean untracked files
	cleanCmd := exec.Command("git", "-C", repoPath, "clean", "-fd")
	if err := cleanCmd.Run(); err != nil {
		return fmt.Errorf("git clean: %w", err)
	}
	return nil
}

// GetRemoteURL returns the URL for a git remote.
// If remoteName is empty, tries "origin" first, then any other remote.
// Returns empty string if no remotes exist.
func GetRemoteURL(repoPath, remoteName string) string {
	if remoteName == "" {
		// Try origin first
		url := getRemoteURLByName(repoPath, "origin")
		if url != "" {
			return url
		}
		// Fall back to any remote
		return getAnyRemoteURL(repoPath)
	}
	return getRemoteURLByName(repoPath, remoteName)
}

func getRemoteURLByName(repoPath, name string) string {
	cmd := exec.Command("git", "remote", "get-url", name)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getAnyRemoteURL(repoPath string) string {
	// List all remotes
	cmd := exec.Command("git", "remote")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	remotes := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, remote := range remotes {
		if remote == "" {
			continue
		}
		url := getRemoteURLByName(repoPath, remote)
		if url != "" {
			return url
		}
	}
	return ""
}
