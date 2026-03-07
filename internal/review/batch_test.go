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

func TestRunBatch(t *testing.T) {
	t.Parallel()
	repo := testutil.NewTestRepoWithCommit(t)
	sha := repo.RevParse("HEAD")

	tests := []struct {
		name         string
		cfg          func() BatchConfig
		wantResults  int
		wantStatus   string
		wantError    string
		validateFunc func(*testing.T, []ReviewResult)
	}{
		{
			name: "single job",
			cfg: func() BatchConfig {
				return BatchConfig{
					RepoPath:    repo.Root,
					GitRef:      sha,
					Agents:      []string{"test"},
					ReviewTypes: []string{"security"},
					AgentRegistry: map[string]agent.Agent{
						"test": &mockAgent{
							name:   "test",
							output: "looks good",
						},
					},
				}
			},
			wantResults: 1,
			wantStatus:  ResultDone,
			validateFunc: func(t *testing.T, res []ReviewResult) {
				if res[0].Agent != "test" {
					t.Errorf("agent = %q, want %q", res[0].Agent, "test")
				}
				if res[0].ReviewType != "security" {
					t.Errorf("reviewType = %q, want %q", res[0].ReviewType, "security")
				}
			},
		},
		{
			name: "matrix",
			cfg: func() BatchConfig {
				return BatchConfig{
					RepoPath:    repo.Root,
					GitRef:      sha,
					Agents:      []string{"test"},
					ReviewTypes: []string{"security", "default"},
					AgentRegistry: map[string]agent.Agent{
						"test": &mockAgent{
							name:   "test",
							output: "ok",
						},
					},
				}
			},
			wantResults: 2,
			wantStatus:  ResultDone,
			validateFunc: func(t *testing.T, res []ReviewResult) {
				getResultByType(t, res, "security")
				getResultByType(t, res, "default")
			},
		},
		{
			name: "agent not found",
			cfg: func() BatchConfig {
				return BatchConfig{
					RepoPath:      repo.Root,
					GitRef:        sha,
					Agents:        []string{"nonexistent-agent-xyz"},
					ReviewTypes:   []string{"security"},
					AgentRegistry: map[string]agent.Agent{},
				}
			},
			wantResults: 1,
			wantStatus:  ResultFailed,
			wantError:   "no agents available (mock registry)",
		},
		{
			name: "agent failure",
			cfg: func() BatchConfig {
				return BatchConfig{
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
			},
			wantResults: 1,
			wantStatus:  ResultFailed,
			wantError:   "agent exploded",
		},
		{
			name: "workflow aware resolution",
			cfg: func() BatchConfig {
				return BatchConfig{
					RepoPath:    repo.Root,
					GitRef:      sha,
					Agents:      []string{""},
					ReviewTypes: []string{"default", "security"},
					GlobalConfig: &config.Config{
						DefaultAgent:  "base-agent",
						SecurityAgent: "security-agent",
					},
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
			},
			wantResults: 2,
			wantStatus:  ResultDone,
			validateFunc: func(t *testing.T, res []ReviewResult) {
				defResult := getResultByType(t, res, "default")
				secResult := getResultByType(t, res, "security")

				if defResult.Agent != "base-agent" {
					t.Errorf("default type resolved to %q, want %q", defResult.Agent, "base-agent")
				}
				if secResult.Agent != "security-agent" {
					t.Errorf("security type resolved to %q, want %q", secResult.Agent, "security-agent")
				}
			},
		},
		{
			name: "workflow model resolution",
			cfg: func() BatchConfig {
				return BatchConfig{
					RepoPath:    repo.Root,
					GitRef:      sha,
					Agents:      []string{""},
					ReviewTypes: []string{"default", "security"},
					GlobalConfig: &config.Config{
						DefaultAgent:  "model-test-agent",
						SecurityModel: "sec-model-v2",
					},
					AgentRegistry: map[string]agent.Agent{
						"model-test-agent": &mockAgent{
							name:   "model-test-agent",
							output: "ok",
						},
					},
				}
			},
			wantResults: 2,
			wantStatus:  ResultDone,
			validateFunc: func(t *testing.T, res []ReviewResult) {
				secOut := getResultByType(t, res, "security").Output
				defOut := getResultByType(t, res, "default").Output

				if !strings.Contains(secOut, "model=sec-model-v2") {
					t.Errorf("security output missing model, got %q", secOut)
				}
				if strings.Contains(defOut, "model=") {
					t.Errorf("default output should have no model, got %q", defOut)
				}
			},
		},
		{
			name: "build prompt failure records agent",
			cfg: func() BatchConfig {
				return BatchConfig{
					RepoPath:    t.TempDir(),
					GitRef:      "HEAD",
					Agents:      []string{""},
					ReviewTypes: []string{"security"},
					GlobalConfig: &config.Config{
						SecurityAgent: "security-agent",
					},
					AgentRegistry: map[string]agent.Agent{
						"security-agent": &mockAgent{
							name: "security-agent",
						},
					},
				}
			},
			wantResults: 1,
			wantStatus:  ResultFailed,
			wantError:   "build prompt:",
			validateFunc: func(t *testing.T, res []ReviewResult) {
				if res[0].Agent != "security-agent" {
					t.Errorf("agent = %q, want %q even on prompt build failure", res[0].Agent, "security-agent")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg()
			results := RunBatch(context.Background(), cfg)

			if len(results) != tt.wantResults {
				t.Fatalf("expected %d results, got %d", tt.wantResults, len(results))
			}

			if tt.wantResults > 0 {
				for i, r := range results {
					if r.Status != tt.wantStatus {
						t.Errorf("result %d: want status %q, got %q", i, tt.wantStatus, r.Status)
					}
					if tt.wantError != "" && !strings.Contains(r.Error, tt.wantError) {
						t.Errorf("result %d: want error containing %q, got %q", i, tt.wantError, r.Error)
					}
				}
			}

			if tt.validateFunc != nil {
				tt.validateFunc(t, results)
			}
		})
	}
}
