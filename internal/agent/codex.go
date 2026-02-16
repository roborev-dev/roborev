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
	"sync"
	"time"
)

// CodexAgent runs code reviews using the Codex CLI
type CodexAgent struct {
	Command   string         // The codex command to run (default: "codex")
	Model     string         // Model to use (e.g., "o3", "o4-mini")
	Reasoning ReasoningLevel // Reasoning level for the agent
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
}

const codexDangerousFlag = "--dangerously-bypass-approvals-and-sandbox"
const codexAutoApproveFlag = "--full-auto"

var codexDangerousSupport sync.Map
var codexAutoApproveSupport sync.Map

// errNoCodexJSON indicates no valid codex --json events were parsed.
var errNoCodexJSON = errors.New("no valid codex --json events parsed from output")

// errCodexStreamFailed indicates codex emitted a failure event in the JSON stream.
var errCodexStreamFailed = errors.New("codex stream reported failure")

// NewCodexAgent creates a new Codex agent with standard reasoning
func NewCodexAgent(command string) *CodexAgent {
	if command == "" {
		command = "codex"
	}
	return &CodexAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the specified reasoning level
func (a *CodexAgent) WithReasoning(level ReasoningLevel) Agent {
	return &CodexAgent{Command: a.Command, Model: a.Model, Reasoning: level, Agentic: a.Agentic}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *CodexAgent) WithAgentic(agentic bool) Agent {
	return &CodexAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *CodexAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &CodexAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

// codexReasoningEffort maps ReasoningLevel to codex-specific effort values
func (a *CodexAgent) codexReasoningEffort() string {
	switch a.Reasoning {
	case ReasoningThorough:
		return "high"
	case ReasoningFast:
		return "low"
	default:
		return "" // use codex default
	}
}

func (a *CodexAgent) Name() string {
	return "codex"
}

func (a *CodexAgent) CommandName() string {
	return a.Command
}

func (a *CodexAgent) CommandLine() string {
	agenticMode := a.Agentic || AllowUnsafeAgents()
	// Show representative args (repo path is a runtime value)
	args := []string{"exec", "--json"}
	if agenticMode {
		args = append(args, codexDangerousFlag)
	} else {
		args = append(args, codexAutoApproveFlag)
	}
	if a.Model != "" {
		args = append(args, "-m", a.Model)
	}
	if effort := a.codexReasoningEffort(); effort != "" {
		args = append(args, "-c", fmt.Sprintf(`model_reasoning_effort="%s"`, effort))
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *CodexAgent) buildArgs(repoPath string, agenticMode, autoApprove bool) []string {
	args := []string{
		"exec",
		"--json",
	}
	if agenticMode {
		args = append(args, codexDangerousFlag)
	}
	if autoApprove {
		args = append(args, codexAutoApproveFlag)
	}
	args = append(args,
		"-C", repoPath,
	)
	if a.Model != "" {
		args = append(args, "-m", a.Model)
	}
	if effort := a.codexReasoningEffort(); effort != "" {
		args = append(args, "-c", fmt.Sprintf(`model_reasoning_effort="%s"`, effort))
	}
	// "-" must come after all flags to read prompt from stdin
	// This avoids Windows command line length limits (~32KB)
	args = append(args, "-")
	return args
}

func codexSupportsDangerousFlag(ctx context.Context, command string) (bool, error) {
	if cached, ok := codexDangerousSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), codexDangerousFlag)
	if err != nil && !supported {
		return false, fmt.Errorf("check %s --help: %w: %s", command, err, output)
	}
	codexDangerousSupport.Store(command, supported)
	return supported, nil
}

func codexSupportsAutoApproveFlag(ctx context.Context, command string) (bool, error) {
	if cached, ok := codexAutoApproveSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), codexAutoApproveFlag)
	if err != nil && !supported {
		return false, fmt.Errorf("check %s --help: %w: %s", command, err, output)
	}
	codexAutoApproveSupport.Store(command, supported)
	return supported, nil
}

func (a *CodexAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Use agentic mode if either per-job setting or global setting enables it
	agenticMode := a.Agentic || AllowUnsafeAgents()

	if agenticMode {
		supported, err := codexSupportsDangerousFlag(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("codex does not support %s; upgrade codex or disable allow_unsafe_agents", codexDangerousFlag)
		}
	}

	// When piping stdin, codex needs --full-auto to run non-interactively.
	// Agentic mode uses --dangerously-bypass-approvals-and-sandbox which implies auto-approve.
	autoApprove := false
	if !agenticMode {
		supported, err := codexSupportsAutoApproveFlag(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("codex requires %s for stdin input; upgrade codex or use --agentic", codexAutoApproveFlag)
		}
		autoApprove = true
	}

	// Use codex exec with --json for JSONL streaming output
	// The prompt is piped via stdin using "-" to avoid command line length limits on Windows
	args := a.buildArgs(repoPath, agenticMode, autoApprove)

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.WaitDelay = 5 * time.Second

	// Pipe prompt via stdin to avoid command line length limits on Windows.
	// Windows has a ~32KB limit on command line arguments, which large diffs easily exceed.
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
		return "", fmt.Errorf("start codex: %w", err)
	}

	// Parse JSONL stream from stdout
	result, parseErr := a.parseStreamJSON(stdoutPipe, sw)

	if waitErr := cmd.Wait(); waitErr != nil {
		if parseErr != nil {
			return "", fmt.Errorf("codex failed: %w (parse error: %v)\nstderr: %s", waitErr, parseErr, stderr.String())
		}
		return "", fmt.Errorf("codex failed: %w\nstderr: %s", waitErr, stderr.String())
	}

	if parseErr != nil {
		if errors.Is(parseErr, errNoCodexJSON) {
			return "", fmt.Errorf("codex CLI did not emit valid --json events; upgrade codex or check CLI compatibility: %w", errNoCodexJSON)
		}
		return "", parseErr
	}

	if result == "" {
		return "No review output generated", nil
	}

	return result, nil
}

// codexEvent represents a top-level event in codex's --json JSONL output.
type codexEvent struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Error   struct {
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
	Item struct {
		ID      string `json:"id,omitempty"`
		Type    string `json:"type,omitempty"`
		Text    string `json:"text,omitempty"`
		Command string `json:"command,omitempty"`
		Status  string `json:"status,omitempty"`
	} `json:"item,omitempty"`
}

func isCodexEventType(eventType string) bool {
	return eventType == "error" ||
		strings.HasPrefix(eventType, "thread.") ||
		strings.HasPrefix(eventType, "turn.") ||
		strings.HasPrefix(eventType, "item.")
}

func codexFailureEventError(ev codexEvent) error {
	switch ev.Type {
	case "turn.failed":
		if ev.Error.Message != "" {
			return fmt.Errorf("%w: %s", errCodexStreamFailed, ev.Error.Message)
		}
		if ev.Message != "" {
			return fmt.Errorf("%w: %s", errCodexStreamFailed, ev.Message)
		}
		return fmt.Errorf("%w: turn failed", errCodexStreamFailed)
	case "error":
		if ev.Error.Message != "" {
			return fmt.Errorf("%w: %s", errCodexStreamFailed, ev.Error.Message)
		}
		if ev.Message != "" {
			return fmt.Errorf("%w: %s", errCodexStreamFailed, ev.Message)
		}
		return fmt.Errorf("%w: stream error", errCodexStreamFailed)
	default:
		return nil
	}
}

// parseStreamJSON parses codex's --json JSONL output and extracts review text.
// Codex emits events like thread.started, turn.started, item.completed (with agent_message),
// and turn.completed. The agent_message items contain the actual review text.
func (a *CodexAgent) parseStreamJSON(r io.Reader, sw *syncWriter) (string, error) {
	br := bufio.NewReader(r)

	var validEventsParsed bool
	var agentMessages []string
	messageIndexByID := make(map[string]int)
	var streamFailure error

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read stream: %w", err)
		}

		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			// Stream raw line to the writer for progress visibility
			if sw != nil {
				sw.Write([]byte(trimmed + "\n"))
			}

			var ev codexEvent
			if jsonErr := json.Unmarshal([]byte(trimmed), &ev); jsonErr == nil {
				if isCodexEventType(ev.Type) {
					validEventsParsed = true

					if streamFailure == nil {
						streamFailure = codexFailureEventError(ev)
					}

					// Collect agent_message text from completed/updated items.
					// For messages with IDs, keep only the latest text per ID to avoid duplicates
					// from incremental updates while preserving first-seen order.
					if (ev.Type == "item.completed" || ev.Type == "item.updated") &&
						ev.Item.Type == "agent_message" && ev.Item.Text != "" {
						if ev.Item.ID == "" {
							agentMessages = append(agentMessages, ev.Item.Text)
						} else if idx, ok := messageIndexByID[ev.Item.ID]; ok {
							agentMessages[idx] = ev.Item.Text
						} else {
							messageIndexByID[ev.Item.ID] = len(agentMessages)
							agentMessages = append(agentMessages, ev.Item.Text)
						}
					}
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	if !validEventsParsed {
		return "", errNoCodexJSON
	}

	if streamFailure != nil {
		return "", streamFailure
	}

	if len(agentMessages) > 0 {
		return strings.Join(agentMessages, "\n"), nil
	}

	return "", nil
}

func init() {
	Register(NewCodexAgent(""))
}
