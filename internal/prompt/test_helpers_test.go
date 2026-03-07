package prompt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
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
	r.git("config", "core.hooksPath", os.DevNull)
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

func (r *testRepo) WriteFile(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

func (r *testRepo) CommitAll(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-m", msg)
	return r.git("rev-parse", "HEAD")
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

const testAuthor = "test"

// setupTestRepo creates a git repo with multiple commits and returns the repo path and commit SHAs
func setupTestRepo(t *testing.T) (string, []string) {
	t.Helper()
	r := newTestRepo(t)

	var commits []string

	// Create 6 commits so we can test with 5 previous commits
	for i := 1; i <= 6; i++ {
		content := strings.Repeat("x", i) // Different content each time
		r.WriteFile("file.txt", content)
		sha := r.CommitAll("commit " + string(rune('0'+i)))
		commits = append(commits, sha)
	}

	return r.dir, commits
}

func setupDBWithCommits(t *testing.T, repoPath string, commits []string) (*storage.DB, int64) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	for _, sha := range commits {
		if _, err := db.GetOrCreateCommit(repo.ID, sha, testAuthor, "commit message", time.Now()); err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
	}
	return db, repo.ID
}

type guidelinesRepoContext struct {
	Dir        string
	BaseSHA    string
	FeatureSHA string
}

type GuidelinesRepoOpts struct {
	DefaultBranch    string
	BaseGuidelines   string
	BranchGuidelines string
	SetupGitOverride func(t *testing.T, r *testRepo)
}

func formatGuidelinesTOML(guidelines string) string {
	return fmt.Sprintf("review_guidelines = \"\"\"\n%s\n\"\"\"\n", guidelines)
}

// setupGuidelinesRepo creates a git repo with .roborev.toml on the
// default branch and optionally a feature branch with different
// guidelines. Returns guidelinesRepoContext.
//
// When setupGit is provided, it takes full control of commit creation
// and remote setup (the caller must set up origin/fetch/symbolic-ref
// if needed). This avoids running git fetch on a repo where setupGit
// may have corrupted objects.
func setupGuidelinesRepo(t *testing.T, opts GuidelinesRepoOpts) guidelinesRepoContext {
	t.Helper()
	r := newTestRepoWithBranch(t, opts.DefaultBranch)
	if opts.SetupGitOverride != nil {
		opts.SetupGitOverride(t, r)
		return guidelinesRepoContext{
			Dir:     r.dir,
			BaseSHA: r.git("rev-parse", "HEAD"),
		}
	}

	// Initial commit with base guidelines
	if opts.BaseGuidelines != "" {
		r.WriteFile(".roborev.toml", formatGuidelinesTOML(opts.BaseGuidelines))
	} else {
		r.WriteFile("README.md", "init")
	}
	baseSHA := r.CommitAll("initial")

	// Set up origin pointing to itself so origin/<branch> exists
	r.git("remote", "add", "origin", r.dir)
	r.git("fetch", "origin")
	// Set origin/HEAD to point to the default branch
	r.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+opts.DefaultBranch)

	// Create feature branch with different guidelines
	var featureSHA string
	if opts.BranchGuidelines != "" {
		r.git("checkout", "-b", "feature-branch")
		r.WriteFile(".roborev.toml", formatGuidelinesTOML(opts.BranchGuidelines))
		featureSHA = r.CommitAll("update guidelines on branch")
		r.git("checkout", opts.DefaultBranch)
	}

	return guidelinesRepoContext{
		Dir:        r.dir,
		BaseSHA:    baseSHA,
		FeatureSHA: featureSHA,
	}
}
