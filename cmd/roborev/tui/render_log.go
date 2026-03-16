package tui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

func (m model) renderLogView() string {
	var b strings.Builder

	// Title with job info (matches Prompt view format)
	var title string
	job := m.logViewLookupJob()
	if job != nil {
		ref := shortJobRef(*job)
		agentStr := formatAgentLabel(job.Agent, job.Model)
		title = fmt.Sprintf(
			"Log #%d %s (%s)", job.ID, ref, agentStr,
		)
	} else {
		title = fmt.Sprintf("Log #%d", m.logJobID)
	}
	if m.logStreaming {
		title += " " + runningStyle.Render("● live")
	} else {
		title += " " + doneStyle.Render("● complete")
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\x1b[K\n")

	// Show command line below title (dimmed, like Prompt view)
	headerLines := 1
	if cmdLine := commandLineForJob(job); cmdLine != "" {
		cmdText := "Command: " + cmdLine
		if m.width > 0 && runewidth.StringWidth(cmdText) > m.width {
			cmdText = runewidth.Truncate(cmdText, m.width, "…")
		}
		b.WriteString(statusStyle.Render(cmdText))
		b.WriteString("\x1b[K\n")
		headerLines++
	}

	// Calculate visible area (must match logVisibleLines())
	logHelp := m.logHelpRows()
	logHelpLines := len(reflowHelpRows(logHelp, m.width))
	reservedLines := (2 + logHelpLines) + headerLines // title + cmd(0-1) + sep + status + help(N)
	visibleLines := max(m.height-reservedLines, 1)

	// Clamp scroll
	maxScroll := max(len(m.logLines)-visibleLines, 0)
	scroll := max(min(m.logScroll, maxScroll), 0)

	// Separator
	b.WriteString(strings.Repeat("─", m.width))
	b.WriteString("\x1b[K\n")

	// Render lines (pre-styled by streamFormatter)
	linesWritten := 0
	if len(m.logLines) == 0 {
		if m.logLines == nil {
			b.WriteString(statusStyle.Render("Waiting for output..."))
		} else {
			b.WriteString(statusStyle.Render("(no output)"))
		}
		b.WriteString("\x1b[K\n")
		linesWritten++
	} else {
		end := min(scroll+visibleLines, len(m.logLines))
		for i := scroll; i < end; i++ {
			b.WriteString(m.logLines[i].text)
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
	}

	// Pad remaining lines
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Status line with position and follow mode
	var status string
	if len(m.logLines) > visibleLines {
		// Calculate actual displayed range (not including padding)
		displayEnd := min(scroll+visibleLines, len(m.logLines))
		status = fmt.Sprintf("[%d-%d of %d lines]", scroll+1, displayEnd, len(m.logLines))
	} else {
		status = fmt.Sprintf("[%d lines]", len(m.logLines))
	}
	if m.logFollow {
		status += " " + runningStyle.Render("[following]")
	} else {
		status += " " + statusStyle.Render("[paused - G to follow]")
	}
	b.WriteString(statusStyle.Render(status))
	b.WriteString("\x1b[K\n")

	b.WriteString(renderHelpTable(logHelp, m.width))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J") // Clear to end of screen

	return b.String()
}

func formatHelpLine(key, desc string) string {
	return fmt.Sprintf("  %-14s %s", key, desc)
}

func disabledHelpLine(key, desc string) string {
	return statusStyle.Render(formatHelpLine(key, desc+" (disabled)"))
}

func helpLines(tasksEnabled, noQuit bool) []string {
	shortcuts := []struct {
		group string
		keys  []struct{ key, desc string }
	}{
		{
			group: "Queue View",
			keys: []struct{ key, desc string }{
				{"↑/k, ↓/j", "Navigate jobs"},
				{"g/Home", "Jump to top"},
				{"PgUp/PgDn", "Page through list"},
				{"enter", "View review"},
				{"p", "View prompt"},
				{"l", "View agent log"},
				{"m", "View commit message"},
			},
		},
		{
			group: "Actions",
			keys: []struct{ key, desc string }{
				{"a", "Toggle closed"},
				{"c", "Add comment"},
				{"y", "Copy review to clipboard"},
				{"x", "Cancel running/queued job"},
				{"r", "Re-run completed/failed job"},
				{"o", "Column options (visibility, order, borders)"},
				{"D", "Toggle distraction-free mode"},
			},
		},
		{
			group: "Filtering",
			keys: []struct{ key, desc string }{
				{"f", "Filter by repository/branch"},
				{"h", "Toggle hide closed/failed"},
				{"esc", "Clear filters (one at a time)"},
			},
		},
		{
			group: "Review View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Scroll content"},
				{"←/→", "Previous / next review"},
				{"PgUp/PgDn", "Page through content"},
				{"p", "Switch to prompt view"},
				{"a", "Toggle closed"},
				{"c", "Add comment"},
				{"y", "Copy review to clipboard"},
				{"m", "View commit message"},
				{"F", "Trigger fix (opens inline panel)"},
				{"esc/q", "Back to queue"},
			},
		},
		{
			group: "Prompt View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Scroll content"},
				{"←/→", "Previous / next prompt"},
				{"PgUp/PgDn", "Page through content"},
				{"p", "Switch to review / back to queue"},
				{"esc/q", "Back to queue"},
			},
		},
		{
			group: "Log View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Scroll output"},
				{"←/→", "Previous / next log"},
				{"PgUp/PgDn", "Page through output"},
				{"g", "Toggle follow mode / jump to top"},
				{"x", "Cancel job"},
				{"esc/q", "Back to queue"},
			},
		},
		{
			group: "Tasks View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Navigate fix jobs"},
				{"A", "Apply patch from completed fix"},
				{"R", "Re-trigger fix (rebase)"},
				{"l", "View agent log"},
				{"x", "Cancel running/queued fix job"},
				{"o", "Column options (order, borders)"},
				{"esc/T", "Back to queue"},
			},
		},
		{
			group: "General",
			keys: func() []struct{ key, desc string } {
				keys := []struct{ key, desc string }{
					{"?", "Toggle this help"},
				}
				if !noQuit {
					keys = append(keys, struct{ key, desc string }{
						"q", "Quit (from queue view)",
					})
				}
				return keys
			}(),
		},
	}

	var lines []string
	for i, g := range shortcuts {
		lines = append(lines, "\x00group:"+g.group)
		if g.group == "Tasks View" && !tasksEnabled {
			lines = append(lines, statusStyle.Render("  advanced.tasks_enabled = false"))
			for _, k := range g.keys {
				lines = append(lines, disabledHelpLine(k.key, k.desc))
			}
			if i < len(shortcuts)-1 {
				lines = append(lines, "")
			}
			continue
		}
		for _, k := range g.keys {
			if g.group == "Review View" && k.key == "F" && !tasksEnabled {
				lines = append(lines, disabledHelpLine(k.key, k.desc))
				continue
			}
			lines = append(lines, formatHelpLine(k.key, k.desc))
		}
		if g.group == "Actions" {
			if tasksEnabled {
				lines = append(lines, formatHelpLine("F", "Trigger fix for selected review"))
				lines = append(lines, formatHelpLine("T", "Open Tasks view"))
			} else {
				lines = append(lines, disabledHelpLine("F", "Trigger fix for selected review"))
				lines = append(lines, disabledHelpLine("T", "Open Tasks view"))
			}
		}
		if i < len(shortcuts)-1 {
			lines = append(lines, "")
		}
	}
	return lines
}
func (m model) helpMaxScroll() int {
	reservedLines := 3 // title + blank + help hint
	visibleLines := max(m.height-reservedLines, 5)
	maxScroll := len(helpLines(m.tasksWorkflowEnabled(), m.noQuit)) - visibleLines
	if maxScroll < 0 {
		return 0
	}
	return maxScroll
}
func (m model) renderHelpView() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Keyboard Shortcuts"))
	b.WriteString("\x1b[K\n\x1b[K\n")

	allLines := helpLines(m.tasksWorkflowEnabled(), m.noQuit)

	// Calculate visible area: title(1) + blank(1) + help(1)
	reservedLines := 3
	visibleLines := max(m.height-reservedLines, 5)

	// Clamp scroll
	maxScroll := max(len(allLines)-visibleLines, 0)
	scroll := min(m.helpScroll, maxScroll)

	// Render visible window
	end := min(scroll+visibleLines, len(allLines))
	linesWritten := 0
	for _, line := range allLines[scroll:end] {
		if after, ok := strings.CutPrefix(line, "\x00group:"); ok {
			b.WriteString(selectedStyle.Render(after))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Pad remaining space
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	b.WriteString(renderHelpTable([][]helpItem{
		{{"↑/↓", "scroll"}, {"esc/q/?", "close"}},
	}, m.width))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J") // Clear to end of screen

	return b.String()
}
