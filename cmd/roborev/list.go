package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func listCmd() *cobra.Command {
	var (
		branch     string
		repoPath   string
		limit      int
		status     string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List review jobs",
		Long: `List review jobs with optional filtering.

By default, lists jobs for the current repo and branch.

Examples:
  roborev list                        # Jobs for current repo/branch
  roborev list --json                 # Output as JSON
  roborev list --branch main          # Jobs for main branch
  roborev list --status done          # Only completed jobs
  roborev list --limit 5              # Show at most 5 jobs`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			addr := getDaemonAddr()

			// Auto-resolve repo from cwd when not specified.
			// Use worktree root for branch detection, main repo root for API queries
			// (daemon stores jobs under the main repo path).
			localRepoPath := repoPath
			if localRepoPath == "" {
				if root, err := git.GetRepoRoot("."); err == nil {
					localRepoPath = root
				}
			}
			if repoPath == "" {
				if root, err := git.GetMainRepoRoot("."); err == nil {
					repoPath = root
				}
			} else {
				// Normalize explicit --repo to main repo root so worktree
				// paths match the daemon's stored repo path.
				if root, err := git.GetMainRepoRoot(repoPath); err == nil {
					repoPath = root
				}
			}
			// Auto-resolve branch from the target repo when not specified.
			if branch == "" && localRepoPath != "" {
				branch = git.GetCurrentBranch(localRepoPath)
			}

			// Build query URL
			params := url.Values{}
			if repoPath != "" {
				params.Set("repo", repoPath)
			}
			if branch != "" {
				params.Set("branch", branch)
				params.Set("branch_include_empty", "true")
			}
			if status != "" {
				params.Set("status", status)
			}
			params.Set("limit", strconv.Itoa(limit))

			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(addr + "/api/jobs?" + params.Encode())
			if err != nil {
				return fmt.Errorf("failed to connect to daemon (is it running?)")
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("daemon returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
			}

			var jobsResp struct {
				Jobs    []storage.ReviewJob `json:"jobs"`
				HasMore bool                `json:"has_more"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(jobsResp.Jobs)
			}

			if len(jobsResp.Jobs) == 0 {
				fmt.Println("No jobs found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "ID\tSHA\tRepo\tAgent\tStatus\tTime\n")
			for _, j := range jobsResp.Jobs {
				elapsed := ""
				if j.StartedAt != nil {
					if j.FinishedAt != nil {
						elapsed = j.FinishedAt.Sub(*j.StartedAt).Round(time.Second).String()
					} else {
						elapsed = time.Since(*j.StartedAt).Round(time.Second).String() + "..."
					}
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
					j.ID, shortRef(j.GitRef), j.RepoName, j.Agent, j.Status, elapsed)
			}
			w.Flush()

			if jobsResp.HasMore {
				fmt.Println("(more results available, use --limit to increase)")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&branch, "branch", "", "filter by branch (default: current branch)")
	cmd.Flags().StringVar(&repoPath, "repo", "", "filter by repo path (default: current repo)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max number of jobs to return")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (queued, running, done, failed)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}
