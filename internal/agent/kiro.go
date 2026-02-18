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

// KiroAgent runs code reviews using the Kiro CLI (kiro-cli)
type KiroAgent struct {
	Command   string         // The kiro-cli command to run (default: "kiro-cli")
	Reasoning ReasoningLevel // Reasoning level (stored; kiro-cli has no reasoning flag)
	Agentic   bool           // Whether agentic mode is enabled (uses --trust-all-tools)
}

// NewKiroAgent creates a new Kiro agent with standard reasoning
func NewKiroAgent(command string) *KiroAgent {
	if command == "" {
		command = "kiro-cli"
	}
	return &KiroAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy with the reasoning level stored.
// kiro-cli has no reasoning flag; callers can map reasoning to agent selection instead.
func (a *KiroAgent) WithReasoning(level ReasoningLevel) Agent {
	return &KiroAgent{Command: a.Command, Reasoning: level, Agentic: a.Agentic}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
// In agentic mode, --trust-all-tools is passed so kiro can use tools without confirmation.
func (a *KiroAgent) WithAgentic(agentic bool) Agent {
	return &KiroAgent{Command: a.Command, Reasoning: a.Reasoning, Agentic: agentic}
}

// WithModel returns the agent unchanged; kiro-cli does not expose a --model CLI flag.
func (a *KiroAgent) WithModel(model string) Agent {
	return a
}

func (a *KiroAgent) Name() string {
	return "kiro"
}

func (a *KiroAgent) CommandName() string {
	return a.Command
}

func (a *KiroAgent) buildArgs(agenticMode bool) []string {
	args := []string{"chat", "--no-interactive"}
	if agenticMode {
		args = append(args, "--trust-all-tools")
	}
	return args
}

func (a *KiroAgent) CommandLine() string {
	agenticMode := a.Agentic || AllowUnsafeAgents()
	args := a.buildArgs(agenticMode)
	return a.Command + " " + strings.Join(args, " ") + " <prompt>"
}

func (a *KiroAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	agenticMode := a.Agentic || AllowUnsafeAgents()

	// kiro-cli chat --no-interactive [--trust-all-tools] <prompt>
	// The prompt is passed as a positional argument (kiro-cli does not read from stdin).
	args := a.buildArgs(agenticMode)
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Env = os.Environ()
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	if sw := newSyncWriter(output); sw != nil {
		cmd.Stdout = io.MultiWriter(&stdout, sw)
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("kiro failed: %w\nstderr: %s", err, stderr.String())
	}

	result := stdout.String()
	if len(result) == 0 {
		return "No review output generated", nil
	}
	return result, nil
}

func init() {
	Register(NewKiroAgent(""))
}
