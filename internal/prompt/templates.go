package prompt

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// GetSystemPrompt returns the system prompt for the specified agent and type.
// If a specific template exists for the agent, it uses that.
// Otherwise, it falls back to the default constant.
// Supported prompt types: review, range, dirty, address, run
func GetSystemPrompt(agentName string, promptType string) string {
	// Normalize agent name
	agentName = strings.ToLower(agentName)
	if agentName == "claude" {
		agentName = "claude-code"
	}

	// For review operations (review, range, dirty), use {agent}_review.tmpl
	// These are all code reviews, just with different input formats
	templateType := promptType
	if promptType == "range" || promptType == "dirty" {
		templateType = "review"
	}

	tmplName := fmt.Sprintf("templates/%s_%s.tmpl", agentName, templateType)
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
	case "run":
		// No default run preamble - return empty so raw prompts are used
		return ""
	default:
		return SystemPromptSingle
	}
}
