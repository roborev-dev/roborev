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

	"github.com/mattn/go-isatty"
)

// ClaudeAgent runs code reviews using Claude Code CLI
type ClaudeAgent struct {
	Command   string         // The claude command to run (default: "claude")
	Reasoning ReasoningLevel // Reasoning level (for future extended thinking support)
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

// WithReasoning returns the agent unchanged (reasoning not supported).
func (a *ClaudeAgent) WithReasoning(level ReasoningLevel) Agent {
	return a
}

func (a *ClaudeAgent) Name() string {
	return "claude-code"
}

func (a *ClaudeAgent) CommandName() string {
	return a.Command
}

func (a *ClaudeAgent) buildArgs(agenticMode bool) []string {
	args := []string{}
	if AllowUnsafeAgents() {
		args = append(args, claudeDangerousFlag)
	}
	if agenticMode {
		// Agentic mode: Claude can use tools and make file changes
		// Use stream-json output format for non-interactive execution
		// (following claude-code-action pattern from Anthropic)
		// Prompt is piped via stdin, not passed as argument
		args = append(args, "-p", "--verbose", "--output-format", "stream-json")
	} else {
		// Print mode: one-shot text response, no tool use
		// Prompt is passed as positional argument
		args = append(args, "--print")
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
	agenticMode := AllowUnsafeAgents()

	if !agenticMode && !isatty.IsTerminal(os.Stdin.Fd()) {
		return "", fmt.Errorf("claude requires a TTY when allow_unsafe_agents is disabled")
	}
	if agenticMode {
		supported, err := claudeSupportsDangerousFlag(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("claude does not support %s; upgrade claude or disable allow_unsafe_agents", claudeDangerousFlag)
		}
	}

	// When AllowUnsafeAgents is true, run in agentic mode (can make file changes)
	// Otherwise, use --print mode for review-only operations
	args := a.buildArgs(agenticMode)

	// In non-agentic mode, pass prompt as positional argument
	// In agentic mode, prompt is piped via stdin (like claude-code-action)
	if !agenticMode {
		args = append(args, "-p", prompt)
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}
	cmd.Stderr = &stderr

	// In agentic mode, pipe prompt via stdin
	if agenticMode {
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			return "", fmt.Errorf("create stdin pipe: %w", err)
		}
		go func() {
			defer stdinPipe.Close()
			io.WriteString(stdinPipe, prompt)
		}()
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	// Parse output based on mode
	var result string
	if agenticMode {
		result, err = a.parseStreamJSON(stdoutPipe, output)
	} else {
		var buf bytes.Buffer
		if sw := newSyncWriter(output); sw != nil {
			io.Copy(io.MultiWriter(&buf, sw), stdoutPipe)
		} else {
			io.Copy(&buf, stdoutPipe)
		}
		result = buf.String()
	}

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

// parseStreamJSON parses Claude's stream-json output and extracts the final result
func (a *ClaudeAgent) parseStreamJSON(r io.Reader, output io.Writer) (string, error) {
	scanner := bufio.NewScanner(r)
	// Increase buffer size for large JSON lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var lastResult string
	var assistantMessages []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Stream raw output to the writer for progress visibility
		if sw := newSyncWriter(output); sw != nil {
			sw.Write([]byte(line + "\n"))
		}

		var msg claudeStreamMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Skip malformed lines
			continue
		}

		// Collect assistant messages for the result
		if msg.Type == "assistant" && msg.Message.Content != "" {
			assistantMessages = append(assistantMessages, msg.Message.Content)
		}

		// The final result message contains the summary
		if msg.Type == "result" {
			if msg.Result != "" {
				lastResult = msg.Result
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan output: %w", err)
	}

	// Prefer the result field if present, otherwise join assistant messages
	if lastResult != "" {
		return lastResult, nil
	}
	if len(assistantMessages) > 0 {
		return strings.Join(assistantMessages, "\n"), nil
	}

	return "", nil
}

func init() {
	Register(NewClaudeAgent(""))
}
