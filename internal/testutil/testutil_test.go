package testutil

import (
	"os/exec"
	"testing"
)

func TestMockExecutableIsolated(t *testing.T) {
	// Create a mock executable "my-mock-tool"
	cleanup := MockExecutableIsolated(t, "my-mock-tool", 0)
	defer cleanup()

	// Verify "my-mock-tool" is found
	if _, err := exec.LookPath("my-mock-tool"); err != nil {
		t.Errorf("expected to find my-mock-tool in PATH, got error: %v", err)
	}

	// Verify "ls" (or any common tool) is NOT found
	// This assumes "ls" is in the original PATH but not in the isolated one.
	if _, err := exec.LookPath("ls"); err == nil {
		t.Error("expected NOT to find 'ls' in isolated PATH, but found it")
	}
}
