package main

import (
	"encoding/json"
	"net/http"
	"net/url"
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

	ts := builder.Build()
	defer ts.Close()

	baseURL, err := url.Parse(ts.URL + "/api/review")
	if err != nil {
		t.Fatalf("failed to parse base URL: %v", err)
	}

	tests := []struct {
		name       string
		jobIDQuery string
		wantJobID  int64
		wantOutput string
		wantStatus int
	}{
		{"Valid-10", "10", 10, "Review for Job 10", http.StatusOK},
		{"Valid-20", "20", 20, "Review for Job 20", http.StatusOK},
		{"Valid-30", "30", 30, "Review for Job 30", http.StatusOK},
		{"NotFound-40", "40", 0, "", http.StatusNotFound},
		{"Invalid-abc", "abc", 0, "", http.StatusBadRequest},
		{"PartialNumeric", "10abc", 0, "", http.StatusBadRequest},
		{"Missing", "", 0, "", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := *baseURL
			if tt.jobIDQuery != "" {
				q := u.Query()
				q.Set("job_id", tt.jobIDQuery)
				u.RawQuery = q.Encode()
			}
			resp, err := http.Get(u.String())
			if err != nil {
				t.Fatalf("failed to make request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, resp.StatusCode)
			}

			if tt.wantStatus != http.StatusOK {
				return
			}

			var review storage.Review
			if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if review.JobID != tt.wantJobID {
				t.Errorf("expected JobID %d, got %d", tt.wantJobID, review.JobID)
			}
			if review.Output != tt.wantOutput {
				t.Errorf("expected output %q, got %q", tt.wantOutput, review.Output)
			}
		})
	}
}
