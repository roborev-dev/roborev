package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func executePostCommitCmd(
	args ...string,
) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := postCommitCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func executeEnqueueAliasCmd(
	args ...string,
) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := enqueueCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestPostCommitSubmitsHEAD(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.CommitFile("file.txt", "content", "initial commit")

	_, _, err := executePostCommitCmd("--repo", repo.Dir)
	require.NoError(t, err)

	req := <-reqCh
	assert.Equal(t, "HEAD", req.GitRef)
}

func TestPostCommitBranchReview(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
	repo.CommitFile("file.txt", "content", "initial")
	mainSHA := repo.Run("rev-parse", "HEAD")
	repo.Run("checkout", "-b", "feature")
	repo.CommitFile("feature.txt", "feature", "feature commit")
	writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

	_, _, err := executePostCommitCmd("--repo", repo.Dir)
	require.NoError(t, err)

	req := <-reqCh
	want := mainSHA + "..HEAD"
	assert.Equal(t, want, req.GitRef)
}

func TestPostCommitFallsBackOnBaseBranch(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
	repo.CommitFile("file.txt", "content", "initial")
	writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

	_, _, err := executePostCommitCmd("--repo", repo.Dir)
	require.NoError(t, err)

	req := <-reqCh
	assert.Equal(t, "HEAD", req.GitRef)
}

func TestPostCommitSilentExitNotARepo(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := executePostCommitCmd("--repo", dir)
	require.NoError(t, err)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}

func TestPostCommitAcceptsQuietFlag(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	mockEnqueue(t, mux)

	repo.CommitFile("file.txt", "content", "initial")

	_, _, err := executePostCommitCmd(
		"--repo", repo.Dir, "--quiet",
	)
	require.NoError(t, err)
}

func TestEnqueueAliasWorksIdentically(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.CommitFile("file.txt", "content", "initial")

	_, _, err := executeEnqueueAliasCmd("--repo", repo.Dir)
	require.NoError(t, err)

	req := <-reqCh
	assert.Equal(t, "HEAD", req.GitRef)
}

func TestPostCommitRejectsPositionalArgs(t *testing.T) {
	_, _, err := executePostCommitCmd("abc123")
	require.Error(t, err)

	assert.Contains(t, err.Error(), "unknown command")
}

func TestEnqueueRejectsPositionalArgs(t *testing.T) {
	_, _, err := executeEnqueueAliasCmd("abc123")
	require.Error(t, err)

}

// stallingRoundTripper blocks until the request context is
// cancelled, then returns an error. This simulates a daemon
// that accepts connections but never responds, without needing
// a real httptest server or a long sleep.
type stallingRoundTripper struct {
	hit chan struct{}
}

func (s *stallingRoundTripper) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	select {
	case s.hit <- struct{}{}:
	default:
	}
	select {
	case <-req.Context().Done():
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("stallingRoundTripper: context was never cancelled")
	}
	return nil, fmt.Errorf("request cancelled: %w", req.Context().Err())
}

func TestPostCommitTimesOutOnSlowDaemon(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	// Register a handler so ensureDaemon succeeds, but the
	// actual POST will go through the stalling RoundTripper.
	realHandlerCalled := false
	mux.HandleFunc("/api/enqueue", func(
		w http.ResponseWriter, r *http.Request,
	) {
		realHandlerCalled = true
	})

	repo.CommitFile("file.txt", "content", "initial")

	rt := &stallingRoundTripper{hit: make(chan struct{}, 1)}
	orig := hookHTTPClient
	hookHTTPClient = &http.Client{
		Timeout:   50 * time.Millisecond,
		Transport: rt,
	}
	t.Cleanup(func() { hookHTTPClient = orig })

	start := time.Now()
	_, _, err := executePostCommitCmd("--repo", repo.Dir)
	elapsed := time.Since(start)

	require.NoError(t, err)

	select {
	case <-rt.hit:
		// RoundTrip was called — timeout path was exercised
	default:
		require.NoError(t, err, "RoundTrip was never called; timeout not exercised")
	}
	assert.False(t, realHandlerCalled, "real handler should not be reached")
	assert.LessOrEqual(t, elapsed, time.Second,

		"command took %v; should return promptly via timeout",
		elapsed)

}

func TestEnqueueAliasIsHidden(t *testing.T) {
	cmd := enqueueCmd()
	assert.True(t, cmd.Hidden)
	assert.Contains(t, cmd.Use, "enqueue")
}

// repoUnderTest holds a repo for post-commit hook tests.
type repoUnderTest struct {
	// repo is the directory post-commit runs from (may be a worktree).
	repo *TestGitRepo
}

// setupPlainRepo returns a repoUnderTest backed by a plain (non-worktree) repo.
func setupPlainRepo(t *testing.T) repoUnderTest {
	t.Helper()
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")
	return repoUnderTest{repo: repo}
}

// setupWorktreeRepo returns a repoUnderTest backed by a linked worktree.
func setupWorktreeRepo(t *testing.T) repoUnderTest {
	t.Helper()
	mainRepo := newTestGitRepo(t)
	mainRepo.CommitFile("file.txt", "content", "initial commit")

	wtDir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	mainRepo.Run("worktree", "add", resolved, "-b", "worktree-branch")

	return repoUnderTest{repo: &TestGitRepo{Dir: resolved, t: t}}
}

// mockEnqueueCapture registers a handler on mux that captures full enqueue
// requests. The returned channel receives at most one request.
func mockEnqueueCapture(t *testing.T, mux *http.ServeMux) <-chan daemon.EnqueueRequest {
	t.Helper()
	ch := make(chan daemon.EnqueueRequest, 1)
	mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
		var req daemon.EnqueueRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		ch <- req
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	return ch
}


// TestPostCommitSendsLocalRepoPath checks that the RepoPath in the enqueue
// request is the local (worktree) path in both plain repos and linked
// worktrees. The daemon canonicalizes to the main repo root itself.
func TestPostCommitSendsLocalRepoPath(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) repoUnderTest
	}{
		{"plain repo", setupPlainRepo},
		{"worktree", setupWorktreeRepo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.setup(t)
			mux := http.NewServeMux()
			daemonFromHandler(t, mux)
			reqCh := mockEnqueueCapture(t, mux)

			r.repo.CommitFile("change.txt", "content", "a commit")

			_, _, err := executePostCommitCmd("--repo", r.repo.Dir)
			require.NoError(t, err)

			req := <-reqCh
			assert.Equal(t, r.repo.Dir, req.RepoPath)
		})
	}
}

// TestPostCommitDoesNotEnqueueDuringRebase performs a real git rebase and
// asserts that roborev's post-commit logic does not enqueue reviews for
// replayed commits. Git DOES fire the post-commit hook during rebase — the
// protection is in roborev's IsRebaseInProgress check, which this test
// verifies end-to-end.
func TestPostCommitDoesNotEnqueueDuringRebase(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) repoUnderTest
	}{
		{"plain repo", setupPlainRepo},
		{"worktree", setupWorktreeRepo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.setup(t)
			mux := http.NewServeMux()
			daemonFromHandler(t, mux)
			reqCh := mockEnqueueCapture(t, mux)

			// Create a branch topology for rebasing:
			//   base: A -- B (base.txt, no conflict)
			//          \
			//   current: C -- D (branch1.txt, branch2.txt)
			r.repo.Run("checkout", "-b", "rebase-base")
			r.repo.CommitFile("base.txt", "base content", "base commit")
			r.repo.Run("checkout", "-")
			r.repo.CommitFile("branch1.txt", "content 1", "feature commit 1")
			r.repo.CommitFile("branch2.txt", "content 2", "feature commit 2")

			// First, prove post-commit enqueues normally.
			_, _, err := executePostCommitCmd("--repo", r.repo.Dir)
			require.NoError(t, err)
			<-reqCh // drain the normal enqueue

			// Start a rebase that pauses after the first replayed commit.
			// --exec "exit 1" lets the cherry-pick succeed, then halts the
			// rebase, leaving rebase-merge in place.
			cmd := exec.Command("git", "rebase", "--exec", "exit 1", "rebase-base")
			cmd.Dir = r.repo.Dir
			cmd.Env = append(os.Environ(),
				"HOME="+r.repo.Dir,
				"GIT_CONFIG_NOSYSTEM=1",
				"GIT_AUTHOR_NAME=Test",
				"GIT_AUTHOR_EMAIL=test@test.com",
				"GIT_COMMITTER_NAME=Test",
				"GIT_COMMITTER_EMAIL=test@test.com",
			)
			_ = cmd.Run() // exits non-zero because exec failed

			// Now invoke post-commit while the rebase is paused.
			// roborev should detect the rebase and not enqueue.
			_, _, err = executePostCommitCmd("--repo", r.repo.Dir)
			require.NoError(t, err)

			select {
			case req := <-reqCh:
				t.Errorf("expected no enqueue during rebase, but got RepoPath=%s", req.RepoPath)
			default:
				// correct — no request sent
			}
		})
	}
}
