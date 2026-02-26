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

func (a *KiloAgent) buildArgs() []string {
	args := []string{"run", "--format", "json"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if a.Agentic || AllowUnsafeAgents() {
		args = append(args, "--auto")
	}
	if variant := a.kiloVariant(); variant != "" {
		args = append(args, "--variant", variant)
	}
	return args
}

func (a *KiloAgent) CommandLine() string {
	return a.Command + " " + strings.Join(a.buildArgs(), " ")
}

// Review runs kilo with --format json and parses the JSONL stream
// (same envelope as opencode: {"type":"...","part":{"type":"text","text":"..."}}).
func (a *KiloAgent) Review(
	ctx context.Context,
	repoPath, commitSHA, prompt string,
	output io.Writer,
) (string, error) {
	cmd := exec.CommandContext(ctx, a.Command, a.buildArgs()...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)

	sw := newSyncWriter(output)

	var stderrBuf bytes.Buffer
	if sw != nil {
		cmd.Stderr = io.MultiWriter(&stderrBuf, sw)
	} else {
		cmd.Stderr = &stderrBuf
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start kilo: %w", err)
	}
	defer stdoutPipe.Close()

	// Tee raw stdout so we can include it in error diagnostics
	// when stderr is empty (some CLIs print errors to stdout).
	var stdoutRaw bytes.Buffer
	tee := io.TeeReader(stdoutPipe, &stdoutRaw)

	result, parseErr := parseOpenCodeJSON(tee, sw)

	// Drain remaining stdout so the subprocess can finish writing
	// and exit. Without this, cmd.Wait() can deadlock if the
	// parser returned early (e.g., read error) while the process
	// is still writing to the pipe.
	_, _ = io.Copy(io.Discard, stdoutPipe)

	if waitErr := cmd.Wait(); waitErr != nil {
		var detail strings.Builder
		fmt.Fprintf(&detail, "kilo failed")
		if parseErr != nil {
			fmt.Fprintf(&detail, "\nstream: %v", parseErr)
		}
		errText := stripTerminalControls(stderrBuf.String())
		if errText != "" {
			fmt.Fprintf(&detail, "\nstderr: %s", errText)
		} else if raw := stripTerminalControls(
			stdoutRaw.String(),
		); raw != "" {
			fmt.Fprintf(&detail, "\noutput: %s", raw)
		}
		if result != "" {
			partial := result
			if len(partial) > 500 {
				partial = partial[:500] + "..."
			}
			fmt.Fprintf(
				&detail, "\npartial output: %s", partial,
			)
		}
		return "", fmt.Errorf("%s: %w", detail.String(), waitErr)
	}

	if parseErr != nil {
		return result, parseErr
	}

	if result == "" {
		// No review text parsed. If stderr has content, the
		// agent likely printed an error (e.g. model not found)
		// that should be surfaced instead of a generic message.
		errOut := stripTerminalControls(stderrBuf.String())
		if errOut = strings.TrimSpace(errOut); errOut != "" {
			return "", fmt.Errorf(
				"kilo produced no output: %s", errOut,
			)
		}
		return "No review output generated", nil
	}
	return result, nil
}

func init() {
	Register(NewKiloAgent(""))
}
