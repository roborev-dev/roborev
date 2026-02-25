package main

import (
	"os"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

// TestMain isolates the entire test package from the real ~/.roborev directory
// and disables external I/O in tests that call newTuiModel by passing
// WithExternalIODisabled(). This prevents the 200+ tests from spawning
// git subprocesses and exhausting macOS CI runner resources.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolatedMain(m))
}
