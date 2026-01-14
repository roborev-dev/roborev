package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/mattn/go-isatty"
)

// ClaudeAgent runs code reviews using Claude Code CLI
type ClaudeAgent struct {
	Command   string         // The claude command to run (default: "claude")
	Reasoning ReasoningLevel // Reasoning level (for future extended thinking support)
}

const claudeDangerousFlag = "--dangerously-skip-permissions"

var claudeDangerousSupport sync.Map

// NewClaudeAgent creates a new Claude Code agent
func NewClaudeAgent(command string) *ClaudeAgent {
	if command == "" {
		command = "claude"
	}
	return &ClaudeAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns the agent unchanged (reasoning not supported).
func (a *ClaudeAgent) WithReasoning(level ReasoningLevel) Agent {
	return a
}

func (a *ClaudeAgent) Name() string {
	return "claude-code"
}

func (a *ClaudeAgent) CommandName() string {
	return a.Command
}

func (a *ClaudeAgent) buildArgs(prompt string) []string {
	args := []string{}
	if AllowUnsafeAgents() {
		args = append(args, claudeDangerousFlag)
	}
	args = append(args,
		"--print",
		"-p", prompt,
	)
	return args
}

func claudeSupportsDangerousFlag(ctx context.Context, command string) (bool, error) {
	if cached, ok := claudeDangerousSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), claudeDangerousFlag)
	if err != nil && !supported {
		return false, fmt.Errorf("check %s --help: %w: %s", command, err, output)
	}
	claudeDangerousSupport.Store(command, supported)
	return supported, nil
}

func (a *ClaudeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	if !AllowUnsafeAgents() && !isatty.IsTerminal(os.Stdin.Fd()) {
		return "", fmt.Errorf("claude requires a TTY when allow_unsafe_agents is disabled")
	}
	if AllowUnsafeAgents() {
		supported, err := claudeSupportsDangerousFlag(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("claude does not support %s; upgrade claude or disable allow_unsafe_agents", claudeDangerousFlag)
		}
	}

	// Use claude CLI in print mode (non-interactive)
	// --print outputs the response without the interactive TUI
	args := a.buildArgs(prompt)

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
		return "", fmt.Errorf("claude failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

func init() {
	Register(NewClaudeAgent(""))
}
