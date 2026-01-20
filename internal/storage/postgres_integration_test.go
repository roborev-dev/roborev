//go:build integration

package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wesm/roborev/internal/config"
)

// getIntegrationPostgresURL returns the postgres URL for integration tests.
// Set via TEST_POSTGRES_URL environment variable or use default from docker-compose.test.yml
func getIntegrationPostgresURL() string {
	if url := os.Getenv("TEST_POSTGRES_URL"); url != "" {
		return url
	}
	return "postgres://roborev_test:roborev_test_password@localhost:5433/roborev_test"
}

// waitForSyncWorkerConnection waits for the SyncWorker to establish a postgres connection
func waitForSyncWorkerConnection(worker *SyncWorker, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check if connected by trying SyncNow
		_, err := worker.SyncNow()
		if err == nil {
			return nil // Connected and synced
		}
		if err.Error() != "not connected to PostgreSQL" {
			return err // Some other error
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for sync worker connection")
}

func TestIntegration_SyncFullCycle(t *testing.T) {
	url := getIntegrationPostgresURL()

	// Connect to postgres
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v (is docker-compose running?)", err)
	}
	defer pool.Close()

	// Clear test data
	_, err = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err != nil {
		t.Fatalf("Failed to clear schema: %v", err)
	}

	// Ensure schema
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Create local SQLite database
	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite: %v", err)
	}
	defer db.Close()

	// Create a repo with identity
	repo, err := db.GetOrCreateRepo(tmpDir, "git@github.com:test/integration.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create a commit
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123def456", "Test Author", "Test subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Enqueue a job
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123def456", "test", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Complete the job - use ClaimJob workflow which sets status to running, then CompleteJob
	// For simplicity, directly update status and complete
	_, err = db.Exec(`UPDATE review_jobs SET status = 'running' WHERE id = ?`, job.ID)
	if err != nil {
		t.Fatalf("Failed to start job: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "Test prompt", "Test review output")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Get the review that was created
	review, err := db.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}

	// Create sync worker and start
	cfg := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "1s",
		MachineName:    "integration-test",
		ConnectTimeout: "5s",
	}

	worker := NewSyncWorker(db, cfg)
	if err := worker.Start(); err != nil {
		t.Fatalf("SyncWorker.Start failed: %v", err)
	}

	// Wait for worker to connect and perform initial sync
	if err := waitForSyncWorkerConnection(worker, 10*time.Second); err != nil {
		worker.Stop()
		t.Fatalf("Failed to connect: %v", err)
	}

	// Initial sync happened in waitForSyncWorkerConnection, verify results directly

	// Verify data in postgres
	var pgRepoCount int
	err = pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.repos").Scan(&pgRepoCount)
	if err != nil {
		t.Fatalf("Failed to query postgres repos: %v", err)
	}
	if pgRepoCount != 1 {
		t.Errorf("Expected 1 repo in postgres, got %d", pgRepoCount)
	}

	var pgJobCount int
	err = pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount)
	if err != nil {
		t.Fatalf("Failed to query postgres jobs: %v", err)
	}
	if pgJobCount != 1 {
		t.Errorf("Expected 1 job in postgres, got %d", pgJobCount)
	}

	var pgReviewCount int
	err = pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.reviews").Scan(&pgReviewCount)
	if err != nil {
		t.Fatalf("Failed to query postgres reviews: %v", err)
	}
	if pgReviewCount != 1 {
		t.Errorf("Expected 1 review in postgres, got %d", pgReviewCount)
	}

	// Verify review UUID matches
	var pgReviewUUID string
	err = pool.pool.QueryRow(ctx, "SELECT uuid FROM roborev.reviews").Scan(&pgReviewUUID)
	if err != nil {
		t.Fatalf("Failed to query review UUID: %v", err)
	}
	if pgReviewUUID != review.UUID {
		t.Errorf("Review UUID mismatch: postgres=%s, local=%s", pgReviewUUID, review.UUID)
	}

	worker.Stop()
}

func TestIntegration_SyncMultipleRepos(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear and setup schema
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite: %v", err)
	}
	defer db.Close()

	// Create two repos with same commit SHA
	repo1, _ := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo1"), "git@github.com:test/repo1.git")
	repo2, _ := db.GetOrCreateRepo(filepath.Join(tmpDir, "repo2"), "git@github.com:test/repo2.git")

	// Same SHA in different repos (valid scenario - forks, etc.)
	sameSHA := "deadbeef12345678"
	commit1, _ := db.GetOrCreateCommit(repo1.ID, sameSHA, "Author", "Subject", time.Now())
	commit2, _ := db.GetOrCreateCommit(repo2.ID, sameSHA, "Author", "Subject", time.Now())

	// Create jobs for both repos and complete them (only terminal jobs are synced)
	job1, _ := db.EnqueueJob(repo1.ID, commit1.ID, sameSHA, "test", "")
	job2, _ := db.EnqueueJob(repo2.ID, commit2.ID, sameSHA, "test", "")

	// Complete both jobs
	db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job1.ID)
	db.CompleteJob(job1.ID, "test", "prompt", "output")
	db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job2.ID)
	db.CompleteJob(job2.ID, "test", "prompt", "output")

	// Start sync
	cfg := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "1s",
		MachineName:    "multi-repo-test",
		ConnectTimeout: "5s",
	}

	worker := NewSyncWorker(db, cfg)
	if err := worker.Start(); err != nil {
		t.Fatalf("SyncWorker.Start failed: %v", err)
	}

	// Wait for worker to connect and perform initial sync
	if err := waitForSyncWorkerConnection(worker, 10*time.Second); err != nil {
		worker.Stop()
		t.Fatalf("Failed to connect: %v", err)
	}

	// Verify both repos synced
	var pgRepoCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.repos").Scan(&pgRepoCount)
	if pgRepoCount != 2 {
		t.Errorf("Expected 2 repos in postgres, got %d", pgRepoCount)
	}

	// Verify commits - both should exist (same SHA, different repos)
	var pgCommitCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.commits WHERE sha = $1", sameSHA).Scan(&pgCommitCount)
	if pgCommitCount != 2 {
		t.Errorf("Expected 2 commits with same SHA in postgres (different repos), got %d", pgCommitCount)
	}

	worker.Stop()
}

func TestIntegration_PullFromRemote(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear and setup schema
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Insert data directly into postgres (simulating another machine's sync)
	remoteMachineUUID := "11111111-1111-1111-1111-111111111111"
	_, err = pool.pool.Exec(ctx, `
		INSERT INTO roborev.machines (machine_id, name, last_seen_at)
		VALUES ($1, 'remote-test', NOW())
	`, remoteMachineUUID)
	if err != nil {
		t.Fatalf("Failed to insert machine: %v", err)
	}

	_, err = pool.pool.Exec(ctx, `
		INSERT INTO roborev.repos (identity, created_at)
		VALUES ('git@github.com:test/pull-test.git', NOW())
	`)
	if err != nil {
		t.Fatalf("Failed to insert repo: %v", err)
	}

	var pgRepoID int64
	pool.pool.QueryRow(ctx, "SELECT id FROM roborev.repos WHERE identity = $1", "git@github.com:test/pull-test.git").Scan(&pgRepoID)

	// Insert a job from the "remote" machine
	remoteJobUUID := "22222222-2222-2222-2222-222222222222"
	_, err = pool.pool.Exec(ctx, `
		INSERT INTO roborev.review_jobs (uuid, repo_id, git_ref, status, agent, reasoning, source_machine_id, enqueued_at, created_at, updated_at)
		VALUES ($1, $2, 'main', 'done', 'test', '', $3, NOW(), NOW(), NOW())
	`, remoteJobUUID, pgRepoID, remoteMachineUUID)
	if err != nil {
		t.Fatalf("Failed to insert job: %v", err)
	}

	// Create local DB
	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite: %v", err)
	}
	defer db.Close()

	// Start sync
	cfg := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "1s",
		MachineName:    "local-test",
		ConnectTimeout: "5s",
	}

	worker := NewSyncWorker(db, cfg)
	if err := worker.Start(); err != nil {
		t.Fatalf("SyncWorker.Start failed: %v", err)
	}

	// Wait for worker to connect
	if err := waitForSyncWorkerConnection(worker, 10*time.Second); err != nil {
		worker.Stop()
		t.Fatalf("Failed to connect: %v", err)
	}

	_, err = worker.SyncNow()
	if err != nil {
		worker.Stop()
		t.Fatalf("SyncNow failed: %v", err)
	}

	// The job should be in the local DB. Note: it might have been pulled by the
	// initial sync (which happens immediately upon connection) rather than this SyncNow() call,
	// so we check the DB directly rather than relying on stats.
	// Verify local DB has the pulled job
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

	worker.Stop()
}

func TestIntegration_FinalPush(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear and setup
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite: %v", err)
	}
	defer db.Close()

	// Create repo and jobs
	repo, _ := db.GetOrCreateRepo(tmpDir, "git@github.com:test/finalpush.git")

	// Create many jobs to test batch pushing (only terminal jobs are synced)
	// Use EnqueueRangeJob since we don't have actual commits in this test
	for i := 0; i < 150; i++ {
		job, err := db.EnqueueRangeJob(repo.ID, "HEAD", "test", "")
		if err != nil {
			t.Fatalf("EnqueueRangeJob %d failed: %v", i, err)
		}
		_, err = db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID)
		if err != nil {
			t.Fatalf("Update job %d to running failed: %v", i, err)
		}
		err = db.CompleteJob(job.ID, "test", "prompt", fmt.Sprintf("output %d", i))
		if err != nil {
			t.Fatalf("CompleteJob %d failed: %v", i, err)
		}
	}

	cfg := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "1h", // Long interval so we only sync via FinalPush
		MachineName:    "finalpush-test",
		ConnectTimeout: "5s",
	}

	worker := NewSyncWorker(db, cfg)
	if err := worker.Start(); err != nil {
		t.Fatalf("SyncWorker.Start failed: %v", err)
	}

	// Wait for worker to connect
	if err := waitForSyncWorkerConnection(worker, 10*time.Second); err != nil {
		worker.Stop()
		t.Fatalf("Failed to connect: %v", err)
	}

	// FinalPush should sync all pending jobs (loops until done)
	if err := worker.FinalPush(); err != nil {
		worker.Stop()
		t.Fatalf("FinalPush failed: %v", err)
	}

	// Verify all jobs synced
	var pgJobCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount)
	if pgJobCount != 150 {
		t.Errorf("Expected 150 jobs in postgres after FinalPush, got %d", pgJobCount)
	}

	worker.Stop()
}

func TestIntegration_SchemaCreation(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear everything
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")

	// EnsureSchema should create all tables
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify tables exist
	tables := []string{"machines", "repos", "commits", "review_jobs", "reviews", "responses"}
	for _, table := range tables {
		var exists bool
		err := pool.pool.QueryRow(ctx, `
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

// TestIntegration_Multiplayer simulates two machines with separate SQLite databases
// syncing to the same PostgreSQL instance - the core multiplayer use case.
func TestIntegration_Multiplayer(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear and setup schema
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Create two separate SQLite databases (simulating two machines)
	tmpDir := t.TempDir()

	dbA, err := Open(filepath.Join(tmpDir, "machine_a.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite for machine A: %v", err)
	}
	defer dbA.Close()

	dbB, err := Open(filepath.Join(tmpDir, "machine_b.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite for machine B: %v", err)
	}
	defer dbB.Close()

	// Both machines work on the same repo (identified by git remote URL)
	sharedRepoIdentity := "git@github.com:test/multiplayer-repo.git"

	// Machine A creates a repo and review
	repoA, err := dbA.GetOrCreateRepo(filepath.Join(tmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}

	commitA, err := dbA.GetOrCreateCommit(repoA.ID, "aaaa1111", "Alice", "Feature A", time.Now())
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateCommit failed: %v", err)
	}

	jobA, err := dbA.EnqueueJob(repoA.ID, commitA.ID, "aaaa1111", "test", "")
	if err != nil {
		t.Fatalf("Machine A: EnqueueJob failed: %v", err)
	}
	dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA.ID)
	if err := dbA.CompleteJob(jobA.ID, "test", "prompt A", "Review from Machine A"); err != nil {
		t.Fatalf("Machine A: CompleteJob failed: %v", err)
	}

	reviewA, err := dbA.GetReviewByJobID(jobA.ID)
	if err != nil {
		t.Fatalf("Machine A: GetReviewByJobID failed: %v", err)
	}

	// Machine B creates a repo and review (different commit)
	repoB, err := dbB.GetOrCreateRepo(filepath.Join(tmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}

	commitB, err := dbB.GetOrCreateCommit(repoB.ID, "bbbb2222", "Bob", "Feature B", time.Now())
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateCommit failed: %v", err)
	}

	jobB, err := dbB.EnqueueJob(repoB.ID, commitB.ID, "bbbb2222", "test", "")
	if err != nil {
		t.Fatalf("Machine B: EnqueueJob failed: %v", err)
	}
	dbB.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobB.ID)
	if err := dbB.CompleteJob(jobB.ID, "test", "prompt B", "Review from Machine B"); err != nil {
		t.Fatalf("Machine B: CompleteJob failed: %v", err)
	}

	reviewB, err := dbB.GetReviewByJobID(jobB.ID)
	if err != nil {
		t.Fatalf("Machine B: GetReviewByJobID failed: %v", err)
	}

	// Start sync workers for both machines
	cfgA := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms", // Fast for testing
		MachineName:    "machine-a",
		ConnectTimeout: "5s",
	}
	cfgB := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms",
		MachineName:    "machine-b",
		ConnectTimeout: "5s",
	}

	workerA := NewSyncWorker(dbA, cfgA)
	workerB := NewSyncWorker(dbB, cfgB)

	if err := workerA.Start(); err != nil {
		t.Fatalf("Machine A: SyncWorker.Start failed: %v", err)
	}
	defer workerA.Stop()

	if err := workerB.Start(); err != nil {
		t.Fatalf("Machine B: SyncWorker.Start failed: %v", err)
	}
	defer workerB.Stop()

	// Wait for both workers to connect
	if err := waitForSyncWorkerConnection(workerA, 10*time.Second); err != nil {
		t.Fatalf("Machine A: Failed to connect: %v", err)
	}
	if err := waitForSyncWorkerConnection(workerB, 10*time.Second); err != nil {
		t.Fatalf("Machine B: Failed to connect: %v", err)
	}

	// Trigger explicit syncs to ensure data is pushed
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}

	// Give a moment for data to settle, then sync again to pull each other's data
	time.Sleep(200 * time.Millisecond)

	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: Second SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: Second SyncNow failed: %v", err)
	}

	// Verify postgres has data from both machines
	var pgJobCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount)
	if pgJobCount != 2 {
		t.Errorf("Expected 2 jobs in postgres (one from each machine), got %d", pgJobCount)
	}

	var pgReviewCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.reviews").Scan(&pgReviewCount)
	if pgReviewCount != 2 {
		t.Errorf("Expected 2 reviews in postgres, got %d", pgReviewCount)
	}

	// Verify Machine A can see Machine B's review
	jobsA, err := dbA.ListJobs("", "", 100, 0)
	if err != nil {
		t.Fatalf("Machine A: ListJobs failed: %v", err)
	}
	if len(jobsA) != 2 {
		t.Errorf("Machine A should see 2 jobs (own + pulled), got %d", len(jobsA))
	}

	// Find Machine B's job in Machine A's database
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
	// Machine A should be able to read Machine B's review
	reviewBinA, err := dbA.GetReviewByCommitSHA("bbbb2222")
	if err != nil {
		t.Fatalf("Machine A: GetReviewByCommitSHA for B's commit failed: %v", err)
	}
	if reviewBinA.UUID != reviewB.UUID {
		t.Errorf("Machine A: pulled review UUID mismatch: got %s, want %s", reviewBinA.UUID, reviewB.UUID)
	}

	// Machine B should be able to read Machine A's review
	reviewAinB, err := dbB.GetReviewByCommitSHA("aaaa1111")
	if err != nil {
		t.Fatalf("Machine B: GetReviewByCommitSHA for A's commit failed: %v", err)
	}
	if reviewAinB.UUID != reviewA.UUID {
		t.Errorf("Machine B: pulled review UUID mismatch: got %s, want %s", reviewAinB.UUID, reviewA.UUID)
	}

	t.Log("Multiplayer sync verified: both machines can see each other's reviews")
}

// TestIntegration_MultiplayerSameCommit tests two machines reviewing the same commit.
// Each should produce its own review with a unique UUID.
func TestIntegration_MultiplayerSameCommit(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear and setup schema
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	tmpDir := t.TempDir()

	dbA, err := Open(filepath.Join(tmpDir, "machine_a.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite for machine A: %v", err)
	}
	defer dbA.Close()

	dbB, err := Open(filepath.Join(tmpDir, "machine_b.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite for machine B: %v", err)
	}
	defer dbB.Close()

	// Same repo identity
	sharedRepoIdentity := "git@github.com:test/same-commit-repo.git"
	sharedCommitSHA := "cccc3333"

	// Both machines review the SAME commit
	repoA, _ := dbA.GetOrCreateRepo(filepath.Join(tmpDir, "repo_a"), sharedRepoIdentity)
	commitA, _ := dbA.GetOrCreateCommit(repoA.ID, sharedCommitSHA, "Charlie", "Shared commit", time.Now())
	jobA, _ := dbA.EnqueueJob(repoA.ID, commitA.ID, sharedCommitSHA, "test", "")
	dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA.ID)
	dbA.CompleteJob(jobA.ID, "test", "prompt", "Machine A's review of shared commit")

	repoB, _ := dbB.GetOrCreateRepo(filepath.Join(tmpDir, "repo_b"), sharedRepoIdentity)
	commitB, _ := dbB.GetOrCreateCommit(repoB.ID, sharedCommitSHA, "Charlie", "Shared commit", time.Now())
	jobB, _ := dbB.EnqueueJob(repoB.ID, commitB.ID, sharedCommitSHA, "test", "")
	dbB.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobB.ID)
	dbB.CompleteJob(jobB.ID, "test", "prompt", "Machine B's review of shared commit")

	// Verify they have different UUIDs
	if jobA.UUID == jobB.UUID {
		t.Fatal("Jobs from different machines should have different UUIDs")
	}

	// Start sync workers
	cfgA := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms",
		MachineName:    "machine-a",
		ConnectTimeout: "5s",
	}
	cfgB := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms",
		MachineName:    "machine-b",
		ConnectTimeout: "5s",
	}

	workerA := NewSyncWorker(dbA, cfgA)
	workerB := NewSyncWorker(dbB, cfgB)

	workerA.Start()
	defer workerA.Stop()
	workerB.Start()
	defer workerB.Stop()

	waitForSyncWorkerConnection(workerA, 10*time.Second)
	waitForSyncWorkerConnection(workerB, 10*time.Second)

	// Sync both machines
	workerA.SyncNow()
	workerB.SyncNow()
	time.Sleep(200 * time.Millisecond)
	workerA.SyncNow()
	workerB.SyncNow()

	// Postgres should have 2 jobs and 2 reviews for the same commit
	var pgJobCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount)
	if pgJobCount != 2 {
		t.Errorf("Expected 2 jobs in postgres for same commit, got %d", pgJobCount)
	}

	var pgReviewCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.reviews").Scan(&pgReviewCount)
	if pgReviewCount != 2 {
		t.Errorf("Expected 2 reviews in postgres for same commit, got %d", pgReviewCount)
	}

	// Both machines should now have both reviews
	jobsA, _ := dbA.ListJobs("", "", 100, 0)
	jobsB, _ := dbB.ListJobs("", "", 100, 0)

	if len(jobsA) != 2 {
		t.Errorf("Machine A should have 2 jobs, got %d", len(jobsA))
	}
	if len(jobsB) != 2 {
		t.Errorf("Machine B should have 2 jobs, got %d", len(jobsB))
	}

	// Verify both reviews exist with different UUIDs
	var uuidsA []string
	for _, j := range jobsA {
		uuidsA = append(uuidsA, j.UUID)
	}

	if len(uuidsA) == 2 && uuidsA[0] == uuidsA[1] {
		t.Error("Machine A has duplicate job UUIDs - deduplication failed")
	}

	t.Log("Same-commit multiplayer verified: both reviews preserved with unique UUIDs")
}

// TestIntegration_MultiplayerRealistic simulates a realistic multiplayer scenario
// with multiple machines creating many reviews over several sync cycles.
func TestIntegration_MultiplayerRealistic(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear and setup schema
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	tmpDir := t.TempDir()

	// Create three machines (simulating a small team)
	dbA, _ := Open(filepath.Join(tmpDir, "machine_a.db"))
	defer dbA.Close()
	dbB, _ := Open(filepath.Join(tmpDir, "machine_b.db"))
	defer dbB.Close()
	dbC, _ := Open(filepath.Join(tmpDir, "machine_c.db"))
	defer dbC.Close()

	sharedRepoIdentity := "git@github.com:team/shared-project.git"

	// Helper to create a completed review
	createReview := func(db *DB, repoID int64, sha, author, subject, output string) (*ReviewJob, error) {
		commit, err := db.GetOrCreateCommit(repoID, sha, author, subject, time.Now())
		if err != nil {
			return nil, fmt.Errorf("GetOrCreateCommit: %w", err)
		}
		job, err := db.EnqueueJob(repoID, commit.ID, sha, "test", "")
		if err != nil {
			return nil, fmt.Errorf("EnqueueJob: %w", err)
		}
		db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID)
		if err := db.CompleteJob(job.ID, "test", "prompt", output); err != nil {
			return nil, fmt.Errorf("CompleteJob: %w", err)
		}
		// Re-fetch to get UUID
		jobs, _ := db.ListJobs("", "", 1000, 0)
		for _, j := range jobs {
			if j.ID == job.ID {
				return &j, nil
			}
		}
		return job, nil
	}

	// Setup repos for each machine
	repoA, _ := dbA.GetOrCreateRepo(filepath.Join(tmpDir, "repo_a"), sharedRepoIdentity)
	repoB, _ := dbB.GetOrCreateRepo(filepath.Join(tmpDir, "repo_b"), sharedRepoIdentity)
	repoC, _ := dbC.GetOrCreateRepo(filepath.Join(tmpDir, "repo_c"), sharedRepoIdentity)

	// Track all created job UUIDs per machine
	var jobsCreatedByA, jobsCreatedByB, jobsCreatedByC []string

	// Round 1: Each machine creates 10 reviews before any syncing
	t.Log("Round 1: Each machine creates 10 reviews (no sync yet)")
	for i := 0; i < 10; i++ {
		job, _ := createReview(dbA, repoA.ID, fmt.Sprintf("a1_%02d", i), "Alice", fmt.Sprintf("Alice commit %d", i), fmt.Sprintf("Review A1-%d", i))
		jobsCreatedByA = append(jobsCreatedByA, job.UUID)

		job, _ = createReview(dbB, repoB.ID, fmt.Sprintf("b1_%02d", i), "Bob", fmt.Sprintf("Bob commit %d", i), fmt.Sprintf("Review B1-%d", i))
		jobsCreatedByB = append(jobsCreatedByB, job.UUID)

		job, _ = createReview(dbC, repoC.ID, fmt.Sprintf("c1_%02d", i), "Carol", fmt.Sprintf("Carol commit %d", i), fmt.Sprintf("Review C1-%d", i))
		jobsCreatedByC = append(jobsCreatedByC, job.UUID)
	}

	// Start sync workers
	makeCfg := func(name string) config.SyncConfig {
		return config.SyncConfig{
			Enabled:        true,
			PostgresURL:    url,
			Interval:       "50ms", // Fast for testing
			MachineName:    name,
			ConnectTimeout: "5s",
		}
	}

	workerA := NewSyncWorker(dbA, makeCfg("alice-laptop"))
	workerB := NewSyncWorker(dbB, makeCfg("bob-desktop"))
	workerC := NewSyncWorker(dbC, makeCfg("carol-workstation"))

	workerA.Start()
	defer workerA.Stop()
	workerB.Start()
	defer workerB.Stop()
	workerC.Start()
	defer workerC.Stop()

	waitForSyncWorkerConnection(workerA, 10*time.Second)
	waitForSyncWorkerConnection(workerB, 10*time.Second)
	waitForSyncWorkerConnection(workerC, 10*time.Second)

	// First sync - each pushes their 10, then pulls others
	t.Log("First sync cycle")
	workerA.SyncNow()
	workerB.SyncNow()
	workerC.SyncNow()
	time.Sleep(100 * time.Millisecond)
	workerA.SyncNow()
	workerB.SyncNow()
	workerC.SyncNow()

	// Verify postgres has 30 jobs
	var pgCount int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgCount)
	if pgCount != 30 {
		t.Errorf("After round 1: expected 30 jobs in postgres, got %d", pgCount)
	}

	// Round 2: Interleaved - A creates 5, syncs, B creates 5, syncs, C creates 5, syncs
	t.Log("Round 2: Interleaved creation and syncing")
	for i := 0; i < 5; i++ {
		job, _ := createReview(dbA, repoA.ID, fmt.Sprintf("a2_%02d", i), "Alice", fmt.Sprintf("Alice round2 %d", i), fmt.Sprintf("Review A2-%d", i))
		jobsCreatedByA = append(jobsCreatedByA, job.UUID)
	}
	workerA.SyncNow()

	for i := 0; i < 5; i++ {
		job, _ := createReview(dbB, repoB.ID, fmt.Sprintf("b2_%02d", i), "Bob", fmt.Sprintf("Bob round2 %d", i), fmt.Sprintf("Review B2-%d", i))
		jobsCreatedByB = append(jobsCreatedByB, job.UUID)
	}
	workerB.SyncNow()

	for i := 0; i < 5; i++ {
		job, _ := createReview(dbC, repoC.ID, fmt.Sprintf("c2_%02d", i), "Carol", fmt.Sprintf("Carol round2 %d", i), fmt.Sprintf("Review C2-%d", i))
		jobsCreatedByC = append(jobsCreatedByC, job.UUID)
	}
	workerC.SyncNow()

	// All machines sync again to get latest
	time.Sleep(100 * time.Millisecond)
	workerA.SyncNow()
	workerB.SyncNow()
	workerC.SyncNow()

	// Verify postgres has 45 jobs
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgCount)
	if pgCount != 45 {
		t.Errorf("After round 2: expected 45 jobs in postgres, got %d", pgCount)
	}

	// Round 3: Concurrent creation - all machines create simultaneously while syncing
	t.Log("Round 3: Concurrent creation during sync")
	done := make(chan bool, 3)

	go func() {
		for i := 0; i < 10; i++ {
			job, _ := createReview(dbA, repoA.ID, fmt.Sprintf("a3_%02d", i), "Alice", fmt.Sprintf("Alice concurrent %d", i), fmt.Sprintf("Review A3-%d", i))
			jobsCreatedByA = append(jobsCreatedByA, job.UUID)
			if i%3 == 0 {
				workerA.SyncNow()
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 10; i++ {
			job, _ := createReview(dbB, repoB.ID, fmt.Sprintf("b3_%02d", i), "Bob", fmt.Sprintf("Bob concurrent %d", i), fmt.Sprintf("Review B3-%d", i))
			jobsCreatedByB = append(jobsCreatedByB, job.UUID)
			if i%3 == 0 {
				workerB.SyncNow()
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 10; i++ {
			job, _ := createReview(dbC, repoC.ID, fmt.Sprintf("c3_%02d", i), "Carol", fmt.Sprintf("Carol concurrent %d", i), fmt.Sprintf("Review C3-%d", i))
			jobsCreatedByC = append(jobsCreatedByC, job.UUID)
			if i%3 == 0 {
				workerC.SyncNow()
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done

	// Final sync to ensure everything is propagated
	t.Log("Final sync cycle")
	workerA.SyncNow()
	workerB.SyncNow()
	workerC.SyncNow()
	time.Sleep(200 * time.Millisecond)
	workerA.SyncNow()
	workerB.SyncNow()
	workerC.SyncNow()

	// Total: 30 (round 1) + 15 (round 2) + 30 (round 3) = 75 jobs
	expectedTotal := 75

	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgCount)
	if pgCount != expectedTotal {
		t.Errorf("Final: expected %d jobs in postgres, got %d", expectedTotal, pgCount)
	}

	// Verify each machine can see all jobs
	jobsA, _ := dbA.ListJobs("", "", 1000, 0)
	jobsB, _ := dbB.ListJobs("", "", 1000, 0)
	jobsC, _ := dbC.ListJobs("", "", 1000, 0)

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

	// Check A has all of B's jobs
	for _, uuid := range jobsCreatedByB {
		if !jobsAMap[uuid] {
			t.Errorf("Machine A missing job %s created by B", uuid)
		}
	}
	// Check A has all of C's jobs
	for _, uuid := range jobsCreatedByC {
		if !jobsAMap[uuid] {
			t.Errorf("Machine A missing job %s created by C", uuid)
		}
	}

	t.Logf("Realistic multiplayer test passed: %d total jobs synced across 3 machines", expectedTotal)
	t.Logf("  Machine A created: %d, Machine B created: %d, Machine C created: %d",
		len(jobsCreatedByA), len(jobsCreatedByB), len(jobsCreatedByC))
}

// TestIntegration_MultiplayerOfflineReconnect tests that a machine can go offline,
// create reviews, and sync them when it reconnects.
func TestIntegration_MultiplayerOfflineReconnect(t *testing.T) {
	url := getIntegrationPostgresURL()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}
	defer pool.Close()

	// Clear and setup schema
	_, _ = pool.pool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	tmpDir := t.TempDir()

	// Machine A starts and syncs
	dbA, _ := Open(filepath.Join(tmpDir, "machine_a.db"))
	defer dbA.Close()

	repoA, _ := dbA.GetOrCreateRepo(filepath.Join(tmpDir, "repo_a"), "git@github.com:test/offline-repo.git")
	commitA1, _ := dbA.GetOrCreateCommit(repoA.ID, "dddd4444", "Dave", "Commit 1", time.Now())
	jobA1, _ := dbA.EnqueueJob(repoA.ID, commitA1.ID, "dddd4444", "test", "")
	dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA1.ID)
	dbA.CompleteJob(jobA1.ID, "test", "prompt", "Online review")

	cfgA := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms",
		MachineName:    "machine-a",
		ConnectTimeout: "5s",
	}
	workerA := NewSyncWorker(dbA, cfgA)
	workerA.Start()

	waitForSyncWorkerConnection(workerA, 10*time.Second)
	workerA.SyncNow()

	// Stop Machine A's sync (simulating going offline)
	workerA.Stop()

	// Machine A creates more reviews while "offline" (no sync worker running)
	commitA2, _ := dbA.GetOrCreateCommit(repoA.ID, "eeee5555", "Dave", "Commit 2", time.Now())
	jobA2, _ := dbA.EnqueueJob(repoA.ID, commitA2.ID, "eeee5555", "test", "")
	dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA2.ID)
	dbA.CompleteJob(jobA2.ID, "test", "prompt", "Offline review 1")

	commitA3, _ := dbA.GetOrCreateCommit(repoA.ID, "ffff6666", "Dave", "Commit 3", time.Now())
	jobA3, _ := dbA.EnqueueJob(repoA.ID, commitA3.ID, "ffff6666", "test", "")
	dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA3.ID)
	dbA.CompleteJob(jobA3.ID, "test", "prompt", "Offline review 2")

	// Postgres should still only have 1 job (the one synced before going offline)
	var pgJobCountBefore int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCountBefore)
	if pgJobCountBefore != 1 {
		t.Errorf("Expected 1 job in postgres before reconnect, got %d", pgJobCountBefore)
	}

	// Machine A reconnects
	workerA2 := NewSyncWorker(dbA, cfgA)
	workerA2.Start()
	defer workerA2.Stop()

	waitForSyncWorkerConnection(workerA2, 10*time.Second)
	workerA2.SyncNow()

	// Now postgres should have all 3 jobs
	var pgJobCountAfter int
	pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCountAfter)
	if pgJobCountAfter != 3 {
		t.Errorf("Expected 3 jobs in postgres after reconnect, got %d", pgJobCountAfter)
	}

	// Machine B connects and should see all of Machine A's reviews (including offline ones)
	dbB, _ := Open(filepath.Join(tmpDir, "machine_b.db"))
	defer dbB.Close()

	cfgB := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms",
		MachineName:    "machine-b",
		ConnectTimeout: "5s",
	}
	workerB := NewSyncWorker(dbB, cfgB)
	workerB.Start()
	defer workerB.Stop()

	waitForSyncWorkerConnection(workerB, 10*time.Second)
	workerB.SyncNow()

	jobsB, _ := dbB.ListJobs("", "", 100, 0)
	if len(jobsB) != 3 {
		t.Errorf("Machine B should see all 3 jobs from Machine A, got %d", len(jobsB))
	}

	t.Log("Offline/reconnect verified: reviews created offline sync correctly after reconnect")
}
