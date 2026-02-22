package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// KiloAgent runs code reviews using the Kilo CLI (https://kilo.ai).
// This implementation is intentionally cloned from OpenCodeAgent rather than
// sharing a base struct, because kilo may drift from opencode in the future.
type KiloAgent struct {
	Command   string         // The kilo command to run (default: "kilo")
	Model     string         // Model to use (provider/model format, e.g., "anthropic/claude-sonnet-4-20250514")
	Reasoning ReasoningLevel // Reasoning level mapped to --variant flag
	Agentic   bool           // Whether agentic mode is enabled (uses --auto)
}

// NewKiloAgent creates a new Kilo agent
func NewKiloAgent(command string) *KiloAgent {
	if command == "" {
		command = "kilo"
	}
	return &KiloAgent{Command: command, Reasoning: ReasoningStandard}
}

func (a *KiloAgent) WithReasoning(level ReasoningLevel) Agent {
	return &KiloAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

func (a *KiloAgent) WithAgentic(agentic bool) Agent {
	return &KiloAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

func (a *KiloAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &KiloAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

func (a *KiloAgent) Name() string {
	return "kilo"
}

func (a *KiloAgent) CommandName() string {
	return a.Command
}

// kiloVariant maps ReasoningLevel to kilo's --variant flag values
func (a *KiloAgent) kiloVariant() string {
	switch a.Reasoning {
	case ReasoningThorough:
		return "high"
	case ReasoningFast:
		return "minimal"
	default:
		return "" // use kilo default
	}
}

func (a *KiloAgent) CommandLine() string {
	args := []string{"run", "--format", "default"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	agenticMode := a.Agentic || AllowUnsafeAgents()
	if agenticMode {
		args = append(args, "--auto")
	}
	if variant := a.kiloVariant(); variant != "" {
		args = append(args, "--variant", variant)
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *KiloAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	args := []string{"run", "--format", "default"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	agenticMode := a.Agentic || AllowUnsafeAgents()
	if agenticMode {
		args = append(args, "--auto")
	}
	if variant := a.kiloVariant(); variant != "" {
		args = append(args, "--variant", variant)
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	if sw := newSyncWriter(output); sw != nil {
		cmd.Stdout = io.MultiWriter(&stdout, sw)
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"kilo failed: %w\nstdout: %s\nstderr: %s",
			err,
			stdout.String(),
			stderr.String(),
		)
	}

	result := filterOpencodeToolCallLines(stdout.String())
	if len(result) == 0 {
		return "No review output generated", nil
	}
	return result, nil
}

func init() {
	Register(NewKiloAgent(""))
}
