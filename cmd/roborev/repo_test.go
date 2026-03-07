package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveRepoIdentifier_Simple(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get wd: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"name unchanged", "my-project", "my-project"},
		{"name with slash unchanged", "org/project", "org/project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertPath(t, resolveRepoIdentifier(wd, tt.input), tt.want)
		})
	}
}

func TestResolveRepoIdentifier_InaccessiblePath(t *testing.T) {
	t.Parallel()

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
	t.Cleanup(func() { _ = os.Chmod(orgDir, 0755) })

	// The test expects "org/project" because it can't stat "org" to see if it's a repo,
	// so it treats it as a name string.
	assertPath(t, resolveRepoIdentifier(tmpDir, "org/project"), "org/project")
}

func TestResolveRepoIdentifier_GitRepo(t *testing.T) {
	t.Parallel()

	root := newTestGitRepo(t).Dir
	// Create common structure: root/sub/dir
	subDir := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Also create a non-git temp dir for that one case
	nonGitDir := t.TempDir()
	resolvedNonGit, err := filepath.EvalSymlinks(nonGitDir)
	if err != nil {
		t.Fatalf("Failed to resolve symlinks: %v", err)
	}

	tests := []struct {
		name  string
		dir   string // directory to execute from
		input string
		want  string
	}{
		{
			name:  "dot in subdir",
			dir:   filepath.Join(root, "sub", "dir"),
			input: ".",
			want:  root,
		},
		{
			name:  "relative path",
			dir:   filepath.Join(root, "sub"),
			input: "./",
			want:  root,
		},
		{
			name:  "absolute path",
			dir:   root,
			input: subDir,
			want:  root,
		},
		{
			name:  "parent traversal",
			dir:   filepath.Join(root, "sub", "dir"),
			input: "..",
			want:  root,
		},
		{
			name:  "non-git path returns absolute path",
			dir:   resolvedNonGit,
			input: ".",
			want:  resolvedNonGit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveRepoIdentifier(tt.dir, tt.input)
			assertPath(t, got, tt.want)
		})
	}
}

func assertPath(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
