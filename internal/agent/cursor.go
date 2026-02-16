package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CursorAgent runs code reviews using the Cursor agent CLI
type CursorAgent struct {
	Command   string         // The agent command to run (default: "agent")
	Model     string         // Model to use
	Reasoning ReasoningLevel // Reasoning level
	Agentic   bool           // Whether agentic mode is enabled
}

// NewCursorAgent creates a new Cursor agent
func NewCursorAgent(command string) *CursorAgent {
	if command == "" {
		command = "agent"
	}
	return &CursorAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy with the reasoning level stored.
// The agent CLI has no reasoning flag; callers can map reasoning to model selection instead.
func (a *CursorAgent) WithReasoning(level ReasoningLevel) Agent {
	return &CursorAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

func (a *CursorAgent) WithAgentic(agentic bool) Agent {
	return &CursorAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

func (a *CursorAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &CursorAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

func (a *CursorAgent) Name() string {
	return "cursor"
}

func (a *CursorAgent) CommandName() string {
	return a.Command
}

func (a *CursorAgent) CommandLine() string {
	agenticMode := a.Agentic || AllowUnsafeAgents()
	// Show flags without the prompt (piped via stdin)
	args := []string{"-p", "--output-format", "stream-json"}
	model := a.Model
	if model == "" {
		model = "auto"
	}
	args = append(args, "--model", model)
	if agenticMode {
		args = append(args, "--force")
	} else {
		args = append(args, "--mode", "plan")
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *CursorAgent) buildArgs(agenticMode bool) []string {
	// -p enables non-interactive print mode (like Claude Code's -p flag)
	args := []string{"-p", "--output-format", "stream-json"}

	// Default to "auto" which lets the agent CLI pick a model based on the task
	model := a.Model
	if model == "" {
		model = "auto"
	}
	args = append(args, "--model", model)

	if agenticMode {
		args = append(args, "--force")
	} else {
		args = append(args, "--mode", "plan")
	}

	return args
}

func (a *CursorAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	agenticMode := a.Agentic || AllowUnsafeAgents()

	args := a.buildArgs(agenticMode)

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Env = os.Environ()
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader(prompt)

	var stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start cursor agent: %w", err)
	}

	// Reuse Claude's stream-json parser (same format)
	claude := &ClaudeAgent{}
	result, err := claude.parseStreamJSON(stdoutPipe, output)

	if waitErr := cmd.Wait(); waitErr != nil {
		if err != nil {
			return "", fmt.Errorf("cursor agent failed: %w (parse error: %v)\nstderr: %s", waitErr, err, stderr.String())
		}
		return "", fmt.Errorf("cursor agent failed: %w\nstderr: %s", waitErr, stderr.String())
	}

	if err != nil {
		return "", err
	}

	return result, nil
}

func init() {
	Register(NewCursorAgent(""))
}
