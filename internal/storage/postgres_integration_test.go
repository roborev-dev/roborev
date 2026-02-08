//go:build integration

package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

// getIntegrationPostgresURL returns the postgres URL for integration tests.
// Set via TEST_POSTGRES_URL environment variable or use default from docker-compose.test.yml
func getIntegrationPostgresURL() string {
	if url := os.Getenv("TEST_POSTGRES_URL"); url != "" {
		return url
	}
	return "postgres://roborev_test:roborev_test_password@localhost:5433/roborev_test"
}

// integrationEnv manages Postgres connection, schema, and temp directory for integration tests.
type integrationEnv struct {
	T      *testing.T
	Ctx    context.Context
	cancel context.CancelFunc
	Pool   *PgPool
	TmpDir string
	pgURL  string
}

// newIntegrationEnv creates a test environment: connects to Postgres, wipes and recreates the schema,
// and provides a temp directory for SQLite databases.
func newIntegrationEnv(t *testing.T, timeout time.Duration) *integrationEnv {
	t.Helper()
	url := getIntegrationPostgresURL()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		cancel()
		t.Fatalf("Failed to connect to postgres: %v (is docker-compose running?)", err)
	}

	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	env := &integrationEnv{
		T:      t,
		Ctx:    ctx,
		cancel: cancel,
		Pool:   pool,
		TmpDir: t.TempDir(),
		pgURL:  url,
	}
	t.Cleanup(func() {
		pool.Close()
		cancel()
	})
	return env
}

// openDB creates a new SQLite database in the temp directory.
func (e *integrationEnv) openDB(name string) *DB {
	e.T.Helper()
	db, err := Open(filepath.Join(e.TmpDir, name))
	if err != nil {
		e.T.Fatalf("Failed to open SQLite %s: %v", name, err)
	}
	e.T.Cleanup(func() { db.Close() })
	return db
}

// validPgTables is the allowlist of tables that may be queried by test helpers.
var validPgTables = map[string]bool{
	"machines":    true,
	"repos":       true,
	"commits":     true,
	"review_jobs": true,
	"reviews":     true,
	"responses":   true,
}

// assertPgCount asserts the row count of a table in Postgres.
// The table parameter is validated against an allowlist to prevent SQL injection.
func (e *integrationEnv) assertPgCount(table string, expected int) {
	e.T.Helper()
	if !validPgTables[table] {
		e.T.Fatalf("assertPgCount: invalid table name %q", table)
	}
	var count int
	err := e.Pool.pool.QueryRow(e.Ctx, fmt.Sprintf("SELECT COUNT(*) FROM roborev.%s", table)).Scan(&count)
	if err != nil {
		e.T.Fatalf("Failed to query postgres %s count: %v", table, err)
	}
	if count != expected {
		e.T.Errorf("Expected %d rows in %s, got %d", expected, table, count)
	}
}

// assertPgCountWhere asserts the row count with a WHERE clause.
// The table parameter is validated against an allowlist to prevent SQL injection.
func (e *integrationEnv) assertPgCountWhere(table, where string, args []interface{}, expected int) {
	e.T.Helper()
	if !validPgTables[table] {
		e.T.Fatalf("assertPgCountWhere: invalid table name %q", table)
	}
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM roborev.%s WHERE %s", table, where)
	err := e.Pool.pool.QueryRow(e.Ctx, query, args...).Scan(&count)
	if err != nil {
		e.T.Fatalf("Failed to query postgres %s: %v", table, err)
	}
	if count != expected {
		e.T.Errorf("Expected %d rows in %s WHERE %s, got %d", expected, table, where, count)
	}
}

// pgQueryString returns a single string value from a Postgres query.
func (e *integrationEnv) pgQueryString(query string, args ...interface{}) string {
	e.T.Helper()
	var val string
	if err := e.Pool.pool.QueryRow(e.Ctx, query, args...).Scan(&val); err != nil {
		e.T.Fatalf("pgQueryString failed: %v", err)
	}
	return val
}

// tryCreateCompletedReview creates a repo, commit, enqueues a job, marks it running, and completes it.
// Returns the job, review, and any error. Safe to call from goroutines (does not call t.Fatalf).
func tryCreateCompletedReview(db *DB, repoID int64, sha, author, subject, prompt, output string) (*ReviewJob, *Review, error) {
	commit, err := db.GetOrCreateCommit(repoID, sha, author, subject, time.Now())
	if err != nil {
		return nil, nil, fmt.Errorf("GetOrCreateCommit failed: %w", err)
	}
	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repoID, CommitID: commit.ID, GitRef: sha, Agent: "test"})
	if err != nil {
		return nil, nil, fmt.Errorf("EnqueueJob failed: %w", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
		return nil, nil, fmt.Errorf("failed to set job running: %w", err)
	}
	if err := db.CompleteJob(job.ID, "test", prompt, output); err != nil {
		return nil, nil, fmt.Errorf("CompleteJob failed: %w", err)
	}
	review, err := db.GetReviewByJobID(job.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("GetReviewByJobID failed: %w", err)
	}
	// Re-fetch job to get UUID
	jobs, err := db.ListJobs("", "", 1000, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("ListJobs failed: %w", err)
	}
	for _, j := range jobs {
		if j.ID == job.ID {
			job = &j
			break
		}
	}
	return job, review, nil
}

// createCompletedReview creates a repo, commit, enqueues a job, marks it running, and completes it.
// Returns the job and review. Must NOT be called from a goroutine (uses t.Fatalf).
func createCompletedReview(t *testing.T, db *DB, repoID int64, sha, author, subject, prompt, output string) (*ReviewJob, *Review) {
	t.Helper()
	job, review, err := tryCreateCompletedReview(db, repoID, sha, author, subject, prompt, output)
	if err != nil {
		t.Fatalf("createCompletedReview failed: %v", err)
	}
	return job, review
}

// startSyncWorker creates a SyncWorker with the given config, starts it, waits for connection,
// and registers cleanup. Returns the worker.
func startSyncWorker(t *testing.T, db *DB, pgURL, machineName, interval string) *SyncWorker {
	t.Helper()
	if interval == "" {
		interval = "100ms"
	}
	cfg := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    pgURL,
		Interval:       interval,
		MachineName:    machineName,
		ConnectTimeout: "5s",
	}
	worker := NewSyncWorker(db, cfg)
	if err := worker.Start(); err != nil {
		t.Fatalf("SyncWorker.Start failed for %s: %v", machineName, err)
	}
	t.Cleanup(func() { worker.Stop() })

	if err := waitForSyncWorkerConnection(worker, 10*time.Second); err != nil {
		t.Fatalf("Failed to connect for %s: %v", machineName, err)
	}
	return worker
}

// waitForSyncWorkerConnection waits for the SyncWorker to establish a postgres connection
func waitForSyncWorkerConnection(worker *SyncWorker, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := worker.SyncNow()
		if err == nil {
			return nil
		}
		if err.Error() != "not connected to PostgreSQL" {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for sync worker connection")
}

// pollForJobCount polls until the expected job count is reached or timeout
func pollForJobCount(db *DB, expected int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastCount int
	for time.Now().Before(deadline) {
		jobs, err := db.ListJobs("", "", 1000, 0)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		lastErr = nil
		lastCount = len(jobs)
		if lastCount >= expected {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("timeout waiting for %d jobs (last error: %w)", expected, lastErr)
	}
	return fmt.Errorf("timeout waiting for %d jobs, got %d", expected, lastCount)
}

func TestIntegration_SyncFullCycle(t *testing.T) {
	env := newIntegrationEnv(t, 30*time.Second)
	db := env.openDB("test.db")

	repo, err := db.GetOrCreateRepo(env.TmpDir, "git@github.com:test/integration.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	_, review := createCompletedReview(t, db, repo.ID, "abc123def456", "Test Author", "Test subject", "Test prompt", "Test review output")

	worker := startSyncWorker(t, db, env.pgURL, "integration-test", "1s")
	_ = worker

	env.assertPgCount("repos", 1)
	env.assertPgCount("review_jobs", 1)
	env.assertPgCount("reviews", 1)

	pgReviewUUID := env.pgQueryString("SELECT uuid FROM roborev.reviews")
	if pgReviewUUID != review.UUID {
		t.Errorf("Review UUID mismatch: postgres=%s, local=%s", pgReviewUUID, review.UUID)
	}
}

func TestIntegration_SyncMultipleRepos(t *testing.T) {
	env := newIntegrationEnv(t, 30*time.Second)
	db := env.openDB("test.db")

	repo1, _ := db.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo1"), "git@github.com:test/repo1.git")
	repo2, _ := db.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo2"), "git@github.com:test/repo2.git")

	sameSHA := "deadbeef12345678"
	createCompletedReview(t, db, repo1.ID, sameSHA, "Author", "Subject", "prompt", "output")
	createCompletedReview(t, db, repo2.ID, sameSHA, "Author", "Subject", "prompt", "output")

	startSyncWorker(t, db, env.pgURL, "multi-repo-test", "1s")

	env.assertPgCount("repos", 2)
	env.assertPgCountWhere("commits", "sha = $1", []interface{}{sameSHA}, 2)
}

func TestIntegration_PullFromRemote(t *testing.T) {
	env := newIntegrationEnv(t, 30*time.Second)

	// Insert data directly into postgres (simulating another machine's sync)
	remoteMachineUUID := "11111111-1111-1111-1111-111111111111"
	_, err := env.Pool.pool.Exec(env.Ctx, `
		INSERT INTO roborev.machines (machine_id, name, last_seen_at)
		VALUES ($1, 'remote-test', NOW())
	`, remoteMachineUUID)
	if err != nil {
		t.Fatalf("Failed to insert machine: %v", err)
	}

	_, err = env.Pool.pool.Exec(env.Ctx, `
		INSERT INTO roborev.repos (identity, created_at)
		VALUES ('git@github.com:test/pull-test.git', NOW())
	`)
	if err != nil {
		t.Fatalf("Failed to insert repo: %v", err)
	}

	var pgRepoID int64
	env.Pool.pool.QueryRow(env.Ctx, "SELECT id FROM roborev.repos WHERE identity = $1", "git@github.com:test/pull-test.git").Scan(&pgRepoID)

	remoteJobUUID := "22222222-2222-2222-2222-222222222222"
	_, err = env.Pool.pool.Exec(env.Ctx, `
		INSERT INTO roborev.review_jobs (uuid, repo_id, git_ref, status, agent, reasoning, job_type, review_type, source_machine_id, enqueued_at, created_at, updated_at)
		VALUES ($1, $2, 'main', 'done', 'test', '', 'review', '', $3, NOW(), NOW(), NOW())
	`, remoteJobUUID, pgRepoID, remoteMachineUUID)
	if err != nil {
		t.Fatalf("Failed to insert job: %v", err)
	}

	db := env.openDB("test.db")
	worker := startSyncWorker(t, db, env.pgURL, "local-test", "1s")

	if _, err := worker.SyncNow(); err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	jobs, err := db.ListJobs("", "", 100, 0)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("Expected 1 job in local DB, got %d", len(jobs))
	}
	if len(jobs) > 0 && jobs[0].UUID != remoteJobUUID {
		t.Errorf("Expected job UUID '%s', got %s", remoteJobUUID, jobs[0].UUID)
	}
}

func TestIntegration_FinalPush(t *testing.T) {
	env := newIntegrationEnv(t, 30*time.Second)
	db := env.openDB("test.db")

	repo, _ := db.GetOrCreateRepo(env.TmpDir, "git@github.com:test/finalpush.git")

	for i := 0; i < 150; i++ {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "HEAD", Agent: "test"})
		if err != nil {
			t.Fatalf("EnqueueRangeJob %d failed: %v", i, err)
		}
		if _, err = db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
			t.Fatalf("Update job %d to running failed: %v", i, err)
		}
		if err = db.CompleteJob(job.ID, "test", "prompt", fmt.Sprintf("output %d", i)); err != nil {
			t.Fatalf("CompleteJob %d failed: %v", i, err)
		}
	}

	worker := startSyncWorker(t, db, env.pgURL, "finalpush-test", "1h")

	if err := worker.FinalPush(); err != nil {
		t.Fatalf("FinalPush failed: %v", err)
	}

	env.assertPgCount("review_jobs", 150)
}

func TestIntegration_SchemaCreation(t *testing.T) {
	env := newIntegrationEnv(t, 30*time.Second)

	tables := []string{"machines", "repos", "commits", "review_jobs", "reviews", "responses"}
	for _, table := range tables {
		var exists bool
		err := env.Pool.pool.QueryRow(env.Ctx, `
			SELECT EXISTS (
				SELECT FROM information_schema.tables
				WHERE table_schema = 'roborev' AND table_name = $1
			)
		`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("Expected table roborev.%s to exist", table)
		}
	}
}

func TestIntegration_Multiplayer(t *testing.T) {
	env := newIntegrationEnv(t, 60*time.Second)

	dbA := env.openDB("machine_a.db")
	dbB := env.openDB("machine_b.db")

	sharedRepoIdentity := "git@github.com:test/multiplayer-repo.git"

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	jobA, reviewA := createCompletedReview(t, dbA, repoA.ID, "aaaa1111", "Alice", "Feature A", "prompt A", "Review from Machine A")

	repoB, err := dbB.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	jobB, reviewB := createCompletedReview(t, dbB, repoB.ID, "bbbb2222", "Bob", "Feature B", "prompt B", "Review from Machine B")

	workerA := startSyncWorker(t, dbA, env.pgURL, "machine-a", "100ms")
	workerB := startSyncWorker(t, dbB, env.pgURL, "machine-b", "100ms")

	// Sync to push, then sync again to pull each other's data
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: Second SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: Second SyncNow failed: %v", err)
	}

	if err := pollForJobCount(dbA, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine A: %v", err)
	}
	if err := pollForJobCount(dbB, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine B: %v", err)
	}

	env.assertPgCount("review_jobs", 2)
	env.assertPgCount("reviews", 2)

	// Verify Machine A can see Machine B's review
	jobsA, err := dbA.ListJobs("", "", 100, 0)
	if err != nil {
		t.Fatalf("Machine A: ListJobs failed: %v", err)
	}
	if len(jobsA) != 2 {
		t.Errorf("Machine A should see 2 jobs (own + pulled), got %d", len(jobsA))
	}

	var foundBinA bool
	for _, j := range jobsA {
		if j.UUID == jobB.UUID {
			foundBinA = true
			break
		}
	}
	if !foundBinA {
		t.Error("Machine A should have pulled Machine B's job")
	}

	// Verify Machine B can see Machine A's review
	jobsB, err := dbB.ListJobs("", "", 100, 0)
	if err != nil {
		t.Fatalf("Machine B: ListJobs failed: %v", err)
	}
	if len(jobsB) != 2 {
		t.Errorf("Machine B should see 2 jobs (own + pulled), got %d", len(jobsB))
	}

	var foundAinB bool
	for _, j := range jobsB {
		if j.UUID == jobA.UUID {
			foundAinB = true
			break
		}
	}
	if !foundAinB {
		t.Error("Machine B should have pulled Machine A's job")
	}

	// Verify review content was pulled correctly
	reviewBinA, err := dbA.GetReviewByCommitSHA("bbbb2222")
	if err != nil {
		t.Fatalf("Machine A: GetReviewByCommitSHA for B's commit failed: %v", err)
	}
	if reviewBinA.UUID != reviewB.UUID {
		t.Errorf("Machine A: pulled review UUID mismatch: got %s, want %s", reviewBinA.UUID, reviewB.UUID)
	}

	reviewAinB, err := dbB.GetReviewByCommitSHA("aaaa1111")
	if err != nil {
		t.Fatalf("Machine B: GetReviewByCommitSHA for A's commit failed: %v", err)
	}
	if reviewAinB.UUID != reviewA.UUID {
		t.Errorf("Machine B: pulled review UUID mismatch: got %s, want %s", reviewAinB.UUID, reviewA.UUID)
	}

	t.Log("Multiplayer sync verified: both machines can see each other's reviews")
}

func TestIntegration_MultiplayerSameCommit(t *testing.T) {
	env := newIntegrationEnv(t, 60*time.Second)

	dbA := env.openDB("machine_a.db")
	dbB := env.openDB("machine_b.db")

	sharedRepoIdentity := "git@github.com:test/same-commit-repo.git"
	sharedCommitSHA := "cccc3333"

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	jobA, reviewA := createCompletedReview(t, dbA, repoA.ID, sharedCommitSHA, "Charlie", "Shared commit", "prompt", "Machine A's review of shared commit")

	repoB, err := dbB.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	jobB, reviewB := createCompletedReview(t, dbB, repoB.ID, sharedCommitSHA, "Charlie", "Shared commit", "prompt", "Machine B's review of shared commit")

	if jobA.UUID == jobB.UUID {
		t.Fatal("Jobs from different machines should have different UUIDs")
	}

	workerA := startSyncWorker(t, dbA, env.pgURL, "machine-a", "100ms")
	workerB := startSyncWorker(t, dbB, env.pgURL, "machine-b", "100ms")

	// Sync both, then pull each other's data
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: Second SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: Second SyncNow failed: %v", err)
	}

	if err := pollForJobCount(dbA, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine A: %v", err)
	}
	if err := pollForJobCount(dbB, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine B: %v", err)
	}

	env.assertPgCount("review_jobs", 2)
	env.assertPgCount("reviews", 2)

	// Both machines should now have both jobs
	jobsA, err := dbA.ListJobs("", "", 100, 0)
	if err != nil {
		t.Fatalf("Machine A: ListJobs failed: %v", err)
	}
	jobsB, err := dbB.ListJobs("", "", 100, 0)
	if err != nil {
		t.Fatalf("Machine B: ListJobs failed: %v", err)
	}

	if len(jobsA) != 2 {
		t.Errorf("Machine A should have 2 jobs, got %d", len(jobsA))
	}
	if len(jobsB) != 2 {
		t.Errorf("Machine B should have 2 jobs, got %d", len(jobsB))
	}

	// Verify both job UUIDs are present in each machine's database
	jobsAMap := make(map[string]bool)
	for _, j := range jobsA {
		jobsAMap[j.UUID] = true
	}
	if !jobsAMap[jobA.UUID] || !jobsAMap[jobB.UUID] {
		t.Errorf("Machine A missing expected job UUIDs: has A=%v, has B=%v", jobsAMap[jobA.UUID], jobsAMap[jobB.UUID])
	}

	jobsBMap := make(map[string]bool)
	for _, j := range jobsB {
		jobsBMap[j.UUID] = true
	}
	if !jobsBMap[jobA.UUID] || !jobsBMap[jobB.UUID] {
		t.Errorf("Machine B missing expected job UUIDs: has A=%v, has B=%v", jobsBMap[jobA.UUID], jobsBMap[jobB.UUID])
	}

	// Verify reviews are present on both machines
	reviewAonA, err := dbA.GetReviewByJobID(jobA.ID)
	if err != nil {
		t.Logf("Machine A: local review A query by job ID: %v (expected for pulled jobs)", err)
	} else if reviewAonA.UUID != reviewA.UUID {
		t.Errorf("Machine A: review A UUID mismatch")
	}

	var jobBIDonA int64
	for _, j := range jobsA {
		if j.UUID == jobB.UUID {
			jobBIDonA = j.ID
			break
		}
	}
	if jobBIDonA == 0 {
		t.Error("Machine A: could not find job B by UUID")
	} else {
		reviewBonA, err := dbA.GetReviewByJobID(jobBIDonA)
		if err != nil {
			t.Errorf("Machine A: failed to get review B: %v", err)
		} else if reviewBonA.UUID != reviewB.UUID {
			t.Errorf("Machine A: review B UUID mismatch: got %s, want %s", reviewBonA.UUID, reviewB.UUID)
		}
	}

	var jobAIDonB int64
	for _, j := range jobsB {
		if j.UUID == jobA.UUID {
			jobAIDonB = j.ID
			break
		}
	}
	if jobAIDonB == 0 {
		t.Error("Machine B: could not find job A by UUID")
	} else {
		reviewAonB, err := dbB.GetReviewByJobID(jobAIDonB)
		if err != nil {
			t.Errorf("Machine B: failed to get review A: %v", err)
		} else if reviewAonB.UUID != reviewA.UUID {
			t.Errorf("Machine B: review A UUID mismatch: got %s, want %s", reviewAonB.UUID, reviewA.UUID)
		}
	}

	reviewBonB, err := dbB.GetReviewByJobID(jobB.ID)
	if err != nil {
		t.Logf("Machine B: local review B query by job ID: %v (expected for pulled jobs)", err)
	} else if reviewBonB.UUID != reviewB.UUID {
		t.Errorf("Machine B: review B UUID mismatch")
	}

	t.Log("Same-commit multiplayer verified: both reviews preserved with unique UUIDs")
}

func TestIntegration_MultiplayerRealistic(t *testing.T) {
	env := newIntegrationEnv(t, 120*time.Second)

	dbA := env.openDB("machine_a.db")
	dbB := env.openDB("machine_b.db")
	dbC := env.openDB("machine_c.db")

	sharedRepoIdentity := "git@github.com:team/shared-project.git"

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	repoB, err := dbB.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	repoC, err := dbC.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_c"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine C: GetOrCreateRepo failed: %v", err)
	}

	var jobsCreatedByA, jobsCreatedByB, jobsCreatedByC []string

	// Round 1: Each machine creates 10 reviews before any syncing
	t.Log("Round 1: Each machine creates 10 reviews (no sync yet)")
	for i := 0; i < 10; i++ {
		job, _ := createCompletedReview(t, dbA, repoA.ID, fmt.Sprintf("a1_%02d", i), "Alice", fmt.Sprintf("Alice commit %d", i), "prompt", fmt.Sprintf("Review A1-%d", i))
		jobsCreatedByA = append(jobsCreatedByA, job.UUID)

		job, _ = createCompletedReview(t, dbB, repoB.ID, fmt.Sprintf("b1_%02d", i), "Bob", fmt.Sprintf("Bob commit %d", i), "prompt", fmt.Sprintf("Review B1-%d", i))
		jobsCreatedByB = append(jobsCreatedByB, job.UUID)

		job, _ = createCompletedReview(t, dbC, repoC.ID, fmt.Sprintf("c1_%02d", i), "Carol", fmt.Sprintf("Carol commit %d", i), "prompt", fmt.Sprintf("Review C1-%d", i))
		jobsCreatedByC = append(jobsCreatedByC, job.UUID)
	}

	workerA := startSyncWorker(t, dbA, env.pgURL, "alice-laptop", "50ms")
	workerB := startSyncWorker(t, dbB, env.pgURL, "bob-desktop", "50ms")
	workerC := startSyncWorker(t, dbC, env.pgURL, "carol-workstation", "50ms")

	// First sync cycle
	t.Log("First sync cycle")
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C: SyncNow failed: %v", err)
	}
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: Second SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: Second SyncNow failed: %v", err)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C: Second SyncNow failed: %v", err)
	}

	if err := pollForJobCount(dbA, 30, 10*time.Second); err != nil {
		t.Fatalf("Machine A round 1: %v", err)
	}

	env.assertPgCount("review_jobs", 30)

	// Round 2: Interleaved - A creates 5, syncs, B creates 5, syncs, C creates 5, syncs
	t.Log("Round 2: Interleaved creation and syncing")
	for i := 0; i < 5; i++ {
		job, _ := createCompletedReview(t, dbA, repoA.ID, fmt.Sprintf("a2_%02d", i), "Alice", fmt.Sprintf("Alice round2 %d", i), "prompt", fmt.Sprintf("Review A2-%d", i))
		jobsCreatedByA = append(jobsCreatedByA, job.UUID)
	}
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A round 2: SyncNow failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		job, _ := createCompletedReview(t, dbB, repoB.ID, fmt.Sprintf("b2_%02d", i), "Bob", fmt.Sprintf("Bob round2 %d", i), "prompt", fmt.Sprintf("Review B2-%d", i))
		jobsCreatedByB = append(jobsCreatedByB, job.UUID)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B round 2: SyncNow failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		job, _ := createCompletedReview(t, dbC, repoC.ID, fmt.Sprintf("c2_%02d", i), "Carol", fmt.Sprintf("Carol round2 %d", i), "prompt", fmt.Sprintf("Review C2-%d", i))
		jobsCreatedByC = append(jobsCreatedByC, job.UUID)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C round 2: SyncNow failed: %v", err)
	}

	// All machines sync again
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A round 2 final: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B round 2 final: SyncNow failed: %v", err)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C round 2 final: SyncNow failed: %v", err)
	}

	if err := pollForJobCount(dbA, 45, 10*time.Second); err != nil {
		t.Fatalf("Machine A round 2: %v", err)
	}

	env.assertPgCount("review_jobs", 45)

	// Round 3: Concurrent creation
	t.Log("Round 3: Concurrent creation during sync")

	type jobResult struct {
		uuid string
		err  error
	}
	jobResultsA := make(chan jobResult, 10)
	jobResultsB := make(chan jobResult, 10)
	jobResultsC := make(chan jobResult, 10)
	syncErrsA := make(chan error, 4)
	syncErrsB := make(chan error, 4)
	syncErrsC := make(chan error, 4)
	done := make(chan bool, 3)

	go func() {
		for i := 0; i < 10; i++ {
			job, _, err := tryCreateCompletedReview(dbA, repoA.ID, fmt.Sprintf("a3_%02d", i), "Alice", fmt.Sprintf("Alice concurrent %d", i), "prompt", fmt.Sprintf("Review A3-%d", i))
			if err != nil {
				jobResultsA <- jobResult{err: err}
				continue
			}
			jobResultsA <- jobResult{uuid: job.UUID}
			if i%3 == 0 {
				if _, err := workerA.SyncNow(); err != nil {
					syncErrsA <- fmt.Errorf("Machine A round 3 sync at job %d: %w", i, err)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(jobResultsA)
		close(syncErrsA)
		done <- true
	}()

	go func() {
		for i := 0; i < 10; i++ {
			job, _, err := tryCreateCompletedReview(dbB, repoB.ID, fmt.Sprintf("b3_%02d", i), "Bob", fmt.Sprintf("Bob concurrent %d", i), "prompt", fmt.Sprintf("Review B3-%d", i))
			if err != nil {
				jobResultsB <- jobResult{err: err}
				continue
			}
			jobResultsB <- jobResult{uuid: job.UUID}
			if i%3 == 0 {
				if _, err := workerB.SyncNow(); err != nil {
					syncErrsB <- fmt.Errorf("Machine B round 3 sync at job %d: %w", i, err)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(jobResultsB)
		close(syncErrsB)
		done <- true
	}()

	go func() {
		for i := 0; i < 10; i++ {
			job, _, err := tryCreateCompletedReview(dbC, repoC.ID, fmt.Sprintf("c3_%02d", i), "Carol", fmt.Sprintf("Carol concurrent %d", i), "prompt", fmt.Sprintf("Review C3-%d", i))
			if err != nil {
				jobResultsC <- jobResult{err: err}
				continue
			}
			jobResultsC <- jobResult{uuid: job.UUID}
			if i%3 == 0 {
				if _, err := workerC.SyncNow(); err != nil {
					syncErrsC <- fmt.Errorf("Machine C round 3 sync at job %d: %w", i, err)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(jobResultsC)
		close(syncErrsC)
		done <- true
	}()

	<-done
	<-done
	<-done

	for r := range jobResultsA {
		if r.err != nil {
			t.Errorf("%v", r.err)
		} else {
			jobsCreatedByA = append(jobsCreatedByA, r.uuid)
		}
	}
	for r := range jobResultsB {
		if r.err != nil {
			t.Errorf("%v", r.err)
		} else {
			jobsCreatedByB = append(jobsCreatedByB, r.uuid)
		}
	}
	for r := range jobResultsC {
		if r.err != nil {
			t.Errorf("%v", r.err)
		} else {
			jobsCreatedByC = append(jobsCreatedByC, r.uuid)
		}
	}

	for err := range syncErrsA {
		t.Errorf("%v", err)
	}
	for err := range syncErrsB {
		t.Errorf("%v", err)
	}
	for err := range syncErrsC {
		t.Errorf("%v", err)
	}

	// Final sync
	t.Log("Final sync cycle")
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A final: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B final: SyncNow failed: %v", err)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C final: SyncNow failed: %v", err)
	}

	expectedTotal := 75

	if err := pollForJobCount(dbA, expectedTotal, 15*time.Second); err != nil {
		t.Fatalf("Machine A final: %v", err)
	}
	if err := pollForJobCount(dbB, expectedTotal, 15*time.Second); err != nil {
		t.Fatalf("Machine B final: %v", err)
	}
	if err := pollForJobCount(dbC, expectedTotal, 15*time.Second); err != nil {
		t.Fatalf("Machine C final: %v", err)
	}

	env.assertPgCount("review_jobs", expectedTotal)

	// Verify each machine can see all jobs
	jobsA, err := dbA.ListJobs("", "", 1000, 0)
	if err != nil {
		t.Fatalf("Machine A: ListJobs failed: %v", err)
	}
	jobsB, err := dbB.ListJobs("", "", 1000, 0)
	if err != nil {
		t.Fatalf("Machine B: ListJobs failed: %v", err)
	}
	jobsC, err := dbC.ListJobs("", "", 1000, 0)
	if err != nil {
		t.Fatalf("Machine C: ListJobs failed: %v", err)
	}

	if len(jobsA) != expectedTotal {
		t.Errorf("Machine A should see %d jobs, got %d", expectedTotal, len(jobsA))
	}
	if len(jobsB) != expectedTotal {
		t.Errorf("Machine B should see %d jobs, got %d", expectedTotal, len(jobsB))
	}
	if len(jobsC) != expectedTotal {
		t.Errorf("Machine C should see %d jobs, got %d", expectedTotal, len(jobsC))
	}

	// Verify each machine has the others' specific jobs
	jobsAMap := make(map[string]bool)
	for _, j := range jobsA {
		jobsAMap[j.UUID] = true
	}
	jobsBMap := make(map[string]bool)
	for _, j := range jobsB {
		jobsBMap[j.UUID] = true
	}
	jobsCMap := make(map[string]bool)
	for _, j := range jobsC {
		jobsCMap[j.UUID] = true
	}

	for _, uuid := range jobsCreatedByB {
		if !jobsAMap[uuid] {
			t.Errorf("Machine A missing job %s created by B", uuid)
		}
	}
	for _, uuid := range jobsCreatedByC {
		if !jobsAMap[uuid] {
			t.Errorf("Machine A missing job %s created by C", uuid)
		}
	}
	for _, uuid := range jobsCreatedByA {
		if !jobsBMap[uuid] {
			t.Errorf("Machine B missing job %s created by A", uuid)
		}
	}
	for _, uuid := range jobsCreatedByC {
		if !jobsBMap[uuid] {
			t.Errorf("Machine B missing job %s created by C", uuid)
		}
	}
	for _, uuid := range jobsCreatedByA {
		if !jobsCMap[uuid] {
			t.Errorf("Machine C missing job %s created by A", uuid)
		}
	}
	for _, uuid := range jobsCreatedByB {
		if !jobsCMap[uuid] {
			t.Errorf("Machine C missing job %s created by B", uuid)
		}
	}

	t.Logf("Realistic multiplayer test passed: %d total jobs synced across 3 machines", expectedTotal)
	t.Logf("  Machine A created: %d, Machine B created: %d, Machine C created: %d",
		len(jobsCreatedByA), len(jobsCreatedByB), len(jobsCreatedByC))
}

func TestIntegration_MultiplayerOfflineReconnect(t *testing.T) {
	env := newIntegrationEnv(t, 60*time.Second)

	dbA := env.openDB("machine_a.db")

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_a"), "git@github.com:test/offline-repo.git")
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	createCompletedReview(t, dbA, repoA.ID, "dddd4444", "Dave", "Commit 1", "prompt", "Online review")

	// Start worker, sync, then stop (simulate going offline)
	cfgA := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    env.pgURL,
		Interval:       "100ms",
		MachineName:    "machine-a",
		ConnectTimeout: "5s",
	}
	workerA := NewSyncWorker(dbA, cfgA)
	if err := workerA.Start(); err != nil {
		t.Fatalf("Machine A: SyncWorker.Start failed: %v", err)
	}
	if err := waitForSyncWorkerConnection(workerA, 10*time.Second); err != nil {
		workerA.Stop()
		t.Fatalf("Machine A: Failed to connect: %v", err)
	}
	if _, err := workerA.SyncNow(); err != nil {
		workerA.Stop()
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}
	workerA.Stop()

	// Machine A creates more reviews while "offline"
	createCompletedReview(t, dbA, repoA.ID, "eeee5555", "Dave", "Commit 2", "prompt", "Offline review 1")
	createCompletedReview(t, dbA, repoA.ID, "ffff6666", "Dave", "Commit 3", "prompt", "Offline review 2")

	env.assertPgCount("review_jobs", 1)

	// Machine A reconnects
	workerA2 := startSyncWorker(t, dbA, env.pgURL, "machine-a", "100ms")
	if _, err := workerA2.SyncNow(); err != nil {
		t.Fatalf("Machine A reconnect: SyncNow failed: %v", err)
	}

	env.assertPgCount("review_jobs", 3)

	// Machine B connects and should see all of Machine A's reviews
	dbB := env.openDB("machine_b.db")
	workerB := startSyncWorker(t, dbB, env.pgURL, "machine-b", "100ms")
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}

	if err := pollForJobCount(dbB, 3, 10*time.Second); err != nil {
		t.Fatalf("Machine B: %v", err)
	}

	jobsB, err := dbB.ListJobs("", "", 100, 0)
	if err != nil {
		t.Fatalf("Machine B: ListJobs failed: %v", err)
	}
	if len(jobsB) != 3 {
		t.Errorf("Machine B should see all 3 jobs from Machine A, got %d", len(jobsB))
	}

	t.Log("Offline/reconnect verified: reviews created offline sync correctly after reconnect")
}

func TestIntegration_SyncNowPushesAllBatches(t *testing.T) {
	env := newIntegrationEnv(t, 30*time.Second)
	db := env.openDB("test.db")

	// Start sync worker FIRST, before creating jobs
	worker := startSyncWorker(t, db, env.pgURL, "", "1h")

	repo, err := db.GetOrCreateRepo("/test/batch-sync-repo", "batch-sync-test-identity")
	if err != nil {
		t.Fatalf("Failed to create repo: %v", err)
	}

	numJobs := 80
	t.Logf("Creating %d jobs to test batch syncing", numJobs)
	for i := 0; i < numJobs; i++ {
		commit, err := db.GetOrCreateCommit(repo.ID, fmt.Sprintf("commit%03d", i), fmt.Sprintf("Author %d", i), fmt.Sprintf("Message %d", i), time.Now())
		if err != nil {
			t.Fatalf("Failed to create commit %d: %v", i, err)
		}
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: fmt.Sprintf("commit%03d", i), Agent: "test"})
		if err != nil {
			t.Fatalf("Failed to enqueue job %d: %v", i, err)
		}
		if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
			t.Fatalf("Failed to update job status %d: %v", i, err)
		}
		if err := db.CompleteJob(job.ID, "test", "test prompt", fmt.Sprintf("Review output %d", i)); err != nil {
			t.Fatalf("Failed to complete job %d: %v", i, err)
		}
	}

	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("Failed to get machine ID: %v", err)
	}
	pendingJobs, err := db.GetJobsToSync(machineID, 1000)
	if err != nil {
		t.Fatalf("Failed to get pending jobs: %v", err)
	}
	t.Logf("Pending jobs before sync: %d", len(pendingJobs))
	if len(pendingJobs) < numJobs {
		t.Fatalf("Expected %d pending jobs, got %d", numJobs, len(pendingJobs))
	}

	t.Log("Calling SyncNow to push all batches")
	stats, err := worker.SyncNow()
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	t.Logf("SyncNow stats: pushed %d jobs, %d reviews, %d responses",
		stats.PushedJobs, stats.PushedReviews, stats.PushedResponses)

	if stats.PushedJobs < numJobs {
		t.Errorf("Expected SyncNow to push %d jobs, only pushed %d", numJobs, stats.PushedJobs)
	}
	if stats.PushedReviews < numJobs {
		t.Errorf("Expected SyncNow to push %d reviews, only pushed %d", numJobs, stats.PushedReviews)
	}

	var pgJobCount int
	if err := env.Pool.pool.QueryRow(env.Ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount); err != nil {
		t.Fatalf("Failed to count jobs in postgres: %v", err)
	}
	if pgJobCount < numJobs {
		t.Errorf("Expected %d jobs in postgres, got %d", numJobs, pgJobCount)
	}

	var pgReviewCount int
	if err := env.Pool.pool.QueryRow(env.Ctx, "SELECT COUNT(*) FROM roborev.reviews").Scan(&pgReviewCount); err != nil {
		t.Fatalf("Failed to count reviews in postgres: %v", err)
	}
	if pgReviewCount < numJobs {
		t.Errorf("Expected %d reviews in postgres, got %d", numJobs, pgReviewCount)
	}

	pendingJobsAfter, err := db.GetJobsToSync(machineID, 1000)
	if err != nil {
		t.Fatalf("Failed to get pending jobs after sync: %v", err)
	}
	if len(pendingJobsAfter) > 0 {
		t.Errorf("Expected 0 pending jobs after sync, got %d", len(pendingJobsAfter))
	}

	t.Logf("Batch sync test passed: %d jobs and %d reviews pushed in batches of %d",
		stats.PushedJobs, stats.PushedReviews, syncBatchSize)
}
