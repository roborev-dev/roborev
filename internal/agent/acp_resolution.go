package agent

import (
	"github.com/roborev-dev/roborev/internal/config"
	"os/exec"
	"strings"
)

func defaultACPAgentConfig() *config.ACPAgentConfig {
	return &config.ACPAgentConfig{
		Name:            defaultACPName,
		Command:         defaultACPCommand,
		Args:            []string{},
		ReadOnlyMode:    defaultACPReadOnlyMode,
		AutoApproveMode: defaultACPAutoApproveMode,
		Mode:            defaultACPReadOnlyMode,
		Model:           "",
		Timeout:         defaultACPTimeoutSeconds,
	}
}

func isConfiguredACPAgentName(name string, cfg *config.Config) bool {
	rawName := strings.TrimSpace(name)
	if rawName == defaultACPName {
		return true
	}
	if cfg == nil || cfg.ACP == nil {
		return false
	}

	configuredName := strings.TrimSpace(cfg.ACP.Name)
	if rawName == "" || configuredName == "" {
		return false
	}

	// Exact match only — no alias resolution. This prevents collisions
	// where an alias like "agent" → "cursor" would incorrectly route
	// cursor requests to ACP. Callers pass rawPreferred (pre-alias) so
	// `acp.name = "claude"` matches request "claude" but not "claude-code".
	return rawName == configuredName
}

func configuredACPAgent(cfg *config.Config) *ACPAgent {
	var acpCfg *config.ACPAgentConfig
	if cfg != nil {
		acpCfg = cfg.ACP
	}
	resolved := NewACPAgentFromConfig(acpCfg)
	// Keep a stable canonical name in runtime state.
	resolved.agentName = defaultACPName
	return resolved
}

// applyCommandOverrides clones the agent and applies the configured
// command override from cfg. Returns the original agent unchanged when
// no override applies. Cloning avoids mutating global registry
// singletons that concurrent callers share.
func applyCommandOverrides(a Agent, cfg *config.Config) Agent {
	if cfg == nil {
		return a
	}
	switch agent := a.(type) {
	case *CodexAgent:
		if cfg.CodexCmd != "" {
			clone := *agent
			clone.Command = cfg.CodexCmd
			return &clone
		}
	case *ClaudeAgent:
		if cfg.ClaudeCodeCmd != "" {
			clone := *agent
			clone.Command = cfg.ClaudeCodeCmd
			return &clone
		}
	case *CursorAgent:
		if cfg.CursorCmd != "" {
			clone := *agent
			clone.Command = cfg.CursorCmd
			return &clone
		}
	case *PiAgent:
		if cfg.PiCmd != "" {
			clone := *agent
			clone.Command = cfg.PiCmd
			return &clone
		}
	case *OpenCodeAgent:
		if cfg.OpenCodeCmd != "" {
			clone := *agent
			clone.Command = cfg.OpenCodeCmd
			return &clone
		}
	}
	return a
}

// isAvailableWithConfig checks whether the named agent can be resolved
// to an executable command, considering config command overrides. If a
// config override points to an available binary, the agent is considered
// available even when the default command isn't in PATH.
func isAvailableWithConfig(name string, cfg *config.Config) bool {
	name = resolveAlias(name)
	a, ok := registry[name]
	if !ok {
		return false
	}
	ca, ok := a.(CommandAgent)
	if !ok {
		return true // non-command agents (e.g. test) are always available
	}
	// Check the configured command first — it takes priority.
	overridden := applyCommandOverrides(a, cfg)
	if oca, ok := overridden.(CommandAgent); ok {
		if _, err := exec.LookPath(oca.CommandName()); err == nil {
			return true
		}
	}
	// Fall back to the default (hardcoded) command.
	_, err := exec.LookPath(ca.CommandName())
	return err == nil
}

// GetAvailableWithConfig resolves an available agent while honoring runtime ACP config.
// It treats cfg.ACP.Name as an alias for "acp" and applies cfg.ACP command/mode/model
// at resolution time instead of package-init time.
// It also applies command overrides for other agents (codex, claude, cursor, pi).
//
// Optional backup agent names are tried after the preferred agent but
// before the hardcoded fallback chain (see GetAvailable).
func GetAvailableWithConfig(preferred string, cfg *config.Config, backups ...string) (Agent, error) {
	rawPreferred := strings.TrimSpace(preferred)
	preferred = resolveAlias(rawPreferred)

	if isConfiguredACPAgentName(rawPreferred, cfg) {
		acpAgent := configuredACPAgent(cfg)
		if _, err := exec.LookPath(acpAgent.CommandName()); err == nil {
			return acpAgent, nil
		}
		// ACP requested with an invalid configured command. Try canonical ACP next.
		if canonicalACP, err := Get(defaultACPName); err == nil {
			if commandAgent, ok := canonicalACP.(CommandAgent); !ok {
				return canonicalACP, nil
			} else if _, err := exec.LookPath(commandAgent.CommandName()); err == nil {
				return canonicalACP, nil
			}
		}

		// ACP unavailable — try backup agents with config-aware
		// availability so *_cmd overrides are honored.
		if cfg != nil {
			for _, b := range backups {
				b = resolveAlias(b)
				if b == "" {
					continue
				}
				if _, ok := registry[b]; ok && isAvailableWithConfig(b, cfg) {
					a, _ := Get(b)
					return applyCommandOverrides(a, cfg), nil
				}
			}
		}

		// Finally fall back to normal auto-selection.
		return GetAvailable("", backups...)
	}

	// Check the preferred agent using config command overrides before
	// falling back. GetAvailable only checks the hardcoded default
	// command via IsAvailable, so a configured command (e.g.
	// claude_code_cmd = "/usr/local/bin/claude-wrapper") would be
	// missed when the default binary isn't in PATH.
	if preferred != "" && cfg != nil {
		if _, ok := registry[preferred]; !ok {
			// Unknown agent — let GetAvailable produce the error.
			return GetAvailable(preferred, backups...)
		}
		if isAvailableWithConfig(preferred, cfg) {
			a, _ := Get(preferred)
			return applyCommandOverrides(a, cfg), nil
		}
	}

	// Try backup agents with config-aware availability before the
	// fallback chain. This runs regardless of whether preferred is
	// set so that backup-only configurations (preferred="" with a
	// backup_agent) still honor *_cmd overrides.
	if cfg != nil {
		for _, b := range backups {
			b = resolveAlias(b)
			if b == "" || b == preferred {
				continue
			}
			if _, ok := registry[b]; ok && isAvailableWithConfig(b, cfg) {
				a, _ := Get(b)
				return applyCommandOverrides(a, cfg), nil
			}
		}
	}

	resolved, err := GetAvailable(preferred, backups...)
	if err != nil {
		return nil, err
	}
	if resolved.Name() == defaultACPName {
		configured := configuredACPAgent(cfg)
		if _, err := exec.LookPath(configured.CommandName()); err == nil {
			return configured, nil
		}
		return resolved, nil
	}

	return applyCommandOverrides(resolved, cfg), nil
}

func applyACPAgentConfigOverride(cfg *config.ACPAgentConfig, override *config.ACPAgentConfig) {
	if cfg == nil || override == nil {
		return
	}

	if name := strings.TrimSpace(override.Name); name != "" {
		cfg.Name = name
	}
	if command := strings.TrimSpace(override.Command); command != "" {
		cfg.Command = command
	}
	if len(override.Args) > 0 {
		cfg.Args = append([]string(nil), override.Args...)
	}
	if readOnlyMode := strings.TrimSpace(override.ReadOnlyMode); readOnlyMode != "" {
		cfg.ReadOnlyMode = readOnlyMode
	}
	if autoApproveMode := strings.TrimSpace(override.AutoApproveMode); autoApproveMode != "" {
		cfg.AutoApproveMode = autoApproveMode
	}
	if override.DisableModeNegotiation {
		cfg.DisableModeNegotiation = true
	}
	if cfg.DisableModeNegotiation {
		cfg.Mode = ""
	} else if mode := strings.TrimSpace(override.Mode); mode != "" {
		cfg.Mode = mode
	} else {
		// If mode is omitted, default to the effective read-only mode.
		cfg.Mode = cfg.ReadOnlyMode
	}
	if model := strings.TrimSpace(override.Model); model != "" {
		cfg.Model = model
	}
	if override.Timeout > 0 {
		cfg.Timeout = override.Timeout
	}
}

func init() {
	Register(NewACPAgent(""))
}
