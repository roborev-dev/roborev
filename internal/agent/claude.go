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

	// Handle API key: use configured key if set, otherwise filter out env var
	// to ensure Claude uses subscription auth instead of unexpected API charges
	if apiKey := AnthropicAPIKey(); apiKey != "" {
		// Use explicitly configured API key from roborev config
		cmd.Env = append(filterEnv(os.Environ(), "ANTHROPIC_API_KEY"), "ANTHROPIC_API_KEY="+apiKey)
	} else {
		// Clear env var so Claude uses subscription auth
		cmd.Env = filterEnv(os.Environ(), "ANTHROPIC_API_KEY")
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
	result, err := a.parseStreamJSON(stdoutPipe, output)

	if waitErr := cmd.Wait(); waitErr != nil {
		if err != nil {
			return "", fmt.Errorf("claude failed: %w (parse error: %v)\nstderr: %s", waitErr, err, stderr.String())
		}
		return "", fmt.Errorf("claude failed: %w\nstderr: %s", waitErr, stderr.String())
	}

	if err != nil {
		return "", err
	}

	return result, nil
}

// claudeStreamMessage represents a message in Claude's stream-json output format
type claudeStreamMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Message struct {
		Content string `json:"content,omitempty"`
	} `json:"message,omitempty"`
	Result string `json:"result,omitempty"`
}

// parseStreamJSON parses Claude's stream-json output and extracts the final result.
// Uses bufio.Reader.ReadString to read lines without buffer size limits.
func (a *ClaudeAgent) parseStreamJSON(r io.Reader, output io.Writer) (string, error) {
	br := bufio.NewReader(r)

	var lastResult string
	var assistantMessages []string
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
				sw.Write([]byte(line + "\n"))
			}

			var msg claudeStreamMessage
			if jsonErr := json.Unmarshal([]byte(line), &msg); jsonErr == nil {
				validEventsParsed = true

				// Collect assistant messages for the result
				if msg.Type == "assistant" && msg.Message.Content != "" {
					assistantMessages = append(assistantMessages, msg.Message.Content)
				}

				// The final result message contains the summary
				if msg.Type == "result" && msg.Result != "" {
					lastResult = msg.Result
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

	// Prefer the result field if present, otherwise join assistant messages
	if lastResult != "" {
		return lastResult, nil
	}
	if len(assistantMessages) > 0 {
		return strings.Join(assistantMessages, "\n"), nil
	}

	// Valid events were parsed but no result or assistant content found
	// This is not an error - Claude might have used tools without text output
	return "", nil
}

// filterEnv returns a copy of env with the specified key removed
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}

func init() {
	Register(NewClaudeAgent(""))
}
