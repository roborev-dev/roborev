package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeACPTestCommand(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write command %s: %v", name, err)
	}
}

func runACPWrapperCommand(t *testing.T, commandPath string, customEnv map[string]string, args ...string) (string, string, error) {
	t.Helper()

	cmd := exec.Command(commandPath, args...)

	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ROBOREV_ACP_") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	for k, v := range customEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestACPWrapperScripts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACP wrapper scripts are POSIX shell scripts")
	}

	_, filename, _, _ := runtime.Caller(0)
	scriptsDir := filepath.Join(filepath.Dir(filename), "..", "..", "scripts")
	basePath := os.Getenv("PATH")

	tests := []struct {
		name            string
		adapter         string
		overrideCommand string
		mockBinary      string
		mockScript      string
		args            []string
		wantStdout      string
		wantStderr      string
		wantErr         bool
	}{
		{
			name:            "ROBOREV_ACP_ADAPTER_COMMAND override takes precedence",
			adapter:         "gemini",
			overrideCommand: "override-acp",
			mockBinary:      "override-acp",
			mockScript:      "#!/bin/sh\necho ACP_OVERRIDE_OK\n",
			wantStdout:      "ACP_OVERRIDE_OK",
		},
		{
			name:       "Adapter dispatch invokes claude wrapper binary from PATH",
			adapter:    "claude",
			mockBinary: "claude-agent-acp",
			mockScript: "#!/bin/sh\necho ACP_CLAUDE_OK\n",
			wantStdout: "ACP_CLAUDE_OK",
		},
		{
			name:       "Adapter dispatch invokes codex wrapper binary from PATH",
			adapter:    "codex",
			mockBinary: "codex-acp",
			mockScript: "#!/bin/sh\necho ACP_CODEX_OK\n",
			wantStdout: "ACP_CODEX_OK",
		},
		{
			name:       "Default adapter dispatch uses codex when adapter env is unset",
			mockBinary: "codex-acp",
			mockScript: "#!/bin/sh\necho ACP_DEFAULT_CODEX_OK\n",
			wantStdout: "ACP_DEFAULT_CODEX_OK",
		},
		{
			name:       "Gemini wrapper appends --experimental-acp flag",
			adapter:    "gemini",
			mockBinary: "gemini",
			mockScript: "#!/bin/sh\necho GEMINI_ARGS:$@\n",
			args:       []string{"--test-arg"},
			wantStdout: "GEMINI_ARGS:--experimental-acp --test-arg",
		},
		{
			name:       "Unsupported adapter returns error",
			adapter:    "unsupported",
			wantStderr: "unsupported ACP adapter",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeBinDir := t.TempDir()

			env := map[string]string{
				"PATH": fakeBinDir + string(os.PathListSeparator) + basePath,
			}

			if tt.adapter != "" {
				env["ROBOREV_ACP_ADAPTER"] = tt.adapter
			}

			if tt.mockBinary != "" {
				writeACPTestCommand(t, fakeBinDir, tt.mockBinary, tt.mockScript)
			}

			if tt.overrideCommand != "" {
				env["ROBOREV_ACP_ADAPTER_COMMAND"] = filepath.Join(fakeBinDir, tt.overrideCommand)
			}

			stdout, stderr, err := runACPWrapperCommand(t, filepath.Join(scriptsDir, "acp-agent"), env, tt.args...)

			if (err != nil) != tt.wantErr {
				t.Fatalf("runACPWrapperCommand() error = %v, stderr = %q, wantErr %v", err, stderr, tt.wantErr)
			}

			if tt.wantStdout != "" && !strings.Contains(stdout, tt.wantStdout) {
				t.Errorf("stdout = %q, want to contain %q", stdout, tt.wantStdout)
			}

			if tt.wantStderr != "" && !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want to contain %q", stderr, tt.wantStderr)
			}
		})
	}
}
