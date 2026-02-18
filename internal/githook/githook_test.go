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

func TestGeneratePostCommit(t *testing.T) {
	content := GeneratePostCommit()
	lines := strings.Split(content, "\n")

	t.Run("has shebang", func(t *testing.T) {
		if !strings.HasPrefix(content, "#!/bin/sh\n") {
			t.Error("hook should start with #!/bin/sh")
		}
	})

	t.Run("has roborev comment", func(t *testing.T) {
		if !strings.Contains(content, "# roborev") {
			t.Error("hook should contain roborev comment")
		}
	})

	t.Run("baked path comes first", func(t *testing.T) {
		bakedIdx := -1
		pathIdx := -1
		for i, line := range lines {
			if strings.HasPrefix(line, "ROBOREV=") &&
				!strings.Contains(line, "command -v") {
				bakedIdx = i
			}
			if strings.Contains(line, "command -v roborev") {
				pathIdx = i
			}
		}
		if bakedIdx == -1 {
			t.Error("hook should have baked ROBOREV= assignment")
		}
		if pathIdx == -1 {
			t.Error("hook should have PATH fallback")
		}
		if bakedIdx > pathIdx {
			t.Error("baked path should come before PATH lookup")
		}
	})

	t.Run("enqueue line without background", func(t *testing.T) {
		found := false
		for _, line := range lines {
			if strings.Contains(line, "enqueue --quiet") &&
				strings.Contains(line, "2>/dev/null") &&
				!strings.HasSuffix(
					strings.TrimSpace(line), "&",
				) {
				found = true
				break
			}
		}
		if !found {
			t.Error("hook should have enqueue with --quiet " +
				"and 2>/dev/null but no trailing &")
		}
	})

	t.Run("has version marker", func(t *testing.T) {
		if !strings.Contains(content, PostCommitVersionMarker) {
			t.Errorf(
				"hook should contain %q",
				PostCommitVersionMarker,
			)
		}
	})

	t.Run("baked path is quoted", func(t *testing.T) {
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
	content := GeneratePostRewrite()

	if !strings.HasPrefix(content, "#!/bin/sh\n") {
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
	t.Run("inserts after shebang", func(t *testing.T) {
		existing := "#!/bin/sh\necho 'user code'\nexit 0\n"
		snippet := "# roborev snippet\n_roborev_hook\n"
		result := embedSnippet(existing, snippet)
		if !strings.HasPrefix(result, "#!/bin/sh\n") {
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
		existing := "#!/bin/sh"
		snippet := "SNIPPET\n"
		result := embedSnippet(existing, snippet)
		if !strings.HasPrefix(result, "#!/bin/sh\n") {
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
	t.Run("outdated hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(
			"#!/bin/sh\n# roborev post-commit hook\n" +
				"roborev enqueue\n",
		)
		if !NeedsUpgrade(
			repo.Root, "post-commit", PostCommitVersionMarker,
		) {
			t.Error("should detect outdated hook")
		}
	})

	t.Run("current hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(
			"#!/bin/sh\n# roborev " +
				PostCommitVersionMarker +
				"\nroborev enqueue\n",
		)
		if NeedsUpgrade(
			repo.Root, "post-commit", PostCommitVersionMarker,
		) {
			t.Error("should not flag current hook")
		}
	})

	t.Run("no hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if NeedsUpgrade(
			repo.Root, "post-commit", PostCommitVersionMarker,
		) {
			t.Error("should not flag missing hook")
		}
	})

	t.Run("non-roborev hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if NeedsUpgrade(
			repo.Root, "post-commit", PostCommitVersionMarker,
		) {
			t.Error("should not flag non-roborev hook")
		}
	})

	t.Run("post-rewrite outdated", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		hooksDir := filepath.Join(repo.Root, ".git", "hooks")
		os.MkdirAll(hooksDir, 0755)
		os.WriteFile(
			filepath.Join(hooksDir, "post-rewrite"),
			[]byte("#!/bin/sh\n# roborev hook\n"+
				"roborev remap\n"),
			0755,
		)
		if !NeedsUpgrade(
			repo.Root, "post-rewrite",
			PostRewriteVersionMarker,
		) {
			t.Error("should detect outdated post-rewrite hook")
		}
	})

	t.Run("post-rewrite current", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		hooksDir := filepath.Join(repo.Root, ".git", "hooks")
		os.MkdirAll(hooksDir, 0755)
		os.WriteFile(
			filepath.Join(hooksDir, "post-rewrite"),
			[]byte("#!/bin/sh\n# roborev "+
				PostRewriteVersionMarker+
				"\nroborev remap\n"),
			0755,
		)
		if NeedsUpgrade(
			repo.Root, "post-rewrite",
			PostRewriteVersionMarker,
		) {
			t.Error("should not flag current post-rewrite hook")
		}
	})
}

func TestNotInstalled(t *testing.T) {
	t.Run("hook file absent", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if !NotInstalled(repo.Root, "post-commit") {
			t.Error("absent hook should be not installed")
		}
	})

	t.Run("hook without roborev", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if !NotInstalled(repo.Root, "post-commit") {
			t.Error("non-roborev hook should be not installed")
		}
	})

	t.Run("hook with roborev", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(GeneratePostCommit())
		if NotInstalled(repo.Root, "post-commit") {
			t.Error("roborev hook should be installed")
		}
	})

	t.Run("non-ENOENT read error returns false",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			// Create a directory where the hook file would be.
			// Reading a directory is a non-ENOENT I/O error.
			hookPath := filepath.Join(
				repo.Root, ".git", "hooks", "post-commit",
			)
			os.MkdirAll(hookPath, 0755)
			if NotInstalled(repo.Root, "post-commit") {
				t.Error(
					"non-ENOENT error should not report " +
						"as not installed",
				)
			}
		},
	)
}

func TestMissing(t *testing.T) {
	t.Run("missing post-rewrite with roborev post-commit",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			repo.WriteHook(
				"#!/bin/sh\n# roborev " +
					PostCommitVersionMarker + "\n" +
					"roborev enqueue\n",
			)
			if !Missing(repo.Root, "post-rewrite") {
				t.Error("should detect missing post-rewrite")
			}
		},
	)

	t.Run("no post-commit hook at all", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if Missing(repo.Root, "post-rewrite") {
			t.Error("should not warn without post-commit")
		}
	})

	t.Run("post-rewrite exists with roborev", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook(
			"#!/bin/sh\n# roborev " +
				PostCommitVersionMarker + "\n" +
				"roborev enqueue\n",
		)
		hooksDir := filepath.Join(repo.Root, ".git", "hooks")
		os.WriteFile(
			filepath.Join(hooksDir, "post-rewrite"),
			[]byte("#!/bin/sh\n# roborev "+
				PostRewriteVersionMarker+
				"\nroborev remap\n"),
			0755,
		)
		if Missing(repo.Root, "post-rewrite") {
			t.Error("should not warn when present")
		}
	})

	t.Run("non-roborev post-commit", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		repo.WriteHook("#!/bin/sh\necho hello\n")
		if Missing(repo.Root, "post-rewrite") {
			t.Error("should not warn for non-roborev")
		}
	})
}

func TestInstall(t *testing.T) {
	t.Run("fresh install creates standalone hook",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}

			err := Install(
				repo.HooksDir, "post-commit", false,
			)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}

			content, _ := os.ReadFile(
				filepath.Join(repo.HooksDir, "post-commit"),
			)
			contentStr := string(content)
			if !strings.HasPrefix(contentStr, "#!/bin/sh\n") {
				t.Error("fresh install should have shebang")
			}
			if !strings.Contains(
				contentStr, PostCommitVersionMarker,
			) {
				t.Error("should have version marker")
			}
		},
	)

	t.Run("embeds after shebang in existing hook",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-commit",
			)
			existing := "#!/bin/sh\necho 'custom'\n"
			os.WriteFile(hookPath, []byte(existing), 0755)

			err := Install(
				repo.HooksDir, "post-commit", false,
			)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}

			content, _ := os.ReadFile(hookPath)
			contentStr := string(content)
			if !strings.Contains(
				contentStr, "echo 'custom'",
			) {
				t.Error("original content should be preserved")
			}
			if !strings.Contains(
				contentStr, PostCommitVersionMarker,
			) {
				t.Error("should have roborev snippet")
			}
		},
	)

	t.Run("function wrapper uses return not exit",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-commit",
			)
			existing := "#!/bin/sh\necho 'custom'\n"
			os.WriteFile(hookPath, []byte(existing), 0755)

			Install(repo.HooksDir, "post-commit", false)

			content, _ := os.ReadFile(hookPath)
			contentStr := string(content)
			if strings.Contains(contentStr, "exit 0") {
				t.Error(
					"embedded snippet must use return, not exit",
				)
			}
			if !strings.Contains(contentStr, "return 0") {
				t.Error("embedded snippet should use return 0")
			}
			if !strings.Contains(
				contentStr, "_roborev_hook() {",
			) {
				t.Error("should use function wrapper")
			}
		},
	)

	t.Run("early exit does not block snippet", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		// Husky-style hook with exit 0 at the end
		husky := "#!/bin/sh\n" +
			". \"$(dirname \"$0\")/_/husky.sh\"\n" +
			"npx lint-staged\n" +
			"exit 0\n"
		os.WriteFile(hookPath, []byte(husky), 0755)

		Install(repo.HooksDir, "post-commit", false)

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		snippetIdx := strings.Index(
			contentStr, "_roborev_hook",
		)
		exitIdx := strings.Index(contentStr, "exit 0")
		if snippetIdx < 0 {
			t.Fatal("snippet should be present")
		}
		if exitIdx < 0 {
			t.Fatal("exit 0 should be preserved")
		}
		if snippetIdx > exitIdx {
			t.Error(
				"roborev snippet should appear before exit 0",
			)
		}
	})

	t.Run("skips current version", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-rewrite",
		)
		current := GeneratePostRewrite()
		os.WriteFile(hookPath, []byte(current), 0755)

		err := Install(
			repo.HooksDir, "post-rewrite", false,
		)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		if string(content) != current {
			t.Error("current hook should not be modified")
		}
	})

	t.Run("upgrades outdated hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		outdated := "#!/bin/sh\n" +
			"# roborev post-commit hook\n" +
			"ROBOREV=\"/usr/local/bin/roborev\"\n" +
			"\"$ROBOREV\" enqueue --quiet 2>/dev/null\n"
		os.WriteFile(hookPath, []byte(outdated), 0755)

		err := Install(
			repo.HooksDir, "post-commit", false,
		)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		if !strings.Contains(
			contentStr, PostCommitVersionMarker,
		) {
			t.Error("should have new version marker")
		}
		if strings.Contains(
			contentStr, "# roborev post-commit hook\n",
		) {
			t.Error("old marker should be removed")
		}
	})

	t.Run("upgrade from v2 to v3", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		v2Hook := "#!/bin/sh\n" +
			"# roborev post-commit hook v2 - " +
			"auto-reviews every commit\n" +
			"ROBOREV=\"/usr/local/bin/roborev\"\n" +
			"if [ ! -x \"$ROBOREV\" ]; then\n" +
			"    ROBOREV=$(command -v roborev " +
			"2>/dev/null)\n" +
			"    [ -z \"$ROBOREV\" ] || " +
			"[ ! -x \"$ROBOREV\" ] && exit 0\n" +
			"fi\n" +
			"\"$ROBOREV\" enqueue --quiet 2>/dev/null\n"
		os.WriteFile(hookPath, []byte(v2Hook), 0755)

		err := Install(
			repo.HooksDir, "post-commit", false,
		)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		if !strings.Contains(
			contentStr, PostCommitVersionMarker,
		) {
			t.Error("should have v3 marker")
		}
		if strings.Contains(contentStr, "hook v2") {
			t.Error("v2 marker should be removed")
		}
	})

	t.Run("upgrades mixed hook preserving user content",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-rewrite",
			)
			mixed := "#!/bin/sh\necho 'user code'\n" +
				"# roborev post-rewrite hook\n" +
				"ROBOREV=\"/usr/bin/roborev\"\n" +
				"\"$ROBOREV\" remap --quiet 2>/dev/null\n"
			os.WriteFile(hookPath, []byte(mixed), 0755)

			err := Install(
				repo.HooksDir, "post-rewrite", false,
			)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}

			content, _ := os.ReadFile(hookPath)
			contentStr := string(content)
			if !strings.Contains(
				contentStr, "echo 'user code'",
			) {
				t.Error("user content should be preserved")
			}
			if !strings.Contains(
				contentStr, PostRewriteVersionMarker,
			) {
				t.Error("should have new version marker")
			}
		},
	)

	t.Run("appends to hooks with various shell shebangs",
		func(t *testing.T) {
			shebangs := []string{
				"#!/bin/sh", "#!/usr/bin/env sh",
				"#!/bin/bash", "#!/usr/bin/env bash",
				"#!/bin/zsh", "#!/usr/bin/env zsh",
				"#!/bin/ksh", "#!/usr/bin/env ksh",
				"#!/bin/dash", "#!/usr/bin/env dash",
			}
			for _, shebang := range shebangs {
				t.Run(shebang, func(t *testing.T) {
					repo := testutil.NewTestRepo(t)
					if err := os.MkdirAll(
						repo.HooksDir, 0755,
					); err != nil {
						t.Fatal(err)
					}
					hookPath := filepath.Join(
						repo.HooksDir, "post-commit",
					)
					existing := shebang + "\necho 'custom'\n"
					os.WriteFile(
						hookPath,
						[]byte(existing),
						0755,
					)

					err := Install(
						repo.HooksDir,
						"post-commit", false,
					)
					if err != nil {
						t.Fatalf(
							"should append to %s: %v",
							shebang, err,
						)
					}

					content, _ := os.ReadFile(hookPath)
					cs := string(content)
					if !strings.Contains(
						cs, "echo 'custom'",
					) {
						t.Error("original should be preserved")
					}
					if !strings.Contains(
						cs, PostCommitVersionMarker,
					) {
						t.Error("roborev should be appended")
					}
				})
			}
		},
	)

	t.Run("refuses non-shell hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		python := "#!/usr/bin/env python3\nprint('hello')\n"
		os.WriteFile(hookPath, []byte(python), 0755)

		err := Install(
			repo.HooksDir, "post-commit", false,
		)
		if err == nil {
			t.Fatal("expected error for non-shell hook")
		}
		if !errors.Is(err, ErrNonShellHook) {
			t.Errorf("should wrap ErrNonShellHook: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		if string(content) != python {
			t.Error("hook should be unchanged")
		}
	})

	t.Run("upgrade returns error on re-read failure",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-commit",
			)
			outdated := "#!/bin/sh\n" +
				"# roborev post-commit hook\n" +
				"ROBOREV=\"/usr/local/bin/roborev\"\n" +
				"\"$ROBOREV\" enqueue --quiet 2>/dev/null\n"
			os.WriteFile(hookPath, []byte(outdated), 0755)

			origReadFile := ReadFile
			ReadFile = func(string) ([]byte, error) {
				return nil, fs.ErrPermission
			}
			t.Cleanup(func() { ReadFile = origReadFile })

			err := Install(
				repo.HooksDir, "post-commit", false,
			)
			if err == nil {
				t.Fatal("expected error from re-read failure")
			}
			if !strings.Contains(err.Error(), "re-read") {
				t.Errorf(
					"error should mention re-read: %v", err,
				)
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf(
					"should wrap ErrPermission: %v", err,
				)
			}
		},
	)

	t.Run("refuses upgrade of non-shell hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		python := "#!/usr/bin/env python3\n" +
			"# reviewed by roborev\nprint('hello')\n"
		os.WriteFile(hookPath, []byte(python), 0755)

		err := Install(
			repo.HooksDir, "post-commit", false,
		)
		if err == nil {
			t.Fatal(
				"expected error for non-shell upgrade",
			)
		}
		if !errors.Is(err, ErrNonShellHook) {
			t.Errorf(
				"should wrap ErrNonShellHook: %v", err,
			)
		}

		content, _ := os.ReadFile(hookPath)
		if string(content) != python {
			t.Error("hook should be unchanged")
		}
	})

	t.Run("force overwrites existing hook", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		existing := "#!/bin/sh\necho 'custom'\n"
		os.WriteFile(hookPath, []byte(existing), 0755)

		err := Install(
			repo.HooksDir, "post-commit", true,
		)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		contentStr := string(content)
		if strings.Contains(contentStr, "echo 'custom'") {
			t.Error("force should overwrite, not append")
		}
		if !strings.Contains(
			contentStr, PostCommitVersionMarker,
		) {
			t.Error("should have roborev content")
		}
	})
}

func TestInstallAll(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test checks Unix exec bits")
	}

	repo := testutil.NewTestRepo(t)
	repo.RemoveHooksDir()

	if err := os.MkdirAll(repo.HooksDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := InstallAll(repo.HooksDir, false); err != nil {
		t.Fatalf("InstallAll: %v", err)
	}

	for _, name := range []string{"post-commit", "post-rewrite"} {
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
	t.Run("generated hook is deleted entirely",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-rewrite",
			)
			os.WriteFile(
				hookPath,
				[]byte(GeneratePostRewrite()),
				0755,
			)

			if err := Uninstall(hookPath); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
				t.Error("should be deleted entirely")
			}
		},
	)

	t.Run("mixed hook preserves non-roborev content",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-rewrite",
			)
			mixed := "#!/bin/sh\necho 'custom logic'\n" +
				GeneratePostRewrite()
			os.WriteFile(hookPath, []byte(mixed), 0755)

			if err := Uninstall(hookPath); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			content, err := os.ReadFile(hookPath)
			if err != nil {
				t.Fatalf("hook should still exist: %v", err)
			}
			cs := string(content)
			if strings.Contains(
				strings.ToLower(cs), "roborev",
			) {
				t.Errorf(
					"roborev content should be removed:\n%s",
					cs,
				)
			}
			if !strings.Contains(cs, "echo 'custom logic'") {
				t.Error("custom content should be preserved")
			}
			if strings.Contains(cs, "\nfi\n") ||
				strings.HasSuffix(
					strings.TrimSpace(cs), "fi",
				) {
				t.Errorf("should not have orphaned fi:\n%s", cs)
			}
		},
	)

	t.Run("v3 function wrapper removed", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		// Simulate a v3 embedded hook
		v3Content := "#!/bin/sh\n" +
			generateEmbeddablePostCommit() +
			"echo 'user code after'\n"
		os.WriteFile(hookPath, []byte(v3Content), 0755)

		if err := Uninstall(hookPath); err != nil {
			t.Fatalf("Uninstall: %v", err)
		}

		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("hook should still exist: %v", err)
		}
		cs := string(content)
		if strings.Contains(cs, "_roborev_hook") {
			t.Errorf(
				"function wrapper should be removed:\n%s", cs,
			)
		}
		if strings.Contains(cs, "return 0") {
			t.Errorf(
				"return 0 should be removed:\n%s", cs,
			)
		}
		if !strings.Contains(cs, "echo 'user code after'") {
			t.Error("user content should be preserved")
		}
	})

	t.Run("v3 mixed hook preserves user content",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-commit",
			)
			v3Mixed := "#!/bin/sh\n" +
				generateEmbeddablePostCommit() +
				"echo 'before'\necho 'after'\n"
			os.WriteFile(hookPath, []byte(v3Mixed), 0755)

			if err := Uninstall(hookPath); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			content, err := os.ReadFile(hookPath)
			if err != nil {
				t.Fatalf("hook should exist: %v", err)
			}
			cs := string(content)
			if strings.Contains(
				strings.ToLower(cs), "roborev",
			) {
				t.Errorf("roborev removed:\n%s", cs)
			}
			if !strings.Contains(cs, "echo 'before'") {
				t.Error("should preserve 'before'")
			}
			if !strings.Contains(cs, "echo 'after'") {
				t.Error("should preserve 'after'")
			}
		},
	)

	t.Run("v0 hook removed", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		v0 := "#!/bin/sh\n" +
			"# RoboRev post-commit hook - " +
			"auto-reviews every commit\n" +
			"roborev enqueue --sha HEAD 2>/dev/null &\n"
		os.WriteFile(hookPath, []byte(v0), 0755)

		if err := Uninstall(hookPath); err != nil {
			t.Fatalf("Uninstall: %v", err)
		}

		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			content, _ := os.ReadFile(hookPath)
			t.Errorf(
				"v0 hook should be deleted:\n%s", content,
			)
		}
	})

	t.Run("v0.5 hook removed", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		v05 := "#!/bin/sh\n" +
			"# RoboRev post-commit hook - " +
			"auto-reviews every commit\n" +
			"ROBOREV=\"/usr/local/bin/roborev\"\n" +
			"if [ ! -x \"$ROBOREV\" ]; then\n" +
			"    ROBOREV=$(command -v roborev) || exit 0\n" +
			"fi\n" +
			"\"$ROBOREV\" enqueue --quiet &\n"
		os.WriteFile(hookPath, []byte(v05), 0755)

		if err := Uninstall(hookPath); err != nil {
			t.Fatalf("Uninstall: %v", err)
		}

		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			content, _ := os.ReadFile(hookPath)
			t.Errorf(
				"v0.5 hook should be deleted:\n%s", content,
			)
		}
	})

	t.Run("v1 hook removed", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-commit",
		)
		v1 := "#!/bin/sh\n" +
			"# RoboRev post-commit hook - " +
			"auto-reviews every commit\n" +
			"ROBOREV=$(command -v roborev 2>/dev/null)\n" +
			"if [ -z \"$ROBOREV\" ] || " +
			"[ ! -x \"$ROBOREV\" ]; then\n" +
			"    ROBOREV=\"/usr/local/bin/roborev\"\n" +
			"    [ ! -x \"$ROBOREV\" ] && exit 0\n" +
			"fi\n" +
			"\"$ROBOREV\" enqueue --quiet 2>/dev/null &\n"
		os.WriteFile(hookPath, []byte(v1), 0755)

		if err := Uninstall(hookPath); err != nil {
			t.Fatalf("Uninstall: %v", err)
		}

		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			content, _ := os.ReadFile(hookPath)
			t.Errorf(
				"v1 hook should be deleted:\n%s", content,
			)
		}
	})

	t.Run("v1 mixed hook removes only roborev block",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-commit",
			)
			v1Block := "# RoboRev post-commit hook - " +
				"auto-reviews every commit\n" +
				"ROBOREV=$(command -v roborev 2>/dev/null)\n" +
				"if [ -z \"$ROBOREV\" ] || " +
				"[ ! -x \"$ROBOREV\" ]; then\n" +
				"    ROBOREV=\"/usr/local/bin/roborev\"\n" +
				"    [ ! -x \"$ROBOREV\" ] && exit 0\n" +
				"fi\n" +
				"\"$ROBOREV\" enqueue --quiet 2>/dev/null &\n"
			mixed := "#!/bin/sh\necho 'custom'\n" + v1Block
			os.WriteFile(hookPath, []byte(mixed), 0755)

			if err := Uninstall(hookPath); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			content, err := os.ReadFile(hookPath)
			if err != nil {
				t.Fatalf("hook should exist: %v", err)
			}
			cs := string(content)
			if strings.Contains(
				strings.ToLower(cs), "roborev",
			) {
				t.Errorf("roborev removed:\n%s", cs)
			}
			if !strings.Contains(cs, "echo 'custom'") {
				t.Error("custom content should be preserved")
			}
		},
	)

	t.Run("no-op for no roborev content", func(t *testing.T) {
		repo := testutil.NewTestRepo(t)
		if err := os.MkdirAll(
			repo.HooksDir, 0755,
		); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(
			repo.HooksDir, "post-rewrite",
		)
		hookContent := "#!/bin/sh\necho 'unrelated'\n"
		os.WriteFile(hookPath, []byte(hookContent), 0755)

		if err := Uninstall(hookPath); err != nil {
			t.Fatalf("Uninstall: %v", err)
		}

		content, _ := os.ReadFile(hookPath)
		if string(content) != hookContent {
			t.Error("hook should be unchanged")
		}
	})

	t.Run("custom if-block after snippet preserved",
		func(t *testing.T) {
			repo := testutil.NewTestRepo(t)
			if err := os.MkdirAll(
				repo.HooksDir, 0755,
			); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(
				repo.HooksDir, "post-rewrite",
			)
			mixed := "#!/bin/sh\n" +
				GeneratePostRewrite() +
				"if [ -f .notify ]; then\n" +
				"    echo 'send notification'\n" +
				"fi\n"
			os.WriteFile(hookPath, []byte(mixed), 0755)

			if err := Uninstall(hookPath); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			content, err := os.ReadFile(hookPath)
			if err != nil {
				t.Fatalf("hook should exist: %v", err)
			}
			cs := string(content)
			if !strings.Contains(cs, "send notification") {
				t.Error("user if-block should be preserved")
			}
			if strings.Contains(cs, "remap --quiet") {
				t.Errorf("snippet should be removed:\n%s", cs)
			}
			// Balanced if/fi
			lines := strings.Split(cs, "\n")
			ifCount, fiCount := 0, 0
			for _, l := range lines {
				tr := strings.TrimSpace(l)
				if strings.HasPrefix(tr, "if ") {
					ifCount++
				}
				if tr == "fi" {
					fiCount++
				}
			}
			if ifCount != fiCount {
				t.Errorf(
					"if/fi mismatch: %d vs %d in:\n%s",
					ifCount, fiCount, cs,
				)
			}
		},
	)

	t.Run("missing file is no-op", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nonexistent")
		if err := Uninstall(path); err != nil {
			t.Errorf("should be no-op: %v", err)
		}
	})
}

func TestVersionMarker(t *testing.T) {
	if m := VersionMarker("post-commit"); m != PostCommitVersionMarker {
		t.Errorf("got %q, want %q", m, PostCommitVersionMarker)
	}
	if m := VersionMarker("post-rewrite"); m != PostRewriteVersionMarker {
		t.Errorf("got %q, want %q", m, PostRewriteVersionMarker)
	}
	if m := VersionMarker("unknown"); m != "" {
		t.Errorf("unknown should return empty, got %q", m)
	}
}

func TestHasRealErrors(t *testing.T) {
	realErr := errors.New("permission denied")

	t.Run("nil", func(t *testing.T) {
		if HasRealErrors(nil) {
			t.Error("nil should return false")
		}
	})

	t.Run("only non-shell", func(t *testing.T) {
		err := fmt.Errorf("hook: %w", ErrNonShellHook)
		if HasRealErrors(err) {
			t.Error("single ErrNonShellHook should return false")
		}
	})

	t.Run("only real", func(t *testing.T) {
		if !HasRealErrors(realErr) {
			t.Error("real error should return true")
		}
	})

	t.Run("joined all non-shell", func(t *testing.T) {
		err := errors.Join(
			fmt.Errorf("a: %w", ErrNonShellHook),
			fmt.Errorf("b: %w", ErrNonShellHook),
		)
		if HasRealErrors(err) {
			t.Error("joined non-shell only should return false")
		}
	})

	t.Run("joined mixed", func(t *testing.T) {
		err := errors.Join(
			fmt.Errorf("a: %w", ErrNonShellHook),
			realErr,
		)
		if !HasRealErrors(err) {
			t.Error("joined with real error should return true")
		}
	})

	t.Run("joined all real", func(t *testing.T) {
		err := errors.Join(realErr, errors.New("disk full"))
		if !HasRealErrors(err) {
			t.Error("joined real errors should return true")
		}
	})
}
