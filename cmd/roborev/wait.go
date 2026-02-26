package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/spf13/cobra"
)

func waitCmd() *cobra.Command {
	var (
		shaFlag    string
		forceJobID bool
		quiet      bool
	)

	cmd := &cobra.Command{
		Use:   "wait [job_id|sha]",
		Short: "Wait for an existing review job to complete",
		Long: `Wait for an already-running review job to complete, without enqueuing a new one.

When using an external coding agent to perform a review-fix refinement loop,
wait provides a token-efficient method for letting the agent wait for a roborev
review to complete. The post-commit hook triggers the review, and the agent
calls wait to block until the result is ready.

The argument can be a job ID (numeric) or a git ref (commit SHA, branch, HEAD).
If no argument is given, defaults to HEAD.

Exit codes:
  0  Review completed with verdict PASS
  1  Any failure (FAIL verdict, no job found, job error)

Examples:
  roborev wait                   # Wait for most recent job for HEAD
  roborev wait abc123            # Wait for most recent job for commit
  roborev wait 42                # Job ID (if "42" is not a valid git ref)
  roborev wait --job 42          # Force as job ID
  roborev wait --sha HEAD~1      # Wait for job matching HEAD~1`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// In quiet mode, suppress cobra's error output
			if quiet {
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
			}

			// Validate flag/arg combinations
			if len(args) > 0 && shaFlag != "" {
				return fmt.Errorf("cannot use both a positional argument and --sha")
			}
			if forceJobID && len(args) == 0 {
				return fmt.Errorf("--job requires a job ID argument")
			}

			// Resolve the target to a job ID (local validation first,
			// daemon contact deferred until actually needed)
			var jobID int64
			var ref string // git ref to resolve via findJobForCommit

			if shaFlag != "" {
				ref = shaFlag
			} else if len(args) > 0 {
				arg := args[0]
				if forceJobID {
					// --job flag: treat as job ID directly
					id, err := strconv.ParseInt(arg, 10, 64)
					if err != nil || id <= 0 {
						return fmt.Errorf("invalid job ID: %s", arg)
					}
					jobID = id
				} else {
					// Try to resolve as git ref first (handles numeric SHAs like "123456")
					if repoRoot, err := git.GetRepoRoot("."); err == nil {
						if _, err := git.ResolveSHA(repoRoot, arg); err == nil {
							ref = arg
						}
					}
					if ref == "" {
						// Not a valid git ref — try as numeric job ID
						if id, err := strconv.ParseInt(arg, 10, 64); err == nil && id > 0 {
							jobID = id
						} else {
							return fmt.Errorf("argument %q is not a valid git ref or job ID", arg)
						}
					}
				}
			} else {
				ref = "HEAD"
			}

			// Validate git ref before contacting daemon
			var sha string
			if ref != "" {
				repoRoot, _ := git.GetRepoRoot(".")
				resolved, err := git.ResolveSHA(repoRoot, ref)
				if err != nil {
					return fmt.Errorf("invalid git ref: %s", ref)
				}
				sha = resolved
			}

			// All local validation passed — now ensure daemon is running
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			// If we have a ref to resolve, use findJobForCommit
			if sha != "" && jobID == 0 {
				mainRoot, _ := git.GetMainRepoRoot(".")
				if mainRoot == "" {
					mainRoot, _ = git.GetRepoRoot(".")
				}
				job, err := findJobForCommit(mainRoot, sha)
				if err != nil {
					return err
				}
				if job == nil {
					if !quiet {
						cmd.Printf("No job found for %s\n", ref)
					}
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
					return &exitError{code: 1}
				}
				jobID = job.ID
			}

			addr := getDaemonAddr()
			err := waitForJob(cmd, addr, jobID, quiet)
			if err != nil {
				// Map ErrJobNotFound to exit 1 with a user-facing message
				// (waitForJob returns a plain error to stay compatible with reviewCmd)
				if errors.Is(err, ErrJobNotFound) {
					if !quiet {
						cmd.Printf("No job found for job %d\n", jobID)
					}
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
					return &exitError{code: 1}
				}
				if _, isExitErr := err.(*exitError); isExitErr {
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
				}
			}
			return err
		},
	}

	cmd.Flags().StringVar(&shaFlag, "sha", "", "git ref to find the most recent job for")
	cmd.Flags().BoolVar(&forceJobID, "job", false, "force argument to be treated as job ID")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output (for use in hooks)")

	return cmd
}
