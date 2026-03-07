package main

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func TestIsRoborevDaemonCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmdLine string
		want    bool
	}{
		{
			name:    "unix daemon run",
			cmdLine: "/usr/local/bin/roborev daemon run",
			want:    true,
		},
		{
			name:    "windows daemon run",
			cmdLine: `C:\Tools\roborev.exe daemon run --port 7373`,
			want:    true,
		},
		{
			name:    "quoted windows args daemon run",
			cmdLine: `"C:\Tools\roborev.exe" "daemon" "run" "--port" "7373"`,
			want:    true,
		},
		{
			name:    "plain flag value before run",
			cmdLine: "roborev daemon --profile prod run",
			want:    true,
		},
		{
			name:    "daemon status command",
			cmdLine: "/usr/local/bin/roborev daemon status",
			want:    false,
		},
		{
			name:    "daemon status with output run",
			cmdLine: "roborev daemon status --output run",
			want:    false,
		},
		{
			name:    "non roborev process",
			cmdLine: "python worker.py",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRoborevDaemonCommand(tt.cmdLine)
			if got != tt.want {
				t.Errorf("isRoborevDaemonCommand(%q)=%v, want %v", tt.cmdLine, got, tt.want)
			}
		})
	}
}

func TestNormalizeCommandLine(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "utf16 with null byte separators",
			raw:  "\xff\xferoborev\x00daemon\x00run\r\n",
			want: "roborev daemon run",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeCommandLine(tt.raw)
			if got != tt.want {
				t.Errorf("normalizeCommandLine(%q)=%q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseWmicOutputUTF16LE(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "wmic output with utf16-le BOM",
			raw: encodeUTF16LE(
				"CommandLine\r\nC:\\Tools\\roborev.exe daemon run --port 7373\r\n", true,
			),
			want: `C:\Tools\roborev.exe daemon run --port 7373`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWmicOutput(tt.raw)
			if got != tt.want {
				t.Errorf("parseWmicOutput()=%q, want %q", got, tt.want)
			}
			if !isRoborevDaemonCommand(got) {
				t.Errorf("expected parsed WMIC output to classify as daemon command, got %q", got)
			}
		})
	}
}

func TestNormalizeCommandLineBytesUTF16LENoBOM(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "utf16-le without BOM",
			raw:  encodeUTF16LE("roborev.exe daemon run --port 7373\r\n", false),
			want: "roborev.exe daemon run --port 7373",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeCommandLineBytes(tt.raw)
			if got != tt.want {
				t.Errorf("normalizeCommandLineBytes()=%q, want %q", got, tt.want)
			}
		})
	}
}

func encodeUTF16LE(s string, withBOM bool) []byte {
	u16 := utf16.Encode([]rune(s))
	offset := 0
	if withBOM {
		offset = 2
	}
	out := make([]byte, offset+len(u16)*2)
	if withBOM {
		out[0] = 0xff
		out[1] = 0xfe
	}
	for i, r := range u16 {
		binary.LittleEndian.PutUint16(out[offset+i*2:offset+i*2+2], r)
	}
	return out
}
