package storage

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackfillSourceMachineID(t *testing.T) {
	h := newSyncTestHelper(t)

	job := h.createPendingJob("abc123")

	// After migration, backfill runs automatically, so it may already have a value
	// Let's clear it to test the backfill
	h.clearSourceMachineID(job.ID)

	// Verify source_machine_id is initially NULL (simulating legacy data)
	sourceMachineID := h.getSourceMachineID(job.ID)
	if sourceMachineID != nil {
		t.Fatalf("Expected initial source_machine_id to be nil after clearing")
	}

	// Run backfill
	err := h.db.BackfillSourceMachineID()
	if err != nil {
		t.Fatalf("BackfillSourceMachineID failed: %v", err)
	}

	// Verify source_machine_id is now set
	newSourceMachineID := h.getSourceMachineID(job.ID)
	if newSourceMachineID == nil {
		t.Fatalf("Expected source_machine_id to be set after backfill")
	}

	machineID, _ := h.db.GetMachineID()
	if *newSourceMachineID != machineID {
		t.Errorf("Expected source_machine_id %q, got %q", machineID, *newSourceMachineID)
	}
}

func TestBackfillRepoIdentities_LocalRepoFallback(t *testing.T) {
	h := newSyncTestHelper(t)

	// Create a git repo without a remote configured in h.repo.RootPath
	cmd := exec.Command("git", "init", h.repo.RootPath)
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed (git not available?): %v", err)
	}

	// Clear identity to simulate legacy repo
	h.clearRepoIdentity(h.repo.ID)

	// Backfill should use local: prefix with repo name (git repo, no remote)
	count, err := h.db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 repo backfilled, got %d", count)
	}

	// Verify identity was set with local: prefix
	identity := h.getRepoIdentity(h.repo.ID)

	expectedPrefix := "local:"
	if !strings.HasPrefix(identity, expectedPrefix) {
		t.Errorf("Expected identity to start with %q, got %q", expectedPrefix, identity)
	}

	// The identity should contain the repo name (last component of path)
	if !strings.Contains(identity, h.repo.Name) {
		t.Errorf("Expected identity to contain repo name %q, got %q", h.repo.Name, identity)
	}
}

func TestBackfillRepoIdentities_SkipsNonGitRepos(t *testing.T) {
	h := newSyncTestHelper(t)

	// h.repo is already a non-git repo (just a temp dir)

	// Clear identity to simulate legacy repo
	h.clearRepoIdentity(h.repo.ID)

	// Backfill should set a local:// identity for non-git repos
	count, err := h.db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 repo backfilled with local:// identity, got %d", count)
	}

	// Verify identity is set to local:// prefix
	identity := h.getRepoIdentity(h.repo.ID)
	if !strings.HasPrefix(identity, "local://") {
		t.Errorf("Expected identity with local:// prefix, got %q", identity)
	}
}

func TestBackfillRepoIdentities_SkipsReposWithIdentity(t *testing.T) {
	h := newSyncTestHelper(t)

	// Set identity for h.repo
	err := h.db.SetRepoIdentity(h.repo.ID, "https://github.com/user/existing.git")
	if err != nil {
		t.Fatalf("SetRepoIdentity failed: %v", err)
	}

	// Backfill should not change existing identity
	count, err := h.db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 repos backfilled (already has identity), got %d", count)
	}

	// Verify identity unchanged
	identity := h.getRepoIdentity(h.repo.ID)
	if identity != "https://github.com/user/existing.git" {
		t.Errorf("Expected identity unchanged, got %q", identity)
	}
}

func TestBackfillRepoIdentities_SkipsMissingPaths(t *testing.T) {
	h := newSyncTestHelper(t)

	// Set identity for h.repo so it doesn't get backfilled
	err := h.db.SetRepoIdentity(h.repo.ID, "https://github.com/user/existing.git")
	if err != nil {
		t.Fatalf("SetRepoIdentity failed: %v", err)
	}

	// Create a repo pointing to a non-existent path (subpath of temp dir that doesn't exist)
	nonExistentPath := filepath.Join(t.TempDir(), "this-subdir-does-not-exist", "nested")
	repoID := h.insertLegacyRepo(nonExistentPath, "missing-repo")

	// Backfill should still set a local:// identity for missing paths
	// (better to have an identity than none, even for stale entries)
	count, err := h.db.BackfillRepoIdentities()
	if err != nil {
		t.Fatalf("BackfillRepoIdentities failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 repo backfilled with local:// identity, got %d", count)
	}

	// Verify identity is set to local:// prefix
	identity := h.getRepoIdentity(repoID)
	if !strings.HasPrefix(identity, "local://") {
		t.Errorf("Expected identity with local:// prefix, got %q", identity)
	}
}

func TestUpsertPulledJob_BackfillsModel(t *testing.T) {
	// This test verifies that upserting a pulled job with a model value backfills
	// an existing job that has NULL model (COALESCE behavior in SQLite)
	h := newSyncTestHelper(t)

	// Insert a job with NULL model using EnqueueJob (which sets model to empty string by default)
	// We need to directly insert with NULL model to test the COALESCE behavior
	jobUUID := "test-uuid-backfill-" + time.Now().Format("20060102150405")
	h.insertJobWithNullModel(jobUUID, h.repo.ID)

	// Verify model is NULL
	modelBefore := h.getJobModel(jobUUID)
	if modelBefore != nil {
		t.Fatalf("Expected model to be NULL before upsert, got %q", *modelBefore)
	}

	// Upsert with a model value - should backfill
	pulledJob := PulledJob{
		UUID:            jobUUID,
		RepoIdentity:    h.repo.RootPath,
		GitRef:          "HEAD",
		Agent:           "test-agent",
		Model:           "gpt-4", // Now providing a model
		Status:          "done",
		SourceMachineID: "test-machine",
		EnqueuedAt:      time.Now(),
		UpdatedAt:       time.Now(),
	}
	err := h.db.UpsertPulledJob(pulledJob, h.repo.ID, nil)
	if err != nil {
		t.Fatalf("UpsertPulledJob failed: %v", err)
	}

	// Verify model was backfilled
	modelAfter := h.getJobModel(jobUUID)
	if modelAfter == nil {
		t.Error("Expected model to be backfilled, but it's still NULL")
	} else if *modelAfter != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got %q", *modelAfter)
	}

	// Also verify that upserting with empty model doesn't clear existing model
	pulledJob.Model = "" // Empty model
	err = h.db.UpsertPulledJob(pulledJob, h.repo.ID, nil)
	if err != nil {
		t.Fatalf("UpsertPulledJob (empty model) failed: %v", err)
	}

	modelPreserved := h.getJobModel(jobUUID)
	if modelPreserved == nil || *modelPreserved != "gpt-4" {
		if modelPreserved == nil {
			t.Errorf("Expected model to be preserved as 'gpt-4' when upserting with empty model, got nil")
		} else {
			t.Errorf("Expected model to be preserved as 'gpt-4' when upserting with empty model, got %q", *modelPreserved)
		}
	}
}
