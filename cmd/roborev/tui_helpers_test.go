package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/storage"
)

var testANSIRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripTestANSI(s string) string {
	return testANSIRegex.ReplaceAllString(s, "")
}

func TestRenderMarkdownLinesPreservesNewlines(t *testing.T) {
	// Verify that single newlines in plain text are preserved (not collapsed into one paragraph)
	lines := renderMarkdownLines("Line 1\nLine 2\nLine 3", 80, 80, styles.DarkStyleConfig, 2)

	found := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripTestANSI(line))
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
		name          string
		text          string
		width         int
		id            int
		expectHit     bool
		expectedMatch string
	}{
		{"SameInputs", baseText, baseWidth, int(baseID), true, ""},
		{"DiffText", "Different", baseWidth, int(baseID), false, "Different"},
		{"DiffWidth", baseText, 40, int(baseID), false, ""},
		{"DiffID", baseText, baseWidth, 2, false, ""},
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

			if tt.expectedMatch != "" {
				combined := ""
				for _, line := range lines2 {
					combined += stripTestANSI(line)
				}
				if !strings.Contains(combined, tt.expectedMatch) {
					t.Errorf("Expected output to contain %q, got %q", tt.expectedMatch, combined)
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
		name string
		view tuiView
	}{
		{"PromptView", tuiViewPrompt},
		{"ReviewView", tuiViewReview},
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

			var maxScroll int
			if tt.view == tuiViewPrompt {
				m.currentReview.Prompt = longContent
				m.renderPromptView()
				maxScroll = m.mdCache.lastPromptMaxScroll
			} else {
				m.currentReview.Output = longContent
				m.renderReviewView()
				maxScroll = m.mdCache.lastReviewMaxScroll
			}

			if maxScroll == 0 {
				t.Fatal("Expected non-zero max scroll")
			}

			// Page down past end
			for range 20 {
				m, _ = pressSpecial(m, tea.KeyPgDown)
			}

			getScroll := func(m tuiModel) int {
				if tt.view == tuiViewPrompt {
					return m.promptScroll
				}
				return m.reviewScroll
			}

			if s := getScroll(m); s > maxScroll {
				t.Errorf("Scroll %d exceeded max %d", s, maxScroll)
			}

			// Page up
			before := getScroll(m)
			m, _ = pressSpecial(m, tea.KeyPgUp)
			if getScroll(m) >= before {
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
				if strings.Contains(line, "xxxxxxxxxx") { // 10 x's is enough to identify the line
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
		combined += stripTestANSI(line) + " "
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
		stripped := stripTestANSI(line)
		if len(stripped) > width+10 { // small tolerance for trailing spaces
			t.Errorf("line %d exceeds width %d: len=%d %q", i, width, len(stripped), stripped)
		}
	}
}

func TestReflowHelpRows(t *testing.T) {

	tests := []struct {
		name     string
		items    []helpItem
		width    int
		wantRows int
	}{
		{
			name:     "all fit in one row",
			items:    []helpItem{{"a", "one"}, {"b", "two"}},
			width:    80,
			wantRows: 1,
		},
		{
			name:     "split into two rows",
			items:    []helpItem{{"a", "one"}, {"b", "two"}, {"c", "three"}, {"d", "four"}},
			width:    28, // 4 cols aligned = 29 chars, won't fit in 28
			wantRows: 2,
		},
		{
			name:     "width zero returns unchanged",
			items:    []helpItem{{"a", "one"}, {"b", "two"}, {"c", "three"}},
			width:    0,
			wantRows: 1,
		},
		{
			name:     "single wide item gets own row",
			items:    []helpItem{{"very-long-item-label", "description"}},
			width:    20,
			wantRows: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := reflowHelpRows([][]helpItem{tc.items}, tc.width)
			if len(rows) != tc.wantRows {
				t.Errorf("got %d rows, want %d (items=%v, width=%d)",
					len(rows), tc.wantRows, tc.items, tc.width)
			}
		})
	}
}

func TestRenderHelpTableLinesWithinWidth(t *testing.T) {

	// Real help row sets used by the TUI views.
	helpSets := map[string][][]helpItem{
		"queue": {
			{{"x", "cancel"}, {"r", "rerun"}, {"l", "log"}, {"p", "prompt"}, {"c", "comment"}, {"y", "copy"}, {"m", "commit msg"}, {"F", "fix"}},
			{{"↑/↓", "navigate"}, {"enter", "review"}, {"a", "addressed"}, {"f", "filter"}, {"h", "hide"}, {"T", "tasks"}, {"?", "help"}, {"q", "quit"}},
		},
		"review": {
			{{"p", "prompt"}, {"c", "comment"}, {"m", "commit msg"}, {"a", "addressed"}, {"y", "copy"}, {"F", "fix"}},
			{{"↑/↓", "scroll"}, {"←/→", "prev/next"}, {"?", "commands"}, {"esc", "back"}},
		},
		"filter": {
			{{"↑/↓", "navigate"}, {"→/←", "expand/collapse"}, {"↵", "select"}, {"esc", "cancel"}, {"type to search", ""}},
		},
		"tasks": {
			{{"enter", "view"}, {"p", "patch"}, {"A", "apply"}, {"l", "log"}, {"x", "cancel"}, {"r", "refresh"}, {"?", "help"}, {"T/esc", "back"}},
		},
	}

	widths := []int{50, 80, 100, 120}

	for name, rows := range helpSets {
		for _, width := range widths {
			t.Run(fmt.Sprintf("%s/width=%d", name, width), func(t *testing.T) {
				rendered := renderHelpTable(rows, width)
				reflowed := reflowHelpRows(rows, width)

				// Rendered line count must match reflowed row count.
				lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
				if len(lines) != len(reflowed) {
					t.Errorf("rendered %d lines but reflowed to %d rows",
						len(lines), len(reflowed))
				}

				// No rendered line should exceed the target width.
				for i, line := range lines {
					visible := stripTestANSI(line)
					visW := runewidth.StringWidth(visible)
					if visW > width {
						t.Errorf("line %d width %d > target %d: %q",
							i, visW, width, visible)
					}
				}
			})
		}
	}
}

func TestSanitizeForDisplay(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text unchanged",
			input:    "Hello, world!",
			expected: "Hello, world!",
		},
		{
			name:     "preserves newlines and tabs",
			input:    "Line1\n\tIndented",
			expected: "Line1\n\tIndented",
		},
		{
			name:     "strips ANSI color codes",
			input:    "\x1b[31mred text\x1b[0m",
			expected: "red text",
		},
		{
			name:     "strips cursor movement",
			input:    "\x1b[2Jhello\x1b[H",
			expected: "hello",
		},
		{
			name:     "strips OSC sequences (title set with BEL)",
			input:    "\x1b]0;Evil Title\x07normal text",
			expected: "normal text",
		},
		{
			name:     "strips OSC sequences (title set with ST)",
			input:    "\x1b]0;Evil Title\x1b\\normal text",
			expected: "normal text",
		},
		{
			name:     "strips control characters",
			input:    "hello\x00world\x07\x08test",
			expected: "helloworldtest",
		},
		{
			name:     "handles complex escape sequence",
			input:    "\x1b[1;32mBold Green\x1b[0m and \x1b[4munderline\x1b[24m",
			expected: "Bold Green and underline",
		},
		{
			name:     "empty string unchanged",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeForDisplay(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeForDisplay(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestPatchFiles(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		want  []string
	}{
		{
			name: "simple add",
			patch: `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1 +1,2 @@
 package main
+// new line
`,
			want: []string{"main.go"},
		},
		{
			name: "file in b/ directory not double-stripped",
			patch: `diff --git a/b/main.go b/b/main.go
--- a/b/main.go
+++ b/b/main.go
@@ -1 +1,2 @@
 package main
+// new line
`,
			want: []string{"b/main.go"},
		},
		{
			name: "file in a/ directory not double-stripped",
			patch: `diff --git a/a/utils.go b/a/utils.go
--- a/a/utils.go
+++ b/a/utils.go
@@ -1 +1,2 @@
 package a
+// new line
`,
			want: []string{"a/utils.go"},
		},
		{
			name: "new file with /dev/null",
			patch: `diff --git a/new.go b/new.go
--- /dev/null
+++ b/new.go
@@ -0,0 +1 @@
+package main
`,
			want: []string{"new.go"},
		},
		{
			name: "deleted file with /dev/null",
			patch: `diff --git a/old.go b/old.go
--- a/old.go
+++ /dev/null
@@ -1 +0,0 @@
-package main
`,
			want: []string{"old.go"},
		},
		{
			name: "rename",
			patch: `diff --git a/old.go b/renamed.go
--- a/old.go
+++ b/renamed.go
@@ -1 +1 @@
-package old
+package renamed
`,
			want: []string{"old.go", "renamed.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := patchFiles(tt.patch)
			if err != nil {
				t.Fatalf("patchFiles returned error: %v", err)
			}
			wantSet := map[string]bool{}
			for _, f := range tt.want {
				wantSet[f] = true
			}
			if len(got) != len(tt.want) {
				t.Errorf("expected %d files, got %d: %v", len(tt.want), len(got), got)
			}
			gotSet := map[string]bool{}
			for _, f := range got {
				if gotSet[f] {
					t.Errorf("duplicate file in output: %q", f)
				}
				gotSet[f] = true
			}
			for f := range wantSet {
				if !gotSet[f] {
					t.Errorf("missing expected file %q", f)
				}
			}
			for f := range gotSet {
				if !wantSet[f] {
					t.Errorf("unexpected file %q", f)
				}
			}
		})
	}
}

func TestDirtyPatchFilesError(t *testing.T) {
	// dirtyPatchFiles should return an error when git diff fails
	// (e.g., invalid repo path), not silently return nil.
	missingPath := filepath.Join(t.TempDir(), "missing")
	_, err := dirtyPatchFiles(missingPath, []string{"file.go"})
	if err == nil {
		t.Fatal("expected error from dirtyPatchFiles with invalid repo, got nil")
	}
	if !strings.Contains(err.Error(), "git diff") {
		t.Errorf("expected error to mention 'git diff', got: %v", err)
	}
}
