package storage

import (
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

func TestMarkBatchSynthesized(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	batch, _ := db.CreateCIBatch("myorg/myrepo", 1, "sha1", 1)
	if batch.Synthesized {
		t.Error("expected Synthesized=false initially")
	}

	if err := db.MarkBatchSynthesized(batch.ID); err != nil {
		t.Fatalf("MarkBatchSynthesized: %v", err)
	}

	// Read back
	var synthesized int
	err := db.QueryRow(`SELECT synthesized FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if synthesized != 1 {
		t.Errorf("got synthesized=%d, want 1", synthesized)
	}
}
