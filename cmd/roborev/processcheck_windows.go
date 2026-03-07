//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// isPIDAliveDefault returns true when pid exists.
// Uses tasklist CSV output which is locale-independent.
func isPIDAliveDefault(pid int) bool {
	if pid <= 0 {
		return false
	}

	exists, err := processExists(pid)
	if err != nil {
		// Be conservative when existence cannot be determined.
		return true
	}
	if !exists {
		return false
	}

	// PID exists. If identity lookup confirms it's not roborev daemon
	// anymore, treat as exited (covers PID reuse after shutdown).
	switch identifyPID(pid) {
	case updatePIDNotRoborev:
		return false
	case updatePIDRoborev:
		return true
	default:
		// Unknown identity -> conservative (assume still alive).
		return true
	}
}

func processExists(pid int) (bool, error) {
	pidStr := strconv.Itoa(pid)
	// Use an absolute path to avoid PATH/CWD binary hijacking.
	cmd := exec.Command(tasklistPath(), "/FI", "PID eq "+pidStr, "/FO", "CSV", "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}

	quotedPID := []byte("\"" + pidStr + "\"")
	return len(out) > 0 && bytes.Contains(out, quotedPID), nil
}

func identifyPID(pid int) updatePIDIdentity {
	pidStr := strconv.Itoa(pid)

	// Try WMIC first for older Windows environments.
	if cmdLine := getCommandLineWmic(pidStr); cmdLine != "" {
		return classifyCommandLine(cmdLine)
	}
	// Fall back to PowerShell CIM query on newer systems.
	if cmdLine := getCommandLinePowerShell(pidStr); cmdLine != "" {
		return classifyCommandLine(cmdLine)
	}

	return updatePIDUnknown
}

func getCommandLineWmic(pidStr string) string {
	cmd := exec.Command(
		wmicPath(), "process", "where", "ProcessId="+pidStr, "get", "commandline",
	)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseWmicOutput(output)
}

func getCommandLinePowerShell(pidStr string) string {
	// Force UTF-8 output to avoid UTF-16LE capture issues.
	script := `[Console]::OutputEncoding=[Text.Encoding]::UTF8;` +
		`(Get-CimInstance Win32_Process -Filter "ProcessId=` + pidStr + `").CommandLine`
	cmd := exec.Command(
		powershellPath(), "-NoProfile", "-NonInteractive", "-Command", script,
	)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return normalizeCommandLineBytes(output)
}

func classifyCommandLine(cmdLine string) updatePIDIdentity {
	cmdLine = normalizeCommandLine(cmdLine)
	if cmdLine == "" {
		return updatePIDUnknown
	}
	if isRoborevDaemonCommand(cmdLine) {
		return updatePIDRoborev
	}
	return updatePIDNotRoborev
}

func systemRoot() string {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = os.Getenv("WINDIR")
	}
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	return systemRoot
}

func tasklistPath() string {
	return filepath.Join(systemRoot(), "System32", "tasklist.exe")
}

func wmicPath() string {
	return filepath.Join(systemRoot(), "System32", "wbem", "wmic.exe")
}

func powershellPath() string {
	return filepath.Join(
		systemRoot(),
		"System32", "WindowsPowerShell", "v1.0", "powershell.exe",
	)
}
