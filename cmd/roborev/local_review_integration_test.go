//go:build integration

package main

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
)

func TestLocalReviewReasoningLevels(t *testing.T) {
	tests := []struct {
		name      string
		reasoning string
		expected  string
	}{
		{name: "Fast", reasoning: string(agent.ReasoningFast), expected: "reasoning: " + string(agent.ReasoningFast)},
		{name: "Standard", reasoning: string(agent.ReasoningStandard), expected: "reasoning: " + string(agent.ReasoningStandard)},
		{name: "Thorough", reasoning: string(agent.ReasoningThorough), expected: "reasoning: " + string(agent.ReasoningThorough)},
		{name: "Default", reasoning: "", expected: "reasoning: " + string(agent.ReasoningThorough)}, // default (agent defaults)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHarness(t)
			err := h.run(runOpts{Agent: "test", Reasoning: tc.reasoning})
			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}
			h.assertOutputContains(tc.expected)
		})
	}
}
