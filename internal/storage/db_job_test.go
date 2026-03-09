package storage

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestJobLifecycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "abc123")

	assert.Equal(t, JobStatusQueued, job.Status, "unexpected condition")

	// Claim job
	claimed := claimJob(t, db, "worker-1")
	assert.Equal(t, claimed.ID, job.ID, "unexpected condition")
	assert.Equal(t, JobStatusRunning, claimed.Status, "unexpected condition")

	// Claim again should return nil (no more jobs)
	claimed2, err := db.ClaimJob("worker-2")
	require.NoError(t, err, "ClaimJob (second) failed: %v")

	assert.Nil(t, claimed2, "unexpected condition")

	// Complete job
	err = db.CompleteJob(job.ID, "codex", "test prompt", "test output")
	require.NoError(t, err, "CompleteJob failed: %v")

	// Verify job status
	updatedJob, err := db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID failed: %v")

	assert.Equal(t, JobStatusDone, updatedJob.Status, "unexpected condition")
}

func TestJobFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "def456")
	claimJob(t, db, "worker-1")

	// Fail the job
	_, err := db.FailJob(job.ID, "", "test error message")
	require.NoError(t, err, "FailJob failed: %v")

	updatedJob, err := db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID failed: %v")

	assert.Equal(t, JobStatusFailed, updatedJob.Status, "unexpected condition")
	assert.Equal(t, "test error message", updatedJob.Error, "unexpected condition")
}

func TestFailJobOwnerScoped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "fail-owner")
	claimJob(t, db, "worker-1")

	// Wrong worker should not be able to fail the job
	updated, err := db.FailJob(job.ID, "worker-2", "stale fail")
	require.NoError(t, err, "FailJob with wrong worker failed: %v")

	assert.False(t, updated, "unexpected condition")

	// Job should still be running
	j, err := db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID failed: %v")

	assert.Equal(t, JobStatusRunning, j.Status, "unexpected condition")

	// Correct worker should succeed
	updated, err = db.FailJob(job.ID, "worker-1", "legit fail")
	require.NoError(t, err, "FailJob with correct worker failed: %v")

	assert.True(t, updated, "unexpected condition")

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID failed: %v")

	assert.Equal(t, JobStatusFailed, j.Status, "unexpected condition")
	assert.Equal(t, "legit fail", j.Error, "unexpected condition")
}

func TestRetryJobOwnerScoped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry-owner")
	claimJob(t, db, "worker-1")

	// Wrong worker should not be able to retry the job
	retried, err := db.RetryJob(job.ID, "worker-2", 3)
	require.NoError(t, err, "RetryJob with wrong worker failed: %v")

	assert.False(t, retried, "unexpected condition")

	// Job should still be running (not requeued)
	j, err := db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID failed: %v")

	assert.Equal(t, JobStatusRunning, j.Status, "unexpected condition")

	// Correct worker should succeed
	retried, err = db.RetryJob(job.ID, "worker-1", 3)
	require.NoError(t, err, "RetryJob with correct worker failed: %v")

	assert.True(t, retried, "unexpected condition")

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID failed: %v")

	assert.Equal(t, JobStatusQueued, j.Status, "unexpected condition")
}

func TestReviewOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "rev123")
	claimJob(t, db, "worker-1")
	if err := db.CompleteJob(job.ID, "codex", "the prompt", "the review output"); err != nil {
		require.NoError(t, err, "CompleteJob failed: %v")
	}

	// Get review by commit SHA
	review, err := db.GetReviewByCommitSHA("rev123")
	require.NoError(t, err, "GetReviewByCommitSHA failed: %v")

	assert.Equal(t, "the review output", review.Output, "unexpected condition")
	assert.Equal(t, "codex", review.Agent, "unexpected condition")
}

func TestReviewVerdictComputation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("verdict populated when output exists and no error", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-pass")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "the prompt", "No issues found. The code looks good.")

		review, err := db.GetReviewByJobID(job.ID)
		require.NoError(t, err, "GetReviewByJobID failed: %v")

		assert.NotNil(t, review.Job.Verdict, "unexpected condition")
		assert.Equal(t, "P", *review.Job.Verdict, "unexpected condition")
	})

	t.Run("verdict nil when output is empty", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-empty")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "the prompt", "") // empty output

		review, err := db.GetReviewByJobID(job.ID)
		require.NoError(t, err, "GetReviewByJobID failed: %v")

		assert.Nil(t, review.Job.Verdict, "unexpected condition")
	})

	t.Run("verdict nil when job has error", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-error")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "API rate limit exceeded")

		// Manually insert a review to simulate edge case
		_, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, 'codex', 'prompt', 'No issues found.')`, job.ID)
		require.NoError(t, err, "Failed to insert review: %v")

		review, err := db.GetReviewByJobID(job.ID)
		require.NoError(t, err, "GetReviewByJobID failed: %v")

		assert.Nil(t, review.Job.Verdict, "unexpected condition")
	})

	t.Run("GetReviewByCommitSHA also respects verdict guard", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-sha")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "the prompt", "No issues found.")

		review, err := db.GetReviewByCommitSHA("verdict-sha")
		require.NoError(t, err, "GetReviewByCommitSHA failed: %v")

		assert.NotNil(t, review.Job.Verdict, "unexpected condition")
		assert.Equal(t, "P", *review.Job.Verdict, "unexpected condition")
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

	assert.Equal(t, "LGTM!", resp.Response, "unexpected condition")

	// Get comments
	comments, err := db.GetCommentsForCommit(commit.ID)
	require.NoError(t, err, "GetCommentsForCommit failed: %v")

	assert.Len(t, comments, 1, "unexpected condition")
}

func TestMarkReviewClosed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "addr123")
	db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "prompt", "output")

	// Get the review
	review, err := db.GetReviewByJobID(job.ID)
	require.NoError(t, err, "GetReviewByJobID failed: %v")

	// Initially not closed
	assert.False(t, review.Closed, "unexpected condition")

	// Mark as closed
	err = db.MarkReviewClosed(review.ID, true)
	require.NoError(t, err, "MarkReviewClosed failed: %v")

	// Verify it's closed
	updated, _ := db.GetReviewByID(review.ID)
	assert.True(t, updated.Closed, "Review should be closed after MarkReviewClosed(true)")

	// Mark as open
	err = db.MarkReviewClosed(review.ID, false)
	require.NoError(t, err, "MarkReviewClosed(false) failed: %v")

	updated2, _ := db.GetReviewByID(review.ID)
	assert.False(t, updated2.Closed, "Review should not be closed after MarkReviewClosed(false)")
}

func TestMarkReviewClosedNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Try to mark a non-existent review
	err := db.MarkReviewClosed(999999, true)
	require.Error(t, err, "unexpected condition")

	// Should be sql.ErrNoRows
	require.ErrorIs(t, err, sql.ErrNoRows, "unexpected condition")
}

func TestMarkReviewClosedByJobID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "jobaddr123")
	db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "prompt", "output")

	// Get the review to verify initial state
	review, err := db.GetReviewByJobID(job.ID)
	require.NoError(t, err, "GetReviewByJobID failed: %v")

	// Initially not closed
	assert.False(t, review.Closed, "unexpected condition")

	// Mark as closed using job ID
	err = db.MarkReviewClosedByJobID(job.ID, true)
	require.NoError(t, err, "MarkReviewClosedByJobID failed: %v")

	// Verify it's closed
	updated, _ := db.GetReviewByJobID(job.ID)
	assert.True(t, updated.Closed, "Review should be closed after MarkReviewClosedByJobID(true)")

	// Mark as open using job ID
	err = db.MarkReviewClosedByJobID(job.ID, false)
	require.NoError(t, err, "MarkReviewClosedByJobID(false) failed: %v")

	updated2, _ := db.GetReviewByJobID(job.ID)
	assert.False(t, updated2.Closed, "Review should not be closed after MarkReviewClosedByJobID(false)")
}

func TestMarkReviewClosedByJobIDNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Try to mark a non-existent job
	err := db.MarkReviewClosedByJobID(999999, true)
	require.Error(t, err, "unexpected condition")

	// Should be sql.ErrNoRows
	require.ErrorIs(t, err, sql.ErrNoRows, "unexpected condition")
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

	assert.True(t, retried, "unexpected condition")

	// Verify job is queued with retry_count=1
	updatedJob, _ := db.GetJobByID(job.ID)
	assert.Equal(t, JobStatusQueued, updatedJob.Status, "unexpected condition")
	count, _ := db.GetJobRetryCount(job.ID)
	assert.Equal(t, 1, count, "unexpected condition")

	// Claim again and retry twice more (retry_count: 1->2, 2->3)
	_, _ = db.ClaimJob("worker-1")
	db.RetryJob(job.ID, "", 3) // retry_count becomes 2
	_, _ = db.ClaimJob("worker-1")
	db.RetryJob(job.ID, "", 3) // retry_count becomes 3

	count, _ = db.GetJobRetryCount(job.ID)
	assert.Equal(t, 3, count, "unexpected condition")

	// Claim again - next retry should fail (at max)
	_, _ = db.ClaimJob("worker-1")
	retried, err = db.RetryJob(job.ID, "", 3)
	require.NoError(t, err, "RetryJob at max failed: %v")

	assert.False(t, retried, "unexpected condition")

	// Job should still be running (retry didn't happen)
	updatedJob, _ = db.GetJobByID(job.ID)
	assert.Equal(t, JobStatusRunning, updatedJob.Status, "unexpected condition")
}

func TestRetryJobOnlyWorksForRunning(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry-status")

	// Try to retry a queued job (should fail - not running)
	retried, err := db.RetryJob(job.ID, "", 3)
	require.NoError(t, err, "RetryJob on queued job failed: %v")

	assert.False(t, retried, "unexpected condition")

	// Claim, complete, then try retry (should fail - job is done)
	_, _ = db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "p", "o")

	retried, err = db.RetryJob(job.ID, "", 3)
	require.NoError(t, err, "RetryJob on done job failed: %v")

	assert.False(t, retried, "unexpected condition")
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

	assert.True(t, retried1, "unexpected condition")
	assert.False(t, retried2, "Second retry should fail (job is no longer running)")

	// Verify retry_count is 1, not 2
	count, _ := db.GetJobRetryCount(job.ID)
	assert.Equal(t, 1, count, "unexpected condition")
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

		assert.True(t, ok, "unexpected condition")

		// Verify: agent swapped, retry_count reset, status queued
		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "backup", updated.Agent, "unexpected condition")
		assert.Equal(t, JobStatusQueued, updated.Status, "unexpected condition")
		count, _ := db.GetJobRetryCount(job.ID)
		assert.Equal(t, 0, count, "unexpected condition")
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

		assert.Equal(t, "o3-mini", job.Model, "unexpected condition")

		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "backup", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.True(t, ok, "unexpected condition")

		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Empty(t, updated.Model, "unexpected condition")
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

		assert.True(t, ok, "unexpected condition")

		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "claude-sonnet", updated.Model, "unexpected condition")
		assert.Equal(t, "backup", updated.Agent, "unexpected condition")
	})

	t.Run("fails with empty backup agent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		_, _, job := createJobChain(t, db, "/tmp/failover-nobackup", "fo-no-backup")
		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "", "")
		require.NoError(t, err, "FailoverJob: %v")

		assert.False(t, ok, "unexpected condition")
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

		assert.False(t, ok, "unexpected condition")
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

		assert.False(t, ok, "unexpected condition")

		// Verify original agent is unchanged
		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "primary", updated.Agent, "unexpected condition")
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
		assert.Equal(t, JobStatusCanceled, updated.Status, "unexpected condition")
	})

	t.Run("cancel running job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-running")
		db.ClaimJob("worker-1")

		err := db.CancelJob(job.ID)
		require.NoError(t, err, "CancelJob failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusCanceled, updated.Status, "unexpected condition")
	})

	t.Run("cancel done job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-done")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.CancelJob(job.ID)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("cancel failed job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-failed")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "some error")

		err := db.CancelJob(job.ID)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("complete respects canceled status", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "complete-canceled")
		db.ClaimJob("worker-1")
		db.CancelJob(job.ID)

		// CompleteJob should not overwrite canceled status
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusCanceled, updated.Status, "unexpected condition")

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
		assert.Equal(t, JobStatusCanceled, updated.Status, "unexpected condition")
	})

	t.Run("canceled jobs counted correctly", func(t *testing.T) {
		// Create and cancel a new job
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-count")
		db.CancelJob(job.ID)

		_, _, _, _, canceled, _, _, err := db.GetJobCounts()
		require.NoError(t, err, "GetJobCounts failed: %v")

		assert.GreaterOrEqual(t, canceled, 1, "unexpected condition")
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
		assert.Equal(t, JobStatusApplied, updated.Status, "unexpected condition")
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-q", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobApplied(job.ID)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("mark applied job again fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-2", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")
		db.MarkJobApplied(job.ID)

		err := db.MarkJobApplied(job.ID)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-review", Agent: "codex"})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.MarkJobApplied(job.ID)
		require.Error(t, err, "unexpected condition")
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
		assert.Equal(t, JobStatusRebased, updated.Status, "unexpected condition")
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-test-q", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobRebased(job.ID)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-review", Agent: "codex"})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.MarkJobRebased(job.ID)
		require.Error(t, err, "unexpected condition")
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
		assert.Equal(t, JobStatusQueued, updated.Status, "unexpected condition")
		assert.Empty(t, updated.Error, "unexpected condition")
		assert.Nil(t, updated.StartedAt, "unexpected condition")
		assert.Nil(t, updated.FinishedAt, "unexpected condition")
	})

	t.Run("rerun canceled job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-canceled")
		db.CancelJob(job.ID)

		err := db.ReenqueueJob(job.ID)
		require.NoError(t, err, "ReenqueueJob failed: %v")

		updated, _ := db.GetJobByID(job.ID)
		assert.Equal(t, JobStatusQueued, updated.Status, "unexpected condition")
	})

	t.Run("rerun done job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-done")
		// ClaimJob returns the claimed job; keep claiming until we get ours
		var claimed *ReviewJob
		for {
			claimed, _ = db.ClaimJob("worker-1")
			assert.NotNil(t, claimed, "unexpected condition")
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
		assert.Equal(t, JobStatusQueued, updated.Status, "unexpected condition")
	})

	t.Run("rerun queued job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-queued")

		err := db.ReenqueueJob(job.ID)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("rerun running job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-running")
		db.ClaimJob("worker-1")

		err := db.ReenqueueJob(job.ID)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("rerun nonexistent job fails", func(t *testing.T) {
		err := db.ReenqueueJob(99999)
		require.Error(t, err, "unexpected condition")
	})

	t.Run("rerun done job and complete again", func(t *testing.T) {
		// Use isolated database to avoid interference from other subtests
		isolatedDB := openTestDB(t)
		defer isolatedDB.Close()

		_, _, job := createJobChain(t, isolatedDB, "/tmp/isolated-repo", "rerun-complete-cycle")

		// First completion cycle
		claimed, _ := isolatedDB.ClaimJob("worker-1")
		assert.False(t, claimed == nil || claimed.ID != job.ID, "unexpected condition")
		err := isolatedDB.CompleteJob(job.ID, "codex", "first prompt", "first output")
		require.NoError(t, err, "First CompleteJob failed: %v")

		// Verify first review exists
		review1, err := isolatedDB.GetReviewByJobID(job.ID)
		require.NoError(t, err, "GetReviewByJobID failed after first complete: %v")

		assert.Equal(t, "first output", review1.Output, "unexpected condition")

		// Re-enqueue the done job
		err = isolatedDB.ReenqueueJob(job.ID)
		require.NoError(t, err, "ReenqueueJob failed: %v")

		// Verify review was deleted
		_, err = isolatedDB.GetReviewByJobID(job.ID)
		require.Error(t, err, "Expected GetReviewByJobID to fail after re-enqueue (review should be deleted)")

		// Second completion cycle
		claimed, _ = isolatedDB.ClaimJob("worker-1")
		assert.False(t, claimed == nil || claimed.ID != job.ID, "unexpected condition")
		err = isolatedDB.CompleteJob(job.ID, "codex", "second prompt", "second output")
		require.NoError(t, err, "Second CompleteJob failed: %v")

		// Verify second review exists with new content
		review2, err := isolatedDB.GetReviewByJobID(job.ID)
		require.NoError(t, err, "GetReviewByJobID failed after second complete: %v")

		assert.Equal(t, "second output", review2.Output, "unexpected condition")
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

	assert.Equal(t, "deadbeef1234", job.PatchID, "unexpected condition")

	// Verify it round-trips through GetJobByID
	got, err := db.GetJobByID(job.ID)
	require.NoError(t, err, "GetJobByID: %v")

	assert.Equal(t, "deadbeef1234", got.PatchID, "unexpected condition")
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

		assert.Equal(t, 1, n, "unexpected condition")

		got, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID: %v")

		assert.Equal(t, "newsha", got.GitRef, "unexpected condition")
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

		assert.Equal(t, 0, n, "unexpected condition")
	})

	t.Run("returns 0 for no matches", func(t *testing.T) {
		newCommit := createCommit(t, db, repo.ID, "nonexistent_new")
		n, err := db.RemapJobGitRef(repo.ID, "nonexistent", "nonexistent_new", "patch", newCommit.ID)
		require.NoError(t, err, "RemapJobGitRef: %v")

		assert.Equal(t, 0, n, "unexpected condition")
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
		assert.False(t, gitRef != expected[i].gitRef || jobType != expected[i].jobType, "unexpected condition")
		i++
	}
	assert.Equal(t, len(expected), i, "unexpected condition")
}

func TestSaveJobSessionID_StaleWorkerIgnored(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "session-race")

	// Worker A claims and saves a session ID while running.
	claimJob(t, db, "worker-A")
	err := db.SaveJobSessionID(job.ID, "worker-A", "session-A")
	require.NoError(t, err, "SaveJobSessionID (worker-A): %v")

	j, _ := db.GetJobByID(job.ID)
	assert.Equal(t, "session-A", j.SessionID, "unexpected condition")

	// Cancel and re-enqueue: clears session_id, resets to queued.
	if err := db.CancelJob(job.ID); err != nil {
		require.NoError(t, err, "CancelJob: %v")
	}
	if err := db.ReenqueueJob(job.ID); err != nil {
		require.NoError(t, err, "ReenqueueJob: %v")
	}
	j, _ = db.GetJobByID(job.ID)
	assert.Empty(t, j.SessionID, "unexpected condition")

	// Worker B claims the re-enqueued job.
	claimJob(t, db, "worker-B")

	// Stale Worker A tries to save its session ID — should be a no-op
	// because worker_id is now "worker-B".
	err = db.SaveJobSessionID(job.ID, "worker-A", "stale-session")
	require.NoError(t, err, "SaveJobSessionID (stale worker-A): %v")

	j, _ = db.GetJobByID(job.ID)
	assert.Empty(t, j.SessionID, "unexpected condition")

	// Worker B saves its own session ID — should succeed.
	err = db.SaveJobSessionID(job.ID, "worker-B", "session-B")
	require.NoError(t, err, "SaveJobSessionID (worker-B): %v")

	j, _ = db.GetJobByID(job.ID)
	assert.Equal(t, "session-B", j.SessionID, "unexpected condition")

	// Worker B's second call is a no-op (first ID wins).
	err = db.SaveJobSessionID(job.ID, "worker-B", "session-B2")
	require.NoError(t, err, "SaveJobSessionID (worker-B second): %v")

	j, _ = db.GetJobByID(job.ID)
	assert.Equal(t, "session-B", j.SessionID, "unexpected condition")
}

func TestSaveJobSessionID_StaleWorkerIgnored(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "session-race")

	// Worker A claims and saves a session ID while running.
	claimJob(t, db, "worker-A")
	err := db.SaveJobSessionID(job.ID, "worker-A", "session-A")
	if err != nil {
		t.Fatalf("SaveJobSessionID (worker-A): %v", err)
	}
	j, _ := db.GetJobByID(job.ID)
	if j.SessionID != "session-A" {
		t.Fatalf("session_id = %q, want session-A", j.SessionID)
	}

	// Cancel and re-enqueue: clears session_id, resets to queued.
	if err := db.CancelJob(job.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if err := db.ReenqueueJob(job.ID); err != nil {
		t.Fatalf("ReenqueueJob: %v", err)
	}
	j, _ = db.GetJobByID(job.ID)
	if j.SessionID != "" {
		t.Fatalf("session_id after reenqueue = %q, want empty", j.SessionID)
	}

	// Worker B claims the re-enqueued job.
	claimJob(t, db, "worker-B")

	// Stale Worker A tries to save its session ID — should be a no-op
	// because worker_id is now "worker-B".
	err = db.SaveJobSessionID(job.ID, "worker-A", "stale-session")
	if err != nil {
		t.Fatalf("SaveJobSessionID (stale worker-A): %v", err)
	}
	j, _ = db.GetJobByID(job.ID)
	if j.SessionID != "" {
		t.Fatalf("stale worker wrote session_id = %q, want empty", j.SessionID)
	}

	// Worker B saves its own session ID — should succeed.
	err = db.SaveJobSessionID(job.ID, "worker-B", "session-B")
	if err != nil {
		t.Fatalf("SaveJobSessionID (worker-B): %v", err)
	}
	j, _ = db.GetJobByID(job.ID)
	if j.SessionID != "session-B" {
		t.Fatalf("session_id = %q, want session-B", j.SessionID)
	}

	// Worker B's second call is a no-op (first ID wins).
	err = db.SaveJobSessionID(job.ID, "worker-B", "session-B2")
	if err != nil {
		t.Fatalf("SaveJobSessionID (worker-B second): %v", err)
	}
	j, _ = db.GetJobByID(job.ID)
	if j.SessionID != "session-B" {
		t.Fatalf("session_id = %q, want session-B (first wins)",
			j.SessionID)
	}
}
