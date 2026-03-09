package storage

import (
	"database/sql"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillSourceMachineID(t *testing.T) {
	h := newSyncTestHelper(t)

	job := h.createPendingJob("abc123")

	// Verify source_machine_id is initially NULL (simulating legacy data)
	var sourceMachineID *string
	err := h.db.QueryRow(`SELECT source_machine_id FROM review_jobs WHERE id = ?`, job.ID).Scan(&sourceMachineID)
	require.NoError(t, err, "Query failed")
	// After migration, backfill runs automatically, so it may already have a value
	// Let's clear it to test the backfill
	h.clearSourceMachineID(job.ID)

	// Run backfill
	err = h.db.BackfillSourceMachineID()
	require.NoError(t, err, "BackfillSourceMachineID failed")

	// Verify source_machine_id is now set
	var newSourceMachineID string
	err = h.db.QueryRow(`SELECT source_machine_id FROM review_jobs WHERE id = ?`, job.ID).Scan(&newSourceMachineID)
	require.NoError(t, err, "Query after backfill failed")

	machineID, _ := h.db.GetMachineID()
	require.Equal(t, machineID, newSourceMachineID,
		"Expected source_machine_id %q, got %q", machineID, newSourceMachineID)
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
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	// Clear identity to simulate legacy repo
	h := &syncTestHelper{t: t, db: db}
	h.clearRepoIdentity(repo.ID)

	// Backfill should use local: prefix with repo name (git repo, no remote)
	count, err := db.BackfillRepoIdentities()
	require.NoError(t, err, "BackfillRepoIdentities failed: %v")

	assert.Equal(t, 1, count, "unexpected condition")

	// Verify identity was set with local: prefix
	var identity string
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repo.ID).Scan(&identity)
	require.NoError(t, err, "Query identity failed: %v")

	expectedPrefix := "local:"
	assert.True(t, strings.HasPrefix(identity, expectedPrefix), "unexpected condition")

	// The identity should contain the repo name (last component of path)
	assert.Contains(t, identity, repo.Name, "unexpected condition")
}

func TestBackfillRepoIdentities_SkipsNonGitRepos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create a repo pointing to a directory that is NOT a git repository
	tempDir := t.TempDir() // Just a plain directory, no git init
	repo, err := db.GetOrCreateRepo(tempDir)
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	// Clear identity to simulate legacy repo
	h := &syncTestHelper{t: t, db: db}
	h.clearRepoIdentity(repo.ID)

	// Backfill should set a local:// identity for non-git repos
	count, err := db.BackfillRepoIdentities()
	require.NoError(t, err, "BackfillRepoIdentities failed: %v")

	assert.Equal(t, 1, count, "unexpected condition")

	// Verify identity is set to local:// prefix
	var identity sql.NullString
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repo.ID).Scan(&identity)
	require.NoError(t, err, "Query identity failed: %v")

	assert.False(t, !identity.Valid || !strings.HasPrefix(identity.String, "local://"), "unexpected condition")
}

func TestBackfillRepoIdentities_SkipsReposWithIdentity(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create a repo and set its identity
	tempDir := t.TempDir()
	repo, err := db.GetOrCreateRepo(tempDir)
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	err = db.SetRepoIdentity(repo.ID, "https://github.com/user/existing.git")
	require.NoError(t, err, "SetRepoIdentity failed: %v")

	// Backfill should not change existing identity
	count, err := db.BackfillRepoIdentities()
	require.NoError(t, err, "BackfillRepoIdentities failed: %v")

	assert.Equal(t, 0, count, "unexpected condition")

	// Verify identity unchanged
	var identity string
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repo.ID).Scan(&identity)
	require.NoError(t, err, "Query identity failed: %v")

	assert.Equal(t, "https://github.com/user/existing.git", identity, "unexpected condition")
}

func TestBackfillRepoIdentities_SkipsMissingPaths(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create a repo pointing to a non-existent path (subpath of temp dir that doesn't exist)
	nonExistentPath := filepath.Join(t.TempDir(), "this-subdir-does-not-exist", "nested")
	_, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, NULL)`,
		nonExistentPath, "missing-repo")
	require.NoError(t, err, "Insert repo failed: %v")

	// Get the repo ID
	var repoID int64
	err = db.QueryRow(`SELECT id FROM repos WHERE root_path = ?`, nonExistentPath).Scan(&repoID)
	require.NoError(t, err, "Get repo ID failed: %v")

	// Backfill should still set a local:// identity for missing paths
	// (better to have an identity than none, even for stale entries)
	count, err := db.BackfillRepoIdentities()
	require.NoError(t, err, "BackfillRepoIdentities failed: %v")

	assert.Equal(t, 1, count, "unexpected condition")

	// Verify identity is set to local:// prefix
	var identity sql.NullString
	err = db.QueryRow(`SELECT identity FROM repos WHERE id = ?`, repoID).Scan(&identity)
	require.NoError(t, err, "Query identity failed: %v")

	assert.False(t, !identity.Valid || !strings.HasPrefix(identity.String, "local://"), "unexpected condition")
}

func TestUpsertPulledJob_BackfillsModel(t *testing.T) {
	// This test verifies that upserting a pulled job with a model value backfills
	// an existing job that has NULL model (COALESCE behavior in SQLite)
	db := openTestDB(t)
	defer db.Close()

	// Create a repo
	repo, err := db.GetOrCreateRepo("/test/repo")
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	// Insert a job with NULL model using EnqueueJob (which sets model to empty string by default)
	// We need to directly insert with NULL model to test the COALESCE behavior
	jobUUID := "test-uuid-backfill-" + time.Now().Format("20060102150405")
	_, err = db.Exec(`
		INSERT INTO review_jobs (uuid, repo_id, git_ref, agent, status, enqueued_at)
		VALUES (?, ?, 'HEAD', 'test-agent', 'done', datetime('now'))
	`, jobUUID, repo.ID)
	require.NoError(t, err, "Failed to insert job with NULL model: %v")

	// Verify model is NULL
	var modelBefore sql.NullString
	err = db.QueryRow(`SELECT model FROM review_jobs WHERE uuid = ?`, jobUUID).Scan(&modelBefore)
	require.NoError(t, err, "Failed to query model before: %v")

	assert.False(t, modelBefore.Valid, "unexpected condition")

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
	require.NoError(t, err, "UpsertPulledJob failed: %v")

	// Verify model was backfilled
	var modelAfter sql.NullString
	err = db.QueryRow(`SELECT model FROM review_jobs WHERE uuid = ?`, jobUUID).Scan(&modelAfter)
	require.NoError(t, err, "Failed to query model after: %v")

	if !modelAfter.Valid {
		assert.True(t, modelAfter.Valid, "Expected model to be backfilled, but it's still NULL")
		return
	}
	assert.Equal(t, "gpt-4", modelAfter.String)

	// Also verify that upserting with empty model doesn't clear existing model
	pulledJob.Model = "" // Empty model
	err = db.UpsertPulledJob(pulledJob, repo.ID, nil)
	require.NoError(t, err, "UpsertPulledJob (empty model) failed: %v")

	var modelPreserved sql.NullString
	err = db.QueryRow(`SELECT model FROM review_jobs WHERE uuid = ?`, jobUUID).Scan(&modelPreserved)
	require.NoError(t, err, "Failed to query model preserved: %v")

	assert.False(t, !modelPreserved.Valid || modelPreserved.String != "gpt-4", "unexpected condition")
}
