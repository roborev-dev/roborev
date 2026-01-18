package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/roborev/internal/config"
	"github.com/wesm/roborev/internal/git"
	"github.com/wesm/roborev/internal/storage"
)

func promptCmd() *cobra.Command {
	var (
		agentName  string
		reasoning  string
		wait       bool
		quiet      bool
		noContext  bool
	)

	cmd := &cobra.Command{
		Use:   "prompt [prompt-text]",
		Short: "Execute an ad-hoc prompt with an AI agent",
		Long: `Execute an arbitrary prompt using an AI agent.

This command runs a prompt directly with an agent, useful for ad-hoc
work that may not be a traditional code review.

The prompt can be provided as:
  1. A positional argument: roborev prompt "your prompt here"
  2. Via stdin: echo "your prompt" | roborev prompt

By default, the job is enqueued and the command returns immediately.
Use --wait to wait for completion and display the result.

By default, context about the repository (name, path, and any project
guidelines from .roborev.toml) is included. Use --no-context to disable.

Examples:
  roborev prompt "Explain the architecture of this codebase"
  roborev prompt --agent claude-code "Refactor the error handling in main.go"
  roborev prompt --reasoning thorough "Find potential security issues"
  roborev prompt --wait "What does the main function do?"
  roborev prompt --no-context "What is 2+2?"
  cat instructions.txt | roborev prompt --wait
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPrompt(cmd, args, agentName, reasoning, wait, quiet, !noContext)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use (default: from config)")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard, or thorough (default)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for job to complete and show result")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output (just enqueue)")
	cmd.Flags().BoolVar(&noContext, "no-context", false, "don't include repository context in prompt")

	return cmd
}

func runPrompt(cmd *cobra.Command, args []string, agentName, reasoningStr string, wait, quiet, includeContext bool) error {
	// Get prompt from args or stdin
	var promptText string
	if len(args) > 0 {
		promptText = strings.Join(args, " ")
	} else {
		// Read from stdin
		stat, err := os.Stdin.Stat()
		if err != nil {
			return fmt.Errorf("unable to read stdin: %w", err)
		}
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			// Stdin has data (piped) - use io.ReadAll to handle large prompts
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			promptText = string(data)
		} else {
			return fmt.Errorf("no prompt provided - pass as argument or pipe via stdin")
		}
	}

	if strings.TrimSpace(promptText) == "" {
		return fmt.Errorf("empty prompt")
	}

	// Determine working directory (use git repo root if in a repo, otherwise cwd)
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Try to use git repo root if available
	repoRoot := workDir
	if root, err := git.GetRepoRoot(workDir); err == nil {
		repoRoot = root
	}

	// Build the full prompt with context if enabled
	fullPrompt := promptText
	if includeContext {
		fullPrompt = buildPromptWithContext(repoRoot, promptText)
	}

	// Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	// Build the request
	reqBody, _ := json.Marshal(map[string]interface{}{
		"repo_path":     repoRoot,
		"git_ref":       "prompt",
		"agent":         agentName,
		"reasoning":     reasoningStr,
		"custom_prompt": fullPrompt,
	})

	resp, err := http.Post(serverAddr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("enqueue failed: %s", body)
	}

	var job storage.ReviewJob
	if err := json.Unmarshal(body, &job); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !quiet {
		cmd.Printf("Enqueued prompt job %d (agent: %s)\n", job.ID, job.Agent)
	}

	// If --wait, poll until job completes and show result
	if wait {
		return waitForPromptJob(cmd, serverAddr, job.ID, quiet)
	}

	return nil
}

// promptPollInterval is the initial poll interval for waiting on prompt jobs.
// Can be overridden in tests to speed them up.
var promptPollInterval = 500 * time.Millisecond

// waitForPromptJob waits for a prompt job to complete and displays the result.
// Unlike waitForJob, this doesn't apply verdict-based exit codes since prompt
// jobs don't have PASS/FAIL verdicts.
func waitForPromptJob(cmd *cobra.Command, serverAddr string, jobID int64, quiet bool) error {
	client := &http.Client{Timeout: 5 * time.Second}

	if !quiet {
		cmd.Printf("Waiting for review to complete...")
	}

	// Poll with exponential backoff
	pollInterval := promptPollInterval
	maxInterval := 5 * time.Second
	unknownStatusCount := 0
	const maxUnknownRetries = 10 // Give up after 10 consecutive unknown statuses

	for {
		resp, err := client.Get(fmt.Sprintf("%s/api/jobs?id=%d", serverAddr, jobID))
		if err != nil {
			return fmt.Errorf("failed to check job status: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("server error checking job status (%d): %s", resp.StatusCode, body)
		}

		var jobsResp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
			resp.Body.Close()
			return fmt.Errorf("failed to parse job status: %w", err)
		}
		resp.Body.Close()

		if len(jobsResp.Jobs) == 0 {
			return fmt.Errorf("job %d not found", jobID)
		}

		job := jobsResp.Jobs[0]

		switch job.Status {
		case storage.JobStatusDone:
			// Pass done message to showPromptResult - it prints after successful fetch
			return showPromptResult(cmd, serverAddr, jobID, quiet, " done!\n\n")

		case storage.JobStatusFailed:
			if !quiet {
				cmd.Printf(" failed!\n")
			}
			return fmt.Errorf("prompt failed: %s", job.Error)

		case storage.JobStatusCanceled:
			if !quiet {
				cmd.Printf(" canceled!\n")
			}
			return fmt.Errorf("prompt was canceled")

		case storage.JobStatusQueued, storage.JobStatusRunning:
			// Still in progress, continue polling
			unknownStatusCount = 0 // Reset counter on known status
			time.Sleep(pollInterval)
			if pollInterval < maxInterval {
				pollInterval = time.Duration(float64(pollInterval) * 1.5)
				if pollInterval > maxInterval {
					pollInterval = maxInterval
				}
			}

		default:
			// Unknown status - treat as transient for forward-compatibility
			// (daemon may add new statuses in the future)
			unknownStatusCount++
			if unknownStatusCount >= maxUnknownRetries {
				return fmt.Errorf("received unknown status %q %d times, giving up (daemon may be newer than CLI)", job.Status, unknownStatusCount)
			}
			if !quiet {
				cmd.Printf("\n(unknown status %q, continuing to poll...)", job.Status)
			}
			time.Sleep(pollInterval)
			if pollInterval < maxInterval {
				pollInterval = time.Duration(float64(pollInterval) * 1.5)
				if pollInterval > maxInterval {
					pollInterval = maxInterval
				}
			}
		}
	}
}

// showPromptResult fetches and displays the result of a prompt job.
// Unlike showReview, this doesn't apply verdict-based exit codes.
// The doneMsg parameter is printed before the result on success (used for "done!" message).
func showPromptResult(cmd *cobra.Command, addr string, jobID int64, quiet bool, doneMsg string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/api/review?job_id=%d", addr, jobID))
	if err != nil {
		return fmt.Errorf("failed to fetch result: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no result found for job %d", jobID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error fetching result (%d): %s", resp.StatusCode, body)
	}

	var review storage.Review
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	// Only print after successful fetch to avoid "done!" followed by error
	if !quiet {
		if doneMsg != "" {
			cmd.Print(doneMsg)
		}
		cmd.Printf("Result (by %s)\n", review.Agent)
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println(review.Output)
	}

	// Prompt jobs always exit 0 on success (no verdict-based exit codes)
	return nil
}

// buildPromptWithContext wraps the user's prompt with repository context
func buildPromptWithContext(repoPath, userPrompt string) string {
	var sb strings.Builder

	repoName := filepath.Base(repoPath)

	sb.WriteString("## Context\n\n")
	sb.WriteString(fmt.Sprintf("You are working in the repository \"%s\" at %s.\n", repoName, repoPath))

	// Load project guidelines if available
	repoCfg, err := config.LoadRepoConfig(repoPath)
	if err == nil && repoCfg != nil && repoCfg.ReviewGuidelines != "" {
		sb.WriteString("\n## Project Guidelines\n\n")
		sb.WriteString("The following are project-specific guidelines for this repository:\n\n")
		sb.WriteString(strings.TrimSpace(repoCfg.ReviewGuidelines))
		sb.WriteString("\n")
	}

	sb.WriteString("\n## Request\n\n")
	sb.WriteString(userPrompt)

	return sb.String()
}
