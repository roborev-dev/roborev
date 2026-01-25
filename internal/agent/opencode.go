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
		Reasoning: a.Reasoning,
		Agentic:   agentic,
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
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isOpencodeToolCallLine(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &m); err != nil {
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
	args := []string{"run", "--format", "default", prompt}

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
