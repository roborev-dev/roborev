package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestAddCommentToJobAllStates verifies that comments can be added to jobs
// in any state: queued, running, done, failed, and canceled.
func TestAddCommentToJobAllStates(t *testing.T) {
	db := openReviewsTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	testCases := []struct {
		name       string
		status     JobStatus
		setupQuery string // SQL to set the job to a specific state
	}{
		{
			name:       "queued job",
			status:     JobStatusQueued,
			setupQuery: "", // Default status is queued
		},
		{
			name:       "running job",
			status:     JobStatusRunning,
			setupQuery: `UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`,
		},
		{
			name:       "completed job",
			status:     JobStatusDone,
			setupQuery: `UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`,
		},
		{
			name:       "failed job",
			status:     JobStatusFailed,
			setupQuery: `UPDATE review_jobs SET status = 'failed', started_at = datetime('now'), finished_at = datetime('now'), error = 'test error' WHERE id = ?`,
		},
		{
			name:       "canceled job",
			status:     JobStatusCanceled,
			setupQuery: `UPDATE review_jobs SET status = 'canceled', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a job for this test case
			job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "claude-code", "", "thorough")
			if err != nil {
				t.Fatalf("EnqueueJob failed: %v", err)
			}

			// Set job to the desired state
			if tc.setupQuery != "" {
				_, err = db.Exec(tc.setupQuery, job.ID)
				if err != nil {
					t.Fatalf("Failed to set job status to %s: %v", tc.status, err)
				}
			}

			// Verify job is in expected state
			var actualStatus string
			err = db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, job.ID).Scan(&actualStatus)
			if err != nil {
				t.Fatalf("Failed to verify job status: %v", err)
			}
			if JobStatus(actualStatus) != tc.status {
				t.Fatalf("Expected job status %s, got %s", tc.status, actualStatus)
			}

			// Add a comment to the job
			comment := "Test comment for " + tc.name
			resp, err := db.AddCommentToJob(job.ID, "test-user", comment)
			if err != nil {
				t.Fatalf("AddCommentToJob failed for %s: %v", tc.name, err)
			}

			// Verify the comment was added
			if resp == nil {
				t.Fatal("Expected non-nil response")
			}
			if resp.Responder != "test-user" {
				t.Errorf("Expected responder 'test-user', got %q", resp.Responder)
			}
			if resp.Response != comment {
				t.Errorf("Expected response %q, got %q", comment, resp.Response)
			}
			if resp.JobID == nil || *resp.JobID != job.ID {
				t.Errorf("Expected job ID %d, got %v", job.ID, resp.JobID)
			}

			// Verify we can retrieve the comment
			comments, err := db.GetCommentsForJob(job.ID)
			if err != nil {
				t.Fatalf("GetCommentsForJob failed: %v", err)
			}
			if len(comments) != 1 {
				t.Fatalf("Expected 1 comment, got %d", len(comments))
			}
			if comments[0].Response != comment {
				t.Errorf("Retrieved comment mismatch: expected %q, got %q", comment, comments[0].Response)
			}
		})
	}
}

// TestAddCommentToJobNonExistent verifies that adding a comment to a
// non-existent job returns an appropriate error.
func TestAddCommentToJobNonExistent(t *testing.T) {
	db := openReviewsTestDB(t)
	defer db.Close()

	// Try to add a comment to a job that doesn't exist
	_, err := db.AddCommentToJob(99999, "test-user", "This should fail")
	if err == nil {
		t.Fatal("Expected error when adding comment to non-existent job")
	}
	if err != sql.ErrNoRows {
		t.Errorf("Expected sql.ErrNoRows, got: %v", err)
	}
}

// TestAddCommentToJobMultipleComments verifies that multiple comments
// can be added to the same job.
func TestAddCommentToJobMultipleComments(t *testing.T) {
	db := openReviewsTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Create a job and set it to running (in-progress)
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "claude-code", "", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	_, err = db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID)
	if err != nil {
		t.Fatalf("Failed to set job to running: %v", err)
	}

	// Add multiple comments from different users
	comments := []struct {
		user    string
		message string
	}{
		{"alice", "First comment while job is running"},
		{"bob", "Second comment from another user"},
		{"alice", "Third comment from alice again"},
	}

	for _, c := range comments {
		_, err := db.AddCommentToJob(job.ID, c.user, c.message)
		if err != nil {
			t.Fatalf("AddCommentToJob failed for %s: %v", c.user, err)
		}
	}

	// Verify all comments were added
	retrieved, err := db.GetCommentsForJob(job.ID)
	if err != nil {
		t.Fatalf("GetCommentsForJob failed: %v", err)
	}
	if len(retrieved) != len(comments) {
		t.Fatalf("Expected %d comments, got %d", len(comments), len(retrieved))
	}

	// Verify comments are in order
	for i, c := range comments {
		if retrieved[i].Responder != c.user {
			t.Errorf("Comment %d: expected responder %q, got %q", i, c.user, retrieved[i].Responder)
		}
		if retrieved[i].Response != c.message {
			t.Errorf("Comment %d: expected message %q, got %q", i, c.message, retrieved[i].Response)
		}
	}
}

// TestAddCommentToJobWithNoReview verifies that comments can be added
// to jobs that have no review (i.e., job exists but has no review record yet).
func TestAddCommentToJobWithNoReview(t *testing.T) {
	db := openReviewsTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Create a job (no review is created yet - job is just queued)
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "", "claude-code", "", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Verify no review exists for this job
	_, err = db.GetReviewByJobID(job.ID)
	if err == nil {
		t.Fatal("Expected error getting review for job with no review")
	}

	// Add a comment to the job (should succeed even without a review)
	resp, err := db.AddCommentToJob(job.ID, "test-user", "Comment on job without review")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if resp.Response != "Comment on job without review" {
		t.Errorf("Unexpected response: %q", resp.Response)
	}
}

func TestGetReviewByJobIDIncludesModel(t *testing.T) {
	db := openReviewsTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	tests := []struct {
		name          string
		gitRef        string
		model         string
		expectedModel string
	}{
		{"model is populated when set", "abc123", "o3", "o3"},
		{"model is empty when not set", "def456", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, err := db.EnqueueRangeJob(repo.ID, tt.gitRef, "", "codex", tt.model, "thorough")
			if err != nil {
				t.Fatalf("EnqueueRangeJob failed: %v", err)
			}

			// Claim job to move to running, then complete it
			db.ClaimJob("test-worker")
			err = db.CompleteJob(job.ID, "codex", "test prompt", "Test review output\n\n## Verdict: PASS")
			if err != nil {
				t.Fatalf("CompleteJob failed: %v", err)
			}

			review, err := db.GetReviewByJobID(job.ID)
			if err != nil {
				t.Fatalf("GetReviewByJobID failed: %v", err)
			}

			if review.Job == nil {
				t.Fatal("Expected review.Job to be populated")
			}
			if review.Job.Model != tt.expectedModel {
				t.Errorf("Expected model %q, got %q", tt.expectedModel, review.Job.Model)
			}
		})
	}
}

func openReviewsTestDB(t *testing.T) *DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	return db
}
