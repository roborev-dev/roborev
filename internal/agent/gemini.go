package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// GeminiAgent runs code reviews using the Gemini CLI
type GeminiAgent struct {
	Command   string         // The gemini command to run (default: "gemini")
	Reasoning ReasoningLevel // Reasoning level (for future support)
}

// NewGeminiAgent creates a new Gemini agent
func NewGeminiAgent(command string) *GeminiAgent {
	if command == "" {
		command = "gemini"
	}
	return &GeminiAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns the agent unchanged (reasoning not supported).
func (a *GeminiAgent) WithReasoning(level ReasoningLevel) Agent {
	return a
}

func (a *GeminiAgent) Name() string {
	return "gemini"
}

func (a *GeminiAgent) CommandName() string {
	return a.Command
}

func (a *GeminiAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Use gemini with -p for non-interactive mode
	// Output goes to stdout
	args := []string{
		"-p", prompt,
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
		return "", fmt.Errorf("gemini failed: %w\nstderr: %s", err, stderr.String())
	}

	result := stdout.String()
	if len(result) == 0 {
		return "No review output generated", nil
	}

	return result, nil
}

func init() {
	Register(NewGeminiAgent(""))
}
