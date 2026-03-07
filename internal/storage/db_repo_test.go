package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRepoOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create repo
	repo, err := db.GetOrCreateRepo("/tmp/test-repo")
	require.NoError(t, err, "GetOrCreateRepo failed")
	require.NotEqual(t, int64(0), repo.ID, "Repo ID should not be 0")
	require.Equal(t, "test-repo", repo.Name)

	// Get same repo again (should return existing)
	repo2, err := db.GetOrCreateRepo("/tmp/test-repo")
	require.NoError(t, err, "GetOrCreateRepo (second call) failed")
	require.Equal(t, repo.ID, repo2.ID, "Should return same repo on second call")
}

func TestCommitOperations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Create commit
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123def456", "Test Author", "Test commit", time.Now())
	require.NoError(t, err, "GetOrCreateCommit failed")
	require.NotEqual(t, int64(0), commit.ID, "Commit ID should not be 0")
	require.Equal(t, "abc123def456", commit.SHA)

	// Get by SHA
	found, err := db.GetCommitBySHA("abc123def456")
	require.NoError(t, err, "GetCommitBySHA failed")
	require.Equal(t, commit.ID, found.ID, "GetCommitBySHA returned wrong commit")
}

func TestBranchPersistence(t *testing.T) {
	t.Run("EnqueueJob stores branch", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		repo := createRepo(t, db, "/tmp/branch-test-repo")
		commit := createCommit(t, db, repo.ID, "branch123")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch123", Branch: "feature/test-branch", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed")
		require.Equal(t, "feature/test-branch", job.Branch)
	})

	t.Run("GetJobByID returns branch", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		repo := createRepo(t, db, "/tmp/branch-test-repo")
		commit := createCommit(t, db, repo.ID, "branch123")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch456", Branch: "main", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed")

		fetched, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID failed")
		require.Equal(t, "main", fetched.Branch)
	})

	t.Run("ListJobs returns branch", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		repo := createRepo(t, db, "/tmp/branch-test-repo")
		commit := createCommit(t, db, repo.ID, "branch123")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branch789", Branch: "develop", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed")

		jobs, err := db.ListJobs("", "", 100, 0)
		require.NoError(t, err, "ListJobs failed")

		var found bool
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				require.Equal(t, "develop", j.Branch)
				break
			}
		}
		require.True(t, found, "ListJobs did not return the job")
	})

	t.Run("ClaimJob returns branch", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		repo := createRepo(t, db, "/tmp/branch-test-repo")
		commit := createCommit(t, db, repo.ID, "branch123")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "branchclaim", Branch: "release/v1", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed")

		claimed, err := db.ClaimJob("test-worker")
		require.NoError(t, err, "ClaimJob failed")
		require.NotNil(t, claimed, "ClaimJob did not return a job")
		require.Equal(t, job.ID, claimed.ID, "ClaimJob did not return the expected job")
		require.Equal(t, "release/v1", claimed.Branch)
	})

	t.Run("empty branch is allowed", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		repo := createRepo(t, db, "/tmp/branch-test-repo")
		commit := createCommit(t, db, repo.ID, "branch123")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "nobranch", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob with empty branch failed")
		require.Empty(t, job.Branch, "Expected empty branch")
	})

	t.Run("UpdateJobBranch backfills empty branch", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		repo := createRepo(t, db, "/tmp/branch-test-repo")
		commit := createCommit(t, db, repo.ID, "branch123")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "updatebranch", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed")
		require.Empty(t, job.Branch, "Expected empty branch initially")

		rowsAffected, err := db.UpdateJobBranch(job.ID, "feature/backfilled")
		require.NoError(t, err, "UpdateJobBranch failed")
		require.Equal(t, int64(1), rowsAffected, "Expected 1 row affected")

		fetched, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID failed")
		require.Equal(t, "feature/backfilled", fetched.Branch)
	})

	t.Run("UpdateJobBranch does not overwrite existing branch", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		repo := createRepo(t, db, "/tmp/branch-test-repo")
		commit := createCommit(t, db, repo.ID, "branch123")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "nooverwrite", Branch: "original-branch", Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed")

		rowsAffected, err := db.UpdateJobBranch(job.ID, "new-branch")
		require.NoError(t, err, "UpdateJobBranch failed")
		require.Equal(t, int64(0), rowsAffected, "Expected 0 rows affected")

		fetched, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID failed")
		require.Equal(t, "original-branch", fetched.Branch)
	})
}

func TestRepoIdentity(t *testing.T) {
	t.Run("sets identity on create", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo, err := db.GetOrCreateRepo("/tmp/identity-test", "git@github.com:foo/bar.git")
		require.NoError(t, err, "GetOrCreateRepo failed")
		require.Equal(t, "git@github.com:foo/bar.git", repo.Identity)

		// Verify it persists
		repo2, err := db.GetOrCreateRepo("/tmp/identity-test")
		require.NoError(t, err, "GetOrCreateRepo (second call) failed")
		require.Equal(t, "git@github.com:foo/bar.git", repo2.Identity)
	})

	t.Run("backfills identity when not set", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create repo without identity
		repo1, err := db.GetOrCreateRepo("/tmp/backfill-test")
		require.NoError(t, err, "GetOrCreateRepo failed")
		require.Empty(t, repo1.Identity, "Expected no identity initially")

		// Call again with identity - should backfill
		repo2, err := db.GetOrCreateRepo("/tmp/backfill-test", "git@github.com:test/backfill.git")
		require.NoError(t, err, "GetOrCreateRepo with identity failed")
		require.Equal(t, "git@github.com:test/backfill.git", repo2.Identity)
	})

	t.Run("does not overwrite existing identity", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		// Create repo with identity
		_, err := db.GetOrCreateRepo("/tmp/no-overwrite-test", "original-identity")
		require.NoError(t, err, "GetOrCreateRepo failed")

		// Call again with different identity - should NOT overwrite
		repo2, err := db.GetOrCreateRepo("/tmp/no-overwrite-test", "new-identity")
		require.NoError(t, err, "GetOrCreateRepo with new identity failed")
		require.Equal(t, "original-identity", repo2.Identity)
	})

	t.Run("multiple clones with same identity allowed", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		sharedIdentity := "git@github.com:org/shared-repo.git"

		// Create first clone
		repo1, err := db.GetOrCreateRepo("/tmp/clone-1", sharedIdentity)
		require.NoError(t, err, "GetOrCreateRepo for clone-1 failed")
		require.Equal(t, sharedIdentity, repo1.Identity)

		// Create second clone with same identity - should succeed
		repo2, err := db.GetOrCreateRepo("/tmp/clone-2", sharedIdentity)
		require.NoError(t, err, "GetOrCreateRepo for clone-2 failed")
		require.Equal(t, sharedIdentity, repo2.Identity)

		// Verify they are different repos
		require.NotEqual(t, repo1.ID, repo2.ID, "Expected different repo IDs")
		require.NotEqual(t, repo1.RootPath, repo2.RootPath, "Expected different root paths")

		// Verify both repos exist and can be retrieved
		repos, err := db.ListRepos()
		require.NoError(t, err, "ListRepos failed")

		foundClone1, foundClone2 := false, false
		for _, r := range repos {
			if r.ID == repo1.ID {
				foundClone1 = true
			}
			if r.ID == repo2.ID {
				foundClone2 = true
			}
		}
		require.True(t, foundClone1 && foundClone2, "Expected both clones to exist in ListRepos")
	})
}

func TestDuplicateSHAHandling(t *testing.T) {
	t.Run("same SHA in different repos creates separate commits", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo1 := createRepo(t, db, "/tmp/sha-test-1")
		repo2 := createRepo(t, db, "/tmp/sha-test-2")

		commit1 := createCommit(t, db, repo1.ID, "abc123")
		commit2 := createCommit(t, db, repo2.ID, "abc123")

		require.NotEqual(t, commit1.ID, commit2.ID, "Same SHA in different repos should create different commits")
		require.Equal(t, repo1.ID, commit1.RepoID, "commit1 should belong to repo1")
		require.Equal(t, repo2.ID, commit2.RepoID, "commit2 should belong to repo2")
	})

	t.Run("GetCommitBySHA returns error when ambiguous", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo1 := createRepo(t, db, "/tmp/ambiguous-1")
		repo2 := createRepo(t, db, "/tmp/ambiguous-2")

		createCommit(t, db, repo1.ID, "ambiguous-sha")
		createCommit(t, db, repo2.ID, "ambiguous-sha")

		_, err := db.GetCommitBySHA("ambiguous-sha")
		require.Error(t, err, "Expected error when SHA is ambiguous across repos")
	})

	t.Run("GetCommitByRepoAndSHA returns correct commit", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		repo1 := createRepo(t, db, "/tmp/repo-and-sha-1")
		repo2 := createRepo(t, db, "/tmp/repo-and-sha-2")

		commit1 := createCommit(t, db, repo1.ID, "same-sha")
		commit2 := createCommit(t, db, repo2.ID, "same-sha")

		found1, err := db.GetCommitByRepoAndSHA(repo1.ID, "same-sha")
		require.NoError(t, err, "GetCommitByRepoAndSHA for repo1 failed")
		require.Equal(t, commit1.ID, found1.ID, "GetCommitByRepoAndSHA returned wrong commit for repo1")

		found2, err := db.GetCommitByRepoAndSHA(repo2.ID, "same-sha")
		require.NoError(t, err, "GetCommitByRepoAndSHA for repo2 failed")
		require.Equal(t, commit2.ID, found2.ID, "GetCommitByRepoAndSHA returned wrong commit for repo2")
	})
}
