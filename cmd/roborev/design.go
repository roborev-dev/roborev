package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/prompt"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func designCmd() *cobra.Command {
	var (
		agentName string
		model     string
		reasoning string
		wait      bool
		quiet     bool
	)

	cmd := &cobra.Command{
		Use:   "design [feature description]",
		Short: "Design a new software feature with a PRD and task list",
		Long: `Design a new software feature using an AI agent.

This command takes a feature description and runs an agentic design session
that explores the codebase, drafts a Product Requirements Document (PRD),
and builds a detailed implementation task list.

The feature description can be provided as:
  1. A positional argument: roborev design "webhook notifications for review completion"
  2. Via stdin: echo "webhook notifications" | roborev design

By default, the job is enqueued and the command returns immediately.
Use --wait to wait for completion and display the result.

Examples:
  roborev design "Add webhook notifications for review completion"
  roborev design --agent claude-code "Implement dark mode support"
  roborev design --wait "Add user authentication"
  echo "Add caching layer for API responses" | roborev design --wait
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDesign(cmd, args, agentName, model, reasoning, wait, quiet)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use (default: from config)")
	cmd.Flags().StringVar(&model, "model", "", "model for agent (format varies: opencode uses provider/model, others use model name)")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard, or thorough (default)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for job to complete and show result")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output (just enqueue)")

	return cmd
}

func runDesign(cmd *cobra.Command, args []string, agentName, modelStr, reasoningStr string, wait, quiet bool) error {
	// Get feature description from args or stdin
	var featureDescription string
	if len(args) > 0 {
		featureDescription = strings.Join(args, " ")
	} else {
		stat, err := os.Stdin.Stat()
		if err != nil {
			return fmt.Errorf("unable to read stdin: %w", err)
		}
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			featureDescription = string(data)
		} else {
			return fmt.Errorf("no feature description provided - pass as argument or pipe via stdin")
		}
	}

	if strings.TrimSpace(featureDescription) == "" {
		return fmt.Errorf("empty feature description")
	}

	// Determine working directory (use git repo root if in a repo, otherwise cwd)
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	repoRoot := workDir
	if root, err := git.GetRepoRoot(workDir); err == nil {
		repoRoot = root
	}

	// Build the design prompt
	designPrompt, err := prompt.NewBuilder(nil).BuildDesign(repoRoot, featureDescription, agentName)
	if err != nil {
		return fmt.Errorf("build design prompt: %w", err)
	}

	// Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	// Enqueue the job (agentic mode since design needs to explore codebase and write files)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"repo_path":     repoRoot,
		"git_ref":       "design",
		"agent":         agentName,
		"model":         modelStr,
		"reasoning":     reasoningStr,
		"custom_prompt": designPrompt,
		"agentic":       true,
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

	if !quiet {
		cmd.Printf("Enqueued design task %d (agent: %s)\n", job.ID, job.Agent)
	}

	// If --wait, poll until job completes and show result
	if wait {
		return waitForPromptJob(cmd, serverAddr, job.ID, quiet)
	}

	return nil
}
