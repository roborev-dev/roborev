package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
)

// setupTestServer creates a temporary DB and Server, handling cleanup automatically.
func setupTestServer(t *testing.T) (*Server, *storage.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")
	t.Cleanup(func() {
		if server.errorLog != nil {
			server.errorLog.Close()
		}
	})
	return server, db
}

// executeHealthCheck sends a request to the health endpoint and returns the recorder.
func executeHealthCheck(server *Server, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/health", nil)
	w := httptest.NewRecorder()
	server.handleHealth(w, req)
	return w
}

// decodeHealthStatus parses a HealthStatus from the response body.
func decodeHealthStatus(t *testing.T, w *httptest.ResponseRecorder) storage.HealthStatus {
	t.Helper()
	var health storage.HealthStatus
	if err := json.NewDecoder(w.Result().Body).Decode(&health); err != nil {
		t.Fatalf("Failed to parse health response: %v", err)
	}
	return health
}

func TestHealthEndpoint(t *testing.T) {
	server, _ := setupTestServer(t)

	w := executeHealthCheck(server, "GET")

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Result().StatusCode)
	}

	health := decodeHealthStatus(t, w)

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

	if !health.Healthy {
		t.Error("Expected health to be OK")
	}
}

func TestHealthEndpointWithErrors(t *testing.T) {
	server, _ := setupTestServer(t)

	// Log some errors
	if server.errorLog != nil {
		server.errorLog.LogError("worker", "test error 1", 123)
		server.errorLog.LogError("worker", "test error 2", 456)
	}

	w := executeHealthCheck(server, "GET")
	health := decodeHealthStatus(t, w)

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
	server, _ := setupTestServer(t)

	w := executeHealthCheck(server, "POST")

	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", w.Result().StatusCode)
	}
}
