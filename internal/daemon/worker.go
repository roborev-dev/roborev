package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	gitpkg "github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/prompt"
	"github.com/roborev-dev/roborev/internal/review"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/worktree"
)

// WorkerPool manages a pool of review workers
type WorkerPool struct {
	db            *storage.DB
	cfgGetter     ConfigGetter
	promptBuilder *prompt.Builder
	broadcaster   Broadcaster
	errorLog      *ErrorLog
	activityLog   *ActivityLog

	numWorkers    int
	activeWorkers atomic.Int32
	stopCh        chan struct{}
	readyCh       chan struct{} // closed after wg.Add in Start
	startOnce     sync.Once
	stopOnce      sync.Once
	wg            sync.WaitGroup

	// Track running jobs for cancellation
	runningJobs    map[int64]context.CancelFunc
	pendingCancels map[int64]bool // Jobs canceled before registered
	runningJobsMu  sync.Mutex

	// Agent cooldowns for quota exhaustion
	agentCooldowns   map[string]time.Time // agent name -> expiry
	agentCooldownsMu sync.RWMutex

	// Output capture for tail command
	outputBuffers *OutputBuffer

	// Test hooks for deterministic synchronization (nil in production)
	testHookAfterSecondCheck    func() // Called after second runningJobs check, before second DB lookup
	testHookCooldownLockUpgrade func() // Called between RUnlock and Lock in isAgentCoolingDown
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(db *storage.DB, cfgGetter ConfigGetter, numWorkers int, broadcaster Broadcaster, errorLog *ErrorLog, activityLog *ActivityLog) *WorkerPool {
	return &WorkerPool{
		db:             db,
		cfgGetter:      cfgGetter,
		promptBuilder:  prompt.NewBuilder(db),
		broadcaster:    broadcaster,
		errorLog:       errorLog,
		activityLog:    activityLog,
		numWorkers:     numWorkers,
		stopCh:         make(chan struct{}),
		readyCh:        make(chan struct{}),
		runningJobs:    make(map[int64]context.CancelFunc),
		pendingCancels: make(map[int64]bool),
		agentCooldowns: make(map[string]time.Time),
		outputBuffers:  NewOutputBuffer(512*1024, 4*1024*1024), // 512KB/job, 4MB total
	}
}

// Start begins the worker pool. Safe to call multiple times;
// only the first call spawns workers.
func (wp *WorkerPool) Start() {
	wp.startOnce.Do(func() {
		log.Printf(
			"Starting worker pool with %d workers",
			wp.numWorkers,
		)
		wp.wg.Add(wp.numWorkers)
		close(wp.readyCh)
		for i := 0; i < wp.numWorkers; i++ {
			go wp.worker(i)
		}
	})
}

// Stop gracefully shuts down the worker pool. Safe to call
// multiple times; only the first call performs shutdown.
func (wp *WorkerPool) Stop() {
	wp.stopOnce.Do(func() {
		log.Println("Stopping worker pool...")
		close(wp.stopCh)
		// Wait for Start to finish wg.Add before calling Wait.
		// If Start was never called, readyCh stays open but
		// stopCh is closed, so any late workers exit immediately.
		select {
		case <-wp.readyCh:
			wp.wg.Wait()
		default:
		}
		log.Println("Worker pool stopped")
	})
}

// ActiveWorkers returns the number of currently active workers
func (wp *WorkerPool) ActiveWorkers() int {
	return int(wp.activeWorkers.Load())
}

// MaxWorkers returns the total number of workers in the pool
func (wp *WorkerPool) MaxWorkers() int {
	return wp.numWorkers
}

// GetJobOutput returns the current output lines for a job.
func (wp *WorkerPool) GetJobOutput(jobID int64) []OutputLine {
	return wp.outputBuffers.GetLines(jobID)
}

// SubscribeJobOutput returns initial lines and a channel for new output.
// Call cancel when done to unsubscribe.
func (wp *WorkerPool) SubscribeJobOutput(jobID int64) ([]OutputLine, <-chan OutputLine, func()) {
	return wp.outputBuffers.Subscribe(jobID)
}

// HasJobOutput returns true if there's active output capture for a job.
func (wp *WorkerPool) HasJobOutput(jobID int64) bool {
	return wp.outputBuffers.IsActive(jobID)
}

// CancelJob cancels a running job by its ID, killing the subprocess.
// Returns true if the job was canceled or marked for pending cancellation.
// Returns false only if the job doesn't exist or isn't in a cancellable state.
func (wp *WorkerPool) CancelJob(jobID int64) bool {
	wp.runningJobsMu.Lock()
	cancel, ok := wp.runningJobs[jobID]
	if ok {
		wp.runningJobsMu.Unlock()
		log.Printf("Canceling job %d", jobID)
		cancel()
		return true
	}
	wp.runningJobsMu.Unlock()

	// Job not registered yet - check if it's a valid job before marking pending
	// This prevents unbounded growth of pendingCancels for invalid/finished job IDs
	// Note: we release the lock before the DB call to avoid blocking other operations
	job, err := wp.db.GetJobByID(jobID)
	if err != nil {
		// DB error - but job may have registered while we were trying to read
		// Re-check runningJobs before giving up
		wp.runningJobsMu.Lock()
		if cancel, ok := wp.runningJobs[jobID]; ok {
			wp.runningJobsMu.Unlock()
			log.Printf("Canceling job %d (registered during failed DB check)", jobID)
			cancel()
			return true
		}
		wp.runningJobsMu.Unlock()
		return false
	}

	// Accept jobs that are queued, running, OR canceled-but-claimed (race condition case)
	// When db.CancelJob is called before workerPool.CancelJob, the status becomes 'canceled'
	// but the worker may not have registered yet. We detect this via WorkerID being set.
	if !wp.isJobCancellable(job) {
		return false
	}

	// Re-lock and check if job was registered while we were checking DB
	wp.runningJobsMu.Lock()
	if cancel, ok := wp.runningJobs[jobID]; ok {
		wp.runningJobsMu.Unlock()
		log.Printf("Canceling job %d (registered during DB check)", jobID)
		cancel()
		return true
	}
	wp.runningJobsMu.Unlock()

	// Test hook: allows tests to register job between second check and final check
	if wp.testHookAfterSecondCheck != nil {
		wp.testHookAfterSecondCheck()
	}

	// Re-verify job is still cancellable before adding to pendingCancels
	// The job may have registered and finished during our DB lookup window
	// Do this outside the lock to avoid blocking other operations
	job, err = wp.db.GetJobByID(jobID)
	if err != nil || !wp.isJobCancellable(job) {
		// Job finished or became non-cancellable - don't add stale entry
		return false
	}

	// Final lock acquisition to set pendingCancels
	wp.runningJobsMu.Lock()

	// Final check if job registered while we did the second DB lookup
	if cancel, ok := wp.runningJobs[jobID]; ok {
		wp.runningJobsMu.Unlock()
		log.Printf("Canceling job %d (registered during second DB check)", jobID)
		cancel()
		return true
	}

	// Mark for pending cancellation
	wp.pendingCancels[jobID] = true
	wp.runningJobsMu.Unlock()
	log.Printf("Job %d not yet registered, marking for pending cancellation", jobID)
	return true
}

// isJobCancellable returns true if the job is in a state that can be canceled
func (wp *WorkerPool) isJobCancellable(job *storage.ReviewJob) bool {
	return job.Status == storage.JobStatusQueued ||
		job.Status == storage.JobStatusRunning ||
		(job.Status == storage.JobStatusCanceled && job.WorkerID != "")
}

// registerRunningJob tracks a running job for potential cancellation.
// If the job was already marked for cancellation (race condition), it
// immediately cancels it.
func (wp *WorkerPool) registerRunningJob(jobID int64, cancel context.CancelFunc) {
	wp.runningJobsMu.Lock()
	wp.runningJobs[jobID] = cancel

	// Check if this job was canceled before we registered it
	if wp.pendingCancels[jobID] {
		delete(wp.pendingCancels, jobID)
		wp.runningJobsMu.Unlock()
		log.Printf("Job %d was pending cancellation, canceling now", jobID)
		cancel()
		return
	}
	wp.runningJobsMu.Unlock()
}

// IsJobPendingCancel reports whether a job is in the pendingCancels set.
// This is intended for use in tests.
func (wp *WorkerPool) IsJobPendingCancel(jobID int64) bool {
	wp.runningJobsMu.Lock()
	defer wp.runningJobsMu.Unlock()
	return wp.pendingCancels[jobID]
}

// unregisterRunningJob removes a job from the running jobs map
func (wp *WorkerPool) unregisterRunningJob(jobID int64) {
	wp.runningJobsMu.Lock()
	delete(wp.runningJobs, jobID)
	delete(wp.pendingCancels, jobID) // Clean up any stale pending cancel
	wp.runningJobsMu.Unlock()
}

func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()
	workerID := fmt.Sprintf("worker-%d", id)

	log.Printf("[%s] Started", workerID)

	for {
		select {
		case <-wp.stopCh:
			log.Printf("[%s] Shutting down", workerID)
			return
		default:
		}

		// Try to claim a job
		job, err := wp.db.ClaimJob(workerID)
		if err != nil {
			log.Printf("[%s] Error claiming job: %v", workerID, err)
			if wp.errorLog != nil {
				wp.errorLog.LogError("worker", fmt.Sprintf("claim job: %v", err), 0)
			}
			time.Sleep(5 * time.Second)
			continue
		}

		if job == nil {
			// No jobs available, wait and retry
			time.Sleep(2 * time.Second)
			continue
		}

		// Process the job
		wp.activeWorkers.Add(1)
		wp.processJob(workerID, job)
		wp.activeWorkers.Add(-1)
	}
}

// maxRetries is the number of retry attempts allowed after initial failure.
// With maxRetries=3, a job can run up to 4 times total (1 initial + 3 retries).
const maxRetries = 3

// reviewTypeTag returns a display prefix for non-default review types
// (e.g. "security "). Returns "" for the default review type to avoid
// redundant "review review" in log lines.
func reviewTypeTag(rt string) string {
	if config.IsDefaultReviewType(rt) {
		return ""
	}
	return rt + " "
}

func (wp *WorkerPool) processJob(workerID string, job *storage.ReviewJob) {
	rtTag := reviewTypeTag(job.ReviewType)

	log.Printf("[%s] Processing job %d %s %sreview/%s ref=%s",
		workerID, job.ID, job.RepoName,
		rtTag, job.Agent, gitpkg.ShortRef(job.GitRef))
	jobStart := time.Now()

	if wp.activityLog != nil {
		wp.activityLog.Log(
			"job.started", "worker",
			fmt.Sprintf("job %d started by %s", job.ID, workerID),
			map[string]string{
				"job_id": fmt.Sprintf("%d", job.ID),
				"worker": workerID,
				"agent":  job.Agent,
				"ref":    job.GitRef,
			},
		)
	}

	// Snapshot config once to ensure consistent settings throughout the job.
	// This prevents mixed settings if config reloads mid-job.
	cfg := wp.cfgGetter.Config()

	// Get timeout from config (per-repo or global, default 30 minutes)
	timeoutMinutes := config.ResolveJobTimeout(job.RepoPath, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMinutes)*time.Minute)
	defer cancel()

	// Register for cancellation tracking
	wp.registerRunningJob(job.ID, cancel)
	defer wp.unregisterRunningJob(job.ID)

	// Skip immediately if the agent is in quota cooldown.
	// Resolve alias so "claude" checks cooldown for "claude-code".
	canonicalAgent := agent.CanonicalName(job.Agent)
	if wp.isAgentCoolingDown(canonicalAgent) {
		log.Printf("[%s] Agent %s in cooldown, skipping job %d",
			workerID, canonicalAgent, job.ID)
		wp.failoverOrFail(workerID, job, canonicalAgent,
			fmt.Sprintf("agent %s quota cooldown active", canonicalAgent))
		return
	}

	// Build the prompt (or use pre-stored prompt for task/compact jobs)
	var reviewPrompt string
	var err error
	if job.UsesStoredPrompt() && job.Prompt != "" {
		// Prompt-native job (task, compact) — prepend agent-specific preamble
		preamble := prompt.GetSystemPrompt(job.Agent, "run")
		if preamble != "" {
			reviewPrompt = preamble + "\n" + job.Prompt
		} else {
			reviewPrompt = job.Prompt
		}
	} else if job.UsesStoredPrompt() {
		// Prompt-native job (task/compact) with missing prompt — likely a
		// daemon version mismatch or storage issue. Fail clearly instead
		// of trying to build a prompt from a non-git label.
		err = fmt.Errorf("%s job %d has no stored prompt (git_ref=%q); restart the daemon with 'roborev daemon restart'", job.JobType, job.ID, job.GitRef)
	} else if job.DiffContent != nil {
		// Dirty job - use pre-captured diff
		reviewPrompt, err = wp.promptBuilder.BuildDirty(job.RepoPath, *job.DiffContent, job.RepoID, cfg.ReviewContextCount, job.Agent, job.ReviewType)
	} else {
		// Normal job - build prompt from git ref
		reviewPrompt, err = wp.promptBuilder.Build(job.RepoPath, job.GitRef, job.RepoID, cfg.ReviewContextCount, job.Agent, job.ReviewType)
	}
	if err != nil {
		log.Printf("[%s] Error building prompt: %v", workerID, err)
		wp.failOrRetry(workerID, job, job.Agent, fmt.Sprintf("build prompt: %v", err))
		return
	}

	// Save the prompt so it can be viewed while job is running
	if err := wp.db.SaveJobPrompt(job.ID, reviewPrompt); err != nil {
		log.Printf("[%s] Error saving prompt: %v", workerID, err)
	}

	// Get the agent (falls back to available agent if preferred not installed)
	baseAgent, err := agent.GetAvailable(job.Agent)
	if err != nil {
		log.Printf("[%s] Error getting agent: %v", workerID, err)
		wp.failOrRetryAgent(workerID, job, job.Agent, fmt.Sprintf("get agent: %v", err))
		return
	}

	// Use reasoning level from job (defaults to thorough for legacy rows)
	// Normalize legacy mixed-case/whitespace values (e.g., "FAST", "High") before parsing
	reasoning := strings.ToLower(strings.TrimSpace(job.Reasoning))
	if reasoning == "" {
		reasoning = "thorough"
	}
	reasoningLevel := agent.ParseReasoningLevel(reasoning)
	a := baseAgent.WithReasoning(reasoningLevel).WithAgentic(job.Agentic).WithModel(job.Model)

	// Use the actual agent name (may differ from requested if fallback occurred)
	agentName := a.Name()
	if agentName != job.Agent {
		log.Printf("[%s] Agent %s not available, using %s", workerID, job.Agent, agentName)
	}

	// Broadcast started event
	wp.broadcaster.Broadcast(Event{
		Type:     "review.started",
		TS:       time.Now(),
		JobID:    job.ID,
		Repo:     job.RepoPath,
		RepoName: job.RepoName,
		SHA:      job.GitRef,
		Agent:    agentName,
	})

	// Create output writer for tail command
	normalizer := GetNormalizer(agentName)
	outputWriter := wp.outputBuffers.Writer(job.ID, normalizer)
	defer func() {
		outputWriter.Flush()
		wp.outputBuffers.CloseJob(job.ID)
	}()

	// For fix jobs, create an isolated worktree to run the agent in.
	// The agent modifies files in the worktree; afterwards we capture the diff as a patch.
	reviewRepoPath := job.RepoPath
	var fixWorktree *worktree.Worktree
	if job.IsFixJob() {
		wt, wtErr := worktree.Create(job.RepoPath, job.GitRef)
		if wtErr != nil {
			log.Printf("[%s] Error creating worktree for fix job %d: %v", workerID, job.ID, wtErr)
			wp.failOrRetry(workerID, job, agentName, fmt.Sprintf("create worktree: %v", wtErr))
			return
		}
		defer wt.Close()
		fixWorktree = wt
		reviewRepoPath = wt.Dir
		log.Printf("[%s] Fix job %d: running agent in worktree %s", workerID, job.ID, wt.Dir)
	}

	// Run the review
	log.Printf("[%s] Running %s %sreview (job %d)...",
		workerID, agentName, rtTag, job.ID)
	output, err := a.Review(ctx, reviewRepoPath, job.GitRef, reviewPrompt, outputWriter)
	if err != nil {
		// Check if this was a cancellation
		if ctx.Err() == context.Canceled {
			log.Printf("[%s] Job %d was canceled", workerID, job.ID)
			// Broadcast cancellation event
			wp.broadcaster.Broadcast(Event{
				Type:     "review.canceled",
				TS:       time.Now(),
				JobID:    job.ID,
				Repo:     job.RepoPath,
				RepoName: job.RepoName,
				SHA:      job.GitRef,
				Agent:    agentName,
			})
			return // Job already marked as canceled in DB, nothing more to do
		}
		log.Printf("[%s] Agent error on job %d: %v",
			workerID, job.ID, err)
		wp.failOrRetryAgent(workerID, job, agentName, fmt.Sprintf("agent: %v", err))
		return
	}

	// For fix jobs, capture the patch from the worktree. Patch capture
	// failures are fatal — a fix job without a patch is useless.
	var fixPatch string
	if job.IsFixJob() {
		var patchErr error
		fixPatch, patchErr = fixWorktree.CapturePatch()
		if patchErr != nil {
			log.Printf("[%s] Fix job %d: patch capture failed: %v", workerID, job.ID, patchErr)
			wp.failOrRetry(workerID, job, agentName, fmt.Sprintf("patch capture: %v", patchErr))
			return
		}
		if fixPatch == "" {
			log.Printf("[%s] Fix job %d: agent produced no file changes", workerID, job.ID)
			wp.failOrRetry(workerID, job, agentName, "agent produced no file changes")
			return
		}
		log.Printf("[%s] Fix job %d: captured patch (%d bytes)", workerID, job.ID, len(fixPatch))
	}

	// For compact jobs, validate raw agent output before storing.
	// Invalid output (empty, error patterns) should fail the job,
	// not produce a "done" review that misleads --wait callers.
	if job.JobType == "compact" && !IsValidCompactOutput(output) {
		log.Printf("[%s] Compact job %d produced invalid output, failing", workerID, job.ID)
		wp.failOrRetryAgent(workerID, job, agentName, "compact output invalid (empty or error)")
		return
	}

	// Store the result (use actual agent name, not requested).
	// CompleteJob/CompleteFixJob is a no-op (returns nil) if the job was
	// canceled between agent finish and now.
	if job.IsFixJob() {
		if err := wp.db.CompleteFixJob(job.ID, agentName, reviewPrompt, output, fixPatch); err != nil {
			log.Printf("[%s] Error storing fix review: %v", workerID, err)
			return
		}
	} else if err := wp.db.CompleteJob(job.ID, agentName, reviewPrompt, output); err != nil {
		log.Printf("[%s] Error storing review: %v", workerID, err)
		return
	}

	// For compact jobs, verify the job actually completed (not
	// silently skipped due to cancel race) before marking source
	// jobs as addressed. CompleteJob no-ops when status != running.
	if job.JobType == "compact" {
		j, err := wp.db.GetJobByID(job.ID)
		if err != nil {
			// Transient read error — skip source marking but don't
			// suppress the completion broadcast below.
			log.Printf("[%s] Compact job %d: failed to verify status: %v", workerID, job.ID, err)
		} else if j.Status != storage.JobStatusDone {
			// Job was canceled between agent finish and CompleteJob.
			// No review was stored, so skip broadcast too.
			log.Printf("[%s] Compact job %d not completed (status=%s), skipping source marking", workerID, job.ID, j.Status)
			return
		} else if err := wp.markCompactSourceJobs(workerID, job.ID); err != nil {
			log.Printf("[%s] Warning: failed to mark compact source jobs for job %d: %v", workerID, job.ID, err)
		}
	}

	log.Printf("[%s] Completed job %d %s %sreview/%s",
		workerID, job.ID, job.RepoName, rtTag, agentName)

	if wp.activityLog != nil {
		wp.activityLog.Log(
			"job.completed", "worker",
			fmt.Sprintf("job %d completed by %s", job.ID, workerID),
			map[string]string{
				"job_id":   fmt.Sprintf("%d", job.ID),
				"worker":   workerID,
				"agent":    agentName,
				"duration": time.Since(jobStart).Round(time.Second).String(),
			},
		)
	}

	// Broadcast completion event
	verdict := storage.ParseVerdict(output)
	wp.broadcaster.Broadcast(Event{
		Type:     "review.completed",
		TS:       time.Now(),
		JobID:    job.ID,
		Repo:     job.RepoPath,
		RepoName: job.RepoName,
		SHA:      job.GitRef,
		Agent:    agentName,
		Verdict:  verdict,
		Findings: output,
	})
}

// failOrRetry attempts to retry the job, or marks it as failed if max retries reached.
// This is used for non-agent errors (e.g., prompt build failures) where switching agents won't help.
func (wp *WorkerPool) failOrRetry(workerID string, job *storage.ReviewJob, agentName string, errorMsg string) {
	wp.failOrRetryInner(workerID, job, agentName, errorMsg, false)
}

// failOrRetryAgent is like failOrRetry but allows failover to a backup agent
// when retries are exhausted. Used for agent-execution errors where switching
// agents may resolve the issue.
func (wp *WorkerPool) failOrRetryAgent(workerID string, job *storage.ReviewJob, agentName string, errorMsg string) {
	wp.failOrRetryInner(workerID, job, agentName, errorMsg, true)
}

func (wp *WorkerPool) failOrRetryInner(workerID string, job *storage.ReviewJob, agentName string, errorMsg string, agentError bool) {
	// Quota errors skip retries entirely — cool down the agent and
	// attempt failover or fail with a quota-prefixed error.
	if agentError && isQuotaError(errorMsg) {
		dur := parseQuotaCooldown(errorMsg, defaultCooldown)
		wp.cooldownAgent(agentName, time.Now().Add(dur))
		log.Printf("[%s] Agent %s quota exhausted, cooldown %v",
			workerID, agentName, dur)
		wp.failoverOrFail(workerID, job, agentName, errorMsg)
		return
	}

	retried, err := wp.db.RetryJob(job.ID, workerID, maxRetries)
	if err != nil {
		log.Printf("[%s] Error retrying job: %v", workerID, err)
		if updated, fErr := wp.db.FailJob(job.ID, workerID, errorMsg); fErr != nil {
			log.Printf("[%s] Error failing job %d: %v", workerID, job.ID, fErr)
		} else if updated {
			wp.broadcastFailed(job, agentName, errorMsg)
			if wp.errorLog != nil {
				wp.errorLog.LogError("worker", fmt.Sprintf("job %d failed: %s", job.ID, errorMsg), job.ID)
			}
			wp.logJobFailed(job.ID, workerID, agentName, errorMsg)
		}
		return
	}

	if retried {
		retryCount, _ := wp.db.GetJobRetryCount(job.ID)
		log.Printf("[%s] Job %d %s queued for retry (%d/%d)",
			workerID, job.ID, job.RepoName, retryCount, maxRetries)
	} else {
		// Retries exhausted -- attempt failover to backup agent if this is an agent error
		if agentError {
			backupAgent := wp.resolveBackupAgent(job)
			if backupAgent != "" && !wp.isAgentCoolingDown(backupAgent) {
				failedOver, foErr := wp.db.FailoverJob(job.ID, workerID, backupAgent)
				if foErr != nil {
					log.Printf("[%s] Error attempting failover for job %d: %v", workerID, job.ID, foErr)
				}
				if failedOver {
					log.Printf("[%s] Job %d failing over from %s to %s after %d retries: %s",
						workerID, job.ID, agentName, backupAgent, maxRetries, errorMsg)
					return
				}
			}
		}

		// No backup or failover failed -- mark as failed
		if updated, fErr := wp.db.FailJob(job.ID, workerID, errorMsg); fErr != nil {
			log.Printf("[%s] Error failing job %d: %v", workerID, job.ID, fErr)
		} else if updated {
			log.Printf("[%s] Job %d %s %sreview/%s failed after %d retries",
				workerID, job.ID, job.RepoName,
				reviewTypeTag(job.ReviewType), agentName,
				maxRetries)
			wp.broadcastFailed(job, agentName, errorMsg)
			if wp.errorLog != nil {
				wp.errorLog.LogError("worker", fmt.Sprintf("job %d failed after %d retries: %s", job.ID, maxRetries, errorMsg), job.ID)
			}
			wp.logJobFailed(job.ID, workerID, agentName, errorMsg)
		}
	}
}

// resolveBackupAgent determines the backup agent for a job from config.
// Returns the canonicalized backup agent name, or "" if none is
// available or it's the same as the job's current agent.
func (wp *WorkerPool) resolveBackupAgent(job *storage.ReviewJob) string {
	cfg := wp.cfgGetter.Config()
	workflow := "review"
	if !config.IsDefaultReviewType(job.ReviewType) {
		workflow = job.ReviewType
	}
	backup := config.ResolveBackupAgentForWorkflow(
		job.RepoPath, cfg, workflow,
	)
	if backup == "" {
		return ""
	}
	// Canonicalize: resolve alias, verify installed, skip if same as primary
	resolved, err := agent.Get(backup)
	if err != nil || !agent.IsAvailable(resolved.Name()) {
		return ""
	}
	if resolved.Name() == agent.CanonicalName(job.Agent) {
		return ""
	}
	return resolved.Name()
}

// broadcastFailed sends a review.failed event for a job
func (wp *WorkerPool) broadcastFailed(job *storage.ReviewJob, agentName, errorMsg string) {
	wp.broadcaster.Broadcast(Event{
		Type:     "review.failed",
		TS:       time.Now(),
		JobID:    job.ID,
		Repo:     job.RepoPath,
		RepoName: job.RepoName,
		SHA:      job.GitRef,
		Agent:    agentName,
		Error:    errorMsg,
	})
}

// defaultCooldown is the fallback duration when the error message doesn't
// contain a parseable "reset after" token.
const defaultCooldown = 30 * time.Minute
const minCooldown = 1 * time.Minute
const maxCooldown = 24 * time.Hour

// isQuotaError returns true if the error message indicates a hard API
// quota exhaustion (case-insensitive). Transient rate-limit/429 errors
// are excluded — those should go through normal retries, not cooldown.
func isQuotaError(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	patterns := []string{
		"resource exhausted",
		"quota exceeded",
		"quota_exceeded",
		"quota exhausted",
		"quota_exhausted",
		"insufficient_quota",
		"exhausted your capacity",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// parseQuotaCooldown extracts a Go-format duration from a "reset after
// <dur>" substring. Returns fallback if not found or unparseable.
func parseQuotaCooldown(errMsg string, fallback time.Duration) time.Duration {
	lower := strings.ToLower(errMsg)
	idx := strings.Index(lower, "reset after ")
	if idx < 0 {
		return fallback
	}
	rest := errMsg[idx+len("reset after "):]
	// Take the next whitespace-delimited token as a duration
	token := rest
	if sp := strings.IndexAny(rest, " \t\n,;)"); sp > 0 {
		token = rest[:sp]
	}
	token = strings.TrimRight(token, ".,;:)]}")
	d, err := time.ParseDuration(token)
	if err != nil || d <= 0 {
		return fallback
	}
	if d < minCooldown {
		return minCooldown
	}
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}

// cooldownAgent sets or extends the cooldown expiry for an agent.
// Only extends — never shortens an existing cooldown.
func (wp *WorkerPool) cooldownAgent(name string, until time.Time) {
	name = agent.CanonicalName(name)
	wp.agentCooldownsMu.Lock()
	defer wp.agentCooldownsMu.Unlock()
	if existing, ok := wp.agentCooldowns[name]; ok && existing.After(until) {
		return
	}
	wp.agentCooldowns[name] = until
}

// isAgentCoolingDown returns true if the agent is currently in a
// quota cooldown period. Expired entries are cleaned up eagerly.
func (wp *WorkerPool) isAgentCoolingDown(name string) bool {
	name = agent.CanonicalName(name)
	wp.agentCooldownsMu.RLock()
	expiry, ok := wp.agentCooldowns[name]
	if !ok {
		wp.agentCooldownsMu.RUnlock()
		return false
	}
	if time.Now().After(expiry) {
		wp.agentCooldownsMu.RUnlock()
		if wp.testHookCooldownLockUpgrade != nil {
			wp.testHookCooldownLockUpgrade()
		}
		// Upgrade to write lock and delete expired entry
		wp.agentCooldownsMu.Lock()
		// Re-check under write lock (may have been refreshed)
		exp, stillExists := wp.agentCooldowns[name]
		if stillExists && time.Now().After(exp) {
			delete(wp.agentCooldowns, name)
			wp.agentCooldownsMu.Unlock()
			return false
		}
		wp.agentCooldownsMu.Unlock()
		return stillExists
	}
	wp.agentCooldownsMu.RUnlock()
	return true
}

// failoverOrFail attempts failover to a backup agent for the job.
// If no backup is available, fails the job with a quota-prefixed error.
func (wp *WorkerPool) failoverOrFail(
	workerID string, job *storage.ReviewJob,
	agentName, errorMsg string,
) {
	backupAgent := wp.resolveBackupAgent(job)
	if backupAgent != "" && !wp.isAgentCoolingDown(backupAgent) {
		failedOver, err := wp.db.FailoverJob(
			job.ID, workerID, backupAgent,
		)
		if err != nil {
			log.Printf("[%s] Error attempting failover for job %d: %v",
				workerID, job.ID, err)
		}
		if failedOver {
			log.Printf("[%s] Job %d failing over from %s to %s (quota): %s",
				workerID, job.ID, agentName, backupAgent, errorMsg)
			return
		}
	}

	// No backup or failover failed — fail with quota prefix
	quotaMsg := review.QuotaErrorPrefix + errorMsg
	if updated, err := wp.db.FailJob(job.ID, workerID, quotaMsg); err != nil {
		log.Printf("[%s] Error failing job %d: %v", workerID, job.ID, err)
	} else if updated {
		log.Printf("[%s] Job %d skipped (agent %s quota exhausted)",
			workerID, job.ID, agentName)
		wp.broadcastFailed(job, agentName, quotaMsg)
		if wp.errorLog != nil {
			wp.errorLog.LogError("worker",
				fmt.Sprintf("job %d skipped (quota): %s", job.ID, errorMsg),
				job.ID)
		}
		wp.logJobFailed(job.ID, workerID, agentName, quotaMsg)
	}
}

// logJobFailed logs a job failure to the activity log
func (wp *WorkerPool) logJobFailed(
	jobID int64, workerID, agentName, errorMsg string,
) {
	if wp.activityLog == nil {
		return
	}
	wp.activityLog.Log(
		"job.failed", "worker",
		fmt.Sprintf("job %d failed", jobID),
		map[string]string{
			"job_id": fmt.Sprintf("%d", jobID),
			"worker": workerID,
			"agent":  agentName,
			"error":  errorMsg,
		},
	)
}

// markCompactSourceJobs marks all source jobs as addressed for a completed compact job
func (wp *WorkerPool) markCompactSourceJobs(workerID string, jobID int64) error {
	// Read metadata file, retrying briefly in case the CLI hasn't finished
	// writing it yet (the file is written after enqueue returns the job ID).
	var metadata *CompactMetadata
	var err error
	for attempt := range 3 {
		metadata, err = ReadCompactMetadata(jobID)
		if err == nil {
			break
		}
		if attempt < 2 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if err != nil {
		log.Printf("[%s] No compact metadata found for job %d after retries: %v", workerID, jobID, err)
		return nil
	}

	if len(metadata.SourceJobIDs) == 0 {
		log.Printf("[%s] No source jobs to mark for compact job %d", workerID, jobID)
		return nil
	}

	log.Printf("[%s] Marking %d source jobs as addressed for compact job %d", workerID, len(metadata.SourceJobIDs), jobID)

	// Mark each source job as addressed
	var failedIDs []int64
	for _, srcJobID := range metadata.SourceJobIDs {
		if err := wp.db.MarkReviewAddressedByJobID(srcJobID, true); err != nil {
			log.Printf("[%s] Failed to mark job %d as addressed: %v", workerID, srcJobID, err)
			failedIDs = append(failedIDs, srcJobID)
		}
	}

	successCount := len(metadata.SourceJobIDs) - len(failedIDs)
	if successCount > 0 {
		log.Printf("[%s] Marked %d/%d source jobs as addressed", workerID, successCount, len(metadata.SourceJobIDs))
	}

	// Only delete metadata when all source jobs were marked.
	// On partial failure, keep metadata so a re-run can retry.
	if len(failedIDs) > 0 {
		log.Printf("[%s] Keeping compact metadata for job %d (%d jobs failed)", workerID, jobID, len(failedIDs))
		return nil
	}

	if err := DeleteCompactMetadata(jobID); err != nil {
		log.Printf("[%s] Failed to delete compact metadata for job %d: %v", workerID, jobID, err)
	} else {
		log.Printf("[%s] Cleaned up compact metadata for job %d", workerID, jobID)
	}

	return nil
}
