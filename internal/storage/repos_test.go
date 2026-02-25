package storage

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupDBAndRepo is a helper that opens a test database and creates a repository.
func setupDBAndRepo(t *testing.T, name string) (*DB, *Repo) {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	repo := createRepo(t, db, filepath.Join(t.TempDir(), name))
	return db, repo
}

// completeTestJob is a helper that claims and completes a job.
func completeTestJob(t *testing.T, db *DB, jobID int64, output string) {
	t.Helper()
	claimJob(t, db, "worker-1")
	if err := db.CompleteJob(jobID, "codex", "prompt", output); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}
}

func TestEnqueuePromptJob(t *testing.T) {
	tests := []struct {
		name        string
		opts        EnqueueOpts
		wantJob     func(*testing.T, *ReviewJob)
		checkClaim  bool
		wantClaimed func(*testing.T, *ReviewJob)
		checkSQL    func(*testing.T, *DB, int64)
	}{
		{
			name: "creates job with custom prompt",
			opts: EnqueueOpts{
				Agent:     "claude-code",
				Reasoning: "thorough",
				Prompt:    "Explain the architecture of this codebase",
			},
			wantJob: func(t *testing.T, j *ReviewJob) {
				if j.GitRef != "prompt" {
					t.Errorf("got git_ref %q, want 'prompt'", j.GitRef)
				}
				if j.Agent != "claude-code" {
					t.Errorf("got agent %q, want 'claude-code'", j.Agent)
				}
				if j.Reasoning != "thorough" {
					t.Errorf("got reasoning %q, want 'thorough'", j.Reasoning)
				}
				if j.Prompt != "Explain the architecture of this codebase" {
					t.Errorf("got prompt %q, want 'Explain the architecture of this codebase'", j.Prompt)
				}
				if j.Status != JobStatusQueued {
					t.Errorf("got status %q, want 'queued'", j.Status)
				}
			},
			checkSQL: func(t *testing.T, db *DB, jobID int64) {
				var gitRef string
				err := db.QueryRow("SELECT git_ref FROM review_jobs WHERE id = ?", jobID).Scan(&gitRef)
				if err != nil {
					t.Fatalf("Failed to query git_ref: %v", err)
				}
				if gitRef != "prompt" {
					t.Errorf("DB git_ref = %q, want 'prompt'", gitRef)
				}
			},
		},
		{
			name: "defaults reasoning to thorough",
			opts: EnqueueOpts{
				Agent:  "codex",
				Prompt: "test prompt",
			},
			wantJob: func(t *testing.T, j *ReviewJob) {
				if j.Reasoning != "thorough" {
					t.Errorf("got default reasoning %q, want 'thorough'", j.Reasoning)
				}
			},
		},
		{
			name: "claimed job has prompt loaded",
			opts: EnqueueOpts{
				Agent:     "claude-code",
				Reasoning: "standard",
				Prompt:    "Find security issues in the codebase",
			},
			checkClaim: true,
			wantClaimed: func(t *testing.T, j *ReviewJob) {
				if j.GitRef != "prompt" {
					t.Errorf("got git_ref %q, want 'prompt'", j.GitRef)
				}
				if j.Prompt != "Find security issues in the codebase" {
					t.Errorf("got prompt %q, want 'Find security issues in the codebase'", j.Prompt)
				}
			},
		},
		{
			name: "agentic flag persists and is claimed correctly",
			opts: EnqueueOpts{
				Agent:   "claude-code",
				Prompt:  "Test agentic prompt",
				Agentic: true,
			},
			wantJob: func(t *testing.T, j *ReviewJob) {
				if !j.Agentic {
					t.Error("Expected Agentic to be true on returned job")
				}
			},
			checkClaim: true,
			wantClaimed: func(t *testing.T, j *ReviewJob) {
				if !j.Agentic {
					t.Error("Expected Agentic to be true on claimed job")
				}
			},
			checkSQL: func(t *testing.T, db *DB, jobID int64) {
				var agentic bool
				err := db.QueryRow("SELECT agentic FROM review_jobs WHERE id = ?", jobID).Scan(&agentic)
				if err != nil {
					t.Fatalf("Failed to query agentic: %v", err)
				}
				if !agentic {
					t.Error("DB agentic = false, want true")
				}
			},
		},
		{
			name: "agentic flag defaults to false",
			opts: EnqueueOpts{
				Agent:     "codex",
				Reasoning: "standard",
				Prompt:    "Non-agentic prompt",
			},
			wantJob: func(t *testing.T, j *ReviewJob) {
				if j.Agentic {
					t.Error("Expected Agentic to be false")
				}
			},
			checkClaim: true,
			wantClaimed: func(t *testing.T, j *ReviewJob) {
				if j.Agentic {
					t.Error("Expected Agentic to be false on claimed job")
				}
			},
			checkSQL: func(t *testing.T, db *DB, jobID int64) {
				var agentic bool
				err := db.QueryRow("SELECT agentic FROM review_jobs WHERE id = ?", jobID).Scan(&agentic)
				if err != nil {
					t.Fatalf("Failed to query agentic: %v", err)
				}
				if agentic {
					t.Error("DB agentic = true, want false")
				}
			},
		},
		{
			name: "ClaimJob loads output_prefix",
			opts: EnqueueOpts{
				Agent:        "test",
				Prompt:       "compact prompt",
				OutputPrefix: "## Compact Analysis\n\n---\n\n",
			},
			checkClaim: true,
			wantClaimed: func(t *testing.T, j *ReviewJob) {
				want := "## Compact Analysis\n\n---\n\n"
				if j.OutputPrefix != want {
					t.Errorf("got OutputPrefix %q, want %q", j.OutputPrefix, want)
				}
			},
		},
		{
			name: "ClaimJob returns empty OutputPrefix when not set",
			opts: EnqueueOpts{
				Agent:  "test",
				Prompt: "plain prompt",
			},
			checkClaim: true,
			wantClaimed: func(t *testing.T, j *ReviewJob) {
				if j.OutputPrefix != "" {
					t.Errorf("got OutputPrefix %q, want empty", j.OutputPrefix)
				}
			},
		},
		{
			name: "custom label sets git_ref",
			opts: EnqueueOpts{
				Agent:  "test",
				Prompt: "Test prompt",
				Label:  "test-fixtures",
			},
			wantJob: func(t *testing.T, j *ReviewJob) {
				if j.GitRef != "test-fixtures" {
					t.Errorf("got git_ref %q, want 'test-fixtures'", j.GitRef)
				}
			},
			checkClaim: true,
			wantClaimed: func(t *testing.T, j *ReviewJob) {
				if j.GitRef != "test-fixtures" {
					t.Errorf("got git_ref %q, want 'test-fixtures'", j.GitRef)
				}
			},
			checkSQL: func(t *testing.T, db *DB, jobID int64) {
				var gitRef string
				err := db.QueryRow("SELECT git_ref FROM review_jobs WHERE id = ?", jobID).Scan(&gitRef)
				if err != nil {
					t.Fatalf("Failed to query git_ref: %v", err)
				}
				if gitRef != "test-fixtures" {
					t.Errorf("DB git_ref = %q, want 'test-fixtures'", gitRef)
				}
			},
		},
		{
			name: "empty label defaults to prompt",
			opts: EnqueueOpts{
				Agent:  "test",
				Prompt: "Test prompt",
			},
			wantJob: func(t *testing.T, j *ReviewJob) {
				if j.GitRef != "prompt" {
					t.Errorf("got git_ref %q, want 'prompt'", j.GitRef)
				}
			},
		},
		{
			name: "run label",
			opts: EnqueueOpts{
				Agent:  "test",
				Prompt: "Test prompt",
				Label:  "run",
			},
			wantJob: func(t *testing.T, j *ReviewJob) {
				if j.GitRef != "run" {
					t.Errorf("got git_ref %q, want 'run'", j.GitRef)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoName := "prompt-test-" + strings.ReplaceAll(tt.name, " ", "-")
			db, repo := setupDBAndRepo(t, repoName)

			opts := tt.opts
			opts.RepoID = repo.ID
			job := mustEnqueuePromptJob(t, db, opts)

			if tt.wantJob != nil {
				tt.wantJob(t, job)
			}

			if tt.checkSQL != nil {
				tt.checkSQL(t, db, job.ID)
			}

			if tt.checkClaim {
				claimed := claimJob(t, db, "test-worker")
				if tt.wantClaimed != nil {
					tt.wantClaimed(t, claimed)
				}
			}
		})
	}
}

func TestPromptJobOutputProcessing(t *testing.T) {
	t.Run("output_prefix is prepended to review output", func(t *testing.T) {
		db, repo := setupDBAndRepo(t, "output-prefix-test")

		outputPrefix := "## Test Analysis\n\n**Files:**\n- file1.go\n- file2.go\n\n---\n\n"
		job := mustEnqueuePromptJob(t, db, EnqueueOpts{
			RepoID:       repo.ID,
			Agent:        "test",
			Prompt:       "Test prompt",
			OutputPrefix: outputPrefix,
		})

		agentOutput := "No issues found."
		completeTestJob(t, db, job.ID, agentOutput)

		// Fetch the review and verify prefix was prepended
		review, err := db.GetReviewByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}

		expectedOutput := outputPrefix + agentOutput
		if review.Output != expectedOutput {
			t.Errorf("got output:\n%s\nwant:\n%s", review.Output, expectedOutput)
		}
	})

	t.Run("empty output_prefix leaves output unchanged", func(t *testing.T) {
		db, repo := setupDBAndRepo(t, "empty-prefix-test")

		job := mustEnqueuePromptJob(t, db, EnqueueOpts{
			RepoID:       repo.ID,
			Agent:        "test",
			Prompt:       "Test prompt",
			OutputPrefix: "", // Empty prefix
		})

		agentOutput := "Analysis complete."
		completeTestJob(t, db, job.ID, agentOutput)

		// Fetch the review and verify output is unchanged
		review, err := db.GetReviewByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}

		if review.Output != agentOutput {
			t.Errorf("got output:\n%s\nwant:\n%s", review.Output, agentOutput)
		}
	})
}

func TestRenameRepo(t *testing.T) {
	db, repo := setupDBAndRepo(t, "rename-test")
	initialPath := repo.RootPath

	t.Run("rename by path", func(t *testing.T) {
		affected, err := db.RenameRepo(initialPath, "new-name")
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
	createRepo(t, db, filepath.Join(t.TempDir(), "repo-a"))
	createRepo(t, db, filepath.Join(t.TempDir(), "repo-b"))

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
	db, repo := setupDBAndRepo(t, "getbyid-test")

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
	db, repo := setupDBAndRepo(t, "getbyname-test")

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
	db, repo := setupDBAndRepo(t, "findrepo-test")
	initialPath := repo.RootPath

	t.Run("find by path", func(t *testing.T) {
		found, err := db.FindRepo(initialPath)
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

	t.Run("created_at is populated", func(t *testing.T) {
		found, err := db.FindRepo(initialPath)
		if err != nil {
			t.Fatalf("FindRepo failed: %v", err)
		}
		if found.CreatedAt.IsZero() {
			t.Error("CreatedAt should not be zero (SQLite CURRENT_TIMESTAMP must be parsed)")
		}
		// Should be recent (within the last minute)
		if time.Since(found.CreatedAt) > time.Minute {
			t.Errorf("CreatedAt too old: %v", found.CreatedAt)
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
	t.Run("empty repo", func(t *testing.T) {
		db, repo := setupDBAndRepo(t, "stats-test")

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

	t.Run("stats with jobs", func(t *testing.T) {
		db, repo := setupDBAndRepo(t, "stats-jobs-test")

		// Add some jobs
		commit1 := createCommit(t, db, repo.ID, "stats-sha1")
		job1 := enqueueJob(t, db, repo.ID, commit1.ID, "stats-sha1")

		commit2 := createCommit(t, db, repo.ID, "stats-sha2")
		job2 := enqueueJob(t, db, repo.ID, commit2.ID, "stats-sha2")

		commit3 := createCommit(t, db, repo.ID, "stats-sha3")
		job3 := enqueueJob(t, db, repo.ID, commit3.ID, "stats-sha3")

		// Complete job1 with PASS verdict
		completeTestJob(t, db, job1.ID, "**Verdict: PASS**\nLooks good!")

		// Complete job2 with FAIL verdict
		completeTestJob(t, db, job2.ID, "**Verdict: FAIL**\nIssues found.")

		// Fail job3
		claimJob(t, db, "worker-1")
		if _, err := db.FailJob(job3.ID, "", "agent error"); err != nil {
			t.Fatalf("FailJob failed: %v", err)
		}

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
		// Both reviews should be unaddressed by default
		if stats.AddressedReviews != 0 {
			t.Errorf("Expected 0 addressed reviews, got %d", stats.AddressedReviews)
		}
		if stats.UnaddressedReviews != 2 {
			t.Errorf("Expected 2 unaddressed reviews, got %d", stats.UnaddressedReviews)
		}
	})

	t.Run("addressed reviews counted", func(t *testing.T) {
		db, repo := setupDBAndRepo(t, "stats-addressed-test")
		commit1 := createCommit(t, db, repo.ID, "stats-sha1")
		job1 := enqueueJob(t, db, repo.ID, commit1.ID, "stats-sha1")

		// Complete job1
		completeTestJob(t, db, job1.ID, "**Verdict: PASS**\nLooks good!")

		// Mark job1's review as addressed
		review, err := db.GetReviewByJobID(job1.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}
		if err := db.MarkReviewAddressed(review.ID, true); err != nil {
			t.Fatalf("MarkReviewAddressed failed: %v", err)
		}

		// Create a second job that will be unaddressed
		commit2 := createCommit(t, db, repo.ID, "stats-sha2")
		job2 := enqueueJob(t, db, repo.ID, commit2.ID, "stats-sha2")

		// Complete job2
		completeTestJob(t, db, job2.ID, "**Verdict: PASS**\nAlso looks good!")

		stats, err := db.GetRepoStats(repo.ID)
		if err != nil {
			t.Fatalf("GetRepoStats failed: %v", err)
		}
		if stats.AddressedReviews != 1 {
			t.Errorf("Expected 1 addressed review, got %d", stats.AddressedReviews)
		}
		if stats.UnaddressedReviews != 1 {
			t.Errorf("Expected 1 unaddressed review, got %d", stats.UnaddressedReviews)
		}
	})

	t.Run("nonexistent repo", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		_, err := db.GetRepoStats(99999)
		if err == nil {
			t.Error("Expected error for nonexistent repo ID")
		}
	})

	t.Run("prompt jobs excluded from verdict counts", func(t *testing.T) {
		db, repo := setupDBAndRepo(t, "stats-prompt-test")

		// Create a regular job with PASS verdict
		commit := createCommit(t, db, repo.ID, "stats-prompt-sha1")
		job1 := enqueueJob(t, db, repo.ID, commit.ID, "stats-prompt-sha1")
		completeTestJob(t, db, job1.ID, "**Verdict: PASS**\nLooks good!")

		// Create a prompt job with output that contains verdict-like text
		promptJob := mustEnqueuePromptJob(t, db, EnqueueOpts{RepoID: repo.ID, Agent: "codex", Prompt: "Test prompt"})
		// This has FAIL verdict text but should NOT count toward failed reviews
		completeTestJob(t, db, promptJob.ID, "**Verdict: FAIL**\nSome issues found")

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
		db, repo := setupDBAndRepo(t, "delete-empty")

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
		db, repo := setupDBAndRepo(t, "delete-with-jobs")
		commit := createCommit(t, db, repo.ID, "delete-sha")
		enqueueJob(t, db, repo.ID, commit.ID, "delete-sha")

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
		db, repo := setupDBAndRepo(t, "delete-cascade")
		commit := createCommit(t, db, repo.ID, "cascade-sha")
		job := enqueueJob(t, db, repo.ID, commit.ID, "cascade-sha")
		completeTestJob(t, db, job.ID, "output")

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

		source := createRepo(t, db, filepath.Join(t.TempDir(), "merge-source"))
		target := createRepo(t, db, filepath.Join(t.TempDir(), "merge-target"))

		// Create jobs in source
		commit1 := createCommit(t, db, source.ID, "merge-sha1")
		enqueueJob(t, db, source.ID, commit1.ID, "merge-sha1")
		commit2 := createCommit(t, db, source.ID, "merge-sha2")
		enqueueJob(t, db, source.ID, commit2.ID, "merge-sha2")

		// Create job in target
		commit3 := createCommit(t, db, target.ID, "merge-sha3")
		enqueueJob(t, db, target.ID, commit3.ID, "merge-sha3")

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
		db, repo := setupDBAndRepo(t, "merge-same")

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

		source := createRepo(t, db, filepath.Join(t.TempDir(), "merge-empty-source"))
		target := createRepo(t, db, filepath.Join(t.TempDir(), "merge-empty-target"))

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

		source := createRepo(t, db, filepath.Join(t.TempDir(), "merge-commits-source"))
		target := createRepo(t, db, filepath.Join(t.TempDir(), "merge-commits-target"))

		// Create commits in source
		commit1 := createCommit(t, db, source.ID, "commit-sha-1")
		commit2 := createCommit(t, db, source.ID, "commit-sha-2")
		enqueueJob(t, db, source.ID, commit1.ID, "commit-sha-1")
		enqueueJob(t, db, source.ID, commit2.ID, "commit-sha-2")

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
	db, repo := setupDBAndRepo(t, "delete-commits-test")
	commit1 := createCommit(t, db, repo.ID, "del-commit-1")
	commit2 := createCommit(t, db, repo.ID, "del-commit-2")
	enqueueJob(t, db, repo.ID, commit1.ID, "del-commit-1")
	enqueueJob(t, db, repo.ID, commit2.ID, "del-commit-2")

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
	db, repo := setupDBAndRepo(t, "delete-legacy-resp-test")
	commit := createCommit(t, db, repo.ID, "legacy-resp-commit")

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
		db, repo := setupDBAndRepo(t, "verdict-prompt-test")

		// Create a prompt job and complete it with output containing verdict-like text
		promptJob := mustEnqueuePromptJob(t, db, EnqueueOpts{RepoID: repo.ID, Agent: "codex", Prompt: "Test prompt"})
		// Output that would normally be parsed as FAIL
		completeTestJob(t, db, promptJob.ID, "Found issues:\n1. Problem A")

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
		db, repo := setupDBAndRepo(t, "verdict-regular-test")
		commit := createCommit(t, db, repo.ID, "verdict-sha")

		// Create a regular job and complete it
		job := enqueueJob(t, db, repo.ID, commit.ID, "verdict-sha")
		claimJob(t, db, "worker-1")
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
		db, repo := setupDBAndRepo(t, "verdict-branch-prompt")
		// Create a commit for a branch literally named "prompt"
		commit := createCommit(t, db, repo.ID, "branch-prompt-sha")

		// Enqueue with git_ref = "prompt" but WITH a commit_id (simulating review of branch "prompt")
		result, _ := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, reasoning, status) VALUES (?, ?, 'prompt', 'codex', 'thorough', 'queued')`,
			repo.ID, commit.ID)
		jobID, _ := result.LastInsertId()

		claimJob(t, db, "worker-1")
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

// TestRetriedReviewJobNotRoutedAsPromptJob verifies that when a review
// job is retried, the saved prompt from the first run does not cause
// the job to be misidentified as a prompt-native job (task/compact).
// This is the storage-level regression test for the UsesStoredPrompt gate.
func TestRetriedReviewJobNotRoutedAsPromptJob(t *testing.T) {
	tests := []struct {
		name               string
		setupJob           func(t *testing.T, db *DB, repoID int64) *ReviewJob
		manuallySavePrompt bool
		expectedJobType    string
		expectStoredPrompt bool
		expectedPrompt     string
	}{
		{
			name: "review job",
			setupJob: func(t *testing.T, db *DB, repoID int64) *ReviewJob {
				commit := createCommit(t, db, repoID, "retry-sha1")
				return enqueueJob(t, db, repoID, commit.ID, "retry-sha1")
			},
			manuallySavePrompt: true,
			expectedJobType:    JobTypeReview,
			expectStoredPrompt: false,
			expectedPrompt:     "Saved prompt...",
		},
		{
			name: "task job",
			setupJob: func(t *testing.T, db *DB, repoID int64) *ReviewJob {
				return mustEnqueuePromptJob(t, db, EnqueueOpts{
					RepoID: repoID,
					Agent:  "test",
					Prompt: "Analyze the codebase architecture",
				})
			},
			manuallySavePrompt: false,
			expectedJobType:    JobTypeTask,
			expectStoredPrompt: true,
			expectedPrompt:     "Analyze the codebase architecture",
		},
		{
			name: "compact job",
			setupJob: func(t *testing.T, db *DB, repoID int64) *ReviewJob {
				return mustEnqueuePromptJob(t, db, EnqueueOpts{
					RepoID:  repoID,
					Agent:   "test",
					Prompt:  "Verify these findings are still relevant...",
					JobType: JobTypeCompact,
					Label:   "compact",
				})
			},
			manuallySavePrompt: false,
			expectedJobType:    JobTypeCompact,
			expectStoredPrompt: true,
			expectedPrompt:     "Verify these findings are still relevant...",
		},
		{
			name: "dirty job",
			setupJob: func(t *testing.T, db *DB, repoID int64) *ReviewJob {
				return mustEnqueuePromptJob(t, db, EnqueueOpts{
					RepoID:      repoID,
					Agent:       "test",
					GitRef:      "dirty",
					DiffContent: "diff --git a/file.go b/file.go\n+new line",
				})
			},
			manuallySavePrompt: true,
			expectedJobType:    JobTypeDirty,
			expectStoredPrompt: false,
			expectedPrompt:     "Saved prompt...",
		},
		{
			name: "range job",
			setupJob: func(t *testing.T, db *DB, repoID int64) *ReviewJob {
				return mustEnqueuePromptJob(t, db, EnqueueOpts{
					RepoID: repoID,
					Agent:  "test",
					GitRef: "abc123..def456",
				})
			},
			manuallySavePrompt: true,
			expectedJobType:    JobTypeRange,
			expectStoredPrompt: false,
			expectedPrompt:     "Saved prompt...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, repo := setupDBAndRepo(t, "retry-"+strings.ReplaceAll(tt.name, " ", "-"))

			job := tt.setupJob(t, db, repo.ID)
			if job.JobType != tt.expectedJobType {
				t.Fatalf("Expected job_type=%q, got %q", tt.expectedJobType, job.JobType)
			}

			claimed := claimJob(t, db, "worker-1")

			if tt.manuallySavePrompt {
				if err := db.SaveJobPrompt(claimed.ID, "Saved prompt..."); err != nil {
					t.Fatalf("SaveJobPrompt failed: %v", err)
				}
			}

			retried, err := db.RetryJob(claimed.ID, "", 3)
			if err != nil {
				t.Fatalf("RetryJob failed: %v", err)
			}
			if !retried {
				t.Fatal("RetryJob returned false")
			}

			reclaimed := claimJob(t, db, "worker-2")
			if reclaimed.UsesStoredPrompt() != tt.expectStoredPrompt {
				t.Errorf("UsesStoredPrompt() = %v, want %v", reclaimed.UsesStoredPrompt(), tt.expectStoredPrompt)
			}
			if reclaimed.Prompt != tt.expectedPrompt {
				t.Errorf("Prompt = %q, want %q", reclaimed.Prompt, tt.expectedPrompt)
			}
		})
	}
}
