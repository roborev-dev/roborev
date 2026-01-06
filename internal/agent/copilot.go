package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// CopilotAgent runs code reviews using the GitHub Copilot CLI
type CopilotAgent struct {
	Command string // The copilot command to run (default: "copilot")
}

// NewCopilotAgent creates a new Copilot agent
func NewCopilotAgent(command string) *CopilotAgent {
	if command == "" {
		command = "copilot"
	}
	return &CopilotAgent{Command: command}
}

func (a *CopilotAgent) Name() string {
	return "copilot"
}

func (a *CopilotAgent) CommandName() string {
	return a.Command
}

func (a *CopilotAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string) (string, error) {
	// Use copilot with --prompt for non-interactive mode
	args := []string{
		"--prompt", prompt,
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("copilot failed: %w\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if len(output) == 0 {
		return "No review output generated", nil
	}

	return output, nil
}

func init() {
	Register(NewCopilotAgent(""))
}
