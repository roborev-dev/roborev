package agent

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentSpecsResolveAliasesAndCanonicalNames(t *testing.T) {
	t.Parallel()

	for _, spec := range allAgentSpecs {
		assert.Equal(t, spec.Name, resolveAlias(spec.Name), "canonical name should resolve to itself: %s", spec.Name)
		for _, alias := range spec.Aliases {
			assert.Equal(t, spec.Name, resolveAlias(alias), "alias %s should resolve to %s", alias, spec.Name)
		}
	}
}

func TestAgentSpecsFallbackOrder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "kiro", "kilo", "droid", "pi"}, fallbackAgentOrder)
	assert.Equal(t, fallbackAgentOrder, installHintAgentNames())
}

func TestBuildFallbackAgentOrderValidatesMetadata(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(t, "invalid fallback rank for claude-code: got 3, want 2", func() {
		buildFallbackAgentOrder([]agentSpec{
			{Name: "codex", FallbackRank: 1},
			{Name: "claude-code", FallbackRank: 3},
		})
	})

	require.PanicsWithValue(t, "duplicate fallback rank 1 for claude-code and codex", func() {
		buildFallbackAgentOrder([]agentSpec{
			{Name: "codex", FallbackRank: 1},
			{Name: "claude-code", FallbackRank: 1},
		})
	})
}

func TestAgentSpecsCommandOverrides(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		CodexCmd:      "custom-codex",
		ClaudeCodeCmd: "custom-claude",
		CursorCmd:     "custom-cursor",
		PiCmd:         "custom-pi",
		OpenCodeCmd:   "custom-opencode",
	}

	tests := []struct {
		name            string
		expectedCommand string
	}{
		{name: "codex", expectedCommand: "custom-codex"},
		{name: "claude-code", expectedCommand: "custom-claude"},
		{name: "gemini", expectedCommand: "gemini"},
		{name: "copilot", expectedCommand: "copilot"},
		{name: "opencode", expectedCommand: "custom-opencode"},
		{name: "cursor", expectedCommand: "custom-cursor"},
		{name: "kiro", expectedCommand: "kiro-cli"},
		{name: "kilo", expectedCommand: "kilo"},
		{name: "droid", expectedCommand: "droid"},
		{name: "pi", expectedCommand: "custom-pi"},
		{name: "acp", expectedCommand: "acp-agent"},
		{name: "test", expectedCommand: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := Get(tt.name)
			require.NoError(t, err)

			overridden := applyCommandOverrides(agent, cfg)
			commandAgent, ok := overridden.(CommandAgent)
			if tt.expectedCommand == "" {
				assert.False(t, ok)
				return
			}

			require.True(t, ok)
			assert.Equal(t, tt.expectedCommand, commandAgent.CommandName())
		})
	}
}
