package storage

import (
	"database/sql"
	"testing"
	"time"
)

func setJobTimestamps(t *testing.T, db *DB, jobID int64, tstamp time.Time) {
	t.Helper()
	ts := tstamp.UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE review_jobs SET synced_at = ?, updated_at = ? WHERE id = ?`, ts, ts, jobID)
	if err != nil {
		t.Fatalf("setJobTimestamps: %v", err)
	}
}

func syncableToPulledJob(s *SyncableJob) PulledJob {
	return PulledJob{
		UUID:            s.UUID,
		RepoIdentity:    s.RepoIdentity,
		GitRef:          s.GitRef,
		Agent:           s.Agent,
		Model:           s.Model,
		Reasoning:       s.Reasoning,
		JobType:         s.JobType,
		ReviewType:      s.ReviewType,
		PatchID:         s.PatchID,
		Status:          s.Status,
		Agentic:         s.Agentic,
		EnqueuedAt:      s.EnqueuedAt,
		StartedAt:       s.StartedAt,
		FinishedAt:      s.FinishedAt,
		Prompt:          s.Prompt,
		DiffContent:     s.DiffContent,
		Error:           s.Error,
		SourceMachineID: s.SourceMachineID,
		UpdatedAt:       s.UpdatedAt,
	}
}

func enqueueTestJob(t *testing.T, db *DB, repoID, commitID int64, gitRef, patchID string) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:   repoID,
		CommitID: commitID,
		GitRef:   gitRef,
		Agent:    "test",
		PatchID:  patchID,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	return job
}

func TestPatchIDSyncRoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	machineID, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID: %v", err)
	}

	repo := createRepo(t, db, t.TempDir())
	commit := createCommit(t, db, repo.ID, "patchid-sync-sha")

	// Enqueue a job with a patch_id
	job := enqueueTestJob(t, db, repo.ID, commit.ID, "patchid-sync-sha", "deadbeef9999")

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

	repo2 := createRepo(t, db2, t.TempDir())
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

	pulledJob := syncableToPulledJob(found)

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

	job := enqueueTestJob(t, db, repo.ID, commit.ID, "running-oldsha", "patchRUN")

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

	job := enqueueTestJob(t, db, repo.ID, commit.ID, "rjrun-oldsha", "patchRJRUN")
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

	repo := createRepo(t, db, t.TempDir())
	commit := createCommit(t, db, repo.ID, "resync-oldsha")

	job := enqueueTestJob(t, db, repo.ID, commit.ID, "resync-oldsha", "patchRESYNC")

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
	setJobTimestamps(t, db, job.ID, time.Now().Add(-time.Hour))

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
