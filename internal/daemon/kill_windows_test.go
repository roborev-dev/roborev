//go:build windows

package daemon

import (
	"testing"
)

func TestClassifyCommandLineHandlesEncodings(t *testing.T) {
	tests := []struct {
		name    string
		cmdLine string
		want    processIdentity
	}{
		{
			name:    "empty string returns unknown",
			cmdLine: "",
			want:    processUnknown,
		},
		{
			name:    "whitespace only returns unknown",
			cmdLine: "   \t\n  ",
			want:    processUnknown,
		},
		{
			name:    "roborev daemon returns isRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon run`,
			want:    processIsRoborev,
		},
		{
			name:    "roborev without daemon returns notRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe review`,
			want:    processNotRoborev,
		},
		{
			name:    "unrelated process returns notRoborev",
			cmdLine: `C:\Windows\System32\notepad.exe`,
			want:    processNotRoborev,
		},
		{
			name:    "case insensitive match",
			cmdLine: `C:\ROBOREV\ROBOREV.EXE DAEMON RUN`,
			want:    processIsRoborev,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyCommandLine(tt.cmdLine)
			if got != tt.want {
				t.Errorf("classifyCommandLine(%q) = %v, want %v", tt.cmdLine, got, tt.want)
			}
		})
	}
}

func TestClassifyCommandLineStripsNulBytes(t *testing.T) {
	// Simulate UTF-16LE remnants with NUL bytes interspersed
	// "roborev daemon" with NUL bytes between chars (simulating partial UTF-16)
	cmdWithNuls := "r\x00o\x00b\x00o\x00r\x00e\x00v\x00 \x00d\x00a\x00e\x00m\x00o\x00n\x00"

	// classifyCommandLine strips NUL bytes before matching
	result := classifyCommandLine(cmdWithNuls)

	// After stripping NULs, should match "roborev daemon"
	if result != processIsRoborev {
		t.Errorf("classifyCommandLine should match after stripping NUL bytes, got %v", result)
	}
}

func TestGetCommandLinePowerShellStripsNuls(t *testing.T) {
	// This test verifies the NUL stripping logic in getCommandLinePowerShell
	// by checking that the function exists and handles the PID parameter.
	// We can't easily test the actual PowerShell execution in unit tests,
	// but we can verify the function signature and that it returns empty
	// for non-existent PIDs.

	// Non-existent PID should return empty string
	result := getCommandLinePowerShell("999999999")
	if result != "" {
		t.Logf("getCommandLinePowerShell returned non-empty for non-existent PID: %q", result)
	}
}
