package daemon

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// workerTestContext encapsulates the common setup for worker pool tests.
type workerTestContext struct {
	DB          *storage.DB
	TmpDir      string
	Repo        *storage.Repo
	Pool        *WorkerPool
	Broadcaster Broadcaster
}

// newWorkerTestContext creates a DB, repo, broadcaster, and worker pool with
// the given number of workers. Pass 0 to use the config default.
func newWorkerTestContext(t *testing.T, workers int) *workerTestContext {
	t.Helper()
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	testutil.InitTestGitRepo(t, tmpDir)

	cfg := config.DefaultConfig()
	if workers > 0 {
		cfg.MaxWorkers = workers
	}

	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	b := NewBroadcaster()
	pool := NewWorkerPool(db, NewStaticConfig(cfg), cfg.MaxWorkers, b, nil)

	return &workerTestContext{
		DB:          db,
		TmpDir:      tmpDir,
		Repo:        repo,
		Pool:        pool,
		Broadcaster: b,
	}
}

// createJob enqueues a job for the given SHA and returns it.
func (c *workerTestContext) createJob(t *testing.T, sha string) *storage.ReviewJob {
	t.Helper()
	commit, err := c.DB.GetOrCreateCommit(c.Repo.ID, sha, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := c.DB.EnqueueJob(storage.EnqueueOpts{RepoID: c.Repo.ID, CommitID: commit.ID, GitRef: sha, Agent: "test"})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	return job
}

// createAndClaimJob enqueues and claims a job, returning both.
func (c *workerTestContext) createAndClaimJob(t *testing.T, sha, workerID string) *storage.ReviewJob {
	t.Helper()
	job := c.createJob(t, sha)
	claimed, err := c.DB.ClaimJob(workerID)
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("Expected to claim job %d, got %d", job.ID, claimed.ID)
	}
	return job
}

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

func TestWorkerPoolConcurrency(t *testing.T) {
	tc := newWorkerTestContext(t, 4)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	for i := 0; i < 5; i++ {
		tc.createJob(t, sha)
	}

	tc.Pool.Start()

	time.Sleep(500 * time.Millisecond)
	activeWorkers := tc.Pool.ActiveWorkers()

	tc.Pool.Stop()

	t.Logf("Peak active workers: %d", activeWorkers)
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

func TestWorkerPoolPendingCancellation(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "pending-cancel", "test-worker")

	// Don't start the pool - test pending cancellation manually
	if !tc.Pool.CancelJob(job.ID) {
		t.Error("CancelJob should return true for valid running job")
	}

	if !tc.Pool.IsJobPendingCancel(job.ID) {
		t.Errorf("Job %d should be in pendingCancels", job.ID)
	}

	canceled := false
	tc.Pool.registerRunningJob(job.ID, func() { canceled = true })

	if !canceled {
		t.Error("Job should have been canceled immediately on registration")
	}

	if tc.Pool.IsJobPendingCancel(job.ID) {
		t.Errorf("Job %d should have been removed from pendingCancels", job.ID)
	}
}

func TestWorkerPoolPendingCancellationAfterDBCancel(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "api-cancel-race", "test-worker")

	// Simulate the API path: db.CancelJob first
	if err := tc.DB.CancelJob(job.ID); err != nil {
		t.Fatalf("db.CancelJob failed: %v", err)
	}

	jobAfterDBCancel, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if jobAfterDBCancel.Status != storage.JobStatusCanceled {
		t.Fatalf("Expected status 'canceled', got '%s'", jobAfterDBCancel.Status)
	}
	if jobAfterDBCancel.WorkerID == "" {
		t.Fatal("Expected WorkerID to be set after claim")
	}

	if !tc.Pool.CancelJob(job.ID) {
		t.Error("CancelJob should return true for canceled-but-claimed job")
	}

	if !tc.Pool.IsJobPendingCancel(job.ID) {
		t.Errorf("Job %d should be in pendingCancels", job.ID)
	}

	canceled := false
	tc.Pool.registerRunningJob(job.ID, func() { canceled = true })

	if !canceled {
		t.Error("Job should have been canceled immediately on registration")
	}
}

func TestWorkerPoolCancelInvalidJob(t *testing.T) {
	db := testutil.OpenTestDB(t)

	cfg := config.DefaultConfig()
	broadcaster := NewBroadcaster()
	pool := NewWorkerPool(db, NewStaticConfig(cfg), 1, broadcaster, nil)

	if pool.CancelJob(99999) {
		t.Error("CancelJob should return false for non-existent job")
	}

	if pool.IsJobPendingCancel(99999) {
		t.Error("Non-existent job should not be added to pendingCancels")
	}
}

func TestWorkerPoolCancelJobFinishedDuringWindow(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "finish-window", "test-worker")

	if err := tc.DB.CompleteJob(job.ID, "test", "prompt", "output"); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	completedJob, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if completedJob.Status != storage.JobStatusDone {
		t.Fatalf("Expected status 'done', got '%s'", completedJob.Status)
	}

	if tc.Pool.CancelJob(job.ID) {
		t.Error("CancelJob should return false for completed job")
	}

	if tc.Pool.IsJobPendingCancel(job.ID) {
		t.Error("Completed job should not be added to pendingCancels")
	}
}

func TestWorkerPoolCancelJobRegisteredDuringCheck(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "register-during", "test-worker")

	canceled := false
	tc.Pool.registerRunningJob(job.ID, func() { canceled = true })

	if !tc.Pool.CancelJob(job.ID) {
		t.Error("CancelJob should return true for registered job")
	}

	if !canceled {
		t.Error("Job should have been canceled")
	}

	if tc.Pool.IsJobPendingCancel(job.ID) {
		t.Error("Registered job should not be in pendingCancels")
	}
}

func TestWorkerPoolCancelJobConcurrentRegister(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "concurrent-register", "test-worker")

	var canceled int32
	cancelFunc := func() { atomic.AddInt32(&canceled, 1) }

	tc.Pool.testHookAfterSecondCheck = func() {
		tc.Pool.registerRunningJob(job.ID, cancelFunc)
	}

	result := tc.Pool.CancelJob(job.ID)

	if !result {
		t.Error("CancelJob should return true")
	}
	if atomic.LoadInt32(&canceled) != 1 {
		t.Error("Job should have been canceled exactly once")
	}

	tc.Pool.unregisterRunningJob(job.ID)
}

func TestWorkerPoolCancelJobFinalCheckDeadlockSafe(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "deadlock-test", "test-worker")

	canceled := false
	cancelFunc := func() {
		canceled = true
		tc.Pool.unregisterRunningJob(job.ID)
	}

	tc.Pool.testHookAfterSecondCheck = func() {
		tc.Pool.registerRunningJob(job.ID, cancelFunc)
	}

	done := make(chan bool)
	go func() {
		done <- tc.Pool.CancelJob(job.ID)
	}()

	select {
	case result := <-done:
		if !result {
			t.Error("CancelJob should return true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelJob deadlocked - cancel() called while holding lock")
	}

	if !canceled {
		t.Error("Job should have been canceled via final check path")
	}
}
