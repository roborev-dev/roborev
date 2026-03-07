package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/daemon"
)

func TestEnsureDaemonRestartsWhenLegacyProbeHasNoVersion(t *testing.T) {
	t.Setenv("ROBOREV_SKIP_VERSION_CHECK", "")

	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "204 no content", statusCode: http.StatusNoContent},
		{name: "500 server error", statusCode: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/ping":
					w.WriteHeader(http.StatusNotFound)
				case "/api/status":
					w.WriteHeader(tt.statusCode)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			origGetAnyRunningDaemon := getAnyRunningDaemon
			origRestartDaemon := restartDaemonForEnsure
			getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
				return &daemon.RuntimeInfo{
					PID:     1234,
					Addr:    strings.TrimPrefix(server.URL, "http://"),
					Version: "",
				}, nil
			}
			restartCalls := 0
			restartDaemonForEnsure = func() error {
				restartCalls++
				return nil
			}
			t.Cleanup(func() {
				getAnyRunningDaemon = origGetAnyRunningDaemon
				restartDaemonForEnsure = origRestartDaemon
			})

			if err := ensureDaemon(); err != nil {
				t.Fatalf("ensureDaemon returned error: %v", err)
			}
			if restartCalls != 1 {
				t.Fatalf("expected restartDaemonForEnsure to be called once, got %d", restartCalls)
			}
		})
	}
}
