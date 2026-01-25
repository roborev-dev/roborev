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
	// Use taskkill to terminate the process
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	output, err := cmd.CombinedOutput()

	if err != nil {
		// Check if the error indicates process doesn't exist
		// taskkill outputs "not found" or "does not exist" when PID is invalid
		outStr := strings.ToLower(string(output))
		if strings.Contains(outStr, "not found") ||
			strings.Contains(outStr, "does not exist") ||
			strings.Contains(outStr, "not running") {
			return true // Process doesn't exist - success
		}
		// Other errors (access denied, etc.) - process may still be running
		return false
	}

	// taskkill succeeded, wait for process to fully terminate
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		// Try to kill again - if it fails with "not found", process is gone
		checkCmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
		checkOutput, checkErr := checkCmd.CombinedOutput()
		if checkErr != nil {
			checkStr := strings.ToLower(string(checkOutput))
			if strings.Contains(checkStr, "not found") ||
				strings.Contains(checkStr, "does not exist") ||
				strings.Contains(checkStr, "not running") {
				return true // Process is gone
			}
		}
	}

	return false // Still running after repeated attempts
}
