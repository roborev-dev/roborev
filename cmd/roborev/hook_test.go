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
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(generateHookContent())
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
		repo := testutil.NewTestRepo(t)
		mixed := "#!/bin/sh\necho 'before'\necho 'after'\n" +
			generateHookContent()
		repo.WriteHook(mixed)
		defer repo.Chdir()()

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should exist with roborev snippet removed
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
		repo.WriteHook(generateHookContent())
		// Also install post-rewrite hook
		prPath := filepath.Join(repo.HooksDir, "post-rewrite")
		os.WriteFile(
			prPath,
			[]byte(generatePostRewriteHookContent()),
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
		// Only install post-rewrite, no post-commit
		prPath := filepath.Join(repo.HooksDir, "post-rewrite")
		os.MkdirAll(repo.HooksDir, 0755)
		os.WriteFile(
			prPath,
			[]byte(generatePostRewriteHookContent()),
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

func TestHookNeedsUpgrade(t *testing.T) {
	t.Run("outdated hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\n# roborev post-commit hook\nroborev enqueue\n")
		if !hookNeedsUpgrade(repo.Root, "post-commit", hookVersionMarker) {
			t.Error("should detect outdated hook")
		}
	})

	t.Run("current hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\n# roborev post-commit hook v2 - auto-reviews every commit\nroborev enqueue\n")
		if hookNeedsUpgrade(repo.Root, "post-commit", hookVersionMarker) {
			t.Error("should not flag current hook as outdated")
		}
	})

	t.Run("no hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if hookNeedsUpgrade(repo.Root, "post-commit", hookVersionMarker) {
			t.Error("should not flag missing hook as outdated")
		}
	})

	t.Run("non-roborev hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if hookNeedsUpgrade(repo.Root, "post-commit", hookVersionMarker) {
			t.Error("should not flag non-roborev hook as outdated")
		}
	})

	t.Run("post-rewrite outdated", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		hooksDir := filepath.Join(repo.Root, ".git", "hooks")
		os.MkdirAll(hooksDir, 0755)
		os.WriteFile(filepath.Join(hooksDir, "post-rewrite"),
			[]byte("#!/bin/sh\n# roborev hook\nroborev remap\n"), 0755)
		if !hookNeedsUpgrade(repo.Root, "post-rewrite", postRewriteHookVersionMarker) {
			t.Error("should detect outdated post-rewrite hook")
		}
	})

	t.Run("post-rewrite current", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		hooksDir := filepath.Join(repo.Root, ".git", "hooks")
		os.MkdirAll(hooksDir, 0755)
		os.WriteFile(filepath.Join(hooksDir, "post-rewrite"),
			[]byte("#!/bin/sh\n# roborev post-rewrite hook v1\nroborev remap\n"), 0755)
		if hookNeedsUpgrade(repo.Root, "post-rewrite", postRewriteHookVersionMarker) {
			t.Error("should not flag current post-rewrite hook")
		}
	})
}

func TestHookMissing(t *testing.T) {
	t.Run("missing post-rewrite with roborev post-commit", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\n# roborev post-commit hook v2\nroborev enqueue\n")
		if !hookMissing(repo.Root, "post-rewrite") {
			t.Error("should detect missing post-rewrite when post-commit has roborev")
		}
	})

	t.Run("no post-commit hook at all", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if hookMissing(repo.Root, "post-rewrite") {
			t.Error("should not warn when post-commit is not installed")
		}
	})

	t.Run("post-rewrite exists with roborev", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\n# roborev post-commit hook v2\nroborev enqueue\n")
		hooksDir := filepath.Join(repo.Root, ".git", "hooks")
		os.WriteFile(filepath.Join(hooksDir, "post-rewrite"),
			[]byte("#!/bin/sh\n# roborev post-rewrite hook v1\nroborev remap\n"), 0755)
		if hookMissing(repo.Root, "post-rewrite") {
			t.Error("should not warn when post-rewrite has roborev content")
		}
	})

	t.Run("non-roborev post-commit", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if hookMissing(repo.Root, "post-rewrite") {
			t.Error("should not warn when post-commit is not roborev")
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
	if !strings.Contains(string(content), postRewriteHookVersionMarker) {
		t.Error("post-rewrite hook should contain version marker")
	}
}

func TestInstallOrUpgradeHook(t *testing.T) {
	t.Run("appends to existing non-roborev hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-commit")
		existing := "#!/bin/sh\necho 'custom logic'\n"
		if err := os.WriteFile(hookPath, []byte(existing), 0755); err != nil {
			t.Fatal(err)
		}

		err := installOrUpgradeHook(
			repo.HooksDir, "post-commit",
			hookVersionMarker, generateHookContent, false,
		)
		if err != nil {
			t.Fatalf("installOrUpgradeHook: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		if !strings.Contains(contentStr, "echo 'custom logic'") {
			t.Error("original content should be preserved")
		}
		if !strings.Contains(contentStr, hookVersionMarker) {
			t.Error("roborev snippet should be appended")
		}
	})

	t.Run("skips current version", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		current := generatePostRewriteHookContent()
		if err := os.WriteFile(hookPath, []byte(current), 0755); err != nil {
			t.Fatal(err)
		}

		err := installOrUpgradeHook(
			repo.HooksDir, "post-rewrite",
			postRewriteHookVersionMarker,
			generatePostRewriteHookContent, false,
		)
		if err != nil {
			t.Fatalf("installOrUpgradeHook: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		if string(content) != current {
			t.Error("current hook should not be modified")
		}
	})

	t.Run("upgrades outdated roborev hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-commit")
		// Simulates an older generated hook (has marker but no version)
		outdated := "#!/bin/sh\n" +
			"# roborev post-commit hook\n" +
			"ROBOREV=\"/usr/local/bin/roborev\"\n" +
			"\"$ROBOREV\" enqueue --quiet 2>/dev/null\n"
		if err := os.WriteFile(hookPath, []byte(outdated), 0755); err != nil {
			t.Fatal(err)
		}

		err := installOrUpgradeHook(
			repo.HooksDir, "post-commit",
			hookVersionMarker, generateHookContent, false,
		)
		if err != nil {
			t.Fatalf("installOrUpgradeHook: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		if !strings.Contains(contentStr, hookVersionMarker) {
			t.Error("should have new version marker")
		}
		// Old marker should be gone
		if strings.Contains(contentStr, "# roborev post-commit hook\n") {
			t.Error("old marker should be removed")
		}
	})

	t.Run("upgrades mixed hook preserving user content", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		mixed := "#!/bin/sh\necho 'user code'\n# roborev post-rewrite hook\nROBOREV=\"/usr/bin/roborev\"\n\"$ROBOREV\" remap --quiet 2>/dev/null\n"
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		err := installOrUpgradeHook(
			repo.HooksDir, "post-rewrite",
			postRewriteHookVersionMarker,
			generatePostRewriteHookContent, false,
		)
		if err != nil {
			t.Fatalf("installOrUpgradeHook: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		if !strings.Contains(contentStr, "echo 'user code'") {
			t.Error("user content should be preserved")
		}
		if !strings.Contains(contentStr, postRewriteHookVersionMarker) {
			t.Error("should have new version marker")
		}
	})

	t.Run("force overwrites existing hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-commit")
		existing := "#!/bin/sh\necho 'custom'\n"
		if err := os.WriteFile(hookPath, []byte(existing), 0755); err != nil {
			t.Fatal(err)
		}

		err := installOrUpgradeHook(
			repo.HooksDir, "post-commit",
			hookVersionMarker, generateHookContent, true,
		)
		if err != nil {
			t.Fatalf("installOrUpgradeHook: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		if strings.Contains(contentStr, "echo 'custom'") {
			t.Error("force should overwrite, not append")
		}
		if !strings.Contains(contentStr, hookVersionMarker) {
			t.Error("should have roborev content")
		}
	})
}

func TestRemoveRoborevFromHook(t *testing.T) {
	t.Run("generated hook is deleted entirely", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		hookContent := generatePostRewriteHookContent()
		if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			t.Error("generated hook should have been deleted entirely")
		}
	})

	t.Run("mixed hook preserves non-roborev content", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		mixed := "#!/bin/sh\necho 'custom logic'\n" + generatePostRewriteHookContent()
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		contentStr := string(content)
		if strings.Contains(strings.ToLower(contentStr), "roborev") {
			t.Errorf("roborev content should be removed, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, "echo 'custom logic'") {
			t.Error("custom content should be preserved")
		}
		// Verify no orphaned fi
		if strings.Contains(contentStr, "\nfi\n") || strings.HasSuffix(strings.TrimSpace(contentStr), "fi") {
			t.Errorf("should not have orphaned fi, got:\n%s", contentStr)
		}
	})

	t.Run("custom line mentioning roborev before snippet", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		// Custom line mentions roborev but isn't the generated marker
		mixed := "#!/bin/sh\necho 'notify roborev team'\n" +
			generatePostRewriteHookContent()
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		contentStr := string(content)
		// The custom line should be preserved
		if !strings.Contains(contentStr, "notify roborev team") {
			t.Error("custom line mentioning roborev should be preserved")
		}
		// The generated snippet should be removed
		if strings.Contains(contentStr, "remap --quiet") {
			t.Errorf("generated snippet should be removed, got:\n%s", contentStr)
		}
	})

	t.Run("custom if-block after snippet is preserved", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		// Snippet followed by user's own if-block
		mixed := "#!/bin/sh\n" +
			generatePostRewriteHookContent() +
			"if [ -f .notify ]; then\n" +
			"    echo 'send notification'\n" +
			"fi\n"
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		contentStr := string(content)
		// User's if-block must survive
		if !strings.Contains(contentStr, "send notification") {
			t.Errorf("user if-block should be preserved, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, "if [ -f .notify ]") {
			t.Errorf("user if-statement should be preserved, got:\n%s", contentStr)
		}
		// No orphaned fi
		lines := strings.Split(contentStr, "\n")
		ifCount, fiCount := 0, 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "if ") {
				ifCount++
			}
			if trimmed == "fi" {
				fiCount++
			}
		}
		if ifCount != fiCount {
			t.Errorf("if/fi mismatch: %d if vs %d fi in:\n%s",
				ifCount, fiCount, contentStr)
		}
		// Generated snippet should be gone
		if strings.Contains(contentStr, "remap --quiet") {
			t.Errorf("generated snippet should be removed, got:\n%s", contentStr)
		}
	})

	t.Run("custom comment starting with # roborev is preserved", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		// User comment starts with "# roborev" but isn't a generated marker
		mixed := "#!/bin/sh\n# roborev notes: this hook was customized\necho 'custom'\n" +
			generatePostRewriteHookContent()
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		contentStr := string(content)
		if !strings.Contains(contentStr, "roborev notes") {
			t.Error("custom comment should be preserved")
		}
		if !strings.Contains(contentStr, "echo 'custom'") {
			t.Error("custom echo should be preserved")
		}
		if strings.Contains(contentStr, "remap --quiet") {
			t.Errorf("generated snippet should be removed, got:\n%s", contentStr)
		}
	})

	t.Run("post-snippet user line mentioning roborev is preserved", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		// Snippet followed by user logic that mentions roborev
		mixed := "#!/bin/sh\n" +
			generatePostRewriteHookContent() +
			"echo 'roborev hook finished'\n" +
			"logger 'roborev done'\n"
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		contentStr := string(content)
		if !strings.Contains(contentStr, "roborev hook finished") {
			t.Errorf("user line mentioning roborev should be preserved, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, "roborev done") {
			t.Errorf("user logger line should be preserved, got:\n%s", contentStr)
		}
		if strings.Contains(contentStr, "remap --quiet") {
			t.Errorf("generated snippet should be removed, got:\n%s", contentStr)
		}
	})

	t.Run("user $ROBOREV line after snippet is preserved", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		// User has their own "$ROBOREV" line (e.g. version check) after snippet
		mixed := "#!/bin/sh\n" +
			generatePostRewriteHookContent() +
			"\"$ROBOREV\" --version\n"
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		contentStr := string(content)
		if !strings.Contains(contentStr, "\"$ROBOREV\" --version") {
			t.Errorf("user $ROBOREV line should be preserved, got:\n%s", contentStr)
		}
		if strings.Contains(contentStr, "remap --quiet") {
			t.Errorf("generated snippet should be removed, got:\n%s", contentStr)
		}
	})

	t.Run("user $ROBOREV enqueue/remap lines after snippet are preserved", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		// User added their own enqueue/remap invocations with different flags
		mixed := "#!/bin/sh\n" +
			generatePostRewriteHookContent() +
			"\"$ROBOREV\" enqueue --dry-run\n" +
			"\"$ROBOREV\" remap --verbose\n"
		if err := os.WriteFile(hookPath, []byte(mixed), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		contentStr := string(content)
		if !strings.Contains(contentStr, "enqueue --dry-run") {
			t.Errorf("user enqueue line should be preserved, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, "remap --verbose") {
			t.Errorf("user remap line should be preserved, got:\n%s", contentStr)
		}
		if strings.Contains(contentStr, "remap --quiet") {
			t.Errorf("generated snippet should be removed, got:\n%s", contentStr)
		}
	})

	t.Run("no-op if hook has no roborev content", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		hookContent := "#!/bin/sh\necho 'unrelated'\n"
		if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
			t.Fatal(err)
		}

		removeRoborevFromHook(hookPath)

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		if string(content) != hookContent {
			t.Errorf("hook should be unchanged, got:\n%s", content)
		}
	})
}

func TestInitInstallsPostRewriteHookOnUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script stub, skipping on Windows")
	}
	root := initNoDaemonSetup(t)

	// Pre-install a current post-commit hook so init takes the
	// "already installed" goto path
	hooksDir := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookContent := generateHookContent()
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

	// Verify post-rewrite hook was installed despite post-commit
	// taking the "already installed" path
	prHookPath := filepath.Join(hooksDir, "post-rewrite")
	content, err := os.ReadFile(prHookPath)
	if err != nil {
		t.Fatalf("post-rewrite hook should be installed even when "+
			"post-commit is already current: %v", err)
	}
	if !strings.Contains(string(content), "remap --quiet") {
		t.Error("post-rewrite hook should contain 'remap --quiet'")
	}
}

func TestGeneratePostRewriteHookContent(t *testing.T) {
	content := generatePostRewriteHookContent()

	if !strings.HasPrefix(content, "#!/bin/sh\n") {
		t.Error("hook should start with #!/bin/sh")
	}
	if !strings.Contains(content, postRewriteHookVersionMarker) {
		t.Error("hook should contain version marker")
	}
	if !strings.Contains(content, "remap --quiet") {
		t.Error("hook should call remap --quiet")
	}
}
