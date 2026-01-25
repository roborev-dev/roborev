package prompt

import (
	"strings"
	"testing"
)

func TestGeminiTemplateLoads(t *testing.T) {
	result := GetSystemPrompt("gemini", "review")
	if result == SystemPromptSingle {
		t.Error("Expected Gemini-specific template, got default SystemPromptSingle")
	}
	if !strings.Contains(result, "Do NOT explain your process") {
		t.Errorf("Expected Gemini template content, got: %s", result[:min(100, len(result))])
	}
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
	if !strings.Contains(result, "Do NOT explain your process") {
		t.Errorf("Expected Gemini run template content, got: %s", result[:min(100, len(result))])
	}
}
