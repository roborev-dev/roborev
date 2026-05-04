package tui

import (
	"strconv"
	"strings"
)

// derefOrZero returns the dereferenced int, or 0 if the pointer is nil.
// Used to safely combine the three finding-count pointers that JSON
// deserialization or ad-hoc producers may populate inconsistently.
func derefOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// renderSeverityBadge formats finding counts as "3/2/5" with severity-
// colored numbers (red high, yellow medium, blue low) and dim slashes.
// Zero counts render in the same dim grey as the slashes so non-zero
// values pop visually. Plain-text width is 5 for all-single-digit
// counts (e.g. "3/2/5") and grows by one per extra digit per slot.
func renderSeverityBadge(h, m, l int) string {
	parts := []string{
		formatSeveritySlot(h, severityHighStyle),
		formatSeveritySlot(m, severityMediumStyle),
		formatSeveritySlot(l, severityLowStyle),
	}
	return strings.Join(parts, severityZeroStyle.Render("/"))
}

// lipglossStyleRenderer is the minimal interface from lipgloss.Style that
// formatSeveritySlot consumes. lipgloss.Style satisfies it natively.
type lipglossStyleRenderer interface {
	Render(strs ...string) string
}

func formatSeveritySlot(count int, active lipglossStyleRenderer) string {
	text := strconv.Itoa(count)
	if count == 0 {
		return severityZeroStyle.Render(text)
	}
	return active.Render(text)
}
