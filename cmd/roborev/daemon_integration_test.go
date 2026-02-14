//go:build integration

package main

import (
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

	// Use temp directories for isolation
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	configPath := filepath.Join(tmpDir, "config.toml")

	// Isolate runtime dir to avoid writing to real ~/.roborev/daemon.json
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

	// Write minimal config
	if err := os.WriteFile(configPath, []byte(`max_workers = 1`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Create the daemon run command with custom flags
	// Use a high base port to avoid conflicts with production (7373).
	// FindAvailablePort will auto-increment if 17373 is busy.
	cmd := daemonRunCmd()
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dbPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify DB was created
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
	var info *daemon.RuntimeInfo
	myPID := os.Getpid()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runtimes, err := daemon.ListAllRuntimes()
		if err == nil {
			// Find the runtime for OUR daemon (matching our PID), not a stale one
			for _, rt := range runtimes {
				if rt.PID == myPID && daemon.IsDaemonAlive(rt.Addr) {
					info = rt
					break
				}
			}
			if info != nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if info == nil {
		// Provide more context for debugging CI failures
		runtimes, _ := daemon.ListAllRuntimes()
		t.Fatalf("daemon did not create runtime file or is not responding (myPID=%d, found %d runtimes)", myPID, len(runtimes))
	}

	// The daemon runs in a goroutine within this test process.
	// Use os.Interrupt to trigger the signal handler.
	proc, err := os.FindProcess(myPID)
	if err != nil {
		t.Fatalf("failed to find own process: %v", err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		t.Fatalf("failed to send interrupt signal: %v", err)
	}

	// Wait for daemon to exit (longer timeout for race detector)
	select {
	case <-errCh:
		// Daemon exited - good
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit within 10 second timeout")
	}
}
