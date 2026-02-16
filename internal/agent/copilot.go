package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// CopilotAgent runs code reviews using the GitHub Copilot CLI
type CopilotAgent struct {
	Command   string         // The copilot command to run (default: "copilot")
	Model     string         // Model to use
	Reasoning ReasoningLevel // Reasoning level (for future support)
	Agentic   bool           // Whether agentic mode is enabled (note: Copilot requires manual approval for actions)
}

// NewCopilotAgent creates a new Copilot agent
func NewCopilotAgent(command string) *CopilotAgent {
	if command == "" {
		command = "copilot"
	}
	return &CopilotAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the model preserved (reasoning not yet supported).
func (a *CopilotAgent) WithReasoning(level ReasoningLevel) Agent {
	return &CopilotAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
// Note: Copilot CLI requires manual approval for all actions and does not support
// automated unsafe execution. The agentic flag is tracked but has no effect on Copilot's behavior.
func (a *CopilotAgent) WithAgentic(agentic bool) Agent {
	return &CopilotAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *CopilotAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &CopilotAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

func (a *CopilotAgent) Name() string {
	return "copilot"
}

func (a *CopilotAgent) CommandName() string {
	return a.Command
}

func (a *CopilotAgent) CommandLine() string {
	var args []string
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *CopilotAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	args := []string{}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Stdin = strings.NewReader(prompt)
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
