package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestUninstallHookCmd(t *testing.T) {
	tests := []struct {
		name         string
		initialHooks map[string]string
		setup        func(t *testing.T, repo *testutil.TestRepo)
		assert       func(t *testing.T, repo *testutil.TestRepo)
	}{
		{
			name: "hook missing",
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
					t.Error("Hook file should not exist")
				}
			},
		},
		{
			name: "hook without roborev",
			initialHooks: map[string]string{
				"post-commit": "#!/bin/bash\necho 'other hook'\n",
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				content, err := os.ReadFile(repo.HookPath)
				if err != nil {
					t.Fatalf("Failed to read hook: %v", err)
				}
				want := "#!/bin/bash\necho 'other hook'\n"
				if string(content) != want {
					t.Errorf("Hook content changed: got %q, want %q", string(content), want)
				}
			},
		},
		{
			name: "hook with roborev only - removes file",
			initialHooks: map[string]string{
				"post-commit": githook.GeneratePostCommit(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
					t.Error("Hook file should have been removed")
				}
			},
		},
		{
			name: "hook with roborev and other commands - preserves others",
			initialHooks: map[string]string{
				"post-commit": "#!/bin/sh\necho 'before'\necho 'after'\n" + githook.GeneratePostCommit(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				content, err := os.ReadFile(repo.HookPath)
				if err != nil {
					t.Fatalf("Failed to read hook: %v", err)
				}
				contentStr := string(content)
				if strings.Contains(contentStr, "enqueue --quiet") {
					t.Error("Hook should not contain generated roborev snippet")
				}
				if !strings.Contains(contentStr, "echo 'before'") {
					t.Error("Hook should still contain 'echo before'")
				}
				if !strings.Contains(contentStr, "echo 'after'") {
					t.Error("Hook should still contain 'echo after'")
				}
			},
		},
		{
			name: "also removes post-rewrite hook",
			initialHooks: map[string]string{
				"post-commit":  githook.GeneratePostCommit(),
				"post-rewrite": githook.GeneratePostRewrite(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
					t.Error("post-commit hook should have been removed")
				}
				prPath := filepath.Join(repo.HooksDir, "post-rewrite")
				if _, err := os.Stat(prPath); !os.IsNotExist(err) {
					t.Error("post-rewrite hook should have been removed")
				}
			},
		},
		{
			name: "removes post-rewrite even without post-commit",
			initialHooks: map[string]string{
				"post-rewrite": githook.GeneratePostRewrite(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				prPath := filepath.Join(repo.HooksDir, "post-rewrite")
				if _, err := os.Stat(prPath); !os.IsNotExist(err) {
					t.Error("post-rewrite hook should have been removed")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			t.Cleanup(repo.Chdir())

			if tt.setup != nil {
				tt.setup(t, repo)
			}

			if len(tt.initialHooks) > 0 {
				if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
					t.Fatal(err)
				}
				for name, content := range tt.initialHooks {
					path := filepath.Join(repo.HooksDir, name)
					if err := os.WriteFile(path, []byte(content), 0755); err != nil {
						t.Fatal(err)
					}
				}
			}

			cmd := uninstallHookCmd()
			err := cmd.Execute()
			if err != nil {
				t.Fatalf("uninstall-hook failed: %v", err)
			}

			tt.assert(t, repo)
		})
	}
}

func TestInstallHookCmdCreatesHooksDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test checks Unix exec bits, skipping on Windows")
	}

	repo := testutil.NewTestRepo(t)
	repo.RemoveHooksDir()

	if _, err := os.Stat(repo.HooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	t.Cleanup(repo.Chdir())

	installCmd := installHookCmd()
	installCmd.SetArgs([]string{})
	err := installCmd.Execute()

	if err != nil {
		t.Fatalf("install-hook command failed: %v", err)
	}

	if _, err := os.Stat(repo.HooksDir); os.IsNotExist(err) {
		t.Error("hooks directory was not created")
	}

	if _, err := os.Stat(repo.HookPath); os.IsNotExist(err) {
		t.Error("post-commit hook was not created")
	}
}

func TestInstallHookCmdCreatesPostRewriteHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test checks Unix exec bits, skipping on Windows")
	}

	repo := testutil.NewTestRepo(t)
	repo.RemoveHooksDir()
	t.Cleanup(repo.Chdir())

	installCmd := installHookCmd()
	installCmd.SetArgs([]string{})
	if err := installCmd.Execute(); err != nil {
		t.Fatalf("install-hook failed: %v", err)
	}

	prHookPath := filepath.Join(repo.HooksDir, "post-rewrite")
	content, err := os.ReadFile(prHookPath)
	if err != nil {
		t.Fatalf("post-rewrite hook not created: %v", err)
	}

	if !strings.Contains(string(content), "remap --quiet") {
		t.Error("post-rewrite hook should contain 'remap --quiet'")
	}
	if !strings.Contains(string(content), githook.PostRewriteVersionMarker) {
		t.Error("post-rewrite hook should contain version marker")
	}
}

func TestIsTransportError(t *testing.T) {
	t.Run("url.Error wrapping OpError is transport error", func(t *testing.T) {
		err := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: &net.OpError{
			Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
		}}
		if !isTransportError(err) {
			t.Error("expected url.Error+OpError to be classified as transport error")
		}
	})

	t.Run("url.Error without OpError is not transport error", func(t *testing.T) {
		err := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: errors.New("some non-transport error")}
		if isTransportError(err) {
			t.Error("expected url.Error without net.OpError to NOT be transport error")
		}
	})

	t.Run("registerRepoError is not transport error", func(t *testing.T) {
		err := &registerRepoError{StatusCode: 500, Body: "internal error"}
		if isTransportError(err) {
			t.Error("expected registerRepoError to NOT be transport error")
		}
	})

	t.Run("plain error is not transport error", func(t *testing.T) {
		err := fmt.Errorf("something else")
		if isTransportError(err) {
			t.Error("expected plain error to NOT be transport error")
		}
	})

	t.Run("wrapped url.Error with OpError is transport error", func(t *testing.T) {
		inner := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: &net.OpError{
			Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
		}}
		err := fmt.Errorf("register failed: %w", inner)
		if !isTransportError(err) {
			t.Error("expected wrapped url.Error+OpError to be transport error")
		}
	})
}

func TestRegisterRepoError(t *testing.T) {
	err := &registerRepoError{StatusCode: 500, Body: "internal server error"}
	if err.Error() != "server returned 500: internal server error" {
		t.Errorf("unexpected error message: %s", err.Error())
	}

	var regErr *registerRepoError
	if !errors.As(err, &regErr) {
		t.Error("expected errors.As to match registerRepoError")
	}
	if regErr.StatusCode != 500 {
		t.Errorf("expected StatusCode 500, got %d", regErr.StatusCode)
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
	t.Cleanup(testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n"))
	t.Cleanup(repo.Chdir())

	return repo
}

func setupMockServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	oldAddr := serverAddr
	serverAddr = ts.URL
	t.Cleanup(func() { serverAddr = oldAddr })
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

			if tt.serverHandler != nil {
				setupMockServer(t, tt.serverHandler)
			} else {
				// Simulate connection error
				oldAddr := serverAddr
				serverAddr = "http://127.0.0.1:1"
				t.Cleanup(func() { serverAddr = oldAddr })
			}

			output := captureStdout(t, func() {
				cmd := initCmd()
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
