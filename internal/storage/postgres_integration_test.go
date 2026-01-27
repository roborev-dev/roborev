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
	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123def456", "", "test", "", "")
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
	job1, _ := db.EnqueueJob(repo1.ID, commit1.ID, sameSHA, "", "test", "", "")
	job2, _ := db.EnqueueJob(repo2.ID, commit2.ID, sameSHA, "", "test", "", "")

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
		job, err := db.EnqueueRangeJob(repo.ID, "HEAD", "", "test", "", "")
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

	jobA, err := dbA.EnqueueJob(repoA.ID, commitA.ID, "aaaa1111", "", "test", "", "")
	if err != nil {
		t.Fatalf("Machine A: EnqueueJob failed: %v", err)
	}
	if _, err := dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA.ID); err != nil {
		t.Fatalf("Machine A: update job status failed: %v", err)
	}
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

	jobB, err := dbB.EnqueueJob(repoB.ID, commitB.ID, "bbbb2222", "", "test", "", "")
	if err != nil {
		t.Fatalf("Machine B: EnqueueJob failed: %v", err)
	}
	if _, err := dbB.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobB.ID); err != nil {
		t.Fatalf("Machine B: update job status failed: %v", err)
	}
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

	// Trigger explicit syncs to push data
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}

	// Sync again to pull each other's data
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: Second SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: Second SyncNow failed: %v", err)
	}

	// Poll until both machines have 2 jobs
	if err := pollForJobCount(dbA, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine A: %v", err)
	}
	if err := pollForJobCount(dbB, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine B: %v", err)
	}

	// Verify postgres has data from both machines
	var pgJobCount int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount); err != nil {
		t.Fatalf("Failed to query postgres job count: %v", err)
	}
	if pgJobCount != 2 {
		t.Errorf("Expected 2 jobs in postgres (one from each machine), got %d", pgJobCount)
	}

	var pgReviewCount int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.reviews").Scan(&pgReviewCount); err != nil {
		t.Fatalf("Failed to query postgres review count: %v", err)
	}
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
	repoA, err := dbA.GetOrCreateRepo(filepath.Join(tmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	commitA, err := dbA.GetOrCreateCommit(repoA.ID, sharedCommitSHA, "Charlie", "Shared commit", time.Now())
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateCommit failed: %v", err)
	}
	jobA, err := dbA.EnqueueJob(repoA.ID, commitA.ID, sharedCommitSHA, "", "test", "", "")
	if err != nil {
		t.Fatalf("Machine A: EnqueueJob failed: %v", err)
	}
	if _, err := dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA.ID); err != nil {
		t.Fatalf("Machine A: update job status failed: %v", err)
	}
	if err := dbA.CompleteJob(jobA.ID, "test", "prompt", "Machine A's review of shared commit"); err != nil {
		t.Fatalf("Machine A: CompleteJob failed: %v", err)
	}
	reviewA, err := dbA.GetReviewByJobID(jobA.ID)
	if err != nil {
		t.Fatalf("Machine A: GetReviewByJobID failed: %v", err)
	}

	repoB, err := dbB.GetOrCreateRepo(filepath.Join(tmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	commitB, err := dbB.GetOrCreateCommit(repoB.ID, sharedCommitSHA, "Charlie", "Shared commit", time.Now())
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateCommit failed: %v", err)
	}
	jobB, err := dbB.EnqueueJob(repoB.ID, commitB.ID, sharedCommitSHA, "", "test", "", "")
	if err != nil {
		t.Fatalf("Machine B: EnqueueJob failed: %v", err)
	}
	if _, err := dbB.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobB.ID); err != nil {
		t.Fatalf("Machine B: update job status failed: %v", err)
	}
	if err := dbB.CompleteJob(jobB.ID, "test", "prompt", "Machine B's review of shared commit"); err != nil {
		t.Fatalf("Machine B: CompleteJob failed: %v", err)
	}
	reviewB, err := dbB.GetReviewByJobID(jobB.ID)
	if err != nil {
		t.Fatalf("Machine B: GetReviewByJobID failed: %v", err)
	}

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

	if err := workerA.Start(); err != nil {
		t.Fatalf("Machine A: SyncWorker.Start failed: %v", err)
	}
	defer workerA.Stop()

	if err := workerB.Start(); err != nil {
		t.Fatalf("Machine B: SyncWorker.Start failed: %v", err)
	}
	defer workerB.Stop()

	if err := waitForSyncWorkerConnection(workerA, 10*time.Second); err != nil {
		t.Fatalf("Machine A: Failed to connect: %v", err)
	}
	if err := waitForSyncWorkerConnection(workerB, 10*time.Second); err != nil {
		t.Fatalf("Machine B: Failed to connect: %v", err)
	}

	// Sync both machines
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}

	// Sync again to pull each other's data
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: Second SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: Second SyncNow failed: %v", err)
	}

	// Poll until both machines have 2 jobs
	if err := pollForJobCount(dbA, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine A: %v", err)
	}
	if err := pollForJobCount(dbB, 2, 10*time.Second); err != nil {
		t.Fatalf("Machine B: %v", err)
	}

	// Postgres should have 2 jobs and 2 reviews for the same commit
	var pgJobCount int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount); err != nil {
		t.Fatalf("Failed to query postgres job count: %v", err)
	}
	if pgJobCount != 2 {
		t.Errorf("Expected 2 jobs in postgres for same commit, got %d", pgJobCount)
	}

	var pgReviewCount int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.reviews").Scan(&pgReviewCount); err != nil {
		t.Fatalf("Failed to query postgres review count: %v", err)
	}
	if pgReviewCount != 2 {
		t.Errorf("Expected 2 reviews in postgres for same commit, got %d", pgReviewCount)
	}

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

	// Verify both reviews are actually present on both machines (not just jobs)
	// Machine A should have both reviews
	reviewAonA, err := dbA.GetReviewByJobID(jobA.ID)
	if err != nil {
		// Job A was created locally, so we can query by local ID
		t.Logf("Machine A: local review A query by job ID: %v (expected for pulled jobs)", err)
	} else if reviewAonA.UUID != reviewA.UUID {
		t.Errorf("Machine A: review A UUID mismatch")
	}

	// For pulled jobs, we need to find them by UUID in the jobs list
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

	// Machine B should have both reviews
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
	dbC, err := Open(filepath.Join(tmpDir, "machine_c.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite for machine C: %v", err)
	}
	defer dbC.Close()

	sharedRepoIdentity := "git@github.com:team/shared-project.git"

	// Helper to create a completed review
	createReview := func(db *DB, repoID int64, sha, author, subject, output string) (*ReviewJob, error) {
		commit, err := db.GetOrCreateCommit(repoID, sha, author, subject, time.Now())
		if err != nil {
			return nil, fmt.Errorf("GetOrCreateCommit: %w", err)
		}
		job, err := db.EnqueueJob(repoID, commit.ID, sha, "", "test", "", "")
		if err != nil {
			return nil, fmt.Errorf("EnqueueJob: %w", err)
		}
		if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
			return nil, fmt.Errorf("update job status: %w", err)
		}
		if err := db.CompleteJob(job.ID, "test", "prompt", output); err != nil {
			return nil, fmt.Errorf("CompleteJob: %w", err)
		}
		// Re-fetch to get UUID
		jobs, err := db.ListJobs("", "", 1000, 0)
		if err != nil {
			return nil, fmt.Errorf("ListJobs: %w", err)
		}
		for _, j := range jobs {
			if j.ID == job.ID {
				return &j, nil
			}
		}
		return job, nil
	}

	// Setup repos for each machine
	repoA, err := dbA.GetOrCreateRepo(filepath.Join(tmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	repoB, err := dbB.GetOrCreateRepo(filepath.Join(tmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	repoC, err := dbC.GetOrCreateRepo(filepath.Join(tmpDir, "repo_c"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine C: GetOrCreateRepo failed: %v", err)
	}

	// Track all created job UUIDs per machine
	var jobsCreatedByA, jobsCreatedByB, jobsCreatedByC []string

	// Round 1: Each machine creates 10 reviews before any syncing
	t.Log("Round 1: Each machine creates 10 reviews (no sync yet)")
	for i := 0; i < 10; i++ {
		job, err := createReview(dbA, repoA.ID, fmt.Sprintf("a1_%02d", i), "Alice", fmt.Sprintf("Alice commit %d", i), fmt.Sprintf("Review A1-%d", i))
		if err != nil {
			t.Fatalf("Machine A round 1: createReview failed: %v", err)
		}
		jobsCreatedByA = append(jobsCreatedByA, job.UUID)

		job, err = createReview(dbB, repoB.ID, fmt.Sprintf("b1_%02d", i), "Bob", fmt.Sprintf("Bob commit %d", i), fmt.Sprintf("Review B1-%d", i))
		if err != nil {
			t.Fatalf("Machine B round 1: createReview failed: %v", err)
		}
		jobsCreatedByB = append(jobsCreatedByB, job.UUID)

		job, err = createReview(dbC, repoC.ID, fmt.Sprintf("c1_%02d", i), "Carol", fmt.Sprintf("Carol commit %d", i), fmt.Sprintf("Review C1-%d", i))
		if err != nil {
			t.Fatalf("Machine C round 1: createReview failed: %v", err)
		}
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

	if err := workerA.Start(); err != nil {
		t.Fatalf("Machine A: SyncWorker.Start failed: %v", err)
	}
	defer workerA.Stop()
	if err := workerB.Start(); err != nil {
		t.Fatalf("Machine B: SyncWorker.Start failed: %v", err)
	}
	defer workerB.Stop()
	if err := workerC.Start(); err != nil {
		t.Fatalf("Machine C: SyncWorker.Start failed: %v", err)
	}
	defer workerC.Stop()

	if err := waitForSyncWorkerConnection(workerA, 10*time.Second); err != nil {
		t.Fatalf("Machine A: Failed to connect: %v", err)
	}
	if err := waitForSyncWorkerConnection(workerB, 10*time.Second); err != nil {
		t.Fatalf("Machine B: Failed to connect: %v", err)
	}
	if err := waitForSyncWorkerConnection(workerC, 10*time.Second); err != nil {
		t.Fatalf("Machine C: Failed to connect: %v", err)
	}

	// First sync - each pushes their 10, then pulls others
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

	// Sync again to pull others' data
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A: Second SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: Second SyncNow failed: %v", err)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C: Second SyncNow failed: %v", err)
	}

	// Poll until all machines have 30 jobs
	if err := pollForJobCount(dbA, 30, 10*time.Second); err != nil {
		t.Fatalf("Machine A round 1: %v", err)
	}

	// Verify postgres has 30 jobs
	var pgCount int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgCount); err != nil {
		t.Fatalf("Failed to query postgres job count: %v", err)
	}
	if pgCount != 30 {
		t.Errorf("After round 1: expected 30 jobs in postgres, got %d", pgCount)
	}

	// Round 2: Interleaved - A creates 5, syncs, B creates 5, syncs, C creates 5, syncs
	t.Log("Round 2: Interleaved creation and syncing")
	for i := 0; i < 5; i++ {
		job, err := createReview(dbA, repoA.ID, fmt.Sprintf("a2_%02d", i), "Alice", fmt.Sprintf("Alice round2 %d", i), fmt.Sprintf("Review A2-%d", i))
		if err != nil {
			t.Fatalf("Machine A round 2: createReview failed: %v", err)
		}
		jobsCreatedByA = append(jobsCreatedByA, job.UUID)
	}
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A round 2: SyncNow failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		job, err := createReview(dbB, repoB.ID, fmt.Sprintf("b2_%02d", i), "Bob", fmt.Sprintf("Bob round2 %d", i), fmt.Sprintf("Review B2-%d", i))
		if err != nil {
			t.Fatalf("Machine B round 2: createReview failed: %v", err)
		}
		jobsCreatedByB = append(jobsCreatedByB, job.UUID)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B round 2: SyncNow failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		job, err := createReview(dbC, repoC.ID, fmt.Sprintf("c2_%02d", i), "Carol", fmt.Sprintf("Carol round2 %d", i), fmt.Sprintf("Review C2-%d", i))
		if err != nil {
			t.Fatalf("Machine C round 2: createReview failed: %v", err)
		}
		jobsCreatedByC = append(jobsCreatedByC, job.UUID)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C round 2: SyncNow failed: %v", err)
	}

	// All machines sync again to get latest
	if _, err := workerA.SyncNow(); err != nil {
		t.Fatalf("Machine A round 2 final: SyncNow failed: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B round 2 final: SyncNow failed: %v", err)
	}
	if _, err := workerC.SyncNow(); err != nil {
		t.Fatalf("Machine C round 2 final: SyncNow failed: %v", err)
	}

	// Poll until all machines have 45 jobs
	if err := pollForJobCount(dbA, 45, 10*time.Second); err != nil {
		t.Fatalf("Machine A round 2: %v", err)
	}

	// Verify postgres has 45 jobs
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgCount); err != nil {
		t.Fatalf("Failed to query postgres job count: %v", err)
	}
	if pgCount != 45 {
		t.Errorf("After round 2: expected 45 jobs in postgres, got %d", pgCount)
	}

	// Round 3: Concurrent creation - all machines create simultaneously while syncing
	t.Log("Round 3: Concurrent creation during sync")

	// Use channels to collect job UUIDs and errors from goroutines to avoid data races
	type jobResult struct {
		uuid string
		err  error
	}
	jobResultsA := make(chan jobResult, 10)
	jobResultsB := make(chan jobResult, 10)
	jobResultsC := make(chan jobResult, 10)
	syncErrsA := make(chan error, 4) // At most 4 syncs (i=0,3,6,9)
	syncErrsB := make(chan error, 4)
	syncErrsC := make(chan error, 4)
	done := make(chan bool, 3)

	go func() {
		for i := 0; i < 10; i++ {
			job, err := createReview(dbA, repoA.ID, fmt.Sprintf("a3_%02d", i), "Alice", fmt.Sprintf("Alice concurrent %d", i), fmt.Sprintf("Review A3-%d", i))
			if err != nil {
				jobResultsA <- jobResult{err: fmt.Errorf("Machine A round 3 job %d: %w", i, err)}
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
			job, err := createReview(dbB, repoB.ID, fmt.Sprintf("b3_%02d", i), "Bob", fmt.Sprintf("Bob concurrent %d", i), fmt.Sprintf("Review B3-%d", i))
			if err != nil {
				jobResultsB <- jobResult{err: fmt.Errorf("Machine B round 3 job %d: %w", i, err)}
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
			job, err := createReview(dbC, repoC.ID, fmt.Sprintf("c3_%02d", i), "Carol", fmt.Sprintf("Carol concurrent %d", i), fmt.Sprintf("Review C3-%d", i))
			if err != nil {
				jobResultsC <- jobResult{err: fmt.Errorf("Machine C round 3 job %d: %w", i, err)}
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

	// Wait for all goroutines
	<-done
	<-done
	<-done

	// Collect job UUIDs from channels (now safe, goroutines are done)
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

	// Check for sync errors
	for err := range syncErrsA {
		t.Errorf("%v", err)
	}
	for err := range syncErrsB {
		t.Errorf("%v", err)
	}
	for err := range syncErrsC {
		t.Errorf("%v", err)
	}

	// Final sync to ensure everything is propagated
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

	// Total: 30 (round 1) + 15 (round 2) + 30 (round 3) = 75 jobs
	expectedTotal := 75

	// Poll until all machines have expected total
	if err := pollForJobCount(dbA, expectedTotal, 15*time.Second); err != nil {
		t.Fatalf("Machine A final: %v", err)
	}
	if err := pollForJobCount(dbB, expectedTotal, 15*time.Second); err != nil {
		t.Fatalf("Machine B final: %v", err)
	}
	if err := pollForJobCount(dbC, expectedTotal, 15*time.Second); err != nil {
		t.Fatalf("Machine C final: %v", err)
	}

	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgCount); err != nil {
		t.Fatalf("Failed to query postgres final job count: %v", err)
	}
	if pgCount != expectedTotal {
		t.Errorf("Final: expected %d jobs in postgres, got %d", expectedTotal, pgCount)
	}

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

	// Check A has all of B's and C's jobs
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
	// Check B has all of A's and C's jobs
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
	// Check C has all of A's and B's jobs
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
	dbA, err := Open(filepath.Join(tmpDir, "machine_a.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite for machine A: %v", err)
	}
	defer dbA.Close()

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(tmpDir, "repo_a"), "git@github.com:test/offline-repo.git")
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	commitA1, err := dbA.GetOrCreateCommit(repoA.ID, "dddd4444", "Dave", "Commit 1", time.Now())
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateCommit failed: %v", err)
	}
	jobA1, err := dbA.EnqueueJob(repoA.ID, commitA1.ID, "dddd4444", "", "test", "", "")
	if err != nil {
		t.Fatalf("Machine A: EnqueueJob failed: %v", err)
	}
	if _, err := dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA1.ID); err != nil {
		t.Fatalf("Machine A: update job status failed: %v", err)
	}
	if err := dbA.CompleteJob(jobA1.ID, "test", "prompt", "Online review"); err != nil {
		t.Fatalf("Machine A: CompleteJob failed: %v", err)
	}

	cfgA := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms",
		MachineName:    "machine-a",
		ConnectTimeout: "5s",
	}
	workerA := NewSyncWorker(dbA, cfgA)
	if err := workerA.Start(); err != nil {
		t.Fatalf("Machine A: SyncWorker.Start failed: %v", err)
	}
	// Note: we explicitly Stop() below to simulate going offline, so no defer here

	if err := waitForSyncWorkerConnection(workerA, 10*time.Second); err != nil {
		workerA.Stop()
		t.Fatalf("Machine A: Failed to connect: %v", err)
	}
	if _, err := workerA.SyncNow(); err != nil {
		workerA.Stop()
		t.Fatalf("Machine A: SyncNow failed: %v", err)
	}

	// Stop Machine A's sync (simulating going offline)
	workerA.Stop()

	// Machine A creates more reviews while "offline" (no sync worker running)
	commitA2, err := dbA.GetOrCreateCommit(repoA.ID, "eeee5555", "Dave", "Commit 2", time.Now())
	if err != nil {
		t.Fatalf("Machine A offline: GetOrCreateCommit failed: %v", err)
	}
	jobA2, err := dbA.EnqueueJob(repoA.ID, commitA2.ID, "eeee5555", "", "test", "", "")
	if err != nil {
		t.Fatalf("Machine A offline: EnqueueJob failed: %v", err)
	}
	if _, err := dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA2.ID); err != nil {
		t.Fatalf("Machine A offline: update job status failed: %v", err)
	}
	if err := dbA.CompleteJob(jobA2.ID, "test", "prompt", "Offline review 1"); err != nil {
		t.Fatalf("Machine A offline: CompleteJob failed: %v", err)
	}

	commitA3, err := dbA.GetOrCreateCommit(repoA.ID, "ffff6666", "Dave", "Commit 3", time.Now())
	if err != nil {
		t.Fatalf("Machine A offline: GetOrCreateCommit failed: %v", err)
	}
	jobA3, err := dbA.EnqueueJob(repoA.ID, commitA3.ID, "ffff6666", "", "test", "", "")
	if err != nil {
		t.Fatalf("Machine A offline: EnqueueJob failed: %v", err)
	}
	if _, err := dbA.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, jobA3.ID); err != nil {
		t.Fatalf("Machine A offline: update job status failed: %v", err)
	}
	if err := dbA.CompleteJob(jobA3.ID, "test", "prompt", "Offline review 2"); err != nil {
		t.Fatalf("Machine A offline: CompleteJob failed: %v", err)
	}

	// Postgres should still only have 1 job (the one synced before going offline)
	var pgJobCountBefore int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCountBefore); err != nil {
		t.Fatalf("Failed to query postgres job count: %v", err)
	}
	if pgJobCountBefore != 1 {
		t.Errorf("Expected 1 job in postgres before reconnect, got %d", pgJobCountBefore)
	}

	// Machine A reconnects
	workerA2 := NewSyncWorker(dbA, cfgA)
	if err := workerA2.Start(); err != nil {
		t.Fatalf("Machine A reconnect: SyncWorker.Start failed: %v", err)
	}
	defer workerA2.Stop()

	if err := waitForSyncWorkerConnection(workerA2, 10*time.Second); err != nil {
		t.Fatalf("Machine A reconnect: Failed to connect: %v", err)
	}
	if _, err := workerA2.SyncNow(); err != nil {
		t.Fatalf("Machine A reconnect: SyncNow failed: %v", err)
	}

	// Now postgres should have all 3 jobs
	var pgJobCountAfter int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCountAfter); err != nil {
		t.Fatalf("Failed to query postgres job count after reconnect: %v", err)
	}
	if pgJobCountAfter != 3 {
		t.Errorf("Expected 3 jobs in postgres after reconnect, got %d", pgJobCountAfter)
	}

	// Machine B connects and should see all of Machine A's reviews (including offline ones)
	dbB, err := Open(filepath.Join(tmpDir, "machine_b.db"))
	if err != nil {
		t.Fatalf("Failed to open SQLite for machine B: %v", err)
	}
	defer dbB.Close()

	cfgB := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "100ms",
		MachineName:    "machine-b",
		ConnectTimeout: "5s",
	}
	workerB := NewSyncWorker(dbB, cfgB)
	if err := workerB.Start(); err != nil {
		t.Fatalf("Machine B: SyncWorker.Start failed: %v", err)
	}
	defer workerB.Stop()

	if err := waitForSyncWorkerConnection(workerB, 10*time.Second); err != nil {
		t.Fatalf("Machine B: Failed to connect: %v", err)
	}
	if _, err := workerB.SyncNow(); err != nil {
		t.Fatalf("Machine B: SyncNow failed: %v", err)
	}

	// Poll until Machine B has all 3 jobs
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

// TestIntegration_SyncNowPushesAllBatches verifies that SyncNow loops until all pending items
// are pushed, not just one batch of syncBatchSize items.
func TestIntegration_SyncNowPushesAllBatches(t *testing.T) {
	url := getIntegrationPostgresURL()

	// Connect to postgres to clean up and verify
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := NewPgPool(ctx, url, DefaultPgPoolConfig())
	if err != nil {
		t.Skipf("Skipping: could not connect to postgres: %v", err)
	}

	// Clean up before test
	_, _ = pool.pool.Exec(ctx, "DELETE FROM roborev.responses")
	_, _ = pool.pool.Exec(ctx, "DELETE FROM roborev.reviews")
	_, _ = pool.pool.Exec(ctx, "DELETE FROM roborev.review_jobs")
	_, _ = pool.pool.Exec(ctx, "DELETE FROM roborev.commits")
	_, _ = pool.pool.Exec(ctx, "DELETE FROM roborev.repos")

	// Create local SQLite database
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Start sync worker FIRST, before creating jobs
	cfg := config.SyncConfig{
		Enabled:        true,
		PostgresURL:    url,
		Interval:       "1h", // Long interval so only manual SyncNow triggers sync
		ConnectTimeout: "5s",
	}
	worker := NewSyncWorker(db, cfg)
	if err := worker.Start(); err != nil {
		t.Fatalf("Failed to start sync worker: %v", err)
	}
	defer worker.Stop()

	// Wait for connection (this will sync, but there's nothing to sync yet)
	if err := waitForSyncWorkerConnection(worker, 30*time.Second); err != nil {
		t.Fatalf("Sync worker failed to connect: %v", err)
	}

	// Now create jobs AFTER the sync worker is connected
	// Create a repo with identity
	repo, err := db.GetOrCreateRepo("/test/batch-sync-repo", "batch-sync-test-identity")
	if err != nil {
		t.Fatalf("Failed to create repo: %v", err)
	}

	// Create more jobs than syncBatchSize (25) to test looping
	// Create 80 jobs (will need 4 batches of 25 to push them all)
	numJobs := 80
	t.Logf("Creating %d jobs to test batch syncing", numJobs)
	for i := 0; i < numJobs; i++ {
		commit, err := db.GetOrCreateCommit(repo.ID, fmt.Sprintf("commit%03d", i), fmt.Sprintf("Author %d", i), fmt.Sprintf("Message %d", i), time.Now())
		if err != nil {
			t.Fatalf("Failed to create commit %d: %v", i, err)
		}
		job, err := db.EnqueueJob(repo.ID, commit.ID, fmt.Sprintf("commit%03d", i), "", "test", "", "")
		if err != nil {
			t.Fatalf("Failed to enqueue job %d: %v", i, err)
		}
		// Mark job as running then complete (CompleteJob creates the review)
		if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
			t.Fatalf("Failed to update job status %d: %v", i, err)
		}
		if err := db.CompleteJob(job.ID, "test", "test prompt", fmt.Sprintf("Review output %d", i)); err != nil {
			t.Fatalf("Failed to complete job %d: %v", i, err)
		}
	}

	// Verify pending items
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

	// Call SyncNow and verify it pushes ALL items
	t.Log("Calling SyncNow to push all batches")
	stats, err := worker.SyncNow()
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	t.Logf("SyncNow stats: pushed %d jobs, %d reviews, %d responses",
		stats.PushedJobs, stats.PushedReviews, stats.PushedResponses)

	// Verify all jobs were pushed
	if stats.PushedJobs < numJobs {
		t.Errorf("Expected SyncNow to push %d jobs, only pushed %d", numJobs, stats.PushedJobs)
	}

	// Verify all reviews were pushed
	if stats.PushedReviews < numJobs {
		t.Errorf("Expected SyncNow to push %d reviews, only pushed %d", numJobs, stats.PushedReviews)
	}

	// Verify in PostgreSQL
	var pgJobCount int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.review_jobs").Scan(&pgJobCount); err != nil {
		t.Fatalf("Failed to count jobs in postgres: %v", err)
	}
	if pgJobCount < numJobs {
		t.Errorf("Expected %d jobs in postgres, got %d", numJobs, pgJobCount)
	}

	var pgReviewCount int
	if err := pool.pool.QueryRow(ctx, "SELECT COUNT(*) FROM roborev.reviews").Scan(&pgReviewCount); err != nil {
		t.Fatalf("Failed to count reviews in postgres: %v", err)
	}
	if pgReviewCount < numJobs {
		t.Errorf("Expected %d reviews in postgres, got %d", numJobs, pgReviewCount)
	}

	// Verify no more pending items
	pendingJobsAfter, err := db.GetJobsToSync(machineID, 1000)
	if err != nil {
		t.Fatalf("Failed to get pending jobs after sync: %v", err)
	}
	if len(pendingJobsAfter) > 0 {
		t.Errorf("Expected 0 pending jobs after sync, got %d", len(pendingJobsAfter))
	}

	t.Logf("Batch sync test passed: %d jobs and %d reviews pushed in batches of %d",
		stats.PushedJobs, stats.PushedReviews, syncBatchSize)

	pool.Close()
}
