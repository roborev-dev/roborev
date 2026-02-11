package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/roborev-dev/roborev/internal/storage"
)

var testANSIRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestRenderMarkdownLinesPreservesNewlines(t *testing.T) {
	// Verify that single newlines in plain text are preserved (not collapsed into one paragraph)
	lines := renderMarkdownLines("Line 1\nLine 2\nLine 3", 80, styles.DarkStyleConfig, 2)

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
	lines := renderMarkdownLines("", 80, styles.DarkStyleConfig, 2)
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

func TestPromptScrollPageUpAfterPageDown(t *testing.T) {
	// Generate enough content to require scrolling (more than terminal height)
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("Line %d of the prompt content", i+1))
	}
	longContent := strings.Join(lines, "\n")

	m := tuiModel{
		width:       80,
		height:      24,
		currentView: tuiViewPrompt,
		mdCache:     newMarkdownCache(2),
		currentReview: &storage.Review{
			ID:     1,
			Prompt: longContent,
			Job: &storage.ReviewJob{
				ID:     1,
				GitRef: "abc1234",
			},
		},
	}

	// First render populates the cache with maxScroll
	m.renderPromptView()

	if m.mdCache.lastPromptMaxScroll == 0 {
		t.Fatal("Expected non-zero max scroll for long content")
	}

	// Page down several times past the end
	for i := 0; i < 20; i++ {
		m, _ = pressSpecial(m, tea.KeyPgDown)
	}

	// promptScroll should be clamped to maxScroll, not inflated
	if m.promptScroll > m.mdCache.lastPromptMaxScroll {
		t.Errorf("promptScroll %d should not exceed maxScroll %d after page-down",
			m.promptScroll, m.mdCache.lastPromptMaxScroll)
	}

	// Now page up once should visibly scroll up
	scrollBefore := m.promptScroll
	m, _ = pressSpecial(m, tea.KeyPgUp)

	if m.promptScroll >= scrollBefore {
		t.Errorf("Page up should reduce scroll: before=%d, after=%d",
			scrollBefore, m.promptScroll)
	}
}

func TestReviewScrollPageUpAfterPageDown(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("Line %d of the review output", i+1))
	}
	longContent := strings.Join(lines, "\n")

	m := tuiModel{
		width:       80,
		height:      24,
		currentView: tuiViewReview,
		mdCache:     newMarkdownCache(2),
		currentReview: &storage.Review{
			ID:     1,
			Output: longContent,
			Job: &storage.ReviewJob{
				ID:       1,
				GitRef:   "abc1234",
				RepoName: "testrepo",
			},
		},
	}

	// First render populates the cache
	m.renderReviewView()

	if m.mdCache.lastReviewMaxScroll == 0 {
		t.Fatal("Expected non-zero max scroll for long content")
	}

	// Page down past the end
	for i := 0; i < 20; i++ {
		m, _ = pressSpecial(m, tea.KeyPgDown)
	}

	if m.reviewScroll > m.mdCache.lastReviewMaxScroll {
		t.Errorf("reviewScroll %d should not exceed maxScroll %d",
			m.reviewScroll, m.mdCache.lastReviewMaxScroll)
	}

	// Page up should work immediately
	scrollBefore := m.reviewScroll
	m, _ = pressSpecial(m, tea.KeyPgUp)

	if m.reviewScroll >= scrollBefore {
		t.Errorf("Page up should reduce scroll: before=%d, after=%d",
			scrollBefore, m.reviewScroll)
	}
}

func TestTruncateLongLines(t *testing.T) {
	input := "short\n" +
		"a very long line that exceeds the width by a lot and should be truncated down to size\n" +
		"also short"
	out := truncateLongLines(input, 20, 2)
	lines := strings.Split(out, "\n")

	if len(lines) != 3 {
		t.Fatalf("Expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "short" {
		t.Errorf("Short line should be unchanged, got %q", lines[0])
	}
	if len(lines[1]) > 20 {
		t.Errorf("Long line should be truncated to <=20 chars, got %d: %q", len(lines[1]), lines[1])
	}
	if lines[2] != "also short" {
		t.Errorf("Short line should be unchanged, got %q", lines[2])
	}
}

func TestTruncateLongLinesPreservesNewlines(t *testing.T) {
	// Ensure blank lines and structure are preserved
	input := "line1\n\n\nline4"
	out := truncateLongLines(input, 80, 2)
	if out != input {
		t.Errorf("Expected input preserved, got %q", out)
	}
}

func TestRenderMarkdownLinesNoOverflow(t *testing.T) {
	// A long diff line should be truncated by renderMarkdownLines, not wrapped
	longLine := strings.Repeat("x", 200)
	text := "Review:\n\n```\n" + longLine + "\n```\n"
	width := 76
	lines := renderMarkdownLines(text, width, styles.DarkStyleConfig, 2)

	for i, line := range lines {
		stripped := testANSIRegex.ReplaceAllString(line, "")
		if len(stripped) > width+10 { // small tolerance for trailing spaces
			t.Errorf("line %d exceeds width %d: len=%d %q", i, width, len(stripped), stripped)
		}
	}
}
