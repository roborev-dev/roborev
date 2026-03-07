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

func assertJobStatus(t *testing.T, db *DB, jobID int64, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, jobID).Scan(&got); err != nil {
		t.Fatalf("assertJobStatus: %v", err)
	}
	assertEq(t, "job status", got, want)
}

func assertBatchCount(t *testing.T, db *DB, batchID int64, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_batches WHERE id = ?`, batchID).Scan(&count); err != nil {
		t.Fatalf("assertBatchCount: %v", err)
	}
	assertEq(t, "batch count", count, want)
}

func assertClaimedAt(t *testing.T, db *DB, batchID int64, wantSet bool) {
	t.Helper()
	var claimedAt sql.NullString
	if err := db.QueryRow(`SELECT claimed_at FROM ci_pr_batches WHERE id = ?`, batchID).Scan(&claimedAt); err != nil {
		t.Fatalf("assertClaimedAt: %v", err)
	}
	assertEq(t, "claimed_at is set", claimedAt.Valid, wantSet)
}

func assertBatchSynthesized(t *testing.T, db *DB, batchID int64, want int) {
	t.Helper()
	var synthesized int
	if err := db.QueryRow(`SELECT synthesized FROM ci_pr_batches WHERE id = ?`, batchID).Scan(&synthesized); err != nil {
		t.Fatalf("assertBatchSynthesized: %v", err)
	}
	assertEq(t, "synthesized", synthesized, want)
}

type LinkedJobOpts struct {
	RepoID     int64
	GithubRepo string
	PRNumber   int
	HeadSHA    string
	GitRef     string
	Agent      string
	ReviewType string
	Status     string
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
func mustCreateLinkedBatchJob(t *testing.T, db *DB, opts LinkedJobOpts) (*CIPRBatch, *ReviewJob) {
	t.Helper()
	batch := mustCreateCIBatch(t, db, opts.GithubRepo, opts.PRNumber, opts.HeadSHA, 1)
	job := mustEnqueueReviewJob(t, db, opts.RepoID, opts.GitRef, opts.Agent, opts.ReviewType)
	mustRecordBatchJob(t, db, batch.ID, job.ID)
	return batch, job
}

// mustCreateLinkedTerminalJob creates a linked batch+job and sets the job to a terminal status.
func mustCreateLinkedTerminalJob(t *testing.T, db *DB, opts LinkedJobOpts) (*CIPRBatch, int64) {
	t.Helper()
	batch, job := mustCreateLinkedBatchJob(t, db, opts)
	setJobStatus(t, db, job.ID, JobStatus(opts.Status))
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
	if found == nil {
		t.Fatal("expected non-nil batch")
	}
	assertEq(t, "found.ID", found.ID, batch.ID)

	found2, err := db.GetCIBatchByJobID(job2.ID)
	if err != nil {
		t.Fatalf("GetCIBatchByJobID: %v", err)
	}
	if found2 == nil {
		t.Fatal("expected non-nil batch")
	}
	assertEq(t, "found2.ID", found2.ID, batch.ID)
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
	assertEq(t, "CompletedJobs", updated.CompletedJobs, 1)

	updated, err = db.IncrementBatchCompleted(batch.ID)
	if err != nil {
		t.Fatalf("IncrementBatchCompleted: %v", err)
	}
	assertEq(t, "CompletedJobs", updated.CompletedJobs, 2)
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
	assertEq(t, "FailedJobs", updated.FailedJobs, 1)

	// Mix completed and failed
	updated, err = db.IncrementBatchCompleted(batch.ID)
	if err != nil {
		t.Fatalf("IncrementBatchCompleted: %v", err)
	}
	assertEq(t, "CompletedJobs", updated.CompletedJobs, 1)
	assertEq(t, "FailedJobs", updated.FailedJobs, 1)
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
	assertEq(t, "CompletedJobs", finalBatch.CompletedJobs, n)
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
		t.Fatalf("expected 2 reviews, got %d", len(reviews))
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
	batch, job := mustCreateLinkedBatchJob(t, db, LinkedJobOpts{
		RepoID:     repo.ID,
		GithubRepo: testRepo,
		PRNumber:   1,
		HeadSHA:    testSHA,
		GitRef:     "abc..def",
		Agent:      testAgent,
		ReviewType: testReview,
	})

	found, err := db.GetCIBatchByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetCIBatchByJobID: %v", err)
	}
	if found == nil {
		t.Fatal("expected non-nil batch")
	}
	assertEq(t, "found.ID", found.ID, batch.ID)

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
	assertEq(t, "batch.Synthesized", batch.Synthesized, false)

	// First claim should succeed
	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatalf("ClaimBatchForSynthesis: %v", err)
	}
	assertEq(t, "claimed", claimed, true)

	// Second claim should fail (already claimed)
	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatalf("ClaimBatchForSynthesis (second): %v", err)
	}
	assertEq(t, "claimed", claimed, false)

	// Unclaim and reclaim should work
	if err := db.UnclaimBatch(batch.ID); err != nil {
		t.Fatalf("UnclaimBatch: %v", err)
	}
	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatalf("ClaimBatchForSynthesis (after unclaim): %v", err)
	}
	assertEq(t, "claimed", claimed, true)
}

func TestFinalizeBatch_PreventsStaleRepost(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	batch, _ := mustCreateLinkedTerminalJob(t, db, LinkedJobOpts{
		RepoID:     repo.ID,
		GithubRepo: testRepo,
		PRNumber:   1,
		HeadSHA:    testSHA,
		GitRef:     testSHA,
		Agent:      testAgent,
		ReviewType: testReview,
		Status:     "done",
	})

	// Claim the batch (simulates postBatchResults starting)
	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, "claimed", claimed, true)

	assertClaimedAt(t, db, batch.ID, true)

	// Finalize after successful post
	if err := db.FinalizeBatch(batch.ID); err != nil {
		t.Fatalf("FinalizeBatch: %v", err)
	}

	assertClaimedAt(t, db, batch.ID, false)

	assertBatchSynthesized(t, db, batch.ID, 1)

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
	assertEq(t, "claimed", claimed, false)
}

func TestGetStaleBatches_StaleClaim(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	batch, _ := mustCreateLinkedTerminalJob(t, db, LinkedJobOpts{
		RepoID:     repo.ID,
		GithubRepo: testRepo,
		PRNumber:   1,
		HeadSHA:    testSHA,
		GitRef:     testSHA,
		Agent:      testAgent,
		ReviewType: testReview,
		Status:     "done",
	})

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
	nonEmpty, _ := mustCreateLinkedBatchJob(t, db, LinkedJobOpts{
		RepoID:     repo.ID,
		GithubRepo: testRepo,
		PRNumber:   3,
		HeadSHA:    "sha-nonempty",
		GitRef:     "a..b",
		Agent:      testAgent,
		ReviewType: testReview,
	})
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

		assertJobStatus(t, db, job.ID, "canceled")
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
	assertEq(t, "len(canceledIDs)", len(canceledIDs), 2)

	// Old batch should be deleted
	has, err := db.HasCIBatch("owner/repo", 1, "oldsha")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	assertEq(t, "has", has, false)

	// Jobs should be canceled
	assertJobStatus(t, db, job1.ID, "canceled")

	// Synthesized batch should still exist
	has, err = db.HasCIBatch("owner/repo", 1, "donesha")
	if err != nil {
		t.Fatalf("HasCIBatch done: %v", err)
	}
	assertEq(t, "has", has, true)

	// No-op when no superseded batches exist
	canceledIDs, err = db.CancelSupersededBatches("owner/repo", 1, "newsha")
	if err != nil {
		t.Fatalf("CancelSupersededBatches no-op: %v", err)
	}
	assertEq(t, "len(canceledIDs)", len(canceledIDs), 0)
}

func TestLatestBatchTimeForPR(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("no batches returns zero time", func(t *testing.T) {
		ts, err := db.LatestBatchTimeForPR(testRepo, 99)
		if err != nil {
			t.Fatalf("LatestBatchTimeForPR: %v", err)
		}
		if !ts.IsZero() {
			t.Errorf("expected zero time, got %v", ts)
		}
	})

	t.Run("returns latest batch time", func(t *testing.T) {
		before := time.Now().UTC().Truncate(time.Second)

		repo, err := db.GetOrCreateRepo("/tmp/test-throttle")
		if err != nil {
			t.Fatalf("GetOrCreateRepo: %v", err)
		}

		batchA := mustCreateCIBatch(
			t, db, testRepo, 42, "sha-a", 1,
		)
		jobA := mustEnqueueReviewJob(
			t, db, repo.ID, "a..b", "codex", "security",
		)
		mustRecordBatchJob(t, db, batchA.ID, jobA.ID)

		batchB := mustCreateCIBatch(
			t, db, testRepo, 42, "sha-b", 1,
		)
		jobB := mustEnqueueReviewJob(
			t, db, repo.ID, "c..d", "codex", "security",
		)
		mustRecordBatchJob(t, db, batchB.ID, jobB.ID)

		ts, err := db.LatestBatchTimeForPR(testRepo, 42)
		if err != nil {
			t.Fatalf("LatestBatchTimeForPR: %v", err)
		}
		if ts.IsZero() {
			t.Fatal("expected non-zero time")
		}
		// The latest batch time should be at or after our "before" marker
		if ts.Before(before.Add(-1 * time.Second)) {
			t.Errorf(
				"expected time >= %v, got %v",
				before, ts,
			)
		}
	})

	t.Run("different PR returns zero", func(t *testing.T) {
		ts, err := db.LatestBatchTimeForPR(testRepo, 999)
		if err != nil {
			t.Fatalf("LatestBatchTimeForPR: %v", err)
		}
		if !ts.IsZero() {
			t.Errorf("expected zero time for different PR, got %v", ts)
		}
	})
}

func TestGetPendingBatchPRs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-pending")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	// Batch for PR #5 (unsynthesized, unclaimed)
	batch5 := mustCreateCIBatch(t, db, testRepo, 5, "sha5", 1)
	job5 := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
	mustRecordBatchJob(t, db, batch5.ID, job5.ID)

	// Batch for PR #7 (unsynthesized, unclaimed)
	batch7 := mustCreateCIBatch(t, db, testRepo, 7, "sha7", 1)
	job7 := mustEnqueueReviewJob(t, db, repo.ID, "c..d", testAgent, testReview)
	mustRecordBatchJob(t, db, batch7.ID, job7.ID)

	// Batch for PR #9 — synthesized (should NOT appear)
	batch9 := mustCreateCIBatch(t, db, testRepo, 9, "sha9", 1)
	job9 := mustEnqueueReviewJob(t, db, repo.ID, "e..f", testAgent, testReview)
	mustRecordBatchJob(t, db, batch9.ID, job9.ID)
	if _, err := db.ClaimBatchForSynthesis(batch9.ID); err != nil {
		t.Fatalf("ClaimBatchForSynthesis: %v", err)
	}

	// Batch for a different repo (should NOT appear)
	batchOther := mustCreateCIBatch(t, db, "other/repo", 5, "sha-other", 1)
	jobOther := mustEnqueueReviewJob(t, db, repo.ID, "g..h", testAgent, testReview)
	mustRecordBatchJob(t, db, batchOther.ID, jobOther.ID)

	refs, err := db.GetPendingBatchPRs(testRepo)
	if err != nil {
		t.Fatalf("GetPendingBatchPRs: %v", err)
	}

	prNums := make(map[int]bool)
	for _, r := range refs {
		prNums[r.PRNumber] = true
		assertEq(t, "GithubRepo", r.GithubRepo, testRepo)
	}
	if !prNums[5] || !prNums[7] {
		t.Errorf("expected PRs 5 and 7, got %v", prNums)
	}
	if prNums[9] {
		t.Error("synthesized PR #9 should not appear")
	}
	assertEq(t, "len(refs)", len(refs), 2)
}

func TestCancelClosedPRBatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-cancel-closed")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	// Create a batch with 2 queued jobs for PR #5
	batch := mustCreateCIBatch(t, db, testRepo, 5, "sha5", 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "a..b", "gemini", "review")
	mustRecordBatchJob(t, db, batch.ID, job1.ID)
	mustRecordBatchJob(t, db, batch.ID, job2.ID)

	// Create a synthesized batch for PR #5 (should NOT be canceled)
	doneBatch := mustCreateCIBatch(t, db, testRepo, 5, "sha5-done", 1)
	doneJob := mustEnqueueReviewJob(t, db, repo.ID, "c..d", testAgent, testReview)
	mustRecordBatchJob(t, db, doneBatch.ID, doneJob.ID)
	if _, err := db.ClaimBatchForSynthesis(doneBatch.ID); err != nil {
		t.Fatalf("ClaimBatchForSynthesis: %v", err)
	}

	canceledIDs, err := db.CancelClosedPRBatches(testRepo, 5)
	if err != nil {
		t.Fatalf("CancelClosedPRBatches: %v", err)
	}
	assertEq(t, "len(canceledIDs)", len(canceledIDs), 2)

	// Unsynthesized batch should be deleted
	assertBatchCount(t, db, batch.ID, 0)

	// Jobs should be canceled
	assertJobStatus(t, db, job1.ID, "canceled")

	// Synthesized batch should still exist
	assertBatchCount(t, db, doneBatch.ID, 1)

	// No-op when no pending batches
	canceledIDs, err = db.CancelClosedPRBatches(testRepo, 5)
	if err != nil {
		t.Fatalf("CancelClosedPRBatches no-op: %v", err)
	}
	assertEq(t, "len(canceledIDs)", len(canceledIDs), 0)
}

func TestCancelClosedPRBatches_SkipsClaimedBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-skip-claimed")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	// Create a batch and claim it (mid-synthesis)
	batch := mustCreateCIBatch(t, db, testRepo, 15, "sha15", 1)
	job := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
	mustRecordBatchJob(t, db, batch.ID, job.ID)

	// Mark job done so batch is eligible for synthesis
	if _, err := db.Exec(
		`UPDATE review_jobs SET status='done' WHERE id = ?`, job.ID,
	); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	// Claim the batch (synthesized=0, claimed_at IS NOT NULL)
	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		t.Fatalf("ClaimBatchForSynthesis: %v", err)
	}
	assertEq(t, "claimed", claimed, true)

	// Unclaim to get back to synthesized=0 but claimed_at set
	// Actually, ClaimBatchForSynthesis sets synthesized=1.
	// We need synthesized=0, claimed_at IS NOT NULL to test the
	// race window. Simulate by unclaiming (sets synthesized=0,
	// claimed_at=NULL) then re-claiming.
	// Instead, just set claimed_at directly for the test scenario.
	if _, err := db.Exec(
		`UPDATE ci_pr_batches SET synthesized = 0, claimed_at = datetime('now') WHERE id = ?`,
		batch.ID,
	); err != nil {
		t.Fatalf("set claimed state: %v", err)
	}

	// CancelClosedPRBatches should skip this claimed batch
	canceledIDs, err := db.CancelClosedPRBatches(testRepo, 15)
	if err != nil {
		t.Fatalf("CancelClosedPRBatches: %v", err)
	}
	assertEq(t, "len(canceledIDs)", len(canceledIDs), 0)

	// Batch should still exist
	assertBatchCount(t, db, batch.ID, 1)
}
