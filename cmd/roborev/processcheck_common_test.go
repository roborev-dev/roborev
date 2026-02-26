package main

import "testing"

func TestIsRoborevDaemonCommandForUpdate(t *testing.T) {
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
			got := isRoborevDaemonCommandForUpdate(tt.cmdLine)
			if got != tt.want {
				t.Fatalf("isRoborevDaemonCommandForUpdate(%q)=%v, want %v", tt.cmdLine, got, tt.want)
			}
		})
	}
}

func TestNormalizeCommandLineForUpdate(t *testing.T) {
	raw := "\xff\xferoborev\x00daemon\x00run\r\n"
	got := normalizeCommandLineForUpdate(raw)
	if got != "roborev daemon run" {
		t.Fatalf("normalizeCommandLineForUpdate(%q)=%q, want %q", raw, got, "roborev daemon run")
	}
}
