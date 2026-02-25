package prompt

import (
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

const testAuthor = "test"

// setupTestRepo creates a git repo with multiple commits and returns the repo path and commit SHAs
func setupTestRepo(t *testing.T) (string, []string) {
	t.Helper()
	r := newTestRepo(t)

	var commits []string

	// Create 6 commits so we can test with 5 previous commits
	for i := 1; i <= 6; i++ {
		filename := filepath.Join(r.dir, "file.txt")
		content := strings.Repeat("x", i) // Different content each time
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		r.git("add", "file.txt")
		r.git("commit", "-m", "commit "+string(rune('0'+i)))

		sha := r.git("rev-parse", "HEAD")
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

// setupGuidelinesRepo creates a git repo with .roborev.toml on the
// default branch and optionally a feature branch with different
// guidelines. Returns guidelinesRepoContext.
//
// When setupGit is provided, it takes full control of commit creation
// and remote setup (the caller must set up origin/fetch/symbolic-ref
// if needed). This avoids running git fetch on a repo where setupGit
// may have corrupted objects.
func setupGuidelinesRepo(t *testing.T, defaultBranch, baseGuidelines, branchGuidelines string, setupGit func(t *testing.T, r *testRepo)) guidelinesRepoContext {
	t.Helper()
	r := newTestRepoWithBranch(t, defaultBranch)
	if setupGit != nil {
		setupGit(t, r)
		return guidelinesRepoContext{
			Dir:     r.dir,
			BaseSHA: r.git("rev-parse", "HEAD"),
		}
	}

	// Initial commit with base guidelines
	if baseGuidelines != "" {
		toml := `review_guidelines = """` + "\n" + baseGuidelines + "\n" + `"""` + "\n"
		if err := os.WriteFile(filepath.Join(r.dir, ".roborev.toml"), []byte(toml), 0644); err != nil {
			t.Fatalf("write .roborev.toml: %v", err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(r.dir, "README.md"), []byte("init"), 0644); err != nil {
			t.Fatalf("write README.md: %v", err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-m", "initial")
	baseSHA := r.git("rev-parse", "HEAD")

	// Set up origin pointing to itself so origin/<branch> exists
	r.git("remote", "add", "origin", r.dir)
	r.git("fetch", "origin")
	// Set origin/HEAD to point to the default branch
	r.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+defaultBranch)

	// Create feature branch with different guidelines
	var featureSHA string
	if branchGuidelines != "" {
		r.git("checkout", "-b", "feature-branch")
		toml := `review_guidelines = """` + "\n" + branchGuidelines + "\n" + `"""` + "\n"
		if err := os.WriteFile(filepath.Join(r.dir, ".roborev.toml"), []byte(toml), 0644); err != nil {
			t.Fatalf("write .roborev.toml: %v", err)
		}
		r.git("add", ".roborev.toml")
		r.git("commit", "-m", "update guidelines on branch")
		featureSHA = r.git("rev-parse", "HEAD")
		r.git("checkout", defaultBranch)
	}

	return guidelinesRepoContext{
		Dir:        r.dir,
		BaseSHA:    baseSHA,
		FeatureSHA: featureSHA,
	}
}
