package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

// RuntimeInfo stores daemon runtime state
type RuntimeInfo struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
	Version string `json:"version"`
}

// RuntimePath returns the path to the runtime info file for the current process
func RuntimePath() string {
	return RuntimePathForPID(os.Getpid())
}

// RuntimePathForPID returns the path to the runtime info file for a specific PID
func RuntimePathForPID(pid int) string {
	return filepath.Join(config.DataDir(), fmt.Sprintf("daemon.%d.json", pid))
}

// LegacyRuntimePath returns the old daemon.json path for migration
func LegacyRuntimePath() string {
	return filepath.Join(config.DataDir(), "daemon.json")
}

// WriteRuntime saves the daemon runtime info
func WriteRuntime(addr string, port int, version string) error {
	info := RuntimeInfo{
		PID:     os.Getpid(),
		Addr:    addr,
		Port:    port,
		Version: version,
	}

	path := RuntimePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// ReadRuntime reads the daemon runtime info for the current process
func ReadRuntime() (*RuntimeInfo, error) {
	return ReadRuntimeForPID(os.Getpid())
}

// ReadRuntimeForPID reads the daemon runtime info for a specific PID
func ReadRuntimeForPID(pid int) (*RuntimeInfo, error) {
	data, err := os.ReadFile(RuntimePathForPID(pid))
	if err != nil {
		return nil, err
	}

	var info RuntimeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	return &info, nil
}

// RemoveRuntime removes the runtime info file for the current process
func RemoveRuntime() {
	os.Remove(RuntimePath())
}

// RemoveRuntimeForPID removes the runtime info file for a specific PID
func RemoveRuntimeForPID(pid int) {
	os.Remove(RuntimePathForPID(pid))
}

// ListAllRuntimes returns info for all daemon runtime files found
func ListAllRuntimes() ([]*RuntimeInfo, error) {
	dataDir := config.DataDir()
	pattern := filepath.Join(dataDir, "daemon.*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	// Also check for legacy daemon.json
	legacyPath := LegacyRuntimePath()
	if _, err := os.Stat(legacyPath); err == nil {
		matches = append(matches, legacyPath)
	}

	var runtimes []*RuntimeInfo
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var info RuntimeInfo
		if err := json.Unmarshal(data, &info); err != nil {
			// Corrupted file - remove it
			os.Remove(path)
			continue
		}
		runtimes = append(runtimes, &info)
	}
	return runtimes, nil
}

// GetAnyRunningDaemon returns info about any running daemon, preferring responsive ones
func GetAnyRunningDaemon() (*RuntimeInfo, error) {
	runtimes, err := ListAllRuntimes()
	if err != nil {
		return nil, err
	}

	// First, try to find a responsive daemon
	for _, info := range runtimes {
		if IsDaemonAlive(info.Addr) {
			return info, nil
		}
	}

	// No responsive daemon found
	if len(runtimes) == 0 {
		return nil, os.ErrNotExist
	}

	// Return first one (even if unresponsive) so caller can try to clean up
	return runtimes[0], nil
}

// IsDaemonAlive checks if a daemon at the given address is actually responding.
// This is more reliable than checking PID and works cross-platform.
func IsDaemonAlive(addr string) bool {
	if addr == "" {
		return false
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://%s/api/status", addr))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// KillDaemon attempts to gracefully shut down a daemon, then force kill if needed.
// Returns true if the daemon was killed or is no longer running.
func KillDaemon(info *RuntimeInfo) bool {
	if info == nil {
		return true
	}

	// First try graceful HTTP shutdown
	if info.Addr != "" {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Post(fmt.Sprintf("http://%s/api/shutdown", info.Addr), "application/json", nil)
		if err == nil {
			resp.Body.Close()
			// Wait for graceful shutdown
			for i := 0; i < 10; i++ {
				time.Sleep(200 * time.Millisecond)
				if !IsDaemonAlive(info.Addr) {
					RemoveRuntimeForPID(info.PID)
					return true
				}
			}
		}
	}

	// HTTP shutdown failed or timed out, try OS-level kill
	if info.PID > 0 {
		if killProcess(info.PID) {
			RemoveRuntimeForPID(info.PID)
			return true
		}
	}

	return false
}

// CleanupZombieDaemons finds and kills all unresponsive daemons.
// Returns the number of zombies cleaned up.
func CleanupZombieDaemons() int {
	runtimes, err := ListAllRuntimes()
	if err != nil {
		return 0
	}

	cleaned := 0
	for _, info := range runtimes {
		// Skip responsive daemons
		if IsDaemonAlive(info.Addr) {
			continue
		}

		// Unresponsive - try to kill it
		if KillDaemon(info) {
			cleaned++
		}
	}

	// Also clean up legacy daemon.json if it exists
	legacyPath := LegacyRuntimePath()
	if _, err := os.Stat(legacyPath); err == nil {
		os.Remove(legacyPath)
	}

	return cleaned
}

// FindAvailablePort finds an available port starting from the configured port.
// After zombie cleanup, this should usually succeed on the first try.
// Falls back to searching if the port is still in use (e.g., by another service).
func FindAvailablePort(startAddr string) (string, int, error) {
	// Parse the address
	host := "127.0.0.1"
	port := 7373

	if startAddr != "" {
		parts := strings.Split(startAddr, ":")
		if len(parts) == 2 {
			host = parts[0]
			if p, err := strconv.Atoi(parts[1]); err == nil {
				port = p
			}
		}
	}

	// Try ports starting from the configured one
	for i := 0; i < 100; i++ {
		addr := fmt.Sprintf("%s:%d", host, port+i)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return addr, port + i, nil
		}
	}

	return "", 0, fmt.Errorf("no available port found starting from %d", port)
}
