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

// WithReasoning returns the agent unchanged (reasoning not supported).
func (a *OpenCodeAgent) WithReasoning(level ReasoningLevel) Agent {
	return a
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

// filterOpencodeToolCallLines removes OpenCode tool-call JSON lines from stdout.
// opencode run --format default streams {"name":"...","arguments":{...}} for tool
// requests; roborev needs only the final text. We drop lines that are solely such objects.
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
	args = append(args, prompt)

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

	result := filterOpencodeToolCallLines(stdout.String())
	if len(result) == 0 {
		return "No review output generated", nil
	}
	return result, nil
}

func init() {
	Register(NewOpenCodeAgent(""))
}
