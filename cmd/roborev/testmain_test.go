package main

import (
	"os"
	"testing"
)

// TestMain isolates the entire test package from the real ~/.roborev directory.
// Without this, newTuiModel (and anything else that calls config.LoadGlobal)
// would read the developer's production config, making test outcomes dependent
// on local machine state.
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "roborev-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmpDir)

	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	os.Exit(m.Run())
}
