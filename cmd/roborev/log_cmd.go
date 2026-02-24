package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
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

			out := cmd.OutOrStdout()

			if showPath {
				fmt.Fprintln(out, daemon.JobLogPath(jobID))
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
				_, err := io.Copy(out, f)
				if isBrokenPipe(err) {
					return nil
				}
				if err != nil {
					return fmt.Errorf("reading log: %w", err)
				}
				return nil
			}

			err = renderJobLog(
				f, out, writerIsTerminal(out),
			)
			if isBrokenPipe(err) {
				return nil
			}
			return err
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
	return renderJobLogWith(r, newStreamFormatter(w, isTTY), w)
}

// renderJobLogWith renders a job log using a pre-configured
// streamFormatter. plainW receives non-JSON lines directly.
func renderJobLogWith(
	r io.Reader, fmtr *streamFormatter, plainW io.Writer,
) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		// ReadString returns data even on error (e.g. EOF
		// without trailing newline), so process before checking.
		line = strings.TrimRight(line, "\n\r")
		if line != "" {
			if looksLikeJSON(line) {
				if _, werr := fmtr.Write(
					[]byte(line + "\n"),
				); werr != nil {
					return werr
				}
			} else {
				// Non-JSON lines: sanitize ANSI/control sequences
				// to prevent terminal spoofing from agent stderr,
				// then print.
				line = sanitizeControlKeepNewlines(line)
				if _, werr := fmt.Fprintln(plainW, line); werr != nil {
					return werr
				}
			}
		} else if err != io.EOF {
			// Preserve blank lines for spacing in rendered output.
			if _, werr := fmt.Fprintln(plainW); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}

	fmtr.Flush()
	return nil
}

// looksLikeJSON returns true if line is a JSON object with a
// non-empty "type" field, matching the stream event format used
// by Claude Code, Codex, and Gemini CLI.
func looksLikeJSON(line string) bool {
	for _, c := range line {
		switch c {
		case ' ', '\t':
			continue
		case '{':
			var probe struct{ Type string }
			if json.Unmarshal([]byte(line), &probe) != nil {
				return false
			}
			return probe.Type != ""
		default:
			return false
		}
	}
	return false
}

// isBrokenPipe returns true if err is a broken pipe (EPIPE) error,
// which happens when output is piped to tools like head that close
// the read end early.
func isBrokenPipe(err error) bool {
	return err != nil && errors.Is(err, syscall.EPIPE)
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
