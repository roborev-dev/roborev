import re

with open('internal/storage/ci_test.go', 'r') as f:
    content = f.read()

# 1. Add assertEq
content = re.sub(
    r'(const \(\n.*?testReview = "security"\n\))',
    r'\1\n\nfunc assertEq[T comparable](t *testing.T, name string, got, want T) {\n\tt.Helper()\n\tif got != want {\n\t\tt.Errorf("got %s=%v, want %v", name, got, want)\n\t}\n}',
    content,
    flags=re.DOTALL
)

# 2. Consolidate setBatchCreatedAt / setBatchClaimedAt
content = re.sub(
    r'func setBatchCreatedAt\(t \*testing\.T, db \*DB, batchID int64, offset time\.Duration\) \{\n\t.*?\}\n\nfunc setBatchClaimedAt\(t \*testing\.T, db \*DB, batchID int64, offset time\.Duration\) \{\n\t.*?\}',
    r'''func setBatchTimestamp(t *testing.T, db *DB, batchID int64, column string, offset time.Duration) {
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
}''',
    content,
    flags=re.DOTALL
)

# 3. Replace TestCreateCIBatch
test_create_ci_batch = """func TestCreateCIBatch(t *testing.T) {
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
}"""
content = re.sub(
    r'func TestCreateCIBatch\(t \*testing\.T\) \{.*?(?=\nfunc TestHasCIBatch)',
    test_create_ci_batch,
    content,
    flags=re.DOTALL
)

# 4. Replace TestGetBatchReviews block
get_batch_reviews_old = """\t// First review should be job1 (codex/security)
	if reviews[0].Agent != testAgent || reviews[0].ReviewType != testReview {
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
	}"""
get_batch_reviews_new = """\t// First review should be job1 (codex/security)
	assertEq(t, "review[0].Agent", reviews[0].Agent, testAgent)
	assertEq(t, "review[0].ReviewType", reviews[0].ReviewType, testReview)
	assertEq(t, "review[0].Output", reviews[0].Output, "finding1")
	assertEq(t, "review[0].Status", reviews[0].Status, "done")

	// Second review should be job2 (gemini/review)
	assertEq(t, "review[1].Agent", reviews[1].Agent, "gemini")
	assertEq(t, "review[1].ReviewType", reviews[1].ReviewType, "review")
	assertEq(t, "review[1].Status", reviews[1].Status, "failed")
	assertEq(t, "review[1].Error", reviews[1].Error, "timeout")"""
content = content.replace(get_batch_reviews_old, get_batch_reviews_new)

# 5. Replace TestHasCIBatch
test_has_ci_batch_new = """func TestHasCIBatch(t *testing.T) {
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
}"""
content = re.sub(
    r'func TestHasCIBatch\(t \*testing\.T\) \{.*?(?=\nfunc TestRecordBatchJob)',
    test_has_ci_batch_new,
    content,
    flags=re.DOTALL
)

# 6. Replace TestCancelJob_ReturnsErrNoRowsForTerminalJobs
test_cancel_job_new = """func TestCancelJob(t *testing.T) {
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
}"""
content = re.sub(
    r'func TestCancelJob_ReturnsErrNoRowsForTerminalJobs\(t \*testing\.T\) \{.*?(?=\nfunc TestCancelSupersededBatches)',
    test_cancel_job_new,
    content,
    flags=re.DOTALL
)

# 7. Replace TestDeleteEmptyBatches
test_delete_empty_batches_new = """func TestDeleteEmptyBatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("SetupAndExecute", func(t *testing.T) {
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
		assertEq(t, "deleted count", n, int64(1))
	})

	t.Run("OldEmptyBatch_Deleted", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 1, "sha-old")
		if err != nil {
			t.Fatalf("HasCIBatch (old empty): %v", err)
		}
		assertEq(t, "has", has, false)
	})

	t.Run("RecentEmptyBatch_NotDeleted", func(t *testing.T) {
		var recentCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_batches WHERE github_repo = ? AND pr_number = ? AND head_sha = ?`,
			testRepo, 2, "sha-recent").Scan(&recentCount); err != nil {
			t.Fatalf("count recent batch: %v", err)
		}
		assertEq(t, "recentCount", recentCount, 1)
	})

	t.Run("NonEmptyBatch_NotDeleted", func(t *testing.T) {
		has, err := db.HasCIBatch(testRepo, 3, "sha-nonempty")
		if err != nil {
			t.Fatalf("HasCIBatch (non-empty): %v", err)
		}
		assertEq(t, "has", has, true)
	})
}"""
content = re.sub(
    r'func TestDeleteEmptyBatches\(t \*testing\.T\) \{.*?(?=\nfunc TestCancelJob_ReturnsErrNoRowsForTerminalJobs|\nfunc TestCancelJob)',
    test_delete_empty_batches_new,
    content,
    flags=re.DOTALL
)

with open('internal/storage/ci_test.go', 'w') as f:
    f.write(content)
