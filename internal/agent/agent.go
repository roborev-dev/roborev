package agent

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// ReasoningLevel controls how much reasoning/thinking an agent uses
type ReasoningLevel string

const (
	// ReasoningThorough uses maximum reasoning for deep analysis (slower)
	ReasoningThorough ReasoningLevel = "thorough"
	// ReasoningStandard uses balanced reasoning (default)
	ReasoningStandard ReasoningLevel = "standard"
	// ReasoningFast uses minimal reasoning for quick responses
	ReasoningFast ReasoningLevel = "fast"
)

// ParseReasoningLevel converts a string to ReasoningLevel, defaulting to standard
func ParseReasoningLevel(s string) ReasoningLevel {
	switch s {
	case "thorough", "high":
		return ReasoningThorough
	case "fast", "low":
		return ReasoningFast
	case "standard", "medium", "":
		return ReasoningStandard
	default:
		return ReasoningStandard
	}
}

// Agent defines the interface for code review agents
type Agent interface {
	// Name returns the agent identifier (e.g., "codex", "claude-code")
	Name() string

	// Review runs a code review and returns the output.
	// If output is non-nil, agent progress is streamed to it in real-time.
	Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (result string, err error)

	// WithReasoning returns a copy of the agent configured with the specified reasoning level.
	// Agents that don't support reasoning levels may return themselves unchanged.
	WithReasoning(level ReasoningLevel) Agent

	// WithAgentic returns a copy of the agent configured for agentic mode.
	// In agentic mode, agents can edit files and run commands.
	// If false, agents operate in read-only review mode.
	WithAgentic(agentic bool) Agent

	// WithModel returns a copy of the agent configured to use the specified model.
	// Agents that don't support model selection may return themselves unchanged.
	// For opencode, the model format is "provider/model" (e.g., "anthropic/claude-sonnet-4-20250514").
	WithModel(model string) Agent
}

// CommandAgent is an agent that uses an external command
type CommandAgent interface {
	Agent
	// CommandName returns the executable command name
	CommandName() string
}

// AvailabilityChecker is optionally implemented by agents to determine
// availability using HTTP health checks (e.g. GET /api/tags for Ollama),
// rather than CLI presence.
type AvailabilityChecker interface {
	Agent
	IsAvailable() bool
}

// Registry holds available agents
var registry = make(map[string]Agent)
var allowUnsafeAgents atomic.Bool
var anthropicAPIKey atomic.Value

func AllowUnsafeAgents() bool {
	return allowUnsafeAgents.Load()
}

func SetAllowUnsafeAgents(allow bool) {
	allowUnsafeAgents.Store(allow)
}

// AnthropicAPIKey returns the configured Anthropic API key, or empty string if not set
func AnthropicAPIKey() string {
	if v := anthropicAPIKey.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// SetAnthropicAPIKey sets the Anthropic API key for Claude Code
func SetAnthropicAPIKey(key string) {
	anthropicAPIKey.Store(key)
}

// aliases maps short names to full agent names
var aliases = map[string]string{
	"claude": "claude-code",
}

// resolveAlias returns the canonical agent name, resolving aliases
func resolveAlias(name string) string {
	if canonical, ok := aliases[name]; ok {
		return canonical
	}
	return name
}

// Register adds an agent to the registry
func Register(a Agent) {
	registry[a.Name()] = a
}

// Get returns an agent by name (supports aliases like "claude" for "claude-code")
func Get(name string) (Agent, error) {
	name = resolveAlias(name)
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent: %s", name)
	}
	return a, nil
}

// Available returns the names of all registered agents
func Available() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// IsAvailable checks if an agent's command is installed on the system.
// Supports aliases like "claude" for "claude-code".
// Agents implementing AvailabilityChecker use their IsAvailable() (e.g. HTTP health check).
func IsAvailable(name string) bool {
	name = resolveAlias(name)
	a, ok := registry[name]
	if !ok {
		return false
	}

	if ac, ok := a.(AvailabilityChecker); ok {
		return ac.IsAvailable()
	}

	if ca, ok := a.(CommandAgent); ok {
		_, err := exec.LookPath(ca.CommandName())
		return err == nil
	}

	// Non-command agents (like test) are always available
	return true
}

// isOllamaAvailableAt checks if the Ollama server at baseURL is reachable.
// If baseURL is empty, uses the default agent's URL (localhost:11434).
// When baseURL is explicitly set to a non-localhost URL (e.g. a remote server),
// we treat it as available without an HTTP check so that firewall/network
// issues or slow startup don't cause "no agents available"; the actual
// Review call will fail with a clear error if the server is unreachable.
func isOllamaAvailableAt(baseURL string) bool {
	a, ok := registry["ollama"]
	if !ok {
		return false
	}
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	} else {
		baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	}
	// Explicit non-localhost URL: trust config, skip reachability check
	if baseURL != "" && baseURL != "http://localhost:11434" {
		return true
	}
	a = WithOllamaBaseURL(a, baseURL)
	ac, ok := a.(AvailabilityChecker)
	if !ok {
		return false
	}
	return ac.IsAvailable()
}

// getOllamaWithBaseURL returns the ollama agent, with baseURL applied if non-empty.
func getOllamaWithBaseURL(baseURL string) (Agent, error) {
	a, err := Get("ollama")
	if err != nil {
		return nil, err
	}
	if baseURL != "" {
		a = WithOllamaBaseURL(a, baseURL)
	}
	return a, nil
}

// GetAvailable returns an available agent, trying the requested one first,
// then falling back to alternatives. Returns error only if no agents available.
// Supports aliases like "claude" for "claude-code"
func GetAvailable(preferred string) (Agent, error) {
	return GetAvailableWithOllamaBaseURL(preferred, "")
}

// GetAvailableWithOllamaBaseURL is like GetAvailable but uses the given Ollama
// base URL when checking ollama availability and when returning the ollama agent.
// Use this when config points at a remote Ollama server (e.g. ollama_base_url).
// Pass empty string to use the default (localhost:11434).
func GetAvailableWithOllamaBaseURL(preferred, ollamaBaseURL string) (Agent, error) {
	// Resolve alias upfront for consistent comparisons
	preferred = resolveAlias(preferred)

	ollamaAvailable := func() bool {
		return isOllamaAvailableAt(ollamaBaseURL)
	}
	ollamaAgent := func() (Agent, error) {
		return getOllamaWithBaseURL(ollamaBaseURL)
	}

	// Try preferred agent first
	if preferred != "" {
		if preferred == "ollama" {
			if ollamaAvailable() {
				return ollamaAgent()
			}
		} else if IsAvailable(preferred) {
			return Get(preferred)
		}
	}

	// Fallback order: codex, claude-code, gemini, copilot, opencode, cursor, droid, ollama
	fallbacks := []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "droid", "ollama"}
	for _, name := range fallbacks {
		if name == preferred {
			continue
		}
		if name == "ollama" {
			if ollamaAvailable() {
				return ollamaAgent()
			}
			continue
		}
		if IsAvailable(name) {
			return Get(name)
		}
	}

	// List what's actually available for error message (exclude test agent)
	var available []string
	for name := range registry {
		if name == "test" {
			continue
		}
		if name == "ollama" {
			if ollamaAvailable() {
				available = append(available, name)
			}
			continue
		}
		if IsAvailable(name) {
			available = append(available, name)
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no agents available (install one of: codex, claude-code, gemini, copilot, opencode, cursor, droid, ollama)\nYou may need to run 'roborev daemon restart' from a shell that has access to your agents")
	}

	// Return first available; for ollama use the configured base URL
	if available[0] == "ollama" {
		return ollamaAgent()
	}
	return Get(available[0])
}

// WithOllamaBaseURL configures the BaseURL for an Ollama agent if it is one.
// Returns the agent unchanged if it's not an Ollama agent.
func WithOllamaBaseURL(a Agent, baseURL string) Agent {
	if a.Name() != "ollama" {
		return a
	}
	// Type assertion to OllamaAgent - safe because we checked the name
	if ollamaAgent, ok := a.(interface {
		WithBaseURL(baseURL string) Agent
	}); ok {
		return ollamaAgent.WithBaseURL(baseURL)
	}
	return a
}

// syncWriter wraps an io.Writer with mutex protection for concurrent writes.
// This is needed because io.MultiWriter sends both stdout and stderr to the
// same output concurrently, which could race if the underlying writer isn't
// thread-safe.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// newSyncWriter creates a thread-safe wrapper around an io.Writer.
// Returns nil if w is nil.
func newSyncWriter(w io.Writer) *syncWriter {
	if w == nil {
		return nil
	}
	return &syncWriter{w: w}
}

func (sw *syncWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}
