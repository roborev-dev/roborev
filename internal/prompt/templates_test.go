package prompt

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type systemPromptTestCase struct {
	name            string
	agent           string
	command         string
	wantContains    []string
	wantNotContains []string
	wantExact       string // if set, checks for exact match
	wantNotDefault  bool   // if true, ensures it's not SystemPromptSingle (default)
	wantEmpty       bool
}

func (tc *systemPromptTestCase) assert(t *testing.T, got string) {
	assert := assert.New(t)

	t.Helper()
	if tc.wantEmpty {
		assert.Empty(got, "got %q, want empty string", got)
		return
	}

	if tc.wantExact != "" && got != tc.wantExact {
		assert.Equal(tc.wantExact, got, "got %q, want %q", got, tc.wantExact)
	}

	if tc.wantNotDefault {
		// The default prompt (SystemPromptSingle) does NOT contain "Do NOT explain your process"
		// but let's be safer.
		// SystemPromptSingle is the fallback base.
		assert.NotContains(got, SystemPromptSingle, "got default SystemPromptSingle, wanted specific template")
	}

	for _, substr := range tc.wantContains {
		assert.Contains(got, substr, "got prompt missing %q", substr)
	}

	for _, substr := range tc.wantNotContains {
		assert.NotContains(got, substr, "prompt should NOT contain %q", substr)
	}
}

func TestGetSystemPrompt_Fallbacks(t *testing.T) {
	fixedTime := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)

	// Define a mock time provider
	mockNow := func() time.Time {
		return fixedTime
	}

	// Get the review prompt to verify fallbacks match exactly
	geminiReviewPrompt := getSystemPrompt("gemini", "review", mockNow)
	codexReviewPrompt := getSystemPrompt("codex", "review", mockNow)
	claudeReviewPrompt := getSystemPrompt("claude-code", "review", mockNow)

	tests := []systemPromptTestCase{
		{
			name:           "Codex Review",
			agent:          "codex",
			command:        "review",
			wantContains:   []string{"## Review Findings", "Do not include any front matter", "Do NOT build the project, run the test suite, or execute the code while reviewing.", "finish all tool use before emitting the final review"},
			wantNotDefault: true,
		},
		{
			name:           "Claude Review",
			agent:          "claude-code",
			command:        "review",
			wantContains:   []string{"## Review Findings", "Do not include any front matter", "Do NOT build the project, run the test suite, or execute the code while reviewing.", "finish all tool use before emitting the final review"},
			wantNotDefault: true,
		},
		{
			name:           "Gemini Review",
			agent:          "gemini",
			command:        "review",
			wantContains:   []string{"Do NOT explain your process", "Do NOT build the project, run the test suite, or execute the code while reviewing.", "finish all tool use before emitting the final review"},
			wantNotDefault: true,
		},
		{
			name:           "Codex Range (Review Fallback)",
			agent:          "codex",
			command:        "range",
			wantExact:      codexReviewPrompt,
			wantNotDefault: true,
		},
		{
			name:           "Claude Dirty (Review Fallback)",
			agent:          "claude-code",
			command:        "dirty",
			wantExact:      claudeReviewPrompt,
			wantNotDefault: true,
		},
		{
			name:           "Gemini Range (Review Fallback)",
			agent:          "gemini",
			command:        "range",
			wantExact:      geminiReviewPrompt,
			wantNotDefault: true,
		},
		{
			name:           "Gemini Dirty (Review Fallback)",
			agent:          "gemini",
			command:        "dirty",
			wantExact:      geminiReviewPrompt,
			wantNotDefault: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getSystemPrompt(tc.agent, tc.command, mockNow)
			tc.assert(t, got)
		})
	}
}

func TestGetSystemPrompt_DateInjection(t *testing.T) {
	fixedTime := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)
	fixedDateStr := "Current date: 2030-06-15 (UTC)"

	mockNow := func() time.Time {
		return fixedTime
	}

	tests := []systemPromptTestCase{
		{
			name:         "Gemini Run",
			agent:        "gemini",
			command:      "run",
			wantContains: []string{"Do NOT explain your process", fixedDateStr},
		},
		{
			name:         "Date Injection (Claude)",
			agent:        "claude-code",
			command:      "review",
			wantContains: []string{fixedDateStr},
		},
		{
			name:         "Date Injection (Gemini)",
			agent:        "gemini",
			command:      "review",
			wantContains: []string{fixedDateStr},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getSystemPrompt(tc.agent, tc.command, mockNow)
			tc.assert(t, got)
		})
	}
}

func TestGetSystemPrompt_Instructions(t *testing.T) {
	fixedTime := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)
	mockNow := func() time.Time { return fixedTime }

	tests := []systemPromptTestCase{
		{
			name:      "Non-Gemini Run (Claude)",
			agent:     "claude-code",
			command:   "run",
			wantEmpty: true,
		},
		{
			name:      "Non-Gemini Run (Claude alias)",
			agent:     "claude",
			command:   "run",
			wantEmpty: true,
		},
		{
			name:      "Non-Gemini Run (Unknown)",
			agent:     "unknown-agent",
			command:   "run",
			wantEmpty: true,
		},
		{
			name:         "Review includes no-skills instruction",
			agent:        "claude-code",
			command:      "review",
			wantContains: []string{"Do NOT use any external skills", "Do NOT include process narration", "put the final review only after the last tool call"},
		},
		{
			name:         "Security includes no-skills instruction",
			agent:        "claude-code",
			command:      "security",
			wantContains: []string{"Do NOT use any external skills", "Do NOT include process narration", "put the final review only after the last tool call"},
		},
		{
			name:         "Address includes no-skills instruction",
			agent:        "claude-code",
			command:      "address",
			wantContains: []string{"Do NOT use any external skills", "Do NOT include process narration", "put the final review only after the last tool call"},
		},
		{
			name:         "Fallback review includes no process narration instruction",
			agent:        "unknown-agent",
			command:      "review",
			wantContains: []string{"Do NOT include process narration", "put the final review only after the last tool call"},
		},
		{
			name:            "Gemini run excludes no-skills instruction",
			agent:           "gemini",
			command:         "run",
			wantContains:    []string{"Do NOT explain your process"},
			wantNotContains: []string{"Do NOT use any external skills"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getSystemPrompt(tc.agent, tc.command, mockNow)
			tc.assert(t, got)
		})
	}
}

func TestGetSystemPrompt_Exported(t *testing.T) {
	assert := assert.New(t)
	before := time.Now().UTC().Truncate(24 * time.Hour)
	got := GetSystemPrompt("gemini", "review")
	after := time.Now().UTC().Truncate(24 * time.Hour)

	assert.NotEmpty(got, "GetSystemPrompt returned empty string for gemini review")

	beforeStr := fmt.Sprintf("Current date: %s (UTC)", before.Format("2006-01-02"))
	afterStr := fmt.Sprintf("Current date: %s (UTC)", after.Format("2006-01-02"))

	assert.Condition(func() bool {
		return strings.Contains(got, beforeStr) || strings.Contains(got, afterStr)
	}, "prompt missing expected date string. Looked for %q or %q", beforeStr, afterStr)
}

func TestGetSystemPrompt_DefaultFallbacksRenderFromTemplates(t *testing.T) {
	fixedTime := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)
	mockNow := func() time.Time { return fixedTime }

	got := getSystemPrompt("unknown-agent", "security", mockNow)

	assert.Contains(t, got, "You are a security code reviewer")
	assert.Contains(t, got, "Do NOT use any external skills")
	assert.Contains(t, got, "Current date: 2030-06-15 (UTC)")
}
