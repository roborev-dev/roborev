package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
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
	Model     string         // Model to use (e.g., "gemini-3.1-pro")
	Reasoning ReasoningLevel // Reasoning level (for future support)
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
}

// NewGeminiAgent creates a new Gemini agent
func NewGeminiAgent(command string) *GeminiAgent {
	if command == "" {
		command = "gemini"
	}
	return &GeminiAgent{Command: command, Model: "gemini-3.1-pro", Reasoning: ReasoningStandard}
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
	return a.buildArgsWithModel(a.Model, agenticMode)
}

func (a *GeminiAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	agenticMode := a.Agentic || AllowUnsafeAgents()
	args := a.buildArgs(agenticMode)

	result, stderrStr, err := a.runGemini(ctx, repoPath, prompt, args, output)
	if err != nil && a.Model != "" && isModelNotFoundError(stderrStr) {
		// Model name may be stale (Google renames frequently).
		// Retry without -m to let the Gemini CLI use its own default.
		log.Printf("gemini: model %q not found, retrying without -m flag", a.Model)
		noModelArgs := a.buildArgsWithModel("", agenticMode)
		result, _, err = a.runGemini(ctx, repoPath, prompt, noModelArgs, output)
	}
	return result, err
}

// buildArgsWithModel builds CLI args with an explicit model override
// (empty string omits the -m flag entirely).
func (a *GeminiAgent) buildArgsWithModel(model string, agenticMode bool) []string {
	args := []string{"--output-format", "stream-json"}

	if model != "" {
		args = append(args, "-m", model)
	}

	if agenticMode {
		args = append(args, "--yolo")
		args = append(args, "--allowed-tools", "Edit,Write,Read,Glob,Grep,Bash,Shell")
	} else {
		args = append(args, "--allowed-tools", "Read,Glob,Grep")
	}
	return args
}

// runGemini executes the Gemini CLI with the given args and returns
// the review result, captured stderr, and any error.
func (a *GeminiAgent) runGemini(ctx context.Context, repoPath, prompt string, args []string, output io.Writer) (string, string, error) {
	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	tracker := configureSubprocess(cmd)

	cmd.Stdin = strings.NewReader(prompt)

	sw := newSyncWriter(output)

	var stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("create stdout pipe: %w", err)
	}
	stopClosingPipe := closeOnContextDone(ctx, stdoutPipe)
	defer stopClosingPipe()
	if sw != nil {
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start gemini: %w", err)
	}

	parsed, parseErr := a.parseStreamJSON(stdoutPipe, sw)

	// Wait() is the synchronization point: all stderr writes are
	// guaranteed complete after it returns.
	waitErr := cmd.Wait()
	stderrStr := stderr.String()

	if waitErr != nil {
		if ctxErr := contextProcessError(ctx, tracker, waitErr, parseErr); ctxErr != nil {
			return "", stderrStr, ctxErr
		}
		if parseErr != nil {
			return "", stderrStr, fmt.Errorf("gemini failed: %w (parse error: %v)\nstderr: %s", waitErr, parseErr, truncateStderr(stderrStr))
		}
		return "", stderrStr, fmt.Errorf("gemini failed: %w\nstderr: %s", waitErr, truncateStderr(stderrStr))
	}

	if ctxErr := contextProcessError(ctx, tracker, nil, parseErr); ctxErr != nil {
		return "", stderrStr, ctxErr
	}

	if parseErr != nil {
		if errors.Is(parseErr, errNoStreamJSON) {
			return "", stderrStr, fmt.Errorf("gemini CLI must support --output-format stream-json; upgrade to latest version\nstderr: %s: %w", truncateStderr(stderrStr), errNoStreamJSON)
		}
		return "", stderrStr, parseErr
	}

	if parsed.result != "" {
		return parsed.result, stderrStr, nil
	}

	return "No review output generated", stderrStr, nil
}

// isModelNotFoundError returns true if stderr indicates the requested
// model does not exist. Google's API returns 404 with "model not found"
// or "is not found" messages when a model name is invalid or retired.
func isModelNotFoundError(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "model") &&
		(strings.Contains(lower, "not found") ||
			strings.Contains(lower, "is not found") ||
			strings.Contains(lower, "not_found"))
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
	assistantMessages := newTrailingReviewText()
	var validEventsParsed bool

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return parseResult{}, fmt.Errorf("read stream: %w", err)
		}

		// Stream line to the writer for progress visibility
		trimmed := strings.TrimSpace(line)
		if sw != nil && trimmed != "" {
			_, _ = sw.Write([]byte(trimmed + "\n"))
		}

		// Try to parse as JSON
		if trimmed != "" {
			var msg geminiStreamMessage
			if jsonErr := json.Unmarshal([]byte(trimmed), &msg); jsonErr == nil {
				validEventsParsed = true

				// Collect assistant messages for the result
				// Gemini format: type="message", role="assistant", content at top level
				if msg.Type == "message" && msg.Role == "assistant" && msg.Content != "" {
					assistantMessages.Add(msg.Content)
				}
				// Claude Code format: type="assistant", message.content nested
				if msg.Type == "assistant" && msg.Message.Content != "" {
					assistantMessages.Add(msg.Message.Content)
				}
				if msg.Type == "tool" || msg.Type == "tool_result" {
					// Treat pre-tool assistant text as provisional; only the
					// trailing post-tool segment becomes review output.
					assistantMessages.ResetAfterTool()
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
	if result := assistantMessages.Join("\n"); result != "" {
		return parseResult{result: result}, nil
	}

	return parseResult{}, nil
}

func init() {
	Register(NewGeminiAgent(""))
}
