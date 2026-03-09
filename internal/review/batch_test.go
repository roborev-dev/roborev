package review

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.Condition(t, func() bool { return false }, "missing result for type %q", rType)
	return ReviewResult{}
}

func TestRunBatch_SingleJob(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

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

	require.Len(results, 1, "expected 1 result")

	r := results[0]
	assert.Equal("test", r.Agent, "agent = %q, want %q", r.Agent, "test")
	assert.Equal("security", r.ReviewType, "reviewType = %q, want %q", r.ReviewType, "security")
	// Without a real git repo, prompt building will fail
	assert.Equal("failed", r.Status, "status = %q, want 'failed'", r.Status)
	assert.Contains(r.Error, "build prompt:", "expected build prompt error, got %q", r.Error)
}

func TestRunBatch_Matrix(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

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

	require.Len(results, 2, "expected 2 results")

	assert.NotEmpty(getResultByType(t, results, "security"))
	assert.NotEmpty(getResultByType(t, results, "default"))
}

func TestRunBatch_AgentNotFound(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Parallel()
	cfg := BatchConfig{
		RepoPath:      t.TempDir(),
		GitRef:        "abc123",
		Agents:        []string{"nonexistent-agent-xyz"},
		ReviewTypes:   []string{"security"},
		AgentRegistry: map[string]agent.Agent{}, // Empty mock registry
	}

	results := RunBatch(context.Background(), cfg)

	require.Len(results, 1, "expected 1 result")
	r := results[0]
	assert.Equal("failed", r.Status, "status = %q, want 'failed'", r.Status)
	assert.NotEmpty(r.Error)
	assert.Contains(r.Error, "no agents available (mock registry)", "expected agent not found error, got %q", r.Error)
}

func TestRunBatch_AgentFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

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

	require.Len(results, 1, "expected 1 result")
	r := results[0]
	assert.Equal("failed", r.Status, "status = %q, want 'failed'", r.Status)
	assert.Contains(r.Error, "agent exploded", "expected agent exploded error, got %q", r.Error)
}

func TestRunBatch_WorkflowAwareResolution(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

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
	require.Len(results, 2, "expected 2 results")

	// Both will fail at prompt building (no real git repo),
	// but we can verify the resolved agent names.
	defResult := getResultByType(t, results, "default")
	secResult := getResultByType(t, results, "security")

	assert.Equal("base-agent", defResult.Agent, "default type resolved to %q, want %q", defResult.Agent, "base-agent")
	assert.Equal("security-agent", secResult.Agent, "security type resolved to %q, want %q", secResult.Agent, "security-agent")
}

func TestRunBatch_WorkflowModelResolution(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

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
	require.Len(results, 2, "expected 2 results")

	for _, r := range results {
		require.Equal(ResultDone, r.Status, "type %s: status=%q err=%q", r.ReviewType, r.Status, r.Error)
	}

	secOut := getResultByType(t, results, "security").Output
	defOut := getResultByType(t, results, "default").Output

	// Security review should have the model applied.
	assert.Contains(secOut, "model=sec-model-v2", "security output missing model, got %q", secOut)
	// Default review should have no model override.
	assert.NotContains(defOut, "model=", "default output should have no model, got %q", defOut)
}

func TestRunBatch_BackupKeepsOwnModelWhenBackupModelUnset(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Parallel()

	repo := testutil.NewTestRepoWithCommit(t)
	sha := repo.RevParse("HEAD")

	cfg := BatchConfig{
		RepoPath:    repo.Root,
		GitRef:      sha,
		Agents:      []string{""},
		ReviewTypes: []string{"review"},
		GlobalConfig: &config.Config{
			DefaultAgent:      "default-agent",
			ReviewAgent:       "primary-agent",
			ReviewBackupAgent: "backup-agent",
			ReviewModel:       "primary-model",
		},
		AgentRegistry: map[string]agent.Agent{
			// Simulate the runtime-selected backup agent while keeping
			// the configured preferred agent name distinct.
			"primary-agent": &mockAgent{
				name:   "backup-agent",
				output: "ok",
			},
		},
	}

	results := RunBatch(context.Background(), cfg)
	require.Len(results, 1, "expected 1 result")

	result := results[0]
	require.Equal(ResultDone, result.Status, "status=%q err=%q", result.Status, result.Error)
	assert.NotContains(result.Output, "model=", "backup agent should keep its default model, got %q", result.Output)
}

func TestRunBatch_BackupKeepsOwnModelWhenBackupModelUnset(t *testing.T) {
	t.Parallel()

	repo := testutil.NewTestRepoWithCommit(t)
	sha := repo.RevParse("HEAD")

	cfg := BatchConfig{
		RepoPath:    repo.Root,
		GitRef:      sha,
		Agents:      []string{""},
		ReviewTypes: []string{"review"},
		GlobalConfig: &config.Config{
			DefaultAgent:      "default-agent",
			ReviewAgent:       "primary-agent",
			ReviewBackupAgent: "backup-agent",
			ReviewModel:       "primary-model",
		},
		AgentRegistry: map[string]agent.Agent{
			// Simulate the runtime-selected backup agent while keeping
			// the configured preferred agent name distinct.
			"primary-agent": &mockAgent{
				name:   "backup-agent",
				output: "ok",
			},
		},
	}

	results := RunBatch(context.Background(), cfg)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Status != ResultDone {
		t.Fatalf("status=%q err=%q", result.Status, result.Error)
	}
	if strings.Contains(result.Output, "model=") {
		t.Fatalf("backup agent should keep its default model, got %q", result.Output)
	}
}
