package main

import (
	"os"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

// TestMain isolates the root e2e test package from production
// ~/.roborev. TestE2EEnqueueAndReview creates a daemon.NewServer
// which opens activity/error logs at config.DataDir().
func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolatedMain(m))
}
