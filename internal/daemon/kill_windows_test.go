//go:build windows

package daemon

import (
	"strings"
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
			name:    "roborev daemon run returns isRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon run`,
			want:    processIsRoborev,
		},
		{
			name:    "roborev daemon run with flags after returns isRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon run --port 7373`,
			want:    processIsRoborev,
		},
		{
			name:    "roborev daemon run with flags between returns isRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon --verbose run`,
			want:    processIsRoborev,
		},
		{
			name:    "roborev daemon run with multiple flags between returns isRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon -v --config C:\roborev.toml run`,
			want:    processIsRoborev,
		},
		{
			name:    "roborev daemon status returns notRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon status`,
			want:    processNotRoborev,
		},
		{
			name:    "roborev daemon stop returns notRoborev",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon stop`,
			want:    processNotRoborev,
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
		// False positive prevention - "run" in flags
		{
			name:    "dry-run flag with daemon status",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon status --dry-run`,
			want:    processNotRoborev,
		},
		{
			name:    "run-once flag with daemon stop",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon stop --run-once`,
			want:    processNotRoborev,
		},
		// False positive prevention - "run" as flag value after another subcommand
		{
			name:    "run as flag value after status",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon status --output run`,
			want:    processNotRoborev,
		},
		{
			name:    "run as positional arg after status",
			cmdLine: `C:\Program Files\roborev\roborev.exe daemon status run`,
			want:    processNotRoborev,
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
	// "roborev daemon run" with NUL bytes between chars (simulating partial UTF-16)
	cmdWithNuls := "r\x00o\x00b\x00o\x00r\x00e\x00v\x00 \x00d\x00a\x00e\x00m\x00o\x00n\x00 \x00r\x00u\x00n\x00"

	// classifyCommandLine strips NUL bytes before matching
	result := classifyCommandLine(cmdWithNuls)

	// After stripping NULs, should match "roborev daemon run"
	if result != processIsRoborev {
		t.Errorf("classifyCommandLine should match after stripping NUL bytes, got %v", result)
	}
}

func TestWmicHeaderOnlyOutput(t *testing.T) {
	// Verify that WMIC output containing only the header is treated as empty.
	// This simulates the flow in getCommandLineWmic without spawning wmic.

	tests := []struct {
		name       string
		wmicOutput string
		wantEmpty  bool
	}{
		{
			name:       "header only",
			wmicOutput: "CommandLine\r\n",
			wantEmpty:  true,
		},
		{
			name:       "header with trailing whitespace",
			wmicOutput: "CommandLine  \r\n  \r\n",
			wantEmpty:  true,
		},
		{
			name:       "header with UTF-16LE BOM (after NUL removal)",
			wmicOutput: "\xff\xfeCommandLine\r\n",
			wantEmpty:  true,
		},
		{
			name:       "header with actual data",
			wmicOutput: "CommandLine\r\nroborev.exe daemon run --port 7373\r\n",
			wantEmpty:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate getCommandLineWmic logic
			result := normalizeCommandLine(tt.wmicOutput)
			lower := strings.ToLower(result)
			if strings.HasPrefix(lower, "commandline") {
				result = strings.TrimSpace(result[11:])
			}

			gotEmpty := result == ""
			if gotEmpty != tt.wantEmpty {
				t.Errorf("header extraction: got empty=%v, want empty=%v (result=%q)",
					gotEmpty, tt.wantEmpty, result)
			}
		})
	}
}

func TestNormalizeCommandLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple string unchanged",
			input: "roborev daemon run",
			want:  "roborev daemon run",
		},
		{
			name:  "strips leading/trailing whitespace",
			input: "  roborev daemon run  \n",
			want:  "roborev daemon run",
		},
		{
			name:  "strips NUL bytes",
			input: "r\x00o\x00b\x00o\x00r\x00e\x00v\x00",
			want:  "roborev",
		},
		{
			name:  "strips UTF-8 BOM",
			input: "\xef\xbb\xbfroborev daemon",
			want:  "roborev daemon",
		},
		{
			name:  "handles UTF-16LE style with NULs",
			input: "r\x00o\x00b\x00o\x00r\x00e\x00v\x00 \x00d\x00a\x00e\x00m\x00o\x00n\x00",
			want:  "roborev daemon",
		},
		{
			name:  "empty string stays empty",
			input: "",
			want:  "",
		},
		{
			name:  "only whitespace becomes empty",
			input: "   \t\n  ",
			want:  "",
		},
		{
			name:  "only NULs becomes empty",
			input: "\x00\x00\x00",
			want:  "",
		},
		{
			name:  "BOM with whitespace becomes empty",
			input: "\xef\xbb\xbf   ",
			want:  "",
		},
		{
			name:  "strips UTF-16LE BOM after NUL removal",
			input: "\xff\xferoborev daemon",
			want:  "roborev daemon",
		},
		{
			name:  "strips UTF-16BE BOM",
			input: "\xfe\xffroborev daemon",
			want:  "roborev daemon",
		},
		{
			name:  "handles real UTF-16LE with BOM",
			input: "\xff\x00\xfe\x00r\x00o\x00b\x00o\x00r\x00e\x00v\x00",
			want:  "roborev", // After NUL removal: \xff\xferoborev, then BOM stripped
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeCommandLine(tt.input)
			if got != tt.want {
				t.Errorf("normalizeCommandLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
