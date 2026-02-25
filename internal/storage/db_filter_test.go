package storage

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestJobCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Create 3 jobs that will stay queued
	for i := range 3 {
		sha := fmt.Sprintf("queued%d", i)
		commit := createCommit(t, db, repo.ID, sha)
		enqueueJob(t, db, repo.ID, commit.ID, sha)
	}

	// Create a job, claim it, and complete it
	commit := createCommit(t, db, repo.ID, "done1")
	job := enqueueJob(t, db, repo.ID, commit.ID, "done1")
	_, _ = db.ClaimJob("drain1")    // Claims oldest queued job (one of queued0-2)
	_, _ = db.ClaimJob("drain2")    // Claims next
	_, _ = db.ClaimJob("drain3")    // Claims next
	claimed, _ := db.ClaimJob("w1") // Should claim "done1" job now
	if claimed != nil {
		if claimed.ID != job.ID {
			t.Errorf("Expected to claim job 'done1' (ID %d), got %d", job.ID, claimed.ID)
		}
		db.CompleteJob(claimed.ID, "codex", "p", "o")
	}

	// Create a job, claim it, and fail it
	commit2 := createCommit(t, db, repo.ID, "fail1")
	enqueueJob(t, db, repo.ID, commit2.ID, "fail1")
	claimed2, _ := db.ClaimJob("w2")
	if claimed2 != nil {
		db.FailJob(claimed2.ID, "", "err")
	}

	queued, running, done, failed, _, _, _, err := db.GetJobCounts()
	if err != nil {
		t.Fatalf("GetJobCounts failed: %v", err)
	}

	// We expect: 0 queued (all were claimed), 1 done, 1 failed, 3 running
	if queued != 0 {
		t.Errorf("Expected 0 queued jobs, got %d", queued)
	}
	if running != 3 {
		t.Errorf("Expected 3 running jobs, got %d", running)
	}
	if done != 1 {
		t.Errorf("Expected 1 done, got %d", done)
	}
	if failed != 1 {
		t.Errorf("Expected 1 failed, got %d", failed)
	}
}

func TestCountStalledJobs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _, _ := createJobChain(t, db, "/tmp/test-repo", "recent1")
	_, _ = db.ClaimJob("worker-1")

	// No stalled jobs yet (just started)
	count, err := db.CountStalledJobs(30 * time.Minute)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 stalled jobs for recently started job, got %d", count)
	}

	// Create a job and manually set started_at to 1 hour ago (simulating stalled job)
	// Use UTC format (ends with Z) to test basic case
	commit2 := createCommit(t, db, repo.ID, "stalled1")
	job2 := enqueueJob(t, db, repo.ID, commit2.ID, "stalled1")
	backdateJobStart(t, db, job2.ID, 1*time.Hour)

	// Now we should have 1 stalled job (running > 30 min)
	count, err = db.CountStalledJobs(30 * time.Minute)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 stalled job for job started 1 hour ago (UTC), got %d", count)
	}

	// Create another stalled job with a non-UTC timezone offset to verify datetime() handles offsets
	// This exercises the fix for RFC3339 timestamps with timezone offsets like "-07:00"
	commit3 := createCommit(t, db, repo.ID, "stalled2")
	job3 := enqueueJob(t, db, repo.ID, commit3.ID, "stalled2")
	// Use a fixed timezone offset (e.g., UTC-7) instead of UTC
	tzMinus7 := time.FixedZone("UTC-7", -7*60*60)
	backdateJobStartWithOffset(t, db, job3.ID, 1*time.Hour, tzMinus7)

	// Should now have 2 stalled jobs - verifies datetime() parses both Z and offset formats
	count, err = db.CountStalledJobs(30 * time.Minute)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 stalled jobs (UTC and offset timestamp), got %d", count)
	}

	// With a longer threshold (2 hours), neither job should be considered stalled
	count, err = db.CountStalledJobs(2 * time.Hour)
	if err != nil {
		t.Fatalf("CountStalledJobs failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 stalled jobs with 2 hour threshold, got %d", count)
	}
}

func TestListReposWithReviewCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("empty database", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts()
		if err != nil {
			t.Fatalf("ListReposWithReviewCounts failed: %v", err)
		}
		if len(repos) != 0 {
			t.Errorf("Expected 0 repos, got %d", len(repos))
		}
		if totalCount != 0 {
			t.Errorf("Expected total count 0, got %d", totalCount)
		}
	})

	// Create repos and jobs
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")
	_ = createRepo(t, db, "/tmp/repo3") // will have 0 jobs

	// Add jobs to repo1 (3 jobs)
	for i := range 3 {
		sha := fmt.Sprintf("repo1-sha%d", i)
		commit := createCommit(t, db, repo1.ID, sha)
		enqueueJob(t, db, repo1.ID, commit.ID, sha)
	}

	// Add jobs to repo2 (2 jobs)
	for i := range 2 {
		sha := fmt.Sprintf("repo2-sha%d", i)
		commit := createCommit(t, db, repo2.ID, sha)
		enqueueJob(t, db, repo2.ID, commit.ID, sha)
	}

	t.Run("repos with varying job counts", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts()
		if err != nil {
			t.Fatalf("ListReposWithReviewCounts failed: %v", err)
		}

		// Should have 3 repos
		if len(repos) != 3 {
			t.Errorf("Expected 3 repos, got %d", len(repos))
		}

		// Total count should be 5 (3 + 2 + 0)
		if totalCount != 5 {
			t.Errorf("Expected total count 5, got %d", totalCount)
		}

		// Build map for easier assertions
		repoMap := make(map[string]int)
		for _, r := range repos {
			repoMap[r.Name] = r.Count
		}

		if repoMap["repo1"] != 3 {
			t.Errorf("Expected repo1 count 3, got %d", repoMap["repo1"])
		}
		if repoMap["repo2"] != 2 {
			t.Errorf("Expected repo2 count 2, got %d", repoMap["repo2"])
		}
		if repoMap["repo3"] != 0 {
			t.Errorf("Expected repo3 count 0, got %d", repoMap["repo3"])
		}
	})

	t.Run("counts include all job statuses", func(t *testing.T) {
		// Claim and complete one job in repo1
		claimed, _ := db.ClaimJob("worker-1")
		if claimed != nil {
			db.CompleteJob(claimed.ID, "codex", "prompt", "output")
		}

		// Claim and fail another job
		claimed2, _ := db.ClaimJob("worker-1")
		if claimed2 != nil {
			db.FailJob(claimed2.ID, "", "test error")
		}

		// Counts should still be the same (counts all jobs, not just completed)
		repos, totalCount, err := db.ListReposWithReviewCounts()
		if err != nil {
			t.Fatalf("ListReposWithReviewCounts failed: %v", err)
		}

		if totalCount != 5 {
			t.Errorf("Expected total count 5 (all statuses), got %d", totalCount)
		}

		repoMap := make(map[string]int)
		for _, r := range repos {
			repoMap[r.Name] = r.Count
		}

		if repoMap["repo1"] != 3 {
			t.Errorf("Expected repo1 count 3 (all statuses), got %d", repoMap["repo1"])
		}
	})
}

func TestListJobsWithRepoFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repos and jobs
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	// Add 3 jobs to repo1
	for i := range 3 {
		sha := fmt.Sprintf("repo1-sha%d", i)
		commit := createCommit(t, db, repo1.ID, sha)
		enqueueJob(t, db, repo1.ID, commit.ID, sha)
	}

	// Add 2 jobs to repo2
	for i := range 2 {
		sha := fmt.Sprintf("repo2-sha%d", i)
		commit := createCommit(t, db, repo2.ID, sha)
		enqueueJob(t, db, repo2.ID, commit.ID, sha)
	}

	t.Run("no filter returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 5 {
			t.Errorf("Expected 5 jobs, got %d", len(jobs))
		}
	})

	t.Run("repo filter returns only matching jobs", func(t *testing.T) {
		// Filter by root_path (not name) since repos with same name could exist at different paths
		jobs, err := db.ListJobs("", repo1.RootPath, 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 3 {
			t.Errorf("Expected 3 jobs for repo1, got %d", len(jobs))
		}
		for _, job := range jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("limit parameter works", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 2, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs with limit=2, got %d", len(jobs))
		}
	})

	t.Run("limit=0 returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 0, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 5 {
			t.Errorf("Expected 5 jobs with limit=0 (no limit), got %d", len(jobs))
		}
	})

	t.Run("repo filter with limit", func(t *testing.T) {
		jobs, err := db.ListJobs("", repo1.RootPath, 2, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs with repo filter and limit=2, got %d", len(jobs))
		}
		for _, job := range jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("status and repo filter combined", func(t *testing.T) {
		// Complete one job from repo1
		claimed, err := db.ClaimJob("worker-1")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		if err := db.CompleteJob(claimed.ID, "codex", "prompt", "output"); err != nil {
			t.Fatalf("CompleteJob failed: %v", err)
		}

		// Query for done jobs in repo1
		jobs, err := db.ListJobs("done", repo1.RootPath, 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 done job for repo1, got %d", len(jobs))
		}
		if len(jobs) > 0 && jobs[0].Status != JobStatusDone {
			t.Errorf("Expected status 'done', got '%s'", jobs[0].Status)
		}
	})

	t.Run("offset pagination", func(t *testing.T) {
		// Get first 2 jobs
		jobs1, err := db.ListJobs("", "", 2, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs1) != 2 {
			t.Errorf("Expected 2 jobs, got %d", len(jobs1))
		}

		// Get next 2 jobs with offset
		jobs2, err := db.ListJobs("", "", 2, 2)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs2) != 2 {
			t.Errorf("Expected 2 jobs with offset=2, got %d", len(jobs2))
		}

		// Ensure no overlap
		for _, j1 := range jobs1 {
			for _, j2 := range jobs2 {
				if j1.ID == j2.ID {
					t.Errorf("Job %d appears in both pages", j1.ID)
				}
			}
		}

		// Get remaining job with offset=4
		jobs3, err := db.ListJobs("", "", 2, 4)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		// 5 jobs total, offset 4 should give 1
		if len(jobs3) != 1 {
			t.Errorf("Expected 1 job with offset=4, got %d", len(jobs3))
		}
	})
}

func TestListJobsWithGitRefFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/repo-gitref")

	// Create jobs with different git refs
	refs := []string{"abc123", "def456", "abc123..def456", "dirty"}
	for _, ref := range refs {
		commit := createCommit(t, db, repo.ID, ref)
		enqueueJob(t, db, repo.ID, commit.ID, ref)
	}

	t.Run("git_ref filter returns matching job", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("abc123"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job with git_ref=abc123, got %d", len(jobs))
		}
		if len(jobs) > 0 && jobs[0].GitRef != "abc123" {
			t.Errorf("Expected GitRef 'abc123', got '%s'", jobs[0].GitRef)
		}
	})

	t.Run("git_ref filter with range ref", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("abc123..def456"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job with range ref, got %d", len(jobs))
		}
		if len(jobs) > 0 && jobs[0].GitRef != "abc123..def456" {
			t.Errorf("Expected GitRef 'abc123..def456', got '%s'", jobs[0].GitRef)
		}
	})

	t.Run("git_ref filter with no match returns empty", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("nonexistent"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 0 {
			t.Errorf("Expected 0 jobs with nonexistent git_ref, got %d", len(jobs))
		}
	})

	t.Run("empty git_ref filter returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 4 {
			t.Errorf("Expected 4 jobs with empty git_ref filter, got %d", len(jobs))
		}
	})

	t.Run("git_ref filter combined with repo filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", repo.RootPath, 50, 0, WithGitRef("def456"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job with git_ref and repo filter, got %d", len(jobs))
		}
	})
}

func TestListJobsWithBranchAndAddressedFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/repo-branch-addr")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create jobs on different branches
	branches := []string{"main", "main", "feature"}
	for i, br := range branches {
		sha := fmt.Sprintf("sha%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Branch: br, Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		// Complete the job so it has a review
		db.ClaimJob("w")
		db.CompleteJob(job.ID, "codex", "", fmt.Sprintf("output %d", i))

		// Mark first job as addressed
		if i == 0 {
			db.MarkReviewAddressedByJobID(job.ID, true)
		}
	}

	t.Run("branch filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs on main, got %d", len(jobs))
		}
	})

	t.Run("addressed=false filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithAddressed(false))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 unaddressed jobs, got %d", len(jobs))
		}
	})

	t.Run("addressed=true filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithAddressed(true))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 addressed job, got %d", len(jobs))
		}
	})

	t.Run("branch + addressed combined", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"), WithAddressed(false))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 unaddressed job on main, got %d", len(jobs))
		}
	})
}

func TestWithBranchOrEmpty(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/repo-branch-empty")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create jobs: one on "main", one on "feature", one branchless
	for i, br := range []string{"main", "feature", ""} {
		sha := fmt.Sprintf("sha-be-%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Branch: br, Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		db.ClaimJob("w")
		db.CompleteJob(job.ID, "codex", "", fmt.Sprintf("output %d", i))
	}

	t.Run("WithBranch strict excludes branchless", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("Expected 1 job, got %d", len(jobs))
		}
	})

	t.Run("WithBranchOrEmpty includes branchless", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranchOrEmpty("main"))
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("Expected 2 jobs (main + branchless), got %d", len(jobs))
		}
	})
}

func TestListJobsAndGetJobByIDReturnAgentic(t *testing.T) {
	// Test that agentic field is properly returned by ListJobs and GetJobByID
	db := openTestDB(t)
	defer db.Close()

	repoPath := filepath.Join(t.TempDir(), "agentic-test-repo")
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Enqueue a prompt job with agentic=true
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:  repo.ID,
		Agent:   "test-agent",
		Prompt:  "Review this code",
		Agentic: true,
	})
	if err != nil {
		t.Fatalf("EnqueuePromptJob failed: %v", err)
	}

	// Verify the returned job has Agentic set
	if !job.Agentic {
		t.Error("EnqueuePromptJob should return job with Agentic=true")
	}

	t.Run("ListJobs returns agentic field", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		if len(jobs) == 0 {
			t.Fatal("Expected at least one job")
		}

		// Find our job
		var found bool
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				if !j.Agentic {
					t.Errorf("ListJobs should return Agentic=true for job %d", j.ID)
				}
				break
			}
		}
		if !found {
			t.Errorf("Job %d not found in ListJobs result", job.ID)
		}
	})

	t.Run("GetJobByID returns agentic field", func(t *testing.T) {
		fetchedJob, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if !fetchedJob.Agentic {
			t.Errorf("GetJobByID should return Agentic=true for job %d", job.ID)
		}
	})

	// Also test with agentic=false to ensure we're not just always returning true
	t.Run("non-agentic job returns Agentic=false", func(t *testing.T) {
		nonAgenticJob, err := db.EnqueueJob(EnqueueOpts{
			RepoID: repo.ID,
			Agent:  "test-agent",
			Prompt: "Another review",
		})
		if err != nil {
			t.Fatalf("EnqueuePromptJob failed: %v", err)
		}

		// Check via GetJobByID
		fetchedJob, err := db.GetJobByID(nonAgenticJob.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetchedJob.Agentic {
			t.Errorf("GetJobByID should return Agentic=false for non-agentic job %d", nonAgenticJob.ID)
		}

		// Check via ListJobs
		jobs, err := db.ListJobs("", "", 50, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		var found bool
		for _, j := range jobs {
			if j.ID == nonAgenticJob.ID {
				found = true
				if j.Agentic {
					t.Errorf("ListJobs should return Agentic=false for non-agentic job %d", j.ID)
				}
				break
			}
		}
		if !found {
			t.Errorf("Non-agentic job %d not found in ListJobs result", nonAgenticJob.ID)
		}
	})
}

func TestListReposWithReviewCountsByBranch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repos
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	// Create commits and jobs with different branches
	commit1 := createCommit(t, db, repo1.ID, "abc123")
	commit2 := createCommit(t, db, repo1.ID, "def456")
	commit3 := createCommit(t, db, repo2.ID, "ghi789")

	job1 := enqueueJob(t, db, repo1.ID, commit1.ID, "abc123")
	job2 := enqueueJob(t, db, repo1.ID, commit2.ID, "def456")
	job3 := enqueueJob(t, db, repo2.ID, commit3.ID, "ghi789")

	// Update some jobs with branches
	setJobBranch(t, db, job1.ID, "main")
	setJobBranch(t, db, job3.ID, "main")
	setJobBranch(t, db, job2.ID, "feature")

	t.Run("filter by main branch", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCountsByBranch("main")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 2 {
			t.Errorf("Expected 2 repos with main branch, got %d", len(repos))
		}
		if totalCount != 2 {
			t.Errorf("Expected total count 2, got %d", totalCount)
		}
	})

	t.Run("filter by feature branch", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCountsByBranch("feature")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 1 {
			t.Errorf("Expected 1 repo with feature branch, got %d", len(repos))
		}
		if totalCount != 1 {
			t.Errorf("Expected total count 1, got %d", totalCount)
		}
	})

	t.Run("filter by (none) branch", func(t *testing.T) {
		// Add a job with no branch
		commit4 := createCommit(t, db, repo1.ID, "jkl012")
		enqueueJob(t, db, repo1.ID, commit4.ID, "jkl012")

		repos, totalCount, err := db.ListReposWithReviewCountsByBranch("(none)")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 1 {
			t.Errorf("Expected 1 repo with (none) branch, got %d", len(repos))
		}
		if totalCount != 1 {
			t.Errorf("Expected total count 1, got %d", totalCount)
		}
	})

	t.Run("empty filter returns all", func(t *testing.T) {
		repos, _, err := db.ListReposWithReviewCountsByBranch("")
		if err != nil {
			t.Fatalf("ListReposWithReviewCountsByBranch failed: %v", err)
		}
		if len(repos) != 2 {
			t.Errorf("Expected 2 repos, got %d", len(repos))
		}
	})
}

func TestListBranchesWithCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repos
	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	// Create commits and jobs with different branches
	commit1 := createCommit(t, db, repo1.ID, "abc123")
	commit2 := createCommit(t, db, repo1.ID, "def456")
	commit3 := createCommit(t, db, repo1.ID, "ghi789")
	commit4 := createCommit(t, db, repo2.ID, "jkl012")
	commit5 := createCommit(t, db, repo2.ID, "mno345")

	job1 := enqueueJob(t, db, repo1.ID, commit1.ID, "abc123")
	job2 := enqueueJob(t, db, repo1.ID, commit2.ID, "def456")
	job3 := enqueueJob(t, db, repo1.ID, commit3.ID, "ghi789")
	job4 := enqueueJob(t, db, repo2.ID, commit4.ID, "jkl012")
	job5 := enqueueJob(t, db, repo2.ID, commit5.ID, "mno345")

	// Update branches
	setJobBranch(t, db, job1.ID, "main")
	setJobBranch(t, db, job2.ID, "main")
	setJobBranch(t, db, job4.ID, "main")
	setJobBranch(t, db, job3.ID, "feature")
	// job 5 has no branch (NULL)

	t.Run("list all branches", func(t *testing.T) {
		result, err := db.ListBranchesWithCounts(nil)
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if len(result.Branches) != 3 {
			t.Errorf("Expected 3 branches, got %d", len(result.Branches))
		}
		if result.TotalCount != 5 {
			t.Errorf("Expected total count 5, got %d", result.TotalCount)
		}
		if result.NullsRemaining != 1 {
			t.Errorf("Expected 1 null remaining, got %d", result.NullsRemaining)
		}
	})

	t.Run("filter by single repo", func(t *testing.T) {
		// Use repo1.RootPath which is the normalized path stored in the DB
		result, err := db.ListBranchesWithCounts([]string{repo1.RootPath})
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if len(result.Branches) != 2 {
			t.Errorf("Expected 2 branches for repo1, got %d", len(result.Branches))
		}
		if result.TotalCount != 3 {
			t.Errorf("Expected total count 3 for repo1, got %d", result.TotalCount)
		}
	})

	t.Run("filter by multiple repos", func(t *testing.T) {
		// Use repo RootPath values which are the normalized paths stored in the DB
		result, err := db.ListBranchesWithCounts([]string{repo1.RootPath, repo2.RootPath})
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if len(result.Branches) != 3 {
			t.Errorf("Expected 3 branches for both repos, got %d", len(result.Branches))
		}
		if result.TotalCount != 5 {
			t.Errorf("Expected total count 5 for both repos, got %d", result.TotalCount)
		}
	})

	t.Run("no nulls when all have branches", func(t *testing.T) {
		setJobBranch(t, db, job5.ID, "develop")
		result, err := db.ListBranchesWithCounts(nil)
		if err != nil {
			t.Fatalf("ListBranchesWithCounts failed: %v", err)
		}
		if result.NullsRemaining != 0 {
			t.Errorf("Expected 0 nulls remaining, got %d", result.NullsRemaining)
		}
	})
}

func TestListJobsVerdictForBranchRangeReview(t *testing.T) {
	// Regression test: branch range reviews (commit_id NULL, git_ref contains "..")
	// should have their verdict parsed, not be misclassified as task jobs.
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, filepath.Join(t.TempDir(), "range-verdict-repo"))

	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc123..def456", Agent: "codex"})
	if err != nil {
		t.Fatalf("EnqueueRangeJob failed: %v", err)
	}

	// Claim and complete with findings output
	_, err = db.ClaimJob("worker-0")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	err = db.CompleteJob(job.ID, "codex", "review prompt", "- Medium — Bug in line 42\nSummary: found issues.")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	jobs, err := db.ListJobs("", "", 50, 0)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}

	var found bool
	for _, j := range jobs {
		if j.ID == job.ID {
			found = true
			if j.Verdict == nil {
				t.Fatal("expected verdict to be parsed for branch range review, got nil")
			}
			if *j.Verdict != "F" {
				t.Errorf("expected verdict F for review with findings, got %q", *j.Verdict)
			}
			break
		}
	}
	if !found {
		t.Fatal("branch range job not found in ListJobs result")
	}
}

func TestListJobsWithJobTypeFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, commit, reviewJob := createJobChain(t, db, "/tmp/repo-jobtype", "jt-sha")

	// Create a fix job parented to the review
	_, err := db.EnqueueJob(EnqueueOpts{
		RepoID:      repo.ID,
		CommitID:    commit.ID,
		GitRef:      "jt-sha",
		Agent:       "codex",
		JobType:     JobTypeFix,
		ParentJobID: reviewJob.ID,
	})
	if err != nil {
		t.Fatalf("EnqueueJob fix failed: %v", err)
	}

	tests := []struct {
		name          string
		opts          []ListJobsOption
		expectedLen   int
		expectedTypes []string // if nil, only checks length
	}{
		{"filter by fix returns only fix jobs", []ListJobsOption{WithJobType("fix")}, 1, []string{JobTypeFix}},
		{"filter by review returns only review jobs", []ListJobsOption{WithJobType("review")}, 1, []string{JobTypeReview}},
		{"no filter returns all jobs", nil, 2, nil},
		{"nonexistent type returns empty", []ListJobsOption{WithJobType("nonexistent")}, 0, nil},
		{"exclude fix returns only non-fix jobs", []ListJobsOption{WithExcludeJobType("fix")}, 1, []string{JobTypeReview}},
		{"exclude review returns only non-review jobs", []ListJobsOption{WithExcludeJobType("review")}, 1, []string{JobTypeFix}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs, err := db.ListJobs("", "", 50, 0, tt.opts...)
			if err != nil {
				t.Fatalf("ListJobs failed: %v", err)
			}
			if len(jobs) != tt.expectedLen {
				t.Fatalf("Expected %d jobs, got %d", tt.expectedLen, len(jobs))
			}
			if tt.expectedTypes != nil {
				for i, typ := range tt.expectedTypes {
					if jobs[i].JobType != typ {
						t.Errorf("Expected job_type %q, got %q", typ, jobs[i].JobType)
					}
				}
			}
		})
	}
}
