package storage

import (
	"context"
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"strings"
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

func TestMigrationAddsSessionIDColumn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'session_id'`).Scan(&count)
	require.NoError(t, err, "Failed to check session_id column: %v")

	assert.Equal(t, 1, count, "unexpected condition")
}

func TestMigrationAddsSessionIDColumn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'session_id'`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check session_id column: %v", err)
	}
	if count != 1 {
		t.Fatal("session_id column not found in review_jobs table")
	}
}

func TestCompleteJobPopulatesVerdictBool(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/verdict-test")
	commit := createCommit(t, db, repo.ID, "verdict123")

	t.Run("pass review stores verdict_bool=1", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "verdict123", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob: %v")

		claimed := claimJob(t, db, "w1")
		assert.NotNil(t, claimed, "unexpected condition")

		err = db.CompleteJob(job.ID, "codex", "prompt", "No issues found.")
		require.NoError(t, err, "CompleteJob: %v")

		var verdictBool sql.NullInt64
		err = db.QueryRow(`SELECT verdict_bool FROM reviews WHERE job_id = ?`, job.ID).Scan(&verdictBool)
		require.NoError(t, err, "query verdict_bool: %v")

		assert.False(t, !verdictBool.Valid || verdictBool.Int64 != 1, "unexpected condition")
	})

	t.Run("fail review stores verdict_bool=0", func(t *testing.T) {
		commit2 := createCommit(t, db, repo.ID, "verdict456")
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit2.ID, GitRef: "verdict456", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob: %v")

		claimed := claimJob(t, db, "w2")
		assert.NotNil(t, claimed, "unexpected condition")

		err = db.CompleteJob(job.ID, "codex", "prompt", "- High — SQL injection in login handler")
		require.NoError(t, err, "CompleteJob: %v")

		var verdictBool sql.NullInt64
		err = db.QueryRow(`SELECT verdict_bool FROM reviews WHERE job_id = ?`, job.ID).Scan(&verdictBool)
		require.NoError(t, err, "query verdict_bool: %v")

		assert.False(t, !verdictBool.Valid || verdictBool.Int64 != 0, "unexpected condition")
	})
}

func TestMigrationQuotedTableWithOrphanedFK(t *testing.T) {
	// Regression test: after a prior migration rebuilds review_jobs via
	// ALTER TABLE ... RENAME, SQLite stores the table name quoted as
	// "review_jobs". The applied/rebased constraint migration must
	// handle this quoted form. Additionally, pre-existing orphaned FK
	// rows (referencing deleted repos) must not block the migration.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "quoted.db")

	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err, "Failed to open raw DB: %v")

	// Create base schema
	_, err = rawDB.Exec(`
		CREATE TABLE repos (
			id INTEGER PRIMARY KEY,
			root_path TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE commits (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			sha TEXT NOT NULL,
			author TEXT NOT NULL,
			subject TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE reviews (
			id INTEGER PRIMARY KEY,
			job_id INTEGER UNIQUE NOT NULL,
			agent TEXT NOT NULL,
			prompt TEXT NOT NULL,
			output TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			addressed INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE responses (
			id INTEGER PRIMARY KEY,
			commit_id INTEGER REFERENCES commits(id),
			responder TEXT NOT NULL,
			response TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		rawDB.Close()
		require.NoError(t, err, "Failed to create base schema: %v")
	}

	// Simulate a prior migration that rebuilt the table via RENAME,
	// which causes SQLite to quote the table name in sqlite_master.
	// Use the 'canceled' constraint (pre-applied/rebased).
	_, err = rawDB.Exec(`
		CREATE TABLE review_jobs_temp (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			commit_id INTEGER REFERENCES commits(id),
			git_ref TEXT NOT NULL,
			branch TEXT,
			agent TEXT NOT NULL DEFAULT 'codex',
			model TEXT,
			reasoning TEXT NOT NULL DEFAULT 'thorough',
			status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed','canceled')) DEFAULT 'queued',
			enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
			started_at TEXT,
			finished_at TEXT,
			worker_id TEXT,
			error TEXT,
			prompt TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0,
			diff_content TEXT,
			agentic INTEGER NOT NULL DEFAULT 0,
			uuid TEXT,
			source_machine_id TEXT,
			updated_at TEXT,
			synced_at TEXT,
			output_prefix TEXT,
			job_type TEXT NOT NULL DEFAULT 'review',
			review_type TEXT NOT NULL DEFAULT '',
			patch_id TEXT
		);
		ALTER TABLE review_jobs_temp RENAME TO review_jobs;
		CREATE INDEX idx_review_jobs_status ON review_jobs(status);
		CREATE INDEX idx_review_jobs_repo ON review_jobs(repo_id);
		CREATE INDEX idx_review_jobs_git_ref ON review_jobs(git_ref);
		CREATE INDEX idx_review_jobs_branch ON review_jobs(branch);
	`)
	if err != nil {
		rawDB.Close()
		require.NoError(t, err, "Failed to create renamed table: %v")
	}

	// Verify the table name is quoted in sqlite_master
	var tableSql string
	if err := rawDB.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='review_jobs'`,
	).Scan(&tableSql); err != nil {
		rawDB.Close()
		require.NoError(t, err, "Failed to read table schema: %v")
	}
	if !strings.Contains(tableSql, `"review_jobs"`) {
		rawDB.Close()
		assert.Contains(t, tableSql, `"review_jobs"`, "assertion failed: expected quoted table name, got: %s", tableSql[:min(len(tableSql), 80)])
	}

	// Insert valid data
	_, err = rawDB.Exec(`
		INSERT INTO repos (id, root_path, name)
			VALUES (1, '/tmp/test', 'test');
		INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
			VALUES (1, 1, 'abc123', 'Author', 'Subject', '2024-01-01');
		INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status)
			VALUES (1, 1, 1, 'abc123', 'codex', 'done');
		INSERT INTO reviews (id, job_id, agent, prompt, output)
			VALUES (1, 1, 'codex', 'test', 'looks good');
	`)
	if err != nil {
		rawDB.Close()
		require.NoError(t, err, "Failed to insert valid data: %v")
	}

	// Insert an orphaned row (repo_id=999 doesn't exist) to simulate
	// pre-existing FK violations
	_, err = rawDB.Exec(`
		INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status)
			VALUES (2, 999, NULL, 'orphan', 'codex', 'done');
	`)
	if err != nil {
		rawDB.Close()
		require.NoError(t, err, "Failed to insert orphaned row: %v")
	}
	rawDB.Close()

	// Open with our migration code — must not fail
	db, err := Open(dbPath)
	require.NoError(t, err, "Open() failed on quoted-table DB: %v")

	defer db.Close()

	// Verify data preserved
	review, err := db.GetReviewByJobID(1)
	require.NoError(t, err, "GetReviewByJobID failed: %v")

	assert.Equal(t, "looks good", review.Output, "unexpected condition")

	// Verify applied/rebased statuses work
	_, err = db.Exec(
		`UPDATE review_jobs SET status = 'applied' WHERE id = 1`,
	)
	require.NoError(t, err, "Setting applied status failed: %v")

	_, err = db.Exec(
		`UPDATE review_jobs SET status = 'rebased' WHERE id = 1`,
	)
	require.NoError(t, err, "Setting rebased status failed: %v")

	// Verify orphaned row still exists (migration didn't drop data)
	var count int
	db.QueryRow(`SELECT count(*) FROM review_jobs WHERE id = 2`).Scan(&count)
	assert.Equal(t, 1, count, "Orphaned row was lost during migration")
}

func TestMigrationCleansUpStaleTemp(t *testing.T) {
	// If a prior migration attempt failed and left review_jobs_new
	// behind, the next attempt should clean it up and succeed.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "stale.db")

	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err, "Failed to open raw DB: %v")

	_, err = rawDB.Exec(`
		CREATE TABLE repos (
			id INTEGER PRIMARY KEY,
			root_path TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE commits (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			sha TEXT NOT NULL,
			author TEXT NOT NULL,
			subject TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE reviews (
			id INTEGER PRIMARY KEY,
			job_id INTEGER UNIQUE NOT NULL,
			agent TEXT NOT NULL,
			prompt TEXT NOT NULL,
			output TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			addressed INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE responses (
			id INTEGER PRIMARY KEY,
			commit_id INTEGER REFERENCES commits(id),
			responder TEXT NOT NULL,
			response TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE review_jobs_temp (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			commit_id INTEGER REFERENCES commits(id),
			git_ref TEXT NOT NULL,
			branch TEXT,
			agent TEXT NOT NULL DEFAULT 'codex',
			model TEXT,
			reasoning TEXT NOT NULL DEFAULT 'thorough',
			status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed','canceled')) DEFAULT 'queued',
			enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
			started_at TEXT,
			finished_at TEXT,
			worker_id TEXT,
			error TEXT,
			prompt TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0,
			diff_content TEXT,
			agentic INTEGER NOT NULL DEFAULT 0,
			uuid TEXT,
			source_machine_id TEXT,
			updated_at TEXT,
			synced_at TEXT,
			output_prefix TEXT,
			job_type TEXT NOT NULL DEFAULT 'review',
			review_type TEXT NOT NULL DEFAULT '',
			patch_id TEXT
		);
		ALTER TABLE review_jobs_temp RENAME TO review_jobs;
		CREATE INDEX idx_review_jobs_status ON review_jobs(status);

		-- Simulate stale temp table from prior failed migration
		CREATE TABLE review_jobs_new (id INTEGER PRIMARY KEY);

		INSERT INTO repos (id, root_path, name)
			VALUES (1, '/tmp/test', 'test');
		INSERT INTO review_jobs (id, repo_id, git_ref, agent, status)
			VALUES (1, 1, 'abc', 'codex', 'done');
	`)
	if err != nil {
		rawDB.Close()
		require.NoError(t, err, "Failed to create schema: %v")
	}
	rawDB.Close()

	db, err := Open(dbPath)
	require.NoError(t, err, "Open() failed with stale temp table: %v")

	defer db.Close()

	// Verify stale temp table was cleaned up
	var tempCount int
	err = db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='review_jobs_new'`,
	).Scan(&tempCount)
	require.NoError(t, err, "Failed to check for stale temp table: %v")

	assert.Equal(t, 0, tempCount, "unexpected condition")

	// Verify applied works and row was preserved
	res, err := db.Exec(
		`UPDATE review_jobs SET status = 'applied' WHERE id = 1`,
	)
	require.NoError(t, err, "Setting applied status failed: %v")

	affected, err := res.RowsAffected()
	require.NoError(t, err, "RowsAffected: %v")

	assert.EqualValues(t, 1, affected, "unexpected condition")

	// Confirm status was actually set
	var status string
	err = db.QueryRow(
		`SELECT status FROM review_jobs WHERE id = 1`,
	).Scan(&status)
	require.NoError(t, err, "Failed to read back status: %v")

	assert.Equal(t, "applied", status, "unexpected condition")
}

func TestMigrationWithAlterTableColumnOrder(t *testing.T) {
	// Test that migration works when columns were added via ALTER TABLE,
	// which puts them at the end of the table (different from CREATE TABLE order)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "altered.db")

	// Create a very old schema WITHOUT prompt and retry_count columns
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
			error TEXT
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
	`, `ALTER TABLE review_jobs ADD COLUMN prompt TEXT;
ALTER TABLE review_jobs ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/altered', 'altered');
INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
		VALUES (1, 1, 'alter123', 'Author', 'Subject', '2024-01-01');
INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status, enqueued_at, prompt, retry_count)
		VALUES (1, 1, 1, 'alter123', 'codex', 'done', '2024-01-01', 'my prompt', 2);
INSERT INTO reviews (id, job_id, agent, prompt, output)
		VALUES (1, 1, 'codex', 'test prompt', 'test output');`)

	// Open with our Open() function - should trigger CHECK constraint migration
	// This tests that the explicit column naming in INSERT works correctly
	// even when column order differs from schema definition
	db, err := Open(dbPath)
	require.NoError(t, err, "Open() failed after migration: %v")

	defer db.Close()

	// Verify job data is preserved with correct values
	job, err := db.GetJobByID(1)
	require.NoError(t, err, "GetJobByID failed: %v")

	assert.Equal(t, "alter123", job.GitRef, "unexpected condition")
	assert.Equal(t, "codex", job.Agent, "unexpected condition")

	// Verify retry_count was preserved (query directly since GetJobByID doesn't load it)
	var retryCount int
	err = db.QueryRow(`SELECT retry_count FROM review_jobs WHERE id = 1`).Scan(&retryCount)
	require.NoError(t, err, "Failed to query retry_count: %v")

	assert.Equal(t, 2, retryCount, "unexpected condition")

	// Verify prompt was preserved
	var prompt string
	err = db.QueryRow(`SELECT prompt FROM review_jobs WHERE id = 1`).Scan(&prompt)
	require.NoError(t, err, "Failed to query prompt: %v")

	assert.Equal(t, "my prompt", prompt, "unexpected condition")

	// Verify review data is preserved
	review, err := db.GetReviewByJobID(1)
	require.NoError(t, err, "GetReviewByJobID failed: %v")

	assert.Equal(t, "test output", review.Output, "unexpected condition")

	// Verify new constraint works by creating and canceling a job.
	// Use raw SQL to insert/update the job, since the migration test's schema may
	// not have all columns that the high-level EnqueueJob function requires.
	repo2, err := db.GetOrCreateRepo("/tmp/test2")
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	commit2, err := db.GetOrCreateCommit(repo2.ID, "newsha", "A", "S", time.Now())
	require.NoError(t, err, "GetOrCreateCommit failed: %v")

	result2, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, status) VALUES (?, ?, 'newsha', 'codex', 'queued')`,
		repo2.ID, commit2.ID)
	require.NoError(t, err, "Failed to insert job: %v")

	newJobID, _ := result2.LastInsertId()

	// Claim the job
	_, err = db.Exec(`UPDATE review_jobs SET status = 'running', worker_id = 'worker-1' WHERE id = ?`, newJobID)
	require.NoError(t, err, "Failed to claim job: %v")

	// Verify the claimed status
	var claimedStatus string
	err = db.QueryRow(`SELECT status FROM review_jobs WHERE id = ?`, newJobID).Scan(&claimedStatus)
	require.NoError(t, err, "Failed to query job status: %v")

	assert.Equal(t, "running", claimedStatus, "unexpected condition")

	// Cancel - this should succeed with new schema (would fail with old constraint)
	_, err = db.Exec(`UPDATE review_jobs SET status = 'canceled' WHERE id = ?`, newJobID)
	require.NoError(t, err, "Setting canceled status failed after migration: %v")

}

func TestMigrationReasoningColumn(t *testing.T) {
	t.Run("missing reasoning gets default", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "missing_reasoning.db")

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
		`, `
			INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/test', 'test');
			INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
				VALUES (1, 1, 'abc123', 'Author', 'Subject', '2024-01-01');
			INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, status, enqueued_at)
				VALUES (1, 1, 1, 'abc123', 'codex', 'done', '2024-01-01');
		`)

		db, err := Open(dbPath)
		require.NoError(t, err, "Open() failed after migration: %v")

		defer db.Close()

		var reasoning string
		if err := db.QueryRow(`SELECT reasoning FROM review_jobs WHERE id = 1`).Scan(&reasoning); err != nil {
			require.NoError(t, err, "Failed to read reasoning: %v")
		}
		assert.Equal(t, "thorough", reasoning, "unexpected condition")
	})

	t.Run("preserves existing reasoning during rebuild", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "preserve_reasoning.db")

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
				reasoning TEXT NOT NULL DEFAULT 'thorough',
				status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed')) DEFAULT 'queued',
				enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
				started_at TEXT,
				finished_at TEXT,
				worker_id TEXT,
				error TEXT,
				prompt TEXT,
				retry_count INTEGER NOT NULL DEFAULT 0
			);
		`, `
			INSERT INTO repos (id, root_path, name) VALUES (1, '/tmp/test', 'test');
			INSERT INTO commits (id, repo_id, sha, author, subject, timestamp)
				VALUES (1, 1, 'abc123', 'Author', 'Subject', '2024-01-01');
			INSERT INTO review_jobs (id, repo_id, commit_id, git_ref, agent, reasoning, status, enqueued_at)
				VALUES (1, 1, 1, 'abc123', 'codex', 'fast', 'done', '2024-01-01');
		`)

		db, err := Open(dbPath)
		require.NoError(t, err, "Open() failed after migration: %v")

		defer db.Close()

		var reasoning string
		if err := db.QueryRow(`SELECT reasoning FROM review_jobs WHERE id = 1`).Scan(&reasoning); err != nil {
			require.NoError(t, err, "Failed to read reasoning: %v")
		}
		assert.Equal(t, "fast", reasoning, "unexpected condition")
	})
}

func TestPatchIDMigration(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'patch_id'`).Scan(&count)
	require.NoError(t, err, "check patch_id column: %v")

	assert.Equal(t, 1, count, "unexpected condition")
}
