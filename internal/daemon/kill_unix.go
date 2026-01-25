//go:build !windows

package daemon

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processIdentity represents the result of identifying a process.
type processIdentity int

const (
	processUnknown    processIdentity = iota // Can't determine identity
	processIsRoborev                         // Confirmed roborev daemon
	processNotRoborev                        // Confirmed NOT roborev daemon
)

// identifyProcess checks if a process is a roborev daemon.
// Returns processIsRoborev, processNotRoborev, or processUnknown.
// This prevents killing unrelated processes if a PID was reused.
var identifyProcess = identifyProcessImpl

func identifyProcessImpl(pid int) processIdentity {
	// Try reading /proc/<pid>/cmdline (Linux)
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err == nil {
		// cmdline uses null bytes as separators
		cmdStr := strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		if cmdStr == "" {
			// Empty cmdline (e.g., kernel thread or permission issue) - unknown
			return processUnknown
		}
		if isRoborevDaemonCommand(cmdStr) {
			return processIsRoborev
		}
		// We got cmdline but it's not roborev daemon
		return processNotRoborev
	}

	// Fall back to ps (macOS/BSD)
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=")
	output, err := cmd.Output()
	if err != nil {
		// Can't determine - could be permissions, missing ps, etc.
		return processUnknown
	}
	cmdStr := strings.TrimSpace(string(output))
	if cmdStr == "" {
		// Empty output - can't determine identity
		return processUnknown
	}
	if isRoborevDaemonCommand(cmdStr) {
		return processIsRoborev
	}
	// We got ps output but it's not roborev daemon
	return processNotRoborev
}

// isRoborevDaemonCommand checks if a command line is a roborev daemon process.
// Requires "roborev" and "daemon" and "run" to distinguish from CLI commands
// like "roborev daemon status" or "roborev daemon stop".
func isRoborevDaemonCommand(cmdStr string) bool {
	return strings.Contains(cmdStr, "roborev") &&
		strings.Contains(cmdStr, "daemon") &&
		strings.Contains(cmdStr, "run")
}

// killProcess kills a process by PID on Unix systems.
// Returns true only if the process is confirmed dead.
// Verifies the process is a roborev daemon before killing to prevent
// killing unrelated processes if the PID was reused.
func killProcess(pid int) bool {
	// os.FindProcess on Unix never returns an error, it always succeeds
	process, _ := os.FindProcess(pid)

	// Check if process is alive first using signal 0
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// EPERM means process exists but we can't signal it (different user)
		// In this case, treat as "alive but not ours to kill"
		if errors.Is(err, syscall.EPERM) {
			return false
		}
		// ESRCH or other errors mean process doesn't exist
		return true
	}

	// Verify this is actually a roborev daemon process before killing
	// This prevents killing unrelated processes if the PID was reused
	switch identifyProcess(pid) {
	case processNotRoborev:
		// Confirmed not a roborev process - PID was reused, daemon is gone
		return true
	case processUnknown:
		// Can't determine identity - be conservative, don't kill or clean up
		return false
	case processIsRoborev:
		// Confirmed roborev daemon - proceed with kill below
	}

	// First try SIGTERM for graceful shutdown
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.EPERM) {
			return false // Can't kill - not ours
		}
		// Check if it died between our check and signal
		if err := process.Signal(syscall.Signal(0)); err != nil && !errors.Is(err, syscall.EPERM) {
			return true
		}
		return false // Can't signal and still alive
	}

	// Wait up to 2 seconds for graceful shutdown
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := process.Signal(syscall.Signal(0)); err != nil {
			if errors.Is(err, syscall.EPERM) {
				return false // Still exists, just can't signal
			}
			return true // Process is dead
		}
	}

	// Still alive, use SIGKILL
	_ = process.Signal(syscall.SIGKILL)

	// Wait and verify death
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := process.Signal(syscall.Signal(0)); err != nil {
			if errors.Is(err, syscall.EPERM) {
				return false
			}
			return true // Process is dead
		}
	}

	return false // Failed to kill
}
