package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetHooksPath(t *testing.T) {
	// Create a temp git repo
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	t.Run("default hooks path", func(t *testing.T) {
		hooksPath, err := GetHooksPath(tmpDir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		// Should be absolute
		if !filepath.IsAbs(hooksPath) {
			t.Errorf("hooks path should be absolute, got: %s", hooksPath)
		}

		// Should end with .git/hooks (normalize for cross-platform)
		cleanPath := filepath.Clean(hooksPath)
		expectedSuffix := filepath.Join(".git", "hooks")
		if !strings.HasSuffix(cleanPath, expectedSuffix) {
			t.Errorf("hooks path should end with %s, got: %s", expectedSuffix, cleanPath)
		}

		// Should be under tmpDir (use filepath.Rel for robust check)
		rel, err := filepath.Rel(tmpDir, hooksPath)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			t.Errorf("hooks path should be under %s, got: %s", tmpDir, hooksPath)
		}
	})

	t.Run("custom core.hooksPath absolute", func(t *testing.T) {
		// Create a custom hooks directory
		customHooksDir := filepath.Join(tmpDir, "my-hooks")
		if err := os.MkdirAll(customHooksDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Set core.hooksPath to absolute path
		cmd := exec.Command("git", "config", "core.hooksPath", customHooksDir)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config failed: %v\n%s", err, out)
		}

		hooksPath, err := GetHooksPath(tmpDir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		// Should return the custom absolute path
		if hooksPath != customHooksDir {
			t.Errorf("expected %s, got %s", customHooksDir, hooksPath)
		}

		// Reset for other tests
		cmd = exec.Command("git", "config", "--unset", "core.hooksPath")
		cmd.Dir = tmpDir
		cmd.Run() // ignore error if not set
	})

	t.Run("custom core.hooksPath relative", func(t *testing.T) {
		// Set core.hooksPath to relative path
		cmd := exec.Command("git", "config", "core.hooksPath", "custom-hooks")
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config failed: %v\n%s", err, out)
		}

		hooksPath, err := GetHooksPath(tmpDir)
		if err != nil {
			t.Fatalf("GetHooksPath failed: %v", err)
		}

		// Should be made absolute
		if !filepath.IsAbs(hooksPath) {
			t.Errorf("hooks path should be absolute, got: %s", hooksPath)
		}

		// Should resolve to tmpDir/custom-hooks
		expected := filepath.Join(tmpDir, "custom-hooks")
		if hooksPath != expected {
			t.Errorf("expected %s, got %s", expected, hooksPath)
		}
	})
}

func TestIsRebaseInProgress(t *testing.T) {
	// Create a temp git repo
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Configure git user for commits
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	t.Run("no rebase", func(t *testing.T) {
		if IsRebaseInProgress(tmpDir) {
			t.Error("expected no rebase in progress")
		}
	})

	t.Run("rebase-merge directory", func(t *testing.T) {
		// Simulate interactive rebase by creating rebase-merge directory
		rebaseMerge := filepath.Join(tmpDir, ".git", "rebase-merge")
		if err := os.MkdirAll(rebaseMerge, 0755); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(rebaseMerge)

		if !IsRebaseInProgress(tmpDir) {
			t.Error("expected rebase in progress with rebase-merge")
		}
	})

	t.Run("rebase-apply directory", func(t *testing.T) {
		// Simulate git am / regular rebase by creating rebase-apply directory
		rebaseApply := filepath.Join(tmpDir, ".git", "rebase-apply")
		if err := os.MkdirAll(rebaseApply, 0755); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(rebaseApply)

		if !IsRebaseInProgress(tmpDir) {
			t.Error("expected rebase in progress with rebase-apply")
		}
	})

	t.Run("non-repo returns false", func(t *testing.T) {
		nonRepo := t.TempDir()
		if IsRebaseInProgress(nonRepo) {
			t.Error("expected false for non-repo")
		}
	})
}
