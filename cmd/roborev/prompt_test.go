package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPromptWithContext(t *testing.T) {
	t.Run("includes repo name and path", func(t *testing.T) {
		repoPath := "/path/to/my-project"
		userPrompt := "Explain this code"

		result := buildPromptWithContext(repoPath, userPrompt)

		if !strings.Contains(result, "my-project") {
			t.Error("Expected result to contain repo name 'my-project'")
		}
		if !strings.Contains(result, repoPath) {
			t.Error("Expected result to contain repo path")
		}
		if !strings.Contains(result, "## Context") {
			t.Error("Expected result to contain '## Context' header")
		}
		if !strings.Contains(result, "## Request") {
			t.Error("Expected result to contain '## Request' header")
		}
		if !strings.Contains(result, userPrompt) {
			t.Error("Expected result to contain user prompt")
		}
	})

	t.Run("includes project guidelines when present", func(t *testing.T) {
		// Create temp repo with .roborev.toml
		repoPath := t.TempDir()
		configContent := `review_guidelines = "Always use tabs for indentation"`
		configPath := filepath.Join(repoPath, ".roborev.toml")
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config: %v", err)
		}

		result := buildPromptWithContext(repoPath, "test prompt")

		if !strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to contain '## Project Guidelines' header")
		}
		if !strings.Contains(result, "Always use tabs for indentation") {
			t.Error("Expected result to contain guidelines text")
		}
	})

	t.Run("omits guidelines section when not configured", func(t *testing.T) {
		// Create temp repo without .roborev.toml
		repoPath := t.TempDir()

		result := buildPromptWithContext(repoPath, "test prompt")

		if strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to NOT contain '## Project Guidelines' header when no config")
		}
	})

	t.Run("omits guidelines when config has no guidelines", func(t *testing.T) {
		// Create temp repo with .roborev.toml but no guidelines
		repoPath := t.TempDir()
		configContent := `agent = "claude-code"`
		configPath := filepath.Join(repoPath, ".roborev.toml")
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config: %v", err)
		}

		result := buildPromptWithContext(repoPath, "test prompt")

		if strings.Contains(result, "## Project Guidelines") {
			t.Error("Expected result to NOT contain '## Project Guidelines' when guidelines empty")
		}
	})

	t.Run("preserves user prompt exactly", func(t *testing.T) {
		repoPath := "/tmp/test"
		userPrompt := "Find all TODO comments\nand list them"

		result := buildPromptWithContext(repoPath, userPrompt)

		if !strings.Contains(result, userPrompt) {
			t.Error("Expected user prompt to be preserved exactly")
		}
	})

	t.Run("correct section order", func(t *testing.T) {
		repoPath := t.TempDir()
		configContent := `review_guidelines = "Test guideline"`
		configPath := filepath.Join(repoPath, ".roborev.toml")
		os.WriteFile(configPath, []byte(configContent), 0644)

		result := buildPromptWithContext(repoPath, "test prompt")

		contextPos := strings.Index(result, "## Context")
		guidelinesPos := strings.Index(result, "## Project Guidelines")
		requestPos := strings.Index(result, "## Request")

		if contextPos == -1 || guidelinesPos == -1 || requestPos == -1 {
			t.Fatal("Missing expected sections")
		}

		if contextPos > guidelinesPos {
			t.Error("Context should come before Guidelines")
		}
		if guidelinesPos > requestPos {
			t.Error("Guidelines should come before Request")
		}
	})
}
