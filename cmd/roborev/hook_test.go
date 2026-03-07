package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/githook"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestUninstallHookCmd(t *testing.T) {
	tests := []struct {
		name         string
		initialHooks map[string]string
		setup        func(t *testing.T, repo *testutil.TestRepo)
		assert       func(t *testing.T, repo *testutil.TestRepo)
	}{
		{
			name: "hook missing",
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
					t.Error("Hook file should not exist")
				}
			},
		},
		{
			name: "hook without roborev",
			initialHooks: map[string]string{
				"post-commit": "#!/bin/bash\necho 'other hook'\n",
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				content, err := os.ReadFile(repo.HookPath)
				if err != nil {
					t.Fatalf("Failed to read hook: %v", err)
				}
				want := "#!/bin/bash\necho 'other hook'\n"
				if string(content) != want {
					t.Errorf("Hook content changed: got %q, want %q", string(content), want)
				}
			},
		},
		{
			name: "hook with roborev only - removes file",
			initialHooks: map[string]string{
				"post-commit": githook.GeneratePostCommit(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
					t.Error("Hook file should have been removed")
				}
			},
		},
		{
			name: "hook with roborev and other commands - preserves others",
			initialHooks: map[string]string{
				"post-commit": "#!/bin/sh\necho 'before'\necho 'after'\n" + githook.GeneratePostCommit(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				content, err := os.ReadFile(repo.HookPath)
				if err != nil {
					t.Fatalf("Failed to read hook: %v", err)
				}
				contentStr := string(content)
				if strings.Contains(contentStr, githook.PostCommitVersionMarker) {
					t.Error("Hook should not contain version marker after uninstall")
				}
				if strings.Contains(contentStr, `"$ROBOREV" post-commit`) {
					t.Error("Hook should not contain generated command after uninstall")
				}
				if !strings.Contains(contentStr, "echo 'before'") {
					t.Error("Hook should still contain 'echo before'")
				}
				if !strings.Contains(contentStr, "echo 'after'") {
					t.Error("Hook should still contain 'echo after'")
				}
			},
		},
		{
			name: "also removes post-rewrite hook",
			initialHooks: map[string]string{
				"post-commit":  githook.GeneratePostCommit(),
				"post-rewrite": githook.GeneratePostRewrite(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				if _, err := os.Stat(repo.HookPath); !os.IsNotExist(err) {
					t.Error("post-commit hook should have been removed")
				}
				prPath := filepath.Join(repo.HooksDir, "post-rewrite")
				if _, err := os.Stat(prPath); !os.IsNotExist(err) {
					t.Error("post-rewrite hook should have been removed")
				}
			},
		},
		{
			name: "removes post-rewrite even without post-commit",
			initialHooks: map[string]string{
				"post-rewrite": githook.GeneratePostRewrite(),
			},
			assert: func(t *testing.T, repo *testutil.TestRepo) {
				prPath := filepath.Join(repo.HooksDir, "post-rewrite")
				if _, err := os.Stat(prPath); !os.IsNotExist(err) {
					t.Error("post-rewrite hook should have been removed")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			t.Cleanup(repo.Chdir())

			if tt.setup != nil {
				tt.setup(t, repo)
			}

			if len(tt.initialHooks) > 0 {
				if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
					t.Fatal(err)
				}
				for name, content := range tt.initialHooks {
					path := filepath.Join(repo.HooksDir, name)
					if err := os.WriteFile(path, []byte(content), 0755); err != nil {
						t.Fatal(err)
					}
				}
			}

			cmd := uninstallHookCmd()
			err := cmd.Execute()
			if err != nil {
				t.Fatalf("uninstall-hook failed: %v", err)
			}

			tt.assert(t, repo)
		})
	}
}

func TestInstallHookCmd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test checks Unix exec bits, skipping on Windows")
	}

	repo := testutil.NewTestRepo(t)
	repo.RemoveHooksDir()

	if _, err := os.Stat(repo.HooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks directory should not exist before test")
	}

	t.Cleanup(repo.Chdir())

	installCmd := installHookCmd()
	installCmd.SetArgs([]string{})
	if err := installCmd.Execute(); err != nil {
		t.Fatalf("install-hook failed: %v", err)
	}

	t.Run("creates hooks directory and post-commit hook", func(t *testing.T) {
		if _, err := os.Stat(repo.HooksDir); os.IsNotExist(err) {
			t.Error("hooks directory was not created")
		}
		if _, err := os.Stat(repo.HookPath); os.IsNotExist(err) {
			t.Error("post-commit hook was not created")
		}
	})

	t.Run("creates post-rewrite hook", func(t *testing.T) {
		prHookPath := filepath.Join(repo.HooksDir, "post-rewrite")
		content, err := os.ReadFile(prHookPath)
		if err != nil {
			t.Fatalf("post-rewrite hook not created: %v", err)
		}
		if !strings.Contains(string(content), "remap --quiet") {
			t.Error("post-rewrite hook should contain 'remap --quiet'")
		}
		if !strings.Contains(string(content), githook.PostRewriteVersionMarker) {
			t.Error("post-rewrite hook should contain version marker")
		}
	})
}
