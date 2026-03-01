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

func acpScriptsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "scripts")
}

func writeACPTestCommand(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write command %s: %v", name, err)
	}
}

func runACPWrapperCommand(t *testing.T, commandPath string, env map[string]string, args ...string) (string, string, error) {
	t.Helper()

	cmd := exec.Command(commandPath, args...)
	pathValue := os.Getenv("PATH")
	if customPath, ok := env["PATH"]; ok {
		pathValue = customPath
	}

	cmd.Env = []string{
		"PATH=" + pathValue,
	}
	if home := os.Getenv("HOME"); home != "" {
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
	if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
		cmd.Env = append(cmd.Env, "TMPDIR="+tmpDir)
	}

	for k, v := range env {
		if k == "PATH" {
			continue
		}
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

	scriptsDir := acpScriptsDir(t)
	basePath := os.Getenv("PATH")

	t.Run("ROBOREV_ACP_ADAPTER_COMMAND override takes precedence", func(t *testing.T) {
		fakeBinDir := t.TempDir()
		overrideCommand := filepath.Join(fakeBinDir, "override-acp")
		writeACPTestCommand(t, fakeBinDir, "override-acp", "#!/bin/sh\necho ACP_OVERRIDE_OK\n")

		stdout, stderr, err := runACPWrapperCommand(t, filepath.Join(scriptsDir, "acp-agent"), map[string]string{
			"ROBOREV_ACP_ADAPTER_COMMAND": overrideCommand,
			"ROBOREV_ACP_ADAPTER":         "gemini",
			"PATH":                        fakeBinDir + string(os.PathListSeparator) + basePath,
		})
		if err != nil {
			t.Fatalf("expected override command to succeed, got err=%v stderr=%q", err, stderr)
		}
		if !strings.Contains(stdout, "ACP_OVERRIDE_OK") {
			t.Fatalf("expected override marker in stdout, got: %q", stdout)
		}
	})

	t.Run("Adapter dispatch invokes claude wrapper binary from PATH", func(t *testing.T) {
		fakeBinDir := t.TempDir()
		writeACPTestCommand(t, fakeBinDir, "claude-agent-acp", "#!/bin/sh\necho ACP_CLAUDE_OK\n")

		stdout, stderr, err := runACPWrapperCommand(t, filepath.Join(scriptsDir, "acp-agent"), map[string]string{
			"ROBOREV_ACP_ADAPTER": "claude",
			"PATH":                fakeBinDir + string(os.PathListSeparator) + basePath,
		})
		if err != nil {
			t.Fatalf("expected claude adapter dispatch to succeed, got err=%v stderr=%q", err, stderr)
		}
		if !strings.Contains(stdout, "ACP_CLAUDE_OK") {
			t.Fatalf("expected claude marker in stdout, got: %q", stdout)
		}
	})

	t.Run("Adapter dispatch invokes codex wrapper binary from PATH", func(t *testing.T) {
		fakeBinDir := t.TempDir()
		writeACPTestCommand(t, fakeBinDir, "codex-acp", "#!/bin/sh\necho ACP_CODEX_OK\n")

		stdout, stderr, err := runACPWrapperCommand(t, filepath.Join(scriptsDir, "acp-agent"), map[string]string{
			"ROBOREV_ACP_ADAPTER": "codex",
			"PATH":                fakeBinDir + string(os.PathListSeparator) + basePath,
		})
		if err != nil {
			t.Fatalf("expected codex adapter dispatch to succeed, got err=%v stderr=%q", err, stderr)
		}
		if !strings.Contains(stdout, "ACP_CODEX_OK") {
			t.Fatalf("expected codex marker in stdout, got: %q", stdout)
		}
	})

	t.Run("Default adapter dispatch uses codex when adapter env is unset", func(t *testing.T) {
		fakeBinDir := t.TempDir()
		writeACPTestCommand(t, fakeBinDir, "codex-acp", "#!/bin/sh\necho ACP_DEFAULT_CODEX_OK\n")

		stdout, stderr, err := runACPWrapperCommand(t, filepath.Join(scriptsDir, "acp-agent"), map[string]string{
			"PATH": fakeBinDir + string(os.PathListSeparator) + basePath,
		})
		if err != nil {
			t.Fatalf("expected default adapter dispatch to succeed, got err=%v stderr=%q", err, stderr)
		}
		if !strings.Contains(stdout, "ACP_DEFAULT_CODEX_OK") {
			t.Fatalf("expected default codex marker in stdout, got: %q", stdout)
		}
	})

	t.Run("Gemini wrapper appends --experimental-acp flag", func(t *testing.T) {
		fakeBinDir := t.TempDir()
		writeACPTestCommand(t, fakeBinDir, "gemini", "#!/bin/sh\necho GEMINI_ARGS:$@\n")

		stdout, stderr, err := runACPWrapperCommand(t, filepath.Join(scriptsDir, "acp-agent"), map[string]string{
			"ROBOREV_ACP_ADAPTER": "gemini",
			"PATH":                fakeBinDir + string(os.PathListSeparator) + basePath,
		}, "--test-arg")
		if err != nil {
			t.Fatalf("expected gemini adapter dispatch to succeed, got err=%v stderr=%q", err, stderr)
		}
		if !strings.Contains(stdout, "GEMINI_ARGS:--experimental-acp --test-arg") {
			t.Fatalf("expected gemini experimental flag in args, got: %q", stdout)
		}
	})

	t.Run("Unsupported adapter returns error", func(t *testing.T) {
		stdout, stderr, err := runACPWrapperCommand(t, filepath.Join(scriptsDir, "acp-agent"), map[string]string{
			"ROBOREV_ACP_ADAPTER": "unsupported",
			"PATH":                basePath,
		})
		if err == nil {
			t.Fatalf("expected unsupported adapter to fail, stdout=%q stderr=%q", stdout, stderr)
		}
		if !strings.Contains(stderr, "unsupported ACP adapter") {
			t.Fatalf("expected unsupported adapter error message, got stderr=%q", stderr)
		}
	})
}
