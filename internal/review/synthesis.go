package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/roborev-dev/roborev/internal/git"
)

// severityAbove maps a minimum severity to the instruction
// describing which levels to include in synthesis output.
var severityAbove = map[string]string{
	"critical": "Only include Critical findings.",
	"high":     "Only include High and Critical findings.",
	"medium":   "Only include Medium, High, and Critical findings.",
}

// BuildSynthesisPrompt creates the prompt for the synthesis agent.
// When minSeverity is non-empty (and not "low"), a filtering
// instruction is appended.
func BuildSynthesisPrompt(
	reviews []ReviewResult,
	minSeverity string,
) string {
	var b strings.Builder
	b.WriteString(
		"You are combining multiple code review outputs " +
			"into a single GitHub PR comment.\nRules:\n" +
			"- Deduplicate findings reported by multiple agents\n" +
			"- Organize by severity (Critical > High > Medium > Low)\n" +
			"- Preserve file/line references\n" +
			"- If all agents agree code is clean, say so concisely\n" +
			"- Start with a one-line summary verdict\n" +
			"- Use markdown formatting\n" +
			"- No preamble about yourself\n")

	if instruction, ok := severityAbove[minSeverity]; ok {
		b.WriteString(
			"- Omit findings below " + minSeverity +
				" severity. " + instruction + "\n")
	}

	b.WriteString("\n")

	// Truncate per-review output to avoid blowing the synthesis
	// agent's context window.
	const maxPerReview = 15000

	for i, r := range reviews {
		fmt.Fprintf(&b,
			"---\n### Review %d: Agent=%s, Type=%s",
			i+1, r.Agent, r.ReviewType)
		if IsQuotaFailure(r) {
			b.WriteString(" [SKIPPED]")
		} else if IsTimeoutCancellation(r) {
			b.WriteString(" [SKIPPED]")
		} else if r.Status == ResultFailed {
			b.WriteString(" [FAILED]")
		}
		b.WriteString("\n")
		if IsQuotaFailure(r) {
			b.WriteString(
				"(review skipped — agent quota exhausted)")
		} else if IsTimeoutCancellation(r) {
			b.WriteString(
				"(review skipped — batch posted early)")
		} else if r.Output != "" {
			output := r.Output
			if len(output) > maxPerReview {
				output = output[:maxPerReview] +
					"\n\n...(truncated)"
			}
			b.WriteString(output)
		} else if r.Status == ResultFailed {
			b.WriteString("(no output — review failed)")
		}
		b.WriteString("\n\n")
	}

	return b.String()
}

// FormatSynthesizedComment wraps synthesized output with header
// and metadata.
func FormatSynthesizedComment(
	output string,
	reviews []ReviewResult,
	headSHA string,
) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"## roborev: Combined Review (`%s`)\n\n",
		git.ShortSHA(headSHA))
	b.WriteString(output)

	agentSet := make(map[string]struct{})
	typeSet := make(map[string]struct{})
	for _, r := range reviews {
		if r.Agent != "" {
			agentSet[r.Agent] = struct{}{}
		}
		if r.ReviewType != "" {
			typeSet[r.ReviewType] = struct{}{}
		}
	}
	agents := sortedKeys(agentSet)
	types := sortedKeys(typeSet)

	fmt.Fprintf(&b,
		"\n\n---\n*Synthesized from %d reviews "+
			"(agents: %s | types: %s)*\n",
		len(reviews),
		strings.Join(agents, ", "),
		strings.Join(types, ", "))

	if note := SkippedAgentNote(reviews); note != "" {
		b.WriteString(note)
	}

	return b.String()
}

// FormatRawBatchComment formats all review outputs as expanded
// inline sections. Used as a fallback when synthesis fails.
func FormatRawBatchComment(
	reviews []ReviewResult,
	headSHA string,
) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"## roborev: Combined Review (`%s`)\n\n",
		git.ShortSHA(headSHA))
	b.WriteString(
		"> Synthesis unavailable. " +
			"Showing individual review outputs.\n\n")

	for i, r := range reviews {
		if i > 0 {
			b.WriteString("---\n\n")
		}
		status := r.Status
		if IsQuotaFailure(r) {
			status = "skipped (quota)"
		} else if IsTimeoutCancellation(r) {
			status = "skipped (timeout)"
		}
		fmt.Fprintf(&b, "### %s — %s (%s)\n\n",
			r.Agent, r.ReviewType, status)

		if IsQuotaFailure(r) {
			b.WriteString(
				"Review skipped — agent quota exhausted.\n\n")
		} else if IsTimeoutCancellation(r) {
			b.WriteString(
				"Review skipped — batch posted early.\n\n")
		} else if r.Status == ResultFailed || r.Status == "canceled" {
			b.WriteString(
				"**Error:** Review failed. " +
					"Check CI logs for details.\n\n")
		} else if r.Output != "" {
			output := r.Output
			const maxLen = 15000
			if len(output) > maxLen {
				output = output[:maxLen] +
					"\n\n...(truncated)"
			}
			b.WriteString(output)
			b.WriteString("\n\n")
		} else {
			b.WriteString("(no output)\n\n")
		}
	}

	if note := SkippedAgentNote(reviews); note != "" {
		b.WriteString(note)
	}

	return b.String()
}

// FormatAllFailedComment formats a comment when every job in a
// batch failed.
func FormatAllFailedComment(
	reviews []ReviewResult,
	headSHA string,
) string {
	quotaSkips := CountQuotaFailures(reviews)
	timeoutSkips := CountTimeoutCancellations(reviews)
	allSkipped := len(reviews) > 0 &&
		quotaSkips+timeoutSkips == len(reviews)

	var b strings.Builder
	if allSkipped {
		fmt.Fprintf(&b,
			"## roborev: Review Skipped (`%s`)\n\n",
			git.ShortSHA(headSHA))
		switch {
		case quotaSkips > 0 && timeoutSkips > 0:
			b.WriteString(
				"All review agents were skipped " +
					"(quota exhaustion and timeout).\n\n")
		case timeoutSkips > 0:
			b.WriteString(
				"All review agents were skipped " +
					"(batch posted early).\n\n")
		default:
			b.WriteString(
				"All review agents were skipped " +
					"due to quota exhaustion.\n\n")
		}
	} else {
		fmt.Fprintf(&b,
			"## roborev: Review Failed (`%s`)\n\n",
			git.ShortSHA(headSHA))
		b.WriteString(
			"All review jobs in this batch failed.\n\n")
	}

	for _, r := range reviews {
		if IsQuotaFailure(r) {
			fmt.Fprintf(&b,
				"- **%s** (%s): skipped (quota)\n",
				r.Agent, r.ReviewType)
		} else if IsTimeoutCancellation(r) {
			fmt.Fprintf(&b,
				"- **%s** (%s): skipped (timeout)\n",
				r.Agent, r.ReviewType)
		} else {
			fmt.Fprintf(&b,
				"- **%s** (%s): failed\n",
				r.Agent, r.ReviewType)
		}
	}

	if !allSkipped {
		b.WriteString("\nCheck CI logs for error details.")
	}

	if note := SkippedAgentNote(reviews); note != "" {
		b.WriteString(note)
	}

	return b.String()
}

// IsQuotaFailure returns true if a review's error indicates a
// quota skip rather than a real failure.
func IsQuotaFailure(r ReviewResult) bool {
	return r.Status == ResultFailed &&
		strings.HasPrefix(r.Error, QuotaErrorPrefix)
}

// CountQuotaFailures returns the number of reviews that failed
// due to agent quota exhaustion rather than a real error.
func CountQuotaFailures(reviews []ReviewResult) int {
	n := 0
	for _, r := range reviews {
		if IsQuotaFailure(r) {
			n++
		}
	}
	return n
}

// IsTimeoutCancellation returns true if a review was canceled
// because the batch timed out and posted early.
func IsTimeoutCancellation(r ReviewResult) bool {
	return r.Status == "canceled" &&
		strings.HasPrefix(r.Error, TimeoutErrorPrefix)
}

// CountTimeoutCancellations returns the number of reviews that
// were canceled due to batch timeout.
func CountTimeoutCancellations(reviews []ReviewResult) int {
	n := 0
	for _, r := range reviews {
		if IsTimeoutCancellation(r) {
			n++
		}
	}
	return n
}

// SkippedAgentNote returns a markdown note listing agents that
// were skipped due to quota or timeout. Returns "" if none.
func SkippedAgentNote(reviews []ReviewResult) string {
	quotaAgents := make(map[string]struct{})
	timeoutAgents := make(map[string]struct{})
	for _, r := range reviews {
		if IsQuotaFailure(r) {
			quotaAgents[r.Agent] = struct{}{}
		} else if IsTimeoutCancellation(r) {
			timeoutAgents[r.Agent] = struct{}{}
		}
	}

	var parts []string
	if len(quotaAgents) > 0 {
		names := sortedKeys(quotaAgents)
		parts = append(parts,
			strings.Join(names, ", ")+" (quota)")
	}
	if len(timeoutAgents) > 0 {
		names := sortedKeys(timeoutAgents)
		parts = append(parts,
			strings.Join(names, ", ")+" (timeout)")
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"\n*Note: %s review(s) skipped*\n",
		strings.Join(parts, "; "))
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
