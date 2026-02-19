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
func setupTestServer(t *testing.T) *Server {
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
	return server
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

func TestHealth(t *testing.T) {
	t.Run("Happy Path", func(t *testing.T) {
		server := setupTestServer(t)
		w := executeHealthCheck(server, http.MethodGet)
		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		health := decodeHealthStatus(t, w)
		assertNotEmpty(t, health.Uptime, "Uptime")
		assertNotEmpty(t, health.Version, "Version")
		assertComponentExists(t, health.Components, "database")
		assertComponentExists(t, health.Components, "workers")

		if !health.Healthy {
			t.Error("Expected health to be OK")
		}
	})

	t.Run("With Errors", func(t *testing.T) {
		server := setupTestServer(t)
		// Log specific errors
		if server.errorLog != nil {
			server.errorLog.LogError("worker", "test error 1", 123)
			server.errorLog.LogError("worker", "test error 2", 456)
		}

		w := executeHealthCheck(server, http.MethodGet)
		health := decodeHealthStatus(t, w)

		if health.ErrorCount == 0 {
			t.Error("Expected error count > 0")
		}

		assertErrorExists(t, health.RecentErrors, "worker", 456)
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		server := setupTestServer(t)
		w := executeHealthCheck(server, http.MethodPost)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405, got %d", w.Code)
		}
	})
}

// Helpers

func assertNotEmpty(t *testing.T, val, name string) {
	t.Helper()
	if val == "" {
		t.Errorf("Expected %s to be set", name)
	}
}

func assertComponentExists(t *testing.T, components []storage.ComponentHealth, name string) {
	t.Helper()
	for _, c := range components {
		if c.Name == name {
			return
		}
	}
	t.Errorf("Expected component '%s' in health check", name)
}

func assertErrorExists(t *testing.T, errors []storage.ErrorEntry, component string, jobID int64) {
	t.Helper()
	for _, e := range errors {
		if e.Component == component && e.JobID == jobID {
			return
		}
	}
	t.Errorf("Expected error for component '%s' with JobID %d", component, jobID)
}
