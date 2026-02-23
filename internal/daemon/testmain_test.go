package daemon

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates the entire daemon test package from the production
// ~/.roborev directory. Without this, NewServer creates activity/error
// logs at DefaultActivityLogPath() → ~/.roborev/activity.log, polluting
// the production log with test events and confusing running TUIs.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	tmpDir, err := os.MkdirTemp("", "roborev-daemon-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"failed to create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	return m.Run()
}
