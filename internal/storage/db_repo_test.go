package storage

import (
	"testing"
	"time"
)

func TestRepoOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repo
	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	if repo.ID == 0 {
		t.Error("Repo ID should not be 0")
	}
	if repo.Name != "test-repo" {
		t.Errorf("Expected name 'test-repo', got '%s'", repo.Name)
	}

	// Get same repo again (should return existing)
	repo2, err := db.GetOrCreateRepo("/tmp/test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateRepo (second call) failed: %v", err)
	}
	if repo2.ID != repo.ID {
		t.Error("Should return same repo on second call")
	}
}

func TestCommitOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Create commit
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123def456", "Test Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	if commit.ID == 0 {
		t.Error("Commit ID should not be 0")
	}
	if commit.SHA != "abc123def456" {
		t.Errorf("Expected SHA 'abc123def456', got '%s'", commit.SHA)
	}

	// Get by SHA
	found, err := db.GetCommitBySHA("abc123def456")
	if err != nil {
		t.Fatalf("GetCommitBySHA failed: %v", err)
	}
	if found.ID != commit.ID {
		t.Error("GetCommitBySHA returned wrong commit")
	}
}

func TestBranchPersistence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/branch-test-repo")
	commit := createCommit(t, db, repo.ID, "branch123")

	t.Run("EnqueueJob stores branch", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch123", Branch: "feature/test-branch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		if job.Branch != "feature/test-branch" {
			t.Errorf("Expected branch 'feature/test-branch', got '%s'", job.Branch)
		}
	})

	t.Run("GetJobByID returns branch", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch456", Branch: "main", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		fetched, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetched.Branch != "main" {
			t.Errorf("GetJobByID: expected branch 'main', got '%s'", fetched.Branch)
		}
	})

	t.Run("ListJobs returns branch", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch789", Branch: "develop", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		jobs, err := db.ListJobs("", "", 100, 0)
		if err != nil {
			t.Fatalf("ListJobs failed: %v", err)
		}
		var found bool
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				if j.Branch != "develop" {
					t.Errorf("ListJobs: expected branch 'develop', got '%s'", j.Branch)
				}
				break
			}
		}
		if !found {
			t.Error("ListJobs did not return the job")
		}
	})

	t.Run("ClaimJob returns branch", func(t *testing.T) {
		// Drain existing jobs
		for {
			j, _ := db.ClaimJob("drain")
			if j == nil {
				break
			}
			db.CompleteJob(j.ID, "codex", "p", "o")
		}

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branchclaim", Branch: "release/v1", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		claimed, err := db.ClaimJob("test-worker")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		if claimed == nil || claimed.ID != job.ID {
			t.Fatal("ClaimJob did not return the expected job")
		}
		if claimed.Branch != "release/v1" {
			t.Errorf("ClaimJob: expected branch 'release/v1', got '%s'", claimed.Branch)
		}
	})

	t.Run("empty branch is allowed", func(t *testing.T) {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "nobranch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob with empty branch failed: %v", err)
		}
		if job.Branch != "" {
			t.Errorf("Expected empty branch, got '%s'", job.Branch)
		}
	})

	t.Run("UpdateJobBranch backfills empty branch", func(t *testing.T) {
		// Create job with empty branch
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "updatebranch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		if job.Branch != "" {
			t.Fatalf("Expected empty branch initially, got '%s'", job.Branch)
		}

		// Update the branch
		rowsAffected, err := db.UpdateJobBranch(job.ID, "feature/backfilled")
		if err != nil {
			t.Fatalf("UpdateJobBranch failed: %v", err)
		}
		if rowsAffected != 1 {
			t.Errorf("Expected 1 row affected, got %d", rowsAffected)
		}

		// Verify the branch was updated
		fetched, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetched.Branch != "feature/backfilled" {
			t.Errorf("Expected branch 'feature/backfilled', got '%s'", fetched.Branch)
		}
	})

	t.Run("UpdateJobBranch does not overwrite existing branch", func(t *testing.T) {
		// Create job with existing branch
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "nooverwrite", Branch: "original-branch", Agent: "codex"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}

		// Try to update - should not change existing branch
		rowsAffected, err := db.UpdateJobBranch(job.ID, "new-branch")
		if err != nil {
			t.Fatalf("UpdateJobBranch failed: %v", err)
		}
		if rowsAffected != 0 {
			t.Errorf("Expected 0 rows affected (branch already set), got %d", rowsAffected)
		}

		// Verify the branch was NOT changed
		fetched, err := db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if fetched.Branch != "original-branch" {
			t.Errorf("Expected branch 'original-branch' (unchanged), got '%s'", fetched.Branch)
		}
	})
}

func TestRepoIdentity(t *testing.T) {
	t.Run("sets identity on create", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, err := db.GetOrCreateRepo("/tmp/identity-test", "git@github.com:foo/bar.git")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}

		if repo.Identity != "git@github.com:foo/bar.git" {
			t.Errorf("Expected identity 'git@github.com:foo/bar.git', got %q", repo.Identity)
		}

		// Verify it persists
		repo2, err := db.GetOrCreateRepo("/tmp/identity-test")
		if err != nil {
			t.Fatalf("GetOrCreateRepo (second call) failed: %v", err)
		}
		if repo2.Identity != "git@github.com:foo/bar.git" {
			t.Errorf("Expected identity to persist, got %q", repo2.Identity)
		}
	})

	t.Run("backfills identity when not set", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create repo without identity
		repo1, err := db.GetOrCreateRepo("/tmp/backfill-test")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}
		if repo1.Identity != "" {
			t.Errorf("Expected no identity initially, got %q", repo1.Identity)
		}

		// Call again with identity - should backfill
		repo2, err := db.GetOrCreateRepo("/tmp/backfill-test", "git@github.com:test/backfill.git")
		if err != nil {
			t.Fatalf("GetOrCreateRepo with identity failed: %v", err)
		}
		if repo2.Identity != "git@github.com:test/backfill.git" {
			t.Errorf("Expected identity to be backfilled, got %q", repo2.Identity)
		}
	})

	t.Run("does not overwrite existing identity", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create repo with identity
		_, err := db.GetOrCreateRepo("/tmp/no-overwrite-test", "original-identity")
		if err != nil {
			t.Fatalf("GetOrCreateRepo failed: %v", err)
		}

		// Call again with different identity - should NOT overwrite
		repo2, err := db.GetOrCreateRepo("/tmp/no-overwrite-test", "new-identity")
		if err != nil {
			t.Fatalf("GetOrCreateRepo with new identity failed: %v", err)
		}
		if repo2.Identity != "original-identity" {
			t.Errorf("Expected identity to remain 'original-identity', got %q", repo2.Identity)
		}
	})

	t.Run("multiple clones with same identity allowed", func(t *testing.T) {
		// This tests the fix for https://github.com/roborev-dev/roborev/issues/131
		// Multiple clones of the same repo (e.g., ~/project-1 and ~/project-2 both
		// cloned from the same remote) should be allowed and share the same identity.
		db := openTestDB(t)
		defer db.Close()

		sharedIdentity := "git@github.com:org/shared-repo.git"

		// Create first clone
		repo1, err := db.GetOrCreateRepo("/tmp/clone-1", sharedIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepo for clone-1 failed: %v", err)
		}
		if repo1.Identity != sharedIdentity {
			t.Errorf("Expected identity %q for clone-1, got %q", sharedIdentity, repo1.Identity)
		}

		// Create second clone with same identity - should succeed (was failing before fix)
		repo2, err := db.GetOrCreateRepo("/tmp/clone-2", sharedIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepo for clone-2 failed: %v (multiple clones with same identity should be allowed)", err)
		}
		if repo2.Identity != sharedIdentity {
			t.Errorf("Expected identity %q for clone-2, got %q", sharedIdentity, repo2.Identity)
		}

		// Verify they are different repos
		if repo1.ID == repo2.ID {
			t.Errorf("Expected different repo IDs, but both are %d", repo1.ID)
		}
		if repo1.RootPath == repo2.RootPath {
			t.Errorf("Expected different root paths")
		}

		// Verify both repos exist and can be retrieved
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos failed: %v", err)
		}

		foundClone1, foundClone2 := false, false
		for _, r := range repos {
			if r.ID == repo1.ID {
				foundClone1 = true
			}
			if r.ID == repo2.ID {
				foundClone2 = true
			}
		}
		if !foundClone1 || !foundClone2 {
			t.Errorf("Expected both clones to exist in ListRepos, found clone1=%v clone2=%v", foundClone1, foundClone2)
		}
	})
}

func TestDuplicateSHAHandling(t *testing.T) {
	t.Run("same SHA in different repos creates separate commits", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create two repos
		repo1, _ := db.GetOrCreateRepo("/tmp/sha-test-1")
		repo2, _ := db.GetOrCreateRepo("/tmp/sha-test-2")

		// Create commits with same SHA in different repos
		commit1, err := db.GetOrCreateCommit(repo1.ID, "abc123", "Author1", "Subject1", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit for repo1 failed: %v", err)
		}

		commit2, err := db.GetOrCreateCommit(repo2.ID, "abc123", "Author2", "Subject2", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit for repo2 failed: %v", err)
		}

		// Should be different commits
		if commit1.ID == commit2.ID {
			t.Error("Same SHA in different repos should create different commits")
		}
		if commit1.RepoID != repo1.ID {
			t.Error("commit1 should belong to repo1")
		}
		if commit2.RepoID != repo2.ID {
			t.Error("commit2 should belong to repo2")
		}
	})

	t.Run("GetCommitBySHA returns error when ambiguous", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create two repos with same SHA
		repo1, _ := db.GetOrCreateRepo("/tmp/ambiguous-1")
		repo2, _ := db.GetOrCreateRepo("/tmp/ambiguous-2")

		db.GetOrCreateCommit(repo1.ID, "ambiguous-sha", "Author", "Subject", time.Now())
		db.GetOrCreateCommit(repo2.ID, "ambiguous-sha", "Author", "Subject", time.Now())

		// GetCommitBySHA should fail when ambiguous
		_, err := db.GetCommitBySHA("ambiguous-sha")
		if err == nil {
			t.Error("Expected error when SHA is ambiguous across repos")
		}
	})

	t.Run("GetCommitByRepoAndSHA returns correct commit", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create two repos with same SHA
		repo1, _ := db.GetOrCreateRepo("/tmp/repo-and-sha-1")
		repo2, _ := db.GetOrCreateRepo("/tmp/repo-and-sha-2")

		commit1, _ := db.GetOrCreateCommit(repo1.ID, "same-sha", "Author1", "Subject1", time.Now())
		commit2, _ := db.GetOrCreateCommit(repo2.ID, "same-sha", "Author2", "Subject2", time.Now())

		// GetCommitByRepoAndSHA should return correct commit for each repo
		found1, err := db.GetCommitByRepoAndSHA(repo1.ID, "same-sha")
		if err != nil {
			t.Fatalf("GetCommitByRepoAndSHA for repo1 failed: %v", err)
		}
		if found1.ID != commit1.ID {
			t.Error("GetCommitByRepoAndSHA returned wrong commit for repo1")
		}

		found2, err := db.GetCommitByRepoAndSHA(repo2.ID, "same-sha")
		if err != nil {
			t.Fatalf("GetCommitByRepoAndSHA for repo2 failed: %v", err)
		}
		if found2.ID != commit2.ID {
			t.Error("GetCommitByRepoAndSHA returned wrong commit for repo2")
		}
	})
}
