package agent

import (
	"strings"

	"github.com/roborev-dev/roborev/internal/config"
)

// ResolveWorkflowModelForAgent resolves a workflow model for the actual
// agent that will run. If that agent differs from the generic default
// agent and no explicit model was provided, generic default_model is
// skipped so the selected agent can keep its own built-in default unless
// a workflow-specific model override exists.
func ResolveWorkflowModelForAgent(
	selectedAgent, cliModel, repoPath string,
	globalCfg *config.Config,
	workflow, level string,
) string {
	if s := strings.TrimSpace(cliModel); s != "" {
		return config.ResolveModelForWorkflow(
			s, repoPath, globalCfg, workflow, level,
		)
	}

	selectedAgent = strings.TrimSpace(selectedAgent)
	if selectedAgent == "" {
		return config.ResolveModelForWorkflow(
			"", repoPath, globalCfg, workflow, level,
		)
	}

	defaultAgent := config.ResolveAgent("", repoPath, globalCfg)
	if CanonicalName(selectedAgent) != CanonicalName(defaultAgent) {
		return config.ResolveWorkflowModel(
			repoPath, globalCfg, workflow, level,
		)
	}

	return config.ResolveModelForWorkflow(
		"", repoPath, globalCfg, workflow, level,
	)
}
