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
	// Initialize a new git repository in a temporary directory.
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
