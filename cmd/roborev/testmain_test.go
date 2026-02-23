package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

// TestMain isolates the entire test package from the real ~/.roborev directory
// and disables external I/O in newTuiModel (daemon detection, config loading,
// git subprocess spawning). Without skipExternalIO, the 200+ tests that call
// newTuiModel each spawn git subprocesses, exhausting macOS CI runner resources.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	skipExternalIO = true

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

	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	code := m.Run()

	// Hard barrier: fail if tests polluted production logs.
	if msg := barrier.Check(); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	return code
}
