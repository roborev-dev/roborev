package main

import (
	"fmt"
	"strings"

	"github.com/roborev-dev/roborev/cmd/roborev/tui"
	"github.com/spf13/cobra"
)

func tuiCmd() *cobra.Command {
	var addr string
	var repoFilter string
	var branchFilter string

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal UI for monitoring reviews",
		Long: `Interactive terminal UI for monitoring reviews.

Use --repo and --branch flags to launch the TUI pre-filtered, useful for
side-by-side working when you want to focus on a specific repo or branch.
When set via flags, the filter is locked and cannot be changed in the TUI.

Without a value, --repo resolves to the current repo and --branch resolves
to the current branch. Use = syntax for explicit values:
  roborev tui --repo                  # current repo
  roborev tui --repo=/path/to/repo    # explicit repo path
  roborev tui --branch                # current branch
  roborev tui --branch=feature-x      # explicit branch name
  roborev tui --repo --branch         # current repo + branch`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon error: %w", err)
			}

			if addr == "" {
				addr = getDaemonAddr()
			} else if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
				addr = "http://" + addr
			}

			if cmd.Flags().Changed("repo") {
				resolved, err := resolveRepoFlag(repoFilter)
				if err != nil {
					return fmt.Errorf("--repo: %w", err)
				}
				repoFilter = resolved
			}
			if cmd.Flags().Changed("branch") {
				branchRepo := "."
				if repoFilter != "" {
					branchRepo = repoFilter
				}
				resolved, err := resolveBranchFlag(
					branchFilter, branchRepo,
				)
				if err != nil {
					return fmt.Errorf("--branch: %w", err)
				}
				branchFilter = resolved
			}

			return tui.Run(tui.Config{
				ServerAddr:   addr,
				RepoFilter:   repoFilter,
				BranchFilter: branchFilter,
			})
		},
	}

	cmd.Flags().StringVar(
		&addr, "addr", "",
		"daemon address (default: auto-detect)",
	)
	cmd.Flags().StringVar(
		&repoFilter, "repo", "",
		"lock filter to a repo (default: current repo)",
	)
	cmd.Flag("repo").NoOptDefVal = "."
	cmd.Flags().StringVar(
		&branchFilter, "branch", "",
		"lock filter to a branch (default: current branch)",
	)
	cmd.Flag("branch").NoOptDefVal = "HEAD"

	return cmd
}
