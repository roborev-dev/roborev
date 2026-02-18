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

func TestUninstallHookCmd(t *testing.T) {
	t.Run("hook missing", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
			t.Error("Hook file should not exist")
		}
	})

	t.Run("hook without roborev", func(t *testing.T) {
		hookContent := "#!/bin/bash\necho 'other hook'\n"
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(hookContent)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		content, err := os.ReadFile(repo.HookPath)
		if err != nil {
			t.Fatalf("Failed to read hook: %v", err)
		}
		if string(content) != hookContent {
			t.Errorf("Hook content changed: got %q, want %q", string(content), hookContent)
		}
	})

	t.Run("hook with roborev only - removes file", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(githook.GeneratePostCommit())
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
			t.Error("Hook file should have been removed")
		}
	})

	t.Run("hook with roborev and other commands - preserves others", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		mixed := "#!/bin/sh\necho 'before'\necho 'after'\n" +
			githook.GeneratePostCommit()
		repo.WriteHook(mixed)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

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
	})

	t.Run("also removes post-rewrite hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(githook.GeneratePostCommit())
		prPath := filepath.Join(repo.HooksDir, "post-rewrite")
		os.WriteFile(
			prPath,
			[]byte(githook.GeneratePostRewrite()),
			0755,
		)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
			t.Error("post-commit hook should have been removed")
		}
		if _, err := os.Stat(prPath); !os.IsNotExist(err) {
			t.Error("post-rewrite hook should have been removed")
		}
	})

	t.Run("removes post-rewrite even without post-commit", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		prPath := filepath.Join(repo.HooksDir, "post-rewrite")
		os.MkdirAll(repo.HooksDir, 0755)
		os.WriteFile(
			prPath,
			[]byte(githook.GeneratePostRewrite()),
			0755,
		)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		if _, err := os.Stat(prPath); !os.IsNotExist(err) {
			t.Error("post-rewrite hook should have been removed")
		}
	})
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

	defer repo.Chdir()()

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
	defer repo.Chdir()()

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
func initNoDaemonSetup(t *testing.T) string {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("ROBOREV_DATA_DIR", filepath.Join(tmpHome, ".roborev"))

	repo := testutil.NewTestRepo(t)
	t.Cleanup(testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n"))
	t.Cleanup(repo.Chdir())

	return repo.Root
}

func TestInitNoDaemon_ConnectionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	initNoDaemonSetup(t)

	oldAddr := serverAddr
	serverAddr = "http://127.0.0.1:1"
	defer func() { serverAddr = oldAddr }()

	output := captureStdout(t, func() {
		cmd := initCmd()
		cmd.SetArgs([]string{"--no-daemon"})
		_ = cmd.Execute()
	})

	if !strings.Contains(output, "Daemon not running") {
		t.Errorf("expected 'Daemon not running' for connection error, got:\n%s", output)
	}
	if !strings.Contains(output, "Setup incomplete") {
		t.Errorf("expected 'Setup incomplete' banner, got:\n%s", output)
	}
	if strings.Contains(output, "Ready!") {
		t.Errorf("should not show 'Ready!' on connection failure, got:\n%s", output)
	}
}

func TestInitNoDaemon_ServerError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	initNoDaemonSetup(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("database locked"))
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	output := captureStdout(t, func() {
		cmd := initCmd()
		cmd.SetArgs([]string{"--no-daemon"})
		_ = cmd.Execute()
	})

	if !strings.Contains(output, "Warning: failed to register repo") {
		t.Errorf("expected server error warning, got:\n%s", output)
	}
	if !strings.Contains(output, "500") {
		t.Errorf("expected status code 500 in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Setup incomplete") {
		t.Errorf("expected 'Setup incomplete' banner, got:\n%s", output)
	}
	if strings.Contains(output, "Ready!") {
		t.Errorf("should not show 'Ready!' on server error, got:\n%s", output)
	}
}

func TestInitNoDaemon_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	initNoDaemonSetup(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	output := captureStdout(t, func() {
		cmd := initCmd()
		cmd.SetArgs([]string{"--no-daemon"})
		_ = cmd.Execute()
	})

	if !strings.Contains(output, "Repo registered with running daemon") {
		t.Errorf("expected success registration message, got:\n%s", output)
	}
	if !strings.Contains(output, "Ready!") {
		t.Errorf("expected 'Ready!' banner on success, got:\n%s", output)
	}
	if strings.Contains(output, "Setup incomplete") {
		t.Errorf("should not show 'Setup incomplete' on success, got:\n%s", output)
	}
}

func TestInitInstallsPostRewriteHookOnUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	root := initNoDaemonSetup(t)

	// Pre-install a current post-commit hook so init sees it
	hooksDir := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookContent := githook.GeneratePostCommit()
	if err := os.WriteFile(filepath.Join(hooksDir, "post-commit"), []byte(hookContent), 0755); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	captureStdout(t, func() {
		cmd := initCmd()
		cmd.SetArgs([]string{"--no-daemon"})
		_ = cmd.Execute()
	})

	prHookPath := filepath.Join(hooksDir, "post-rewrite")
	content, err := os.ReadFile(prHookPath)
	if err != nil {
		t.Fatalf("post-rewrite hook should be installed: %v", err)
	}
	if !strings.Contains(string(content), "remap --quiet") {
		t.Error("post-rewrite hook should contain 'remap --quiet'")
	}
}

func TestInitCmdWarnsOnNonShellHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	root := initNoDaemonSetup(t)

	// Install a non-shell post-commit hook
	hooksDir := filepath.Join(root, ".git", "hooks")
	os.MkdirAll(hooksDir, 0755)
	os.WriteFile(
		filepath.Join(hooksDir, "post-commit"),
		[]byte("#!/usr/bin/env python3\nprint('hello')\n"),
		0755,
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	output := captureStdout(t, func() {
		cmd := initCmd()
		cmd.SetArgs([]string{"--no-daemon"})
		_ = cmd.Execute()
	})

	// Should warn about non-shell hook but still succeed
	if !strings.Contains(output, "non-shell interpreter") {
		t.Errorf("expected non-shell warning, got:\n%s", output)
	}
	if !strings.Contains(output, "Ready!") {
		t.Errorf("init should still succeed with Ready!, got:\n%s", output)
	}
}

func TestInitCmdFailsOnHookWriteError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	root := initNoDaemonSetup(t)

	// Make hooks directory unwritable to trigger a real error
	hooksDir := filepath.Join(root, ".git", "hooks")
	os.MkdirAll(hooksDir, 0755)
	os.Chmod(hooksDir, 0555)
	t.Cleanup(func() { os.Chmod(hooksDir, 0755) })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	cmd := initCmd()
	cmd.SetArgs([]string{"--no-daemon"})
	err := cmd.Execute()

	// Should fail on permission error, not warn-and-continue
	if err == nil {
		t.Fatal("expected error for unwritable hooks directory")
	}
	if !strings.Contains(err.Error(), "install hooks") {
		t.Errorf("error should mention install hooks: %v", err)
	}
}

func TestInitCmdFailsOnMixedHookErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	root := initNoDaemonSetup(t)

	// Non-shell post-commit (produces ErrNonShellHook) +
	// unwritable post-rewrite (produces real write error).
	// InstallAll joins both; init must treat as fatal.
	hooksDir := filepath.Join(root, ".git", "hooks")
	os.MkdirAll(hooksDir, 0755)
	os.WriteFile(
		filepath.Join(hooksDir, "post-commit"),
		[]byte("#!/usr/bin/env python3\nprint('hello')\n"),
		0755,
	)
	// Create post-rewrite as a directory so writing it fails.
	os.MkdirAll(
		filepath.Join(hooksDir, "post-rewrite"), 0755,
	)

	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}),
	)
	defer ts.Close()

	oldAddr := serverAddr
	serverAddr = ts.URL
	defer func() { serverAddr = oldAddr }()

	cmd := initCmd()
	cmd.SetArgs([]string{"--no-daemon"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error when mixed errors from InstallAll")
	}
	if !strings.Contains(err.Error(), "install hooks") {
		t.Errorf("error should mention install hooks: %v", err)
	}
}
