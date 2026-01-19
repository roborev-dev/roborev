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
	agenticMode := AllowUnsafeAgents()
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
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start gemini: %w", err)
	}

	// Parse stream-json output
	result, parseErr := a.parseStreamJSON(stdoutPipe, output)

	if waitErr := cmd.Wait(); waitErr != nil {
		if parseErr != nil {
			return "", fmt.Errorf("gemini failed: %w (parse error: %v)\nstderr: %s", waitErr, parseErr, stderr.String())
		}
		return "", fmt.Errorf("gemini failed: %w\nstderr: %s", waitErr, stderr.String())
	}

	if parseErr != nil {
		return "", parseErr
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
func (a *GeminiAgent) parseStreamJSON(r io.Reader, output io.Writer) (string, error) {
	br := bufio.NewReader(r)

	var lastResult string
	var assistantMessages []string
	var validEventsParsed bool

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line != "" {
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

	// If no valid events were parsed, the output might be plain text (older gemini version)
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

	return "", nil
}

func init() {
	Register(NewGeminiAgent(""))
}
