package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows due to shell script stubs")
	}
}

func assertFileContains(t *testing.T, path string, expected, notExpected []string) {
	t.Helper()
	if len(expected) == 0 && len(notExpected) == 0 {
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read generated file: %v", err)
	}
	strContent := string(content)
	for _, exp := range expected {
		if !strings.Contains(strContent, exp) {
			t.Errorf("generated file missing expected content: %q", exp)
		}
	}
	for _, bad := range notExpected {
		if strings.Contains(strContent, bad) {
			t.Errorf("generated file should not contain: %q", bad)
		}
	}
}

func setupGhActionTest(t *testing.T) (*testutil.TestRepo, string) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("ROBOREV_DATA_DIR", filepath.Join(tmpHome, ".roborev"))

	repo := testutil.NewTestRepo(t)
	t.Cleanup(repo.Chdir())

	outPath := filepath.Join(repo.Root, ".github", "workflows", "roborev.yml")
	return repo, outPath
}

func TestGhActionCmd(t *testing.T) {
	skipOnWindows(t)

	tests := []struct {
		name             string
		flags            []string
		repoConfig       string
		expectError      bool
		errorContains    string
		expectedContains []string
		notContains      []string
	}{
		{
			name:             "default flags",
			flags:            []string{},
			expectedContains: []string{"OPENAI_API_KEY", "@openai/codex@latest", "roborev ci review"},
		},
		{
			name:             "custom agent flag",
			flags:            []string{"--agent", "claude-code"},
			expectedContains: []string{"ANTHROPIC_API_KEY", "@anthropic-ai/claude-code@latest"},
		},
		{
			name: "multi-agent flag",
			flags: []string{
				"--agent", "codex,claude-code",
			},
			expectedContains: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "@openai/codex@latest", "@anthropic-ai/claude-code@latest"},
		},
		{
			name:             "pinned version",
			flags:            []string{"--roborev-version", "0.33.1"},
			expectedContains: []string{`ROBOREV_VERSION="0.33.1"`},
		},
		{
			name:             "infers agent from repo config",
			repoConfig:       "agent = \"gemini\"\n",
			expectedContains: []string{"GOOGLE_API_KEY", "@google/gemini-cli@latest"},
		},
		{
			name:             "flag overrides repo config",
			repoConfig:       "agent = \"gemini\"\n",
			flags:            []string{"--agent", "codex"},
			expectedContains: []string{"OPENAI_API_KEY"},
		},
		{
			name:  "kilo gets multi-provider guidance",
			flags: []string{"--agent", "kilo"},
			expectedContains: []string{
				"ANTHROPIC_API_KEY",
				"@kilocode/cli@latest",
				"different model provider",
				"default for kilo",
			},
		},
		{
			name:  "kiro has no secret in env block",
			flags: []string{"--agent", "kiro"},
			expectedContains: []string{
				"kiro.dev",
			},
			notContains: []string{
				"OPENAI_API_KEY:",
				"AWS_ACCESS_KEY_ID:",
			},
		},
		{
			name: "infers agents from repo CI config",
			repoConfig: "[ci]\nagents = " +
				"[\"codex\", \"claude-code\"]\n",
			expectedContains: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, outPath := setupGhActionTest(t)

			if tt.repoConfig != "" {
				configPath := filepath.Join(repo.Root, ".roborev.toml")
				if err := os.WriteFile(configPath, []byte(tt.repoConfig), 0644); err != nil {
					t.Fatal(err)
				}
			}

			cmd := ghActionCmd()
			args := append(append([]string(nil), tt.flags...), "--output", outPath)
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

			assertFileContains(t, outPath, tt.expectedContains, tt.notContains)
		})
	}
}

func TestGhActionCmd_ForceOverwrite(t *testing.T) {
	skipOnWindows(t)

	_, outPath := setupGhActionTest(t)

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
		t.Error("force should have overwritten with workflow")
	}
}

func TestGhActionCmd_NotGitRepo(t *testing.T) {
	skipOnWindows(t)

	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

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
