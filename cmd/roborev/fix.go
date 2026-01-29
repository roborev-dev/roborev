package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

func fixCmd() *cobra.Command {
	var (
		agentName string
		model     string
		reasoning string
		quiet     bool
	)

	cmd := &cobra.Command{
		Use:   "fix <job_id> [job_id...]",
		Short: "Apply fixes for an analysis job",
		Long: `Apply fixes for one or more analysis jobs.

This command fetches the analysis output from a completed job and runs
an agentic agent locally to apply the suggested fixes. When complete,
a response is added to the job and it is marked as addressed.

The fix runs synchronously in your terminal, streaming output as the
agent works. This allows you to review the analysis results before
deciding which jobs to fix.

Examples:
  roborev fix 123                    # Fix a single job
  roborev fix 123 124 125            # Fix multiple jobs
  roborev fix --agent claude-code 123
`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse job IDs
			var jobIDs []int64
			for _, arg := range args {
				var id int64
				if _, err := fmt.Sscanf(arg, "%d", &id); err != nil {
					return fmt.Errorf("invalid job ID %q: must be a number", arg)
				}
				jobIDs = append(jobIDs, id)
			}

			opts := fixOptions{
				agentName: agentName,
				model:     model,
				reasoning: reasoning,
				quiet:     quiet,
			}

			return runFix(cmd, jobIDs, opts)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use for fixes (default: from config)")
	cmd.Flags().StringVar(&model, "model", "", "model for agent")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard, or thorough")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")

	return cmd
}

type fixOptions struct {
	agentName string
	model     string
	reasoning string
	quiet     bool
}

func runFix(cmd *cobra.Command, jobIDs []int64, opts fixOptions) error {
	// Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	// Get working directory and repo root
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	repoRoot := workDir
	if root, err := git.GetRepoRoot(workDir); err == nil {
		repoRoot = root
	}

	// Process each job
	for i, jobID := range jobIDs {
		if len(jobIDs) > 1 && !opts.quiet {
			cmd.Printf("\n=== Fixing job %d (%d/%d) ===\n", jobID, i+1, len(jobIDs))
		}

		if err := fixSingleJob(cmd, repoRoot, jobID, opts); err != nil {
			if len(jobIDs) == 1 {
				return err
			}
			// Multiple jobs: log error and continue
			if !opts.quiet {
				cmd.Printf("Error fixing job %d: %v\n", jobID, err)
			}
		}
	}

	return nil
}

func fixSingleJob(cmd *cobra.Command, repoRoot string, jobID int64, opts fixOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Fetch the job to check status
	job, err := fetchJob(ctx, serverAddr, jobID)
	if err != nil {
		return fmt.Errorf("fetch job: %w", err)
	}

	// Check if job is complete
	if job.Status != storage.JobStatusDone {
		return fmt.Errorf("job %d is not complete (status: %s)", jobID, job.Status)
	}

	// Fetch the review/analysis output
	review, err := fetchReview(ctx, serverAddr, jobID)
	if err != nil {
		return fmt.Errorf("fetch review: %w", err)
	}

	if !opts.quiet {
		cmd.Printf("Job %d analysis output:\n", jobID)
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println(review.Output)
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println()
	}

	// Build fix prompt
	fixPrompt := buildGenericFixPrompt(review.Output)

	if !opts.quiet {
		cmd.Printf("Running fix agent to apply changes...\n\n")
	}

	// Get HEAD before running fix agent
	headBefore, headErr := git.ResolveSHA(repoRoot, "HEAD")
	canVerifyCommits := headErr == nil

	// Run fix agent
	if err := runFixAgentWithOpts(cmd, repoRoot, opts, fixPrompt); err != nil {
		return fmt.Errorf("fix agent failed: %w", err)
	}

	// Check if commit was created
	var commitCreated bool
	if canVerifyCommits {
		headAfter, err := git.ResolveSHA(repoRoot, "HEAD")
		if err == nil && headBefore != headAfter {
			commitCreated = true
		}

		// If no commit, check for uncommitted changes and retry
		if !commitCreated {
			hasChanges, err := git.HasUncommittedChanges(repoRoot)
			if err == nil && hasChanges {
				if !opts.quiet {
					cmd.Println("\nNo commit was created. Re-running agent with commit instructions...")
					cmd.Println()
				}

				commitPrompt := buildGenericCommitPrompt()
				if err := runFixAgentWithOpts(cmd, repoRoot, opts, commitPrompt); err != nil {
					if !opts.quiet {
						cmd.Printf("Warning: commit agent failed: %v\n", err)
					}
				}

				// Check again
				headFinal, err := git.ResolveSHA(repoRoot, "HEAD")
				if err == nil && headFinal != headAfter {
					commitCreated = true
				}
			}
		}
	}

	// Report commit status
	if !opts.quiet {
		if !canVerifyCommits {
			// Couldn't verify commits
		} else if commitCreated {
			cmd.Println("\nChanges committed successfully.")
		} else {
			hasChanges, err := git.HasUncommittedChanges(repoRoot)
			if err == nil && hasChanges {
				cmd.Println("\nWarning: Changes were made but not committed. Please review and commit manually.")
			} else if err == nil {
				cmd.Println("\nNo changes were made by the fix agent.")
			}
		}
	}

	// Ensure the fix commit gets a review enqueued
	// (post-commit hooks may not fire reliably from agent subprocesses)
	if commitCreated {
		if head, err := git.ResolveSHA(repoRoot, "HEAD"); err == nil {
			if err := enqueueIfNeeded(serverAddr, repoRoot, head); err != nil && !opts.quiet {
				cmd.Printf("Warning: could not enqueue review for fix commit: %v\n", err)
			}
		}
	}

	// Add response to job and mark as addressed
	responseText := "Fix applied via `roborev fix` command"
	if commitCreated {
		if head, err := git.ResolveSHA(repoRoot, "HEAD"); err == nil {
			responseText = fmt.Sprintf("Fix applied via `roborev fix` command (commit: %s)", head[:7])
		}
	}

	if err := addJobResponse(serverAddr, jobID, responseText); err != nil {
		if !opts.quiet {
			cmd.Printf("Warning: could not add response to job: %v\n", err)
		}
	}

	if err := markJobAddressed(serverAddr, jobID); err != nil {
		if !opts.quiet {
			cmd.Printf("Warning: could not mark job as addressed: %v\n", err)
		}
	} else if !opts.quiet {
		cmd.Printf("Job %d marked as addressed\n", jobID)
	}

	return nil
}

// fetchJob retrieves a job from the daemon
func fetchJob(ctx context.Context, serverAddr string, jobID int64) (*storage.ReviewJob, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/jobs?id=%d", serverAddr, jobID), nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, body)
	}

	var jobsResp struct {
		Jobs []storage.ReviewJob `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
		return nil, err
	}

	if len(jobsResp.Jobs) == 0 {
		return nil, fmt.Errorf("job %d not found", jobID)
	}

	return &jobsResp.Jobs[0], nil
}

// fetchReview retrieves the review output for a job
func fetchReview(ctx context.Context, serverAddr string, jobID int64) (*storage.Review, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/review?job_id=%d", serverAddr, jobID), nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, body)
	}

	var review storage.Review
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return nil, err
	}

	return &review, nil
}

// buildGenericFixPrompt creates a fix prompt without knowing the analysis type
func buildGenericFixPrompt(analysisOutput string) string {
	var sb strings.Builder
	sb.WriteString("# Fix Request\n\n")
	sb.WriteString("An analysis was performed and produced the following findings:\n\n")
	sb.WriteString("## Analysis Findings\n\n")
	sb.WriteString(analysisOutput)
	sb.WriteString("\n\n## Instructions\n\n")
	sb.WriteString("Please apply the suggested changes from the analysis above. ")
	sb.WriteString("Make the necessary edits to address each finding. ")
	sb.WriteString("Focus on the highest priority items first.\n\n")
	sb.WriteString("After making changes:\n")
	sb.WriteString("1. Verify the code still compiles/passes linting\n")
	sb.WriteString("2. Run any relevant tests to ensure nothing is broken\n")
	sb.WriteString("3. Create a git commit with a descriptive message summarizing the changes\n")
	return sb.String()
}

// buildGenericCommitPrompt creates a prompt to commit uncommitted changes
func buildGenericCommitPrompt() string {
	var sb strings.Builder
	sb.WriteString("# Commit Request\n\n")
	sb.WriteString("There are uncommitted changes from a previous fix operation.\n\n")
	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Review the current uncommitted changes using `git status` and `git diff`\n")
	sb.WriteString("2. Stage the appropriate files\n")
	sb.WriteString("3. Create a git commit with a descriptive message\n\n")
	sb.WriteString("The commit message should:\n")
	sb.WriteString("- Summarize what was changed and why\n")
	sb.WriteString("- Be concise but informative\n")
	return sb.String()
}

// runFixAgentWithOpts runs the fix agent with the given options
func runFixAgentWithOpts(cmd *cobra.Command, repoPath string, opts fixOptions, prompt string) error {
	// Load config
	cfg, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve agent
	agentName := opts.agentName
	if agentName == "" {
		agentName = cfg.DefaultAgent
	}

	a, err := agent.GetAvailable(agentName)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Configure agent
	reasoningLevel := agent.ParseReasoningLevel(opts.reasoning)
	a = a.WithAgentic(true).WithReasoning(reasoningLevel)
	if opts.model != "" {
		a = a.WithModel(opts.model)
	}

	// Use stdout for streaming output, with stream formatting for TTY
	var out io.Writer
	var fmtr *streamFormatter
	if opts.quiet {
		out = io.Discard
	} else {
		fmtr = newStreamFormatter(cmd.OutOrStdout(), writerIsTerminal(cmd.OutOrStdout()))
		out = fmtr
	}

	// Use command context
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	_, err = a.Review(ctx, repoPath, "fix", prompt, out)
	if fmtr != nil {
		fmtr.Flush()
	}
	if err != nil {
		return err
	}

	if !opts.quiet {
		fmt.Fprintln(cmd.OutOrStdout())
	}
	return nil
}

// addJobResponse adds a response/comment to a job
func addJobResponse(serverAddr string, jobID int64, response string) error {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"job_id":    jobID,
		"commenter": "roborev-fix",
		"comment":   response,
	})

	resp, err := http.Post(serverAddr+"/api/comment", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add response failed: %s", body)
	}
	return nil
}

// enqueueIfNeeded enqueues a review for a commit via the daemon API.
// This ensures fix commits get reviewed even if the post-commit hook
// didn't fire (e.g., agent subprocesses may not trigger hooks reliably).
func enqueueIfNeeded(serverAddr, repoPath, sha string) error {
	branchName := git.GetCurrentBranch(repoPath)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"repo_path": repoPath,
		"git_ref":   sha,
		"branch":    branchName,
	})

	resp, err := http.Post(serverAddr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 200 (skipped) and 201 (enqueued) are both fine
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("enqueue failed: %s", body)
	}
	return nil
}
