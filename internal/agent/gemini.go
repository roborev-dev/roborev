package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// errNoStreamJSON indicates no valid stream-json events were parsed.
// Stream-json output is required; this error means the Gemini CLI may need to be upgraded.
var errNoStreamJSON = errors.New("no valid stream-json events parsed from output")

// maxStderrLen is the maximum number of bytes of stderr to include in error messages.
const maxStderrLen = 1024

// truncateStderr truncates stderr output to a reasonable size for error messages.
func truncateStderr(stderr string) string {
	if len(stderr) <= maxStderrLen {
		return stderr
	}
	return stderr[:maxStderrLen] + "... (truncated)"
}

// GeminiAgent runs code reviews using the Gemini CLI
type GeminiAgent struct {
	Command   string         // The gemini command to run (default: "gemini")
	Model     string         // Model to use (e.g., "gemini-3-pro-preview")
	Reasoning ReasoningLevel // Reasoning level (for future support)
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
}

// NewGeminiAgent creates a new Gemini agent
func NewGeminiAgent(command string) *GeminiAgent {
	if command == "" {
		command = "gemini"
	}
	return &GeminiAgent{Command: command, Model: "gemini-3-pro-preview", Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the model preserved (reasoning not yet supported).
func (a *GeminiAgent) WithReasoning(level ReasoningLevel) Agent {
	return &GeminiAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *GeminiAgent) WithAgentic(agentic bool) Agent {
	return &GeminiAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *GeminiAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &GeminiAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

func (a *GeminiAgent) Name() string {
	return "gemini"
}

func (a *GeminiAgent) CommandName() string {
	return a.Command
}

func (a *GeminiAgent) CommandLine() string {
	agenticMode := a.Agentic || AllowUnsafeAgents()
	args := a.buildArgs(agenticMode)
	return a.Command + " " + strings.Join(args, " ")
}

func (a *GeminiAgent) buildArgs(agenticMode bool) []string {
	// Use stream-json output for parsing, prompt via stdin
	args := []string{"--output-format", "stream-json"}

	if a.Model != "" {
		args = append(args, "-m", a.Model)
	}

	if agenticMode {
		// Agentic mode: auto-approve all actions, allow write tools
		args = append(args, "--yolo")
		args = append(args, "--allowed-tools", "Edit,Write,Read,Glob,Grep,Bash,Shell")
	} else {
		// Review mode: read-only tools only
		args = append(args, "--allowed-tools", "Read,Glob,Grep")
	}
	return args
}

func (a *GeminiAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Use agentic mode if either per-job setting or global setting enables it
	agenticMode := a.Agentic || AllowUnsafeAgents()
	args := a.buildArgs(agenticMode)

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.WaitDelay = 5 * time.Second

	// Pipe prompt via stdin
	cmd.Stdin = strings.NewReader(prompt)

	// Create one shared sync writer for thread-safe output
	sw := newSyncWriter(output)

	var stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}
	// Tee stderr to output writer for live error visibility
	if sw != nil {
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start gemini: %w", err)
	}

	// Parse stream-json output
	parsed, parseErr := a.parseStreamJSON(stdoutPipe, sw)

	if waitErr := cmd.Wait(); waitErr != nil {
		if parseErr != nil {
			return "", fmt.Errorf("gemini failed: %w (parse error: %v)\nstderr: %s", waitErr, parseErr, truncateStderr(stderr.String()))
		}
		return "", fmt.Errorf("gemini failed: %w\nstderr: %s", waitErr, truncateStderr(stderr.String()))
	}

	if parseErr != nil {
		if errors.Is(parseErr, errNoStreamJSON) {
			return "", fmt.Errorf("gemini CLI must support --output-format stream-json; upgrade to latest version\nstderr: %s: %w", truncateStderr(stderr.String()), errNoStreamJSON)
		}
		return "", parseErr
	}

	if parsed.result != "" {
		return parsed.result, nil
	}

	return "No review output generated", nil
}

// geminiStreamMessage represents a message in Gemini's stream-json output format
type geminiStreamMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	// Top-level fields for "message" type events
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	Delta   bool   `json:"delta,omitempty"`
	// Nested message field (older format / Claude Code compatibility)
	Message struct {
		Content string `json:"content,omitempty"`
	} `json:"message,omitempty"`
	// Result field for "result" type events
	Result string `json:"result,omitempty"`
}

// parseResult contains the parsed result from stream-json output
type parseResult struct {
	result string // The extracted result text
}

// parseStreamJSON parses Gemini's stream-json output and extracts the final result.
// Returns parseResult with the extracted content, or error on I/O or parse failure.
// The sw parameter is the shared sync writer for thread-safe output (may be nil).
func (a *GeminiAgent) parseStreamJSON(r io.Reader, sw *syncWriter) (parseResult, error) {
	br := bufio.NewReader(r)

	var lastResult string
	var assistantMessages []string
	var validEventsParsed bool

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return parseResult{}, fmt.Errorf("read stream: %w", err)
		}

		// Stream line to the writer for progress visibility
		trimmed := strings.TrimSpace(line)
		if sw != nil && trimmed != "" {
			sw.Write([]byte(trimmed + "\n"))
		}

		// Try to parse as JSON
		if trimmed != "" {
			var msg geminiStreamMessage
			if jsonErr := json.Unmarshal([]byte(trimmed), &msg); jsonErr == nil {
				validEventsParsed = true

				// Collect assistant messages for the result
				// Gemini format: type="message", role="assistant", content at top level
				if msg.Type == "message" && msg.Role == "assistant" && msg.Content != "" {
					assistantMessages = append(assistantMessages, msg.Content)
				}
				// Claude Code format: type="assistant", message.content nested
				if msg.Type == "assistant" && msg.Message.Content != "" {
					assistantMessages = append(assistantMessages, msg.Message.Content)
				}

				// The final result message contains the summary
				if msg.Type == "result" && msg.Result != "" {
					lastResult = msg.Result
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	// If no valid events were parsed, return error
	if !validEventsParsed {
		return parseResult{}, errNoStreamJSON
	}

	// Prefer the result field if present, otherwise join assistant messages
	if lastResult != "" {
		return parseResult{result: lastResult}, nil
	}
	if len(assistantMessages) > 0 {
		return parseResult{result: strings.Join(assistantMessages, "\n")}, nil
	}

	return parseResult{}, nil
}

func init() {
	Register(NewGeminiAgent(""))
}
