package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// CopilotAgent runs code reviews using the GitHub Copilot CLI
type CopilotAgent struct {
	Command   string         // The copilot command to run (default: "copilot")
	Reasoning ReasoningLevel // Reasoning level (for future support)
}

// NewCopilotAgent creates a new Copilot agent
func NewCopilotAgent(command string) *CopilotAgent {
	if command == "" {
		command = "copilot"
	}
	return &CopilotAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns the agent unchanged (reasoning not supported).
func (a *CopilotAgent) WithReasoning(level ReasoningLevel) Agent {
	return a
}

func (a *CopilotAgent) Name() string {
	return "copilot"
}

func (a *CopilotAgent) CommandName() string {
	return a.Command
}

func (a *CopilotAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Use copilot with --prompt for non-interactive mode
	args := []string{
		"--prompt", prompt,
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stdout, stderr bytes.Buffer
	if sw := newSyncWriter(output); sw != nil {
		cmd.Stdout = io.MultiWriter(&stdout, sw)
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("copilot failed: %w\nstderr: %s", err, stderr.String())
	}

	result := stdout.String()
	if len(result) == 0 {
		return "No review output generated", nil
	}

	return result, nil
}

func init() {
	Register(NewCopilotAgent(""))
}
