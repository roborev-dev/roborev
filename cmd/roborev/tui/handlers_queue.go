package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

func (m *model) handleQueueMouseClick(_ int, y int) {
	visibleJobList := m.getVisibleJobs()
	if len(visibleJobList) == 0 {
		return
	}

	visibleRows := m.queueVisibleRows()
	start := 0
	end := len(visibleJobList)
	if len(visibleJobList) > visibleRows {
		visibleSelectedIdx := max(m.getVisibleSelectedIdx(), 0)
		start = max(visibleSelectedIdx-visibleRows/2, 0)
		end = start + visibleRows
		if end > len(visibleJobList) {
			end = len(visibleJobList)
			start = max(end-visibleRows, 0)
		}
	}
	row := y - 5 // rows start after title, status, update, header, separator
	if row < 0 || row >= visibleRows {
		return
	}
	visibleIdx := start + row
	if visibleIdx < start || visibleIdx >= end {
		return
	}

	targetJobID := visibleJobList[visibleIdx].ID
	for i := range m.jobs {
		if m.jobs[i].ID == targetJobID {
			m.selectedIdx = i
			m.selectedJobID = targetJobID
			return
		}
	}
}

func (m model) tasksVisibleWindow(totalJobs int) (int, int, int) {
	tasksHelpRows := [][]helpItem{
		{{"enter", "view"}, {"P", "parent"}, {"p", "patch"}, {"A", "apply"}, {"l", "log"}, {"x", "cancel"}, {"?", "help"}, {"T/esc", "back"}},
	}
	tasksHelpLines := len(reflowHelpRows(tasksHelpRows, m.width))
	visibleRows := max(m.height-(6+tasksHelpLines), 1)
	startIdx := 0
	if m.fixSelectedIdx >= visibleRows {
		startIdx = m.fixSelectedIdx - visibleRows + 1
	}
	endIdx := min(totalJobs, startIdx+visibleRows)
	return visibleRows, startIdx, endIdx
}

func (m *model) handleTasksMouseClick(y int) {
	if m.fixShowHelp || len(m.fixJobs) == 0 {
		return
	}
	visibleRows, start, end := m.tasksVisibleWindow(len(m.fixJobs))
	row := y - 3 // rows start after title, header, separator
	if row < 0 || row >= visibleRows {
		return
	}
	idx := start + row
	if idx < start || idx >= end {
		return
	}
	m.fixSelectedIdx = idx
}

func (m model) handleEnterKey() (tea.Model, tea.Cmd) {
	job, ok := m.selectedJob()
	if m.currentView != viewQueue || !ok {
		return m, nil
	}
	switch job.Status {
	case storage.JobStatusDone:
		m.reviewFromView = viewQueue
		return m, m.fetchReview(job.ID)
	case storage.JobStatusFailed:
		m.currentBranch = ""
		jobCopy := *job
		m.currentReview = &storage.Review{
			Agent:  job.Agent,
			Output: "Job failed:\n\n" + job.Error,
			Job:    &jobCopy,
		}
		m.reviewFromView = viewQueue
		m.currentView = viewReview
		m.reviewScroll = 0
		return m, nil
	}
	var status string
	switch job.Status {
	case storage.JobStatusQueued:
		status = "queued"
	case storage.JobStatusRunning:
		status = "in progress"
	case storage.JobStatusCanceled:
		status = "canceled"
	default:
		status = string(job.Status)
	}
	m.setFlash(fmt.Sprintf("Job #%d is %s — no review yet", job.ID, status), 2*time.Second, viewQueue)
	return m, nil
}

func (m model) handleFilterOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue {
		return m, nil
	}
	// Block filter modal when both repo and branch are locked via CLI flags
	if m.lockedRepoFilter && m.lockedBranchFilter {
		return m, nil
	}
	m.filterTree = nil
	m.filterFlatList = nil
	m.filterSelectedIdx = 0
	m.filterSearch = ""
	m.currentView = viewFilter
	if !m.branchBackfillDone {
		return m, tea.Batch(m.fetchRepos(), m.backfillBranches())
	}
	return m, m.fetchRepos()
}

func (m model) handleBranchFilterOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue {
		return m, nil
	}
	// Block branch filter when locked via CLI flag
	if m.lockedBranchFilter {
		return m, nil
	}
	m.filterBranchMode = true
	return m.handleFilterOpenKey()
}

func (m model) handleHideAddressedKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue {
		return m, nil
	}
	m.hideAddressed = !m.hideAddressed
	if len(m.jobs) > 0 {
		if m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) || !m.isJobVisible(m.jobs[m.selectedIdx]) {
			m.selectedIdx = m.findFirstVisibleJob()
			m.updateSelectedJobID()
		}
		if m.getVisibleSelectedIdx() < 0 && m.findFirstVisibleJob() >= 0 {
			m.selectedIdx = m.findFirstVisibleJob()
			m.updateSelectedJobID()
		}
	}
	m.fetchSeq++
	m.loadingJobs = true
	return m, m.fetchJobs()
}
