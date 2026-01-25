package prompt

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// getSystemPrompt returns the system prompt for the specified agent and type.
// If a specific template exists for the agent, it uses that.
// Otherwise, it falls back to the default constant.
func getSystemPrompt(agentName string, promptType string) string {
	// Normalize agent name
	agentName = strings.ToLower(agentName)
	if agentName == "claude" {
		agentName = "claude-code"
	}

	// Try to load template: templates/{agent}_{type}.tmpl
	// e.g. templates/gemini_review.tmpl
	tmplName := fmt.Sprintf("templates/%s_%s.tmpl", agentName, promptType)
	
	content, err := templateFS.ReadFile(tmplName)
	if err == nil {
		return string(content)
	}

	// Fallback to default constants
	switch promptType {
	case "review":
		return SystemPromptSingle
	case "dirty":
		return SystemPromptDirty
	case "range":
		return SystemPromptRange
	case "address":
		return SystemPromptAddress
	default:
		return SystemPromptSingle
	}
}
