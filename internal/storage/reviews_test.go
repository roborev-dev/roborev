package storage

import (
	"database/sql"
	"testing"
)

// TestAddCommentToJobAllStates verifies that comments can be added to jobs
// in any state: queued, running, done, failed, and canceled.
func TestAddCommentToJobAllStates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	testCases := []struct {
		name   string
		status JobStatus
	}{
		{"queued job", JobStatusQueued},
		{"running job", JobStatusRunning},
		{"completed job", JobStatusDone},
		{"failed job", JobStatusFailed},
		{"canceled job", JobStatusCanceled},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			job := enqueueJob(t, db, repo.ID, commit.ID, "abc123")
			setJobStatus(t, db, job.ID, tc.status)

			// Verify job is in expected state
			updatedJob, err := db.GetJobByID(job.ID)
			if err != nil {
				t.Fatalf("Failed to verify job status: %v", err)
			}
			if updatedJob.Status != tc.status {
				t.Fatalf("Expected job status %s, got %s", tc.status, updatedJob.Status)
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
			verifyComment(t, *resp, "test-user", comment)
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
			verifyComment(t, comments[0], "test-user", comment)
		})
	}
}

// TestAddCommentToJobNonExistent verifies that adding a comment to a
// non-existent job returns an appropriate error.
func TestAddCommentToJobNonExistent(t *testing.T) {
	db := openTestDB(t)
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
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "abc123")
	setJobStatus(t, db, job.ID, JobStatusRunning)

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
		verifyComment(t, retrieved[i], c.user, c.message)
	}
}

// TestAddCommentToJobWithNoReview verifies that comments can be added
// to jobs that have no review (i.e., job exists but has no review record yet).
func TestAddCommentToJobWithNoReview(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, _, job := createJobChain(t, db, "/tmp/test-repo", "abc123")

	// Verify no review exists for this job
	_, err := db.GetReviewByJobID(job.ID)
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
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

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
			job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: tt.gitRef, Agent: "codex", Model: tt.model, Reasoning: "thorough"})
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

func TestGetJobsWithReviewsByIDs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Job 1: with review
	job1 := createCompletedJob(t, db, repo.ID, "abc123", "output1")

	// Job 3: with review
	// Note: We create job3 before job2 so that the queue is empty when we claim/complete job3.
	// If job2 were created first, ClaimJob would pick it up instead.
	job3 := createCompletedJob(t, db, repo.ID, "ghi789", "output3")

	// Job 2: no review (still queued)
	job2 := enqueueJob(t, db, repo.ID, 0, "def456")

	// Job 4: does not exist
	nonExistentJobID := int64(9999)

	t.Run("fetch multiple jobs", func(t *testing.T) {
		jobIDs := []int64{job1.ID, job2.ID, job3.ID, nonExistentJobID}
		results, err := db.GetJobsWithReviewsByIDs(jobIDs)
		if err != nil {
			t.Fatalf("GetJobsWithReviewsByIDs failed: %v", err)
		}

		if len(results) != 3 {
			t.Errorf("Expected 3 results, got %d", len(results))
		}

		// Check job 1 (with review)
		res1, ok := results[job1.ID]
		if !ok {
			t.Errorf("Expected result for job ID %d", job1.ID)
		}
		if res1.Job.ID != job1.ID {
			t.Errorf("Expected job ID %d, got %d", job1.ID, res1.Job.ID)
		}
		if res1.Review == nil {
			t.Error("Expected review for job 1, but got nil")
		} else if res1.Review.Output != "output1" {
			t.Errorf("Expected review output 'output1', got %q", res1.Review.Output)
		}

		// Check job 2 (no review)
		res2, ok := results[job2.ID]
		if !ok {
			t.Errorf("Expected result for job ID %d", job2.ID)
		}
		if res2.Job.ID != job2.ID {
			t.Errorf("Expected job ID %d, got %d", job2.ID, res2.Job.ID)
		}
		if res2.Review != nil {
			t.Errorf("Expected no review for job 2, but got one: %+v", res2.Review)
		}

		// Check job 3 (with review)
		res3, ok := results[job3.ID]
		if !ok {
			t.Errorf("Expected result for job ID %d", job3.ID)
		}
		if res3.Review == nil {
			t.Error("Expected review for job 3, but got nil")
		} else if res3.Review.Output != "output3" {
			t.Errorf("Expected review output 'output3', got %q", res3.Review.Output)
		}

		// Check non-existent job
		if _, ok := results[nonExistentJobID]; ok {
			t.Errorf("Expected no result for non-existent job ID %d", nonExistentJobID)
		}
	})

	t.Run("empty id list", func(t *testing.T) {
		results, err := db.GetJobsWithReviewsByIDs([]int64{})
		if err != nil {
			t.Fatalf("GetJobsWithReviewsByIDs with empty slice failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("Expected 0 results for empty ID list, got %d", len(results))
		}
	})

	t.Run("only non-existent ids", func(t *testing.T) {
		results, err := db.GetJobsWithReviewsByIDs([]int64{999, 998, 997})
		if err != nil {
			t.Fatalf("GetJobsWithReviewsByIDs with non-existent IDs failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("Expected 0 results for non-existent IDs, got %d", len(results))
		}
	})
}

// createCompletedJob helper creates a job, claims it, and completes it.
func createCompletedJob(t *testing.T, db *DB, repoID int64, gitRef, output string) *ReviewJob {
	t.Helper()
	job := enqueueJob(t, db, repoID, 0, gitRef)
	claimed, err := db.ClaimJob("test-worker")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimJob returned nil; expected a queued job")
	}
	if claimed.ID != job.ID {
		t.Fatalf("Claimed job ID %d, expected %d", claimed.ID, job.ID)
	}

	if err := db.CompleteJob(job.ID, "test-agent", "prompt", output); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Refresh job to get updated status/fields
	updatedJob, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if updatedJob.Status != JobStatusDone {
		t.Fatalf("Expected job status %s, got %s", JobStatusDone, updatedJob.Status)
	}
	return updatedJob
}

// verifyComment helper checks if a comment matches expected values.
func verifyComment(t *testing.T, actual Response, expectedUser, expectedMsg string) {
	t.Helper()
	if actual.Responder != expectedUser {
		t.Errorf("Expected responder %q, got %q", expectedUser, actual.Responder)
	}
	if actual.Response != expectedMsg {
		t.Errorf("Expected response %q, got %q", expectedMsg, actual.Response)
	}
}
