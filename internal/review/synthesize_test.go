package review

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
)

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected string to contain %q, but got:\n%s", substr, s)
	}
}

type synthMockAgent struct {
	name       string
	reviewFunc func(ctx context.Context, typ, gitRef, prompt string, w io.Writer) (string, error)
}

func (m *synthMockAgent) Name() string { return m.name }

func (m *synthMockAgent) CommandLine() string                              { return m.name }
func (m *synthMockAgent) WithReasoning(_ agent.ReasoningLevel) agent.Agent { return m }
func (m *synthMockAgent) WithAgentic(_ bool) agent.Agent                   { return m }
func (m *synthMockAgent) WithModel(_ string) agent.Agent                   { return m }

func (m *synthMockAgent) Review(ctx context.Context, typ, gitRef, prompt string, w io.Writer) (string, error) {
	if m.reviewFunc != nil {
		return m.reviewFunc(ctx, typ, gitRef, prompt, w)
	}
	return "", nil
}

var defaultTestResults = []ReviewResult{
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
	ag := &synthMockAgent{
		name: "failing-synth",
		reviewFunc: func(_ context.Context, _, _, _ string, _ io.Writer) (string, error) {
			return "", errors.New("agent exploded")
		},
	}
	agent.Register(ag)
	t.Cleanup(func() { agent.Unregister("failing-synth") })

	comment, err := Synthesize(
		context.Background(), defaultTestResults, SynthesizeOpts{
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
	var capturedGitRef string
	cap := &synthMockAgent{
		name: "capture",
		reviewFunc: func(_ context.Context, _, gitRef, _ string, _ io.Writer) (string, error) {
			capturedGitRef = gitRef
			return "synthesized output", nil
		},
	}
	agent.Register(cap)
	t.Cleanup(func() { agent.Unregister("capture") })

	_, err := Synthesize(
		context.Background(), defaultTestResults, SynthesizeOpts{
			Agent:   "capture",
			GitRef:  "aaa111..bbb222",
			HeadSHA: "bbb222",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedGitRef != "aaa111..bbb222" {
		t.Errorf(
			"gitRef = %q, want %q",
			capturedGitRef, "aaa111..bbb222")
	}
}

func TestSynthesize_PassesGlobalConfigToResolver(t *testing.T) {
	var seenAgent string
	var seenCfg *config.Config
	cap := &synthMockAgent{
		name: "capture",
		reviewFunc: func(_ context.Context, _, _, _ string, _ io.Writer) (string, error) {
			return "synthesized output", nil
		},
	}

	cfg := &config.Config{
		ACP: &config.ACPAgentConfig{
			Name:    "custom-acp",
			Command: "acp-agent",
		},
	}

	comment, err := Synthesize(context.Background(), defaultTestResults, SynthesizeOpts{
		Agent:        "custom-acp",
		GlobalConfig: cfg,
		HeadSHA:      "abc123",
		GitRef:       "abc123..def456",
		Resolver: func(agentName string, c *config.Config) (agent.Agent, error) {
			seenAgent = agentName
			seenCfg = c
			return cap, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenAgent != "custom-acp" {
		t.Fatalf("resolver agent = %q, want %q", seenAgent, "custom-acp")
	}
	if seenCfg != cfg {
		t.Fatalf("resolver cfg pointer mismatch: got %p want %p", seenCfg, cfg)
	}
	assertContains(t, comment, "synthesized output")
}
