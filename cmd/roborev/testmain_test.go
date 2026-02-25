package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

// TestMain isolates the entire test package from the real ~/.roborev directory
// and disables external I/O in tests that call newTuiModel by passing
// WithExternalIODisabled(). This prevents the 200+ tests from spawning
// git subprocesses and exhausting macOS CI runner resources.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	// Snapshot prod log state BEFORE overriding ROBOREV_DATA_DIR.
	barrier := testenv.NewProdLogBarrier(
		testenv.DefaultProdDataDir(),
	)

	tmpDir, err := os.MkdirTemp("", "roborev-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	origEnv, origSet := os.LookupEnv("ROBOREV_DATA_DIR")
	defer func() {
		if origSet {
			os.Setenv("ROBOREV_DATA_DIR", origEnv)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	code := m.Run()

	// Hard barrier: fail if tests polluted production logs.
	if msg := barrier.Check(); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	return code
}
