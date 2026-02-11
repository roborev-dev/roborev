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
	lines := renderMarkdownLines("Line 1\nLine 2\nLine 3", 80, 80, styles.DarkStyleConfig, 2)

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
	lines := renderMarkdownLines("", 80, 80, styles.DarkStyleConfig, 2)
	// Should not panic and should produce some output (even if empty)
	if lines == nil {
		t.Error("Expected non-nil result for empty input")
	}
}

func TestMarkdownCacheHit(t *testing.T) {
	c := &markdownCache{}

	// First call should render
	lines1 := c.getReviewLines("Hello\nWorld", 80, 80, 1)
	if len(lines1) == 0 {
		t.Fatal("Expected non-empty lines")
	}

	// Second call with same inputs should return cached result (same slice)
	lines2 := c.getReviewLines("Hello\nWorld", 80, 80, 1)
	if &lines1[0] != &lines2[0] {
		t.Error("Expected cache hit to return same slice")
	}
}

func TestMarkdownCacheInvalidatesOnTextChange(t *testing.T) {
	c := &markdownCache{}
	lines1 := c.getReviewLines("Hello", 80, 80, 1)
	lines2 := c.getReviewLines("Goodbye", 80, 80, 1)

	// Different text should produce different content
	if strings.TrimSpace(lines1[len(lines1)-1]) == strings.TrimSpace(lines2[len(lines2)-1]) {
		t.Error("Expected different content after text change")
	}
}

func TestMarkdownCacheInvalidatesOnWidthChange(t *testing.T) {
	c := &markdownCache{}
	lines1 := c.getReviewLines("Hello", 80, 80, 1)
	lines2 := c.getReviewLines("Hello", 40, 40, 1)

	// Same text but different width should re-render (different slice)
	if &lines1[0] == &lines2[0] {
		t.Error("Expected cache miss when width changes")
	}
}

func TestMarkdownCacheInvalidatesOnReviewIDChange(t *testing.T) {
	c := &markdownCache{}
	lines1 := c.getReviewLines("Hello", 80, 80, 1)
	lines2 := c.getReviewLines("Hello", 80, 80, 2)

	// Same text but different review ID should re-render
	if &lines1[0] == &lines2[0] {
		t.Error("Expected cache miss when review ID changes")
	}
}

func TestMarkdownCachePromptSeparateFromReview(t *testing.T) {
	c := &markdownCache{}

	// Review and prompt caches are independent
	reviewLines := c.getReviewLines("Review text", 80, 80, 1)
	promptLines := c.getPromptLines("Prompt text", 80, 80, 1)

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

func TestTruncateLongLinesOnlyTruncatesCodeBlocks(t *testing.T) {
	longLine := "a very long line that exceeds the width by a lot and should be truncated down to size"
	input := "short\n```\n" + longLine + "\n```\n" + longLine
	out := truncateLongLines(input, 20, 2)
	lines := strings.Split(out, "\n")

	if len(lines) != 5 {
		t.Fatalf("Expected 5 lines, got %d", len(lines))
	}
	if lines[0] != "short" {
		t.Errorf("Short line should be unchanged, got %q", lines[0])
	}
	if len(lines[2]) > 20 {
		t.Errorf("Code block line should be truncated to <=20 chars, got %d: %q", len(lines[2]), lines[2])
	}
	// Prose line outside code block should be preserved intact
	if lines[4] != longLine {
		t.Errorf("Prose line should be preserved, got %q", lines[4])
	}
}

func TestTruncateLongLinesFenceEdgeCases(t *testing.T) {
	longLine := strings.Repeat("x", 50)
	tests := []struct {
		name      string
		input     string
		wantTrunc bool // whether longLine inside the fence should be truncated
	}{
		{
			name:      "tilde fence",
			input:     "~~~\n" + longLine + "\n~~~",
			wantTrunc: true,
		},
		{
			name:      "indented fence (2 spaces)",
			input:     "  ```\n" + longLine + "\n  ```",
			wantTrunc: true,
		},
		{
			name:      "4-backtick fence",
			input:     "````\n" + longLine + "\n````",
			wantTrunc: true,
		},
		{
			name:      "4-backtick fence not closed by 3",
			input:     "````\n" + longLine + "\n```",
			wantTrunc: true, // still inside — 3 backticks can't close a 4-backtick fence
		},
		{
			name:      "backtick fence with info string",
			input:     "```diff\n" + longLine + "\n```",
			wantTrunc: true,
		},
		{
			name:      "prose with triple backtick in text not a fence",
			input:     longLine, // no fence at all
			wantTrunc: false,
		},
		{
			name:      "closing backtick fence with info string does not close",
			input:     "```\n```lang\n" + longLine,
			wantTrunc: true, // long line after invalid closer is still inside fence
		},
		{
			name:      "closing tilde fence with text does not close",
			input:     "~~~\n~~~text\n" + longLine,
			wantTrunc: true, // long line after invalid closer is still inside fence
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := truncateLongLines(tt.input, 20, 2)
			lines := strings.Split(out, "\n")
			// Find the longLine (or its truncation) in the output
			for _, line := range lines {
				if strings.HasPrefix(line, "xxx") || line == longLine {
					truncated := len(line) <= 20
					if tt.wantTrunc && !truncated {
						t.Errorf("Expected truncation inside fence, got len=%d: %q", len(line), line)
					}
					if !tt.wantTrunc && truncated {
						t.Errorf("Expected no truncation outside fence, got len=%d: %q", len(line), line)
					}
					return
				}
			}
			t.Error("Could not find long line in output")
		})
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

func TestRenderMarkdownLinesPreservesLongProse(t *testing.T) {
	// Long prose lines should be word-wrapped by glamour, not truncated.
	// All words must appear in the rendered output.
	longProse := "This is a very long prose line with important content that should be word-wrapped by glamour rather than truncated so that no information is lost from the rendered output"
	lines := renderMarkdownLines(longProse, 60, 80, styles.DarkStyleConfig, 2)

	combined := ""
	for _, line := range lines {
		combined += testANSIRegex.ReplaceAllString(line, "") + " "
	}
	for _, word := range []string{"important", "word-wrapped", "truncated", "information", "rendered"} {
		if !strings.Contains(combined, word) {
			t.Errorf("Expected word %q preserved in rendered output, got: %s", word, combined)
		}
	}
}

func TestSanitizeEscapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "SGR preserved",
			input: "\x1b[31mred\x1b[0m",
			want:  "\x1b[31mred\x1b[0m",
		},
		{
			name:  "OSC stripped",
			input: "hello\x1b]0;evil title\x07world",
			want:  "helloworld",
		},
		{
			name:  "DCS stripped",
			input: "hello\x1bPevil\x1b\\world",
			want:  "helloworld",
		},
		{
			name:  "CSI non-SGR stripped",
			input: "hello\x1b[2Jworld", // ED (erase display)
			want:  "helloworld",
		},
		{
			name:  "bare ESC stripped",
			input: "hello\x1bcworld", // RIS (reset)
			want:  "helloworld",
		},
		{
			name:  "mixed: SGR kept, OSC stripped",
			input: "\x1b[1mbold\x1b]0;evil\x07\x1b[0m",
			want:  "\x1b[1mbold\x1b[0m",
		},
		{
			name:  "private-mode CSI stripped (hide cursor)",
			input: "hello\x1b[?25lworld",
			want:  "helloworld",
		},
		{
			name:  "private-mode CSI stripped (DA2)",
			input: "hello\x1b[>0cworld",
			want:  "helloworld",
		},
		{
			name:  "CSI with intermediate byte stripped (cursor style)",
			input: "hello\x1b[1 qworld",
			want:  "helloworld",
		},
		{
			// Incomplete CSI without a final byte is preserved: each line
			// is sanitized independently so there is no cross-line completion,
			// and stripping partial CSI would also catch legitimate trailing SGR.
			name:  "unterminated CSI at end of string preserved",
			input: "hello\x1b[31",
			want:  "hello\x1b[31",
		},
		{
			name:  "unterminated OSC stripped (no terminator)",
			input: "hello\x1b]0;title",
			want:  "hello",
		},
		{
			name:  "bare CSI introducer at end of string preserved",
			input: "hello\x1b[",
			want:  "hello\x1b[",
		},
		{
			name:  "carriage return stripped (prevents line overwrite)",
			input: "fake\rreal",
			want:  "fakereal",
		},
		{
			name:  "backspace stripped (prevents overwrite spoofing)",
			input: "hello\b\b\b\b\bworld",
			want:  "helloworld",
		},
		{
			name:  "BEL stripped",
			input: "hello\aworld",
			want:  "helloworld",
		},
		{
			name:  "tab and newline preserved",
			input: "col1\tcol2\nrow2",
			want:  "col1\tcol2\nrow2",
		},
		{
			name:  "null byte stripped",
			input: "hello\x00world",
			want:  "helloworld",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeEscapes(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeEscapes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRenderMarkdownLinesNoOverflow(t *testing.T) {
	// A long diff line should be truncated by renderMarkdownLines, not wrapped
	longLine := strings.Repeat("x", 200)
	text := "Review:\n\n```\n" + longLine + "\n```\n"
	width := 76
	lines := renderMarkdownLines(text, width, width, styles.DarkStyleConfig, 2)

	for i, line := range lines {
		stripped := testANSIRegex.ReplaceAllString(line, "")
		if len(stripped) > width+10 { // small tolerance for trailing spaces
			t.Errorf("line %d exceeds width %d: len=%d %q", i, width, len(stripped), stripped)
		}
	}
}
