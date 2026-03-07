package review

import (
	"strings"
	"testing"
)

func TestBuildSynthesisPrompt(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
		{Agent: "gemini", ReviewType: "review", Output: "Consider error handling in foo.go:42", Status: "done"},
		{Agent: "codex", ReviewType: "review", Status: "failed", Error: "timeout"},
	}

	prompt := BuildSynthesisPrompt(reviews, "")

	assertContainsAll(t, prompt, []string{
		"Deduplicate findings",
		"Organize by severity",
		"### Review 1: Agent=codex, Type=security",
		"### Review 2: Agent=gemini, Type=review",
		"[FAILED]",
		"No issues found.",
		"foo.go:42",
		"(no output",
	})
}

func TestBuildSynthesisPrompt_TruncatesLargeOutputs(t *testing.T) {
	largeOutput := strings.Repeat("x", 20000)
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security", Output: largeOutput, Status: "done"},
	}

	prompt := BuildSynthesisPrompt(reviews, "")

	if len(prompt) > 16500 { // 15k truncated + headers/instructions
		t.Errorf("synthesis prompt too large (%d chars), expected truncation", len(prompt))
	}
	if !strings.Contains(prompt, "...(truncated)") {
		t.Error("expected truncation marker in synthesis prompt")
	}
}

func TestBuildSynthesisPrompt_SanitizesErrors(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security", Status: "failed", Error: "secret-token-abc123: auth error"},
	}
	prompt := BuildSynthesisPrompt(reviews, "")
	if strings.Contains(prompt, "secret-token-abc123") {
		t.Error("raw error text should not appear in synthesis prompt")
	}
	if !strings.Contains(prompt, "[FAILED]") {
		t.Error("expected [FAILED] marker in synthesis prompt")
	}
}

func TestBuildSynthesisPrompt_WithMinSeverity(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
	}

	t.Run("no filter when empty", func(t *testing.T) {
		prompt := BuildSynthesisPrompt(reviews, "")
		if strings.Contains(prompt, "Omit findings below") {
			t.Error("expected no severity filter instruction when minSeverity is empty")
		}
	})

	t.Run("no filter when low", func(t *testing.T) {
		prompt := BuildSynthesisPrompt(reviews, "low")
		if strings.Contains(prompt, "Omit findings below") {
			t.Error("expected no severity filter instruction when minSeverity is low")
		}
	})

	t.Run("filter for medium", func(t *testing.T) {
		prompt := BuildSynthesisPrompt(reviews, "medium")
		assertContainsAll(t, prompt, []string{
			"Omit findings below medium severity",
			"Only include Medium, High, and Critical findings.",
		})
	})

	t.Run("filter for high", func(t *testing.T) {
		prompt := BuildSynthesisPrompt(reviews, "high")
		assertContainsAll(t, prompt, []string{
			"Omit findings below high severity",
			"Only include High and Critical findings.",
		})
	})

	t.Run("filter for critical", func(t *testing.T) {
		prompt := BuildSynthesisPrompt(reviews, "critical")
		assertContainsAll(t, prompt, []string{
			"Omit findings below critical severity",
			"Only include Critical findings.",
		})
	})
}

func TestBuildSynthesisPrompt_QuotaSkippedLabel(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security", Output: "No issues.", Status: "done"},
		{Agent: "gemini", ReviewType: "security", Status: "failed", Error: QuotaErrorPrefix + "quota exhausted"},
	}

	prompt := BuildSynthesisPrompt(reviews, "")

	assertContainsAll(t, prompt, []string{
		"[SKIPPED]",
		"review skipped",
	})
	// Should NOT contain [FAILED] for the quota-skipped review
	// Count occurrences of [FAILED]
	if strings.Contains(prompt, "[FAILED]") {
		t.Error("quota-skipped review should use [SKIPPED], not [FAILED]")
	}
}
