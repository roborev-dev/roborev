package agent

import (
	"bytes"
	"context"
	"encoding/json"
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
	SessionID string         // Existing session ID to resume
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
		SessionID: a.SessionID,
	}
}

func (a *KiloAgent) WithAgentic(agentic bool) Agent {
	return &KiloAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
		SessionID: a.SessionID,
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
		SessionID: a.SessionID,
	}
}

// WithSessionID returns a copy of the agent configured to resume a prior session.
func (a *KiloAgent) WithSessionID(sessionID string) Agent {
	return &KiloAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
		SessionID: sanitizedResumeSessionID(sessionID),
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
	case ReasoningMaximum, ReasoningThorough:
		return "high"
	case ReasoningFast:
		return "minimal"
	default:
		return "" // use kilo default
	}
}

func (a *KiloAgent) buildArgs() []string {
	sessionID := sanitizedResumeSessionID(a.SessionID)
	args := []string{"run", "--format", "json"}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
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
	tracker := configureSubprocess(cmd)

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
	stopClosingPipe := closeOnContextDone(ctx, stdoutPipe)
	defer stopClosingPipe()

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
	_, _ = io.Copy(&stdoutRaw, stdoutPipe)

	if waitErr := cmd.Wait(); waitErr != nil {
		if ctxErr := contextProcessError(ctx, tracker, waitErr, parseErr); ctxErr != nil {
			return "", ctxErr
		}
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

	if ctxErr := contextProcessError(ctx, tracker, nil, parseErr); ctxErr != nil {
		return "", ctxErr
	}

	if parseErr != nil {
		return result, parseErr
	}

	if result == "" {
		// No review text parsed. Surface stderr or non-JSON
		// stdout so the actual error is visible instead of
		// a generic "no output" message.
		errOut := stripTerminalControls(stderrBuf.String())
		if errOut = strings.TrimSpace(errOut); errOut != "" {
			return "", fmt.Errorf(
				"kilo produced no output: %s", errOut,
			)
		}
		if raw := strings.TrimSpace(
			stripTerminalControls(stdoutRaw.String()),
		); raw != "" && hasNonJSONLine(raw) {
			return "", fmt.Errorf(
				"kilo produced no output: %s", raw,
			)
		}
		return "No review output generated", nil
	}
	return result, nil
}

// hasNonJSONLine returns true if s contains any non-empty line
// that is not valid JSON. Used to distinguish plain-text error
// output from valid JSONL with no text events.
func hasNonJSONLine(s string) bool {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			return true
		}
	}
	return false
}

func init() {
	Register(NewKiloAgent(""))
}
