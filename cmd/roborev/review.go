package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/prompt"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

// MaxDirtyDiffSize is the maximum size of a dirty diff in bytes (200KB)
const MaxDirtyDiffSize = 200 * 1024

func reviewCmd() *cobra.Command {
	var (
		repoPath   string
		sha        string
		agent      string
		model      string
		reasoning  string
		reviewType string
		fast       bool
		quiet      bool
		dirty      bool
		wait       bool
		branch     string
		baseBranch string
		since      string
		local      bool
	)

	cmd := &cobra.Command{
		Use:     "review [commit] or review [start] [end]",
		Aliases: []string{"enqueue"}, // Backwards compatibility
		Short:   "Review a commit, commit range, or uncommitted changes",
		Long: `Review a commit, commit range, or uncommitted changes.

Examples:
  roborev review              # Review HEAD
  roborev review abc123       # Review specific commit
  roborev review abc123 def456  # Review range from abc123 to def456 (inclusive)
  roborev review --dirty      # Review uncommitted changes
  roborev review --dirty --wait  # Review uncommitted changes and wait for result
  roborev review --type design   # Design-focused review of HEAD
  roborev review --branch     # Review all commits on current branch since main
  roborev review --branch --base develop  # Review branch against develop
  roborev review --branch=feature-xyz     # Review a specific branch
  roborev review --since HEAD~5  # Review last 5 commits
  roborev review --since abc123  # Review commits since abc123 (exclusive)
  roborev review --type security   # Security-focused review of HEAD
  roborev review --branch --type security  # Security review of branch
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// In quiet mode, suppress cobra's error output (hook uses &, so exit code doesn't matter)
			if quiet {
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
			}

			// --fast is shorthand for --reasoning fast (explicit --reasoning takes precedence)
			reasoning = resolveReasoningWithFast(reasoning, fast, cmd.Flags().Changed("reasoning"))

			// Default to current directory
			if repoPath == "" {
				repoPath = "."
			}

			// Get repo root
			root, err := git.GetRepoRoot(repoPath)
			if err != nil {
				if quiet {
					return nil // Not a repo - silent exit for hooks
				}
				return fmt.Errorf("not a git repository: %w", err)
			}

			// Skip during rebase to avoid reviewing every replayed commit
			if git.IsRebaseInProgress(root) {
				if !quiet {
					cmd.Println("Skipping: rebase in progress")
				}
				return nil // Intentional skip, exit 0
			}

			// Validate mutually exclusive options
			if branch != "" && dirty {
				return fmt.Errorf("cannot use --branch with --dirty")
			}
			if branch != "" && since != "" {
				return fmt.Errorf("cannot use --branch with --since")
			}
			if since != "" && dirty {
				return fmt.Errorf("cannot use --since with --dirty")
			}
			if branch != "" && len(args) > 0 {
				return fmt.Errorf("cannot specify commits with --branch (to review a specific branch, use --branch=<name>)")
			}
			if since != "" && len(args) > 0 {
				return fmt.Errorf("cannot specify commits with --since")
			}

			// Validate --type flag
			if reviewType != "" && reviewType != "security" && reviewType != "design" {
				return fmt.Errorf("invalid --type %q (valid: security, design)", reviewType)
			}

			// Auto-install/upgrade hooks when running from CLI
			// (not when called from a hook via --quiet).
			// Runs after validation so invalid args don't
			// cause side effects.
			if !quiet {
				autoInstallHooks(root)
			}

			// Ensure daemon is running (skip for --local mode)
			if !local {
				if err := ensureDaemon(); err != nil {
					return err // Return error (quiet mode silences output, not exit code)
				}
			}

			var gitRef string
			var diffContent string

			if branch != "" {
				// Branch review - review all commits since diverging from base
				targetRef := "HEAD"
				targetLabel := git.GetCurrentBranch(root)
				if branch != "HEAD" {
					targetRef = branch
					targetLabel = branch
					if _, err := git.ResolveSHA(root, targetRef); err != nil {
						return fmt.Errorf("cannot resolve branch %q: %w", branch, err)
					}
				}

				base := baseBranch
				if base == "" {
					var err error
					base, err = git.GetDefaultBranch(root)
					if err != nil {
						return fmt.Errorf("cannot determine base branch: %w", err)
					}
				}

				// Validate not on base branch (only when reviewing current branch)
				if targetRef == "HEAD" {
					currentBranch := git.GetCurrentBranch(root)
					if currentBranch == git.LocalBranchName(base) {
						return fmt.Errorf("already on %s - create a feature branch first", git.LocalBranchName(base))
					}
				}

				// Get merge-base
				mergeBase, err := git.GetMergeBase(root, base, targetRef)
				if err != nil {
					return fmt.Errorf("cannot find merge-base with %s: %w", base, err)
				}

				// Validate has commits
				rangeRef := mergeBase + ".." + targetRef
				commits, err := git.GetRangeCommits(root, rangeRef)
				if err != nil {
					return fmt.Errorf("cannot get commits: %w", err)
				}
				if len(commits) == 0 {
					return fmt.Errorf("no commits on branch since %s", base)
				}

				gitRef = rangeRef

				if !quiet {
					cmd.Printf("Reviewing branch %q: %d commits since %s\n",
						targetLabel, len(commits), base)
				}
			} else if since != "" {
				// Review commits since a specific commit (exclusive)
				sinceCommit, err := git.ResolveSHA(root, since)
				if err != nil {
					return fmt.Errorf("invalid --since commit %q: %w", since, err)
				}

				// Validate has commits
				commits, err := git.GetCommitsSince(root, sinceCommit)
				if err != nil {
					return fmt.Errorf("cannot get commits: %w", err)
				}
				if len(commits) == 0 {
					return fmt.Errorf("no commits since %s", since)
				}

				gitRef = sinceCommit + ".." + "HEAD"

				if !quiet {
					cmd.Printf("Reviewing %d commits since %s\n", len(commits), since)
				}
			} else if dirty {
				// Dirty review - capture uncommitted changes
				hasChanges, err := git.HasUncommittedChanges(root)
				if err != nil {
					return fmt.Errorf("check uncommitted changes: %w", err)
				}
				if !hasChanges {
					return fmt.Errorf("no uncommitted changes to review")
				}

				// Generate dirty diff (includes untracked files)
				diffContent, err = git.GetDirtyDiff(root)
				if err != nil {
					return fmt.Errorf("get dirty diff: %w", err)
				}

				// Check size limit
				if len(diffContent) > MaxDirtyDiffSize {
					return fmt.Errorf("dirty diff too large (%d bytes, max %d bytes)\nConsider committing changes in smaller chunks",
						len(diffContent), MaxDirtyDiffSize)
				}

				if diffContent == "" {
					return fmt.Errorf("no changes to review (diff is empty)")
				}

				gitRef = "dirty"
			} else if len(args) >= 2 {
				// Range: START END -> START^..END (inclusive)
				gitRef = args[0] + "^.." + args[1]
			} else if len(args) == 1 {
				// Single commit
				gitRef = args[0]
			} else {
				// Default to HEAD
				gitRef = sha
			}

			// Get branch name for tracking. When --branch=<name> targets
			// a different branch, use that name instead of the checked-out branch.
			branchName := git.GetCurrentBranch(root)
			if branch != "" && branch != "HEAD" {
				branchName = branch
			}

			// Handle --local mode: run agent directly without daemon
			if local {
				return runLocalReview(cmd, root, gitRef, diffContent, agent, model, reasoning, reviewType, quiet)
			}

			// Build request body
			reqFields := daemon.EnqueueRequest{
				RepoPath:    root,
				GitRef:      gitRef,
				Branch:      branchName,
				Agent:       agent,
				Model:       model,
				Reasoning:   reasoning,
				ReviewType:  reviewType,
				DiffContent: diffContent,
			}

			reqBody, _ := json.Marshal(reqFields)

			resp, err := http.Post(serverAddr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)

			// Handle skipped response (200 OK with skipped flag)
			if resp.StatusCode == http.StatusOK {
				var skipResp struct {
					Skipped bool   `json:"skipped"`
					Reason  string `json:"reason"`
				}
				if err := json.Unmarshal(body, &skipResp); err == nil && skipResp.Skipped {
					if !quiet {
						cmd.Printf("Skipped: %s\n", skipResp.Reason)
					}
					return nil
				}
			}

			if resp.StatusCode != http.StatusCreated {
				return fmt.Errorf("review failed: %s", body)
			}

			var job storage.ReviewJob
			_ = json.Unmarshal(body, &job)

			if !quiet {
				if dirty {
					cmd.Printf("Enqueued dirty review job %d (agent: %s)\n", job.ID, job.Agent)
				} else {
					cmd.Printf("Enqueued job %d for %s (agent: %s)\n", job.ID, shortRef(job.GitRef), job.Agent)
				}
			}

			// If --wait, poll until job completes and show result
			if wait {
				err := waitForJob(cmd, serverAddr, job.ID, quiet)
				// Only silence Cobra's error output for exitError (verdict-based exit codes)
				// Keep error output for actual failures (network errors, job not found, etc.)
				if _, isExitErr := err.(*exitError); isExitErr {
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
				}
				return err
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&repoPath, "repo", "", "path to git repository (default: current directory)")
	cmd.Flags().StringVar(&sha, "sha", "HEAD", "commit SHA to review (used when no positional args)")
	cmd.Flags().StringVar(&agent, "agent", "", "agent to use (codex, claude-code, gemini, copilot, opencode, cursor, kilo)")
	cmd.Flags().StringVar(&model, "model", "", "model for agent (format varies: opencode uses provider/model, others use model name)")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: thorough (default), standard, or fast")
	cmd.Flags().BoolVar(&fast, "fast", false, "shorthand for --reasoning fast")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output (for use in hooks)")
	cmd.Flags().BoolVar(&dirty, "dirty", false, "review uncommitted changes instead of a commit")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for review to complete and show result")
	cmd.Flags().StringVar(&branch, "branch", "", "review all changes since branch diverged from base (optionally specify branch name)")
	cmd.Flags().Lookup("branch").NoOptDefVal = "HEAD"
	cmd.Flags().StringVar(&baseBranch, "base", "", "base branch for --branch comparison (default: auto-detect)")
	cmd.Flags().StringVar(&since, "since", "", "review commits since this commit (exclusive, like git's .. range)")
	cmd.Flags().BoolVar(&local, "local", false, "run review locally without daemon (streams output to console)")
	cmd.Flags().StringVar(&reviewType, "type", "", "review type (security, design) — changes system prompt")
	registerAgentCompletion(cmd)
	registerReasoningCompletion(cmd)

	return cmd
}

// runLocalReview runs a review directly without the daemon
func runLocalReview(cmd *cobra.Command, repoPath, gitRef, diffContent, agentName, model, reasoning, reviewType string, quiet bool) error {
	// Load config
	cfg, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve and validate reasoning (matches daemon behavior)
	reasoning, err = config.ResolveReviewReasoning(reasoning, repoPath)
	if err != nil {
		return fmt.Errorf("invalid reasoning: %w", err)
	}

	// Map review_type to config workflow (matches daemon behavior)
	workflow := "review"
	if !config.IsDefaultReviewType(reviewType) {
		workflow = reviewType
	}

	// Resolve agent using workflow-specific resolution (matches daemon behavior)
	agentName = config.ResolveAgentForWorkflow(agentName, repoPath, cfg, workflow, reasoning)

	// Get the agent
	a, err := agent.GetAvailable(agentName)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Resolve model using workflow-specific resolution (matches daemon behavior)
	model = config.ResolveModelForWorkflow(model, repoPath, cfg, workflow, reasoning)

	// Configure agent with model and reasoning
	reasoningLevel := agent.ParseReasoningLevel(reasoning)
	a = a.WithReasoning(reasoningLevel).WithModel(model)

	// Use consistent output writer, respecting --quiet
	var out = cmd.OutOrStdout()
	if quiet {
		out = io.Discard
	}

	if !quiet {
		fmt.Fprintf(out, "Running %s review (model: %s, reasoning: %s)...\n\n", a.Name(), model, reasoning)
	}

	// Build prompt
	var reviewPrompt string
	if diffContent != "" {
		// Dirty review
		reviewPrompt, err = prompt.NewBuilder(nil).BuildDirty(repoPath, diffContent, 0, cfg.ReviewContextCount, a.Name(), reviewType)
	} else {
		reviewPrompt, err = prompt.NewBuilder(nil).Build(repoPath, gitRef, 0, cfg.ReviewContextCount, a.Name(), reviewType)
	}
	if err != nil {
		return fmt.Errorf("build prompt: %w", err)
	}

	// Run review with output writer
	ctx := context.Background()
	_, err = a.Review(ctx, repoPath, gitRef, reviewPrompt, out)
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	if !quiet {
		fmt.Fprintln(out) // Final newline
	}
	return nil
}
