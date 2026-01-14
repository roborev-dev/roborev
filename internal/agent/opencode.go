package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// OpenCodeAgent runs code reviews using the OpenCode CLI
type OpenCodeAgent struct {
	Command   string         // The opencode command to run (default: "opencode")
	Reasoning ReasoningLevel // Reasoning level (for future support)
}

// NewOpenCodeAgent creates a new OpenCode agent
func NewOpenCodeAgent(command string) *OpenCodeAgent {
	if command == "" {
		command = "opencode"
	}
	return &OpenCodeAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns the agent unchanged (reasoning not supported).
func (a *OpenCodeAgent) WithReasoning(level ReasoningLevel) Agent {
	return a
}

func (a *OpenCodeAgent) Name() string {
	return "opencode"
}

func (a *OpenCodeAgent) CommandName() string {
	return a.Command
}

func (a *OpenCodeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// OpenCode CLI supports a headless invocation via `opencode run [message..]`.
	// We run it from the repo root so it can use project context, and pass the full
	// roborev prompt as the message.
	//
	// Helpful reference:
	//   opencode --help
	//   opencode run --help
	args := []string{"run", "--format", "default", prompt}

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
		// opencode sometimes prints failures/usage to stdout; include both streams.
		return "", fmt.Errorf(
			"opencode failed: %w\nstdout: %s\nstderr: %s",
			err,
			stdout.String(),
			stderr.String(),
		)
	}

	result := stdout.String()
	if len(result) == 0 {
		return "No review output generated", nil
	}

	return result, nil
}

func init() {
	Register(NewOpenCodeAgent(""))
}
