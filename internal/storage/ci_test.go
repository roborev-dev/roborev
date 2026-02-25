package storage

import (
	"database/sql"
	"sync"
	"testing"
	"time"
)

const (
	testRepo   = "myorg/myrepo"
	testSHA    = "sha1"
	testAgent  = "codex"
	testReview = "security"
)

func assertEq[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %s=%v, want %v", name, got, want)
	}
}

// mustCreateCIBatch creates a CI batch, failing the test on error.
func mustCreateCIBatch(t *testing.T, db *DB, ghRepo string, prNum int, headSHA string, totalJobs int) *CIPRBatch {
	t.Helper()
	batch, _, err := db.CreateCIBatch(ghRepo, prNum, headSHA, totalJobs)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	return batch
}

// mustEnqueueReviewJob enqueues a review job, failing the test on error.
func mustEnqueueReviewJob(t *testing.T, db *DB, repoID int64, gitRef, agent, reviewType string) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID: repoID, GitRef: gitRef, Agent: agent, ReviewType: reviewType,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	return job
}

// mustRecordBatchJob links a job to a batch, failing the test on error.
func mustRecordBatchJob(t *testing.T, db *DB, batchID, jobID int64) {
	t.Helper()
	if err := db.RecordBatchJob(batchID, jobID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
}

// mustCreateLinkedBatchJob creates a batch, enqueues a job, and links them.
func mustCreateLinkedBatchJob(t *testing.T, db *DB, repoID int64, ghRepo string, prNum int, headSHA, gitRef, agent, reviewType string) (*CIPRBatch, *ReviewJob) {
	t.Helper()
	batch := mustCreateCIBatch(t, db, ghRepo, prNum, headSHA, 1)
	job := mustEnqueueReviewJob(t, db, repoID, gitRef, agent, reviewType)
	mustRecordBatchJob(t, db, batch.ID, job.ID)
	return batch, job
}

// mustCreateLinkedTerminalJob creates a linked batch+job and sets the job to a terminal status.
func mustCreateLinkedTerminalJob(t *testing.T, db *DB, repoID int64, ghRepo string, prNum int, headSHA, gitRef, agent, reviewType, status string) (*CIPRBatch, int64) {
	t.Helper()
	batch, job := mustCreateLinkedBatchJob(t, db, repoID, ghRepo, prNum, headSHA, gitRef, agent, reviewType)
	setJobStatus(t, db, job.ID, JobStatus(status))
	return batch, job.ID
}

func setBatchTimestamp(t *testing.T, db *DB, batchID int64, column string, offset time.Duration) {
	t.Helper()
	ts := time.Now().UTC().Add(offset).Format("2006-01-02 15:04:05")
	query := `UPDATE ci_pr_batches SET ` + column + ` = ? WHERE id = ?`
	if _, err := db.Exec(query, ts, batchID); err != nil {
		t.Fatalf("setBatchTimestamp (%s): %v", column, err)
	}
}

func setBatchCreatedAt(t *testing.T, db *DB, batchID int64, offset time.Duration) {
	setBatchTimestamp(t, db, batchID, "created_at", offset)
}

func setBatchClaimedAt(t *testing.T, db *DB, batchID int64, offset time.Duration) {
	setBatchTimestamp(t, db, batchID, "claimed_at", offset)
}

func setJobStatusAndError(t *testing.T, db *DB, jobID int64, status, errorMsg string) {
	t.Helper()
	res, err := db.Exec(`UPDATE review_jobs SET status=?, error=? WHERE id=?`, status, errorMsg, jobID)
	if err != nil {
		t.Fatalf("setJobStatusAndError: %v", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("Failed to get rows affected: %v", err)
	}
	if rows != 1 {
		t.Fatalf("Expected exactly 1 row updated for jobID %d, got %d", jobID, rows)
	}
}

func mustAddReview(t *testing.T, db *DB, jobID int64, agent, output string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, ?, 'test-prompt', ?)`, jobID, agent, output); err != nil {
		t.Fatalf("mustAddReview: %v", err)
	}
}

func getBatch(t *testing.T, db *DB, id int64) *CIPRBatch {
	t.Helper()
	var b CIPRBatch
	var synthesized int
	err := db.QueryRow(`SELECT id, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE id = ?`, id).Scan(
		&b.ID, &b.TotalJobs, &b.CompletedJobs, &b.FailedJobs, &synthesized,
	)
	if err != nil {
		t.Fatalf("getBatch: %v", err)
	}
	b.Synthesized = synthesized != 0
	return &b
}

func TestCreateCIBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, created, err := db.CreateCIBatch(testRepo, 42, "abc123", 4)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	assertEq(t, "created", created, true)
	if batch.ID == 0 {
		t.Error("expected non-zero batch ID")
	}
	assertEq(t, "GithubRepo", batch.GithubRepo, testRepo)
	assertEq(t, "PRNumber", batch.PRNumber, 42)
	assertEq(t, "HeadSHA", batch.HeadSHA, "abc123")
	assertEq(t, "TotalJobs", batch.TotalJobs, 4)
	assertEq(t, "CompletedJobs", batch.CompletedJobs, 0)
	assertEq(t, "FailedJobs", batch.FailedJobs, 0)
	assertEq(t, "Synthesized", batch.Synthesized, false)

	// Duplicate insert should return the same batch but created=false
	batch2, created2, err := db.CreateCIBatch(testRepo, 42, "abc123", 4)
	if err != nil {
		t.Fatalf("CreateCIBatch duplicate: %v", err)
	}
	assertEq(t, "created", created2, false)
	assertEq(t, "ID", batch2.ID, batch.ID)
}

func TestHasCIBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("BeforeCreation", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 1, testSHA)
		if err != nil {
			t.Fatalf("HasCIBatch: %v", err)
		}
		assertEq(t, "has", has, false)
	})

	repo, err := db.GetOrCreateRepo("/tmp/test-repo-hasbatch")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	batch := mustCreateCIBatch(t, db, testRepo, 1, testSHA, 2)

	t.Run("EmptyBatch", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 1, testSHA)
		if err != nil {
			t.Fatalf("HasCIBatch (empty): %v", err)
		}
		assertEq(t, "has", has, false)
	})

	t.Run("WithLinkedJob", func(t *testing.T) {
		job := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", "test", testReview)
		mustRecordBatchJob(t, db, batch.ID, job.ID)

		has, err := db.HasCIBatch(testRepo, 1, testSHA)
		if err != nil {
			t.Fatalf("HasCIBatch: %v", err)
		}
		assertEq(t, "has", has, true)
	})

	t.Run("DifferentSHA", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 1, "sha2")
		if err != nil {
			t.Fatalf("HasCIBatch: %v", err)
		}
		assertEq(t, "has", has, false)
	})
}

func TestRecordBatchJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	batch := mustCreateCIBatch(t, db, testRepo, 1, testSHA, 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", "gemini", "review")
	mustRecordBatchJob(t, db, batch.ID, job1.ID)
	mustRecordBatchJob(t, db, batch.ID, job2.ID)

	found, err := db.GetCIBatchByJobID(job1.ID)
	if err != nil {
		t.Fatalf("GetCIBatchByJobID: %v", err)
	}
	if found == nil || found.ID != batch.ID {
		t.Errorf("expected batch ID %d, got %v", batch.ID, found)
	}

	found2, err := db.GetCIBatchByJobID(job2.ID)
	if err != nil {
		t.Fatalf("GetCIBatchByJobID: %v", err)
	}
	if found2 == nil || found2.ID != batch.ID {
		t.Errorf("expected batch ID %d, got %v", batch.ID, found2)
	}
}

func TestIncrementBatchCompleted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, _, err := db.CreateCIBatch(testRepo, 1, testSHA, 3)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	updated, err := db.IncrementBatchCompleted(batch.ID)
	if err != nil {
		t.Fatalf("IncrementBatchCompleted: %v", err)
	}
	if updated.CompletedJobs != 1 {
		t.Errorf("got CompletedJobs=%d, want 1", updated.CompletedJobs)
	}

	updated, err = db.IncrementBatchCompleted(batch.ID)
	if err != nil {
		t.Fatalf("IncrementBatchCompleted: %v", err)
	}
	if updated.CompletedJobs != 2 {
		t.Errorf("got CompletedJobs=%d, want 2", updated.CompletedJobs)
	}
}

func TestIncrementBatchFailed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, _, err := db.CreateCIBatch(testRepo, 1, testSHA, 3)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	updated, err := db.IncrementBatchFailed(batch.ID)
	if err != nil {
		t.Fatalf("IncrementBatchFailed: %v", err)
	}
	if updated.FailedJobs != 1 {
		t.Errorf("got FailedJobs=%d, want 1", updated.FailedJobs)
	}

	// Mix completed and failed
	updated, err = db.IncrementBatchCompleted(batch.ID)
	if err != nil {
		t.Fatalf("IncrementBatchCompleted: %v", err)
	}
	if updated.CompletedJobs != 1 || updated.FailedJobs != 1 {
		t.Errorf("got CompletedJobs=%d, FailedJobs=%d, want 1, 1", updated.CompletedJobs, updated.FailedJobs)
	}
}

func TestIncrementBatchConcurrent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	n := 10
	batch, _, err := db.CreateCIBatch(testRepo, 1, testSHA, n)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.IncrementBatchCompleted(batch.ID)
			if err != nil {
				t.Errorf("IncrementBatchCompleted: %v", err)
			}
		}()
	}
	wg.Wait()

	// Verify final count
	// Can't use GetCIBatchByJobID with 0, read directly
	finalBatch := getBatch(t, db, batch.ID)
	if finalBatch.CompletedJobs != n {
		t.Errorf("got CompletedJobs=%d, want %d", finalBatch.CompletedJobs, n)
	}
}

func TestGetBatchReviews(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	batch := mustCreateCIBatch(t, db, testRepo, 1, testSHA, 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", "gemini", "review")
	mustRecordBatchJob(t, db, batch.ID, job1.ID)
	mustRecordBatchJob(t, db, batch.ID, job2.ID)

	// Complete job1 with a review
	setJobStatus(t, db, job1.ID, JobStatusDone)
	mustAddReview(t, db, job1.ID, testAgent, "finding1")

	// Fail job2
	setJobStatusAndError(t, db, job2.ID, "failed", "timeout")

	reviews, err := db.GetBatchReviews(batch.ID)
	if err != nil {
		t.Fatalf("GetBatchReviews: %v", err)
	}
	if len(reviews) != 2 {
		t.Fatalf("got %d reviews, want 2", len(reviews))
	}

	// First review should be job1 (codex/security)
	assertEq(t, "review[0].Agent", reviews[0].Agent, testAgent)
	assertEq(t, "review[0].ReviewType", reviews[0].ReviewType, testReview)
	assertEq(t, "review[0].Output", reviews[0].Output, "finding1")
	assertEq(t, "review[0].Status", reviews[0].Status, "done")

	// Second review should be job2 (gemini/review)
	assertEq(t, "review[1].Agent", reviews[1].Agent, "gemini")
	assertEq(t, "review[1].ReviewType", reviews[1].ReviewType, "review")
	assertEq(t, "review[1].Status", reviews[1].Status, "failed")
	assertEq(t, "review[1].Error", reviews[1].Error, "timeout")
}

func TestGetCIBatchByJobID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	batch, job := mustCreateLinkedBatchJob(t, db, repo.ID, testRepo, 1, testSHA, "abc..def", testAgent, testReview)

	found, err := db.GetCIBatchByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetCIBatchByJobID: %v", err)
	}
	if found == nil {
		t.Fatal("expected non-nil batch")
	}
	if found.ID != batch.ID {
		t.Errorf("got batch ID %d, want %d", found.ID, batch.ID)
	}

	// Job not in any batch
	notFound, err := db.GetCIBatchByJobID(99999)
	if err != nil {
		t.Fatalf("GetCIBatchByJobID: %v", err)
	}
	if notFound != nil {
		t.Error("expected nil for unknown job ID")
	}
}

func TestClaimBatchForSynthesis(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, _, _ := db.CreateCIBatch(testRepo, 1, testSHA, 1)
	if batch.Synthesized {
		t.Error("expected Synthesized=false initially")
	}

	// First claim should succeed
	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatalf("ClaimBatchForSynthesis: %v", err)
	}
	if !claimed {
		t.Error("expected first claim to succeed")
	}

	// Second claim should fail (already claimed)
	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatalf("ClaimBatchForSynthesis (second): %v", err)
	}
	if claimed {
		t.Error("expected second claim to fail")
	}

	// Unclaim and reclaim should work
	if err := db.UnclaimBatch(batch.ID); err != nil {
		t.Fatalf("UnclaimBatch: %v", err)
	}
	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatalf("ClaimBatchForSynthesis (after unclaim): %v", err)
	}
	if !claimed {
		t.Error("expected claim after unclaim to succeed")
	}
}

func TestFinalizeBatch_PreventsStaleRepost(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	batch, _ := mustCreateLinkedTerminalJob(t, db, repo.ID, testRepo, 1, testSHA, testSHA, testAgent, testReview, "done")

	// Claim the batch (simulates postBatchResults starting)
	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected claim to succeed")
	}

	// Verify claimed_at is set after claim
	var claimedBefore sql.NullString
	if err := db.QueryRow(`SELECT claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&claimedBefore); err != nil {
		t.Fatalf("scan claimed_at before finalize: %v", err)
	}
	if !claimedBefore.Valid {
		t.Fatal("expected claimed_at to be set after claim")
	}

	// Finalize after successful post
	if err := db.FinalizeBatch(batch.ID); err != nil {
		t.Fatalf("FinalizeBatch: %v", err)
	}

	// Verify claimed_at is cleared after finalize
	var claimedAfter sql.NullString
	if err := db.QueryRow(`SELECT claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&claimedAfter); err != nil {
		t.Fatalf("scan claimed_at after finalize: %v", err)
	}
	if claimedAfter.Valid {
		t.Fatalf("expected claimed_at to be NULL after finalize, got %q", claimedAfter.String)
	}

	// Verify synthesized is still 1
	var synthesized int
	if err := db.QueryRow(`SELECT synthesized FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized); err != nil {
		t.Fatalf("scan synthesized after finalize: %v", err)
	}
	if synthesized != 1 {
		t.Fatalf("expected synthesized=1 after finalize, got %d", synthesized)
	}

	// Finalized batch should NOT appear in stale batches
	stale, err := db.GetStaleBatches()
	if err != nil {
		t.Fatalf("GetStaleBatches: %v", err)
	}
	for _, b := range stale {
		if b.ID == batch.ID {
			t.Error("finalized batch should not appear in stale batches")
		}
	}

	// Re-claiming a finalized batch should fail (synthesized=1)
	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Error("should not be able to re-claim a finalized batch")
	}
}

func TestGetStaleBatches_StaleClaim(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	batch, _ := mustCreateLinkedTerminalJob(t, db, repo.ID, testRepo, 1, testSHA, testSHA, testAgent, testReview, "done")

	// Claim the batch, then backdate claimed_at to simulate a stale claim
	_, _ = db.ClaimBatchForSynthesis(batch.ID)
	setBatchClaimedAt(t, db, batch.ID, -10*time.Minute)

	stale, err := db.GetStaleBatches()
	if err != nil {
		t.Fatalf("GetStaleBatches: %v", err)
	}

	found := false
	for _, b := range stale {
		if b.ID == batch.ID {
			found = true
		}
	}
	if !found {
		t.Error("stale claimed batch should appear in GetStaleBatches")
	}
}

func TestDeleteEmptyBatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create an empty batch and backdate it so it's eligible for cleanup
	emptyOld := mustCreateCIBatch(t, db, testRepo, 1, "sha-old", 2)
	setBatchCreatedAt(t, db, emptyOld.ID, -5*time.Minute)

	// Create an empty batch that's recent (should NOT be deleted)
	mustCreateCIBatch(t, db, testRepo, 2, "sha-recent", 1)

	// Create a non-empty batch that's old (should NOT be deleted)
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	nonEmpty, _ := mustCreateLinkedBatchJob(t, db, repo.ID, testRepo, 3, "sha-nonempty", "a..b", testAgent, testReview)
	setBatchCreatedAt(t, db, nonEmpty.ID, -5*time.Minute)

	// Run cleanup
	n, err := db.DeleteEmptyBatches()
	if err != nil {
		t.Fatalf("DeleteEmptyBatches: %v", err)
	}
	assertEq(t, "deleted count", n, 1)

	has, err := db.HasCIBatch(testRepo, 1, "sha-old")
	if err != nil {
		t.Fatalf("HasCIBatch (old empty): %v", err)
	}
	assertEq(t, "has", has, false)

	var recentCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_batches WHERE github_repo = ? AND pr_number = ? AND head_sha = ?`,
		testRepo, 2, "sha-recent").Scan(&recentCount); err != nil {
		t.Fatalf("count recent batch: %v", err)
	}
	assertEq(t, "recentCount", recentCount, 1)

	has, err = db.HasCIBatch(testRepo, 3, "sha-nonempty")
	if err != nil {
		t.Fatalf("HasCIBatch (non-empty): %v", err)
	}
	assertEq(t, "has", has, true)
}

func TestCancelJob_ReturnsErrNoRowsForTerminalJobs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-cancel")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	t.Run("TerminalJob_ReturnsErrNoRows", func(t *testing.T) {
		job := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
		setJobStatus(t, db, job.ID, JobStatusDone)

		err := db.CancelJob(job.ID)
		if err != sql.ErrNoRows {
			t.Fatalf("expected sql.ErrNoRows, got: %v", err)
		}
	})

	t.Run("QueuedJob_Succeeds", func(t *testing.T) {
		job := mustEnqueueReviewJob(t, db, repo.ID, "c..d", testAgent, testReview)
		if err := db.CancelJob(job.ID); err != nil {
			t.Fatalf("CancelJob on queued job: %v", err)
		}

		var status string
		if err := db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
			t.Fatalf("query status: %v", err)
		}
		assertEq(t, "status", status, "canceled")
	})
}

func TestCancelSupersededBatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-supersede")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	// Create an old batch with linked jobs
	oldBatch := mustCreateCIBatch(t, db, "owner/repo", 1, "oldsha", 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "base..oldsha", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "base..oldsha", testAgent, "review")
	if err := db.RecordBatchJob(oldBatch.ID, job1.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
	if err := db.RecordBatchJob(oldBatch.ID, job2.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}

	// Create a synthesized (already posted) batch — should NOT be canceled
	doneBatch := mustCreateCIBatch(t, db, "owner/repo", 1, "donesha", 1)
	doneJob := mustEnqueueReviewJob(t, db, repo.ID, "base..donesha", testAgent, testReview)
	if err := db.RecordBatchJob(doneBatch.ID, doneJob.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
	if _, err := db.ClaimBatchForSynthesis(doneBatch.ID); err != nil {
		t.Fatalf("ClaimBatchForSynthesis: %v", err)
	}

	// Cancel superseded batches for a new HEAD
	canceledIDs, err := db.CancelSupersededBatches("owner/repo", 1, "newsha")
	if err != nil {
		t.Fatalf("CancelSupersededBatches: %v", err)
	}
	if len(canceledIDs) != 2 {
		t.Errorf("len(canceledIDs) = %d, want 2", len(canceledIDs))
	}

	// Old batch should be deleted
	has, err := db.HasCIBatch("owner/repo", 1, "oldsha")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	if has {
		t.Error("old batch should have been deleted")
	}

	// Jobs should be canceled
	var status string
	if err := db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, job1.ID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "canceled" {
		t.Errorf("job1 status = %q, want canceled", status)
	}

	// Synthesized batch should still exist
	has, err = db.HasCIBatch("owner/repo", 1, "donesha")
	if err != nil {
		t.Fatalf("HasCIBatch done: %v", err)
	}
	if !has {
		t.Error("synthesized batch should NOT have been canceled")
	}

	// No-op when no superseded batches exist
	canceledIDs, err = db.CancelSupersededBatches("owner/repo", 1, "newsha")
	if err != nil {
		t.Fatalf("CancelSupersededBatches no-op: %v", err)
	}
	if len(canceledIDs) != 0 {
		t.Errorf("expected 0 canceled on no-op, got %d", len(canceledIDs))
	}
}
