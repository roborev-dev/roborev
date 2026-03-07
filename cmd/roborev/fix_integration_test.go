//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type Job struct {
	ID int `json:"id"`
}

type JobResponse struct {
	Jobs []Job `json:"jobs"`
}

type EnqueueResponse struct {
	ID int `json:"id"`
}

type fixMockServer struct {
	*httptest.Server
	jobCheckCalls atomic.Int32
	enqueueCalls  atomic.Int32
}

func newFixMockServer(t *testing.T, jobResponses []JobResponse) *fixMockServer {
	m := &fixMockServer{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			n := m.jobCheckCalls.Add(1)
			if len(jobResponses) == 0 {
				t.Errorf("jobResponses must not be empty")
				http.Error(w, "jobResponses empty", http.StatusInternalServerError)
				return
			}
			// Return the response corresponding to the call sequence, or the last one
			idx := int(n - 1)
			if idx >= len(jobResponses) {
				idx = len(jobResponses) - 1
			}
			json.NewEncoder(w).Encode(jobResponses[idx])
		case "/api/enqueue":
			m.enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(EnqueueResponse{ID: 99})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	m.Server = httptest.NewServer(handler)
	t.Cleanup(m.Close)
	return m
}

func TestEnqueueIfNeeded(t *testing.T) {
	// Constants used across tests
	const sha = "abc123def456"

	tests := []struct {
		name             string
		jobResponses     []JobResponse
		expectedEnqueues int32
		validateChecks   func(t *testing.T, calls int32)
	}{
		{
			name: "SkipsWhenJobAppearsAfterWait",
			jobResponses: []JobResponse{
				{Jobs: []Job{}},         // First call
				{Jobs: []Job{{ID: 42}}}, // Second call
			},
			expectedEnqueues: 0,
			validateChecks: func(t *testing.T, calls int32) {
				if calls != 2 {
					t.Errorf("expected 2 job checks, got %d", calls)
				}
			},
		},
		{
			name: "EnqueuesWhenNoJobExists",
			jobResponses: []JobResponse{
				{Jobs: []Job{}},
			},
			expectedEnqueues: 1,
			validateChecks: func(t *testing.T, calls int32) {
				if calls < 1 {
					t.Errorf("expected at least 1 job check, got %d", calls)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := initTestGitRepo(t)
			ts := newFixMockServer(t, tt.jobResponses)

			err := enqueueIfNeeded(context.Background(), ts.URL, tmpDir, sha)
			if err != nil {
				t.Fatalf("enqueueIfNeeded: %v", err)
			}

			if tt.validateChecks != nil {
				tt.validateChecks(t, ts.jobCheckCalls.Load())
			}

			if ts.enqueueCalls.Load() != tt.expectedEnqueues {
				t.Errorf("expected %d enqueues, got %d", tt.expectedEnqueues, ts.enqueueCalls.Load())
			}
		})
	}
}
