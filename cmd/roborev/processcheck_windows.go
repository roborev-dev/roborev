//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// isPIDAliveForUpdateDefault returns true when pid exists.
// Uses tasklist CSV output which is locale-independent.
func isPIDAliveForUpdateDefault(pid int) bool {
	if pid <= 0 {
		return false
	}

	pidStr := strconv.Itoa(pid)
	// Use an absolute path to avoid PATH/CWD binary hijacking.
	cmd := exec.Command(tasklistPath(), "/FI", "PID eq "+pidStr, "/FO", "CSV", "/NH")
	out, err := cmd.Output()
	if err != nil {
		// Be conservative on command failure.
		return true
	}

	quotedPID := []byte("\"" + pidStr + "\"")
	return len(out) > 0 && bytes.Contains(out, quotedPID)
}

func tasklistPath() string {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = os.Getenv("WINDIR")
	}
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	return filepath.Join(systemRoot, "System32", "tasklist.exe")
}
