package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

var testANSIRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestRenderMarkdownLinesPreservesNewlines(t *testing.T) {
	// Verify that single newlines in plain text are preserved (not collapsed into one paragraph)
	lines := renderMarkdownLines("Line 1\nLine 2\nLine 3", 80, "dark")

	found := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(testANSIRegex.ReplaceAllString(line, ""))
		if trimmed == "Line 1" || trimmed == "Line 2" || trimmed == "Line 3" {
			found++
		}
	}
	if found != 3 {
		t.Errorf("Expected 3 separate lines preserved, found %d in: %v", found, lines)
	}
}

func TestRenderMarkdownLinesFallsBackOnEmpty(t *testing.T) {
	lines := renderMarkdownLines("", 80, "dark")
	// Should not panic and should produce some output (even if empty)
	if lines == nil {
		t.Error("Expected non-nil result for empty input")
	}
}

func TestMarkdownCacheHit(t *testing.T) {
	c := &markdownCache{}

	// First call should render
	lines1 := c.getReviewLines("Hello\nWorld", 80, 1)
	if len(lines1) == 0 {
		t.Fatal("Expected non-empty lines")
	}

	// Second call with same inputs should return cached result (same slice)
	lines2 := c.getReviewLines("Hello\nWorld", 80, 1)
	if &lines1[0] != &lines2[0] {
		t.Error("Expected cache hit to return same slice")
	}
}

func TestMarkdownCacheInvalidatesOnTextChange(t *testing.T) {
	c := &markdownCache{}
	lines1 := c.getReviewLines("Hello", 80, 1)
	lines2 := c.getReviewLines("Goodbye", 80, 1)

	// Different text should produce different content
	if strings.TrimSpace(lines1[len(lines1)-1]) == strings.TrimSpace(lines2[len(lines2)-1]) {
		t.Error("Expected different content after text change")
	}
}

func TestMarkdownCacheInvalidatesOnWidthChange(t *testing.T) {
	c := &markdownCache{}
	lines1 := c.getReviewLines("Hello", 80, 1)
	lines2 := c.getReviewLines("Hello", 40, 1)

	// Same text but different width should re-render (different slice)
	if &lines1[0] == &lines2[0] {
		t.Error("Expected cache miss when width changes")
	}
}

func TestMarkdownCacheInvalidatesOnReviewIDChange(t *testing.T) {
	c := &markdownCache{}
	lines1 := c.getReviewLines("Hello", 80, 1)
	lines2 := c.getReviewLines("Hello", 80, 2)

	// Same text but different review ID should re-render
	if &lines1[0] == &lines2[0] {
		t.Error("Expected cache miss when review ID changes")
	}
}

func TestMarkdownCachePromptSeparateFromReview(t *testing.T) {
	c := &markdownCache{}

	// Review and prompt caches are independent
	reviewLines := c.getReviewLines("Review text", 80, 1)
	promptLines := c.getPromptLines("Prompt text", 80, 1)

	reviewContent := strings.TrimSpace(reviewLines[len(reviewLines)-1])
	promptContent := strings.TrimSpace(promptLines[len(promptLines)-1])

	if reviewContent == promptContent {
		t.Error("Expected review and prompt to cache independently")
	}
}

func TestNilMdCacheRenderReviewView(t *testing.T) {
	// Verify that a zero-value model (nil mdCache) does not panic in renderReviewView
	m := tuiModel{
		width:       80,
		height:      24,
		currentView: tuiViewReview,
		currentReview: &storage.Review{
			ID:     1,
			Output: "test output",
			Job: &storage.ReviewJob{
				ID:     1,
				GitRef: "abc1234",
			},
		},
	}

	// Should not panic
	output := m.renderReviewView()
	if !strings.Contains(output, "test output") {
		t.Error("Expected output to contain review text")
	}
}

func TestNilMdCacheRenderPromptView(t *testing.T) {
	// Verify that a zero-value model (nil mdCache) does not panic in renderPromptView
	m := tuiModel{
		width:       80,
		height:      24,
		currentView: tuiViewPrompt,
		currentReview: &storage.Review{
			ID:     1,
			Prompt: "test prompt",
			Job: &storage.ReviewJob{
				ID:     1,
				GitRef: "abc1234",
			},
		},
	}

	// Should not panic
	output := m.renderPromptView()
	if !strings.Contains(output, "test prompt") {
		t.Error("Expected output to contain prompt text")
	}
}
