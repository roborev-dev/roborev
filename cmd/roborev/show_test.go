package main

// Tests for the show command

import (
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestShowCommandSHADisambiguation(t *testing.T) {
	t.Run("numeric ref resolvable in repo treated as SHA not job ID", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")
		repo.Run("tag", "12345")
		tagSHA := repo.Run("rev-parse", "12345")

		getQuery := mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
		})

		chdir(t, repo.Dir)
		_ = runShowCmd(t, "12345")

		q := getQuery()
		if !strings.Contains(q, "sha=") {
			t.Errorf("expected sha= in query, got: %s", q)
		}
		if strings.Contains(q, "job_id=") {
			t.Errorf("numeric ref '12345' should resolve as SHA, not job_id; got: %s", q)
		}
		if !strings.Contains(q, tagSHA[:7]) {
			t.Errorf("expected query to contain resolved SHA %s, got: %s", tagSHA[:7], q)
		}
	})

	t.Run("numeric non-resolvable treated as job ID", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		getQuery := mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 99999, Output: "LGTM", Agent: "test",
		})

		chdir(t, repo.Dir)
		_ = runShowCmd(t, "99999")

		q := getQuery()
		if !strings.Contains(q, "job_id=99999") {
			t.Errorf("expected job_id=99999 in query, got: %s", q)
		}
		if strings.Contains(q, "sha=") {
			t.Errorf("did not expect sha= in query, got: %s", q)
		}
	})

	t.Run("non-numeric argument treated as SHA", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		getQuery := mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
		})

		chdir(t, repo.Dir)
		_ = runShowCmd(t, "abc123def")

		q := getQuery()
		if !strings.Contains(q, "sha=abc123def") {
			t.Errorf("expected sha=abc123def in query, got: %s", q)
		}
		if strings.Contains(q, "job_id=") {
			t.Errorf("did not expect job_id= in query, got: %s", q)
		}
	})
}

func TestShowJobFlag(t *testing.T) {
	t.Run("--job forces job ID interpretation even when ref is resolvable", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")
		repo.Run("tag", "12345")

		getQuery := mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 12345, Output: "LGTM", Agent: "test",
		})

		chdir(t, repo.Dir)
		_ = runShowCmd(t, "--job", "12345")

		q := getQuery()
		if !strings.Contains(q, "job_id=12345") {
			t.Errorf("expected job_id=12345 in query, got: %s", q)
		}
		if strings.Contains(q, "sha=") {
			t.Errorf("did not expect sha= in query, got: %s", q)
		}
	})
}

func TestShowOutputFormat(t *testing.T) {
	t.Run("job ID shows 'Review for job X (by agent)'", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 42, Output: "Test review output", Agent: "codex",
		})

		chdir(t, repo.Dir)
		output := runShowCmd(t, "--job", "42")

		if !strings.Contains(output, "Review for job 42 (by codex)") {
			t.Errorf("expected 'Review for job 42 (by codex)' in output, got: %s", output)
		}
		if strings.Contains(output, "(job 42, by") {
			t.Errorf("did not expect redundant job ID in output, got: %s", output)
		}
	})

	t.Run("SHA shows 'Review for abc123 (job X, by agent)'", func(t *testing.T) {
		repo := newTestGitRepo(t)
		commitSHA := repo.CommitFile("file.txt", "content", "initial commit")
		shortSHA := commitSHA[:7]

		mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 42, Output: "Test review output", Agent: "codex",
		})

		chdir(t, repo.Dir)
		output := runShowCmd(t, commitSHA)

		expectedPattern := "Review for " + shortSHA + " (job 42, by codex)"
		if !strings.Contains(output, expectedPattern) {
			t.Errorf("expected '%s' in output, got: %s", expectedPattern, output)
		}
	})
}
