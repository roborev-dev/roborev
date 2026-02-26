package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIFetchReviewNotFound(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.fetchReview(999)
	msg := cmd()

	errMsg, ok := msg.(errMsg)
	if !ok {
		t.Fatalf("Expected errMsg for 404, got %T: %v", msg, msg)
	}
	if errMsg.Error() != "no review found" {
		t.Errorf("Expected 'no review found', got: %v", errMsg)
	}
}

func TestTUIFetchReviewServerError(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cmd := m.fetchReview(1)
	msg := cmd()

	errMsg, ok := msg.(errMsg)
	if !ok {
		t.Fatalf("Expected errMsg for 500, got %T: %v", msg, msg)
	}
	if errMsg.Error() != "fetch review: 500 Internal Server Error" {
		t.Errorf("Expected status in error, got: %v", errMsg)
	}
}

func TestTUIFetchReviewFallbackSHAResponses(t *testing.T) {
	// Test that when job_id responses are empty, TUI falls back to SHA-based responses
	requestedPaths := []string{}
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.String())

		if r.URL.Path == "/api/review" {
			// Return a review for a single commit (not a range or dirty)
			review := storage.Review{
				ID:     1,
				JobID:  42,
				Agent:  "test",
				Output: "No issues found.",
				Job: &storage.ReviewJob{
					ID:       42,
					GitRef:   "abc123def456", // Single commit SHA (not a range)
					RepoPath: "/test/repo",
				},
			}
			json.NewEncoder(w).Encode(review)
			return
		}

		if r.URL.Path == "/api/comments" {
			jobID := r.URL.Query().Get("job_id")
			sha := r.URL.Query().Get("sha")

			if jobID != "" {
				// Job ID query returns empty responses
				json.NewEncoder(w).Encode(map[string]any{
					"responses": []storage.Response{},
				})
				return
			}
			if sha != "" {
				// SHA fallback query returns legacy responses
				json.NewEncoder(w).Encode(map[string]any{
					"responses": []storage.Response{
						{ID: 1, Responder: "user", Response: "Legacy response from SHA lookup"},
					},
				})
				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.fetchReview(42)
	msg := cmd()

	reviewMsg, ok := msg.(reviewMsg)
	if !ok {
		t.Fatalf("Expected reviewMsg, got %T: %v", msg, msg)
	}

	// Should have fetched both job_id and sha responses
	foundJobIDRequest := false
	foundSHARequest := false
	for _, path := range requestedPaths {
		if strings.Contains(path, "job_id=42") {
			foundJobIDRequest = true
		}
		if strings.Contains(path, "sha=abc123def456") {
			foundSHARequest = true
		}
	}

	if !foundJobIDRequest {
		t.Error("Expected request for job_id responses")
	}
	if !foundSHARequest {
		t.Error("Expected fallback request for SHA responses when job_id returned empty")
	}

	// Should have the legacy response from SHA fallback
	if len(reviewMsg.responses) != 1 {
		t.Fatalf("Expected 1 response from SHA fallback, got %d", len(reviewMsg.responses))
	}
	if reviewMsg.responses[0].Response != "Legacy response from SHA lookup" {
		t.Errorf("Expected legacy response, got: %s", reviewMsg.responses[0].Response)
	}
}

func TestTUIFetchReviewNoFallbackForRangeReview(t *testing.T) {
	// Test that SHA fallback is NOT used for range reviews (abc..def format)
	requestedPaths := []string{}
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.String())

		if r.URL.Path == "/api/review" {
			// Return a review for a commit range (not a single commit)
			review := storage.Review{
				ID:     1,
				JobID:  42,
				Agent:  "test",
				Output: "No issues found.",
				Job: &storage.ReviewJob{
					ID:       42,
					GitRef:   "abc123..def456", // Range review
					RepoPath: "/test/repo",
				},
			}
			json.NewEncoder(w).Encode(review)
			return
		}

		if r.URL.Path == "/api/comments" {
			// Return empty responses for job_id
			json.NewEncoder(w).Encode(map[string]any{
				"responses": []storage.Response{},
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.fetchReview(42)
	msg := cmd()

	_, ok := msg.(reviewMsg)
	if !ok {
		t.Fatalf("Expected reviewMsg, got %T: %v", msg, msg)
	}

	// Should NOT have made a SHA fallback request for range review
	for _, path := range requestedPaths {
		if strings.Contains(path, "sha=") {
			t.Error("Should not make SHA fallback request for range review")
		}
	}
}
