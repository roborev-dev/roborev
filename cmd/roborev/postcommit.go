package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/spf13/cobra"
)

// hookHTTPClient is used for hook HTTP requests. Short timeout
// ensures hooks never block commits if the daemon stalls.
var hookHTTPClient = &http.Client{Timeout: 3 * time.Second}

func postCommitCmd() *cobra.Command {
	var (
		repoPath   string
		baseBranch string
	)

	cmd := &cobra.Command{
		Use:           "post-commit",
		Short:         "Hook entry point: enqueue a review after commit",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoPath == "" {
				repoPath = "."
			}

			root, err := git.GetRepoRoot(repoPath)
			if err != nil {
				return nil // Not a repo — silent exit for hooks
			}

			if git.IsRebaseInProgress(root) {
				return nil
			}

			if err := ensureDaemon(); err != nil {
				return nil // Can't reach daemon — don't block commit
			}

			var gitRef string
			if ref, ok := tryBranchReview(root, baseBranch); ok {
				gitRef = ref
			} else {
				gitRef = "HEAD"
			}

			branchName := git.GetCurrentBranch(root)

			reqBody, _ := json.Marshal(daemon.EnqueueRequest{
				RepoPath: root,
				GitRef:   gitRef,
				Branch:   branchName,
			})

			resp, err := hookHTTPClient.Post(
				serverAddr+"/api/enqueue",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				return nil // Network error — don't block commit
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)

			return nil
		},
	}

	cmd.Flags().StringVar(
		&repoPath, "repo", "",
		"path to git repository (default: current directory)",
	)
	cmd.Flags().StringVar(
		&baseBranch, "base", "",
		"base branch for branch review comparison",
	)

	// Accept --quiet without error for backward compat with
	// old hooks that called `roborev enqueue --quiet`.
	var quiet bool
	cmd.Flags().BoolVarP(
		&quiet, "quiet", "q", false,
		"accepted for backward compatibility (no-op)",
	)
	_ = cmd.Flags().MarkHidden("quiet")

	return cmd
}

// enqueueCmd returns a hidden backward-compatibility alias
// for postCommitCmd. Old hooks that call `roborev enqueue`
// continue to work.
func enqueueCmd() *cobra.Command {
	cmd := postCommitCmd()
	cmd.Use = "enqueue"
	cmd.Hidden = true
	return cmd
}
