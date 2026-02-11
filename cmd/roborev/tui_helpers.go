package main

import (
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/roborev-dev/roborev/internal/storage"
)

// Filter type constants used in filterStack and popFilter/pushFilter.
const (
	filterTypeRepo   = "repo"
	filterTypeBranch = "branch"
)

// branchNone is the sentinel value for jobs with no branch information.
const branchNone = "(none)"

// mutateJob finds a job by ID and applies the mutation function.
// Returns true if the job was found and mutated.
func (m *tuiModel) mutateJob(id int64, fn func(*storage.ReviewJob)) bool {
	for i := range m.jobs {
		if m.jobs[i].ID == id {
			fn(&m.jobs[i])
			return true
		}
	}
	return false
}

// setJobAddressed updates the addressed state for a job by ID.
// Handles nil pointer by allocating if necessary.
func (m *tuiModel) setJobAddressed(jobID int64, state bool) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		if job.Addressed == nil {
			job.Addressed = new(bool)
		}
		*job.Addressed = state
	})
}

// setJobStatus updates the status for a job by ID.
func (m *tuiModel) setJobStatus(jobID int64, status storage.JobStatus) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.Status = status
	})
}

// setJobFinishedAt updates the FinishedAt for a job by ID.
func (m *tuiModel) setJobFinishedAt(jobID int64, finishedAt *time.Time) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.FinishedAt = finishedAt
	})
}

// setJobStartedAt updates the StartedAt for a job by ID.
func (m *tuiModel) setJobStartedAt(jobID int64, startedAt *time.Time) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.StartedAt = startedAt
	})
}

// setJobError updates the Error for a job by ID.
func (m *tuiModel) setJobError(jobID int64, errMsg string) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.Error = errMsg
	})
}

// wrapText wraps text to the specified width, preserving existing line breaks
// and breaking at word boundaries when possible. Uses runewidth for correct
// display width calculation with Unicode and wide characters.
func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 100
	}

	var result []string
	for _, line := range strings.Split(text, "\n") {
		lineWidth := runewidth.StringWidth(line)
		if lineWidth <= width {
			result = append(result, line)
			continue
		}

		// Wrap long lines using display width
		for runewidth.StringWidth(line) > width {
			runes := []rune(line)
			breakPoint := 0
			currentWidth := 0

			// Walk runes up to the width limit
			for i, r := range runes {
				rw := runewidth.RuneWidth(r)
				if currentWidth+rw > width {
					break
				}
				currentWidth += rw
				breakPoint = i + 1
			}

			// Ensure forward progress: if the first rune is wider than width,
			// take at least one rune to avoid an infinite loop.
			if breakPoint == 0 {
				breakPoint = 1
			}

			// Try to find a space to break at (look back from breakPoint)
			bestBreak := breakPoint
			scanWidth := 0
			for i := breakPoint - 1; i >= 0; i-- {
				if runes[i] == ' ' {
					bestBreak = i
					break
				}
				scanWidth += runewidth.RuneWidth(runes[i])
				if scanWidth > width/2 {
					break // Don't look back too far
				}
			}

			result = append(result, string(runes[:bestBreak]))
			line = strings.TrimLeft(string(runes[bestBreak:]), " ")
		}
		if len(line) > 0 {
			result = append(result, line)
		}
	}

	return result
}

// markdownCache caches glamour-rendered lines for review and prompt views.
// Stored as a pointer in tuiModel so that View() (value receiver) can update
// the cache and have it persist across bubbletea's model copies.
//
// glamourStyle is detected once at creation time (before bubbletea takes over
// the terminal) to avoid calling termenv.HasDarkBackground() on every render,
// which blocks for seconds inside bubbletea's raw-mode input loop.
type markdownCache struct {
	glamourStyle gansi.StyleConfig // custom style derived from dark/light, detected once at init

	reviewLines []string
	reviewID    int64
	reviewWidth int
	reviewText  string // raw input text used to produce reviewLines

	promptLines []string
	promptID    int64
	promptWidth int
	promptText  string // raw input text used to produce promptLines

	// Max scroll positions computed during the last render.
	// Stored here (in the shared pointer) so key handlers can clamp
	// scroll values even though View() uses a value receiver.
	lastReviewMaxScroll int
	lastPromptMaxScroll int
}

// newMarkdownCache creates a markdownCache, detecting terminal background
// color now (before bubbletea enters raw mode and takes over stdin).
// Builds a custom style with zero margins to avoid extra padding.
func newMarkdownCache() *markdownCache {
	style := styles.LightStyleConfig
	if termenv.HasDarkBackground() {
		style = styles.DarkStyleConfig
	}
	// Remove document and code block margins that add extra indentation.
	zeroMargin := uint(0)
	style.Document.Margin = &zeroMargin
	style.CodeBlock.Margin = &zeroMargin
	// Remove inline code prefix/suffix spaces (rendered as visible
	// colored blocks around `backtick` content).
	style.Code.Prefix = ""
	style.Code.Suffix = ""
	return &markdownCache{glamourStyle: style}
}

// truncateLongLines normalizes tabs and truncates any input line exceeding
// maxWidth so glamour won't word-wrap it. With WithPreservedNewLines each
// line is independent, so this prevents glamour from reflowing code/diff
// lines into multiple output lines.
func truncateLongLines(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return text
	}
	// Expand tabs to 4 spaces. runewidth counts tabs as width 0 but
	// terminals expand them to up to 8 columns, causing width mismatch.
	text = strings.ReplaceAll(text, "\t", "    ")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if runewidth.StringWidth(line) > maxWidth {
			lines[i] = runewidth.Truncate(line, maxWidth, "")
		}
	}
	return strings.Join(lines, "\n")
}

// trailingPadRe matches trailing whitespace and ANSI SGR sequences.
// Glamour pads code block lines with spaces (using background color) to fill
// the wrap width. Stripping this padding prevents overflow on narrow terminals.
var trailingPadRe = regexp.MustCompile(`(\s|\x1b\[[0-9;]*m)+$`)

// stripTrailingPadding removes trailing whitespace and ANSI SGR codes from a
// glamour output line, then appends a reset to ensure clean color state.
func stripTrailingPadding(line string) string {
	return trailingPadRe.ReplaceAllString(line, "") + "\x1b[0m"
}

// renderMarkdownLines renders markdown text using glamour and splits into lines.
// Falls back to wrapText if glamour rendering fails.
func renderMarkdownLines(text string, width int, glamourStyle gansi.StyleConfig) []string {
	// Truncate long lines before glamour so they don't get word-wrapped.
	text = truncateLongLines(text, width)
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(glamourStyle),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return wrapText(text, width)
	}
	out, err := r.Render(text)
	if err != nil {
		return wrapText(text, width)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		line = stripTrailingPadding(line)
		// Truncate output lines that still exceed width (glamour can add
		// indentation for block quotes, lists, etc. beyond the input width).
		if xansi.StringWidth(line) > width {
			line = xansi.Truncate(line, width, "")
		}
		lines[i] = line
	}
	return lines
}

// getReviewLines returns glamour-rendered lines for a review, using the cache
// when the inputs (review ID, width, text) haven't changed.
func (c *markdownCache) getReviewLines(text string, width int, reviewID int64) []string {
	if c.reviewID == reviewID && c.reviewWidth == width && c.reviewText == text {
		return c.reviewLines
	}
	c.reviewLines = renderMarkdownLines(text, width, c.glamourStyle)
	c.reviewID = reviewID
	c.reviewWidth = width
	c.reviewText = text
	return c.reviewLines
}

// getPromptLines returns glamour-rendered lines for a prompt, using the cache
// when the inputs (review ID, width, text) haven't changed.
func (c *markdownCache) getPromptLines(text string, width int, reviewID int64) []string {
	if c.promptID == reviewID && c.promptWidth == width && c.promptText == text {
		return c.promptLines
	}
	c.promptLines = renderMarkdownLines(text, width, c.glamourStyle)
	c.promptID = reviewID
	c.promptWidth = width
	c.promptText = text
	return c.promptLines
}
