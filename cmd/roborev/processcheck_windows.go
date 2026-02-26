//go:build windows

package main

import (
	"bytes"
	"os/exec"
	"strconv"
)

// isPIDAliveForUpdateDefault returns true when pid exists.
// Uses tasklist CSV output which is locale-independent.
func isPIDAliveForUpdateDefault(pid int) bool {
	if pid <= 0 {
		return false
	}

	pidStr := strconv.Itoa(pid)
	cmd := exec.Command("tasklist", "/FI", "PID eq "+pidStr, "/FO", "CSV", "/NH")
	out, err := cmd.Output()
	if err != nil {
		// Be conservative on command failure.
		return true
	}

	quotedPID := []byte("\"" + pidStr + "\"")
	return len(out) > 0 && bytes.Contains(out, quotedPID)
}
