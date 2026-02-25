package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
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
		{"↑/↓", "navigate"}, {"↵", "review"}, {"a", "addressed"},
	}
	if !m.lockedRepoFilter || !m.lockedBranchFilter {
		row2 = append(row2, helpItem{"f", "filter"})
	}
	row2 = append(row2, helpItem{"h", "hide"}, helpItem{"T", "tasks"}, helpItem{"?", "help"}, helpItem{"q", "quit"})
	return [][]helpItem{row1, row2}
}
func (m model) queueHelpLines() int {
	return len(reflowHelpRows(m.queueHelpRows(), m.width))
}
func (m model) queueVisibleRows() int {
	// title(1) + status(2) + header(2) + scroll(1) + flash(1) + help(dynamic)
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
func (m model) renderQueueView() string {
	var b strings.Builder

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
	b.WriteString("\x1b[K\n") // Clear to end of line

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

	visibleJobList := m.getVisibleJobs()
	visibleSelectedIdx := m.getVisibleSelectedIdx()

	// Calculate visible job range based on terminal height
	// title(1) + status(2) + header(2) + scroll(1) + flash(1) + help(dynamic)
	reservedLines := 7 + m.queueHelpLines()
	visibleRows := max(m.height-reservedLines,
		// Show at least 3 jobs
		3)

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
		// Also need header lines (2) to match non-empty case
		linesWritten := 1
		for linesWritten < visibleRows+2 { // +2 for header lines we skipped
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

		// Calculate column widths dynamically based on terminal width
		colWidths := m.calculateColumnWidths(idWidth)

		// Header (with 2-char prefix to align with row selector)
		header := fmt.Sprintf("  %-*s %-*s %-*s %-*s %-*s %-8s %-3s %-12s %-8s %s",
			idWidth, "JobID",
			colWidths.ref, "Ref",
			colWidths.branch, "Branch",
			colWidths.repo, "Repo",
			colWidths.agent, "Agent",
			"Status", "P/F", "Queued", "Elapsed", "Addressed")
		b.WriteString(statusStyle.Render(header))
		b.WriteString("\x1b[K\n") // Clear to end of line
		b.WriteString("  " + strings.Repeat("-", min(m.width-4, 200)))
		b.WriteString("\x1b[K\n") // Clear to end of line

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

		// Jobs
		jobLinesWritten := 0
		for i := start; i < end; i++ {
			job := visibleJobList[i]
			selected := i == visibleSelectedIdx
			line := m.renderJobLine(job, selected, idWidth, colWidths)
			if selected {
				// Pad line to full terminal width for background styling
				lineWidth := lipgloss.Width(line)
				paddedLine := "> " + line
				if padding := m.width - lineWidth - 2; padding > 0 {
					paddedLine += strings.Repeat(" ", padding)
				}
				line = selectedStyle.Render(paddedLine)
			} else {
				line = "  " + line
			}
			b.WriteString(line)
			b.WriteString("\x1b[K\n") // Clear to end of line before newline
			jobLinesWritten++
		}

		// Pad with clear-to-end-of-line sequences to prevent ghost text
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
	b.WriteString("\x1b[K") // Clear to end of line (no newline at end)
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}
func (m model) calculateColumnWidths(idWidth int) columnWidths {
	// Fixed widths: ID (idWidth), Status (8), P/F (3), Queued (12), Elapsed (8), Addressed (9)
	// Status width 8 accommodates "canceled" (longest status)
	// Plus spacing: 2 (prefix) + 9 spaces between columns (one more for branch)
	fixedWidth := 2 + idWidth + 8 + 3 + 12 + 8 + 9 + 9

	// Available width for flexible columns (ref, branch, repo, agent)
	// Don't artificially inflate - if terminal is too narrow, columns will be tiny
	availableWidth := max(4, m.width-fixedWidth) // At least 4 chars total for columns

	// Distribute available width: ref (20%), branch (32%), repo (33%), agent (15%)
	refWidth := max(1, availableWidth*20/100)
	branchWidth := max(1, availableWidth*32/100)
	repoWidth := max(1, availableWidth*33/100)
	agentWidth := max(1, availableWidth*15/100)

	// Scale down if total exceeds available (can happen due to rounding with small values)
	total := refWidth + branchWidth + repoWidth + agentWidth
	if total > availableWidth && availableWidth > 0 {
		refWidth = max(1, availableWidth*20/100)
		branchWidth = max(1, availableWidth*32/100)
		repoWidth = max(1, availableWidth*33/100)
		agentWidth = max(
			// Give remainder to agent
			availableWidth-refWidth-branchWidth-repoWidth, 1)
	}

	// Apply higher minimums only when there's plenty of space
	if availableWidth >= 45 {
		refWidth = max(8, refWidth)
		branchWidth = max(10, branchWidth)
		repoWidth = max(10, repoWidth)
		agentWidth = max(6, agentWidth)
	}

	return columnWidths{
		ref:    refWidth,
		branch: branchWidth,
		repo:   repoWidth,
		agent:  agentWidth,
	}
}
func (m model) renderJobLine(job storage.ReviewJob, selected bool, idWidth int, colWidths columnWidths) string {
	ref := shortJobRef(job)
	// Show review type tag for non-standard review types (e.g., [security])
	if !config.IsDefaultReviewType(job.ReviewType) {
		ref = ref + " [" + job.ReviewType + "]"
	}
	if len(ref) > colWidths.ref {
		ref = ref[:max(1, colWidths.ref-3)] + "..."
	}

	// Get branch name with fallback to git lookup
	branch := m.getBranchForJob(job)
	if len(branch) > colWidths.branch {
		branch = branch[:max(1, colWidths.branch-3)] + "..."
	}

	// Use cached display name, falling back to RepoName
	repo := m.getDisplayName(job.RepoPath, job.RepoName)
	// Append [remote] indicator for jobs from other machines
	if m.status.MachineID != "" && job.SourceMachineID != "" && job.SourceMachineID != m.status.MachineID {
		repo += " [R]"
	}
	if len(repo) > colWidths.repo {
		repo = repo[:max(1, colWidths.repo-3)] + "..."
	}

	agent := job.Agent
	// Normalize agent display names for compactness
	if agent == "claude-code" {
		agent = "claude"
	}
	if len(agent) > colWidths.agent {
		agent = agent[:max(1, colWidths.agent-3)] + "..."
	}

	// Format enqueue time as compact timestamp in local time
	enqueued := job.EnqueuedAt.Local().Format("Jan 02 15:04")

	// Format elapsed time
	elapsed := ""
	if job.StartedAt != nil {
		if job.FinishedAt != nil {
			elapsed = job.FinishedAt.Sub(*job.StartedAt).Round(time.Second).String()
		} else {
			elapsed = time.Since(*job.StartedAt).Round(time.Second).String()
		}
	}

	// Color the status only when not selected (selection style should be uniform)
	status := string(job.Status)
	var styledStatus string
	if selected {
		styledStatus = status
	} else {
		switch job.Status {
		case storage.JobStatusQueued:
			styledStatus = queuedStyle.Render(status)
		case storage.JobStatusRunning:
			styledStatus = runningStyle.Render(status)
		case storage.JobStatusDone:
			styledStatus = doneStyle.Render(status)
		case storage.JobStatusFailed:
			styledStatus = failedStyle.Render(status)
		case storage.JobStatusCanceled:
			styledStatus = canceledStyle.Render(status)
		default:
			styledStatus = status
		}
	}
	// Pad after coloring since lipgloss strips trailing spaces
	// Width 8 accommodates "canceled" (longest status)
	padding := 8 - len(status)
	if padding > 0 {
		styledStatus += strings.Repeat(" ", padding)
	}

	// Verdict: P (pass) or F (fail), styled with color
	verdict := "-"
	if job.Verdict != nil {
		v := *job.Verdict
		if selected {
			verdict = v
		} else if v == "P" {
			verdict = passStyle.Render(v)
		} else {
			verdict = failStyle.Render(v)
		}
	}
	// Pad to 3 chars
	if job.Verdict == nil || len(*job.Verdict) < 3 {
		verdict += strings.Repeat(" ", 3-1) // "-" or "P"/"F" is 1 char
	}

	// Addressed status: nil means no review yet, true/false for reviewed jobs
	addr := ""
	if job.Addressed != nil {
		if *job.Addressed {
			if selected {
				addr = "true"
			} else {
				addr = addressedStyle.Render("true")
			}
		} else {
			if selected {
				addr = "false"
			} else {
				addr = queuedStyle.Render("false")
			}
		}
	}

	return fmt.Sprintf("%-*d %-*s %-*s %-*s %-*s %s %s %-12s %-8s %s",
		idWidth, job.ID,
		colWidths.ref, ref,
		colWidths.branch, branch,
		colWidths.repo, repo,
		colWidths.agent, agent,
		styledStatus, verdict, enqueued, elapsed, addr)
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
