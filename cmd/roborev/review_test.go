package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func respondJSON(
	w http.ResponseWriter, status int, payload any,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func executeReviewCmd(
	args ...string,
) (string, string, error) {
	var stdout, stderr bytes.Buffer

	cmd := reviewCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func setupTestEnvironment(
	t *testing.T,
) (*TestGitRepo, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	daemonFromHandler(t, mux)
	return newTestGitRepo(t), mux
}

type capturedEnqueue struct {
	GitRef    string `json:"git_ref"`
	Reasoning string `json:"reasoning"`
}

func mockEnqueue(
	t *testing.T, mux *http.ServeMux,
) <-chan capturedEnqueue {
	t.Helper()
	ch := make(chan capturedEnqueue, 1)
	mux.HandleFunc("/api/enqueue", func(
		w http.ResponseWriter, r *http.Request,
	) {
		var req capturedEnqueue
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err, "decode enqueue request")
		select {
		case ch <- req:
		default:
			assert.Condition(t, func() bool {
				return false
			}, "mockEnqueue: unexpected extra enqueue request")
			http.Error(w, "duplicate request", http.StatusConflict)
			return
		}
		respondJSON(w, http.StatusCreated, storage.ReviewJob{
			ID: 1, GitRef: req.GitRef, Agent: "test",
		})
	})
	return ch
}

func mockWaitableReview(
	t *testing.T, mux *http.ServeMux, output string,
) {
	t.Helper()
	mux.HandleFunc("/api/enqueue", func(
		w http.ResponseWriter, r *http.Request,
	) {
		respondJSON(w, http.StatusCreated, storage.ReviewJob{
			ID: 1, GitRef: "abc123", Agent: "test",
			Status: "queued",
		})
	})
	mux.HandleFunc("/api/jobs", func(
		w http.ResponseWriter, r *http.Request,
	) {
		job := storage.ReviewJob{
			ID: 1, GitRef: "abc123", Agent: "test",
			Status: "done",
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"jobs": []storage.ReviewJob{job}, "has_more": false,
		})
	})
	mux.HandleFunc("/api/review", func(
		w http.ResponseWriter, r *http.Request,
	) {
		respondJSON(w, http.StatusOK, storage.Review{
			ID: 1, JobID: 1, Agent: "test", Output: output,
		})
	})
}

func TestEnqueueCmdPositionalArg(t *testing.T) {
	t.Run("positional arg overrides default HEAD", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		shortFirstSHA := firstSHA[:7]
		_, _, err := executeReviewCmd("--repo", repo.Dir, shortFirstSHA)
		require.NoError(t, err, "enqueue failed: %v")

		req := <-reqCh
		assert.Equal(t, req.GitRef, shortFirstSHA, "unexpected condition")
		assert.NotEqual(t, "HEAD", req.GitRef, "unexpected condition")
	})

	t.Run("sha flag works", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		shortFirstSHA := firstSHA[:7]
		_, _, err := executeReviewCmd("--repo", repo.Dir, "--sha", shortFirstSHA)
		require.NoError(t, err, "enqueue failed: %v")

		req := <-reqCh
		assert.Equal(t, req.GitRef, shortFirstSHA, "unexpected condition")
	})

	t.Run("defaults to HEAD", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.CommitFile("file1.txt", "first", "first commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir)
		require.NoError(t, err, "enqueue failed: %v")

		req := <-reqCh
		assert.Equal(t, "HEAD", req.GitRef, "unexpected condition")
	})
}

func TestEnqueueSkippedBranch(t *testing.T) {
	t.Run("skipped response prints message and exits successfully", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, map[string]any{
				"skipped": true,
				"reason":  "branch \"wip\" is excluded from reviews",
			})
		})

		repo.CommitFile("file.txt", "content", "initial commit")

		stdout, _, err := executeReviewCmd("--repo", repo.Dir)
		require.NoError(t, err, "unexpected condition")

		assert.Contains(t, stdout, "Skipped", "unexpected condition")
	})

	t.Run("skipped response in quiet mode suppresses output", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, map[string]any{
				"skipped": true,
				"reason":  "branch \"wip\" is excluded from reviews",
			})
		})

		repo.CommitFile("file.txt", "content", "initial commit")

		stdout, _, err := executeReviewCmd("--repo", repo.Dir, "--quiet")
		require.NoError(t, err, "unexpected condition")

		assert.Empty(t, stdout, "unexpected condition")
	})
}

func TestWaitQuietVerdictExitCode(t *testing.T) {
	setupFastPolling(t)

	t.Run("passing review exits 0 with no output", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")
		mockWaitableReview(t, mux, "No issues found.")

		stdout, stderr, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

		require.NoError(t, err, "unexpected condition")
		assert.Empty(t, stdout, "unexpected condition")
		assert.Empty(t, stderr, "unexpected condition")
	})

	t.Run("failing review exits 1 with no output", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")
		mockWaitableReview(t, mux,
			"Found 2 issues:\n1. Bug in foo.go\n2. Missing error handling",
		)

		stdout, stderr, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

		require.Error(t, err, "expected review command to fail in this case")
		exitErr, ok := err.(*exitError)
		require.True(t, ok, "expected exitError, got: %T %v", err)
		require.Equal(t, 1, exitErr.code, "expected exit code 1")
		assert.Empty(t, stdout, "unexpected condition")
		assert.Empty(t, stderr, "unexpected condition")
	})
}

func TestWaitForJobUnknownStatus(t *testing.T) {
	setupFastPolling(t)

	t.Run("unknown status exceeds max retries", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		callCount := 0
		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
			respondJSON(w, http.StatusCreated, job)
		})
		mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			callCount++
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "future_status"}
			respondJSON(w, http.StatusOK, map[string]any{
				"jobs":     []storage.ReviewJob{job},
				"has_more": false,
			})
		})

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

		assertErrorContains(t, err, "unknown status")
		assertErrorContains(t, err, "daemon may be newer than CLI")

		assert.Equal(t, 10, callCount, "unexpected condition")
	})

	t.Run("counter resets on known status", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		poller := &mockJobPoller{}

		mux.HandleFunc("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			job := storage.ReviewJob{ID: 1, GitRef: "abc123", Agent: "test", Status: "queued"}
			respondJSON(w, http.StatusCreated, job)
		})
		mux.HandleFunc("/api/jobs", poller.HandleJobs)
		mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, storage.Review{
				ID:     1,
				JobID:  1,
				Agent:  "test",
				Output: "No issues found. LGTM!",
			})
		})

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--wait", "--quiet")

		require.NoError(t, err, "unexpected condition")
		assert.Equal(t, 12, poller.callCount, "unexpected condition")
	})
}

type mockJobPoller struct {
	callCount int
}

func (m *mockJobPoller) HandleJobs(
	w http.ResponseWriter, r *http.Request,
) {
	m.callCount++
	var status string
	switch {
	case m.callCount <= 5:
		status = "future_status"
	case m.callCount == 6:
		status = "running"
	case m.callCount <= 11:
		status = "future_status"
	default:
		status = "done"
	}
	job := storage.ReviewJob{
		ID: 1, GitRef: "abc123", Agent: "test",
		Status: storage.JobStatus(status),
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"jobs":     []storage.ReviewJob{job},
		"has_more": false,
	})
}

func TestReviewFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			"since and branch exclusive",
			[]string{"--since", "abc123", "--branch"},
			[]string{"cannot use --branch with --since"},
		},
		{
			"since and dirty exclusive",
			[]string{"--since", "abc123", "--dirty"},
			[]string{"cannot use --since with --dirty"},
		},
		{
			"since with positional args",
			[]string{"--since", "abc123", "def456"},
			[]string{"cannot specify commits with --since"},
		},
		{
			"branch and dirty exclusive",
			[]string{"--branch", "--dirty"},
			[]string{"cannot use --branch with --dirty"},
		},
		{
			"branch with positional args",
			[]string{"--branch", "abc123"},
			[]string{
				"cannot specify commits with --branch",
				"--branch=<name>",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _ := setupTestEnvironment(t)
			_, _, err := executeReviewCmd(
				append([]string{"--repo", repo.Dir}, tt.args...)...,
			)
			for _, w := range tt.want {
				assertErrorContains(t, err, w)
			}
		})
	}
}

func TestReviewSinceFlag(t *testing.T) {
	t.Run("since with valid ref succeeds", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		firstSHA := repo.CommitFile("file1.txt", "first", "first commit")
		repo.CommitFile("file2.txt", "second", "second commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", firstSHA[:7])
		require.NoError(t, err)

		req := <-reqCh
		assert.Contains(t, req.GitRef, firstSHA, "unexpected condition")
		assert.True(t, strings.HasSuffix(req.GitRef, "..HEAD"), "unexpected condition")
	})

	t.Run("since with invalid ref fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", "nonexistent123")
		assertErrorContains(t, err, "invalid --since commit")
	})

	t.Run("since with no commits ahead fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)
		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--since", "HEAD")
		assertErrorContains(t, err, "no commits since")
	})
}

func TestReviewBranchFlag(t *testing.T) {
	t.Run("branch on default branch fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch")
		assertErrorContains(t, err, "already on main")
	})

	t.Run("branch with no commits fails", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		repo.Run("checkout", "-b", "feature")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch")
		assertErrorContains(t, err, "no commits on branch")
	})

	t.Run("branch review succeeds with commits", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		mainSHA := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch")
		require.NoError(t, err)

		req := <-reqCh
		assert.Contains(t, req.GitRef, mainSHA, "unexpected condition")
		assert.True(t, strings.HasSuffix(req.GitRef, "..HEAD"), "unexpected condition")
	})
}

func TestReviewFastFlag(t *testing.T) {
	t.Run("fast flag sets reasoning to fast", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--fast")
		require.NoError(t, err)

		req := <-reqCh
		assert.Equal(t, "fast", req.Reasoning, "unexpected condition")
	})

	t.Run("explicit reasoning takes precedence over fast", func(t *testing.T) {
		repo, mux := setupTestEnvironment(t)
		reqCh := mockEnqueue(t, mux)

		repo.CommitFile("file.txt", "content", "initial")

		_, _, err := executeReviewCmd("--repo", repo.Dir, "--fast", "--reasoning", "thorough")
		require.NoError(t, err)

		req := <-reqCh
		assert.Equal(t, "thorough", req.Reasoning, "unexpected condition")
	})
}

func TestReviewInvalidArgsNoSideEffects(t *testing.T) {
	repo, mux := setupTestEnvironment(t)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		assert.Condition(t, func() bool {
			return false
		}, "unexpected request %s %s", r.Method, r.URL.Path)
	})

	hooksDir := filepath.Join(repo.Dir, ".git", "hooks")

	_, _, err := executeReviewCmd("--repo", repo.Dir, "--branch", "--dirty")
	assertErrorContains(t, err, "cannot use --branch with --dirty")

	for _, name := range []string{"post-commit", "post-rewrite"} {
		_, statErr := os.Stat(
			filepath.Join(hooksDir, name),
		)
		require.ErrorIs(t, statErr, os.ErrNotExist, "%s should not exist after invalid args", name)
	}
}

func writeRoborevConfig(t *testing.T, repo *TestGitRepo, content string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(repo.Dir, ".roborev.toml"),
		[]byte(content), 0644,
	); err != nil {
		require.NoError(t, err, "write .roborev.toml: %v")
	}
}

func TestTryBranchReview(t *testing.T) {
	t.Run("returns false when no config", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")

		_, ok := tryBranchReview(repo.Dir, "")
		assert.False(t, ok, "unexpected condition")
	})

	t.Run("returns false when config is commit", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")
		writeRoborevConfig(t, repo, `post_commit_review = "commit"`)

		_, ok := tryBranchReview(repo.Dir, "")
		assert.False(t, ok, "unexpected condition")
	})

	t.Run("returns merge-base range when config is branch", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		mainSHA := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")
		writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

		ref, ok := tryBranchReview(repo.Dir, "")
		require.True(t, ok, "expected true with branch config")

		assert.Contains(t, ref, mainSHA, "unexpected condition")
		assert.True(t, strings.HasSuffix(ref, "..HEAD"), "unexpected condition")
	})

	t.Run("covers multiple commits on feature branch", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		mainSHA := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("a.txt", "a", "first feature commit")
		repo.CommitFile("b.txt", "b", "second feature commit")
		repo.CommitFile("c.txt", "c", "third feature commit")
		writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

		ref, ok := tryBranchReview(repo.Dir, "")
		require.True(t, ok, "expected true")

		want := mainSHA + "..HEAD"
		assert.Equal(t, want, ref, "unexpected condition")
	})

	t.Run("returns false on base branch", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

		_, ok := tryBranchReview(repo.Dir, "")
		assert.False(t, ok, "unexpected condition")
	})

	t.Run("uses baseBranch override", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/develop")
		repo.CommitFile("file.txt", "content", "initial")
		developSHA := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", "-b", "feature")
		repo.CommitFile("feature.txt", "feature", "feature commit")
		writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

		ref, ok := tryBranchReview(repo.Dir, "develop")
		require.True(t, ok, "expected true with baseBranch override")

		want := developSHA + "..HEAD"
		assert.Equal(t, want, ref, "unexpected condition")
	})

	t.Run("returns false on detached HEAD", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
		repo.CommitFile("file.txt", "content", "initial")
		sha := repo.Run("rev-parse", "HEAD")
		repo.Run("checkout", sha)
		writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

		_, ok := tryBranchReview(repo.Dir, "")
		assert.False(t, ok, "unexpected condition")
	})
}

func TestReviewIgnoresBranchConfig(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
	repo.CommitFile("file.txt", "content", "initial")
	repo.Run("checkout", "-b", "feature")
	repo.CommitFile("feature.txt", "feature", "feature commit")
	writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

	_, _, err := executeReviewCmd("--repo", repo.Dir)
	require.NoError(t, err)

	req := <-reqCh
	assert.Equal(t, "HEAD", req.GitRef, "unexpected condition")
}

func TestReviewQuietIgnoresBranchConfig(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
	repo.CommitFile("file.txt", "content", "initial")
	repo.Run("checkout", "-b", "feature")
	repo.CommitFile("feature.txt", "feature", "feature commit")
	writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

	_, _, err := executeReviewCmd("--repo", repo.Dir, "--quiet")
	require.NoError(t, err)

	req := <-reqCh
	assert.Equal(t, "HEAD", req.GitRef, "unexpected condition")
}

func TestFindChildGitRepos(t *testing.T) {
	parent := t.TempDir()

	regularRepo := filepath.Join(parent, "regular")
	os.Mkdir(regularRepo, 0755)
	os.Mkdir(filepath.Join(regularRepo, ".git"), 0755)

	worktreeRepo := filepath.Join(parent, "worktree")
	os.Mkdir(worktreeRepo, 0755)
	os.WriteFile(
		filepath.Join(worktreeRepo, ".git"),
		[]byte("gitdir: /some/main/.git/worktrees/wt"),
		0644,
	)

	plainDir := filepath.Join(parent, "plain")
	os.Mkdir(plainDir, 0755)

	hiddenDir := filepath.Join(parent, ".hidden")
	os.Mkdir(hiddenDir, 0755)
	os.Mkdir(filepath.Join(hiddenDir, ".git"), 0755)

	repos := findChildGitRepos(parent)

	assert.Len(t, repos, 2, "unexpected condition")

	found := make(map[string]bool)
	for _, r := range repos {
		found[r] = true
	}
	assert.True(t, found["regular"], "unexpected condition")
	assert.True(t, found["worktree"], "unexpected condition")
	assert.False(t, found["plain"], "unexpected condition")
	assert.False(t, found[".hidden"], "unexpected condition")
}

func TestFindChildGitReposHintPaths(t *testing.T) {
	parent := t.TempDir()

	repoDir := filepath.Join(parent, "my-repo")
	os.Mkdir(repoDir, 0755)
	os.Mkdir(filepath.Join(repoDir, ".git"), 0755)

	_, _, err := executeReviewCmd("--repo", parent)
	require.Error(t, err, "Expected error for non-git directory")

	errMsg := err.Error()
	expectedPath := filepath.Join(parent, "my-repo")
	assert.Contains(t, errMsg, expectedPath, "Hint should contain full path %q, got: %s", expectedPath, errMsg)
}
