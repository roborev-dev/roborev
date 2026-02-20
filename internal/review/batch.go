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
	Agents       []string // resolved agent names
	ReviewTypes  []string // resolved review types
	Reasoning    string
	ContextCount int
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

	// Resolve agent
	resolvedAgent, err := agent.GetAvailable(agentName)
	if err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf(
			"resolve agent %q: %v", agentName, err)
		return result
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
		result.Status = "failed"
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
		result.Status = "failed"
		result.Error = fmt.Sprintf(
			"agent review: %v", err)
		return result
	}

	result.Status = "done"
	result.Output = output
	return result
}
