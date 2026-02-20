package worktree

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Worktree represents a temporary git worktree for isolated agent work.
// Call Close to remove the worktree and its directory.
type Worktree struct {
	Dir      string // Path to the worktree directory
	repoPath string // Path to the parent repository
	baseSHA  string // SHA of the commit the worktree was detached at
}

// Close removes the worktree and its directory.
func (w *Worktree) Close() {
	_ = exec.Command("git", "-C", w.repoPath, "worktree", "remove", "--force", w.Dir).Run()
	_ = os.RemoveAll(w.Dir)
}

// Create creates a temporary git worktree detached at the given ref
// for isolated agent work. Pass "HEAD" for the current checkout.
func Create(repoPath, ref string) (*Worktree, error) {
	if ref == "" {
		return nil, fmt.Errorf("ref must not be empty")
	}
	worktreeDir, err := os.MkdirTemp("", "roborev-worktree-")
	if err != nil {
		return nil, err
	}

	// Create the worktree (without --recurse-submodules for compatibility with older git).
	// Suppress hooks via core.hooksPath=<null> — user hooks shouldn't run in internal worktrees.
	cmd := exec.Command("git", "-C", repoPath, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", worktreeDir, ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(worktreeDir)
		return nil, fmt.Errorf("git worktree add: %w: %s", err, out)
	}

	// Initialize and update submodules in the worktree
	initArgs := []string{"-C", worktreeDir}
	if submoduleRequiresFileProtocol(worktreeDir) {
		initArgs = append(initArgs, "-c", "protocol.file.allow=always")
	}
	initArgs = append(initArgs, "submodule", "update", "--init")
	cmd = exec.Command("git", initArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		_ = os.RemoveAll(worktreeDir)
		return nil, fmt.Errorf("git submodule update: %w: %s", err, out)
	}

	updateArgs := []string{"-C", worktreeDir}
	if submoduleRequiresFileProtocol(worktreeDir) {
		updateArgs = append(updateArgs, "-c", "protocol.file.allow=always")
	}
	updateArgs = append(updateArgs, "submodule", "update", "--init", "--recursive")
	cmd = exec.Command("git", updateArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		_ = os.RemoveAll(worktreeDir)
		return nil, fmt.Errorf("git submodule update: %w: %s", err, out)
	}

	lfsCmd := exec.Command("git", "-C", worktreeDir, "lfs", "env")
	if err := lfsCmd.Run(); err == nil {
		cmd = exec.Command("git", "-C", worktreeDir, "lfs", "pull")
		_ = cmd.Run()
	}

	// Record the base SHA for patch capture
	shaCmd := exec.Command("git", "-C", worktreeDir, "rev-parse", "HEAD")
	shaOut, shaErr := shaCmd.Output()
	baseSHA := ""
	if shaErr == nil {
		baseSHA = strings.TrimSpace(string(shaOut))
	}

	return &Worktree{Dir: worktreeDir, repoPath: repoPath, baseSHA: baseSHA}, nil
}

// CapturePatch stages all changes in the worktree and returns the diff as a patch string.
// Returns empty string if there are no changes. Handles both uncommitted and committed
// changes by diffing the final tree state against the base SHA.
func (w *Worktree) CapturePatch() (string, error) {
	// Stage all changes in worktree
	cmd := exec.Command("git", "-C", w.Dir, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add in worktree: %w: %s", err, out)
	}

	// If we have a base SHA, diff the current tree state (HEAD + staged) against it.
	// This captures both committed and uncommitted changes the agent made.
	if w.baseSHA != "" {
		// Create a temporary tree object from the index (staged state)
		treeCmd := exec.Command("git", "-C", w.Dir, "write-tree")
		treeOut, err := treeCmd.Output()
		if err != nil {
			log.Printf("CapturePatch: write-tree failed, falling back to diff --cached: %v", err)
		} else {
			tree := strings.TrimSpace(string(treeOut))
			diffCmd := exec.Command("git", "-C", w.Dir, "diff-tree", "-p", "--binary", w.baseSHA, tree)
			diff, err := diffCmd.Output()
			if err != nil {
				log.Printf("CapturePatch: diff-tree failed, falling back to diff --cached: %v", err)
			} else if len(diff) > 0 {
				return string(diff), nil
			}
		}
	}

	// Fallback: diff staged changes against HEAD (works when agent didn't commit)
	diffCmd := exec.Command("git", "-C", w.Dir, "diff", "--cached", "--binary")
	diff, err := diffCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff in worktree: %w", err)
	}
	return string(diff), nil
}

// ApplyPatch applies a patch to a repository. Returns nil if patch is empty.
func ApplyPatch(repoPath, patch string) error {
	if patch == "" {
		return nil
	}
	applyCmd := exec.Command("git", "-C", repoPath, "apply", "--binary", "-")
	applyCmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	applyCmd.Stderr = &stderr
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, stderr.String())
	}
	return nil
}

// PatchConflictError indicates the patch does not apply due to merge conflicts.
// Other errors (malformed patch, permission errors) are returned as plain errors.
type PatchConflictError struct {
	Detail string
}

func (e *PatchConflictError) Error() string {
	return "patch conflict: " + e.Detail
}

// CheckPatch does a dry-run apply to check if a patch applies cleanly.
// Returns a *PatchConflictError when the patch fails due to conflicts,
// or a plain error for other failures (malformed patch, etc.).
func CheckPatch(repoPath, patch string) error {
	if patch == "" {
		return nil
	}
	applyCmd := exec.Command("git", "-C", repoPath, "apply", "--check", "--binary", "-")
	applyCmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	applyCmd.Stderr = &stderr
	if err := applyCmd.Run(); err != nil {
		msg := stderr.String()
		// "error: patch failed" and "does not apply" indicate merge conflicts.
		// Other messages (e.g. "corrupt patch") are non-conflict errors.
		if strings.Contains(msg, "patch failed") || strings.Contains(msg, "does not apply") {
			return &PatchConflictError{Detail: msg}
		}
		return fmt.Errorf("patch check failed: %s", msg)
	}
	return nil
}

func submoduleRequiresFileProtocol(repoPath string) bool {
	gitmodulesPaths := findGitmodulesPaths(repoPath)
	if len(gitmodulesPaths) == 0 {
		return false
	}
	for _, gitmodulesPath := range gitmodulesPaths {
		file, err := os.Open(gitmodulesPath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(parts[0]), "url") {
				continue
			}
			url := strings.TrimSpace(parts[1])
			if unquoted, err := strconv.Unquote(url); err == nil {
				url = unquoted
			}
			if isFileProtocolURL(url) {
				file.Close()
				return true
			}
		}
		file.Close()
	}
	return false
}

func findGitmodulesPaths(repoPath string) []string {
	var paths []string
	err := filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Name() == ".gitmodules" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil
	}
	return paths
}

func isFileProtocolURL(url string) bool {
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "file:") {
		return true
	}
	if strings.HasPrefix(url, "/") || strings.HasPrefix(url, "./") || strings.HasPrefix(url, "../") {
		return true
	}
	if len(url) >= 2 && isAlpha(url[0]) && url[1] == ':' {
		return true
	}
	if strings.HasPrefix(url, `\\`) {
		return true
	}
	return false
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
