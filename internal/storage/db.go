package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesm/roborev/internal/config"
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
				diff_content TEXT
			)
		`)
		if err != nil {
			return fmt.Errorf("create new review_jobs table: %w", err)
		}

		// Check which optional columns exist in source table
		var hasDiffContent, hasReasoning bool
		checkRows, checkErr := tx.Query(`SELECT name FROM pragma_table_info('review_jobs') WHERE name IN ('diff_content', 'reasoning')`)
		if checkErr == nil {
			for checkRows.Next() {
				var colName string
				checkRows.Scan(&colName)
				if colName == "diff_content" {
					hasDiffContent = true
				} else if colName == "reasoning" {
					hasReasoning = true
				}
			}
			checkRows.Close()
		}

		// Build INSERT statement based on which columns exist
		var insertSQL string
		switch {
		case hasDiffContent && hasReasoning:
			insertSQL = `
				INSERT INTO review_jobs_new (id, repo_id, commit_id, git_ref, agent, reasoning, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count, diff_content)
				SELECT id, repo_id, commit_id, git_ref, agent, reasoning, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count, diff_content
				FROM review_jobs`
		case hasDiffContent:
			insertSQL = `
				INSERT INTO review_jobs_new (id, repo_id, commit_id, git_ref, agent, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count, diff_content)
				SELECT id, repo_id, commit_id, git_ref, agent, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count, diff_content
				FROM review_jobs`
		case hasReasoning:
			insertSQL = `
				INSERT INTO review_jobs_new (id, repo_id, commit_id, git_ref, agent, reasoning, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count)
				SELECT id, repo_id, commit_id, git_ref, agent, reasoning, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count
				FROM review_jobs`
		default:
			insertSQL = `
				INSERT INTO review_jobs_new (id, repo_id, commit_id, git_ref, agent, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count)
				SELECT id, repo_id, commit_id, git_ref, agent, status, enqueued_at, started_at, finished_at, worker_id, error, prompt, retry_count
				FROM review_jobs`
		}
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
