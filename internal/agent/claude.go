package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// ClaudeAgent runs code reviews using Claude Code CLI
type ClaudeAgent struct {
	Command   string         // The claude command to run (default: "claude")
	Model     string         // Model to use (e.g., "opus", "sonnet", or full name)
	Reasoning ReasoningLevel // Reasoning level (for future extended thinking support)
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
}

const claudeDangerousFlag = "--dangerously-skip-permissions"

var claudeDangerousSupport sync.Map

// NewClaudeAgent creates a new Claude Code agent
func NewClaudeAgent(command string) *ClaudeAgent {
	if command == "" {
		command = "claude"
	}
	return &ClaudeAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the model preserved (reasoning not yet supported).
func (a *ClaudeAgent) WithReasoning(level ReasoningLevel) Agent {
	return &ClaudeAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *ClaudeAgent) WithAgentic(agentic bool) Agent {
	return &ClaudeAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *ClaudeAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &ClaudeAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

func (a *ClaudeAgent) Name() string {
	return "claude-code"
}

func (a *ClaudeAgent) CommandName() string {
	return a.Command
}

func (a *ClaudeAgent) CommandLine() string {
	agenticMode := a.Agentic || AllowUnsafeAgents()
	args := a.buildArgs(agenticMode)
	return a.Command + " " + strings.Join(args, " ")
}

func (a *ClaudeAgent) buildArgs(agenticMode bool) []string {
	// Always use stdin piping + stream-json for non-interactive execution
	// (following claude-code-action pattern from Anthropic)
	args := []string{"-p", "--verbose", "--output-format", "stream-json"}

	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}

	if agenticMode {
		// Agentic mode: Claude can use tools and make file changes
		args = append(args, claudeDangerousFlag)
		args = append(args, "--allowedTools", "Edit,MultiEdit,Write,Read,Glob,Grep,Bash")
	} else {
		// Review mode: read-only tools only (no Bash to prevent arbitrary command execution)
		args = append(args, "--allowedTools", "Read,Glob,Grep")
	}
	return args
}

func claudeSupportsDangerousFlag(ctx context.Context, command string) (bool, error) {
	if cached, ok := claudeDangerousSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), claudeDangerousFlag)
	if err != nil && !supported {
		return false, fmt.Errorf("check %s --help: %w: %s", command, err, output)
	}
	claudeDangerousSupport.Store(command, supported)
	return supported, nil
}

func (a *ClaudeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Use agentic mode if either per-job setting or global setting enables it
	agenticMode := a.Agentic || AllowUnsafeAgents()

	if agenticMode {
		supported, err := claudeSupportsDangerousFlag(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("claude does not support %s; upgrade claude or disable allow_unsafe_agents", claudeDangerousFlag)
		}
	}

	// Build args - always uses stdin piping + stream-json for non-interactive execution
	args := a.buildArgs(agenticMode)

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.WaitDelay = 5 * time.Second

	// Strip CLAUDECODE to prevent nested-session detection (#270),
	// and handle API key (configured key or subscription auth).
	stripKeys := []string{"ANTHROPIC_API_KEY", "CLAUDECODE"}
	if apiKey := AnthropicAPIKey(); apiKey != "" {
		cmd.Env = append(filterEnv(os.Environ(), stripKeys...), "ANTHROPIC_API_KEY="+apiKey)
	} else {
		cmd.Env = filterEnv(os.Environ(), stripKeys...)
	}
	// Suppress sounds from Claude Code (notification/completion sounds)
	cmd.Env = append(cmd.Env, "CLAUDE_NO_SOUND=1")

	var stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}
	cmd.Stderr = &stderr

	// Always pipe prompt via stdin (stream-json mode)
	cmd.Stdin = strings.NewReader(prompt)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	// Parse stream-json output
	result, err := parseStreamJSON(stdoutPipe, output)

	if waitErr := cmd.Wait(); waitErr != nil {
		// Build a detailed error including any partial output and stream errors
		var detail strings.Builder
		fmt.Fprintf(&detail, "%s failed", a.Name())
		if err != nil {
			fmt.Fprintf(&detail, "\nstream: %v", err)
		}
		if s := stderr.String(); s != "" {
			fmt.Fprintf(&detail, "\nstderr: %s", s)
		}
		if result != "" {
			// Truncate partial output to keep error messages readable
			partial := result
			if len(partial) > 500 {
				partial = partial[:500] + "..."
			}
			fmt.Fprintf(&detail, "\npartial output: %s", partial)
		}
		return "", fmt.Errorf("%s: %w", detail.String(), waitErr)
	}

	if err != nil {
		return "", err
	}

	if result == "" {
		return "No review output generated", nil
	}

	return result, nil
}

// claudeStreamMessage represents a message in Claude's stream-json output format
type claudeStreamMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Message struct {
		Content json.RawMessage `json:"content,omitempty"`
	} `json:"message,omitempty"`
	Result string `json:"result,omitempty"`
	Error  struct {
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}

// extractContentText extracts only the text that appears after the last tool-use
// block in a Claude message content field. Content can be a plain string or an
// array of content blocks (e.g. [{"type":"text","text":"..."}]).
func extractContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first (simple format)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// Try array of content blocks (real Claude Code format)
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		texts := newTrailingReviewText()
		for _, b := range blocks {
			switch b.Type {
			case "text":
				texts.Add(b.Text)
			case "tool_use", "tool_result":
				texts.ResetAfterTool()
			}
		}
		return texts.Join("\n")
	}
	return ""
}

// parseStreamJSON parses Claude's stream-json output and extracts the final result.
// Uses bufio.Reader.ReadString to read lines without buffer size limits.
// On success, returns (result, nil). On failure, returns (partialOutput, error)
// where partialOutput contains any assistant messages collected before the error.
func parseStreamJSON(r io.Reader, output io.Writer) (string, error) {
	br := bufio.NewReader(r)

	var lastResult string
	assistantMessages := newTrailingReviewText()
	var errorMessages []string
	var validEventsParsed bool

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read stream: %w", err)
		}

		// Process line even if EOF (might have trailing content without newline)
		line = strings.TrimSpace(line)
		if line != "" {
			// Stream raw line to the writer for progress visibility
			if sw := newSyncWriter(output); sw != nil {
				_, _ = sw.Write([]byte(line + "\n"))
			}

			var msg claudeStreamMessage
			if jsonErr := json.Unmarshal([]byte(line), &msg); jsonErr == nil {
				validEventsParsed = true

				// Collect assistant messages for the result.
				// Content can be a string or an array of content blocks.
				if msg.Type == "assistant" {
					if text := extractContentText(msg.Message.Content); text != "" {
						assistantMessages.Add(text)
					}
				}
				if msg.Type == "tool_use" || msg.Type == "tool_result" {
					// Only the trailing post-tool assistant segment is treated
					// as the review body.
					assistantMessages.ResetAfterTool()
				}

				// The final result message contains the summary.
				// When is_error is set, the result event signals a
				// failure (auth, API, quota, etc.) — capture the
				// error details instead of treating it as output.
				if msg.Type == "result" {
					if msg.IsError {
						errMsg := "review returned error"
						if msg.Error.Message != "" {
							errMsg = msg.Error.Message
						} else if msg.Result != "" {
							errMsg = msg.Result
						}
						errorMessages = append(errorMessages, errMsg)
					} else if msg.Result != "" {
						lastResult = msg.Result
					}
				}

				// Capture standalone error events from Claude Code
				if msg.Type == "error" && msg.Error.Message != "" {
					errorMessages = append(errorMessages, msg.Error.Message)
				}
			}
			// Skip malformed JSON lines silently
		}

		if err == io.EOF {
			break
		}
	}

	// Error if we didn't parse any valid events
	if !validEventsParsed {
		return "", fmt.Errorf("no valid stream-json events parsed from output")
	}

	// Build partial output for error context
	partial := assistantMessages.Join("\n")

	// If error events were received but we got no result, report them with any partial output
	if len(errorMessages) > 0 && lastResult == "" {
		return partial, fmt.Errorf("stream errors: %s", strings.Join(errorMessages, "; "))
	}

	// Prefer the result field if present, otherwise join assistant messages
	if lastResult != "" {
		return lastResult, nil
	}
	if result := assistantMessages.Join("\n"); result != "" {
		return result, nil
	}

	return "", nil
}

// filterEnv returns a copy of env with the specified key removed
func filterEnv(env []string, keys ...string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		strip := slices.Contains(keys, k)
		if !strip {
			result = append(result, e)
		}
	}
	return result
}

func init() {
	Register(NewClaudeAgent(""))
}
