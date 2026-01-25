//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
	"time"
)

// killProcess kills a process by PID on Windows
func killProcess(pid int) bool {
	// Use taskkill to terminate the process
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	if err := cmd.Run(); err != nil {
		// Process may already be dead or we don't have permission
		// Either way, we tried
		return true
	}

	// Wait a bit for the process to terminate
	time.Sleep(500 * time.Millisecond)
	return true
}
