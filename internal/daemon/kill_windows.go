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
	// Normalize and remove the header
	result := normalizeCommandLine(string(output))
	// Remove the "CommandLine" header if present (case-insensitive)
	lower := strings.ToLower(result)
	if strings.HasPrefix(lower, "commandline") {
		result = strings.TrimSpace(result[11:]) // len("commandline") == 11
	}
	// If result is empty or just the header was present, return empty
	// This ensures header-only output is treated as "no data" (unknown)
	return result
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
	return normalizeCommandLine(string(output))
}

// normalizeCommandLine cleans up command line output from system tools.
// Strips BOMs, NUL bytes (from UTF-16LE encoding issues), and trims whitespace.
func normalizeCommandLine(s string) string {
	// Strip NUL bytes first (common with UTF-16LE encoding issues)
	s = strings.ReplaceAll(s, "\x00", "")
	// Strip UTF-8 BOM
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	// Strip UTF-16LE BOM (after NUL removal, this becomes \xff\xfe)
	s = strings.TrimPrefix(s, "\xff\xfe")
	// Strip UTF-16BE BOM
	s = strings.TrimPrefix(s, "\xfe\xff")
	return strings.TrimSpace(s)
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
	if isRoborevDaemonCommand(cmdLine) {
		return processIsRoborev
	}
	// We got command line but it's not roborev daemon
	return processNotRoborev
}

// isRoborevDaemonCommand checks if a command line is a roborev daemon process.
// Requires "daemon" followed by "run" as the first subcommand to distinguish from
// CLI commands like "roborev daemon status" or "roborev daemon status --output run".
// Case-insensitive for Windows compatibility.
func isRoborevDaemonCommand(cmdLine string) bool {
	cmdLower := strings.ToLower(cmdLine)
	// Must contain roborev somewhere (binary name or path)
	if !strings.Contains(cmdLower, "roborev") {
		return false
	}
	// Tokenize and look for "daemon" followed by "run" as first subcommand
	fields := strings.Fields(cmdLower)
	foundDaemon := false
	for _, field := range fields {
		if !foundDaemon {
			// Look for "daemon" token (or path ending in \daemon or /daemon)
			if field == "daemon" || strings.HasSuffix(field, "\\daemon") || strings.HasSuffix(field, "/daemon") {
				foundDaemon = true
			}
			continue
		}
		// Skip flags
		if strings.HasPrefix(field, "-") {
			continue
		}
		// Skip tokens that look like flag values (paths, numbers, key=value)
		if looksLikeFlagValue(field) {
			continue
		}
		// First subcommand-like token after "daemon" - must be "run"
		return field == "run"
	}
	return false
}

// looksLikeFlagValue returns true if the token looks like a flag value rather
// than a subcommand. This helps distinguish "daemon --config C:\foo run" from
// "daemon status --output run".
func looksLikeFlagValue(token string) bool {
	// Paths contain separators
	if strings.ContainsAny(token, "/\\") {
		return true
	}
	// Windows drive letters or URLs contain colons
	if strings.Contains(token, ":") {
		return true
	}
	// Key=value pairs
	if strings.Contains(token, "=") {
		return true
	}
	// Numbers (port numbers, timeouts, etc.)
	if len(token) > 0 && token[0] >= '0' && token[0] <= '9' {
		return true
	}
	// File extensions suggest paths
	if strings.Contains(token, ".") {
		return true
	}
	return false
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
