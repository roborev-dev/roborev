package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// DroidAgent runs code reviews using Factory's Droid CLI
type DroidAgent struct {
	Command   string         // The droid command to run (default: "droid")
	Reasoning ReasoningLevel // Reasoning level for the agent
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
}

// NewDroidAgent creates a new Droid agent with standard reasoning
func NewDroidAgent(command string) *DroidAgent {
	if command == "" {
		command = "droid"
	}
	return &DroidAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the specified reasoning level
func (a *DroidAgent) WithReasoning(level ReasoningLevel) Agent {
	return &DroidAgent{Command: a.Command, Reasoning: level, Agentic: a.Agentic}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *DroidAgent) WithAgentic(agentic bool) Agent {
	return &DroidAgent{
		Command:   a.Command,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

// droidReasoningEffort maps ReasoningLevel to droid-specific effort values
func (a *DroidAgent) droidReasoningEffort() string {
	switch a.Reasoning {
	case ReasoningThorough:
		return "high"
	case ReasoningFast:
		return "low"
	default:
		return "" // use droid default
	}
}

func (a *DroidAgent) Name() string {
	return "droid"
}

func (a *DroidAgent) CommandName() string {
	return a.Command
}

func (a *DroidAgent) buildArgs(repoPath, outputFile, prompt string, agenticMode bool) []string {
	args := []string{"exec"}

	// Set autonomy level based on agentic mode
	if agenticMode {
		args = append(args, "--auto", "medium")
	} else {
		args = append(args, "--auto", "low")
	}

	// Note: working directory is set via cmd.Dir in Review(), not via CLI flag
	// (droid doesn't support -C like some other tools)

	// Set output file
	args = append(args, "-o", outputFile)

	// Set reasoning effort if specified
	if effort := a.droidReasoningEffort(); effort != "" {
		args = append(args, "--reasoning-effort", effort)
	}

	// Add -- to stop flag parsing, then the prompt as the final argument
	// This prevents prompts starting with "-" from being parsed as flags
	args = append(args, "--", prompt)

	return args
}

func (a *DroidAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Use agentic mode if either per-job setting or global setting enables it
	agenticMode := a.Agentic || AllowUnsafeAgents()

	// Create unique temp file for output
	tmpFile, err := os.CreateTemp("", "roborev-droid-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	outputFile := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(outputFile)

	args := a.buildArgs(repoPath, outputFile, prompt, agenticMode)

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath

	var stderr bytes.Buffer
	if sw := newSyncWriter(output); sw != nil {
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("droid failed: %w\nstderr: %s", err, stderr.String())
	}

	// Read the output file
	result, err := os.ReadFile(outputFile)
	if err != nil {
		return "", fmt.Errorf("read output: %w", err)
	}

	if len(result) == 0 {
		return "No review output generated", nil
	}

	return string(result), nil
}

func init() {
	Register(NewDroidAgent(""))
}
