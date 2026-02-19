package prompt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	// Default to "master" or "main" depending on git version, but here we force "main" for consistency
	// actually git init might not support -b in older versions, but let's assume modern git environment given the context.
	// If -b is not supported, we can just init and then branch.
	// But let's stick to simple init if branch name is not crucial for most tests, or use -b if we want to be explicit.
	// The original code used `git init` (default branch) and `git init -b` (explicit branch).
	
	// Let's implement newTestRepo to just call init without branch, matching original behavior for setupTestRepo
	// but setupGuidelinesRepo used -b.
	
	dir := t.TempDir()
	r := &testRepo{t: t, dir: dir}
	r.git("init")
	r.configure()
	return r
}

func newTestRepoWithBranch(t *testing.T, branch string) *testRepo {
	t.Helper()
	dir := t.TempDir()
	r := &testRepo{t: t, dir: dir}
	r.git("init", "-b", branch)
	r.configure()
	return r
}

func (r *testRepo) configure() {
	r.t.Helper()
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test User")
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func assertContains(t *testing.T, doc, substring, msg string) {
	t.Helper()
	if !strings.Contains(doc, substring) {
		t.Errorf("%s: expected to find %q", msg, substring)
	}
}

func assertNotContains(t *testing.T, doc, substring, msg string) {
	t.Helper()
	if strings.Contains(doc, substring) {
		t.Errorf("%s: expected NOT to find %q", msg, substring)
	}
}
