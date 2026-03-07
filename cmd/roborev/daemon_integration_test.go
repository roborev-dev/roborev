//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
)

func mockSignalHandler(t *testing.T) (chan os.Signal, *bool) {
	t.Helper()

	var cleanupCalled bool
	orig := setupSignalHandler
	t.Cleanup(func() { setupSignalHandler = orig })

	sigCh := make(chan os.Signal, 1)
	setupSignalHandler = func() (chan os.Signal, func()) {
		return sigCh, func() { cleanupCalled = true }
	}
	return sigCh, &cleanupCalled
}

func initDaemonTest(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping daemon integration test on Windows due to file locking differences")
	}
	return setupTestDaemon(t)
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

func waitForDaemonReady(t *testing.T, timeout time.Duration, pid int) bool {
	t.Helper()
	return waitFor(t, timeout, func() bool {
		runtimes, err := daemon.ListAllRuntimes()
		if err == nil {
			for _, rt := range runtimes {
				if rt.PID == pid && daemon.IsDaemonAlive(rt.Addr) {
					return true
				}
			}
		}
		return false
	})
}

func buildAndRunTestDaemon(t *testing.T, dbPath, configPath string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	tmpDir := filepath.Dir(dbPath) // Extract isolated temp dir for binary build

	// 1. Build a test binary
	binPath := filepath.Join(tmpDir, "roborev-test")
	// Use "." since we are in cmd/roborev package
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test binary: %v\n%s", err, out)
	}

	// 2. Start daemon in subprocess
	// Use an ephemeral port (0) to avoid conflicts
	cmd := exec.Command(binPath, "daemon", "run",
		"--db", dbPath,
		"--config", configPath,
		"--addr", "127.0.0.1:0",
	)
	// Important: Set ROBOREV_DATA_DIR so it writes runtime file to our tmpDir
	cmd.Env = append(os.Environ(), "ROBOREV_DATA_DIR="+tmpDir)

	// Capture output for debugging
	outputBuffer := new(bytes.Buffer)
	cmd.Stdout = outputBuffer
	cmd.Stderr = outputBuffer

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	return cmd, outputBuffer
}

func TestDaemonRunStartsAndShutdownsCleanly(t *testing.T) {
	dbPath, configPath := initDaemonTest(t)

	// Mock setupSignalHandler to verify cleanup
	_, cleanupCalled := mockSignalHandler(t)

	// Create context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the daemon run command with custom flags
	// Use an ephemeral port (0) to avoid conflicts with production.
	cmd := daemonRunCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{
		"--db", dbPath,
		"--config", configPath,
		"--addr", "127.0.0.1:0",
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

	// Wait for daemon to be fully started and responsive
	// The runtime file is written before ListenAndServe, so we need to verify
	// the HTTP server is actually accepting connections.
	// Use longer timeout for race detector which adds significant overhead.
	myPID := os.Getpid()

	readyCh := make(chan bool)
	go func() {
		readyCh <- waitForDaemonReady(t, 10*time.Second, myPID)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("daemon exited prematurely during startup: %v", err)
	case ready := <-readyCh:
		if !ready {
			// Provide more context for debugging CI failures
			runtimes, _ := daemon.ListAllRuntimes()
			t.Fatalf("daemon did not create runtime file or is not responding (myPID=%d, found %d runtimes)", myPID, len(runtimes))
		}
	}

	// Trigger shutdown via context cancellation instead of sending OS signal
	cancel()

	// Wait for daemon to exit (longer timeout for race detector)
	select {
	case <-errCh:
		// Daemon exited - good
		if !*cleanupCalled {
			t.Error("expected signal.Stop (cleanup) to be called")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit within 10 second timeout")
	}
}

func TestDaemonShutdownBySignal(t *testing.T) {
	dbPath, configPath := initDaemonTest(t)
	tmpDir := filepath.Dir(dbPath) // Extract isolated temp dir for binary build

	cmd, outputBuffer := buildAndRunTestDaemon(t, dbPath, configPath)

	// Ensure cleanup in case of failure
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	t.Cleanup(func() {
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	})

	// 3. Wait for daemon to be ready
	// The daemon writes daemon.<pid>.json
	daemonJSON := filepath.Join(tmpDir, fmt.Sprintf("daemon.%d.json", cmd.Process.Pid))
	if !waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(daemonJSON)
		return err == nil
	}) {
		// Cleanup handled by defer
		t.Fatalf("timed out waiting for daemon to start (%s not found). Output:\n%s", daemonJSON, outputBuffer.String())
	}

	// 4. Send SIGINT
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("failed to send SIGINT: %v", err)
	}

	// 5. Wait for exit
	select {
	case err := <-done:
		if err != nil {
			// Check if it's an exit status error. Ideally exit code 0.
			if exitErr, ok := err.(*exec.ExitError); ok {
				t.Fatalf("daemon exited with non-zero status: %v (code %d)", err, exitErr.ExitCode())
			}
			t.Fatalf("daemon wait returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for daemon to exit after SIGINT")
	}
}

func TestDaemonSignalCleanup(t *testing.T) {
	dbPath, configPath := initDaemonTest(t)

	// Verify that signal.Stop is called when shutdown
	// is triggered by a signal.
	sigCh, cleanupCalled := mockSignalHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := daemonRunCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{
		"--db", dbPath,
		"--config", configPath,
		"--addr", "127.0.0.1:0",
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Execute()
	}()

	// Wait for daemon to be fully started and responsive
	myPID := os.Getpid()
	readyCh := make(chan bool)
	go func() {
		readyCh <- waitForDaemonReady(t, 10*time.Second, myPID)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("daemon exited prematurely during startup: %v", err)
	case ready := <-readyCh:
		if !ready {
			t.Fatalf("daemon did not create runtime file or is not responding")
		}
	}

	// Trigger shutdown via signal.
	sigCh <- os.Interrupt

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("daemon exited with error: %v", err)
		}
		if !*cleanupCalled {
			t.Error(
				"expected signal.Stop (cleanup) to be" +
					" called after signal shutdown",
			)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit within timeout")
	}
}

func TestDaemonNonDefaultPortRegression(t *testing.T) {
	dbPath, configPath := initDaemonTest(t)
	tmpDir := filepath.Dir(dbPath)

	// Build and run the test daemon on an ephemeral port
	daemonCmd, outputBuffer := buildAndRunTestDaemon(t, dbPath, configPath)

	done := make(chan error, 1)
	go func() { done <- daemonCmd.Wait() }()
	t.Cleanup(func() {
		if daemonCmd.ProcessState == nil || !daemonCmd.ProcessState.Exited() {
			_ = daemonCmd.Process.Kill()
		}
	})

	// We must set ROBOREV_DATA_DIR in env so that daemon_lifecycle.go finds the runtime file
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	if !waitForDaemonReady(t, 10*time.Second, daemonCmd.Process.Pid) {
		t.Fatalf("timed out waiting for daemon to start. Output:\n%s", outputBuffer.String())
	}

	// Capture the expected ephemeral addr from the runtime file
	runtimes, err := daemon.ListAllRuntimes()
	if err != nil || len(runtimes) == 0 {
		t.Fatalf("failed to list runtimes: %v", err)
	}
	var expectedAddr string
	for _, rt := range runtimes {
		if rt.PID == daemonCmd.Process.Pid {
			expectedAddr = "http://" + rt.Addr
			break
		}
	}
	if expectedAddr == "" {
		t.Fatalf("could not find runtime file for test daemon (PID: %d)", daemonCmd.Process.Pid)
	}

	// We create a dummy git repo so commands like `run` or `analyze` can work
	repoDir := initTestGitRepo(t)

	// Also need to set working dir for the command
	originalWd, _ := os.Getwd()
	os.Chdir(repoDir)
	t.Cleanup(func() { os.Chdir(originalWd) })

	// Execute `run` command as a test. `run` should use `getDaemonAddr(cmd)` to talk to the non-default port daemon.
	rc := runCmd()

	// Explicitly initialize server flag to the default to mirror root command initialization
	rc.Flags().String("server", "http://127.0.0.1:7373", "daemon server address")

	// Pass an argument so it triggers the enqueue logic
	rc.SetArgs([]string{"--quiet", "hello from regression test"})

	var out bytes.Buffer
	rc.SetOut(&out)
	rc.SetErr(&out)

	actualAddr := getDaemonAddr(rc)
	if actualAddr != expectedAddr {
		t.Fatalf("expected command to use address %q, got %q", expectedAddr, actualAddr)
	}

	err = rc.Execute()

	// We expect the command to connect successfully. It might fail because there's no real agent,
	// but it should NOT fail with a connection refused error on the default port.
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") || isTransportError(err) {
			t.Fatalf("regression: command failed to connect to daemon, possibly using wrong port (default instead of ephemeral): %v", err)
		}
		// If it's another error (e.g., agent not found), that's fine for this test
	}
}
