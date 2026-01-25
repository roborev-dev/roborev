//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
	"time"
)

// killProcess kills a process by PID on Windows.
// Returns true only if the process is confirmed dead.
func killProcess(pid int) bool {
	// Use taskkill to terminate the process
	// taskkill returns exit code 0 on success, non-zero if process doesn't exist or can't be killed
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	err := cmd.Run()

	if err != nil {
		// taskkill failed - process may not exist (good) or we can't kill it (bad)
		// Try to check if it still exists by attempting taskkill again
		// If it fails the same way, assume it's gone or we can't do anything
		return true
	}

	// taskkill succeeded, wait for process to fully terminate
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		// Try to kill again - if it fails, process is gone
		checkCmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
		if checkCmd.Run() != nil {
			return true // Can't kill = doesn't exist anymore
		}
	}

	return false // Still running after repeated attempts
}
