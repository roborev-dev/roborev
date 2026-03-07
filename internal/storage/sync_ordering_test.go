package storage

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

const syncLimit = 100

func assertLen[T any](t *testing.T, items []T, expected int, name string) {
	t.Helper()
	if len(items) != expected {
		t.Fatalf("Expected %d %s to sync, got %d", expected, name, len(items))
	}
}

func assertResponseCount(t *testing.T, db *DB, uuid string, expected int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM responses WHERE uuid = ?`, uuid).Scan(&count); err != nil {
		t.Fatalf("Failed to count responses: %v", err)
	}
	if count != expected {
		t.Errorf("Expected %d responses for uuid %s, got %d", expected, uuid, count)
	}
}

func TestUpsertPulledResponse_MissingParentJob(t *testing.T) {
	// This test verifies that UpsertPulledResponse gracefully handles responses
	// for jobs that don't exist locally (returns nil, doesn't error)
	db := openTestDB(t)
	defer db.Close()

	// Try to upsert a response for a job that doesn't exist
	nonexistentJobUUID := GenerateUUID()
	response := PulledResponse{
		UUID:            GenerateUUID(),
		JobUUID:         nonexistentJobUUID,
		Responder:       "human",
		Response:        "Test response for missing job",
		SourceMachineID: GenerateUUID(),
		CreatedAt:       time.Now(),
	}

	// Should return nil (not error) for missing parent job
	err := db.UpsertPulledResponse(response)
	if err != nil {
		t.Fatalf("Expected nil error for missing parent job, got: %v", err)
	}

	// Verify no response was inserted
	assertResponseCount(t, db, response.UUID, 0)
}

func TestUpsertPulledResponse_WithParentJob(t *testing.T) {
	// This test verifies UpsertPulledResponse works when the parent job exists
	h := newSyncTestHelper(t)
	job := h.createPendingJob("parent-job-sha")

	// Upsert a response for the existing job
	response := PulledResponse{
		UUID:            GenerateUUID(),
		JobUUID:         job.UUID,
		Responder:       "human",
		Response:        "Test response for existing job",
		SourceMachineID: GenerateUUID(),
		CreatedAt:       time.Now(),
	}

	err := h.db.UpsertPulledResponse(response)
	if err != nil {
		t.Fatalf("UpsertPulledResponse failed: %v", err)
	}

	// Verify response was inserted
	assertResponseCount(t, h.db, response.UUID, 1)
}

// TestClearAllSyncedAt verifies that ClearAllSyncedAt clears synced_at
// on all tables (jobs, reviews, responses).
func TestClearAllSyncedAt(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job with a review
	job := h.createCompletedJob("clear-test-sha")

	// Add a response
	_, err := h.db.AddCommentToJob(job.ID, "user", "test response")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}

	// Mark everything as synced
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}
	review, err := h.db.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}
	if err := h.db.MarkReviewSynced(review.ID); err != nil {
		t.Fatalf("MarkReviewSynced failed: %v", err)
	}

	// Verify nothing needs to sync
	jobs, _ := h.db.GetJobsToSync(h.machineID, syncLimit)
	assertLen(t, jobs, 0, "jobs")
	reviews, _ := h.db.GetReviewsToSync(h.machineID, syncLimit)
	assertLen(t, reviews, 0, "reviews")

	// Clear all synced_at
	if err := h.db.ClearAllSyncedAt(); err != nil {
		t.Fatalf("ClearAllSyncedAt failed: %v", err)
	}

	// Now everything should need to sync again
	jobs, err = h.db.GetJobsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	assertLen(t, jobs, 1, "jobs")

	// Mark job synced so reviews become available
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}

	reviews, err = h.db.GetReviewsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	assertLen(t, reviews, 1, "reviews")

	responses, err := h.db.GetCommentsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	assertLen(t, responses, 1, "responses")
}

// TestBatchMarkSynced verifies the batch MarkXSynced functions work correctly.
func TestBatchMarkSynced(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create multiple jobs with reviews and responses
	var jobs []*ReviewJob
	for i := range 5 {
		job := h.createCompletedJob(fmt.Sprintf("batch-test-sha-%d", i))
		jobs = append(jobs, job)
		_, err := h.db.AddCommentToJob(job.ID, "user", fmt.Sprintf("response %d", i))
		if err != nil {
			t.Fatalf("AddCommentToJob failed: %v", err)
		}
	}

	t.Run("MarkJobsSynced marks multiple jobs", func(t *testing.T) {
		// Get jobs to sync before
		toSync, err := h.db.GetJobsToSync(h.machineID, syncLimit)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		assertLen(t, toSync, 5, "jobs")

		// Mark first 3 as synced
		jobIDs := []int64{jobs[0].ID, jobs[1].ID, jobs[2].ID}
		if err := h.db.MarkJobsSynced(jobIDs); err != nil {
			t.Fatalf("MarkJobsSynced failed: %v", err)
		}

		// Verify only 2 jobs left to sync
		toSync, err = h.db.GetJobsToSync(h.machineID, syncLimit)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		assertLen(t, toSync, 2, "jobs")
	})

	t.Run("MarkReviewsSynced marks multiple reviews", func(t *testing.T) {
		// Get reviews for synced jobs
		reviews, err := h.db.GetReviewsToSync(h.machineID, syncLimit)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		assertLen(t, reviews, 3, "reviews")

		// Mark all 3 as synced
		reviewIDs := make([]int64, len(reviews))
		for i, r := range reviews {
			reviewIDs[i] = r.ID
		}
		if err := h.db.MarkReviewsSynced(reviewIDs); err != nil {
			t.Fatalf("MarkReviewsSynced failed: %v", err)
		}

		// Verify no reviews left to sync (for synced jobs)
		reviews, err = h.db.GetReviewsToSync(h.machineID, syncLimit)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		assertLen(t, reviews, 0, "reviews")
	})

	t.Run("MarkCommentsSynced marks multiple comments", func(t *testing.T) {
		// Get responses for synced jobs
		responses, err := h.db.GetCommentsToSync(h.machineID, syncLimit)
		if err != nil {
			t.Fatalf("GetCommentsToSync failed: %v", err)
		}
		assertLen(t, responses, 3, "responses")

		// Mark all 3 as synced
		responseIDs := make([]int64, len(responses))
		for i, r := range responses {
			responseIDs[i] = r.ID
		}
		if err := h.db.MarkCommentsSynced(responseIDs); err != nil {
			t.Fatalf("MarkCommentsSynced failed: %v", err)
		}

		// Verify no responses left to sync (for synced jobs)
		responses, err = h.db.GetCommentsToSync(h.machineID, syncLimit)
		if err != nil {
			t.Fatalf("GetCommentsToSync failed: %v", err)
		}
		assertLen(t, responses, 0, "responses")
	})

	t.Run("empty slice is no-op", func(t *testing.T) {
		// Empty slices should not error
		if err := h.db.MarkJobsSynced([]int64{}); err != nil {
			t.Errorf("MarkJobsSynced with empty slice failed: %v", err)
		}
		if err := h.db.MarkReviewsSynced([]int64{}); err != nil {
			t.Errorf("MarkReviewsSynced with empty slice failed: %v", err)
		}
		if err := h.db.MarkCommentsSynced([]int64{}); err != nil {
			t.Errorf("MarkCommentsSynced with empty slice failed: %v", err)
		}
	})
}

// TestGetReviewsToSync_RequiresJobSynced verifies that reviews are only
// returned when their parent job has been synced (j.synced_at IS NOT NULL).
func TestGetReviewsToSync_RequiresJobSynced(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job (not synced yet)
	job := h.createCompletedJob("sync-order-sha")

	// Before job is synced, GetReviewsToSync should return nothing
	reviews, err := h.db.GetReviewsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	assertLen(t, reviews, 0, "reviews")

	// Mark job as synced
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("Failed to mark job synced: %v", err)
	}

	// Now GetReviewsToSync should return the review
	reviews, err = h.db.GetReviewsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	assertLen(t, reviews, 1, "reviews")
}

// TestGetCommentsToSync_RequiresJobSynced verifies that responses are only
// returned when their parent job has been synced (j.synced_at IS NOT NULL).
func TestGetCommentsToSync_RequiresJobSynced(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job (not synced yet)
	job := h.createCompletedJob("response-sync-sha")

	// Add a response to the job
	_, err := h.db.AddCommentToJob(job.ID, "test-user", "test response")
	if err != nil {
		t.Fatalf("Failed to add response: %v", err)
	}

	// Before job is synced, GetResponsesToSync should return nothing
	responses, err := h.db.GetCommentsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	assertLen(t, responses, 0, "responses")

	// Mark job as synced
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("Failed to mark job synced: %v", err)
	}

	// Now GetResponsesToSync should return the response
	responses, err = h.db.GetCommentsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	assertLen(t, responses, 1, "responses")
}

// TestGetJobsToSync_RequiresRepoIdentity verifies that jobs without a
// repo identity are still returned (the identity check happens at push time).
func TestGetJobsToSync_RequiresRepoIdentity(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job
	_ = h.createCompletedJob("identity-test-sha")

	// Verify repo has no identity initially (GetOrCreateRepo doesn't set one)
	var identity sql.NullString
	err := h.db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, h.repo.ID).Scan(&identity)
	if err != nil {
		t.Fatalf("Failed to query repo: %v", err)
	}
	if identity.Valid && identity.String != "" {
		t.Errorf("Expected no identity, got %q", identity.String)
	}

	// GetJobsToSync should return the job (with empty identity)
	jobs, err := h.db.GetJobsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	assertLen(t, jobs, 1, "jobs")
	if jobs[0].RepoIdentity != "" {
		t.Errorf("Expected empty repo identity, got %q", jobs[0].RepoIdentity)
	}

	// Now set the repo identity
	if err := h.db.SetRepoIdentity(h.repo.ID, "git@github.com:test/repo.git"); err != nil {
		t.Fatalf("Failed to set repo identity: %v", err)
	}

	// GetJobsToSync should now return the job with identity
	jobs, err = h.db.GetJobsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	assertLen(t, jobs, 1, "jobs")
	if jobs[0].RepoIdentity != "git@github.com:test/repo.git" {
		t.Errorf("Expected repo identity 'git@github.com:test/repo.git', got %q", jobs[0].RepoIdentity)
	}
}

// TestSyncOrder_FullWorkflow tests the complete sync ordering:
// 1. Jobs must be synced first
// 2. Reviews can only sync after their job is synced
// 3. Responses can only sync after their job is synced
func TestSyncOrder_FullWorkflow(t *testing.T) {
	h := newSyncTestHelper(t)

	// Set repo identity
	if err := h.db.SetRepoIdentity(h.repo.ID, "git@github.com:test/workflow.git"); err != nil {
		t.Fatalf("Failed to set repo identity: %v", err)
	}

	// Create 3 jobs with reviews and responses
	var createdJobs []*ReviewJob
	for i := range 3 {
		job := h.createCompletedJob(fmt.Sprintf("workflow-sha-%d", i))
		createdJobs = append(createdJobs, job)
		// Add a response
		_, err := h.db.AddCommentToJob(job.ID, "user", "response")
		if err != nil {
			t.Fatalf("Failed to add response %d: %v", i, err)
		}
	}

	// Initial state: 3 jobs to sync, 0 reviews (jobs not synced), 0 responses (jobs not synced)
	jobs, err := h.db.GetJobsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	assertLen(t, jobs, 3, "jobs")

	reviews, err := h.db.GetReviewsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	assertLen(t, reviews, 0, "reviews")

	responses, err := h.db.GetCommentsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	assertLen(t, responses, 0, "responses")

	// Sync first job
	if err := h.db.MarkJobSynced(createdJobs[0].ID); err != nil {
		t.Fatalf("Failed to mark job synced: %v", err)
	}

	// Now: 2 jobs to sync, 1 review (first job synced), 1 response (first job synced)
	jobs, err = h.db.GetJobsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	assertLen(t, jobs, 2, "jobs")

	reviews, err = h.db.GetReviewsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	assertLen(t, reviews, 1, "reviews")

	responses, err = h.db.GetCommentsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	assertLen(t, responses, 1, "responses")

	// Sync remaining jobs
	for _, j := range createdJobs[1:] {
		if err := h.db.MarkJobSynced(j.ID); err != nil {
			t.Fatalf("Failed to mark job synced: %v", err)
		}
	}

	// Now: 0 jobs to sync, 3 reviews, 3 responses
	jobs, err = h.db.GetJobsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	assertLen(t, jobs, 0, "jobs")

	reviews, err = h.db.GetReviewsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	assertLen(t, reviews, 3, "reviews")

	responses, err = h.db.GetCommentsToSync(h.machineID, syncLimit)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	assertLen(t, responses, 3, "responses")
}
