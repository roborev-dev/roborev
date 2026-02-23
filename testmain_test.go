package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

// TestMain isolates the root e2e test package from production
// ~/.roborev. TestE2EEnqueueAndReview creates a daemon.NewServer
// which opens activity/error logs at config.DataDir().
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	barrier := testenv.NewProdLogBarrier(
		testenv.DefaultProdDataDir(),
	)

	tmpDir, err := os.MkdirTemp("", "roborev-e2e-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"failed to create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	code := m.Run()

	if msg := barrier.Check(); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	return code
}
