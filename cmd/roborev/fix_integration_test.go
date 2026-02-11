//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEnqueueIfNeededSkipsWhenJobAppearsAfterWait(t *testing.T) {
	tmpDir := initTestGitRepo(t)
	sha := "abc123def456"

	var jobCheckCalls atomic.Int32
	var enqueueCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(ts.URL, tmpDir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if jobCheckCalls.Load() != 2 {
		t.Errorf("expected 2 job checks, got %d", jobCheckCalls.Load())
	}
	if enqueueCalls.Load() != 0 {
		t.Error("should not enqueue when job appears on second check")
	}
}

func TestEnqueueIfNeededEnqueuesWhenNoJobExists(t *testing.T) {
	tmpDir := initTestGitRepo(t)
	sha := "abc123def456"

	var enqueueCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jobs": []map[string]interface{}{},
			})
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(ts.URL, tmpDir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if enqueueCalls.Load() != 1 {
		t.Errorf("should have enqueued exactly once, got %d", enqueueCalls.Load())
	}
}
