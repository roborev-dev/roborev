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
