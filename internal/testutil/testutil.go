// Package testutil provides shared test utilities for roborev tests.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

const (
	GitUserName  = "Test"
	GitUserEmail = "test@test.com"
)

// TestRepo encapsulates a temporary git repository for tests.
type TestRepo struct {
	Root     string
	GitDir   string
	HooksDir string
	HookPath string
	t        *testing.T
}

// NewTestRepo creates a temporary git repository with hooks suppressed
// to prevent global post-commit hooks from leaking test data into the
// production daemon.
func NewTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	tmpDir := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "core.hooksPath", filepath.Join(tmpDir, ".git", "hooks")},
	} {
		cmd := newIsolatedGitCmd(tmpDir, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	return &TestRepo{
		Root:     tmpDir,
		GitDir:   filepath.Join(tmpDir, ".git"),
		HooksDir: filepath.Join(tmpDir, ".git", "hooks"),
		HookPath: filepath.Join(tmpDir, ".git", "hooks", "post-commit"),
		t:        t,
	}
}

// NewTestRepoWithCommit creates a temporary git repository with a file and
// initial commit, suitable for tests that need a valid git history.
func NewTestRepoWithCommit(t *testing.T) *TestRepo {
	t.Helper()
	repo := NewTestRepo(t)

	runGit := func(args ...string) {
		cmd := newIsolatedGitCmd(repo.Root, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("config", "user.email", GitUserEmail)
	runGit("config", "user.name", GitUserName)

	if err := os.WriteFile(filepath.Join(repo.Root, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runGit("add", "main.go")
	runGit("commit", "-m", "initial commit")

	return repo
}

// InitTestRepo creates a standard test repository with an initial commit on the main branch.
func InitTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	repo := NewTestRepo(t)
	repo.RunGit("init")
	repo.SymbolicRef("HEAD", "refs/heads/main")
	repo.Config("user.email", GitUserEmail)
	repo.Config("user.name", GitUserName)
	repo.CommitFile("base.txt", "base", "base commit")
	return repo
}

// RunGit runs a git command in the repo directory.
func (r *TestRepo) RunGit(args ...string) {
	r.t.Helper()
	cmd := newIsolatedGitCmd(r.Root, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// RevParse runs git rev-parse and returns the trimmed output.
func (r *TestRepo) RevParse(args ...string) string {
	r.t.Helper()
	cmd := newIsolatedGitCmd(r.Root, append([]string{"rev-parse"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git rev-parse %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// CommitFile writes a file, stages it, commits, and returns the new HEAD SHA.
func (r *TestRepo) CommitFile(filename, content, msg string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.Root, filename), []byte(content), 0644); err != nil {
		r.t.Fatal(err)
	}
	r.RunGit("add", filename)
	r.RunGit("commit", "-m", msg)
	return r.RevParse("HEAD")
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

// Chdir changes the working directory to the repo root and returns a
// restore function. The caller should defer the returned function.
func (r *TestRepo) Chdir() func() {
	r.t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.Chdir(r.Root); err != nil {
		r.t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(orig); err != nil {
			r.t.Fatal(err)
		}
	}
}

// getActiveHooksDir resolves the current hooks path using git.
func (r *TestRepo) getActiveHooksDir() string {
	r.t.Helper()
	hooksDir := r.RevParse("--git-path", "hooks")
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(r.Root, hooksDir)
	}
	return hooksDir
}

// WriteHook writes a post-commit hook with the given content.
func (r *TestRepo) WriteHook(content string) {
	r.t.Helper()
	r.WriteNamedHook("post-commit", content)
}

// WriteNamedHook writes a hook with the given name and content.
func (r *TestRepo) WriteNamedHook(name, content string) {
	r.t.Helper()
	hooksDir := r.getActiveHooksDir()
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		r.t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, name)
	if err := os.WriteFile(hookPath, []byte(content), 0755); err != nil {
		r.t.Fatal(err)
	}
}

// GetHookPath returns the path to a specific hook in the test repository.
func (r *TestRepo) GetHookPath(name string) string {
	r.t.Helper()
	return filepath.Join(r.getActiveHooksDir(), name)
}

// RemoveHooksDir removes the active hooks directory.
func (r *TestRepo) RemoveHooksDir() {
	r.t.Helper()
	hooksDir := r.getActiveHooksDir()
	// Don't try to remove /dev/null
	if hooksDir == os.DevNull {
		return
	}
	if err := os.RemoveAll(hooksDir); err != nil {
		r.t.Fatal(err)
	}
}

// MockExecutable creates a fake executable in PATH that exits with the given code.
func MockExecutable(t *testing.T, binName string, exitCode int) {
	t.Helper()
	tmpBin := t.TempDir()
	var path string
	var content []byte

	if runtime.GOOS == "windows" {
		path = filepath.Join(tmpBin, binName+".bat")
		content = fmt.Appendf(nil, "@exit /b %d\r\n", exitCode)
	} else {
		path = filepath.Join(tmpBin, binName)
		content = fmt.Appendf(nil, "#!/bin/sh\nexit %d\n", exitCode)
	}

	if err := os.WriteFile(path, content, 0755); err != nil {
		t.Fatal(err)
	}

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpBin+string(os.PathListSeparator)+origPath)

	t.Cleanup(func() {
		os.Setenv("PATH", origPath)
	})
}

// MockExecutableIsolated creates a fake executable in a new directory and sets PATH to ONLY that directory.
func MockExecutableIsolated(t *testing.T, binName string, exitCode int) {
	t.Helper()
	tmpBin := t.TempDir()
	var path string
	var content []byte

	if runtime.GOOS == "windows" {
		path = filepath.Join(tmpBin, binName+".bat")
		content = fmt.Appendf(nil, "@exit /b %d\r\n", exitCode)
	} else {
		path = filepath.Join(tmpBin, binName)
		content = fmt.Appendf(nil, "#!/bin/sh\nexit %d\n", exitCode)
	}

	if err := os.WriteFile(path, content, 0755); err != nil {
		t.Fatal(err)
	}

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpBin)

	t.Cleanup(func() {
		os.Setenv("PATH", origPath)
	})
}

// MockBinaryInPath creates a fake executable in PATH.
func MockBinaryInPath(t *testing.T, binName, scriptContent string) {
	t.Helper()
	tmpBin := t.TempDir()

	path := filepath.Join(tmpBin, binName)
	if err := os.WriteFile(path, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpBin+string(os.PathListSeparator)+origPath)

	t.Cleanup(func() {
		os.Setenv("PATH", origPath)
	})
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
		t.Fatalf("Expected status %d, got %d: %s", expected, w.Code, w.Body.String())
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

	for i := range count {
		sha := fmt.Sprintf("sha%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Test Author", fmt.Sprintf("Test commit %d", i), time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}

		job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Agent: agent})
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

	job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Agent: agent})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	return job
}

// InitTestGitRepo initializes a git repository with a commit in the given directory.
// Creates the directory if it doesn't exist, runs git init, configures user, creates
// a test file, and makes an initial commit.
func InitTestGitRepo(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create repo dir: %v", err)
	}

	cmds := [][]string{
		{"init"},
		{"config", "core.hooksPath", filepath.Join(dir, ".git", "hooks")},
		{"config", "user.email", GitUserEmail},
		{"config", "user.name", GitUserName},
	}
	for _, args := range cmds {
		cmd := newIsolatedGitCmd(dir, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	addCmd := newIsolatedGitCmd(dir, "add", ".")
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commitCmd := newIsolatedGitCmd(dir, "commit", "-m", "initial commit")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}
}

// GetHeadSHA returns the HEAD commit SHA for the git repo at dir.
func GetHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := newIsolatedGitCmd(dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// MakeJSONRequest creates an HTTP request with the given body marshaled as JSON.
func MakeJSONRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()

	reqBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ReceiveWithTimeout reads a single value from a channel, failing the test
// if nothing is received within the given timeout.
func ReceiveWithTimeout[T any](t *testing.T, ch <-chan T, timeout time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatal("Timed out waiting to receive from channel")
		var zero T
		return zero
	}
}

// WaitForJobStatus polls until the job reaches one of the expected statuses or
// the timeout expires. Returns the final job state.
func WaitForJobStatus(t *testing.T, db *storage.DB, jobID int64, timeout time.Duration, statuses ...storage.JobStatus) *storage.ReviewJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := db.GetJobByID(jobID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if slices.Contains(statuses, job.Status) {
			return job
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Job %d did not reach any of %v within %v", jobID, statuses, timeout)
	return nil
}

// CreateCompletedReview creates a commit (if needed) and a completed review job.
// Returns the created job.
// NOTE: Uses ClaimJob which claims the next available job globally. This is safe
// when each test uses an isolated DB (via OpenTestDB), but callers must not
// enqueue multiple jobs before calling this helper.
func CreateCompletedReview(t *testing.T, db *storage.DB, repoID int64, sha, agent, reviewText string) *storage.ReviewJob {
	t.Helper()

	commit, err := db.GetOrCreateCommit(repoID, sha, GitUserName, "test commit", time.Now())
	if err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}

	job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repoID, CommitID: commit.ID, GitRef: sha, Agent: agent})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	if _, err := db.ClaimJob("test-worker"); err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if err := db.CompleteJob(job.ID, "test-worker", "prompt", reviewText); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	return job
}

// ReviewComment represents a comment on a review with a defined order.
type ReviewComment struct {
	User string
	Text string
}

// CreateReviewWithComments creates a completed review and adds comments to it.
// Comments are added in slice order to ensure deterministic behavior.
func CreateReviewWithComments(t *testing.T, db *storage.DB, repoID int64, sha, reviewText string, comments []ReviewComment) *storage.ReviewJob {
	t.Helper()

	job := CreateCompletedReview(t, db, repoID, sha, "test", reviewText)
	for _, c := range comments {
		if _, err := db.AddCommentToJob(job.ID, c.User, c.Text); err != nil {
			t.Fatalf("AddCommentToJob failed: %v", err)
		}
	}

	return job
}

// DecodeJSON unmarshals the response body from an httptest.ResponseRecorder into v.
func DecodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()

	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
}

// Ptr returns a pointer to the given value.
func Ptr[T any](v T) *T {
	return &v
}

// MapKeys returns the keys of the map m.
func MapKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
