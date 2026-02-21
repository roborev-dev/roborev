package review

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
)

// failingSynthAgent always returns an error from Review,
// used to force synthesis fallback deterministically.
type failingSynthAgent struct{}

func (a *failingSynthAgent) Name() string {
	return "failing-synth"
}
func (a *failingSynthAgent) Review(
	_ context.Context, _, _, _ string, _ io.Writer,
) (string, error) {
	return "", errors.New("agent exploded")
}
func (a *failingSynthAgent) WithReasoning(
	_ agent.ReasoningLevel,
) agent.Agent {
	return a
}
func (a *failingSynthAgent) WithAgentic(_ bool) agent.Agent {
	return a
}
func (a *failingSynthAgent) WithModel(_ string) agent.Agent {
	return a
}
func (a *failingSynthAgent) CommandLine() string {
	return "failing-synth"
}

// capturingAgent records the gitRef passed to Review.
type capturingAgent struct {
	capturedGitRef string
}

func (a *capturingAgent) Name() string { return "capture" }
func (a *capturingAgent) Review(
	_ context.Context, _, gitRef, _ string, _ io.Writer,
) (string, error) {
	a.capturedGitRef = gitRef
	return "synthesized output", nil
}
func (a *capturingAgent) WithReasoning(
	_ agent.ReasoningLevel,
) agent.Agent {
	return a
}
func (a *capturingAgent) WithAgentic(_ bool) agent.Agent {
	return a
}
func (a *capturingAgent) WithModel(_ string) agent.Agent {
	return a
}
func (a *capturingAgent) CommandLine() string {
	return "capture"
}

func TestSynthesize_AllFailed(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "failed",
			Error:      "agent crashed",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			HeadSHA: "abc123456789",
		})
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf(
			"expected ErrAllFailed, got: %v", err)
	}

	if !strings.Contains(comment, "Review Failed") {
		t.Error("expected 'Review Failed' in comment")
	}
}

func TestSynthesize_SingleSuccess(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "No issues found.",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			HeadSHA: "abc123456789",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(comment, "Review Passed") {
		t.Error("expected 'Review Passed' in comment")
	}
	if !strings.Contains(
		comment, "No issues found.") {
		t.Error("expected output in comment")
	}
	if !strings.Contains(comment, "Agent: codex") {
		t.Error("expected agent name in metadata")
	}
}

func TestSynthesize_MultipleResults_FallsBackToRaw(t *testing.T) {
	// Register an agent that always fails so the test is
	// deterministic regardless of what other agents exist
	// in the global registry.
	agent.Register(&failingSynthAgent{})

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
	if !strings.Contains(
		comment, "Synthesis unavailable") {
		t.Error(
			"expected raw fallback when synthesis fails")
	}
	if !strings.Contains(
		comment, "Found issue A") {
		t.Error("expected first review output")
	}
	if !strings.Contains(
		comment, "Design looks good") {
		t.Error("expected second review output")
	}
}

func TestSynthesize_AllQuota(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "failed",
			Error:      QuotaErrorPrefix + "exhausted",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			HeadSHA: "abc123456789",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(comment, "Review Skipped") {
		t.Error("expected 'Review Skipped' in comment")
	}
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
	if !strings.Contains(
		comment, "Combined Review") {
		t.Error("expected combined review header")
	}
}

func TestSynthesize_PassesGitRefToAgent(t *testing.T) {
	cap := &capturingAgent{}
	agent.Register(cap)

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
