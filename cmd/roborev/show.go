package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func showCmd() *cobra.Command {
	var forceJobID bool
	var showPrompt bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "show [job_id|sha]",
		Short: "Show review for a commit or job",
		Long: `Show review output for a commit or job.

The argument can be either a job ID (numeric) or a commit SHA.
Job IDs are displayed in review notifications and the TUI.

In a git repo, the argument is first tried as a git ref. If that fails
and it's numeric, it's treated as a job ID. Use --job to force job ID.

Examples:
  roborev show              # Show review for HEAD
  roborev show abc123       # Show review for commit
  roborev show 42           # Job ID (if "42" is not a valid git ref)
  roborev show --job 42     # Force as job ID even if "42" is a valid ref
  roborev show --prompt 42  # Show the prompt sent to the agent`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running (and restart if version mismatch)
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			addr := getDaemonAddr()
			client := &http.Client{Timeout: 5 * time.Second}

			var queryURL string
			var displayRef string

			if len(args) == 0 {
				if forceJobID {
					return fmt.Errorf("--job requires a job ID argument")
				}
				// Default to HEAD
				sha := "HEAD"
				if root, err := git.GetRepoRoot("."); err == nil {
					if resolved, err := git.ResolveSHA(root, sha); err == nil {
						sha = resolved
					}
				}
				queryURL = addr + "/api/review?sha=" + sha
				displayRef = git.ShortSHA(sha)
			} else {
				arg := args[0]
				var isJobID bool
				var resolvedSHA string

				if forceJobID {
					isJobID = true
				} else {
					// Try to resolve as SHA first (handles numeric SHAs like "123456")
					if root, err := git.GetRepoRoot("."); err == nil {
						if resolved, err := git.ResolveSHA(root, arg); err == nil {
							resolvedSHA = resolved
						}
					}
					// If not resolvable as SHA and is numeric, treat as job ID
					if resolvedSHA == "" {
						if _, err := strconv.ParseInt(arg, 10, 64); err == nil {
							isJobID = true
						}
					}
				}

				if isJobID {
					queryURL = addr + "/api/review?job_id=" + arg
					displayRef = "job " + arg
				} else {
					sha := arg
					if resolvedSHA != "" {
						sha = resolvedSHA
					}
					queryURL = addr + "/api/review?sha=" + sha
					displayRef = git.ShortSHA(sha)
				}
			}

			resp, err := client.Get(queryURL)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon (is it running?)")
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("no review found for %s", displayRef)
			}

			var review storage.Review
			if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(&review)
			}

			// Avoid redundant "job X (job X, ...)" output
			if strings.HasPrefix(displayRef, "job ") {
				fmt.Printf("Review for %s (by %s)\n", displayRef, review.Agent)
			} else {
				fmt.Printf("Review for %s (job %d, by %s)\n", displayRef, review.JobID, review.Agent)
			}
			fmt.Println(strings.Repeat("-", 60))
			if showPrompt {
				fmt.Println(review.Prompt)
			} else {
				fmt.Println(review.Output)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&forceJobID, "job", false, "force argument to be treated as job ID")
	cmd.Flags().BoolVar(&showPrompt, "prompt", false, "show the prompt sent to the agent instead of the review output")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}
