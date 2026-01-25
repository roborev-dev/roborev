//go:build !windows

package daemon

import (
	"os"
	"syscall"
	"time"
)

// killProcess kills a process by PID on Unix systems.
// Returns true only if the process is confirmed dead.
func killProcess(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return true // Process doesn't exist
	}

	// Check if process is alive first
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return true // Already dead
	}

	// First try SIGTERM for graceful shutdown
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Check if it died between our check and signal
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return true
		}
		return false // Can't signal and still alive
	}

	// Wait up to 2 seconds for graceful shutdown
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return true // Process is dead
		}
	}

	// Still alive, use SIGKILL
	_ = process.Signal(syscall.SIGKILL)

	// Wait and verify death
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return true // Process is dead
		}
	}

	return false // Failed to kill
}
