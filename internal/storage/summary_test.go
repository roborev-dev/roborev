package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSummary_Empty(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-7 * 24 * time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, s.Overview.Total)
	assert.Equal(t, 0, s.Verdicts.Total)
	assert.Empty(t, s.Agents)
	assert.Empty(t, s.JobTypes)
	assert.Empty(t, s.Hotspots)
	assert.Equal(t, 0, s.Failures.Total)
}

func TestGetSummary_Overview(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Enqueue 3 jobs, complete 2 (one pass, one fail), leave 1 queued
	j1 := enqueueJob(t, db, repo.ID, commit.ID, "abc123")
	j2 := enqueueJob(t, db, repo.ID, commit.ID, "abc123")
	_ = enqueueJob(t, db, repo.ID, commit.ID, "abc123") // stays queued

	// Claim and complete j1 (pass)
	claimJob(t, db, "w1")
	require.NoError(t, db.CompleteJob(j1.ID, "codex", "prompt", "No issues found."))

	// Claim and complete j2 (fail)
	claimJob(t, db, "w2")
	require.NoError(t, db.CompleteJob(j2.ID, "codex", "prompt", "- Medium — Bug found"))

	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)

	assert.Equal(t, 3, s.Overview.Total)
	assert.Equal(t, 2, s.Overview.Done)
	assert.Equal(t, 1, s.Overview.Queued)
}

func TestGetSummary_Verdicts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Create 3 completed jobs: 2 pass, 1 fail
	for range 2 {
		j := enqueueJob(t, db, repo.ID, commit.ID, "abc123")
		claimJob(t, db, "w1")
		require.NoError(t, db.CompleteJob(j.ID, "codex", "p", "No issues found."))
	}
	j := enqueueJob(t, db, repo.ID, commit.ID, "abc123")
	claimJob(t, db, "w1")
	require.NoError(t, db.CompleteJob(j.ID, "codex", "p", "- High — Security issue"))

	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)

	assert.Equal(t, 3, s.Verdicts.Total)
	assert.Equal(t, 2, s.Verdicts.Passed)
	assert.Equal(t, 1, s.Verdicts.Failed)
	assert.InDelta(t, 0.667, s.Verdicts.PassRate, 0.01)
}

func TestGetSummary_AgentBreakdown(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Create jobs with different agents
	for _, agent := range []string{"codex", "codex", "claude-code"} {
		j, err := db.EnqueueJob(EnqueueOpts{
			RepoID: repo.ID, CommitID: commit.ID, GitRef: "abc123", Agent: agent,
		})
		require.NoError(t, err)
		claimJob(t, db, "w1")
		require.NoError(t, db.CompleteJob(j.ID, agent, "p", "No issues found."))
	}

	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)

	assert.Len(t, s.Agents, 2)
	// Sorted by total DESC
	assert.Equal(t, "codex", s.Agents[0].Agent)
	assert.Equal(t, 2, s.Agents[0].Total)
	assert.Equal(t, "claude-code", s.Agents[1].Agent)
	assert.Equal(t, 1, s.Agents[1].Total)
}

func TestGetSummary_JobTypes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Regular review
	enqueueJob(t, db, repo.ID, commit.ID, "abc123")

	// Task job
	_, err := db.EnqueueJob(EnqueueOpts{
		RepoID: repo.ID, GitRef: "analyze", Agent: "codex",
		Prompt: "analyze this", JobType: JobTypeTask,
	})
	require.NoError(t, err)

	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)

	assert.Len(t, s.JobTypes, 2)
	typeMap := make(map[string]int)
	for _, jt := range s.JobTypes {
		typeMap[jt.Type] = jt.Count
	}
	assert.Equal(t, 1, typeMap["review"])
	assert.Equal(t, 1, typeMap["task"])
}

func TestGetSummary_Failures(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Create and fail jobs with different errors
	errors := []string{
		"quota exceeded: rate limit hit",
		"timeout: deadline exceeded",
		"exit status 1",
	}
	for _, errMsg := range errors {
		j := enqueueJob(t, db, repo.ID, commit.ID, "abc123")
		claimJob(t, db, "w1")
		_, err := db.FailJob(j.ID, "w1", errMsg)
		require.NoError(t, err)
	}

	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)

	assert.Equal(t, 3, s.Failures.Total)
	assert.Equal(t, 1, s.Failures.Errors["quota"])
	assert.Equal(t, 1, s.Failures.Errors["timeout"])
	assert.Equal(t, 1, s.Failures.Errors["crash"])
}

func TestGetSummary_RepoFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo1 := createRepo(t, db, "/tmp/repo1")
	repo2 := createRepo(t, db, "/tmp/repo2")
	c1 := createCommit(t, db, repo1.ID, "aaa111")
	c2 := createCommit(t, db, repo2.ID, "bbb222")

	// 2 jobs in repo1, 1 in repo2
	enqueueJob(t, db, repo1.ID, c1.ID, "aaa111")
	enqueueJob(t, db, repo1.ID, c1.ID, "aaa111")
	enqueueJob(t, db, repo2.ID, c2.ID, "bbb222")

	// All repos
	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, 3, s.Overview.Total)

	// Filtered to repo1
	s, err = db.GetSummary(SummaryOptions{
		RepoPath: "/tmp/repo1",
		Since:    time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, s.Overview.Total)
}

func TestGetSummary_BranchFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Jobs on different branches
	for _, branch := range []string{"main", "main", "feature"} {
		_, err := db.EnqueueJob(EnqueueOpts{
			RepoID: repo.ID, CommitID: commit.ID, GitRef: "abc123",
			Agent: "codex", Branch: branch,
		})
		require.NoError(t, err)
	}

	s, err := db.GetSummary(SummaryOptions{
		Branch: "main",
		Since:  time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, s.Overview.Total)
}

func TestGetSummary_SinceFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Create a job now
	enqueueJob(t, db, repo.ID, commit.ID, "abc123")

	// Query with a since in the future — should find nothing
	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(1 * time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, s.Overview.Total)

	// Query with since in the past — should find the job
	s, err = db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, s.Overview.Total)
}

func TestGetSummary_Hotspots(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repo := createRepo(t, db, "/tmp/test-repo")

	// Create multiple failing reviews for one ref and one for another
	for i := range 3 {
		c := createCommit(t, db, repo.ID, "hot"+string(rune('a'+i)))
		j, err := db.EnqueueJob(EnqueueOpts{
			RepoID: repo.ID, CommitID: c.ID, GitRef: "hotref", Agent: "codex",
		})
		require.NoError(t, err)
		claimJob(t, db, "w1")
		require.NoError(t, db.CompleteJob(j.ID, "codex", "p", "- High — Bug"))
	}
	c := createCommit(t, db, repo.ID, "cold1")
	j, err := db.EnqueueJob(EnqueueOpts{
		RepoID: repo.ID, CommitID: c.ID, GitRef: "coldref", Agent: "codex",
	})
	require.NoError(t, err)
	claimJob(t, db, "w1")
	require.NoError(t, db.CompleteJob(j.ID, "codex", "p", "- Low — Minor thing"))

	s, err := db.GetSummary(SummaryOptions{
		Since: time.Now().Add(-1 * time.Hour),
	})
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(s.Hotspots), 1)
	assert.Equal(t, "hotref", s.Hotspots[0].GitRef)
	assert.Equal(t, 3, s.Hotspots[0].Failures)
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		p      float64
		want   float64
	}{
		{"empty", nil, 0.5, 0},
		{"single", []float64{42}, 0.5, 42},
		{"two_p50", []float64{10, 20}, 0.5, 15},
		{"three_p50", []float64{10, 20, 30}, 0.5, 20},
		{"five_p90", []float64{1, 2, 3, 4, 5}, 0.9, 4.6},
		{"unsorted", []float64{5, 1, 3, 2, 4}, 0.5, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentile(tt.values, tt.p)
			assert.InDelta(t, tt.want, got, 0.01)
		})
	}
}

func TestCategorizeError(t *testing.T) {
	tests := []struct {
		err  string
		want string
	}{
		{"quota exceeded", "quota"},
		{"rate limit hit (429)", "quota"},
		{"context deadline exceeded", "timeout"},
		{"process killed by signal", "crash"},
		{"exit status 1", "crash"},
		{"something unknown", "other"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, categorizeError(tt.err))
		})
	}
}
