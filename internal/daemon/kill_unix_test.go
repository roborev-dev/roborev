//go:build !windows

package daemon

import "testing"

func TestIsRoborevDaemonCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmdLine string
		want    bool
	}{
		// Should match - daemon run command
		{
			name:    "daemon run",
			cmdLine: "/usr/local/bin/roborev daemon run",
			want:    true,
		},
		{
			name:    "daemon run with flags",
			cmdLine: "/usr/local/bin/roborev daemon run --port 7373",
			want:    true,
		},
		{
			name:    "go run daemon",
			cmdLine: "go run ./cmd/roborev daemon run",
			want:    true,
		},

		// Should NOT match - other daemon subcommands
		{
			name:    "daemon status",
			cmdLine: "/usr/local/bin/roborev daemon status",
			want:    false,
		},
		{
			name:    "daemon stop",
			cmdLine: "/usr/local/bin/roborev daemon stop",
			want:    false,
		},
		{
			name:    "daemon logs",
			cmdLine: "/usr/local/bin/roborev daemon logs",
			want:    false,
		},

		// Should NOT match - other commands
		{
			name:    "review command",
			cmdLine: "/usr/local/bin/roborev review",
			want:    false,
		},
		{
			name:    "unrelated process",
			cmdLine: "/usr/bin/vim",
			want:    false,
		},
		{
			name:    "empty string",
			cmdLine: "",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRoborevDaemonCommand(tt.cmdLine)
			if got != tt.want {
				t.Errorf("isRoborevDaemonCommand(%q) = %v, want %v", tt.cmdLine, got, tt.want)
			}
		})
	}
}
