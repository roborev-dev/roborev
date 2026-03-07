package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

// GitHelper runs git commands in a repo directory.
type GitHelper struct {
	t            *testing.T
	dir          string
	resolvedPath string
}

func newIsolatedGitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testenv.BuildIsolatedGitEnv(os.Environ(), dir)

	return cmd
}

func (g *GitHelper) Run(args ...string) {
	g.t.Helper()
	cmd := newIsolatedGitCmd(g.dir, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func (g *GitHelper) Path() string {
	if g.resolvedPath != "" {
		return g.resolvedPath
	}
	return g.dir
}

func (g *GitHelper) HeadSHA() string {
	g.t.Helper()
	cmd := newIsolatedGitCmd(g.dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		g.t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func (g *GitHelper) CommitFile(name, content, msg string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, name), []byte(content), 0644); err != nil {
		g.t.Fatal(err)
	}
	g.Run("add", name)
	g.Run("commit", "-m", msg)
}

func NewGitRepo(t *testing.T) *GitHelper {
	t.Helper()
	dir := t.TempDir()

	// Resolve symlinks for macOS /var -> /private/var
	resolvedPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolvedPath = dir
	}

	g := &GitHelper{t: t, dir: dir, resolvedPath: resolvedPath}
	g.Run("init", "-b", "main")
	g.Run("config", "core.hooksPath", os.DevNull)
	g.Run("config", "user.email", "test@test.com")
	g.Run("config", "user.name", "Test")
	return g
}
