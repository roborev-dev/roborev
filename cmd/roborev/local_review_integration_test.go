//go:build integration

package main

import "testing"

func TestLocalReviewReasoningLevels(t *testing.T) {
	tests := []struct {
		name      string
		reasoning string
		expected  string
	}{
		{"Fast", "fast", "reasoning: fast"},
		{"Standard", "standard", "reasoning: standard"},
		{"Thorough", "thorough", "reasoning: thorough"},
		{"Default", "", "reasoning: thorough"}, // default (agent defaults)
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
