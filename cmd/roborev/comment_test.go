package main

// Tests for the comment command

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func mockCommentHandler(t *testing.T, receivedJobID *int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/comment" && r.Method == "POST" {
			var req struct {
				JobID int64 `json:"job_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode request body: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			*receivedJobID = req.JobID
			w.WriteHeader(http.StatusCreated)
			if err := json.NewEncoder(w).Encode(storage.Response{ID: 1, JobID: receivedJobID}); err != nil {
				t.Logf("failed to encode response: %v", err)
			}
			return
		}
	}
}

func TestCommentJobFlag(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantJobID     int64
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name:      "forces job ID interpretation",
			args:      []string{"--job", "1234567", "-m", "test message"},
			wantJobID: 1234567,
			wantErr:   false,
		},
		{
			name:          "rejects non-numeric input",
			args:          []string{"--job", "abc123", "-m", "test"},
			wantErr:       true,
			wantErrSubstr: "--job requires numeric job ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedJobID int64

			if !tt.wantErr {
				daemonFromHandler(t, mockCommentHandler(t, &receivedJobID))
			}

			cmd := commentCmd()
			cmd.SetArgs(tt.args)
			// Silence usage output for expected errors to keep test output clean
			if tt.wantErr {
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
			}
			err := cmd.Execute()

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErrSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if receivedJobID != tt.wantJobID {
					t.Errorf("expected job_id %d, got %d", tt.wantJobID, receivedJobID)
				}
			}
		})
	}
}
