//go:build !windows

package daemon

import "testing"

func TestIsRoborevDaemonCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmdLine string
		want    bool
	}{
		// Match cases
		{
			name:    "daemon run",
			cmdLine: "/usr/local/bin/roborev daemon run",
			want:    true,
		},
		{
			name:    "daemon run with flags after",
			cmdLine: "/usr/local/bin/roborev daemon run --port 7373",
			want:    true,
		},
		{
			name:    "daemon run with flags between",
			cmdLine: "/usr/local/bin/roborev daemon --verbose run",
			want:    true,
		},
		{
			name:    "daemon run with multiple flags between",
			cmdLine: "/usr/local/bin/roborev daemon -v --config /etc/roborev.toml run",
			want:    true,
		},
		{
			name:    "go run daemon",
			cmdLine: "go run ./cmd/roborev daemon run",
			want:    true,
		},
		// Robustness tests
		{
			name:    "daemon run with extra spaces",
			cmdLine: "/usr/local/bin/roborev   daemon    run",
			want:    true,
		},
		{
			name:    "daemon run with tabs",
			cmdLine: "/usr/local/bin/roborev\tdaemon\trun",
			want:    true,
		},
		// No match cases
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
		// Should NOT match - "run" in path (false positive prevention)
		{
			name:    "run in path with daemon status",
			cmdLine: "/run/user/1000/roborev daemon status",
			want:    false,
		},
		{
			name:    "run in path with daemon stop",
			cmdLine: "/var/run/roborev daemon stop",
			want:    false,
		},
		// Should NOT match - "run" in flags (false positive prevention)
		{
			name:    "dry-run flag with daemon status",
			cmdLine: "/usr/local/bin/roborev daemon status --dry-run",
			want:    false,
		},
		{
			name:    "run-once flag with daemon stop",
			cmdLine: "/usr/local/bin/roborev daemon stop --run-once",
			want:    false,
		},
		// Should NOT match - "run" as flag value after another subcommand
		{
			name:    "run as flag value after status",
			cmdLine: "/usr/local/bin/roborev daemon status --output run",
			want:    false,
		},
		{
			name:    "run as positional arg after status",
			cmdLine: "/usr/local/bin/roborev daemon status run",
			want:    false,
		},
		{
			name:    "run as flag value after stop",
			cmdLine: "/usr/local/bin/roborev daemon stop --format run",
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
