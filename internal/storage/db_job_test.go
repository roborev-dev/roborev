package storage

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestJobLifecycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "abc123")

	if job.Status != JobStatusQueued {
		t.Errorf("Expected status 'queued', got '%s'", job.Status)
	}

	// Claim job
	claimed := claimJob(t, db, "worker-1")
	if claimed.ID != job.ID {
		t.Error("ClaimJob returned wrong job")
	}
	if claimed.Status != JobStatusRunning {
		t.Errorf("Expected status 'running', got '%s'", claimed.Status)
	}

	// Claim again should return nil (no more jobs)
	claimed2, err := db.ClaimJob("worker-2")
	if err != nil {
		t.Fatalf("ClaimJob (second) failed: %v", err)
	}
	if claimed2 != nil {
		t.Error("ClaimJob should return nil when no jobs available")
	}

	// Complete job
	err = db.CompleteJob(job.ID, "codex", "test prompt", "test output")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Verify job status
	updatedJob, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if updatedJob.Status != JobStatusDone {
		t.Errorf("Expected status 'done', got '%s'", updatedJob.Status)
	}
}

func TestJobFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "def456")
	claimJob(t, db, "worker-1")

	// Fail the job
	_, err := db.FailJob(job.ID, "", "test error message")
	if err != nil {
		t.Fatalf("FailJob failed: %v", err)
	}

	updatedJob, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if updatedJob.Status != JobStatusFailed {
		t.Errorf("Expected status 'failed', got '%s'", updatedJob.Status)
	}
	if updatedJob.Error != "test error message" {
		t.Errorf("Expected error message 'test error message', got '%s'", updatedJob.Error)
	}
}

func TestFailJobOwnerScoped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "fail-owner")
	claimJob(t, db, "worker-1")

	// Wrong worker should not be able to fail the job
	updated, err := db.FailJob(job.ID, "worker-2", "stale fail")
	if err != nil {
		t.Fatalf("FailJob with wrong worker failed: %v", err)
	}
	if updated {
		t.Error("FailJob should return false for wrong worker")
	}

	// Job should still be running
	j, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if j.Status != JobStatusRunning {
		t.Errorf("Expected status 'running', got '%s'", j.Status)
	}

	// Correct worker should succeed
	updated, err = db.FailJob(job.ID, "worker-1", "legit fail")
	if err != nil {
		t.Fatalf("FailJob with correct worker failed: %v", err)
	}
	if !updated {
		t.Error("FailJob should return true for correct worker")
	}

	j, err = db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if j.Status != JobStatusFailed {
		t.Errorf("Expected status 'failed', got '%s'", j.Status)
	}
	if j.Error != "legit fail" {
		t.Errorf("Expected error 'legit fail', got '%s'", j.Error)
	}
}

func TestRetryJobOwnerScoped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry-owner")
	claimJob(t, db, "worker-1")

	// Wrong worker should not be able to retry the job
	retried, err := db.RetryJob(job.ID, "worker-2", 3)
	if err != nil {
		t.Fatalf("RetryJob with wrong worker failed: %v", err)
	}
	if retried {
		t.Error("RetryJob should return false for wrong worker")
	}

	// Job should still be running (not requeued)
	j, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if j.Status != JobStatusRunning {
		t.Errorf("Expected status 'running', got '%s'", j.Status)
	}

	// Correct worker should succeed
	retried, err = db.RetryJob(job.ID, "worker-1", 3)
	if err != nil {
		t.Fatalf("RetryJob with correct worker failed: %v", err)
	}
	if !retried {
		t.Error("RetryJob should return true for correct worker")
	}

	j, err = db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if j.Status != JobStatusQueued {
		t.Errorf("Expected status 'queued', got '%s'", j.Status)
	}
}

func TestReviewOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "rev123")
	claimJob(t, db, "worker-1")
	if err := db.CompleteJob(job.ID, "codex", "the prompt", "the review output"); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Get review by commit SHA
	review, err := db.GetReviewByCommitSHA("rev123")
	if err != nil {
		t.Fatalf("GetReviewByCommitSHA failed: %v", err)
	}

	if review.Output != "the review output" {
		t.Errorf("Expected output 'the review output', got '%s'", review.Output)
	}
	if review.Agent != "codex" {
		t.Errorf("Expected agent 'codex', got '%s'", review.Agent)
	}
}

func TestReviewVerdictComputation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("verdict populated when output exists and no error", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-pass")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "the prompt", "No issues found. The code looks good.")

		review, err := db.GetReviewByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}
		if review.Job.Verdict == nil {
			t.Fatal("Expected verdict to be populated, got nil")
		}
		if *review.Job.Verdict != "P" {
			t.Errorf("Expected verdict 'P', got '%s'", *review.Job.Verdict)
		}
	})

	t.Run("verdict nil when output is empty", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-empty")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "the prompt", "") // empty output

		review, err := db.GetReviewByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}
		if review.Job.Verdict != nil {
			t.Errorf("Expected verdict to be nil for empty output, got '%s'", *review.Job.Verdict)
		}
	})

	t.Run("verdict nil when job has error", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-error")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "API rate limit exceeded")

		// Manually insert a review to simulate edge case
		_, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, 'codex', 'prompt', 'No issues found.')`, job.ID)
		if err != nil {
			t.Fatalf("Failed to insert review: %v", err)
		}

		review, err := db.GetReviewByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}
		if review.Job.Verdict != nil {
			t.Errorf("Expected verdict to be nil when job has error, got '%s'", *review.Job.Verdict)
		}
	})

	t.Run("GetReviewByCommitSHA also respects verdict guard", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "verdict-sha")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "the prompt", "No issues found.")

		review, err := db.GetReviewByCommitSHA("verdict-sha")
		if err != nil {
			t.Fatalf("GetReviewByCommitSHA failed: %v", err)
		}
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

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	commit, _ := db.GetOrCreateCommit(repo.ID, "resp123", "Author", "Subject", time.Now())

	// Add comment
	resp, err := db.AddComment(commit.ID, "test-user", "LGTM!")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	if resp.Response != "LGTM!" {
		t.Errorf("Expected comment 'LGTM!', got '%s'", resp.Response)
	}

	// Get comments
	comments, err := db.GetCommentsForCommit(commit.ID)
	if err != nil {
		t.Fatalf("GetCommentsForCommit failed: %v", err)
	}

	if len(comments) != 1 {
		t.Errorf("Expected 1 comment, got %d", len(comments))
	}
}

func TestMarkReviewAddressed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "addr123")
	db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "prompt", "output")

	// Get the review
	review, err := db.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}

	// Initially not addressed
	if review.Addressed {
		t.Error("Review should not be addressed initially")
	}

	// Mark as addressed
	err = db.MarkReviewAddressed(review.ID, true)
	if err != nil {
		t.Fatalf("MarkReviewAddressed failed: %v", err)
	}

	// Verify it's addressed
	updated, _ := db.GetReviewByID(review.ID)
	if !updated.Addressed {
		t.Error("Review should be addressed after MarkReviewAddressed(true)")
	}

	// Mark as unaddressed
	err = db.MarkReviewAddressed(review.ID, false)
	if err != nil {
		t.Fatalf("MarkReviewAddressed(false) failed: %v", err)
	}

	updated2, _ := db.GetReviewByID(review.ID)
	if updated2.Addressed {
		t.Error("Review should not be addressed after MarkReviewAddressed(false)")
	}
}

func TestMarkReviewAddressedNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Try to mark a non-existent review
	err := db.MarkReviewAddressed(999999, true)
	if err == nil {
		t.Fatal("Expected error for non-existent review")
	}

	// Should be sql.ErrNoRows
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows, got: %v", err)
	}
}

func TestMarkReviewAddressedByJobID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "jobaddr123")
	db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "prompt", "output")

	// Get the review to verify initial state
	review, err := db.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}

	// Initially not addressed
	if review.Addressed {
		t.Error("Review should not be addressed initially")
	}

	// Mark as addressed using job ID
	err = db.MarkReviewAddressedByJobID(job.ID, true)
	if err != nil {
		t.Fatalf("MarkReviewAddressedByJobID failed: %v", err)
	}

	// Verify it's addressed
	updated, _ := db.GetReviewByJobID(job.ID)
	if !updated.Addressed {
		t.Error("Review should be addressed after MarkReviewAddressedByJobID(true)")
	}

	// Mark as unaddressed using job ID
	err = db.MarkReviewAddressedByJobID(job.ID, false)
	if err != nil {
		t.Fatalf("MarkReviewAddressedByJobID(false) failed: %v", err)
	}

	updated2, _ := db.GetReviewByJobID(job.ID)
	if updated2.Addressed {
		t.Error("Review should not be addressed after MarkReviewAddressedByJobID(false)")
	}
}

func TestMarkReviewAddressedByJobIDNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Try to mark a non-existent job
	err := db.MarkReviewAddressedByJobID(999999, true)
	if err == nil {
		t.Fatal("Expected error for non-existent job")
	}

	// Should be sql.ErrNoRows
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows, got: %v", err)
	}
}

func TestRetryJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry123")

	// Claim the job (makes it running)
	claimJob(t, db, "worker-1")

	// Retry should succeed (retry_count: 0 -> 1)
	retried, err := db.RetryJob(job.ID, "", 3)
	if err != nil {
		t.Fatalf("RetryJob failed: %v", err)
	}
	if !retried {
		t.Error("First retry should succeed")
	}

	// Verify job is queued with retry_count=1
	updatedJob, _ := db.GetJobByID(job.ID)
	if updatedJob.Status != JobStatusQueued {
		t.Errorf("Expected status 'queued', got '%s'", updatedJob.Status)
	}
	count, _ := db.GetJobRetryCount(job.ID)
	if count != 1 {
		t.Errorf("Expected retry_count=1, got %d", count)
	}

	// Claim again and retry twice more (retry_count: 1->2, 2->3)
	_, _ = db.ClaimJob("worker-1")
	db.RetryJob(job.ID, "", 3) // retry_count becomes 2
	_, _ = db.ClaimJob("worker-1")
	db.RetryJob(job.ID, "", 3) // retry_count becomes 3

	count, _ = db.GetJobRetryCount(job.ID)
	if count != 3 {
		t.Errorf("Expected retry_count=3, got %d", count)
	}

	// Claim again - next retry should fail (at max)
	_, _ = db.ClaimJob("worker-1")
	retried, err = db.RetryJob(job.ID, "", 3)
	if err != nil {
		t.Fatalf("RetryJob at max failed: %v", err)
	}
	if retried {
		t.Error("Retry should fail when at maxRetries")
	}

	// Job should still be running (retry didn't happen)
	updatedJob, _ = db.GetJobByID(job.ID)
	if updatedJob.Status != JobStatusRunning {
		t.Errorf("Expected status 'running' after failed retry, got '%s'", updatedJob.Status)
	}
}

func TestRetryJobOnlyWorksForRunning(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "retry-status")

	// Try to retry a queued job (should fail - not running)
	retried, err := db.RetryJob(job.ID, "", 3)
	if err != nil {
		t.Fatalf("RetryJob on queued job failed: %v", err)
	}
	if retried {
		t.Error("RetryJob should not work on queued jobs")
	}

	// Claim, complete, then try retry (should fail - job is done)
	_, _ = db.ClaimJob("worker-1")
	db.CompleteJob(job.ID, "codex", "p", "o")

	retried, err = db.RetryJob(job.ID, "", 3)
	if err != nil {
		t.Fatalf("RetryJob on done job failed: %v", err)
	}
	if retried {
		t.Error("RetryJob should not work on completed jobs")
	}
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

	if !retried1 {
		t.Error("First retry should succeed")
	}
	if retried2 {
		t.Error("Second retry should fail (job is no longer running)")
	}

	// Verify retry_count is 1, not 2
	count, _ := db.GetJobRetryCount(job.ID)
	if count != 1 {
		t.Errorf("Expected retry_count=1 (atomic), got %d", count)
	}
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
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}

		// Claim to make it running
		claimJob(t, db, "worker-1")

		// Failover should succeed
		ok, err := db.FailoverJob(job.ID, "worker-1", "backup")
		if err != nil {
			t.Fatalf("FailoverJob: %v", err)
		}
		if !ok {
			t.Fatal("Expected failover to succeed")
		}

		// Verify: agent swapped, retry_count reset, status queued
		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID: %v", err)
		}
		if updated.Agent != "backup" {
			t.Errorf("Agent = %q, want %q", updated.Agent, "backup")
		}
		if updated.Status != JobStatusQueued {
			t.Errorf("Status = %q, want %q", updated.Status, JobStatusQueued)
		}
		count, _ := db.GetJobRetryCount(job.ID)
		if count != 0 {
			t.Errorf("RetryCount = %d, want 0", count)
		}
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
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		if job.Model != "o3-mini" {
			t.Fatalf("Model = %q, want %q", job.Model, "o3-mini")
		}

		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "backup")
		if err != nil {
			t.Fatalf("FailoverJob: %v", err)
		}
		if !ok {
			t.Fatal("Expected failover to succeed")
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID: %v", err)
		}
		if updated.Model != "" {
			t.Errorf("Model = %q, want empty (cleared on failover)", updated.Model)
		}
	})

	t.Run("fails with empty backup agent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		_, _, job := createJobChain(t, db, "/tmp/failover-nobackup", "fo-no-backup")
		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "")
		if err != nil {
			t.Fatalf("FailoverJob: %v", err)
		}
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
			Agent:    "codex",
		})
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		claimJob(t, db, "worker-1")

		ok, err := db.FailoverJob(job.ID, "worker-1", "codex")
		if err != nil {
			t.Fatalf("FailoverJob: %v", err)
		}
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
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}

		ok, err := db.FailoverJob(job.ID, "worker-1", "backup")
		if err != nil {
			t.Fatalf("FailoverJob: %v", err)
		}
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
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		claimJob(t, db, "worker-1")

		// First failover: primary -> backup
		db.FailoverJob(job.ID, "worker-1", "backup")

		// Reclaim, now agent is "backup"
		claimJob(t, db, "worker-1")

		// Second failover with same backup agent should fail (agent == backup)
		ok, err := db.FailoverJob(job.ID, "worker-1", "backup")
		if err != nil {
			t.Fatalf("FailoverJob second attempt: %v", err)
		}
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
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		claimJob(t, db, "worker-1")

		// A different worker should not be able to failover this job
		ok, err := db.FailoverJob(job.ID, "worker-2", "backup")
		if err != nil {
			t.Fatalf("FailoverJob: %v", err)
		}
		if ok {
			t.Error("Expected failover to return false when called by wrong worker")
		}

		// Verify original agent is unchanged
		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID: %v", err)
		}
		if updated.Agent != "primary" {
			t.Errorf("Agent = %q, want %q (should not have changed)", updated.Agent, "primary")
		}
	})
}

func TestCancelJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("cancel queued job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-queued")

		err := db.CancelJob(job.ID)
		if err != nil {
			t.Fatalf("CancelJob failed: %v", err)
		}

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusCanceled {
			t.Errorf("Expected status 'canceled', got '%s'", updated.Status)
		}
	})

	t.Run("cancel running job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-running")
		db.ClaimJob("worker-1")

		err := db.CancelJob(job.ID)
		if err != nil {
			t.Fatalf("CancelJob failed: %v", err)
		}

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusCanceled {
			t.Errorf("Expected status 'canceled', got '%s'", updated.Status)
		}
	})

	t.Run("cancel done job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-done")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.CancelJob(job.ID)
		if err == nil {
			t.Error("CancelJob should fail for done jobs")
		}
	})

	t.Run("cancel failed job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-failed")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "some error")

		err := db.CancelJob(job.ID)
		if err == nil {
			t.Error("CancelJob should fail for failed jobs")
		}
	})

	t.Run("complete respects canceled status", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "complete-canceled")
		db.ClaimJob("worker-1")
		db.CancelJob(job.ID)

		// CompleteJob should not overwrite canceled status
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusCanceled {
			t.Errorf("CompleteJob should not overwrite canceled status, got '%s'", updated.Status)
		}

		// Verify no review was inserted (should get sql.ErrNoRows)
		_, err := db.GetReviewByJobID(job.ID)
		if err == nil {
			t.Error("No review should be inserted for canceled job")
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Expected sql.ErrNoRows, got: %v", err)
		}
	})

	t.Run("fail respects canceled status", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "fail-canceled")
		db.ClaimJob("worker-1")
		db.CancelJob(job.ID)

		// FailJob should not overwrite canceled status
		db.FailJob(job.ID, "", "some error")

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusCanceled {
			t.Errorf("FailJob should not overwrite canceled status, got '%s'", updated.Status)
		}
	})

	t.Run("canceled jobs counted correctly", func(t *testing.T) {
		// Create and cancel a new job
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "cancel-count")
		db.CancelJob(job.ID)

		_, _, _, _, canceled, _, _, err := db.GetJobCounts()
		if err != nil {
			t.Fatalf("GetJobCounts failed: %v", err)
		}
		if canceled < 1 {
			t.Errorf("Expected at least 1 canceled job, got %d", canceled)
		}
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
		if err != nil {
			t.Fatalf("MarkJobApplied failed: %v", err)
		}

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusApplied {
			t.Errorf("Expected status 'applied', got '%s'", updated.Status)
		}
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-q", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobApplied(job.ID)
		if err == nil {
			t.Error("MarkJobApplied should fail for queued jobs")
		}
	})

	t.Run("mark applied job again fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-test-2", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")
		db.MarkJobApplied(job.ID)

		err := db.MarkJobApplied(job.ID)
		if err == nil {
			t.Error("MarkJobApplied should fail for already-applied jobs")
		}
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "applied-review", Agent: "codex"})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.MarkJobApplied(job.ID)
		if err == nil {
			t.Error("MarkJobApplied should fail for non-fix jobs")
		}
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
		if err != nil {
			t.Fatalf("MarkJobRebased failed: %v", err)
		}

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusRebased {
			t.Errorf("Expected status 'rebased', got '%s'", updated.Status)
		}
	})

	t.Run("mark non-done job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-test-q", Agent: "codex", JobType: JobTypeFix, ParentJobID: 1})

		err := db.MarkJobRebased(job.ID)
		if err == nil {
			t.Error("MarkJobRebased should fail for queued jobs")
		}
	})

	t.Run("mark non-fix job fails", func(t *testing.T) {
		job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rebased-review", Agent: "codex"})
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

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
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-failed")
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "some error")

		err := db.ReenqueueJob(job.ID)
		if err != nil {
			t.Fatalf("ReenqueueJob failed: %v", err)
		}

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", updated.Status)
		}
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
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-canceled")
		db.CancelJob(job.ID)

		err := db.ReenqueueJob(job.ID)
		if err != nil {
			t.Fatalf("ReenqueueJob failed: %v", err)
		}

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun done job", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-done")
		// ClaimJob returns the claimed job; keep claiming until we get ours
		var claimed *ReviewJob
		for {
			claimed, _ = db.ClaimJob("worker-1")
			if claimed == nil {
				t.Fatal("No job to claim")
			}
			if claimed.ID == job.ID {
				break
			}
			// Complete other jobs to clear them
			db.CompleteJob(claimed.ID, "codex", "prompt", "output")
		}
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		err := db.ReenqueueJob(job.ID)
		if err != nil {
			t.Fatalf("ReenqueueJob failed: %v", err)
		}

		updated, _ := db.GetJobByID(job.ID)
		if updated.Status != JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun queued job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-queued")

		err := db.ReenqueueJob(job.ID)
		if err == nil {
			t.Error("ReenqueueJob should fail for queued jobs")
		}
	})

	t.Run("rerun running job fails", func(t *testing.T) {
		_, _, job := createJobChain(t, db, "/tmp/test-repo", "rerun-running")
		db.ClaimJob("worker-1")

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
		claimed, _ := isolatedDB.ClaimJob("worker-1")
		if claimed == nil || claimed.ID != job.ID {
			t.Fatal("Failed to claim the expected job")
		}
		err := isolatedDB.CompleteJob(job.ID, "codex", "first prompt", "first output")
		if err != nil {
			t.Fatalf("First CompleteJob failed: %v", err)
		}

		// Verify first review exists
		review1, err := isolatedDB.GetReviewByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed after first complete: %v", err)
		}
		if review1.Output != "first output" {
			t.Errorf("Expected first output, got '%s'", review1.Output)
		}

		// Re-enqueue the done job
		err = isolatedDB.ReenqueueJob(job.ID)
		if err != nil {
			t.Fatalf("ReenqueueJob failed: %v", err)
		}

		// Verify review was deleted
		_, err = isolatedDB.GetReviewByJobID(job.ID)
		if err == nil {
			t.Error("Expected GetReviewByJobID to fail after re-enqueue (review should be deleted)")
		}

		// Second completion cycle
		claimed, _ = isolatedDB.ClaimJob("worker-1")
		if claimed == nil || claimed.ID != job.ID {
			t.Fatal("Failed to claim the expected job for second cycle")
		}
		err = isolatedDB.CompleteJob(job.ID, "codex", "second prompt", "second output")
		if err != nil {
			t.Fatalf("Second CompleteJob failed: %v", err)
		}

		// Verify second review exists with new content
		review2, err := isolatedDB.GetReviewByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed after second complete: %v", err)
		}
		if review2.Output != "second output" {
			t.Errorf("Expected second output, got '%s'", review2.Output)
		}
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
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if job.PatchID != "deadbeef1234" {
		t.Errorf("expected PatchID=deadbeef1234, got %q", job.PatchID)
	}

	// Verify it round-trips through GetJobByID
	got, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.PatchID != "deadbeef1234" {
		t.Errorf("GetJobByID: expected PatchID=deadbeef1234, got %q", got.PatchID)
	}
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
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}

		newCommit := createCommit(t, db, repo.ID, "newsha")
		n, err := db.RemapJobGitRef(repo.ID, "oldsha", "newsha", "patchabc", newCommit.ID)
		if err != nil {
			t.Fatalf("RemapJobGitRef: %v", err)
		}
		if n != 1 {
			t.Errorf("expected 1 row updated, got %d", n)
		}

		got, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID: %v", err)
		}
		if got.GitRef != "newsha" {
			t.Errorf("expected git_ref=newsha, got %q", got.GitRef)
		}
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
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}

		newCommit := createCommit(t, db, repo.ID, "sha2_new")
		n, err := db.RemapJobGitRef(repo.ID, "sha2", "sha2_new", "patch_different", newCommit.ID)
		if err != nil {
			t.Fatalf("RemapJobGitRef: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 rows updated (patch_id mismatch), got %d", n)
		}
	})

	t.Run("returns 0 for no matches", func(t *testing.T) {
		newCommit := createCommit(t, db, repo.ID, "nonexistent_new")
		n, err := db.RemapJobGitRef(repo.ID, "nonexistent", "nonexistent_new", "patch", newCommit.ID)
		if err != nil {
			t.Fatalf("RemapJobGitRef: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 rows updated, got %d", n)
		}
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
	if err != nil {
		t.Fatalf("insert review job: %v", err)
	}

	// 2. Dirty job (git_ref='dirty') - should become 'dirty'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'dirty', 'codex', 'done', 'review')`, repo.ID)
	if err != nil {
		t.Fatalf("insert dirty job: %v", err)
	}

	// 3. Dirty job (diff_content set) - should become 'dirty'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type, diff_content) VALUES (?, 'some-ref', 'codex', 'done', 'review', 'diff here')`, repo.ID)
	if err != nil {
		t.Fatalf("insert dirty-with-diff job: %v", err)
	}

	// 4. Range job (git_ref has ..) - should become 'range'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'abc..def', 'codex', 'done', 'review')`, repo.ID)
	if err != nil {
		t.Fatalf("insert range job: %v", err)
	}

	// 5. Task job (no commit_id, no diff, non-dirty git_ref) - should become 'task'
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, agent, status, job_type) VALUES (?, 'analyze', 'codex', 'done', 'review')`, repo.ID)
	if err != nil {
		t.Fatalf("insert task job: %v", err)
	}

	// Run backfill SQL (same as migration)
	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'dirty' WHERE (git_ref = 'dirty' OR diff_content IS NOT NULL) AND job_type = 'review'`)
	if err != nil {
		t.Fatalf("backfill dirty: %v", err)
	}
	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'range' WHERE git_ref LIKE '%..%' AND commit_id IS NULL AND job_type = 'review'`)
	if err != nil {
		t.Fatalf("backfill range: %v", err)
	}
	_, err = db.Exec(`UPDATE review_jobs SET job_type = 'task' WHERE commit_id IS NULL AND diff_content IS NULL AND git_ref != 'dirty' AND git_ref NOT LIKE '%..%' AND git_ref != '' AND job_type = 'review'`)
	if err != nil {
		t.Fatalf("backfill task: %v", err)
	}

	// Verify results
	rows, err := db.Query(`SELECT git_ref, job_type FROM review_jobs ORDER BY id`)
	if err != nil {
		t.Fatalf("query jobs: %v", err)
	}
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
