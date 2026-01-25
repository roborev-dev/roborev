//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// killProcess kills a process by PID on Unix systems.
// Returns true only if the process is confirmed dead.
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
