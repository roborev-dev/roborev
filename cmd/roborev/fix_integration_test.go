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
		handlerFactory   func(*atomic.Int32, *atomic.Int32) http.HandlerFunc
		expectedChecks   int32
		expectedEnqueues int32
		minChecks        int32
	}{
		{
			name: "SkipsWhenJobAppearsAfterWait",
			handlerFactory: func(jobCheckCalls *atomic.Int32, enqueueCalls *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/jobs":
						n := jobCheckCalls.Add(1)
						if n == 1 {
							// First check: no jobs yet
							json.NewEncoder(w).Encode(map[string]interface{}{
								"jobs": []map[string]interface{}{},
							})
						} else {
							// Second check: hook has fired
							json.NewEncoder(w).Encode(map[string]interface{}{
								"jobs": []map[string]interface{}{{"id": 42}},
							})
						}
					case "/api/enqueue":
						enqueueCalls.Add(1)
						w.WriteHeader(http.StatusCreated)
						json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}
			},
			expectedChecks:   2,
			expectedEnqueues: 0,
		},
		{
			name: "EnqueuesWhenNoJobExists",
			handlerFactory: func(jobCheckCalls *atomic.Int32, enqueueCalls *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/jobs":
						jobCheckCalls.Add(1)
						json.NewEncoder(w).Encode(map[string]interface{}{
							"jobs": []map[string]interface{}{},
						})
					case "/api/enqueue":
						enqueueCalls.Add(1)
						w.WriteHeader(http.StatusCreated)
						json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}
			},
			minChecks:        1,
			expectedEnqueues: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := initTestGitRepo(t)

			var jobCheckCalls atomic.Int32
			var enqueueCalls atomic.Int32

			ts := httptest.NewServer(tt.handlerFactory(&jobCheckCalls, &enqueueCalls))
			defer ts.Close()

			err := enqueueIfNeeded(ts.URL, tmpDir, sha)
			if err != nil {
				t.Fatalf("enqueueIfNeeded: %v", err)
			}

			if tt.expectedChecks > 0 {
				if jobCheckCalls.Load() != tt.expectedChecks {
					t.Errorf("expected %d job checks, got %d", tt.expectedChecks, jobCheckCalls.Load())
				}
			} else if tt.minChecks > 0 {
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
