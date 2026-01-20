package storage

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/wesm/roborev/internal/config"
)

// SyncWorker handles background synchronization between SQLite and PostgreSQL
type SyncWorker struct {
	db      *DB
	cfg     config.SyncConfig
	pgPool  *PgPool
	stopCh  chan struct{}
	doneCh  chan struct{}
	mu      sync.Mutex // protects running and pgPool
	syncMu  sync.Mutex // serializes sync operations (doSync, SyncNow, FinalPush)
	running bool
}

// NewSyncWorker creates a new sync worker
func NewSyncWorker(db *DB, cfg config.SyncConfig) *SyncWorker {
	return &SyncWorker{
		db:     db,
		cfg:    cfg,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start begins the sync worker in a background goroutine.
// The worker can be stopped with Stop() and restarted with Start().
func (w *SyncWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.running {
		return fmt.Errorf("sync worker already running")
	}

	if !w.cfg.Enabled {
		return fmt.Errorf("sync is not enabled in config")
	}

	// Parse sync interval
	interval, err := time.ParseDuration(w.cfg.Interval)
	if err != nil || interval <= 0 {
		interval = time.Hour // Default
	}

	// Parse connect timeout
	connectTimeout, err := time.ParseDuration(w.cfg.ConnectTimeout)
	if err != nil || connectTimeout <= 0 {
		connectTimeout = 5 * time.Second // Default
	}

	// Reinitialize channels for Start→Stop→Start cycles
	w.stopCh = make(chan struct{})
	w.doneCh = make(chan struct{})

	w.running = true
	go w.run(interval, connectTimeout)
	return nil
}

// Stop gracefully stops the sync worker
func (w *SyncWorker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	// Capture channels and set running=false while holding lock.
	// This prevents races with concurrent Start() which could reinitialize
	// the channels after we unlock but before we close them.
	stopCh := w.stopCh
	doneCh := w.doneCh
	w.running = false
	w.mu.Unlock()

	close(stopCh)
	<-doneCh

	// Acquire syncMu to wait for any in-flight SyncNow or FinalPush to complete
	// before closing the pool
	w.syncMu.Lock()
	defer w.syncMu.Unlock()

	w.mu.Lock()
	if w.pgPool != nil {
		w.pgPool.Close()
		w.pgPool = nil
	}
	w.mu.Unlock()
}

// SyncStats contains statistics from a sync operation
type SyncStats struct {
	PushedJobs      int
	PushedReviews   int
	PushedResponses int
	PulledJobs      int
	PulledReviews   int
	PulledResponses int
}

// FinalPush performs a push-only sync for graceful shutdown.
// This ensures all local changes are pushed before the daemon exits.
// Loops until all pending items are synced (not just one batch).
// Does not pull changes since we're shutting down.
func (w *SyncWorker) FinalPush() error {
	w.mu.Lock()
	pool := w.pgPool
	w.mu.Unlock()

	if pool == nil {
		return nil // Not connected, nothing to push
	}

	// Serialize with other sync operations
	w.syncMu.Lock()
	defer w.syncMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Loop until all pending items are pushed
	var totalJobs, totalReviews, totalResponses int
	for {
		stats, err := w.pushChangesWithStats(ctx, pool)
		if err != nil {
			return fmt.Errorf("final push: %w", err)
		}

		totalJobs += stats.Jobs
		totalReviews += stats.Reviews
		totalResponses += stats.Responses

		// If no items were pushed this round, we're done
		if stats.Jobs == 0 && stats.Reviews == 0 && stats.Responses == 0 {
			break
		}
	}

	if totalJobs > 0 || totalReviews > 0 || totalResponses > 0 {
		log.Printf("Sync: final push completed (%d jobs, %d reviews, %d responses)",
			totalJobs, totalReviews, totalResponses)
	}

	return nil
}

// SyncNow triggers an immediate sync cycle and returns statistics.
// Returns an error if the worker is not running or not connected.
func (w *SyncWorker) SyncNow() (*SyncStats, error) {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return nil, fmt.Errorf("sync worker not running")
	}
	pool := w.pgPool
	w.mu.Unlock()

	if pool == nil {
		return nil, fmt.Errorf("not connected to PostgreSQL")
	}

	// Serialize with other sync operations
	w.syncMu.Lock()
	defer w.syncMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stats := &SyncStats{}

	// Push local changes
	pushed, err := w.pushChangesWithStats(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("push: %w", err)
	}
	stats.PushedJobs = pushed.Jobs
	stats.PushedReviews = pushed.Reviews
	stats.PushedResponses = pushed.Responses

	// Pull remote changes
	pulled, err := w.pullChangesWithStats(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("pull: %w", err)
	}
	stats.PulledJobs = pulled.Jobs
	stats.PulledReviews = pulled.Reviews
	stats.PulledResponses = pulled.Responses

	return stats, nil
}

// pushPullStats tracks counts for push/pull operations
type pushPullStats struct {
	Jobs      int
	Reviews   int
	Responses int
}

// run is the main sync loop
func (w *SyncWorker) run(interval, connectTimeout time.Duration) {
	defer close(w.doneCh)

	// Initial connection attempt with backoff
	backoff := time.Second
	maxBackoff := 5 * time.Minute

	for {
		select {
		case <-w.stopCh:
			return
		default:
		}

		// Try to connect
		if err := w.connect(connectTimeout); err != nil {
			log.Printf("Sync: connection failed: %v (retry in %v)", err, backoff)
			select {
			case <-w.stopCh:
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		// Connected - reset backoff
		backoff = time.Second
		log.Printf("Sync: connected to PostgreSQL")

		// Run sync loop until disconnection or stop
		w.syncLoop(interval)

		// If we get here, we disconnected - try to reconnect
		w.mu.Lock()
		if w.pgPool != nil {
			w.pgPool.Close()
			w.pgPool = nil
		}
		w.mu.Unlock()
	}
}

// connect establishes the PostgreSQL connection
func (w *SyncWorker) connect(timeout time.Duration) error {
	url := w.cfg.PostgresURLExpanded()
	if url == "" {
		return fmt.Errorf("postgres_url not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cfg := DefaultPgPoolConfig()
	cfg.ConnectTimeout = timeout

	pool, err := NewPgPool(ctx, url, cfg)
	if err != nil {
		return err
	}

	// Ensure schema exists
	if err := pool.EnsureSchema(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("ensure schema: %w", err)
	}

	// Register this machine
	machineID, err := w.db.GetMachineID()
	if err != nil {
		pool.Close()
		return fmt.Errorf("get machine ID: %w", err)
	}

	if err := pool.RegisterMachine(ctx, machineID, w.cfg.MachineName); err != nil {
		pool.Close()
		return fmt.Errorf("register machine: %w", err)
	}

	w.mu.Lock()
	w.pgPool = pool
	w.mu.Unlock()

	return nil
}

// syncLoop runs the periodic sync until stop or disconnection
func (w *SyncWorker) syncLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Do initial sync immediately
	if err := w.doSync(); err != nil {
		log.Printf("Sync: error: %v", err)
	}

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			if err := w.doSync(); err != nil {
				log.Printf("Sync: error: %v", err)
				// Check if connection is still alive - grab pool under lock
				w.mu.Lock()
				pool := w.pgPool
				w.mu.Unlock()
				if pool != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					if pool.Ping(ctx) != nil {
						cancel()
						return // Connection lost, exit to reconnect
					}
					cancel()
				}
			}
		}
	}
}

// doSync performs a single sync cycle (push then pull)
func (w *SyncWorker) doSync() error {
	w.syncMu.Lock()
	defer w.syncMu.Unlock()

	w.mu.Lock()
	pool := w.pgPool
	w.mu.Unlock()

	if pool == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Push local changes to PostgreSQL
	if err := w.pushChanges(ctx, pool); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	// Pull remote changes from PostgreSQL
	if err := w.pullChanges(ctx, pool); err != nil {
		return fmt.Errorf("pull: %w", err)
	}

	return nil
}

// pushChanges pushes local changes to PostgreSQL
func (w *SyncWorker) pushChanges(ctx context.Context, pool *PgPool) error {
	_, err := w.pushChangesWithStats(ctx, pool)
	return err
}

// pushChangesWithStats pushes local changes and returns statistics
func (w *SyncWorker) pushChangesWithStats(ctx context.Context, pool *PgPool) (pushPullStats, error) {
	stats := pushPullStats{}

	machineID, err := w.db.GetMachineID()
	if err != nil {
		return stats, fmt.Errorf("get machine ID: %w", err)
	}

	// Push jobs
	jobs, err := w.db.GetJobsToSync(machineID, 100)
	if err != nil {
		return stats, fmt.Errorf("get jobs to sync: %w", err)
	}

	for _, j := range jobs {
		if err := w.pushJob(ctx, pool, j); err != nil {
			log.Printf("Sync: failed to push job %s: %v", j.UUID, err)
			continue
		}
		if err := w.db.MarkJobSynced(j.ID); err != nil {
			log.Printf("Sync: failed to mark job %d synced: %v", j.ID, err)
		}
		stats.Jobs++
	}

	// Push reviews
	reviews, err := w.db.GetReviewsToSync(machineID, 100)
	if err != nil {
		return stats, fmt.Errorf("get reviews to sync: %w", err)
	}

	for _, r := range reviews {
		if err := pool.UpsertReview(ctx, r); err != nil {
			log.Printf("Sync: failed to push review %s: %v", r.UUID, err)
			continue
		}
		if err := w.db.MarkReviewSynced(r.ID); err != nil {
			log.Printf("Sync: failed to mark review %d synced: %v", r.ID, err)
		}
		stats.Reviews++
	}

	// Push responses
	responses, err := w.db.GetResponsesToSync(machineID, 100)
	if err != nil {
		return stats, fmt.Errorf("get responses to sync: %w", err)
	}

	for _, r := range responses {
		if err := pool.InsertResponse(ctx, r); err != nil {
			log.Printf("Sync: failed to push response %s: %v", r.UUID, err)
			continue
		}
		if err := w.db.MarkResponseSynced(r.ID); err != nil {
			log.Printf("Sync: failed to mark response %d synced: %v", r.ID, err)
		}
		stats.Responses++
	}

	if stats.Jobs > 0 || stats.Reviews > 0 || stats.Responses > 0 {
		log.Printf("Sync: pushed %d jobs, %d reviews, %d responses", stats.Jobs, stats.Reviews, stats.Responses)
	}

	return stats, nil
}

// pushJob pushes a single job to PostgreSQL, creating repo/commit as needed
func (w *SyncWorker) pushJob(ctx context.Context, pool *PgPool, j SyncableJob) error {
	// Get or create repo in PostgreSQL
	if j.RepoIdentity == "" {
		return fmt.Errorf("job %s has no repo identity", j.UUID)
	}

	pgRepoID, err := pool.GetOrCreateRepo(ctx, j.RepoIdentity)
	if err != nil {
		return fmt.Errorf("get or create repo: %w", err)
	}

	// Get or create commit if we have one
	var pgCommitID *int64
	if j.CommitSHA != "" {
		id, err := pool.GetOrCreateCommit(ctx, pgRepoID, j.CommitSHA, j.CommitAuthor, j.CommitSubject, j.CommitTimestamp)
		if err != nil {
			return fmt.Errorf("get or create commit: %w", err)
		}
		pgCommitID = &id
	}

	// Upsert the job
	if err := pool.UpsertJob(ctx, j, pgRepoID, pgCommitID); err != nil {
		return fmt.Errorf("upsert job: %w", err)
	}

	return nil
}

// pullChanges pulls remote changes from PostgreSQL
func (w *SyncWorker) pullChanges(ctx context.Context, pool *PgPool) error {
	_, err := w.pullChangesWithStats(ctx, pool)
	return err
}

// pullChangesWithStats pulls remote changes and returns statistics
func (w *SyncWorker) pullChangesWithStats(ctx context.Context, pool *PgPool) (pushPullStats, error) {
	stats := pushPullStats{}

	machineID, err := w.db.GetMachineID()
	if err != nil {
		return stats, fmt.Errorf("get machine ID: %w", err)
	}

	// Pull jobs
	jobCursor, err := w.db.GetSyncState(SyncStateLastJobCursor)
	if err != nil {
		return stats, fmt.Errorf("get job cursor: %w", err)
	}

	for {
		jobs, newCursor, err := pool.PullJobs(ctx, machineID, jobCursor, 100)
		if err != nil {
			return stats, fmt.Errorf("pull jobs: %w", err)
		}
		if len(jobs) == 0 {
			break
		}

		for _, j := range jobs {
			if err := w.pullJob(j); err != nil {
				// Don't advance cursor if any upsert fails - we'll retry next sync
				return stats, fmt.Errorf("pull job %s: %w", j.UUID, err)
			}
			stats.Jobs++
		}

		jobCursor = newCursor
		if err := w.db.SetSyncState(SyncStateLastJobCursor, jobCursor); err != nil {
			return stats, fmt.Errorf("save job cursor: %w", err)
		}

		if len(jobs) < 100 {
			break
		}
	}

	// Pull reviews - only for jobs we have locally.
	// Note: knownJobUUIDs is fetched AFTER pulling all jobs above, so it includes
	// any jobs we just pulled in this sync cycle.
	reviewCursor, err := w.db.GetSyncState(SyncStateLastReviewCursor)
	if err != nil {
		return stats, fmt.Errorf("get review cursor: %w", err)
	}

	knownJobUUIDs, err := w.db.GetKnownJobUUIDs()
	if err != nil {
		return stats, fmt.Errorf("get known job UUIDs: %w", err)
	}

	for {
		reviews, newCursor, err := pool.PullReviews(ctx, machineID, knownJobUUIDs, reviewCursor, 100)
		if err != nil {
			return stats, fmt.Errorf("pull reviews: %w", err)
		}
		if len(reviews) == 0 {
			break
		}

		for _, r := range reviews {
			pr := PulledReview{
				UUID:               r.UUID,
				JobUUID:            r.JobUUID,
				Agent:              r.Agent,
				Prompt:             r.Prompt,
				Output:             r.Output,
				Addressed:          r.Addressed,
				UpdatedByMachineID: r.UpdatedByMachineID,
				CreatedAt:          r.CreatedAt,
				UpdatedAt:          r.UpdatedAt,
			}
			if err := w.db.UpsertPulledReview(pr); err != nil {
				// Don't advance cursor if any upsert fails - we'll retry next sync
				return stats, fmt.Errorf("pull review %s: %w", r.UUID, err)
			}
			stats.Reviews++
		}

		reviewCursor = newCursor
		if err := w.db.SetSyncState(SyncStateLastReviewCursor, reviewCursor); err != nil {
			return stats, fmt.Errorf("save review cursor: %w", err)
		}

		if len(reviews) < 100 {
			break
		}
	}

	// Pull responses
	responseIDStr, err := w.db.GetSyncState(SyncStateLastResponseID)
	if err != nil {
		return stats, fmt.Errorf("get response cursor: %w", err)
	}
	var responseID int64
	if responseIDStr != "" {
		fmt.Sscanf(responseIDStr, "%d", &responseID)
	}

	for {
		responses, newID, err := pool.PullResponses(ctx, machineID, responseID, 100)
		if err != nil {
			return stats, fmt.Errorf("pull responses: %w", err)
		}
		if len(responses) == 0 {
			break
		}

		for _, r := range responses {
			pr := PulledResponse{
				UUID:            r.UUID,
				JobUUID:         r.JobUUID,
				Responder:       r.Responder,
				Response:        r.Response,
				SourceMachineID: r.SourceMachineID,
				CreatedAt:       r.CreatedAt,
			}
			if err := w.db.UpsertPulledResponse(pr); err != nil {
				// Don't advance cursor if any upsert fails - we'll retry next sync
				return stats, fmt.Errorf("pull response %s: %w", r.UUID, err)
			}
			stats.Responses++
		}

		responseID = newID
		if err := w.db.SetSyncState(SyncStateLastResponseID, fmt.Sprintf("%d", responseID)); err != nil {
			return stats, fmt.Errorf("save response cursor: %w", err)
		}

		if len(responses) < 100 {
			break
		}
	}

	if stats.Jobs > 0 || stats.Reviews > 0 || stats.Responses > 0 {
		log.Printf("Sync: pulled %d jobs, %d reviews, %d responses", stats.Jobs, stats.Reviews, stats.Responses)
	}

	return stats, nil
}

// pullJob inserts a pulled job into SQLite, creating repo/commit as needed
func (w *SyncWorker) pullJob(j PulledJob) error {
	// Get or create repo by identity
	repoID, err := w.db.GetOrCreateRepoByIdentity(j.RepoIdentity)
	if err != nil {
		return fmt.Errorf("get or create repo: %w", err)
	}

	// Get or create commit if we have one
	var commitID *int64
	if j.CommitSHA != "" {
		id, err := w.db.GetOrCreateCommitByRepoAndSHA(repoID, j.CommitSHA, j.CommitAuthor, j.CommitSubject, j.CommitTimestamp)
		if err != nil {
			return fmt.Errorf("get or create commit: %w", err)
		}
		commitID = &id
	}

	return w.db.UpsertPulledJob(j, repoID, commitID)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
