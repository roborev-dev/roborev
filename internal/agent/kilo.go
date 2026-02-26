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
	args := []string{"run", "--format", "default"}
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

func (a *KiloAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	cmd := exec.CommandContext(ctx, a.Command, a.buildArgs()...)
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
			"kilo failed: %w\nstderr: %s",
			err, stderrOrStdout(stderr, stdout),
		)
	}

	result := filterToolCallLines(stdout.String())
	if len(result) == 0 {
		// No review text on stdout. If stderr has content, the
		// agent likely printed an error (e.g. model not found)
		// that should be surfaced instead of a generic message.
		if errOut := strings.TrimSpace(stderr.String()); errOut != "" {
			return "", fmt.Errorf(
				"kilo produced no output: %s", errOut,
			)
		}
		return "No review output generated", nil
	}
	return result, nil
}

// stderrOrStdout returns stderr content if non-empty, otherwise
// stdout. Some CLIs (especially Node.js) print errors to stdout
// instead of stderr; this ensures the error is always captured.
func stderrOrStdout(stderr, stdout bytes.Buffer) string {
	if s := strings.TrimSpace(stderr.String()); s != "" {
		return s
	}
	return strings.TrimSpace(stdout.String())
}

// filterToolCallLines removes lines that look like JSON tool-call
// objects from plain-text CLI output. Kilo's --format default mode
// interleaves tool-call JSON ({"name":...,"arguments":...}) with
// human-readable review text; we keep only the review text.
func filterToolCallLines(s string) string {
	var kept []string
	for line := range strings.SplitSeq(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			kept = append(kept, line)
			continue
		}
		if isToolCallJSON(trimmed) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

// isToolCallJSON returns true if line matches the tool-call
// envelope exactly: a JSON object with only "name" (string) and
// "arguments" (object) keys. The caller must trim whitespace
// before passing the line.
func isToolCallJSON(line string) bool {
	if len(line) == 0 || line[0] != '{' {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &obj) != nil {
		return false
	}
	if len(obj) != 2 {
		return false
	}
	nameRaw, hasName := obj["name"]
	argsRaw, hasArgs := obj["arguments"]
	if !hasName || !hasArgs {
		return false
	}
	var name string
	if json.Unmarshal(nameRaw, &name) != nil {
		return false
	}
	trimmedArgs := bytes.TrimSpace(argsRaw)
	return len(trimmedArgs) > 0 && trimmedArgs[0] == '{'
}

func init() {
	Register(NewKiloAgent(""))
}
