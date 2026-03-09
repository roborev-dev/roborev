package storage

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGetKnownJobUUIDs(t *testing.T) {
	h := newSyncTestHelper(t)

	t.Run("returns empty when no jobs exist", func(t *testing.T) {
		uuids, err := h.db.GetKnownJobUUIDs()
		require.NoError(t, err, "GetKnownJobUUIDs failed: %v")

		assert.Empty(t, uuids)
	})

	t.Run("returns UUIDs of jobs with UUIDs", func(t *testing.T) {

		job1 := h.createPendingJob("abc123")
		job2 := h.createPendingJob("def456")

		uuids, err := h.db.GetKnownJobUUIDs()
		require.NoError(t, err, "GetKnownJobUUIDs failed: %v")

		assert.Len(t, uuids, 2)

		uuidMap := make(map[string]bool)
		for _, u := range uuids {
			uuidMap[u] = true
		}

		assert.True(t, uuidMap[job1.UUID])
		assert.True(t, uuidMap[job2.UUID])
	})
}

func TestParseSQLiteTime(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantYear int
		wantZero bool
	}{
		{
			name:     "RFC3339 with Z",
			input:    "2024-06-15T10:30:00Z",
			wantYear: 2024,
		},
		{
			name:     "RFC3339 with offset",
			input:    "2024-06-15T10:30:00-05:00",
			wantYear: 2024,
		},
		{
			name:     "RFC3339 with positive offset",
			input:    "2024-06-15T10:30:00+02:00",
			wantYear: 2024,
		},
		{
			name:     "SQLite datetime format",
			input:    "2024-06-15 10:30:00",
			wantYear: 2024,
		},
		{
			name:     "empty string",
			input:    "",
			wantZero: true,
		},
		{
			name:     "invalid format",
			input:    "not-a-date",
			wantZero: true,
		},
		{
			name:     "partial date",
			input:    "2024-06-15",
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSQLiteTime(tt.input)
			if tt.wantZero {
				assert.True(t, got.IsZero())
				return
			}
			assert.False(t, got.IsZero())
			assert.Equal(t, tt.wantYear, got.Year())
		})
	}
}

func TestGetJobsToSync_TimestampComparison(t *testing.T) {
	h := newSyncTestHelper(t)
	job := h.createCompletedJob("sync-test-sha")

	t.Run("job with null synced_at is returned", func(t *testing.T) {
		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		require.NoError(t, err, "GetJobsToSync failed: %v")

		found := false
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				break
			}
		}
		assert.True(t, found)
	})

	t.Run("job after MarkJobSynced is not returned", func(t *testing.T) {
		err := h.db.MarkJobSynced(job.ID)
		require.NoError(t, err, "MarkJobSynced failed: %v")

		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		require.NoError(t, err, "GetJobsToSync failed: %v")

		for _, j := range jobs {
			assert.NotEqual(t, j.ID, job.ID)
		}
	})

	t.Run("job with updated_at after synced_at is returned", func(t *testing.T) {

		pastTime := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		futureTime := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		h.setJobTimestamps(job.ID, sql.NullString{String: pastTime, Valid: true}, futureTime)

		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		require.NoError(t, err, "GetJobsToSync failed: %v")

		found := false
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "Expected job with updated_at > synced_at to be returned for sync")
	})

	t.Run("mixed format timestamps compare correctly", func(t *testing.T) {

		job2 := h.createCompletedJob("mixed-format-sha")

		h.setJobTimestamps(job2.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T14:30:00+02:00")

		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		require.NoError(t, err, "GetJobsToSync failed: %v")

		found := false
		for _, j := range jobs {
			if j.ID == job2.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "Expected job with mixed format timestamps (updated_at > synced_at) to be returned")

		h.setJobTimestamps(job2.ID,
			sql.NullString{String: "2024-06-15 20:00:00", Valid: true},
			"2024-06-15T10:30:00Z")

		jobs, err = h.db.GetJobsToSync(h.machineID, 10)
		require.NoError(t, err, "GetJobsToSync failed: %v")

		found = false
		for _, j := range jobs {
			if j.ID == job2.ID {
				found = true
				break
			}
		}
		assert.False(t, found, "Expected job with synced_at > updated_at to NOT be returned")
	})

	t.Run("mixed format timestamps work correctly in non-UTC timezone", func(t *testing.T) {

		t.Setenv("TZ", "America/New_York")

		hTZ := newSyncTestHelper(t)
		job3 := hTZ.createCompletedJob("tz-test-sha")

		hTZ.setJobTimestamps(job3.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T12:30:00Z")

		jobs, err := hTZ.db.GetJobsToSync(hTZ.machineID, 10)
		require.NoError(t, err, "GetJobsToSync failed: %v")

		found := false
		for _, j := range jobs {
			if j.ID == job3.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "Expected job with updated_at > synced_at to be returned regardless of local timezone")

		hTZ.setJobTimestamps(job3.ID,
			sql.NullString{String: "2024-06-15 14:00:00", Valid: true},
			"2024-06-15T12:30:00Z")

		jobs, err = hTZ.db.GetJobsToSync(hTZ.machineID, 10)
		require.NoError(t, err, "GetJobsToSync failed: %v")

		found = false
		for _, j := range jobs {
			if j.ID == job3.ID {
				found = true
				break
			}
		}
		assert.False(t, found, "Expected job with synced_at > updated_at to NOT be returned regardless of local timezone")
	})
}

func TestGetReviewsToSync_TimestampComparison(t *testing.T) {
	h := newSyncTestHelper(t)
	job := h.createCompletedJob("review-sync-sha")

	err := h.db.MarkJobSynced(job.ID)
	require.NoError(t, err, "MarkJobSynced failed: %v")

	review, err := h.db.GetReviewByJobID(job.ID)
	require.NoError(t, err, "GetReviewByJobID failed: %v")

	t.Run("review with null synced_at is returned", func(t *testing.T) {
		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		require.NoError(t, err, "GetReviewsToSync failed: %v")

		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		assert.True(t, found)
	})

	t.Run("review after MarkReviewSynced is not returned", func(t *testing.T) {
		err := h.db.MarkReviewSynced(review.ID)
		require.NoError(t, err, "MarkReviewSynced failed: %v")

		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		require.NoError(t, err, "GetReviewsToSync failed: %v")

		for _, r := range reviews {
			assert.NotEqual(t, r.ID, review.ID)
		}
	})

	t.Run("review with updated_at after synced_at is returned", func(t *testing.T) {

		pastTime := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		futureTime := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		h.setReviewTimestamps(review.ID, sql.NullString{String: pastTime, Valid: true}, futureTime)

		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		require.NoError(t, err, "GetReviewsToSync failed: %v")

		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "Expected review with updated_at > synced_at to be returned for sync")
	})

	t.Run("mixed format timestamps compare correctly", func(t *testing.T) {

		h.setReviewTimestamps(review.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T14:30:00+02:00")

		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		require.NoError(t, err, "GetReviewsToSync failed: %v")

		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "Expected review with mixed format timestamps (updated_at > synced_at) to be returned")

		h.setReviewTimestamps(review.ID,
			sql.NullString{String: "2024-06-15 20:00:00", Valid: true},
			"2024-06-15T10:30:00Z")

		reviews, err = h.db.GetReviewsToSync(h.machineID, 10)
		require.NoError(t, err, "GetReviewsToSync failed: %v")

		found = false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		assert.False(t, found, "Expected review with synced_at > updated_at to NOT be returned")
	})

	t.Run("mixed format timestamps work correctly in non-UTC timezone", func(t *testing.T) {

		t.Setenv("TZ", "America/New_York")

		hTZ := newSyncTestHelper(t)
		tzJob := hTZ.createCompletedJob("tz-review-sha")

		err := hTZ.db.MarkJobSynced(tzJob.ID)
		require.NoError(t, err, "MarkJobSynced failed: %v")

		tzReview, err := hTZ.db.GetReviewByJobID(tzJob.ID)
		require.NoError(t, err, "GetReviewByJobID failed: %v")

		hTZ.setReviewTimestamps(tzReview.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T12:30:00Z")

		reviews, err := hTZ.db.GetReviewsToSync(hTZ.machineID, 10)
		require.NoError(t, err, "GetReviewsToSync failed: %v")

		found := false
		for _, r := range reviews {
			if r.ID == tzReview.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "Expected review with updated_at > synced_at to be returned regardless of local timezone")

		hTZ.setReviewTimestamps(tzReview.ID,
			sql.NullString{String: "2024-06-15 14:00:00", Valid: true},
			"2024-06-15T12:30:00Z")

		reviews, err = hTZ.db.GetReviewsToSync(hTZ.machineID, 10)
		require.NoError(t, err, "GetReviewsToSync failed: %v")

		found = false
		for _, r := range reviews {
			if r.ID == tzReview.ID {
				found = true
				break
			}
		}
		assert.False(t, found, "Expected review with synced_at > updated_at to NOT be returned regardless of local timezone")
	})
}

func TestSessionID_SyncRoundTrip(t *testing.T) {

	src := newSyncTestHelper(t)

	job := src.createCompletedJob("session-sync-sha")
	_, err := src.db.Exec(
		`UPDATE review_jobs SET session_id = ? WHERE id = ?`,
		"agent-session-abc", job.ID)
	require.NoError(t, err, "set session_id: %v")

	exported, err := src.db.GetJobsToSync(src.machineID, 10)
	require.NoError(t, err, "GetJobsToSync: %v")

	var syncJob *SyncableJob
	for i := range exported {
		if exported[i].ID == job.ID {
			syncJob = &exported[i]
			break
		}
	}
	assert.NotNil(t, syncJob)
	assert.Equal(t, "agent-session-abc", syncJob.SessionID)

	dst := newSyncTestHelper(t)
	pulled := PulledJob{
		UUID:            syncJob.UUID,
		RepoIdentity:    syncJob.RepoIdentity,
		CommitSHA:       syncJob.CommitSHA,
		CommitAuthor:    syncJob.CommitAuthor,
		CommitSubject:   syncJob.CommitSubject,
		CommitTimestamp: syncJob.CommitTimestamp,
		GitRef:          syncJob.GitRef,
		SessionID:       syncJob.SessionID,
		Agent:           syncJob.Agent,
		Model:           syncJob.Model,
		Reasoning:       syncJob.Reasoning,
		JobType:         syncJob.JobType,
		ReviewType:      syncJob.ReviewType,
		PatchID:         syncJob.PatchID,
		Status:          syncJob.Status,
		Agentic:         syncJob.Agentic,
		EnqueuedAt:      syncJob.EnqueuedAt,
		StartedAt:       syncJob.StartedAt,
		FinishedAt:      syncJob.FinishedAt,
		Prompt:          syncJob.Prompt,
		DiffContent:     syncJob.DiffContent,
		Error:           syncJob.Error,
		SourceMachineID: syncJob.SourceMachineID,
		UpdatedAt:       syncJob.UpdatedAt,
	}
	if err := dst.db.UpsertPulledJob(pulled, dst.repo.ID, nil); err != nil {
		require.NoError(t, err, "UpsertPulledJob: %v")
	}

	var gotSessionID sql.NullString
	err = dst.db.QueryRow(
		`SELECT session_id FROM review_jobs WHERE uuid = ?`,
		syncJob.UUID).Scan(&gotSessionID)
	require.NoError(t, err, "query imported session_id: %v")

	assert.False(t, !gotSessionID.Valid || gotSessionID.String != "agent-session-abc")
}

func TestGetCommentsToSync_LegacyCommentsExcluded(t *testing.T) {

	h := newSyncTestHelper(t)
	job := h.createCompletedJob("legacy-resp-sha")

	commit, err := h.db.GetCommitBySHA("legacy-resp-sha")
	require.NoError(t, err, "GetCommitBySHA failed: %v")

	err = h.db.MarkJobSynced(job.ID)
	require.NoError(t, err, "MarkJobSynced failed: %v")

	jobResp, err := h.db.AddCommentToJob(job.ID, "human", "This is a job response")
	require.NoError(t, err, "AddCommentToJob failed: %v")

	result, err := h.db.Exec(`
		INSERT INTO responses (commit_id, responder, response, uuid, source_machine_id, created_at)
		VALUES (?, 'human', 'This is a legacy response', ?, ?, datetime('now'))
	`, commit.ID, GenerateUUID(), h.machineID)
	require.NoError(t, err, "Failed to insert legacy response: %v")

	legacyRespID, _ := result.LastInsertId()

	responses, err := h.db.GetCommentsToSync(h.machineID, 100)
	require.NoError(t, err, "GetCommentsToSync failed: %v")

	foundJobResp := false
	foundLegacyResp := false
	for _, r := range responses {
		if r.ID == jobResp.ID {
			foundJobResp = true
		}
		if r.ID == legacyRespID {
			foundLegacyResp = true
		}
	}

	assert.True(t, foundJobResp)
	assert.False(t, foundLegacyResp, "Expected legacy response (job_id IS NULL) to be EXCLUDED from sync")

}
