package review

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
)

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected string to contain %q, but got:\n%s", substr, s)
	}
}

// commonMockAgent provides default no-op chaining methods
type commonMockAgent struct {
	self agent.Agent
}

func (a *commonMockAgent) WithReasoning(_ agent.ReasoningLevel) agent.Agent { return a.self }
func (a *commonMockAgent) WithAgentic(_ bool) agent.Agent                   { return a.self }
func (a *commonMockAgent) WithModel(_ string) agent.Agent                   { return a.self }

// failingSynthAgent always returns an error from Review,
// used to force synthesis fallback deterministically.
type failingSynthAgent struct {
	commonMockAgent
}

func newFailingSynthAgent() *failingSynthAgent {
	a := &failingSynthAgent{}
	a.self = a
	return a
}

func (a *failingSynthAgent) Name() string { return "failing-synth" }
func (a *failingSynthAgent) Review(
	_ context.Context, _, _, _ string, _ io.Writer,
) (string, error) {
	return "", errors.New("agent exploded")
}
func (a *failingSynthAgent) CommandLine() string { return "failing-synth" }

// capturingAgent records the gitRef passed to Review.
type capturingAgent struct {
	commonMockAgent
	capturedGitRef string
}

func newCapturingAgent() *capturingAgent {
	a := &capturingAgent{}
	a.self = a
	return a
}

func (a *capturingAgent) Name() string { return "capture" }
func (a *capturingAgent) Review(
	_ context.Context, _, gitRef, _ string, _ io.Writer,
) (string, error) {
	a.capturedGitRef = gitRef
	return "synthesized output", nil
}
func (a *capturingAgent) CommandLine() string { return "capture" }

func TestSynthesize_Formatting(t *testing.T) {
	tests := []struct {
		name          string
		results       []ReviewResult
		expectedErr   error
		expectedTexts []string
	}{
		{
			name: "AllFailed",
			results: []ReviewResult{
				{
					Agent:      "codex",
					ReviewType: "security",
					Status:     "failed",
					Error:      "agent crashed",
				},
			},
			expectedErr: ErrAllFailed,
			expectedTexts: []string{
				"Review Failed",
			},
		},
		{
			name: "SingleSuccess",
			results: []ReviewResult{
				{
					Agent:      "codex",
					ReviewType: "security",
					Status:     "done",
					Output:     "No issues found.",
				},
			},
			expectedErr: nil,
			expectedTexts: []string{
				"Review Passed",
				"No issues found.",
				"Agent: codex",
			},
		},
		{
			name: "AllQuota",
			results: []ReviewResult{
				{
					Agent:      "codex",
					ReviewType: "security",
					Status:     "failed",
					Error:      QuotaErrorPrefix + "exhausted",
				},
			},
			expectedErr: nil,
			expectedTexts: []string{
				"Review Skipped",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment, err := Synthesize(
				context.Background(), tt.results, SynthesizeOpts{
					HeadSHA: "abc123456789",
				})
			if !errors.Is(err, tt.expectedErr) {
				t.Fatalf("expected error %v, got: %v", tt.expectedErr, err)
			}
			for _, text := range tt.expectedTexts {
				assertContains(t, comment, text)
			}
		})
	}
}

func TestSynthesize_MultipleResults_FallsBackToRaw(t *testing.T) {
	// Register an agent that always fails so the test is
	// deterministic regardless of what other agents exist
	// in the global registry.
	ag := newFailingSynthAgent()
	agent.Register(ag)
	t.Cleanup(func() { agent.Unregister("failing-synth") })

	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "Found issue A",
		},
		{
			Agent:      "codex",
			ReviewType: "design",
			Status:     "done",
			Output:     "Design looks good",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			Agent:   "failing-synth",
			HeadSHA: "def456789012",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fall back to raw format
	assertContains(t, comment, "Synthesis unavailable")
	assertContains(t, comment, "Found issue A")
	assertContains(t, comment, "Design looks good")
}

func TestSynthesize_MixedSuccessAndFailure(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "Found vulnerability",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     "failed",
			Error:      "agent crashed",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			Agent:   "nonexistent-synthesis-agent",
			HeadSHA: "abc123456789",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fall back to raw format since synthesis agent
	// doesn't exist
	assertContains(t, comment, "Combined Review")
}

func TestSynthesize_PassesGitRefToAgent(t *testing.T) {
	cap := newCapturingAgent()
	agent.Register(cap)
	t.Cleanup(func() { agent.Unregister("capture") })

	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "Found issue A",
		},
		{
			Agent:      "codex",
			ReviewType: "design",
			Status:     "done",
			Output:     "Design looks good",
		},
	}

	_, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			Agent:   "capture",
			GitRef:  "aaa111..bbb222",
			HeadSHA: "bbb222",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.capturedGitRef != "aaa111..bbb222" {
		t.Errorf(
			"gitRef = %q, want %q",
			cap.capturedGitRef, "aaa111..bbb222")
	}
}
