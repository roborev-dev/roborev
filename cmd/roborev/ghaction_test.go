package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestGhActionCmd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows due to shell script stubs")
	}

	tests := []struct {
		name          string
		flags         []string
		repoConfig    string
		expectError   bool
		errorContains string
		checkFile     func(t *testing.T, content string)
	}{
		{
			name:  "default flags",
			flags: []string{},
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "--agent codex") {
					t.Error("expected default agent codex")
				}
				if !strings.Contains(content, "OPENAI_API_KEY") {
					t.Error("expected OPENAI_API_KEY")
				}
				if !strings.Contains(content, "--reasoning thorough") {
					t.Error("expected default reasoning thorough")
				}
			},
		},
		{
			name:  "custom agent flag",
			flags: []string{"--agent", "claude-code"},
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "--agent claude-code") {
					t.Error("expected claude-code agent")
				}
				if !strings.Contains(content, "ANTHROPIC_API_KEY") {
					t.Error("expected ANTHROPIC_API_KEY")
				}
			},
		},
		{
			name:  "custom model and reasoning",
			flags: []string{"--agent", "codex", "--model", "gpt-5.2-codex", "--reasoning", "fast"},
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "--model gpt-5.2-codex") {
					t.Error("expected model in output")
				}
				if !strings.Contains(content, "--reasoning fast") {
					t.Error("expected fast reasoning")
				}
			},
		},
		{
			name:  "custom secret name",
			flags: []string{"--secret-name", "MY_API_KEY"},
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "secrets.MY_API_KEY") {
					t.Error("expected custom secret name")
				}
			},
		},
		{
			name:  "custom review types",
			flags: []string{"--review-types", "security,default"},
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "--type security,default") {
					t.Error("expected custom review types")
				}
			},
		},
		{
			name:  "pinned version",
			flags: []string{"--roborev-version", "0.33.1"},
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, `ROBOREV_VERSION="0.33.1"`) {
					t.Error("expected pinned version")
				}
			},
		},
		{
			name:       "infers agent from repo config",
			repoConfig: "agent = \"gemini\"\n",
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "--agent gemini") {
					t.Error("expected gemini agent from repo config")
				}
				if !strings.Contains(content, "GOOGLE_API_KEY") {
					t.Error("expected GOOGLE_API_KEY for gemini")
				}
			},
		},
		{
			name:       "flag overrides repo config",
			repoConfig: "agent = \"gemini\"\n",
			flags:      []string{"--agent", "codex"},
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "--agent codex") {
					t.Error("expected codex from flag, not gemini from config")
				}
			},
		},
		{
			name:       "infers review types from repo CI config",
			repoConfig: "[ci]\nreview_types = [\"security\", \"design\"]\n",
			checkFile: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "--type security,design") {
					t.Error("expected review types from repo CI config")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup isolated environment
			tmpHome := t.TempDir()
			t.Setenv("HOME", tmpHome)
			t.Setenv("USERPROFILE", tmpHome)
			t.Setenv("ROBOREV_DATA_DIR", filepath.Join(tmpHome, ".roborev"))

			repo := testutil.NewTestRepo(t)
			t.Cleanup(repo.Chdir())

			// Write repo config if specified
			if tt.repoConfig != "" {
				if err := os.WriteFile(filepath.Join(repo.Root, ".roborev.toml"), []byte(tt.repoConfig), 0644); err != nil {
					t.Fatal(err)
				}
			}

			// Build output path
			outPath := filepath.Join(repo.Root, ".github", "workflows", "roborev.yml")

			// Build command
			cmd := ghActionCmd()
			args := append([]string{}, tt.flags...)
			args = append(args, "--output", outPath)
			cmd.SetArgs(args)

			err := cmd.Execute()

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got none")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errorContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Check generated file
			if tt.checkFile != nil {
				content, err := os.ReadFile(outPath)
				if err != nil {
					t.Fatalf("failed to read generated file: %v", err)
				}
				tt.checkFile(t, string(content))
			}
		})
	}
}

func TestGhActionCmd_ForceOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("ROBOREV_DATA_DIR", filepath.Join(tmpHome, ".roborev"))

	repo := testutil.NewTestRepo(t)
	t.Cleanup(repo.Chdir())

	outPath := filepath.Join(repo.Root, ".github", "workflows", "roborev.yml")

	// Create existing file
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, []byte("existing content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Without --force should fail
	cmd := ghActionCmd()
	cmd.SetArgs([]string{"--output", outPath})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error without --force")
	}

	// Verify original content preserved
	content, _ := os.ReadFile(outPath)
	if string(content) != "existing content" {
		t.Error("original content should be preserved")
	}

	// With --force should succeed
	cmd2 := ghActionCmd()
	cmd2.SetArgs([]string{"--output", outPath, "--force"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("force should succeed: %v", err)
	}

	content, _ = os.ReadFile(outPath)
	if !strings.Contains(string(content), "name: roborev") {
		t.Error("force should have overwritten with workflow content")
	}
}

func TestGhActionCmd_NotGitRepo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	cmd := ghActionCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error outside git repo")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("expected 'not a git repository' error, got: %v", err)
	}
}
