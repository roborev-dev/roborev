//go:build integration

package daemon

import (
	"net/http"
	"sync/atomic"
	"testing"
)

func TestKillDaemonMakesHTTPForLoopback(t *testing.T) {
	// Create a test server that tracks if shutdown was called
	var shutdownCalled atomic.Bool
	var requestCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		shutdownCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		// Return 500 for status checks so KillDaemon exits quickly
		w.WriteHeader(http.StatusInternalServerError)
	})

	addr := startMockDaemon(t, mux.ServeHTTP)

	const dummyNonExistentPID = 999999

	info := &RuntimeInfo{
		PID:  dummyNonExistentPID,
		Addr: addr, // Loopback address from test server
	}

	// This should make HTTP request since address is loopback
	KillDaemon(info)

	if !shutdownCalled.Load() {
		t.Error("KillDaemon should make HTTP shutdown request to loopback addresses")
	}
	if requestCount.Load() == 0 {
		t.Error("KillDaemon should have made at least one HTTP request")
	}
}
