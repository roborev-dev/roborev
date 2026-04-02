package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// CodexAgent runs code reviews using the Codex CLI
type CodexAgent struct {
	Command   string         // The codex command to run (default: "codex")
	Model     string         // Model to use (e.g., "o3", "o4-mini")
	Reasoning ReasoningLevel // Reasoning level for the agent
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
	SessionID string         // Existing session/thread ID to resume
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

func (a *CodexAgent) clone(opts ...agentCloneOption) *CodexAgent {
	cfg := newAgentCloneConfig(
		a.Command,
		a.Model,
		a.Reasoning,
		a.Agentic,
		a.SessionID,
		opts...,
	)
	return &CodexAgent{
		Command:   cfg.Command,
		Model:     cfg.Model,
		Reasoning: cfg.Reasoning,
		Agentic:   cfg.Agentic,
		SessionID: cfg.SessionID,
	}
}

// WithReasoning returns a copy of the agent with the specified reasoning level
func (a *CodexAgent) WithReasoning(level ReasoningLevel) Agent {
	return a.clone(withClonedReasoning(level))
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *CodexAgent) WithAgentic(agentic bool) Agent {
	return a.clone(withClonedAgentic(agentic))
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *CodexAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return a.clone(withClonedModel(model))
}

// WithSessionID returns a copy of the agent configured to resume a prior session.
func (a *CodexAgent) WithSessionID(sessionID string) Agent {
	return a.clone(withClonedSessionID(sessionID))
}

// codexReasoningEffort maps ReasoningLevel to codex-specific effort values
func (a *CodexAgent) codexReasoningEffort() string {
	switch a.Reasoning {
	case ReasoningMaximum:
		return "xhigh"
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
	args := a.commandArgs(codexArgOptions{
		agenticMode: agenticMode,
		autoApprove: !agenticMode,
		preview:     true,
	})
	return a.Command + " " + strings.Join(args, " ")
}

func (a *CodexAgent) buildArgs(repoPath string, agenticMode, autoApprove bool) []string {
	return a.commandArgs(codexArgOptions{
		repoPath:    repoPath,
		agenticMode: agenticMode,
		autoApprove: autoApprove,
	})
}

type codexArgOptions struct {
	repoPath    string
	agenticMode bool
	autoApprove bool
	preview     bool
}

func (a *CodexAgent) commandArgs(opts codexArgOptions) []string {
	sessionID := sanitizedResumeSessionID(a.SessionID)
	args := []string{
		"exec",
	}
	if sessionID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json")
	if opts.agenticMode {
		args = append(args, codexDangerousFlag)
	}
	if opts.autoApprove {
		// Use full-access sandbox for review mode. The read-only
		// sandbox blocks loopback networking, which prevents git
		// commands from working in CI review jobs. roborev runs in
		// trusted environments where the code is the operator's own,
		// so sandbox enforcement is unnecessary.
		args = append(args, "--sandbox", "danger-full-access")
	}
	if !opts.preview {
		args = append(args, "-C", opts.repoPath)
	}
	if a.Model != "" {
		args = append(args, "-m", a.Model)
	}
	if effort := a.codexReasoningEffort(); effort != "" {
		args = append(args, "-c", fmt.Sprintf(`model_reasoning_effort="%s"`, effort))
	}
	if sessionID != "" {
		args = append(args, sessionID)
	}
	if !opts.preview {
		// "-" must come after all flags to read prompt from stdin
		// This avoids Windows command line length limits (~32KB)
		args = append(args, "-")
	}
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

// codexSupportsNonInteractive checks that codex supports --sandbox,
// needed for non-agentic review mode (--sandbox danger-full-access).
func codexSupportsNonInteractive(ctx context.Context, command string) (bool, error) {
	if cached, ok := codexAutoApproveSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "exec", "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), "--sandbox")
	if err != nil && !supported {
		return false, fmt.Errorf("check %s exec --help: %w: %s", command, err, output)
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

	// Non-agentic review mode uses --sandbox danger-full-access for
	// non-interactive execution that can still run git commands.
	autoApprove := false
	if !agenticMode {
		supported, err := codexSupportsNonInteractive(ctx, a.Command)
		if err != nil {
			return "", err
		}
		if !supported {
			return "", fmt.Errorf("codex version too old for non-interactive execution; upgrade codex or use --agentic")
		}
		autoApprove = true
	}

	// Use codex exec with --json for JSONL streaming output
	// The prompt is piped via stdin using "-" to avoid command line length limits on Windows
	args := a.buildArgs(repoPath, agenticMode, autoApprove)

	runResult, runErr := runStreamingCLI(ctx, streamingCLISpec{
		Name:         "codex",
		Command:      a.Command,
		Args:         args,
		Dir:          repoPath,
		Stdin:        strings.NewReader(prompt),
		Output:       output,
		StreamStderr: true,
		Parse: func(r io.Reader, sw *syncWriter) (string, error) {
			return a.parseStreamJSON(r, sw)
		},
	})
	if runErr != nil {
		return "", runErr
	}

	if runResult.WaitErr != nil {
		return "", formatStreamingCLIWaitError("codex", runResult, runResult.Stderr)
	}

	if runResult.ParseErr != nil {
		if errors.Is(runResult.ParseErr, errNoCodexJSON) {
			return "", fmt.Errorf("codex CLI did not emit valid --json events; upgrade codex or check CLI compatibility: %w", errNoCodexJSON)
		}
		return "", runResult.ParseErr
	}

	if runResult.Result == "" {
		return "No review output generated", nil
	}

	return runResult.Result, nil
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

func isCodexToolEvent(ev codexEvent) bool {
	if ev.Type != "item.started" &&
		ev.Type != "item.updated" &&
		ev.Type != "item.completed" {
		return false
	}
	switch ev.Item.Type {
	case "command_execution", "file_change":
		return true
	default:
		return false
	}
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
	var validEventsParsed bool
	agentMessages := newTrailingReviewText()
	var streamFailure error

	err := scanStreamJSONLines(r, sw, func(line string) error {
		var ev codexEvent
		if jsonErr := json.Unmarshal([]byte(line), &ev); jsonErr == nil {
			if isCodexEventType(ev.Type) {
				validEventsParsed = true

				if streamFailure == nil {
					streamFailure = codexFailureEventError(ev)
				}

				if isCodexToolEvent(ev) {
					// The persisted review is defined as the assistant text
					// after the last tool event in the stream.
					agentMessages.ResetAfterTool()
				}

				// Collect agent_message text from completed/updated items.
				// For messages with IDs, keep only the latest text per ID to avoid duplicates
				// from incremental updates while preserving first-seen order.
				if (ev.Type == "item.completed" || ev.Type == "item.updated") &&
					ev.Item.Type == "agent_message" && ev.Item.Text != "" {
					agentMessages.AddWithID(ev.Item.ID, ev.Item.Text)
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if !validEventsParsed {
		return "", errNoCodexJSON
	}

	if streamFailure != nil {
		return "", streamFailure
	}

	if result := agentMessages.Join("\n"); result != "" {
		return result, nil
	}

	return "", nil
}

func init() {
	Register(NewCodexAgent(""))
}
