package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestServer creates a temporary DB and Server, handling cleanup automatically.
func setupTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err, "Failed to open database")
	t.Cleanup(func() { db.Close() })

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")
	t.Cleanup(func() { server.Close() })
	return server
}

// executeHealthCheck sends a request to the health endpoint and returns the recorder.
func executeHealthCheck(server *Server, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/health", nil)
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)
	return w
}

// decodeHealthStatus parses a HealthStatus from the response body.
func decodeHealthStatus(t *testing.T, w *httptest.ResponseRecorder) storage.HealthStatus {
	t.Helper()
	var health storage.HealthStatus
	res := w.Result()
	defer res.Body.Close()
	err := json.NewDecoder(res.Body).Decode(&health)
	require.NoError(t, err, "Failed to parse health response")
	return health
}

func TestHealth(t *testing.T) {
	t.Run("Happy Path", func(t *testing.T) {
		server := setupTestServer(t)
		w := executeHealthCheck(server, http.MethodGet)
		assert.Equal(t, http.StatusOK, w.Code)

		health := decodeHealthStatus(t, w)
		assert.NotEmpty(t, health.Uptime, "Uptime")
		assert.NotEmpty(t, health.Version, "Version")

		hasDB := slices.ContainsFunc(health.Components, func(c storage.ComponentHealth) bool { return c.Name == "database" })
		assert.True(t, hasDB, "Expected component 'database' in health check")

		hasWorkers := slices.ContainsFunc(health.Components, func(c storage.ComponentHealth) bool { return c.Name == "workers" })
		assert.True(t, hasWorkers, "Expected component 'workers' in health check")

		assert.True(t, health.Healthy, "Expected health to be OK")
	})

	t.Run("With Errors", func(t *testing.T) {
		server := setupTestServer(t)
		// Log specific errors
		require.NotNil(t, server.errorLog, "server.errorLog must be initialized")
		server.errorLog.LogError("worker", "test error 1", 123)
		server.errorLog.LogError("worker", "test error 2", 456)

		w := executeHealthCheck(server, http.MethodGet)
		assert.Equal(t, http.StatusOK, w.Code)
		health := decodeHealthStatus(t, w)

		assert.Greater(t, health.ErrorCount, 0, "Expected error count > 0")

		hasErr := slices.ContainsFunc(health.RecentErrors, func(e storage.ErrorEntry) bool {
			return e.Component == "worker" && e.JobID == 456
		})
		assert.True(t, hasErr, "Expected error for component 'worker' with JobID 456")
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		server := setupTestServer(t)
		w := executeHealthCheck(server, http.MethodPost)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}
