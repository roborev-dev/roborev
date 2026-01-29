// Package testutil provides shared test utilities for roborev tests.
package testutil

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

// TestRepo encapsulates a temporary git repository for tests.
type TestRepo struct {
	Root     string
	GitDir   string
	HooksDir string
	HookPath string
	t        *testing.T
}

// NewTestRepo creates a temporary git repository.
func NewTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	return &TestRepo{
		Root:     tmpDir,
		GitDir:   filepath.Join(tmpDir, ".git"),
		HooksDir: filepath.Join(tmpDir, ".git", "hooks"),
		HookPath: filepath.Join(tmpDir, ".git", "hooks", "post-commit"),
		t:        t,
	}
}

// Chdir changes the working directory to the repo root and returns a
// restore function. The caller should defer the returned function.
func (r *TestRepo) Chdir() func() {
	r.t.Helper()
	orig, _ := os.Getwd()
	if err := os.Chdir(r.Root); err != nil {
		r.t.Fatal(err)
	}
	return func() { os.Chdir(orig) }
}

// WriteHook writes a post-commit hook with the given content.
func (r *TestRepo) WriteHook(content string) {
	r.t.Helper()
	if err := os.MkdirAll(r.HooksDir, 0755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(r.HookPath, []byte(content), 0755); err != nil {
		r.t.Fatal(err)
	}
}

// RemoveHooksDir removes the .git/hooks directory.
func (r *TestRepo) RemoveHooksDir() {
	r.t.Helper()
	if err := os.RemoveAll(r.HooksDir); err != nil {
		r.t.Fatal(err)
	}
}

// MockBinaryInPath creates a fake executable in PATH and returns a cleanup function.
func MockBinaryInPath(t *testing.T, binName, scriptContent string) func() {
	t.Helper()
	tmpBin := t.TempDir()

	path := filepath.Join(tmpBin, binName)
	if err := os.WriteFile(path, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpBin+string(os.PathListSeparator)+origPath)

	return func() {
		os.Setenv("PATH", origPath)
	}
}

// OpenTestDB creates a test database in a temporary directory.
// The database is automatically closed when the test completes.
func OpenTestDB(t *testing.T) *storage.DB {
	t.Helper()

	db, _ := OpenTestDBWithDir(t)
	return db
}

// OpenTestDBWithDir creates a test database and returns both the DB and the
// temporary directory path. Useful when tests need to create repos or other
// files in the same directory. The database is automatically closed when
// the test completes.
func OpenTestDBWithDir(t *testing.T) (*storage.DB, string) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
	})

	return db, tmpDir
}

// AssertStatusCode checks that the response has the expected HTTP status code.
// On failure, it reports the response body for debugging.
func AssertStatusCode(t *testing.T, w *httptest.ResponseRecorder, expected int) {
	t.Helper()

	if w.Code != expected {
		t.Errorf("Expected status %d, got %d: %s", expected, w.Code, w.Body.String())
	}
}

// CreateTestRepo creates a test repository in the database.
// Uses the test's temp directory as the repo path.
func CreateTestRepo(t *testing.T, db *storage.DB) *storage.Repo {
	t.Helper()

	tmpDir := t.TempDir()
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	return repo
}

// CreateTestJobs creates the specified number of test jobs in a repository.
// Each job is created with a unique SHA (sha0, sha1, etc.) and the specified agent.
// Returns the created jobs in order.
func CreateTestJobs(t *testing.T, db *storage.DB, repo *storage.Repo, count int, agent string) []*storage.ReviewJob {
	t.Helper()

	jobs := make([]*storage.ReviewJob, 0, count)

	for i := 0; i < count; i++ {
		sha := fmt.Sprintf("sha%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Test Author", fmt.Sprintf("Test commit %d", i), time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}

		job, err := db.EnqueueJob(repo.ID, commit.ID, sha, "", agent, "", "")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}

		jobs = append(jobs, job)
	}

	return jobs
}

// CreateTestJobWithSHA creates a single test job with a specific SHA.
func CreateTestJobWithSHA(t *testing.T, db *storage.DB, repo *storage.Repo, sha, agent string) *storage.ReviewJob {
	t.Helper()

	commit, err := db.GetOrCreateCommit(repo.ID, sha, "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	job, err := db.EnqueueJob(repo.ID, commit.ID, sha, "", agent, "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	return job
}
