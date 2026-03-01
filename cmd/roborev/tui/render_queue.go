package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

func (m model) getVisibleJobs() []storage.ReviewJob {
	if len(m.activeRepoFilter) == 0 && m.activeBranchFilter == "" && !m.hideAddressed {
		return m.jobs
	}
	var visible []storage.ReviewJob
	for _, job := range m.jobs {
		if m.isJobVisible(job) {
			visible = append(visible, job)
		}
	}
	return visible
}
func (m model) queueHelpRows() [][]helpItem {
	row1 := []helpItem{
		{"x", "cancel"}, {"r", "rerun"}, {"l", "log"}, {"p", "prompt"},
		{"c", "comment"}, {"y", "copy"}, {"m", "commit msg"}, {"F", "fix"},
	}
	row2 := []helpItem{
		{"↑/↓", "nav"}, {"↵", "review"}, {"a", "handled"},
	}
	if !m.lockedRepoFilter || !m.lockedBranchFilter {
		row2 = append(row2, helpItem{"f", "filter"})
	}
	row2 = append(row2, helpItem{"h", "hide"}, helpItem{"D", "focus"}, helpItem{"T", "tasks"}, helpItem{"?", "help"}, helpItem{"q", "quit"})
	return [][]helpItem{row1, row2}
}
func (m model) queueHelpLines() int {
	return len(reflowHelpRows(m.queueHelpRows(), m.width))
}

// queueCompact returns true when chrome should be hidden
// (status line, table header, scroll indicator, flash, help footer).
// Triggered automatically for short terminals or manually via distraction-free mode.
func (m model) queueCompact() bool {
	return m.height < 15 || m.distractionFree
}

func (m model) queueVisibleRows() int {
	if m.queueCompact() {
		// compact: title(1) only
		return max(m.height-1, 1)
	}
	// title(1) + status(2) + header(1) + separator(1) + scroll(1) + flash(1) + help(dynamic)
	reserved := 7 + m.queueHelpLines()
	visibleRows := max(m.height-reserved, 3)
	return visibleRows
}
func (m model) canPaginate() bool {
	return m.hasMore && !m.loadingMore && !m.loadingJobs &&
		len(m.activeRepoFilter) <= 1 && m.activeBranchFilter != branchNone
}
func (m model) getVisibleSelectedIdx() int {
	if m.selectedIdx < 0 {
		return -1
	}
	if len(m.activeRepoFilter) == 0 && m.activeBranchFilter == "" && !m.hideAddressed {
		return m.selectedIdx
	}
	count := 0
	for i, job := range m.jobs {
		if m.isJobVisible(job) {
			if i == m.selectedIdx {
				return count
			}
			count++
		}
	}
	return -1
}

// Queue table column indices.
const (
	colSel     = iota // "> " selection indicator
	colJobID          // Job ID
	colRef            // Git ref (short SHA or range)
	colBranch         // Branch name
	colRepo           // Repository display name
	colAgent          // Agent name
	colStatus         // Job status
	colQueued         // Enqueue timestamp
	colElapsed        // Elapsed time
	colPF             // Pass/Fail verdict
	colHandled        // Addressed status
	colCount          // total number of columns
)

func (m model) renderQueueView() string {
	var b strings.Builder
	compact := m.queueCompact()

	// Title with version, optional update notification, and filter indicators (in stack order)
	var title strings.Builder
	fmt.Fprintf(&title, "roborev queue (%s)", version.Version)
	for _, filterType := range m.filterStack {
		switch filterType {
		case filterTypeRepo:
			if len(m.activeRepoFilter) > 0 {
				filterName := m.getDisplayName(m.activeRepoFilter[0], filepath.Base(m.activeRepoFilter[0]))
				fmt.Fprintf(&title, " [f: %s]", filterName)
			}
		case filterTypeBranch:
			if m.activeBranchFilter != "" {
				fmt.Fprintf(&title, " [b: %s]", m.activeBranchFilter)
			}
		}
	}
	if m.hideAddressed {
		title.WriteString(" [hiding addressed]")
	}
	b.WriteString(titleStyle.Render(title.String()))
	// In compact mode, show version mismatch inline since the status area is hidden
	if compact && m.versionMismatch {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}).Bold(true)
		b.WriteString(" ")
		b.WriteString(errorStyle.Render(fmt.Sprintf("MISMATCH: TUI %s != Daemon %s", version.Version, m.daemonVersion)))
	}
	b.WriteString("\x1b[K\n") // Clear to end of line

	if !compact {
		// Status line - use server-side aggregate counts for paginated views,
		// fall back to client-side counting for multi-repo filters (which load all jobs)
		var statusLine string
		var done, addressed, unaddressed int
		if len(m.activeRepoFilter) > 1 || m.activeBranchFilter == branchNone {
			// Client-side filtered views load all jobs, so count locally
			for _, job := range m.jobs {
				if len(m.activeRepoFilter) > 0 && !m.repoMatchesFilter(job.RepoPath) {
					continue
				}
				if m.activeBranchFilter == branchNone && job.Branch != "" {
					continue
				}
				if job.Status == storage.JobStatusDone {
					done++
					if job.Addressed != nil {
						if *job.Addressed {
							addressed++
						} else {
							unaddressed++
						}
					}
				}
			}
		} else {
			done = m.jobStats.Done
			addressed = m.jobStats.Addressed
			unaddressed = m.jobStats.Unaddressed
		}
		if len(m.activeRepoFilter) > 0 || m.activeBranchFilter != "" {
			statusLine = fmt.Sprintf("Daemon: %s | Done: %d | Addressed: %d | Unaddressed: %d",
				m.daemonVersion, done, addressed, unaddressed)
		} else {
			statusLine = fmt.Sprintf("Daemon: %s | Workers: %d/%d | Done: %d | Addressed: %d | Unaddressed: %d",
				m.daemonVersion,
				m.status.ActiveWorkers, m.status.MaxWorkers,
				done, addressed, unaddressed)
		}
		b.WriteString(statusStyle.Render(statusLine))
		b.WriteString("\x1b[K\n") // Clear status line

		// Update notification on line 3 (above the table)
		if m.updateAvailable != "" {
			updateStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "226"}).Bold(true)
			var updateMsg string
			if m.updateIsDevBuild {
				updateMsg = fmt.Sprintf("Dev build - latest release: %s - run 'roborev update --force'", m.updateAvailable)
			} else {
				updateMsg = fmt.Sprintf("Update available: %s - run 'roborev update'", m.updateAvailable)
			}
			b.WriteString(updateStyle.Render(updateMsg))
		}
		b.WriteString("\x1b[K\n") // Clear line 3
	}

	visibleJobList := m.getVisibleJobs()
	visibleSelectedIdx := m.getVisibleSelectedIdx()

	visibleRows := m.queueVisibleRows()

	// Track scroll indicator state for later
	var scrollInfo string
	start := 0
	end := 0

	if len(visibleJobList) == 0 {
		if m.loadingJobs || m.loadingMore {
			b.WriteString("Loading...")
			b.WriteString("\x1b[K\n")
		} else if len(m.activeRepoFilter) > 0 || m.hideAddressed {
			b.WriteString("No jobs matching filters")
			b.WriteString("\x1b[K\n")
		} else {
			b.WriteString("No jobs in queue")
			b.WriteString("\x1b[K\n")
		}
		// Pad empty queue to fill visibleRows (minus 1 for the message we just wrote)
		// Also need header lines (2) to match non-empty case (skip in compact)
		linesWritten := 1
		padTarget := visibleRows
		if !compact {
			padTarget += 2 // +2 for header lines we skipped
		}
		for linesWritten < padTarget {
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
	} else {
		// Calculate ID column width based on max ID
		idWidth := 5 // minimum width (fits "JobID" header)
		for _, job := range visibleJobList {
			w := len(fmt.Sprintf("%d", job.ID))
			if w > idWidth {
				idWidth = w
			}
		}

		// Determine which jobs to show, keeping selected item visible
		start = 0
		end = len(visibleJobList)

		if len(visibleJobList) > visibleRows {
			// Center the selected item when possible
			start = max(visibleSelectedIdx-visibleRows/2, 0)
			end = start + visibleRows
			if end > len(visibleJobList) {
				end = len(visibleJobList)
				start = end - visibleRows
			}
		}

		// Build row data — each row is plain text cells, styling handled by StyleFunc
		rows := make([][]string, 0, end-start)
		windowJobs := visibleJobList[start:end] // jobs in the visible window
		for i, job := range windowJobs {
			sel := "  "
			if start+i == visibleSelectedIdx {
				sel = "> "
			}
			cells := m.jobCells(job)
			row := make([]string, colCount)
			row[colSel] = sel
			row[colJobID] = fmt.Sprintf("%d", job.ID)
			copy(row[colRef:], cells)
			rows = append(rows, row)
		}

		// Compute the selected row index within the visible window
		selectedWindowIdx := visibleSelectedIdx - start

		t := table.New().
			BorderTop(false).
			BorderBottom(false).
			BorderLeft(false).
			BorderRight(false).
			BorderColumn(false).
			BorderRow(false).
			BorderHeader(!compact).
			Border(lipgloss.Border{Bottom: "─"}).
			BorderStyle(lipgloss.NewStyle()).
			Width(m.width).
			Wrap(false).
			StyleFunc(func(row, col int) lipgloss.Style {
				s := lipgloss.NewStyle()

				// Fixed-width columns
				switch col {
				case colSel:
					s = s.Width(2)
				case colJobID:
					s = s.Width(idWidth)
				case colStatus:
					s = s.Width(8)
				case colQueued:
					s = s.Width(12)
				case colElapsed:
					s = s.Width(8).Align(lipgloss.Right)
				case colPF:
					s = s.Width(3)
				}

				// Header row styling
				if row == table.HeaderRow {
					return s.Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"})
				}

				// Selection highlighting — uniform background, no per-cell coloring
				if row == selectedWindowIdx {
					return s.Background(lipgloss.AdaptiveColor{Light: "153", Dark: "24"})
				}

				// Per-cell coloring for non-selected rows
				if row >= 0 && row < len(windowJobs) {
					job := windowJobs[row]
					switch col {
					case colStatus:
						switch job.Status {
						case storage.JobStatusQueued:
							s = s.Foreground(queuedStyle.GetForeground())
						case storage.JobStatusRunning:
							s = s.Foreground(runningStyle.GetForeground())
						case storage.JobStatusDone:
							s = s.Foreground(doneStyle.GetForeground())
						case storage.JobStatusFailed:
							s = s.Foreground(failedStyle.GetForeground())
						case storage.JobStatusCanceled:
							s = s.Foreground(canceledStyle.GetForeground())
						}
					case colPF:
						if job.Verdict != nil {
							if *job.Verdict == "P" {
								s = s.Foreground(passStyle.GetForeground())
							} else {
								s = s.Foreground(failStyle.GetForeground())
							}
						}
					case colHandled:
						if job.Addressed != nil {
							if *job.Addressed {
								s = s.Foreground(addressedStyle.GetForeground())
							} else {
								s = s.Foreground(queuedStyle.GetForeground())
							}
						}
					}
				}
				return s
			})

		if !compact {
			t = t.Headers("", "JobID", "Ref", "Branch", "Repo", "Agent", "Status", "Queued", "Elapsed", "P/F", "Handled")
		}
		t = t.Rows(rows...)

		tableStr := t.Render()
		b.WriteString(tableStr)
		b.WriteString("\x1b[K\n")

		// Pad with clear-to-end-of-line sequences to prevent ghost text
		tableLines := strings.Count(tableStr, "\n") + 1
		headerLines := 0
		if !compact {
			headerLines = 2 // header + separator
		}
		jobLinesWritten := tableLines - headerLines
		for jobLinesWritten < visibleRows {
			b.WriteString("\x1b[K\n")
			jobLinesWritten++
		}

		// Build scroll indicator if needed
		if len(visibleJobList) > visibleRows || m.hasMore || m.loadingMore {
			if m.loadingMore {
				scrollInfo = fmt.Sprintf("[showing %d-%d of %d] Loading more...", start+1, end, len(visibleJobList))
			} else if m.hasMore && len(m.activeRepoFilter) <= 1 {
				scrollInfo = fmt.Sprintf("[showing %d-%d of %d+] scroll down to load more", start+1, end, len(visibleJobList))
			} else if len(visibleJobList) > visibleRows {
				scrollInfo = fmt.Sprintf("[showing %d-%d of %d]", start+1, end, len(visibleJobList))
			}
		}
	}

	if !compact {
		// Always emit scroll indicator line (blank if no scroll info) to maintain consistent height
		if scrollInfo != "" {
			b.WriteString(statusStyle.Render(scrollInfo))
		}
		b.WriteString("\x1b[K\n") // Clear scroll indicator line

		// Status line: flash message (temporary)
		// Version mismatch takes priority over flash messages (it's persistent and important)
		if m.versionMismatch {
			errorStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}).Bold(true) // Red
			b.WriteString(errorStyle.Render(fmt.Sprintf("VERSION MISMATCH: TUI %s != Daemon %s - restart TUI or daemon", version.Version, m.daemonVersion)))
		} else if m.flashMessage != "" && time.Now().Before(m.flashExpiresAt) && m.flashView == viewQueue {
			flashStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"}) // Green
			b.WriteString(flashStyle.Render(m.flashMessage))
		}
		b.WriteString("\x1b[K\n") // Clear to end of line

		// Help
		b.WriteString(renderHelpTable(m.queueHelpRows(), m.width))
	}

	output := b.String()
	if compact {
		// Trim trailing newline to avoid layout overflow (compact has no
		// help footer to consume the final line).
		output = strings.TrimSuffix(output, "\n")
	}
	output += "\x1b[K" // Clear to end of line (no newline at end)
	output += "\x1b[J" // Clear to end of screen to prevent artifacts

	return output
}

// jobCells returns plain text cell values for a job row.
// Order: ref, branch, repo, agent, status, queued, elapsed, verdict, handled
// (colRef through colHandled, 9 values).
func (m model) jobCells(job storage.ReviewJob) []string {
	ref := shortJobRef(job)
	if !config.IsDefaultReviewType(job.ReviewType) {
		ref = ref + " [" + job.ReviewType + "]"
	}

	branch := m.getBranchForJob(job)

	repo := m.getDisplayName(job.RepoPath, job.RepoName)
	if m.status.MachineID != "" && job.SourceMachineID != "" && job.SourceMachineID != m.status.MachineID {
		repo += " [R]"
	}

	agentName := job.Agent
	if agentName == "claude-code" {
		agentName = "claude"
	}

	enqueued := job.EnqueuedAt.Local().Format("Jan 02 15:04")

	elapsed := ""
	if job.StartedAt != nil {
		if job.FinishedAt != nil {
			elapsed = job.FinishedAt.Sub(*job.StartedAt).Round(time.Second).String()
		} else {
			elapsed = time.Since(*job.StartedAt).Round(time.Second).String()
		}
	}

	status := string(job.Status)

	verdict := "-"
	if job.Verdict != nil {
		verdict = *job.Verdict
	}

	handled := ""
	if job.Addressed != nil {
		if *job.Addressed {
			handled = "yes"
		} else {
			handled = "no"
		}
	}

	return []string{ref, branch, repo, agentName, status, enqueued, elapsed, verdict, handled}
}

// commandLineForJob computes the representative agent command line from job parameters.
// Returns empty string if the agent is not available.
func commandLineForJob(job *storage.ReviewJob) string {
	if job == nil {
		return ""
	}
	a, err := agent.Get(job.Agent)
	if err != nil {
		return ""
	}
	reasoning := strings.ToLower(strings.TrimSpace(job.Reasoning))
	if reasoning == "" {
		reasoning = "thorough"
	}
	cmd := a.WithReasoning(agent.ParseReasoningLevel(reasoning)).WithAgentic(job.Agentic).WithModel(job.Model).CommandLine()
	return stripControlChars(cmd)
}

// stripControlChars removes all control characters including C0 (\x00-\x1f),
// DEL (\x7f), and C1 (\x80-\x9f) from a string to prevent terminal escape
// injection and line/tab spoofing in single-line display contexts.
func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
