package storage

import (
	"database/sql"
	"errors"
	"testing"

	"time"

	"github.com/stretchr/testify/require"
)

const (
	workerID     = "worker-1"
	workerID2    = "worker-2"
	agentName    = "codex"
	testRepoPath = "/tmp/test-repo"
)

func setupJobEnv(t *testing.T, ref string) (*DB, *ReviewJob) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	_, _, job := createJobChain(t, db, testRepoPath, ref)
	return db, job
}

func TestJobLifecycle(t *testing.T) {
	db, job := setupJobEnv(t, "abc123")

	require.Equal(t, JobStatusQueued, job.Status)

	// Claim job
	claimed := claimJob(t, db, workerID)
	if claimed.ID != job.ID {
		t.Error("ClaimJob returned wrong job")
	}
	require.Equal(t, JobStatusRunning, claimed.Status)

	// Claim again should return nil (no more jobs)
	claimed2, err := db.ClaimJob(workerID2)
	require.NoError(t, err)
	if claimed2 != nil {
		t.Error("ClaimJob should return nil when no jobs available")
	}

	// Complete job
	err = db.CompleteJob(job.ID, agentName, "test prompt", "test output")
	require.NoError(t, err)

	// Verify job status
	updatedJob, err := db.GetJobByID(job.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusDone, updatedJob.Status)
}

func TestJobFailure(t *testing.T) {
	db, job := setupJobEnv(t, "def456")
	claimJob(t, db, workerID)

	// Fail the job
	_, err := db.FailJob(job.ID, "", "test error message")
	require.NoError(t, err)

	updatedJob, err := db.GetJobByID(job.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusFailed, updatedJob.Status)
	require.Equal(t, "test error message", updatedJob.Error)
}

func TestFailJobOwnerScoped(t *testing.T) {
	db, job := setupJobEnv(t, "fail-owner")
	claimJob(t, db, workerID)

	// Wrong worker should not be able to fail the job
	updated, err := db.FailJob(job.ID, workerID2, "stale fail")
	require.NoError(t, err)
	if updated {
		t.Error("FailJob should return false for wrong worker")
	}

	// Job should still be running
	j, err := db.GetJobByID(job.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusRunning, j.Status)

	// Correct worker should succeed
	updated, err = db.FailJob(job.ID, workerID, "legit fail")
	require.NoError(t, err)
	if !updated {
		t.Error("FailJob should return true for correct worker")
	}

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusFailed, j.Status)
	require.Equal(t, "legit fail", j.Error)
}

func TestRetryJobOwnerScoped(t *testing.T) {
	db, job := setupJobEnv(t, "retry-owner")
	claimJob(t, db, workerID)

	// Wrong worker should not be able to retry the job
	retried, err := db.RetryJob(job.ID, workerID2, 3)
	require.NoError(t, err)
	if retried {
		t.Error("RetryJob should return false for wrong worker")
	}

	// Job should still be running (not requeued)
	j, err := db.GetJobByID(job.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusRunning, j.Status)

	// Correct worker should succeed
	retried, err = db.RetryJob(job.ID, workerID, 3)
	require.NoError(t, err)
	if !retried {
		t.Error("RetryJob should return true for correct worker")
	}

	j, err = db.GetJobByID(job.ID)
	require.NoError(t, err)
	require.Equal(t, JobStatusQueued, j.Status)
}

func TestReviewOperations(t *testing.T) {
	db, job := setupJobEnv(t, "rev123")
	claimJob(t, db, workerID)
	if err := db.CompleteJob(job.ID, agentName, "the prompt", "the review output"); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Get review by commit SHA
	review, err := db.GetReviewByCommitSHA("rev123")
	require.NoError(t, err)

	require.Equal(t, "the review output", review.Output)
	if review.Agent != agentName {
		t.Errorf("Expected agent 'codex', got '%s'", review.Agent)
	}
}

func TestReviewVerdictComputation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("verdict populated when output exists and no error", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "verdict-pass")
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "the prompt", "No issues found. The code looks good.")

		review, err := db.GetReviewByJobID(job.ID)
		require.NoError(t, err)
		if review.Job.Verdict == nil {
			t.Fatal("Expected verdict to be populated, got nil")
		}
		if *review.Job.Verdict != "P" {
			t.Errorf("Expected verdict 'P', got '%s'", *review.Job.Verdict)
		}
	})

	t.Run("verdict nil when output is empty", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "verdict-empty")
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "the prompt", "") // empty output

		review, err := db.GetReviewByJobID(job.ID)
		require.NoError(t, err)
		if review.Job.Verdict != nil {
			t.Errorf("Expected verdict to be nil for empty output, got '%s'", *review.Job.Verdict)
		}
	})

	t.Run("verdict nil when job has error", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "verdict-error")
		db.ClaimJob(workerID)
		db.FailJob(job.ID, "", "API rate limit exceeded")

		// Manually insert a review to simulate edge case
		_, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, 'codex', 'prompt', 'No issues found.')`, job.ID)
		require.NoError(t, err)

		review, err := db.GetReviewByJobID(job.ID)
		require.NoError(t, err)
		if review.Job.Verdict != nil {
			t.Errorf("Expected verdict to be nil when job has error, got '%s'", *review.Job.Verdict)
		}
	})

	t.Run("GetReviewByCommitSHA also respects verdict guard", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "verdict-sha")
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "the prompt", "No issues found.")

		review, err := db.GetReviewByCommitSHA("verdict-sha")
		require.NoError(t, err)
		if review.Job.Verdict == nil {
			t.Fatal("Expected verdict to be populated, got nil")
		}
		if *review.Job.Verdict != "P" {
			t.Errorf("Expected verdict 'P', got '%s'", *review.Job.Verdict)
		}
	})
}

func TestResponseOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo(testRepoPath)
	commit, _ := db.GetOrCreateCommit(repo.ID, "resp123", "Author", "Subject", time.Now())

	// Add comment
	resp, err := db.AddComment(commit.ID, "test-user", "LGTM!")
	require.NoError(t, err)

	require.Equal(t, "LGTM!", resp.Response)

	// Get comments
	comments, err := db.GetCommentsForCommit(commit.ID)
	require.NoError(t, err)

	require.Equal(t, 1, len(comments))
}

func TestMarkReviewClosed(t *testing.T) {
	db, job := setupJobEnv(t, "addr123")
	db.ClaimJob(workerID)
	db.CompleteJob(job.ID, agentName, "prompt", "output")

	// Get the review
	review, err := db.GetReviewByJobID(job.ID)
	require.NoError(t, err)

	// Initially not closed
	if review.Closed {
		t.Error("Review should not be closed initially")
	}

	// Mark as closed
	err = db.MarkReviewClosed(review.ID, true)
	require.NoError(t, err)

	// Verify it's closed
	updated, _ := db.GetReviewByID(review.ID)
	if !updated.Closed {
		t.Error("Review should be closed after MarkReviewClosed(true)")
	}

	// Mark as open
	err = db.MarkReviewClosed(review.ID, false)
	require.NoError(t, err)

	updated2, _ := db.GetReviewByID(review.ID)
	if updated2.Closed {
		t.Error("Review should not be closed after MarkReviewClosed(false)")
	}
}

func TestMarkReviewClosedNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Try to mark a non-existent review
	err := db.MarkReviewClosed(999999, true)
	if err == nil {
		t.Fatal("Expected error for non-existent review")
	}

	// Should be sql.ErrNoRows
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows, got: %v", err)
	}
}

func TestMarkReviewClosedByJobID(t *testing.T) {
	db, job := setupJobEnv(t, "jobaddr123")
	db.ClaimJob(workerID)
	db.CompleteJob(job.ID, agentName, "prompt", "output")

	// Get the review to verify initial state
	review, err := db.GetReviewByJobID(job.ID)
	require.NoError(t, err)

	// Initially not closed
	if review.Closed {
		t.Error("Review should not be closed initially")
	}

	// Mark as closed using job ID
	err = db.MarkReviewClosedByJobID(job.ID, true)
	require.NoError(t, err)

	// Verify it's closed
	updated, _ := db.GetReviewByJobID(job.ID)
	if !updated.Closed {
		t.Error("Review should be closed after MarkReviewClosedByJobID(true)")
	}

	// Mark as open using job ID
	err = db.MarkReviewClosedByJobID(job.ID, false)
	require.NoError(t, err)

	updated2, _ := db.GetReviewByJobID(job.ID)
	if updated2.Closed {
		t.Error("Review should not be closed after MarkReviewClosedByJobID(false)")
	}
}

func TestMarkReviewClosedByJobIDNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Try to mark a non-existent job
	err := db.MarkReviewClosedByJobID(999999, true)
	if err == nil {
		t.Fatal("Expected error for non-existent job")
	}

	// Should be sql.ErrNoRows
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows, got: %v", err)
	}
}

func TestRetryJob(t *testing.T) {
	db, job := setupJobEnv(t, "retry123")

	// Claim the job (makes it running)
	claimJob(t, db, workerID)

	// Retry should succeed (retry_count: 0 -> 1)
	retried, err := db.RetryJob(job.ID, "", 3)
	require.NoError(t, err)
	if !retried {
		t.Error("First retry should succeed")
	}

	// Verify job is queued with retry_count=1
	updatedJob, _ := db.GetJobByID(job.ID)
	require.Equal(t, JobStatusQueued, updatedJob.Status)
	count, _ := db.GetJobRetryCount(job.ID)
	require.Equal(t, 1, count)

	// Claim again and retry twice more (retry_count: 1->2, 2->3)
	_, _ = db.ClaimJob(workerID)
	db.RetryJob(job.ID, "", 3) // retry_count becomes 2
	_, _ = db.ClaimJob(workerID)
	db.RetryJob(job.ID, "", 3) // retry_count becomes 3

	count, _ = db.GetJobRetryCount(job.ID)
	require.Equal(t, 3, count)

	// Claim again - next retry should fail (at max)
	_, _ = db.ClaimJob(workerID)
	retried, err = db.RetryJob(job.ID, "", 3)
	require.NoError(t, err)
	if retried {
		t.Error("Retry should fail when at maxRetries")
	}

	// Job should still be running (retry didn't happen)
	updatedJob, _ = db.GetJobByID(job.ID)
	require.Equal(t, JobStatusRunning, updatedJob.Status)
}

func TestRetryJobOnlyWorksForRunning(t *testing.T) {
	db, job := setupJobEnv(t, "retry-status")

	// Try to retry a queued job (should fail - not running)
	retried, err := db.RetryJob(job.ID, "", 3)
	require.NoError(t, err)
	if retried {
		t.Error("RetryJob should not work on queued jobs")
	}

	// Claim, complete, then try retry (should fail - job is done)
	_, _ = db.ClaimJob(workerID)
	db.CompleteJob(job.ID, agentName, "p", "o")

	retried, err = db.RetryJob(job.ID, "", 3)
	require.NoError(t, err)
	if retried {
		t.Error("RetryJob should not work on completed jobs")
	}
}

func TestRetryJobAtomic(t *testing.T) {
	db, job := setupJobEnv(t, "retry-atomic")
	claimJob(t, db, workerID)

	// Simulate two concurrent retries - only first should succeed
	// (In practice this tests the atomic update)
	retried1, _ := db.RetryJob(job.ID, "", 3)
	retried2, _ := db.RetryJob(job.ID, "", 3) // Job is now queued, not running

	if !retried1 {
		t.Error("First retry should succeed")
	}
	if retried2 {
		t.Error("Second retry should fail (job is no longer running)")
	}

	// Verify retry_count is 1, not 2
	count, _ := db.GetJobRetryCount(job.ID)
	require.Equal(t, 1, count)
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
		require.NoError(t, err)

		// Claim to make it running
		claimJob(t, db, workerID)

		// Failover should succeed
		ok, err := db.FailoverJob(job.ID, workerID, "backup", "")
		require.NoError(t, err)
		if !ok {
			t.Fatal("Expected failover to succeed")
		}

		// Verify: agent swapped, retry_count reset, status queued
		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err)
		if updated.Agent != "backup" {
			t.Errorf("Agent = %q, want %q", updated.Agent, "backup")
		}
		if updated.Status != JobStatusQueued {
			t.Errorf("Status = %q, want %q", updated.Status, JobStatusQueued)
		}
		count, _ := db.GetJobRetryCount(job.ID)
		require.Equal(t, 0, count)
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
		require.NoError(t, err)
		if job.Model != "o3-mini" {
			t.Fatalf("Model = %q, want %q", job.Model, "o3-mini")
		}

		claimJob(t, db, workerID)

		ok, err := db.FailoverJob(job.ID, workerID, "backup", "")
		require.NoError(t, err)
		if !ok {
			t.Fatal("Expected failover to succeed")
		}

		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err)
		if updated.Model != "" {
			t.Errorf("Model = %q, want empty (cleared on failover)", updated.Model)
		}
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
		require.NoError(t, err)

		claimJob(t, db, workerID)

		ok, err := db.FailoverJob(job.ID, workerID, "backup", "claude-sonnet")
		require.NoError(t, err)
		if !ok {
			t.Fatal("Expected failover to succeed")
		}

		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err)
		if updated.Model != "claude-sonnet" {
			t.Errorf("Model = %q, want %q", updated.Model, "claude-sonnet")
		}
		if updated.Agent != "backup" {
			t.Errorf("Agent = %q, want %q", updated.Agent, "backup")
		}
	})

	t.Run("fails with empty backup agent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		_, _, job := createJobChain(t, db, "/tmp/failover-nobackup", "fo-no-backup")
		claimJob(t, db, workerID)

		ok, err := db.FailoverJob(job.ID, workerID, "", "")
		require.NoError(t, err)
		if ok {
			t.Error("Expected failover to return false with empty backup agent")
		}
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
			Agent:    agentName,
		})
		require.NoError(t, err)
		claimJob(t, db, workerID)

		ok, err := db.FailoverJob(job.ID, workerID, agentName, "")
		require.NoError(t, err)
		if ok {
			t.Error("Expected failover to return false when backup == agent")
		}
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
		require.NoError(t, err)

		ok, err := db.FailoverJob(job.ID, workerID, "backup", "")
		require.NoError(t, err)
		if ok {
			t.Error("Expected failover to return false for queued job")
		}
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
		require.NoError(t, err)
		claimJob(t, db, workerID)

		// First failover: primary -> backup
		db.FailoverJob(job.ID, workerID, "backup", "")

		// Reclaim, now agent is "backup"
		claimJob(t, db, workerID)

		// Second failover with same backup agent should fail (agent == backup)
		ok, err := db.FailoverJob(job.ID, workerID, "backup", "")
		require.NoError(t, err)
		if ok {
			t.Error("Expected second failover to return false (agent already is backup)")
		}
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
		require.NoError(t, err)
		claimJob(t, db, workerID)

		// A different worker should not be able to failover this job
		ok, err := db.FailoverJob(job.ID, workerID2, "backup", "")
		require.NoError(t, err)
		if ok {
			t.Error("Expected failover to return false when called by wrong worker")
		}

		// Verify original agent is unchanged
		updated, err := db.GetJobByID(job.ID)
		require.NoError(t, err)
		if updated.Agent != "primary" {
			t.Errorf("Agent = %q, want %q (should not have changed)", updated.Agent, "primary")
		}
	})
}

func TestCancelJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("cancel queued job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "cancel-queued")

		err := db.CancelJob(job.ID)
		require.NoError(t, err)

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusCanceled, updated.Status)
	})

	t.Run("cancel running job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "cancel-running")
		db.ClaimJob(workerID)

		err := db.CancelJob(job.ID)
		require.NoError(t, err)

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusCanceled, updated.Status)
	})

	t.Run("cancel done job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "cancel-done")
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "prompt", "output")

		err := db.CancelJob(job.ID)
		if err == nil {
			t.Error("CancelJob should fail for done jobs")
		}
	})

	t.Run("cancel failed job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "cancel-failed")
		db.ClaimJob(workerID)
		db.FailJob(job.ID, "", "some error")

		err := db.CancelJob(job.ID)
		if err == nil {
			t.Error("CancelJob should fail for failed jobs")
		}
	})

	t.Run("complete respects canceled status", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "complete-canceled")
		db.ClaimJob(workerID)
		db.CancelJob(job.ID)

		// CompleteJob should not overwrite canceled status
		db.CompleteJob(job.ID, agentName, "prompt", "output")

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusCanceled, updated.Status)

		// Verify no review was inserted (should get sql.ErrNoRows)
		_, err := db.GetReviewByJobID(job.ID)
		if err == nil {
			t.Error("No review should be inserted for canceled job")
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Expected sql.ErrNoRows, got: %v", err)
		}
	})

	t.Run("fail respects canceled status", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "fail-canceled")
		db.ClaimJob(workerID)
		db.CancelJob(job.ID)

		// FailJob should not overwrite canceled status
		db.FailJob(job.ID, "", "some error")

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusCanceled, updated.Status)
	})

	t.Run("canceled jobs counted correctly", func(t *testing.T) {
		// Create and cancel a new job
		_, _, job := createJobChain(t, db, testRepoPath, "cancel-count")
		db.CancelJob(job.ID)

		_, _, _, _, canceled, _, _, err := db.GetJobCounts()
		require.NoError(t, err)
		if canceled < 1 {
			t.Errorf("Expected at least 1 canceled job, got %d", canceled)
		}
	})
}

func TestMarkJobApplied(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo(testRepoPath)
	commit, _ := db.GetOrCreateCommit(repo.ID, "applied-test", "A", "S", time.Now())

	t.Run("mark done fix job as applied", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test", Agent: agentName, JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "prompt", "output")

		err := db.MarkJobApplied(job.ID)
		require.NoError(t, err)

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusApplied, updated.Status)
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-q", Agent: agentName, JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobApplied(job.ID)
		if err == nil {
			t.Error("MarkJobApplied should fail for queued jobs")
		}
	})

	t.Run("mark applied job again fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-2", Agent: agentName, JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "prompt", "output")
		db.MarkJobApplied(job.ID)

		err := db.MarkJobApplied(job.ID)
		if err == nil {
			t.Error("MarkJobApplied should fail for already-applied jobs")
		}
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-review", Agent: agentName})
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "prompt", "output")

		err := db.MarkJobApplied(job.ID)
		if err == nil {
			t.Error("MarkJobApplied should fail for non-fix jobs")
		}
	})
}

func TestMarkJobRebased(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo(testRepoPath)
	commit, _ := db.GetOrCreateCommit(repo.ID, "rebased-test", "A", "S", time.Now())

	t.Run("mark done fix job as rebased", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-test", Agent: agentName, JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "prompt", "output")

		err := db.MarkJobRebased(job.ID)
		require.NoError(t, err)

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusRebased, updated.Status)
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-test-q", Agent: agentName, JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobRebased(job.ID)
		if err == nil {
			t.Error("MarkJobRebased should fail for queued jobs")
		}
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-review", Agent: agentName})
		db.ClaimJob(workerID)
		db.CompleteJob(job.ID, agentName, "prompt", "output")

		err := db.MarkJobRebased(job.ID)
		if err == nil {
			t.Error("MarkJobRebased should fail for non-fix jobs")
		}
	})
}

func TestReenqueueJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("rerun failed job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "rerun-failed")
		db.ClaimJob(workerID)
		db.FailJob(job.ID, "", "some error")

		err := db.ReenqueueJob(job.ID)
		require.NoError(t, err)

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusQueued, updated.Status)
		if updated.Error != "" {
			t.Errorf("Expected error to be cleared, got '%s'", updated.Error)
		}
		if updated.StartedAt != nil {
			t.Error("Expected started_at to be nil")
		}
		if updated.FinishedAt != nil {
			t.Error("Expected finished_at to be nil")
		}
	})

	t.Run("rerun canceled job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "rerun-canceled")
		db.CancelJob(job.ID)

		err := db.ReenqueueJob(job.ID)
		require.NoError(t, err)

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusQueued, updated.Status)
	})

	t.Run("rerun done job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "rerun-done")
		// ClaimJob returns the claimed job; keep claiming until we get ours
		var claimed *ReviewJob
		for {
			claimed, _ = db.ClaimJob(workerID)
			if claimed == nil {
				t.Fatal("No job to claim")
			}
			if claimed.ID == job.ID {
				break
			}
			// Complete other jobs to clear them
			db.CompleteJob(claimed.ID, agentName, "prompt", "output")
		}
		db.CompleteJob(job.ID, agentName, "prompt", "output")

		err := db.ReenqueueJob(job.ID)
		require.NoError(t, err)

		updated, _ := db.GetJobByID(job.ID)
		require.Equal(t, JobStatusQueued, updated.Status)
	})

	t.Run("rerun queued job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "rerun-queued")

		err := db.ReenqueueJob(job.ID)
		if err == nil {
			t.Error("ReenqueueJob should fail for queued jobs")
		}
	})

	t.Run("rerun running job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, testRepoPath, "rerun-running")
		db.ClaimJob(workerID)

		err := db.ReenqueueJob(job.ID)
		if err == nil {
			t.Error("ReenqueueJob should fail for running jobs")
		}
	})

	t.Run("rerun nonexistent job fails", func(t *testing.T) {
		err := db.ReenqueueJob(99999)
		if err == nil {
			t.Error("ReenqueueJob should fail for nonexistent jobs")
		}
	})

	t.Run("rerun done job and complete again", func(t *testing.T) {
		// Use isolated database to avoid interference from other subtests
		isolatedDB := openTestDB(t)
		defer isolatedDB.Close()

		_, _, job := createJobChain(t, isolatedDB, "/tmp/isolated-repo", "rerun-complete-cycle")

		// First completion cycle
		claimed, _ := isolatedDB.ClaimJob(workerID)
		if claimed == nil || claimed.ID != job.ID {
			t.Fatal("Failed to claim the expected job")
		}
		err := isolatedDB.CompleteJob(job.ID, agentName, "first prompt", "first output")
		require.NoError(t, err)

		// Verify first review exists
		review1, err := isolatedDB.GetReviewByJobID(job.ID)
		require.NoError(t, err)
		require.Equal(t, "first output", review1.Output)

		// Re-enqueue the done job
		err = isolatedDB.ReenqueueJob(job.ID)
		require.NoError(t, err)

		// Verify review was deleted
		_, err = isolatedDB.GetReviewByJobID(job.ID)
		if err == nil {
			t.Error("Expected GetReviewByJobID to fail after re-enqueue (review should be deleted)")
		}

		// Second completion cycle
		claimed, _ = isolatedDB.ClaimJob(workerID)
		if claimed == nil || claimed.ID != job.ID {
			t.Fatal("Failed to claim the expected job for second cycle")
		}
		err = isolatedDB.CompleteJob(job.ID, agentName, "second prompt", "second output")
		require.NoError(t, err)

		// Verify second review exists with new content
		review2, err := isolatedDB.GetReviewByJobID(job.ID)
		require.NoError(t, err)
		require.Equal(t, "second output", review2.Output)
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
	require.NoError(t, err)
	require.Equal(t, "deadbeef1234", job.PatchID)

	// Verify it round-trips through GetJobByID
	got, err := db.GetJobByID(job.ID)
	require.NoError(t, err)
	require.Equal(t, "deadbeef1234", got.PatchID)
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
		require.NoError(t, err)

		newCommit := createCommit(t, db, repo.ID, "newsha")
		n, err := db.RemapJobGitRef(repo.ID, "oldsha", "newsha", "patchabc", newCommit.ID)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		got, err := db.GetJobByID(job.ID)
		require.NoError(t, err)
		require.Equal(t, "newsha", got.GitRef)
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
		require.NoError(t, err)

		newCommit := createCommit(t, db, repo.ID, "sha2_new")
		n, err := db.RemapJobGitRef(repo.ID, "sha2", "sha2_new", "patch_different", newCommit.ID)
		require.NoError(t, err)
		require.Equal(t, 0, n)
	})

	t.Run("returns 0 for no matches", func(t *testing.T) {
		newCommit := createCommit(t, db, repo.ID, "nonexistent_new")
		n, err := db.RemapJobGitRef(repo.ID, "nonexistent", "nonexistent_new", "patch", newCommit.ID)
		require.NoError(t, err)
		require.Equal(t, 0, n)
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
	require.NoError(t, err)

	// 2. Dirty job (git_ref='dirty') - should become 'dirty'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'dirty', 'codex', 'done', 'review')`, repo.ID)
	require.NoError(t, err)

	// 3. Dirty job (diff_content set) - should become 'dirty'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type, diff_content) VALUES (?, 'some-ref', 'codex', 'done', 'review', 'diff here')`, repo.ID)
	require.NoError(t, err)

	// 4. Range job (git_ref has ..) - should become 'range'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'abc..def', 'codex', 'done', 'review')`, repo.ID)
	require.NoError(t, err)

	// 5. Task job (no commit_id, no diff, non-dirty git_ref) - should become 'task'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'analyze', 'codex', 'done', 'review')`, repo.ID)
	require.NoError(t, err)

	// Run backfill SQL (same as migration)
	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'dirty' WHERE (git_ref = 'dirty' OR diff_content IS NOT NULL) AND job_type = 'review'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'range' WHERE git_ref LIKE '%..%' AND commit_id IS NULL AND job_type = 'review'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'task' WHERE commit_id IS NULL AND diff_content IS NULL AND git_ref != 'dirty' AND git_ref NOT LIKE '%..%' AND git_ref != '' AND job_type = 'review'`)
	require.NoError(t, err)

	// Verify results
	rows, err := db.Query(`SELECT git_ref, job_type FROM review_jobs ORDER BY id`)
	require.NoError(t, err)
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
			t.Fatalf("scan row: %v", err)
		}
		if i >= len(expected) {
			t.Fatalf("more rows than expected")
		}
		if gitRef != expected[i].gitRef || jobType != expected[i].jobType {
			t.Errorf("row %d: got (%q, %q), want (%q, %q)", i, gitRef, jobType, expected[i].gitRef, expected[i].jobType)
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("got %d rows, want %d", i, len(expected))
	}
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
