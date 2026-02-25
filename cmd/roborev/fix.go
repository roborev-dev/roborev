package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func fixCmd() *cobra.Command {
	var (
		agentName   string
		model       string
		reasoning   string
		quiet       bool
		unaddressed bool
		allBranches bool
		newestFirst bool
		branch      string
		batch       bool
		list        bool
	)

	cmd := &cobra.Command{
		Use:   "fix [job_id...]",
		Short: "One-shot fix for review findings",
		Long: `Run an agent to address findings from one or more completed reviews.

This is a single-pass fix: the agent applies changes and commits, but
does not re-review or iterate. Use 'roborev refine' for an automated
loop that re-reviews fixes and retries until reviews pass.

The agent runs synchronously in your terminal, streaming output as it
works. The review output is printed first so you can see what needs
fixing. When complete, the job is marked as addressed.

Use --unaddressed to automatically discover and fix all unaddressed
completed jobs for the current repo.

Examples:
  roborev fix 123                        # Fix a single job
  roborev fix 123 124 125                # Fix multiple jobs sequentially
  roborev fix --agent claude-code 123    # Use a specific agent
  roborev fix --unaddressed              # Fix all unaddressed on current branch
  roborev fix --unaddressed --branch main
  roborev fix --all-branches              # Fix all unaddressed across all branches
  roborev fix --batch 123 124 125        # Batch multiple jobs into one prompt
  roborev fix --batch                    # Batch all unaddressed on current branch
  roborev fix --list                     # List unaddressed jobs without fixing
  roborev fix --unaddressed --list       # Same as above
`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if allBranches && !unaddressed && !batch && !list {
				unaddressed = true
			}
			if branch != "" && !unaddressed && !batch && !list {
				return fmt.Errorf("--branch requires --unaddressed, --batch, or --list")
			}
			if allBranches && branch != "" {
				return fmt.Errorf("--all-branches and --branch are mutually exclusive")
			}
			if newestFirst && !unaddressed && !batch && !list {
				return fmt.Errorf("--newest-first requires --unaddressed, --batch, or --list")
			}
			if unaddressed && len(args) > 0 {
				return fmt.Errorf("--unaddressed cannot be used with positional job IDs")
			}
			if batch && unaddressed {
				return fmt.Errorf("--batch and --unaddressed are mutually exclusive (--batch without args already discovers unaddressed jobs)")
			}
			if list && len(args) > 0 {
				return fmt.Errorf("--list cannot be used with positional job IDs")
			}
			if list && batch {
				return fmt.Errorf("--list and --batch are mutually exclusive")
			}
			if list {
				// When --all-branches, effectiveBranch stays "" so
				// queryUnaddressedJobs omits the branch filter.
				effectiveBranch := branch
				if !allBranches && effectiveBranch == "" {
					workDir, err := os.Getwd()
					if err != nil {
						return fmt.Errorf("get working directory: %w", err)
					}
					repoRoot := workDir
					if root, err := git.GetRepoRoot(workDir); err == nil {
						repoRoot = root
					}
					effectiveBranch = git.GetCurrentBranch(repoRoot)
				}
				return runFixList(cmd, effectiveBranch, newestFirst)
			}
			opts := fixOptions{
				agentName: agentName,
				model:     model,
				reasoning: reasoning,
				quiet:     quiet,
			}

			if batch {
				var jobIDs []int64
				for _, arg := range args {
					var id int64
					if _, err := fmt.Sscanf(arg, "%d", &id); err != nil {
						return fmt.Errorf("invalid job ID %q: must be a number", arg)
					}
					jobIDs = append(jobIDs, id)
				}
				if len(jobIDs) > 0 && (branch != "" || allBranches || newestFirst) {
					return fmt.Errorf("--branch, --all-branches, and --newest-first cannot be used with explicit job IDs")
				}
				// If no args, discover unaddressed jobs
				if len(jobIDs) == 0 {
					effectiveBranch := branch
					if !allBranches && effectiveBranch == "" {
						workDir, err := os.Getwd()
						if err != nil {
							return fmt.Errorf("get working directory: %w", err)
						}
						repoRoot := workDir
						if root, err := git.GetRepoRoot(workDir); err == nil {
							repoRoot = root
						}
						effectiveBranch = git.GetCurrentBranch(repoRoot)
					}
					return runFixBatch(cmd, nil, effectiveBranch, newestFirst, opts)
				}
				return runFixBatch(cmd, jobIDs, "", false, opts)
			}

			if unaddressed || len(args) == 0 {
				// Default to current branch unless --branch or --all-branches is set
				effectiveBranch := branch
				if !allBranches && effectiveBranch == "" {
					workDir, err := os.Getwd()
					if err != nil {
						return fmt.Errorf("get working directory: %w", err)
					}
					repoRoot := workDir
					if root, err := git.GetRepoRoot(workDir); err == nil {
						repoRoot = root
					}
					effectiveBranch = git.GetCurrentBranch(repoRoot)
				}
				return runFixUnaddressed(cmd, effectiveBranch, newestFirst, opts)
			}

			// Parse job IDs
			var jobIDs []int64
			for _, arg := range args {
				var id int64
				if _, err := fmt.Sscanf(arg, "%d", &id); err != nil {
					return fmt.Errorf("invalid job ID %q: must be a number", arg)
				}
				jobIDs = append(jobIDs, id)
			}

			return runFix(cmd, jobIDs, opts)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use for fixes (default: from config)")
	cmd.Flags().StringVar(&model, "model", "", "model for agent")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard, or thorough")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")
	cmd.Flags().BoolVar(&unaddressed, "unaddressed", false, "fix all unaddressed completed jobs for the current repo")
	cmd.Flags().StringVar(&branch, "branch", "", "filter by branch (default: current branch; requires --unaddressed)")
	cmd.Flags().BoolVar(&allBranches, "all-branches", false, "include unaddressed jobs from all branches (implies --unaddressed)")
	cmd.Flags().BoolVar(&newestFirst, "newest-first", false, "process jobs newest first instead of oldest first (requires --unaddressed)")
	cmd.Flags().BoolVar(&batch, "batch", false, "concatenate reviews into a single prompt for the agent")
	cmd.Flags().BoolVar(&list, "list", false, "list unaddressed jobs without fixing (implies --unaddressed)")
	registerAgentCompletion(cmd)
	registerReasoningCompletion(cmd)

	return cmd
}

type fixOptions struct {
	agentName string
	model     string
	reasoning string
	quiet     bool
}

// fixJobParams configures a fixJobDirect operation.
type fixJobParams struct {
	RepoRoot string
	Agent    agent.Agent
	Output   io.Writer // agent streaming output (nil = discard)
}

// fixJobResult contains the outcome of a fix operation.
type fixJobResult struct {
	CommitCreated bool
	NewCommitSHA  string
	NoChanges     bool
	AgentOutput   string
}

// detectNewCommit checks whether HEAD has moved past headBefore.
func detectNewCommit(repoRoot, headBefore string) (string, bool) {
	head, err := git.ResolveSHA(repoRoot, "HEAD")
	if err != nil {
		return "", false
	}
	if head != headBefore {
		return head, true
	}
	return "", false
}

// fixJobDirect runs the agent directly on the repo and detects commits.
// If the agent leaves uncommitted changes, it retries with a commit prompt.
func fixJobDirect(ctx context.Context, params fixJobParams, prompt string) (*fixJobResult, error) {
	out := params.Output
	if out == nil {
		out = io.Discard
	}

	headBefore, err := git.ResolveSHA(params.RepoRoot, "HEAD")
	if err != nil {
		// Only proceed if this is specifically an unborn HEAD (empty repo).
		// Other errors (corrupt repo, permissions, non-git dir) should surface.
		if !git.IsUnbornHead(params.RepoRoot) {
			return nil, fmt.Errorf("resolve HEAD: %w", err)
		}
		// Unborn HEAD (empty repo) - run agent and check outcome
		agentOutput, agentErr := params.Agent.Review(ctx, params.RepoRoot, "HEAD", prompt, out)
		if agentErr != nil {
			return nil, fmt.Errorf("fix agent failed: %w", agentErr)
		}
		// Check if the agent created the first commit
		if headAfter, resolveErr := git.ResolveSHA(params.RepoRoot, "HEAD"); resolveErr == nil {
			return &fixJobResult{CommitCreated: true, NewCommitSHA: headAfter, AgentOutput: agentOutput}, nil
		}
		// Still no commit - check working tree
		hasChanges, hcErr := git.HasUncommittedChanges(params.RepoRoot)
		if hcErr != nil {
			return nil, fmt.Errorf("failed to check working tree state: %w", hcErr)
		}
		return &fixJobResult{NoChanges: !hasChanges, AgentOutput: agentOutput}, nil
	}

	agentOutput, agentErr := params.Agent.Review(ctx, params.RepoRoot, "HEAD", prompt, out)
	if agentErr != nil {
		return nil, fmt.Errorf("fix agent failed: %w", agentErr)
	}

	if sha, ok := detectNewCommit(params.RepoRoot, headBefore); ok {
		return &fixJobResult{CommitCreated: true, NewCommitSHA: sha, AgentOutput: agentOutput}, nil
	}

	// No commit - retry if there are uncommitted changes
	hasChanges, err := git.HasUncommittedChanges(params.RepoRoot)
	if err != nil || !hasChanges {
		return &fixJobResult{NoChanges: (err == nil && !hasChanges), AgentOutput: agentOutput}, nil
	}

	fmt.Fprint(out, "\nNo commit was created. Re-running agent with commit instructions...\n\n")
	if _, retryErr := params.Agent.Review(ctx, params.RepoRoot, "HEAD", buildGenericCommitPrompt(), out); retryErr != nil {
		fmt.Fprintf(out, "Warning: commit agent failed: %v\n", retryErr)
	}
	if sha, ok := detectNewCommit(params.RepoRoot, headBefore); ok {
		return &fixJobResult{CommitCreated: true, NewCommitSHA: sha, AgentOutput: agentOutput}, nil
	}

	// Still no commit - report whether changes remain
	hasChanges, _ = git.HasUncommittedChanges(params.RepoRoot)
	return &fixJobResult{NoChanges: !hasChanges, AgentOutput: agentOutput}, nil
}

// resolveFixAgent resolves and configures the agent for fix operations.
func resolveFixAgent(repoPath string, opts fixOptions) (agent.Agent, error) {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	reasoning, err := config.ResolveFixReasoning(opts.reasoning, repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve fix reasoning: %w", err)
	}

	agentName := config.ResolveAgentForWorkflow(opts.agentName, repoPath, cfg, "fix", reasoning)
	modelStr := config.ResolveModelForWorkflow(opts.model, repoPath, cfg, "fix", reasoning)

	a, err := agent.GetAvailable(agentName)
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}

	reasoningLevel := agent.ParseReasoningLevel(reasoning)
	a = a.WithAgentic(true).WithReasoning(reasoningLevel)
	if modelStr != "" {
		a = a.WithModel(modelStr)
	}
	return a, nil
}

func runFix(cmd *cobra.Command, jobIDs []int64, opts fixOptions) error {
	return runFixWithSeen(cmd, jobIDs, opts, nil)
}

func runFixWithSeen(cmd *cobra.Command, jobIDs []int64, opts fixOptions, seen map[int64]bool) error {
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

		err := fixSingleJob(cmd, repoRoot, jobID, opts)
		if err != nil {
			if isConnectionError(err) {
				if !opts.quiet {
					cmd.Printf("Daemon connection lost, attempting recovery...\n")
				}
				if recoverErr := ensureDaemon(); recoverErr != nil {
					return fmt.Errorf("daemon connection lost and recovery failed: %w", recoverErr)
				}
				// Retry this job once after recovery
				err = fixSingleJob(cmd, repoRoot, jobID, opts)
				if err != nil {
					if isConnectionError(err) {
						return fmt.Errorf("daemon connection lost after recovery: %w", err)
					}
					// Non-connection error on retry: fall through to normal error handling
				}
			}
			if err != nil {
				if len(jobIDs) == 1 {
					return err
				}
				if !opts.quiet {
					cmd.Printf("Error fixing job %d: %v\n", jobID, err)
				}
			}
		}
		// Mark as seen so the re-query loop doesn't retry this job.
		// Connection errors bail early (fatal return above), so only
		// successfully attempted jobs reach here.
		if seen != nil {
			seen[jobID] = true
		}
	}

	return nil
}

func runFixUnaddressed(cmd *cobra.Command, branch string, newestFirst bool, opts fixOptions) error {
	// Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	repoRoot := workDir
	if root, err := git.GetMainRepoRoot(workDir); err == nil {
		repoRoot = root
	}

	seen := make(map[int64]bool)

	for {
		jobIDs, err := queryUnaddressedJobIDs(repoRoot, branch)
		if err != nil {
			return err
		}

		// Filter out jobs we've already processed
		var newIDs []int64
		for _, id := range jobIDs {
			if !seen[id] {
				newIDs = append(newIDs, id)
			}
		}

		if len(newIDs) == 0 {
			if len(seen) == 0 && !opts.quiet {
				cmd.Println("No unaddressed jobs found.")
			}
			return nil
		}

		// API returns newest first; reverse to process oldest first by default
		if !newestFirst {
			for i, j := 0, len(newIDs)-1; i < j; i, j = i+1, j-1 {
				newIDs[i], newIDs[j] = newIDs[j], newIDs[i]
			}
		}

		if !opts.quiet {
			if len(seen) > 0 {
				cmd.Printf("\nFound %d new unaddressed job(s): %v\n", len(newIDs), newIDs)
			} else {
				cmd.Printf("Found %d unaddressed job(s): %v\n", len(newIDs), newIDs)
			}
		}

		if err := runFixWithSeen(cmd, newIDs, opts, seen); err != nil {
			return err
		}
	}
}

func queryUnaddressedJobs(
	repoRoot, branch string,
) ([]storage.ReviewJob, error) {
	queryURL := fmt.Sprintf(
		"%s/api/jobs?status=done&repo=%s&addressed=false&limit=0",
		serverAddr, url.QueryEscape(repoRoot),
	)
	if branch != "" {
		queryURL += "&branch=" + url.QueryEscape(branch) +
			"&branch_include_empty=true"
	}

	resp, err := http.Get(queryURL)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(
			"server error (%d): %s", resp.StatusCode, body,
		)
	}

	var jobsResp struct {
		Jobs []storage.ReviewJob `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return jobsResp.Jobs, nil
}

func queryUnaddressedJobIDs(
	repoRoot, branch string,
) ([]int64, error) {
	jobs, err := queryUnaddressedJobs(repoRoot, branch)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	return ids, nil
}

// runFixList prints unaddressed jobs with detailed information without running any agent.
func runFixList(cmd *cobra.Command, branch string, newestFirst bool) error {
	if err := ensureDaemon(); err != nil {
		return err
	}

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	repoRoot := workDir
	if root, err := git.GetMainRepoRoot(workDir); err == nil {
		repoRoot = root
	}

	jobIDs, err := queryUnaddressedJobIDs(repoRoot, branch)
	if err != nil {
		return err
	}

	if !newestFirst {
		for i, j := 0, len(jobIDs)-1; i < j; i, j = i+1, j-1 {
			jobIDs[i], jobIDs[j] = jobIDs[j], jobIDs[i]
		}
	}

	if len(jobIDs) == 0 {
		cmd.Println("No unaddressed jobs found.")
		return nil
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cmd.Printf("Found %d unaddressed fix(es):\n\n", len(jobIDs))

	for _, id := range jobIDs {
		job, err := fetchJob(ctx, serverAddr, id)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not fetch job %d: %v\n", id, err)
			continue
		}
		review, err := fetchReview(ctx, serverAddr, id)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not fetch review for job %d: %v\n", id, err)
			continue
		}

		// Format the output with all available information
		cmd.Printf("Job #%d\n", id)
		cmd.Printf("  Git Ref:  %s\n", git.ShortSHA(job.GitRef))
		if job.Branch != "" {
			cmd.Printf("  Branch:   %s\n", job.Branch)
		}
		if job.CommitSubject != "" {
			cmd.Printf("  Subject:  %s\n", truncateString(job.CommitSubject, 60))
		}
		cmd.Printf("  Agent:    %s\n", job.Agent)
		if job.Model != "" {
			cmd.Printf("  Model:    %s\n", job.Model)
		}
		if job.FinishedAt != nil {
			cmd.Printf("  Finished: %s\n", job.FinishedAt.Local().Format("2006-01-02 15:04:05"))
		}
		if job.Verdict != nil && *job.Verdict != "" {
			cmd.Printf("  Verdict:  %s\n", *job.Verdict)
		}
		summary := firstLine(review.Output)
		if summary != "" {
			cmd.Printf("  Summary:  %s\n", summary)
		}
		cmd.Println()
	}

	cmd.Printf("To apply a fix: roborev fix <job_id>\n")
	cmd.Printf("To apply all:   roborev fix --unaddressed\n")

	return nil
}

// truncateString truncates s to maxLen characters, adding "..." if truncated.
// It operates on Unicode runes to avoid cutting multi-byte characters.
func truncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// firstLine returns the first non-empty line of s, truncated to 80 chars.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return truncateString(line, 80)
		}
	}
	return truncateString(s, 80)
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
		printMarkdownOrPlain(cmd.OutOrStdout(), review.Output)
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println()
	}

	// Resolve agent
	fixAgent, err := resolveFixAgent(repoRoot, opts)
	if err != nil {
		return err
	}

	if !opts.quiet {
		cmd.Printf("Running fix agent (%s) to apply changes...\n\n", fixAgent.Name())
	}

	// Set up output
	var out io.Writer
	var fmtr *streamFormatter
	if opts.quiet {
		out = io.Discard
	} else {
		fmtr = newStreamFormatter(cmd.OutOrStdout(), writerIsTerminal(cmd.OutOrStdout()))
		out = fmtr
	}

	result, err := fixJobDirect(ctx, fixJobParams{
		RepoRoot: repoRoot,
		Agent:    fixAgent,
		Output:   out,
	}, buildGenericFixPrompt(review.Output))
	if fmtr != nil {
		fmtr.Flush()
	}
	if err != nil {
		return err
	}

	if !opts.quiet {
		fmt.Fprintln(cmd.OutOrStdout())
	}

	// Report commit status
	if !opts.quiet {
		if result.CommitCreated {
			cmd.Println("\nChanges committed successfully.")
		} else if result.NoChanges {
			cmd.Println("\nNo changes were made by the fix agent.")
		} else {
			hasChanges, err := git.HasUncommittedChanges(repoRoot)
			if err == nil && hasChanges {
				cmd.Println("\nWarning: Changes were made but not committed. Please review and commit manually.")
			}
		}
	}

	// Enqueue review for fix commit
	if result.CommitCreated {
		if err := enqueueIfNeeded(serverAddr, repoRoot, result.NewCommitSHA); err != nil && !opts.quiet {
			cmd.Printf("Warning: could not enqueue review for fix commit: %v\n", err)
		}
	}

	// Add response and mark as addressed
	responseText := "Fix applied via `roborev fix` command"
	if result.CommitCreated {
		responseText = fmt.Sprintf("Fix applied via `roborev fix` command (commit: %s)", git.ShortSHA(result.NewCommitSHA))
	}

	if err := addJobResponse(serverAddr, jobID, "roborev-fix", responseText); err != nil {
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

// batchEntry holds a fetched job and its review for batch processing.
type batchEntry struct {
	jobID  int64
	job    *storage.ReviewJob
	review *storage.Review
}

// runFixBatch discovers jobs (or uses provided IDs), splits them into batches
// respecting max prompt size, and runs each batch as a single agent invocation.
func runFixBatch(cmd *cobra.Command, jobIDs []int64, branch string, newestFirst bool, opts fixOptions) error {
	if err := ensureDaemon(); err != nil {
		return err
	}

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	repoRoot := workDir
	if root, err := git.GetRepoRoot(workDir); err == nil {
		repoRoot = root
	}
	// Use main repo root for API queries (daemon stores jobs under main repo path)
	apiRepoRoot := repoRoot
	if root, err := git.GetMainRepoRoot(workDir); err == nil {
		apiRepoRoot = root
	}

	// Discover jobs if none provided
	if len(jobIDs) == 0 {
		jobIDs, err = queryUnaddressedJobIDs(apiRepoRoot, branch)
		if err != nil {
			return err
		}
		if !newestFirst {
			for i, j := 0, len(jobIDs)-1; i < j; i, j = i+1, j-1 {
				jobIDs[i], jobIDs[j] = jobIDs[j], jobIDs[i]
			}
		}
	}

	if len(jobIDs) == 0 {
		if !opts.quiet {
			cmd.Println("No unaddressed jobs found.")
		}
		return nil
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Fetch all jobs and reviews
	var entries []batchEntry
	for _, id := range jobIDs {
		job, err := fetchJob(ctx, serverAddr, id)
		if err != nil {
			if !opts.quiet {
				cmd.Printf("Warning: skipping job %d: %v\n", id, err)
			}
			continue
		}
		if job.Status != storage.JobStatusDone {
			if !opts.quiet {
				cmd.Printf("Warning: skipping job %d (status: %s)\n", id, job.Status)
			}
			continue
		}
		review, err := fetchReview(ctx, serverAddr, id)
		if err != nil {
			if !opts.quiet {
				cmd.Printf("Warning: skipping job %d: %v\n", id, err)
			}
			continue
		}
		entries = append(entries, batchEntry{jobID: id, job: job, review: review})
	}

	if len(entries) == 0 {
		if !opts.quiet {
			cmd.Println("No eligible jobs to batch.")
		}
		return nil
	}

	// Split into batches by prompt size
	cfg, _ := config.LoadGlobal()
	maxSize := config.ResolveMaxPromptSize(repoRoot, cfg)
	batches := splitIntoBatches(entries, maxSize)

	// Resolve agent once
	fixAgent, err := resolveFixAgent(repoRoot, opts)
	if err != nil {
		return err
	}

	for i, batch := range batches {
		batchJobIDs := make([]int64, len(batch))
		for j, e := range batch {
			batchJobIDs[j] = e.jobID
		}

		if !opts.quiet {
			cmd.Printf("\n=== Batch %d/%d (jobs %s) ===\n\n", i+1, len(batches), formatJobIDs(batchJobIDs))
			w := cmd.OutOrStdout()
			for _, e := range batch {
				cmd.Printf("Job %d findings:\n", e.jobID)
				cmd.Println(strings.Repeat("-", 60))
				printMarkdownOrPlain(w, e.review.Output)
				cmd.Println(strings.Repeat("-", 60))
				cmd.Println()
			}
			cmd.Printf("Running fix agent (%s) to apply changes...\n\n", fixAgent.Name())
		}

		prompt := buildBatchFixPrompt(batch)

		var out io.Writer
		var fmtr *streamFormatter
		if opts.quiet {
			out = io.Discard
		} else {
			fmtr = newStreamFormatter(cmd.OutOrStdout(), writerIsTerminal(cmd.OutOrStdout()))
			out = fmtr
		}

		result, err := fixJobDirect(ctx, fixJobParams{
			RepoRoot: repoRoot,
			Agent:    fixAgent,
			Output:   out,
		}, prompt)
		if fmtr != nil {
			fmtr.Flush()
		}
		if err != nil {
			if !opts.quiet {
				cmd.Printf("Error in batch %d: %v\n", i+1, err)
			}
			continue
		}

		if !opts.quiet {
			fmt.Fprintln(cmd.OutOrStdout())
			if result.CommitCreated {
				cmd.Println("Changes committed successfully.")
			} else if result.NoChanges {
				cmd.Println("No changes were made by the fix agent.")
			} else {
				if hasChanges, hcErr := git.HasUncommittedChanges(repoRoot); hcErr == nil && hasChanges {
					cmd.Println("Warning: Changes were made but not committed. Please review and commit manually.")
				}
			}
		}

		// Enqueue review for fix commit
		if result.CommitCreated {
			if enqErr := enqueueIfNeeded(serverAddr, repoRoot, result.NewCommitSHA); enqErr != nil && !opts.quiet {
				cmd.Printf("Warning: could not enqueue review for fix commit: %v\n", enqErr)
			}
		}

		// Mark all jobs in this batch as addressed
		responseText := "Fix applied via `roborev fix --batch`"
		if result.CommitCreated {
			responseText = fmt.Sprintf("Fix applied via `roborev fix --batch` (commit: %s)", git.ShortSHA(result.NewCommitSHA))
		}
		for _, e := range batch {
			if addErr := addJobResponse(serverAddr, e.jobID, "roborev-fix", responseText); addErr != nil && !opts.quiet {
				cmd.Printf("Warning: could not add response to job %d: %v\n", e.jobID, addErr)
			}
			if markErr := markJobAddressed(serverAddr, e.jobID); markErr != nil {
				if !opts.quiet {
					cmd.Printf("Warning: could not mark job %d as addressed: %v\n", e.jobID, markErr)
				}
			} else if !opts.quiet {
				cmd.Printf("Job %d marked as addressed\n", e.jobID)
			}
		}
	}

	return nil
}

// batchPromptOverhead is the fixed size of the batch prompt header + footer.
var batchPromptOverhead = len(batchPromptHeader + batchPromptFooter)

const batchPromptHeader = "# Batch Fix Request\n\nThe following reviews found issues that need to be fixed.\nAddress all findings across all reviews in a single pass.\n\n"
const batchPromptFooter = "## Instructions\n\nPlease apply fixes for all the findings above.\nFocus on the highest priority items first.\nAfter making changes, verify the code compiles/passes linting,\nrun relevant tests, and create a git commit summarizing all changes.\n"

// batchEntrySize returns the size of a single entry in the batch prompt.
// The index parameter is the 1-based position in the batch.
func batchEntrySize(index int, e batchEntry) int {
	return len(fmt.Sprintf("## Review %d (Job %d — %s)\n\n%s\n\n", index, e.jobID, git.ShortSHA(e.job.GitRef), e.review.Output))
}

// splitIntoBatches groups entries into batches respecting maxSize.
// Greedily packs reviews; a single oversized review gets its own batch.
func splitIntoBatches(entries []batchEntry, maxSize int) [][]batchEntry {
	var batches [][]batchEntry
	var current []batchEntry
	currentSize := 0

	for _, e := range entries {
		entrySize := batchEntrySize(len(current)+1, e)

		if len(current) > 0 && currentSize+entrySize > maxSize {
			batches = append(batches, current)
			current = nil
			currentSize = 0
			entrySize = batchEntrySize(1, e)
		}

		current = append(current, e)
		if currentSize == 0 {
			currentSize = batchPromptOverhead
		}
		currentSize += entrySize
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// buildBatchFixPrompt creates a concatenated prompt from multiple reviews.
func buildBatchFixPrompt(entries []batchEntry) string {
	var sb strings.Builder
	sb.WriteString(batchPromptHeader)

	for i, e := range entries {
		fmt.Fprintf(&sb, "## Review %d (Job %d — %s)\n\n", i+1, e.jobID, git.ShortSHA(e.job.GitRef))
		sb.WriteString(e.review.Output)
		sb.WriteString("\n\n")
	}

	sb.WriteString(batchPromptFooter)
	return sb.String()
}

// formatJobIDs formats a slice of job IDs as a comma-separated string.
func formatJobIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, ", ")
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

// addJobResponse adds a response/comment to a job
func addJobResponse(serverAddr string, jobID int64, commenter, response string) error {
	reqBody, _ := json.Marshal(map[string]any{
		"job_id":    jobID,
		"commenter": commenter,
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
	// Check if a review job already exists for this commit (e.g., from the
	// post-commit hook). If so, skip enqueuing to avoid duplicates.
	// The post-commit hook normally completes before control returns here,
	// but under heavy load it may take longer. Poll with short intervals
	// up to a max wait to avoid both unnecessary delays and duplicates.
	for range 10 {
		if hasJobForSHA(serverAddr, sha) {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	if hasJobForSHA(serverAddr, sha) {
		return nil
	}

	branchName := git.GetCurrentBranch(repoPath)

	reqBody, _ := json.Marshal(daemon.EnqueueRequest{
		RepoPath: repoPath,
		GitRef:   sha,
		Branch:   branchName,
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

// hasJobForSHA checks if a review job already exists for the given commit SHA.
func hasJobForSHA(serverAddr, sha string) bool {
	checkURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&limit=1", serverAddr, url.QueryEscape(sha))
	resp, err := http.Get(checkURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		Jobs []struct{ ID int64 } `json:"jobs"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return false
	}
	return len(result.Jobs) > 0
}
