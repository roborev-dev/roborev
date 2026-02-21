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

func TestRunBatch_SingleJob(t *testing.T) {
	// Register a test agent
	agent.Register(&mockAgent{
		name:   "test",
		output: "looks good",
	})

	cfg := BatchConfig{
		RepoPath:    t.TempDir(),
		GitRef:      "abc123",
		Agents:      []string{"test"},
		ReviewTypes: []string{"security"},
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
}

func TestRunBatch_Matrix(t *testing.T) {
	agent.Register(&mockAgent{
		name:   "test",
		output: "ok",
	})

	cfg := BatchConfig{
		RepoPath: t.TempDir(),
		GitRef:   "abc..def",
		Agents:   []string{"test"},
		ReviewTypes: []string{
			"security", "default",
		},
	}

	results := RunBatch(context.Background(), cfg)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	types := map[string]bool{}
	for _, r := range results {
		types[r.ReviewType] = true
	}
	if !types["security"] || !types["default"] {
		t.Errorf(
			"expected both review types, got %v", types)
	}
}

func TestRunBatch_AgentNotFound(t *testing.T) {
	cfg := BatchConfig{
		RepoPath:    t.TempDir(),
		GitRef:      "abc123",
		Agents:      []string{"nonexistent-agent-xyz"},
		ReviewTypes: []string{"security"},
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
}

func TestRunBatch_AgentFailure(t *testing.T) {
	agent.Register(&mockAgent{
		name: "fail-agent",
		err:  fmt.Errorf("agent exploded"),
	})

	cfg := BatchConfig{
		RepoPath:    t.TempDir(),
		GitRef:      "abc123",
		Agents:      []string{"fail-agent"},
		ReviewTypes: []string{"security"},
	}

	results := RunBatch(context.Background(), cfg)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != "failed" {
		t.Errorf("status = %q, want 'failed'", r.Status)
	}
}

func TestRunBatch_WorkflowAwareResolution(t *testing.T) {
	// Register two distinct agents.
	agent.Register(&mockAgent{
		name:   "base-agent",
		output: "base",
	})
	agent.Register(&mockAgent{
		name:   "security-agent",
		output: "security",
	})

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
	}

	results := RunBatch(context.Background(), cfg)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Both will fail at prompt building (no real git repo),
	// but we can verify the resolved agent names.
	agentByType := map[string]string{}
	for _, r := range results {
		agentByType[r.ReviewType] = r.Agent
	}
	if agentByType["default"] != "base-agent" {
		t.Errorf(
			"default type resolved to %q, want %q",
			agentByType["default"], "base-agent")
	}
	if agentByType["security"] != "security-agent" {
		t.Errorf(
			"security type resolved to %q, want %q",
			agentByType["security"], "security-agent")
	}
}

func TestRunBatch_WorkflowModelResolution(t *testing.T) {
	agent.Register(&mockAgent{
		name:   "model-test-agent",
		output: "ok",
	})

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

	outputByType := map[string]string{}
	for _, r := range results {
		outputByType[r.ReviewType] = r.Output
	}

	secOut, ok := outputByType["security"]
	if !ok {
		t.Fatal("missing result for security review type")
	}
	defOut, ok := outputByType["default"]
	if !ok {
		t.Fatal("missing result for default review type")
	}

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
