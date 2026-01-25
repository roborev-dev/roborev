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
var identifyProcess = identifyProcessImpl

func identifyProcessImpl(pid int) processIdentity {
	pidStr := strconv.Itoa(pid)

	// Try wmic first (available on most Windows versions)
	if cmdLine := getCommandLineWmic(pidStr); cmdLine != "" {
		return classifyCommandLine(cmdLine)
	}

	// Fall back to PowerShell Get-CimInstance (Win11 and newer without wmic)
	if cmdLine := getCommandLinePowerShell(pidStr); cmdLine != "" {
		return classifyCommandLine(cmdLine)
	}

	// Neither method worked - can't determine identity
	return processUnknown
}

// getCommandLineWmic tries to get process command line via wmic.
// Returns empty string on failure or if no command line data.
func getCommandLineWmic(pidStr string) string {
	cmd := exec.Command("wmic", "process", "where", "ProcessId="+pidStr, "get", "commandline")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	// wmic output has header line "CommandLine" followed by data
	// Trim and check for actual content beyond the header
	trimmed := strings.TrimSpace(string(output))
	// Remove the "CommandLine" header if present
	trimmed = strings.TrimPrefix(trimmed, "CommandLine")
	trimmed = strings.TrimSpace(trimmed)
	return trimmed
}

// getCommandLinePowerShell tries to get process command line via PowerShell.
// Returns empty string on failure or if no command line data.
func getCommandLinePowerShell(pidStr string) string {
	// Use Get-CimInstance which is the modern replacement for wmic
	// Force UTF-8 output to avoid UTF-16LE encoding issues when capturing stdout
	script := `[Console]::OutputEncoding=[Text.Encoding]::UTF8;` +
		`(Get-CimInstance Win32_Process -Filter "ProcessId=` + pidStr + `").CommandLine`
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Also strip any BOM or stray NUL bytes that might slip through
	result := strings.TrimSpace(string(output))
	result = strings.ReplaceAll(result, "\x00", "")
	return result
}

// classifyCommandLine determines process identity from command line string.
func classifyCommandLine(cmdLine string) processIdentity {
	// Strip any stray NUL bytes (can happen with encoding issues)
	cmdLine = strings.ReplaceAll(cmdLine, "\x00", "")
	cmdLine = strings.TrimSpace(cmdLine)
	if cmdLine == "" {
		// Empty command line - can't determine, treat as unknown
		return processUnknown
	}
	cmdLower := strings.ToLower(cmdLine)
	if strings.Contains(cmdLower, "roborev") && strings.Contains(cmdLower, "daemon") {
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
