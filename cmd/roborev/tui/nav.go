package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

// updateSelectedJobID updates the tracked job ID after navigation
func (m *model) updateSelectedJobID() {
	if m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		m.selectedJobID = m.jobs[m.selectedIdx].ID
	}
}

// findNextViewableJob finds the next job that can be viewed (done or failed).
// Respects active filters. Returns the index or -1 if none found.
func (m *model) findNextViewableJob() int {
	for i := m.selectedIdx + 1; i < len(m.jobs); i++ {
		job := m.jobs[i]
		if (job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed) &&
			m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// findPrevViewableJob finds the previous job that can be viewed (done or failed).
// Respects active filters. Returns the index or -1 if none found.
func (m *model) findPrevViewableJob() int {
	for i := m.selectedIdx - 1; i >= 0; i-- {
		job := m.jobs[i]
		if (job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed) &&
			m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// findNextPromptableJob finds the next job that has a viewable prompt (done or running with prompt).
// Respects active filters. Returns the index or -1 if none found.
func (m *model) findNextPromptableJob() int {
	for i := m.selectedIdx + 1; i < len(m.jobs); i++ {
		job := m.jobs[i]
		if m.isJobVisible(job) &&
			(job.Status == storage.JobStatusDone || (job.Status == storage.JobStatusRunning && job.Prompt != "")) {
			return i
		}
	}
	return -1
}

// findPrevPromptableJob finds the previous job that has a viewable prompt (done or running with prompt).
// Respects active filters. Returns the index or -1 if none found.
func (m *model) findPrevPromptableJob() int {
	for i := m.selectedIdx - 1; i >= 0; i-- {
		job := m.jobs[i]
		if m.isJobVisible(job) &&
			(job.Status == storage.JobStatusDone || (job.Status == storage.JobStatusRunning && job.Prompt != "")) {
			return i
		}
	}
	return -1
}

// findNextLoggableJob finds the next job that has a log
// (running, done, or failed). Respects active filters.
func (m *model) findNextLoggableJob() int {
	for i := m.selectedIdx + 1; i < len(m.jobs); i++ {
		job := m.jobs[i]
		if job.Status != storage.JobStatusQueued &&
			m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// findPrevLoggableJob finds the previous job that has a log
// (running, done, or failed). Respects active filters.
func (m *model) findPrevLoggableJob() int {
	for i := m.selectedIdx - 1; i >= 0; i-- {
		job := m.jobs[i]
		if job.Status != storage.JobStatusQueued &&
			m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// findNextLoggableFixJob finds the next fix job that has a log.
func (m *model) findNextLoggableFixJob() int {
	for i := m.fixSelectedIdx + 1; i < len(m.fixJobs); i++ {
		if m.fixJobs[i].Status != storage.JobStatusQueued {
			return i
		}
	}
	return -1
}

// findPrevLoggableFixJob finds the previous fix job that has a
// log.
func (m *model) findPrevLoggableFixJob() int {
	for i := m.fixSelectedIdx - 1; i >= 0; i-- {
		if m.fixJobs[i].Status != storage.JobStatusQueued {
			return i
		}
	}
	return -1
}

// logViewLookupJob finds the job being viewed in the log view.
// Searches m.jobs first, then m.fixJobs for jobs opened from
// the tasks view.
func (m *model) logViewLookupJob() *storage.ReviewJob {
	for i := range m.jobs {
		if m.jobs[i].ID == m.logJobID {
			return &m.jobs[i]
		}
	}
	for i := range m.fixJobs {
		if m.fixJobs[i].ID == m.logJobID {
			return &m.fixJobs[i]
		}
	}
	return nil
}

// logVisibleLines returns the number of content lines visible in the
// log view, accounting for title, optional command line, separator,
// status, and help bar.
func (m *model) logVisibleLines() int {
	// title + separator + status + help(N)
	helpRows := m.logHelpRows()
	reserved := 3 + len(reflowHelpRows(helpRows, m.width))
	// Check if command line header is shown
	if job := m.logViewLookupJob(); job != nil {
		if commandLineForJob(job) != "" {
			reserved++
		}
	}
	return max(m.height-reserved, 1)
}

// logHelpRows returns the help row items for the log view.
func (m *model) logHelpRows() [][]helpItem {
	helpRow := []helpItem{
		{"↑/↓", "scroll"}, {"←/→", "prev/next"}, {"g", "toggle top/bottom"},
	}
	if m.logStreaming {
		helpRow = append(helpRow, helpItem{"x", "cancel"})
	}
	helpRow = append(helpRow, helpItem{"esc/q", "back"})
	return [][]helpItem{helpRow}
}

// normalizeSelectionIfHidden adjusts selectedIdx/selectedJobID if the current
// selection is hidden (e.g., marked addressed with hideAddressed filter active).
// Call this when returning to queue view from review view.
func (m *model) normalizeSelectionIfHidden() {
	if m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) && !m.isJobVisible(m.jobs[m.selectedIdx]) {
		nextIdx := m.findNextVisibleJob(m.selectedIdx)
		if nextIdx < 0 {
			nextIdx = m.findPrevVisibleJob(m.selectedIdx)
		}
		if nextIdx < 0 {
			nextIdx = m.findFirstVisibleJob()
		}
		if nextIdx >= 0 {
			m.selectedIdx = nextIdx
			m.updateSelectedJobID()
		}
	}
}

// findNextVisibleJob returns the first visible job index after currentIdx.
func (m model) findNextVisibleJob(currentIdx int) int {
	for i := currentIdx + 1; i < len(m.jobs); i++ {
		if m.isJobVisible(m.jobs[i]) {
			return i
		}
	}
	return -1
}

// findPrevVisibleJob returns the first visible job index before currentIdx.
func (m model) findPrevVisibleJob(currentIdx int) int {
	for i := currentIdx - 1; i >= 0; i-- {
		if m.isJobVisible(m.jobs[i]) {
			return i
		}
	}
	return -1
}

// countVisibleJobsAfter returns the number of visible jobs after currentIdx,
// short-circuiting once the count reaches queuePrefetchBuffer since callers
// only need to know whether the count is below that threshold.
func (m model) countVisibleJobsAfter(currentIdx int) int {
	count := 0
	for i := currentIdx + 1; i < len(m.jobs); i++ {
		if m.isJobVisible(m.jobs[i]) {
			count++
			if count >= queuePrefetchBuffer {
				return count
			}
		}
	}
	return count
}

// maybePrefetch triggers a page fetch if the cursor is near the end of loaded
// data. Returns a tea.Cmd if a fetch was started, nil otherwise.
func (m *model) maybePrefetch(idx int) tea.Cmd {
	if m.canPaginate() && m.countVisibleJobsAfter(idx) < queuePrefetchBuffer {
		m.loadingMore = true
		return m.fetchMoreJobs()
	}
	return nil
}

// findFirstVisibleJob returns the index of the first visible job.
func (m model) findFirstVisibleJob() int {
	for i := range m.jobs {
		if m.isJobVisible(m.jobs[i]) {
			return i
		}
	}
	return -1
}

// hasActiveFixJobs returns true if any fix jobs are queued or running.
func (m model) hasActiveFixJobs() bool {
	for _, j := range m.fixJobs {
		if j.Status == storage.JobStatusQueued || j.Status == storage.JobStatusRunning {
			return true
		}
	}
	return false
}
