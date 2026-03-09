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
)

func TestControlSocketPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	cleanup, err := startControlListener(
		socketPath, newTestProgramUnix(t),
	)
	if err != nil {
		t.Fatalf("startControlListener: %v", err)
	}
	t.Cleanup(cleanup)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Type()&os.ModeSocket == 0 {
		t.Errorf("expected socket type, got %s",
			info.Mode().Type())
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		t.Errorf("socket permissions %o allow group/other access",
			perm)
	}
}

func TestRemoveStaleSocket_IncompatibleSocketRefused(t *testing.T) {
	path := shortSocketPath(t, "dgram")
	// Create a DGRAM socket — dial with STREAM will fail with a
	// non-ECONNREFUSED error, which should NOT be treated as stale.
	fd, err := syscall.Socket(
		syscall.AF_UNIX, syscall.SOCK_DGRAM, 0,
	)
	if err != nil {
		t.Fatalf("create dgram socket fd: %v", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(
		fd, &syscall.SockaddrUnix{Name: path},
	); err != nil {
		t.Fatalf("bind dgram socket: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	err = removeStaleSocket(path)
	if err == nil {
		t.Fatal("expected error for incompatible socket, got nil")
	}
	// The socket must not have been removed.
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("incompatible socket was deleted")
	}
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
