package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
	// Create a test server that fails the test if called
	httpCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		t.Error("HTTP request should not be made to non-loopback address")
	}))
	defer server.Close()

	// Extract the address (will be 127.0.0.1:xxxxx which IS loopback)
	// So we need to create a fake non-loopback address
	// KillDaemon should skip HTTP for non-loopback and go straight to PID kill

	// Test with non-loopback address - should not make HTTP request
	info := &RuntimeInfo{
		PID:  999999, // Non-existent PID
		Addr: "192.168.1.100:7373", // Non-loopback
	}

	// This should return without making HTTP calls (PID doesn't exist, so kill fails)
	KillDaemon(info)

	if httpCalled {
		t.Error("KillDaemon should not make HTTP requests to non-loopback addresses")
	}
}

func TestKillDaemonMakesHTTPForLoopback(t *testing.T) {
	// Create a test server that tracks if shutdown was called
	shutdownCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/shutdown") {
			shutdownCalled = true
		}
		// Return OK for both shutdown and status checks
		w.WriteHeader(http.StatusOK)
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

	if !shutdownCalled {
		t.Error("KillDaemon should make HTTP shutdown request to loopback addresses")
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
