//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEnqueueIfNeeded(t *testing.T) {
	// Constants used across tests
	const sha = "abc123def456"

	tests := []struct {
		name             string
		jobResponses     []any
		expectedChecks   int32
		expectedEnqueues int32
		minChecks        int32
		checkExact       bool
	}{
		{
			name: "SkipsWhenJobAppearsAfterWait",
			jobResponses: []any{
				map[string]any{"jobs": []any{}},                         // First call
				map[string]any{"jobs": []any{map[string]any{"id": 42}}}, // Second call
			},
			expectedChecks:   2,
			expectedEnqueues: 0,
			checkExact:       true,
		},
		{
			name: "EnqueuesWhenNoJobExists",
			jobResponses: []any{
				map[string]any{"jobs": []any{}},
			},
			minChecks:        1,
			expectedEnqueues: 1,
			checkExact:       false,
		},
	}

	tmpDir := initTestGitRepo(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var jobCheckCalls atomic.Int32
			var enqueueCalls atomic.Int32

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/jobs":
					n := jobCheckCalls.Add(1)
					if len(tt.jobResponses) == 0 {
						t.Errorf("jobResponses must not be empty")
						http.Error(w, "jobResponses empty", http.StatusInternalServerError)
						return
					}
					// Return the response corresponding to the call sequence, or the last one
					idx := int(n - 1)
					if idx >= len(tt.jobResponses) {
						idx = len(tt.jobResponses) - 1
					}
					json.NewEncoder(w).Encode(tt.jobResponses[idx])
				case "/api/enqueue":
					enqueueCalls.Add(1)
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]any{"id": 99})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			ts := httptest.NewServer(handler)
			defer ts.Close()

			err := enqueueIfNeeded(ts.URL, tmpDir, sha)
			if err != nil {
				t.Fatalf("enqueueIfNeeded: %v", err)
			}

			if tt.checkExact {
				if jobCheckCalls.Load() != tt.expectedChecks {
					t.Errorf("expected %d job checks, got %d", tt.expectedChecks, jobCheckCalls.Load())
				}
			} else {
				if jobCheckCalls.Load() < tt.minChecks {
					t.Errorf("expected at least %d job checks, got %d", tt.minChecks, jobCheckCalls.Load())
				}
			}

			if enqueueCalls.Load() != tt.expectedEnqueues {
				t.Errorf("expected %d enqueues, got %d", tt.expectedEnqueues, enqueueCalls.Load())
			}
		})
	}
}
