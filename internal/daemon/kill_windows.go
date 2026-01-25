//go:build windows

package daemon

import (
	"bytes"
	"os/exec"
	"strconv"
	"time"
)

// killProcess kills a process by PID on Windows.
// Returns true only if the process is confirmed dead.
func killProcess(pid int) bool {
	// Check if process exists before trying to kill
	if !processExists(pid) {
		return true // Already dead
	}

	// Use taskkill to terminate the process
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	_ = cmd.Run() // Ignore error - we'll verify with processExists

	// Wait for process to fully terminate
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		if !processExists(pid) {
			return true // Process is gone
		}
	}

	return false // Still running after repeated attempts
}

// processExists checks if a process with the given PID exists.
// Uses tasklist with CSV output which is locale-independent.
func processExists(pid int) bool {
	// tasklist /FI "PID eq N" /FO CSV /NH
	// - Returns exit code 0 whether or not process is found
	// - If found: outputs CSV line with process info including PID
	// - If not found: outputs empty or info message (no CSV data)
	// We check if output contains the PID as a CSV field
	pidStr := strconv.Itoa(pid)
	cmd := exec.Command("tasklist", "/FI", "PID eq "+pidStr, "/FO", "CSV", "/NH")
	output, err := cmd.Output()
	if err != nil {
		// tasklist failed - assume process might exist to be safe
		return true
	}

	// In CSV output, the PID appears as a quoted field: "1234"
	// Check if the output contains the quoted PID
	quotedPID := []byte("\"" + pidStr + "\"")
	return len(output) > 0 && bytes.Contains(output, quotedPID)
}
