package worktree

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
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
	w.cleanup()
}

func (w *Worktree) cleanup() {
	_ = exec.Command("git", "-C", w.repoPath, "worktree", "remove", "--force", w.Dir).Run()
	_ = os.RemoveAll(w.Dir)
}

// Create creates a temporary git worktree detached at the given ref
// for isolated agent work. Pass "HEAD" for the current checkout.
func Create(repoPath, ref string) (_ *Worktree, err error) {
	if ref == "" {
		return nil, fmt.Errorf("ref must not be empty")
	}
	wt, err := newWorktree(repoPath, ref)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			wt.cleanup()
		}
	}()

	if err = wt.initSubmodules(); err != nil {
		return nil, err
	}
	wt.maybePullLFS()
	wt.baseSHA = wt.resolveBaseSHA()
	return wt, nil
}

func newWorktree(repoPath, ref string) (*Worktree, error) {
	worktreeDir, err := os.MkdirTemp("", "roborev-worktree-")
	if err != nil {
		return nil, err
	}

	// Create the worktree (without --recurse-submodules for compatibility with older git).
	// Suppress hooks via core.hooksPath=<null> — user hooks shouldn't run in internal worktrees.
	_, stderr, err := runGitCommand(repoPath, nil, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", worktreeDir, ref)
	if err != nil {
		_ = os.RemoveAll(worktreeDir)
		return nil, gitCommandError("git worktree add", err, stderr)
	}
	return &Worktree{Dir: worktreeDir, repoPath: repoPath}, nil
}

func (w *Worktree) initSubmodules() error {
	// Only enable file protocol for the non-recursive (top-level) pass
	// where the URLs come from the repo owner's .gitmodules. The recursive
	// pass never gets the override because nested .gitmodules content may
	// be attacker-controlled (CVE-2022-39253). Users who legitimately need
	// file-protocol nested submodules can set protocol.file.allow globally.
	allowFileProtocol, err := repoUsesFileProtocolSubmodules(w.Dir)
	if err != nil {
		return fmt.Errorf("detect submodule protocol requirements: %w", err)
	}
	if err := runSubmoduleUpdate(w.Dir, allowFileProtocol, false); err != nil {
		return err
	}
	// Only suppress file-protocol denials in the recursive pass. Other
	// failures (broken URLs, auth errors, missing commits) still surface.
	if err := runSubmoduleUpdate(w.Dir, false, true); err != nil {
		if !isFileProtocolError(err) {
			return err
		}
	}
	return nil
}

// isFileProtocolError reports whether err is a git "transport 'file' not
// allowed" denial, which we intentionally cause by not passing
// protocol.file.allow=always on the recursive submodule pass.
func isFileProtocolError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "transport 'file' not allowed")
}

func runSubmoduleUpdate(repoPath string, allowFileProtocol, recursive bool) error {
	args := make([]string, 0, 6)
	if allowFileProtocol {
		args = append(args, "-c", "protocol.file.allow=always")
	}
	args = append(args, "submodule", "update", "--init")
	if recursive {
		args = append(args, "--recursive")
	}
	_, stderr, err := runGitCommand(repoPath, nil, args...)
	if err != nil {
		return gitCommandError("git submodule update", err, stderr)
	}
	return nil
}

func (w *Worktree) maybePullLFS() {
	if _, _, err := runGitCommand(w.Dir, nil, "lfs", "env"); err == nil {
		_, _, _ = runGitCommand(w.Dir, nil, "lfs", "pull")
	}
}

func (w *Worktree) resolveBaseSHA() string {
	shaOut, stderr, err := runGitCommand(w.Dir, nil, "rev-parse", "HEAD")
	if err != nil {
		_ = stderr
		return ""
	}
	return strings.TrimSpace(string(shaOut))
}

// CapturePatch stages all changes in the worktree and returns the diff as a patch string.
// Returns empty string if there are no changes. Handles both uncommitted and committed
// changes by diffing the final tree state against the base SHA.
func (w *Worktree) CapturePatch() (string, error) {
	if err := w.stageAllChanges(); err != nil {
		return "", err
	}
	if patch, ok, err := w.capturePatchAgainstBase(); err != nil {
		return "", err
	} else if ok {
		return patch, nil
	}
	return w.captureCachedPatch()
}

func (w *Worktree) stageAllChanges() error {
	_, stderr, err := runGitCommand(w.Dir, nil, "add", "-A")
	if err != nil {
		return gitCommandError("git add in worktree", err, stderr)
	}
	return nil
}

func (w *Worktree) capturePatchAgainstBase() (string, bool, error) {
	if w.baseSHA == "" {
		return "", false, nil
	}

	treeOut, _, err := runGitCommand(w.Dir, nil, "write-tree")
	if err != nil {
		return "", false, nil
	}

	tree := strings.TrimSpace(string(treeOut))
	diff, _, err := runGitCommand(w.Dir, nil, "diff-tree", "-p", "--binary", w.baseSHA, tree)
	if err != nil {
		return "", false, nil
	}
	if len(diff) == 0 {
		return "", false, nil
	}
	return string(diff), true, nil
}

func (w *Worktree) captureCachedPatch() (string, error) {
	diff, stderr, err := runGitCommand(w.Dir, nil, "diff", "--cached", "--binary")
	if err != nil {
		return "", gitCommandError("git diff in worktree", err, stderr)
	}
	return string(diff), nil
}

// ApplyPatch applies a patch to a repository. Returns nil if patch is empty.
func ApplyPatch(repoPath, patch string) error {
	return applyPatch(repoPath, patch, false)
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
	return applyPatch(repoPath, patch, true)
}

func applyPatch(repoPath, patch string, checkOnly bool) error {
	if patch == "" {
		return nil
	}
	args := []string{"apply"}
	if checkOnly {
		args = append(args, "--check")
	}
	args = append(args, "--binary", "-")

	_, stderr, err := runGitCommand(repoPath, strings.NewReader(patch), args...)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		// "error: patch failed" and "does not apply" indicate merge conflicts.
		// Other messages (e.g. "corrupt patch") are non-conflict errors.
		if checkOnly && isPatchConflict(msg) {
			return &PatchConflictError{Detail: msg}
		}
		if checkOnly {
			if msg == "" {
				return err
			}
			return fmt.Errorf("patch check failed: %s", msg)
		}
		return gitCommandError("git apply", err, stderr)
	}
	return nil
}

func isPatchConflict(msg string) bool {
	return strings.Contains(msg, "patch failed") || strings.Contains(msg, "does not apply")
}

func repoUsesFileProtocolSubmodules(repoPath string) (bool, error) {
	gitmodulesPaths, err := findGitmodulesPaths(repoPath)
	if err != nil {
		return false, err
	}
	topLevel := filepath.Join(repoPath, ".gitmodules")
	for _, gitmodulesPath := range gitmodulesPaths {
		usesFileProtocol, err := gitmodulesUsesFileProtocol(gitmodulesPath)
		if err != nil {
			if gitmodulesPath == topLevel {
				return false, err // top-level .gitmodules must be readable
			}
			continue // best-effort: skip unreadable nested .gitmodules
		}
		if usesFileProtocol {
			return true, nil
		}
	}
	return false, nil
}

func gitmodulesUsesFileProtocol(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		url, ok := parseGitmodulesURL(scanner.Text())
		if !ok {
			continue
		}
		if isFileProtocolURL(url) {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func parseGitmodulesURL(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", false
	}
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", false
	}
	if !strings.EqualFold(strings.TrimSpace(parts[0]), "url") {
		return "", false
	}

	url := strings.TrimSpace(parts[1])
	if unquoted, err := strconv.Unquote(url); err == nil {
		url = unquoted
	}
	return url, true
}

func findGitmodulesPaths(repoPath string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == repoPath {
				return err // root target must be readable
			}
			return nil // best-effort: skip unreadable nested paths
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == ".gitmodules" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

func runGitCommand(dir string, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func gitCommandError(prefix string, err error, stderr []byte) error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w: %s", prefix, err, msg)
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
