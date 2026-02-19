package prompt

import (
	"embed"
	"fmt"
	"strings"
	"time"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// GetSystemPrompt returns the system prompt for the specified agent and type.
// If a specific template exists for the agent, it uses that.
// Otherwise, it falls back to the default constant.
// Supported prompt types: review, range, dirty, address, design-review, run, security
func GetSystemPrompt(agentName string, promptType string) string {
	return getSystemPrompt(agentName, promptType, time.Now)
}

// getSystemPrompt is the internal implementation that accepts a time provider
func getSystemPrompt(agentName string, promptType string, now func() time.Time) string {
	// Normalize agent name
	agentName = strings.ToLower(agentName)
	if agentName == "claude" {
		agentName = "claude-code"
	}

	// For review operations (review, range, dirty), use {agent}_review.tmpl
	// These are all code reviews, just with different input formats
	// Security reviews use {agent}_security.tmpl
	templateType := promptType
	if promptType == "range" || promptType == "dirty" {
		templateType = "review"
	}

	tmplName := fmt.Sprintf("templates/%s_%s.tmpl", agentName, templateType)
	content, err := templateFS.ReadFile(tmplName)
	if err == nil {
		return appendDateLine(string(content), now)
	}

	// Fallback to default constants
	var base string
	switch promptType {
	case "review":
		base = SystemPromptSingle
	case "dirty":
		base = SystemPromptDirty
	case "range":
		base = SystemPromptRange
	case "address":
		base = SystemPromptAddress
	case "security":
		base = SystemPromptSecurity
	case "design-review":
		base = SystemPromptDesignReview
	case "run":
		// No default run preamble - return empty so raw prompts are used
		return ""
	default:
		base = SystemPromptSingle
	}
	return appendDateLine(base, now)
}

// appendDateLine adds the current UTC date to a system prompt.
func appendDateLine(prompt string, now func() time.Time) string {
	return prompt + "\n\nCurrent date: " + now().UTC().Format("2006-01-02") + " (UTC)"
}
