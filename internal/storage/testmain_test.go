package storage

import (
	"os"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

func TestMain(m *testing.M) {
	code := testenv.RunIsolatedMain(m)
	CleanupTemplate()
	os.Exit(code)
}
