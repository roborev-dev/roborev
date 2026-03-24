package agent

import (
	"context"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// expectedAgents is the single source of truth for registered agent names.
var expectedAgents = []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "kiro", "kilo", "droid", "test"}

// verifyAgentPassesFlag creates a mock command that echoes args, runs the agent's Review method,
// and validates that the output contains the expected flag and value.
func verifyAgentPassesFlag(t *testing.T, createAgent func(cmdPath string) Agent, expectedFlag, expectedValue string) {
	t.Helper()
	skipIfWindows(t)

	script := "#!/bin/sh\necho \"args: $@\"\n"
	cmdPath := writeTempCommand(t, script)
	agent := createAgent(cmdPath)

	result, err := agent.Review(context.Background(), t.TempDir(), "head", "test prompt", nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	if !strings.Contains(result, expectedFlag) {
		t.Errorf("expected flag %q in args, got: %q", expectedFlag, result)
	}
	if expectedValue != "" && !strings.Contains(result, expectedValue) {
		t.Errorf("expected value %q in args, got: %q", expectedValue, result)
	}
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}

// skipIfWindows skips the test on Windows with a message.
// Use for tests that rely on Unix shell scripts.
func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping test that requires Unix shell scripts")
	}
}

// withUnsafeAgents sets the global AllowUnsafeAgents flag and restores it on cleanup.
func withUnsafeAgents(t *testing.T, allow bool) {
	t.Helper()
	prev := AllowUnsafeAgents()
	SetAllowUnsafeAgents(allow)
	t.Cleanup(func() { SetAllowUnsafeAgents(prev) })
}

// assertContains fails the test if s does not contain substr.
func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected string to contain %q\nDocument content:\n%s", substr, s)
	}
}

// assertNotContains fails the test if s contains substr.
func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("expected %q to not contain %q", s, substr)
	}
}

// assertEqual fails the test if got != want.
func assertEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// FailingWriter always returns an error on Write.
type FailingWriter struct {
	Err error
}

func (w *FailingWriter) Write(p []byte) (int, error) {
	return 0, w.Err
}

// assertFileContent reads the file at path and verifies it matches expected content.
func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if got := strings.TrimSpace(string(content)); got != expected {
		t.Errorf("file %s content = %q, want %q", path, got, expected)
	}
}

// assertFileNotContains reads the file at path and verifies it does NOT contain the unexpected string.
func assertFileNotContains(t *testing.T, path, unexpected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if strings.Contains(string(content), unexpected) {
		t.Errorf("file %s contained leaked string: %q", path, unexpected)
	}
}
