package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
)

const (
	defaultTestPort = 7373
	defaultTestAddr = "127.0.0.1:7373"
)

type runtimeData struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
	Version string `json:"version"`
}

// createRuntimeFile creates a daemon runtime JSON file in dir. If data is
// nil a valid default is generated from pid.
func createRuntimeFile(t *testing.T, dir string, pid int, data *runtimeData) string {
	t.Helper()
	if data == nil {
		data = &runtimeData{
			PID:     pid,
			Addr:    defaultTestAddr,
			Port:    defaultTestPort,
			Version: "test",
		}
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Failed to marshal runtime data: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("daemon.%d.json", pid))
	if err := os.WriteFile(path, bytes, 0644); err != nil {
		t.Fatalf("Failed to write runtime file: %v", err)
	}
	return path
}

// startMockDaemon starts an httptest server handled by handler and returns the
// "host:port" address. The server is closed automatically when the test ends.
func startMockDaemon(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

// mockIdentifyProcess replaces the global identifyProcess function with mock
// for the duration of the test. Not safe for use with t.Parallel().
func mockIdentifyProcess(t *testing.T, mock func(int) processIdentity) {
	t.Helper()
	orig := identifyProcess
	identifyProcess = mock
	t.Cleanup(func() { identifyProcess = orig })
}

func TestFindAvailablePort(t *testing.T) {
	// Test finding an available port
	addr, port, err := FindAvailablePort(defaultTestAddr)
	if err != nil {
		t.Fatalf("FindAvailablePort failed: %v", err)
	}

	if addr == "" {
		t.Error("Expected non-empty address")
	}
	if port < defaultTestPort {
		t.Errorf("Expected port >= %d, got %d", defaultTestPort, port)
	}
}

func TestRuntimeInfoReadWrite(t *testing.T) {
	testenv.SetDataDir(t)

	// Write runtime info
	err := WriteRuntime(defaultTestAddr, defaultTestPort, "test-version")
	if err != nil {
		t.Fatalf("WriteRuntime failed: %v", err)
	}

	// Read it back
	info, err := ReadRuntime()
	if err != nil {
		t.Fatalf("ReadRuntime failed: %v", err)
	}

	if info.Addr != defaultTestAddr {
		t.Errorf("Expected addr '%s', got '%s'", defaultTestAddr, info.Addr)
	}
	if info.Port != defaultTestPort {
		t.Errorf("Expected port %d, got %d", defaultTestPort, info.Port)
	}
	if info.PID == 0 {
		t.Error("Expected non-zero PID")
	}
	if info.Version != "test-version" {
		t.Errorf("Expected version 'test-version', got '%s'", info.Version)
	}

	// Remove it
	RemoveRuntime()

	// Should fail to read now
	_, err = ReadRuntime()
	if err == nil {
		t.Error("Expected error after RemoveRuntime")
	}
}

func TestKillDaemonSkipsHTTPForNonLoopback(t *testing.T) {
	// Verify that isLoopbackAddr correctly rejects non-loopback addresses,
	// which prevents KillDaemon from making HTTP requests to them.
	if isLoopbackAddr("192.168.1.100:7373") {
		t.Fatal("192.168.1.100:7373 should not be identified as loopback")
	}

	// KillDaemon with a non-loopback address should skip HTTP and fall
	// through to killProcess (which fails for a non-existent PID).
	// This must complete promptly without attempting network connections.
	info := &RuntimeInfo{
		PID:  999999,               // Non-existent PID
		Addr: "192.168.1.100:7373", // Non-loopback address
	}

	result := KillDaemon(info)

	// killProcess confirms a non-existent PID is dead, so KillDaemon returns true
	if !result {
		t.Error("KillDaemon should return true for non-existent PID (process confirmed dead)")
	}
}

func TestListAllRuntimesSkipsUnreadableFiles(t *testing.T) {
	// Skip on Windows where chmod 0000 doesn't block reads
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 doesn't block reads on Windows")
	}

	dataDir := testenv.SetDataDir(t)

	// Create a valid runtime file
	createRuntimeFile(t, dataDir, 12345, nil)

	// Create an unreadable runtime file
	unreadablePath := createRuntimeFile(t, dataDir, 99999, &runtimeData{
		PID:  99999,
		Addr: "127.0.0.1:7374",
	})
	os.Chmod(unreadablePath, 0000)
	t.Cleanup(func() { os.Chmod(unreadablePath, 0644) })

	// Probe whether chmod 0000 actually blocks reads on this filesystem
	if f, probeErr := os.Open(unreadablePath); probeErr == nil {
		f.Close()
		t.Skip("filesystem does not enforce chmod 0000 read restrictions")
	}

	// ListAllRuntimes should return the readable entry without error
	runtimes, err := ListAllRuntimes()
	if err != nil {
		t.Fatalf("ListAllRuntimes should not error on unreadable files: %v", err)
	}

	// Should have found the valid runtime
	if len(runtimes) != 1 {
		t.Errorf("Expected 1 runtime, got %d", len(runtimes))
	}
	if len(runtimes) > 0 && runtimes[0].PID != 12345 {
		t.Errorf("Expected PID 12345, got %d", runtimes[0].PID)
	}
}

func TestIdentifyProcessTriState(t *testing.T) {
	// Test that identifyProcess returns appropriate tri-state values

	// Non-existent PID should return processUnknown (can't determine)
	// or processNotRoborev if the system can confirm no such process
	result := identifyProcess(999999999)
	// Either unknown or not-roborev is acceptable for non-existent PID
	if result == processIsRoborev {
		t.Error("identifyProcess(999999999) should not return processIsRoborev for non-existent PID")
	}

	// Current process is a test binary, not roborev daemon
	// Should return processNotRoborev (confirmed not a daemon)
	currentPID := os.Getpid()
	result = identifyProcess(currentPID)
	if result == processIsRoborev {
		t.Errorf("identifyProcess(%d) should not return processIsRoborev for test process", currentPID)
	}
	// On most systems we should be able to identify our own process
	if result == processUnknown {
		t.Logf("identifyProcess(%d) returned processUnknown (may be expected on some systems)", currentPID)
	}
}

func TestKillProcessConservativeOnUnknown(t *testing.T) {
	// Test that killProcess is conservative when process identity is unknown
	// Using a very high PID that almost certainly doesn't exist
	nonExistentPID := 999999999

	// killProcess should return true for non-existent PID (process is dead)
	// This is safe because the process doesn't exist at all
	result := killProcess(nonExistentPID)
	if !result {
		t.Error("killProcess should return true for non-existent PID")
	}
}

func TestKillProcessUnknownIdentityIsConservative(t *testing.T) {
	// Mock identifyProcess to always return unknown
	mockIdentifyProcess(t, func(pid int) processIdentity {
		return processUnknown
	})

	// Use current process PID (definitely exists)
	currentPID := os.Getpid()

	// killProcess should return false (conservative - don't clean up)
	// when identity is unknown for a live process
	result := killProcess(currentPID)
	if result {
		t.Error("killProcess should return false (conservative) when identity is unknown for live process")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		// Valid loopback addresses
		{"127.0.0.1:7373", true},
		{"127.0.0.1:80", true},
		{"127.0.1.1:7373", true},
		{"localhost:7373", true},
		{"[::1]:7373", true},

		// Invalid/non-loopback
		{"192.168.1.1:7373", false},
		{"10.0.0.1:7373", false},
		{"8.8.8.8:7373", false},
		{"example.com:7373", false},
		{"", false},

		// Bypass attempts
		{"127.0.0.1.evil.com:80", false},   // Hostname that starts with 127
		{"127.0.0.1@evil.com:80", false},   // Userinfo bypass
		{"localhost.evil.com:7373", false}, // Hostname that starts with localhost
		{"evil.com:7373", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := isLoopbackAddr(tt.addr)
			if got != tt.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestIsDaemonAliveStatusCodes(t *testing.T) {
	var nextStatus int
	// Start one server for all cases
	addr := startMockDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(nextStatus)
	})

	tests := []struct {
		name       string
		statusCode int
		wantAlive  bool
	}{
		// 2xx - success, daemon is alive
		{"200 OK", http.StatusOK, true},
		{"201 Created", http.StatusCreated, true},
		{"204 No Content", http.StatusNoContent, true},

		// 5xx - server error, daemon is alive but having issues
		{"500 Internal Server Error", http.StatusInternalServerError, true},
		{"502 Bad Gateway", http.StatusBadGateway, true},
		{"503 Service Unavailable", http.StatusServiceUnavailable, true},

		// 3xx - redirect, likely different service
		{"301 Moved Permanently", http.StatusMovedPermanently, false},
		{"302 Found", http.StatusFound, false},

		// 4xx - client error, likely different service (auth proxy, unrelated API)
		{"400 Bad Request", http.StatusBadRequest, false},
		{"401 Unauthorized", http.StatusUnauthorized, false},
		{"403 Forbidden", http.StatusForbidden, false},
		{"404 Not Found", http.StatusNotFound, false},
		{"405 Method Not Allowed", http.StatusMethodNotAllowed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextStatus = tt.statusCode
			got := IsDaemonAlive(addr)
			if got != tt.wantAlive {
				t.Errorf("IsDaemonAlive with %d = %v, want %v", tt.statusCode, got, tt.wantAlive)
			}
		})
	}
}

func TestListAllRuntimesWithGlobMetacharacters(t *testing.T) {
	// Create a temp directory with glob metacharacters in the name
	tmpDir := t.TempDir()
	// Create a subdirectory with brackets (glob metacharacter)
	dataDir := filepath.Join(tmpDir, "data[test]")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Set ROBOREV_DATA_DIR to the directory with metacharacters
	t.Setenv("ROBOREV_DATA_DIR", dataDir)

	// Create a valid runtime file
	createRuntimeFile(t, dataDir, 12345, nil)

	// ListAllRuntimes should work despite glob metacharacters in path
	runtimes, err := ListAllRuntimes()
	if err != nil {
		t.Fatalf("ListAllRuntimes failed with glob metacharacters in path: %v", err)
	}

	if len(runtimes) != 1 {
		t.Errorf("Expected 1 runtime, got %d", len(runtimes))
	}
	if len(runtimes) > 0 && runtimes[0].PID != 12345 {
		t.Errorf("Expected PID 12345, got %d", runtimes[0].PID)
	}
}
