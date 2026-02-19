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

func (c *workerTestContext) assertJobPendingCancel(t *testing.T, jobID int64, expected bool) {
	t.Helper()
	if got := c.Pool.IsJobPendingCancel(jobID); got != expected {
		t.Errorf("IsJobPendingCancel(%d) = %v, want %v", jobID, got, expected)
	}
}

func TestWorkerPoolConcurrency(t *testing.T) {
	tc := newWorkerTestContext(t, 4)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	for range 5 {
		tc.createJob(t, sha)
	}

	tc.Pool.Start()

	// Poll until workers are active or timeout
	var activeWorkers int
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		activeWorkers = tc.Pool.ActiveWorkers()
		if activeWorkers > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if activeWorkers == 0 {
		t.Fatal("expected active worker within timeout")
	}

	tc.Pool.Stop()

	t.Logf("Peak active workers: %d", activeWorkers)
}

func TestWorkerPoolPendingCancellation(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "pending-cancel", "test-worker")

	// Don't start the pool - test pending cancellation manually
	if !tc.Pool.CancelJob(job.ID) {
		t.Error("CancelJob should return true for valid running job")
	}

	tc.assertJobPendingCancel(t, job.ID, true)

	canceled := false
	tc.Pool.registerRunningJob(job.ID, func() { canceled = true })

	if !canceled {
		t.Error("Job should have been canceled immediately on registration")
	}

	tc.assertJobPendingCancel(t, job.ID, false)
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

	tc.assertJobPendingCancel(t, job.ID, true)

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

	tc.assertJobPendingCancel(t, job.ID, false)
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

func TestResolveBackupAgent(t *testing.T) {
	tests := []struct {
		name       string
		jobAgent   string
		reviewType string
		config     config.Config
		want       string
	}{
		{
			name:     "no backup configured",
			jobAgent: "test",
			config:   config.Config{},
			want:     "",
		},
		{
			name:     "unknown backup agent",
			jobAgent: "test",
			config: config.Config{
				DefaultBackupAgent: "nonexistent-agent-xyz",
			},
			want: "",
		},
		{
			name:     "backup same as primary",
			jobAgent: "test",
			config: config.Config{
				DefaultBackupAgent: "test",
			},
			want: "",
		},
		{
			name:     "default review type uses review workflow",
			jobAgent: "codex",
			config: config.Config{
				ReviewBackupAgent: "test",
			},
			want: "test",
		},
		{
			name:       "security review type uses security workflow",
			jobAgent:   "codex",
			reviewType: "security",
			config: config.Config{
				SecurityBackupAgent: "test",
			},
			want: "test",
		},
		{
			name:       "design review type uses design workflow",
			jobAgent:   "codex",
			reviewType: "design",
			config: config.Config{
				DesignBackupAgent: "test",
			},
			want: "test",
		},
		{
			name:       "workflow mismatch returns empty",
			jobAgent:   "codex",
			reviewType: "security",
			config: config.Config{
				ReviewBackupAgent: "test",
			},
			want: "",
		},
		{
			name:     "default_backup_agent fallback",
			jobAgent: "codex",
			config: config.Config{
				DefaultBackupAgent: "test",
			},
			want: "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()

			// Merge test config with defaults
			cfg.DefaultBackupAgent = tt.config.DefaultBackupAgent
			cfg.ReviewBackupAgent = tt.config.ReviewBackupAgent
			cfg.SecurityBackupAgent = tt.config.SecurityBackupAgent
			cfg.DesignBackupAgent = tt.config.DesignBackupAgent

			pool := NewWorkerPool(nil, NewStaticConfig(cfg), 1, NewBroadcaster(), nil)
			job := &storage.ReviewJob{
				Agent:      tt.jobAgent,
				RepoPath:   t.TempDir(),
				ReviewType: tt.reviewType,
			}

			got := pool.resolveBackupAgent(job)
			if got != tt.want {
				t.Errorf("resolveBackupAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}
