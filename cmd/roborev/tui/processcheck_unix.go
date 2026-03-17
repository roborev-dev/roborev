//go:build !windows

package tui

import (
	"errors"
	"os"
	"syscall"
)

// isProcessAlive checks whether a process with the given PID exists.
// Returns true when the process is running, even if owned by another
// user (EPERM). Only returns false when the process is confirmed
// gone (ESRCH or similar).
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to another user.
	return errors.Is(err, syscall.EPERM)
}
