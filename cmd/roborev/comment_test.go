package main

// Tests for the comment command

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

func TestCommentJobFlag(t *testing.T) {
	t.Run("--job forces job ID interpretation", func(t *testing.T) {
		var receivedJobID int64
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/comment" && r.Method == "POST" {
				var req struct {
					JobID int64 `json:"job_id"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				receivedJobID = req.JobID
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(storage.Response{ID: 1, JobID: &receivedJobID})
				return
			}
		}))
		defer cleanup()

		// "1234567" could be a SHA, but --job forces job ID interpretation
		cmd := commentCmd()
		cmd.SetArgs([]string{"--job", "1234567", "-m", "test message"})
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if receivedJobID != 1234567 {
			t.Errorf("expected job_id 1234567, got %d", receivedJobID)
		}
	})

	t.Run("--job rejects non-numeric input", func(t *testing.T) {
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
		}))
		defer cleanup()

		cmd := commentCmd()
		cmd.SetArgs([]string{"--job", "abc123", "-m", "test"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for non-numeric --job value")
		}
		if !strings.Contains(err.Error(), "--job requires numeric job ID") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
