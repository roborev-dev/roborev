package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/spf13/cobra"
)

func logCmd() *cobra.Command {
	var showPath bool

	cmd := &cobra.Command{
		Use:   "log <job-id>",
		Short: "Show agent output log for a job",
		Long: `Show the raw agent output log for a completed or running job.

Logs are plain-text files written as the agent runs. They persist
after the job finishes, unlike the in-memory tail buffer.

Examples:
  roborev log 42          # Print job 42's log to stdout
  roborev log --path 42   # Print the log file path`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid job ID: %w", err)
			}

			if showPath {
				fmt.Println(daemon.JobLogPath(jobID))
				return nil
			}

			data, err := daemon.ReadJobLog(jobID)
			if err != nil {
				return fmt.Errorf(
					"no log for job %d (file: %s)",
					jobID, daemon.JobLogPath(jobID),
				)
			}
			_, _ = os.Stdout.Write(data)
			return nil
		},
	}

	cmd.Flags().BoolVar(
		&showPath, "path", false,
		"print the log file path instead of contents",
	)

	cmd.AddCommand(logCleanCmd())
	return cmd
}

func logCleanCmd() *cobra.Command {
	var maxDays int

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove old job log files",
		Long: `Remove job log files older than the specified age.

Examples:
  roborev log clean          # Remove logs older than 7 days
  roborev log clean --days 3 # Remove logs older than 3 days`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			maxAge := time.Duration(maxDays) * 24 * time.Hour
			n := daemon.CleanJobLogs(maxAge)
			fmt.Printf("Removed %d log file(s)\n", n)
			return nil
		},
	}

	cmd.Flags().IntVar(
		&maxDays, "days", 7,
		"remove logs older than this many days",
	)

	return cmd
}
