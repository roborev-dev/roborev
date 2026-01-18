package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			// Stdin has data (piped)
			scanner := bufio.NewScanner(os.Stdin)
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			promptText = strings.Join(lines, "\n")
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

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("enqueue failed: %s", body)
	}

	var job storage.ReviewJob
	json.Unmarshal(body, &job)

	if !quiet {
		cmd.Printf("Enqueued prompt job %d (agent: %s)\n", job.ID, job.Agent)
	}

	// If --wait, poll until job completes and show result
	if wait {
		err := waitForJob(cmd, serverAddr, job.ID, quiet)
		// Only silence Cobra's error output for exitError (verdict-based exit codes)
		if _, isExitErr := err.(*exitError); isExitErr {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
		}
		return err
	}

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
