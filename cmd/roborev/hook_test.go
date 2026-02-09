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

		// Hook should still not exist
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

		// Hook should be unchanged
		content, err := os.ReadFile(repo.HookPath)
		if err != nil {
			t.Fatalf("Failed to read hook: %v", err)
		}
		if string(content) != hookContent {
			t.Errorf("Hook content changed: got %q, want %q", string(content), hookContent)
		}
	})

	t.Run("hook with roborev only - removes file", func(t *testing.T) {
		hookContent := "#!/bin/bash\n# roborev auto-commit hook\nroborev enqueue\n"
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(hookContent)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should be removed entirely
		if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
			t.Error("Hook file should have been removed")
		}
	})

	t.Run("hook with roborev and other commands - preserves others", func(t *testing.T) {
		hookContent := "#!/bin/bash\necho 'before'\nroborev enqueue\necho 'after'\n"
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(hookContent)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should exist with roborev line removed
		content, err := os.ReadFile(repo.HookPath)
		if err != nil {
			t.Fatalf("Failed to read hook: %v", err)
		}

		contentStr := string(content)
		if strings.Contains(strings.ToLower(contentStr), "roborev") {
			t.Error("Hook should not contain roborev")
		}
		if !strings.Contains(contentStr, "echo 'before'") {
			t.Error("Hook should still contain 'echo before'")
		}
		if !strings.Contains(contentStr, "echo 'after'") {
			t.Error("Hook should still contain 'echo after'")
		}
	})

	t.Run("hook with capitalized RoboRev", func(t *testing.T) {
		hookContent := "#!/bin/bash\n# RoboRev hook\nRoboRev enqueue\n"
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(hookContent)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should be removed (only had RoboRev content)
		if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
			t.Error("Hook file should have been removed")
		}
	})
}

func TestInitCmdCreatesHooksDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}

	// Override HOME to prevent reading real daemon.json
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := testutil.NewTestRepo(t)
	repo.RemoveHooksDir()

	// Verify hooks directory doesn't exist
	if _, err := os.Stat(repo.HooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	// Create a fake roborev binary in PATH
	defer testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n")()

	defer repo.Chdir()()

	// Build init command
	initCommand := initCmd()
	initCommand.SetArgs([]string{"--agent", "test"})
	err := initCommand.Execute()

	// Should succeed (not fail with "no such file or directory")
	if err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	// Verify hooks directory was created
	if _, err := os.Stat(repo.HooksDir); os.IsNotExist(err) {
		t.Error("hooks directory was not created")
	}

	// Verify hook file was created
	if _, err := os.Stat(repo.HookPath); os.IsNotExist(err) {
		t.Error("post-commit hook was not created")
	}

	// Verify hook is executable
	info, err := os.Stat(repo.HookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("post-commit hook is not executable")
	}
}

func TestInstallHookCmdCreatesHooksDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test checks Unix exec bits, skipping on Windows")
	}

	repo := testutil.NewTestRepo(t)
	repo.RemoveHooksDir()

	// Verify hooks directory doesn't exist
	if _, err := os.Stat(repo.HooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	defer repo.Chdir()()

	// Run install-hook command
	installCmd := installHookCmd()
	installCmd.SetArgs([]string{})
	err := installCmd.Execute()

	if err != nil {
		t.Fatalf("install-hook command failed: %v", err)
	}

	// Verify hooks directory was created
	if _, err := os.Stat(repo.HooksDir); os.IsNotExist(err) {
		t.Error("hooks directory was not created")
	}

	// Verify hook file was created
	if _, err := os.Stat(repo.HookPath); os.IsNotExist(err) {
		t.Error("post-commit hook was not created")
	}
}

func TestGenerateHookContent(t *testing.T) {
	content := generateHookContent()
	lines := strings.Split(content, "\n")

	t.Run("has shebang", func(t *testing.T) {
		if !strings.HasPrefix(content, "#!/bin/sh\n") {
			t.Error("hook should start with #!/bin/sh")
		}
	})

	t.Run("has roborev comment", func(t *testing.T) {
		if !strings.Contains(content, "# roborev") {
			t.Error("hook should contain roborev comment for detection")
		}
	})

	t.Run("baked path comes first", func(t *testing.T) {
		// Security: baked path should be set before any PATH lookup
		bakedIdx := -1
		pathIdx := -1
		for i, line := range lines {
			if strings.HasPrefix(line, "ROBOREV=") && !strings.Contains(line, "command -v") {
				bakedIdx = i
			}
			if strings.Contains(line, "command -v roborev") {
				pathIdx = i
			}
		}
		if bakedIdx == -1 {
			t.Error("hook should have baked ROBOREV= assignment")
		}
		if pathIdx == -1 {
			t.Error("hook should have PATH fallback via command -v")
		}
		if bakedIdx > pathIdx {
			t.Error("baked path should come before PATH lookup for security")
		}
	})

	t.Run("enqueue line has quiet and stderr redirect without background", func(t *testing.T) {
		found := false
		for _, line := range lines {
			if strings.Contains(line, "enqueue --quiet") &&
				strings.Contains(line, "2>/dev/null") &&
				!strings.HasSuffix(strings.TrimSpace(line), "&") {
				found = true
				break
			}
		}
		if !found {
			t.Error("hook should have enqueue line with --quiet and 2>/dev/null but no trailing &")
		}
	})

	t.Run("has version marker", func(t *testing.T) {
		if !strings.Contains(content, "hook v2") {
			t.Error("hook should contain version marker 'hook v2'")
		}
	})

	t.Run("baked path is quoted", func(t *testing.T) {
		// The baked path should be properly quoted to handle spaces
		for _, line := range lines {
			if strings.HasPrefix(line, "ROBOREV=") && !strings.Contains(line, "command -v") {
				// Should be ROBOREV="/path/to/roborev" or ROBOREV="roborev"
				if !strings.Contains(line, `ROBOREV="`) {
					t.Errorf("baked path should be quoted, got: %s", line)
				}
				break
			}
		}
	})

	t.Run("baked path is absolute", func(t *testing.T) {
		// os.Executable() returns absolute path; verify it's baked correctly
		for _, line := range lines {
			if strings.HasPrefix(line, "ROBOREV=") && !strings.Contains(line, "command -v") {
				// Extract the path from ROBOREV="/path/here"
				start := strings.Index(line, `"`)
				end := strings.LastIndex(line, `"`)
				if start != -1 && end > start {
					path := line[start+1 : end]
					if !filepath.IsAbs(path) {
						t.Errorf("baked path should be absolute, got: %s", path)
					}
				}
				break
			}
		}
	})
}

func TestInitCmdUpgradesOutdatedHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}

	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := testutil.NewTestRepo(t)

	// Write a realistic old-style hook (no version marker, has &, includes if/fi block)
	oldHook := "#!/bin/sh\n# roborev post-commit hook - auto-reviews every commit\nROBOREV=\"/usr/local/bin/roborev\"\nif [ ! -x \"$ROBOREV\" ]; then\n    ROBOREV=$(command -v roborev 2>/dev/null)\n    [ -z \"$ROBOREV\" ] || [ ! -x \"$ROBOREV\" ] && exit 0\nfi\n\"$ROBOREV\" enqueue --quiet 2>/dev/null &\n"
	repo.WriteHook(oldHook)

	defer testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n")()
	defer repo.Chdir()()

	initCommand := initCmd()
	initCommand.SetArgs([]string{"--agent", "test"})
	err := initCommand.Execute()
	if err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	content, err := os.ReadFile(repo.HookPath)
	if err != nil {
		t.Fatalf("Failed to read hook: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "hook v2") {
		t.Error("upgraded hook should contain version marker")
	}
	if strings.Contains(contentStr, "2>/dev/null &") {
		t.Error("upgraded hook should not have backgrounded enqueue")
	}
	if !strings.Contains(contentStr, "2>/dev/null\n") {
		t.Error("upgraded hook should still have enqueue line (without &)")
	}
	// Verify the if/fi block is preserved intact (no stray fi)
	if !strings.Contains(contentStr, "if [ ! -x") {
		t.Error("upgraded hook should preserve the if block")
	}
}

func TestInitCmdPreservesOtherHooksOnUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}

	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := testutil.NewTestRepo(t)

	oldHook := "#!/bin/sh\necho 'my custom hook'\n# roborev post-commit hook - auto-reviews every commit\nROBOREV=\"/usr/local/bin/roborev\"\nif [ ! -x \"$ROBOREV\" ]; then\n    ROBOREV=$(command -v roborev 2>/dev/null)\n    [ -z \"$ROBOREV\" ] || [ ! -x \"$ROBOREV\" ] && exit 0\nfi\n\"$ROBOREV\" enqueue --quiet 2>/dev/null &\n"
	repo.WriteHook(oldHook)

	defer testutil.MockBinaryInPath(t, "roborev", "#!/bin/sh\nexit 0\n")()
	defer repo.Chdir()()

	initCommand := initCmd()
	initCommand.SetArgs([]string{"--agent", "test"})
	err := initCommand.Execute()
	if err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	content, err := os.ReadFile(repo.HookPath)
	if err != nil {
		t.Fatalf("Failed to read hook: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "echo 'my custom hook'") {
		t.Error("upgrade should preserve non-roborev lines")
	}
	if !strings.Contains(contentStr, "hook v2") {
		t.Error("upgrade should contain version marker")
	}
	if strings.Contains(contentStr, "2>/dev/null &") {
		t.Error("upgrade should remove trailing &")
	}
}

func TestHookNeedsUpgrade(t *testing.T) {
	t.Run("outdated hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\n# roborev post-commit hook\nroborev enqueue\n")
		if !hookNeedsUpgrade(repo.Root) {
			t.Error("should detect outdated hook")
		}
	})

	t.Run("current hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\n# roborev post-commit hook v2 - auto-reviews every commit\nroborev enqueue\n")
		if hookNeedsUpgrade(repo.Root) {
			t.Error("should not flag current hook as outdated")
		}
	})

	t.Run("no hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if hookNeedsUpgrade(repo.Root) {
			t.Error("should not flag missing hook as outdated")
		}
	})

	t.Run("non-roborev hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if hookNeedsUpgrade(repo.Root) {
			t.Error("should not flag non-roborev hook as outdated")
		}
	})
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
		// e.g. malformed URL, TLS config error
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

	// Verify it satisfies the error interface and is distinguishable
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
// Returns the repo path and a cleanup function.
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

	// Point to a port that's definitely not listening
	oldAddr := serverAddr
	serverAddr = "http://127.0.0.1:1" // port 1 won't have a daemon
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

	// Spin up a test server that returns 500
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("database locked"))
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

	// Spin up a test server that returns 200
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
