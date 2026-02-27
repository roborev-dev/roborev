package storage

import (
	"database/sql"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackfillSourceMachineID(t *testing.T) {
	h := newSyncTestHelper(t)

	job := h.createPendingJob("abc123")

	// Verify source_machine_id is initially NULL (simulating legacy data)
	var sourceMachineID *string
	err := h.db.QueryRow(`SELECT source_machine_id FROM review_jobs WHERE id = ?`, job.ID).Scan(&sourceMachineID)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	// After migration, backfill runs automatically, so it may already have a value
	// Let's clear it to test the backfill
	h.clearSourceMachineID(job.ID)

	// Run backfill
	err = h.db.BackfillSourceMachineID()
	if err != nil {
		t.Fatalf("BackfillSourceMachineID failed: %v", err)
	}

	// Verify source_machine_id is now set
	var newSourceMachineID string
	err = h.db.QueryRow(`SELECT source_machine_id FROM review_jobs WHERE id = ?`, job.ID).Scan(&newSourceMachineID)
	if err != nil {
		t.Fatalf("Query after backfill failed: %v", err)
	}

	machineID, _ := h.db.GetMachineID()
	if newSourceMachineID != machineID {
		t.Errorf("Expected source_machine_id %q, got %q", machineID, newSourceMachineID)
	}
}

func TestBackfillRepoIdentities_LocalRepoFallback(t *testing.T) {
	db := openTestDB(t)
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
	h := &syncTestHelper{t: t, db: db}
	h.clearRepoIdentity(repo.ID)

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
	db := openTestDB(t)
	defer db.Close()

	// Create a repo pointing to a directory that is NOT a git repository
	tempDir := t.TempDir() // Just a plain directory, no git init
	repo, err := db.GetOrCreateRepo(tempDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Clear identity to simulate legacy repo
	h := &syncTestHelper{t: t, db: db}
	h.clearRepoIdentity(repo.ID)

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
	db := openTestDB(t)
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
	db := openTestDB(t)
	defer db.Close()

	// Create a repo pointing to a non-existent path (subpath of temp dir that doesn't exist)
	nonExistentPath := filepath.Join(t.TempDir(), "this-subdir-does-not-exist", "nested")
	_, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, NULL)`,
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

func TestUpsertPulledJob_BackfillsModel(t *testing.T) {
	// This test verifies that upserting a pulled job with a model value backfills
	// an existing job that has NULL model (COALESCE behavior in SQLite)
	db := openTestDB(t)
	defer db.Close()

	// Create a repo
	repo, err := db.GetOrCreateRepo("/test/repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Insert a job with NULL model using EnqueueJob (which sets model to empty string by default)
	// We need to directly insert with NULL model to test the COALESCE behavior
	jobUUID := "test-uuid-backfill-" + time.Now().Format("20060102150405")
	_, err = db.Exec(`
		INSERT INTO review_jobs (uuid, repo_id, git_ref, agent, status, enqueued_at)
		VALUES (?, ?, 'HEAD', 'test-agent', 'done', datetime('now'))
	`, jobUUID, repo.ID)
	if err != nil {
		t.Fatalf("Failed to insert job with NULL model: %v", err)
	}

	// Verify model is NULL
	var modelBefore sql.NullString
	err = db.QueryRow(`SELECT model FROM review_jobs WHERE uuid = ?`, jobUUID).Scan(&modelBefore)
	if err != nil {
		t.Fatalf("Failed to query model before: %v", err)
	}
	if modelBefore.Valid {
		t.Fatalf("Expected model to be NULL before upsert, got %q", modelBefore.String)
	}

	// Upsert with a model value - should backfill
	pulledJob := PulledJob{
		UUID:            jobUUID,
		RepoIdentity:    "/test/repo",
		GitRef:          "HEAD",
		Agent:           "test-agent",
		Model:           "gpt-4", // Now providing a model
		Status:          "done",
		SourceMachineID: "test-machine",
		EnqueuedAt:      time.Now(),
		UpdatedAt:       time.Now(),
	}
	err = db.UpsertPulledJob(pulledJob, repo.ID, nil)
	if err != nil {
		t.Fatalf("UpsertPulledJob failed: %v", err)
	}

	// Verify model was backfilled
	var modelAfter sql.NullString
	err = db.QueryRow(`SELECT model FROM review_jobs WHERE uuid = ?`, jobUUID).Scan(&modelAfter)
	if err != nil {
		t.Fatalf("Failed to query model after: %v", err)
	}
	if !modelAfter.Valid {
		t.Error("Expected model to be backfilled, but it's still NULL")
	} else if modelAfter.String != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got %q", modelAfter.String)
	}

	// Also verify that upserting with empty model doesn't clear existing model
	pulledJob.Model = "" // Empty model
	err = db.UpsertPulledJob(pulledJob, repo.ID, nil)
	if err != nil {
		t.Fatalf("UpsertPulledJob (empty model) failed: %v", err)
	}

	var modelPreserved sql.NullString
	err = db.QueryRow(`SELECT model FROM review_jobs WHERE uuid = ?`, jobUUID).Scan(&modelPreserved)
	if err != nil {
		t.Fatalf("Failed to query model preserved: %v", err)
	}
	if !modelPreserved.Valid || modelPreserved.String != "gpt-4" {
		t.Errorf("Expected model to be preserved as 'gpt-4' when upserting with empty model, got %v", modelPreserved)
	}
}
