package agent

import (
	"context"
	"fmt"
	"io"
	"os/exec"
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

// ReasoningLevels returns the canonical reasoning level names.
func ReasoningLevels() []string {
	return []string{string(ReasoningFast), string(ReasoningStandard), string(ReasoningThorough)}
}

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
	// If model is empty, the agent is returned unchanged (preserving any built-in default).
	// Agents that don't support model selection may return themselves unchanged.
	// For opencode, the model format is "provider/model" (e.g., "anthropic/claude-sonnet-4-20250514").
	WithModel(model string) Agent

	// CommandLine returns a representative command line for this agent (binary + flags).
	// Runtime-specific arguments (repo path, output file, prompt) are excluded.
	// Useful for debugging which binary, model, and flags were used.
	CommandLine() string
}

// CommandAgent is an agent that uses an external command
type CommandAgent interface {
	Agent
	// CommandName returns the executable command name
	CommandName() string
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

// CanonicalName resolves an agent alias to its canonical name.
// Returns the name unchanged if it is not an alias.
func CanonicalName(name string) string {
	return resolveAlias(name)
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

// IsAvailable checks if an agent's command is installed on the system
// Supports aliases like "claude" for "claude-code"
func IsAvailable(name string) bool {
	name = resolveAlias(name)
	a, ok := registry[name]
	if !ok {
		return false
	}

	// Check if agent implements CommandAgent interface
	if ca, ok := a.(CommandAgent); ok {
		_, err := exec.LookPath(ca.CommandName())
		return err == nil
	}

	// Non-command agents (like test) are always available
	return true
}

// GetAvailable returns an available agent, trying the requested one first,
// then falling back to alternatives. Returns error only if no agents available.
// Supports aliases like "claude" for "claude-code"
func GetAvailable(preferred string) (Agent, error) {
	// Resolve alias upfront for consistent comparisons
	preferred = resolveAlias(preferred)

	// Try preferred agent first
	if preferred != "" && IsAvailable(preferred) {
		return Get(preferred)
	}

	// Fallback order: codex, claude-code, gemini, copilot, opencode, cursor, droid
	fallbacks := []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "droid"}
	for _, name := range fallbacks {
		if name != preferred && IsAvailable(name) {
			return Get(name)
		}
	}

	// List what's actually available for error message (exclude test agent)
	var available []string
	for name := range registry {
		if name != "test" && IsAvailable(name) {
			available = append(available, name)
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no agents available (install one of: codex, claude-code, gemini, copilot, opencode, cursor, droid)\nYou may need to run 'roborev daemon restart' from a shell that has access to your agents")
	}

	return Get(available[0])
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

// Unregister removes an agent from the registry (useful for testing)
func Unregister(name string) {
	delete(registry, name)
}
