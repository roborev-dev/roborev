//go:build integration

package main

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
)

func TestLocalReviewReasoningLevels(t *testing.T) {
	tests := []struct {
		name      string
		reasoning agent.ReasoningLevel
	}{
		{name: "Fast", reasoning: agent.ReasoningFast},
		{name: "Standard", reasoning: agent.ReasoningStandard},
		{name: "Thorough", reasoning: agent.ReasoningThorough},
		{name: "Default", reasoning: ""}, // default (agent defaults)
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHarness(t)
			err := h.run(runOpts{Agent: "test", Reasoning: tc.reasoning})
			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}

			expectedReasoning := tc.reasoning
			if expectedReasoning == "" {
				expectedReasoning = agent.ReasoningThorough
			}
			h.assertOutputContains("reasoning: " + string(expectedReasoning))
		})
	}
}
