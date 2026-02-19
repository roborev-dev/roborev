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
	return testutil.WaitForJobStatus(t, c.DB, jobID, 10*time.Second, statuses...)
}

func TestWorkerPoolE2E(t *testing.T) {
	tc := newWorkerTestContext(t, 2)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createJob(t, sha)

	tc.Pool.Start()
	finalJob := tc.waitForJobStatus(t, job.ID, storage.JobStatusDone, storage.JobStatusFailed)
	tc.Pool.Stop()

	if finalJob.Status != storage.JobStatusDone && finalJob.Status != storage.JobStatusFailed {
		t.Errorf("Job should be done or failed, got %s", finalJob.Status)
	}

	if finalJob.Status == storage.JobStatusDone {
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
