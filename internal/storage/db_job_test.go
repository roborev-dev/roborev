package storage

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type jobEnv struct {
	db     *DB
	repo   *Repo
	commit *Commit
	job    *ReviewJob
}

func setupJobEnv(
	t *testing.T, repoPath, gitRef string,
) jobEnv {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	repo, commit, job := createJobChain(t, db, repoPath, gitRef)
	return jobEnv{
		db:     db,
		repo:   repo,
		commit: commit,
		job:    job,
	}
}

func TestJobLifecycle(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "abc123")

	assert.Equal(t, JobStatusQueued, env.job.Status)

	// Claim job
	claimed := claimJob(t, env.db, "worker-1")
	assert.Equal(t, claimed.ID, env.job.ID)
	assert.Equal(t, JobStatusRunning, claimed.Status)

	// Claim again should return nil (no more jobs)
	claimed2, err := env.db.ClaimJob("worker-2")
	require.NoError(t, err, "ClaimJob (second) failed")
	assert.Nil(t, claimed2)

	// Complete job
	require.NoError(t, env.db.CompleteJob(
		env.job.ID, "codex", "test prompt", "test output",
	), "CompleteJob failed")

	// Verify job status
	updatedJob, err := env.db.GetJobByID(env.job.ID)
	require.NoError(t, err, "GetJobByID failed")

	assert.Equal(t, JobStatusDone, updatedJob.Status)
}

func TestJobFailure(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "def456")
	claimJob(t, env.db, "worker-1")

	// Fail the job
	_, err := env.db.FailJob(env.job.ID, "", "test error message")
	require.NoError(t, err, "FailJob failed")

	updatedJob, err := env.db.GetJobByID(env.job.ID)
	require.NoError(t, err, "GetJobByID failed")

	assert.Equal(t, JobStatusFailed, updatedJob.Status)
	assert.Equal(t, "test error message", updatedJob.Error)
}

func TestFailJobOwnerScoped(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "fail-owner")
	claimJob(t, env.db, "worker-1")

	// Wrong worker should not be able to fail the job
	updated, err := env.db.FailJob(env.job.ID, "worker-2", "stale fail")
	require.NoError(t, err, "FailJob with wrong worker failed")

	assert.False(t, updated)

	// Job should still be running
	j, err := env.db.GetJobByID(env.job.ID)
	require.NoError(t, err, "GetJobByID failed")

	assert.Equal(t, JobStatusRunning, j.Status)

	// Correct worker should succeed
	updated, err = env.db.FailJob(env.job.ID, "worker-1", "legit fail")
	require.NoError(t, err, "FailJob with correct worker failed")

	assert.True(t, updated)

	j, err = env.db.GetJobByID(env.job.ID)
	require.NoError(t, err, "GetJobByID failed")

	assert.Equal(t, JobStatusFailed, j.Status)
	assert.Equal(t, "legit fail", j.Error)
}

func TestRetryJobOwnerScoped(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "retry-owner")
	claimJob(t, env.db, "worker-1")

	// Wrong worker should not be able to retry the job
	retried, err := env.db.RetryJob(env.job.ID, "worker-2", 3)
	require.NoError(t, err, "RetryJob with wrong worker failed")

	assert.False(t, retried)

	// Job should still be running (not requeued)
	j, err := env.db.GetJobByID(env.job.ID)
	require.NoError(t, err, "GetJobByID failed")

	assert.Equal(t, JobStatusRunning, j.Status)

	// Correct worker should succeed
	retried, err = env.db.RetryJob(env.job.ID, "worker-1", 3)
	require.NoError(t, err, "RetryJob with correct worker failed")

	assert.True(t, retried)

	j, err = env.db.GetJobByID(env.job.ID)
	require.NoError(t, err, "GetJobByID failed")

	assert.Equal(t, JobStatusQueued, j.Status)
}

func TestReviewOperations(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "rev123")
	claimJob(t, env.db, "worker-1")
	require.NoError(t, env.db.CompleteJob(
		env.job.ID, "codex", "the prompt", "the review output",
	), "CompleteJob failed")

	// Get review by commit SHA
	review, err := env.db.GetReviewByCommitSHA("rev123")
	require.NoError(t, err, "GetReviewByCommitSHA failed")

	assert.Equal(t, "the review output", review.Output)
	assert.Equal(t, "codex", review.Agent)
}

func TestReviewVerdictComputation(t *testing.T) {
	t.Run("verdict populated when output exists and no error", func(t *testing.T) {
		env := setupJobEnv(t, "/tmp/test-repo", "verdict-pass")
		_, err := env.db.ClaimJob("worker-1")
		require.NoError(t, err)
		require.NoError(t, env.db.CompleteJob(
			env.job.ID, "codex", "the prompt",
			"No issues found. The code looks good.",
		))

		review, err := env.db.GetReviewByJobID(env.job.ID)
		require.NoError(t, err, "GetReviewByJobID failed")

		assert.NotNil(t, review.Job.Verdict)
		assert.Equal(t, "P", *review.Job.Verdict)
	})

	t.Run("verdict nil when output is empty", func(t *testing.T) {
		env := setupJobEnv(t, "/tmp/test-repo", "verdict-empty")
		_, err := env.db.ClaimJob("worker-1")
		require.NoError(t, err)
		require.NoError(t, env.db.CompleteJob(
			env.job.ID, "codex", "the prompt", "",
		)) // empty output

		review, err := env.db.GetReviewByJobID(env.job.ID)
		require.NoError(t, err, "GetReviewByJobID failed")

		assert.Nil(t, review.Job.Verdict)
	})

	t.Run("verdict nil when job has error", func(t *testing.T) {
		env := setupJobEnv(t, "/tmp/test-repo", "verdict-error")
		_, err := env.db.ClaimJob("worker-1")
		require.NoError(t, err)
		_, err = env.db.FailJob(
			env.job.ID, "", "API rate limit exceeded",
		)
		require.NoError(t, err)

		// Manually insert a review to simulate edge case
		_, err = env.db.Exec(
			`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, 'codex', 'prompt', 'No issues found.')`,
			env.job.ID,
		)
		require.NoError(t, err, "Failed to insert review")

		review, err := env.db.GetReviewByJobID(env.job.ID)
		require.NoError(t, err, "GetReviewByJobID failed")

		assert.Nil(t, review.Job.Verdict)
	})

	t.Run("GetReviewByCommitSHA also respects verdict guard", func(t *testing.T) {
		env := setupJobEnv(t, "/tmp/test-repo", "verdict-sha")
		_, err := env.db.ClaimJob("worker-1")
		require.NoError(t, err)
		require.NoError(t, env.db.CompleteJob(
			env.job.ID, "codex", "the prompt", "No issues found.",
		))

		review, err := env.db.GetReviewByCommitSHA("verdict-sha")
		require.NoError(t, err, "GetReviewByCommitSHA failed")

		assert.NotNil(t, review.Job.Verdict)
		assert.Equal(t, "P", *review.Job.Verdict)
	})
}

func TestResponseOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "resp123", "Author", "Subject", time.Now())

	// Add comment
	resp, err := db.AddComment(commit.ID, "test-user", "LGTM!")
	require.NoError(t, err, "AddComment failed: %v")

	assert.Equal(t, "LGTM!", resp.Response)

	// Get comments
	comments, err := db.GetCommentsForCommit(commit.ID)
	require.NoError(t, err, "GetCommentsForCommit failed: %v")

	assert.Len(t, comments, 1)
}

func TestMarkReviewClosed(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "addr123")
	_, err := env.db.ClaimJob("worker-1")
	require.NoError(t, err)
	require.NoError(t, env.db.CompleteJob(
		env.job.ID, "codex", "prompt", "output",
	))

	// Get the review
	review, err := env.db.GetReviewByJobID(env.job.ID)
	require.NoError(t, err, "GetReviewByJobID failed")

	// Initially not closed
	assert.False(t, review.Closed)

	// Mark as closed
	err = env.db.MarkReviewClosed(review.ID, true)
	require.NoError(t, err, "MarkReviewClosed failed")

	// Verify it's closed
	updated, err := env.db.GetReviewByID(review.ID)
	require.NoError(t, err)
	assert.True(t, updated.Closed, "Review should be closed after MarkReviewClosed(true)")

	// Mark as open
	err = env.db.MarkReviewClosed(review.ID, false)
	require.NoError(t, err, "MarkReviewClosed(false) failed")

	updated2, err := env.db.GetReviewByID(review.ID)
	require.NoError(t, err)
	assert.False(t, updated2.Closed, "Review should not be closed after MarkReviewClosed(false)")
}

func TestMarkReviewClosedNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Try to mark a non-existent review
	err := db.MarkReviewClosed(999999, true)
	require.Error(t, err)

	// Should be sql.ErrNoRows
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMarkReviewClosedByJobID(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "jobaddr123")
	_, err := env.db.ClaimJob("worker-1")
	require.NoError(t, err)
	require.NoError(t, env.db.CompleteJob(
		env.job.ID, "codex", "prompt", "output",
	))

	// Get the review to verify initial state
	review, err := env.db.GetReviewByJobID(env.job.ID)
	require.NoError(t, err, "GetReviewByJobID failed")

	// Initially not closed
	assert.False(t, review.Closed)

	// Mark as closed using job ID
	err = env.db.MarkReviewClosedByJobID(env.job.ID, true)
	require.NoError(t, err, "MarkReviewClosedByJobID failed")

	// Verify it's closed
	updated, err := env.db.GetReviewByJobID(env.job.ID)
	require.NoError(t, err)
	assert.True(t, updated.Closed, "Review should be closed after MarkReviewClosedByJobID(true)")

	// Mark as open using job ID
	err = env.db.MarkReviewClosedByJobID(env.job.ID, false)
	require.NoError(t, err, "MarkReviewClosedByJobID(false) failed")

	updated2, err := env.db.GetReviewByJobID(env.job.ID)
	require.NoError(t, err)
	assert.False(t, updated2.Closed, "Review should not be closed after MarkReviewClosedByJobID(false)")
}

func TestMarkReviewClosedByJobIDNotFound(t *testing.T) {
	env := setupJobEnv(t, "/tmp/test-repo", "jobaddr-missing")

	// Try to mark a non-existent job
	err := env.db.MarkReviewClosedByJobID(999999, true)
	require.Error(t, err)

	// Should be sql.ErrNoRows
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestRetryJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry123")

	// Claim the job (makes it running)
	claimJob(t, db, "worker-1")

	// Retry should succeed (retry_count: 0 -> 1)
	retried, err := db.RetryJob(job.ID, "", 3)
	require.NoError(t, err, "RetryJob failed: %v")

	assert.True(t, retried)

	// Verify job is queued with retry_count=1
	updatedJob, _ := db.GetJobByID(job.ID)
	assert.Equal(t, JobStatusQueued, updatedJob.Status)
	count, _ := db.GetJobRetryCount(job.ID)
	assert.Equal(t, 1, count)

	// Claim again and retry twice more (retry_count: 1->2, 2->3)
	_, _ = db.ClaimJob("worker-1")
	db.RetryJob(job.ID, "", 3) // retry_count becomes 2
	_, _ = db.ClaimJob("worker-1")
	db.RetryJob(job.ID, "", 3) // retry_count becomes 3

	count, _ = db.GetJobRetryCount(job.ID)
	assert.Equal(t, 3, count)

	// Claim again - next retry should fail (at max)
	_, _ = db.ClaimJob("worker-1")
	retried, err = db.RetryJob(job.ID, "", 3)
	require.NoError(t, err, "RetryJob at max failed: %v")

	assert.False(t, retried)

	// Job should still be running (retry didn't happen)
	updatedJob, _ = db.GetJobByID(job.ID)
	assert.Equal(t, JobStatusRunning, updatedJob.Status)
}

func TestRetryJobOnlyWorksForRunning(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry-status")

	// Try to retry a queued job (should fail - not running)
	retried, err := db.RetryJob(job.ID, "", 3)
	require.NoError(t, err, "RetryJob on queued job failed: %v")

	assert.False(t, retried)

	// Claim, complete, then try retry (should fail - job is done)
	_, _ = db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "p", "o")

	retried, err = db.RetryJob(job.ID, "", 3)
	require.NoError(t, err, "RetryJob on done job failed: %v")

	assert.False(t, retried)
}

func TestRetryJobAtomic(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry-atomic")
	claimJob(t, db, "worker-1")

	// Simulate two concurrent retries - only first should succeed
	// (In practice this tests the atomic update)
	retried1, _ := db.RetryJob(job.ID, "", 3)
	retried2, _ := db.RetryJob(job.ID, "", 3) // Job is now queued, not running

	assert.True(t, retried1)
	assert.False(t, retried2, "Second retry should fail (job is no longer running)")

	// Verify retry_count is 1, not 2
	count, _ := db.GetJobRetryCount(job.ID)
	assert.Equal(t, 1, count)
}

func TestFailoverJob(t *testing.T) {
	t.Run("succeeds with backup agent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo := createRepo(t, db, "/tmp/failover-repo")
		commit := createCommit(t, db, repo.ID, "fo-abc123")

		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fo-abc123",
			Agent:    "primary",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		// Claim to make it running
		claimJob(t, db, "worker-1")

		// Failover should succeed
		ok, err := db.FailoverJob(job.ID, "worker-1", "backup", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.True(t, ok)

		// Verify: agent swapped, retry_count reset, status queued
		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "backup", updated.Agent)
		assert.Equal(t, JobStatusQueued, updated.Status)
		count, _ := db.GetJobRetryCount(job.ID)
		assert.Equal(t, 0, count)
	})

	t.Run("clears model on failover", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo := createRepo(t, db, "/tmp/failover-model")
		commit := createCommit(t, db, repo.ID, "fo-model")

		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fo-model",
			Agent:    "primary",
			Model:    "o3-mini",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		assert.Equal(t, "o3-mini", job.Model)

		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "backup", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.True(t, ok)

		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Empty(t, updated.Model)
	})

	t.Run("sets backup model on failover", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo := createRepo(t, db, "/tmp/failover-bmodel")
		commit := createCommit(t, db, repo.ID, "fo-bmodel")

		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fo-bmodel",
			Agent:    "primary",
			Model:    "o3-mini",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "backup", "claude-sonnet")
		require.NoError(t, err, "FailoverJob: %v")

		assert.True(t, ok)

		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "claude-sonnet", updated.Model)
		assert.Equal(t, "backup", updated.Agent)
	})

	t.Run("fails with empty backup agent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		_, _, job := createJobChain(t, db, "/tmp/failover-nobackup", "fo-no-backup")
		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.False(t, ok)
	})

	t.Run("fails when backup equals agent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo := createRepo(t, db, "/tmp/failover-same")
		commit := createCommit(t, db, repo.ID, "fo-same123")

		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fo-same123",
			Agent:    "codex",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "codex", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.False(t, ok, "Expected failover to return false when backup == agent")
	})

	t.Run("fails when not running", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo := createRepo(t, db, "/tmp/failover-queued")
		commit := createCommit(t, db, repo.ID, "fo-queued")

		// Job is queued (not claimed/running)
		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fo-queued",
			Agent:    "primary",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		ok, err := db.FailoverJob(job.ID, "worker-1", "backup", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.False(t, ok)
	})

	t.Run("second failover with same backup is no-op", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo := createRepo(t, db, "/tmp/failover-double")
		commit := createCommit(t, db, repo.ID, "fo-double")

		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fo-double",
			Agent:    "primary",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		claimJob(t, db, "worker-1")

		// First failover: primary -> backup
		db.FailoverJob(job.ID, "worker-1", "backup", "")

		// Reclaim, now agent is "backup"
		claimJob(t, db, "worker-1")

		// Second failover with same backup agent should fail (agent == backup)
		ok, err := db.FailoverJob(job.ID, "worker-1", "backup", "")
		require.NoError(t, err, "FailoverJob second attempt: %v")

		assert.False(t, ok, "Expected second failover to return false (agent already is backup)")
	})

	t.Run("fails when wrong worker", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo := createRepo(t, db, "/tmp/failover-wrongworker")
		commit := createCommit(t, db, repo.ID, "fo-wrongw")

		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fo-wrongw",
			Agent:    "primary",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		claimJob(t, db, "worker-1")

		// A different worker should not be able to failover this job
		ok, err := db.FailoverJob(job.ID, "worker-2", "backup", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.False(t, ok)

		// Verify original agent is unchanged
		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "primary", updated.Agent)
	})
}

func TestCancelJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("cancel queued job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-queued")

		err := db.CancelJob(job.ID)
		require.NoError(t, err, "CancelJob failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusCanceled, updated.Status)
	})

	t.Run("cancel running job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-running")
		db.ClaimJob("worker-1")

		err := db.CancelJob(job.ID)
		require.NoError(t, err, "CancelJob failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusCanceled, updated.Status)
	})

	t.Run("cancel done job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-done")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.CancelJob(job.ID)
		require.Error(t, err)
	})

	t.Run("cancel failed job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-failed")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "some error")

		err := db.CancelJob(job.ID)
		require.Error(t, err)
	})

	t.Run("complete respects canceled status", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "complete-canceled")
		db.ClaimJob("worker-1")
		db.CancelJob(job.ID)

		// CompleteJob should not overwrite canceled status
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusCanceled, updated.Status)

		// Verify no review was inserted (should get sql.ErrNoRows)
		_, err := db.GetReviewByJobID(job.ID)
		require.Error(t, err)
		require.ErrorIs(t, err, sql.ErrNoRows, "expected no rows error, got: %v", err)
	})

	t.Run("fail respects canceled status", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "fail-canceled")
		db.ClaimJob("worker-1")
		db.CancelJob(job.ID)

		// FailJob should not overwrite canceled status
		db.FailJob(job.ID, "", "some error")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusCanceled, updated.Status)
	})

	t.Run("canceled jobs counted correctly", func(t *testing.T) {
		// Create and cancel a new job
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-count")
		db.CancelJob(job.ID)

		_, _, _, _, canceled, _, _, err := db.GetJobCounts()
		require.NoError(t, err, "GetJobCounts failed: %v")

		assert.GreaterOrEqual(t, canceled, 1)
	})
}

func TestMarkJobApplied(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "applied-test", "A", "S", time.Now())

	t.Run("mark done fix job as applied", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.MarkJobApplied(job.ID)
		require.NoError(t, err, "MarkJobApplied failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusApplied, updated.Status)
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-q", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobApplied(job.ID)
		require.Error(t, err)
	})

	t.Run("mark applied job again fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-2", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")
		db.MarkJobApplied(job.ID)

		err := db.MarkJobApplied(job.ID)
		require.Error(t, err)
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-review", Agent: "codex"})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.MarkJobApplied(job.ID)
		require.Error(t, err)
	})
}

func TestMarkJobRebased(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "rebased-test", "A", "S", time.Now())

	t.Run("mark done fix job as rebased", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-test", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.MarkJobRebased(job.ID)
		require.NoError(t, err, "MarkJobRebased failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusRebased, updated.Status)
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-test-q", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobRebased(job.ID)
		require.Error(t, err)
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-review", Agent: "codex"})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.MarkJobRebased(job.ID)
		require.Error(t, err)
	})
}

func TestReenqueueJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("rerun failed job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-failed")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "some error")

		err := db.ReenqueueJob(job.ID)
		require.NoError(t, err, "ReenqueueJob failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusQueued, updated.Status)
		assert.Empty(t, updated.Error)
		assert.Nil(t, updated.StartedAt)
		assert.Nil(t, updated.FinishedAt)
	})

	t.Run("rerun canceled job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-canceled")
		db.CancelJob(job.ID)

		err := db.ReenqueueJob(job.ID)
		require.NoError(t, err, "ReenqueueJob failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusQueued, updated.Status)
	})

	t.Run("rerun done job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-done")
		// ClaimJob returns the claimed job; keep claiming until we get ours
		var claimed *ReviewJob
		for {
			claimed, _ = db.ClaimJob("worker-1")
			assert.NotNil(t, claimed)
			if claimed.ID == job.ID {
				break
			}
			// Complete other jobs to clear them
			db.CompleteJob(claimed.ID, "codex", "prompt", "output")
		}
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.ReenqueueJob(job.ID)
		require.NoError(t, err, "ReenqueueJob failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusQueued, updated.Status)
	})

	t.Run("rerun queued job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-queued")

		err := db.ReenqueueJob(job.ID)
		require.Error(t, err)
	})

	t.Run("rerun running job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-running")
		db.ClaimJob("worker-1")

		err := db.ReenqueueJob(job.ID)
		require.Error(t, err)
	})

	t.Run("rerun nonexistent job fails", func(t *testing.T) {
		err := db.ReenqueueJob(99999)
		require.Error(t, err)
	})

	t.Run("rerun done job and complete again", func(t *testing.T) {
		// Use isolated database to avoid interference from other subtests
		isolatedDB := openTestDB(t)
		defer isolatedDB.Close()

		_, _, job := createJobChain(t, isolatedDB, "/tmp/isolated-repo", "rerun-complete-cycle")

		// First completion cycle
		claimed, _ := isolatedDB.ClaimJob("worker-1")
		assert.False(t, claimed == nil || claimed.ID != job.ID)
		err := isolatedDB.CompleteJob(job.ID, "codex", "first prompt", "first output")
		require.NoError(t, err, "First CompleteJob failed: %v")

		// Verify first review exists
		review1, err := isolatedDB.GetReviewByJobID(job.ID)
		require.NoError(t, err, "GetReviewByJobID failed after first complete: %v")

		assert.Equal(t, "first output", review1.Output)

		// Re-enqueue the done job
		err = isolatedDB.ReenqueueJob(job.ID)
		require.NoError(t, err, "ReenqueueJob failed: %v")

		// Verify review was deleted
		_, err = isolatedDB.GetReviewByJobID(job.ID)
		require.Error(t, err, "Expected GetReviewByJobID to fail after re-enqueue (review should be deleted)")

		// Second completion cycle
		claimed, _ = isolatedDB.ClaimJob("worker-1")
		assert.False(t, claimed == nil || claimed.ID != job.ID)
		err = isolatedDB.CompleteJob(job.ID, "codex", "second prompt", "second output")
		require.NoError(t, err, "Second CompleteJob failed: %v")

		// Verify second review exists with new content
		review2, err := isolatedDB.GetReviewByJobID(job.ID)
		require.NoError(t, err, "GetReviewByJobID failed after second complete: %v")

		assert.Equal(t, "second output", review2.Output)
	})
}

func TestEnqueueJobWithPatchID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-patch-id")
	commit := createCommit(t, db, repo.ID, "abc123")

	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "abc123",
		Agent:    "test",
		PatchID:  "deadbeef1234",
	})
	require.NoError(t, err, "EnqueueJob: %v")

	assert.Equal(t, "deadbeef1234", job.PatchID)

	// Verify it round-trips through GetJobByID
	got, err := db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID: %v")

	assert.Equal(t, "deadbeef1234", got.PatchID)
}

func TestRemapJobGitRef(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-remap")
	commit := createCommit(t, db, repo.ID, "oldsha")

	t.Run("remap updates matching jobs", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "oldsha",
			Agent:    "test",
			PatchID:  "patchabc",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		newCommit := createCommit(t, db, repo.ID, "newsha")
		n, err := db.RemapJobGitRef(repo.ID, "oldsha", "newsha", "patchabc", newCommit.ID)
		require.NoError(t, err, "RemapJobGitRef: %v")

		assert.Equal(t, 1, n)

		got, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "newsha", got.GitRef)
	})

	t.Run("skips on patch_id mismatch", func(t *testing.T) {
		commit2 := createCommit(t, db, repo.ID, "sha2")
		_, err := db.EnqueueJob(EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit2.ID,
			GitRef:   "sha2",
			Agent:    "test",
			PatchID:  "patch_original",
		})
		require.NoError(t, err, "EnqueueJob: %v")

		newCommit := createCommit(t, db, repo.ID, "sha2_new")
		n, err := db.RemapJobGitRef(repo.ID, "sha2", "sha2_new", "patch_different", newCommit.ID)
		require.NoError(t, err, "RemapJobGitRef: %v")

		assert.Equal(t, 0, n)
	})

	t.Run("returns 0 for no matches", func(t *testing.T) {
		newCommit := createCommit(t, db, repo.ID, "nonexistent_new")
		n, err := db.RemapJobGitRef(repo.ID, "nonexistent", "nonexistent_new", "patch", newCommit.ID)
		require.NoError(t, err, "RemapJobGitRef: %v")

		assert.Equal(t, 0, n)
	})
}

func TestJobTypeBackfill(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/backfill-test")

	// Insert jobs with job_type='review' to simulate pre-migration state
	// 1. Normal commit review - should stay 'review'
	commit := createCommit(t, db, repo.ID, "abc123")
	_, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, status, job_type) VALUES (?, ?, 'abc123', 'codex', 'done', 'review')`,
		repo.ID, commit.ID)
	require.NoError(t, err, "insert review job: %v")

	// 2. Dirty job (git_ref='dirty') - should become 'dirty'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'dirty', 'codex', 'done', 'review')`, repo.ID)
	require.NoError(t, err, "insert dirty job: %v")

	// 3. Dirty job (diff_content set) - should become 'dirty'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type, diff_content) VALUES (?, 'some-ref', 'codex', 'done', 'review', 'diff here')`, repo.ID)
	require.NoError(t, err, "insert dirty-with-diff job: %v")

	// 4. Range job (git_ref has ..) - should become 'range'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'abc..def', 'codex', 'done', 'review')`, repo.ID)
	require.NoError(t, err, "insert range job: %v")

	// 5. Task job (no commit_id, no diff, non-dirty git_ref) - should become 'task'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'analyze', 'codex', 'done', 'review')`, repo.ID)
	require.NoError(t, err, "insert task job: %v")

	// Run backfill SQL (same as migration)
	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'dirty' WHERE (git_ref = 'dirty' OR diff_content IS NOT NULL) AND job_type = 'review'`)
	require.NoError(t, err, "backfill dirty: %v")

	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'range' WHERE git_ref LIKE '%..%' AND commit_id IS NULL AND job_type = 'review'`)
	require.NoError(t, err, "backfill range: %v")

	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'task' WHERE commit_id IS NULL AND diff_content IS NULL AND git_ref != 'dirty' AND git_ref NOT LIKE '%..%' AND git_ref != '' AND job_type = 'review'`)
	require.NoError(t, err, "backfill task: %v")

	// Verify results
	rows, err := db.Query(`SELECT git_ref, job_type FROM review_jobs ORDER BY id`)
	require.NoError(t, err, "query jobs: %v")

	defer rows.Close()

	expected := []struct {
		gitRef  string
		jobType string
	}{
		{"abc123", "review"},
		{"dirty", "dirty"},
		{"some-ref", "dirty"},
		{"abc..def", "range"},
		{"analyze", "task"},
	}

	i := 0
	for rows.Next() {
		var gitRef, jobType string
		if err := rows.Scan(&gitRef, &jobType); err != nil {
			require.NoError(t, err, "scan row: %v")
		}
		assert.Less(t, i, len(expected), "more rows than expected")
		assert.False(t, gitRef != expected[i].gitRef || jobType != expected[i].jobType)
		i++
	}
	assert.Equal(t, len(expected), i)
}

func TestSaveJobSessionID_StaleWorkerIgnored(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "session-race")

	claimJob(t, db, "worker-A")

	err := db.SaveJobSessionID(job.ID, "worker-A", "session-A")
	require.NoError(t, err, "SaveJobSessionID (worker-A): %v", err)

	j, err := db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID after worker-A save: %v", err)
	assert.Equal(t, "session-A", j.SessionID)

	err = db.CancelJob(job.ID)
	require.NoError(t, err, "CancelJob: %v", err)

	err = db.ReenqueueJob(job.ID)
	require.NoError(t, err, "ReenqueueJob: %v", err)

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID after reenqueue: %v", err)
	assert.Empty(t, j.SessionID)

	claimJob(t, db, "worker-B")

	err = db.SaveJobSessionID(job.ID, "worker-A", "stale-session")
	require.NoError(t, err, "SaveJobSessionID (stale worker-A): %v", err)

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID after stale worker save: %v", err)
	assert.Empty(t, j.SessionID)

	err = db.SaveJobSessionID(job.ID, "worker-B", "session-B")
	require.NoError(t, err, "SaveJobSessionID (worker-B): %v", err)

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID after worker-B save: %v", err)
	assert.Equal(t, "session-B", j.SessionID)

	err = db.SaveJobSessionID(job.ID, "worker-B", "session-B2")
	require.NoError(t, err, "SaveJobSessionID (worker-B second): %v", err)

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID after worker-B second save: %v", err)
	assert.Equal(t, "session-B", j.SessionID)
}
