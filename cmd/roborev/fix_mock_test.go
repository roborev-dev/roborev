package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestMockDaemonBuilderMultipleReviews(t *testing.T) {
	// This test verifies that multiple calls to WithReview for different job IDs
	// are correctly stored and retrieved by the mock server.
	builder := newMockDaemonBuilder(t).
		WithReview(10, "Review for Job 10").
		WithReview(20, "Review for Job 20").
		WithReview(30, "Review for Job 30")

	ts, cleanup := builder.Build()
	defer cleanup()

	tests := []struct {
		jobID      int64
		wantOutput string
		wantErr    bool
	}{
		{10, "Review for Job 10", false},
		{20, "Review for Job 20", false},
		{30, "Review for Job 30", false},
		{40, "", true}, // Not found
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("Job-%d", tt.jobID), func(t *testing.T) {
			resp, err := http.Get(fmt.Sprintf("%s/api/review?job_id=%d", ts.URL, tt.jobID))
			if err != nil {
				t.Fatalf("failed to make request: %v", err)
			}
			defer resp.Body.Close()

			if tt.wantErr {
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("expected status 404, got %d", resp.StatusCode)
				}
				return
			}

			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected status 200, got %d", resp.StatusCode)
				return
			}

			var review storage.Review
			if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if review.JobID != tt.jobID {
				t.Errorf("expected JobID %d, got %d", tt.jobID, review.JobID)
			}
			if review.Output != tt.wantOutput {
				t.Errorf("expected output %q, got %q", tt.wantOutput, review.Output)
			}
		})
	}
}
