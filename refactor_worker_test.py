import re

with open("internal/daemon/worker_test.go", "r") as f:
    content = f.read()

# 1. Add const
content = content.replace(
"""type workerTestContext struct {
	DB          *storage.DB""",
"""const testWorkerID = "test-worker"

type workerTestContext struct {
	DB          *storage.DB""")

# 2. Add helpers
helpers = """// createJobWithAgent enqueues a job for the given SHA and agent and returns it.
func (c *workerTestContext) createJobWithAgent(t *testing.T, sha, agent string) *storage.ReviewJob {
	t.Helper()
	commit, err := c.DB.GetOrCreateCommit(c.Repo.ID, sha, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := c.DB.EnqueueJob(storage.EnqueueOpts{RepoID: c.Repo.ID, CommitID: commit.ID, GitRef: sha, Agent: agent})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	return job
}

// createAndClaimJobWithAgent enqueues and claims a job with a specific agent.
func (c *workerTestContext) createAndClaimJobWithAgent(t *testing.T, sha, workerID, agent string) *storage.ReviewJob {
	t.Helper()
	job := c.createJobWithAgent(t, sha, agent)
	claimed, err := c.DB.ClaimJob(workerID)
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("Expected to claim job %d, got %d", job.ID, claimed.ID)
	}
	return claimed
}

// createJob enqueues a job for the given SHA and returns it.
func (c *workerTestContext) createJob(t *testing.T, sha string) *storage.ReviewJob {
	t.Helper()
	return c.createJobWithAgent(t, sha, "test")
}

// createAndClaimJob enqueues and claims a job, returning both.
func (c *workerTestContext) createAndClaimJob(t *testing.T, sha, workerID string) *storage.ReviewJob {
	t.Helper()
	return c.createAndClaimJobWithAgent(t, sha, workerID, "test")
}

// exhaustRetries exhausts retries for a job to simulate failure loop
func (c *workerTestContext) exhaustRetries(t *testing.T, job *storage.ReviewJob, workerID, agent string) *storage.ReviewJob {
	t.Helper()
	for i := range maxRetries {
		c.Pool.failOrRetryInner(workerID, job, agent, "connection reset", true)
		reclaimed, err := c.DB.ClaimJob(workerID)
		if err != nil || reclaimed == nil {
			t.Fatalf("re-claim after retry %d: %v", i, err)
		}
		job = reclaimed
	}
	return job
}"""

old_helpers = """// createJob enqueues a job for the given SHA and returns it.
func (c *workerTestContext) createJob(t *testing.T, sha string) *storage.ReviewJob {
	t.Helper()
	commit, err := c.DB.GetOrCreateCommit(c.Repo.ID, sha, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := c.DB.EnqueueJob(storage.EnqueueOpts{RepoID: c.Repo.ID, CommitID: commit.ID, GitRef: sha, Agent: "test"})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	return job
}

// createAndClaimJob enqueues and claims a job, returning both.
func (c *workerTestContext) createAndClaimJob(t *testing.T, sha, workerID string) *storage.ReviewJob {
	t.Helper()
	job := c.createJob(t, sha)
	claimed, err := c.DB.ClaimJob(workerID)
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("Expected to claim job %d, got %d", job.ID, claimed.ID)
	}
	return job
}"""

content = content.replace(old_helpers, helpers)

# 3. Replace test blocks

block1 = """	// Enqueue a job with the alias "claude" (canonical: "claude-code")
	commit, err := tc.DB.GetOrCreateCommit(
		tc.Repo.ID, sha, "A", "S", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}
	job, err := tc.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID:   tc.Repo.ID,
		CommitID: commit.ID,
		GitRef:   sha,
		Agent:    "claude",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, err := tc.DB.ClaimJob("test-worker")
	if err != nil || claimed.ID != job.ID {
		t.Fatalf("ClaimJob: err=%v, claimed=%v", err, claimed)
	}"""
block1_new = """	// Enqueue a job with the alias "claude" (canonical: "claude-code")
	claimed := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, "claude")
	job := claimed"""
content = content.replace(block1, block1_new)


block2 = """	// Enqueue with agent "codex" (backup is "test")
	commit, err := tc.DB.GetOrCreateCommit(tc.Repo.ID, sha, "A", "S", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}
	job, err := tc.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID:   tc.Repo.ID,
		CommitID: commit.ID,
		GitRef:   sha,
		Agent:    "codex",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, err := tc.DB.ClaimJob("test-worker")
	if err != nil || claimed.ID != job.ID {
		t.Fatalf("ClaimJob: err=%v, claimed=%v", err, claimed)
	}"""
block2_new = """	// Enqueue with agent "codex" (backup is "test")
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, "codex")
	claimed := job"""
content = content.replace(block2, block2_new)


block3 = """	// Enqueue with agent "codex"
	commit, err := tc.DB.GetOrCreateCommit(
		tc.Repo.ID, sha, "A", "S", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}
	job, err := tc.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID:   tc.Repo.ID,
		CommitID: commit.ID,
		GitRef:   sha,
		Agent:    "codex",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, err := tc.DB.ClaimJob("test-worker")
	if err != nil || claimed.ID != job.ID {
		t.Fatalf("ClaimJob: err=%v, claimed=%v", err, claimed)
	}
	job.RepoPath = tc.TmpDir

	// Exhaust retries
	for i := range maxRetries {
		tc.Pool.failOrRetryInner(
			"test-worker", job, "codex",
			"connection reset", true,
		)
		reclaimed, claimErr := tc.DB.ClaimJob("test-worker")
		if claimErr != nil || reclaimed == nil {
			t.Fatalf("re-claim after retry %d: %v", i, claimErr)
		}
		job = reclaimed
	}"""
block3_new = """	// Enqueue with agent "codex"
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, "codex")
	job.RepoPath = tc.TmpDir

	// Exhaust retries
	job = tc.exhaustRetries(t, job, testWorkerID, "codex")"""
content = content.replace(block3, block3_new)
content = content.replace(block3, block3_new) # it happens twice


# Replace magic strings remaining
content = content.replace('"test-worker"', "testWorkerID")

with open("internal/daemon/worker_test.go", "w") as f:
    f.write(content)

