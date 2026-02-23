package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/spf13/cobra"
)

func logCmd() *cobra.Command {
	var (
		showPath  bool
		rawOutput bool
	)

	cmd := &cobra.Command{
		Use:   "log <job-id>",
		Short: "Show agent output log for a job",
		Long: `Show the agent output log for a completed or running job.

By default, JSONL agent output is rendered as human-readable
progress lines (tool calls, agent text). Non-JSON logs are
printed as-is.

Use --raw to print the original log bytes unchanged.

Examples:
  roborev log 42          # Human-friendly rendered output
  roborev log --raw 42    # Raw log bytes (JSONL)
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

			f, err := os.Open(daemon.JobLogPath(jobID))
			if err != nil {
				return fmt.Errorf(
					"no log for job %d (file: %s)",
					jobID, daemon.JobLogPath(jobID),
				)
			}
			defer f.Close()

			if rawOutput {
				_, err := io.Copy(os.Stdout, f)
				if err != nil {
					return fmt.Errorf("reading log: %w", err)
				}
				return nil
			}

			return renderJobLog(
				f, os.Stdout, writerIsTerminal(os.Stdout),
			)
		},
	}

	cmd.Flags().BoolVar(
		&showPath, "path", false,
		"print the log file path instead of contents",
	)
	cmd.Flags().BoolVar(
		&rawOutput, "raw", false,
		"print raw log bytes without formatting",
	)

	cmd.AddCommand(logCleanCmd())
	return cmd
}

// renderJobLog reads a job log file and writes human-friendly
// output. JSONL lines are processed through streamFormatter for
// compact tool/text rendering. Non-JSON lines are printed as-is.
func renderJobLog(r io.Reader, w io.Writer, isTTY bool) error {
	scanner := bufio.NewScanner(r)
	// Allow up to 1MB per line (agent JSON events can be large)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmtr := newStreamFormatter(w, isTTY)
	hasJSON := false

	for scanner.Scan() {
		line := scanner.Text()
		if looksLikeJSON(line) {
			hasJSON = true
			// Feed through streamFormatter (adds newline internally)
			if _, err := fmtr.Write([]byte(line + "\n")); err != nil {
				return err
			}
		} else if !hasJSON {
			// Non-JSON log — print lines directly
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		// Once we've seen JSON, skip non-JSON lines (noise/blank)
	}

	fmtr.Flush()
	return scanner.Err()
}

// looksLikeJSON does a quick check for a JSON object line.
func looksLikeJSON(line string) bool {
	for _, c := range line {
		switch c {
		case ' ', '\t':
			continue
		case '{':
			// Quick validation: try to parse just the "type" field
			var probe struct{ Type string }
			return json.Unmarshal([]byte(line), &probe) == nil
		default:
			return false
		}
	}
	return false
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
			if maxDays < 0 || maxDays > 3650 {
				return fmt.Errorf(
					"--days must be between 0 and 3650",
				)
			}
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
