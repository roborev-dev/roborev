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
				Status: ResultFailed,
				Error:  QuotaErrorPrefix + "exhausted",
			},
			want: true,
		},
		{
			name: "real failure",
			r: ReviewResult{
				Status: ResultFailed,
				Error:  "agent crashed",
			},
			want: false,
		},
		{
			name: "success",
			r:    ReviewResult{Status: ResultDone},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsQuotaFailure(tt.r); got != tt.want {
				t.Errorf("IsQuotaFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountQuotaFailures(t *testing.T) {
	tests := []struct {
		name    string
		reviews []ReviewResult
		want    int
	}{
		{
			name: "mixed results",
			reviews: []ReviewResult{
				{Status: ResultDone},
				{Status: ResultFailed, Error: QuotaErrorPrefix + "exhausted"},
				{Status: ResultFailed, Error: "real error"},
				{Status: ResultFailed, Error: QuotaErrorPrefix + "limit reached"},
			},
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CountQuotaFailures(tt.reviews); got != tt.want {
				t.Errorf("CountQuotaFailures() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuildSynthesisPrompt_Basic(t *testing.T) {
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     ResultDone,
			Output:     "Found XSS vulnerability",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     ResultDone,
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
			Status:     ResultDone,
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
			if tt.wantContains != "" {
				assertContainsAll(t, prompt, []string{tt.wantContains})
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
			Status:     ResultDone,
			Output:     "looks good",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     ResultFailed,
			Error:      QuotaErrorPrefix + "exhausted",
		},
		{
			Agent:      "droid",
			ReviewType: "security",
			Status:     ResultFailed,
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
			Status:     ResultDone,
			Output:     longOutput,
		},
	}
	prompt := BuildSynthesisPrompt(reviews, "")

	assertContainsAll(t, prompt, []string{"...(truncated)"})
	if len(prompt) > promptLimit {
		t.Errorf("prompt should be truncated, got %d chars", len(prompt))
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
	assertContainsAll(t, comment, []string{"truncated"})
}

func TestFormatSingleResult_TruncationUTF8Safe(t *testing.T) {
	// Place a 4-byte emoji so it straddles the actual cut boundary.
	// The cut point is maxLen = MaxCommentLen - len("\n\n...(truncated)")
	// applied to r.Output. Put the emoji starting 2 bytes before that
	// so a naive byte slice would land inside the 4-byte character.
	const truncSuffix = "\n\n...(truncated)"
	maxLen := MaxCommentLen - len(truncSuffix)
	paddingLen := maxLen - 2
	r := ReviewResult{
		Agent:      "codex",
		ReviewType: "security",
		Status:     ResultDone,
		Output:     strings.Repeat("x", paddingLen) + "😀" + strings.Repeat("y", 100),
	}
	comment := formatSingleResult(r, "abc123456789")

	if !utf8.ValidString(comment) {
		t.Fatal("truncated comment is not valid UTF-8")
	}
	assertContainsAll(t, comment, []string{"truncated"})
}

func TestFormatSynthesizedComment(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security"},
		{Agent: "gemini", ReviewType: "design"},
	}
	comment := FormatSynthesizedComment("Combined findings here", reviews, "abc123456789")

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
			Status:     ResultDone,
			Output:     "Found issue X",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     ResultFailed,
			Error:      "crashed",
		},
	}
	comment := FormatRawBatchComment(reviews, "def456789012")

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

func TestFormatRawBatchComment_Truncation(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security", Output: strings.Repeat("x", 20000), Status: "done"},
	}

	comment := FormatRawBatchComment(reviews, "abc123def456")
	if !strings.Contains(comment, "...(truncated)") {
		t.Error("expected truncation for large output")
	}
	if len(comment) > 16000 {
		t.Errorf("comment too long: %d", len(comment))
	}
}

func TestFormatRawBatchComment_TruncationUTF8Safe(t *testing.T) {
	const maxLen = 15000
	paddingLen := maxLen - 2
	reviews := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     strings.Repeat("x", paddingLen) + "😀" + strings.Repeat("y", 100),
		},
	}

	comment := FormatRawBatchComment(reviews, "abc123def456")
	if !utf8.ValidString(comment) {
		t.Fatal("truncated comment is not valid UTF-8")
	}
	if !strings.Contains(comment, "...(truncated)") {
		t.Error("expected truncation for large output")
	}
	if len(comment) > 16000 {
		t.Errorf("comment too long: %d", len(comment))
	}
}

func TestFormatRawBatchComment_QuotaSkippedNote(t *testing.T) {
	reviews := []ReviewResult{
		{Agent: "codex", ReviewType: "security", Output: "Finding A", Status: "done"},
		{Agent: "gemini", ReviewType: "security", Status: "failed", Error: QuotaErrorPrefix + "quota exhausted"},
	}

	comment := FormatRawBatchComment(reviews, "abc123def456")

	assertContainsAll(t, comment, []string{
		"skipped (quota)",
		"gemini review skipped",
	})
}

func TestFormatAllFailedComment(t *testing.T) {
	tests := []struct {
		name         string
		reviews      []ReviewResult
		hash         string
		wantContains []string
		notContains  []string
	}{
		{
			name: "real failures",
			reviews: []ReviewResult{
				{
					Agent:      "codex",
					ReviewType: "security",
					Status:     ResultFailed,
					Error:      "crashed",
				},
			},
			hash:         "aaa111222333",
			wantContains: []string{"Review Failed", "Check CI logs"},
		},
		{
			name: "all quota",
			reviews: []ReviewResult{
				{
					Agent:      "codex",
					ReviewType: "security",
					Status:     ResultFailed,
					Error:      QuotaErrorPrefix + "exhausted",
				},
			},
			hash:         "bbb222333444",
			wantContains: []string{"Review Skipped"},
			notContains:  []string{"Check CI logs"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment := FormatAllFailedComment(tt.reviews, tt.hash)
			if len(tt.wantContains) > 0 {
				assertContainsAll(t, comment, tt.wantContains)
			}
			for _, nc := range tt.notContains {
				if strings.Contains(comment, nc) {
					t.Errorf("comment unexpectedly contains %q", nc)
				}
			}
		})
	}
}

func TestSkippedAgentNote(t *testing.T) {
	tests := []struct {
		name    string
		reviews []ReviewResult
		wants   []string
	}{
		{
			name: "no skips",
			reviews: []ReviewResult{
				{Status: ResultDone},
			},
			wants: nil,
		},
		{
			name: "one skip",
			reviews: []ReviewResult{
				{
					Agent:  "gemini",
					Status: ResultFailed,
					Error:  QuotaErrorPrefix + "exhausted",
				},
			},
			wants: []string{"gemini", "review skipped"},
		},
		{
			name: "multiple skips",
			reviews: []ReviewResult{
				{
					Agent:  "codex",
					Status: ResultFailed,
					Error:  QuotaErrorPrefix + "x",
				},
				{
					Agent:  "gemini",
					Status: ResultFailed,
					Error:  QuotaErrorPrefix + "y",
				},
			},
			wants: []string{"reviews skipped"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			note := SkippedAgentNote(tt.reviews)
			if len(tt.wants) == 0 && note != "" {
				t.Errorf("expected empty, got %q", note)
			} else if len(tt.wants) > 0 {
				assertContainsAll(t, note, tt.wants)
			}
		})
	}
}
