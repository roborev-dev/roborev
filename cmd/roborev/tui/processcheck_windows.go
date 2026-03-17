//go:build windows

package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// isProcessAlive checks whether a process with the given PID exists
// using tasklist, which works reliably on Windows (unlike signal 0).
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	pidStr := strconv.Itoa(pid)
	tasklistExe := filepath.Join(
		systemRoot(), "System32", "tasklist.exe",
	)
	cmd := exec.Command(
		tasklistExe,
		"/FI", "PID eq "+pidStr,
		"/FO", "CSV",
		"/NH",
	)
	out, err := cmd.Output()
	if err != nil {
		// Cannot determine — be conservative, assume alive.
		return true
	}
	quoted := []byte("\"" + pidStr + "\"")
	return len(out) > 0 && bytes.Contains(out, quoted)
}

func systemRoot() string {
	if sr := os.Getenv("SystemRoot"); sr != "" {
		return sr
	}
	if wd := os.Getenv("WINDIR"); wd != "" {
		return wd
	}
	return `C:\Windows`
}
