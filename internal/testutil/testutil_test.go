package testutil

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
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

	_, err := exec.LookPath("my-mock-tool")
	require.NoError(t, err, "expected to find my-mock-tool in PATH")

	_, err = exec.LookPath(baseline)
	require.Error(t, err, "expected %q to be absent from isolated PATH", baseline)
}
