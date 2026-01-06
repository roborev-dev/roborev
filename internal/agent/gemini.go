package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// GeminiAgent runs code reviews using the Gemini CLI
type GeminiAgent struct {
	Command string // The gemini command to run (default: "gemini")
}

// NewGeminiAgent creates a new Gemini agent
func NewGeminiAgent(command string) *GeminiAgent {
	if command == "" {
		command = "gemini"
	}
	return &GeminiAgent{Command: command}
}

func (a *GeminiAgent) Name() string {
	return "gemini"
}

func (a *GeminiAgent) CommandName() string {
	return a.Command
}

func (a *GeminiAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string) (string, error) {
	// Use gemini with -p for non-interactive mode
	// Output goes to stdout
	args := []string{
		"-p", prompt,
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gemini failed: %w\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if len(output) == 0 {
		return "No review output generated", nil
	}

	return output, nil
}

func init() {
	Register(NewGeminiAgent(""))
}
