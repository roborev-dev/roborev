package daemon

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/review"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// workerTestContext encapsulates the common setup for worker pool tests.
const testWorkerID = "test-worker"

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
	pool := NewWorkerPool(db, NewStaticConfig(cfg), cfg.MaxWorkers, b, nil, nil)

	return &workerTestContext{
		DB:          db,
		TmpDir:      tmpDir,
		Repo:        repo,
		Pool:        pool,
		Broadcaster: b,
	}
}

// createJobWithAgent enqueues a job for the given SHA and agent and returns it.
func (c *workerTestContext) createJobWithAgent(t *testing.T, sha, agent string) *storage.ReviewJob {
	t.Helper()
	commit, err := c.DB.GetOrCreateCommit(c.Repo.ID, sha, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := c.DB.EnqueueJob(storage.EnqueueOpts{RepoID: c.Repo.ID, CommitID: commit.ID, GitRef: sha, Agent: agent})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	return job
}

// createAndClaimJobWithAgent enqueues and claims a job with a specific agent.
func (c *workerTestContext) createAndClaimJobWithAgent(t *testing.T, sha, workerID, agent string) *storage.ReviewJob {
	t.Helper()
	job := c.createJobWithAgent(t, sha, agent)
	claimed, err := c.DB.ClaimJob(workerID)
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("Expected to claim job %d, got %d", job.ID, claimed.ID)
	}
	return claimed
}

// createJob enqueues a job for the given SHA and returns it.
func (c *workerTestContext) createJob(t *testing.T, sha string) *storage.ReviewJob {
	t.Helper()
	return c.createJobWithAgent(t, sha, "test")
}

// createAndClaimJob enqueues and claims a job, returning both.
func (c *workerTestContext) createAndClaimJob(t *testing.T, sha, workerID string) *storage.ReviewJob {
	t.Helper()
	return c.createAndClaimJobWithAgent(t, sha, workerID, "test")
}

// exhaustRetries exhausts retries for a job to simulate failure loop
func (c *workerTestContext) exhaustRetries(t *testing.T, job *storage.ReviewJob, workerID, agent string) *storage.ReviewJob {
	t.Helper()
	for i := range maxRetries {
		c.Pool.failOrRetryInner(workerID, job, agent, "connection reset", true)
		reclaimed, err := c.DB.ClaimJob(workerID)
		if err != nil || reclaimed == nil {
			t.Fatalf("re-claim after retry %d: %v", i, err)
		}
		job = reclaimed
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
	job := tc.createAndClaimJob(t, "pending-cancel", testWorkerID)

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
	job := tc.createAndClaimJob(t, "api-cancel-race", testWorkerID)

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
	pool := NewWorkerPool(db, NewStaticConfig(cfg), 1, broadcaster, nil, nil)

	if pool.CancelJob(99999) {
		t.Error("CancelJob should return false for non-existent job")
	}

	if pool.IsJobPendingCancel(99999) {
		t.Error("Non-existent job should not be added to pendingCancels")
	}
}

func TestWorkerPoolCancelJobFinishedDuringWindow(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob(t, "finish-window", testWorkerID)

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
	job := tc.createAndClaimJob(t, "register-during", testWorkerID)

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
	job := tc.createAndClaimJob(t, "concurrent-register", testWorkerID)

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
	job := tc.createAndClaimJob(t, "deadlock-test", testWorkerID)

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

func TestIsQuotaError(t *testing.T) {
	tests := []struct {
		errMsg string
		want   bool
	}{
		// Hard quota exhaustion — should trigger cooldown/skip
		{"quota exceeded for model", true},
		{"QUOTA_EXCEEDED: limit reached", true},
		{"quota exhausted, reset after 8h", true},
		{"QUOTA_EXHAUSTED: try later", true},
		{"insufficient_quota: limit reached", true},
		{"You have exhausted your capacity", true},
		{"RESOURCE EXHAUSTED: try later", true},
		// Transient rate limits — should NOT trigger cooldown (use retries)
		{"Rate limit reached", false},
		{"rate_limit_error: too fast", false},
		{"Too Many Requests (429)", false},
		{"HTTP 429: slow down", false},
		{"status 429 received from API", false},
		// Other non-quota errors
		{"connection reset by peer", false},
		{"timeout after 30s", false},
		{"agent not found", false},
		{"disk quota full", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.errMsg, func(t *testing.T) {
			if got := isQuotaError(tt.errMsg); got != tt.want {
				t.Errorf("isQuotaError(%q) = %v, want %v",
					tt.errMsg, got, tt.want)
			}
		})
	}
}

func TestParseQuotaCooldown(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		fallback time.Duration
		want     time.Duration
	}{
		{
			name:     "extracts go duration",
			errMsg:   "quota exhausted, reset after 8h26m13s please wait",
			fallback: 30 * time.Minute,
			want:     8*time.Hour + 26*time.Minute + 13*time.Second,
		},
		{
			name:     "no reset token falls back",
			errMsg:   "quota exceeded for model gemini-2.5-pro",
			fallback: 30 * time.Minute,
			want:     30 * time.Minute,
		},
		{
			name:     "unparseable duration falls back",
			errMsg:   "reset after bogus",
			fallback: 15 * time.Minute,
			want:     15 * time.Minute,
		},
		{
			name:     "duration at end of string",
			errMsg:   "reset after 2h30m",
			fallback: 30 * time.Minute,
			want:     2*time.Hour + 30*time.Minute,
		},
		{
			name:     "trailing punctuation trimmed",
			errMsg:   "reset after 8h26m13s.",
			fallback: 30 * time.Minute,
			want:     8*time.Hour + 26*time.Minute + 13*time.Second,
		},
		{
			name:     "trailing paren trimmed",
			errMsg:   "reset after 1h30m)",
			fallback: 30 * time.Minute,
			want:     1*time.Hour + 30*time.Minute,
		},
		{
			name:     "clamped to max 24h",
			errMsg:   "reset after 99999h",
			fallback: 30 * time.Minute,
			want:     24 * time.Hour,
		},
		{
			name:     "clamped to min 1m",
			errMsg:   "reset after 5s",
			fallback: 30 * time.Minute,
			want:     1 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseQuotaCooldown(tt.errMsg, tt.fallback)
			if got != tt.want {
				t.Errorf("parseQuotaCooldown() = %v, want %v",
					got, tt.want)
			}
		})
	}
}

func TestAgentCooldown(t *testing.T) {
	cfg := config.DefaultConfig()
	pool := NewWorkerPool(nil, NewStaticConfig(cfg), 1, NewBroadcaster(), nil, nil)

	// Not cooling down initially
	if pool.isAgentCoolingDown("gemini") {
		t.Error("expected gemini not in cooldown initially")
	}

	// Set cooldown
	pool.cooldownAgent("gemini", time.Now().Add(1*time.Hour))
	if !pool.isAgentCoolingDown("gemini") {
		t.Error("expected gemini in cooldown after set")
	}

	// Different agent not affected
	if pool.isAgentCoolingDown("codex") {
		t.Error("expected codex not in cooldown")
	}

	// Expired cooldown returns false
	pool.cooldownAgent("codex", time.Now().Add(-1*time.Second))
	if pool.isAgentCoolingDown("codex") {
		t.Error("expected expired cooldown to return false")
	}

	// cooldownAgent never shortens
	pool.cooldownAgent("gemini", time.Now().Add(1*time.Minute))
	if !pool.isAgentCoolingDown("gemini") {
		t.Error("cooldown should not have been shortened")
	}
}

func TestAgentCooldown_ExpiredEntryDeleted(t *testing.T) {
	cfg := config.DefaultConfig()
	pool := NewWorkerPool(
		nil, NewStaticConfig(cfg), 1, NewBroadcaster(), nil, nil,
	)

	// Set an already-expired cooldown
	pool.cooldownAgent("gemini", time.Now().Add(-1*time.Second))

	// Should return false and clean up the entry
	if pool.isAgentCoolingDown("gemini") {
		t.Error("expected expired cooldown to return false")
	}

	// Entry should be deleted from the map
	pool.agentCooldownsMu.RLock()
	_, exists := pool.agentCooldowns["gemini"]
	pool.agentCooldownsMu.RUnlock()
	if exists {
		t.Error("expected expired entry to be deleted from map")
	}
}

func TestAgentCooldown_RefreshDuringUpgrade(t *testing.T) {
	cfg := config.DefaultConfig()
	pool := NewWorkerPool(
		nil, NewStaticConfig(cfg), 1, NewBroadcaster(), nil, nil,
	)

	// Set an already-expired cooldown so RLock path enters upgrade
	pool.cooldownAgent("gemini", time.Now().Add(-1*time.Second))

	// Use the test hook to refresh the cooldown in the window
	// between RUnlock and Lock, simulating a concurrent goroutine.
	pool.testHookCooldownLockUpgrade = func() {
		pool.agentCooldownsMu.Lock()
		pool.agentCooldowns["gemini"] = time.Now().Add(1 * time.Hour)
		pool.agentCooldownsMu.Unlock()
	}

	// The read-lock path sees expired, upgrades, recheck sees
	// refreshed entry — should return true.
	if !pool.isAgentCoolingDown("gemini") {
		t.Error("expected refreshed cooldown to return true")
	}
}

func TestProcessJob_CooldownResolvesAlias(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	// Enqueue a job with the alias "claude" (canonical: "claude-code")
	claimed := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, "claude")
	job := claimed

	// Cool down "claude-code" (canonical name)
	tc.Pool.cooldownAgent(
		"claude-code", time.Now().Add(1*time.Hour),
	)

	// processJob should detect cooldown via alias resolution
	tc.Pool.processJob(testWorkerID, claimed)

	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if updated.Status != storage.JobStatusFailed {
		t.Errorf(
			"status=%q, want failed (cooldown via alias)",
			updated.Status,
		)
	}
}

func TestResolveBackupAgent_AliasMatchesPrimary(t *testing.T) {
	// "claude" is an alias for "claude-code". If job.Agent is "claude"
	// and backup resolves to "claude-code", they are the same agent.
	cfg := config.DefaultConfig()
	cfg.DefaultBackupAgent = "claude-code"
	pool := NewWorkerPool(
		nil, NewStaticConfig(cfg), 1, NewBroadcaster(), nil, nil,
	)
	job := &storage.ReviewJob{
		Agent:    "claude",
		RepoPath: t.TempDir(),
	}
	got := pool.resolveBackupAgent(job)
	// Should return "" because claude == claude-code after alias
	// resolution. (May also return "" if claude-code binary is not
	// installed, which is fine — both reasons are correct.)
	if got != "" {
		t.Errorf(
			"resolveBackupAgent() = %q, want empty (alias match)",
			got,
		)
	}
}

func TestFailOrRetryInner_QuotaSkipsRetries(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJob(t, sha, testWorkerID)

	// Subscribe to events to verify broadcast
	_, eventCh := tc.Broadcaster.Subscribe("")

	quotaErr := "resource exhausted: reset after 1h"
	tc.Pool.failOrRetryInner(testWorkerID, job, "gemini", quotaErr, true)

	// Job should be failed (not retried) with quota prefix
	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if updated.Status != storage.JobStatusFailed {
		t.Errorf("status=%q, want failed", updated.Status)
	}
	if !strings.HasPrefix(updated.Error, review.QuotaErrorPrefix) {
		t.Errorf("error=%q, want prefix %q", updated.Error, review.QuotaErrorPrefix)
	}

	// Retry count should be 0 — no retries attempted
	retryCount, err := tc.DB.GetJobRetryCount(job.ID)
	if err != nil {
		t.Fatalf("GetJobRetryCount: %v", err)
	}
	if retryCount != 0 {
		t.Errorf("retry_count=%d, want 0 (quota should skip retries)", retryCount)
	}

	// Agent should be in cooldown
	if !tc.Pool.isAgentCoolingDown("gemini") {
		t.Error("expected gemini in cooldown after quota error")
	}

	// Broadcast should have fired
	select {
	case ev := <-eventCh:
		if ev.Type != "review.failed" {
			t.Errorf("event type=%q, want review.failed", ev.Type)
		}
		if !strings.HasPrefix(ev.Error, review.QuotaErrorPrefix) {
			t.Errorf("event error=%q, want prefix %q", ev.Error, review.QuotaErrorPrefix)
		}
	case <-time.After(time.Second):
		t.Error("no broadcast event received")
	}
}

func TestFailOrRetryInner_QuotaExhaustedVariant(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJob(t, sha, testWorkerID)

	// "quota exhausted" (not "quota exceeded") must also trigger quota-skip
	tc.Pool.failOrRetryInner(testWorkerID, job, "gemini", "quota exhausted, reset after 2h", true)

	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if updated.Status != storage.JobStatusFailed {
		t.Errorf("status=%q, want failed", updated.Status)
	}
	if !strings.HasPrefix(updated.Error, review.QuotaErrorPrefix) {
		t.Errorf("error=%q, want prefix %q", updated.Error, review.QuotaErrorPrefix)
	}
	retryCount, _ := tc.DB.GetJobRetryCount(job.ID)
	if retryCount != 0 {
		t.Errorf("retry_count=%d, want 0", retryCount)
	}
}

func TestFailOrRetryInner_NonQuotaStillRetries(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJob(t, sha, testWorkerID)

	// A non-quota agent error should follow the normal retry path
	tc.Pool.failOrRetryInner(testWorkerID, job, "gemini", "connection reset", true)

	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	// Should be queued for retry, not failed
	if updated.Status != storage.JobStatusQueued {
		t.Errorf("status=%q, want queued (retry)", updated.Status)
	}

	retryCount, err := tc.DB.GetJobRetryCount(job.ID)
	if err != nil {
		t.Fatalf("GetJobRetryCount: %v", err)
	}
	if retryCount != 1 {
		t.Errorf("retry_count=%d, want 1", retryCount)
	}

	// Agent should NOT be in cooldown
	if tc.Pool.isAgentCoolingDown("gemini") {
		t.Error("expected gemini NOT in cooldown for non-quota error")
	}
}

func TestFailoverOrFail_FailsOverToBackup(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	// Configure backup agent
	cfg := config.DefaultConfig()
	cfg.DefaultBackupAgent = "test"
	tc.Pool = NewWorkerPool(tc.DB, NewStaticConfig(cfg), 1, tc.Broadcaster, nil, nil)

	// Enqueue with agent "codex" (backup is "test")
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, "codex")
	// Fill in RepoPath so resolveBackupAgent can work
	job.RepoPath = tc.TmpDir

	tc.Pool.failoverOrFail(testWorkerID, job, "codex", "quota exhausted")

	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	// Should be queued for failover, agent changed to "test"
	if updated.Status != storage.JobStatusQueued {
		t.Errorf("status=%q, want queued (failover)", updated.Status)
	}
	if updated.Agent != "test" {
		t.Errorf("agent=%q, want test (failover)", updated.Agent)
	}
}

func TestFailoverOrFail_NoBackupFailsWithQuotaPrefix(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJob(t, sha, testWorkerID)

	// No backup configured — should fail with quota prefix
	tc.Pool.failoverOrFail(testWorkerID, job, "test", "quota exhausted")

	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if updated.Status != storage.JobStatusFailed {
		t.Errorf("status=%q, want failed", updated.Status)
	}
	if !strings.HasPrefix(updated.Error, review.QuotaErrorPrefix) {
		t.Errorf("error=%q, want prefix %q", updated.Error, review.QuotaErrorPrefix)
	}
}

func TestFailOrRetryInner_RetryExhaustedBackupInCooldown(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	// Configure backup agent
	cfg := config.DefaultConfig()
	cfg.DefaultBackupAgent = "test"
	tc.Pool = NewWorkerPool(
		tc.DB, NewStaticConfig(cfg), 1, tc.Broadcaster, nil, nil,
	)

	// Enqueue with agent "codex"
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, "codex")
	job.RepoPath = tc.TmpDir

	// Exhaust retries
	job = tc.exhaustRetries(t, job, testWorkerID, "codex")

	// Put the backup agent in cooldown
	tc.Pool.cooldownAgent(
		"test", time.Now().Add(30*time.Minute),
	)

	// Final failure — retries exhausted, backup in cooldown
	tc.Pool.failOrRetryInner(
		testWorkerID, job, "codex",
		"connection reset", true,
	)

	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	// Should be failed, NOT queued for failover to cooled-down agent
	if updated.Status != storage.JobStatusFailed {
		t.Errorf("status=%q, want failed", updated.Status)
	}
	// Agent should still be codex (not failed over)
	if updated.Agent != "codex" {
		t.Errorf("agent=%q, want codex (no failover)", updated.Agent)
	}
}

func TestFailOrRetryInner_RetryExhaustedFailsOverToBackup(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	// Configure backup agent
	cfg := config.DefaultConfig()
	cfg.DefaultBackupAgent = "test"
	tc.Pool = NewWorkerPool(
		tc.DB, NewStaticConfig(cfg), 1, tc.Broadcaster, nil, nil,
	)

	// Enqueue with agent "codex"
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, "codex")
	job.RepoPath = tc.TmpDir

	// Exhaust retries
	job = tc.exhaustRetries(t, job, testWorkerID, "codex")

	// Final failure — retries exhausted, backup available
	tc.Pool.failOrRetryInner(
		testWorkerID, job, "codex",
		"connection reset", true,
	)

	updated, err := tc.DB.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	// Should be queued for failover, agent changed to "test"
	if updated.Status != storage.JobStatusQueued {
		t.Errorf("status=%q, want queued (failover)", updated.Status)
	}
	if updated.Agent != "test" {
		t.Errorf("agent=%q, want test (failover)", updated.Agent)
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

			pool := NewWorkerPool(nil, NewStaticConfig(cfg), 1, NewBroadcaster(), nil, nil)
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
