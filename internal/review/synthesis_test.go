package review

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func assertContainsAll(t *testing.T, got string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output missing expected substring %q\nDocument content:\n%s", want, got)
		}
	}
}

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

	assertContainsAll(t, prompt, []string{
		"combining multiple code review outputs",
		"Agent=codex",
		"Agent=gemini",
		"Found XSS vulnerability",
		"No issues found.",
	})
}

func TestBuildSynthesisPrompt_Severity(t *testing.T) {
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "test output",
		},
	}

	tests := []struct {
		name            string
		severity        string
		wantContains    string
		wantNotContains string
	}{
		{"high severity", "high", "Only include High and Critical", ""},
		{"low severity", "low", "", "Omit findings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := BuildSynthesisPrompt(reviews, tt.severity)
			if tt.wantContains != "" && !strings.Contains(prompt, tt.wantContains) {
				t.Errorf("prompt missing %q", tt.wantContains)
			}
			if tt.wantNotContains != "" && strings.Contains(prompt, tt.wantNotContains) {
				t.Errorf("prompt unexpectedly contains %q", tt.wantNotContains)
			}
		})
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

	assertContainsAll(t, prompt, []string{
		"[SKIPPED]",
		"[FAILED]",
		"agent quota exhausted",
	})
}

func TestBuildSynthesisPrompt_Truncation(t *testing.T) {
	const promptLimit = 20000
	longOutput := strings.Repeat("x", promptLimit)
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     longOutput,
		},
	}
	prompt := BuildSynthesisPrompt(reviews, "")

	assertContainsAll(t, prompt, []string{"...(truncated)"})
	if len(prompt) > promptLimit {
		t.Errorf(
			"prompt should be truncated, got %d chars",
			len(prompt))
	}
}

func TestFormatSingleResult_Truncation(t *testing.T) {
	r := ReviewResult{
		Agent:      "codex",
		ReviewType: "security",
		Status:     ResultDone,
		Output:     strings.Repeat("x", MaxCommentLen+500),
	}
	comment := formatSingleResult(r, "abc123456789")

	if len(comment) > MaxCommentLen+200 {
		// The header and footer add some overhead, but the output
		// portion must not exceed MaxCommentLen.
		t.Fatalf("comment too long: %d", len(comment))
	}
	if !strings.Contains(comment, "truncated") {
		t.Fatal("expected truncation suffix")
	}
}

func TestFormatSingleResult_TruncationUTF8Safe(t *testing.T) {
	// Build output that would split a 4-byte emoji at the cut point.
	emoji := "😀"
	padding := strings.Repeat("x", MaxCommentLen-2)
	r := ReviewResult{
		Agent:      "codex",
		ReviewType: "security",
		Status:     ResultDone,
		Output:     padding + emoji,
	}
	comment := formatSingleResult(r, "abc123456789")

	if !utf8.ValidString(comment) {
		t.Fatal("truncated comment is not valid UTF-8")
	}
	if !strings.Contains(comment, "truncated") {
		t.Fatal("expected truncation suffix")
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

	assertContainsAll(t, comment, []string{
		"## roborev: Combined Review (`abc1234`)",
		"Combined findings here",
		"Synthesized from 2 reviews",
		"codex",
		"gemini",
		"security",
		"design",
	})
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

	assertContainsAll(t, comment, []string{
		"## roborev: Combined Review (`def4567`)",
		"Synthesis unavailable",
		"### codex — security (done)",
		"Found issue X",
		"### gemini — security (failed)",
		"Review failed",
		"---",
	})

	if strings.Contains(comment, "<details>") {
		t.Error("raw batch comment should not use <details> blocks")
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

		assertContainsAll(t, comment, []string{
			"Review Failed",
			"Check CI logs",
		})
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

		assertContainsAll(t, comment, []string{"Review Skipped"})
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
		assertContainsAll(t, note, []string{"gemini", "review skipped"})
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
		assertContainsAll(t, note, []string{"reviews skipped"})
	})
}
