package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
)

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
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows due to shell script stubs")
	}

	tests := []struct {
		name             string
		flags            []string
		repoConfig       string
		expectError      bool
		errorContains    string
		expectedContains []string
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
				if err := os.WriteFile(
					filepath.Join(
						repo.Root, ".roborev.toml"),
					[]byte(tt.repoConfig), 0644,
				); err != nil {
					t.Fatal(err)
				}
			}

			cmd := ghActionCmd()
			args := append(
				[]string{}, tt.flags...)
			args = append(args, "--output", outPath)
			cmd.SetArgs(args)

			err := cmd.Execute()

			if tt.expectError {
				if err == nil {
					t.Fatal(
						"expected error but got none")
				}
				if tt.errorContains != "" &&
					!strings.Contains(
						err.Error(),
						tt.errorContains) {
					t.Errorf(
						"error %q should contain %q",
						err.Error(),
						tt.errorContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(tt.expectedContains) > 0 {
				contentBytes, err := os.ReadFile(outPath)
				if err != nil {
					t.Fatalf("failed to read generated file: %v", err)
				}
				content := string(contentBytes)
				for _, expected := range tt.expectedContains {
					if !strings.Contains(content, expected) {
						t.Errorf("generated file missing expected content: %q", expected)
					}
				}
			}
		})
	}
}

func TestGhActionCmd_ForceOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	_, outPath := setupGhActionTest(t)

	if err := os.MkdirAll(
		filepath.Dir(outPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		outPath, []byte("existing content"), 0644,
	); err != nil {
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
	cmd2.SetArgs([]string{
		"--output", outPath, "--force"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("force should succeed: %v", err)
	}

	content, _ = os.ReadFile(outPath)
	if !strings.Contains(
		string(content), "name: roborev") {
		t.Error(
			"force should have overwritten with workflow")
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
	if !strings.Contains(
		err.Error(), "not a git repository") {
		t.Errorf(
			"expected 'not a git repository' error, "+
				"got: %v", err)
	}
}
