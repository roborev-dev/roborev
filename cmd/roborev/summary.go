package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func summaryCmd() *cobra.Command {
	var (
		repoPath   string
		branch     string
		since      string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show aggregate review statistics",
		Long: `Show aggregate review statistics from existing data.

Surfaces pass/fail trends, agent effectiveness, review duration,
fix adoption rates, and codebase hotspots.

Examples:
  roborev summary                     # Last 7 days, current repo
  roborev summary --since 30d         # Last 30 days
  roborev summary --branch main       # Filter by branch
  roborev summary --repo /path/to/repo
  roborev summary --json              # Structured output for scripting`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			addr := getDaemonAddr()

			// Auto-resolve repo from cwd when not specified
			if repoPath == "" {
				if root, err := git.GetMainRepoRoot("."); err == nil {
					repoPath = root
				}
			} else {
				if root, err := git.GetMainRepoRoot(repoPath); err == nil {
					repoPath = root
				}
			}

			// Build query
			params := url.Values{}
			if repoPath != "" {
				params.Set("repo", repoPath)
			}
			if branch != "" {
				params.Set("branch", branch)
			}
			if since != "" {
				params.Set("since", since)
			}

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get(addr + "/api/summary?" + params.Encode())
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("daemon returned %s", resp.Status)
			}

			var summary storage.Summary
			if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(summary)
			}

			printSummary(cmd, summary)
			return nil
		},
	}

	cmd.Flags().StringVar(&repoPath, "repo", "", "scope to a single repo (default: current repo)")
	cmd.Flags().StringVar(&branch, "branch", "", "scope to a single branch")
	cmd.Flags().StringVar(&since, "since", "7d", "time window (e.g. 24h, 7d, 30d)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "structured output for scripting")

	return cmd
}

func printSummary(cmd *cobra.Command, s storage.Summary) {
	repoLabel := s.RepoPath
	if repoLabel != "" {
		repoLabel = filepath.Base(repoLabel)
	} else {
		repoLabel = "all repos"
	}

	sinceLabel := formatSince(s.Since)
	header := fmt.Sprintf("Review Summary: %s", repoLabel)
	if s.Branch != "" {
		header += fmt.Sprintf(" (%s)", s.Branch)
	}
	header += fmt.Sprintf(" [last %s]", sinceLabel)
	cmd.Println(header)
	cmd.Println()

	// Overview
	cmd.Println("Overview")
	o := s.Overview
	cmd.Printf("  Total: %d | Done: %d | Failed: %d | Canceled: %d | Queued: %d | Running: %d\n",
		o.Total, o.Done+o.Applied+o.Rebased, o.Failed, o.Canceled, o.Queued, o.Running)
	if o.Applied > 0 || o.Rebased > 0 {
		cmd.Printf("  Fix patches: %d applied, %d rebased\n", o.Applied, o.Rebased)
	}
	cmd.Println()

	// Verdicts
	if s.Verdicts.Total > 0 {
		cmd.Println("Verdicts")
		v := s.Verdicts
		cmd.Printf("  Pass: %d | Fail: %d | Addressed: %d | Pass rate: %.0f%%\n",
			v.Passed, v.Failed, v.Addressed, v.PassRate*100)
		cmd.Println()
	}

	// Agent breakdown
	if len(s.Agents) > 0 {
		cmd.Println("Agent Breakdown")
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "  Agent\tJobs\tPass\tFail\tErrors\tPass Rate\tMedian\n")
		for _, a := range s.Agents {
			passRate := "-"
			if a.Passed+a.Failed > 0 {
				passRate = fmt.Sprintf("%.0f%%", a.PassRate*100)
			}
			median := "-"
			if a.MedianSecs > 0 {
				median = formatDuration(a.MedianSecs)
			}
			fmt.Fprintf(w, "  %s\t%d\t%d\t%d\t%d\t%s\t%s\n",
				a.Agent, a.Total, a.Passed, a.Failed, a.Errors, passRate, median)
		}
		w.Flush()
		cmd.Println()
	}

	// Duration stats
	if s.Duration.ReviewP50 > 0 {
		cmd.Println("Duration")
		cmd.Printf("  Review:  p50=%s  p90=%s  p99=%s\n",
			formatDuration(s.Duration.ReviewP50),
			formatDuration(s.Duration.ReviewP90),
			formatDuration(s.Duration.ReviewP99))
		cmd.Printf("  Queue:   p50=%s  p90=%s  p99=%s\n",
			formatDuration(s.Duration.QueueP50),
			formatDuration(s.Duration.QueueP90),
			formatDuration(s.Duration.QueueP99))
		cmd.Println()
	}

	// Job types
	if len(s.JobTypes) > 0 {
		cmd.Println("Job Types")
		for _, t := range s.JobTypes {
			line := fmt.Sprintf("  %-10s %d", t.Type, t.Count)
			if t.Applied > 0 || t.Rebased > 0 {
				line += fmt.Sprintf(" (applied: %d, rebased: %d)", t.Applied, t.Rebased)
			}
			cmd.Println(line)
		}
		cmd.Println()
	}

	// Hotspots
	if len(s.Hotspots) > 0 {
		cmd.Println("Hotspots (most failures)")
		for _, h := range s.Hotspots {
			cmd.Printf("  %s  %d failures\n", shortRef(h.GitRef), h.Failures)
		}
		cmd.Println()
	}

	// Failures
	if s.Failures.Total > 0 {
		cmd.Println("Failures")
		cmd.Printf("  Total: %d | Retries: %d\n", s.Failures.Total, s.Failures.Retries)
		if len(s.Failures.Errors) > 0 {
			cmd.Println("  By category:")
			for cat, count := range s.Failures.Errors {
				cmd.Printf("    %-12s %d\n", cat, count)
			}
		}
		cmd.Println()
	}

	if s.Overview.Total == 0 {
		cmd.Println("No review data for this time window.")
	}
}

// formatDuration formats seconds into a human-readable string.
func formatDuration(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", secs)
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// formatSince returns a human-friendly label for how long ago the since time was.
func formatSince(since time.Time) string {
	d := time.Since(since)
	hours := d.Hours()
	if hours < 48 {
		return fmt.Sprintf("%.0fh", hours)
	}
	days := hours / 24
	if days < 14 {
		return fmt.Sprintf("%.0fd", days)
	}
	return fmt.Sprintf("%.0fw", days/7)
}
