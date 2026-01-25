package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFindAvailablePort(t *testing.T) {
	// Test finding an available port
	addr, port, err := FindAvailablePort("127.0.0.1:7373")
	if err != nil {
		t.Fatalf("FindAvailablePort failed: %v", err)
	}

	if addr == "" {
		t.Error("Expected non-empty address")
	}
	if port < 7373 {
		t.Errorf("Expected port >= 7373, got %d", port)
	}
}

func TestRuntimeInfoReadWrite(t *testing.T) {
	// Use temp home directory
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Write runtime info
	err := WriteRuntime("127.0.0.1:7373", 7373, "test-version")
	if err != nil {
		t.Fatalf("WriteRuntime failed: %v", err)
	}

	// Read it back
	info, err := ReadRuntime()
	if err != nil {
		t.Fatalf("ReadRuntime failed: %v", err)
	}

	if info.Addr != "127.0.0.1:7373" {
		t.Errorf("Expected addr '127.0.0.1:7373', got '%s'", info.Addr)
	}
	if info.Port != 7373 {
		t.Errorf("Expected port 7373, got %d", info.Port)
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
	// Test with non-loopback address - should not make HTTP request
	// We verify this by checking isLoopbackAddr directly and by timing:
	// if HTTP was attempted to a non-routable IP, it would take at least
	// 500ms (client timeout). Without HTTP, it returns in <100ms.

	info := &RuntimeInfo{
		PID:  999999,               // Non-existent PID
		Addr: "192.168.1.100:7373", // Non-loopback, non-routable
	}

	// Verify the address is correctly identified as non-loopback
	if isLoopbackAddr(info.Addr) {
		t.Error("192.168.1.100:7373 should not be identified as loopback")
	}

	// Time the call - if HTTP is attempted, it would timeout after 500ms+
	start := time.Now()
	result := KillDaemon(info)
	elapsed := time.Since(start)

	// With non-existent PID and non-loopback addr, should return true
	if !result {
		t.Error("KillDaemon should return true for non-existent PID")
	}

	// Should complete quickly (no HTTP call). Allow 200ms for process checks.
	// If HTTP was attempted, it would take at least 500ms (client timeout).
	if elapsed > 200*time.Millisecond {
		t.Errorf("KillDaemon took %v, suggesting HTTP was attempted to non-loopback address", elapsed)
	}
}

func TestKillDaemonMakesHTTPForLoopback(t *testing.T) {
	// Create a test server that tracks if shutdown was called
	var shutdownCalled atomic.Bool
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if strings.HasSuffix(r.URL.Path, "/api/shutdown") {
			shutdownCalled.Store(true)
			// Return OK for shutdown
			w.WriteHeader(http.StatusOK)
		} else {
			// Return 500 for status checks so KillDaemon exits quickly
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Extract address from test server (will be 127.0.0.1:xxxxx)
	addr := strings.TrimPrefix(server.URL, "http://")

	info := &RuntimeInfo{
		PID:  999999, // Non-existent PID
		Addr: addr,   // Loopback address from test server
	}

	// This should make HTTP request since address is loopback
	KillDaemon(info)

	if !shutdownCalled.Load() {
		t.Error("KillDaemon should make HTTP shutdown request to loopback addresses")
	}
	if requestCount.Load() == 0 {
		t.Error("KillDaemon should have made at least one HTTP request")
	}
}

func TestListAllRuntimesSkipsUnreadableFiles(t *testing.T) {
	// Skip on Windows where chmod 0000 doesn't block reads
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 doesn't block reads on Windows")
	}

	// Use ROBOREV_DATA_DIR to override data directory (works cross-platform)
	dataDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", dataDir)
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

	// Create a valid runtime file
	validContent := `{"pid": 12345, "addr": "127.0.0.1:7373", "port": 7373, "version": "test"}`
	validPath := dataDir + "/daemon.12345.json"
	if err := os.WriteFile(validPath, []byte(validContent), 0644); err != nil {
		t.Fatalf("Failed to write valid runtime file: %v", err)
	}

	// Create an unreadable runtime file
	unreadablePath := dataDir + "/daemon.99999.json"
	if err := os.WriteFile(unreadablePath, []byte(`{"pid": 99999, "addr": "127.0.0.1:7374"}`), 0000); err != nil {
		t.Fatalf("Failed to write unreadable runtime file: %v", err)
	}
	defer os.Chmod(unreadablePath, 0644) // Restore permissions for cleanup

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
		{"127.0.0.1.evil.com:80", false},      // Hostname that starts with 127
		{"127.0.0.1@evil.com:80", false},      // Userinfo bypass
		{"localhost.evil.com:7373", false},   // Hostname that starts with localhost
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
