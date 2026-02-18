//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
)

func TestDaemonRunStartsAndShutdownsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping daemon integration test on Windows due to file locking differences")
	}

	dbPath, configPath := setupTestDaemon(t)

	// Create context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the daemon run command with custom flags
	// Use a high base port to avoid conflicts with production (7373).
	// FindAvailablePort will auto-increment if 17373 is busy.
	cmd := daemonRunCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{
		"--db", dbPath,
		"--config", configPath,
		"--addr", "127.0.0.1:17373",
	})

	// Run daemon in goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Execute()
	}()

	// Wait for daemon to start (check if DB file is created)
	if !waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(dbPath)
		return err == nil
	}) {
		t.Fatal("timed out waiting for database creation")
	}

	// Verify DB was created (redundant with waitFor success, but keeps original intent)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected database to be created")
	}

	// Check that daemon didn't exit early with an error
	select {
	case err := <-errCh:
		t.Fatalf("daemon exited unexpectedly: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Daemon is still running - good
	}

	// Wait for daemon to be fully started and responsive
	// The runtime file is written before ListenAndServe, so we need to verify
	// the HTTP server is actually accepting connections.
	// Use longer timeout for race detector which adds significant overhead.
	myPID := os.Getpid()

	if !waitFor(t, 10*time.Second, func() bool {
		runtimes, err := daemon.ListAllRuntimes()
		if err == nil {
			// Find the runtime for OUR daemon (matching our PID), not a stale one
			for _, rt := range runtimes {
				if rt.PID == myPID && daemon.IsDaemonAlive(rt.Addr) {
					return true
				}
			}
		}
		return false
	}) {
		// Provide more context for debugging CI failures
		runtimes, _ := daemon.ListAllRuntimes()
		t.Fatalf("daemon did not create runtime file or is not responding (myPID=%d, found %d runtimes)", myPID, len(runtimes))
	}

	// Trigger shutdown via context cancellation instead of sending OS signal
	cancel()

	// Wait for daemon to exit (longer timeout for race detector)
	select {
	case <-errCh:
		// Daemon exited - good
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit within 10 second timeout")
	}
}

func setupTestDaemon(t *testing.T) (string, string) {
	t.Helper()

	// Use temp directories for isolation
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	configPath := filepath.Join(tmpDir, "config.toml")

	// Isolate runtime dir to avoid writing to real ~/.roborev/daemon.json
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Write minimal config
	if err := os.WriteFile(configPath, []byte(`max_workers = 1`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return dbPath, configPath
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
