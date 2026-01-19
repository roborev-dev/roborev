package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/roborev/internal/config"
	"github.com/wesm/roborev/internal/daemon"
	"github.com/wesm/roborev/internal/git"
	"github.com/wesm/roborev/internal/skills"
	"github.com/wesm/roborev/internal/storage"
	"github.com/wesm/roborev/internal/update"
	"github.com/wesm/roborev/internal/version"
)

var (
	serverAddr string
	verbose    bool

	// Polling intervals for waitForJob - exposed for testing
	pollStartInterval = 1 * time.Second
	pollMaxInterval   = 5 * time.Second
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "roborev",
		Short: "Automatic code review for git commits",
		Long:  "roborev automatically reviews git commits using AI agents (Codex, Claude Code, Gemini, Copilot, OpenCode)",
	}

	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", "http://127.0.0.1:7373", "daemon server address")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(reviewCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(showCmd())
	rootCmd.AddCommand(respondCmd())
	rootCmd.AddCommand(addressCmd())
	rootCmd.AddCommand(installHookCmd())
	rootCmd.AddCommand(uninstallHookCmd())
	rootCmd.AddCommand(daemonCmd())
	rootCmd.AddCommand(streamCmd())
	rootCmd.AddCommand(tuiCmd())
	rootCmd.AddCommand(refineCmd())
	rootCmd.AddCommand(promptCmd())
	rootCmd.AddCommand(repoCmd())
	rootCmd.AddCommand(skillsCmd())
	rootCmd.AddCommand(updateCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		// Check for exitError to exit with specific code without extra output
		if exitErr, ok := err.(*exitError); ok {
			os.Exit(exitErr.code)
		}
		os.Exit(1)
	}
}

// getDaemonAddr returns the daemon address from runtime file or default
func getDaemonAddr() string {
	if info, err := daemon.ReadRuntime(); err == nil {
		return fmt.Sprintf("http://%s", info.Addr)
	}
	return serverAddr
}

// ensureDaemon checks if daemon is running, starts it if not
// If daemon is running but has different version, restart it
func ensureDaemon() error {
	client := &http.Client{Timeout: 500 * time.Millisecond}

	// First check runtime file for daemon address
	if info, err := daemon.ReadRuntime(); err == nil {
		addr := fmt.Sprintf("http://%s/api/status", info.Addr)
		resp, err := client.Get(addr)
		if err == nil {
			defer resp.Body.Close()

			// Parse response to get actual daemon version
			var status struct {
				Version string `json:"version"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&status)

			// Fail closed: restart if decode fails, version empty, or mismatch
			if decodeErr != nil || status.Version == "" || status.Version != version.Version {
				if verbose {
					fmt.Printf("Daemon version mismatch or unreadable (daemon: %s, cli: %s), restarting...\n", status.Version, version.Version)
				}
				return restartDaemon()
			}

			serverAddr = fmt.Sprintf("http://%s", info.Addr)
			return nil
		}
	}

	// Try default address - also check version from response
	resp, err := client.Get(serverAddr + "/api/status")
	if err == nil {
		defer resp.Body.Close()
		var status struct {
			Version string `json:"version"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)

		// Fail closed: restart if decode fails, version empty, or mismatch
		if decodeErr != nil || status.Version == "" || status.Version != version.Version {
			if verbose {
				fmt.Printf("Daemon version mismatch or unreadable (daemon: %s, cli: %s), restarting...\n", status.Version, version.Version)
			}
			return restartDaemon()
		}
		return nil
	}

	// Start daemon in background
	return startDaemon()
}

// startDaemon starts a new daemon process
func startDaemon() error {
	if verbose {
		fmt.Println("Starting daemon...")
	}

	roborevdPath, err := exec.LookPath("roborevd")
	if err != nil {
		exe, _ := os.Executable()
		roborevdPath = filepath.Join(filepath.Dir(exe), "roborevd")
	}

	cmd := exec.Command(roborevdPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Wait for daemon to be ready and update serverAddr from runtime file
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if info, err := daemon.ReadRuntime(); err == nil {
			addr := fmt.Sprintf("http://%s", info.Addr)
			resp, err := client.Get(addr + "/api/status")
			if err == nil {
				resp.Body.Close()
				serverAddr = addr
				return nil
			}
		}
	}

	return fmt.Errorf("daemon failed to start")
}

// stopDaemon stops the running daemon using PID from daemon.json
func stopDaemon() error {
	info, err := daemon.ReadRuntime()
	if err == nil && info.PID > 0 {
		// Kill by specific PID
		if runtime.GOOS == "windows" {
			exec.Command("taskkill", "/PID", fmt.Sprintf("%d", info.PID), "/F").Run()
		} else {
			// Send SIGTERM first for graceful shutdown
			exec.Command("kill", "-TERM", fmt.Sprintf("%d", info.PID)).Run()
			time.Sleep(500 * time.Millisecond)
			// Then SIGKILL to ensure it's dead
			exec.Command("kill", "-KILL", fmt.Sprintf("%d", info.PID)).Run()
		}
		// Clean up runtime file
		daemon.RemoveRuntime()
	} else {
		// Fallback to pkill if no PID file (shouldn't happen normally)
		if runtime.GOOS == "windows" {
			exec.Command("taskkill", "/IM", "roborevd.exe", "/F").Run()
		} else {
			exec.Command("pkill", "-TERM", "-x", "roborevd").Run()
			time.Sleep(500 * time.Millisecond)
			exec.Command("pkill", "-KILL", "-x", "roborevd").Run()
		}
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}

// restartDaemon stops the running daemon and starts a new one
func restartDaemon() error {
	stopDaemon()

	// Checkpoint WAL to ensure clean state for new daemon
	// Retry a few times in case daemon hasn't fully released the DB
	if dbPath := storage.DefaultDBPath(); dbPath != "" {
		var lastErr error
		for i := 0; i < 3; i++ {
			db, err := storage.Open(dbPath)
			if err != nil {
				lastErr = err
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				lastErr = err
				db.Close()
				time.Sleep(200 * time.Millisecond)
				continue
			}
			db.Close()
			lastErr = nil
			break
		}
		if lastErr != nil && verbose {
			fmt.Printf("Warning: WAL checkpoint failed: %v\n", lastErr)
		}
	}

	return startDaemon()
}

func initCmd() *cobra.Command {
	var agent string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize roborev in current repository",
		Long: `Initialize roborev with a single command:
  - Creates ~/.roborev/ global config directory
  - Creates .roborev.toml in repo (if --agent specified)
  - Installs post-commit hook
  - Starts the daemon`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Initializing roborev...")

			// 1. Ensure we're in a git repo
			root, err := git.GetRepoRoot(".")
			if err != nil {
				return fmt.Errorf("not a git repository - run this from inside a git repo")
			}

			// 2. Create config directory and default config
			configDir := config.DataDir()
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("create config dir: %w", err)
			}

			configPath := config.GlobalConfigPath()
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				cfg := config.DefaultConfig()
				if agent != "" {
					cfg.DefaultAgent = agent
				}
				if err := config.SaveGlobal(cfg); err != nil {
					return fmt.Errorf("save config: %w", err)
				}
				fmt.Printf("  Created config at %s\n", configPath)
			} else {
				fmt.Printf("  Config already exists at %s\n", configPath)
			}

			// 3. Create per-repo config if agent specified
			repoConfigPath := filepath.Join(root, ".roborev.toml")
			if agent != "" {
				if _, err := os.Stat(repoConfigPath); os.IsNotExist(err) {
					repoConfig := fmt.Sprintf("# roborev per-repo configuration\nagent = %q\n", agent)
					if err := os.WriteFile(repoConfigPath, []byte(repoConfig), 0644); err != nil {
						return fmt.Errorf("create repo config: %w", err)
					}
					fmt.Printf("  Created %s\n", repoConfigPath)
				}
			}

			// 4. Install post-commit hook
			hooksDir, err := git.GetHooksPath(root)
			if err != nil {
				return fmt.Errorf("get hooks path: %w", err)
			}
			hookPath := filepath.Join(hooksDir, "post-commit")
			hookContent := generateHookContent()

			// Ensure hooks directory exists
			if err := os.MkdirAll(hooksDir, 0755); err != nil {
				return fmt.Errorf("create hooks directory: %w", err)
			}

			// Check for existing hook
			if existing, err := os.ReadFile(hookPath); err == nil {
				if !strings.Contains(string(existing), "roborev") {
					// Append to existing hook
					hookContent = string(existing) + "\n" + hookContent
				} else {
					fmt.Println("  Hook already installed")
					goto startDaemon
				}
			}

			if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
				return fmt.Errorf("install hook: %w", err)
			}
			fmt.Printf("  Installed post-commit hook\n")

		startDaemon:
			// 5. Start daemon
			if err := ensureDaemon(); err != nil {
				fmt.Printf("  Warning: %v\n", err)
				fmt.Println("  Run 'roborev daemon start' to start manually")
			} else {
				fmt.Println("  Daemon is running")
			}

			// 5. Success message
			fmt.Println()
			fmt.Println("Ready! Every commit will now be automatically reviewed.")
			fmt.Println()
			fmt.Println("Commands:")
			fmt.Println("  roborev status      - view queue and daemon status")
			fmt.Println("  roborev show HEAD   - view review for a commit")
			fmt.Println("  roborev tui         - interactive terminal UI")

			return nil
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "default agent (codex, claude-code, gemini, copilot, opencode)")

	return cmd
}

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the roborev daemon",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureDaemon(); err != nil {
				return err
			}
			fmt.Println("Daemon started")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			stopDaemon()
			fmt.Println("Daemon stopped")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			stopDaemon()
			if err := ensureDaemon(); err != nil {
				return err
			}
			fmt.Println("Daemon restarted")
			return nil
		},
	})

	return cmd
}

// MaxDirtyDiffSize is the maximum size of a dirty diff in bytes (200KB)
const MaxDirtyDiffSize = 200 * 1024

func reviewCmd() *cobra.Command {
	var (
		repoPath   string
		sha        string
		agent      string
		reasoning  string
		quiet      bool
		dirty      bool
		wait       bool
		branch     bool
		baseBranch string
		since      string
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
  roborev review --branch     # Review all commits on current branch since main
  roborev review --branch --base develop  # Review branch against develop
  roborev review --since HEAD~5  # Review last 5 commits
  roborev review --since abc123  # Review commits since abc123 (exclusive)
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// In quiet mode, suppress cobra's error output (hook uses &, so exit code doesn't matter)
			if quiet {
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
			}

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

			// Ensure daemon is running
			if err := ensureDaemon(); err != nil {
				return err // Return error (quiet mode silences output, not exit code)
			}

			// Validate mutually exclusive options
			if branch && dirty {
				return fmt.Errorf("cannot use --branch with --dirty")
			}
			if branch && since != "" {
				return fmt.Errorf("cannot use --branch with --since")
			}
			if since != "" && dirty {
				return fmt.Errorf("cannot use --since with --dirty")
			}
			if branch && len(args) > 0 {
				return fmt.Errorf("cannot specify commits with --branch")
			}
			if since != "" && len(args) > 0 {
				return fmt.Errorf("cannot specify commits with --since")
			}

			var gitRef string
			var diffContent string

			if branch {
				// Branch review - review all commits since diverging from base
				base := baseBranch
				if base == "" {
					var err error
					base, err = git.GetDefaultBranch(root)
					if err != nil {
						return fmt.Errorf("cannot determine base branch: %w", err)
					}
				}

				// Validate not on base branch
				currentBranch := git.GetCurrentBranch(root)
				if currentBranch == git.LocalBranchName(base) {
					return fmt.Errorf("already on %s - create a feature branch first", git.LocalBranchName(base))
				}

				// Get merge-base
				mergeBase, err := git.GetMergeBase(root, base, "HEAD")
				if err != nil {
					return fmt.Errorf("cannot find merge-base with %s: %w", base, err)
				}

				// Validate has commits
				commits, err := git.GetCommitsSince(root, mergeBase)
				if err != nil {
					return fmt.Errorf("cannot get commits: %w", err)
				}
				if len(commits) == 0 {
					return fmt.Errorf("no commits on branch since %s", base)
				}

				gitRef = mergeBase + ".." + "HEAD"

				if !quiet {
					cmd.Printf("Reviewing branch %q: %d commits since %s\n",
						currentBranch, len(commits), base)
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

			// Make request - server will validate and resolve refs
			reqBody, _ := json.Marshal(map[string]interface{}{
				"repo_path":    root,
				"git_ref":      gitRef,
				"agent":        agent,
				"reasoning":    reasoning,
				"diff_content": diffContent,
			})

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
			json.Unmarshal(body, &job)

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
	cmd.Flags().StringVar(&agent, "agent", "", "agent to use (codex, claude-code, gemini, copilot, opencode)")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: thorough (default), standard, or fast")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output (for use in hooks)")
	cmd.Flags().BoolVar(&dirty, "dirty", false, "review uncommitted changes instead of a commit")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for review to complete and show result")
	cmd.Flags().BoolVar(&branch, "branch", false, "review all changes since branch diverged from base")
	cmd.Flags().StringVar(&baseBranch, "base", "", "base branch for --branch comparison (default: auto-detect)")
	cmd.Flags().StringVar(&since, "since", "", "review commits since this commit (exclusive, like git's .. range)")

	return cmd
}

// waitForJob polls until a job completes and displays the review
// Uses the provided serverAddr to ensure we poll the same daemon that received the job.
func waitForJob(cmd *cobra.Command, serverAddr string, jobID int64, quiet bool) error {
	client := &http.Client{Timeout: 5 * time.Second}

	if !quiet {
		cmd.Printf("Waiting for review to complete...")
	}

	// Poll with exponential backoff
	pollInterval := pollStartInterval
	maxInterval := pollMaxInterval
	unknownStatusCount := 0
	const maxUnknownRetries = 10 // Give up after 10 consecutive unknown statuses

	for {
		resp, err := client.Get(fmt.Sprintf("%s/api/jobs?id=%d", serverAddr, jobID))
		if err != nil {
			return fmt.Errorf("failed to check job status: %w", err)
		}

		// Handle non-200 responses
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
			if !quiet {
				cmd.Printf(" done!\n\n")
			}
			// Fetch and display the review
			return showReview(cmd, serverAddr, jobID, quiet)

		case storage.JobStatusFailed:
			if !quiet {
				cmd.Printf(" failed!\n")
			}
			return fmt.Errorf("review failed: %s", job.Error)

		case storage.JobStatusCanceled:
			if !quiet {
				cmd.Printf(" canceled!\n")
			}
			return fmt.Errorf("review was canceled")

		case storage.JobStatusQueued, storage.JobStatusRunning:
			// Still in progress, continue polling
			unknownStatusCount = 0 // Reset counter on known status
			time.Sleep(pollInterval)
			if pollInterval < maxInterval {
				pollInterval = pollInterval * 3 / 2 // 1.5x backoff
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
				pollInterval = pollInterval * 3 / 2
				if pollInterval > maxInterval {
					pollInterval = maxInterval
				}
			}
		}
	}
}

// showReview fetches and displays a review by job ID
// When quiet is true, suppresses output but still returns exit code based on verdict.
func showReview(cmd *cobra.Command, addr string, jobID int64, quiet bool) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/api/review?job_id=%d", addr, jobID))
	if err != nil {
		return fmt.Errorf("failed to fetch review: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no review found for job %d", jobID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error fetching review (%d): %s", resp.StatusCode, body)
	}

	var review storage.Review
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return fmt.Errorf("failed to parse review: %w", err)
	}

	if !quiet {
		cmd.Printf("Review (by %s)\n", review.Agent)
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println(review.Output)
	}

	// Return exit code based on verdict
	verdict := storage.ParseVerdict(review.Output)
	if verdict == "F" {
		// Use a special error that cobra will treat as exit code 1
		return &exitError{code: 1}
	}

	return nil
}

// exitError is an error that signals a specific exit code
type exitError struct {
	code int
}

func (e *exitError) Error() string {
	return fmt.Sprintf("exit code %d", e.code)
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon and queue status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running (and restart if version mismatch)
			if err := ensureDaemon(); err != nil {
				fmt.Println("Daemon: not running")
				fmt.Println()
				fmt.Println("Start with: roborev daemon start")
				return nil
			}

			addr := getDaemonAddr()
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(addr + "/api/status")
			if err != nil {
				fmt.Println("Daemon: not running")
				fmt.Println()
				fmt.Println("Start with: roborev daemon start")
				return nil
			}
			defer resp.Body.Close()

			var status storage.DaemonStatus
			if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			fmt.Println("Daemon: running")
			fmt.Printf("Workers: %d/%d active\n", status.ActiveWorkers, status.MaxWorkers)
			fmt.Printf("Jobs:    %d queued, %d running, %d completed, %d failed\n",
				status.QueuedJobs, status.RunningJobs, status.CompletedJobs, status.FailedJobs)
			fmt.Println()

			// Get recent jobs
			resp, err = client.Get(addr + "/api/jobs?limit=10")
			if err != nil {
				return nil
			}
			defer resp.Body.Close()

			var jobsResp struct {
				Jobs []storage.ReviewJob `json:"jobs"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
				return nil
			}

			if len(jobsResp.Jobs) > 0 {
				fmt.Println("Recent Jobs:")
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintf(w, "  ID\tSHA\tRepo\tAgent\tStatus\tTime\n")
				for _, j := range jobsResp.Jobs {
					elapsed := ""
					if j.StartedAt != nil {
						if j.FinishedAt != nil {
							elapsed = j.FinishedAt.Sub(*j.StartedAt).Round(time.Second).String()
						} else {
							elapsed = time.Since(*j.StartedAt).Round(time.Second).String() + "..."
						}
					}
					fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\t%s\n",
						j.ID, shortRef(j.GitRef), j.RepoName, j.Agent, j.Status, elapsed)
				}
				w.Flush()
			}

			return nil
		},
	}
}

func showCmd() *cobra.Command {
	var forceJobID bool

	cmd := &cobra.Command{
		Use:   "show [job_id|sha]",
		Short: "Show review for a commit or job",
		Long: `Show review output for a commit or job.

The argument can be either a job ID (numeric) or a commit SHA.
Job IDs are displayed in review notifications and the TUI.

In a git repo, the argument is first tried as a git ref. If that fails
and it's numeric, it's treated as a job ID. Use --job to force job ID.

Examples:
  roborev show              # Show review for HEAD
  roborev show abc123       # Show review for commit
  roborev show 42           # Job ID (if "42" is not a valid git ref)
  roborev show --job 42     # Force as job ID even if "42" is a valid ref`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running (and restart if version mismatch)
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			addr := getDaemonAddr()
			client := &http.Client{Timeout: 5 * time.Second}

			var queryURL string
			var displayRef string

			if len(args) == 0 {
				if forceJobID {
					return fmt.Errorf("--job requires a job ID argument")
				}
				// Default to HEAD
				sha := "HEAD"
				if root, err := git.GetRepoRoot("."); err == nil {
					if resolved, err := git.ResolveSHA(root, sha); err == nil {
						sha = resolved
					}
				}
				queryURL = addr + "/api/review?sha=" + sha
				displayRef = shortSHA(sha)
			} else {
				arg := args[0]
				var isJobID bool
				var resolvedSHA string

				if forceJobID {
					isJobID = true
				} else {
					// Try to resolve as SHA first (handles numeric SHAs like "123456")
					if root, err := git.GetRepoRoot("."); err == nil {
						if resolved, err := git.ResolveSHA(root, arg); err == nil {
							resolvedSHA = resolved
						}
					}
					// If not resolvable as SHA and is numeric, treat as job ID
					if resolvedSHA == "" {
						if _, err := strconv.ParseInt(arg, 10, 64); err == nil {
							isJobID = true
						}
					}
				}

				if isJobID {
					queryURL = addr + "/api/review?job_id=" + arg
					displayRef = "job " + arg
				} else {
					sha := arg
					if resolvedSHA != "" {
						sha = resolvedSHA
					}
					queryURL = addr + "/api/review?sha=" + sha
					displayRef = shortSHA(sha)
				}
			}

			resp, err := client.Get(queryURL)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon (is it running?)")
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("no review found for %s", displayRef)
			}

			var review storage.Review
			if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			// Avoid redundant "job X (job X, ...)" output
			if strings.HasPrefix(displayRef, "job ") {
				fmt.Printf("Review for %s (by %s)\n", displayRef, review.Agent)
			} else {
				fmt.Printf("Review for %s (job %d, by %s)\n", displayRef, review.JobID, review.Agent)
			}
			fmt.Println(strings.Repeat("-", 60))
			fmt.Println(review.Output)

			return nil
		},
	}

	cmd.Flags().BoolVar(&forceJobID, "job", false, "force argument to be treated as job ID")
	return cmd
}

func respondCmd() *cobra.Command {
	var (
		responder  string
		message    string
		forceJobID bool
	)

	cmd := &cobra.Command{
		Use:   "respond <job_id|sha> [message]",
		Short: "Add a response to a review",
		Long: `Add a response or note to a review.

The first argument can be either a job ID (numeric) or a commit SHA.
Using job IDs is recommended since they are displayed in the TUI.

Examples:
  roborev respond 42 "Fixed the null pointer issue"
  roborev respond 42 -m "Added missing error handling"
  roborev respond abc123 "Addressed by refactoring"
  roborev respond 42     # Opens editor for message
  roborev respond --job 1234567 "msg"  # Force numeric arg as job ID`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			ref := args[0]

			// Check if ref is a job ID (numeric) or SHA
			var jobID int64
			var sha string

			if forceJobID {
				// --job flag: treat ref as job ID
				id, err := strconv.ParseInt(ref, 10, 64)
				if err != nil {
					return fmt.Errorf("--job requires numeric job ID, got %q", ref)
				}
				jobID = id
			} else {
				// Auto-detect: try git object first, then job ID
				// A numeric string could be either - check if it resolves as a git object first
				if root, err := git.GetRepoRoot("."); err == nil {
					if resolved, err := git.ResolveSHA(root, ref); err == nil {
						sha = resolved
					}
				}

				// If not a valid git object, try parsing as job ID
				if sha == "" {
					if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
						jobID = id
					} else {
						// Not a valid git object or job ID - use ref as-is
						sha = ref
					}
				}
			}

			// Message can be positional argument or flag
			if len(args) > 1 {
				message = args[1]
			}

			// If no message provided, open editor
			if message == "" {
				editor := os.Getenv("EDITOR")
				if editor == "" {
					editor = "vim"
				}

				tmpfile, err := os.CreateTemp("", "roborev-response-*.md")
				if err != nil {
					return fmt.Errorf("create temp file: %w", err)
				}
				tmpfile.Close()
				defer os.Remove(tmpfile.Name())

				editorCmd := exec.Command(editor, tmpfile.Name())
				editorCmd.Stdin = os.Stdin
				editorCmd.Stdout = os.Stdout
				editorCmd.Stderr = os.Stderr
				if err := editorCmd.Run(); err != nil {
					return fmt.Errorf("editor failed: %w", err)
				}

				content, err := os.ReadFile(tmpfile.Name())
				if err != nil {
					return fmt.Errorf("read response: %w", err)
				}
				message = strings.TrimSpace(string(content))
			}

			if message == "" {
				return fmt.Errorf("empty response, aborting")
			}

			if responder == "" {
				responder = os.Getenv("USER")
				if responder == "" {
					responder = "anonymous"
				}
			}

			// Build request with either job_id or sha
			reqData := map[string]interface{}{
				"responder": responder,
				"response":  message,
			}
			if jobID != 0 {
				reqData["job_id"] = jobID
			} else {
				reqData["sha"] = sha
			}

			reqBody, _ := json.Marshal(reqData)

			addr := getDaemonAddr()
			resp, err := http.Post(addr+"/api/respond", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusCreated {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("failed to add response: %s", body)
			}

			fmt.Println("Response added successfully")
			return nil
		},
	}

	cmd.Flags().StringVar(&responder, "responder", "", "responder name (default: $USER)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "response message (opens editor if not provided)")
	cmd.Flags().BoolVar(&forceJobID, "job", false, "force argument to be treated as job ID (not SHA)")

	return cmd
}

func addressCmd() *cobra.Command {
	var unaddress bool

	cmd := &cobra.Command{
		Use:   "address <review_id>",
		Short: "Mark a review as addressed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			reviewID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || reviewID <= 0 {
				return fmt.Errorf("invalid review_id: %s", args[0])
			}

			addressed := !unaddress
			reqBody, _ := json.Marshal(map[string]interface{}{
				"review_id": reviewID,
				"addressed": addressed,
			})

			addr := getDaemonAddr()
			resp, err := http.Post(addr+"/api/review/address", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("failed to mark review: %s", body)
			}

			if addressed {
				fmt.Printf("Review %d marked as addressed\n", reviewID)
			} else {
				fmt.Printf("Review %d marked as unaddressed\n", reviewID)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&unaddress, "unaddress", false, "mark as unaddressed instead")

	return cmd
}

// findJobForCommit finds a job for the given commit SHA in the specified repo
func findJobForCommit(repoPath, sha string) (*storage.ReviewJob, error) {
	addr := getDaemonAddr()
	client := &http.Client{Timeout: 5 * time.Second}

	// Normalize repo path to handle symlinks/relative paths consistently
	normalizedRepo := repoPath
	if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
		normalizedRepo = resolved
	}
	if abs, err := filepath.Abs(normalizedRepo); err == nil {
		normalizedRepo = abs
	}

	// Query by git_ref and repo to avoid matching jobs from different repos
	queryURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&repo=%s&limit=1",
		addr, url.QueryEscape(sha), url.QueryEscape(normalizedRepo))
	resp, err := client.Get(queryURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query for %s: server returned %s", sha, resp.Status)
	}

	var result struct {
		Jobs []storage.ReviewJob `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("query for %s: decode error: %w", sha, err)
	}

	if len(result.Jobs) > 0 {
		return &result.Jobs[0], nil
	}

	// Fallback: if repo filter yielded no results, try git_ref only
	// This handles cases where daemon stores paths differently
	// Fetch jobs and filter client-side to avoid cross-repo mismatch
	// Use high limit since we're filtering client-side; in practice same SHA
	// across many repos is rare
	fallbackURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&limit=100", addr, url.QueryEscape(sha))
	fallbackResp, err := client.Get(fallbackURL)
	if err != nil {
		return nil, fmt.Errorf("fallback query for %s: %w", sha, err)
	}
	defer fallbackResp.Body.Close()

	if fallbackResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fallback query for %s: server returned %s", sha, fallbackResp.Status)
	}

	var fallbackResult struct {
		Jobs []storage.ReviewJob `json:"jobs"`
	}
	if err := json.NewDecoder(fallbackResp.Body).Decode(&fallbackResult); err != nil {
		return nil, fmt.Errorf("fallback query for %s: decode error: %w", sha, err)
	}

	// Filter client-side: find a job whose repo path matches when normalized
	for i := range fallbackResult.Jobs {
		job := &fallbackResult.Jobs[i]
		jobRepo := job.RepoPath
		// Skip empty or relative paths to avoid false matches from cwd resolution
		if jobRepo == "" || !filepath.IsAbs(jobRepo) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(jobRepo); err == nil {
			jobRepo = resolved
		}
		if jobRepo == normalizedRepo {
			return job, nil
		}
	}

	return nil, nil
}

// waitForReview waits for a review to complete and returns it
func waitForReview(jobID int64) (*storage.Review, error) {
	return waitForReviewWithInterval(jobID, 2*time.Second)
}

func waitForReviewWithInterval(jobID int64, pollInterval time.Duration) (*storage.Review, error) {
	addr := getDaemonAddr()
	client := &http.Client{Timeout: 10 * time.Second}

	for {
		resp, err := client.Get(fmt.Sprintf("%s/api/jobs?id=%d", addr, jobID))
		if err != nil {
			return nil, fmt.Errorf("polling job %d: %w", jobID, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("polling job %d: server returned %s", jobID, resp.Status)
		}

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("polling job %d: decode error: %w", jobID, err)
		}
		resp.Body.Close()

		if len(result.Jobs) == 0 {
			return nil, fmt.Errorf("job %d not found", jobID)
		}

		job := result.Jobs[0]
		switch job.Status {
		case storage.JobStatusDone:
			// Get the review
			reviewResp, err := client.Get(fmt.Sprintf("%s/api/review?job_id=%d", addr, jobID))
			if err != nil {
				return nil, err
			}
			defer reviewResp.Body.Close()

			var review storage.Review
			if err := json.NewDecoder(reviewResp.Body).Decode(&review); err != nil {
				return nil, err
			}
			return &review, nil

		case storage.JobStatusFailed:
			return nil, fmt.Errorf("job failed: %s", job.Error)

		case storage.JobStatusCanceled:
			return nil, fmt.Errorf("job was canceled")
		}

		time.Sleep(pollInterval)
	}
}

// enqueueReview enqueues a review job and returns the job ID
func enqueueReview(repoPath, gitRef, agentName string) (int64, error) {
	addr := getDaemonAddr()

	reqBody, _ := json.Marshal(map[string]string{
		"repo_path": repoPath,
		"git_ref":   gitRef,
		"agent":     agentName,
	})

	resp, err := http.Post(addr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("enqueue failed: %s", body)
	}

	var job storage.ReviewJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return 0, err
	}

	return job.ID, nil
}

// getResponsesForJob fetches responses for a job
func getResponsesForJob(jobID int64) ([]storage.Response, error) {
	addr := getDaemonAddr()
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(fmt.Sprintf("%s/api/responses?job_id=%d", addr, jobID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch responses: %s", resp.Status)
	}

	var result struct {
		Responses []storage.Response `json:"responses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Responses, nil
}

func streamCmd() *cobra.Command {
	var repoFilter string

	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Stream review events in real-time",
		Long: `Stream review events from the daemon in real-time.

Events are printed as JSONL (one JSON object per line).

Examples:
  roborev stream              # Stream all events
  roborev stream --repo .     # Stream events for current repo only
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			// Resolve repo filter if set - use main repo root for worktree compatibility
			if repoFilter != "" {
				root, err := git.GetMainRepoRoot(repoFilter)
				if err != nil {
					return fmt.Errorf("resolve repo path: %w", err)
				}
				repoFilter = root
			}

			// Build URL with optional repo filter
			addr := getDaemonAddr()
			streamURL := addr + "/api/stream/events"
			if repoFilter != "" {
				streamURL += "?" + url.Values{"repo": {repoFilter}}.Encode()
			}

			// Create request
			req, err := http.NewRequest("GET", streamURL, nil)
			if err != nil {
				return fmt.Errorf("create request: %w", err)
			}

			// Set up context for Ctrl+C handling
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req = req.WithContext(ctx)

			// Handle Ctrl+C
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt)
			go func() {
				<-sigCh
				cancel()
			}()

			// Make request
			client := &http.Client{Timeout: 0} // No timeout for streaming
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("connect to daemon: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("stream failed: %s", body)
			}

			// Stream events - pass through lines directly to preserve all fields
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Println(scanner.Text())
			}
			if err := scanner.Err(); err != nil && ctx.Err() == nil {
				return fmt.Errorf("read stream: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repoFilter, "repo", "", "filter events by repository path")

	return cmd
}

func installHookCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "install-hook",
		Short: "Install post-commit hook in current repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := git.GetRepoRoot(".")
			if err != nil {
				return fmt.Errorf("not a git repository: %w", err)
			}

			hooksDir, err := git.GetHooksPath(root)
			if err != nil {
				return fmt.Errorf("get hooks path: %w", err)
			}
			hookPath := filepath.Join(hooksDir, "post-commit")

			// Check if hook already exists
			if _, err := os.Stat(hookPath); err == nil && !force {
				return fmt.Errorf("hook already exists at %s (use --force to overwrite)", hookPath)
			}

			// Ensure hooks directory exists
			if err := os.MkdirAll(hooksDir, 0755); err != nil {
				return fmt.Errorf("create hooks directory: %w", err)
			}

			hookContent := generateHookContent()

			if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
				return fmt.Errorf("write hook: %w", err)
			}

			fmt.Printf("Installed post-commit hook at %s\n", hookPath)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing hook")

	return cmd
}

func uninstallHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall-hook",
		Short: "Remove post-commit hook from current repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := git.GetRepoRoot(".")
			if err != nil {
				return fmt.Errorf("not a git repository: %w", err)
			}

			hooksDir, err := git.GetHooksPath(root)
			if err != nil {
				return fmt.Errorf("get hooks path: %w", err)
			}
			hookPath := filepath.Join(hooksDir, "post-commit")

			// Check if hook exists
			content, err := os.ReadFile(hookPath)
			if os.IsNotExist(err) {
				fmt.Println("No post-commit hook found")
				return nil
			} else if err != nil {
				return fmt.Errorf("read hook: %w", err)
			}

			// Check if it contains roborev (case-insensitive)
			hookStr := string(content)
			if !strings.Contains(strings.ToLower(hookStr), "roborev") {
				fmt.Println("Post-commit hook does not contain roborev")
				return nil
			}

			// Remove roborev lines from the hook
			lines := strings.Split(hookStr, "\n")
			var newLines []string
			for _, line := range lines {
				// Skip roborev-related lines (case-insensitive)
				if strings.Contains(strings.ToLower(line), "roborev") {
					continue
				}
				newLines = append(newLines, line)
			}

			// Check if anything remains (besides shebang and empty lines)
			hasContent := false
			for _, line := range newLines {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" && !strings.HasPrefix(trimmed, "#!") {
					hasContent = true
					break
				}
			}

			if hasContent {
				// Write back the hook without roborev lines
				newContent := strings.Join(newLines, "\n")
				if err := os.WriteFile(hookPath, []byte(newContent), 0755); err != nil {
					return fmt.Errorf("write hook: %w", err)
				}
				fmt.Printf("Removed roborev from post-commit hook at %s\n", hookPath)
			} else {
				// Remove the hook entirely
				if err := os.Remove(hookPath); err != nil {
					return fmt.Errorf("remove hook: %w", err)
				}
				fmt.Printf("Removed post-commit hook at %s\n", hookPath)
			}

			return nil
		},
	}
}

func skillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage AI agent skills",
		Long:  "Install and manage roborev skills for AI agents (Claude Code, Codex)",
	}

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install roborev skills for AI agents",
		Long: `Install roborev skills to your AI agent configuration directories.

Skills are installed for agents whose config directories exist:
  - Claude Code: ~/.claude/skills/
  - Codex: ~/.codex/skills/

This command is idempotent - running it multiple times is safe.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			results, err := skills.Install()
			if err != nil {
				return err
			}

			// formatSkills formats skill names with the correct invocation prefix per agent
			// Claude uses /skill:name, Codex uses $skill:name
			// Directory names use hyphens (roborev-address) but invocation uses colons (roborev:address)
			formatSkills := func(agent skills.Agent, skillNames []string) string {
				prefix := "/"
				if agent == skills.AgentCodex {
					prefix = "$"
				}
				formatted := make([]string, len(skillNames))
				for i, name := range skillNames {
					// Convert roborev-address to roborev:address
					formatted[i] = prefix + strings.Replace(name, "roborev-", "roborev:", 1)
				}
				return strings.Join(formatted, ", ")
			}

			anyInstalled := false
			var installedAgents []skills.Agent
			for _, result := range results {
				if result.Skipped {
					fmt.Printf("%s: skipped (no ~/.%s directory)\n", result.Agent, result.Agent)
					continue
				}

				if len(result.Installed) > 0 {
					anyInstalled = true
					installedAgents = append(installedAgents, result.Agent)
					fmt.Printf("%s: installed %s\n", result.Agent, formatSkills(result.Agent, result.Installed))
				}
				if len(result.Updated) > 0 {
					anyInstalled = true
					if len(result.Installed) == 0 {
						installedAgents = append(installedAgents, result.Agent)
					}
					fmt.Printf("%s: updated %s\n", result.Agent, formatSkills(result.Agent, result.Updated))
				}
			}

			if !anyInstalled {
				fmt.Println("\nNo agents found. Install Claude Code or Codex first, then run this command.")
			} else {
				fmt.Println("\nSkills installed! Try:")
				for _, agent := range installedAgents {
					if agent == skills.AgentClaude {
						fmt.Println("  Claude Code: /roborev:address or /roborev:respond")
					} else if agent == skills.AgentCodex {
						fmt.Println("  Codex: $roborev:address or $roborev:respond")
					}
				}
			}

			return nil
		},
	}

	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Update roborev skills for agents that have them installed",
		Long: `Update roborev skills only for agents that already have them installed.

Unlike 'install', this command does NOT install skills for new agents -
it only updates existing installations. Used by 'roborev update'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			results, err := skills.Update()
			if err != nil {
				return err
			}

			if len(results) == 0 {
				fmt.Println("No skills to update (none installed)")
				return nil
			}

			for _, result := range results {
				if len(result.Updated) > 0 {
					fmt.Printf("%s: updated %d skill(s)\n", result.Agent, len(result.Updated))
				}
				if len(result.Installed) > 0 {
					// This can happen if user had one skill but not the other
					fmt.Printf("%s: installed %d skill(s)\n", result.Agent, len(result.Installed))
				}
			}

			return nil
		},
	}

	cmd.AddCommand(installCmd)
	cmd.AddCommand(updateCmd)
	return cmd
}

func updateCmd() *cobra.Command {
	var checkOnly bool
	var yes bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update roborev to the latest version",
		Long: `Check for and install roborev updates.

Shows exactly what will be downloaded and where it will be installed.
Requires confirmation before making changes (use --yes to skip).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Checking for updates...")

			info, err := update.CheckForUpdate(true) // Force check, ignore cache
			if err != nil {
				return fmt.Errorf("check for updates: %w", err)
			}

			if info == nil {
				fmt.Printf("Already running latest version (%s)\n", version.Version)
				return nil
			}

			fmt.Printf("\n  Current version: %s\n", info.CurrentVersion)
			fmt.Printf("  Latest version:  %s\n", info.LatestVersion)
			if info.IsDevBuild {
				fmt.Println("\nYou're running a dev build. Latest stable release available.")
			} else {
				fmt.Println("\nUpdate available!")
			}
			fmt.Println("\nDownload:")
			fmt.Printf("  URL:  %s\n", info.DownloadURL)
			fmt.Printf("  Size: %s\n", update.FormatSize(info.Size))
			if info.Checksum != "" {
				fmt.Printf("  SHA256: %s\n", info.Checksum)
			}

			// Show install location
			currentExe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find executable: %w", err)
			}
			currentExe, _ = filepath.EvalSymlinks(currentExe)
			binDir := filepath.Dir(currentExe)

			fmt.Println("\nInstall location:")
			fmt.Printf("  %s\n", binDir)

			if checkOnly {
				return nil
			}

			// Confirm
			if !yes {
				fmt.Print("\nProceed with update? [y/N] ")
				var response string
				fmt.Scanln(&response)
				if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
					fmt.Println("Update cancelled")
					return nil
				}
			}

			fmt.Println()

			// Progress display
			var lastPercent int
			progressFn := func(downloaded, total int64) {
				if total > 0 {
					percent := int(downloaded * 100 / total)
					if percent != lastPercent {
						fmt.Printf("\rDownloading... %d%% (%s / %s)",
							percent, update.FormatSize(downloaded), update.FormatSize(total))
						lastPercent = percent
					}
				}
			}

			// Perform update
			if err := update.PerformUpdate(info, progressFn); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}

			fmt.Printf("\nUpdated to %s\n", info.LatestVersion)

			// Restart daemon if running
			if daemonInfo, err := daemon.ReadRuntime(); err == nil && daemonInfo != nil {
				fmt.Print("Restarting daemon... ")
				// Stop old daemon with timeout
				stopURL := fmt.Sprintf("http://%s/api/shutdown", daemonInfo.Addr)
				client := &http.Client{Timeout: 5 * time.Second}
				if resp, err := client.Post(stopURL, "application/json", nil); err != nil {
					fmt.Printf("warning: failed to stop daemon: %v\n", err)
				} else {
					resp.Body.Close()
				}
				time.Sleep(500 * time.Millisecond)

				// Start new daemon
				daemonPath := filepath.Join(binDir, "roborevd")
				if runtime.GOOS == "windows" {
					daemonPath += ".exe"
				}
				startCmd := exec.Command(daemonPath)
				if err := startCmd.Start(); err != nil {
					fmt.Printf("warning: failed to start daemon: %v\n", err)
				} else {
					fmt.Println("OK")
				}
			}

			// Update skills using the NEW binary (current process has old embedded skills)
			// Use "skills update" to only update agents that already have skills installed
			if skills.IsInstalled(skills.AgentClaude) || skills.IsInstalled(skills.AgentCodex) {
				fmt.Print("Updating skills... ")
				newBinary := filepath.Join(binDir, "roborev")
				if runtime.GOOS == "windows" {
					newBinary += ".exe"
				}
				skillsCmd := exec.Command(newBinary, "skills", "update")
				if output, err := skillsCmd.CombinedOutput(); err != nil {
					fmt.Printf("warning: %v\n", err)
				} else {
					// Parse output to show what was updated
					lines := strings.Split(strings.TrimSpace(string(output)), "\n")
					for _, line := range lines {
						if strings.Contains(line, "updated") {
							fmt.Println(line)
						}
					}
					if !strings.Contains(string(output), "updated") {
						fmt.Println("OK")
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "only check for updates, don't install")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show roborev version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("roborev %s\n", version.Version)
		},
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func shortRef(ref string) string {
	// For ranges like "abc123..def456", show as "abc123..def456" (up to 17 chars)
	// For single SHAs, truncate to 7 chars
	if strings.Contains(ref, "..") {
		if len(ref) > 17 {
			return ref[:17]
		}
		return ref
	}
	return shortSHA(ref)
}

// generateHookContent creates the post-commit hook script content.
// It bakes the path to the currently running binary for consistency.
// Falls back to PATH lookup if the baked path becomes unavailable.
func generateHookContent() string {
	// Get path to the currently running binary (not just first in PATH)
	roborevPath, err := os.Executable()
	if err == nil {
		// Resolve symlinks to get the real path
		if resolved, err := filepath.EvalSymlinks(roborevPath); err == nil {
			roborevPath = resolved
		}
	} else {
		// Fallback to PATH lookup if os.Executable fails (shouldn't happen)
		roborevPath, _ = exec.LookPath("roborev")
		if roborevPath == "" {
			roborevPath = "roborev"
		}
	}

	// Prefer baked path (security), fall back to PATH only if baked is missing
	return fmt.Sprintf(`#!/bin/sh
# roborev post-commit hook - auto-reviews every commit
ROBOREV=%q
if [ ! -x "$ROBOREV" ]; then
    ROBOREV=$(command -v roborev 2>/dev/null)
    [ -z "$ROBOREV" ] || [ ! -x "$ROBOREV" ] && exit 0
fi
"$ROBOREV" enqueue --quiet 2>/dev/null &
`, roborevPath)
}
