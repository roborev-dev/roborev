package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

var (
	// Polling intervals for waitForJob - exposed for testing
	pollStartInterval = 1 * time.Second
	pollMaxInterval   = 5 * time.Second

	// Update daemon restart controls - exposed for testing.
	updateRestartWaitTimeout  = 2 * time.Second
	updateRestartPollInterval = 200 * time.Millisecond
	getAnyRunningDaemon       = daemon.GetAnyRunningDaemon
	listAllRuntimes           = daemon.ListAllRuntimes
	isPIDAliveForUpdate       = isPIDAliveForUpdateDefault
	stopDaemonForUpdate       = stopDaemon
	killAllDaemonsForUpdate   = killAllDaemons
	startUpdatedDaemon        = func(binDir string) error {
		newBinary := filepath.Join(binDir, "roborev")
		if runtime.GOOS == "windows" {
			newBinary += ".exe"
		}
		startCmd := exec.Command(newBinary, "daemon", "run")
		startCmd.Env = filterGitEnv(os.Environ())
		return startCmd.Start()
	}

	// setupSignalHandler allows tests to mock signal handling
	setupSignalHandler = func() (chan os.Signal, func()) {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		if runtime.GOOS != "windows" {
			// SIGTERM is not available on Windows
			signal.Notify(sigCh, os.Signal(syscall.Signal(15))) // SIGTERM
		}
		return sigCh, func() { signal.Stop(sigCh) }
	}
)

// ErrDaemonNotRunning indicates no daemon runtime file was found
var ErrDaemonNotRunning = fmt.Errorf("daemon not running (no runtime file found)")

// ErrJobNotFound indicates a job ID was not found during polling
var ErrJobNotFound = fmt.Errorf("job not found")

// getDaemonAddr returns the daemon address from runtime file or default
func getDaemonAddr() string {
	if info, err := daemon.GetAnyRunningDaemon(); err == nil {
		return fmt.Sprintf("http://%s", info.Addr)
	}
	return serverAddr
}

// registerRepoError is a server-side error from the register endpoint
// (daemon reachable but returned non-200). Distinguished from connection
// errors so callers can report appropriately.
type registerRepoError struct {
	StatusCode int
	Body       string
}

func (e *registerRepoError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.StatusCode, e.Body)
}

// isTransportError returns true if err indicates a transport-level failure
// (connection refused, timeout, DNS resolution, etc.) where the daemon is
// likely not reachable. Returns false for malformed URLs, TLS config errors,
// and other non-transport url.Error cases that deserve explicit reporting.
func isTransportError(err error) bool {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return false
	}
	// Check if the underlying error is a net-level transport failure
	var opErr *net.OpError
	if errors.As(urlErr.Err, &opErr) {
		return true
	}
	// Also catch net.Error (timeout interface) that isn't wrapped in OpError
	var netErr net.Error
	return errors.As(urlErr.Err, &netErr)
}

// registerRepo tells the daemon to persist a repo to the DB so that the
// CI poller (and other components) can find it after a daemon restart.
func registerRepo(repoPath string) error {
	body, err := json.Marshal(map[string]string{"repo_path": repoPath})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(getDaemonAddr()+"/api/repos/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return err // connection error (*url.Error wrapping net.Error)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return &registerRepoError{StatusCode: resp.StatusCode, Body: string(msg)}
	}
	return nil
}

// ensureDaemon checks if daemon is running, starts it if not
// If daemon is running but has different version, restart it
func ensureDaemon() error {
	client := &http.Client{Timeout: 500 * time.Millisecond}

	// First check runtime files for any running daemon
	if info, err := daemon.GetAnyRunningDaemon(); err == nil {
		addr := fmt.Sprintf("http://%s/api/status", info.Addr)
		resp, err := client.Get(addr)
		if err == nil {
			defer resp.Body.Close()

			// Parse response to get actual daemon version
			var status struct {
				Version string `json:"version"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&status)

			// Fail closed: restart if decode fails, version empty, or mismatch
			if decodeErr != nil || status.Version == "" || status.Version != version.Version {
				if verbose {
					fmt.Printf("Daemon version mismatch or unreadable (daemon: %s, cli: %s), restarting...\n", status.Version, version.Version)
				}
				return restartDaemon()
			}

			serverAddr = fmt.Sprintf("http://%s", info.Addr)
			return nil
		}
	}

	// Try default address - also check version from response
	resp, err := client.Get(serverAddr + "/api/status")
	if err == nil {
		defer resp.Body.Close()
		var status struct {
			Version string `json:"version"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)

		// Fail closed: restart if decode fails, version empty, or mismatch
		if decodeErr != nil || status.Version == "" || status.Version != version.Version {
			if verbose {
				fmt.Printf("Daemon version mismatch or unreadable (daemon: %s, cli: %s), restarting...\n", status.Version, version.Version)
			}
			return restartDaemon()
		}
		return nil
	}

	// Start daemon in background
	return startDaemon()
}

func startDaemon() error {
	if verbose {
		fmt.Println("Starting daemon...")
	}

	// Use the current executable with "daemon run" subcommand
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find executable: %w", err)
	}
	if shouldRefuseAutoStartDaemon(exe) {
		return fmt.Errorf(
			"refusing to auto-start daemon from ephemeral binary (%s); "+
				"use the installed roborev binary instead",
			filepath.Base(exe),
		)
	}

	cmd := exec.Command(exe, "daemon", "run")
	cmd.Env = filterGitEnv(os.Environ())
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Wait for daemon to be ready and update serverAddr from runtime file
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for range 30 {
		time.Sleep(100 * time.Millisecond)
		if info, err := daemon.GetAnyRunningDaemon(); err == nil {
			addr := fmt.Sprintf("http://%s", info.Addr)
			resp, err := client.Get(addr + "/api/status")
			if err == nil {
				resp.Body.Close()
				serverAddr = addr
				return nil
			}
		}
	}

	return fmt.Errorf("daemon failed to start")
}

// stopDaemon stops any running daemons.
// Returns ErrDaemonNotRunning if no daemon runtime files are found.
func stopDaemon() error {
	runtimes, err := daemon.ListAllRuntimes()
	if err != nil {
		// Check if it's just a "not exist" type error
		if os.IsNotExist(err) {
			return ErrDaemonNotRunning
		}
		// Propagate other errors (permission, IO, etc.)
		return fmt.Errorf("failed to list daemon runtimes: %w", err)
	}
	if len(runtimes) == 0 {
		return ErrDaemonNotRunning
	}

	// Kill all found daemons, track failures
	var lastErr error
	for _, info := range runtimes {
		if !daemon.KillDaemon(info) {
			lastErr = fmt.Errorf("failed to kill daemon (pid %d)", info.PID)
		}
	}

	return lastErr
}

// killAllDaemons kills any roborev daemon processes that might be running
// This handles orphaned processes from old binaries or crashed restarts
func killAllDaemons() {
	if runtime.GOOS == "windows" {
		// On Windows, use wmic to find daemon processes by command line
		// and kill only those running "daemon run"
		_ = exec.Command("wmic", "process", "where",
			"commandline like '%roborev%daemon%run%'",
			"call", "terminate").Run()
	} else {
		// On Unix, use pkill to kill all roborev daemon processes
		// Use -f to match against full command line
		_ = exec.Command("pkill", "-f", "roborev daemon run").Run()
		time.Sleep(100 * time.Millisecond)
		// Force kill any remaining
		_ = exec.Command("pkill", "-9", "-f", "roborev daemon run").Run()
	}
	time.Sleep(200 * time.Millisecond)
}

// restartDaemon stops the running daemon and starts a new one
func restartDaemon() error {
	_ = stopDaemon() // Ignore error - killAllDaemons is the fallback
	// Also kill any orphaned daemon processes from old binaries
	killAllDaemons()

	// Checkpoint WAL to ensure clean state for new daemon
	// Retry a few times in case daemon hasn't fully released the DB
	if dbPath := storage.DefaultDBPath(); dbPath != "" {
		var lastErr error
		for range 3 {
			db, err := storage.Open(dbPath)
			if err != nil {
				lastErr = err
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				lastErr = err
				db.Close()
				time.Sleep(200 * time.Millisecond)
				continue
			}
			db.Close()
			lastErr = nil
			break
		}
		if lastErr != nil && verbose {
			fmt.Printf("Warning: WAL checkpoint failed: %v\n", lastErr)
		}
	}

	return startDaemon()
}
