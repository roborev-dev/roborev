package prompt

import (
	"strings"
	"testing"
)

func TestGeminiTemplateLoads(t *testing.T) {
	result := getSystemPrompt("gemini", "review")
	if result == SystemPromptSingle {
		t.Error("Expected Gemini-specific template, got default SystemPromptSingle")
	}
	if !strings.Contains(result, "Do NOT explain your process") {
		t.Errorf("Expected Gemini template content, got: %s", result[:min(100, len(result))])
	}
}

func TestAgentTemplatesFallbackToReview(t *testing.T) {
	// Range and dirty should use the same template as review
	review := getSystemPrompt("gemini", "review")
	rang := getSystemPrompt("gemini", "range")
	dirty := getSystemPrompt("gemini", "dirty")

	if rang != review {
		t.Error("Expected range to use same template as review")
	}
	if dirty != review {
		t.Error("Expected dirty to use same template as review")
	}
}
