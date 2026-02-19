package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ansiEscape matches ANSI/VT100 terminal escape sequences.
var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[A-Za-z]|[^\[])`)

// stripKiroOutput removes Kiro's UI chrome (logo, tip box, model line, timing footer)
// and ANSI escape codes, returning only the review text.
func stripKiroOutput(raw string) string {
	s := ansiEscape.ReplaceAllString(raw, "")

	// Kiro prepends a splash screen and tip box before the response.
	// The "> " prompt marker appears near the top; limit the search to avoid
	// mistaking markdown blockquotes in review content for the start marker.
	lines := strings.Split(s, "\n")
	limit := 30
	if len(lines) < limit {
		limit = len(lines)
	}
	start := -1
	for i, line := range lines[:limit] {
		if strings.HasPrefix(line, "> ") || line == ">" {
			start = i
			break
		}
	}
	if start == -1 {
		return strings.TrimSpace(s)
	}

	// Strip the "> " chat-prompt prefix from the first content line.
	lines[start] = strings.TrimPrefix(lines[start], "> ")

	// Drop the timing footer ("▸ Time: Xs") and anything after it.
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "▸ Time:") {
			end = i
			break
		}
	}

	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

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

	// kiro-cli emits ANSI terminal escape codes that are not suitable for streaming
	// through roborev's output writer. Capture stdout/stderr and return stripped text.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("kiro failed: %w\nstderr: %s", err, stderr.String())
	}

	result := stripKiroOutput(stdout.String())
	if len(result) == 0 {
		return "No review output generated", nil
	}
	if sw := newSyncWriter(output); sw != nil {
		_, _ = sw.Write([]byte(result + "\n"))
	}
	return result, nil
}

func init() {
	Register(NewKiroAgent(""))
}
