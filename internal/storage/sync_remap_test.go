package storage

import (
	"database/sql"
	"testing"
	"time"
)

func TestPatchIDSyncRoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID: %v", err)
	}

	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	commit, err := db.GetOrCreateCommit(
		repo.ID, "patchid-sync-sha", "Author", "Subject", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}

	// Enqueue a job with a patch_id
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "patchid-sync-sha",
		Agent:    "test",
		PatchID:  "deadbeef9999",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Complete the job so it becomes sync-eligible
	_, err = db.ClaimJob("worker-sync")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	// Verify patch_id appears in GetJobsToSync
	syncJobs, err := db.GetJobsToSync(machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync: %v", err)
	}
	var found *SyncableJob
	for i := range syncJobs {
		if syncJobs[i].ID == job.ID {
			found = &syncJobs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("job not returned by GetJobsToSync")
	}
	if found.PatchID != "deadbeef9999" {
		t.Errorf("GetJobsToSync PatchID: got %q, want %q",
			found.PatchID, "deadbeef9999")
	}

	// Simulate pull: upsert back into a fresh DB via UpsertPulledJob
	db2 := openTestDB(t)
	defer db2.Close()

	repo2, err := db2.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("db2 GetOrCreateRepo: %v", err)
	}
	commitID2 := int64(0)
	if found.CommitSHA != "" {
		c, err := db2.GetOrCreateCommit(
			repo2.ID, found.CommitSHA,
			found.CommitAuthor, found.CommitSubject, found.CommitTimestamp,
		)
		if err != nil {
			t.Fatalf("db2 GetOrCreateCommit: %v", err)
		}
		commitID2 = c.ID
	}

	pulledJob := PulledJob{
		UUID:            found.UUID,
		RepoIdentity:    found.RepoIdentity,
		GitRef:          found.GitRef,
		Agent:           found.Agent,
		Model:           found.Model,
		Reasoning:       found.Reasoning,
		JobType:         found.JobType,
		ReviewType:      found.ReviewType,
		PatchID:         found.PatchID,
		Status:          found.Status,
		Agentic:         found.Agentic,
		EnqueuedAt:      found.EnqueuedAt,
		StartedAt:       found.StartedAt,
		FinishedAt:      found.FinishedAt,
		Prompt:          found.Prompt,
		DiffContent:     found.DiffContent,
		Error:           found.Error,
		SourceMachineID: found.SourceMachineID,
		UpdatedAt:       found.UpdatedAt,
	}

	cid := &commitID2
	err = db2.UpsertPulledJob(pulledJob, repo2.ID, cid)
	if err != nil {
		t.Fatalf("UpsertPulledJob: %v", err)
	}

	// Verify patch_id survived the round trip
	var patchIDVal sql.NullString
	err = db2.QueryRow(
		`SELECT patch_id FROM review_jobs WHERE uuid = ?`, found.UUID,
	).Scan(&patchIDVal)
	if err != nil {
		t.Fatalf("query patch_id in db2: %v", err)
	}
	if !patchIDVal.Valid || patchIDVal.String != "deadbeef9999" {
		t.Errorf("patch_id after UpsertPulledJob: got %v, want %q",
			patchIDVal, "deadbeef9999")
	}
}

func TestRemapJobGitRef_RunningJob(t *testing.T) {
	// Running jobs must be skipped by remap: the worker has already
	// built the prompt with the old SHA, so updating git_ref would
	// create a mismatch between the stored prompt and the ref.
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-remap-running")
	commit := createCommit(t, db, repo.ID, "running-oldsha")

	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "running-oldsha",
		Agent:    "test",
		PatchID:  "patchRUN",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Set job to running
	setJobStatus(t, db, job.ID, JobStatusRunning)

	// Remap should skip running jobs
	newCommit := createCommit(t, db, repo.ID, "running-newsha")
	n, err := db.RemapJobGitRef(
		repo.ID, "running-oldsha", "running-newsha",
		"patchRUN", newCommit.ID,
	)
	if err != nil {
		t.Fatalf("RemapJobGitRef: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows updated for running job, got %d", n)
	}

	got, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.GitRef != "running-oldsha" {
		t.Errorf(
			"running job git_ref should be unchanged, got %q",
			got.GitRef,
		)
	}
	if got.Status != JobStatusRunning {
		t.Errorf("status should remain running, got %s", got.Status)
	}
}

func TestRemapJob_RunningJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-remap-job-running")
	commit := createCommit(t, db, repo.ID, "rjrun-oldsha")

	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "rjrun-oldsha",
		Agent:    "test",
		PatchID:  "patchRJRUN",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	setJobStatus(t, db, job.ID, JobStatusRunning)

	n, err := db.RemapJob(
		repo.ID, "rjrun-oldsha", "rjrun-newsha",
		"patchRJRUN", "author", "subject", time.Now(),
	)
	if err != nil {
		t.Fatalf("RemapJob: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows remapped, got %d", n)
	}

	got, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.GitRef != "rjrun-oldsha" {
		t.Errorf("git_ref should be unchanged, got %q", got.GitRef)
	}
	if got.Status != JobStatusRunning {
		t.Errorf("status should remain running, got %s", got.Status)
	}
}

func TestRemapTriggersResync(t *testing.T) {
	// After remapping a synced job, updated_at should exceed synced_at,
	// causing GetJobsToSync to include it again.
	db := openTestDB(t)
	defer db.Close()

	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID: %v", err)
	}

	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	commit, err := db.GetOrCreateCommit(
		repo.ID, "resync-oldsha", "Author", "Subject", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}

	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "resync-oldsha",
		Agent:    "test",
		PatchID:  "patchRESYNC",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Complete and mark synced
	_, err = db.ClaimJob("worker-resync")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	// Set synced_at to a past time so remap's updated_at is guaranteed later
	pastTime := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_, err = db.Exec(
		`UPDATE review_jobs SET synced_at = ?, updated_at = ? WHERE id = ?`,
		pastTime, pastTime, job.ID,
	)
	if err != nil {
		t.Fatalf("set synced_at: %v", err)
	}

	// Verify job is NOT returned by GetJobsToSync (updated_at == synced_at)
	preJobs, err := db.GetJobsToSync(machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync (pre): %v", err)
	}
	for _, j := range preJobs {
		if j.ID == job.ID {
			t.Fatal("job should not appear in sync before remap")
		}
	}

	// Remap the job — this sets updated_at to time.Now() which is after pastTime
	newCommit := createCommit(t, db, repo.ID, "resync-newsha")
	n, err := db.RemapJobGitRef(
		repo.ID, "resync-oldsha", "resync-newsha",
		"patchRESYNC", newCommit.ID,
	)
	if err != nil {
		t.Fatalf("RemapJobGitRef: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row updated, got %d", n)
	}

	// Now GetJobsToSync should include it again
	postJobs, err := db.GetJobsToSync(machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync (post): %v", err)
	}
	found := false
	for _, j := range postJobs {
		if j.ID == job.ID {
			found = true
			if j.GitRef != "resync-newsha" {
				t.Errorf("synced job git_ref: got %q, want %q",
					j.GitRef, "resync-newsha")
			}
			break
		}
	}
	if !found {
		t.Error("remapped job should appear in GetJobsToSync")
	}
}
