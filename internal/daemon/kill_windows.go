//go:build windows

package daemon

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// isRoborevProcess checks if a process is a roborev daemon.
// This prevents killing unrelated processes if a PID was reused.
func isRoborevProcess(pid int) bool {
	// Use wmic to get the command line for the process
	pidStr := strconv.Itoa(pid)
	cmd := exec.Command("wmic", "process", "where", "ProcessId="+pidStr, "get", "commandline")
	output, err := cmd.Output()
	if err != nil {
		// Fall back to checking process name via tasklist
		cmd := exec.Command("tasklist", "/FI", "PID eq "+pidStr, "/FO", "CSV", "/NH")
		output, err := cmd.Output()
		if err != nil {
			return false // Can't determine, assume not ours
		}
		// CSV format: "Image Name","PID",...
		// Check if it's roborev.exe
		return bytes.Contains(bytes.ToLower(output), []byte("roborev"))
	}
	// Check if command line contains roborev and daemon
	cmdStr := strings.ToLower(string(output))
	return strings.Contains(cmdStr, "roborev") && strings.Contains(cmdStr, "daemon")
}

// killProcess kills a process by PID on Windows.
// Returns true only if the process is confirmed dead.
// Verifies the process is a roborev daemon before killing to prevent
// killing unrelated processes if the PID was reused.
func killProcess(pid int) bool {
	// Check if process exists before trying to kill
	if !processExists(pid) {
		return true // Already dead
	}

	// Verify this is actually a roborev daemon process before killing
	if !isRoborevProcess(pid) {
		// Not a roborev process - the original daemon is gone and PID was reused
		return true
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
