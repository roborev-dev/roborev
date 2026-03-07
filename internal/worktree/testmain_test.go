package worktree

import (
	"os"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolatedMain(m))
}
