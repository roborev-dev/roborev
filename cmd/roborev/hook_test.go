package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUninstallHookCmd(t *testing.T) {
	// Helper to create a git repo with an optional hook
	setupRepo := func(t *testing.T, hookContent string) (repoPath string, hookPath string) {
		tmpDir := t.TempDir()

		// Initialize git repo
		cmd := exec.Command("git", "init")
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init failed: %v\n%s", err, out)
		}

		hookPath = filepath.Join(tmpDir, ".git", "hooks", "post-commit")

		if hookContent != "" {
			if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
				t.Fatal(err)
			}
		}

		return tmpDir, hookPath
	}

	t.Run("hook missing", func(t *testing.T) {
		repoPath, hookPath := setupRepo(t, "")

		// Change to repo dir for the command
		origDir, _ := os.Getwd()
		os.Chdir(repoPath)
		defer os.Chdir(origDir)

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should still not exist
		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			t.Error("Hook file should not exist")
		}
	})

	t.Run("hook without roborev", func(t *testing.T) {
		hookContent := "#!/bin/bash\necho 'other hook'\n"
		repoPath, hookPath := setupRepo(t, hookContent)

		origDir, _ := os.Getwd()
		os.Chdir(repoPath)
		defer os.Chdir(origDir)

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should be unchanged
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("Failed to read hook: %v", err)
		}
		if string(content) != hookContent {
			t.Errorf("Hook content changed: got %q, want %q", string(content), hookContent)
		}
	})

	t.Run("hook with roborev only - removes file", func(t *testing.T) {
		hookContent := "#!/bin/bash\n# roborev auto-commit hook\nroborev enqueue\n"
		repoPath, hookPath := setupRepo(t, hookContent)

		origDir, _ := os.Getwd()
		os.Chdir(repoPath)
		defer os.Chdir(origDir)

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should be removed entirely
		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			t.Error("Hook file should have been removed")
		}
	})

	t.Run("hook with roborev and other commands - preserves others", func(t *testing.T) {
		hookContent := "#!/bin/bash\necho 'before'\nroborev enqueue\necho 'after'\n"
		repoPath, hookPath := setupRepo(t, hookContent)

		origDir, _ := os.Getwd()
		os.Chdir(repoPath)
		defer os.Chdir(origDir)

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should exist with roborev line removed
		content, err := os.ReadFile(hookPath)
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
		repoPath, hookPath := setupRepo(t, hookContent)

		origDir, _ := os.Getwd()
		os.Chdir(repoPath)
		defer os.Chdir(origDir)

		cmd := uninstallHookCmd()
		err := cmd.Execute()
		if err != nil {
			t.Fatalf("uninstall-hook failed: %v", err)
		}

		// Hook should be removed (only had RoboRev content)
		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
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

	// Create a temp git repo WITHOUT a hooks directory
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(),
		"HOME="+tmpHome,
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Remove .git/hooks directory to simulate the problematic scenario
	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	if err := os.RemoveAll(hooksDir); err != nil {
		t.Fatalf("Failed to remove hooks directory: %v", err)
	}

	// Verify hooks directory doesn't exist
	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	// Create a roborevd binary in PATH for the test (init tries to start daemon)
	// We'll use a fake one that does nothing
	binDir := filepath.Join(tmpHome, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	fakeDaemon := filepath.Join(binDir, "roborevd")
	fakeScript := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(fakeDaemon, []byte(fakeScript), 0755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	defer os.Setenv("PATH", origPath)

	// Change to the test repo directory before running
	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// Build init command
	initCommand := initCmd()
	initCommand.SetArgs([]string{"--agent", "test"})
	err := initCommand.Execute()

	// Should succeed (not fail with "no such file or directory")
	if err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	// Verify hooks directory was created
	if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
		t.Error("hooks directory was not created")
	}

	// Verify hook file was created
	hookPath := filepath.Join(hooksDir, "post-commit")
	if _, err := os.Stat(hookPath); os.IsNotExist(err) {
		t.Error("post-commit hook was not created")
	}

	// Verify hook is executable
	info, err := os.Stat(hookPath)
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

	// Test that install-hook creates .git/hooks directory if missing
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Remove .git/hooks directory
	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	if err := os.RemoveAll(hooksDir); err != nil {
		t.Fatalf("Failed to remove hooks directory: %v", err)
	}

	// Verify hooks directory doesn't exist
	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	// Change to the test repo directory
	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// Run install-hook command
	installCmd := installHookCmd()
	installCmd.SetArgs([]string{})
	err := installCmd.Execute()

	if err != nil {
		t.Fatalf("install-hook command failed: %v", err)
	}

	// Verify hooks directory was created
	if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
		t.Error("hooks directory was not created")
	}

	// Verify hook file was created
	hookPath := filepath.Join(hooksDir, "post-commit")
	if _, err := os.Stat(hookPath); os.IsNotExist(err) {
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

	t.Run("enqueue line has quiet and stderr redirect", func(t *testing.T) {
		// Must have exact enqueue line with --quiet, stderr redirect, and background
		found := false
		for _, line := range lines {
			if strings.Contains(line, "enqueue --quiet") &&
				strings.Contains(line, "2>/dev/null") &&
				strings.HasSuffix(strings.TrimSpace(line), "&") {
				found = true
				break
			}
		}
		if !found {
			t.Error("hook should have enqueue line with --quiet, 2>/dev/null, and & on same line")
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
