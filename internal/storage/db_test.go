package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// templateDB holds a pre-migrated SQLite database file. Tests copy this
// file instead of re-running the full migration chain on every call to
// openTestDB, which is the dominant cost on macOS ARM64 with -race.
var (
	templateOnce sync.Once
	templatePath string
	templateErr  error
)

func getTemplatePath() (string, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "roborev-test-template-*")
		if err != nil {
			templateErr = err
			return
		}
		p := filepath.Join(dir, "template.db")
		db, err := Open(p)
		if err != nil {
			templateErr = err
			return
		}
		db.Close()
		templatePath = p
	})
	return templatePath, templateErr
}

func TestOpenAndClose(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestRepoOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repo
	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	if repo.ID == 0 {
		t.Error("Repo ID should not be 0")
	}
	if repo.Name != "test-repo" {
		t.Errorf("Expected name 'test-repo', got '%s'", repo.Name)
	}

	// Get same repo again (should return existing)
	repo2, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo (second call) failed: %v", err)
	}
	if repo2.ID != repo.ID {
		t.Error("Should return same repo on second call")
	}
}

func TestCommitOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Create commit
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123def456", "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	if commit.ID == 0 {
		t.Error("Commit ID should not be 0")
	}
	if commit.SHA != "abc123def456" {
		t.Errorf("Expected SHA 'abc123def456', got '%s'", commit.SHA)
	}

	// Get by SHA
	found, err := db.GetCommitBySHA("abc123def456")
	if err != nil {
		t.Fatalf("GetCommitBySHA failed: %v", err)
	}
	if found.ID != commit.ID {
		t.Error("GetCommitBySHA returned wrong commit")
	}
}

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

func TestBranchPersistence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/branch-test-repo")
	commit := createCommit(t, db, repo.ID, "branch123")

	t.Run("EnqueueJob stores branch", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch123", Branch: "feature/test-branch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		if job.Branch != "feature/test-branch" {
			t.Errorf("Expected branch 'feature/test-branch', got '%s'", job.Branch)
		}
	})

	t.Run("GetJobByID returns branch", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch456", Branch: "main", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		fetched, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetched.Branch != "main" {
			t.Errorf("GetJobByID: expected branch 'main', got '%s'", fetched.Branch)
		}
	})

	t.Run("ListJobs returns branch", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch789", Branch: "develop", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		jobs, err := db.ListJobs("", "", 100, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		var found bool
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				if j.Branch != "develop" {
					t.Errorf("ListJobs: expected branch 'develop', got '%s'", j.Branch)
				}
				break
			}
		}
		if !found {
			t.Error("ListJobs did not return the job")
		}
	})

	t.Run("ClaimJob returns branch", func(t *testing.T) {
		// Drain existing jobs
		for {
			j, _ := db.ClaimJob("drain")
			if j == nil {
				break
			}
			db.CompleteJob(j.ID, "codex", "p", "o")
		}

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branchclaim", Branch: "release/v1", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		claimed, err := db.ClaimJob("test-worker")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		if claimed == nil || claimed.ID != job.ID {
			t.Fatal("ClaimJob did not return the expected job")
		}
		if claimed.Branch != "release/v1" {
			t.Errorf("ClaimJob: expected branch 'release/v1', got '%s'", claimed.Branch)
		}
	})

	t.Run("empty branch is allowed", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "nobranch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob with empty branch failed: %v", err)
		}
		if job.Branch != "" {
			t.Errorf("Expected empty branch, got '%s'", job.Branch)
		}
	})

	t.Run("UpdateJobBranch backfills empty branch", func(t *testing.T) {
		// Create job with empty branch
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "updatebranch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		if job.Branch != "" {
			t.Fatalf("Expected empty branch initially, got '%s'", job.Branch)
		}

		// Update the branch
		rowsAffected, err := db.UpdateJobBranch(job.ID, "feature/backfilled")
		if err != nil {
			t.Fatalf("UpdateJobBranch failed: %v", err)
		}
		if rowsAffected != 1 {
			t.Errorf("Expected 1 row affected, got %d", rowsAffected)
		}

		// Verify the branch was updated
		fetched, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetched.Branch != "feature/backfilled" {
			t.Errorf("Expected branch 'feature/backfilled', got '%s'", fetched.Branch)
		}
	})

	t.Run("UpdateJobBranch does not overwrite existing branch", func(t *testing.T) {
		// Create job with existing branch
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "nooverwrite", Branch: "original-branch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}

		// Try to update - should not change existing branch
		rowsAffected, err := db.UpdateJobBranch(job.ID, "new-branch")
		if err != nil {
			t.Fatalf("UpdateJobBranch failed: %v", err)
		}
		if rowsAffected != 0 {
			t.Errorf("Expected 0 rows affected (branch already set), got %d", rowsAffected)
		}

		// Verify the branch was NOT changed
		fetched, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetched.Branch != "original-branch" {
			t.Errorf("Expected branch 'original-branch' (unchanged), got '%s'", fetched.Branch)
		}
	})
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

func TestJobCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Create 3 jobs that will stay queued
	for i := range 3 {
		sha := fmt.Sprintf("queued%d", i)
		commit := createCommit(t, db, repo.ID, sha)
		enqueueJob(t, db, repo.ID, commit.ID, sha)
	}

	// Create a job, claim it, and complete it
	commit := createCommit(t, db, repo.ID, "done1")
	job := enqueueJob(t, db, repo.ID, commit.ID, "done1")
	_, _ = db.ClaimJob("drain1")    // Claims oldest queued job (one of queued0-2)
	_, _ = db.ClaimJob("drain2")    // Claims next
	_, _ = db.ClaimJob("drain3")    // Claims next
	claimed, _ := db.ClaimJob("w1") // Should claim "done1" job now
	if claimed != nil {
		if claimed.ID != job.ID {
			t.Errorf("Expected to claim job 'done1' (ID %d), got %d", job.ID, claimed.ID)
		}
		db.CompleteJob(claimed.ID, "codex", "p", "o")
	}

	// Create a job, claim it, and fail it
	commit2 := createCommit(t, db, repo.ID, "fail1")
	enqueueJob(t, db, repo.ID, commit2.ID, "fail1")
	claimed2, _ := db.ClaimJob("w2")
	if claimed2 != nil {
		db.FailJob(claimed2.ID, "", "err")
	}

	queued, running, done, failed, _, err := db.GetJobCounts()
	if err != nil {
		t.Fatalf("GetJobCounts failed: %v", err)
	}

	// We expect: 0 queued (all were claimed), 1 done, 1 failed, 3 running
	if queued != 0 {
		t.Errorf("Expected 0 queued jobs, got %d", queued)
	}
	if running != 3 {
		t.Errorf("Expected 3 running jobs, got %d", running)
	}
	if done != 1 {
		t.Errorf("Expected 1 done, got %d", done)
	}
	if failed != 1 {
		t.Errorf("Expected 1 failed, got %d", failed)
	}
}

func TestCountStalledJobs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Create a job and claim it (makes it running with current timestamp)
	commit1 := createCommit(t, db, repo.ID, "recent1")
	enqueueJob(t, db, repo.ID, commit1.ID, "recent1")
	_, _ = db.ClaimJob("worker-1")

	// No stalled jobs yet (just started)
	count, err := db.CountStalledJobs(30 * time.Minute)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 stalled jobs for recently started job, got %d", count)
	}

	// Create a job and manually set started_at to 1 hour ago (simulating stalled job)
	// Use UTC format (ends with Z) to test basic case
	commit2 := createCommit(t, db, repo.ID, "stalled1")
	job2 := enqueueJob(t, db, repo.ID, commit2.ID, "stalled1")
	backdateJobStart(t, db, job2.ID, 1*time.Hour)

	// Now we should have 1 stalled job (running > 30 min)
	count, err = db.CountStalledJobs(30 * time.Minute)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 stalled job for job started 1 hour ago (UTC), got %d", count)
	}

	// Create another stalled job with a non-UTC timezone offset to verify datetime() handles offsets
	// This exercises the fix for RFC3339 timestamps with timezone offsets like "-07:00"
	commit3 := createCommit(t, db, repo.ID, "stalled2")
	job3 := enqueueJob(t, db, repo.ID, commit3.ID, "stalled2")
	// Use a fixed timezone offset (e.g., UTC-7) instead of UTC
	tzMinus7 := time.FixedZone("UTC-7", -7*60*60)
	backdateJobStartWithOffset(t, db, job3.ID, 1*time.Hour, tzMinus7)

	// Should now have 2 stalled jobs - verifies datetime() parses both Z and offset formats
	count, err = db.CountStalledJobs(30 * time.Minute)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 stalled jobs (UTC and offset timestamp), got %d", count)
	}

	// With a longer threshold (2 hours), neither job should be considered stalled
	count, err = db.CountStalledJobs(2 * time.Hour)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 stalled jobs with 2 hour threshold, got %d", count)
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

		_, _, _, _, canceled, err := db.GetJobCounts()
		if err != nil {
			t.Fatalf("GetJobCounts failed: %v", err)
		}
		if canceled < 1 {
			t.Errorf("Expected at least 1 canceled job, got %d", canceled)
		}
	})
}

func TestMigrationFromOldSchema(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "old.db")

	// Create database with OLD schema (without 'canceled' status)
	oldSchema := `
		CREATE TABLE repos (
			id INTEGER PRIMARY KEY,
			root_path TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE commits (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			sha TEXT UNIQUE NOT NULL,
			author TEXT NOT NULL,
			subject TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE review_jobs (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			commit_id INTEGER REFERENCES commits(id),
			git_ref TEXT NOT NULL,
			agent TEXT NOT NULL DEFAULT 'codex',
			status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed')) DEFAULT 'queued',
			enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
			started_at TEXT,
			finished_at TEXT,
			worker_id TEXT,
			error TEXT,
			prompt TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE reviews (
			id INTEGER PRIMARY KEY,
			job_id INTEGER UNIQUE NOT NULL REFERENCES review_jobs(id),
			agent TEXT NOT NULL,
			prompt TEXT NOT NULL,
			output TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			addressed INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE responses (
			id INTEGER PRIMARY KEY,
			commit_id INTEGER NOT NULL REFERENCES commits(id),
			responder TEXT NOT NULL,
			response TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX idx_review_jobs_status ON review_jobs(status);
	`

	// Open raw connection and create old schema
	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("Failed to open raw DB: %v", err)
	}

	if _, err := rawDB.Exec(oldSchema); err != nil {
		rawDB.Close()
		t.Fatalf("Failed to create old schema: %v", err)
	}

	// Insert test data including a review (to test FK handling during migration)
	_, err = rawDB.Exec(`
		INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/test', 'test');
		INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
			VALUES (1, 1, 'abc123', 'Author', 'Subject', '2024-01-01');
		INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status, enqueued_at)
			VALUES (1, 1, 1, 'abc123', 'codex', 'done', '2024-01-01');
		INSERT INTO reviews (id, job_id, agent, prompt, output)
			VALUES (1, 1, 'codex', 'test prompt', 'test output');
	`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert test data: %v", err)
	}
	rawDB.Close()

	// Now open with our Open() function - should trigger migration
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() failed after migration: %v", err)
	}
	defer db.Close()

	// Verify the old data is preserved
	review, err := db.GetReviewByJobID(1)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}
	if review.Output != "test output" {
		t.Errorf("Expected output 'test output', got '%s'", review.Output)
	}

	// Verify the new constraint allows 'canceled' status
	// Use raw SQL to insert a job, since the migration test's schema may not
	// have all columns that the high-level EnqueueJob function requires.
	repo, _ := db.GetOrCreateRepo("/tmp/test2")
	commit, _ := db.GetOrCreateCommit(repo.ID, "def456", "A", "S", time.Now())
	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, status) VALUES (?, ?, 'def456', 'codex', 'queued')`,
		repo.ID, commit.ID)
	if err != nil {
		t.Fatalf("Failed to insert job: %v", err)
	}
	jobID, _ := result.LastInsertId()

	// Claim the job so we can cancel it
	_, err = db.Exec(`UPDATE review_jobs SET status = 'running', worker_id = 'worker-1' WHERE id = ?`, jobID)
	if err != nil {
		t.Fatalf("Failed to claim job: %v", err)
	}

	// This should succeed with new schema (would fail with old constraint)
	_, err = db.Exec(`UPDATE review_jobs SET status = 'canceled' WHERE id = ?`, jobID)
	if err != nil {
		t.Fatalf("Setting canceled status failed after migration: %v", err)
	}

	// Verify the status was set correctly
	var status string
	err = db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, jobID).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query job status: %v", err)
	}
	if status != "canceled" {
		t.Errorf("Expected status 'canceled', got '%s'", status)
	}

	// Verify constraint still rejects invalid status
	_, err = db.Exec(`UPDATE review_jobs SET status = 'invalid' WHERE id = ?`, jobID)
	if err == nil {
		t.Error("Expected constraint violation for invalid status")
	}

	// Verify FK enforcement works after migration
	// Note: SQLite FKs are OFF by default per-connection, so we can't test that the migration
	// "left FKs enabled" in the pool. What we CAN verify is:
	// 1. The migration succeeded (which includes the FK check at the end)
	// 2. FK enforcement works when enabled on a connection
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	defer conn.Close()

	// Check FK pragma value on this connection before we modify it
	// This is informational - FKs are OFF by default in SQLite
	var fkEnabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkEnabled); err != nil {
		t.Fatalf("Failed to check foreign_keys pragma: %v", err)
	}
	t.Logf("foreign_keys pragma on pooled connection: %d", fkEnabled)

	// Enable FK enforcement and verify it works (proves schema is correct for FKs)
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("Failed to enable foreign keys: %v", err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO reviews (job_id, agent, prompt, output) VALUES (99999, 'test', 'p', 'o')`)
	if err == nil {
		t.Error("Expected foreign key violation for invalid job_id - FKs may not be working")
	}
}

func TestMigrationWithAlterTableColumnOrder(t *testing.T) {
	// Test that migration works when columns were added via ALTER TABLE,
	// which puts them at the end of the table (different from CREATE TABLE order)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "altered.db")

	// Create a very old schema WITHOUT prompt and retry_count columns
	oldSchema := `
		CREATE TABLE repos (
			id INTEGER PRIMARY KEY,
			root_path TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE commits (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			sha TEXT UNIQUE NOT NULL,
			author TEXT NOT NULL,
			subject TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE review_jobs (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			commit_id INTEGER REFERENCES commits(id),
			git_ref TEXT NOT NULL,
			agent TEXT NOT NULL DEFAULT 'codex',
			status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed')) DEFAULT 'queued',
			enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
			started_at TEXT,
			finished_at TEXT,
			worker_id TEXT,
			error TEXT
		);
		CREATE TABLE reviews (
			id INTEGER PRIMARY KEY,
			job_id INTEGER UNIQUE NOT NULL REFERENCES review_jobs(id),
			agent TEXT NOT NULL,
			prompt TEXT NOT NULL,
			output TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			addressed INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE responses (
			id INTEGER PRIMARY KEY,
			commit_id INTEGER NOT NULL REFERENCES commits(id),
			responder TEXT NOT NULL,
			response TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`

	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("Failed to open raw DB: %v", err)
	}

	if _, err := rawDB.Exec(oldSchema); err != nil {
		rawDB.Close()
		t.Fatalf("Failed to create old schema: %v", err)
	}

	// Add columns via ALTER TABLE - this puts them at the END of the table,
	// not in the position they appear in the current CREATE TABLE schema
	_, err = rawDB.Exec(`ALTER TABLE review_jobs ADD COLUMN prompt TEXT`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to add prompt column: %v", err)
	}
	_, err = rawDB.Exec(`ALTER TABLE review_jobs ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to add retry_count column: %v", err)
	}

	// Insert test data with values for the altered columns
	_, err = rawDB.Exec(`
		INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/altered', 'altered');
		INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
			VALUES (1, 1, 'alter123', 'Author', 'Subject', '2024-01-01');
		INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status, enqueued_at, prompt, retry_count)
			VALUES (1, 1, 1, 'alter123', 'codex', 'done', '2024-01-01', 'my prompt', 2);
		INSERT INTO reviews (id, job_id, agent, prompt, output)
			VALUES (1, 1, 'codex', 'test prompt', 'test output');
	`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert test data: %v", err)
	}
	rawDB.Close()

	// Open with our Open() function - should trigger CHECK constraint migration
	// This tests that the explicit column naming in INSERT works correctly
	// even when column order differs from schema definition
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() failed after migration: %v", err)
	}
	defer db.Close()

	// Verify job data is preserved with correct values
	job, err := db.GetJobByID(1)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if job.GitRef != "alter123" {
		t.Errorf("Expected git_ref 'alter123', got '%s'", job.GitRef)
	}
	if job.Agent != "codex" {
		t.Errorf("Expected agent 'codex', got '%s'", job.Agent)
	}

	// Verify retry_count was preserved (query directly since GetJobByID doesn't load it)
	var retryCount int
	err = db.QueryRow(`SELECT retry_count FROM review_jobs WHERE id = 1`).Scan(&retryCount)
	if err != nil {
		t.Fatalf("Failed to query retry_count: %v", err)
	}
	if retryCount != 2 {
		t.Errorf("Expected retry_count 2, got %d", retryCount)
	}

	// Verify prompt was preserved
	var prompt string
	err = db.QueryRow(`SELECT prompt FROM review_jobs WHERE id = 1`).Scan(&prompt)
	if err != nil {
		t.Fatalf("Failed to query prompt: %v", err)
	}
	if prompt != "my prompt" {
		t.Errorf("Expected prompt 'my prompt', got '%s'", prompt)
	}

	// Verify review data is preserved
	review, err := db.GetReviewByJobID(1)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}
	if review.Output != "test output" {
		t.Errorf("Expected output 'test output', got '%s'", review.Output)
	}

	// Verify new constraint works by creating and canceling a job.
	// Use raw SQL to insert/update the job, since the migration test's schema may
	// not have all columns that the high-level EnqueueJob function requires.
	repo2, err := db.GetOrCreateRepo("/tmp/test2")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit2, err := db.GetOrCreateCommit(repo2.ID, "newsha", "A", "S", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	result2, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, status) VALUES (?, ?, 'newsha', 'codex', 'queued')`,
		repo2.ID, commit2.ID)
	if err != nil {
		t.Fatalf("Failed to insert job: %v", err)
	}
	newJobID, _ := result2.LastInsertId()

	// Claim the job
	_, err = db.Exec(`UPDATE review_jobs SET status = 'running', worker_id = 'worker-1' WHERE id = ?`, newJobID)
	if err != nil {
		t.Fatalf("Failed to claim job: %v", err)
	}

	// Verify the claimed status
	var claimedStatus string
	err = db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, newJobID).Scan(&claimedStatus)
	if err != nil {
		t.Fatalf("Failed to query job status: %v", err)
	}
	if claimedStatus != "running" {
		t.Fatalf("Expected job status 'running', got '%s'", claimedStatus)
	}

	// Cancel - this should succeed with new schema (would fail with old constraint)
	_, err = db.Exec(`UPDATE review_jobs SET status = 'canceled' WHERE id = ?`, newJobID)
	if err != nil {
		t.Fatalf("Setting canceled status failed after migration: %v", err)
	}
}

func TestMigrationReasoningColumn(t *testing.T) {
	t.Run("missing reasoning gets default", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "missing_reasoning.db")

		oldSchema := `
			CREATE TABLE repos (
				id INTEGER PRIMARY KEY,
				root_path TEXT UNIQUE NOT NULL,
				name TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE TABLE commits (
				id INTEGER PRIMARY KEY,
				repo_id INTEGER NOT NULL REFERENCES repos(id),
				sha TEXT UNIQUE NOT NULL,
				author TEXT NOT NULL,
				subject TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE TABLE review_jobs (
				id INTEGER PRIMARY KEY,
				repo_id INTEGER NOT NULL REFERENCES repos(id),
				commit_id INTEGER REFERENCES commits(id),
				git_ref TEXT NOT NULL,
				agent TEXT NOT NULL DEFAULT 'codex',
				status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed')) DEFAULT 'queued',
				enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
				started_at TEXT,
				finished_at TEXT,
				worker_id TEXT,
				error TEXT,
				prompt TEXT,
				retry_count INTEGER NOT NULL DEFAULT 0
			);
		`

		rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatalf("Failed to open raw DB: %v", err)
		}
		if _, err := rawDB.Exec(oldSchema); err != nil {
			rawDB.Close()
			t.Fatalf("Failed to create old schema: %v", err)
		}
		_, err = rawDB.Exec(`
			INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/test', 'test');
			INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
				VALUES (1, 1, 'abc123', 'Author', 'Subject', '2024-01-01');
			INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status, enqueued_at)
				VALUES (1, 1, 1, 'abc123', 'codex', 'done', '2024-01-01');
		`)
		if err != nil {
			rawDB.Close()
			t.Fatalf("Failed to insert test data: %v", err)
		}
		rawDB.Close()

		db, err := Open(dbPath)
		if err != nil {
			t.Fatalf("Open() failed after migration: %v", err)
		}
		defer db.Close()

		var reasoning string
		if err := db.QueryRow(`SELECT reasoning FROM review_jobs WHERE id = 1`).Scan(&reasoning); err != nil {
			t.Fatalf("Failed to read reasoning: %v", err)
		}
		if reasoning != "thorough" {
			t.Errorf("Expected default reasoning 'thorough', got '%s'", reasoning)
		}
	})

	t.Run("preserves existing reasoning during rebuild", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "preserve_reasoning.db")

		oldSchema := `
			CREATE TABLE repos (
				id INTEGER PRIMARY KEY,
				root_path TEXT UNIQUE NOT NULL,
				name TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE TABLE commits (
				id INTEGER PRIMARY KEY,
				repo_id INTEGER NOT NULL REFERENCES repos(id),
				sha TEXT UNIQUE NOT NULL,
				author TEXT NOT NULL,
				subject TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE TABLE review_jobs (
				id INTEGER PRIMARY KEY,
				repo_id INTEGER NOT NULL REFERENCES repos(id),
				commit_id INTEGER REFERENCES commits(id),
				git_ref TEXT NOT NULL,
				agent TEXT NOT NULL DEFAULT 'codex',
				reasoning TEXT NOT NULL DEFAULT 'thorough',
				status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed')) DEFAULT 'queued',
				enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
				started_at TEXT,
				finished_at TEXT,
				worker_id TEXT,
				error TEXT,
				prompt TEXT,
				retry_count INTEGER NOT NULL DEFAULT 0
			);
		`

		rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatalf("Failed to open raw DB: %v", err)
		}
		if _, err := rawDB.Exec(oldSchema); err != nil {
			rawDB.Close()
			t.Fatalf("Failed to create old schema: %v", err)
		}
		_, err = rawDB.Exec(`
			INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/test', 'test');
			INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
				VALUES (1, 1, 'abc123', 'Author', 'Subject', '2024-01-01');
			INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, reasoning, status, enqueued_at)
				VALUES (1, 1, 1, 'abc123', 'codex', 'fast', 'done', '2024-01-01');
		`)
		if err != nil {
			rawDB.Close()
			t.Fatalf("Failed to insert test data: %v", err)
		}
		rawDB.Close()

		db, err := Open(dbPath)
		if err != nil {
			t.Fatalf("Open() failed after migration: %v", err)
		}
		defer db.Close()

		var reasoning string
		if err := db.QueryRow(`SELECT reasoning FROM review_jobs WHERE id = 1`).Scan(&reasoning); err != nil {
			t.Fatalf("Failed to read reasoning: %v", err)
		}
		if reasoning != "fast" {
			t.Errorf("Expected preserved reasoning 'fast', got '%s'", reasoning)
		}
	})
}

func TestListReposWithReviewCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("empty database", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts()
		if err != nil {
			t.Fatalf("ListReposWithReviewCounts failed: %v", err)
		}
		if len(repos) != 0 {
			t.Errorf("Expected 0 repos, got %d", len(repos))
		}
		if totalCount != 0 {
			t.Errorf("Expected total count 0, got %d", totalCount)
		}
	})

	// Create repos and jobs
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")
	_ = createRepo(t, db, "/tmp/repo3") // will have 0 jobs

	// Add jobs to repo1 (3 jobs)
	for i := range 3 {
		sha := fmt.Sprintf("repo1-sha%d", i)
		commit := createCommit(t, db, repo1.ID, sha)
		enqueueJob(t, db, repo1.ID, commit.ID, sha)
	}

	// Add jobs to repo2 (2 jobs)
	for i := range 2 {
		sha := fmt.Sprintf("repo2-sha%d", i)
		commit := createCommit(t, db, repo2.ID, sha)
		enqueueJob(t, db, repo2.ID, commit.ID, sha)
	}

	t.Run("repos with varying job counts", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts()
		if err != nil {
			t.Fatalf("ListReposWithReviewCounts failed: %v", err)
		}

		// Should have 3 repos
		if len(repos) != 3 {
			t.Errorf("Expected 3 repos, got %d", len(repos))
		}

		// Total count should be 5 (3 + 2 + 0)
		if totalCount != 5 {
			t.Errorf("Expected total count 5, got %d", totalCount)
		}

		// Build map for easier assertions
		repoMap := make(map[string]int)
		for _, r := range repos {
			repoMap[r.Name] = r.Count
		}

		if repoMap["repo1"] != 3 {
			t.Errorf("Expected repo1 count 3, got %d", repoMap["repo1"])
		}
		if repoMap["repo2"] != 2 {
			t.Errorf("Expected repo2 count 2, got %d", repoMap["repo2"])
		}
		if repoMap["repo3"] != 0 {
			t.Errorf("Expected repo3 count 0, got %d", repoMap["repo3"])
		}
	})

	t.Run("counts include all job statuses", func(t *testing.T) {
		// Claim and complete one job in repo1
		claimed, _ := db.ClaimJob("worker-1")
		if claimed != nil {
			db.CompleteJob(claimed.ID, "codex", "prompt", "output")
		}

		// Claim and fail another job
		claimed2, _ := db.ClaimJob("worker-1")
		if claimed2 != nil {
			db.FailJob(claimed2.ID, "", "test error")
		}

		// Counts should still be the same (counts all jobs, not just completed)
		repos, totalCount, err := db.ListReposWithReviewCounts()
		if err != nil {
			t.Fatalf("ListReposWithReviewCounts failed: %v", err)
		}

		if totalCount != 5 {
			t.Errorf("Expected total count 5 (all statuses), got %d", totalCount)
		}

		repoMap := make(map[string]int)
		for _, r := range repos {
			repoMap[r.Name] = r.Count
		}

		if repoMap["repo1"] != 3 {
			t.Errorf("Expected repo1 count 3 (all statuses), got %d", repoMap["repo1"])
		}
	})
}

func TestListJobsWithRepoFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repos and jobs
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	// Add 3 jobs to repo1
	for i := range 3 {
		sha := fmt.Sprintf("repo1-sha%d", i)
		commit := createCommit(t, db, repo1.ID, sha)
		enqueueJob(t, db, repo1.ID, commit.ID, sha)
	}

	// Add 2 jobs to repo2
	for i := range 2 {
		sha := fmt.Sprintf("repo2-sha%d", i)
		commit := createCommit(t, db, repo2.ID, sha)
		enqueueJob(t, db, repo2.ID, commit.ID, sha)
	}

	t.Run("no filter returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 5 {
			t.Errorf("Expected 5 jobs, got %d", len(jobs))
		}
	})

	t.Run("repo filter returns only matching jobs", func(t *testing.T) {
		// Filter by root_path (not name) since repos with same name could exist at different paths
		jobs, err := db.ListJobs("", repo1.RootPath, 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 3 {
			t.Errorf("Expected 3 jobs for repo1, got %d", len(jobs))
		}
		for _, job := range jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("limit parameter works", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 2, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs with limit=2, got %d", len(jobs))
		}
	})

	t.Run("limit=0 returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 0, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 5 {
			t.Errorf("Expected 5 jobs with limit=0 (no limit), got %d", len(jobs))
		}
	})

	t.Run("repo filter with limit", func(t *testing.T) {
		jobs, err := db.ListJobs("", repo1.RootPath, 2, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs with repo filter and limit=2, got %d", len(jobs))
		}
		for _, job := range jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("status and repo filter combined", func(t *testing.T) {
		// Complete one job from repo1
		claimed, err := db.ClaimJob("worker-1")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		if err := db.CompleteJob(claimed.ID, "codex", "prompt", "output"); err != nil {
			t.Fatalf("CompleteJob failed: %v", err)
		}

		// Query for done jobs in repo1
		jobs, err := db.ListJobs("done", repo1.RootPath, 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 done job for repo1, got %d", len(jobs))
		}
		if len(jobs) > 0 && jobs[0].Status != JobStatusDone {
			t.Errorf("Expected status 'done', got '%s'", jobs[0].Status)
		}
	})

	t.Run("offset pagination", func(t *testing.T) {
		// Get first 2 jobs
		jobs1, err := db.ListJobs("", "", 2, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs1) != 2 {
			t.Errorf("Expected 2 jobs, got %d", len(jobs1))
		}

		// Get next 2 jobs with offset
		jobs2, err := db.ListJobs("", "", 2, 2)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs2) != 2 {
			t.Errorf("Expected 2 jobs with offset=2, got %d", len(jobs2))
		}

		// Ensure no overlap
		for _, j1 := range jobs1 {
			for _, j2 := range jobs2 {
				if j1.ID == j2.ID {
					t.Errorf("Job %d appears in both pages", j1.ID)
				}
			}
		}

		// Get remaining job with offset=4
		jobs3, err := db.ListJobs("", "", 2, 4)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		// 5 jobs total, offset 4 should give 1
		if len(jobs3) != 1 {
			t.Errorf("Expected 1 job with offset=4, got %d", len(jobs3))
		}
	})
}

func TestListJobsWithGitRefFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/repo-gitref")

	// Create jobs with different git refs
	refs := []string{"abc123", "def456", "abc123..def456", "dirty"}
	for _, ref := range refs {
		commit := createCommit(t, db, repo.ID, ref)
		enqueueJob(t, db, repo.ID, commit.ID, ref)
	}

	t.Run("git_ref filter returns matching job", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("abc123"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job with git_ref=abc123, got %d", len(jobs))
		}
		if len(jobs) > 0 && jobs[0].GitRef != "abc123" {
			t.Errorf("Expected GitRef 'abc123', got '%s'", jobs[0].GitRef)
		}
	})

	t.Run("git_ref filter with range ref", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("abc123..def456"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job with range ref, got %d", len(jobs))
		}
		if len(jobs) > 0 && jobs[0].GitRef != "abc123..def456" {
			t.Errorf("Expected GitRef 'abc123..def456', got '%s'", jobs[0].GitRef)
		}
	})

	t.Run("git_ref filter with no match returns empty", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("nonexistent"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 0 {
			t.Errorf("Expected 0 jobs with nonexistent git_ref, got %d", len(jobs))
		}
	})

	t.Run("empty git_ref filter returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 4 {
			t.Errorf("Expected 4 jobs with empty git_ref filter, got %d", len(jobs))
		}
	})

	t.Run("git_ref filter combined with repo filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", repo.RootPath, 50, 0, WithGitRef("def456"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job with git_ref and repo filter, got %d", len(jobs))
		}
	})
}

func TestListJobsWithBranchAndAddressedFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/repo-branch-addr")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create jobs on different branches
	branches := []string{"main", "main", "feature"}
	for i, br := range branches {
		sha := fmt.Sprintf("sha%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Branch: br, Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		// Complete the job so it has a review
		db.ClaimJob("w")
		db.CompleteJob(job.ID, "codex", "", fmt.Sprintf("output %d", i))

		// Mark first job as addressed
		if i == 0 {
			db.MarkReviewAddressedByJobID(job.ID, true)
		}
	}

	t.Run("branch filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs on main, got %d", len(jobs))
		}
	})

	t.Run("addressed=false filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithAddressed(false))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 unaddressed jobs, got %d", len(jobs))
		}
	})

	t.Run("addressed=true filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithAddressed(true))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 addressed job, got %d", len(jobs))
		}
	})

	t.Run("branch + addressed combined", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"), WithAddressed(false))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 unaddressed job on main, got %d", len(jobs))
		}
	})
}

func TestWithBranchOrEmpty(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/repo-branch-empty")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create jobs: one on "main", one on "feature", one branchless
	for i, br := range []string{"main", "feature", ""} {
		sha := fmt.Sprintf("sha-be-%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Branch: br, Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		db.ClaimJob("w")
		db.CompleteJob(job.ID, "codex", "", fmt.Sprintf("output %d", i))
	}

	t.Run("WithBranch strict excludes branchless", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job, got %d", len(jobs))
		}
	})

	t.Run("WithBranchOrEmpty includes branchless", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranchOrEmpty("main"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs (main + branchless), got %d", len(jobs))
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

func TestListJobsAndGetJobByIDReturnAgentic(t *testing.T) {
	// Test that agentic field is properly returned by ListJobs and GetJobByID
	db := openTestDB(t)
	defer db.Close()

	repoPath := filepath.Join(t.TempDir(), "agentic-test-repo")
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Enqueue a prompt job with agentic=true
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:  repo.ID,
		Agent:   "test-agent",
		Prompt:  "Review this code",
		Agentic: true,
	})
	if err != nil {
		t.Fatalf("EnqueuePromptJob failed: %v", err)
	}

	// Verify the returned job has Agentic set
	if !job.Agentic {
		t.Error("EnqueuePromptJob should return job with Agentic=true")
	}

	t.Run("ListJobs returns agentic field", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) == 0 {
			t.Fatal("Expected at least one job")
		}

		// Find our job
		var found bool
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				if !j.Agentic {
					t.Errorf("ListJobs should return Agentic=true for job %d", j.ID)
				}
				break
			}
		}
		if !found {
			t.Errorf("Job %d not found in ListJobs result", job.ID)
		}
	})

	t.Run("GetJobByID returns agentic field", func(t *testing.T) {
		fetchedJob, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if !fetchedJob.Agentic {
			t.Errorf("GetJobByID should return Agentic=true for job %d", job.ID)
		}
	})

	// Also test with agentic=false to ensure we're not just always returning true
	t.Run("non-agentic job returns Agentic=false", func(t *testing.T) {
		nonAgenticJob, err := db.EnqueueJob(EnqueueOpts{
			RepoID: repo.ID,
			Agent:  "test-agent",
			Prompt: "Another review",
		})
		if err != nil {
			t.Fatalf("EnqueuePromptJob failed: %v", err)
		}

		// Check via GetJobByID
		fetchedJob, err := db.GetJobByID(nonAgenticJob.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetchedJob.Agentic {
			t.Errorf("GetJobByID should return Agentic=false for non-agentic job %d", nonAgenticJob.ID)
		}

		// Check via ListJobs
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		var found bool
		for _, j := range jobs {
			if j.ID == nonAgenticJob.ID {
				found = true
				if j.Agentic {
					t.Errorf("ListJobs should return Agentic=false for non-agentic job %d", j.ID)
				}
				break
			}
		}
		if !found {
			t.Errorf("Non-agentic job %d not found in ListJobs result", nonAgenticJob.ID)
		}
	})
}

func TestRepoIdentity(t *testing.T) {
	t.Run("sets identity on create", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, err := db.GetOrCreateRepo("/tmp/identity-test", "git@github.com:foo/bar.git")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}

		if repo.Identity != "git@github.com:foo/bar.git" {
			t.Errorf("Expected identity 'git@github.com:foo/bar.git', got %q", repo.Identity)
		}

		// Verify it persists
		repo2, err := db.GetOrCreateRepo("/tmp/identity-test")
		if err != nil {
			t.Fatalf("GetOrCreateRepo (second call) failed: %v", err)
		}
		if repo2.Identity != "git@github.com:foo/bar.git" {
			t.Errorf("Expected identity to persist, got %q", repo2.Identity)
		}
	})

	t.Run("backfills identity when not set", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create repo without identity
		repo1, err := db.GetOrCreateRepo("/tmp/backfill-test")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}
		if repo1.Identity != "" {
			t.Errorf("Expected no identity initially, got %q", repo1.Identity)
		}

		// Call again with identity - should backfill
		repo2, err := db.GetOrCreateRepo("/tmp/backfill-test", "git@github.com:test/backfill.git")
		if err != nil {
			t.Fatalf("GetOrCreateRepo with identity failed: %v", err)
		}
		if repo2.Identity != "git@github.com:test/backfill.git" {
			t.Errorf("Expected identity to be backfilled, got %q", repo2.Identity)
		}
	})

	t.Run("does not overwrite existing identity", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create repo with identity
		_, err := db.GetOrCreateRepo("/tmp/no-overwrite-test", "original-identity")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}

		// Call again with different identity - should NOT overwrite
		repo2, err := db.GetOrCreateRepo("/tmp/no-overwrite-test", "new-identity")
		if err != nil {
			t.Fatalf("GetOrCreateRepo with new identity failed: %v", err)
		}
		if repo2.Identity != "original-identity" {
			t.Errorf("Expected identity to remain 'original-identity', got %q", repo2.Identity)
		}
	})

	t.Run("multiple clones with same identity allowed", func(t *testing.T) {
		// This tests the fix for https://github.com/roborev-dev/roborev/issues/131
		// Multiple clones of the same repo (e.g., ~/project-1 and ~/project-2 both
		// cloned from the same remote) should be allowed and share the same identity.
		db := openTestDB(t)
		defer db.Close()

		sharedIdentity := "git@github.com:org/shared-repo.git"

		// Create first clone
		repo1, err := db.GetOrCreateRepo("/tmp/clone-1", sharedIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepo for clone-1 failed: %v", err)
		}
		if repo1.Identity != sharedIdentity {
			t.Errorf("Expected identity %q for clone-1, got %q", sharedIdentity, repo1.Identity)
		}

		// Create second clone with same identity - should succeed (was failing before fix)
		repo2, err := db.GetOrCreateRepo("/tmp/clone-2", sharedIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepo for clone-2 failed: %v (multiple clones with same identity should be allowed)", err)
		}
		if repo2.Identity != sharedIdentity {
			t.Errorf("Expected identity %q for clone-2, got %q", sharedIdentity, repo2.Identity)
		}

		// Verify they are different repos
		if repo1.ID == repo2.ID {
			t.Errorf("Expected different repo IDs, but both are %d", repo1.ID)
		}
		if repo1.RootPath == repo2.RootPath {
			t.Errorf("Expected different root paths")
		}

		// Verify both repos exist and can be retrieved
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos failed: %v", err)
		}

		foundClone1, foundClone2 := false, false
		for _, r := range repos {
			if r.ID == repo1.ID {
				foundClone1 = true
			}
			if r.ID == repo2.ID {
				foundClone2 = true
			}
		}
		if !foundClone1 || !foundClone2 {
			t.Errorf("Expected both clones to exist in ListRepos, found clone1=%v clone2=%v", foundClone1, foundClone2)
		}
	})
}

func TestDuplicateSHAHandling(t *testing.T) {
	t.Run("same SHA in different repos creates separate commits", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create two repos
		repo1, _ := db.GetOrCreateRepo("/tmp/sha-test-1")
		repo2, _ := db.GetOrCreateRepo("/tmp/sha-test-2")

		// Create commits with same SHA in different repos
		commit1, err := db.GetOrCreateCommit(repo1.ID, "abc123", "Author1", "Subject1", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit for repo1 failed: %v", err)
		}

		commit2, err := db.GetOrCreateCommit(repo2.ID, "abc123", "Author2", "Subject2", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit for repo2 failed: %v", err)
		}

		// Should be different commits
		if commit1.ID == commit2.ID {
			t.Error("Same SHA in different repos should create different commits")
		}
		if commit1.RepoID != repo1.ID {
			t.Error("commit1 should belong to repo1")
		}
		if commit2.RepoID != repo2.ID {
			t.Error("commit2 should belong to repo2")
		}
	})

	t.Run("GetCommitBySHA returns error when ambiguous", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create two repos with same SHA
		repo1, _ := db.GetOrCreateRepo("/tmp/ambiguous-1")
		repo2, _ := db.GetOrCreateRepo("/tmp/ambiguous-2")

		db.GetOrCreateCommit(repo1.ID, "ambiguous-sha", "Author", "Subject", time.Now())
		db.GetOrCreateCommit(repo2.ID, "ambiguous-sha", "Author", "Subject", time.Now())

		// GetCommitBySHA should fail when ambiguous
		_, err := db.GetCommitBySHA("ambiguous-sha")
		if err == nil {
			t.Error("Expected error when SHA is ambiguous across repos")
		}
	})

	t.Run("GetCommitByRepoAndSHA returns correct commit", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create two repos with same SHA
		repo1, _ := db.GetOrCreateRepo("/tmp/repo-and-sha-1")
		repo2, _ := db.GetOrCreateRepo("/tmp/repo-and-sha-2")

		commit1, _ := db.GetOrCreateCommit(repo1.ID, "same-sha", "Author1", "Subject1", time.Now())
		commit2, _ := db.GetOrCreateCommit(repo2.ID, "same-sha", "Author2", "Subject2", time.Now())

		// GetCommitByRepoAndSHA should return correct commit for each repo
		found1, err := db.GetCommitByRepoAndSHA(repo1.ID, "same-sha")
		if err != nil {
			t.Fatalf("GetCommitByRepoAndSHA for repo1 failed: %v", err)
		}
		if found1.ID != commit1.ID {
			t.Error("GetCommitByRepoAndSHA returned wrong commit for repo1")
		}

		found2, err := db.GetCommitByRepoAndSHA(repo2.ID, "same-sha")
		if err != nil {
			t.Fatalf("GetCommitByRepoAndSHA for repo2 failed: %v", err)
		}
		if found2.ID != commit2.ID {
			t.Error("GetCommitByRepoAndSHA returned wrong commit for repo2")
		}
	})
}

func TestListReposWithReviewCountsByBranch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repos
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	// Create commits and jobs with different branches
	commit1 := createCommit(t, db, repo1.ID, "abc123")
	commit2 := createCommit(t, db, repo1.ID, "def456")
	commit3 := createCommit(t, db, repo2.ID, "ghi789")

	job1 := enqueueJob(t, db, repo1.ID, commit1.ID, "abc123")
	job2 := enqueueJob(t, db, repo1.ID, commit2.ID, "def456")
	job3 := enqueueJob(t, db, repo2.ID, commit3.ID, "ghi789")

	// Update some jobs with branches
	setJobBranch(t, db, job1.ID, "main")
	setJobBranch(t, db, job3.ID, "main")
	setJobBranch(t, db, job2.ID, "feature")

	t.Run("filter by main branch", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCountsByBranch("main")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 2 {
			t.Errorf("Expected 2 repos with main branch, got %d", len(repos))
		}
		if totalCount != 2 {
			t.Errorf("Expected total count 2, got %d", totalCount)
		}
	})

	t.Run("filter by feature branch", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCountsByBranch("feature")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 1 {
			t.Errorf("Expected 1 repo with feature branch, got %d", len(repos))
		}
		if totalCount != 1 {
			t.Errorf("Expected total count 1, got %d", totalCount)
		}
	})

	t.Run("filter by (none) branch", func(t *testing.T) {
		// Add a job with no branch
		commit4 := createCommit(t, db, repo1.ID, "jkl012")
		enqueueJob(t, db, repo1.ID, commit4.ID, "jkl012")

		repos, totalCount, err := db.ListReposWithReviewCountsByBranch("(none)")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 1 {
			t.Errorf("Expected 1 repo with (none) branch, got %d", len(repos))
		}
		if totalCount != 1 {
			t.Errorf("Expected total count 1, got %d", totalCount)
		}
	})

	t.Run("empty filter returns all", func(t *testing.T) {
		repos, _, err := db.ListReposWithReviewCountsByBranch("")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 2 {
			t.Errorf("Expected 2 repos, got %d", len(repos))
		}
	})
}

func TestListBranchesWithCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repos
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	// Create commits and jobs with different branches
	commit1 := createCommit(t, db, repo1.ID, "abc123")
	commit2 := createCommit(t, db, repo1.ID, "def456")
	commit3 := createCommit(t, db, repo1.ID, "ghi789")
	commit4 := createCommit(t, db, repo2.ID, "jkl012")
	commit5 := createCommit(t, db, repo2.ID, "mno345")

	job1 := enqueueJob(t, db, repo1.ID, commit1.ID, "abc123")
	job2 := enqueueJob(t, db, repo1.ID, commit2.ID, "def456")
	job3 := enqueueJob(t, db, repo1.ID, commit3.ID, "ghi789")
	job4 := enqueueJob(t, db, repo2.ID, commit4.ID, "jkl012")
	job5 := enqueueJob(t, db, repo2.ID, commit5.ID, "mno345")

	// Update branches
	setJobBranch(t, db, job1.ID, "main")
	setJobBranch(t, db, job2.ID, "main")
	setJobBranch(t, db, job4.ID, "main")
	setJobBranch(t, db, job3.ID, "feature")
	// job 5 has no branch (NULL)

	t.Run("list all branches", func(t *testing.T) {
		result, err := db.ListBranchesWithCounts(nil)
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if len(result.Branches) != 3 {
			t.Errorf("Expected 3 branches, got %d", len(result.Branches))
		}
		if result.TotalCount != 5 {
			t.Errorf("Expected total count 5, got %d", result.TotalCount)
		}
		if result.NullsRemaining != 1 {
			t.Errorf("Expected 1 null remaining, got %d", result.NullsRemaining)
		}
	})

	t.Run("filter by single repo", func(t *testing.T) {
		// Use repo1.RootPath which is the normalized path stored in the DB
		result, err := db.ListBranchesWithCounts([]string{repo1.RootPath})
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if len(result.Branches) != 2 {
			t.Errorf("Expected 2 branches for repo1, got %d", len(result.Branches))
		}
		if result.TotalCount != 3 {
			t.Errorf("Expected total count 3 for repo1, got %d", result.TotalCount)
		}
	})

	t.Run("filter by multiple repos", func(t *testing.T) {
		// Use repo RootPath values which are the normalized paths stored in the DB
		result, err := db.ListBranchesWithCounts([]string{repo1.RootPath, repo2.RootPath})
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if len(result.Branches) != 3 {
			t.Errorf("Expected 3 branches for both repos, got %d", len(result.Branches))
		}
		if result.TotalCount != 5 {
			t.Errorf("Expected total count 5 for both repos, got %d", result.TotalCount)
		}
	})

	t.Run("no nulls when all have branches", func(t *testing.T) {
		setJobBranch(t, db, job5.ID, "develop")
		result, err := db.ListBranchesWithCounts(nil)
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if result.NullsRemaining != 0 {
			t.Errorf("Expected 0 nulls remaining, got %d", result.NullsRemaining)
		}
	})
}

func TestPatchIDMigration(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'patch_id'`).Scan(&count)
	if err != nil {
		t.Fatalf("check patch_id column: %v", err)
	}
	if count != 1 {
		t.Errorf("expected patch_id column to exist, got count=%d", count)
	}
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

func openTestDB(t *testing.T) *DB {
	t.Helper()
	tmpl, err := getTemplatePath()
	if err != nil {
		t.Fatalf("Failed to create template DB: %v", err)
	}

	data, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("Failed to read template DB: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(dbPath, data, 0644); err != nil {
		t.Fatalf("Failed to write test DB: %v", err)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	return db
}

func createRepo(t *testing.T, db *DB, path string) *Repo {
	t.Helper()
	repo, err := db.GetOrCreateRepo(path)
	if err != nil {
		t.Fatalf("Failed to create repo: %v", err)
	}
	return repo
}

func createCommit(t *testing.T, db *DB, repoID int64, sha string) *Commit {
	t.Helper()
	commit, err := db.GetOrCreateCommit(repoID, sha, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}
	return commit
}

func enqueueJob(t *testing.T, db *DB, repoID, commitID int64, sha string) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repoID, CommitID: commitID, GitRef: sha, Agent: "codex"})
	if err != nil {
		t.Fatalf("Failed to enqueue job: %v", err)
	}
	return job
}

func claimJob(t *testing.T, db *DB, workerID string) *ReviewJob {
	t.Helper()
	job, err := db.ClaimJob(workerID)
	if err != nil {
		t.Fatalf("Failed to claim job: %v", err)
	}
	if job == nil {
		t.Fatal("Expected to claim a job, got nil")
	}
	return job
}

func mustEnqueuePromptJob(t *testing.T, db *DB, opts EnqueueOpts) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(opts)
	if err != nil {
		t.Fatalf("Failed to enqueue prompt job: %v", err)
	}
	return job
}

// setJobStatus forces a job into a specific state via raw SQL, replacing
// manual UPDATE statements scattered across tests.
func setJobStatus(t *testing.T, db *DB, jobID int64, status JobStatus) {
	t.Helper()
	var query string
	switch status {
	case JobStatusQueued:
		query = `UPDATE review_jobs SET status = 'queued', started_at = NULL, finished_at = NULL, error = NULL WHERE id = ?`
	case JobStatusRunning:
		query = `UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`
	case JobStatusDone:
		query = `UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`
	case JobStatusFailed:
		query = `UPDATE review_jobs SET status = 'failed', started_at = datetime('now'), finished_at = datetime('now'), error = 'test error' WHERE id = ?`
	case JobStatusCanceled:
		query = `UPDATE review_jobs SET status = 'canceled', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`
	default:
		t.Fatalf("Unknown job status: %s", status)
	}
	if _, err := db.Exec(query, jobID); err != nil {
		t.Fatalf("Failed to set job status to %s: %v", status, err)
	}
}

func TestListJobsVerdictForBranchRangeReview(t *testing.T) {
	// Regression test: branch range reviews (commit_id NULL, git_ref contains "..")
	// should have their verdict parsed, not be misclassified as task jobs.
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, filepath.Join(t.TempDir(), "range-verdict-repo"))

	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc123..def456", Agent: "codex"})
	if err != nil {
		t.Fatalf("EnqueueRangeJob failed: %v", err)
	}

	// Claim and complete with findings output
	_, err = db.ClaimJob("worker-0")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	err = db.CompleteJob(job.ID, "codex", "review prompt", "- Medium — Bug in line 42\nSummary: found issues.")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	jobs, err := db.ListJobs("", "", 50, 0)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}

	var found bool
	for _, j := range jobs {
		if j.ID == job.ID {
			found = true
			if j.Verdict == nil {
				t.Fatal("expected verdict to be parsed for branch range review, got nil")
			}
			if *j.Verdict != "F" {
				t.Errorf("expected verdict F for review with findings, got %q", *j.Verdict)
			}
			break
		}
	}
	if !found {
		t.Fatal("branch range job not found in ListJobs result")
	}
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

// backdateJobStart updates a job's started_at time to the specified duration ago.
func backdateJobStart(t *testing.T, db *DB, jobID int64, d time.Duration) {
	t.Helper()
	startTime := time.Now().Add(-d).UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = ? WHERE id = ?`, startTime, jobID)
	if err != nil {
		t.Fatalf("failed to backdate job: %v", err)
	}
}

// backdateJobStartWithOffset updates a job's started_at time to the specified duration ago,
// preserving the timezone offset of the generated time.
func backdateJobStartWithOffset(t *testing.T, db *DB, jobID int64, d time.Duration, loc *time.Location) {
	t.Helper()
	startTime := time.Now().Add(-d).In(loc).Format(time.RFC3339)
	_, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = ? WHERE id = ?`, startTime, jobID)
	if err != nil {
		t.Fatalf("failed to backdate job with offset: %v", err)
	}
}

// setJobBranch updates a job's branch.
func setJobBranch(t *testing.T, db *DB, jobID int64, branch string) {
	t.Helper()
	_, err := db.Exec(`UPDATE review_jobs SET branch = ? WHERE id = ?`, branch, jobID)
	if err != nil {
		t.Fatalf("failed to set job branch: %v", err)
	}
}

// createJobChain creates a repo, commit, and enqueued job, returning all three.
func createJobChain(t *testing.T, db *DB, repoPath, sha string) (*Repo, *Commit, *ReviewJob) {
	t.Helper()
	repo := createRepo(t, db, repoPath)
	commit := createCommit(t, db, repo.ID, sha)
	job := enqueueJob(t, db, repo.ID, commit.ID, sha)
	return repo, commit, job
}
