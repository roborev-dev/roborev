package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKiloModelFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		wantModel    bool
		wantContains string
	}{
		{name: "no model omits flag", model: ""},
		{
			name:         "explicit model includes flag",
			model:        "anthropic/claude-sonnet-4-20250514",
			wantModel:    true,
			wantContains: "anthropic/claude-sonnet-4-20250514",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewKiloAgent("kilo")
			a.Model = tt.model
			cl := a.CommandLine()
			if tt.wantModel {
				assert.Contains(t, cl, "--model")
				assert.Contains(t, cl, tt.wantContains)
			} else {
				assert.NotContains(t, cl, "--model")
			}
		})
	}
}

func TestKiloAgenticAutoFlag(t *testing.T) {
	withUnsafeAgents(t, false)

	a := NewKiloAgent("kilo").WithAgentic(true).(*KiloAgent)
	assert.Contains(t, a.CommandLine(), "--auto")

	b := NewKiloAgent("kilo").WithAgentic(false).(*KiloAgent)
	assert.NotContains(t, b.CommandLine(), "--auto")
}

func TestKiloVariantFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		reasoning   ReasoningLevel
		wantVariant bool
		wantValue   string
	}{
		{name: "thorough maps to high", reasoning: ReasoningThorough, wantVariant: true, wantValue: "high"},
		{name: "fast maps to minimal", reasoning: ReasoningFast, wantVariant: true, wantValue: "minimal"},
		{name: "standard omits variant", reasoning: ReasoningStandard},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewKiloAgent("kilo").WithReasoning(tt.reasoning).(*KiloAgent)
			cl := a.CommandLine()
			if tt.wantVariant {
				assert.Contains(t, cl, "--variant")
				assert.Contains(t, cl, tt.wantValue)
			} else {
				assert.NotContains(t, cl, "--variant")
			}
		})
	}
}

func TestKiloUsesJSONFormat(t *testing.T) {
	t.Parallel()
	a := NewKiloAgent("kilo")
	assert.Contains(t, a.CommandLine(), "--format json")
}
