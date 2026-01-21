package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestHealthEndpoint(t *testing.T) {
	// Create temp database
	dir := t.TempDir()
	db, err := storage.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create server
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)
	defer func() {
		if server.errorLog != nil {
			server.errorLog.Close()
		}
	}()

	// Create test request
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	// Call the handler
	server.handleHealth(w, req)

	// Check response
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Parse response
	var health storage.HealthStatus
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("Failed to parse health response: %v", err)
	}

	// Check basic fields
	if health.Uptime == "" {
		t.Error("Expected uptime to be set")
	}
	if health.Version == "" {
		t.Error("Expected version to be set")
	}

	// Check that we have at least database and workers components
	componentNames := make(map[string]bool)
	for _, c := range health.Components {
		componentNames[c.Name] = true
	}

	if !componentNames["database"] {
		t.Error("Expected 'database' component in health check")
	}
	if !componentNames["workers"] {
		t.Error("Expected 'workers' component in health check")
	}

	// Health should be OK since database is working
	if !health.Healthy {
		t.Error("Expected health to be OK")
	}
}

func TestHealthEndpointWithErrors(t *testing.T) {
	// Create temp database
	dir := t.TempDir()
	db, err := storage.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create server
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)
	defer func() {
		if server.errorLog != nil {
			server.errorLog.Close()
		}
	}()

	// Log some errors
	if server.errorLog != nil {
		server.errorLog.LogError("worker", "test error 1", 123)
		server.errorLog.LogError("worker", "test error 2", 456)
	}

	// Create test request
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	// Call the handler
	server.handleHealth(w, req)

	// Parse response
	var health storage.HealthStatus
	if err := json.NewDecoder(w.Result().Body).Decode(&health); err != nil {
		t.Fatalf("Failed to parse health response: %v", err)
	}

	// Check that errors are included
	if health.ErrorCount == 0 {
		t.Error("Expected error count > 0")
	}
	if len(health.RecentErrors) == 0 {
		t.Error("Expected recent errors to be returned")
	}

	// Check error details
	found := false
	for _, e := range health.RecentErrors {
		if e.Component == "worker" && e.JobID == 456 {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected to find logged error in recent errors")
	}
}

func TestHealthEndpointMethodNotAllowed(t *testing.T) {
	// Create temp database
	dir := t.TempDir()
	db, err := storage.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create server
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg)
	defer func() {
		if server.errorLog != nil {
			server.errorLog.Close()
		}
	}()

	// Create POST request (should fail)
	req := httptest.NewRequest("POST", "/api/health", nil)
	w := httptest.NewRecorder()

	// Call the handler
	server.handleHealth(w, req)

	// Check response
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", w.Result().StatusCode)
	}
}
