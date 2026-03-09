package main

import (
	"errors"
	"fmt"
	"io/fs"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertError(t *testing.T, err error, expectError bool, contains string) {
	t.Helper()
	if expectError {
		require.Error(t, err, "expected error but got nil")
		if contains != "" {
			assert.Contains(t, err.Error(), contains, "error text")
		}
	} else {
		require.NoError(t, err)
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
				_, err := os.Stat(repo.HookPath)
				require.ErrorIs(t, err, fs.ErrNotExist)
			},
		},
		{
			name: "hook without roborev",
			initialHooks: map[string]string{
				"post-commit": "#!/bin/bash\necho 'other hook'\n",
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				content, err := os.ReadFile(repo.HookPath)
				require.NoError(t, err, "Failed to read hook: %v")

				want := "#!/bin/bash\necho 'other hook'\n"
				assert.Equal(t, want, string(content), "unexpected condition")
			},
		},
		{
			name: "hook with roborev only - removes file",
			initialHooks: map[string]string{
				"post-commit": githook.GeneratePostCommit(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				_, err := os.Stat(repo.HookPath)
				require.ErrorIs(t, err, fs.ErrNotExist)
			},
		},
		{
			name: "hook with roborev and other commands - preserves others",
			initialHooks: map[string]string{
				"post-commit": "#!/bin/sh\necho 'before'\necho 'after'\n" + githook.GeneratePostCommit(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				content, err := os.ReadFile(repo.HookPath)
				require.NoError(t, err, "Failed to read hook: %v")

				contentStr := string(content)
				assert.NotContains(t, contentStr, githook.PostCommitVersionMarker, "unexpected condition")
				assert.NotContains(t, contentStr, `"$ROBOREV" post-commit`, "unexpected condition")
				assert.Contains(t, contentStr, "echo 'before'", "unexpected condition")
				assert.Contains(t, contentStr, "echo 'after'", "unexpected condition")
			},
		},
		{
			name: "also removes post-rewrite hook",
			initialHooks: map[string]string{
				"post-commit":  githook.GeneratePostCommit(),
				"post-rewrite": githook.GeneratePostRewrite(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				_, err := os.Stat(repo.HookPath)
				require.ErrorIs(t, err, fs.ErrNotExist)
				prPath := filepath.Join(repo.HooksDir, "post-rewrite")
				_, err = os.Stat(prPath)
				require.ErrorIs(t, err, fs.ErrNotExist)
			},
		},
		{
			name: "removes post-rewrite even without post-commit",
			initialHooks: map[string]string{
				"post-rewrite": githook.GeneratePostRewrite(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				prPath := filepath.Join(repo.HooksDir, "post-rewrite")
				_, err := os.Stat(prPath)
				require.ErrorIs(t, err, fs.ErrNotExist)
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
					require.NoError(t, err)
				}
				for name, content := range tt.initialHooks {
					path := filepath.Join(repo.HooksDir, name)
					if err := os.WriteFile(path, []byte(content), 0755); err != nil {
						require.NoError(t, err)
					}
				}
			}

			cmd := uninstallHookCmd()
			err := cmd.Execute()
			require.NoError(t, err, "uninstall-hook failed: %v")

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
		require.NoError(t, err, "hooks directory should not exist before test")
	}

	t.Cleanup(repo.Chdir())

	installCmd := installHookCmd()
	installCmd.SetArgs([]string{})
	err := installCmd.Execute()

	require.NoError(t, err, "install-hook command failed: %v")

	_, err = os.Stat(repo.HooksDir)
	require.NoError(t, err)

	_, err = os.Stat(repo.HookPath)
	require.NoError(t, err)
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
		require.NoError(t, err, "install-hook failed: %v")
	}

	prHookPath := filepath.Join(repo.HooksDir, "post-rewrite")
	content, err := os.ReadFile(prHookPath)
	require.NoError(t, err, "post-rewrite hook not created: %v")

	assert.Contains(t, string(content), "remap --quiet", "unexpected condition")
	assert.Contains(t, string(content), githook.PostRewriteVersionMarker, "unexpected condition")
}

func TestIsTransportError(t *testing.T) {
	t.Run("url.Error wrapping OpError is transport error", func(t *testing.T) {
		err := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: &net.OpError{
			Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
		}}
		assert.True(t, isTransportError(err), "unexpected condition")
	})

	t.Run("url.Error without OpError is not transport error", func(t *testing.T) {
		err := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: errors.New("some non-transport error")}
		assert.False(t, isTransportError(err), "unexpected condition")
	})

	t.Run("registerRepoError is not transport error", func(t *testing.T) {
		err := &registerRepoError{StatusCode: 500, Body: "internal error"}
		assert.False(t, isTransportError(err), "unexpected condition")
	})

	t.Run("plain error is not transport error", func(t *testing.T) {
		err := fmt.Errorf("something else")
		assert.False(t, isTransportError(err), "unexpected condition")
	})

	t.Run("wrapped url.Error with OpError is transport error", func(t *testing.T) {
		inner := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: &net.OpError{
			Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
		}}
		err := fmt.Errorf("register failed: %w", inner)
		assert.True(t, isTransportError(err), "unexpected condition")
	})
}

func TestRegisterRepoError(t *testing.T) {
	err := &registerRepoError{StatusCode: 500, Body: "internal server error"}
	assert.Equal(t, "server returned 500: internal server error", err.Error(), "unexpected condition")

	var regErr *registerRepoError
	require.ErrorAs(t, err, &regErr, "unexpected condition")
	assert.Equal(t, 500, regErr.StatusCode, "unexpected condition")
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
					require.NoError(t, err)
				}
				if err := os.WriteFile(filepath.Join(repo.HooksDir, "post-commit"), []byte(githook.GeneratePostCommit()), 0755); err != nil {
					require.NoError(t, err)
				}
			},
			postCheck: func(t *testing.T, repo *testutil.TestRepo) {
				prHookPath := filepath.Join(repo.HooksDir, "post-rewrite")
				content, err := os.ReadFile(prHookPath)
				require.NoError(t, err, "post-rewrite hook should be installed: %v")

				assert.Contains(t, string(content), "remap --quiet", "unexpected condition")
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
				assert.Contains(t, output, s, "unexpected condition")
			}
			for _, s := range tt.expectNot {
				assert.NotContains(t, output, s, "unexpected condition")
			}

			if tt.postCheck != nil {
				tt.postCheck(t, repo)
			}
		})
	}
}

func TestInitNoDaemonWithAgentCreatesCommentedRepoConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows due to shell script stubs")
	}

	repo := initNoDaemonSetup(t)
	setupMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	output := captureStdout(t, func() {
		cmd := initCmd()
		cmd.SetArgs([]string{"--no-daemon", "--agent", "codex"})
		if err := cmd.Execute(); err != nil {
			require.Condition(t, func() bool {
				return false
			}, "init failed: %v", err)
		}
	})

	if !strings.Contains(output, "Created ") || !strings.Contains(output, ".roborev.toml") {
		require.Condition(t, func() bool {
			return false
		}, "init output missing repo config creation message:\n%s", output)
	}

	data, err := os.ReadFile(filepath.Join(repo.Root, ".roborev.toml"))
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "read repo config: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"# Default agent for this repo when no workflow-specific agent is set.\n",
		"agent = 'codex'",
	} {
		if !strings.Contains(got, want) {
			require.Condition(t, func() bool {
				return false
			}, "repo config missing %q:\n%s", want, got)
		}
	}
}
