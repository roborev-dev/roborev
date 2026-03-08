package agent

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
)

func TestResolveWorkflowModelForAgentSkipsGenericDefaultModel(t *testing.T) {
	t.Parallel()

	mkCfg := func(workflow string, workflowAgent string, workflowModel string) *config.Config {
		cfg := &config.Config{
			DefaultAgent: "codex",
			DefaultModel: "gpt-5.4",
		}
		switch workflow {
		case "fix":
			cfg.FixAgent = workflowAgent
			cfg.FixModel = workflowModel
		case "review":
			cfg.ReviewAgent = workflowAgent
			cfg.ReviewModel = workflowModel
		case "refine":
			cfg.RefineAgent = workflowAgent
			cfg.RefineModel = workflowModel
		case "security":
			cfg.SecurityAgent = workflowAgent
			cfg.SecurityModel = workflowModel
		case "design":
			cfg.DesignAgent = workflowAgent
			cfg.DesignModel = workflowModel
		default:
			t.Fatalf("unsupported workflow %q", workflow)
		}
		return cfg
	}

	tests := []struct {
		name          string
		workflow      string
		selectedAgent string
		cfg           *config.Config
		want          string
	}{
		{
			name:          "fix skips default model for configured non-default agent",
			workflow:      "fix",
			selectedAgent: "claude-code",
			cfg:           mkCfg("fix", "claude", ""),
			want:          "",
		},
		{
			name:          "fix uses workflow model for configured non-default agent",
			workflow:      "fix",
			selectedAgent: "claude-code",
			cfg:           mkCfg("fix", "claude", "claude-sonnet"),
			want:          "claude-sonnet",
		},
		{
			name:          "fix uses default model for actual fallback default agent",
			workflow:      "fix",
			selectedAgent: "codex",
			cfg:           mkCfg("fix", "claude", ""),
			want:          "gpt-5.4",
		},
		{
			name:          "review skips default model for configured non-default agent",
			workflow:      "review",
			selectedAgent: "claude-code",
			cfg:           mkCfg("review", "claude", ""),
			want:          "",
		},
		{
			name:          "review uses workflow model for configured non-default agent",
			workflow:      "review",
			selectedAgent: "claude-code",
			cfg:           mkCfg("review", "claude", "claude-sonnet"),
			want:          "claude-sonnet",
		},
		{
			name:          "review uses default model for actual fallback default agent",
			workflow:      "review",
			selectedAgent: "codex",
			cfg:           mkCfg("review", "claude", ""),
			want:          "gpt-5.4",
		},
		{
			name:          "refine skips default model for configured non-default agent",
			workflow:      "refine",
			selectedAgent: "claude-code",
			cfg:           mkCfg("refine", "claude", ""),
			want:          "",
		},
		{
			name:          "refine uses workflow model for configured non-default agent",
			workflow:      "refine",
			selectedAgent: "claude-code",
			cfg:           mkCfg("refine", "claude", "claude-sonnet"),
			want:          "claude-sonnet",
		},
		{
			name:          "refine uses default model for actual fallback default agent",
			workflow:      "refine",
			selectedAgent: "codex",
			cfg:           mkCfg("refine", "claude", ""),
			want:          "gpt-5.4",
		},
		{
			name:          "security skips default model for configured non-default agent",
			workflow:      "security",
			selectedAgent: "claude-code",
			cfg:           mkCfg("security", "claude", ""),
			want:          "",
		},
		{
			name:          "security uses workflow model for configured non-default agent",
			workflow:      "security",
			selectedAgent: "claude-code",
			cfg:           mkCfg("security", "claude", "claude-sonnet"),
			want:          "claude-sonnet",
		},
		{
			name:          "security uses default model for actual fallback default agent",
			workflow:      "security",
			selectedAgent: "codex",
			cfg:           mkCfg("security", "claude", ""),
			want:          "gpt-5.4",
		},
		{
			name:          "design skips default model for configured non-default agent",
			workflow:      "design",
			selectedAgent: "claude-code",
			cfg:           mkCfg("design", "claude", ""),
			want:          "",
		},
		{
			name:          "design uses workflow model for configured non-default agent",
			workflow:      "design",
			selectedAgent: "claude-code",
			cfg:           mkCfg("design", "claude", "claude-sonnet"),
			want:          "claude-sonnet",
		},
		{
			name:          "design uses default model for actual fallback default agent",
			workflow:      "design",
			selectedAgent: "codex",
			cfg:           mkCfg("design", "claude", ""),
			want:          "gpt-5.4",
		},
	}

	repoPath := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveWorkflowModelForAgent(
				tt.selectedAgent,
				"",
				repoPath,
				tt.cfg,
				tt.workflow,
				"standard",
			)
			if got != tt.want {
				t.Fatalf("ResolveWorkflowModelForAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}
