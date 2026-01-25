//go:build windows

package daemon

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
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
func identifyProcess(pid int) processIdentity {
	// Use wmic to get the command line for the process
	pidStr := strconv.Itoa(pid)
	cmd := exec.Command("wmic", "process", "where", "ProcessId="+pidStr, "get", "commandline")
	output, err := cmd.Output()
	if err != nil {
		// wmic failed - we can't reliably determine if this is a daemon
		// tasklist only gives us image name, which could match any roborev.exe
		// (e.g., CLI commands, not just daemon). Return unknown to be safe.
		return processUnknown
	}
	// Check if command line contains roborev and daemon
	cmdStr := strings.ToLower(string(output))
	if strings.Contains(cmdStr, "roborev") && strings.Contains(cmdStr, "daemon") {
		return processIsRoborev
	}
	// We got command line but it's not roborev daemon
	return processNotRoborev
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
