package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type testRepoOptions struct {
	dir           string
	initArgs      []string
	configureUser bool
	resolvePath   bool
}

// TestRepo encapsulates a temporary git repository for tests.
type TestRepo struct {
	Root         string
	GitDir       string
	HooksDir     string
	HookPath     string
	resolvedPath string
	t            *testing.T
}

func newTestRepo(t *testing.T, opts testRepoOptions) *TestRepo {
	t.Helper()

	dir := opts.dir
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create repo dir %q: %v", dir, err)
	}

	repo := &TestRepo{
		Root:     dir,
		GitDir:   filepath.Join(dir, ".git"),
		HooksDir: filepath.Join(dir, ".git", "hooks"),
		HookPath: filepath.Join(dir, ".git", "hooks", "post-commit"),
		t:        t,
	}

	if len(opts.initArgs) > 0 {
		runGit(t, repo.Root, nil, opts.initArgs...)
	}

	if opts.configureUser {
		repo.Config("user.email", GitUserEmail)
		repo.Config("user.name", GitUserName)
	}

	if opts.resolvePath {
		resolvedPath, err := filepath.EvalSymlinks(repo.Root)
		if err != nil {
			resolvedPath = repo.Root
		}
		repo.resolvedPath = resolvedPath
	}

	return repo
}

func runGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func (r *TestRepo) runGitWithEnv(env []string, args ...string) string {
	r.t.Helper()
	return runGit(r.t, r.Root, env, args...)
}

func (r *TestRepo) Run(args ...string) string {
	r.t.Helper()
	return r.runGitWithEnv(nil, args...)
}

// NewTestRepo creates a temporary git repository.
func NewTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	return newTestRepo(t, testRepoOptions{
		dir:      t.TempDir(),
		initArgs: []string{"init"},
	})
}

// NewTestRepoWithCommit creates a temporary git repository with a file and
// initial commit, suitable for tests that need a valid git history.
func NewTestRepoWithCommit(t *testing.T) *TestRepo {
	t.Helper()

	repo := newTestRepo(t, testRepoOptions{
		dir:           t.TempDir(),
		initArgs:      []string{"init"},
		configureUser: true,
	})

	repo.WriteFile("main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")
	repo.RunGit("add", "main.go")
	repo.RunGit("commit", "-m", "initial commit")

	return repo
}

// InitTestRepo creates a standard test repository with an initial commit on the main branch.
func InitTestRepo(t *testing.T) *TestRepo {
	t.Helper()

	repo := newTestRepo(t, testRepoOptions{
		dir:           t.TempDir(),
		initArgs:      []string{"init"},
		configureUser: true,
	})

	repo.SymbolicRef("HEAD", "refs/heads/main")
	repo.CommitFile("base.txt", "base", "base commit")

	return repo
}

func (r *TestRepo) Path() string {
	if r.resolvedPath != "" {
		return r.resolvedPath
	}
	return r.Root
}

func (r *TestRepo) HeadSHA() string {
	r.t.Helper()
	return r.RevParse("HEAD")
}

// RunGit runs a git command in the repo directory.
func (r *TestRepo) RunGit(args ...string) {
	r.t.Helper()
	r.Run(args...)
}

// RevParse runs git rev-parse and returns the trimmed output.
func (r *TestRepo) RevParse(args ...string) string {
	r.t.Helper()
	return r.Run(append([]string{"rev-parse"}, args...)...)
}

// WriteFile writes a file relative to the repo root, creating parent directories as needed.
func (r *TestRepo) WriteFile(name, content string) {
	r.t.Helper()

	path := filepath.Join(r.Root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// CommitFile writes a file, stages it, commits, and returns the new HEAD SHA.
func (r *TestRepo) CommitFile(filename, content, msg string) string {
	r.t.Helper()

	r.WriteFile(filename, content)
	r.RunGit("add", filename)
	r.RunGit("commit", "-m", msg)
	return r.HeadSHA()
}

// Config sets a git config value.
func (r *TestRepo) Config(key, value string) {
	r.t.Helper()
	r.RunGit("config", key, value)
}

// Checkout runs git checkout.
func (r *TestRepo) Checkout(args ...string) {
	r.t.Helper()
	allArgs := append([]string{"checkout"}, args...)
	r.RunGit(allArgs...)
}

// SymbolicRef runs git symbolic-ref.
func (r *TestRepo) SymbolicRef(ref, target string) {
	r.t.Helper()
	r.RunGit("symbolic-ref", ref, target)
}

func NewGitRepo(t *testing.T) *TestRepo {
	t.Helper()
	return newTestRepo(t, testRepoOptions{
		dir:           t.TempDir(),
		initArgs:      []string{"init", "-b", "main"},
		configureUser: true,
		resolvePath:   true,
	})
}

// InitTestGitRepo initializes a git repository with a commit in the given directory.
// Creates the directory if it doesn't exist, runs git init, configures user, creates
// a test file, and makes an initial commit.
func InitTestGitRepo(t *testing.T, dir string) {
	t.Helper()

	repo := newTestRepo(t, testRepoOptions{
		dir:           dir,
		initArgs:      []string{"init"},
		configureUser: true,
	})
	repo.CommitFile("test.txt", "test content", "initial commit")
}

// GetHeadSHA returns the HEAD commit SHA for the git repo at dir.
func GetHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	return runGit(t, dir, nil, "rev-parse", "HEAD")
}
