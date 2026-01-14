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

// CodexAgent runs code reviews using the Codex CLI
type CodexAgent struct {
	Command   string         // The codex command to run (default: "codex")
	Reasoning ReasoningLevel // Reasoning level for the agent
}

const codexDangerousFlag = "--dangerously-bypass-approvals-and-sandbox"
const codexAutoApproveFlag = "--full-auto"

var codexDangerousSupport sync.Map
var codexAutoApproveSupport sync.Map

// NewCodexAgent creates a new Codex agent with standard reasoning
func NewCodexAgent(command string) *CodexAgent {
	if command == "" {
		command = "codex"
	}
	return &CodexAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the specified reasoning level
func (a *CodexAgent) WithReasoning(level ReasoningLevel) Agent {
	return &CodexAgent{Command: a.Command, Reasoning: level}
}

// codexReasoningEffort maps ReasoningLevel to codex-specific effort values
func (a *CodexAgent) codexReasoningEffort() string {
	switch a.Reasoning {
	case ReasoningThorough:
		return "high"
	case ReasoningFast:
		return "low"
	default:
		return "" // use codex default
	}
}

func (a *CodexAgent) Name() string {
	return "codex"
}

func (a *CodexAgent) CommandName() string {
	return a.Command
}

func (a *CodexAgent) buildArgs(repoPath, outputFile, prompt string) []string {
	args := []string{
		"exec",
	}
	if AllowUnsafeAgents() {
		args = append(args, codexDangerousFlag)
	}
	args = append(args,
		"-C", repoPath,
		"-o", outputFile,
	)
	if effort := a.codexReasoningEffort(); effort != "" {
		args = append(args, "-c", fmt.Sprintf(`model_reasoning_effort="%s"`, effort))
	}
	args = append(args, prompt)
	return args
}

func codexSupportsDangerousFlag(ctx context.Context, command string) (bool, error) {
	if cached, ok := codexDangerousSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), codexDangerousFlag)
	if err != nil && !supported {
		return false, fmt.Errorf("check %s --help: %w: %s", command, err, output)
	}
	codexDangerousSupport.Store(command, supported)
	return supported, nil
}

func codexSupportsAutoApproveFlag(ctx context.Context, command string) (bool, error) {
	if cached, ok := codexAutoApproveSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), codexAutoApproveFlag)
	if err != nil && !supported {
		return false, fmt.Errorf("check %s --help: %w: %s", command, err, output)
	}
	codexAutoApproveSupport.Store(command, supported)
	return supported, nil
}

func (a *CodexAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Create unique temp file for output
	tmpFile, err := os.CreateTemp("", "roborev-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	outputFile := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(outputFile)

	if AllowUnsafeAgents() {
		supported, err := codexSupportsDangerousFlag(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("codex does not support %s; upgrade codex or disable allow_unsafe_agents", codexDangerousFlag)
		}
	}

	// Use codex exec with output capture
	// The prompt is constructed by the prompt builder with full context
	args := a.buildArgs(repoPath, outputFile, prompt)
	if !AllowUnsafeAgents() && !isatty.IsTerminal(os.Stdin.Fd()) {
		supported, err := codexSupportsAutoApproveFlag(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("codex requires a TTY or %s; rerun with --allow-unsafe-agents or upgrade codex", codexAutoApproveFlag)
		}
		if len(args) > 0 && args[0] == "exec" {
			args = append(args[:1], append([]string{codexAutoApproveFlag}, args[1:]...)...)
		} else {
			args = append([]string{codexAutoApproveFlag}, args...)
		}
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stderr bytes.Buffer
	if sw := newSyncWriter(output); sw != nil {
		// Stream stderr (progress info) to output
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("codex failed: %w\nstderr: %s", err, stderr.String())
	}

	// Read the output file
	result, err := os.ReadFile(outputFile)
	if err != nil {
		return "", fmt.Errorf("read output: %w", err)
	}

	if len(result) == 0 {
		return "No review output generated", nil
	}

	return string(result), nil
}

func init() {
	Register(NewCodexAgent(""))
}
