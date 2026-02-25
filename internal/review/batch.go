package review

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/prompt"
)

// BatchConfig holds parameters for a parallel review batch.
type BatchConfig struct {
	RepoPath     string
	GitRef       string   // "BASE..HEAD" range
	Agents       []string // agent names (resolved per-job)
	ReviewTypes  []string // resolved review types
	Reasoning    string
	ContextCount int
	// GlobalConfig enables workflow-aware agent/model resolution.
	// When set, each job resolves its agent and model through
	// config.ResolveAgentForWorkflow / ResolveModelForWorkflow,
	// matching the CI poller's behavior. When nil, agents are
	// used as-is (backward compatible).
	GlobalConfig *config.Config
	// AgentRegistry is an optional registry for dependency injection in testing.
	// If nil, the global agent registry is used.
	AgentRegistry map[string]agent.Agent
}

// RunBatch executes all review_type x agent combinations in
// parallel. Uses goroutines + sync.WaitGroup, no daemon/database.
func RunBatch(
	ctx context.Context,
	cfg BatchConfig,
) []ReviewResult {
	type job struct {
		agent      string
		reviewType string
	}

	var jobs []job
	for _, rt := range cfg.ReviewTypes {
		for _, ag := range cfg.Agents {
			jobs = append(jobs, job{
				agent:      ag,
				reviewType: rt,
			})
		}
	}

	results := make([]ReviewResult, len(jobs))
	var wg sync.WaitGroup

	for i, j := range jobs {
		wg.Add(1)
		go func(idx int, j job) {
			defer wg.Done()
			results[idx] = runSingle(
				ctx, cfg, j.agent, j.reviewType)
		}(i, j)
	}

	wg.Wait()
	return results
}

func runSingle(
	ctx context.Context,
	cfg BatchConfig,
	agentName string,
	reviewType string,
) ReviewResult {
	result := ReviewResult{
		Agent:      agentName,
		ReviewType: reviewType,
	}

	// Map review type to workflow name for config
	// resolution (same mapping as CI poller).
	workflow := "review"
	if !config.IsDefaultReviewType(reviewType) {
		workflow = reviewType
	}

	// Workflow-aware agent/model resolution when config
	// is available; otherwise use the agent name as-is.
	resolvedName := agentName
	var model string
	if cfg.GlobalConfig != nil {
		resolvedName = config.ResolveAgentForWorkflow(
			agentName, cfg.RepoPath,
			cfg.GlobalConfig, workflow, cfg.Reasoning)
		model = config.ResolveModelForWorkflow(
			"", cfg.RepoPath,
			cfg.GlobalConfig, workflow, cfg.Reasoning)
	}

	var resolvedAgent agent.Agent
	var err error
	if cfg.AgentRegistry != nil {
		if a, ok := cfg.AgentRegistry[resolvedName]; ok {
			resolvedAgent = a
		} else {
			err = fmt.Errorf("no agents available (mock registry)")
		}
	} else {
		resolvedAgent, err = agent.GetAvailable(resolvedName)
	}

	if err != nil {
		result.Status = ResultFailed
		result.Error = fmt.Sprintf(
			"resolve agent %q: %v",
			resolvedName, err)
		return result
	}

	// Apply model override
	if model != "" {
		resolvedAgent = resolvedAgent.WithModel(model)
	}

	// Apply reasoning level
	if cfg.Reasoning != "" {
		resolvedAgent = resolvedAgent.WithReasoning(
			agent.ParseReasoningLevel(cfg.Reasoning))
	}

	// Record the resolved agent name
	result.Agent = resolvedAgent.Name()

	// Build prompt (nil DB = no previous review context)
	builder := prompt.NewBuilder(nil)

	// Normalize review type for prompt building
	promptReviewType := reviewType
	if config.IsDefaultReviewType(reviewType) {
		promptReviewType = ""
	}

	reviewPrompt, err := builder.Build(
		cfg.RepoPath, cfg.GitRef, 0, cfg.ContextCount,
		resolvedAgent.Name(), promptReviewType)
	if err != nil {
		result.Status = ResultFailed
		result.Error = fmt.Sprintf(
			"build prompt: %v", err)
		return result
	}

	// Run review
	log.Printf(
		"ci review: running agent=%s type=%s ref=%s",
		resolvedAgent.Name(), reviewType, cfg.GitRef)

	output, err := resolvedAgent.Review(
		ctx, cfg.RepoPath, cfg.GitRef, reviewPrompt, nil)
	if err != nil {
		result.Status = ResultFailed
		result.Error = fmt.Sprintf(
			"agent review: %v", err)
		return result
	}

	result.Status = ResultDone
	result.Output = output
	return result
}
