package review

import (
	"strings"
	"testing"
)

func TestIsQuotaFailure(t *testing.T) {
	tests := []struct {
		name string
		r    ReviewResult
		want bool
	}{
		{
			name: "quota failure",
			r: ReviewResult{
				Status: "failed",
				Error:  QuotaErrorPrefix + "exhausted",
			},
			want: true,
		},
		{
			name: "real failure",
			r: ReviewResult{
				Status: "failed",
				Error:  "agent crashed",
			},
			want: false,
		},
		{
			name: "success",
			r:    ReviewResult{Status: "done"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsQuotaFailure(tt.r); got != tt.want {
				t.Errorf(
					"IsQuotaFailure() = %v, want %v",
					got, tt.want)
			}
		})
	}
}

func TestCountQuotaFailures(t *testing.T) {
	reviews := []ReviewResult{
		{Status: "done"},
		{
			Status: "failed",
			Error:  QuotaErrorPrefix + "exhausted",
		},
		{Status: "failed", Error: "real error"},
		{
			Status: "failed",
			Error:  QuotaErrorPrefix + "limit reached",
		},
	}
	if got := CountQuotaFailures(reviews); got != 2 {
		t.Errorf(
			"CountQuotaFailures() = %d, want 2", got)
	}
}

func TestBuildSynthesisPrompt_Basic(t *testing.T) {
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "Found XSS vulnerability",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     "done",
			Output:     "No issues found.",
		},
	}
	prompt := BuildSynthesisPrompt(reviews, "")

	checks := []string{
		"combining multiple code review outputs",
		"Agent=codex",
		"Agent=gemini",
		"Found XSS vulnerability",
		"No issues found.",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt missing %q", check)
		}
	}
}

func TestBuildSynthesisPrompt_MinSeverity(t *testing.T) {
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "test output",
		},
	}

	prompt := BuildSynthesisPrompt(reviews, "high")
	if !strings.Contains(
		prompt, "Only include High and Critical") {
		t.Error(
			"expected severity filter instruction for high")
	}

	prompt = BuildSynthesisPrompt(reviews, "low")
	if strings.Contains(prompt, "Omit findings") {
		t.Error(
			"low severity should not add filter instruction")
	}
}

func TestBuildSynthesisPrompt_QuotaAndFailed(t *testing.T) {
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "looks good",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     "failed",
			Error: QuotaErrorPrefix +
				"exhausted",
		},
		{
			Agent:      "droid",
			ReviewType: "security",
			Status:     "failed",
			Error:      "agent crashed",
		},
	}
	prompt := BuildSynthesisPrompt(reviews, "")

	if !strings.Contains(prompt, "[SKIPPED]") {
		t.Error("expected [SKIPPED] for quota failure")
	}
	if !strings.Contains(prompt, "[FAILED]") {
		t.Error("expected [FAILED] for real failure")
	}
	if !strings.Contains(
		prompt, "agent quota exhausted") {
		t.Error("expected quota note")
	}
}

func TestBuildSynthesisPrompt_Truncation(t *testing.T) {
	longOutput := strings.Repeat("x", 20000)
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     longOutput,
		},
	}
	prompt := BuildSynthesisPrompt(reviews, "")

	if !strings.Contains(prompt, "...(truncated)") {
		t.Error("expected truncation marker for long output")
	}
	if len(prompt) > 20000 {
		t.Errorf(
			"prompt should be truncated, got %d chars",
			len(prompt))
	}
}

func TestFormatSynthesizedComment(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security"},
		{Agent: "gemini", ReviewType: "design"},
	}
	comment := FormatSynthesizedComment(
		"Combined findings here", reviews,
		"abc123456789")

	checks := []string{
		"## roborev: Combined Review (`abc1234`)",
		"Combined findings here",
		"Synthesized from 2 reviews",
		"codex",
		"gemini",
		"security",
		"design",
	}
	for _, check := range checks {
		if !strings.Contains(comment, check) {
			t.Errorf("comment missing %q", check)
		}
	}
}

func TestFormatRawBatchComment(t *testing.T) {
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "Found issue X",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     "failed",
			Error:      "crashed",
		},
	}
	comment := FormatRawBatchComment(
		reviews, "def456789012")

	checks := []string{
		"## roborev: Combined Review (`def4567`)",
		"Synthesis unavailable",
		"<details>",
		"Found issue X",
		"Review failed",
	}
	for _, check := range checks {
		if !strings.Contains(comment, check) {
			t.Errorf("comment missing %q", check)
		}
	}
}

func TestFormatAllFailedComment(t *testing.T) {
	t.Run("real failures", func(t *testing.T) {
		reviews := []ReviewResult{
			{
				Agent:      "codex",
				ReviewType: "security",
				Status:     "failed",
				Error:      "crashed",
			},
		}
		comment := FormatAllFailedComment(
			reviews, "aaa111222333")

		if !strings.Contains(
			comment, "Review Failed") {
			t.Error("expected 'Review Failed' header")
		}
		if !strings.Contains(
			comment, "Check CI logs") {
			t.Error("expected log check instruction")
		}
	})

	t.Run("all quota", func(t *testing.T) {
		reviews := []ReviewResult{
			{
				Agent:      "codex",
				ReviewType: "security",
				Status:     "failed",
				Error: QuotaErrorPrefix +
					"exhausted",
			},
		}
		comment := FormatAllFailedComment(
			reviews, "bbb222333444")

		if !strings.Contains(
			comment, "Review Skipped") {
			t.Error("expected 'Review Skipped' header")
		}
		if strings.Contains(
			comment, "Check CI logs") {
			t.Error(
				"all-quota should not mention CI logs")
		}
	})
}

func TestSkippedAgentNote(t *testing.T) {
	t.Run("no skips", func(t *testing.T) {
		reviews := []ReviewResult{
			{Status: "done"},
		}
		if note := SkippedAgentNote(reviews); note != "" {
			t.Errorf("expected empty, got %q", note)
		}
	})

	t.Run("one skip", func(t *testing.T) {
		reviews := []ReviewResult{
			{
				Agent:  "gemini",
				Status: "failed",
				Error: QuotaErrorPrefix +
					"exhausted",
			},
		}
		note := SkippedAgentNote(reviews)
		if !strings.Contains(note, "gemini") {
			t.Error("expected gemini in note")
		}
		if !strings.Contains(
			note, "review skipped") {
			t.Error("expected singular 'review skipped'")
		}
	})

	t.Run("multiple skips", func(t *testing.T) {
		reviews := []ReviewResult{
			{
				Agent:  "codex",
				Status: "failed",
				Error:  QuotaErrorPrefix + "x",
			},
			{
				Agent:  "gemini",
				Status: "failed",
				Error:  QuotaErrorPrefix + "y",
			},
		}
		note := SkippedAgentNote(reviews)
		if !strings.Contains(
			note, "reviews skipped") {
			t.Error("expected plural 'reviews skipped'")
		}
	})
}
