package storage

import (
	"database/sql"
	"sync"
	"testing"
)

func TestCreateCIBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, err := db.CreateCIBatch("myorg/myrepo", 42, "abc123", 4)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	if batch.ID == 0 {
		t.Error("expected non-zero batch ID")
	}
	if batch.GithubRepo != "myorg/myrepo" {
		t.Errorf("got GithubRepo=%q, want %q", batch.GithubRepo, "myorg/myrepo")
	}
	if batch.PRNumber != 42 {
		t.Errorf("got PRNumber=%d, want 42", batch.PRNumber)
	}
	if batch.HeadSHA != "abc123" {
		t.Errorf("got HeadSHA=%q, want %q", batch.HeadSHA, "abc123")
	}
	if batch.TotalJobs != 4 {
		t.Errorf("got TotalJobs=%d, want 4", batch.TotalJobs)
	}
	if batch.CompletedJobs != 0 || batch.FailedJobs != 0 {
		t.Errorf("got CompletedJobs=%d, FailedJobs=%d, want 0, 0", batch.CompletedJobs, batch.FailedJobs)
	}
	if batch.Synthesized {
		t.Error("expected Synthesized=false")
	}

	// Duplicate insert should return the same batch (INSERT OR IGNORE)
	batch2, err := db.CreateCIBatch("myorg/myrepo", 42, "abc123", 4)
	if err != nil {
		t.Fatalf("CreateCIBatch duplicate: %v", err)
	}
	if batch2.ID != batch.ID {
		t.Errorf("duplicate CreateCIBatch returned different ID: %d vs %d", batch2.ID, batch.ID)
	}
}

func TestHasCIBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	has, err := db.HasCIBatch("myorg/myrepo", 1, "sha1")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	if has {
		t.Error("expected false before creation")
	}

	_, err = db.CreateCIBatch("myorg/myrepo", 1, "sha1", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	has, err = db.HasCIBatch("myorg/myrepo", 1, "sha1")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	if !has {
		t.Error("expected true after creation")
	}

	// Different SHA should be false
	has, err = db.HasCIBatch("myorg/myrepo", 1, "sha2")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	if has {
		t.Error("expected false for different SHA")
	}
}

func TestRecordBatchJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	batch, err := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	job1, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc..def", Agent: "codex", ReviewType: "security"})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	job2, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc..def", Agent: "gemini", ReviewType: "review"})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	if err := db.RecordBatchJob(batch.ID, job1.ID); err != nil {
		t.Fatalf("RecordBatchJob 1: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job2.ID); err != nil {
		t.Fatalf("RecordBatchJob 2: %v", err)
	}

	// Verify via GetCIBatchByJobID
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

	batch, err := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 3)
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

	batch, err := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 3)
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
	batch, err := db.CreateCIBatch("myorg/myrepo", 1, "sha1", n)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
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
	final, err := db.GetCIBatchByJobID(0)
	// Can't use GetCIBatchByJobID with 0, read directly
	var finalBatch CIPRBatch
	var synthesized int
	err = db.QueryRow(`SELECT id, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE id = ?`,
		batch.ID).Scan(&finalBatch.ID, &finalBatch.TotalJobs, &finalBatch.CompletedJobs, &finalBatch.FailedJobs, &synthesized)
	if err != nil {
		t.Fatalf("query final batch: %v", err)
	}
	_ = final
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

	batch, err := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	// Create and link jobs
	job1, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc..def", Agent: "codex", ReviewType: "security"})
	job2, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc..def", Agent: "gemini", ReviewType: "review"})
	db.RecordBatchJob(batch.ID, job1.ID)
	db.RecordBatchJob(batch.ID, job2.ID)

	// Complete job1 with a review
	db.Exec(`UPDATE review_jobs SET status = 'done' WHERE id = ?`, job1.ID)
	db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, 'codex', 'test', 'finding1')`, job1.ID)

	// Fail job2
	db.Exec(`UPDATE review_jobs SET status = 'failed', error = 'timeout' WHERE id = ?`, job2.ID)

	reviews, err := db.GetBatchReviews(batch.ID)
	if err != nil {
		t.Fatalf("GetBatchReviews: %v", err)
	}
	if len(reviews) != 2 {
		t.Fatalf("got %d reviews, want 2", len(reviews))
	}

	// First review should be job1 (codex/security)
	if reviews[0].Agent != "codex" || reviews[0].ReviewType != "security" {
		t.Errorf("review 0: got agent=%s, type=%s", reviews[0].Agent, reviews[0].ReviewType)
	}
	if reviews[0].Output != "finding1" {
		t.Errorf("review 0: got output=%q, want %q", reviews[0].Output, "finding1")
	}
	if reviews[0].Status != "done" {
		t.Errorf("review 0: got status=%q, want %q", reviews[0].Status, "done")
	}

	// Second review should be job2 (gemini/review)
	if reviews[1].Agent != "gemini" || reviews[1].ReviewType != "review" {
		t.Errorf("review 1: got agent=%s, type=%s", reviews[1].Agent, reviews[1].ReviewType)
	}
	if reviews[1].Status != "failed" {
		t.Errorf("review 1: got status=%q, want %q", reviews[1].Status, "failed")
	}
	if reviews[1].Error != "timeout" {
		t.Errorf("review 1: got error=%q, want %q", reviews[1].Error, "timeout")
	}
}

func TestGetCIBatchByJobID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	batch, _ := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 1)
	job, _ := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc..def", Agent: "codex", ReviewType: "security"})
	db.RecordBatchJob(batch.ID, job.ID)

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

	batch, _ := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 1)
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

	batch, _ := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 1)

	// Create a linked job so the batch qualifies for GetStaleBatches
	_, err := db.Exec(`INSERT INTO repos (root_path, name) VALUES ('/tmp/test', 'test')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, prompt, status, agent, review_type) VALUES (1, 'sha1', 'p', 'done', 'test', 'security')`)
	if err != nil {
		t.Fatal(err)
	}
	var jobID int64
	db.QueryRow(`SELECT last_insert_rowid()`).Scan(&jobID)
	db.RecordBatchJob(batch.ID, jobID)

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
	db.QueryRow(`SELECT claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&claimedBefore)
	if !claimedBefore.Valid {
		t.Fatal("expected claimed_at to be set after claim")
	}

	// Finalize after successful post
	if err := db.FinalizeBatch(batch.ID); err != nil {
		t.Fatalf("FinalizeBatch: %v", err)
	}

	// Verify claimed_at is cleared after finalize
	var claimedAfter sql.NullString
	db.QueryRow(`SELECT claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&claimedAfter)
	if claimedAfter.Valid {
		t.Fatalf("expected claimed_at to be NULL after finalize, got %q", claimedAfter.String)
	}

	// Verify synthesized is still 1
	var synthesized int
	db.QueryRow(`SELECT synthesized FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized)
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

	batch, _ := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 1)

	// Create a linked terminal job
	_, err := db.Exec(`INSERT INTO repos (root_path, name) VALUES ('/tmp/test', 'test')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO review_jobs (repo_id, git_ref, prompt, status, agent, review_type) VALUES (1, 'sha1', 'p', 'done', 'test', 'security')`)
	if err != nil {
		t.Fatal(err)
	}
	var jobID int64
	db.QueryRow(`SELECT last_insert_rowid()`).Scan(&jobID)
	db.RecordBatchJob(batch.ID, jobID)

	// Claim the batch, then backdate claimed_at to simulate a stale claim
	db.ClaimBatchForSynthesis(batch.ID)
	_, err = db.Exec(`UPDATE ci_pr_batches SET claimed_at = datetime('now', '-10 minutes') WHERE id = ?`, batch.ID)
	if err != nil {
		t.Fatal(err)
	}

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
