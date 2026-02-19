//go:build !windows

package daemon

import "testing"

func TestIsRoborevDaemonCommand(t *testing.T) {
	matchCases := []struct {
		name    string
		cmdLine string
	}{
		{
			name:    "daemon run",
			cmdLine: "/usr/local/bin/roborev daemon run",
		},
		{
			name:    "daemon run with flags after",
			cmdLine: "/usr/local/bin/roborev daemon run --port 7373",
		},
		{
			name:    "daemon run with flags between",
			cmdLine: "/usr/local/bin/roborev daemon --verbose run",
		},
		{
			name:    "daemon run with multiple flags between",
			cmdLine: "/usr/local/bin/roborev daemon -v --config /etc/roborev.toml run",
		},
		{
			name:    "go run daemon",
			cmdLine: "go run ./cmd/roborev daemon run",
		},
		// Robustness tests
		{
			name:    "daemon run with extra spaces",
			cmdLine: "/usr/local/bin/roborev   daemon    run",
		},
		{
			name:    "daemon run with tabs",
			cmdLine: "/usr/local/bin/roborev\tdaemon\trun",
		},
	}

	noMatchCases := []struct {
		name    string
		cmdLine string
	}{
		{
			name:    "daemon status",
			cmdLine: "/usr/local/bin/roborev daemon status",
		},
		{
			name:    "daemon stop",
			cmdLine: "/usr/local/bin/roborev daemon stop",
		},
		{
			name:    "daemon logs",
			cmdLine: "/usr/local/bin/roborev daemon logs",
		},
		// Should NOT match - "run" in path (false positive prevention)
		{
			name:    "run in path with daemon status",
			cmdLine: "/run/user/1000/roborev daemon status",
		},
		{
			name:    "run in path with daemon stop",
			cmdLine: "/var/run/roborev daemon stop",
		},
		// Should NOT match - "run" in flags (false positive prevention)
		{
			name:    "dry-run flag with daemon status",
			cmdLine: "/usr/local/bin/roborev daemon status --dry-run",
		},
		{
			name:    "run-once flag with daemon stop",
			cmdLine: "/usr/local/bin/roborev daemon stop --run-once",
		},
		// Should NOT match - "run" as flag value after another subcommand
		{
			name:    "run as flag value after status",
			cmdLine: "/usr/local/bin/roborev daemon status --output run",
		},
		{
			name:    "run as positional arg after status",
			cmdLine: "/usr/local/bin/roborev daemon status run",
		},
		{
			name:    "run as flag value after stop",
			cmdLine: "/usr/local/bin/roborev daemon stop --format run",
		},
		// Should NOT match - other commands
		{
			name:    "review command",
			cmdLine: "/usr/local/bin/roborev review",
		},
		{
			name:    "unrelated process",
			cmdLine: "/usr/bin/vim",
		},
		{
			name:    "empty string",
			cmdLine: "",
		},
	}

	runTests := func(cases []struct{ name, cmdLine string }, want bool) {
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				got := isRoborevDaemonCommand(tt.cmdLine)
				if got != want {
					t.Errorf("isRoborevDaemonCommand(%q) = %v, want %v", tt.cmdLine, got, want)
				}
			})
		}
	}

	runTests(matchCases, true)
	runTests(noMatchCases, false)
}
