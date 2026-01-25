//go:build !windows

package daemon

import (
	"os"
	"syscall"
	"time"
)

// killProcess kills a process by PID on Unix systems
func killProcess(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return true // Process doesn't exist
	}

	// First try SIGTERM for graceful shutdown
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process already dead or we don't have permission
		return true
	}

	// Wait up to 2 seconds for graceful shutdown
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		// Check if process is still alive using signal 0
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return true // Process is dead
		}
	}

	// Still alive, use SIGKILL
	if err := process.Signal(syscall.SIGKILL); err != nil {
		return true // Already dead or no permission
	}

	// Wait a bit more for SIGKILL to take effect
	time.Sleep(200 * time.Millisecond)
	return true
}
