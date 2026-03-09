package storage

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestJobCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	for i := range 3 {
		sha := fmt.Sprintf("queued%d", i)
		commit := createCommit(t, db, repo.ID, sha)
		enqueueJob(t, db, repo.ID, commit.ID, sha)
	}

	commit := createCommit(t, db, repo.ID, "done1")
	job := enqueueJob(t, db, repo.ID, commit.ID, "done1")
	_, _ = db.ClaimJob("drain1")
	_, _ = db.ClaimJob("drain2")
	_, _ = db.ClaimJob("drain3")
	claimed, _ := db.ClaimJob("w1")
	if claimed != nil {
		assert.Equal(t, claimed.ID, job.ID, "unexpected condition")
		db.CompleteJob(claimed.ID, "codex", "p", "o")
	}

	commit2 := createCommit(t, db, repo.ID, "fail1")
	enqueueJob(t, db, repo.ID, commit2.ID, "fail1")
	claimed2, _ := db.ClaimJob("w2")
	if claimed2 != nil {
		db.FailJob(claimed2.ID, "", "err")
	}

	queued, running, done, failed, _, _, _, err := db.GetJobCounts()
	require.NoError(t, err, "GetJobCounts failed: %v")

	assert.Equal(t, 0, queued, "unexpected condition")
	assert.Equal(t, 3, running, "unexpected condition")
	assert.Equal(t, 1, done, "unexpected condition")
	assert.Equal(t, 1, failed, "unexpected condition")
}

func TestCountStalledJobs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, _, _ := createJobChain(t, db, "/tmp/test-repo", "recent1")
	_, _ = db.ClaimJob("worker-1")

	count, err := db.CountStalledJobs(30 * time.Minute)
	require.NoError(t, err, "CountStalledJobs failed: %v")

	assert.Equal(t, 0, count, "unexpected condition")

	commit2 := createCommit(t, db, repo.ID, "stalled1")
	job2 := enqueueJob(t, db, repo.ID, commit2.ID, "stalled1")
	backdateJobStart(t, db, job2.ID, 1*time.Hour)

	count, err = db.CountStalledJobs(30 * time.Minute)
	require.NoError(t, err, "CountStalledJobs failed: %v")

	assert.Equal(t, 1, count, "unexpected condition")

	commit3 := createCommit(t, db, repo.ID, "stalled2")
	job3 := enqueueJob(t, db, repo.ID, commit3.ID, "stalled2")

	tzMinus7 := time.FixedZone("UTC-7", -7*60*60)
	backdateJobStartWithOffset(t, db, job3.ID, 1*time.Hour, tzMinus7)

	count, err = db.CountStalledJobs(30 * time.Minute)
	require.NoError(t, err, "CountStalledJobs failed: %v")

	assert.Equal(t, 2, count, "unexpected condition")

	count, err = db.CountStalledJobs(2 * time.Hour)
	require.NoError(t, err, "CountStalledJobs failed: %v")

	assert.Equal(t, 0, count, "unexpected condition")
}

func TestListReposWithReviewCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("empty database", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts()
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Empty(t, repos, "unexpected condition")
		assert.Equal(t, 0, totalCount, "unexpected condition")
	})

	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")
	_ = createRepo(t, db, "/tmp/repo3")

	for i := range 3 {
		sha := fmt.Sprintf("repo1-sha%d", i)
		commit := createCommit(t, db, repo1.ID, sha)
		enqueueJob(t, db, repo1.ID, commit.ID, sha)
	}

	for i := range 2 {
		sha := fmt.Sprintf("repo2-sha%d", i)
		commit := createCommit(t, db, repo2.ID, sha)
		enqueueJob(t, db, repo2.ID, commit.ID, sha)
	}

	t.Run("repos with varying job counts", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts()
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Len(t, repos, 3, "unexpected condition")

		assert.Equal(t, 5, totalCount, "unexpected condition")

		repoMap := make(map[string]int)
		for _, r := range repos {
			repoMap[r.Name] = r.Count
		}

		assert.Equal(t, 3, repoMap["repo1"], "unexpected condition")
		assert.Equal(t, 2, repoMap["repo2"], "unexpected condition")
		assert.Equal(t, 0, repoMap["repo3"], "unexpected condition")
	})

	t.Run("counts include all job statuses", func(t *testing.T) {

		claimed, _ := db.ClaimJob("worker-1")
		if claimed != nil {
			db.CompleteJob(claimed.ID, "codex", "prompt", "output")
		}

		claimed2, _ := db.ClaimJob("worker-1")
		if claimed2 != nil {
			db.FailJob(claimed2.ID, "", "test error")
		}

		repos, totalCount, err := db.ListReposWithReviewCounts()
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Equal(t, 5, totalCount, "unexpected condition")

		repoMap := make(map[string]int)
		for _, r := range repos {
			repoMap[r.Name] = r.Count
		}

		assert.Equal(t, 3, repoMap["repo1"], "unexpected condition")
	})
}

func TestListJobsWithRepoFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	for i := range 3 {
		sha := fmt.Sprintf("repo1-sha%d", i)
		commit := createCommit(t, db, repo1.ID, sha)
		enqueueJob(t, db, repo1.ID, commit.ID, sha)
	}

	for i := range 2 {
		sha := fmt.Sprintf("repo2-sha%d", i)
		commit := createCommit(t, db, repo2.ID, sha)
		enqueueJob(t, db, repo2.ID, commit.ID, sha)
	}

	t.Run("no filter returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 5, "unexpected condition")
	})

	t.Run("repo filter returns only matching jobs", func(t *testing.T) {

		jobs, err := db.ListJobs("", repo1.RootPath, 50, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 3, "unexpected condition")
		for _, job := range jobs {
			assert.Equal(t, "repo1", job.RepoName, "unexpected condition")
		}
	})

	t.Run("limit parameter works", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 2, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 2, "unexpected condition")
	})

	t.Run("limit=0 returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 0, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 5, "unexpected condition")
	})

	t.Run("repo filter with limit", func(t *testing.T) {
		jobs, err := db.ListJobs("", repo1.RootPath, 2, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 2, "unexpected condition")
		for _, job := range jobs {
			assert.Equal(t, "repo1", job.RepoName, "unexpected condition")
		}
	})

	t.Run("status and repo filter combined", func(t *testing.T) {

		claimed, err := db.ClaimJob("worker-1")
		require.NoError(t, err, "ClaimJob failed: %v")

		if err := db.CompleteJob(claimed.ID, "codex", "prompt", "output"); err != nil {
			require.NoError(t, err, "CompleteJob failed: %v")
		}

		jobs, err := db.ListJobs("done", repo1.RootPath, 50, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
		assert.False(t, len(jobs) > 0 && jobs[0].Status != JobStatusDone, "unexpected condition")
	})

	t.Run("offset pagination", func(t *testing.T) {

		jobs1, err := db.ListJobs("", "", 2, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs1, 2, "unexpected condition")

		jobs2, err := db.ListJobs("", "", 2, 2)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs2, 2, "unexpected condition")

		for _, j1 := range jobs1 {
			for _, j2 := range jobs2 {
				assert.NotEqual(t, j1.ID, j2.ID, "unexpected condition")
			}
		}

		jobs3, err := db.ListJobs("", "", 2, 4)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs3, 1, "unexpected condition")
	})
}

func TestListJobsWithGitRefFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/repo-gitref")

	refs := []string{"abc123", "def456", "abc123..def456", "dirty"}
	for _, ref := range refs {
		commit := createCommit(t, db, repo.ID, ref)
		enqueueJob(t, db, repo.ID, commit.ID, ref)
	}

	t.Run("git_ref filter returns matching job", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("abc123"))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
		assert.False(t, len(jobs) > 0 && jobs[0].GitRef != "abc123", "unexpected condition")
	})

	t.Run("git_ref filter with range ref", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("abc123..def456"))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
		assert.False(t, len(jobs) > 0 && jobs[0].GitRef != "abc123..def456", "unexpected condition")
	})

	t.Run("git_ref filter with no match returns empty", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithGitRef("nonexistent"))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Empty(t, jobs, "unexpected condition")
	})

	t.Run("empty git_ref filter returns all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 4, "unexpected condition")
	})

	t.Run("git_ref filter combined with repo filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", repo.RootPath, 50, 0, WithGitRef("def456"))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
	})
}

func TestListJobsWithBranchAndClosedFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/repo-branch-addr")
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	branches := []string{"main", "main", "feature"}
	for i, br := range branches {
		sha := fmt.Sprintf("sha%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		require.NoError(t, err, "GetOrCreateCommit failed: %v")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Branch: br, Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed: %v")

		db.ClaimJob("w")
		db.CompleteJob(job.ID, "codex", "", fmt.Sprintf("output %d", i))

		if i == 0 {
			db.MarkReviewClosedByJobID(job.ID, true)
		}
	}

	t.Run("branch filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 2, "unexpected condition")
	})

	t.Run("closed=false filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithClosed(false))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 2, "unexpected condition")
	})

	t.Run("closed=true filter", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithClosed(true))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
	})

	t.Run("branch + closed combined", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"), WithClosed(false))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
	})
}

func TestWithBranchOrEmpty(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, err := db.GetOrCreateRepo("/tmp/repo-branch-empty")
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	for i, br := range []string{"main", "feature", ""} {
		sha := fmt.Sprintf("sha-be-%d", i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		require.NoError(t, err, "GetOrCreateCommit failed: %v")

		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Branch: br, Agent: "codex"})
		require.NoError(t, err, "EnqueueJob failed: %v")

		db.ClaimJob("w")
		db.CompleteJob(job.ID, "codex", "", fmt.Sprintf("output %d", i))
	}

	t.Run("WithBranch strict excludes branchless", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranch("main"))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
	})

	t.Run("WithBranchOrEmpty includes branchless", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0, WithBranchOrEmpty("main"))
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 2, "unexpected condition")
	})
}

func TestListJobsAndGetJobByIDReturnAgentic(t *testing.T) {

	db := openTestDB(t)
	defer db.Close()

	repoPath := filepath.Join(t.TempDir(), "agentic-test-repo")
	repo, err := db.GetOrCreateRepo(repoPath)
	require.NoError(t, err, "GetOrCreateRepo failed: %v")

	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:  repo.ID,
		Agent:   "test-agent",
		Prompt:  "Review this code",
		Agentic: true,
	})
	require.NoError(t, err, "EnqueuePromptJob failed: %v")

	assert.True(t, job.Agentic, "EnqueuePromptJob should return job with Agentic=true")

	t.Run("ListJobs returns agentic field", func(t *testing.T) {
		jobs, err := db.ListJobs("", "", 50, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.NotEmpty(t, jobs, "unexpected condition")

		var found bool
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				assert.True(t, j.Agentic, "unexpected condition")
				break
			}
		}
		assert.True(t, found, "unexpected condition")
	})

	t.Run("GetJobByID returns agentic field", func(t *testing.T) {
		fetchedJob, err := db.GetJobByID(job.ID)
		require.NoError(t, err, "GetJobByID failed: %v")

		assert.True(t, fetchedJob.Agentic, "unexpected condition")
	})

	t.Run("non-agentic job returns Agentic=false", func(t *testing.T) {
		nonAgenticJob, err := db.EnqueueJob(EnqueueOpts{
			RepoID: repo.ID,
			Agent:  "test-agent",
			Prompt: "Another review",
		})
		require.NoError(t, err, "EnqueuePromptJob failed: %v")

		fetchedJob, err := db.GetJobByID(nonAgenticJob.ID)
		require.NoError(t, err, "GetJobByID failed: %v")

		assert.False(t, fetchedJob.Agentic, "unexpected condition")

		jobs, err := db.ListJobs("", "", 50, 0)
		require.NoError(t, err, "ListJobs failed: %v")

		var found bool
		for _, j := range jobs {
			if j.ID == nonAgenticJob.ID {
				found = true
				assert.False(t, j.Agentic, "unexpected condition")
				break
			}
		}
		assert.True(t, found, "unexpected condition")
	})
}

func TestListReposWithReviewCountsByBranch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

	commit1 := createCommit(t, db, repo1.ID, "abc123")
	commit2 := createCommit(t, db, repo1.ID, "def456")
	commit3 := createCommit(t, db, repo2.ID, "ghi789")

	job1 := enqueueJob(t, db, repo1.ID, commit1.ID, "abc123")
	job2 := enqueueJob(t, db, repo1.ID, commit2.ID, "def456")
	job3 := enqueueJob(t, db, repo2.ID, commit3.ID, "ghi789")

	setJobBranch(t, db, job1.ID, "main")
	setJobBranch(t, db, job3.ID, "main")
	setJobBranch(t, db, job2.ID, "feature")

	t.Run("filter by main branch", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts(WithRepoBranch("main"))
		require.NoError(t, err, "ListReposWithReviewCounts(branch=main) failed: %v")

		assert.Len(t, repos, 2, "unexpected condition")
		assert.Equal(t, 2, totalCount, "unexpected condition")
	})

	t.Run("filter by feature branch", func(t *testing.T) {
		repos, totalCount, err := db.ListReposWithReviewCounts(WithRepoBranch("feature"))
		require.NoError(t, err, "ListReposWithReviewCounts(branch=feature) failed: %v")

		assert.Len(t, repos, 1, "unexpected condition")
		assert.Equal(t, 1, totalCount, "unexpected condition")
	})

	t.Run("filter by (none) branch", func(t *testing.T) {

		commit4 := createCommit(t, db, repo1.ID, "jkl012")
		enqueueJob(t, db, repo1.ID, commit4.ID, "jkl012")

		repos, totalCount, err := db.ListReposWithReviewCounts(WithRepoBranch("(none)"))
		require.NoError(t, err, "ListReposWithReviewCounts(branch=(none)) failed: %v")

		assert.Len(t, repos, 1, "unexpected condition")
		assert.Equal(t, 1, totalCount, "unexpected condition")
	})

	t.Run("empty filter returns all", func(t *testing.T) {
		repos, _, err := db.ListReposWithReviewCounts()
		require.NoError(t, err, "ListReposWithReviewCounts() failed: %v")

		assert.Len(t, repos, 2, "unexpected condition")
	})
}

func TestListBranchesWithCounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")

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

	setJobBranch(t, db, job1.ID, "main")
	setJobBranch(t, db, job2.ID, "main")
	setJobBranch(t, db, job4.ID, "main")
	setJobBranch(t, db, job3.ID, "feature")

	t.Run("list all branches", func(t *testing.T) {
		result, err := db.ListBranchesWithCounts(nil)
		require.NoError(t, err, "ListBranchesWithCounts failed: %v")

		assert.Len(t, result.Branches, 3, "unexpected condition")
		assert.Equal(t, 5, result.TotalCount, "unexpected condition")
		assert.Equal(t, 1, result.NullsRemaining, "unexpected condition")
	})

	t.Run("filter by single repo", func(t *testing.T) {

		result, err := db.ListBranchesWithCounts([]string{repo1.RootPath})
		require.NoError(t, err, "ListBranchesWithCounts failed: %v")

		assert.Len(t, result.Branches, 2, "unexpected condition")
		assert.Equal(t, 3, result.TotalCount, "unexpected condition")
	})

	t.Run("filter by multiple repos", func(t *testing.T) {

		result, err := db.ListBranchesWithCounts([]string{repo1.RootPath, repo2.RootPath})
		require.NoError(t, err, "ListBranchesWithCounts failed: %v")

		assert.Len(t, result.Branches, 3, "unexpected condition")
		assert.Equal(t, 5, result.TotalCount, "unexpected condition")
	})

	t.Run("no nulls when all have branches", func(t *testing.T) {
		setJobBranch(t, db, job5.ID, "develop")
		result, err := db.ListBranchesWithCounts(nil)
		require.NoError(t, err, "ListBranchesWithCounts failed: %v")

		assert.Equal(t, 0, result.NullsRemaining, "unexpected condition")
	})
}

func TestListJobsVerdictForBranchRangeReview(t *testing.T) {

	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, filepath.Join(t.TempDir(), "range-verdict-repo"))

	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, GitRef: "abc123..def456", Agent: "codex"})
	require.NoError(t, err, "EnqueueRangeJob failed: %v")

	_, err = db.ClaimJob("worker-0")
	require.NoError(t, err, "ClaimJob failed: %v")

	err = db.CompleteJob(job.ID, "codex", "review prompt", "- Medium — Bug in line 42\nSummary: found issues.")
	require.NoError(t, err, "CompleteJob failed: %v")

	jobs, err := db.ListJobs("", "", 50, 0)
	require.NoError(t, err, "ListJobs failed: %v")

	var found bool
	for _, j := range jobs {
		if j.ID == job.ID {
			found = true
			assert.NotNil(t, j.Verdict, "unexpected condition")
			assert.Equal(t, "F", *j.Verdict, "unexpected condition")
			break
		}
	}
	assert.True(t, found, "unexpected condition")
}

func TestListJobsWithJobTypeFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo, commit, reviewJob := createJobChain(t, db, "/tmp/repo-jobtype", "jt-sha")

	_, err := db.EnqueueJob(EnqueueOpts{
		RepoID:      repo.ID,
		CommitID:    commit.ID,
		GitRef:      "jt-sha",
		Agent:       "codex",
		JobType:     JobTypeFix,
		ParentJobID: reviewJob.ID,
	})
	require.NoError(t, err, "EnqueueJob fix failed: %v")

	tests := []struct {
		name          string
		opts          []ListJobsOption
		expectedLen   int
		expectedTypes []string
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
			require.NoError(t, err, "ListJobs failed: %v")

			if len(jobs) != tt.expectedLen {
				assert.Len(t, jobs, tt.expectedLen, "Expected %d jobs, got %d", tt.expectedLen, len(jobs))
			}
			if tt.expectedTypes != nil {
				for i, typ := range tt.expectedTypes {
					assert.Equal(t, jobs[i].JobType, typ, "unexpected condition")
				}
			}
		})
	}
}

func TestEscapeLike(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain", "plain"},
		{"100%", "100!%"},
		{"under_score", "under!_score"},
		{"has!bang", "has!!bang"},
		{`C:\Users\foo`, `C:\Users\foo`},
		{"combo!_%", "combo!!!_!%"},
	}
	for _, tt := range tests {
		got := escapeLike(tt.input)
		assert.Equal(t, tt.want, got, "escapeLike(%q) = %q, want %q", tt.input, got, tt.want)

	}
}

func TestPrefixFilterWithSpecialChars(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	other := filepath.Join(base, "other")
	wsPrefix := filepath.ToSlash(workspace)
	otherPrefix := filepath.ToSlash(other)

	createRepo(t, db, filepath.Join(workspace, "repo_one"))
	createRepo(t, db, filepath.Join(workspace, "repo%two"))
	createRepo(t, db, filepath.Join(other, "repo"))

	repo1, _ := db.GetRepoByPath(filepath.Join(workspace, "repo_one"))
	commit1 := createCommit(t, db, repo1.ID, "sha1")
	enqueueJob(t, db, repo1.ID, commit1.ID, "sha1")

	repo2, _ := db.GetRepoByPath(filepath.Join(workspace, "repo%two"))
	commit2 := createCommit(t, db, repo2.ID, "sha2")
	enqueueJob(t, db, repo2.ID, commit2.ID, "sha2")

	repo3, _ := db.GetRepoByPath(filepath.Join(other, "repo"))
	commit3 := createCommit(t, db, repo3.ID, "sha3")
	enqueueJob(t, db, repo3.ID, commit3.ID, "sha3")

	t.Run("prefix with underscore matches correctly", func(t *testing.T) {
		jobs, err := db.ListJobs(
			"", "", 50, 0, WithRepoPrefix(wsPrefix),
		)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 2, "unexpected condition")
	})

	t.Run("prefix filter excludes non-matching", func(t *testing.T) {
		jobs, err := db.ListJobs(
			"", "", 50, 0, WithRepoPrefix(otherPrefix),
		)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 1, "unexpected condition")
	})

	for range 3 {
		claimed := claimJob(t, db, "w1")
		if err := db.CompleteJob(claimed.ID, "codex", "p", "o"); err != nil {
			require.NoError(t, err, "CompleteJob failed: %v")
		}
	}

	t.Run("CountJobStats with special-char prefix", func(t *testing.T) {
		stats, err := db.CountJobStats(
			"", WithRepoPrefix(wsPrefix),
		)
		require.NoError(t, err, "CountJobStats failed: %v")

		assert.Equal(t, 2, stats.Done, "unexpected condition")
		assert.Equal(t, 2, stats.Open, "unexpected condition")
	})

	t.Run("ListReposWithReviewCounts with special-char prefix", func(t *testing.T) {
		repos, total, err := db.ListReposWithReviewCounts(
			WithRepoPathPrefix(wsPrefix),
		)
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Len(t, repos, 2, "unexpected condition")
		assert.Equal(t, 2, total, "unexpected condition")
	})

	t.Run("backslash in path does not cause SQL error", func(t *testing.T) {

		createRepo(t, db, `C:\Users\dev\workspace\project-a`)
		rA, _ := db.GetRepoByPath(`C:\Users\dev\workspace\project-a`)
		cA := createCommit(t, db, rA.ID, "win-a")
		enqueueJob(t, db, rA.ID, cA.ID, "win-a")

		_, err := db.ListJobs(
			"", "", 50, 0, WithRepoPrefix(`C:\Users\dev\workspace`),
		)
		require.NoError(t, err, "ListJobs with backslash prefix should not error: %v")

		_, err = db.CountJobStats(
			"", WithRepoPrefix(`C:\Users\dev\workspace`),
		)
		require.NoError(t, err, "CountJobStats with backslash prefix should not error: %v")

		_, _, err = db.ListReposWithReviewCounts(
			WithRepoPathPrefix(`C:\Users\dev\workspace`),
		)
		require.NoError(t, err, "ListReposWithReviewCounts with backslash prefix should not error: %v")

	})
}

func TestRootPrefixMatchesAllRepos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	base := t.TempDir()
	basePrefix := filepath.ToSlash(base)

	createRepo(t, db, filepath.Join(base, "a", "repo1"))
	createRepo(t, db, filepath.Join(base, "b", "repo2"))

	r1, _ := db.GetRepoByPath(filepath.Join(base, "a", "repo1"))
	c1 := createCommit(t, db, r1.ID, "r1")
	enqueueJob(t, db, r1.ID, c1.ID, "r1")

	r2, _ := db.GetRepoByPath(filepath.Join(base, "b", "repo2"))
	c2 := createCommit(t, db, r2.ID, "r2")
	enqueueJob(t, db, r2.ID, c2.ID, "r2")

	t.Run("parent prefix returns all repos via ListJobs", func(t *testing.T) {
		jobs, err := db.ListJobs(
			"", "", 50, 0, WithRepoPrefix(basePrefix),
		)
		require.NoError(t, err, "ListJobs failed: %v")

		assert.Len(t, jobs, 2, "unexpected condition")
	})

	t.Run("parent prefix returns all repos via ListReposWithReviewCounts", func(t *testing.T) {
		repos, total, err := db.ListReposWithReviewCounts(
			WithRepoPathPrefix(basePrefix),
		)
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Len(t, repos, 2, "unexpected condition")
		assert.Equal(t, 2, total, "unexpected condition")
	})
}

func TestListReposWithCombinedPrefixAndBranch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	wsPrefix := filepath.ToSlash(ws)

	r1 := createRepo(t, db, filepath.Join(ws, "repo-a"))
	r2 := createRepo(t, db, filepath.Join(ws, "repo-b"))
	r3 := createRepo(t, db, filepath.Join(base, "other", "repo-c"))

	c1 := createCommit(t, db, r1.ID, "a1")
	c2 := createCommit(t, db, r1.ID, "a2")
	c3 := createCommit(t, db, r1.ID, "a3")
	j1 := enqueueJob(t, db, r1.ID, c1.ID, "a1")
	j2 := enqueueJob(t, db, r1.ID, c2.ID, "a2")
	j3 := enqueueJob(t, db, r1.ID, c3.ID, "a3")
	setJobBranch(t, db, j1.ID, "main")
	setJobBranch(t, db, j2.ID, "main")
	setJobBranch(t, db, j3.ID, "feature")

	c4 := createCommit(t, db, r2.ID, "b1")
	j4 := enqueueJob(t, db, r2.ID, c4.ID, "b1")
	setJobBranch(t, db, j4.ID, "main")

	c5 := createCommit(t, db, r3.ID, "c1")
	j5 := enqueueJob(t, db, r3.ID, c5.ID, "c1")
	setJobBranch(t, db, j5.ID, "main")

	t.Run("prefix + branch filters both", func(t *testing.T) {
		repos, total, err := db.ListReposWithReviewCounts(
			WithRepoPathPrefix(wsPrefix),
			WithRepoBranch("main"),
		)
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Len(t, repos, 2, "unexpected condition")
		assert.Equal(t, 3, total, "unexpected condition")
	})

	t.Run("prefix + feature branch", func(t *testing.T) {
		repos, total, err := db.ListReposWithReviewCounts(
			WithRepoPathPrefix(wsPrefix),
			WithRepoBranch("feature"),
		)
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Len(t, repos, 1, "unexpected condition")
		assert.Equal(t, 1, total, "unexpected condition")
	})

	t.Run("prefix only returns all branches", func(t *testing.T) {
		repos, total, err := db.ListReposWithReviewCounts(
			WithRepoPathPrefix(wsPrefix),
		)
		require.NoError(t, err, "ListReposWithReviewCounts failed: %v")

		assert.Len(t, repos, 2, "unexpected condition")
		assert.Equal(t, 4, total, "unexpected condition")
	})
}
