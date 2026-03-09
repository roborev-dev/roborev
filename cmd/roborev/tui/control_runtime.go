package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/roborev-dev/roborev/internal/config"
)

// TUIRuntimeInfo stores metadata about a running TUI instance for
// discoverability by external tools.
type TUIRuntimeInfo struct {
	PID          int      `json:"pid"`
	SocketPath   string   `json:"socket_path"`
	ServerAddr   string   `json:"server_addr"`
	RepoFilter   []string `json:"repo_filter,omitempty"`
	BranchFilter string   `json:"branch_filter,omitempty"`
}

// tuiRuntimePath returns the metadata file path for a given PID.
func tuiRuntimePath(pid int) string {
	return filepath.Join(
		config.DataDir(), fmt.Sprintf("tui.%d.json", pid),
	)
}

// WriteTUIRuntime writes the TUI runtime metadata file atomically.
func WriteTUIRuntime(info TUIRuntimeInfo) error {
	path := tuiRuntimePath(info.PID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, "tui.*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}

	success = true
	return nil
}

// RemoveTUIRuntime removes the runtime metadata and socket files
// for the current process.
func RemoveTUIRuntime(socketPath string) {
	os.Remove(tuiRuntimePath(os.Getpid()))
	// Socket file is already removed by the control listener cleanup,
	// but remove here as a safety net.
	if socketPath != "" {
		os.Remove(socketPath)
	}
}

// ListAllTUIRuntimes returns metadata for all discovered TUI
// runtime files.
func ListAllTUIRuntimes() ([]*TUIRuntimeInfo, error) {
	dataDir := config.DataDir()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var runtimes []*TUIRuntimeInfo
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "tui.") ||
			!strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dataDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var info TUIRuntimeInfo
		if err := json.Unmarshal(data, &info); err != nil {
			os.Remove(path)
			continue
		}
		if info.PID <= 0 || info.SocketPath == "" {
			os.Remove(path)
			continue
		}
		runtimes = append(runtimes, &info)
	}
	return runtimes, nil
}

// CleanupStaleTUIRuntimes removes socket and metadata files for
// TUI processes that are no longer running.
func CleanupStaleTUIRuntimes() int {
	runtimes, err := ListAllTUIRuntimes()
	if err != nil {
		return 0
	}

	cleaned := 0
	for _, info := range runtimes {
		if isProcessAlive(info.PID) {
			continue
		}
		os.Remove(info.SocketPath)
		os.Remove(tuiRuntimePath(info.PID))
		cleaned++
	}
	return cleaned
}

// isProcessAlive checks whether a process with the given PID exists.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without sending a real signal.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// defaultControlSocketPath returns the default socket path for the
// current process: {DataDir}/tui.{PID}.sock
func defaultControlSocketPath() string {
	return filepath.Join(
		config.DataDir(),
		fmt.Sprintf("tui.%d.sock", os.Getpid()),
	)
}
