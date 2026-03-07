package testutil

import (
	"path/filepath"
	"runtime"
	"strings"
)

// NoopCmd returns a platform-appropriate no-op shell command.
func NoopCmd() string {
	if runtime.GOOS == "windows" {
		return "Write-Output ok"
	}
	return "true"
}

// TouchCmd returns a platform-appropriate shell command to create a file.
// Uses forward slashes on Windows to avoid TOML/shell escaping issues.
func TouchCmd(path string) string {
	if runtime.GOOS == "windows" {
		// runHook uses PowerShell on Windows, so use PowerShell commands directly.
		// Use forward slashes — PowerShell resolves them correctly.
		escapedPath := strings.ReplaceAll(filepath.ToSlash(path), "'", "''")
		return "New-Item -ItemType File -Force -Path '" + escapedPath + "'"
	}
	escapedPath := strings.ReplaceAll(path, "'", "'\\''")
	return "touch '" + escapedPath + "'"
}

// PwdCmd returns a platform-appropriate shell command to write the cwd to a file.
func PwdCmd(path string) string {
	if runtime.GOOS == "windows" {
		escapedPath := strings.ReplaceAll(filepath.ToSlash(path), "'", "''")
		return "[IO.File]::WriteAllText('" + escapedPath + "', (Get-Location).Path)"
	}
	escapedPath := strings.ReplaceAll(path, "'", "'\\''")
	return "pwd > '" + escapedPath + "'"
}
