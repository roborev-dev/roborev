package testutil

import (
	"os/exec"
	"testing"
)

func findBaselineCommand(t *testing.T) string {
	t.Helper()
	// Find a command that exists before isolation so we can
	// verify it becomes unreachable afterward. Try several
	// candidates to handle both Unix and Windows.
	candidates := []string{"ls", "cat", "echo", "cmd", "whoami"}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	t.Skip("no baseline command found in PATH; cannot verify isolation")
	return ""
}

func TestMockExecutableIsolated(t *testing.T) {
	baseline := findBaselineCommand(t)

	cleanup := MockExecutableIsolated(t, "my-mock-tool", 0)
	defer cleanup()

	if _, err := exec.LookPath("my-mock-tool"); err != nil {
		t.Errorf("expected to find my-mock-tool in PATH, got: %v", err)
	}

	if _, err := exec.LookPath(baseline); err == nil {
		t.Errorf("expected %q to be absent from isolated PATH, but it was found", baseline)
	}
}
