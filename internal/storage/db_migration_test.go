package storage

import (
	"context"
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func setupOldSchemaDB(t *testing.T, dbPath string, schema string, seedData string) {
	t.Helper()
	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err, "Failed to open raw DB: %v")

	defer rawDB.Close()

	if _, err := rawDB.Exec(schema); err != nil {
		require.NoError(t, err, "Failed to create old schema: %v")
	}
	if seedData != "" {
		if _, err := rawDB.Exec(seedData); err != nil {
			require.NoError(t, err, "Failed to insert seed data: %v")
		}
	}
}

func TestMigrationFromOldSchema(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "old.db")

	// Create database with OLD schema (without 'canceled' status)
	setupOldSchemaDB(t, dbPath, `
		CREATE TABLE repos (
			id INTEGER PRIMARY KEY,
			root_path TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE commits (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			sha TEXT UNIQUE NOT NULL,
			author TEXT NOT NULL,
			subject TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE review_jobs (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			commit_id INTEGER REFERENCES commits(id),
			git_ref TEXT NOT NULL,
			agent TEXT NOT NULL DEFAULT 'codex',
			status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed')) DEFAULT 'queued',
			enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
			started_at TEXT,
			finished_at TEXT,
			worker_id TEXT,
			error TEXT,
			prompt TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE reviews (
			id INTEGER PRIMARY KEY,
			job_id INTEGER UNIQUE NOT NULL REFERENCES review_jobs(id),
			agent TEXT NOT NULL,
			prompt TEXT NOT NULL,
			output TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			addressed INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE responses (
			id INTEGER PRIMARY KEY,
			commit_id INTEGER NOT NULL REFERENCES commits(id),
			responder TEXT NOT NULL,
			response TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX idx_review_jobs_status ON review_jobs(status);
	`, `
		INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/test', 'test');
		INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
			VALUES (1, 1, 'abc123', 'Author', 'Subject', '2024-01-01');
		INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status, enqueued_at)
			VALUES (1, 1, 1, 'abc123', 'codex', 'done', '2024-01-01');
		INSERT INTO reviews (id, job_id, agent, prompt, output)
			VALUES (1, 1, 'codex', 'test prompt', 'test output');
	`)

	// Now open with our Open() function - should trigger migration
	db, err := Open(dbPath)
	require.NoError(t, err, "Open() failed after migration: %v")

	defer db.Close()

	// Verify the old data is preserved
	review, err := db.GetReviewByJobID(1)
	require.NoError(t, err, "GetReviewByJobID failed: %v")

	assert.Equal(t, "test output", review.Output, "unexpected condition")

	// Verify the new constraint allows 'canceled' status
	// Use raw SQL to insert a job, since the migration test's schema may not
	// have all columns that the high-level EnqueueJob function requires.
	repo, _ := db.GetOrCreateRepo("/tmp/test2")
	commit, _ := db.GetOrCreateCommit(repo.ID, "def456", "A", "S", time.Now())
	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, status) VALUES (?, ?, 'def456', 'codex', 'queued')`,
		repo.ID, commit.ID)
	require.NoError(t, err, "Failed to insert job: %v")

	jobID, _ := result.LastInsertId()

	// Claim the job so we can cancel it
	_, err = db.Exec(`UPDATE review_jobs SET status = 'running', worker_id = 'worker-1' WHERE id = ?`, jobID)
	require.NoError(t, err, "Failed to claim job: %v")

	// This should succeed with new schema (would fail with old constraint)
	_, err = db.Exec(`UPDATE review_jobs SET status = 'canceled' WHERE id = ?`, jobID)
	require.NoError(t, err, "Setting canceled status failed after migration: %v")

	// Verify the status was set correctly
	var status string
	err = db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, jobID).Scan(&status)
	require.NoError(t, err, "Failed to query job status: %v")

	assert.Equal(t, "canceled", status, "unexpected condition")

	// Verify 'applied' and 'rebased' statuses work after migration
	_, err = db.Exec(`UPDATE review_jobs SET status = 'done' WHERE id = ?`, jobID)
	require.NoError(t, err, "Failed to set done status: %v")

	_, err = db.Exec(`UPDATE review_jobs SET status = 'applied' WHERE id = ?`, jobID)
	require.NoError(t, err, "Setting applied status failed after migration: %v")

	_, err = db.Exec(`UPDATE review_jobs SET status = 'done' WHERE id = ?`, jobID)
	require.NoError(t, err, "Failed to reset to done: %v")

	_, err = db.Exec(`UPDATE review_jobs SET status = 'rebased' WHERE id = ?`, jobID)
	require.NoError(t, err, "Setting rebased status failed after migration: %v")

	// Verify constraint still rejects invalid status
	_, err = db.Exec(`UPDATE review_jobs SET status = 'invalid' WHERE id = ?`, jobID)
	require.Error(t, err, "unexpected condition")

	// Verify FK enforcement works after migration
	// Note: SQLite FKs are OFF by default per-connection, so we can't test that the migration
	// "left FKs enabled" in the pool. What we CAN verify is:
	// 1. The migration succeeded (which includes the FK check at the end)
	// 2. FK enforcement works when enabled on a connection
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err, "Failed to get connection: %v")

	defer conn.Close()

	// Check FK pragma value on this connection before we modify it
	// This is informational - FKs are OFF by default in SQLite
	var fkEnabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkEnabled); err != nil {
		require.NoError(t, err, "Failed to check foreign_keys pragma: %v")
	}
	t.Logf("foreign_keys pragma on pooled connection: %d", fkEnabled)

	// Enable FK enforcement and verify it works (proves schema is correct for FKs)
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		require.NoError(t, err, "Failed to enable foreign keys: %v")
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO reviews (job_id, agent, prompt, output) VALUES (99999, 'test', 'p', 'o')`)
	require.Error(t, err, "unexpected condition")
}

func TestMigrationAddsVerdictBoolColumn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Verify verdict_bool column exists
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('reviews') WHERE name = 'verdict_bool'`).Scan(&count)
	require.NoError(t, err, "Failed to check verdict_bool column: %v")

	assert.Equal(t, 1, count, "unexpected condition")

	// Verify the index exists
	var indexCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_reviews_verdict_bool'`).Scan(&indexCount)
	require.NoError(t, err, "Failed to check verdict_bool index: %v")

	assert.Equal(t, 1, indexCount, "unexpected condition")
}
