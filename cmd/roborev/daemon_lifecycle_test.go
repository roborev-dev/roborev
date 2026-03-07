package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/version"
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

func TestEnsureDaemonRestartsWhenManualLegacyProbeHasNoVersion(t *testing.T) {
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
				return nil, os.ErrNotExist
			}
			patchServerAddr(t, server.URL)
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

func TestEnsureDaemonPrefersLiveDaemonVersionOverRuntimeMetadata(t *testing.T) {
	t.Setenv("ROBOREV_SKIP_VERSION_CHECK", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ping":
			_ = json.NewEncoder(w).Encode(daemon.PingInfo{
				Service: "roborev",
				Version: "v-other-daemon",
			})
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
			Version: version.Version,
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
}

func TestEnsureDaemonRestartsWhenLiveProbeFailsDespiteRuntimeVersion(t *testing.T) {
	t.Setenv("ROBOREV_SKIP_VERSION_CHECK", "")

	origGetAnyRunningDaemon := getAnyRunningDaemon
	origRestartDaemon := restartDaemonForEnsure
	getAnyRunningDaemon = func() (*daemon.RuntimeInfo, error) {
		return &daemon.RuntimeInfo{
			PID:     1234,
			Addr:    "127.0.0.1:1",
			Version: version.Version,
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
}
