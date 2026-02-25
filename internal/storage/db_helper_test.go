package storage

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// templateDB holds a pre-migrated SQLite database file. Tests copy this
// file instead of re-running the full migration chain on every call to
// openTestDB, which is the dominant cost on macOS ARM64 with -race.

var (
	templateOnce sync.Once
	templatePath string
	templateErr  error
)

func getTemplatePath() (string, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "roborev-test-template-*")
		if err != nil {
			templateErr = err
			return
		}
		p := filepath.Join(dir, "template.db")
		db, err := Open(p)
		if err != nil {
			templateErr = err
			return
		}
		db.Close()
		templatePath = p
	})
	return templatePath, templateErr
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	tmpl, err := getTemplatePath()
	if err != nil {
		t.Fatalf("Failed to create template DB: %v", err)
	}

	data, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("Failed to read template DB: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(dbPath, data, 0644); err != nil {
		t.Fatalf("Failed to write test DB: %v", err)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	return db
}

func createRepo(t *testing.T, db *DB, path string) *Repo {
	t.Helper()
	repo, err := db.GetOrCreateRepo(path)
	if err != nil {
		t.Fatalf("Failed to create repo: %v", err)
	}
	return repo
}

func createCommit(t *testing.T, db *DB, repoID int64, sha string) *Commit {
	t.Helper()
	commit, err := db.GetOrCreateCommit(repoID, sha, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}
	return commit
}

func enqueueJob(t *testing.T, db *DB, repoID, commitID int64, sha string) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repoID, CommitID: commitID, GitRef: sha, Agent: "codex"})
	if err != nil {
		t.Fatalf("Failed to enqueue job: %v", err)
	}
	return job
}

func claimJob(t *testing.T, db *DB, workerID string) *ReviewJob {
	t.Helper()
	job, err := db.ClaimJob(workerID)
	if err != nil {
		t.Fatalf("Failed to claim job: %v", err)
	}
	if job == nil {
		t.Fatal("Expected to claim a job, got nil")
	}
	return job
}

func mustEnqueuePromptJob(t *testing.T, db *DB, opts EnqueueOpts) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(opts)
	if err != nil {
		t.Fatalf("Failed to enqueue prompt job: %v", err)
	}
	return job
}

// setJobStatus forces a job into a specific state via raw SQL, replacing
// manual UPDATE statements scattered across tests.

func setJobStatus(t *testing.T, db *DB, jobID int64, status JobStatus) {
	t.Helper()
	var query string
	switch status {
	case JobStatusQueued:
		query = `UPDATE review_jobs SET status = 'queued', started_at = NULL, finished_at = NULL, error = NULL WHERE id = ?`
	case JobStatusRunning:
		query = `UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`
	case JobStatusDone:
		query = `UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`
	case JobStatusFailed:
		query = `UPDATE review_jobs SET status = 'failed', started_at = datetime('now'), finished_at = datetime('now'), error = 'test error' WHERE id = ?`
	case JobStatusCanceled:
		query = `UPDATE review_jobs SET status = 'canceled', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`
	default:
		t.Fatalf("Unknown job status: %s", status)
	}
	res, err := db.Exec(query, jobID)
	if err != nil {
		t.Fatalf("Failed to set job status to %s: %v", status, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("Failed to get rows affected: %v", err)
	}
	if rows != 1 {
		t.Fatalf("Expected exactly 1 row updated for jobID %d, got %d", jobID, rows)
	}
}

// backdateJobStart updates a job's started_at time to the specified duration ago.
func backdateJobStart(t *testing.T, db *DB, jobID int64, d time.Duration) {
	t.Helper()
	startTime := time.Now().Add(-d).UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = ? WHERE id = ?`, startTime, jobID)
	if err != nil {
		t.Fatalf("failed to backdate job: %v", err)
	}
}

// backdateJobStartWithOffset updates a job's started_at time to the specified duration ago,
// preserving the timezone offset of the generated time.

func backdateJobStartWithOffset(t *testing.T, db *DB, jobID int64, d time.Duration, loc *time.Location) {
	t.Helper()
	startTime := time.Now().Add(-d).In(loc).Format(time.RFC3339)
	_, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = ? WHERE id = ?`, startTime, jobID)
	if err != nil {
		t.Fatalf("failed to backdate job with offset: %v", err)
	}
}

// setJobBranch updates a job's branch.

func setJobBranch(t *testing.T, db *DB, jobID int64, branch string) {
	t.Helper()
	_, err := db.Exec(`UPDATE review_jobs SET branch = ? WHERE id = ?`, branch, jobID)
	if err != nil {
		t.Fatalf("failed to set job branch: %v", err)
	}
}

// createJobChain creates a repo, commit, and enqueued job, returning all three.

func createJobChain(t *testing.T, db *DB, repoPath, sha string) (*Repo, *Commit, *ReviewJob) {
	t.Helper()
	repo := createRepo(t, db, repoPath)
	commit := createCommit(t, db, repo.ID, sha)
	job := enqueueJob(t, db, repo.ID, commit.ID, sha)
	return repo, commit, job
}
