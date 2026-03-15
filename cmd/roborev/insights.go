package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/prompt"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func insightsCmd() *cobra.Command {
	var (
		repoPath   string
		branch     string
		since      string
		agentName  string
		model      string
		reasoning  string
		wait       bool
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "insights",
		Short: "Analyze review patterns and suggest guideline improvements",
		Long: `Analyze failing code reviews to identify recurring patterns and suggest
improvements to review guidelines.

This is an LLM-powered command that:
1. Queries completed reviews (focusing on failures) from the database
2. Includes the current review_guidelines from .roborev.toml as context
3. Sends the batch to an agent with a structured analysis prompt
4. Returns actionable recommendations for guideline changes

The agent produces:
- Recurring finding patterns across reviews
- Hotspot areas (files/packages that concentrate failures)
- Noise candidates (findings consistently dismissed without code changes)
- Guideline gaps (patterns flagged by reviews but not in guidelines)
- Suggested guideline additions (concrete text for .roborev.toml)

Examples:
  roborev insights                          # Analyze last 30 days of reviews
  roborev insights --since 7d               # Last 7 days only
  roborev insights --branch main            # Only reviews on main branch
  roborev insights --repo /path/to/repo     # Specific repo
  roborev insights --agent gemini --wait    # Use specific agent, wait for result
  roborev insights --json                   # Output job info as JSON`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInsights(cmd, insightsOptions{
				repoPath:   repoPath,
				branch:     branch,
				since:      since,
				agentName:  agentName,
				model:      model,
				reasoning:  reasoning,
				wait:       wait,
				jsonOutput: jsonOutput,
			})
		},
	}

	cmd.Flags().StringVar(&repoPath, "repo", "", "scope to a single repo (default: current repo if tracked)")
	cmd.Flags().StringVar(&branch, "branch", "", "scope to a single branch")
	cmd.Flags().StringVar(&since, "since", "30d", "time window for reviews (e.g., 7d, 30d, 90d)")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use for analysis (default: from config)")
	cmd.Flags().StringVar(&model, "model", "", "model for agent")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard, or thorough")
	cmd.Flags().BoolVar(&wait, "wait", true, "wait for completion and display result")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output job info as JSON")
	registerAgentCompletion(cmd)
	registerReasoningCompletion(cmd)

	return cmd
}

type insightsOptions struct {
	repoPath   string
	branch     string
	since      string
	agentName  string
	model      string
	reasoning  string
	wait       bool
	jsonOutput bool
}

func runInsights(cmd *cobra.Command, opts insightsOptions) error {
	// Resolve repo path
	repoRoot := opts.repoPath
	if repoRoot == "" {
		workDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		if root, err := git.GetRepoRoot(workDir); err == nil {
			repoRoot = root
		} else {
			repoRoot = workDir
		}
	} else {
		var err error
		repoRoot, err = filepath.Abs(repoRoot)
		if err != nil {
			return fmt.Errorf("resolve repo path: %w", err)
		}
	}

	// Parse --since duration
	sinceTime, err := parseSinceDuration(opts.since)
	if err != nil {
		return fmt.Errorf("invalid --since value %q: %w", opts.since, err)
	}

	// Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	if !opts.jsonOutput {
		cmd.Printf("Gathering failing reviews since %s...\n", sinceTime.Format("2006-01-02"))
	}

	// Fetch failing reviews from daemon API
	reviews, err := fetchFailingReviews(serverAddr, repoRoot, opts.branch, sinceTime)
	if err != nil {
		return fmt.Errorf("fetch reviews: %w", err)
	}

	if len(reviews) == 0 {
		cmd.Println("No failing reviews found in the specified time window.")
		return nil
	}

	if !opts.jsonOutput {
		cmd.Printf("Found %d failing review(s). Building analysis prompt...\n", len(reviews))
	}

	// Load current review guidelines
	guidelines := ""
	if repoCfg, err := config.LoadRepoConfig(repoRoot); err == nil && repoCfg != nil {
		guidelines = repoCfg.ReviewGuidelines
	}

	// Build the insights prompt
	insightsPrompt := prompt.BuildInsightsPrompt(prompt.InsightsData{
		Reviews:    reviews,
		Guidelines: guidelines,
		RepoName:   filepath.Base(repoRoot),
		Since:      sinceTime,
	})

	// Enqueue as a task job
	branch := git.GetCurrentBranch(repoRoot)
	reqBody, _ := json.Marshal(daemon.EnqueueRequest{
		RepoPath:     repoRoot,
		GitRef:       "insights",
		Branch:       branch,
		Agent:        opts.agentName,
		Model:        opts.model,
		Reasoning:    opts.reasoning,
		CustomPrompt: insightsPrompt,
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

	// JSON output mode
	if opts.jsonOutput {
		result := map[string]any{
			"job_id":           job.ID,
			"agent":            job.Agent,
			"reviews_analyzed": len(reviews),
			"since":            sinceTime.Format(time.RFC3339),
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		return enc.Encode(result)
	}

	cmd.Printf("Enqueued insights job %d (agent: %s, analyzing %d reviews)\n", job.ID, job.Agent, len(reviews))

	// Wait for completion
	if opts.wait {
		return waitForPromptJob(cmd, serverAddr, job.ID, false, promptPollInterval)
	}

	return nil
}

// fetchFailingReviews queries the daemon API for done jobs with failing verdicts
// in the given time window, then fetches review output for each.
func fetchFailingReviews(addr, repoPath, branch string, since time.Time) ([]prompt.InsightsReview, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	// Build query for done jobs, excluding task and fix jobs
	params := url.Values{}
	params.Set("status", "done")
	params.Set("repo", repoPath)
	params.Set("limit", "200")
	params.Set("exclude_job_type", "task")
	if branch != "" {
		params.Set("branch", branch)
	}

	resp, err := client.Get(fmt.Sprintf("%s/api/jobs?%s", addr, params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
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
		return nil, fmt.Errorf("parse jobs response: %w", err)
	}

	// Filter to failing reviews within the time window
	var reviews []prompt.InsightsReview
	for _, job := range jobsResp.Jobs {
		// Skip jobs outside time window
		if job.FinishedAt != nil && job.FinishedAt.Before(since) {
			continue
		}

		// Skip fix jobs
		if job.IsFixJob() {
			continue
		}

		// Only include failing verdicts
		if job.Verdict == nil || *job.Verdict != "F" {
			continue
		}

		// Fetch the review output
		review, err := fetchReviewForInsights(client, addr, job.ID)
		if err != nil {
			continue // Skip reviews we can't fetch
		}

		reviews = append(reviews, prompt.InsightsReviewFromJob(job, review.Output, review.Closed))
	}

	return reviews, nil
}

// fetchReviewForInsights fetches a review by job ID
func fetchReviewForInsights(client *http.Client, addr string, jobID int64) (*storage.Review, error) {
	resp, err := client.Get(fmt.Sprintf("%s/api/review?job_id=%d", addr, jobID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var review storage.Review
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return nil, err
	}
	return &review, nil
}

// parseSinceDuration parses a duration string like "7d", "30d", "90d" into a time.Time.
func parseSinceDuration(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().AddDate(0, 0, -30), nil
	}

	// Try standard Go duration first (e.g., "720h")
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}

	// Parse day-based durations (e.g., "7d", "30d")
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Now().AddDate(0, 0, -days), nil
		}
	}

	// Parse week-based durations (e.g., "2w", "4w")
	if strings.HasSuffix(s, "w") {
		var weeks int
		if _, err := fmt.Sscanf(s, "%dw", &weeks); err == nil && weeks > 0 {
			return time.Now().AddDate(0, 0, -weeks*7), nil
		}
	}

	return time.Time{}, fmt.Errorf("expected format like 7d, 4w, or 720h")
}
