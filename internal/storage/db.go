package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS repos (
  id INTEGER PRIMARY KEY,
  root_path TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS commits (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  sha TEXT UNIQUE NOT NULL,
  author TEXT NOT NULL,
  subject TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS review_jobs (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  commit_id INTEGER REFERENCES commits(id),
  git_ref TEXT NOT NULL,
  agent TEXT NOT NULL DEFAULT 'codex',
  reasoning TEXT NOT NULL DEFAULT 'thorough',
  status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed','canceled')) DEFAULT 'queued',
  enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
  started_at TEXT,
  finished_at TEXT,
  worker_id TEXT,
  error TEXT,
  prompt TEXT,
  retry_count INTEGER NOT NULL DEFAULT 0,
  diff_content TEXT
);

CREATE TABLE IF NOT EXISTS reviews (
  id INTEGER PRIMARY KEY,
  job_id INTEGER UNIQUE NOT NULL REFERENCES review_jobs(id),
  agent TEXT NOT NULL,
  prompt TEXT NOT NULL,
  output TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  addressed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS responses (
  id INTEGER PRIMARY KEY,
  commit_id INTEGER REFERENCES commits(id),
  responder TEXT NOT NULL,
  response TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_review_jobs_status ON review_jobs(status);
CREATE INDEX IF NOT EXISTS idx_review_jobs_repo ON review_jobs(repo_id);
CREATE INDEX IF NOT EXISTS idx_review_jobs_git_ref ON review_jobs(git_ref);
CREATE INDEX IF NOT EXISTS idx_commits_sha ON commits(sha);
`

type DB struct {
	*sql.DB
}

// DefaultDBPath returns the default database path
func DefaultDBPath() string {
	return filepath.Join(config.DataDir(), "reviews.db")
}

// Open opens or creates the database at the given path
func Open(dbPath string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	// Open with WAL mode and busy timeout
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	wrapped := &DB{db}

	// Initialize schema (CREATE IF NOT EXISTS is idempotent)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	// Run migrations for existing databases
	if err := wrapped.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return wrapped, nil
}

// migrate runs any needed migrations for existing databases
func (db *DB) migrate() error {
	// Migration: add prompt column to review_jobs if missing
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'prompt'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check prompt column: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(`ALTER TABLE review_jobs ADD COLUMN prompt TEXT`)
		if err != nil {
			return fmt.Errorf("add prompt column: %w", err)
		}
	}

	// Migration: add addressed column to reviews if missing
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('reviews') WHERE name = 'addressed'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check addressed column: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(`ALTER TABLE reviews ADD COLUMN addressed INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			return fmt.Errorf("add addressed column: %w", err)
		}
	}

	// Migration: add retry_count column to review_jobs if missing
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'retry_count'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check retry_count column: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(`ALTER TABLE review_jobs ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			return fmt.Errorf("add retry_count column: %w", err)
		}
	}

	// Migration: add diff_content column to review_jobs if missing (for dirty reviews)
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'diff_content'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check diff_content column: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(`ALTER TABLE review_jobs ADD COLUMN diff_content TEXT`)
		if err != nil {
			return fmt.Errorf("add diff_content column: %w", err)
		}
	}

	// Migration: add reasoning column to review_jobs if missing
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'reasoning'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check reasoning column: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(`ALTER TABLE review_jobs ADD COLUMN reasoning TEXT NOT NULL DEFAULT 'thorough'`)
		if err != nil {
			return fmt.Errorf("add reasoning column: %w", err)
		}
	}

	// Migration: add agentic column to review_jobs if missing
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'agentic'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check agentic column: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(`ALTER TABLE review_jobs ADD COLUMN agentic INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			return fmt.Errorf("add agentic column: %w", err)
		}
	}

	// Migration: update CHECK constraint to include 'canceled' status
	// SQLite requires table recreation to modify CHECK constraints
	var tableSql string
	err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='review_jobs'`).Scan(&tableSql)
	if err != nil {
		return fmt.Errorf("check review_jobs schema: %w", err)
	}
	// Only migrate if the old constraint exists (doesn't include 'canceled')
	if strings.Contains(tableSql, "CHECK(status IN ('queued','running','done','failed'))") {
		// Use a dedicated connection for the entire migration since PRAGMA is connection-scoped
		// This ensures FK disable/enable and the transaction all use the same connection
		ctx := context.Background()
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("get connection for migration: %w", err)
		}
		defer conn.Close()

		// Disable foreign keys for table rebuild (reviews references review_jobs)
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return fmt.Errorf("disable foreign keys: %w", err)
		}
		// Ensure FKs are re-enabled even if we return early due to error
		// This prevents returning a connection to the pool with FKs disabled
		defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)

		// Recreate table with updated constraint in a transaction for safety
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration transaction: %w", err)
		}
		defer tx.Rollback()

		_, err = tx.Exec(`
			CREATE TABLE review_jobs_new (
				id INTEGER PRIMARY KEY,
				repo_id INTEGER NOT NULL REFERENCES repos(id),
				commit_id INTEGER REFERENCES commits(id),
				git_ref TEXT NOT NULL,
				agent TEXT NOT NULL DEFAULT 'codex',
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
				agentic INTEGER NOT NULL DEFAULT 0
			)
		`)
		if err != nil {
			return fmt.Errorf("create new review_jobs table: %w", err)
		}

		// Check which optional columns exist in source table
		var hasDiffContent, hasReasoning, hasAgentic bool
		checkRows, checkErr := tx.Query(`SELECT name FROM pragma_table_info('review_jobs') WHERE name IN ('diff_content', 'reasoning', 'agentic')`)
		if checkErr == nil {
			for checkRows.Next() {
				var colName string
				checkRows.Scan(&colName)
				switch colName {
				case "diff_content":
					hasDiffContent = true
				case "reasoning":
					hasReasoning = true
				case "agentic":
					hasAgentic = true
				}
			}
			checkRows.Close()
		}

		// Build INSERT statement based on which columns exist
		// We need to handle all combinations of optional columns
		var insertSQL string
		cols := "id, repo_id, commit_id, git_ref, agent, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count"
		if hasReasoning {
			cols = "id, repo_id, commit_id, git_ref, agent, reasoning, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count"
		}
		if hasDiffContent {
			cols += ", diff_content"
		}
		if hasAgentic {
			cols += ", agentic"
		}
		insertSQL = fmt.Sprintf(`INSERT INTO review_jobs_new (%s) SELECT %s FROM review_jobs`, cols, cols)
		_, err = tx.Exec(insertSQL)
		if err != nil {
			return fmt.Errorf("copy review_jobs data: %w", err)
		}

		_, err = tx.Exec(`DROP TABLE review_jobs`)
		if err != nil {
			return fmt.Errorf("drop old review_jobs table: %w", err)
		}

		_, err = tx.Exec(`ALTER TABLE review_jobs_new RENAME TO review_jobs`)
		if err != nil {
			return fmt.Errorf("rename review_jobs table: %w", err)
		}

		_, err = tx.Exec(`
			CREATE INDEX IF NOT EXISTS idx_review_jobs_status ON review_jobs(status);
			CREATE INDEX IF NOT EXISTS idx_review_jobs_repo ON review_jobs(repo_id);
			CREATE INDEX IF NOT EXISTS idx_review_jobs_git_ref ON review_jobs(git_ref)
		`)
		if err != nil {
			return fmt.Errorf("recreate review_jobs indexes: %w", err)
		}

		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit migration transaction: %w", err)
		}

		// Re-enable foreign keys explicitly before checking (defer will also run, harmlessly)
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			return fmt.Errorf("re-enable foreign keys: %w", err)
		}

		// Verify foreign key integrity after migration
		// Use PRAGMA foreign_key_check (not table-valued function) for older SQLite compatibility
		rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			return fmt.Errorf("foreign key check failed: %w", err)
		}
		defer rows.Close()
		if rows.Next() {
			return fmt.Errorf("foreign key violations detected after migration")
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("foreign key check iteration failed: %w", err)
		}
	}

	// Migration: make commit_id nullable in responses table (for job-based responses)
	// Check if commit_id is NOT NULL by examining the schema
	var responsesSql string
	err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='responses'`).Scan(&responsesSql)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check responses schema: %w", err)
	}
	// Only migrate if commit_id is NOT NULL (old schema)
	if strings.Contains(responsesSql, "commit_id INTEGER NOT NULL") {
		// Rebuild table to make commit_id nullable
		ctx := context.Background()
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("get connection for responses migration: %w", err)
		}
		defer conn.Close()

		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return fmt.Errorf("disable foreign keys: %w", err)
		}
		defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)

		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin responses migration transaction: %w", err)
		}
		defer tx.Rollback()

		// Check if job_id column already exists in old table
		var hasJobID bool
		checkRows, _ := tx.Query(`SELECT COUNT(*) FROM pragma_table_info('responses') WHERE name = 'job_id'`)
		if checkRows != nil {
			if checkRows.Next() {
				var cnt int
				checkRows.Scan(&cnt)
				hasJobID = cnt > 0
			}
			checkRows.Close()
		}

		_, err = tx.Exec(`
			CREATE TABLE responses_new (
				id INTEGER PRIMARY KEY,
				commit_id INTEGER REFERENCES commits(id),
				job_id INTEGER REFERENCES review_jobs(id),
				responder TEXT NOT NULL,
				response TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)
		`)
		if err != nil {
			return fmt.Errorf("create new responses table: %w", err)
		}

		if hasJobID {
			_, err = tx.Exec(`
				INSERT INTO responses_new (id, commit_id, job_id, responder, response, created_at)
				SELECT id, commit_id, job_id, responder, response, created_at FROM responses
			`)
		} else {
			_, err = tx.Exec(`
				INSERT INTO responses_new (id, commit_id, responder, response, created_at)
				SELECT id, commit_id, responder, response, created_at FROM responses
			`)
		}
		if err != nil {
			return fmt.Errorf("copy responses data: %w", err)
		}

		_, err = tx.Exec(`DROP TABLE responses`)
		if err != nil {
			return fmt.Errorf("drop old responses table: %w", err)
		}

		_, err = tx.Exec(`ALTER TABLE responses_new RENAME TO responses`)
		if err != nil {
			return fmt.Errorf("rename responses table: %w", err)
		}

		_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_responses_job_id ON responses(job_id)`)
		if err != nil {
			return fmt.Errorf("create idx_responses_job_id: %w", err)
		}

		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit responses migration: %w", err)
		}

		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			return fmt.Errorf("re-enable foreign keys: %w", err)
		}
	} else {
		// Table already has nullable commit_id, just add job_id if missing
		err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('responses') WHERE name = 'job_id'`).Scan(&count)
		if err != nil {
			return fmt.Errorf("check job_id column in responses: %w", err)
		}
		if count == 0 {
			_, err = db.Exec(`ALTER TABLE responses ADD COLUMN job_id INTEGER REFERENCES review_jobs(id)`)
			if err != nil {
				return fmt.Errorf("add job_id column to responses: %w", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_responses_job_id ON responses(job_id)`)
			if err != nil {
				return fmt.Errorf("create idx_responses_job_id: %w", err)
			}
		}
	}

	// Run sync-related migrations
	if err := db.migrateSyncColumns(); err != nil {
		return err
	}

	return nil
}

// hasUniqueIndexOnShaOnly checks if commits table has a unique constraint on just sha
// (not the composite repo_id, sha constraint). Uses PRAGMA index_list/index_info for robustness.
func (db *DB) hasUniqueIndexOnShaOnly() (bool, error) {
	// Get all indexes on commits table
	rows, err := db.Query(`PRAGMA index_list('commits')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin string
		var partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return false, err
		}
		if unique == 0 {
			continue // Not a unique index
		}
		// Check if this unique index is on sha only
		// PRAGMA doesn't support parameterized queries, so we escape quotes in the name
		safeName := strings.ReplaceAll(name, "'", "''")
		infoRows, err := db.Query(fmt.Sprintf(`PRAGMA index_info('%s')`, safeName))
		if err != nil {
			return false, err
		}
		var cols []string
		for infoRows.Next() {
			var seqno, cid int
			var colName string
			if err := infoRows.Scan(&seqno, &cid, &colName); err != nil {
				infoRows.Close()
				return false, err
			}
			cols = append(cols, colName)
		}
		if err := infoRows.Err(); err != nil {
			infoRows.Close()
			return false, err
		}
		infoRows.Close()
		// If this unique index only has sha, we need to migrate
		if len(cols) == 1 && cols[0] == "sha" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migrateSyncColumns adds columns needed for PostgreSQL sync functionality.
// These migrations are idempotent - they check if columns exist before adding.
func (db *DB) migrateSyncColumns() error {
	// Helper to check if a column exists
	hasColumn := func(table, column string) (bool, error) {
		var count int
		err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count)
		return count > 0, err
	}

	// Migration: Add sync columns to review_jobs
	for _, col := range []struct {
		name string
		def  string
	}{
		{"uuid", "TEXT"},
		{"source_machine_id", "TEXT"},
		{"updated_at", "TEXT"},
		{"synced_at", "TEXT"},
	} {
		has, err := hasColumn("review_jobs", col.name)
		if err != nil {
			return fmt.Errorf("check %s column in review_jobs: %w", col.name, err)
		}
		if !has {
			_, err = db.Exec(fmt.Sprintf(`ALTER TABLE review_jobs ADD COLUMN %s %s`, col.name, col.def))
			if err != nil {
				return fmt.Errorf("add %s column to review_jobs: %w", col.name, err)
			}
		}
	}

	// Backfill UUIDs for review_jobs
	_, err := db.Exec(`UPDATE review_jobs SET uuid = ` + sqliteUUIDExpr + ` WHERE uuid IS NULL`)
	if err != nil {
		return fmt.Errorf("backfill review_jobs uuid: %w", err)
	}

	// Backfill updated_at for review_jobs (use finished_at or enqueued_at)
	_, err = db.Exec(`UPDATE review_jobs SET updated_at = COALESCE(finished_at, enqueued_at) WHERE updated_at IS NULL`)
	if err != nil {
		return fmt.Errorf("backfill review_jobs updated_at: %w", err)
	}

	// Create unique index on review_jobs.uuid
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_uuid ON review_jobs(uuid)`)
	if err != nil {
		return fmt.Errorf("create idx_review_jobs_uuid: %w", err)
	}

	// Migration: Add sync columns to reviews
	for _, col := range []struct {
		name string
		def  string
	}{
		{"uuid", "TEXT"},
		{"updated_at", "TEXT"},
		{"updated_by_machine_id", "TEXT"},
		{"synced_at", "TEXT"},
	} {
		has, err := hasColumn("reviews", col.name)
		if err != nil {
			return fmt.Errorf("check %s column in reviews: %w", col.name, err)
		}
		if !has {
			_, err = db.Exec(fmt.Sprintf(`ALTER TABLE reviews ADD COLUMN %s %s`, col.name, col.def))
			if err != nil {
				return fmt.Errorf("add %s column to reviews: %w", col.name, err)
			}
		}
	}

	// Backfill UUIDs for reviews
	_, err = db.Exec(`UPDATE reviews SET uuid = ` + sqliteUUIDExpr + ` WHERE uuid IS NULL`)
	if err != nil {
		return fmt.Errorf("backfill reviews uuid: %w", err)
	}

	// Backfill updated_at for reviews (use created_at)
	_, err = db.Exec(`UPDATE reviews SET updated_at = created_at WHERE updated_at IS NULL`)
	if err != nil {
		return fmt.Errorf("backfill reviews updated_at: %w", err)
	}

	// Create unique index on reviews.uuid
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_reviews_uuid ON reviews(uuid)`)
	if err != nil {
		return fmt.Errorf("create idx_reviews_uuid: %w", err)
	}

	// Migration: Add sync columns to responses
	for _, col := range []struct {
		name string
		def  string
	}{
		{"uuid", "TEXT"},
		{"source_machine_id", "TEXT"},
		{"synced_at", "TEXT"},
	} {
		has, err := hasColumn("responses", col.name)
		if err != nil {
			return fmt.Errorf("check %s column in responses: %w", col.name, err)
		}
		if !has {
			_, err = db.Exec(fmt.Sprintf(`ALTER TABLE responses ADD COLUMN %s %s`, col.name, col.def))
			if err != nil {
				return fmt.Errorf("add %s column to responses: %w", col.name, err)
			}
		}
	}

	// Backfill UUIDs for responses
	_, err = db.Exec(`UPDATE responses SET uuid = ` + sqliteUUIDExpr + ` WHERE uuid IS NULL`)
	if err != nil {
		return fmt.Errorf("backfill responses uuid: %w", err)
	}

	// Create unique index on responses.uuid
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_responses_uuid ON responses(uuid)`)
	if err != nil {
		return fmt.Errorf("create idx_responses_uuid: %w", err)
	}

	// Create index for GetCommentsToSync query pattern (source_machine_id + synced_at)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_responses_sync ON responses(source_machine_id, synced_at)`)
	if err != nil {
		return fmt.Errorf("create idx_responses_sync: %w", err)
	}

	// Migration: Add identity column to repos
	has, err := hasColumn("repos", "identity")
	if err != nil {
		return fmt.Errorf("check identity column in repos: %w", err)
	}
	if !has {
		_, err = db.Exec(`ALTER TABLE repos ADD COLUMN identity TEXT`)
		if err != nil {
			return fmt.Errorf("add identity column to repos: %w", err)
		}
	}

	// Create unique index on repos.identity (allows NULL values, only enforces uniqueness on non-NULL)
	// First normalize empty strings to NULL (treat empty as "unset")
	_, err = db.Exec(`UPDATE repos SET identity = NULL WHERE identity = ''`)
	if err != nil {
		return fmt.Errorf("normalize empty identities to NULL: %w", err)
	}

	// Check for duplicates that would prevent index creation
	var dupCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT identity FROM repos WHERE identity IS NOT NULL
		GROUP BY identity HAVING COUNT(*) > 1
	)`).Scan(&dupCount)
	if err != nil {
		return fmt.Errorf("check duplicate identities: %w", err)
	}
	if dupCount > 0 {
		return fmt.Errorf("cannot create unique index on repos.identity: %d duplicate non-NULL identities exist; resolve duplicates before upgrading", dupCount)
	}

	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_repos_identity ON repos(identity) WHERE identity IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("create idx_repos_identity: %w", err)
	}

	// Migration: Create sync_state table for tracking sync status
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS sync_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("create sync_state table: %w", err)
	}

	// Migration: Align commits uniqueness to UNIQUE(repo_id, sha) instead of just UNIQUE(sha)
	// Check if we need to migrate by checking for a unique index on just sha (not repo_id, sha)
	needsCommitsMigration, err := db.hasUniqueIndexOnShaOnly()
	if err != nil {
		return fmt.Errorf("check commits unique constraint: %w", err)
	}

	if needsCommitsMigration {
		// Need to rebuild table. Use a dedicated connection since PRAGMA is connection-scoped.
		ctx := context.Background()
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("get connection for commits migration: %w", err)
		}
		defer conn.Close()

		// Disable foreign keys OUTSIDE transaction (SQLite ignores inside tx)
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return fmt.Errorf("disable foreign keys for commits: %w", err)
		}
		defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)

		// Run rebuild in a transaction for atomicity
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin commits migration transaction: %w", err)
		}
		defer tx.Rollback()

		// Step 1: Create backup
		_, err = tx.Exec(`CREATE TABLE commits_backup AS SELECT * FROM commits`)
		if err != nil {
			return fmt.Errorf("create commits_backup: %w", err)
		}

		// Step 2: Drop original
		_, err = tx.Exec(`DROP TABLE commits`)
		if err != nil {
			return fmt.Errorf("drop commits: %w", err)
		}

		// Step 3: Create new table with UNIQUE(repo_id, sha)
		_, err = tx.Exec(`
			CREATE TABLE commits (
				id INTEGER PRIMARY KEY,
				repo_id INTEGER NOT NULL REFERENCES repos(id),
				sha TEXT NOT NULL,
				author TEXT NOT NULL,
				subject TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(repo_id, sha)
			)
		`)
		if err != nil {
			return fmt.Errorf("create new commits table: %w", err)
		}

		// Step 4: Copy data from backup
		_, err = tx.Exec(`INSERT INTO commits SELECT * FROM commits_backup`)
		if err != nil {
			return fmt.Errorf("copy commits data: %w", err)
		}

		// Step 5: Drop backup
		_, err = tx.Exec(`DROP TABLE commits_backup`)
		if err != nil {
			return fmt.Errorf("drop commits_backup: %w", err)
		}

		// Step 6: Recreate index
		_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commits_sha ON commits(sha)`)
		if err != nil {
			return fmt.Errorf("recreate idx_commits_sha: %w", err)
		}

		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit commits migration: %w", err)
		}

		// Re-enable foreign keys and verify
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			return fmt.Errorf("re-enable foreign keys: %w", err)
		}

		// Verify foreign key integrity
		rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			return fmt.Errorf("foreign key check failed: %w", err)
		}
		defer rows.Close()
		if rows.Next() {
			return fmt.Errorf("foreign key violations detected after commits migration")
		}
	}

	return nil
}

// ResetStaleJobs marks all running jobs as queued (for daemon restart)
func (db *DB) ResetStaleJobs() error {
	_, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'queued', worker_id = NULL, started_at = NULL
		WHERE status = 'running'
	`)
	return err
}

// CountStalledJobs returns the number of jobs that have been running longer than the threshold
func (db *DB) CountStalledJobs(threshold time.Duration) (int, error) {
	// Use threshold in seconds for SQLite datetime arithmetic
	// This avoids timezone issues with RFC3339 string comparison
	thresholdSecs := int64(threshold.Seconds())

	var count int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM review_jobs
		WHERE status = 'running'
		AND started_at IS NOT NULL
		AND datetime(started_at) < datetime('now', ? || ' seconds')
	`, -thresholdSecs).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}
