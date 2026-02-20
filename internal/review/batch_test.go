package review

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
)

// mockAgent implements agent.Agent for testing.
type mockAgent struct {
	name   string
	output string
	err    error
}

func (m *mockAgent) Name() string { return m.name }
func (m *mockAgent) Review(
	_ context.Context, _, _, _ string, _ io.Writer,
) (string, error) {
	return m.output, m.err
}
func (m *mockAgent) WithReasoning(
	_ agent.ReasoningLevel,
) agent.Agent {
	return m
}
func (m *mockAgent) WithAgentic(_ bool) agent.Agent {
	return m
}
func (m *mockAgent) WithModel(_ string) agent.Agent {
	return m
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
