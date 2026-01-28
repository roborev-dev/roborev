package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandAndReadFiles(t *testing.T) {
	// Create temp directory with test files
	tmpDir := t.TempDir()

	// Create some test files
	writeFile := func(path, content string) {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeFile("main.go", "package main\n")
	writeFile("util.go", "package main\n// util\n")
	writeFile("sub/helper.go", "package sub\n")
	writeFile("sub/helper_test.go", "package sub\n// test\n")
	writeFile("data.json", `{"key": "value"}`)
	writeFile("README.md", "# README\n")
	writeFile(".hidden/secret.go", "package hidden\n")

	tests := []struct {
		name     string
		patterns []string
		wantKeys []string
		wantErr  bool
	}{
		{
			name:     "single file",
			patterns: []string{"main.go"},
			wantKeys: []string{"main.go"},
		},
		{
			name:     "glob pattern",
			patterns: []string{"*.go"},
			wantKeys: []string{"main.go", "util.go"},
		},
		{
			name:     "subdirectory file",
			patterns: []string{"sub/helper.go"},
			wantKeys: []string{"sub/helper.go"},
		},
		{
			name:     "subdirectory glob",
			patterns: []string{"sub/*.go"},
			wantKeys: []string{"sub/helper.go", "sub/helper_test.go"},
		},
		{
			name:     "multiple patterns",
			patterns: []string{"main.go", "sub/helper.go"},
			wantKeys: []string{"main.go", "sub/helper.go"},
		},
		{
			name:     "non-source file",
			patterns: []string{"data.json"},
			wantKeys: []string{"data.json"},
		},
		{
			name:     "directory as pattern",
			patterns: []string{"sub"},
			wantKeys: []string{"sub/helper.go", "sub/helper_test.go"},
		},
		{
			name:     "no match",
			patterns: []string{"nonexistent.go"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := expandAndReadFiles(tmpDir, tt.patterns)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(files) != len(tt.wantKeys) {
				t.Errorf("got %d files, want %d", len(files), len(tt.wantKeys))
			}

			for _, key := range tt.wantKeys {
				if _, ok := files[key]; !ok {
					t.Errorf("missing expected file %q, got keys: %v", key, mapKeys(files))
				}
			}
		})
	}
}

func TestExpandAndReadFilesRecursive(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure
	files := map[string]string{
		"main.go":           "package main",
		"cmd/app/app.go":    "package app",
		"internal/util.go":  "package internal",
		"vendor/dep/dep.go": "package dep", // Should be skipped
		".git/config":       "[core]",      // Should be skipped
	}

	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	result, err := expandAndReadFiles(tmpDir, []string{"./..."})
	if err != nil {
		t.Fatalf("expandAndReadFiles error: %v", err)
	}

	// Should include main.go, cmd/app/app.go, internal/util.go
	// Should NOT include vendor/dep/dep.go or .git/config
	want := []string{"main.go", "cmd/app/app.go", "internal/util.go"}
	for _, w := range want {
		if _, ok := result[w]; !ok {
			t.Errorf("missing expected file %q", w)
		}
	}

	noWant := []string{"vendor/dep/dep.go", ".git/config"}
	for _, nw := range noWant {
		if _, ok := result[nw]; ok {
			t.Errorf("should not include %q", nw)
		}
	}
}

func TestIsSourceFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"script.py", true},
		{"app.js", true},
		{"app.ts", true},
		{"app.tsx", true},
		{"lib.rs", true},
		{"config.yaml", true},
		{"config.yml", true},
		{"data.json", true},
		{"readme.md", true},
		{"binary.exe", false},
		{"image.png", false},
		{"archive.tar.gz", false},
		{".gitignore", false},
		{"Makefile", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isSourceFile(tt.path)
			if got != tt.want {
				t.Errorf("isSourceFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]string{
		"c": "3",
		"a": "1",
		"b": "2",
	}

	keys := sortedKeys(m)

	if len(keys) != 3 {
		t.Fatalf("got %d keys, want 3", len(keys))
	}
	if keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("keys not sorted: %v", keys)
	}
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
