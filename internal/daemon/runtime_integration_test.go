//go:build integration

package daemon

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestKillDaemonMakesHTTPForLoopback(t *testing.T) {
	// Create a test server that tracks if shutdown was called
	var shutdownCalled atomic.Bool
	var requestCount atomic.Int32

	addr := startMockDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if strings.HasSuffix(r.URL.Path, "/api/shutdown") {
			shutdownCalled.Store(true)
			w.WriteHeader(http.StatusOK)
		} else {
			// Return 500 for status checks so KillDaemon exits quickly
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	info := &RuntimeInfo{
		PID:  999999, // Non-existent PID
		Addr: addr,   // Loopback address from test server
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
