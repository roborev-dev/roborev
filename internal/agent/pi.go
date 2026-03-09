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
	"path/filepath"
	"strings"
)

// PiAgent runs code reviews using the pi CLI
type PiAgent struct {
	Command   string         // The pi command to run (default: "pi")
	Model     string         // Model to use (provider/model format or just model)
	Provider  string         // Explicit provider (optional)
	Reasoning ReasoningLevel // Reasoning level
	Agentic   bool           // Agentic mode
	SessionID string         // Existing session ID to resume
}

// NewPiAgent creates a new pi agent
func NewPiAgent(command string) *PiAgent {
	if command == "" {
		command = "pi"
	}
	return &PiAgent{Command: command, Reasoning: ReasoningStandard}
}

func (a *PiAgent) Name() string {
	return "pi"
}

// WithReasoning returns a copy of the agent configured with the specified reasoning level.
func (a *PiAgent) WithReasoning(level ReasoningLevel) Agent {
	return &PiAgent{
		Command:   a.Command,
		Model:     a.Model,
		Provider:  a.Provider,
		Reasoning: level,
		Agentic:   a.Agentic,
		SessionID: a.SessionID,
	}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *PiAgent) WithAgentic(agentic bool) Agent {
	return &PiAgent{
		Command:   a.Command,
		Model:     a.Model,
		Provider:  a.Provider,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
		SessionID: a.SessionID,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *PiAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &PiAgent{
		Command:   a.Command,
		Model:     model,
		Provider:  a.Provider,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
		SessionID: a.SessionID,
	}
}

// WithProvider returns a copy of the agent configured to use the specified provider.
func (a *PiAgent) WithProvider(provider string) Agent {
	if provider == "" {
		return a
	}
	return &PiAgent{
		Command:   a.Command,
		Model:     a.Model,
		Provider:  provider,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
		SessionID: a.SessionID,
	}
}

// WithSessionID returns a copy of the agent configured to resume a prior session.
func (a *PiAgent) WithSessionID(sessionID string) Agent {
	return &PiAgent{
		Command:   a.Command,
		Model:     a.Model,
		Provider:  a.Provider,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
		SessionID: sanitizedResumeSessionID(sessionID),
	}
}

func (a *PiAgent) CommandName() string {
	return a.Command
}

func (a *PiAgent) CommandLine() string {
	args := a.buildArgs("")
	if len(args) == 0 {
		args = []string{"-p", "--mode", "json"}
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *PiAgent) buildArgs(repoPath string) []string {
	args := []string{"-p", "--mode", "json"}
	if repoPath != "" {
		if sessionPath := resolvePiSessionPath(sanitizedResumeSessionID(a.SessionID)); sessionPath != "" {
			args = append(args, "--session", sessionPath)
		}
	}
	if a.Provider != "" {
		args = append(args, "--provider", a.Provider)
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if level := a.thinkingLevel(); level != "" {
		args = append(args, "--thinking", level)
	}
	return args
}

func (a *PiAgent) thinkingLevel() string {
	switch a.Reasoning {
	case ReasoningThorough:
		return "high"
	case ReasoningFast:
		return "low"
	default: // Standard
		return "medium"
	}
}

func (a *PiAgent) Review(
	ctx context.Context,
	repoPath, commitSHA, prompt string,
	output io.Writer,
) (string, error) {
	// Write prompt to a temporary file to avoid command line length limits
	// and to properly handle special characters.
	tmpDir := os.TempDir()
	tmpFile, err := os.CreateTemp(tmpDir, "roborev-pi-prompt-*.md")
	if err != nil {
		return "", fmt.Errorf("create temp prompt file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(prompt); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("write prompt to temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close temp prompt file: %w", err)
	}

	args := a.buildArgs(repoPath)

	// Add the prompt file as an input argument (prefixed with @)
	// Pi treats @files as context/input.
	// Since the prompt contains the instructions, we pass it as a file.
	// But pi might expect instructions as text arguments.
	// The docs say: pi [options] [@files...] [messages...]
	// If we only provide @file, pi reads it. Does it treat it as a user message or context?
	// Usually @file is context.
	// We want the prompt to be the "message".
	// But if the prompt is huge, we can't pass it as an argument.
	// If we pass @file, pi loads the file.
	// We might need to add a small trigger message like "Follow the instructions in the attached file."
	args = append(args, "@"+tmpFile.Name(), "Please follow the instructions in the attached file to review the code.")

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	tracker := configureSubprocess(cmd)

	// Capture stdout for the result
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	// Stream stdout to output writer if provided
	if output != nil {
		sw := newSyncWriter(output)
		cmd.Stdout = io.MultiWriter(&stdoutBuf, sw)
		cmd.Stderr = io.MultiWriter(&stderrBuf, sw)
	} else {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	}

	if err := cmd.Run(); err != nil {
		if ctxErr := contextProcessError(ctx, tracker, err, nil); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("pi failed: %w\nstderr: %s", err, stderrBuf.String())
	}

	result := stdoutBuf.String()
	if result == "" {
		if stderrBuf.Len() > 0 {
			return stderrBuf.String(), nil
		}
		return "No review output generated", nil
	}

	parsed, parseErr := parsePiJSON(strings.NewReader(result))
	if parseErr != nil {
		return "", parseErr
	}
	if parsed == "" {
		if stderrBuf.Len() > 0 {
			return stderrBuf.String(), nil
		}
		return "No review output generated", nil
	}
	return parsed, nil
}

func piDataDir() string {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

func resolvePiSessionPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(piDataDir(), "sessions", "*", "*_"+sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func parsePiJSON(r io.Reader) (string, error) {
	br := bufio.NewScanner(r)
	var latest string
	for br.Scan() {
		line := strings.TrimSpace(br.Text())
		if line == "" {
			continue
		}

		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text,omitempty"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Message.Role != "assistant" {
			continue
		}
		var parts []string
		for _, item := range ev.Message.Content {
			if item.Type == "text" && item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
		if len(parts) > 0 {
			latest = strings.Join(parts, "\n")
		}
	}
	if err := br.Err(); err != nil {
		return latest, fmt.Errorf("read pi stream: %w", err)
	}
	return latest, nil
}

func init() {
	Register(NewPiAgent(""))
}
