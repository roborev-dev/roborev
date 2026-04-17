package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// ClaudeAgent runs code reviews using Claude Code CLI
type ClaudeAgent struct {
	Command   string         // The claude command to run (default: "claude")
	Model     string         // Model to use (e.g., "opus", "sonnet", or full name)
	Reasoning ReasoningLevel // Reasoning level mapped to --effort flag
	Agentic   bool           // Whether agentic mode is enabled (allow file edits)
	SessionID string         // Existing session ID to resume
}

const claudeDangerousFlag = "--dangerously-skip-permissions"
const claudeEffortFlag = "--effort"

var claudeDangerousSupport sync.Map
var claudeEffortSupport sync.Map

// NewClaudeAgent creates a new Claude Code agent
func NewClaudeAgent(command string) *ClaudeAgent {
	if command == "" {
		command = "claude"
	}
	return &ClaudeAgent{Command: command, Reasoning: ReasoningStandard}
}

func (a *ClaudeAgent) clone(opts ...agentCloneOption) *ClaudeAgent {
	cfg := newAgentCloneConfig(
		a.Command,
		a.Model,
		a.Reasoning,
		a.Agentic,
		a.SessionID,
		opts...,
	)
	return &ClaudeAgent{
		Command:   cfg.Command,
		Model:     cfg.Model,
		Reasoning: cfg.Reasoning,
		Agentic:   cfg.Agentic,
		SessionID: cfg.SessionID,
	}
}

// WithReasoning returns a copy of the agent with the specified reasoning level.
func (a *ClaudeAgent) WithReasoning(level ReasoningLevel) Agent {
	return a.clone(withClonedReasoning(level))
}

// WithAgentic returns a copy of the agent configured for agentic mode.
func (a *ClaudeAgent) WithAgentic(agentic bool) Agent {
	return a.clone(withClonedAgentic(agentic))
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *ClaudeAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return a.clone(withClonedModel(model))
}

// WithSessionID returns a copy of the agent configured to resume a prior session.
func (a *ClaudeAgent) WithSessionID(sessionID string) Agent {
	return a.clone(withClonedSessionID(sessionID))
}

// claudeEffort maps ReasoningLevel to Claude Code's --effort flag values
func (a *ClaudeAgent) claudeEffort() string {
	switch a.Reasoning {
	case ReasoningMaximum:
		return "max"
	case ReasoningThorough:
		return "high"
	case ReasoningMedium:
		return "medium"
	case ReasoningFast:
		return "low"
	default:
		return "" // use claude default (standard = no override)
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
	args := a.buildArgs(agenticMode, true)
	return a.Command + " " + strings.Join(args, " ")
}

// parseModel splits a model spec of the form "<model>@<base_url>" into its
// components. The split happens at the first "@" whose suffix starts with
// http:// or https:// (so proxy URLs embedded after the model are recognized
// while leaving the rest of the spec intact).
//
// Rejections (return error):
//   - Proxy URL containing userinfo (user:pass@host) — these leak into
//     child-process env, /proc, and error messages. Operators must use
//     ROBOREV_CLAUDE_PROXY_TOKEN for proxy auth.
//   - Trailing bare "@" with no URL suffix (e.g. "sonnet@") — malformed
//     input that previously fell through to native routing silently.
//   - Leading "@http(s)://" with no model — proxy mode must pin tier
//     aliases to a concrete model name.
//   - Bare "http(s)://" URL with no "<model>@" prefix — same reason.
//   - Leading "@" with non-URL suffix (e.g. "@foo") — would pass through to
//     Claude as `--model @foo` and produce a confusing downstream error.
func parseModel(spec string) (model, baseURL string, err error) {
	if strings.HasPrefix(spec, "@http://") || strings.HasPrefix(spec, "@https://") {
		return "", "", fmt.Errorf("model spec %q has proxy URL but no model; use '<model>@%s'", spec, spec[1:])
	}
	if strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://") {
		return "", "", fmt.Errorf("model spec %q is a bare proxy URL; use '<model>@%s'", spec, spec)
	}
	if strings.HasPrefix(spec, "@") {
		return "", "", fmt.Errorf("model spec %q starts with '@'; model name must come before the '@<base_url>' suffix", spec)
	}
	for i := 1; i < len(spec); i++ {
		if spec[i] != '@' {
			continue
		}
		suffix := spec[i+1:]
		if strings.HasPrefix(suffix, "http://") || strings.HasPrefix(suffix, "https://") {
			if err := validateProxyURL(suffix); err != nil {
				return "", "", err
			}
			model := spec[:i]
			// Reject specs like "foo@httpx://bar@http://host" where the model
			// component itself contains a URL-like substring — these indicate
			// a malformed multi-URL spec that would otherwise silently pass
			// a nonsensical --model argument to Claude.
			if strings.Contains(model, "://") {
				return "", "", fmt.Errorf("model spec %q has URL-like substring in model component %q; only one '@<base_url>' suffix is allowed", spec, model)
			}
			return model, suffix, nil
		}
	}
	if strings.HasSuffix(spec, "@") {
		return "", "", fmt.Errorf("model spec %q has trailing '@' — remove it or append a proxy URL as '<model>@http(s)://<host>'", spec)
	}
	return spec, "", nil
}

// validateProxyURL rejects proxy URLs that would leak credentials via the
// child-process environment. The parsed URL must not contain userinfo; http://
// is only permitted for loopback hosts so real credentials aren't forwarded
// over plaintext to remote endpoints.
func validateProxyURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("invalid proxy URL %q: %w", raw, err)
	}
	if u.Host == "" {
		return fmt.Errorf("proxy URL %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("proxy URL must not embed credentials; set ROBOREV_CLAUDE_PROXY_TOKEN instead")
	}
	// Reject fragments — they have no server-side meaning for HTTP requests
	// and indicate operator error. Belt-and-suspenders: also scan the raw
	// string in case ParseRequestURI's fragment handling changes.
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("proxy URL %q must not contain a fragment", raw)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("proxy URL %q uses http:// with a non-loopback host; use https:// or a loopback address", raw)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func (a *ClaudeAgent) buildArgs(agenticMode, includeEffort bool) []string {
	sessionID := sanitizedResumeSessionID(a.SessionID)
	// Always use stdin piping + stream-json for non-interactive execution
	// (following claude-code-action pattern from Anthropic)
	args := []string{"-p", "--verbose", "--output-format", "stream-json"}

	// buildArgs is also called from CommandLine() for display; on parse error
	// fall back to the raw configured model so operators see what they typed
	// in logs. Review() re-parses and surfaces the error before execution.
	model, _, parseErr := parseModel(a.Model)
	if parseErr != nil {
		model = a.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}

	if includeEffort {
		if effort := a.claudeEffort(); effort != "" {
			args = append(args, claudeEffortFlag, effort)
		}
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

func claudeSupportsEffortFlag(ctx context.Context, command string) bool {
	if cached, ok := claudeEffortSupport.Load(command); ok {
		return cached.(bool)
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, _ := cmd.CombinedOutput()
	supported := strings.Contains(string(output), claudeEffortFlag)
	claudeEffortSupport.Store(command, supported)
	return supported
}

func (a *ClaudeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	model, baseURL, err := parseModel(a.Model)
	if err != nil {
		return "", err
	}

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

	// Only pass --effort if the installed Claude Code supports it
	includeEffort := a.claudeEffort() != "" && claudeSupportsEffortFlag(ctx, a.Command)

	// Build args - always uses stdin piping + stream-json for non-interactive execution
	args := a.buildArgs(agenticMode, includeEffort)

	// Strip CLAUDECODE to prevent nested-session detection (#270),
	// and handle API key (configured key or subscription auth).
	// Use cmd.Environ() (not os.Environ()) so PWD=<cmd.Dir> is
	// synthesized correctly. Set env before configureSubprocess so
	// GIT_OPTIONAL_LOCKS=0 is appended to the final environment.
	// Always strip inherited Anthropic routing env so roborev owns the
	// routing decision: native mode uses Anthropic defaults, proxy mode
	// injects its own block below. This prevents silent misrouting when
	// the user has exported these vars in their shell.
	stripKeys := []string{
		"ANTHROPIC_API_KEY",
		"CLAUDECODE",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
	}
	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	baseEnv := cmd.Environ()
	env := filterEnv(baseEnv, stripKeys...)
	if baseURL != "" {
		// Route Claude Code to an OpenAI/Anthropic-compatible proxy (Ollama,
		// LiteLLM, etc.). Pin all tier aliases to the same model so Claude's
		// internal tier-switching stays on the proxy target.
		//
		// Proxy auth is opt-in via ROBOREV_CLAUDE_PROXY_TOKEN, read from the
		// roborev process environment (not the agent's). We deliberately do
		// NOT reuse ANTHROPIC_API_KEY: forwarding a real Anthropic credential
		// to an arbitrary third-party proxy is an exfiltration risk. If the
		// env var is unset, send a placeholder — gateways that don't check
		// the header (Ollama, most local dev proxies) accept it, and
		// gateways that do check will reject with a clear 401.
		// Trim whitespace so a token pasted from a config file with a
		// trailing newline doesn't fail auth at the proxy with a confusing
		// 401. Reject embedded control characters (newline, carriage return,
		// NUL) that survive trimming — these would either be rejected by
		// exec (NUL) or produce malformed HTTP headers at the proxy.
		authToken := strings.TrimSpace(os.Getenv("ROBOREV_CLAUDE_PROXY_TOKEN"))
		if strings.ContainsAny(authToken, "\n\r\x00") {
			return "", fmt.Errorf("ROBOREV_CLAUDE_PROXY_TOKEN must not contain control characters (newline, carriage return, or NUL)")
		}
		if authToken == "" {
			authToken = "proxy"
		}
		env = append(env,
			"ANTHROPIC_BASE_URL="+baseURL,
			"ANTHROPIC_AUTH_TOKEN="+authToken,
			"ANTHROPIC_DEFAULT_OPUS_MODEL="+model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL="+model,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL="+model,
			"CLAUDE_CODE_SUBAGENT_MODEL="+model,
		)
	} else if apiKey := AnthropicAPIKey(); apiKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+apiKey)
	}
	env = append(env, "CLAUDE_NO_SOUND=1")

	runResult, runErr := runStreamingCLI(ctx, streamingCLISpec{
		Name:    "claude",
		Command: a.Command,
		Args:    args,
		Dir:     repoPath,
		Env:     env,
		Stdin:   strings.NewReader(prompt),
		Output:  output,
		Parse: func(r io.Reader, sw *syncWriter) (string, error) {
			if sw == nil {
				return parseStreamJSON(r, nil)
			}
			return parseStreamJSON(r, sw)
		},
	})
	if runErr != nil {
		return "", runErr
	}

	if runResult.WaitErr != nil {
		return "", formatDetailedCLIWaitError(runResult, detailedCLIWaitErrorOptions{
			AgentName:     a.Name(),
			Stderr:        runResult.Stderr,
			PartialOutput: runResult.Result,
		})
	}

	if runResult.ParseErr != nil {
		return "", runResult.ParseErr
	}

	if runResult.Result == "" {
		return "No review output generated", nil
	}

	return runResult.Result, nil
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
	var lastResult string
	assistantMessages := newTrailingReviewText()
	var errorMessages []string
	var validEventsParsed bool

	err := scanStreamJSONLines(r, output, func(line string) error {
		var msg claudeStreamMessage
		if jsonErr := json.Unmarshal([]byte(line), &msg); jsonErr == nil {
			validEventsParsed = true

			if msg.Type == "assistant" {
				if text := extractContentText(msg.Message.Content); text != "" {
					assistantMessages.Add(text)
				}
			}
			if msg.Type == "tool_use" || msg.Type == "tool_result" {
				assistantMessages.ResetAfterTool()
			}

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

			if msg.Type == "error" && msg.Error.Message != "" {
				errorMessages = append(errorMessages, msg.Error.Message)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
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
