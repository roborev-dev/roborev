package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/githook"
	"github.com/roborev-dev/roborev/internal/storage"
)

// exitError is an error that signals a specific exit code
type exitError struct {
	code int
}

func (e *exitError) Error() string {
	return fmt.Sprintf("exit code %d", e.code)
}

func shortRef(ref string) string {
	// For ranges like "abc123..def456", show as "abc123..def456" (up to 17 chars)
	// For single SHAs, truncate to 7 chars
	if strings.Contains(ref, "..") {
		if len(ref) > 17 {
			return ref[:17]
		}
		return ref
	}
	return git.ShortSHA(ref)
}

// shortJobRef returns a display-friendly ref for a job, handling special job types.
// Task jobs (no CommitID, no DiffContent) display their GitRef directly (run, analyze, or custom label).
// Regular review jobs display their GitRef shortened.
func shortJobRef(job storage.ReviewJob) string {
	// Task jobs are identified by: no CommitID, no DiffContent
	// (Note: Prompt field is set for ALL jobs after worker starts, so can't use that)
	if job.CommitID == nil && job.DiffContent == nil {
		// Map legacy "prompt" to "run" for display consistency
		if job.GitRef == "prompt" {
			return "run"
		}
		// Return GitRef directly as the display label (run, analyze, or custom)
		return job.GitRef
	}
	return shortRef(job.GitRef)
}

// resolveReasoningWithFast returns the effective reasoning value, applying
// the --fast shorthand only when --reasoning wasn't explicitly set.
func resolveReasoningWithFast(reasoning string, fast bool, reasoningExplicitlySet bool) string {
	if fast && !reasoningExplicitlySet {
		return "fast"
	}
	return reasoning
}

// autoInstallHooks upgrades outdated hooks and installs
// companion hooks (e.g. post-rewrite when post-commit
// exists). It does NOT install hooks from scratch so that
// explicit uninstall-hook is respected.
func autoInstallHooks(repoPath string) {
	hooksDir, err := git.GetHooksPath(repoPath)
	if err != nil {
		return
	}
	for _, name := range []string{"post-commit", "post-rewrite"} {
		marker := githook.VersionMarker(name)
		if githook.NeedsUpgrade(repoPath, name, marker) ||
			githook.Missing(repoPath, name) {
			if err := githook.Install(hooksDir, name, false); err != nil {
				// Non-shell hooks are a persistent condition;
				// don't warn on every invocation.
				if !errors.Is(err, githook.ErrNonShellHook) {
					fmt.Fprintf(os.Stderr,
						"Warning: auto-install %s hook: %v\n",
						name, err)
				}
			}
		}
	}
}
