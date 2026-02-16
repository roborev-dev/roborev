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

// OpenCodeAgent runs code reviews using the OpenCode CLI
type OpenCodeAgent struct {
	Command   string         // The opencode command to run (default: "opencode")
	Model     string         // Model to use (provider/model format, e.g., "anthropic/claude-sonnet-4-20250514")
	Reasoning ReasoningLevel // Reasoning level (for future support)
	Agentic   bool           // Whether agentic mode is enabled (OpenCode auto-approves in non-interactive mode)
}

// NewOpenCodeAgent creates a new OpenCode agent
func NewOpenCodeAgent(command string) *OpenCodeAgent {
	if command == "" {
		command = "opencode"
	}
	return &OpenCodeAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the model preserved (reasoning not yet supported).
func (a *OpenCodeAgent) WithReasoning(level ReasoningLevel) Agent {
	return &OpenCodeAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
// Note: OpenCode's `run` command auto-approves all permissions in non-interactive mode,
// so agentic mode is effectively always enabled when running through roborev.
func (a *OpenCodeAgent) WithAgentic(agentic bool) Agent {
	return &OpenCodeAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *OpenCodeAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &OpenCodeAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

func (a *OpenCodeAgent) Name() string {
	return "opencode"
}

func (a *OpenCodeAgent) CommandName() string {
	return a.Command
}

// filterOpencodeToolCallLines removes LLM tool-call JSON lines that may appear in stdout.
// When LLM providers stream responses, raw tool calls in the standard format
// {"name":"...","arguments":{...}} (exactly 2 keys) may occasionally leak through
// to stdout. Normally OpenCode formats these with ANSI codes, but edge cases
// (streaming glitches, format failures) can expose the raw JSON.
// We filter lines matching this exact format while preserving legitimate JSON
// examples which would have additional keys.
func filterOpencodeToolCallLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if isOpencodeToolCallLine(line) {
			continue
		}
		out = append(out, line)
	}
	// Only trim trailing newlines to preserve leading indentation in code blocks
	return strings.TrimRight(strings.Join(out, "\n"), "\r\n")
}

func isOpencodeToolCallLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return false
	}
	// Tool calls have exactly "name" and "arguments" keys, nothing else.
	// This avoids stripping legitimate JSON examples that happen to include these keys.
	if len(m) != 2 {
		return false
	}
	_, hasName := m["name"]
	_, hasArgs := m["arguments"]
	return hasName && hasArgs
}

func (a *OpenCodeAgent) CommandLine() string {
	args := []string{"run", "--format", "default"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *OpenCodeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// OpenCode CLI supports a headless invocation via `opencode run [message..]`.
	// We run it from the repo root so it can use project context, and pass the full
	// roborev prompt as the message.
	//
	// Helpful reference:
	//   opencode --help
	//   opencode run --help
	args := []string{"run", "--format", "default"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
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
		// opencode sometimes prints failures/usage to stdout; include both streams.
		return "", fmt.Errorf(
			"opencode failed: %w\nstdout: %s\nstderr: %s",
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
	Register(NewOpenCodeAgent(""))
}
