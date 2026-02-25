import re

with open("internal/storage/sync_test.go", "r") as f:
    content = f.read()

# Refactor TestGetCommentsToSync_LegacyCommentsExcluded
old_get_comments = """func TestGetCommentsToSync_LegacyCommentsExcluded(t *testing.T) {
	// This test verifies that legacy responses with job_id IS NULL (tied only to commit_id)
	// are excluded from sync since they cannot be synced via job_uuid.
	db := openTestDB(t)
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
	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "legacy-resp-sha", Agent: "test", Reasoning: "thorough"})
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
	err = db.MarkJobSynced(job.ID)"""

new_get_comments = """func TestGetCommentsToSync_LegacyCommentsExcluded(t *testing.T) {
	// This test verifies that legacy responses with job_id IS NULL (tied only to commit_id)
	// are excluded from sync since they cannot be synced via job_uuid.
	h := newSyncTestHelper(t)
	job := h.createCompletedJob("legacy-resp-sha")

	// Need commit ID for the legacy response
	commit, err := h.db.GetCommitBySHA(h.repo.ID, "legacy-resp-sha")
	if err != nil {
		t.Fatalf("GetCommitBySHA failed: %v", err)
	}

	// Mark job as synced (required before responses can sync due to FK ordering)
	err = h.db.MarkJobSynced(job.ID)"""

content = content.replace(old_get_comments, new_get_comments)

# Also need to replace db. with h.db. in this function
func_start = content.find("func TestGetCommentsToSync_LegacyCommentsExcluded(t *testing.T) {")
func_end = content.find("}\n\nfunc TestUpsertPulledResponse_MissingParentJob", func_start)
if func_start != -1 and func_end != -1:
    body = content[func_start:func_end]
    body = body.replace("db.MarkJobSynced", "h.db.MarkJobSynced")
    body = body.replace("db.AddCommentToJob", "h.db.AddCommentToJob")
    body = body.replace("db.Exec", "h.db.Exec")
    body = body.replace("db.GetCommentsToSync", "h.db.GetCommentsToSync")
    content = content[:func_start] + body + content[func_end:]


# Refactor TestUpsertPulledResponse_WithParentJob
old_upsert = """func TestUpsertPulledResponse_WithParentJob(t *testing.T) {
	// This test verifies UpsertPulledResponse works when the parent job exists
	db := openTestDB(t)
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
	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "parent-job-sha", Agent: "test", Reasoning: "thorough"})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}"""

new_upsert = """func TestUpsertPulledResponse_WithParentJob(t *testing.T) {
	// This test verifies UpsertPulledResponse works when the parent job exists
	h := newSyncTestHelper(t)
	job := h.createPendingJob("parent-job-sha")"""

content = content.replace(old_upsert, new_upsert)

func_start2 = content.find("func TestUpsertPulledResponse_WithParentJob(t *testing.T) {")
func_end2 = content.find("}\n\nfunc TestSyncWorker_SyncNowReturnsErrorWhenNotRunning", func_start2)
if func_start2 != -1 and func_end2 != -1:
    body2 = content[func_start2:func_end2]
    body2 = body2.replace("err = db.UpsertPulledResponse", "err := h.db.UpsertPulledResponse")
    body2 = body2.replace("err = db.QueryRow", "err = h.db.QueryRow")
    content = content[:func_start2] + body2 + content[func_end2:]

with open("internal/storage/sync_test.go", "w") as f:
    f.write(content)

