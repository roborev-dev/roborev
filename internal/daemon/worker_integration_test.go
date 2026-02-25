//go:build integration

package daemon

import (
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// waitForJobStatus polls until the job reaches one of the given statuses.
func (c *workerTestContext) waitForJobStatus(t *testing.T, jobID int64, statuses ...storage.JobStatus) *storage.ReviewJob {
	t.Helper()

	waitingForFailure := false
	for _, status := range statuses {
		if status == storage.JobStatusFailed {
			waitingForFailure = true
			break
		}
	}

	waitStatuses := statuses
	if !waitingForFailure {
		waitStatuses = append(statuses, storage.JobStatusFailed)
	}

	job := testutil.WaitForJobStatus(t, c.DB, jobID, 10*time.Second, waitStatuses...)

	if !waitingForFailure && job.Status == storage.JobStatusFailed {
		t.Fatalf("job failed unexpectedly: %s", job.Error)
	}

	return job
}

func TestWorkerPoolE2E(t *testing.T) {
	tc := newWorkerTestContext(t, 2)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createJob(t, sha)

	tc.Pool.Start()
	defer tc.Pool.Stop()
	finalJob := tc.waitForJobStatus(t, job.ID, storage.JobStatusDone, storage.JobStatusFailed)

	if finalJob.Status != storage.JobStatusDone {
		t.Fatalf("Expected job to complete successfully, got status: %s", finalJob.Status)
	}

	review, err := tc.DB.GetReviewByCommitSHA(sha)
	if err != nil {
		t.Fatalf("GetReviewByCommitSHA failed: %v", err)
	}
	if review.Agent != "test" {
		t.Errorf("Expected agent 'test', got '%s'", review.Agent)
	}
	if review.Output == "" {
		t.Error("Review output should not be empty")
	}
}

func TestWorkerPoolCancelRunningJob(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createJob(t, sha)

	tc.Pool.Start()
	defer tc.Pool.Stop()

	// Wait for job to be claimed
	tc.waitForJobStatus(t, job.ID, storage.JobStatusRunning)

	// Cancel the job
	if err := tc.DB.CancelJob(job.ID); err != nil {
		t.Fatalf("CancelJob failed: %v", err)
	}
	tc.Pool.CancelJob(job.ID)

	finalJob := tc.waitForJobStatus(t, job.ID, storage.JobStatusCanceled)

	if finalJob.Status != storage.JobStatusCanceled {
		t.Errorf("Expected status 'canceled', got '%s'", finalJob.Status)
	}

	_, err := tc.DB.GetReviewByJobID(job.ID)
	if err == nil {
		t.Error("Expected no review for canceled job, but found one")
	}
}
