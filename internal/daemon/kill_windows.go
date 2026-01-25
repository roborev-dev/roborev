//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// killProcess kills a process by PID on Windows.
// Returns true only if the process is confirmed dead.
func killProcess(pid int) bool {
	// Check if process exists first
	if !isProcessRunning(pid) {
		return true // Already dead
	}

	// Use taskkill to terminate the process
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	_ = cmd.Run()

	// Wait and verify death
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		if !isProcessRunning(pid) {
			return true
		}
	}

	return false // Failed to kill
}

// isProcessRunning checks if a process with given PID exists on Windows
func isProcessRunning(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	// tasklist returns "INFO: No tasks are running..." if not found
	return !strings.Contains(string(output), "No tasks")
}
