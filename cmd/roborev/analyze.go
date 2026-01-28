package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/prompt/analyze"
	"github.com/roborev-dev/roborev/internal/storage"
)

// Maximum time to wait for an analysis job to complete
const analyzeJobTimeout = 30 * time.Minute

func analyzeCmd() *cobra.Command {
	var (
		agentName  string
		model      string
		reasoning  string
		wait       bool
		quiet      bool
		listTypes  bool
		fix        bool
		fixAgent   string
		fixModel   string
	)

	cmd := &cobra.Command{
		Use:   "analyze <type> <files...>",
		Short: "Run built-in analysis on files",
		Long: `Run a built-in analysis type on one or more files.

This command provides predefined analysis prompts for common code review
tasks like finding duplication, suggesting refactorings, or identifying
test fixture opportunities.

The output is formatted for easy copy-paste into agent sessions, with
a header showing the analysis type and files analyzed.

Available analysis types:
  test-fixtures  Find test fixture and helper opportunities
  duplication    Find code duplication across files
  refactor       Suggest refactoring opportunities
  complexity     Analyze complexity and suggest simplifications
  api-design     Review API consistency and design patterns
  dead-code      Find unused exports and unreachable code
  architecture   Review architectural patterns and structure

Examples:
  roborev analyze test-fixtures internal/storage/*_test.go
  roborev analyze duplication cmd/roborev/*.go
  roborev analyze refactor --wait main.go utils.go
  roborev analyze complexity --agent gemini ./...
  roborev analyze architecture internal/storage/    # analyze a directory
  roborev analyze --list

Fix mode (--fix):
  Runs analysis, then invokes an agentic agent to apply the suggested changes.
  The analysis is saved to the database and marked as addressed when complete.

  roborev analyze refactor --fix ./...
  roborev analyze duplication --fix --fix-agent claude-code *.go
`,
		Args: func(cmd *cobra.Command, args []string) error {
			if listTypes {
				return nil
			}
			if len(args) < 2 {
				return fmt.Errorf("requires analysis type and at least one file")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if listTypes {
				return listAnalysisTypes(cmd)
			}
			opts := analyzeOptions{
				agentName:  agentName,
				model:      model,
				reasoning:  reasoning,
				wait:       wait,
				quiet:      quiet,
				fix:        fix,
				fixAgent:   fixAgent,
				fixModel:   fixModel,
			}
			return runAnalysis(cmd, args[0], args[1:], opts)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use for analysis (default: from config)")
	cmd.Flags().StringVar(&model, "model", "", "model for analysis agent")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard, or thorough")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for job to complete and show result")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output (just enqueue)")
	cmd.Flags().BoolVar(&listTypes, "list", false, "list available analysis types")
	cmd.Flags().BoolVar(&fix, "fix", false, "after analysis, run an agentic agent to apply fixes")
	cmd.Flags().StringVar(&fixAgent, "fix-agent", "", "agent to use for fixes (default: same as --agent)")
	cmd.Flags().StringVar(&fixModel, "fix-model", "", "model for fix agent (default: same as --model)")

	return cmd
}

type analyzeOptions struct {
	agentName  string
	model      string
	reasoning  string
	wait       bool
	quiet      bool
	fix        bool
	fixAgent   string
	fixModel   string
}

func listAnalysisTypes(cmd *cobra.Command) error {
	cmd.Println("Available analysis types:")
	cmd.Println()
	for _, t := range analyze.AllTypes {
		cmd.Printf("  %-14s %s\n", t.Name, t.Description)
	}
	return nil
}

func runAnalysis(cmd *cobra.Command, typeName string, filePatterns []string, opts analyzeOptions) error {
	// Validate analysis type
	analysisType := analyze.GetType(typeName)
	if analysisType == nil {
		return fmt.Errorf("unknown analysis type %q (use --list to see available types)", typeName)
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

	// Expand file patterns and read contents
	files, err := expandAndReadFiles(repoRoot, filePatterns)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no files matched the provided patterns")
	}

	if !opts.quiet {
		cmd.Printf("Analyzing %d file(s) with %q analysis...\n", len(files), typeName)
	}

	// Build the full prompt
	fullPrompt, err := analysisType.BuildPrompt(files)
	if err != nil {
		return fmt.Errorf("build prompt: %w", err)
	}

	// Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	// Build the request
	reqBody, _ := json.Marshal(map[string]interface{}{
		"repo_path":     repoRoot,
		"git_ref":       "analyze",
		"agent":         opts.agentName,
		"model":         opts.model,
		"reasoning":     opts.reasoning,
		"custom_prompt": fullPrompt,
		"agentic":       false, // Analysis is read-only
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

	if !opts.quiet {
		cmd.Printf("Enqueued analysis job %d (agent: %s)\n", job.ID, job.Agent)
	}

	// If --fix, we need to wait for analysis, run fixer, then mark addressed
	if opts.fix {
		return runAnalyzeAndFix(cmd, serverAddr, repoRoot, job.ID, analysisType, opts)
	}

	// If --wait, poll until job completes and show result
	if opts.wait {
		return waitForPromptJob(cmd, serverAddr, job.ID, opts.quiet)
	}

	return nil
}

// runAnalyzeAndFix waits for analysis to complete, runs a fixer agent, then marks addressed
func runAnalyzeAndFix(cmd *cobra.Command, serverAddr, repoRoot string, jobID int64, analysisType *analyze.AnalysisType, opts analyzeOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if !opts.quiet {
		cmd.Printf("Waiting for analysis to complete...")
	}

	// Wait for analysis job to complete (with timeout)
	ctx, cancel := context.WithTimeout(ctx, analyzeJobTimeout)
	defer cancel()

	review, err := waitForAnalysisJob(ctx, serverAddr, jobID)
	if err != nil {
		return fmt.Errorf("analysis failed: %w", err)
	}

	if !opts.quiet {
		cmd.Printf(" done!\n\n")
		cmd.Println("Analysis result:")
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println(review.Output)
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println()
	}

	// Build the fix prompt
	fixPrompt := buildFixPrompt(analysisType, review.Output)

	// Resolve fix agent (defaults to analysis agent)
	fixAgentName := opts.fixAgent
	if fixAgentName == "" {
		fixAgentName = opts.agentName
	}
	fixModel := opts.fixModel
	if fixModel == "" {
		fixModel = opts.model
	}

	if !opts.quiet {
		cmd.Printf("Running fix agent (%s) to apply changes...\n\n", fixAgentName)
	}

	// Get HEAD before running fix agent (errors are non-fatal, just skip verification)
	headBefore, headErr := git.ResolveSHA(repoRoot, "HEAD")
	canVerifyCommits := headErr == nil

	// Run the fix agent locally in agentic mode
	if err := runFixAgent(cmd, repoRoot, fixAgentName, fixModel, opts.reasoning, fixPrompt, opts.quiet); err != nil {
		return fmt.Errorf("fix agent failed: %w", err)
	}

	// Check if a commit was created (only if we could get HEAD before)
	var commitCreated bool
	if canVerifyCommits {
		headAfter, err := git.ResolveSHA(repoRoot, "HEAD")
		if err == nil && headBefore != headAfter {
			commitCreated = true
		}

		// If no commit was created, check for uncommitted changes and retry with commit instructions
		if !commitCreated {
			hasChanges, err := git.HasUncommittedChanges(repoRoot)
			if err == nil && hasChanges {
				if !opts.quiet {
					cmd.Println("\nNo commit was created. Re-running agent with commit instructions...")
					cmd.Println()
				}

				commitPrompt := buildCommitPrompt(analysisType)
				if err := runFixAgent(cmd, repoRoot, fixAgentName, fixModel, opts.reasoning, commitPrompt, opts.quiet); err != nil {
					if !opts.quiet {
						cmd.Printf("\nWarning: commit agent failed: %v\n", err)
					}
				}

				// Check again if commit was created
				headFinal, err := git.ResolveSHA(repoRoot, "HEAD")
				if err == nil && headFinal != headAfter {
					commitCreated = true
				}
			}
		}
	}

	if !opts.quiet {
		if !canVerifyCommits {
			// Couldn't verify commits, don't report on commit status
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

	// Mark the analysis as addressed
	if err := markJobAddressed(serverAddr, jobID); err != nil {
		// Non-fatal - the fixes were applied, just couldn't update status
		if !opts.quiet {
			cmd.Printf("\nWarning: could not mark job as addressed: %v\n", err)
		}
	} else if !opts.quiet {
		cmd.Printf("Analysis job %d marked as addressed\n", jobID)
	}

	return nil
}

// waitForAnalysisJob polls until the job completes and returns the review.
// The context controls the maximum wait time.
func waitForAnalysisJob(ctx context.Context, serverAddr string, jobID int64) (*storage.Review, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	pollInterval := 1 * time.Second
	maxInterval := 5 * time.Second

	for {
		// Check for cancellation/timeout
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for job: %w", ctx.Err())
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/jobs?id=%d", serverAddr, jobID), nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("check job status: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, body)
		}

		var jobsResp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("parse job status: %w", err)
		}
		resp.Body.Close()

		if len(jobsResp.Jobs) == 0 {
			return nil, fmt.Errorf("job %d not found", jobID)
		}

		job := jobsResp.Jobs[0]
		switch job.Status {
		case storage.JobStatusDone:
			// Fetch the review
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
			return &review, nil

		case storage.JobStatusFailed:
			return nil, fmt.Errorf("job failed: %s", job.Error)

		case storage.JobStatusCanceled:
			return nil, fmt.Errorf("job was canceled")
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for job: %w", ctx.Err())
		case <-time.After(pollInterval):
		}

		if pollInterval < maxInterval {
			pollInterval = pollInterval * 3 / 2
			if pollInterval > maxInterval {
				pollInterval = maxInterval
			}
		}
	}
}

// buildFixPrompt constructs a prompt for the fixer agent
func buildFixPrompt(analysisType *analyze.AnalysisType, analysisOutput string) string {
	var sb strings.Builder
	sb.WriteString("# Fix Request\n\n")
	sb.WriteString(fmt.Sprintf("An analysis of type **%s** was performed and produced the following findings:\n\n", analysisType.Name))
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

// buildCommitPrompt constructs a prompt to commit uncommitted changes
func buildCommitPrompt(analysisType *analyze.AnalysisType) string {
	var sb strings.Builder
	sb.WriteString("# Commit Request\n\n")
	sb.WriteString("There are uncommitted changes from a previous fix operation.\n\n")
	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Review the current uncommitted changes using `git status` and `git diff`\n")
	sb.WriteString("2. Stage the appropriate files\n")
	sb.WriteString("3. Create a git commit with a descriptive message\n\n")
	sb.WriteString("The commit message should:\n")
	sb.WriteString(fmt.Sprintf("- Reference the '%s' analysis that prompted the changes\n", analysisType.Name))
	sb.WriteString("- Summarize what was changed and why\n")
	sb.WriteString("- Be concise but informative\n")
	return sb.String()
}

// runFixAgent runs an agent locally in agentic mode to apply fixes
func runFixAgent(cmd *cobra.Command, repoPath, agentName, model, reasoning, prompt string, quiet bool) error {
	// Load config
	cfg, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve agent
	if agentName == "" {
		agentName = cfg.DefaultAgent
	}

	a, err := agent.GetAvailable(agentName)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Configure agent: agentic mode, with model and reasoning
	reasoningLevel := agent.ParseReasoningLevel(reasoning)
	a = a.WithAgentic(true).WithReasoning(reasoningLevel)
	if model != "" {
		a = a.WithModel(model)
	}

	// Use stdout for streaming output
	var out io.Writer = cmd.OutOrStdout()
	if quiet {
		out = io.Discard
	}

	// Use command context for cancellation support
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	_, err = a.Review(ctx, repoPath, "fix", prompt, out)
	if err != nil {
		return err
	}

	if !quiet {
		fmt.Fprintln(out) // Final newline
	}
	return nil
}

// markJobAddressed marks a job as addressed via the API
func markJobAddressed(serverAddr string, jobID int64) error {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"job_id":    jobID,
		"addressed": true,
	})

	resp, err := http.Post(serverAddr+"/api/review/address", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mark addressed failed: %s", body)
	}
	return nil
}

// expandAndReadFiles expands glob patterns and reads file contents.
// Returns a map of relative path -> content.
func expandAndReadFiles(repoRoot string, patterns []string) (map[string]string, error) {
	files := make(map[string]string)
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		// Handle ./... pattern (all files recursively)
		if pattern == "./..." || pattern == "..." {
			if err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					// Skip hidden directories and common non-code directories
					base := filepath.Base(path)
					if strings.HasPrefix(base, ".") || base == "node_modules" || base == "vendor" {
						return filepath.SkipDir
					}
					return nil
				}
				// Only include source files
				if isSourceFile(path) {
					relPath, _ := filepath.Rel(repoRoot, path)
					if !seen[relPath] {
						seen[relPath] = true
						content, err := os.ReadFile(path)
						if err != nil {
							return fmt.Errorf("read %s: %w", relPath, err)
						}
						files[relPath] = string(content)
					}
				}
				return nil
			}); err != nil {
				return nil, err
			}
			continue
		}

		// Make pattern absolute if relative
		absPattern := pattern
		if !filepath.IsAbs(pattern) {
			absPattern = filepath.Join(repoRoot, pattern)
		}

		// Expand glob pattern
		matches, err := filepath.Glob(absPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}

		if len(matches) == 0 {
			// Try as literal file
			if _, err := os.Stat(absPattern); err == nil {
				matches = []string{absPattern}
			} else {
				return nil, fmt.Errorf("no files match pattern %q", pattern)
			}
		}

		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				continue
			}

			if info.IsDir() {
				// If directory, include all source files in it
				if err := filepath.Walk(match, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if info.IsDir() {
						return nil
					}
					if isSourceFile(path) {
						relPath, _ := filepath.Rel(repoRoot, path)
						if !seen[relPath] {
							seen[relPath] = true
							content, err := os.ReadFile(path)
							if err != nil {
								return fmt.Errorf("read %s: %w", relPath, err)
							}
							files[relPath] = string(content)
						}
					}
					return nil
				}); err != nil {
					return nil, err
				}
			} else {
				relPath, _ := filepath.Rel(repoRoot, match)
				if !seen[relPath] {
					seen[relPath] = true
					content, err := os.ReadFile(match)
					if err != nil {
						return nil, fmt.Errorf("read %s: %w", relPath, err)
					}
					files[relPath] = string(content)
				}
			}
		}
	}

	return files, nil
}

// isSourceFile returns true if the file looks like source code
func isSourceFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	sourceExts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
		".rs": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cc": true,
		".java": true, ".kt": true, ".scala": true, ".rb": true, ".php": true,
		".swift": true, ".m": true, ".cs": true, ".fs": true, ".vb": true,
		".sh": true, ".bash": true, ".zsh": true, ".fish": true,
		".sql": true, ".graphql": true, ".proto": true,
		".yaml": true, ".yml": true, ".toml": true, ".json": true,
		".md": true, ".txt": true, ".html": true, ".css": true, ".scss": true,
	}
	return sourceExts[ext]
}
