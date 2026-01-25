package storage

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	_ "modernc.org/sqlite"
)

func TestSyncState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	t.Run("get nonexistent key returns empty", func(t *testing.T) {
		val, err := db.GetSyncState("nonexistent")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if val != "" {
			t.Errorf("Expected empty string, got %q", val)
		}
	})

	t.Run("set and get", func(t *testing.T) {
		err := db.SetSyncState("test_key", "test_value")
		if err != nil {
			t.Fatalf("SetSyncState failed: %v", err)
		}

		val, err := db.GetSyncState("test_key")
		if err != nil {
			t.Fatalf("GetSyncState failed: %v", err)
		}
		if val != "test_value" {
			t.Errorf("Expected 'test_value', got %q", val)
		}
	})

	t.Run("upsert overwrites", func(t *testing.T) {
		err := db.SetSyncState("upsert_key", "first")
		if err != nil {
			t.Fatalf("SetSyncState failed: %v", err)
		}

		err = db.SetSyncState("upsert_key", "second")
		if err != nil {
			t.Fatalf("SetSyncState upsert failed: %v", err)
		}

		val, err := db.GetSyncState("upsert_key")
		if err != nil {
			t.Fatalf("GetSyncState failed: %v", err)
		}
		if val != "second" {
			t.Errorf("Expected 'second', got %q", val)
		}
	})
}

func TestGetMachineID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// UUID pattern
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	t.Run("generates new ID on first call", func(t *testing.T) {
		id, err := db.GetMachineID()
		if err != nil {
			t.Fatalf("GetMachineID failed: %v", err)
		}
		if !uuidPattern.MatchString(id) {
			t.Errorf("Machine ID %q is not a valid UUID", id)
		}
	})

	t.Run("returns same ID on subsequent calls", func(t *testing.T) {
		id1, err := db.GetMachineID()
		if err != nil {
			t.Fatalf("First GetMachineID failed: %v", err)
		}

		id2, err := db.GetMachineID()
		if err != nil {
			t.Fatalf("Second GetMachineID failed: %v", err)
		}

		if id1 != id2 {
			t.Errorf("Machine IDs differ: %q vs %q", id1, id2)
		}
	})
}

func TestBackfillSourceMachineID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create test data
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create a commit for the job
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Test Author", "Test Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	job, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "test", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Verify source_machine_id is initially NULL (simulating legacy data)
	var sourceMachineID *string
	err = db.QueryRow(`SELECT source_machine_id FROM review_jobs WHERE id = ?`, job.ID).Scan(&sourceMachineID)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	// After migration, backfill runs automatically, so it may already have a value
	// Let's clear it to test the backfill
	_, err = db.Exec(`UPDATE review_jobs SET source_machine_id = NULL WHERE id = ?`, job.ID)
	if err != nil {
		t.Fatalf("Failed to clear source_machine_id: %v", err)
	}

	// Run backfill
	err = db.BackfillSourceMachineID()
	if err != nil {
		t.Fatalf("BackfillSourceMachineID failed: %v", err)
	}

	// Verify source_machine_id is now set
	var newSourceMachineID string
	err = db.QueryRow(`SELECT source_machine_id FROM review_jobs WHERE id = ?`, job.ID).Scan(&newSourceMachineID)
	if err != nil {
		t.Fatalf("Query after backfill failed: %v", err)
	}

	machineID, _ := db.GetMachineID()
	if newSourceMachineID != machineID {
		t.Errorf("Expected source_machine_id %q, got %q", machineID, newSourceMachineID)
	}
}

func TestBackfillRepoIdentities_LocalRepoFallback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create a git repo without a remote configured
	tempDir := t.TempDir()
	cmd := exec.Command("git", "init", tempDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed (git not available?): %v", err)
	}

	repo, err := db.GetOrCreateRepo(tempDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Clear identity to simulate legacy repo
	_, err = db.Exec(`UPDATE repos SET identity = NULL WHERE id = ?`, repo.ID)
	if err != nil {
		t.Fatalf("Failed to clear identity: %v", err)
	}

	// Backfill should use local: prefix with repo name (git repo, no remote)
	count, err := db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 repo backfilled, got %d", count)
	}

	// Verify identity was set with local: prefix
	var identity string
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repo.ID).Scan(&identity)
	if err != nil {
		t.Fatalf("Query identity failed: %v", err)
	}

	expectedPrefix := "local:"
	if !strings.HasPrefix(identity, expectedPrefix) {
		t.Errorf("Expected identity to start with %q, got %q", expectedPrefix, identity)
	}

	// The identity should contain the repo name (last component of path)
	if !strings.Contains(identity, repo.Name) {
		t.Errorf("Expected identity to contain repo name %q, got %q", repo.Name, identity)
	}
}

func TestBackfillRepoIdentities_SkipsNonGitRepos(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create a repo pointing to a directory that is NOT a git repository
	tempDir := t.TempDir() // Just a plain directory, no git init
	repo, err := db.GetOrCreateRepo(tempDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Clear identity to simulate legacy repo
	_, err = db.Exec(`UPDATE repos SET identity = NULL WHERE id = ?`, repo.ID)
	if err != nil {
		t.Fatalf("Failed to clear identity: %v", err)
	}

	// Backfill should set a local:// identity for non-git repos
	count, err := db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 repo backfilled with local:// identity, got %d", count)
	}

	// Verify identity is set to local:// prefix
	var identity sql.NullString
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repo.ID).Scan(&identity)
	if err != nil {
		t.Fatalf("Query identity failed: %v", err)
	}
	if !identity.Valid || !strings.HasPrefix(identity.String, "local://") {
		t.Errorf("Expected identity with local:// prefix, got %q", identity.String)
	}
}

func TestBackfillRepoIdentities_SkipsReposWithIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create a repo and set its identity
	tempDir := t.TempDir()
	repo, err := db.GetOrCreateRepo(tempDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	err = db.SetRepoIdentity(repo.ID, "https://github.com/user/existing.git")
	if err != nil {
		t.Fatalf("SetRepoIdentity failed: %v", err)
	}

	// Backfill should not change existing identity
	count, err := db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 repos backfilled (already has identity), got %d", count)
	}

	// Verify identity unchanged
	var identity string
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repo.ID).Scan(&identity)
	if err != nil {
		t.Fatalf("Query identity failed: %v", err)
	}
	if identity != "https://github.com/user/existing.git" {
		t.Errorf("Expected identity unchanged, got %q", identity)
	}
}

func TestBackfillRepoIdentities_SkipsMissingPaths(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create a repo pointing to a non-existent path (subpath of temp dir that doesn't exist)
	nonExistentPath := filepath.Join(t.TempDir(), "this-subdir-does-not-exist", "nested")
	_, err = db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, NULL)`,
		nonExistentPath, "missing-repo")
	if err != nil {
		t.Fatalf("Insert repo failed: %v", err)
	}

	// Get the repo ID
	var repoID int64
	err = db.QueryRow(`SELECT id FROM repos WHERE root_path = ?`, nonExistentPath).Scan(&repoID)
	if err != nil {
		t.Fatalf("Get repo ID failed: %v", err)
	}

	// Backfill should still set a local:// identity for missing paths
	// (better to have an identity than none, even for stale entries)
	count, err := db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 repo backfilled with local:// identity, got %d", count)
	}

	// Verify identity is set to local:// prefix
	var identity sql.NullString
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repoID).Scan(&identity)
	if err != nil {
		t.Fatalf("Query identity failed: %v", err)
	}
	if !identity.Valid || !strings.HasPrefix(identity.String, "local://") {
		t.Errorf("Expected identity with local:// prefix, got %q", identity.String)
	}
}

func TestGetOrCreateRepoByIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	t.Run("creates placeholder repo for local identity", func(t *testing.T) {
		localIdentity := "local:my-local-project"

		// Create repo by identity
		repoID, err := db.GetOrCreateRepoByIdentity(localIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepoByIdentity failed: %v", err)
		}
		if repoID == 0 {
			t.Fatal("Expected non-zero repo ID")
		}

		// Verify repo has correct fields
		var rootPath, name, identity string
		err = db.QueryRow(`SELECT root_path, name, identity FROM repos WHERE id = ?`, repoID).Scan(&rootPath, &name, &identity)
		if err != nil {
			t.Fatalf("Query repo failed: %v", err)
		}
		if rootPath != localIdentity {
			t.Errorf("Expected root_path %q, got %q", localIdentity, rootPath)
		}
		if name != localIdentity {
			t.Errorf("Expected name %q, got %q", localIdentity, name)
		}
		if identity != localIdentity {
			t.Errorf("Expected identity %q, got %q", localIdentity, identity)
		}
	})

	t.Run("returns same ID on subsequent calls", func(t *testing.T) {
		localIdentity := "local:another-project"

		id1, err := db.GetOrCreateRepoByIdentity(localIdentity)
		if err != nil {
			t.Fatalf("First GetOrCreateRepoByIdentity failed: %v", err)
		}

		id2, err := db.GetOrCreateRepoByIdentity(localIdentity)
		if err != nil {
			t.Fatalf("Second GetOrCreateRepoByIdentity failed: %v", err)
		}

		if id1 != id2 {
			t.Errorf("Expected same repo ID, got %d and %d", id1, id2)
		}
	})

	t.Run("creates different repos for different identities", func(t *testing.T) {
		id1, err := db.GetOrCreateRepoByIdentity("local:project-a")
		if err != nil {
			t.Fatalf("First GetOrCreateRepoByIdentity failed: %v", err)
		}

		id2, err := db.GetOrCreateRepoByIdentity("local:project-b")
		if err != nil {
			t.Fatalf("Second GetOrCreateRepoByIdentity failed: %v", err)
		}

		if id1 == id2 {
			t.Errorf("Expected different repo IDs, both got %d", id1)
		}
	})

	t.Run("works with git URL identities too", func(t *testing.T) {
		gitIdentity := "https://github.com/user/repo.git"

		repoID, err := db.GetOrCreateRepoByIdentity(gitIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepoByIdentity failed: %v", err)
		}

		// Verify identity is set correctly
		var identity string
		err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repoID).Scan(&identity)
		if err != nil {
			t.Fatalf("Query repo failed: %v", err)
		}
		if identity != gitIdentity {
			t.Errorf("Expected identity %q, got %q", gitIdentity, identity)
		}
	})
}

func TestSetRepoIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create test repo
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Set identity
	err = db.SetRepoIdentity(repo.ID, "https://github.com/user/repo.git")
	if err != nil {
		t.Fatalf("SetRepoIdentity failed: %v", err)
	}

	// Verify via GetRepoByIdentity
	found, err := db.GetRepoByIdentity("https://github.com/user/repo.git")
	if err != nil {
		t.Fatalf("GetRepoByIdentity failed: %v", err)
	}
	if found == nil {
		t.Fatal("Expected to find repo by identity")
	}
	if found.ID != repo.ID {
		t.Errorf("Expected repo ID %d, got %d", repo.ID, found.ID)
	}
	if found.Identity != "https://github.com/user/repo.git" {
		t.Errorf("Expected identity 'https://github.com/user/repo.git', got %q", found.Identity)
	}
}

func TestGetRepoByIdentity_NotFound(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	found, err := db.GetRepoByIdentity("nonexistent")
	if err != nil {
		t.Fatalf("GetRepoByIdentity failed: %v", err)
	}
	if found != nil {
		t.Errorf("Expected nil for nonexistent identity, got %+v", found)
	}
}

func TestGetKnownJobUUIDs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	t.Run("returns empty when no jobs exist", func(t *testing.T) {
		uuids, err := db.GetKnownJobUUIDs()
		if err != nil {
			t.Fatalf("GetKnownJobUUIDs failed: %v", err)
		}
		if len(uuids) != 0 {
			t.Errorf("Expected empty slice, got %d UUIDs", len(uuids))
		}
	})

	t.Run("returns UUIDs of jobs with UUIDs", func(t *testing.T) {
		repo, err := db.GetOrCreateRepo(t.TempDir())
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}

		commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Test Author", "Test Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}

		// Create two jobs with UUIDs
		job1, err := db.EnqueueJob(repo.ID, commit.ID, "abc123", "test", "thorough")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		job2, err := db.EnqueueJob(repo.ID, commit.ID, "def456", "test", "quick")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}

		uuids, err := db.GetKnownJobUUIDs()
		if err != nil {
			t.Fatalf("GetKnownJobUUIDs failed: %v", err)
		}

		if len(uuids) != 2 {
			t.Errorf("Expected 2 UUIDs, got %d", len(uuids))
		}

		// Verify the UUIDs are the ones we created
		uuidMap := make(map[string]bool)
		for _, u := range uuids {
			uuidMap[u] = true
		}

		if !uuidMap[job1.UUID] {
			t.Errorf("Expected to find job1 UUID %s", job1.UUID)
		}
		if !uuidMap[job2.UUID] {
			t.Errorf("Expected to find job2 UUID %s", job2.UUID)
		}
	})
}

func TestCommitsMigration_SameSHADifferentRepos(t *testing.T) {
	// This test creates an old-schema database manually, runs migration,
	// and verifies that the same SHA can now exist in different repos.
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create database with old schema (sha TEXT UNIQUE NOT NULL)
	rawDB, err := openRawDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to open raw database: %v", err)
	}

	// Create old schema with UNIQUE(sha) constraint
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
		CREATE INDEX idx_commits_sha ON commits(sha);
	`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to create old schema: %v", err)
	}

	// Insert two repos
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name) VALUES ('/repo1', 'repo1')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo1: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name) VALUES ('/repo2', 'repo2')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo2: %v", err)
	}

	// Insert a commit in repo1
	_, err = rawDB.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (1, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert commit in repo1: %v", err)
	}

	// Verify old schema prevents same SHA in different repo
	_, err = rawDB.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (2, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err == nil {
		rawDB.Close()
		t.Fatal("Expected error inserting duplicate SHA in old schema, but got none")
	}

	rawDB.Close()

	// Now open with our storage.Open which runs migrations
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database with migrations: %v", err)
	}
	defer db.Close()

	// After migration, same SHA in different repo should work
	_, err = db.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (2, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("After migration, same SHA in different repo should succeed: %v", err)
	}

	// Verify both commits exist
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM commits WHERE sha = 'abc123'`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count commits: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 commits with sha abc123, got %d", count)
	}

	// Verify duplicate in same repo is still rejected
	_, err = db.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (1, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err == nil {
		t.Error("Expected error inserting duplicate SHA in same repo, but got none")
	}
}

// openRawDB opens a database without running migrations
func openRawDB(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
}

func TestDuplicateRepoIdentity_MigrationError(t *testing.T) {
	// This test verifies that migration fails with a clear error if duplicate
	// non-NULL repos.identity values exist before creating the unique index.
	dbPath := filepath.Join(t.TempDir(), "test.db")

	rawDB, err := openRawDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to open raw database: %v", err)
	}

	// Create schema with identity column but no unique index (simulates partial migration)
	_, err = rawDB.Exec(`
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
		CREATE TABLE review_jobs (
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
		CREATE INDEX idx_commits_sha ON commits(sha);
	`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}

	// Insert two repos with the same identity (duplicate)
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/repo1', 'repo1', 'dup-identity')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo1: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/repo2', 'repo2', 'dup-identity')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo2: %v", err)
	}

	rawDB.Close()

	// Now open with storage.Open which runs migrations - should fail with clear error
	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("Expected migration to fail due to duplicate identities, but it succeeded")
	}
	if !regexp.MustCompile(`duplicate.*identit`).MatchString(err.Error()) {
		t.Errorf("Expected error about duplicate identities, got: %v", err)
	}
}

func TestGetRepoByIdentity_DuplicateError(t *testing.T) {
	// This test verifies GetRepoByIdentity returns an error if duplicates exist
	// (which shouldn't happen with the unique index, but tests the code path)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create two repos with different paths but we'll manually set same identity
	// bypassing the unique constraint by using raw SQL after dropping the index
	_, err = db.Exec(`DROP INDEX IF EXISTS idx_repos_identity`)
	if err != nil {
		t.Fatalf("Failed to drop index: %v", err)
	}

	_, err = db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/path1', 'repo1', 'same-id')`)
	if err != nil {
		t.Fatalf("Failed to insert repo1: %v", err)
	}
	_, err = db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/path2', 'repo2', 'same-id')`)
	if err != nil {
		t.Fatalf("Failed to insert repo2: %v", err)
	}

	// GetRepoByIdentity should return error for duplicates
	_, err = db.GetRepoByIdentity("same-id")
	if err == nil {
		t.Fatal("Expected error for duplicate identities, but got nil")
	}
	if !regexp.MustCompile(`multiple repos found`).MatchString(err.Error()) {
		t.Errorf("Expected 'multiple repos found' error, got: %v", err)
	}
}

func TestGetMachineID_EmptyValueRegeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Insert an empty machine ID (simulating manual edit or past bug)
	_, err = db.Exec(`INSERT OR REPLACE INTO sync_state (key, value) VALUES (?, '')`, SyncStateMachineID)
	if err != nil {
		t.Fatalf("Failed to insert empty machine ID: %v", err)
	}

	// GetMachineID should regenerate when value is empty
	id, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID failed: %v", err)
	}
	if id == "" {
		t.Error("Expected non-empty machine ID after regeneration")
	}

	// Verify it's now stored
	var stored string
	err = db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, SyncStateMachineID).Scan(&stored)
	if err != nil {
		t.Fatalf("Failed to query stored ID: %v", err)
	}
	if stored != id {
		t.Errorf("Stored ID %q doesn't match returned ID %q", stored, id)
	}
}

func TestSyncWorker_StartStopStart(t *testing.T) {
	// This test verifies that SyncWorker can be started, stopped, and restarted
	// without issues (channel reinitialization on restart).
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Note: Sync is disabled by default, so Start() will fail with "sync is not enabled"
	// which is expected. We're testing that the worker handles restarts correctly
	// when enabled.

	t.Run("Start fails when sync is disabled", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{Enabled: false})
		err := worker.Start()
		if err == nil {
			t.Fatal("Expected error when starting with sync disabled")
		}
	})

	t.Run("Start fails without postgres_url", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{
			Enabled:  true,
			Interval: "1s",
		})
		// This will start the goroutine which will fail to connect,
		// but Start() itself should succeed
		err := worker.Start()
		if err != nil {
			t.Fatalf("Expected Start to succeed (connection fails async): %v", err)
		}
		// Stop the worker
		worker.Stop()
	})

	t.Run("Start-Stop-Start cycle works", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{
			Enabled:  true,
			Interval: "1s",
		})

		// First start
		err := worker.Start()
		if err != nil {
			t.Fatalf("First Start failed: %v", err)
		}

		// Stop
		worker.Stop()

		// Second start - this tests that channels are reinitialized
		err = worker.Start()
		if err != nil {
			t.Fatalf("Second Start failed (channel reinit issue?): %v", err)
		}

		// Stop again
		worker.Stop()
	})

	t.Run("Double start fails", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{
			Enabled:  true,
			Interval: "1s",
		})

		err := worker.Start()
		if err != nil {
			t.Fatalf("First Start failed: %v", err)
		}
		defer worker.Stop()

		err = worker.Start()
		if err == nil {
			t.Fatal("Expected error on double start")
		}
	})
}

func TestParseSQLiteTime(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantYear int
		wantZero bool
	}{
		{
			name:     "RFC3339 with Z",
			input:    "2024-06-15T10:30:00Z",
			wantYear: 2024,
		},
		{
			name:     "RFC3339 with offset",
			input:    "2024-06-15T10:30:00-05:00",
			wantYear: 2024,
		},
		{
			name:     "RFC3339 with positive offset",
			input:    "2024-06-15T10:30:00+02:00",
			wantYear: 2024,
		},
		{
			name:     "SQLite datetime format",
			input:    "2024-06-15 10:30:00",
			wantYear: 2024,
		},
		{
			name:     "empty string",
			input:    "",
			wantZero: true,
		},
		{
			name:     "invalid format",
			input:    "not-a-date",
			wantZero: true,
		},
		{
			name:     "partial date",
			input:    "2024-06-15",
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSQLiteTime(tt.input)
			if tt.wantZero {
				if !got.IsZero() {
					t.Errorf("parseSQLiteTime(%q) = %v, want zero time", tt.input, got)
				}
				return
			}
			if got.IsZero() {
				t.Errorf("parseSQLiteTime(%q) returned zero time, want year %d", tt.input, tt.wantYear)
				return
			}
			if got.Year() != tt.wantYear {
				t.Errorf("parseSQLiteTime(%q).Year() = %d, want %d", tt.input, got.Year(), tt.wantYear)
			}
		})
	}
}

func TestGetJobsToSync_TimestampComparison(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Get machine ID for this test
	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID failed: %v", err)
	}

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "sync-test-sha", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Create a job and complete it
	job, err := db.EnqueueJob(repo.ID, commit.ID, "sync-test-sha", "test", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	_, err = db.ClaimJob("worker-1")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	t.Run("job with null synced_at is returned", func(t *testing.T) {
		jobs, err := db.GetJobsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with NULL synced_at to be returned for sync")
		}
	})

	t.Run("job after MarkJobSynced is not returned", func(t *testing.T) {
		err := db.MarkJobSynced(job.ID)
		if err != nil {
			t.Fatalf("MarkJobSynced failed: %v", err)
		}

		jobs, err := db.GetJobsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		for _, j := range jobs {
			if j.ID == job.ID {
				t.Error("Expected synced job to NOT be returned")
			}
		}
	})

	t.Run("job with updated_at after synced_at is returned", func(t *testing.T) {
		// Update the job's updated_at to be after synced_at
		futureTime := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		_, err := db.Exec(`UPDATE review_jobs SET updated_at = ? WHERE id = ?`, futureTime, job.ID)
		if err != nil {
			t.Fatalf("Failed to update updated_at: %v", err)
		}

		jobs, err := db.GetJobsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with updated_at > synced_at to be returned for sync")
		}
	})

	t.Run("mixed format timestamps compare correctly", func(t *testing.T) {
		// Create a new job for this subtest
		commit2, err := db.GetOrCreateCommit(repo.ID, "mixed-format-sha", "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job2, err := db.EnqueueJob(repo.ID, commit2.ID, "mixed-format-sha", "test", "thorough")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		_, err = db.ClaimJob("worker-2")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		err = db.CompleteJob(job2.ID, "test", "prompt", "output")
		if err != nil {
			t.Fatalf("CompleteJob failed: %v", err)
		}

		// Set synced_at in SQLite datetime format (legacy format) - 10:30 UTC
		_, err = db.Exec(`UPDATE review_jobs SET synced_at = '2024-06-15 10:30:00' WHERE id = ?`, job2.ID)
		if err != nil {
			t.Fatalf("Failed to set synced_at: %v", err)
		}

		// Set updated_at in RFC3339 with offset: 14:30+02:00 = 12:30 UTC (later than 10:30 UTC)
		_, err = db.Exec(`UPDATE review_jobs SET updated_at = '2024-06-15T14:30:00+02:00' WHERE id = ?`, job2.ID)
		if err != nil {
			t.Fatalf("Failed to set updated_at: %v", err)
		}

		jobs, err := db.GetJobsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job2.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with mixed format timestamps (updated_at > synced_at) to be returned")
		}

		// Now test the opposite: synced_at is later than updated_at
		// synced_at: 2024-06-15 20:00:00 (8pm UTC)
		// updated_at: 2024-06-15T10:30:00Z (10:30am UTC)
		_, err = db.Exec(`UPDATE review_jobs SET synced_at = '2024-06-15 20:00:00', updated_at = '2024-06-15T10:30:00Z' WHERE id = ?`, job2.ID)
		if err != nil {
			t.Fatalf("Failed to update timestamps: %v", err)
		}

		jobs, err = db.GetJobsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		for _, j := range jobs {
			if j.ID == job2.ID {
				t.Error("Expected job with synced_at > updated_at to NOT be returned")
			}
		}
	})

	t.Run("mixed format timestamps work correctly in non-UTC timezone", func(t *testing.T) {
		// Set TZ to a non-UTC timezone BEFORE opening the DB to ensure
		// SQLite/Go uses the non-UTC timezone for localtime operations
		t.Setenv("TZ", "America/New_York")

		// Open a fresh database after setting TZ so timezone is properly initialized
		tzDBPath := filepath.Join(t.TempDir(), "tz-test.db")
		tzDB, err := Open(tzDBPath)
		if err != nil {
			t.Fatalf("Failed to open TZ test database: %v", err)
		}
		defer tzDB.Close()

		tzMachineID, err := tzDB.GetMachineID()
		if err != nil {
			t.Fatalf("GetMachineID failed: %v", err)
		}

		// Create test data in the new DB
		tzRepo, err := tzDB.GetOrCreateRepo(t.TempDir())
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}
		commit3, err := tzDB.GetOrCreateCommit(tzRepo.ID, "tz-test-sha", "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job3, err := tzDB.EnqueueJob(tzRepo.ID, commit3.ID, "tz-test-sha", "test", "thorough")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		_, err = tzDB.ClaimJob("worker-3")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		err = tzDB.CompleteJob(job3.ID, "test", "prompt", "output")
		if err != nil {
			t.Fatalf("CompleteJob failed: %v", err)
		}

		// synced_at: legacy format 10:30 (should be treated as UTC)
		// updated_at: 12:30 UTC (later than synced_at)
		_, err = tzDB.Exec(`UPDATE review_jobs SET synced_at = '2024-06-15 10:30:00', updated_at = '2024-06-15T12:30:00Z' WHERE id = ?`, job3.ID)
		if err != nil {
			t.Fatalf("Failed to set timestamps: %v", err)
		}

		jobs, err := tzDB.GetJobsToSync(tzMachineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job3.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with updated_at > synced_at to be returned regardless of local timezone")
		}

		// synced_at: legacy format 14:00 (should be treated as UTC)
		// updated_at: 12:30 UTC (earlier than synced_at)
		_, err = tzDB.Exec(`UPDATE review_jobs SET synced_at = '2024-06-15 14:00:00', updated_at = '2024-06-15T12:30:00Z' WHERE id = ?`, job3.ID)
		if err != nil {
			t.Fatalf("Failed to update timestamps: %v", err)
		}

		jobs, err = tzDB.GetJobsToSync(tzMachineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		for _, j := range jobs {
			if j.ID == job3.ID {
				t.Error("Expected job with synced_at > updated_at to NOT be returned regardless of local timezone")
			}
		}
	})
}

func TestGetReviewsToSync_TimestampComparison(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Get machine ID for this test
	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID failed: %v", err)
	}

	// Create a repo, commit, and job
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "review-sync-sha", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "review-sync-sha", "test", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	_, err = db.ClaimJob("worker-1")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Mark job as synced (required before reviews can sync due to FK ordering)
	err = db.MarkJobSynced(job.ID)
	if err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}

	// Get the review ID
	review, err := db.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}

	t.Run("review with null synced_at is returned", func(t *testing.T) {
		reviews, err := db.GetReviewsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with NULL synced_at to be returned for sync")
		}
	})

	t.Run("review after MarkReviewSynced is not returned", func(t *testing.T) {
		err := db.MarkReviewSynced(review.ID)
		if err != nil {
			t.Fatalf("MarkReviewSynced failed: %v", err)
		}

		reviews, err := db.GetReviewsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		for _, r := range reviews {
			if r.ID == review.ID {
				t.Error("Expected synced review to NOT be returned")
			}
		}
	})

	t.Run("review with updated_at after synced_at is returned", func(t *testing.T) {
		// Update the review's updated_at to be after synced_at
		futureTime := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		_, err := db.Exec(`UPDATE reviews SET updated_at = ? WHERE id = ?`, futureTime, review.ID)
		if err != nil {
			t.Fatalf("Failed to update updated_at: %v", err)
		}

		reviews, err := db.GetReviewsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with updated_at > synced_at to be returned for sync")
		}
	})

	t.Run("mixed format timestamps compare correctly", func(t *testing.T) {
		// Set synced_at in SQLite datetime format (legacy format) - 10:30 UTC
		_, err := db.Exec(`UPDATE reviews SET synced_at = '2024-06-15 10:30:00' WHERE id = ?`, review.ID)
		if err != nil {
			t.Fatalf("Failed to set synced_at: %v", err)
		}

		// Set updated_at in RFC3339 with offset: 14:30+02:00 = 12:30 UTC (later than 10:30 UTC)
		_, err = db.Exec(`UPDATE reviews SET updated_at = '2024-06-15T14:30:00+02:00' WHERE id = ?`, review.ID)
		if err != nil {
			t.Fatalf("Failed to set updated_at: %v", err)
		}

		reviews, err := db.GetReviewsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with mixed format timestamps (updated_at > synced_at) to be returned")
		}

		// Now test the opposite: synced_at is later than updated_at
		_, err = db.Exec(`UPDATE reviews SET synced_at = '2024-06-15 20:00:00', updated_at = '2024-06-15T10:30:00Z' WHERE id = ?`, review.ID)
		if err != nil {
			t.Fatalf("Failed to update timestamps: %v", err)
		}

		reviews, err = db.GetReviewsToSync(machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		for _, r := range reviews {
			if r.ID == review.ID {
				t.Error("Expected review with synced_at > updated_at to NOT be returned")
			}
		}
	})

	t.Run("mixed format timestamps work correctly in non-UTC timezone", func(t *testing.T) {
		// Set TZ to a non-UTC timezone BEFORE opening the DB to ensure
		// SQLite/Go uses the non-UTC timezone for localtime operations
		t.Setenv("TZ", "America/New_York")

		// Open a fresh database after setting TZ so timezone is properly initialized
		tzDBPath := filepath.Join(t.TempDir(), "tz-review-test.db")
		tzDB, err := Open(tzDBPath)
		if err != nil {
			t.Fatalf("Failed to open TZ test database: %v", err)
		}
		defer tzDB.Close()

		tzMachineID, err := tzDB.GetMachineID()
		if err != nil {
			t.Fatalf("GetMachineID failed: %v", err)
		}

		// Create test data in the new DB
		tzRepo, err := tzDB.GetOrCreateRepo(t.TempDir())
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}
		tzCommit, err := tzDB.GetOrCreateCommit(tzRepo.ID, "tz-review-sha", "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		tzJob, err := tzDB.EnqueueJob(tzRepo.ID, tzCommit.ID, "tz-review-sha", "test", "thorough")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		_, err = tzDB.ClaimJob("worker-tz")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		err = tzDB.CompleteJob(tzJob.ID, "test", "prompt", "output")
		if err != nil {
			t.Fatalf("CompleteJob failed: %v", err)
		}

		// Mark job as synced (required before reviews can sync due to FK ordering)
		err = tzDB.MarkJobSynced(tzJob.ID)
		if err != nil {
			t.Fatalf("MarkJobSynced failed: %v", err)
		}

		// Get the review ID
		tzReview, err := tzDB.GetReviewByJobID(tzJob.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}

		// synced_at: legacy format 10:30 (should be treated as UTC)
		// updated_at: 12:30 UTC (later than synced_at)
		_, err = tzDB.Exec(`UPDATE reviews SET synced_at = '2024-06-15 10:30:00', updated_at = '2024-06-15T12:30:00Z' WHERE id = ?`, tzReview.ID)
		if err != nil {
			t.Fatalf("Failed to set timestamps: %v", err)
		}

		reviews, err := tzDB.GetReviewsToSync(tzMachineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == tzReview.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with updated_at > synced_at to be returned regardless of local timezone")
		}

		// synced_at: legacy format 14:00 (should be treated as UTC)
		// updated_at: 12:30 UTC (earlier than synced_at)
		_, err = tzDB.Exec(`UPDATE reviews SET synced_at = '2024-06-15 14:00:00', updated_at = '2024-06-15T12:30:00Z' WHERE id = ?`, tzReview.ID)
		if err != nil {
			t.Fatalf("Failed to update timestamps: %v", err)
		}

		reviews, err = tzDB.GetReviewsToSync(tzMachineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		for _, r := range reviews {
			if r.ID == tzReview.ID {
				t.Error("Expected review with synced_at > updated_at to NOT be returned regardless of local timezone")
			}
		}
	})
}

func TestGetCommentsToSync_LegacyCommentsExcluded(t *testing.T) {
	// This test verifies that legacy responses with job_id IS NULL (tied only to commit_id)
	// are excluded from sync since they cannot be synced via job_uuid.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID failed: %v", err)
	}

	// Create a repo and commit
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "legacy-resp-sha", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Create a job-based response (should be synced)
	job, err := db.EnqueueJob(repo.ID, commit.ID, "legacy-resp-sha", "test", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	_, err = db.ClaimJob("worker-1")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Mark job as synced (required before responses can sync due to FK ordering)
	err = db.MarkJobSynced(job.ID)
	if err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}

	jobResp, err := db.AddCommentToJob(job.ID, "human", "This is a job response")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}

	// Create a legacy commit-only response by directly inserting with job_id IS NULL
	result, err := db.Exec(`
		INSERT INTO responses (commit_id, responder, response, uuid, source_machine_id, created_at)
		VALUES (?, 'human', 'This is a legacy response', ?, ?, datetime('now'))
	`, commit.ID, GenerateUUID(), machineID)
	if err != nil {
		t.Fatalf("Failed to insert legacy response: %v", err)
	}
	legacyRespID, _ := result.LastInsertId()

	// Get responses to sync - should only include the job-based response
	responses, err := db.GetCommentsToSync(machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}

	foundJobResp := false
	foundLegacyResp := false
	for _, r := range responses {
		if r.ID == jobResp.ID {
			foundJobResp = true
		}
		if r.ID == legacyRespID {
			foundLegacyResp = true
		}
	}

	if !foundJobResp {
		t.Error("Expected job-based response to be included in sync")
	}
	if foundLegacyResp {
		t.Error("Expected legacy response (job_id IS NULL) to be EXCLUDED from sync")
	}
}

func TestUpsertPulledResponse_MissingParentJob(t *testing.T) {
	// This test verifies that UpsertPulledResponse gracefully handles responses
	// for jobs that don't exist locally (returns nil, doesn't error)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Try to upsert a response for a job that doesn't exist
	nonexistentJobUUID := GenerateUUID()
	response := PulledResponse{
		UUID:            GenerateUUID(),
		JobUUID:         nonexistentJobUUID,
		Responder:       "human",
		Response:        "Test response for missing job",
		SourceMachineID: GenerateUUID(),
		CreatedAt:       time.Now(),
	}

	// Should return nil (not error) for missing parent job
	err = db.UpsertPulledResponse(response)
	if err != nil {
		t.Errorf("Expected nil error for missing parent job, got: %v", err)
	}

	// Verify no response was inserted
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM responses WHERE uuid = ?`, response.UUID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count responses: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 responses for missing parent job, got %d", count)
	}
}

func TestUpsertPulledResponse_WithParentJob(t *testing.T) {
	// This test verifies UpsertPulledResponse works when the parent job exists
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create a repo and job
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "parent-job-sha", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(repo.ID, commit.ID, "parent-job-sha", "test", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Upsert a response for the existing job
	response := PulledResponse{
		UUID:            GenerateUUID(),
		JobUUID:         job.UUID,
		Responder:       "human",
		Response:        "Test response for existing job",
		SourceMachineID: GenerateUUID(),
		CreatedAt:       time.Now(),
	}

	err = db.UpsertPulledResponse(response)
	if err != nil {
		t.Fatalf("UpsertPulledResponse failed: %v", err)
	}

	// Verify response was inserted
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM responses WHERE uuid = ?`, response.UUID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count responses: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 response, got %d", count)
	}
}

func TestSyncWorker_SyncNowReturnsErrorWhenNotRunning(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	cfg := config.SyncConfig{
		Enabled: true,
	}
	worker := NewSyncWorker(db, cfg)

	// SyncNow should return error when worker is not running
	_, err = worker.SyncNow()
	if err == nil {
		t.Fatal("Expected error from SyncNow when worker not running")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("Expected 'not running' error, got: %v", err)
	}
}

func TestSyncWorker_FinalPushReturnsNilWhenNotConnected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	cfg := config.SyncConfig{
		Enabled: true,
	}
	worker := NewSyncWorker(db, cfg)

	// FinalPush should return nil when not connected (nothing to push)
	err = worker.FinalPush()
	if err != nil {
		t.Fatalf("FinalPush should return nil when not connected, got: %v", err)
	}
}

// TestClearAllSyncedAt verifies that ClearAllSyncedAt clears synced_at
// on all tables (jobs, reviews, responses).
func TestClearAllSyncedAt(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job with a review
	job := h.createCompletedJob("clear-test-sha")

	// Add a response
	_, err := h.db.AddCommentToJob(job.ID, "user", "test response")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}

	// Mark everything as synced
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}
	review, err := h.db.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}
	if err := h.db.MarkReviewSynced(review.ID); err != nil {
		t.Fatalf("MarkReviewSynced failed: %v", err)
	}

	// Verify nothing needs to sync
	jobs, _ := h.db.GetJobsToSync(h.machineID, 100)
	if len(jobs) != 0 {
		t.Errorf("Expected 0 jobs to sync before clear, got %d", len(jobs))
	}
	reviews, _ := h.db.GetReviewsToSync(h.machineID, 100)
	if len(reviews) != 0 {
		t.Errorf("Expected 0 reviews to sync before clear, got %d", len(reviews))
	}

	// Clear all synced_at
	if err := h.db.ClearAllSyncedAt(); err != nil {
		t.Fatalf("ClearAllSyncedAt failed: %v", err)
	}

	// Now everything should need to sync again
	jobs, err = h.db.GetJobsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("Expected 1 job to sync after clear, got %d", len(jobs))
	}

	// Mark job synced so reviews become available
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}

	reviews, err = h.db.GetReviewsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	if len(reviews) != 1 {
		t.Errorf("Expected 1 review to sync after clear, got %d", len(reviews))
	}

	responses, err := h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	if len(responses) != 1 {
		t.Errorf("Expected 1 response to sync after clear, got %d", len(responses))
	}
}

// TestBatchMarkSynced verifies the batch MarkXSynced functions work correctly.
func TestBatchMarkSynced(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create multiple jobs with reviews and responses
	var jobs []*ReviewJob
	for i := 0; i < 5; i++ {
		job := h.createCompletedJob(fmt.Sprintf("batch-test-sha-%d", i))
		jobs = append(jobs, job)
		_, err := h.db.AddCommentToJob(job.ID, "user", fmt.Sprintf("response %d", i))
		if err != nil {
			t.Fatalf("AddCommentToJob failed: %v", err)
		}
	}

	t.Run("MarkJobsSynced marks multiple jobs", func(t *testing.T) {
		// Get jobs to sync before
		toSync, err := h.db.GetJobsToSync(h.machineID, 100)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		if len(toSync) != 5 {
			t.Fatalf("Expected 5 jobs to sync, got %d", len(toSync))
		}

		// Mark first 3 as synced
		jobIDs := []int64{jobs[0].ID, jobs[1].ID, jobs[2].ID}
		if err := h.db.MarkJobsSynced(jobIDs); err != nil {
			t.Fatalf("MarkJobsSynced failed: %v", err)
		}

		// Verify only 2 jobs left to sync
		toSync, err = h.db.GetJobsToSync(h.machineID, 100)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		if len(toSync) != 2 {
			t.Errorf("Expected 2 jobs to sync after batch mark, got %d", len(toSync))
		}
	})

	t.Run("MarkReviewsSynced marks multiple reviews", func(t *testing.T) {
		// Get reviews for synced jobs
		reviews, err := h.db.GetReviewsToSync(h.machineID, 100)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		if len(reviews) != 3 {
			t.Fatalf("Expected 3 reviews to sync (jobs 0-2 synced), got %d", len(reviews))
		}

		// Mark all 3 as synced
		reviewIDs := make([]int64, len(reviews))
		for i, r := range reviews {
			reviewIDs[i] = r.ID
		}
		if err := h.db.MarkReviewsSynced(reviewIDs); err != nil {
			t.Fatalf("MarkReviewsSynced failed: %v", err)
		}

		// Verify no reviews left to sync (for synced jobs)
		reviews, err = h.db.GetReviewsToSync(h.machineID, 100)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		if len(reviews) != 0 {
			t.Errorf("Expected 0 reviews to sync after batch mark, got %d", len(reviews))
		}
	})

	t.Run("MarkCommentsSynced marks multiple comments", func(t *testing.T) {
		// Get responses for synced jobs
		responses, err := h.db.GetCommentsToSync(h.machineID, 100)
		if err != nil {
			t.Fatalf("GetCommentsToSync failed: %v", err)
		}
		if len(responses) != 3 {
			t.Fatalf("Expected 3 responses to sync (jobs 0-2 synced), got %d", len(responses))
		}

		// Mark all 3 as synced
		responseIDs := make([]int64, len(responses))
		for i, r := range responses {
			responseIDs[i] = r.ID
		}
		if err := h.db.MarkCommentsSynced(responseIDs); err != nil {
			t.Fatalf("MarkCommentsSynced failed: %v", err)
		}

		// Verify no responses left to sync (for synced jobs)
		responses, err = h.db.GetCommentsToSync(h.machineID, 100)
		if err != nil {
			t.Fatalf("GetCommentsToSync failed: %v", err)
		}
		if len(responses) != 0 {
			t.Errorf("Expected 0 responses to sync after batch mark, got %d", len(responses))
		}
	})

	t.Run("empty slice is no-op", func(t *testing.T) {
		// Empty slices should not error
		if err := h.db.MarkJobsSynced([]int64{}); err != nil {
			t.Errorf("MarkJobsSynced with empty slice failed: %v", err)
		}
		if err := h.db.MarkReviewsSynced([]int64{}); err != nil {
			t.Errorf("MarkReviewsSynced with empty slice failed: %v", err)
		}
		if err := h.db.MarkCommentsSynced([]int64{}); err != nil {
			t.Errorf("MarkCommentsSynced with empty slice failed: %v", err)
		}
	})
}

// syncTestHelper creates a test DB with common setup for sync ordering tests.
type syncTestHelper struct {
	t         *testing.T
	db        *DB
	machineID string
	repo      *Repo
}

func newSyncTestHelper(t *testing.T) *syncTestHelper {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
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

// createCompletedJob creates a job, marks it done, and creates a review.
func (h *syncTestHelper) createCompletedJob(sha string) *ReviewJob {
	commit, err := h.db.GetOrCreateCommit(h.repo.ID, sha, "Author", "Subject", time.Now())
	if err != nil {
		h.t.Fatalf("Failed to create commit: %v", err)
	}
	job, err := h.db.EnqueueJob(h.repo.ID, commit.ID, sha, "test", "thorough")
	if err != nil {
		h.t.Fatalf("Failed to enqueue job: %v", err)
	}
	_, err = h.db.ClaimJob("worker")
	if err != nil {
		h.t.Fatalf("Failed to claim job: %v", err)
	}
	err = h.db.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		h.t.Fatalf("Failed to complete job: %v", err)
	}
	return job
}

// TestGetReviewsToSync_RequiresJobSynced verifies that reviews are only
// returned when their parent job has been synced (j.synced_at IS NOT NULL).
func TestGetReviewsToSync_RequiresJobSynced(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job (not synced yet)
	job := h.createCompletedJob("sync-order-sha")

	// Before job is synced, GetReviewsToSync should return nothing
	reviews, err := h.db.GetReviewsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	if len(reviews) != 0 {
		t.Errorf("Expected 0 reviews before job is synced, got %d", len(reviews))
	}

	// Mark job as synced
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("Failed to mark job synced: %v", err)
	}

	// Now GetReviewsToSync should return the review
	reviews, err = h.db.GetReviewsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	if len(reviews) != 1 {
		t.Errorf("Expected 1 review after job is synced, got %d", len(reviews))
	}
}

// TestGetCommentsToSync_RequiresJobSynced verifies that responses are only
// returned when their parent job has been synced (j.synced_at IS NOT NULL).
func TestGetCommentsToSync_RequiresJobSynced(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job (not synced yet)
	job := h.createCompletedJob("response-sync-sha")

	// Add a response to the job
	_, err := h.db.AddCommentToJob(job.ID, "test-user", "test response")
	if err != nil {
		t.Fatalf("Failed to add response: %v", err)
	}

	// Before job is synced, GetResponsesToSync should return nothing
	responses, err := h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	if len(responses) != 0 {
		t.Errorf("Expected 0 responses before job is synced, got %d", len(responses))
	}

	// Mark job as synced
	if err := h.db.MarkJobSynced(job.ID); err != nil {
		t.Fatalf("Failed to mark job synced: %v", err)
	}

	// Now GetResponsesToSync should return the response
	responses, err = h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	if len(responses) != 1 {
		t.Errorf("Expected 1 response after job is synced, got %d", len(responses))
	}
}

// TestGetJobsToSync_RequiresRepoIdentity verifies that jobs without a
// repo identity are still returned (the identity check happens at push time).
func TestGetJobsToSync_RequiresRepoIdentity(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a completed job
	_ = h.createCompletedJob("identity-test-sha")

	// Verify repo has no identity initially (GetOrCreateRepo doesn't set one)
	var identity sql.NullString
	err := h.db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, h.repo.ID).Scan(&identity)
	if err != nil {
		t.Fatalf("Failed to query repo: %v", err)
	}
	if identity.Valid && identity.String != "" {
		t.Errorf("Expected no identity, got %q", identity.String)
	}

	// GetJobsToSync should return the job (with empty identity)
	jobs, err := h.db.GetJobsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("Expected 1 job, got %d", len(jobs))
	}
	if jobs[0].RepoIdentity != "" {
		t.Errorf("Expected empty repo identity, got %q", jobs[0].RepoIdentity)
	}

	// Now set the repo identity
	if err := h.db.SetRepoIdentity(h.repo.ID, "git@github.com:test/repo.git"); err != nil {
		t.Fatalf("Failed to set repo identity: %v", err)
	}

	// GetJobsToSync should now return the job with identity
	jobs, err = h.db.GetJobsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("Expected 1 job, got %d", len(jobs))
	}
	if jobs[0].RepoIdentity != "git@github.com:test/repo.git" {
		t.Errorf("Expected repo identity 'git@github.com:test/repo.git', got %q", jobs[0].RepoIdentity)
	}
}

// TestSyncOrder_FullWorkflow tests the complete sync ordering:
// 1. Jobs must be synced first
// 2. Reviews can only sync after their job is synced
// 3. Responses can only sync after their job is synced
func TestSyncOrder_FullWorkflow(t *testing.T) {
	h := newSyncTestHelper(t)

	// Set repo identity
	if err := h.db.SetRepoIdentity(h.repo.ID, "git@github.com:test/workflow.git"); err != nil {
		t.Fatalf("Failed to set repo identity: %v", err)
	}

	// Create 3 jobs with reviews and responses
	var createdJobs []*ReviewJob
	for i := 0; i < 3; i++ {
		job := h.createCompletedJob("workflow-sha-" + string(rune('a'+i)))
		createdJobs = append(createdJobs, job)
		// Add a response
		_, err := h.db.AddCommentToJob(job.ID, "user", "response")
		if err != nil {
			t.Fatalf("Failed to add response %d: %v", i, err)
		}
	}

	// Initial state: 3 jobs to sync, 0 reviews (jobs not synced), 0 responses (jobs not synced)
	jobs, err := h.db.GetJobsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	if len(jobs) != 3 {
		t.Errorf("Expected 3 jobs to sync, got %d", len(jobs))
	}

	reviews, err := h.db.GetReviewsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	if len(reviews) != 0 {
		t.Errorf("Expected 0 reviews to sync (jobs not synced), got %d", len(reviews))
	}

	responses, err := h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	if len(responses) != 0 {
		t.Errorf("Expected 0 responses to sync (jobs not synced), got %d", len(responses))
	}

	// Sync first job
	if err := h.db.MarkJobSynced(createdJobs[0].ID); err != nil {
		t.Fatalf("Failed to mark job synced: %v", err)
	}

	// Now: 2 jobs to sync, 1 review (first job synced), 1 response (first job synced)
	jobs, err = h.db.GetJobsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("Expected 2 jobs to sync, got %d", len(jobs))
	}

	reviews, err = h.db.GetReviewsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	if len(reviews) != 1 {
		t.Errorf("Expected 1 review to sync, got %d", len(reviews))
	}

	responses, err = h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	if len(responses) != 1 {
		t.Errorf("Expected 1 response to sync, got %d", len(responses))
	}

	// Sync remaining jobs
	for _, j := range createdJobs[1:] {
		if err := h.db.MarkJobSynced(j.ID); err != nil {
			t.Fatalf("Failed to mark job synced: %v", err)
		}
	}

	// Now: 0 jobs to sync, 3 reviews, 3 responses
	jobs, err = h.db.GetJobsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("Expected 0 jobs to sync, got %d", len(jobs))
	}

	reviews, err = h.db.GetReviewsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetReviewsToSync failed: %v", err)
	}
	if len(reviews) != 3 {
		t.Errorf("Expected 3 reviews to sync, got %d", len(reviews))
	}

	responses, err = h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}
	if len(responses) != 3 {
		t.Errorf("Expected 3 responses to sync, got %d", len(responses))
	}
}
