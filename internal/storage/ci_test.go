package storage

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equalf(t, want, got, "assertion failed for %s: got=%v, want=%v", name, got, want)
}

func mustCreateCIBatch(t *testing.T, db *DB, ghRepo string, prNum int, headSHA string, totalJobs int) *CIPRBatch {
	t.Helper()
	batch, _, err := db.CreateCIBatch(ghRepo, prNum, headSHA, totalJobs)
	require.NoError(t, err, "CreateCIBatch: %v")

	return batch
}

func mustEnqueueReviewJob(t *testing.T, db *DB, repoID int64, gitRef, agent, reviewType string) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID: repoID, GitRef: gitRef, Agent: agent, ReviewType: reviewType,
	})
	require.NoError(t, err, "EnqueueJob: %v")

	return job
}

func mustRecordBatchJob(t *testing.T, db *DB, batchID, jobID int64) {
	t.Helper()
	if err := db.RecordBatchJob(batchID, jobID); err != nil {
		require.NoError(t, err, "RecordBatchJob: %v")
	}
}

func mustCreateLinkedBatchJob(t *testing.T, db *DB, repoID int64, ghRepo string, prNum int, headSHA, gitRef, agent, reviewType string) (*CIPRBatch, *ReviewJob) {
	t.Helper()
	batch := mustCreateCIBatch(t, db, ghRepo, prNum, headSHA, 1)
	job := mustEnqueueReviewJob(t, db, repoID, gitRef, agent, reviewType)
	mustRecordBatchJob(t, db, batch.ID, job.ID)
	return batch, job
}

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
		require.Errorf(t, err, "setBatchTimestamp (%s): %v", column, err)
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
	require.NoError(t, err, "setJobStatusAndError: %v")

	rows, err := res.RowsAffected()
	require.NoError(t, err, "Failed to get rows affected: %v")

	if rows != 1 {
		require.Condition(t, func() bool { return false }, "Expected exactly 1 row updated for jobID %d, got %d", jobID, rows)
	}
}

func mustAddReview(t *testing.T, db *DB, jobID int64, agent, output string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, ?, 'test-prompt', ?)`, jobID, agent, output); err != nil {
		require.NoError(t, err, "mustAddReview: %v")
	}
}

func getBatch(t *testing.T, db *DB, id int64) *CIPRBatch {
	t.Helper()
	var b CIPRBatch
	var synthesized int
	err := db.QueryRow(`SELECT id, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE id = ?`, id).Scan(
		&b.ID, &b.TotalJobs, &b.CompletedJobs, &b.FailedJobs, &synthesized,
	)
	require.NoError(t, err, "getBatch: %v")

	b.Synthesized = synthesized != 0
	return &b
}

func TestCreateCIBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, created, err := db.CreateCIBatch(testRepo, 42, "abc123", 4)
	require.NoError(t, err, "CreateCIBatch: %v")

	assertEq(t, "created", created, true)
	assert.NotZero(t, batch.ID, "expected non-zero batch ID")
	assertEq(t, "GithubRepo", batch.GithubRepo, testRepo)
	assertEq(t, "PRNumber", batch.PRNumber, 42)
	assertEq(t, "HeadSHA", batch.HeadSHA, "abc123")
	assertEq(t, "TotalJobs", batch.TotalJobs, 4)
	assertEq(t, "CompletedJobs", batch.CompletedJobs, 0)
	assertEq(t, "FailedJobs", batch.FailedJobs, 0)
	assertEq(t, "Synthesized", batch.Synthesized, false)

	batch2, created2, err := db.CreateCIBatch(testRepo, 42, "abc123", 4)
	require.NoError(t, err, "CreateCIBatch duplicate: %v")

	assertEq(t, "created", created2, false)
	assertEq(t, "ID", batch2.ID, batch.ID)
}

func TestHasCIBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("BeforeCreation", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 1, testSHA)
		require.NoError(t, err, "HasCIBatch: %v")

		assertEq(t, "has", has, false)
	})

	repo, err := db.GetOrCreateRepo("/tmp/test-repo-hasbatch")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch := mustCreateCIBatch(t, db, testRepo, 1, testSHA, 2)

	t.Run("EmptyBatch", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 1, testSHA)
		require.NoError(t, err, "HasCIBatch (empty): %v")

		assertEq(t, "has", has, false)
	})

	t.Run("WithLinkedJob", func(t *testing.T) {
		job := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", "test", testReview)
		mustRecordBatchJob(t, db, batch.ID, job.ID)

		has, err := db.HasCIBatch(testRepo, 1, testSHA)
		require.NoError(t, err, "HasCIBatch: %v")

		assertEq(t, "has", has, true)
	})

	t.Run("DifferentSHA", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 1, "sha2")
		require.NoError(t, err, "HasCIBatch: %v")

		assertEq(t, "has", has, false)
	})
}

func TestRecordBatchJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch := mustCreateCIBatch(t, db, testRepo, 1, testSHA, 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", "gemini", "review")
	mustRecordBatchJob(t, db, batch.ID, job1.ID)
	mustRecordBatchJob(t, db, batch.ID, job2.ID)

	found, err := db.GetCIBatchByJobID(job1.ID)
	require.NoError(t, err, "GetCIBatchByJobID: %v")

	assert.NotNil(t, found, "expected batch by job ID")
	assert.Equalf(t, batch.ID, found.ID, "expected batch ID %d, got %v", batch.ID, found.ID)

	found2, err := db.GetCIBatchByJobID(job2.ID)
	require.NoError(t, err, "GetCIBatchByJobID: %v")

	assert.NotNil(t, found2, "expected batch by job ID")
	assert.Equalf(t, batch.ID, found2.ID, "expected batch ID %d, got %v", batch.ID, found2.ID)
}

func TestIncrementBatchCompleted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, _, err := db.CreateCIBatch(testRepo, 1, testSHA, 3)
	require.NoError(t, err, "CreateCIBatch: %v")

	updated, err := db.IncrementBatchCompleted(batch.ID)
	require.NoError(t, err, "IncrementBatchCompleted: %v")
	assert.Equal(t, 1, updated.CompletedJobs, "got CompletedJobs=%d, want 1", updated.CompletedJobs)

	updated, err = db.IncrementBatchCompleted(batch.ID)
	require.NoError(t, err, "IncrementBatchCompleted: %v")
	assert.Equal(t, 2, updated.CompletedJobs, "got CompletedJobs=%d, want 2", updated.CompletedJobs)

}

func TestIncrementBatchFailed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, _, err := db.CreateCIBatch(testRepo, 1, testSHA, 3)
	require.NoError(t, err, "CreateCIBatch: %v")

	updated, err := db.IncrementBatchFailed(batch.ID)
	require.NoError(t, err, "IncrementBatchFailed: %v")
	assert.Equal(t, 1, updated.FailedJobs, "got FailedJobs=%d, want 1", updated.FailedJobs)

	updated, err = db.IncrementBatchCompleted(batch.ID)
	require.NoError(t, err, "IncrementBatchCompleted: %v")

	assert.Equal(t, 1, updated.CompletedJobs, "got CompletedJobs=%d, FailedJobs=%d, want 1, 1", updated.CompletedJobs, updated.FailedJobs)
	assert.Equal(t, 1, updated.FailedJobs, "got CompletedJobs=%d, FailedJobs=%d, want 1, 1", updated.CompletedJobs, updated.FailedJobs)
}

func TestIncrementBatchConcurrent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	n := 10
	batch, _, err := db.CreateCIBatch(testRepo, 1, testSHA, n)
	require.NoError(t, err, "CreateCIBatch: %v")

	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.IncrementBatchCompleted(batch.ID)
			assert.NoError(t, err, "IncrementBatchCompleted")
		}()
	}
	wg.Wait()

	finalBatch := getBatch(t, db, batch.ID)
	assert.Equal(t, n, finalBatch.CompletedJobs, "got CompletedJobs=%d, want %d", finalBatch.CompletedJobs, n)

}

func TestGetBatchReviews(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch := mustCreateCIBatch(t, db, testRepo, 1, testSHA, 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "abc..def", "gemini", "review")
	mustRecordBatchJob(t, db, batch.ID, job1.ID)
	mustRecordBatchJob(t, db, batch.ID, job2.ID)

	setJobStatus(t, db, job1.ID, JobStatusDone)
	mustAddReview(t, db, job1.ID, testAgent, "finding1")

	setJobStatusAndError(t, db, job2.ID, "failed", "timeout")

	reviews, err := db.GetBatchReviews(batch.ID)
	require.NoError(t, err, "GetBatchReviews: %v")

	if len(reviews) != 2 {
		assert.Len(t, reviews, 2, "got %d reviews, want 2", len(reviews))
	}

	assertEq(t, "review[0].Agent", reviews[0].Agent, testAgent)
	assertEq(t, "review[0].ReviewType", reviews[0].ReviewType, testReview)
	assertEq(t, "review[0].Output", reviews[0].Output, "finding1")
	assertEq(t, "review[0].Status", reviews[0].Status, "done")

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
	require.NoError(t, err, "GetCIBatchByJobID: %v")
	require.NotNil(t, found, "expected non-nil batch")

	assert.Equal(t, batch.ID, found.ID, "got batch ID %d, want %d", found.ID, batch.ID)

	notFound, err := db.GetCIBatchByJobID(99999)
	require.NoError(t, err, "GetCIBatchByJobID: %v")
	assert.Nil(t, notFound, "expected nil for unknown job ID")

}

func TestClaimBatchForSynthesis(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, _, _ := db.CreateCIBatch(testRepo, 1, testSHA, 1)
	assert.False(t, batch.Synthesized, "expected Synthesized=false initially")

	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	require.NoError(t, err, "ClaimBatchForSynthesis: %v")

	assert.True(t, claimed, "expected first claim to succeed")

	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	require.NoError(t, err, "ClaimBatchForSynthesis (second): %v")

	assert.False(t, claimed, "expected second claim to fail")

	if err := db.UnclaimBatch(batch.ID); err != nil {
		require.NoError(t, err, "UnclaimBatch: %v")
	}
	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	require.NoError(t, err, "ClaimBatchForSynthesis (after unclaim): %v")

	assert.True(t, claimed, "expected claim after unclaim to succeed")

}

func TestFinalizeBatch_PreventsStaleRepost(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch, _ := mustCreateLinkedTerminalJob(t, db, repo.ID, testRepo, 1, testSHA, testSHA, testAgent, testReview, "done")

	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	require.NoError(t, err)
	require.True(t, claimed, "expected claim to succeed")

	var claimedBefore sql.NullString
	if err := db.QueryRow(`SELECT claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&claimedBefore); err != nil {
		require.NoError(t, err, "scan claimed_at before finalize: %v")
	}
	require.True(t, claimedBefore.Valid, "expected claimed_at to be set after claim")

	if err := db.FinalizeBatch(batch.ID); err != nil {
		require.NoError(t, err, "FinalizeBatch: %v")
	}

	var claimedAfter sql.NullString
	if err := db.QueryRow(`SELECT claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&claimedAfter); err != nil {
		require.NoError(t, err, "scan claimed_at after finalize: %v")
	}
	assert.False(t, claimedAfter.Valid, "expected claimed_at to be NULL after finalize, got %q", claimedAfter.String)

	var synthesized int
	if err := db.QueryRow(`SELECT synthesized FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized); err != nil {
		require.NoError(t, err, "scan synthesized after finalize: %v")
	}
	require.Equal(t, 1, synthesized, "expected synthesized=1 after finalize, got %d", synthesized)

	stale, err := db.GetStaleBatches()
	require.NoError(t, err, "GetStaleBatches: %v")

	for _, b := range stale {
		if b.ID == batch.ID {
			assert.NotEqual(t, batch.ID, b.ID, "finalized batch should not appear in stale batches")
		}
	}

	claimed, err = db.ClaimBatchForSynthesis(batch.ID)
	require.NoError(t, err)
	assert.False(t, claimed, "should not be able to re-claim a finalized batch")

}

func TestGetStaleBatches_StaleClaim(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch, _ := mustCreateLinkedTerminalJob(t, db, repo.ID, testRepo, 1, testSHA, testSHA, testAgent, testReview, "done")

	_, _ = db.ClaimBatchForSynthesis(batch.ID)
	setBatchClaimedAt(t, db, batch.ID, -10*time.Minute)

	stale, err := db.GetStaleBatches()
	require.NoError(t, err, "GetStaleBatches: %v")

	found := false
	for _, b := range stale {
		if b.ID == batch.ID {
			found = true
		}
	}
	assert.True(t, found, "stale claimed batch should appear in GetStaleBatches")

}

func TestDeleteEmptyBatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	emptyOld := mustCreateCIBatch(t, db, testRepo, 1, "sha-old", 2)
	setBatchCreatedAt(t, db, emptyOld.ID, -5*time.Minute)

	mustCreateCIBatch(t, db, testRepo, 2, "sha-recent", 1)

	repo, err := db.GetOrCreateRepo(t.TempDir())
	require.NoError(t, err, "GetOrCreateRepo: %v")

	nonEmpty, _ := mustCreateLinkedBatchJob(t, db, repo.ID, testRepo, 3, "sha-nonempty", "a..b", testAgent, testReview)
	setBatchCreatedAt(t, db, nonEmpty.ID, -5*time.Minute)

	n, err := db.DeleteEmptyBatches()
	require.NoError(t, err, "DeleteEmptyBatches: %v")

	assertEq(t, "deleted count", n, 1)

	has, err := db.HasCIBatch(testRepo, 1, "sha-old")
	require.NoError(t, err, "HasCIBatch (old empty): %v")

	assertEq(t, "has", has, false)

	var recentCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_batches WHERE github_repo = ? AND pr_number = ? AND head_sha = ?`,
		testRepo, 2, "sha-recent").Scan(&recentCount); err != nil {
		require.NoError(t, err, "count recent batch: %v")
	}
	assertEq(t, "recentCount", recentCount, 1)

	has, err = db.HasCIBatch(testRepo, 3, "sha-nonempty")
	require.NoError(t, err, "HasCIBatch (non-empty): %v")

	assertEq(t, "has", has, true)
}

func TestCancelJob_ReturnsErrNoRowsForTerminalJobs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-cancel")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	t.Run("TerminalJob_ReturnsErrNoRows", func(t *testing.T) {
		job := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
		setJobStatus(t, db, job.ID, JobStatusDone)

		err := db.CancelJob(job.ID)
		require.ErrorIs(t, err, sql.ErrNoRows, "expected sql.ErrNoRows, got: %v", err)

	})

	t.Run("QueuedJob_Succeeds", func(t *testing.T) {
		job := mustEnqueueReviewJob(t, db, repo.ID, "c..d", testAgent, testReview)
		if err := db.CancelJob(job.ID); err != nil {
			require.NoError(t, err, "CancelJob on queued job: %v")
		}

		var status string
		if err := db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
			require.NoError(t, err, "query status: %v")
		}
		assertEq(t, "status", status, "canceled")
	})
}

func TestCancelSupersededBatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-supersede")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	oldBatch := mustCreateCIBatch(t, db, "owner/repo", 1, "oldsha", 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "base..oldsha", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "base..oldsha", testAgent, "review")
	if err := db.RecordBatchJob(oldBatch.ID, job1.ID); err != nil {
		require.NoError(t, err, "RecordBatchJob: %v")
	}
	if err := db.RecordBatchJob(oldBatch.ID, job2.ID); err != nil {
		require.NoError(t, err, "RecordBatchJob: %v")
	}

	doneBatch := mustCreateCIBatch(t, db, "owner/repo", 1, "donesha", 1)
	doneJob := mustEnqueueReviewJob(t, db, repo.ID, "base..donesha", testAgent, testReview)
	if err := db.RecordBatchJob(doneBatch.ID, doneJob.ID); err != nil {
		require.NoError(t, err, "RecordBatchJob: %v")
	}
	if _, err := db.ClaimBatchForSynthesis(doneBatch.ID); err != nil {
		require.NoError(t, err, "ClaimBatchForSynthesis: %v")
	}

	canceledIDs, err := db.CancelSupersededBatches("owner/repo", 1, "newsha")
	require.NoError(t, err, "CancelSupersededBatches: %v")

	if len(canceledIDs) != 2 {
		assert.Len(t, canceledIDs, 2, "len(canceledIDs) = %d, want 2", len(canceledIDs))
	}

	has, err := db.HasCIBatch("owner/repo", 1, "oldsha")
	require.NoError(t, err, "HasCIBatch: %v")

	assert.False(t, has, "old batch should have been deleted")

	var status string
	if err := db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, job1.ID).Scan(&status); err != nil {
		require.NoError(t, err, "query status: %v")
	}
	assert.Equal(t, "canceled", status, "job1 status = %q, want canceled", status)

	has, err = db.HasCIBatch("owner/repo", 1, "donesha")
	require.NoError(t, err, "HasCIBatch done: %v")

	assert.True(t, has, "synthesized batch should NOT have been canceled")

	canceledIDs, err = db.CancelSupersededBatches("owner/repo", 1, "newsha")
	require.NoError(t, err, "CancelSupersededBatches no-op: %v")

	if len(canceledIDs) != 0 {
		assert.Empty(t, canceledIDs, "expected 0 canceled on no-op, got %d", len(canceledIDs))
	}
}

func TestLatestBatchTimeForPR(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("no batches returns zero time", func(t *testing.T) {
		ts, err := db.LatestBatchTimeForPR(testRepo, 99)
		require.NoError(t, err, "LatestBatchTimeForPR: %v")
		assert.True(t, ts.IsZero(), "expected zero time, got %v", ts)

	})

	t.Run("returns latest batch time", func(t *testing.T) {
		before := time.Now().UTC().Truncate(time.Second)

		repo, err := db.GetOrCreateRepo("/tmp/test-throttle")
		require.NoError(t, err, "GetOrCreateRepo: %v")

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
		require.NoError(t, err, "LatestBatchTimeForPR: %v")

		if ts.IsZero() {
			require.False(t, ts.IsZero(), "expected non-zero time")
		}

		if ts.Before(before.Add(-1 * time.Second)) {
			assert.False(t, ts.Before(before.Add(-1*time.Second)), "expected time >= %v, got %v", before, ts)
		}
	})

	t.Run("different PR returns zero", func(t *testing.T) {
		ts, err := db.LatestBatchTimeForPR(testRepo, 999)
		require.NoError(t, err, "LatestBatchTimeForPR: %v")
		assert.True(t, ts.IsZero(), "expected zero time for different PR, got %v", ts)

	})
}

func TestGetPendingBatchPRs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-pending")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch5 := mustCreateCIBatch(t, db, testRepo, 5, "sha5", 1)
	job5 := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
	mustRecordBatchJob(t, db, batch5.ID, job5.ID)

	batch7 := mustCreateCIBatch(t, db, testRepo, 7, "sha7", 1)
	job7 := mustEnqueueReviewJob(t, db, repo.ID, "c..d", testAgent, testReview)
	mustRecordBatchJob(t, db, batch7.ID, job7.ID)

	batch9 := mustCreateCIBatch(t, db, testRepo, 9, "sha9", 1)
	job9 := mustEnqueueReviewJob(t, db, repo.ID, "e..f", testAgent, testReview)
	mustRecordBatchJob(t, db, batch9.ID, job9.ID)
	if _, err := db.ClaimBatchForSynthesis(batch9.ID); err != nil {
		require.NoError(t, err, "ClaimBatchForSynthesis: %v")
	}

	batchOther := mustCreateCIBatch(t, db, "other/repo", 5, "sha-other", 1)
	jobOther := mustEnqueueReviewJob(t, db, repo.ID, "g..h", testAgent, testReview)
	mustRecordBatchJob(t, db, batchOther.ID, jobOther.ID)

	refs, err := db.GetPendingBatchPRs(testRepo)
	require.NoError(t, err, "GetPendingBatchPRs: %v")

	prNums := make(map[int]bool)
	for _, r := range refs {
		prNums[r.PRNumber] = true
		assertEq(t, "GithubRepo", r.GithubRepo, testRepo)
	}
	if !prNums[5] || !prNums[7] {
		assert.True(t, prNums[5] && prNums[7], "expected PRs 5 and 7, got %v", prNums)
	}
	assert.False(t, prNums[9], "synthesized PR #9 should not appear")

	if len(refs) != 2 {
		assert.Len(t, refs, 2, "expected 2 refs, got %d", len(refs))
	}
}

func TestCancelClosedPRBatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-cancel-closed")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch := mustCreateCIBatch(t, db, testRepo, 5, "sha5", 2)
	job1 := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
	job2 := mustEnqueueReviewJob(t, db, repo.ID, "a..b", "gemini", "review")
	mustRecordBatchJob(t, db, batch.ID, job1.ID)
	mustRecordBatchJob(t, db, batch.ID, job2.ID)

	doneBatch := mustCreateCIBatch(t, db, testRepo, 5, "sha5-done", 1)
	doneJob := mustEnqueueReviewJob(t, db, repo.ID, "c..d", testAgent, testReview)
	mustRecordBatchJob(t, db, doneBatch.ID, doneJob.ID)
	if _, err := db.ClaimBatchForSynthesis(doneBatch.ID); err != nil {
		require.NoError(t, err, "ClaimBatchForSynthesis: %v")
	}

	canceledIDs, err := db.CancelClosedPRBatches(testRepo, 5)
	require.NoError(t, err, "CancelClosedPRBatches: %v")

	if len(canceledIDs) != 2 {
		assert.Len(t, canceledIDs, 2, "expected 2 canceled jobs, got %d", len(canceledIDs))
	}

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM ci_pr_batches WHERE id = ?`,
		batch.ID,
	).Scan(&count); err != nil {
		require.NoError(t, err, "count deleted batch: %v")
	}
	assertEq(t, "deleted batch count", count, 0)

	var status string
	if err := db.QueryRow(
		`SELECT status FROM review_jobs WHERE id = ?`, job1.ID,
	).Scan(&status); err != nil {
		require.NoError(t, err, "query job1 status: %v")
	}
	assertEq(t, "job1 status", status, "canceled")

	var doneCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM ci_pr_batches WHERE id = ?`,
		doneBatch.ID,
	).Scan(&doneCount); err != nil {
		require.NoError(t, err, "count done batch: %v")
	}
	assertEq(t, "done batch count", doneCount, 1)

	canceledIDs, err = db.CancelClosedPRBatches(testRepo, 5)
	require.NoError(t, err, "CancelClosedPRBatches no-op: %v")

	assert.Empty(t, canceledIDs, "unexpected condition")
}

func TestCancelClosedPRBatches_SkipsClaimedBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-skip-claimed")
	require.NoError(t, err, "GetOrCreateRepo: %v")

	batch := mustCreateCIBatch(t, db, testRepo, 15, "sha15", 1)
	job := mustEnqueueReviewJob(t, db, repo.ID, "a..b", testAgent, testReview)
	mustRecordBatchJob(t, db, batch.ID, job.ID)

	if _, err := db.Exec(
		`UPDATE review_jobs SET status='done' WHERE id = ?`, job.ID,
	); err != nil {
		require.NoError(t, err, "mark done: %v")
	}

	claimed, err := db.ClaimBatchForSynthesis(batch.ID)
	require.NoError(t, err, "ClaimBatchForSynthesis: %v")

	require.True(t, claimed, "expected successful claim")

	if _, err := db.Exec(
		`UPDATE ci_pr_batches SET synthesized = 0, claimed_at = datetime('now') WHERE id = ?`,
		batch.ID,
	); err != nil {
		require.NoError(t, err, "set claimed state: %v")
	}

	canceledIDs, err := db.CancelClosedPRBatches(testRepo, 15)
	require.NoError(t, err, "CancelClosedPRBatches: %v")

	assert.Empty(t, canceledIDs, "unexpected condition")

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM ci_pr_batches WHERE id = ?`,
		batch.ID,
	).Scan(&count); err != nil {
		require.NoError(t, err, "count batch: %v")
	}
	assertEq(t, "claimed batch should survive", count, 1)
}
