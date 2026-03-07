package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/githook"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func assertError(t *testing.T, err error, expectError bool, contains string) {
	t.Helper()
	if expectError {
		if err == nil {
			t.Error("expected error but got nil")
		} else if contains != "" && !strings.Contains(err.Error(), contains) {
			t.Errorf("error %q expected to contain %q", err.Error(), contains)
		}
	} else if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// initNoDaemonSetup prepares the environment for init --no-daemon tests:
// isolated HOME, fake roborev binary, and chdir to a test repo.
func initNoDaemonSetup(t *testing.T) *testutil.TestRepo {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("ROBOREV_DATA_DIR", filepath.Join(tmpHome, ".roborev"))

	repo := testutil.NewTestRepo(t)

	testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n")
	t.Cleanup(repo.Chdir())

	return repo
}

func setupMockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestInitNoDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows due to shell script stubs")
	}

	tests := []struct {
		name           string
		serverHandler  http.HandlerFunc
		setupFiles     func(t *testing.T, repo *testutil.TestRepo)
		expectContains []string
		expectNot      []string
		expectError    bool
		errorContains  string
		postCheck      func(t *testing.T, repo *testutil.TestRepo)
	}{
		{
			name:          "Connection Error",
			serverHandler: nil, // Simulates bad connection
			expectContains: []string{
				"Daemon not running",
				"Setup incomplete",
			},
			expectNot: []string{"Ready!"},
		},
		{
			name: "Server Error 500",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(500)
				_, _ = w.Write([]byte("database locked"))
			},
			expectContains: []string{
				"Warning: failed to register repo",
				"500",
				"Setup incomplete",
			},
			expectNot: []string{"Ready!"},
		},
		{
			name: "Success",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			},
			expectContains: []string{
				"Repo registered with running daemon",
				"Ready!",
			},
			expectNot: []string{"Setup incomplete"},
		},
		{
			name: "Installs PostRewrite Hook On Upgrade",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			},
			setupFiles: func(t *testing.T, repo *testutil.TestRepo) {
				if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo.HooksDir, "post-commit"), []byte(githook.GeneratePostCommit()), 0755); err != nil {
					t.Fatal(err)
				}
			},
			postCheck: func(t *testing.T, repo *testutil.TestRepo) {
				prHookPath := filepath.Join(repo.HooksDir, "post-rewrite")
				content, err := os.ReadFile(prHookPath)
				if err != nil {
					t.Fatalf("post-rewrite hook should be installed: %v", err)
				}
				if !strings.Contains(string(content), "remap --quiet") {
					t.Error("post-rewrite hook should contain 'remap --quiet'")
				}
			},
		},
		{
			name: "Warns On Non-Shell Hook",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			},
			setupFiles: func(t *testing.T, repo *testutil.TestRepo) {
				os.MkdirAll(repo.HooksDir, 0755)
				os.WriteFile(
					filepath.Join(repo.HooksDir, "post-commit"),
					[]byte("#!/usr/bin/env python3\nprint('hello')\n"),
					0755,
				)
			},
			expectContains: []string{
				"non-shell interpreter",
				"Ready!",
			},
		},
		{
			name: "Fails On Hook Write Error",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			},
			setupFiles: func(t *testing.T, repo *testutil.TestRepo) {
				os.MkdirAll(repo.HooksDir, 0755)
				os.Chmod(repo.HooksDir, 0555)
				t.Cleanup(func() { os.Chmod(repo.HooksDir, 0755) })
			},
			expectError:   true,
			errorContains: "install hooks",
		},
		{
			name: "Fails On Mixed Hook Errors",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			},
			setupFiles: func(t *testing.T, repo *testutil.TestRepo) {
				os.MkdirAll(repo.HooksDir, 0755)
				os.WriteFile(
					filepath.Join(repo.HooksDir, "post-commit"),
					[]byte("#!/usr/bin/env python3\nprint('hello')\n"),
					0755,
				)
				// Create post-rewrite as a directory so writing it fails.
				os.MkdirAll(filepath.Join(repo.HooksDir, "post-rewrite"), 0755)
			},
			expectError:   true,
			errorContains: "install hooks",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initNoDaemonSetup(t)

			if tt.setupFiles != nil {
				tt.setupFiles(t, repo)
			}

			var testServerAddr string
			if tt.serverHandler != nil {
				ts := setupMockServer(t, tt.serverHandler)
				testServerAddr = ts.URL
			} else {
				// Simulate connection error
				testServerAddr = "http://127.0.0.1:1"
			}

			output := captureStdout(t, func() {
				cmd := initCmd()
				ctx := context.WithValue(context.Background(), serverAddrKey{}, testServerAddr)
				cmd.SetContext(ctx)
				cmd.SetArgs([]string{"--no-daemon"})
				err := cmd.Execute()
				assertError(t, err, tt.expectError, tt.errorContains)
			})

			for _, s := range tt.expectContains {
				if !strings.Contains(output, s) {
					t.Errorf("output missing %q", s)
				}
			}
			for _, s := range tt.expectNot {
				if strings.Contains(output, s) {
					t.Errorf("output should not contain %q", s)
				}
			}

			if tt.postCheck != nil {
				tt.postCheck(t, repo)
			}
		})
	}
}
