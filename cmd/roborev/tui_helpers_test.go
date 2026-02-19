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

func TestMarkdownCacheBehavior(t *testing.T) {
	baseText := "Hello\nWorld"
	baseWidth := 80
	baseID := int64(1)

	tests := []struct {
		name      string
		text      string
		width     int
		id        int
		expectHit bool
	}{
		{"SameInputs", baseText, baseWidth, int(baseID), true},
		{"DiffText", "Different", baseWidth, int(baseID), false},
		{"DiffWidth", baseText, 40, int(baseID), false},
		{"DiffID", baseText, baseWidth, 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Always start with a fresh cache to ensure isolation
			c := &markdownCache{}
			// Prime cache with base state
			lines1 := c.getReviewLines(baseText, baseWidth, baseWidth, baseID)

			// Exercise the cache with the test case inputs
			lines2 := c.getReviewLines(tt.text, tt.width, tt.width, int64(tt.id))

			// Check if the underlying array is the same (cache hit vs miss)
			if len(lines1) == 0 || len(lines2) == 0 {
				t.Fatal("Unexpected empty lines from render")
			}
			isSameObject := &lines1[0] == &lines2[0]

			if tt.expectHit {
				if !isSameObject {
					t.Error("Expected cache hit (same slice pointer)")
				}
			} else {
				if isSameObject {
					t.Error("Expected cache miss (different slice pointer)")
				}
			}

			// Additional check for content correctness on text change
			if tt.name == "DiffText" {
				// Verify the content actually reflects the new input
				combined := ""
				for _, line := range lines2 {
					combined += testANSIRegex.ReplaceAllString(line, "")
				}
				if !strings.Contains(combined, "Different") {
					t.Errorf("Expected output to contain 'Different', got %q", combined)
				}
			}
		})
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

func TestRenderViewSafety_NilCache(t *testing.T) {
	tests := []struct {
		name   string
		view   tuiView
		setup  func(*storage.Review)
		render func(tuiModel) string
		want   string
	}{
		{
			name:   "ReviewView",
			view:   tuiViewReview,
			setup:  func(r *storage.Review) { r.Output = "output text" },
			render: func(m tuiModel) string { return m.renderReviewView() },
			want:   "output text",
		},
		{
			name:   "PromptView",
			view:   tuiViewPrompt,
			setup:  func(r *storage.Review) { r.Prompt = "prompt text" },
			render: func(m tuiModel) string { return m.renderPromptView() },
			want:   "prompt text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tuiModel{
				width:       80,
				height:      24,
				currentView: tt.view,
				currentReview: &storage.Review{
					ID:  1,
					Job: &storage.ReviewJob{GitRef: "abc1234"},
				},
			}
			tt.setup(m.currentReview)

			// Should not panic despite nil mdCache
			if got := tt.render(m); !strings.Contains(got, tt.want) {
				t.Errorf("Expected output containing %q", tt.want)
			}
		})
	}
}

func TestScrollPageUpAfterPageDown(t *testing.T) {
	tests := []struct {
		name      string
		view      tuiView
		setup     func(*tuiModel, string)
		render    func(*tuiModel) string
		getScroll func(tuiModel) int
		getMax    func(tuiModel) int
	}{
		{
			name: "PromptView",
			view: tuiViewPrompt,
			setup: func(m *tuiModel, content string) {
				m.currentReview.Prompt = content
			},
			render:    func(m *tuiModel) string { return m.renderPromptView() },
			getScroll: func(m tuiModel) int { return m.promptScroll },
			getMax:    func(m tuiModel) int { return m.mdCache.lastPromptMaxScroll },
		},
		{
			name: "ReviewView",
			view: tuiViewReview,
			setup: func(m *tuiModel, content string) {
				m.currentReview.Output = content
			},
			render:    func(m *tuiModel) string { return m.renderReviewView() },
			getScroll: func(m tuiModel) int { return m.reviewScroll },
			getMax:    func(m tuiModel) int { return m.mdCache.lastReviewMaxScroll },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lines []string
			for i := range 100 {
				lines = append(lines, fmt.Sprintf("Line %d of content", i+1))
			}
			longContent := strings.Join(lines, "\n")

			m := tuiModel{
				width:       80,
				height:      24,
				currentView: tt.view,
				mdCache:     newMarkdownCache(2),
				currentReview: &storage.Review{
					ID:  1,
					Job: &storage.ReviewJob{GitRef: "abc"},
				},
			}
			tt.setup(&m, longContent)

			tt.render(&m)
			maxScroll := tt.getMax(m)
			if maxScroll == 0 {
				t.Fatal("Expected non-zero max scroll")
			}

			// Page down past end
			for range 20 {
				m, _ = pressSpecial(m, tea.KeyPgDown)
			}

			if s := tt.getScroll(m); s > maxScroll {
				t.Errorf("Scroll %d exceeded max %d", s, maxScroll)
			}

			// Page up
			before := tt.getScroll(m)
			m, _ = pressSpecial(m, tea.KeyPgUp)
			if tt.getScroll(m) >= before {
				t.Error("Page up did not reduce scroll")
			}
		})
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
			lines := strings.SplitSeq(out, "\n")
			// Find the longLine (or its truncation) in the output
			for line := range lines {
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
