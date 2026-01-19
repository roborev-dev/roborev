package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// GeminiAgent runs code reviews using the Gemini CLI
type GeminiAgent struct {
	Command   string         // The gemini command to run (default: "gemini")
	Reasoning ReasoningLevel // Reasoning level (for future support)
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
}

// NewGeminiAgent creates a new Gemini agent
func NewGeminiAgent(command string) *GeminiAgent {
	if command == "" {
		command = "gemini"
	}
	return &GeminiAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns the agent unchanged (reasoning not supported).
func (a *GeminiAgent) WithReasoning(level ReasoningLevel) Agent {
	return a
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *GeminiAgent) WithAgentic(agentic bool) Agent {
	return &GeminiAgent{
		Command:   a.Command,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

func (a *GeminiAgent) Name() string {
	return "gemini"
}

func (a *GeminiAgent) CommandName() string {
	return a.Command
}

func (a *GeminiAgent) buildArgs(agenticMode bool) []string {
	// Use stream-json output for parsing, prompt via stdin
	args := []string{"--output-format", "stream-json"}

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

	// Pipe prompt via stdin
	cmd.Stdin = strings.NewReader(prompt)

	var stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}
	// Tee stderr to output writer for live error visibility
	if sw := newSyncWriter(output); sw != nil {
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start gemini: %w", err)
	}

	// Parse stream-json output, capturing raw stdout as fallback
	result, rawOutput, parseErr := a.parseStreamJSON(stdoutPipe, output)

	if waitErr := cmd.Wait(); waitErr != nil {
		if parseErr != nil {
			return "", fmt.Errorf("gemini failed: %w (parse error: %v)\nstderr: %s", waitErr, parseErr, stderr.String())
		}
		return "", fmt.Errorf("gemini failed: %w\nstderr: %s", waitErr, stderr.String())
	}

	// If stream-json parsing failed but we have raw output, use it as fallback
	if parseErr != nil && rawOutput != "" {
		return rawOutput, nil
	}
	if parseErr != nil {
		return "", parseErr
	}

	if result == "" {
		return "No review output generated", nil
	}

	return result, nil
}

// geminiStreamMessage represents a message in Gemini's stream-json output format
type geminiStreamMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Message struct {
		Content string `json:"content,omitempty"`
	} `json:"message,omitempty"`
	Result string `json:"result,omitempty"`
}

// parseStreamJSON parses Gemini's stream-json output and extracts the final result.
// Returns (result, rawOutput, error) where rawOutput is the raw stdout for fallback use.
func (a *GeminiAgent) parseStreamJSON(r io.Reader, output io.Writer) (string, string, error) {
	br := bufio.NewReader(r)

	var lastResult string
	var assistantMessages []string
	var rawLines []string
	var validEventsParsed bool

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", "", fmt.Errorf("read stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line != "" {
			// Collect raw output for potential fallback
			rawLines = append(rawLines, line)

			// Stream raw line to the writer for progress visibility
			if sw := newSyncWriter(output); sw != nil {
				sw.Write([]byte(line + "\n"))
			}

			var msg geminiStreamMessage
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
		}

		if err == io.EOF {
			break
		}
	}

	rawOutput := strings.Join(rawLines, "\n")

	// If no valid events were parsed, return error with raw output for fallback
	if !validEventsParsed {
		return "", rawOutput, fmt.Errorf("no valid stream-json events parsed from output")
	}

	// Prefer the result field if present, otherwise join assistant messages
	if lastResult != "" {
		return lastResult, rawOutput, nil
	}
	if len(assistantMessages) > 0 {
		return strings.Join(assistantMessages, "\n"), rawOutput, nil
	}

	return "", rawOutput, nil
}

func init() {
	Register(NewGeminiAgent(""))
}
