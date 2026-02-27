package storage

import (
	"database/sql"
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
		uuidMap := make(map[string]bool)
		for _, u := range uuids {
			uuidMap[u] = true
		}

		if !uuidMap[job1.UUID] {
			t.Errorf("Expected to find job1 UUID %s", job1.UUID)
		}
		if !uuidMap[job2.UUID] {
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

func TestGetJobsToSync_TimestampComparison(t *testing.T) {
	h := newSyncTestHelper(t)
	job := h.createCompletedJob("sync-test-sha")

	t.Run("job with null synced_at is returned", func(t *testing.T) {
		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with NULL synced_at to be returned for sync")
		}
	})

	t.Run("job after MarkJobSynced is not returned", func(t *testing.T) {
		err := h.db.MarkJobSynced(job.ID)
		if err != nil {
			t.Fatalf("MarkJobSynced failed: %v", err)
		}

		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		for _, j := range jobs {
			if j.ID == job.ID {
				t.Error("Expected synced job to NOT be returned")
			}
		}
	})

	t.Run("job with updated_at after synced_at is returned", func(t *testing.T) {
		// Update the job's updated_at to be after synced_at
		pastTime := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		futureTime := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		h.setJobTimestamps(job.ID, sql.NullString{String: pastTime, Valid: true}, futureTime)

		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with updated_at > synced_at to be returned for sync")
		}
	})

	t.Run("mixed format timestamps compare correctly", func(t *testing.T) {
		// Create a new job for this subtest
		job2 := h.createCompletedJob("mixed-format-sha")

		// Set synced_at in SQLite datetime format (legacy format) - 10:30 UTC
		// Set updated_at in RFC3339 with offset: 14:30+02:00 = 12:30 UTC (later than 10:30 UTC)
		h.setJobTimestamps(job2.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T14:30:00+02:00")

		jobs, err := h.db.GetJobsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job2.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with mixed format timestamps (updated_at > synced_at) to be returned")
		}

		// Now test the opposite: synced_at is later than updated_at
		// synced_at: 2024-06-15 20:00:00 (8pm UTC)
		// updated_at: 2024-06-15T10:30:00Z (10:30am UTC)
		h.setJobTimestamps(job2.ID,
			sql.NullString{String: "2024-06-15 20:00:00", Valid: true},
			"2024-06-15T10:30:00Z")

		jobs, err = h.db.GetJobsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		for _, j := range jobs {
			if j.ID == job2.ID {
				t.Error("Expected job with synced_at > updated_at to NOT be returned")
			}
		}
	})

	t.Run("mixed format timestamps work correctly in non-UTC timezone", func(t *testing.T) {
		// Set TZ to a non-UTC timezone BEFORE opening the DB to ensure
		// SQLite/Go uses the non-UTC timezone for localtime operations
		t.Setenv("TZ", "America/New_York")

		// Create a NEW helper which opens a new DB
		hTZ := newSyncTestHelper(t)
		job3 := hTZ.createCompletedJob("tz-test-sha")

		// synced_at: legacy format 10:30 (should be treated as UTC)
		// updated_at: 12:30 UTC (later than synced_at)
		hTZ.setJobTimestamps(job3.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T12:30:00Z")

		jobs, err := hTZ.db.GetJobsToSync(hTZ.machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == job3.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected job with updated_at > synced_at to be returned regardless of local timezone")
		}

		// synced_at: legacy format 14:00 (should be treated as UTC)
		// updated_at: 12:30 UTC (earlier than synced_at)
		hTZ.setJobTimestamps(job3.ID,
			sql.NullString{String: "2024-06-15 14:00:00", Valid: true},
			"2024-06-15T12:30:00Z")

		jobs, err = hTZ.db.GetJobsToSync(hTZ.machineID, 10)
		if err != nil {
			t.Fatalf("GetJobsToSync failed: %v", err)
		}
		for _, j := range jobs {
			if j.ID == job3.ID {
				t.Error("Expected job with synced_at > updated_at to NOT be returned regardless of local timezone")
			}
		}
	})
}

func TestGetReviewsToSync_TimestampComparison(t *testing.T) {
	h := newSyncTestHelper(t)
	job := h.createCompletedJob("review-sync-sha")

	// Mark job as synced (required before reviews can sync due to FK ordering)
	err := h.db.MarkJobSynced(job.ID)
	if err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}

	// Get the review ID
	review, err := h.db.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}

	t.Run("review with null synced_at is returned", func(t *testing.T) {
		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with NULL synced_at to be returned for sync")
		}
	})

	t.Run("review after MarkReviewSynced is not returned", func(t *testing.T) {
		err := h.db.MarkReviewSynced(review.ID)
		if err != nil {
			t.Fatalf("MarkReviewSynced failed: %v", err)
		}

		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		for _, r := range reviews {
			if r.ID == review.ID {
				t.Error("Expected synced review to NOT be returned")
			}
		}
	})

	t.Run("review with updated_at after synced_at is returned", func(t *testing.T) {
		// Update the review's updated_at to be after synced_at
		pastTime := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		futureTime := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		h.setReviewTimestamps(review.ID, sql.NullString{String: pastTime, Valid: true}, futureTime)

		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with updated_at > synced_at to be returned for sync")
		}
	})

	t.Run("mixed format timestamps compare correctly", func(t *testing.T) {
		// Set synced_at in SQLite datetime format (legacy format) - 10:30 UTC
		// Set updated_at in RFC3339 with offset: 14:30+02:00 = 12:30 UTC (later than 10:30 UTC)
		h.setReviewTimestamps(review.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T14:30:00+02:00")

		reviews, err := h.db.GetReviewsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == review.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with mixed format timestamps (updated_at > synced_at) to be returned")
		}

		// Now test the opposite: synced_at is later than updated_at
		h.setReviewTimestamps(review.ID,
			sql.NullString{String: "2024-06-15 20:00:00", Valid: true},
			"2024-06-15T10:30:00Z")

		reviews, err = h.db.GetReviewsToSync(h.machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		for _, r := range reviews {
			if r.ID == review.ID {
				t.Error("Expected review with synced_at > updated_at to NOT be returned")
			}
		}
	})

	t.Run("mixed format timestamps work correctly in non-UTC timezone", func(t *testing.T) {
		// Set TZ to a non-UTC timezone BEFORE opening the DB to ensure
		// SQLite/Go uses the non-UTC timezone for localtime operations
		t.Setenv("TZ", "America/New_York")

		// Create a NEW helper which opens a new DB
		hTZ := newSyncTestHelper(t)
		tzJob := hTZ.createCompletedJob("tz-review-sha")

		// Mark job as synced (required before reviews can sync due to FK ordering)
		err := hTZ.db.MarkJobSynced(tzJob.ID)
		if err != nil {
			t.Fatalf("MarkJobSynced failed: %v", err)
		}

		// Get the review ID
		tzReview, err := hTZ.db.GetReviewByJobID(tzJob.ID)
		if err != nil {
			t.Fatalf("GetReviewByJobID failed: %v", err)
		}

		// synced_at: legacy format 10:30 (should be treated as UTC)
		// updated_at: 12:30 UTC (later than synced_at)
		hTZ.setReviewTimestamps(tzReview.ID,
			sql.NullString{String: "2024-06-15 10:30:00", Valid: true},
			"2024-06-15T12:30:00Z")

		reviews, err := hTZ.db.GetReviewsToSync(hTZ.machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		found := false
		for _, r := range reviews {
			if r.ID == tzReview.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected review with updated_at > synced_at to be returned regardless of local timezone")
		}

		// synced_at: legacy format 14:00 (should be treated as UTC)
		// updated_at: 12:30 UTC (earlier than synced_at)
		hTZ.setReviewTimestamps(tzReview.ID,
			sql.NullString{String: "2024-06-15 14:00:00", Valid: true},
			"2024-06-15T12:30:00Z")

		reviews, err = hTZ.db.GetReviewsToSync(hTZ.machineID, 10)
		if err != nil {
			t.Fatalf("GetReviewsToSync failed: %v", err)
		}
		for _, r := range reviews {
			if r.ID == tzReview.ID {
				t.Error("Expected review with synced_at > updated_at to NOT be returned regardless of local timezone")
			}
		}
	})
}

func TestGetCommentsToSync_LegacyCommentsExcluded(t *testing.T) {
	// This test verifies that legacy responses with job_id IS NULL (tied only to commit_id)
	// are excluded from sync since they cannot be synced via job_uuid.
	h := newSyncTestHelper(t)
	job := h.createCompletedJob("legacy-resp-sha")

	// Need commit ID for the legacy response
	commit, err := h.db.GetCommitBySHA("legacy-resp-sha")
	if err != nil {
		t.Fatalf("GetCommitBySHA failed: %v", err)
	}

	// Mark job as synced (required before responses can sync due to FK ordering)
	err = h.db.MarkJobSynced(job.ID)
	if err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}

	jobResp, err := h.db.AddCommentToJob(job.ID, "human", "This is a job response")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}

	// Create a legacy commit-only response by directly inserting with job_id IS NULL
	result, err := h.db.Exec(`
		INSERT INTO responses (commit_id, responder, response, uuid, source_machine_id, created_at)
		VALUES (?, 'human', 'This is a legacy response', ?, ?, datetime('now'))
	`, commit.ID, GenerateUUID(), h.machineID)
	if err != nil {
		t.Fatalf("Failed to insert legacy response: %v", err)
	}
	legacyRespID, _ := result.LastInsertId()

	// Get responses to sync - should only include the job-based response
	responses, err := h.db.GetCommentsToSync(h.machineID, 100)
	if err != nil {
		t.Fatalf("GetCommentsToSync failed: %v", err)
	}

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

	if !foundJobResp {
		t.Error("Expected job-based response to be included in sync")
	}
	if foundLegacyResp {
		t.Error("Expected legacy response (job_id IS NULL) to be EXCLUDED from sync")
	}
}
