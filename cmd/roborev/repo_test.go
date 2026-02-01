package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// setupGitRepo creates a temporary git repository with symlinks resolved.
// Returns the absolute path to the repo root.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping test")
	}
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("Failed to resolve symlinks: %v", err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = resolved
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to run git %v: %v", args, err)
		}
	}
	return resolved
}

// chdir changes to dir and registers a t.Cleanup to restore the original directory.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

// makeDir creates a subdirectory under parent and returns its path.
func makeDir(t *testing.T, parent string, parts ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{parent}, parts...)...)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}
	return dir
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
		if runtime.GOOS == "windows" {
			t.Skip("skipping permission test on Windows")
		}
		if os.Getuid() == 0 {
			t.Skip("skipping permission test when running as root")
		}

		tmpDir := t.TempDir()
		orgDir := filepath.Join(tmpDir, "org")
		if err := os.Mkdir(orgDir, 0755); err != nil {
			t.Fatalf("Failed to create org dir: %v", err)
		}
		if err := os.Chmod(orgDir, 0000); err != nil {
			t.Fatalf("Failed to chmod: %v", err)
		}
		defer os.Chmod(orgDir, 0755)

		chdir(t, tmpDir)

		result := resolveRepoIdentifier("org/project")
		if result != "org/project" {
			t.Errorf("Expected 'org/project' (name), got %q", result)
		}
	})

	t.Run("resolves dot to git root", func(t *testing.T) {
		tmpDir := setupGitRepo(t)
		subDir := makeDir(t, tmpDir, "sub", "dir")
		chdir(t, subDir)

		result := resolveRepoIdentifier(".")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("resolves relative path to git root", func(t *testing.T) {
		tmpDir := setupGitRepo(t)
		subDir := makeDir(t, tmpDir, "sub")
		chdir(t, subDir)

		result := resolveRepoIdentifier("./")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("resolves absolute path to git root", func(t *testing.T) {
		tmpDir := setupGitRepo(t)
		subDir := makeDir(t, tmpDir, "sub", "dir")

		result := resolveRepoIdentifier(subDir)
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("non-git path returns absolute path", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolved, err := filepath.EvalSymlinks(tmpDir)
		if err != nil {
			t.Fatalf("Failed to resolve symlinks: %v", err)
		}
		chdir(t, resolved)

		result := resolveRepoIdentifier(".")
		if result != resolved {
			t.Errorf("Expected %q (abs path), got %q", resolved, result)
		}
	})

	t.Run("dotdot resolves correctly", func(t *testing.T) {
		tmpDir := setupGitRepo(t)
		subDir := makeDir(t, tmpDir, "a", "b", "c")
		chdir(t, subDir)

		result := resolveRepoIdentifier("..")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})
}
