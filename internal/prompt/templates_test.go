package prompt

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGetSystemPrompt(t *testing.T) {
	fixedTime := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)
	fixedDateStr := "Current date: 2030-06-15 (UTC)"

	// Define a mock time provider
	mockNow := func() time.Time {
		return fixedTime
	}

	// Get the review prompt to verify fallbacks match exactly
	geminiReviewPrompt := getSystemPrompt("gemini", "review", mockNow)

	type testCase struct {
		name           string
		agent          string
		command        string
		wantContains   []string
		wantExact      string // if set, checks for exact match
		wantNotDefault bool   // if true, ensures it's not SystemPromptSingle (default)
		wantEmpty      bool
	}

	tests := []testCase{
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Call the internal helper with mock time
			got := getSystemPrompt(tc.agent, tc.command, mockNow)

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

	// Anchored regex that captures the date for parsing
	datePat := regexp.MustCompile(
		`(?m)^Current date: (\d{4}-\d{2}-\d{2}) \(UTC\)$`,
	)
	m := datePat.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf(
			"GetSystemPrompt missing date line matching %q. Got:\n%s",
			datePat.String(), got,
		)
	}

	parsed, err := time.Parse("2006-01-02", m[1])
	if err != nil {
		t.Fatalf("date %q is not a valid calendar date: %v", m[1], err)
	}

	// Allow either the before or after date in case midnight
	// was crossed between the two clock reads.
	if parsed.Before(before) || parsed.After(after) {
		t.Errorf(
			"date %s not in expected window [%s, %s]",
			m[1],
			before.Format("2006-01-02"),
			after.Format("2006-01-02"),
		)
	}
}
