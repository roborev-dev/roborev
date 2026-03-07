package githook

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/testutil"
)

const (
	shebang         = "#!/bin/sh\n"
	hookPostCommit  = "post-commit"
	hookPostRewrite = "post-rewrite"
)

func TestGeneratePostCommit(t *testing.T) {
	t.Parallel()
	content := GeneratePostCommit()
	lines := strings.Split(content, "\n")

	t.Run("has shebang", func(t *testing.T) {
		t.Parallel()
		if !strings.HasPrefix(content, shebang) {
			t.Error("hook should start with #!/bin/sh")
		}
	})

	t.Run("has roborev comment", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(content, "# roborev") {
			t.Error("hook should contain roborev comment")
		}
	})

	t.Run("baked path comes first", func(t *testing.T) {
		t.Parallel()
		assertSubstringsInOrder(t, content, "ROBOREV=\"", "command -v roborev")
	})

	t.Run("post-commit line without background", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(content, "post-commit") || !strings.Contains(content, "2>/dev/null") {
			t.Error("hook should call post-commit with 2>/dev/null")
		}

		idx := strings.Index(content, "\" post-commit")
		if idx != -1 {
			lineEnd := strings.Index(content[idx:], "\n")
			if lineEnd == -1 {
				lineEnd = len(content[idx:])
			}
			line := strings.TrimSpace(content[idx : idx+lineEnd])
			if strings.HasSuffix(line, "&") {
				t.Error("post-commit line should not have trailing &")
			}
		}
	})

	t.Run("has version marker", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(content, PostCommitVersionMarker) {
			t.Errorf(
				"hook should contain %q",
				PostCommitVersionMarker,
			)
		}
	})

	t.Run("baked path is quoted", func(t *testing.T) {
		t.Parallel()
		for _, line := range lines {
			if strings.HasPrefix(line, "ROBOREV=") &&
				!strings.Contains(line, "command -v") {
				if !strings.Contains(line, `ROBOREV="`) {
					t.Errorf(
						"baked path should be quoted: %s",
						line,
					)
				}
				break
			}
		}
	})

	t.Run("baked path is absolute", func(t *testing.T) {
		t.Parallel()
		for _, line := range lines {
			if strings.HasPrefix(line, "ROBOREV=") &&
				!strings.Contains(line, "command -v") {
				start := strings.Index(line, `"`)
				end := strings.LastIndex(line, `"`)
				if start != -1 && end > start {
					path := line[start+1 : end]
					if !filepath.IsAbs(path) {
						t.Errorf(
							"baked path should be absolute: %s",
							path,
						)
					}
				}
				break
			}
		}
	})
}

func TestGeneratePostRewrite(t *testing.T) {
	t.Parallel()
	content := GeneratePostRewrite()

	if !strings.HasPrefix(content, shebang) {
		t.Error("hook should start with #!/bin/sh")
	}
	if !strings.Contains(content, PostRewriteVersionMarker) {
		t.Error("hook should contain version marker")
	}
	if !strings.Contains(content, "remap --quiet") {
		t.Error("hook should call remap --quiet")
	}
}

func TestGenerateEmbeddablePostCommit(t *testing.T) {
	t.Parallel()
	content := generateEmbeddablePostCommit()

	if strings.HasPrefix(content, "#!") {
		t.Error("embeddable should not have shebang")
	}
	if !strings.Contains(content, "_roborev_hook() {") {
		t.Error("embeddable should use function wrapper")
	}
	if !strings.Contains(content, "return 0") {
		t.Error("embeddable should use return, not exit")
	}
	if strings.Contains(content, "exit 0") {
		t.Error("embeddable must not use exit 0")
	}
	if !strings.Contains(content, PostCommitVersionMarker) {
		t.Error("embeddable should contain version marker")
	}
	// Ends with function call
	lines := strings.Split(
		strings.TrimRight(content, "\n"), "\n",
	)
	last := strings.TrimSpace(lines[len(lines)-1])
	if last != "_roborev_hook" {
		t.Errorf(
			"embeddable should end with function call, got: %s",
			last,
		)
	}
}

func TestGenerateEmbeddablePostRewrite(t *testing.T) {
	t.Parallel()
	content := generateEmbeddablePostRewrite()

	if strings.HasPrefix(content, "#!") {
		t.Error("embeddable should not have shebang")
	}
	if !strings.Contains(content, "_roborev_remap() {") {
		t.Error("embeddable should use function wrapper")
	}
	if !strings.Contains(content, "return 0") {
		t.Error("embeddable should use return, not exit")
	}
	if strings.Contains(content, "exit 0") {
		t.Error("embeddable must not use exit 0")
	}
	if !strings.Contains(content, PostRewriteVersionMarker) {
		t.Error("embeddable should contain version marker")
	}
}

func TestEmbedSnippet(t *testing.T) {
	t.Parallel()
	t.Run("inserts after shebang", func(t *testing.T) {
		t.Parallel()
		existing := "#!/bin/sh\necho 'user code'\nexit 0\n"
		snippet := "# roborev snippet\n_roborev_hook\n"
		result := embedSnippet(existing, snippet)
		if !strings.HasPrefix(result, shebang) {
			t.Error("should preserve shebang")
		}
		shebangEnd := strings.Index(result, "\n") + 1
		afterShebang := result[shebangEnd:]
		if !strings.HasPrefix(afterShebang, "# roborev snippet") {
			t.Errorf(
				"snippet should come right after shebang, got:\n%s",
				result,
			)
		}
		if !strings.Contains(result, "echo 'user code'") {
			t.Error("user code should be preserved")
		}
	})

	t.Run("snippet before exit 0", func(t *testing.T) {
		t.Parallel()
		existing := "#!/bin/sh\nexit 0\n"
		snippet := "SNIPPET\n"
		result := embedSnippet(existing, snippet)
		snippetIdx := strings.Index(result, "SNIPPET")
		exitIdx := strings.Index(result, "exit 0")
		if snippetIdx > exitIdx {
			t.Error("snippet should appear before exit 0")
		}
	})

	t.Run("no shebang prepends", func(t *testing.T) {
		t.Parallel()
		existing := "echo 'no shebang'\n"
		snippet := "SNIPPET\n"
		result := embedSnippet(existing, snippet)
		if !strings.HasPrefix(result, "SNIPPET\n") {
			t.Errorf(
				"snippet should be prepended, got:\n%s",
				result,
			)
		}
	})

	t.Run("shebang without trailing newline", func(t *testing.T) {
		t.Parallel()
		existing := "#!/bin/sh"
		snippet := "SNIPPET\n"
		result := embedSnippet(existing, snippet)
		if !strings.HasPrefix(result, shebang) {
			t.Errorf(
				"shebang should get trailing newline, got:\n%q",
				result,
			)
		}
		if !strings.Contains(result, "SNIPPET") {
			t.Error("snippet should be present")
		}
	})
}

func TestNeedsUpgrade(t *testing.T) {
	t.Parallel()
	t.Run("outdated hook", func(t *testing.T) {
		t.Parallel()
		repo := setupHooksRepo(t)
		repo.WriteHook(
			"#!/bin/sh\n# roborev post-commit hook\n" +
				"roborev enqueue\n",
		)
		if !NeedsUpgrade(
			repo.Root, hookPostCommit, PostCommitVersionMarker,
		) {
			t.Error("should detect outdated hook")
		}
	})

	t.Run("current hook", func(t *testing.T) {
		t.Parallel()
		repo := setupHooksRepo(t)
		repo.WriteHook(
			"#!/bin/sh\n# roborev " +
				PostCommitVersionMarker +
				"\nroborev enqueue\n",
		)
		if NeedsUpgrade(
			repo.Root, hookPostCommit, PostCommitVersionMarker,
		) {
			t.Error("should not flag current hook")
		}
	})

	t.Run("no hook", func(t *testing.T) {
		t.Parallel()
		repo := setupHooksRepo(t)
		if NeedsUpgrade(
			repo.Root, hookPostCommit, PostCommitVersionMarker,
		) {
			t.Error("should not flag missing hook")
		}
	})

	t.Run("non-roborev hook", func(t *testing.T) {
		t.Parallel()
		repo := setupHooksRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if NeedsUpgrade(
			repo.Root, hookPostCommit, PostCommitVersionMarker,
		) {
			t.Error("should not flag non-roborev hook")
		}
	})

	t.Run("post-rewrite outdated", func(t *testing.T) {
		t.Parallel()
		repo := setupHooksRepo(t)
		os.WriteFile(
			filepath.Join(repo.HooksDir, hookPostRewrite),
			[]byte("#!/bin/sh\n# roborev hook\n"+
				"roborev remap\n"),
			0755,
		)
		if !NeedsUpgrade(
			repo.Root, hookPostRewrite,
			PostRewriteVersionMarker,
		) {
			t.Error("should detect outdated post-rewrite hook")
		}
	})

	t.Run("post-rewrite current", func(t *testing.T) {
		t.Parallel()
		repo := setupHooksRepo(t)
		os.WriteFile(
			filepath.Join(repo.HooksDir, hookPostRewrite),
			[]byte("#!/bin/sh\n# roborev "+
				PostRewriteVersionMarker+
				"\nroborev remap\n"),
			0755,
		)
		if NeedsUpgrade(
			repo.Root, hookPostRewrite,
			PostRewriteVersionMarker,
		) {
			t.Error("should not flag current post-rewrite hook")
		}
	})
}

func TestNotInstalled(t *testing.T) {
	t.Parallel()
	t.Run("hook file absent", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewTestRepo(t)
		if !NotInstalled(repo.Root, hookPostCommit) {
			t.Error("absent hook should be not installed")
		}
	})

	t.Run("hook without roborev", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if !NotInstalled(repo.Root, hookPostCommit) {
			t.Error("non-roborev hook should be not installed")
		}
	})

	t.Run("hook with roborev", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(GeneratePostCommit())
		if NotInstalled(repo.Root, hookPostCommit) {
			t.Error("roborev hook should be installed")
		}
	})

	t.Run("non-ENOENT read error returns false",
		func(t *testing.T) {
			t.Parallel()
			repo := testutil.NewTestRepo(t)
			// Create a directory where the hook file would be.
			// Reading a directory is a non-ENOENT I/O error.
			hookPath := filepath.Join(
				repo.Root, ".git", "hooks", hookPostCommit,
			)
			os.MkdirAll(hookPath, 0755)
			if NotInstalled(repo.Root, hookPostCommit) {
				t.Error(
					"non-ENOENT error should not report " +
						"as not installed",
				)
			}
		},
	)
}

func TestMissing(t *testing.T) {
	t.Parallel()
	t.Run("missing post-rewrite with roborev post-commit",
		func(t *testing.T) {
			t.Parallel()
			repo := testutil.NewTestRepo(t)
			repo.WriteHook(
				"#!/bin/sh\n# roborev " +
					PostCommitVersionMarker + "\n" +
					"roborev enqueue\n",
			)
			if !Missing(repo.Root, hookPostRewrite) {
				t.Error("should detect missing post-rewrite")
			}
		},
	)

	t.Run("no post-commit hook at all", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewTestRepo(t)
		if Missing(repo.Root, hookPostRewrite) {
			t.Error("should not warn without post-commit")
		}
	})

	t.Run("post-rewrite exists with roborev", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(
			"#!/bin/sh\n# roborev " +
				PostCommitVersionMarker + "\n" +
				"roborev enqueue\n",
		)
		hooksDir := filepath.Join(repo.Root, ".git", "hooks")
		os.WriteFile(
			filepath.Join(hooksDir, hookPostRewrite),
			[]byte("#!/bin/sh\n# roborev "+
				PostRewriteVersionMarker+
				"\nroborev remap\n"),
			0755,
		)
		if Missing(repo.Root, hookPostRewrite) {
			t.Error("should not warn when present")
		}
	})

	t.Run("non-roborev post-commit", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if Missing(repo.Root, hookPostRewrite) {
			t.Error("should not warn for non-roborev")
		}
	})

	t.Run("non-ENOENT read error returns false",
		func(t *testing.T) {
			t.Parallel()
			if runtime.GOOS == "windows" {
				t.Skip("permission test unreliable on Windows")
			}
			repo := testutil.NewTestRepo(t)
			repo.WriteHook(
				"#!/bin/sh\n# roborev " +
					PostCommitVersionMarker + "\n" +
					"roborev enqueue\n",
			)
			// Create post-rewrite as a directory so ReadFile
			// fails with a non-ENOENT error.
			prPath := filepath.Join(
				repo.Root, ".git", "hooks", hookPostRewrite,
			)
			os.MkdirAll(prPath, 0755)
			if Missing(repo.Root, hookPostRewrite) {
				t.Error(
					"non-ENOENT error should return false",
				)
			}
		},
	)
}

type fileStateAssertion struct {
	expectDeleted bool
	expectPrefix  string
	expectExact   string
	expectContent []string
	expectMissing []string
	orderedChecks []string
}

func assertFileState(t *testing.T, path string, a fileStateAssertion) {
	t.Helper()
	if a.expectDeleted {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("should be deleted entirely")
		}
		return
	}
	if a.expectExact != "" {
		assertFileEquals(t, path, a.expectExact)
		return
	}
	if a.expectPrefix != "" {
		assertFileHasPrefix(t, path, a.expectPrefix)
	}
	if len(a.expectContent) > 0 {
		assertFileContains(t, path, a.expectContent...)
	}
	if len(a.expectMissing) > 0 {
		assertFileNotContains(t, path, a.expectMissing...)
	}
	if len(a.orderedChecks) > 1 {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		assertSubstringsInOrder(t, string(content), a.orderedChecks...)
	}
}

type installTestCase struct {
	name           string
	hookName       string
	initialContent string
	force          bool
	expectedError  error
	fileStateAssertion
}

func TestInstall(t *testing.T) {
	t.Parallel()
	tests := []installTestCase{
		{
			name:     "fresh install creates standalone hook",
			hookName: hookPostCommit,
			fileStateAssertion: fileStateAssertion{
				expectPrefix:  shebang,
				expectContent: []string{PostCommitVersionMarker},
			},
		},
		{
			name:           "embeds after shebang in existing hook",
			hookName:       hookPostCommit,
			initialContent: "#!/bin/sh\necho 'custom'\n",
			fileStateAssertion: fileStateAssertion{
				expectPrefix:  shebang,
				expectContent: []string{"echo 'custom'", PostCommitVersionMarker},
			},
		},
		{
			name:           "function wrapper uses return not exit",
			hookName:       hookPostCommit,
			initialContent: "#!/bin/sh\necho 'custom'\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"return 0", "_roborev_hook() {"},
				expectMissing: []string{"exit 0"},
			},
		},
		{
			name:     "early exit does not block snippet",
			hookName: hookPostCommit,
			initialContent: shebang +
				". \"$(dirname \"$0\")/_/husky.sh\"\n" +
				"npx lint-staged\n" +
				"exit 0\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"_roborev_hook", "exit 0"},
				orderedChecks: []string{"\n_roborev_hook\n", "\nexit 0\n"},
			},
		},
		{
			name:           "skips current version",
			hookName:       hookPostRewrite,
			initialContent: GeneratePostRewrite(),
			fileStateAssertion: fileStateAssertion{
				expectExact: GeneratePostRewrite(),
			},
		},
		{
			name:     "upgrades outdated hook",
			hookName: hookPostCommit,
			initialContent: shebang +
				"# roborev post-commit hook\n" +
				"ROBOREV=\"/usr/local/bin/roborev\"\n" +
				"\"$ROBOREV\" enqueue --quiet 2>/dev/null\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{PostCommitVersionMarker},
				expectMissing: []string{"# roborev post-commit hook\n"},
			},
		},
		{
			name:     "upgrade from v2 to v3",
			hookName: hookPostCommit,
			initialContent: shebang +
				"# roborev post-commit hook v2 - auto-reviews every commit\n" +
				"ROBOREV=\"/usr/local/bin/roborev\"\n" +
				"if [ ! -x \"$ROBOREV\" ]; then\n" +
				"    ROBOREV=$(command -v roborev 2>/dev/null)\n" +
				"    [ -z \"$ROBOREV\" ] || [ ! -x \"$ROBOREV\" ] && exit 0\n" +
				"fi\n" +
				"\"$ROBOREV\" enqueue --quiet 2>/dev/null\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{PostCommitVersionMarker},
				expectMissing: []string{"hook v2"},
			},
		},
		{
			name:     "upgrades mixed hook preserving user content",
			hookName: hookPostRewrite,
			initialContent: "#!/bin/sh\necho 'user code'\n" +
				"# roborev post-rewrite hook\n" +
				"ROBOREV=\"/usr/bin/roborev\"\n" +
				"\"$ROBOREV\" remap --quiet 2>/dev/null\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"echo 'user code'", PostRewriteVersionMarker},
			},
		},
		{
			name:           "refuses non-shell hook",
			hookName:       hookPostCommit,
			initialContent: "#!/usr/bin/env python3\nprint('hello')\n",
			expectedError:  ErrNonShellHook,
			fileStateAssertion: fileStateAssertion{
				expectExact: "#!/usr/bin/env python3\nprint('hello')\n",
			},
		},
		{
			name:           "refuses upgrade of non-shell hook",
			hookName:       hookPostCommit,
			initialContent: "#!/usr/bin/env python3\n# reviewed by roborev\nprint('hello')\n",
			expectedError:  ErrNonShellHook,
			fileStateAssertion: fileStateAssertion{
				expectExact: "#!/usr/bin/env python3\n# reviewed by roborev\nprint('hello')\n",
			},
		},
		{
			name:           "force overwrites existing hook",
			hookName:       hookPostCommit,
			initialContent: "#!/bin/sh\necho 'custom'\n",
			force:          true,
			fileStateAssertion: fileStateAssertion{
				expectPrefix:  shebang,
				expectContent: []string{PostCommitVersionMarker},
				expectMissing: []string{"echo 'custom'"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := setupHooksRepo(t)
			hookPath := filepath.Join(repo.HooksDir, tc.hookName)

			if tc.initialContent != "" {
				if err := os.WriteFile(hookPath, []byte(tc.initialContent), 0755); err != nil {
					t.Fatal(err)
				}
			}

			err := Install(repo.HooksDir, tc.hookName, tc.force)

			if tc.expectedError != nil {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, tc.expectedError) {
					t.Errorf("expected error %v, got %v", tc.expectedError, err)
				}
			} else if err != nil {
				t.Fatalf("Install: %v", err)
			}

			assertFileState(t, hookPath, tc.fileStateAssertion)
		})
	}

	t.Run("appends to hooks with various shell shebangs", func(t *testing.T) {
		shebangs := []string{
			"#!/bin/sh", "#!/usr/bin/env sh",
			"#!/bin/bash", "#!/usr/bin/env bash",
			"#!/bin/zsh", "#!/usr/bin/env zsh",
			"#!/bin/ksh", "#!/usr/bin/env ksh",
			"#!/bin/dash", "#!/usr/bin/env dash",
		}
		for _, shebang := range shebangs {
			t.Run(shebang, func(t *testing.T) {
				repo := setupHooksRepo(t)
				hookPath := filepath.Join(repo.HooksDir, hookPostCommit)
				existing := shebang + "\necho 'custom'\n"
				os.WriteFile(hookPath, []byte(existing), 0755)

				if err := Install(repo.HooksDir, hookPostCommit, false); err != nil {
					t.Fatalf("should append to %s: %v", shebang, err)
				}
				assertFileContains(t, hookPath, "echo 'custom'", PostCommitVersionMarker)
			})
		}
	})
}

func TestInstall_ReReadError(t *testing.T) {
	t.Parallel()
	repo := setupHooksRepo(t)
	hookPath := filepath.Join(repo.HooksDir, hookPostCommit)
	outdated := shebang +
		"# roborev post-commit hook\n" +
		"ROBOREV=\"/usr/local/bin/roborev\"\n" +
		"\"$ROBOREV\" enqueue --quiet 2>/dev/null\n"
	os.WriteFile(hookPath, []byte(outdated), 0755)

	callCount := 0
	err := installWithFileReader(repo.HooksDir, hookPostCommit, false, func(name string) ([]byte, error) {
		if callCount == 0 {
			callCount++
			return os.ReadFile(name)
		}
		return nil, fs.ErrPermission
	})
	if err == nil {
		t.Fatal("expected error from re-read failure")
	}
	if !strings.Contains(err.Error(), "re-read") {
		t.Errorf("error should mention re-read: %v", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("should wrap ErrPermission: %v", err)
	}
}

func TestInstallAll(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test checks Unix exec bits")
	}

	repo := setupHooksRepo(t)

	if err := InstallAll(repo.HooksDir, false); err != nil {
		t.Fatalf("InstallAll: %v", err)
	}

	for _, name := range []string{hookPostCommit, hookPostRewrite} {
		path := filepath.Join(repo.HooksDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s hook not created: %v", name, err)
			continue
		}
		if !strings.Contains(
			string(content), VersionMarker(name),
		) {
			t.Errorf(
				"%s should contain version marker", name,
			)
		}
	}
}

func TestUninstall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		hookName       string
		initialContent string
		fileStateAssertion
	}{
		{
			name:           "generated hook is deleted entirely",
			hookName:       hookPostRewrite,
			initialContent: GeneratePostRewrite(),
			fileStateAssertion: fileStateAssertion{
				expectDeleted: true,
			},
		},
		{
			name:     "mixed hook preserves non-roborev content",
			hookName: hookPostRewrite,
			initialContent: "#!/bin/sh\necho 'custom logic'\n" +
				GeneratePostRewrite(),
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"echo 'custom logic'"},
				expectMissing: []string{"roborev", "fi"},
			},
		},
		{
			name:     "v3 function wrapper removed",
			hookName: hookPostCommit,
			initialContent: shebang +
				generateEmbeddablePostCommit() +
				"echo 'user code after'\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"echo 'user code after'"},
				expectMissing: []string{"_roborev_hook", "return 0"},
			},
		},
		{
			name:     "v3 mixed hook preserves user content",
			hookName: hookPostCommit,
			initialContent: shebang +
				generateEmbeddablePostCommit() +
				"echo 'before'\necho 'after'\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"echo 'before'", "echo 'after'"},
				expectMissing: []string{"roborev"},
			},
		},
		{
			name:           "v0 hook removed",
			hookName:       hookPostCommit,
			initialContent: v0Hook,
			fileStateAssertion: fileStateAssertion{
				expectDeleted: true,
			},
		},
		{
			name:           "v0.5 hook removed",
			hookName:       hookPostCommit,
			initialContent: v05Hook,
			fileStateAssertion: fileStateAssertion{
				expectDeleted: true,
			},
		},
		{
			name:           "v1 hook removed",
			hookName:       hookPostCommit,
			initialContent: v1Hook,
			fileStateAssertion: fileStateAssertion{
				expectDeleted: true,
			},
		},
		{
			name:           "v1 mixed hook removes only roborev block",
			hookName:       hookPostCommit,
			initialContent: "#!/bin/sh\necho 'custom'\n" + strings.TrimPrefix(v1Hook, shebang),
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"echo 'custom'"},
				expectMissing: []string{"roborev"},
			},
		},
		{
			name:           "no-op for no roborev content",
			hookName:       hookPostRewrite,
			initialContent: "#!/bin/sh\necho 'unrelated'\n",
			fileStateAssertion: fileStateAssertion{
				expectContent: []string{"#!/bin/sh\necho 'unrelated'\n"},
			},
		},
		{
			name:     "custom if-block after snippet preserved",
			hookName: hookPostRewrite,
			initialContent: GeneratePostRewrite() +
				"if [ -f .notify ]; then\n" +
				"    echo 'send notification'\n" +
				"fi\n",
			fileStateAssertion: fileStateAssertion{
				expectExact: shebang +
					"if [ -f .notify ]; then\n" +
					"    echo 'send notification'\n" +
					"fi\n",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := setupHooksRepo(t)
			hookPath := filepath.Join(repo.HooksDir, tc.hookName)

			if err := os.WriteFile(hookPath, []byte(tc.initialContent), 0755); err != nil {
				t.Fatal(err)
			}

			if err := Uninstall(hookPath); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			assertFileState(t, hookPath, tc.fileStateAssertion)
		})
	}

	t.Run("missing file is no-op", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nonexistent")
		if err := Uninstall(path); err != nil {
			t.Errorf("should be no-op: %v", err)
		}
	})
}

func TestVersionMarker(t *testing.T) {
	t.Parallel()
	if m := VersionMarker(hookPostCommit); m != PostCommitVersionMarker {
		t.Errorf("got %q, want %q", m, PostCommitVersionMarker)
	}
	if m := VersionMarker(hookPostRewrite); m != PostRewriteVersionMarker {
		t.Errorf("got %q, want %q", m, PostRewriteVersionMarker)
	}
	if m := VersionMarker("unknown"); m != "" {
		t.Errorf("unknown should return empty, got %q", m)
	}
}

func TestHasRealErrors(t *testing.T) {
	t.Parallel()
	realErr := errors.New("permission denied")

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		if HasRealErrors(nil) {
			t.Error("nil should return false")
		}
	})

	t.Run("only non-shell", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("hook: %w", ErrNonShellHook)
		if HasRealErrors(err) {
			t.Error("single ErrNonShellHook should return false")
		}
	})

	t.Run("only real", func(t *testing.T) {
		t.Parallel()
		if !HasRealErrors(realErr) {
			t.Error("real error should return true")
		}
	})

	t.Run("joined all non-shell", func(t *testing.T) {
		t.Parallel()
		err := errors.Join(
			fmt.Errorf("a: %w", ErrNonShellHook),
			fmt.Errorf("b: %w", ErrNonShellHook),
		)
		if HasRealErrors(err) {
			t.Error("joined non-shell only should return false")
		}
	})

	t.Run("joined mixed", func(t *testing.T) {
		t.Parallel()
		err := errors.Join(
			fmt.Errorf("a: %w", ErrNonShellHook),
			realErr,
		)
		if !HasRealErrors(err) {
			t.Error("joined with real error should return true")
		}
	})

	t.Run("joined all real", func(t *testing.T) {
		t.Parallel()
		err := errors.Join(realErr, errors.New("disk full"))
		if !HasRealErrors(err) {
			t.Error("joined real errors should return true")
		}
	})
}

func TestIsRoborevSnippetLine(t *testing.T) {
	positives := []string{
		`ROBOREV="/usr/local/bin/roborev"`,
		`ROBOREV=$(command -v roborev 2>/dev/null)`,
		`"$ROBOREV" post-commit 2>/dev/null`,
		`"$ROBOREV" enqueue --quiet 2>/dev/null`,
		`"$ROBOREV" remap --quiet 2>/dev/null`,
		`roborev post-commit`,
		`roborev enqueue --quiet`,
		`roborev remap --quiet`,
		`if [ ! -x "$ROBOREV" ]; then`,
		`[ -z "$ROBOREV" ] || [ ! -x "$ROBOREV" ] && exit 0`,
		`return 0`,
		`_roborev_hook() {`,
		`_roborev_hook`,
		`_roborev_remap() {`,
		`_roborev_remap`,
	}
	for _, line := range positives {
		if !isRoborevSnippetLine(line) {
			t.Errorf("expected true for %q", line)
		}
	}

	negatives := []string{
		"echo hello",
		"",
		"#!/bin/sh",
		"npm test",
	}
	for _, line := range negatives {
		if isRoborevSnippetLine(line) {
			t.Errorf("expected false for %q", line)
		}
	}
}

// Helpers

func setupHooksRepo(t *testing.T) *testutil.TestRepo {
	t.Helper()
	repo := testutil.NewTestRepo(t)
	if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func readFileForAssert(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return string(content)
}

func assertFileContains(t *testing.T, path string, substrings ...string) {
	t.Helper()
	str := readFileForAssert(t, path)
	for _, sub := range substrings {
		if !strings.Contains(str, sub) {
			t.Errorf("file %s should contain %q", filepath.Base(path), sub)
		}
	}
}

func assertFileNotContains(t *testing.T, path string, substrings ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return // If file doesn't exist, it doesn't contain the substrings
		}
		t.Fatalf("failed to read %s: %v", path, err)
	}
	str := string(content)
	if str == "" {
		return
	}
	for _, sub := range substrings {
		if strings.Contains(str, sub) {
			t.Errorf("file %s should NOT contain %q", filepath.Base(path), sub)
		}
	}
}

func assertFileEquals(t *testing.T, path string, expected string) {
	t.Helper()
	str := readFileForAssert(t, path)
	if str != expected {
		t.Errorf("file %s content mismatch.\nGot:\n%q\nWant:\n%q", filepath.Base(path), str, expected)
	}
}

func assertFileHasPrefix(t *testing.T, path string, prefix string) {
	t.Helper()
	str := readFileForAssert(t, path)
	if !strings.HasPrefix(str, prefix) {
		t.Errorf("file %s should start with:\n%q\nGot start:\n%q", filepath.Base(path), prefix, str)
	}
}

func assertSubstringsInOrder(t *testing.T, s string, substrings ...string) {
	t.Helper()
	searchFrom := 0
	for _, sub := range substrings {
		idx := strings.Index(s[searchFrom:], sub)
		if idx == -1 {
			t.Errorf("missing %q after offset %d in hook", sub, searchFrom)
			return
		}
		searchFrom += idx + len(sub)
	}
}

const (
	v0Hook = `#!/bin/sh
# RoboRev post-commit hook - auto-reviews every commit
roborev enqueue --sha HEAD 2>/dev/null &
`
	v05Hook = `#!/bin/sh
# RoboRev post-commit hook - auto-reviews every commit
ROBOREV="/usr/local/bin/roborev"
if [ ! -x "$ROBOREV" ]; then
    ROBOREV=$(command -v roborev) || exit 0
fi
"$ROBOREV" enqueue --quiet &
`
	v1Hook = `#!/bin/sh
# RoboRev post-commit hook - auto-reviews every commit
ROBOREV=$(command -v roborev 2>/dev/null)
if [ -z "$ROBOREV" ] || [ ! -x "$ROBOREV" ]; then
    ROBOREV="/usr/local/bin/roborev"
    [ ! -x "$ROBOREV" ] && exit 0
fi
"$ROBOREV" enqueue --quiet 2>/dev/null &
`
)
