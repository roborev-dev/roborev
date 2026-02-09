package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
		tmpDir := newTestGitRepo(t).Dir
		subDir := filepath.Join(tmpDir, "sub", "dir")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		chdir(t, subDir)

		result := resolveRepoIdentifier(".")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("resolves relative path to git root", func(t *testing.T) {
		tmpDir := newTestGitRepo(t).Dir
		subDir := filepath.Join(tmpDir, "sub")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		chdir(t, subDir)

		result := resolveRepoIdentifier("./")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})

	t.Run("resolves absolute path to git root", func(t *testing.T) {
		tmpDir := newTestGitRepo(t).Dir
		subDir := filepath.Join(tmpDir, "sub", "dir")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

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
		tmpDir := newTestGitRepo(t).Dir
		subDir := filepath.Join(tmpDir, "a", "b", "c")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		chdir(t, subDir)

		result := resolveRepoIdentifier("..")
		if result != tmpDir {
			t.Errorf("Expected %q (git root), got %q", tmpDir, result)
		}
	})
}
