package worktree

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Create creates a temporary git worktree detached at HEAD for isolated agent work.
// Returns the worktree directory path, a cleanup function, and any error.
// The cleanup function removes the worktree and its directory.
func Create(repoPath string) (string, func(), error) {
	worktreeDir, err := os.MkdirTemp("", "roborev-worktree-")
	if err != nil {
		return "", nil, err
	}

	// Create the worktree (without --recurse-submodules for compatibility with older git).
	// Suppress hooks via core.hooksPath=<null> — user hooks shouldn't run in internal worktrees.
	cmd := exec.Command("git", "-C", repoPath, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", worktreeDir, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(worktreeDir)
		return "", nil, fmt.Errorf("git worktree add: %w: %s", err, out)
	}

	// Initialize and update submodules in the worktree
	initArgs := []string{"-C", worktreeDir}
	if submoduleRequiresFileProtocol(worktreeDir) {
		initArgs = append(initArgs, "-c", "protocol.file.allow=always")
	}
	initArgs = append(initArgs, "submodule", "update", "--init")
	cmd = exec.Command("git", initArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		os.RemoveAll(worktreeDir)
		return "", nil, fmt.Errorf("git submodule update: %w: %s", err, out)
	}

	updateArgs := []string{"-C", worktreeDir}
	if submoduleRequiresFileProtocol(worktreeDir) {
		updateArgs = append(updateArgs, "-c", "protocol.file.allow=always")
	}
	updateArgs = append(updateArgs, "submodule", "update", "--init", "--recursive")
	cmd = exec.Command("git", updateArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		os.RemoveAll(worktreeDir)
		return "", nil, fmt.Errorf("git submodule update: %w: %s", err, out)
	}

	lfsCmd := exec.Command("git", "-C", worktreeDir, "lfs", "env")
	if err := lfsCmd.Run(); err == nil {
		cmd = exec.Command("git", "-C", worktreeDir, "lfs", "pull")
		cmd.Run()
	}

	cleanup := func() {
		exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		os.RemoveAll(worktreeDir)
	}

	return worktreeDir, cleanup, nil
}

// CapturePatch stages all changes in the worktree and returns the diff as a patch string.
// Returns empty string if there are no changes.
func CapturePatch(worktreeDir string) (string, error) {
	// Stage all changes in worktree
	cmd := exec.Command("git", "-C", worktreeDir, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add in worktree: %w: %s", err, out)
	}

	// Get diff as patch
	diffCmd := exec.Command("git", "-C", worktreeDir, "diff", "--cached", "--binary")
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

// CheckPatch does a dry-run apply to check if a patch applies cleanly.
func CheckPatch(repoPath, patch string) error {
	if patch == "" {
		return nil
	}
	applyCmd := exec.Command("git", "-C", repoPath, "apply", "--check", "--binary", "-")
	applyCmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	applyCmd.Stderr = &stderr
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("patch does not apply cleanly: %s", stderr.String())
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
