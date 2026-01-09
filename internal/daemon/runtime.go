package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wesm/roborev/internal/config"
)

// RuntimeInfo stores daemon runtime state
type RuntimeInfo struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
	Version string `json:"version"`
}

// RuntimePath returns the path to the runtime info file
func RuntimePath() string {
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

// ReadRuntime reads the daemon runtime info
func ReadRuntime() (*RuntimeInfo, error) {
	data, err := os.ReadFile(RuntimePath())
	if err != nil {
		return nil, err
	}

	var info RuntimeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	return &info, nil
}

// RemoveRuntime removes the runtime info file
func RemoveRuntime() {
	os.Remove(RuntimePath())
}

// FindAvailablePort finds an available port starting from the given port
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

	return "", 0, fmt.Errorf("no available port found")
}
