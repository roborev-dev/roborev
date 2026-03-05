package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// PiAgent runs code reviews using the pi CLI
type PiAgent struct {
	Command   string         // The pi command to run (default: "pi")
	Model     string         // Model to use (provider/model format or just model)
	Provider  string         // Explicit provider (optional)
	Reasoning ReasoningLevel // Reasoning level
	Agentic   bool           // Agentic mode
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
	}
}

func (a *PiAgent) CommandName() string {
	return a.Command
}

func (a *PiAgent) CommandLine() string {
	args := []string{"-p"}
	if a.Provider != "" {
		args = append(args, "--provider", a.Provider)
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if level := a.thinkingLevel(); level != "" {
		args = append(args, "--thinking", level)
	}
	return a.Command + " " + strings.Join(args, " ")
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

	args := []string{"-p"} // Print response and exit
	if a.Provider != "" {
		args = append(args, "--provider", a.Provider)
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if level := a.thinkingLevel(); level != "" {
		args = append(args, "--thinking", level)
	}

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
		return "", fmt.Errorf("pi failed: %w\nstderr: %s", err, stderrBuf.String())
	}

	result := stdoutBuf.String()
	if result == "" {
		// Fallback to stderr if stdout is empty (though -p should print to stdout)
		if stderrBuf.Len() > 0 {
			return stderrBuf.String(), nil
		}
		return "No review output generated", nil
	}

	return result, nil
}

func init() {
	Register(NewPiAgent(""))
}
