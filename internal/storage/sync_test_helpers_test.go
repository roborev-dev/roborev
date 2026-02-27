package storage

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// syncTestHelper creates a test DB with common setup for sync ordering tests.
type syncTestHelper struct {
	t         *testing.T
	db        *DB
	machineID string
	repo      *Repo
}

func newSyncTestHelper(t *testing.T) *syncTestHelper {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("Failed to get machine ID: %v", err)
	}

	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("Failed to create repo: %v", err)
	}

	return &syncTestHelper{t: t, db: db, machineID: machineID, repo: repo}
}

func (h *syncTestHelper) createPendingJob(sha string) *ReviewJob {
	commit, err := h.db.GetOrCreateCommit(h.repo.ID, sha, "Author", "Subject", time.Now())
	if err != nil {
		h.t.Fatalf("Failed to create commit: %v", err)
	}
	job, err := h.db.EnqueueJob(EnqueueOpts{RepoID: h.repo.ID, CommitID: commit.ID, GitRef: sha, Agent: "test", Reasoning: "thorough"})
	if err != nil {
		h.t.Fatalf("Failed to enqueue job: %v", err)
	}
	return job
}

func (h *syncTestHelper) clearSourceMachineID(jobID int64) {
	_, err := h.db.Exec(`UPDATE review_jobs SET source_machine_id = NULL WHERE id = ?`, jobID)
	if err != nil {
		h.t.Fatalf("Failed to clear source_machine_id: %v", err)
	}
}

func (h *syncTestHelper) clearRepoIdentity(repoID int64) {
	_, err := h.db.Exec(`UPDATE repos SET identity = NULL WHERE id = ?`, repoID)
	if err != nil {
		h.t.Fatalf("Failed to clear identity: %v", err)
	}
}

// createCompletedJob creates a job, marks it done, and creates a review.
func (h *syncTestHelper) createCompletedJob(sha string) *ReviewJob {
	commit, err := h.db.GetOrCreateCommit(h.repo.ID, sha, "Author", "Subject", time.Now())
	if err != nil {
		h.t.Fatalf("Failed to create commit: %v", err)
	}
	job, err := h.db.EnqueueJob(EnqueueOpts{RepoID: h.repo.ID, CommitID: commit.ID, GitRef: sha, Agent: "test", Reasoning: "thorough"})
	if err != nil {
		h.t.Fatalf("Failed to enqueue job: %v", err)
	}
	claimed, err := h.db.ClaimJob("worker")
	if err != nil {
		h.t.Fatalf("Failed to claim job: %v", err)
	}
	if claimed == nil {
		h.t.Fatal("ClaimJob returned nil job")
	}
	if claimed.ID != job.ID {
		h.t.Fatalf("Claimed wrong job: expected %d, got %d", job.ID, claimed.ID)
	}
	err = h.db.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		h.t.Fatalf("Failed to complete job: %v", err)
	}
	return job
}

func (h *syncTestHelper) setJobTimestamps(id int64, syncedAt sql.NullString, updatedAt string) {
	var err error
	if syncedAt.Valid {
		_, err = h.db.Exec(`UPDATE review_jobs SET synced_at = ?, updated_at = ? WHERE id = ?`, syncedAt.String, updatedAt, id)
	} else {
		_, err = h.db.Exec(`UPDATE review_jobs SET synced_at = NULL, updated_at = ? WHERE id = ?`, updatedAt, id)
	}
	if err != nil {
		h.t.Fatalf("Failed to set job timestamps: %v", err)
	}
}

func (h *syncTestHelper) setReviewTimestamps(id int64, syncedAt sql.NullString, updatedAt string) {
	var err error
	if syncedAt.Valid {
		_, err = h.db.Exec(`UPDATE reviews SET synced_at = ?, updated_at = ? WHERE id = ?`, syncedAt.String, updatedAt, id)
	} else {
		_, err = h.db.Exec(`UPDATE reviews SET synced_at = NULL, updated_at = ? WHERE id = ?`, updatedAt, id)
	}
	if err != nil {
		h.t.Fatalf("Failed to set review timestamps: %v", err)
	}
}

// Legacy schema DDL constants for migration tests.

const legacySchemaV1DDL = `
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
		CREATE INDEX idx_commits_sha ON commits(sha);
	`

const legacySchemaV2DDL = `
		CREATE TABLE repos (
			id INTEGER PRIMARY KEY,
			root_path TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			identity TEXT
		);
		CREATE TABLE commits (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			sha TEXT NOT NULL,
			author TEXT NOT NULL,
			subject TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(repo_id, sha)
		);

		CREATE INDEX idx_commits_sha ON commits(sha);
	`

const legacySchemaV3DDL = `
		CREATE TABLE repos (
			id INTEGER PRIMARY KEY,
			root_path TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			identity TEXT
		);
		CREATE UNIQUE INDEX idx_repos_identity ON repos(identity) WHERE identity IS NOT NULL;
		CREATE TABLE commits (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			sha TEXT NOT NULL,
			author TEXT NOT NULL,
			subject TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(repo_id, sha)
		);

		CREATE INDEX idx_commits_sha ON commits(sha);
	`

func createLegacyCommonTables(t *testing.T, db *sql.DB) {
	_, err := db.Exec(`
		CREATE TABLE review_jobs (
			id INTEGER PRIMARY KEY,
			repo_id INTEGER NOT NULL REFERENCES repos(id),
			commit_id INTEGER REFERENCES commits(id),
			git_ref TEXT NOT NULL,
			agent TEXT NOT NULL DEFAULT 'codex',
			reasoning TEXT NOT NULL DEFAULT 'thorough',
			status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed','canceled','applied','rebased')) DEFAULT 'queued',
			enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
			started_at TEXT,
			finished_at TEXT,
			worker_id TEXT,
			error TEXT,
			prompt TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0,
			diff_content TEXT,
			agentic INTEGER NOT NULL DEFAULT 0
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
			commit_id INTEGER REFERENCES commits(id),
			job_id INTEGER REFERENCES review_jobs(id),
			responder TEXT NOT NULL,
			response TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create common legacy schema: %v", err)
	}
}

func setupLegacySchema(t *testing.T, db *sql.DB, ddl string) {
	_, err := db.Exec(ddl)
	if err != nil {
		db.Close()
		t.Fatalf("Failed to create old schema: %v", err)
	}
	createLegacyCommonTables(t, db)
}

// openRawDB opens a database without running migrations
func openRawDB(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
}
