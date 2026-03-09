package main

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := <-reqCh
	if req.GitRef != "HEAD" {
		t.Errorf("expected HEAD, got %q", req.GitRef)
	}
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := <-reqCh
	want := mainSHA + "..HEAD"
	if req.GitRef != want {
		t.Errorf("expected git_ref %q, got %q", want, req.GitRef)
	}
}

func TestPostCommitFallsBackOnBaseBranch(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.Run("symbolic-ref", "HEAD", "refs/heads/main")
	repo.CommitFile("file.txt", "content", "initial")
	writeRoborevConfig(t, repo, `post_commit_review = "branch"`)

	_, _, err := executePostCommitCmd("--repo", repo.Dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := <-reqCh
	if req.GitRef != "HEAD" {
		t.Errorf("expected HEAD fallback on base branch, got %q", req.GitRef)
	}
}

func TestPostCommitSilentExitNotARepo(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := executePostCommitCmd("--repo", dir)
	if err != nil {
		t.Errorf("expected silent exit, got error: %v", err)
	}
	if stdout != "" {
		t.Errorf("expected no stdout, got: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("expected no stderr, got: %q", stderr)
	}
}

func TestPostCommitAcceptsQuietFlag(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	mockEnqueue(t, mux)

	repo.CommitFile("file.txt", "content", "initial")

	_, _, err := executePostCommitCmd(
		"--repo", repo.Dir, "--quiet",
	)
	if err != nil {
		t.Errorf("--quiet should be accepted: %v", err)
	}
}

func TestEnqueueAliasWorksIdentically(t *testing.T) {
	repo, mux := setupTestEnvironment(t)
	reqCh := mockEnqueue(t, mux)

	repo.CommitFile("file.txt", "content", "initial")

	_, _, err := executeEnqueueAliasCmd("--repo", repo.Dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := <-reqCh
	if req.GitRef != "HEAD" {
		t.Errorf("expected HEAD, got %q", req.GitRef)
	}
}

func TestPostCommitRejectsPositionalArgs(t *testing.T) {
	_, _, err := executePostCommitCmd("abc123")
	if err == nil {
		t.Fatal("expected error for positional args")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("expected 'unknown command' error, got: %v", err)
	}
}

func TestEnqueueRejectsPositionalArgs(t *testing.T) {
	_, _, err := executeEnqueueAliasCmd("abc123")
	if err == nil {
		t.Fatal("expected error for positional args")
	}
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
	mux.HandleFunc("/api/enqueue", func(
		w http.ResponseWriter, r *http.Request,
	) {
		t.Error("real handler should not be reached")
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

	if err != nil {
		t.Errorf("expected nil (fail open), got: %v", err)
	}

	select {
	case <-rt.hit:
		// RoundTrip was called — timeout path was exercised
	default:
		t.Fatal("RoundTrip was never called; timeout not exercised")
	}

	if elapsed > 3*time.Second {
		t.Errorf(
			"command took %v; should return promptly via timeout",
			elapsed,
		)
	}
}

func TestEnqueueAliasIsHidden(t *testing.T) {
	cmd := enqueueCmd()
	if !cmd.Hidden {
		t.Error("enqueue alias should be hidden")
	}
	if !strings.Contains(cmd.Use, "enqueue") {
		t.Errorf("expected Use to contain 'enqueue', got %q", cmd.Use)
	}
}
