package review

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// mockAgent implements agent.Agent for testing.
type mockAgent struct {
	name   string
	model  string
	output string
	err    error
}

func (m *mockAgent) Name() string { return m.name }
func (m *mockAgent) Review(
	_ context.Context, _, _, _ string, _ io.Writer,
) (string, error) {
	out := m.output
	if m.model != "" {
		out += " model=" + m.model
	}
	return out, m.err
}
func (m *mockAgent) WithReasoning(
	_ agent.ReasoningLevel,
) agent.Agent {
	return m
}
func (m *mockAgent) WithAgentic(_ bool) agent.Agent {
	return m
}
func (m *mockAgent) WithModel(model string) agent.Agent {
	c := *m
	c.model = model
	return &c
}
func (m *mockAgent) CommandLine() string {
	return m.name
}

// getResultByType is a helper to find a ReviewResult by its ReviewType
func getResultByType(t *testing.T, results []ReviewResult, rType string) ReviewResult {
	t.Helper()
	for _, r := range results {
		if r.ReviewType == rType {
			return r
		}
	}
	t.Fatalf("missing result for type %q", rType)
	return ReviewResult{}
}

func TestRunBatch_SingleJob(t *testing.T) {
	t.Parallel()
	cfg := BatchConfig{
		RepoPath:    t.TempDir(),
		GitRef:      "abc123",
		Agents:      []string{"test"},
		ReviewTypes: []string{"security"},
		AgentRegistry: map[string]agent.Agent{
			"test": &mockAgent{
				name:   "test",
				output: "looks good",
			},
		},
	}

	// RunBatch will fail at prompt building because there's no
	// real git repo, but we can verify it creates the right
	// number of jobs and handles errors gracefully.
	results := RunBatch(context.Background(), cfg)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Agent != "test" {
		t.Errorf("agent = %q, want %q", r.Agent, "test")
	}
	if r.ReviewType != "security" {
		t.Errorf(
			"reviewType = %q, want %q",
			r.ReviewType, "security")
	}
	// Without a real git repo, prompt building will fail
	if r.Status != "failed" {
		t.Errorf("status = %q, want 'failed'", r.Status)
	}
	if !strings.Contains(r.Error, "build prompt:") {
		t.Errorf("expected build prompt error, got %q", r.Error)
	}
}

func TestRunBatch_Matrix(t *testing.T) {
	t.Parallel()
	cfg := BatchConfig{
		RepoPath: t.TempDir(),
		GitRef:   "abc..def",
		Agents:   []string{"test"},
		ReviewTypes: []string{
			"security", "default",
		},
		AgentRegistry: map[string]agent.Agent{
			"test": &mockAgent{
				name:   "test",
				output: "ok",
			},
		},
	}

	results := RunBatch(context.Background(), cfg)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	getResultByType(t, results, "security")
	getResultByType(t, results, "default")
}

func TestRunBatch_AgentNotFound(t *testing.T) {
	t.Parallel()
	cfg := BatchConfig{
		RepoPath:      t.TempDir(),
		GitRef:        "abc123",
		Agents:        []string{"nonexistent-agent-xyz"},
		ReviewTypes:   []string{"security"},
		AgentRegistry: map[string]agent.Agent{}, // Empty mock registry
	}

	results := RunBatch(context.Background(), cfg)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != "failed" {
		t.Errorf("status = %q, want 'failed'", r.Status)
	}
	if r.Error == "" {
		t.Error("expected non-empty error message")
	}
	if !strings.Contains(r.Error, "no agents available (mock registry)") {
		t.Errorf("expected agent not found error, got %q", r.Error)
	}
}

func TestRunBatch_AgentFailure(t *testing.T) {
	t.Parallel()
	repo := testutil.NewTestRepoWithCommit(t)
	sha := repo.RevParse("HEAD")

	cfg := BatchConfig{
		RepoPath:    repo.Root,
		GitRef:      sha,
		Agents:      []string{"fail-agent"},
		ReviewTypes: []string{"security"},
		AgentRegistry: map[string]agent.Agent{
			"fail-agent": &mockAgent{
				name: "fail-agent",
				err:  fmt.Errorf("agent exploded"),
			},
		},
	}

	results := RunBatch(context.Background(), cfg)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != "failed" {
		t.Errorf("status = %q, want 'failed'", r.Status)
	}
	if !strings.Contains(r.Error, "agent exploded") {
		t.Errorf("expected agent exploded error, got %q", r.Error)
	}
}

func TestRunBatch_WorkflowAwareResolution(t *testing.T) {
	t.Parallel()
	// Configure security_agent override so security reviews
	// resolve to "security-agent" while default reviews use
	// the base agent.
	globalCfg := &config.Config{
		DefaultAgent:  "base-agent",
		SecurityAgent: "security-agent",
	}

	cfg := BatchConfig{
		RepoPath:     t.TempDir(),
		GitRef:       "abc..def",
		Agents:       []string{""},
		ReviewTypes:  []string{"default", "security"},
		GlobalConfig: globalCfg,
		AgentRegistry: map[string]agent.Agent{
			"base-agent": &mockAgent{
				name:   "base-agent",
				output: "base",
			},
			"security-agent": &mockAgent{
				name:   "security-agent",
				output: "security",
			},
		},
	}

	results := RunBatch(context.Background(), cfg)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Both will fail at prompt building (no real git repo),
	// but we can verify the resolved agent names.
	defResult := getResultByType(t, results, "default")
	secResult := getResultByType(t, results, "security")

	if defResult.Agent != "base-agent" {
		t.Errorf(
			"default type resolved to %q, want %q",
			defResult.Agent, "base-agent")
	}
	if secResult.Agent != "security-agent" {
		t.Errorf(
			"security type resolved to %q, want %q",
			secResult.Agent, "security-agent")
	}
}

func TestRunBatch_WorkflowModelResolution(t *testing.T) {
	t.Parallel()
	// Configure a security-specific model override.
	globalCfg := &config.Config{
		DefaultAgent:  "model-test-agent",
		SecurityModel: "sec-model-v2",
	}

	// Use a real git repo so prompt building succeeds
	// and Review() is called, making model observable.
	repo := testutil.NewTestRepoWithCommit(t)
	sha := repo.RevParse("HEAD")

	cfg := BatchConfig{
		RepoPath:     repo.Root,
		GitRef:       sha,
		Agents:       []string{""},
		ReviewTypes:  []string{"default", "security"},
		GlobalConfig: globalCfg,
		AgentRegistry: map[string]agent.Agent{
			"model-test-agent": &mockAgent{
				name:   "model-test-agent",
				output: "ok",
			},
		},
	}

	results := RunBatch(context.Background(), cfg)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Status != ResultDone {
			t.Fatalf(
				"type %s: status=%q err=%q",
				r.ReviewType, r.Status, r.Error)
		}
	}

	secOut := getResultByType(t, results, "security").Output
	defOut := getResultByType(t, results, "default").Output

	// Security review should have the model applied.
	if !strings.Contains(secOut, "model=sec-model-v2") {
		t.Errorf(
			"security output missing model, got %q",
			secOut)
	}
	// Default review should have no model override.
	if strings.Contains(defOut, "model=") {
		t.Errorf(
			"default output should have no model, got %q",
			defOut)
	}
}
