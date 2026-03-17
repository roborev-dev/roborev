//go:build !windows

package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlSocketPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	cleanup, err := startControlListener(
		socketPath, newTestProgramUnix(t),
	)
	require.NoError(t, err, "startControlListener")
	t.Cleanup(cleanup)

	info, err := os.Stat(socketPath)
	require.NoError(t, err, "stat socket")
	assert.NotZero(t, info.Mode().Type()&os.ModeSocket,
		"expected socket type, got %s", info.Mode().Type())
	perm := info.Mode().Perm()
	assert.Zero(t, perm&0077,
		"socket permissions %o allow group/other access", perm)
}

func TestControlSocketTightensExistingDir(t *testing.T) {
	// Use a short base path to stay within the Unix socket
	// path length limit (~104 bytes on macOS).
	socketDir, err := os.MkdirTemp("", "tui")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	// Simulate a pre-existing data directory created with 0755.
	require.NoError(t, os.Chmod(socketDir, 0755))
	socketPath := filepath.Join(socketDir, "t.sock")

	cleanup, err := startControlListener(
		socketPath, newTestProgramUnix(t),
	)
	require.NoError(t, err, "startControlListener")
	t.Cleanup(cleanup)

	di, err := os.Stat(socketDir)
	require.NoError(t, err, "stat socket dir")
	assert.Equal(t, os.FileMode(0700), di.Mode().Perm(),
		"socket directory should be tightened to 0700")
}

func TestRemoveStaleSocket_IncompatibleSocketRefused(t *testing.T) {
	path := shortSocketPath(t, "dgram")
	// Create a DGRAM socket -- dial with STREAM will fail with a
	// non-ECONNREFUSED error, which should NOT be treated as stale.
	fd, err := syscall.Socket(
		syscall.AF_UNIX, syscall.SOCK_DGRAM, 0,
	)
	require.NoError(t, err, "create dgram socket fd")
	defer syscall.Close(fd)
	require.NoError(t,
		syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}),
		"bind dgram socket")
	t.Cleanup(func() { os.Remove(path) })

	err = removeStaleSocket(path)
	require.Error(t, err,
		"expected error for incompatible socket")
	assert.FileExists(t, path,
		"incompatible socket should not be deleted")
}

// newTestProgramUnix creates a tea.Program for Unix-only tests.
func newTestProgramUnix(t *testing.T) *tea.Program {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{})
		},
	))
	t.Cleanup(ts.Close)
	m := newModel(ts.URL, withExternalIODisabled())
	p := tea.NewProgram(m, tea.WithoutRenderer())
	go func() { _, _ = p.Run() }()
	t.Cleanup(func() { p.Kill() })
	time.Sleep(100 * time.Millisecond)
	return p
}
