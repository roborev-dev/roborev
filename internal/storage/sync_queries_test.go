package storage

import (
	"database/sql"
	"slices"
	"testing"
	"time"
)

func TestGetKnownJobUUIDs(t *testing.T) {
	h := newSyncTestHelper(t)

	t.Run("returns empty when no jobs exist", func(t *testing.T) {
		uuids, err := h.db.GetKnownJobUUIDs()
		if err != nil {
			t.Fatalf("GetKnownJobUUIDs failed: %v", err)
		}
		if len(uuids) != 0 {
			t.Errorf("Expected empty slice, got %d UUIDs", len(uuids))
		}
	})

	t.Run("returns UUIDs of jobs with UUIDs", func(t *testing.T) {
		// Create two jobs with UUIDs
		job1 := h.createPendingJob("abc123")
		job2 := h.createPendingJob("def456")

		uuids, err := h.db.GetKnownJobUUIDs()
		if err != nil {
			t.Fatalf("GetKnownJobUUIDs failed: %v", err)
		}

		if len(uuids) != 2 {
			t.Errorf("Expected 2 UUIDs, got %d", len(uuids))
		}

		// Verify the UUIDs are the ones we created
		if !slices.Contains(uuids, job1.UUID) {
			t.Errorf("Expected to find job1 UUID %s", job1.UUID)
		}
		if !slices.Contains(uuids, job2.UUID) {
			t.Errorf("Expected to find job2 UUID %s", job2.UUID)
		}
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
				if !got.IsZero() {
					t.Errorf("parseSQLiteTime(%q) = %v, want zero time", tt.input, got)
				}
				return
			}
			if got.IsZero() {
				t.Errorf("parseSQLiteTime(%q) returned zero time, want year %d", tt.input, tt.wantYear)
				return
			}
			if got.Year() != tt.wantYear {
				t.Errorf("parseSQLiteTime(%q).Year() = %d, want %d", tt.input, got.Year(), tt.wantYear)
			}
		})
	}
}

// testSyncTimestampComparison runs a standard set of subtests for timestamp comparison logic
func testSyncTimestampComparison(
	t *testing.T,
	setup func(h *syncTestHelper) (
		id int64,
		markSynced func() error,
		setTime func(id int64, syncedAt sql.NullString, updatedAt string),
		getSync func() (bool, error),
	),
) {
	t.Run("with null synced_at is returned", func(t *testing.T) {
		h := newSyncTestHelper(t)
		_, _, _, getSync := setup(h)

		found, err := getSync()
		if err != nil {
			t.Fatalf("getSync failed: %v", err)
		}
		if !found {
			t.Error("Expected item with NULL synced_at to be returned for sync")
		}
	})

	t.Run("after marked synced is not returned", func(t *testing.T) {
		h := newSyncTestHelper(t)
		_, markSynced, _, getSync := setup(h)

		err := markSynced()
		if err != nil {
			t.Fatalf("markSynced failed: %v", err)
		}

		found, err := getSync()
		if err != nil {
			t.Fatalf("getSync failed: %v", err)
		}
		if found {
			t.Error("Expected synced item to NOT be returned")
		}
	})

	tests := []struct {
		name      string
		syncedAt  sql.NullString
		updatedAt string
		wantFound bool
	}{
		{
			name:      "updated_at after synced_at is returned",
			syncedAt:  sql.NullString{String: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), Valid: true},
			updatedAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			wantFound: true,
		},
		{
			name:      "mixed format timestamps (updated_at > synced_at)",
			syncedAt:  sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			updatedAt: "2024-06-15T14:30:00+02:00",
			wantFound: true,
		},
		{
			name:      "mixed format timestamps (synced_at > updated_at)",
			syncedAt:  sql.NullString{String: "2024-06-15 20:00:00", Valid: true},
			updatedAt: "2024-06-15T10:30:00Z",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSyncTestHelper(t)
			id, _, setTime, getSync := setup(h)

			setTime(id, tt.syncedAt, tt.updatedAt)

			found, err := getSync()
			if err != nil {
				t.Fatalf("getSync failed: %v", err)
			}
			if found != tt.wantFound {
				t.Errorf("got found=%v, want %v", found, tt.wantFound)
			}
		})
	}

	t.Run("mixed format timestamps work correctly in non-UTC timezone", func(t *testing.T) {
		t.Setenv("TZ", "America/New_York")

		testsTZ := []struct {
			name      string
			syncedAt  sql.NullString
			updatedAt string
			wantFound bool
		}{
			{
				name:      "updated_at > synced_at regardless of local timezone",
				syncedAt:  sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
				updatedAt: "2024-06-15T12:30:00Z",
				wantFound: true,
			},
			{
				name:      "synced_at > updated_at regardless of local timezone",
				syncedAt:  sql.NullString{String: "2024-06-15 14:00:00", Valid: true},
				updatedAt: "2024-06-15T12:30:00Z",
				wantFound: false,
			},
		}

		for _, tt := range testsTZ {
			t.Run(tt.name, func(t *testing.T) {
				hTZ := newSyncTestHelper(t)
				id, _, setTime, getSync := setup(hTZ)

				setTime(id, tt.syncedAt, tt.updatedAt)

				found, err := getSync()
				if err != nil {
					t.Fatalf("getSync failed: %v", err)
				}
				if found != tt.wantFound {
					t.Errorf("got found=%v, want %v", found, tt.wantFound)
				}
			})
		}
	})
}

func TestGetJobsToSync_TimestampComparison(t *testing.T) {
	testSyncTimestampComparison(t, func(h *syncTestHelper) (int64, func() error, func(int64, sql.NullString, string), func() (bool, error)) {
		job := h.createCompletedJob("sync-test-sha")

		markSynced := func() error {
			return h.db.MarkJobSynced(job.ID)
		}

		setTime := func(id int64, s sql.NullString, u string) {
			h.setJobTimestamps(id, s, u)
		}

		getSync := func() (bool, error) {
			jobs, err := h.db.GetJobsToSync(h.machineID, 10)
			found := slices.ContainsFunc(jobs, func(j SyncableJob) bool { return j.ID == job.ID })
			return found, err
		}

		return job.ID, markSynced, setTime, getSync
	})
}

func TestGetReviewsToSync_TimestampComparison(t *testing.T) {
	testSyncTimestampComparison(t, func(h *syncTestHelper) (int64, func() error, func(int64, sql.NullString, string), func() (bool, error)) {
		job := h.createCompletedJob("review-sync-sha")

		// Mark job as synced (required before reviews can sync due to FK ordering)
		err := h.db.MarkJobSynced(job.ID)
		if err != nil {
			h.t.Fatalf("MarkJobSynced failed: %v", err)
		}

		review, err := h.db.GetReviewByJobID(job.ID)
		if err != nil {
			h.t.Fatalf("GetReviewByJobID failed: %v", err)
		}

		markSynced := func() error {
			return h.db.MarkReviewSynced(review.ID)
		}

		setTime := func(id int64, s sql.NullString, u string) {
			h.setReviewTimestamps(id, s, u)
		}

		getSync := func() (bool, error) {
			reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
			found := slices.ContainsFunc(reviews, func(r SyncableReview) bool { return r.ID == review.ID })
			return found, err
		}

		return review.ID, markSynced, setTime, getSync
	})
}

func TestSessionID_SyncRoundTrip(t *testing.T) {
	// Verify session_id survives the SQLite export → PulledJob → SQLite
	// import cycle. This catches column-order or placeholder mismatches
	// in GetJobsToSync and UpsertPulledJob that unit tests won't.
	src := newSyncTestHelper(t)

	// Create a completed job and set its session_id.
	job := src.createCompletedJob("session-sync-sha")
	_, err := src.db.Exec(
		`UPDATE review_jobs SET session_id = ? WHERE id = ?`,
		"agent-session-abc", job.ID)
	if err != nil {
		t.Fatalf("set session_id: %v", err)
	}

	// Export from source DB.
	exported, err := src.db.GetJobsToSync(src.machineID, 10)
	if err != nil {
		t.Fatalf("GetJobsToSync: %v", err)
	}
	var syncJob *SyncableJob
	for i := range exported {
		if exported[i].ID == job.ID {
			syncJob = &exported[i]
			break
		}
	}
	if syncJob == nil {
		t.Fatal("completed job not returned by GetJobsToSync")
	}
	if syncJob.SessionID != "agent-session-abc" {
		t.Fatalf("exported SessionID = %q, want %q",
			syncJob.SessionID, "agent-session-abc")
	}

	// Import into a fresh destination DB via UpsertPulledJob.
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
		t.Fatalf("UpsertPulledJob: %v", err)
	}

	// Verify session_id survived the round-trip.
	var gotSessionID sql.NullString
	err = dst.db.QueryRow(
		`SELECT session_id FROM review_jobs WHERE uuid = ?`,
		syncJob.UUID).Scan(&gotSessionID)
	if err != nil {
		t.Fatalf("query imported session_id: %v", err)
	}
	if !gotSessionID.Valid || gotSessionID.String != "agent-session-abc" {
		t.Fatalf("imported session_id = %v, want %q",
			gotSessionID, "agent-session-abc")
	}
}

func TestGetCommentsToSync_LegacyCommentsExcluded(t *testing.T) {
	h := newSyncTestHelper(t)
	job := h.createCompletedJob("legacy-resp-sha")

	commit, err := h.db.GetCommitBySHA("legacy-resp-sha")
	if err != nil {
		t.Fatalf("GetCommitBySHA failed: %v", err)
	}

	err = h.db.MarkJobSynced(job.ID)
	if err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}

	jobResp, err := h.db.AddCommentToJob(job.ID, "human", "This is a job response")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}

	result, err := h.db.Exec(`
		INSERT INTO responses (commit_id, responder, response, uuid, source_machine_id, created_at)
		VALUES (?, 'human', 'This is a legacy response', ?, ?, datetime('now'))
	`, commit.ID, GenerateUUID(), h.machineID)
	if err != nil {
		t.Fatalf("Failed to insert legacy response: %v", err)
	}
	legacyRespID, _ := result.LastInsertId()

	responses, err := h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}

	foundJobResp := slices.ContainsFunc(responses, func(r SyncableResponse) bool { return r.ID == jobResp.ID })
	foundLegacyResp := slices.ContainsFunc(responses, func(r SyncableResponse) bool { return r.ID == legacyRespID })

	if !foundJobResp {
		t.Error("Expected job-based response to be included in sync")
	}
	if foundLegacyResp {
		t.Error("Expected legacy response (job_id IS NULL) to be EXCLUDED from sync")
	}
}
