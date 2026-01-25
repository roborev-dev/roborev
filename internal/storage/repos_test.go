package storage

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestEnqueuePromptJob(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/prompt-test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("creates job with custom prompt", func(t *testing.T) {
		customPrompt := "Explain the architecture of this codebase"
		job, err := db.EnqueuePromptJob(repo.ID, "claude-code", "thorough", customPrompt, false)
		if err != nil {
			t.Fatalf("EnqueuePromptJob failed: %v", err)
		}

		if job.GitRef != "prompt" {
			t.Errorf("Expected git_ref 'prompt', got '%s'", job.GitRef)
		}
		if job.Agent != "claude-code" {
			t.Errorf("Expected agent 'claude-code', got '%s'", job.Agent)
		}
		if job.Reasoning != "thorough" {
			t.Errorf("Expected reasoning 'thorough', got '%s'", job.Reasoning)
		}
		if job.Prompt != customPrompt {
			t.Errorf("Expected prompt '%s', got '%s'", customPrompt, job.Prompt)
		}
		if job.Status != JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", job.Status)
		}
	})

	t.Run("defaults reasoning to thorough", func(t *testing.T) {
		job, err := db.EnqueuePromptJob(repo.ID, "codex", "", "test prompt", false)
		if err != nil {
			t.Fatalf("EnqueuePromptJob failed: %v", err)
		}
		if job.Reasoning != "thorough" {
			t.Errorf("Expected default reasoning 'thorough', got '%s'", job.Reasoning)
		}
	})

	t.Run("claimed job has prompt loaded", func(t *testing.T) {
		// Drain any existing queued jobs first
		for {
			job, err := db.ClaimJob("drain-worker")
			if err != nil {
				t.Fatalf("ClaimJob failed during drain: %v", err)
			}
			if job == nil {
				break
			}
		}

		customPrompt := "Find security issues in the codebase"
		_, err := db.EnqueuePromptJob(repo.ID, "claude-code", "standard", customPrompt, false)
		if err != nil {
			t.Fatalf("EnqueuePromptJob failed: %v", err)
		}

		// Claim the job
		claimed, err := db.ClaimJob("test-worker")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		if claimed == nil {
			t.Fatal("Expected to claim a job")
		}

		if claimed.GitRef != "prompt" {
			t.Errorf("Expected git_ref 'prompt', got '%s'", claimed.GitRef)
		}
		if claimed.Prompt != customPrompt {
			t.Errorf("Expected prompt '%s', got '%s'", customPrompt, claimed.Prompt)
		}
	})

	t.Run("agentic flag persists and is claimed correctly", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, err := db.GetOrCreateRepo("/tmp/agentic-test")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}

		// Enqueue with agentic=true
		job, err := db.EnqueuePromptJob(repo.ID, "claude-code", "thorough", "Test agentic prompt", true)
		if err != nil {
			t.Fatalf("EnqueuePromptJob failed: %v", err)
		}

		if !job.Agentic {
			t.Error("Expected Agentic to be true on returned job")
		}

		// Verify it's stored in the database
		var agenticInt int
		err = db.QueryRow(`SELECT agentic FROM review_jobs WHERE id = ?`, job.ID).Scan(&agenticInt)
		if err != nil {
			t.Fatalf("Failed to query agentic: %v", err)
		}
		if agenticInt != 1 {
			t.Errorf("Expected agentic=1 in database, got %d", agenticInt)
		}

		// Claim the job and verify agentic flag is loaded
		claimed, err := db.ClaimJob("test-worker")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		if claimed == nil {
			t.Fatal("Expected to claim a job")
		}

		if !claimed.Agentic {
			t.Error("Expected Agentic to be true on claimed job")
		}
	})

	t.Run("agentic flag defaults to false", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, err := db.GetOrCreateRepo("/tmp/agentic-default-test")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}

		// Enqueue with agentic=false
		job, err := db.EnqueuePromptJob(repo.ID, "codex", "standard", "Non-agentic prompt", false)
		if err != nil {
			t.Fatalf("EnqueuePromptJob failed: %v", err)
		}

		if job.Agentic {
			t.Error("Expected Agentic to be false")
		}

		// Claim and verify
		claimed, err := db.ClaimJob("test-worker")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		if claimed == nil {
			t.Fatal("Expected to claim a job")
		}

		if claimed.Agentic {
			t.Error("Expected Agentic to be false on claimed job")
		}
	})
}

func TestRenameRepo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/rename-test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("rename by path", func(t *testing.T) {
		affected, err := db.RenameRepo("/tmp/rename-test", "new-name")
		if err != nil {
			t.Fatalf("RenameRepo failed: %v", err)
		}
		if affected != 1 {
			t.Errorf("Expected 1 affected row, got %d", affected)
		}

		// Verify the rename
		updated, err := db.GetRepoByID(repo.ID)
		if err != nil {
			t.Fatalf("GetRepoByID failed: %v", err)
		}
		if updated.Name != "new-name" {
			t.Errorf("Expected name 'new-name', got '%s'", updated.Name)
		}
	})

	t.Run("rename by name", func(t *testing.T) {
		affected, err := db.RenameRepo("new-name", "another-name")
		if err != nil {
			t.Fatalf("RenameRepo failed: %v", err)
		}
		if affected != 1 {
			t.Errorf("Expected 1 affected row, got %d", affected)
		}

		updated, err := db.GetRepoByID(repo.ID)
		if err != nil {
			t.Fatalf("GetRepoByID failed: %v", err)
		}
		if updated.Name != "another-name" {
			t.Errorf("Expected name 'another-name', got '%s'", updated.Name)
		}
	})

	t.Run("rename nonexistent repo returns 0", func(t *testing.T) {
		affected, err := db.RenameRepo("nonexistent", "something")
		if err != nil {
			t.Fatalf("RenameRepo failed: %v", err)
		}
		if affected != 0 {
			t.Errorf("Expected 0 affected rows for nonexistent repo, got %d", affected)
		}
	})
}

func TestListRepos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("empty database", func(t *testing.T) {
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos failed: %v", err)
		}
		if len(repos) != 0 {
			t.Errorf("Expected 0 repos, got %d", len(repos))
		}
	})

	// Create repos
	_, err := db.GetOrCreateRepo("/tmp/repo-a")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	_, err = db.GetOrCreateRepo("/tmp/repo-b")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("lists repos in order", func(t *testing.T) {
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos failed: %v", err)
		}
		if len(repos) != 2 {
			t.Errorf("Expected 2 repos, got %d", len(repos))
		}
		// Should be ordered by name
		if len(repos) >= 2 && repos[0].Name > repos[1].Name {
			t.Errorf("Repos not ordered by name: %s > %s", repos[0].Name, repos[1].Name)
		}
	})
}

func TestGetRepoByID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/getbyid-test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		found, err := db.GetRepoByID(repo.ID)
		if err != nil {
			t.Fatalf("GetRepoByID failed: %v", err)
		}
		if found.ID != repo.ID {
			t.Errorf("Expected ID %d, got %d", repo.ID, found.ID)
		}
		if found.RootPath != repo.RootPath {
			t.Errorf("Expected path '%s', got '%s'", repo.RootPath, found.RootPath)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := db.GetRepoByID(99999)
		if err == nil {
			t.Error("Expected error for nonexistent ID")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Expected sql.ErrNoRows, got: %v", err)
		}
	})
}

func TestGetRepoByName(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/getbyname-test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		found, err := db.GetRepoByName("getbyname-test")
		if err != nil {
			t.Fatalf("GetRepoByName failed: %v", err)
		}
		if found.ID != repo.ID {
			t.Errorf("Expected ID %d, got %d", repo.ID, found.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := db.GetRepoByName("nonexistent")
		if err == nil {
			t.Error("Expected error for nonexistent name")
		}
	})
}

func TestFindRepo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/findrepo-test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("find by path", func(t *testing.T) {
		found, err := db.FindRepo("/tmp/findrepo-test")
		if err != nil {
			t.Fatalf("FindRepo by path failed: %v", err)
		}
		if found.ID != repo.ID {
			t.Errorf("Expected ID %d, got %d", repo.ID, found.ID)
		}
	})

	t.Run("find by name", func(t *testing.T) {
		found, err := db.FindRepo("findrepo-test")
		if err != nil {
			t.Fatalf("FindRepo by name failed: %v", err)
		}
		if found.ID != repo.ID {
			t.Errorf("Expected ID %d, got %d", repo.ID, found.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := db.FindRepo("nonexistent")
		if err == nil {
			t.Error("Expected error for nonexistent repo")
		}
	})
}

func TestGetRepoStats(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/stats-test")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("empty repo", func(t *testing.T) {
		stats, err := db.GetRepoStats(repo.ID)
		if err != nil {
			t.Fatalf("GetRepoStats failed: %v", err)
		}
		if stats.TotalJobs != 0 {
			t.Errorf("Expected 0 total jobs, got %d", stats.TotalJobs)
		}
		if stats.Repo.ID != repo.ID {
			t.Errorf("Expected repo ID %d, got %d", repo.ID, stats.Repo.ID)
		}
	})

	// Add some jobs
	commit1, _ := db.GetOrCreateCommit(repo.ID, "stats-sha1", "A", "S", time.Now())
	job1, _ := db.EnqueueJob(repo.ID, commit1.ID, "stats-sha1", "codex", "")

	commit2, _ := db.GetOrCreateCommit(repo.ID, "stats-sha2", "A", "S", time.Now())
	job2, _ := db.EnqueueJob(repo.ID, commit2.ID, "stats-sha2", "codex", "")

	commit3, _ := db.GetOrCreateCommit(repo.ID, "stats-sha3", "A", "S", time.Now())
	job3, _ := db.EnqueueJob(repo.ID, commit3.ID, "stats-sha3", "codex", "")

	// Complete job1 with PASS verdict
	db.ClaimJob("worker-1")
	db.CompleteJob(job1.ID, "codex", "prompt", "**Verdict: PASS**\nLooks good!")

	// Complete job2 with FAIL verdict
	db.ClaimJob("worker-1")
	db.CompleteJob(job2.ID, "codex", "prompt", "**Verdict: FAIL**\nIssues found.")

	// Fail job3
	db.ClaimJob("worker-1")
	db.FailJob(job3.ID, "agent error")

	t.Run("stats with jobs", func(t *testing.T) {
		stats, err := db.GetRepoStats(repo.ID)
		if err != nil {
			t.Fatalf("GetRepoStats failed: %v", err)
		}
		if stats.TotalJobs != 3 {
			t.Errorf("Expected 3 total jobs, got %d", stats.TotalJobs)
		}
		if stats.CompletedJobs != 2 {
			t.Errorf("Expected 2 completed jobs, got %d", stats.CompletedJobs)
		}
		if stats.FailedJobs != 1 {
			t.Errorf("Expected 1 failed job, got %d", stats.FailedJobs)
		}
		if stats.PassedReviews != 1 {
			t.Errorf("Expected 1 passed review, got %d", stats.PassedReviews)
		}
		if stats.FailedReviews != 1 {
			t.Errorf("Expected 1 failed review, got %d", stats.FailedReviews)
		}
	})

	t.Run("nonexistent repo", func(t *testing.T) {
		_, err := db.GetRepoStats(99999)
		if err == nil {
			t.Error("Expected error for nonexistent repo ID")
		}
	})

	t.Run("prompt jobs excluded from verdict counts", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/stats-prompt-test")

		// Create a regular job with PASS verdict
		commit, _ := db.GetOrCreateCommit(repo.ID, "stats-prompt-sha1", "A", "S", time.Now())
		job1, _ := db.EnqueueJob(repo.ID, commit.ID, "stats-prompt-sha1", "codex", "")
		db.ClaimJob("worker-1")
		db.CompleteJob(job1.ID, "codex", "prompt", "**Verdict: PASS**\nLooks good!")

		// Create a prompt job with output that contains verdict-like text
		promptJob, _ := db.EnqueuePromptJob(repo.ID, "codex", "thorough", "Test prompt", false)
		db.ClaimJob("worker-1")
		// This has FAIL verdict text but should NOT count toward failed reviews
		db.CompleteJob(promptJob.ID, "codex", "prompt", "**Verdict: FAIL**\nSome issues found")

		// Get stats - prompt job should be excluded from verdict counts
		stats, err := db.GetRepoStats(repo.ID)
		if err != nil {
			t.Fatalf("GetRepoStats failed: %v", err)
		}

		// Total jobs should include both
		if stats.TotalJobs != 2 {
			t.Errorf("Expected 2 total jobs, got %d", stats.TotalJobs)
		}
		if stats.CompletedJobs != 2 {
			t.Errorf("Expected 2 completed jobs, got %d", stats.CompletedJobs)
		}

		// Verdict counts should only reflect the regular job
		if stats.PassedReviews != 1 {
			t.Errorf("Expected 1 passed review (prompt job excluded), got %d", stats.PassedReviews)
		}
		if stats.FailedReviews != 0 {
			t.Errorf("Expected 0 failed reviews (prompt job excluded), got %d", stats.FailedReviews)
		}
	})
}

func TestDeleteRepo(t *testing.T) {
	t.Run("delete empty repo", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/delete-empty")

		err := db.DeleteRepo(repo.ID, false)
		if err != nil {
			t.Fatalf("DeleteRepo failed: %v", err)
		}

		// Verify deleted
		_, err = db.GetRepoByID(repo.ID)
		if err == nil {
			t.Error("Repo should be deleted")
		}
	})

	t.Run("delete repo with jobs without cascade returns error", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/delete-with-jobs")
		commit, _ := db.GetOrCreateCommit(repo.ID, "delete-sha", "A", "S", time.Now())
		db.EnqueueJob(repo.ID, commit.ID, "delete-sha", "codex", "")

		// Without cascade, delete should return ErrRepoHasJobs
		err := db.DeleteRepo(repo.ID, false)
		if err == nil {
			t.Error("Expected error when deleting repo with jobs without cascade")
		}
		if !errors.Is(err, ErrRepoHasJobs) {
			t.Errorf("Expected ErrRepoHasJobs, got: %v", err)
		}

		// Verify repo still exists
		_, err = db.GetRepoByID(repo.ID)
		if err != nil {
			t.Error("Repo should still exist after failed delete")
		}
	})

	t.Run("delete repo with cascade", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/delete-cascade")
		commit, _ := db.GetOrCreateCommit(repo.ID, "cascade-sha", "A", "S", time.Now())
		job, _ := db.EnqueueJob(repo.ID, commit.ID, "cascade-sha", "codex", "")
		db.ClaimJob("worker-1")
		db.CompleteJob(job.ID, "codex", "prompt", "output")

		// Add a comment
		db.AddCommentToJob(job.ID, "user", "comment")

		err := db.DeleteRepo(repo.ID, true)
		if err != nil {
			t.Fatalf("DeleteRepo with cascade failed: %v", err)
		}

		// Verify repo deleted
		_, err = db.GetRepoByID(repo.ID)
		if err == nil {
			t.Error("Repo should be deleted")
		}

		// Verify jobs deleted
		jobs, _ := db.ListJobs("", "", 100, 0)
		for _, j := range jobs {
			if j.RepoID == repo.ID {
				t.Error("Jobs should be deleted")
			}
		}
	})

	t.Run("delete nonexistent repo", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		err := db.DeleteRepo(99999, false)
		if err == nil {
			t.Error("Expected error for nonexistent repo")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Expected sql.ErrNoRows, got: %v", err)
		}
	})
}

func TestMergeRepos(t *testing.T) {
	t.Run("merge repos moves jobs", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		source, _ := db.GetOrCreateRepo("/tmp/merge-source")
		target, _ := db.GetOrCreateRepo("/tmp/merge-target")

		// Create jobs in source
		commit1, _ := db.GetOrCreateCommit(source.ID, "merge-sha1", "A", "S", time.Now())
		db.EnqueueJob(source.ID, commit1.ID, "merge-sha1", "codex", "")
		commit2, _ := db.GetOrCreateCommit(source.ID, "merge-sha2", "A", "S", time.Now())
		db.EnqueueJob(source.ID, commit2.ID, "merge-sha2", "codex", "")

		// Create job in target
		commit3, _ := db.GetOrCreateCommit(target.ID, "merge-sha3", "A", "S", time.Now())
		db.EnqueueJob(target.ID, commit3.ID, "merge-sha3", "codex", "")

		moved, err := db.MergeRepos(source.ID, target.ID)
		if err != nil {
			t.Fatalf("MergeRepos failed: %v", err)
		}
		if moved != 2 {
			t.Errorf("Expected 2 jobs moved, got %d", moved)
		}

		// Verify source is deleted
		_, err = db.GetRepoByID(source.ID)
		if err == nil {
			t.Error("Source repo should be deleted")
		}

		// Verify all jobs now belong to target
		jobs, _ := db.ListJobs("", target.RootPath, 100, 0)
		if len(jobs) != 3 {
			t.Errorf("Expected 3 jobs in target, got %d", len(jobs))
		}
	})

	t.Run("merge same repo returns 0", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/merge-same")

		moved, err := db.MergeRepos(repo.ID, repo.ID)
		if err != nil {
			t.Fatalf("MergeRepos failed: %v", err)
		}
		if moved != 0 {
			t.Errorf("Expected 0 jobs moved when merging to self, got %d", moved)
		}

		// Verify repo still exists
		_, err = db.GetRepoByID(repo.ID)
		if err != nil {
			t.Error("Repo should still exist after self-merge")
		}
	})

	t.Run("merge empty source", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		source, _ := db.GetOrCreateRepo("/tmp/merge-empty-source")
		target, _ := db.GetOrCreateRepo("/tmp/merge-empty-target")

		moved, err := db.MergeRepos(source.ID, target.ID)
		if err != nil {
			t.Fatalf("MergeRepos failed: %v", err)
		}
		if moved != 0 {
			t.Errorf("Expected 0 jobs moved, got %d", moved)
		}

		// Source should be deleted even if empty
		_, err = db.GetRepoByID(source.ID)
		if err == nil {
			t.Error("Source repo should be deleted even when empty")
		}
	})

	t.Run("merge moves commits to target", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		source, _ := db.GetOrCreateRepo("/tmp/merge-commits-source")
		target, _ := db.GetOrCreateRepo("/tmp/merge-commits-target")

		// Create commits in source
		commit1, _ := db.GetOrCreateCommit(source.ID, "commit-sha-1", "Author", "Subject 1", time.Now())
		commit2, _ := db.GetOrCreateCommit(source.ID, "commit-sha-2", "Author", "Subject 2", time.Now())
		db.EnqueueJob(source.ID, commit1.ID, "commit-sha-1", "codex", "")
		db.EnqueueJob(source.ID, commit2.ID, "commit-sha-2", "codex", "")

		// Verify commits belong to source before merge
		var sourceCommitCount int
		db.QueryRow(`SELECT COUNT(*) FROM commits WHERE repo_id = ?`, source.ID).Scan(&sourceCommitCount)
		if sourceCommitCount != 2 {
			t.Fatalf("Expected 2 commits in source, got %d", sourceCommitCount)
		}

		_, err := db.MergeRepos(source.ID, target.ID)
		if err != nil {
			t.Fatalf("MergeRepos failed: %v", err)
		}

		// Verify commits now belong to target
		var targetCommitCount int
		db.QueryRow(`SELECT COUNT(*) FROM commits WHERE repo_id = ?`, target.ID).Scan(&targetCommitCount)
		if targetCommitCount != 2 {
			t.Errorf("Expected 2 commits in target after merge, got %d", targetCommitCount)
		}

		// Verify no commits remain with source ID (source is deleted)
		var orphanedCount int
		db.QueryRow(`SELECT COUNT(*) FROM commits WHERE repo_id = ?`, source.ID).Scan(&orphanedCount)
		if orphanedCount != 0 {
			t.Errorf("Expected 0 orphaned commits, got %d", orphanedCount)
		}
	})
}

func TestDeleteRepoCascadeDeletesCommits(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/delete-commits-test")
	commit1, _ := db.GetOrCreateCommit(repo.ID, "del-commit-1", "A", "S1", time.Now())
	commit2, _ := db.GetOrCreateCommit(repo.ID, "del-commit-2", "A", "S2", time.Now())
	db.EnqueueJob(repo.ID, commit1.ID, "del-commit-1", "codex", "")
	db.EnqueueJob(repo.ID, commit2.ID, "del-commit-2", "codex", "")

	// Verify commits exist before delete
	var beforeCount int
	db.QueryRow(`SELECT COUNT(*) FROM commits WHERE repo_id = ?`, repo.ID).Scan(&beforeCount)
	if beforeCount != 2 {
		t.Fatalf("Expected 2 commits before delete, got %d", beforeCount)
	}

	err := db.DeleteRepo(repo.ID, true)
	if err != nil {
		t.Fatalf("DeleteRepo with cascade failed: %v", err)
	}

	// Verify commits are deleted
	var afterCount int
	db.QueryRow(`SELECT COUNT(*) FROM commits WHERE repo_id = ?`, repo.ID).Scan(&afterCount)
	if afterCount != 0 {
		t.Errorf("Expected 0 commits after cascade delete, got %d", afterCount)
	}
}

func TestDeleteRepoCascadeDeletesLegacyCommitResponses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _ := db.GetOrCreateRepo("/tmp/delete-legacy-resp-test")
	commit, _ := db.GetOrCreateCommit(repo.ID, "legacy-resp-commit", "A", "S", time.Now())

	// Add legacy commit-based comment (not job-based)
	_, err := db.AddComment(commit.ID, "reviewer", "Legacy comment on commit")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	// Verify comment exists
	var beforeCount int
	db.QueryRow(`SELECT COUNT(*) FROM responses WHERE commit_id = ?`, commit.ID).Scan(&beforeCount)
	if beforeCount != 1 {
		t.Fatalf("Expected 1 legacy response before delete, got %d", beforeCount)
	}

	err = db.DeleteRepo(repo.ID, true)
	if err != nil {
		t.Fatalf("DeleteRepo with cascade failed: %v", err)
	}

	// Verify legacy responses are deleted (by checking all responses - commit is gone)
	var afterCount int
	db.QueryRow(`SELECT COUNT(*) FROM responses WHERE commit_id = ?`, commit.ID).Scan(&afterCount)
	if afterCount != 0 {
		t.Errorf("Expected 0 legacy responses after cascade delete, got %d", afterCount)
	}
}

func TestVerdictSuppressionForPromptJobs(t *testing.T) {
	t.Run("prompt jobs do not get verdict computed", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/verdict-prompt-test")

		// Create a prompt job and complete it with output containing verdict-like text
		promptJob, _ := db.EnqueuePromptJob(repo.ID, "codex", "thorough", "Test prompt", false)
		db.ClaimJob("worker-1")
		// Output that would normally be parsed as FAIL
		db.CompleteJob(promptJob.ID, "codex", "prompt", "Found issues:\n1. Problem A")

		// Fetch via ListJobs and check verdict is nil
		jobs, _ := db.ListJobs("", repo.RootPath, 100, 0)
		var found *ReviewJob
		for i := range jobs {
			if jobs[i].ID == promptJob.ID {
				found = &jobs[i]
				break
			}
		}

		if found == nil {
			t.Fatal("Prompt job not found in ListJobs")
		}

		if found.Verdict != nil {
			t.Errorf("Prompt job should have nil verdict, got %v", *found.Verdict)
		}
	})

	t.Run("regular jobs still get verdict computed", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/verdict-regular-test")
		commit, _ := db.GetOrCreateCommit(repo.ID, "verdict-sha", "Author", "Subject", time.Now())

		// Create a regular job and complete it
		job, _ := db.EnqueueJob(repo.ID, commit.ID, "verdict-sha", "codex", "")
		db.ClaimJob("worker-1")
		// Output that should be parsed as PASS
		db.CompleteJob(job.ID, "codex", "prompt", "No issues found in this commit.")

		// Fetch via ListJobs and check verdict is set
		jobs, _ := db.ListJobs("", repo.RootPath, 100, 0)
		var found *ReviewJob
		for i := range jobs {
			if jobs[i].ID == job.ID {
				found = &jobs[i]
				break
			}
		}

		if found == nil {
			t.Fatal("Regular job not found in ListJobs")
		}

		if found.Verdict == nil {
			t.Error("Regular job should have verdict computed")
		} else if *found.Verdict != "P" {
			t.Errorf("Expected verdict 'P', got '%s'", *found.Verdict)
		}
	})

	t.Run("branch named prompt with commit_id gets verdict", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, _ := db.GetOrCreateRepo("/tmp/verdict-branch-prompt")
		// Create a commit for a branch literally named "prompt"
		commit, _ := db.GetOrCreateCommit(repo.ID, "branch-prompt-sha", "Author", "Subject", time.Now())

		// Enqueue with git_ref = "prompt" but WITH a commit_id (simulating review of branch "prompt")
		result, _ := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, reasoning, status) VALUES (?, ?, 'prompt', 'codex', 'thorough', 'queued')`,
			repo.ID, commit.ID)
		jobID, _ := result.LastInsertId()

		db.ClaimJob("worker-1")
		// Output that should be parsed as FAIL
		db.CompleteJob(jobID, "codex", "prompt", "Found issues:\n1. Bug found")

		// Fetch via ListJobs and check verdict IS computed (because commit_id is not NULL)
		jobs, _ := db.ListJobs("", repo.RootPath, 100, 0)
		var found *ReviewJob
		for i := range jobs {
			if jobs[i].ID == jobID {
				found = &jobs[i]
				break
			}
		}

		if found == nil {
			t.Fatal("Branch 'prompt' job not found in ListJobs")
		}

		// This job has commit_id set, so it's NOT a prompt job - verdict should be computed
		if found.Verdict == nil {
			t.Error("Job for branch named 'prompt' should have verdict computed")
		} else if *found.Verdict != "F" {
			t.Errorf("Expected verdict 'F', got '%s'", *found.Verdict)
		}
	})
}
