package prompt

import (
	"strings"
	"testing"
	"time"
)

// mockNow sets nowFunc to return fixedTime and restores it when the test ends.
func mockNow(t *testing.T, fixedTime time.Time) {
	t.Helper()
	orig := nowFunc
	nowFunc = func() time.Time { return fixedTime }
	t.Cleanup(func() { nowFunc = orig })
}

// assertPromptContains checks that prompt contains expected, with truncated output on failure.
func assertPromptContains(t *testing.T, prompt, expected string) {
	t.Helper()
	if !strings.Contains(prompt, expected) {
		snippet := prompt
		if len(prompt) > 100 {
			snippet = prompt[:100] + "..."
		}
		t.Errorf("Expected prompt to contain %q, got start: %s", expected, snippet)
	}
}

func TestGeminiTemplateLoads(t *testing.T) {
	result := GetSystemPrompt("gemini", "review")
	if result == SystemPromptSingle {
		t.Error("Expected Gemini-specific template, got default SystemPromptSingle")
	}
	assertPromptContains(t, result, "Do NOT explain your process")
}

func TestAgentTemplatesFallbackToReview(t *testing.T) {
	// Range and dirty should use the same template as review
	review := GetSystemPrompt("gemini", "review")
	rang := GetSystemPrompt("gemini", "range")
	dirty := GetSystemPrompt("gemini", "dirty")

	if rang != review {
		t.Error("Expected range to use same template as review")
	}
	if dirty != review {
		t.Error("Expected dirty to use same template as review")
	}
}

func TestGeminiRunTemplateLoads(t *testing.T) {
	result := GetSystemPrompt("gemini", "run")
	if result == "" {
		t.Error("Expected Gemini run template to load")
	}
	assertPromptContains(t, result, "Do NOT explain your process")
}

func TestSystemPromptIncludesDate(t *testing.T) {
	mockNow(t, time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC))

	result := GetSystemPrompt("claude-code", "review")
	assertPromptContains(t, result, "Current date: 2030-06-15 (UTC)")

	// Gemini template should also get the date
	gemini := GetSystemPrompt("gemini", "review")
	assertPromptContains(t, gemini, "Current date: 2030-06-15 (UTC)")

	// Run prompt for non-Gemini should remain empty (no date appended)
	run := GetSystemPrompt("claude-code", "run")
	if run != "" {
		t.Errorf("Expected empty run prompt, got: %q", run)
	}
}

func TestNonGeminiRunReturnsEmpty(t *testing.T) {
	// Non-Gemini agents without a run template should return empty string,
	// NOT the review prompt. This ensures roborev run uses raw prompts
	// without review-style preambles for agents that don't have one.
	for _, agent := range []string{"claude-code", "claude", "unknown-agent"} {
		result := GetSystemPrompt(agent, "run")
		if result != "" {
			snippet := result
			if len(result) > 100 {
				snippet = result[:100] + "..."
			}
			t.Errorf("GetSystemPrompt(%q, \"run\") = %q, want empty string", agent, snippet)
		}
	}
}
