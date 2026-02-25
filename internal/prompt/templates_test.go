package prompt

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
	t.Helper()
	if tc.wantEmpty {
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
		return
	}

	if tc.wantExact != "" && got != tc.wantExact {
		t.Errorf("got %q, want %q", got, tc.wantExact)
	}

	if tc.wantNotDefault {
		// The default prompt (SystemPromptSingle) does NOT contain "Do NOT explain your process"
		// but let's be safer.
		// SystemPromptSingle is the fallback base.
		// If we got the default base, it means we didn't load the template.
		// We can check if it contains SystemPromptSingle.
		if strings.Contains(got, SystemPromptSingle) {
			t.Errorf("got default SystemPromptSingle, wanted specific template")
		}
	}

	for _, substr := range tc.wantContains {
		if !strings.Contains(got, substr) {
			snippet := got
			if len(got) > 100 {
				snippet = got[:100] + "..."
			}
			t.Errorf("got prompt missing %q. Got start: %s", substr, snippet)
		}
	}

	for _, substr := range tc.wantNotContains {
		if strings.Contains(got, substr) {
			t.Errorf("prompt should NOT contain %q", substr)
		}
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

	tests := []systemPromptTestCase{
		{
			name:           "Gemini Review",
			agent:          "gemini",
			command:        "review",
			wantContains:   []string{"Do NOT explain your process"},
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
			wantContains: []string{"Do NOT use any external skills"},
		},
		{
			name:         "Security includes no-skills instruction",
			agent:        "claude-code",
			command:      "security",
			wantContains: []string{"Do NOT use any external skills"},
		},
		{
			name:         "Address includes no-skills instruction",
			agent:        "claude-code",
			command:      "address",
			wantContains: []string{"Do NOT use any external skills"},
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
	before := time.Now().UTC().Truncate(24 * time.Hour)
	got := GetSystemPrompt("gemini", "review")
	after := time.Now().UTC().Truncate(24 * time.Hour)

	if got == "" {
		t.Error("GetSystemPrompt returned empty string for gemini review")
	}

	beforeStr := fmt.Sprintf("Current date: %s (UTC)", before.Format("2006-01-02"))
	afterStr := fmt.Sprintf("Current date: %s (UTC)", after.Format("2006-01-02"))

	if !strings.Contains(got, beforeStr) && !strings.Contains(got, afterStr) {
		t.Errorf("prompt missing expected date string. Looked for %q or %q", beforeStr, afterStr)
	}
}
