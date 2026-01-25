package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/prompt"
	"github.com/roborev-dev/roborev/internal/storage"
)

// postCommitWaitDelay is the delay after creating a commit before checking
// if a review was queued by the post-commit hook. Tests can override this.
var postCommitWaitDelay = 1 * time.Second

func refineCmd() *cobra.Command {
	var (
		agentName         string
		reasoning         string
		maxIterations     int
		quiet             bool
		allowUnsafeAgents bool
		since             string
	)

	cmd := &cobra.Command{
		Use:          "refine",
		Short:        "Automatically address failed code reviews",
		SilenceUsage: true,
		Long: `Automatically address failed code reviews using an AI agent.

This command runs an agentic loop that:
1. Finds failed reviews for commits on the current branch
2. Uses an AI agent to make code changes addressing the findings
3. Commits the changes and waits for re-review
4. Repeats until all reviews pass or max iterations reached

Prerequisites:
- Must be in a git repository
- Working tree must be clean (no uncommitted changes)
- Not in the middle of a rebase

The agent will run tests and verify the build before committing.

Use --since to specify a starting commit when on the main branch or to
limit how far back to look for reviews to address.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			unsafeFlagChanged := cmd.Flags().Changed("allow-unsafe-agents")
			return runRefine(agentName, reasoning, maxIterations, quiet, allowUnsafeAgents, unsafeFlagChanged, since)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use for addressing findings (default: from config)")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard (default), or thorough")
	cmd.Flags().IntVar(&maxIterations, "max-iterations", 10, "maximum refinement iterations")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress agent output, show elapsed time instead")
	cmd.Flags().BoolVar(&allowUnsafeAgents, "allow-unsafe-agents", false, "allow agents to run without sandboxing")
	cmd.Flags().StringVar(&since, "since", "", "base commit to refine from (exclusive, like git's .. range)")

	return cmd
}

// stepTimer tracks elapsed time for quiet mode display
type stepTimer struct {
	start  time.Time
	stop   chan struct{}
	done   chan struct{}
	prefix string
}

var isTerminal = func(fd uintptr) bool {
	return isatty.IsTerminal(fd)
}

func newStepTimer() *stepTimer {
	return &stepTimer{start: time.Now()}
}

func (t *stepTimer) elapsed() string {
	d := time.Since(t.start)
	return fmt.Sprintf("[%d:%02d]", int(d.Minutes()), int(d.Seconds())%60)
}

// startLive begins a live-updating timer display. Call stopLive() when done.
func (t *stepTimer) startLive(prefix string) {
	t.prefix = prefix
	t.stop = make(chan struct{})
	t.done = make(chan struct{})
	t.start = time.Now()

	go func() {
		defer close(t.done)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		// Print initial state
		fmt.Printf("\r%s %s", t.prefix, t.elapsed())

		for {
			select {
			case <-t.stop:
				return
			case <-ticker.C:
				fmt.Printf("\r%s %s", t.prefix, t.elapsed())
			}
		}
	}()
}

// stopLive stops the live timer and prints the final elapsed time
func (t *stepTimer) stopLive() {
	if t.stop != nil {
		close(t.stop)
		<-t.done // Wait for goroutine to exit
	}
	// Clear line and print final time with newline
	fmt.Printf("\r%s %s\n", t.prefix, t.elapsed())
}

// validateRefineContext validates git and branch preconditions for refine.
// Returns repoPath, currentBranch, defaultBranch, mergeBase, or an error.
// This validation happens before any daemon interaction.
func validateRefineContext(since string) (repoPath, currentBranch, defaultBranch, mergeBase string, err error) {
	repoPath, err = git.GetRepoRoot(".")
	if err != nil {
		return "", "", "", "", fmt.Errorf("not in a git repository: %w", err)
	}

	if git.IsRebaseInProgress(repoPath) {
		return "", "", "", "", fmt.Errorf("rebase in progress - complete or abort it first")
	}

	if !git.IsWorkingTreeClean(repoPath) {
		return "", "", "", "", fmt.Errorf("working tree not clean - commit or stash your changes first")
	}

	defaultBranch, err = git.GetDefaultBranch(repoPath)
	if err != nil {
		return "", "", "", "", fmt.Errorf("cannot determine default branch: %w", err)
	}

	currentBranch = git.GetCurrentBranch(repoPath)

	if since != "" {
		// Resolve the --since commit to a full SHA
		mergeBase, err = git.ResolveSHA(repoPath, since)
		if err != nil {
			return "", "", "", "", fmt.Errorf("cannot resolve --since %q: %w", since, err)
		}
		// Verify --since is an ancestor of HEAD (reachable in commit history)
		isAncestor, err := git.IsAncestor(repoPath, mergeBase, "HEAD")
		if err != nil {
			return "", "", "", "", fmt.Errorf("checking --since ancestry: %w", err)
		}
		if !isAncestor {
			return "", "", "", "", fmt.Errorf("--since %q is not an ancestor of HEAD", since)
		}
	} else {
		// Default behavior: use merge-base with default branch
		if currentBranch == git.LocalBranchName(defaultBranch) {
			return "", "", "", "", fmt.Errorf("refusing to refine on %s branch without --since flag", git.LocalBranchName(defaultBranch))
		}

		mergeBase, err = git.GetMergeBase(repoPath, defaultBranch, "HEAD")
		if err != nil {
			return "", "", "", "", fmt.Errorf("cannot find merge-base with %s: %w", defaultBranch, err)
		}
	}

	return repoPath, currentBranch, defaultBranch, mergeBase, nil
}

func runRefine(agentName, reasoningStr string, maxIterations int, quiet bool, allowUnsafeAgents bool, unsafeFlagChanged bool, since string) error {
	// 1. Validate git and branch context (before touching daemon)
	repoPath, currentBranch, defaultBranch, mergeBase, err := validateRefineContext(since)
	if err != nil {
		return err
	}

	// 2. Connect to daemon (only after all validation passes)
	if err := ensureDaemon(); err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	client, err := daemon.NewHTTPClientFromRuntime()
	if err != nil {
		return fmt.Errorf("cannot connect to daemon: %w", err)
	}

	// Print branch context after successful connection
	if since != "" {
		fmt.Printf("Refining commits since %s on branch %q\n", mergeBase[:7], currentBranch)
	} else {
		fmt.Printf("Refining branch %q (diverged from %s at %s)\n", currentBranch, defaultBranch, mergeBase[:7])
	}

	// Resolve agent
	cfg, _ := config.LoadGlobal()
	resolvedAgent := config.ResolveAgent(agentName, repoPath, cfg)
	allowUnsafeAgents = resolveAllowUnsafeAgents(allowUnsafeAgents, unsafeFlagChanged, cfg)
	agent.SetAllowUnsafeAgents(allowUnsafeAgents)
	if cfg != nil {
		agent.SetAnthropicAPIKey(cfg.AnthropicAPIKey)
	}

	// Resolve reasoning level from CLI or config (default: fast)
	resolvedReasoning, err := config.ResolveRefineReasoning(reasoningStr, repoPath)
	if err != nil {
		return err
	}
	reasoningLevel := agent.ParseReasoningLevel(resolvedReasoning)

	// Get the agent with configured reasoning level
	addressAgent, err := selectRefineAgent(resolvedAgent, reasoningLevel)
	if err != nil {
		return fmt.Errorf("no agent available: %w", err)
	}
	fmt.Printf("Using agent: %s\n", addressAgent.Name())

	// 3. Refinement loop
	// Track current failed review - when a fix fails, we continue fixing it
	// before moving on to the next oldest failed commit
	var currentFailedReview *storage.Review
	// Track reviews we've given up on this run to avoid re-selecting them
	skippedReviews := make(map[int64]bool)

	for iteration := 1; iteration <= maxIterations; {
		// Get commits on current branch
		commits, err := git.GetCommitsSince(repoPath, mergeBase)
		if err != nil {
			return fmt.Errorf("cannot get commits: %w", err)
		}

		if len(commits) == 0 {
			fmt.Println("No commits on branch - nothing to refine")
			return nil
		}

		// Only search for a new failed review if we don't have one to work on
		// (either first iteration, or previous fix passed)
		if currentFailedReview == nil {
			currentFailedReview, err = findFailedReviewForBranch(client, commits, skippedReviews)
			if err != nil {
				return fmt.Errorf("error finding reviews: %w", err)
			}
		}

		if currentFailedReview == nil {
			// Check for pending jobs before triggering a branch review
			pendingJob, err := findPendingJobForBranch(client, repoPath, commits)
			if err != nil {
				return fmt.Errorf("error checking pending jobs: %w", err)
			}
			if pendingJob != nil {
				// Wait for the pending job to complete, then loop back to check its result
				// This does NOT consume an iteration - we only count actual fix attempts
				fmt.Printf("Waiting for in-progress review (job %d)...\n", pendingJob.ID)
				review, err := client.WaitForReview(pendingJob.ID)
				if err != nil {
					fmt.Printf("Warning: review failed: %v\n", err)
					continue // Loop back, will re-check
				}
				verdict := storage.ParseVerdict(review.Output)
				if verdict == "F" && !review.Addressed {
					currentFailedReview = review
				} else if verdict == "P" {
					if err := client.MarkReviewAddressed(review.ID); err != nil {
						fmt.Printf("Warning: failed to mark review %d as addressed: %v\n", review.ID, err)
					}
					continue // Loop back to check for more
				}
				// If we have a failed review now, fall through to address it
				// Otherwise loop back
				if currentFailedReview == nil {
					continue
				}
			} else {
				// No pending commit jobs and no failed reviews - check for branch review
				// Resolve HEAD to SHA to ensure stable rangeRef (avoids stale results if HEAD moves)
				headSHA, err := git.ResolveSHA(repoPath, "HEAD")
				if err != nil {
					return fmt.Errorf("cannot resolve HEAD: %w", err)
				}
				rangeRef := mergeBase + ".." + headSHA

				// Check if a branch review job already exists (queued or running).
				// Note: We don't filter by agent here because the --agent flag controls
				// the ADDRESSING agent (which fixes code), not the REVIEW agent.
				// We use the SHA-based rangeRef to ensure we only reuse jobs for the
				// exact same HEAD - if HEAD has moved, we want a fresh review.
				existingJob, err := client.FindPendingJobForRef(repoPath, rangeRef)
				if err != nil {
					return fmt.Errorf("error checking for existing branch review: %w", err)
				}

				var jobID int64
				if existingJob != nil {
					// Wait for existing pending branch review
					fmt.Printf("Waiting for in-progress branch review (job %d)...\n", existingJob.ID)
					jobID = existingJob.ID
				} else {
					// No pending branch review - enqueue a new one
					fmt.Println("No individual failed reviews - running branch review...")
					jobID, err = client.EnqueueReview(repoPath, rangeRef, resolvedAgent)
					if err != nil {
						return fmt.Errorf("failed to enqueue branch review: %w", err)
					}
					fmt.Printf("Waiting for branch review (job %d)...\n", jobID)
				}

				review, err := client.WaitForReview(jobID)
				if err != nil {
					return fmt.Errorf("branch review failed: %w", err)
				}

				verdict := storage.ParseVerdict(review.Output)
				if verdict == "P" {
					fmt.Println("\nAll reviews passed! Branch is ready.")
					return nil
				}

				// Branch review failed - address its findings
				fmt.Printf("\nBranch review failed. Addressing findings...\n")
				currentFailedReview = review
			}
		}

		// Now we have a review to address - this counts as an iteration
		fmt.Printf("\n=== Refinement iteration %d/%d ===\n", iteration, maxIterations)
		iteration++

		// Address the failed review
		liveTimer := quiet && isTerminal(os.Stdout.Fd())
		if !quiet {
			fmt.Printf("Addressing review (job %d)...\n", currentFailedReview.JobID)
		}

		// Get previous attempts for context
		previousAttempts, err := client.GetCommentsForJob(currentFailedReview.JobID)
		if err != nil {
			return fmt.Errorf("fetch previous comments: %w", err)
		}

		// Build address prompt
		builder := prompt.NewBuilder(nil)
		addressPrompt, err := builder.BuildAddressPrompt(repoPath, currentFailedReview, previousAttempts)
		if err != nil {
			return fmt.Errorf("build address prompt: %w", err)
		}

		// Record clean state before agent runs to detect user edits during run
		wasCleanBeforeAgent := git.IsWorkingTreeClean(repoPath)

		// Capture HEAD SHA and branch to detect concurrent changes (branch switch, pull, etc.)
		headBeforeAgent, err := git.ResolveSHA(repoPath, "HEAD")
		if err != nil {
			return fmt.Errorf("cannot determine HEAD: %w", err)
		}
		branchBeforeAgent := git.GetCurrentBranch(repoPath)

		// Create temp worktree to isolate agent work from user's working tree
		worktreePath, cleanupWorktree, err := createTempWorktree(repoPath)
		if err != nil {
			return fmt.Errorf("create worktree: %w", err)
		}

		// Determine output writer for agent streaming
		var agentOutput io.Writer = os.Stdout
		if quiet {
			agentOutput = io.Discard
		}

		// Run agent to make changes in the isolated worktree (1 hour timeout)
		timer := newStepTimer()
		if liveTimer {
			timer.startLive(fmt.Sprintf("Addressing review (job %d)...", currentFailedReview.JobID))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Hour)
		output, agentErr := addressAgent.Review(ctx, worktreePath, "HEAD", addressPrompt, agentOutput)
		cancel()

		// Show elapsed time
		if liveTimer {
			timer.stopLive()
		} else if quiet {
			fmt.Printf("Addressing review (job %d)... %s\n", currentFailedReview.JobID, timer.elapsed())
		} else {
			fmt.Printf("Agent completed %s\n", timer.elapsed())
		}

		// Check if user made changes to main repo during agent run
		if wasCleanBeforeAgent && !git.IsWorkingTreeClean(repoPath) {
			cleanupWorktree()
			return fmt.Errorf("working tree changed during refine - aborting to prevent data loss")
		}

		// Check if HEAD or branch changed during agent run (branch switch, pull, etc.)
		headAfterAgent, resolveErr := git.ResolveSHA(repoPath, "HEAD")
		if resolveErr != nil {
			cleanupWorktree()
			return fmt.Errorf("cannot determine HEAD after agent run: %w", resolveErr)
		}
		branchAfterAgent := git.GetCurrentBranch(repoPath)
		if headAfterAgent != headBeforeAgent || branchAfterAgent != branchBeforeAgent {
			cleanupWorktree()
			return fmt.Errorf("HEAD changed during refine (was %s on %s, now %s on %s) - aborting to prevent applying patch to wrong commit",
				headBeforeAgent[:7], branchBeforeAgent, headAfterAgent[:7], branchAfterAgent)
		}

		if agentErr != nil {
			cleanupWorktree()
			fmt.Printf("Agent error: %v\n", agentErr)
			fmt.Println("Will retry in next iteration")
			continue
		}

		// Check if changes were made in worktree
		if git.IsWorkingTreeClean(worktreePath) {
			cleanupWorktree()
			fmt.Println("Agent made no changes")
			// Check how many times we've tried this review (only count our own attempts)
			attempts, err := client.GetCommentsForJob(currentFailedReview.JobID)
			if err != nil {
				return fmt.Errorf("fetch attempts: %w", err)
			}
			noChangeAttempts := 0
			for _, a := range attempts {
				if a.Responder == "roborev-refine" && strings.Contains(a.Response, "could not determine how to address") {
					noChangeAttempts++
				}
			}
			if noChangeAttempts >= 2 {
				// Tried 3 times (including this one), give up on this review
				// Do NOT mark as addressed - the review still needs attention
				fmt.Println("Giving up after multiple failed attempts (review remains unaddressed)")
				client.AddComment(currentFailedReview.JobID, "roborev-refine", "Agent could not determine how to address findings (attempt 3, giving up)")
				skippedReviews[currentFailedReview.ID] = true // Don't re-select this run
				currentFailedReview = nil                     // Move on to next oldest failed commit
			} else {
				// Record attempt but don't mark addressed - might work on retry with different context
				client.AddComment(currentFailedReview.JobID, "roborev-refine", fmt.Sprintf("Agent could not determine how to address findings (attempt %d)", noChangeAttempts+1))
				fmt.Printf("Attempt %d failed, will retry\n", noChangeAttempts+1)
			}
			continue
		}

		// Apply worktree changes to main repo
		if err := applyWorktreeChanges(repoPath, worktreePath); err != nil {
			cleanupWorktree()
			return fmt.Errorf("apply worktree changes: %w", err)
		}
		cleanupWorktree()

		// Commit the changes
		commitMsg := fmt.Sprintf("Address review findings (job %d)\n\n%s", currentFailedReview.JobID, summarizeAgentOutput(output))
		newCommit, err := git.CreateCommit(repoPath, commitMsg)
		if err != nil {
			return fmt.Errorf("failed to commit changes: %w", err)
		}
		fmt.Printf("Created commit %s\n", newCommit[:7])

		// Add response recording what was done (include full agent output for database)
		responseText := fmt.Sprintf("Created commit %s to address findings\n\n%s", newCommit[:7], output)
		client.AddComment(currentFailedReview.JobID, "roborev-refine", responseText)

		// Mark old review as addressed
		if err := client.MarkReviewAddressed(currentFailedReview.ID); err != nil {
			fmt.Printf("Warning: failed to mark review %d as addressed: %v\n", currentFailedReview.ID, err)
		}

		// Wait for new commit to be reviewed (if post-commit hook triggers it)
		// Give a short delay for the hook to fire
		time.Sleep(postCommitWaitDelay)

		// Check if a review was queued for the new commit
		newJob, err := client.FindJobForCommit(repoPath, newCommit)
		if err != nil || newJob == nil {
			// No review queued - move on to next oldest failed commit
			currentFailedReview = nil
			continue
		}

		fmt.Printf("Waiting for review of new commit (job %d)...\n", newJob.ID)
		review, err := client.WaitForReview(newJob.ID)
		if err != nil {
			fmt.Printf("Warning: review failed: %v\n", err)
			currentFailedReview = nil // Move on, can't determine status
			continue
		}

		verdict := storage.ParseVerdict(review.Output)
		if verdict == "P" {
			fmt.Println("New commit passed review!")
			if err := client.MarkReviewAddressed(review.ID); err != nil {
				fmt.Printf("Warning: failed to mark review %d as addressed: %v\n", review.ID, err)
			}
			currentFailedReview = nil // Move on to next oldest failed commit
		} else {
			fmt.Println("New commit failed review - continuing to address")
			currentFailedReview = review // Stay on this fix chain
		}
	}

	return fmt.Errorf("max iterations (%d) reached without all reviews passing", maxIterations)
}

// resolveAllowUnsafeAgents determines whether to allow unsafe agents.
// Priority: CLI flag > config file > default (true for refine).
// Refine defaults to true because it fundamentally requires file modifications.
// Users can disable with --allow-unsafe-agents=false or config if they want (though refine won't work).
func resolveAllowUnsafeAgents(flag bool, flagChanged bool, cfg *config.Config) bool {
	// If user explicitly set the CLI flag, honor their choice
	if flagChanged {
		return flag
	}
	// If config file explicitly sets allow_unsafe_agents, honor it
	if cfg != nil && cfg.AllowUnsafeAgents != nil {
		return *cfg.AllowUnsafeAgents
	}
	// Default to true for refine - it can't work without file modifications
	return true
}

// findFailedReviewForBranch finds an unaddressed failed review for any of the given commits.
// Iterates oldest to newest so earlier commits are fixed before later ones.
// Passing reviews are marked as addressed automatically.
// Reviews in the skip set are ignored (used for reviews we've given up on this run).
func findFailedReviewForBranch(client daemon.Client, commits []string, skip map[int64]bool) (*storage.Review, error) {
	// Iterate oldest to newest (commits are in chronological order)
	for _, sha := range commits {
		review, err := client.GetReviewBySHA(sha)
		if err != nil {
			return nil, fmt.Errorf("fetching review for %s: %w", sha[:7], err)
		}
		if review == nil {
			continue
		}

		// Skip already addressed reviews
		if review.Addressed {
			continue
		}

		// Skip reviews we've given up on this run
		if skip[review.ID] {
			continue
		}

		verdict := storage.ParseVerdict(review.Output)
		if verdict == "F" {
			return review, nil
		}

		// Mark passing reviews as addressed so they don't need to be checked again
		if verdict == "P" {
			if err := client.MarkReviewAddressed(review.ID); err != nil {
				return nil, fmt.Errorf("marking review %d as addressed: %w", review.ID, err)
			}
		}
	}

	return nil, nil
}

// findPendingJobForBranch finds a queued or running job for any of the given commits.
// Returns the first pending job found (oldest commit first), or nil if all jobs are complete.
func findPendingJobForBranch(client daemon.Client, repoPath string, commits []string) (*storage.ReviewJob, error) {
	for _, sha := range commits {
		job, err := client.FindJobForCommit(repoPath, sha)
		if err != nil {
			return nil, err
		}
		if job == nil {
			continue
		}
		// Check if job is still pending (queued or running)
		if job.Status == storage.JobStatusQueued || job.Status == storage.JobStatusRunning {
			return job, nil
		}
	}
	return nil, nil
}

// summarizeAgentOutput extracts a short summary from agent output
func summarizeAgentOutput(output string) string {
	lines := strings.Split(output, "\n")
	// Take first non-empty lines as summary
	var summary []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			summary = append(summary, line)
			if len(summary) >= 10 {
				break
			}
		}
	}
	if len(summary) == 0 {
		return "Automated fix"
	}
	return strings.Join(summary, "\n")
}

// createTempWorktree creates a temporary git worktree for isolated agent work
func createTempWorktree(repoPath string) (string, func(), error) {
	worktreeDir, err := os.MkdirTemp("", "roborev-refine-")
	if err != nil {
		return "", nil, err
	}

	// Create the worktree (without --recurse-submodules for compatibility with older git)
	cmd := exec.Command("git", "-C", repoPath, "worktree", "add", "--detach", worktreeDir, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(worktreeDir)
		return "", nil, fmt.Errorf("git worktree add: %w: %s", err, out)
	}

	// Initialize and update submodules in the worktree
	initArgs := []string{"-C", worktreeDir}
	if submoduleRequiresFileProtocol(worktreeDir) {
		initArgs = append(initArgs, "-c", "protocol.file.allow=always")
	}
	initArgs = append(initArgs, "submodule", "update", "--init")
	cmd = exec.Command("git", initArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		os.RemoveAll(worktreeDir)
		return "", nil, fmt.Errorf("git submodule update: %w: %s", err, out)
	}

	updateArgs := []string{"-C", worktreeDir}
	if submoduleRequiresFileProtocol(worktreeDir) {
		updateArgs = append(updateArgs, "-c", "protocol.file.allow=always")
	}
	updateArgs = append(updateArgs, "submodule", "update", "--init", "--recursive")
	cmd = exec.Command("git", updateArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		os.RemoveAll(worktreeDir)
		return "", nil, fmt.Errorf("git submodule update: %w: %s", err, out)
	}

	lfsCmd := exec.Command("git", "-C", worktreeDir, "lfs", "env")
	if err := lfsCmd.Run(); err == nil {
		cmd = exec.Command("git", "-C", worktreeDir, "lfs", "pull")
		cmd.Run()
	}

	cleanup := func() {
		exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		os.RemoveAll(worktreeDir)
	}

	return worktreeDir, cleanup, nil
}

func submoduleRequiresFileProtocol(repoPath string) bool {
	gitmodulesPaths := findGitmodulesPaths(repoPath)
	if len(gitmodulesPaths) == 0 {
		return false
	}
	for _, gitmodulesPath := range gitmodulesPaths {
		file, err := os.Open(gitmodulesPath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(parts[0]), "url") {
				continue
			}
			url := strings.TrimSpace(parts[1])
			if unquoted, err := strconv.Unquote(url); err == nil {
				url = unquoted
			}
			if isFileProtocolURL(url) {
				file.Close()
				return true
			}
		}
		file.Close()
	}
	return false
}

func findGitmodulesPaths(repoPath string) []string {
	var paths []string
	err := filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Name() == ".gitmodules" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil
	}
	return paths
}

func isFileProtocolURL(url string) bool {
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "file:") {
		return true
	}
	if strings.HasPrefix(url, "/") || strings.HasPrefix(url, "./") || strings.HasPrefix(url, "../") {
		return true
	}
	if len(url) >= 2 && isAlpha(url[0]) && url[1] == ':' {
		return true
	}
	if strings.HasPrefix(url, `\\`) {
		return true
	}
	return false
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// applyWorktreeChanges applies changes from worktree to main repo via patch
func applyWorktreeChanges(repoPath, worktreePath string) error {
	// Stage all changes in worktree
	cmd := exec.Command("git", "-C", worktreePath, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add in worktree: %w: %s", err, out)
	}

	// Get diff as patch
	diffCmd := exec.Command("git", "-C", worktreePath, "diff", "--cached", "--binary")
	diff, err := diffCmd.Output()
	if err != nil {
		return fmt.Errorf("git diff in worktree: %w", err)
	}
	if len(diff) == 0 {
		return nil // No changes
	}

	// Apply patch to main repo
	applyCmd := exec.Command("git", "-C", repoPath, "apply", "--binary", "-")
	applyCmd.Stdin = bytes.NewReader(diff)
	var stderr bytes.Buffer
	applyCmd.Stderr = &stderr
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, stderr.String())
	}

	return nil
}

func selectRefineAgent(resolvedAgent string, reasoningLevel agent.ReasoningLevel) (agent.Agent, error) {
	if resolvedAgent == "codex" && agent.IsAvailable("codex") {
		baseAgent, err := agent.Get("codex")
		if err != nil {
			return nil, err
		}
		return baseAgent.WithReasoning(reasoningLevel), nil
	}

	baseAgent, err := agent.GetAvailable(resolvedAgent)
	if err != nil {
		return nil, err
	}
	return baseAgent.WithReasoning(reasoningLevel), nil
}
