package main

import (
	"os"
	"testing"
)

// TestMain isolates the entire test package from the real ~/.roborev directory
// and disables external I/O in newTuiModel (daemon detection, config loading,
// git subprocess spawning). Without skipExternalIO, the 200+ tests that call
// newTuiModel each spawn git subprocesses, exhausting macOS CI runner resources.
func TestMain(m *testing.M) {
	skipExternalIO = true

	tmpDir, err := os.MkdirTemp("", "roborev-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}
