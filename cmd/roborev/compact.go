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
		unaddressed bool
		allBranches bool
		branch      string
		dryRun      bool
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

Examples:
  roborev compact                              # Compact unaddressed jobs on current branch
  roborev compact --branch main                # Compact jobs on main branch
  roborev compact --all-branches               # Compact jobs across all branches
  roborev compact --dry-run                    # Show what would be done
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
				unaddressed: unaddressed,
				allBranches: allBranches,
				branch:      branch,
				dryRun:      dryRun,
			})
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use for verification (defaults to fix agent from config)")
	cmd.Flags().StringVar(&model, "model", "", "model to use")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level (fast/standard/thorough)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")
	cmd.Flags().BoolVar(&unaddressed, "unaddressed", true, "process unaddressed jobs")
	cmd.Flags().BoolVar(&allBranches, "all-branches", false, "include all branches")
	cmd.Flags().StringVar(&branch, "branch", "", "filter by branch (default: current branch)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be done without executing")

	return cmd
}

type compactOptions struct {
	agentName   string
	model       string
	reasoning   string
	quiet       bool
	unaddressed bool
	allBranches bool
	branch      string
	dryRun      bool
}

type jobReview struct {
	jobID  int64
	job    *storage.ReviewJob
	review *storage.Review
}

func runCompact(cmd *cobra.Command, opts compactOptions) error {
	// 1. Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	// 2. Get repo root
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	repoRoot := workDir
	if root, err := git.GetMainRepoRoot(workDir); err == nil {
		repoRoot = root
	}

	// 3. Determine branch filter
	branchFilter := opts.branch
	if !opts.allBranches && branchFilter == "" {
		branchFilter = git.GetCurrentBranch(repoRoot)
	}

	// 4. Query unaddressed jobs
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

	if !opts.quiet {
		branchMsg := ""
		if branchFilter != "" {
			branchMsg = fmt.Sprintf(" on branch %s", branchFilter)
		}
		cmd.Printf("Found %d unaddressed job(s)%s: %v\n\n", len(jobIDs), branchMsg, jobIDs)
	}

	// 5. Dry-run: just show what would be done
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

	// 6. Fetch review outputs
	if !opts.quiet {
		cmd.Print("Fetching review outputs... ")
	}

	var jobReviews []jobReview
	for _, jobID := range jobIDs {
		job, err := fetchJob(ctx, serverAddr, jobID)
		if err != nil {
			if !opts.quiet {
				cmd.Printf("\nWarning: failed to fetch job %d: %v\n", jobID, err)
			}
			continue
		}
		review, err := fetchReview(ctx, serverAddr, jobID)
		if err != nil {
			if !opts.quiet {
				cmd.Printf("\nWarning: failed to fetch review for job %d: %v\n", jobID, err)
			}
			continue
		}
		jobReviews = append(jobReviews, jobReview{
			jobID:  jobID,
			job:    job,
			review: review,
		})
	}

	if !opts.quiet {
		cmd.Println("done")
	}

	if len(jobReviews) == 0 {
		return fmt.Errorf("failed to fetch any review outputs")
	}

	// 7. Build verification prompt
	prompt := buildCompactPrompt(jobReviews, branchFilter)

	// 8. Resolve agent
	agent, err := resolveFixAgent(repoRoot, fixOptions{
		agentName: opts.agentName,
		model:     opts.model,
		reasoning: opts.reasoning,
	})
	if err != nil {
		return err
	}

	if !opts.quiet {
		cmd.Printf("\nRunning verification agent (%s) to check findings against current codebase...\n\n", agent.Name())
	}

	// 9. Enqueue consolidated task job
	timestamp := time.Now().Format("20060102-150405")
	label := fmt.Sprintf("compact-%s-%s", branchFilter, timestamp)
	if branchFilter == "" {
		label = fmt.Sprintf("compact-all-%s", timestamp)
	}

	outputPrefix := buildCompactOutputPrefix(len(jobIDs), branchFilter, jobIDs)

	job, err := enqueueCompactJob(repoRoot, prompt, outputPrefix, label, branchFilter, opts)
	if err != nil {
		return fmt.Errorf("enqueue verification job: %w", err)
	}

	// 10. Wait for completion
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var output io.Writer
	if !opts.quiet {
		output = cmd.OutOrStdout()
	}

	_, err = waitForCompactJob(ctx, serverAddr, job.ID, output)
	if err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	if !opts.quiet {
		cmd.Println("\nVerification complete!")
		cmd.Printf("\nConsolidated review created: job %d\n", job.ID)
	}

	// 11. Mark original jobs as addressed
	if !opts.quiet {
		cmd.Print("\nMarking original jobs as addressed... ")
	}

	for _, jobID := range jobIDs {
		if err := markJobAddressed(serverAddr, jobID); err != nil {
			if !opts.quiet {
				cmd.Printf("\nWarning: failed to mark job %d as addressed: %v\n", jobID, err)
			}
		}
	}

	if !opts.quiet {
		cmd.Println("done")
	}

	// 12. Show next steps
	if !opts.quiet {
		cmd.Println("\nView with: roborev tui")
		cmd.Printf("Fix with: roborev fix %d\n", job.ID)
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

func waitForCompactJob(ctx context.Context, serverAddr string, jobID int64, output io.Writer) (*storage.Review, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	pollInterval := 1 * time.Second
	maxInterval := 5 * time.Second
	lastOutputLen := 0

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		// Check job status
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/jobs?id=%d", serverAddr, jobID), nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			continue // Keep polling
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var jobsResp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if len(jobsResp.Jobs) == 0 {
			return nil, fmt.Errorf("job %d not found", jobID)
		}

		job := jobsResp.Jobs[0]

		// Fetch partial review output for streaming progress
		if output != nil && job.Status == storage.JobStatusRunning {
			reviewReq, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/review?job_id=%d", serverAddr, jobID), nil)
			if err == nil {
				reviewResp, err := client.Do(reviewReq)
				if err == nil {
					if reviewResp.StatusCode == http.StatusOK {
						var review storage.Review
						if json.NewDecoder(reviewResp.Body).Decode(&review) == nil {
							newOutput := review.Output
							if len(newOutput) > lastOutputLen {
								fmt.Fprint(output, newOutput[lastOutputLen:])
								lastOutputLen = len(newOutput)
							}
						}
					}
					reviewResp.Body.Close()
				}
			}
		}

		switch job.Status {
		case storage.JobStatusDone:
			// Fetch the final review
			reviewReq, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/review?job_id=%d", serverAddr, jobID), nil)
			if err != nil {
				return nil, fmt.Errorf("create review request: %w", err)
			}

			reviewResp, err := client.Do(reviewReq)
			if err != nil {
				return nil, fmt.Errorf("fetch review: %w", err)
			}
			defer reviewResp.Body.Close()

			if reviewResp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(reviewResp.Body)
				return nil, fmt.Errorf("fetch review (%d): %s", reviewResp.StatusCode, body)
			}

			var review storage.Review
			if err := json.NewDecoder(reviewResp.Body).Decode(&review); err != nil {
				return nil, fmt.Errorf("parse review: %w", err)
			}

			// Print any remaining output
			if output != nil && len(review.Output) > lastOutputLen {
				fmt.Fprint(output, review.Output[lastOutputLen:])
			}

			return &review, nil

		case storage.JobStatusFailed:
			return nil, fmt.Errorf("job failed: %s", job.Error)

		case storage.JobStatusCanceled:
			return nil, fmt.Errorf("job was canceled")
		}

		// Increase poll interval over time
		if pollInterval < maxInterval {
			pollInterval = pollInterval * 3 / 2
			if pollInterval > maxInterval {
				pollInterval = maxInterval
			}
		}
	}
}
