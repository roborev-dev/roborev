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

const (
	testGitUser  = "Test User"
	testGitEmail = "test@example.com"
)

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	return initTestRepo(t)
}

func newTestRepoWithBranch(t *testing.T, branch string) *testRepo {
	t.Helper()
	return initTestRepo(t, "-b", branch)
}

func initTestRepo(t *testing.T, initArgs ...string) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git(append([]string{"init"}, initArgs...)...)
	r.configure()
	return r
}

func (r *testRepo) configure() {
	r.t.Helper()
	r.git("config", "user.email", testGitEmail)
	r.git("config", "user.name", testGitUser)
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+testGitUser,
		"GIT_AUTHOR_EMAIL="+testGitEmail,
		"GIT_COMMITTER_NAME="+testGitUser,
		"GIT_COMMITTER_EMAIL="+testGitEmail,
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
		t.Errorf("%s: expected to find %q in document:\n%s", msg, substring, doc)
	}
}

func assertNotContains(t *testing.T, doc, substring, msg string) {
	t.Helper()
	if strings.Contains(doc, substring) {
		t.Errorf("%s: expected NOT to find %q in document:\n%s", msg, substring, doc)
	}
}
