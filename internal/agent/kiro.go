package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// maxPromptArgLen is a conservative limit for passing prompts as
// CLI arguments. macOS ARG_MAX is ~1 MB; we leave headroom for
// the command name, flags, and environment.
const maxPromptArgLen = 512 * 1024

// stripKiroOutput removes Kiro's UI chrome (logo, tip box, model line, timing footer)
// and terminal control sequences, returning only the review text.
func stripKiroOutput(raw string) string {
	s := stripTerminalControls(raw)

	// Kiro prepends a splash screen and tip box before the response.
	// The "> " prompt marker appears near the top; limit the search to avoid
	// mistaking markdown blockquotes in review content for the start marker.
	lines := strings.Split(s, "\n")
	limit := min(30, len(lines))
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

	// Strip the prompt marker from the first content line.
	// A bare ">" (no trailing content) is skipped entirely.
	if lines[start] == ">" {
		start++
		if start >= len(lines) {
			return ""
		}
	} else {
		lines[start] = strings.TrimPrefix(lines[start], "> ")
	}

	// Drop the timing footer ("▸ Time: Xs") and anything after it.
	// Only scan the last 5 lines to avoid truncating review content
	// that happens to contain "▸ Time:" in a code snippet.
	end := len(lines)
	scanFrom := max(start, end-5)
	for i := scanFrom; i < end; i++ {
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
	if len(prompt) > maxPromptArgLen {
		return "", fmt.Errorf(
			"prompt too large for kiro-cli argv (%d bytes, max %d)",
			len(prompt), maxPromptArgLen,
		)
	}

	agenticMode := a.Agentic || AllowUnsafeAgents()

	// kiro-cli chat --no-interactive [--trust-all-tools] <prompt>
	// The prompt is passed as a positional argument
	// (kiro-cli does not support stdin).
	args := a.buildArgs(agenticMode)
	args = append(args, "--", prompt)

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Env = os.Environ()
	cmd.WaitDelay = 5 * time.Second

	// kiro-cli emits ANSI terminal escape codes that are not
	// suitable for streaming. Capture and return stripped text.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"kiro failed: %w\nstderr: %s",
			err, stderr.String(),
		)
	}

	result := stripKiroOutput(stdout.String())
	if len(result) == 0 {
		// Some CLI tools emit review text on stderr.
		result = stripKiroOutput(stderr.String())
	}
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
