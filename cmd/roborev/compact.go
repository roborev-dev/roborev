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

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func compactCmd() *cobra.Command {
	var (
		agentName   string
		model       string
		reasoning   string
		quiet       bool
		allBranches bool
		branch      string
		dryRun      bool
		limit       int
	)

	cmd := &cobra.Command{
		Use:   "compact",
		Short: "Verify and consolidate unaddressed review findings",
		Long: `Verify and consolidate unaddressed review findings.

Discovers all unaddressed completed review jobs, sends them to an agent
for verification against the current codebase, consolidates related findings,
and creates a new consolidated review job. Original jobs are marked as addressed.

This adds a quality layer between 'review' and 'fix' to reduce false positives
and consolidate findings from multiple reviews.

Note: This operation is not atomic. Avoid running multiple compact commands
concurrently on the same branch to prevent inconsistent state.

Examples:
  roborev compact                              # Compact unaddressed jobs on current branch
  roborev compact --branch main                # Compact jobs on main branch
  roborev compact --all-branches               # Compact jobs across all branches
  roborev compact --dry-run                    # Show what would be done
  roborev compact --limit 10                   # Process at most 10 jobs
  roborev compact --agent claude-code          # Use specific agent for verification
  roborev compact --reasoning thorough         # Use thorough reasoning level
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if branch != "" && allBranches {
				return fmt.Errorf("--branch and --all-branches are mutually exclusive")
			}

			return runCompact(cmd, compactOptions{
				agentName:   agentName,
				model:       model,
				reasoning:   reasoning,
				quiet:       quiet,
				allBranches: allBranches,
				branch:      branch,
				dryRun:      dryRun,
				limit:       limit,
			})
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use for verification (defaults to fix agent from config)")
	cmd.Flags().StringVar(&model, "model", "", "model to use")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level (fast/standard/thorough)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")
	cmd.Flags().BoolVar(&allBranches, "all-branches", false, "include all branches")
	cmd.Flags().StringVar(&branch, "branch", "", "filter by branch (default: current branch)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be done without executing")
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of jobs to compact at once")

	return cmd
}

type compactOptions struct {
	agentName   string
	model       string
	reasoning   string
	quiet       bool
	allBranches bool
	branch      string
	dryRun      bool
	limit       int
}

type jobReview struct {
	jobID  int64
	job    *storage.ReviewJob
	review *storage.Review
}

// fetchJobReviews fetches job and review data for all job IDs.
// Returns successfully fetched entries and list of IDs that were processed.
func fetchJobReviews(ctx context.Context, jobIDs []int64, quiet bool, cmd *cobra.Command) ([]jobReview, []int64, error) {
	var jobReviews []jobReview
	var successfulJobIDs []int64

	for i, jobID := range jobIDs {
		if !quiet {
			cmd.Printf("  [%d/%d] Fetching job %d... ", i+1, len(jobIDs), jobID)
		}

		job, err := fetchJob(ctx, serverAddr, jobID)
		if err != nil {
			if !quiet {
				cmd.Printf("failed: %v\n", err)
			}
			continue
		}

		review, err := fetchReview(ctx, serverAddr, jobID)
		if err != nil {
			if !quiet {
				cmd.Printf("failed: %v\n", err)
			}
			continue
		}

		if !quiet {
			cmd.Printf("done\n")
		}

		successfulJobIDs = append(successfulJobIDs, jobID)
		jobReviews = append(jobReviews, jobReview{
			jobID:  jobID,
			job:    job,
			review: review,
		})
	}

	if len(jobReviews) == 0 {
		return nil, nil, fmt.Errorf("failed to fetch any review outputs")
	}

	return jobReviews, successfulJobIDs, nil
}

// verifyAndConsolidate sends reviews to agent for verification and consolidation.
// Returns the consolidated job ID and validated review output.
func verifyAndConsolidate(ctx context.Context, cmd *cobra.Command, repoRoot string, jobReviews []jobReview, branchFilter string, opts compactOptions) (int64, *storage.Review, error) {
	// Build verification prompt
	prompt := buildCompactPrompt(jobReviews, branchFilter)

	// Resolve agent
	agent, err := resolveFixAgent(repoRoot, fixOptions{
		agentName: opts.agentName,
		model:     opts.model,
		reasoning: opts.reasoning,
	})
	if err != nil {
		return 0, nil, err
	}

	if !opts.quiet {
		cmd.Printf("\nRunning verification agent (%s) to check findings against current codebase...\n\n", agent.Name())
	}

	// Enqueue consolidated task job
	timestamp := time.Now().Format("20060102-150405")
	label := fmt.Sprintf("compact-%s-%s", branchFilter, timestamp)
	if branchFilter == "" {
		label = fmt.Sprintf("compact-all-%s", timestamp)
	}

	outputPrefix := buildCompactOutputPrefix(len(jobReviews), branchFilter, extractJobIDs(jobReviews))

	job, err := enqueueCompactJob(repoRoot, prompt, outputPrefix, label, branchFilter, opts)
	if err != nil {
		return 0, nil, fmt.Errorf("enqueue verification job: %w", err)
	}

	if !opts.quiet {
		cmd.Printf("Enqueued consolidation job %d\n", job.ID)
	}

	// Wait for completion
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var output io.Writer
	if !opts.quiet {
		output = cmd.OutOrStdout()
	}

	_, err = waitForJobCompletion(ctx, serverAddr, job.ID, output)
	if err != nil {
		return 0, nil, fmt.Errorf("verification failed: %w", err)
	}

	// Fetch and validate final output
	review, err := fetchReview(ctx, serverAddr, job.ID)
	if err != nil {
		return 0, nil, fmt.Errorf("fetch final review: %w", err)
	}

	if !isValidConsolidatedReview(review.Output) {
		return 0, nil, fmt.Errorf("agent produced invalid output (no findings or error message)")
	}

	return job.ID, review, nil
}

// markJobsAsAddressed marks all jobs in the list as addressed.
// Logs warnings for failures but continues processing remaining jobs.
func markJobsAsAddressed(jobIDs []int64, quiet bool, cmd *cobra.Command) {
	if !quiet {
		cmd.Println("\nMarking original jobs as addressed:")
	}

	for i, jobID := range jobIDs {
		if !quiet {
			cmd.Printf("  [%d/%d] Marking job %d... ", i+1, len(jobIDs), jobID)
		}

		if err := markJobAddressed(serverAddr, jobID); err != nil {
			if !quiet {
				cmd.Printf("failed: %v\n", err)
			}
		} else if !quiet {
			cmd.Printf("done\n")
		}
	}
}

// runCompact verifies and consolidates unaddressed review findings.
//
// Known limitation: This is not an atomic operation. If another process marks jobs
// as addressed or modifies them between the initial query and the final marking step,
// there could be inconsistent state. This is acceptable since compact is typically
// run manually by a single user. For concurrent operations, users should coordinate
// to avoid running multiple compact commands simultaneously on the same branch.
func runCompact(cmd *cobra.Command, opts compactOptions) error {
	// Setup
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

	branchFilter := opts.branch
	if !opts.allBranches && branchFilter == "" {
		branchFilter = git.GetCurrentBranch(repoRoot)
	}

	// Query and limit jobs
	jobIDs, err := queryUnaddressedJobs(repoRoot, branchFilter)
	if err != nil {
		return err
	}

	if len(jobIDs) == 0 {
		if !opts.quiet {
			cmd.Println("No unaddressed jobs found.")
		}
		return nil
	}

	// Apply limit
	if opts.limit > 0 && len(jobIDs) > opts.limit {
		if !opts.quiet {
			cmd.Printf("Found %d unaddressed jobs, limiting to %d (use --limit to adjust)\n\n",
				len(jobIDs), opts.limit)
		}
		jobIDs = jobIDs[:opts.limit]
	} else if !opts.quiet {
		branchMsg := ""
		if branchFilter != "" {
			branchMsg = fmt.Sprintf(" on branch %s", branchFilter)
		}
		cmd.Printf("Found %d unaddressed job(s)%s: %v\n\n", len(jobIDs), branchMsg, jobIDs)
	}

	// Warn about very large limits
	if opts.limit > 50 && !opts.quiet {
		cmd.Printf("Warning: --limit=%d may create a very large prompt\n\n", opts.limit)
	}

	// Dry-run early exit
	if opts.dryRun {
		cmd.Println("Would verify and consolidate these reviews")
		cmd.Println("Would create 1 consolidated review job")
		cmd.Printf("Would mark %d jobs as addressed\n\n", len(jobIDs))
		cmd.Println("Run without --dry-run to execute.")
		return nil
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Fetch review outputs
	if !opts.quiet {
		cmd.Println("Fetching review outputs:")
	}

	jobReviews, successfulJobIDs, err := fetchJobReviews(ctx, jobIDs, opts.quiet, cmd)
	if err != nil {
		return err
	}

	// Verify and consolidate
	consolidatedJobID, _, err := verifyAndConsolidate(ctx, cmd, repoRoot, jobReviews, branchFilter, opts)
	if err != nil {
		return err
	}

	if !opts.quiet {
		cmd.Println("\nVerification complete!")
		cmd.Printf("\nConsolidated review created: job %d\n", consolidatedJobID)
	}

	// Mark jobs as addressed (only those successfully processed)
	markJobsAsAddressed(successfulJobIDs, opts.quiet, cmd)

	// Show next steps
	if !opts.quiet {
		cmd.Println("\nView with: roborev tui")
		cmd.Printf("Fix with: roborev fix %d\n", consolidatedJobID)
	}

	return nil
}

func buildCompactPrompt(jobReviews []jobReview, branch string) string {
	var sb strings.Builder

	sb.WriteString("# Verification and Consolidation Request\n\n")
	sb.WriteString("You are a code reviewer tasked with verifying and consolidating previous review findings.\n\n")

	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. **Verify each finding against the current codebase:**\n")
	sb.WriteString("   - Search the codebase to check if the issue still exists\n")
	sb.WriteString("   - Use wide code search patterns (grep, find files, read context)\n")
	sb.WriteString("   - Mark findings as VERIFIED or FALSE_POSITIVE\n\n")

	sb.WriteString("2. **Consolidate related findings:**\n")
	sb.WriteString("   - Group findings that address the same underlying issue\n")
	sb.WriteString("   - Merge duplicate findings from different reviews\n")
	sb.WriteString("   - Provide a single comprehensive description for each group\n\n")

	sb.WriteString("3. **Output format:**\n")
	sb.WriteString("   - List only VERIFIED findings in your output\n")
	sb.WriteString("   - Use the same severity levels (Critical, High, Medium, Low)\n")
	sb.WriteString("   - Include file and line references where possible\n")
	sb.WriteString("   - Explain what the issue is and why it matters\n\n")

	sb.WriteString("## Unaddressed Review Findings\n\n")
	sb.WriteString(fmt.Sprintf("Below are %d unaddressed reviews", len(jobReviews)))
	if branch != "" {
		sb.WriteString(fmt.Sprintf(" from branch %s", branch))
	}
	sb.WriteString(":\n\n")

	for i, jr := range jobReviews {
		sb.WriteString(fmt.Sprintf("--- Review %d (Job %d", i+1, jr.jobID))
		if jr.job.GitRef != "" {
			sb.WriteString(fmt.Sprintf(" — %s", shortSHA(jr.job.GitRef)))
		}
		sb.WriteString(") ---\n")
		sb.WriteString(jr.review.Output)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Expected Output\n\n")
	sb.WriteString("Provide a consolidated review output containing only verified findings.\n")
	sb.WriteString("Format it exactly like a code review, with:\n")
	sb.WriteString("- Brief summary of findings\n")
	sb.WriteString("- Each verified finding with severity, file/line, description\n")
	sb.WriteString("- NO false positives or already-fixed issues\n\n")
	sb.WriteString("If NO findings remain after verification, state \"All previous findings have been addressed.\"\n")

	return sb.String()
}

func buildCompactOutputPrefix(jobCount int, branch string, jobIDs []int64) string {
	var sb strings.Builder
	sb.WriteString("## Compact Analysis\n\n")
	sb.WriteString(fmt.Sprintf("Verified and consolidated %d unaddressed reviews", jobCount))
	if branch != "" {
		sb.WriteString(fmt.Sprintf(" from branch %s", branch))
	}
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("Original jobs: %s\n\n", formatJobIDs(jobIDs)))
	sb.WriteString("---\n\n")
	return sb.String()
}

func enqueueCompactJob(repoRoot, prompt, outputPrefix, label, branch string, opts compactOptions) (*storage.ReviewJob, error) {
	if branch == "" {
		branch = git.GetCurrentBranch(repoRoot)
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"repo_path":     repoRoot,
		"git_ref":       label,
		"branch":        branch,
		"agent":         opts.agentName,
		"model":         opts.model,
		"reasoning":     opts.reasoning,
		"custom_prompt": prompt,
		"output_prefix": outputPrefix,
		"agentic":       true,
		"job_type":      "compact",
	})

	resp, err := http.Post(serverAddr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("enqueue failed: %s", body)
	}

	var job storage.ReviewJob
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &job, nil
}

// isValidConsolidatedReview checks if output looks like actual findings vs error message
func isValidConsolidatedReview(output string) bool {
	output = strings.TrimSpace(output)

	// Check for empty output
	if output == "" {
		return false
	}

	lowerOutput := strings.ToLower(output)

	// Valid if it contains "All previous findings have been addressed" (success case)
	if strings.Contains(lowerOutput, "all previous findings have been addressed") {
		return true
	}

	// Check for common error patterns at start of lines (agent failures)
	// Only check at line start to avoid false positives in review content
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(trimmed, "error:") ||
			strings.HasPrefix(trimmed, "exception:") ||
			strings.HasPrefix(trimmed, "traceback") {
			return false
		}
	}

	// Valid if it looks like structured review output
	// Require both severity indicators AND structural markers for confidence
	hasSeverity := strings.Contains(lowerOutput, "severity") ||
		strings.Contains(lowerOutput, "critical") ||
		strings.Contains(lowerOutput, "high") ||
		strings.Contains(lowerOutput, "medium") ||
		strings.Contains(lowerOutput, "low")

	hasStructure := strings.Contains(output, "##") || // Markdown headers
		strings.Contains(output, "###") ||
		strings.Contains(lowerOutput, "verified") ||
		strings.Contains(lowerOutput, "findings") ||
		strings.Contains(lowerOutput, "issues")

	// Accept if it has both severity levels and structural elements
	if hasSeverity && hasStructure {
		return true
	}

	// Also accept if it has file references (even without severity markers)
	hasFileRef := strings.Contains(output, ".go:") ||
		strings.Contains(output, ".py:") ||
		strings.Contains(output, ".js:") ||
		strings.Contains(output, ".ts:")

	return hasFileRef && hasStructure
}

// extractJobIDs extracts job IDs from jobReview slice
func extractJobIDs(reviews []jobReview) []int64 {
	ids := make([]int64, len(reviews))
	for i, jr := range reviews {
		ids[i] = jr.jobID
	}
	return ids
}
