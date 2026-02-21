package review

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/git"
)

// ErrAllFailed is returned by Synthesize when every review job
// in the batch failed (excluding all-quota-skipped batches).
var ErrAllFailed = errors.New(
	"all review jobs failed")

// SynthesizeOpts controls synthesis behavior.
type SynthesizeOpts struct {
	// Agent name for synthesis (empty = first available).
	Agent string
	// Model override for the synthesis agent.
	Model string
	// MinSeverity filters findings below this level.
	MinSeverity string
	// RepoPath is the working directory for the synthesis agent.
	RepoPath string
	// GitRef is the reviewed git ref, passed to the synthesis agent.
	GitRef string
	// HeadSHA is used for comment formatting headers.
	HeadSHA string
}

// Synthesize combines multiple review results into a single
// formatted comment string.
//
// Single successful result: returns it directly (no LLM call).
// All failed: returns failure comment.
// Multiple results with successes: runs synthesis agent, falls
// back to raw format on error.
func Synthesize(
	ctx context.Context,
	results []ReviewResult,
	opts SynthesizeOpts,
) (string, error) {
	successCount := 0
	for _, r := range results {
		if r.Status == ResultDone {
			successCount++
		}
	}

	// All failed
	if successCount == 0 {
		comment := FormatAllFailedComment(
			results, opts.HeadSHA)
		// All-quota is not an error (nothing actionable).
		quotaSkips := CountQuotaFailures(results)
		if len(results) > 0 && quotaSkips == len(results) {
			return comment, nil
		}
		return comment, ErrAllFailed
	}

	// Single result — return directly, no synthesis needed
	if len(results) == 1 && successCount == 1 {
		return formatSingleResult(
			results[0], opts.HeadSHA), nil
	}

	// Multiple results — synthesize with LLM
	comment, err := runSynthesis(ctx, results, opts)
	if err != nil {
		log.Printf(
			"ci review: synthesis failed: %v "+
				"(falling back to raw format)", err)
		return FormatRawBatchComment(
			results, opts.HeadSHA), nil
	}
	return comment, nil
}

func formatSingleResult(
	r ReviewResult,
	headSHA string,
) string {
	var header string
	if r.Output == "" || r.Output == "No issues found." {
		header = fmt.Sprintf(
			"## roborev: Review Passed (`%s`)\n\n",
			git.ShortSHA(headSHA))
	} else {
		header = fmt.Sprintf(
			"## roborev: Review Complete (`%s`)\n\n",
			git.ShortSHA(headSHA))
	}

	output := r.Output
	if len(output) > MaxCommentLen {
		output = output[:MaxCommentLen] +
			"\n\n...(truncated)"
	}

	return header + output + fmt.Sprintf(
		"\n\n---\n*Agent: %s | Type: %s*\n",
		r.Agent, r.ReviewType)
}

func runSynthesis(
	ctx context.Context,
	results []ReviewResult,
	opts SynthesizeOpts,
) (string, error) {
	synthAgent, err := agent.GetAvailable(opts.Agent)
	if err != nil {
		return "", fmt.Errorf("get synthesis agent: %w", err)
	}

	if opts.Model != "" {
		synthAgent = synthAgent.WithModel(opts.Model)
	}

	synthPrompt := BuildSynthesisPrompt(
		results, opts.MinSeverity)

	synthCtx, cancel := context.WithTimeout(
		ctx, 5*time.Minute)
	defer cancel()

	output, err := synthAgent.Review(
		synthCtx, opts.RepoPath, opts.GitRef, synthPrompt, nil)
	if err != nil {
		return "", fmt.Errorf("synthesis review: %w", err)
	}

	return FormatSynthesizedComment(
		output, results, opts.HeadSHA), nil
}
