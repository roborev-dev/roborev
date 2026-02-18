//go:build integration

package main

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
)

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	testutil.InitTestGitRepo(t, tmpDir)
	return tmpDir
}
