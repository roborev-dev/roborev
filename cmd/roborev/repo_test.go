package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// requireGit skips the test if git is not available
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping test")
	}
}

// initGitRepo initializes a real git repo in the given directory
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	requireGit(t)
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to git init: %v", err)
	}
}

// evalSymlinks resolves symlinks in a path (needed on macOS where /var -> /private/var)
func evalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("Failed to resolve symlinks: %v", err)
	}
	return resolved
}

func TestResolveRepoIdentifier(t *testing.T) {
	t.Run("returns name unchanged", func(t *testing.T) {
		// Names without path separators should be returned as-is
		result := resolveRepoIdentifier("my-project")
		if result != "my-project" {
			t.Errorf("Expected 'my-project', got %q", result)
		}
	})

	t.Run("returns name with slash unchanged when not a path", func(t *testing.T) {
		// Names like "org/project" should be returned as-is if they don't exist on disk
		result := resolveRepoIdentifier("org/project")
		if result != "org/project" {
			t.Errorf("Expected 'org/project', got %q", result)
		}
	})

	t.Run("returns name with slash when path is inaccessible", func(t *testing.T) {
		// Skip on Windows (chmod doesn't make directories unreadable) or if running as root
		if runtime.GOOS == "windows" {
			t.Skip("skipping permission test on Windows")
		}
		if os.Getuid() == 0 {
			t.Skip("skipping permission test when running as root")
		}

		tmpDir := t.TempDir()
		// Create org/project structure
		orgDir := filepath.Join(tmpDir, "org")
		if err := os.Mkdir(orgDir, 0755); err != nil {
			t.Fatalf("Failed to create org dir: %v", err)
		}

		// Make org unreadable
		if err := os.Chmod(orgDir, 0000); err != nil {
			t.Fatalf("Failed to chmod: %v", err)
		}
		defer os.Chmod(orgDir, 0755) // Restore for cleanup

		// Change to tmpDir so "org/project" is a relative path that exists but is unreadable
		origDir, _ := os.Getwd()
		defer os.Chdir(origDir)
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}

		// "org/project" should be treated as a name (not a path) since it's inaccessible
		// and user didn't use explicit path syntax like "./org/project"
		result := resolveRepoIdentifier("org/project")
		if result != "org/project" {
			t.Errorf("Expected 'org/project' (name), got %q", result)
		}
	})

	t.Run("resolves dot to git root", func(t *testing.T) {
		// Create a temp git repo with a subdirectory
		tmpDir := evalSymlinks(t, t.TempDir())
		initGitRepo(t, tmpDir)

		subDir := filepath.Join(tmpDir, "sub", "dir")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}

		// Change to subdirectory
		origDir, _ := os.Getwd()
		defer os.Chdir(origDir)
		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}

		result := resolveRepoIdentifier(".")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("resolves relative path to git root", func(t *testing.T) {
		// Create a temp git repo with a subdirectory
		tmpDir := evalSymlinks(t, t.TempDir())
		initGitRepo(t, tmpDir)

		subDir := filepath.Join(tmpDir, "sub")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}

		// Change to subdirectory
		origDir, _ := os.Getwd()
		defer os.Chdir(origDir)
		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}

		result := resolveRepoIdentifier("./")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("resolves absolute path to git root", func(t *testing.T) {
		// Create a temp git repo with a subdirectory
		tmpDir := evalSymlinks(t, t.TempDir())
		initGitRepo(t, tmpDir)

		subDir := filepath.Join(tmpDir, "sub", "dir")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}

		result := resolveRepoIdentifier(subDir)
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("non-git path returns absolute path", func(t *testing.T) {
		// Create a temp dir without .git
		tmpDir := evalSymlinks(t, t.TempDir())

		// Change to directory
		origDir, _ := os.Getwd()
		defer os.Chdir(origDir)
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}

		result := resolveRepoIdentifier(".")
		if result != tmpDir {
			t.Errorf("Expected %q (abs path), got %q", tmpDir, result)
		}
	})

	t.Run("dotdot resolves correctly", func(t *testing.T) {
		// Create a temp git repo with nested subdirs
		tmpDir := evalSymlinks(t, t.TempDir())
		initGitRepo(t, tmpDir)

		subDir := filepath.Join(tmpDir, "a", "b", "c")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}

		// Change to deepest directory
		origDir, _ := os.Getwd()
		defer os.Chdir(origDir)
		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}

		result := resolveRepoIdentifier("..")
		// ".." from a/b/c = a/b, which is still inside the git repo
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})
}
