//go:build integration

package daemon

import (
	"context"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// MockDaemon represents a mock roborev daemon for testing
type MockDaemon struct {
	addr           string
	mux            *http.ServeMux
	shutdownCalled atomic.Bool
}

// startNewMockDaemon starts an httptest server and returns a MockDaemon.
func startNewMockDaemon(t *testing.T) *MockDaemon {
	t.Helper()
	addr, mux := startMockDaemon(t)
	return &MockDaemon{
		addr: addr,
		mux:  mux,
	}
}

func (m *MockDaemon) Addr() string {
	return m.addr
}

func (m *MockDaemon) NonExistentPID() int {
	return math.MaxInt32 // Guaranteed not to exist for typical OSes
}

func (m *MockDaemon) ExpectShutdown() {
	m.mux.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		m.shutdownCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})
}

func (m *MockDaemon) FailStatusChecks() {
	m.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Return 500 for status checks so KillDaemon exits quickly
		w.WriteHeader(http.StatusInternalServerError)
	})
}

func TestKillDaemonMakesHTTPForLoopback(t *testing.T) {
	mock := startNewMockDaemon(t)
	mock.ExpectShutdown()
	mock.FailStatusChecks()

	info := &RuntimeInfo{
		PID:  mock.NonExistentPID(),
		Addr: mock.Addr(), // Loopback address from test server
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// This should make HTTP request since address is loopback
	KillDaemon(ctx, info)

	if !mock.shutdownCalled.Load() {
		t.Error("Expected /api/shutdown to be called via loopback address")
	}
}

func TestKillDaemonTimeoutFallsBackToKill(t *testing.T) {
	mock := startNewMockDaemon(t)
	mock.ExpectShutdown()
	// Return 200 for status checks so IsDaemonAlive returns true (daemon didn't exit)
	mock.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	info := &RuntimeInfo{
		PID:  mock.NonExistentPID(),
		Addr: mock.Addr(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should attempt HTTP shutdown, time out during the loop, then fall back to killProcess
	result := KillDaemon(ctx, info)

	// Since mock PID is non-existent, killProcess returns true (process already dead)
	if !result {
		t.Errorf("Expected KillDaemon to return true after falling back to OS kill")
	}

	if !mock.shutdownCalled.Load() {
		t.Error("Expected /api/shutdown to be hit before falling back to OS kill")
	}
}

func TestKillDaemonBlocksUntilCancellation(t *testing.T) {
	mock := startNewMockDaemon(t)

	block := make(chan struct{})
	defer close(block)

	entered := make(chan struct{})

	mock.mux.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-block // Block until test finishes
		w.WriteHeader(http.StatusOK)
	})

	info := &RuntimeInfo{
		PID:  mock.NonExistentPID(),
		Addr: mock.Addr(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should try HTTP shutdown, which blocks, then context cancels, so it aborts HTTP and falls back to killProcess
	result := KillDaemon(ctx, info)

	// Since mock PID is non-existent, killProcess returns true (process already dead)
	if !result {
		t.Errorf("Expected KillDaemon to return true after falling back to OS kill")
	}

	select {
	case <-entered:
		// expected
	default:
		t.Error("Expected /api/shutdown handler to be entered before cancellation")
	}
}

func TestKillDaemonClientTimeoutFallsBackToKill(t *testing.T) {
	mock := startNewMockDaemon(t)

	block := make(chan struct{})
	defer close(block)
	entered := make(chan struct{})

	mock.mux.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-block // Block to trigger client timeout (2s)
	})

	info := &RuntimeInfo{
		PID:  mock.NonExistentPID(),
		Addr: mock.Addr(),
	}

	// Use a background context so the only timeout is the http.Client{Timeout: 2*time.Second}
	start := time.Now()
	result := KillDaemon(context.Background(), info)
	elapsed := time.Since(start)

	if !result {
		t.Errorf("Expected KillDaemon to return true after falling back to OS kill")
	}

	select {
	case <-entered:
	default:
		t.Error("Expected /api/shutdown handler to be entered before client timeout")
	}

	if elapsed < 2*time.Second {
		t.Errorf("Expected KillDaemon to wait for 2s client timeout, got %v", elapsed)
	}
}
