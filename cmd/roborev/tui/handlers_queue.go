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
	headerRows := 5 // title, status, update, header, separator
	if m.queueCompact() {
		headerRows = 1 // title only
	}
	row := y - headerRows
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

func (m model) handleDistractionFreeKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue {
		return m, nil
	}
	m.distractionFree = !m.distractionFree
	return m, nil
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

func (m model) handleColumnOptionsKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue && m.currentView != viewTasks {
		return m, nil
	}
	m.colOptionsReturnView = m.currentView

	var opts []columnOption
	if m.currentView == viewTasks {
		for _, col := range m.taskColumnOrder {
			opts = append(opts, columnOption{
				id:      col,
				name:    taskColumnDisplayName(col),
				enabled: true,
			})
		}
	} else {
		for _, col := range m.columnOrder {
			opts = append(opts, columnOption{
				id:      col,
				name:    columnDisplayName(col),
				enabled: !m.hiddenColumns[col],
			})
		}
	}
	// Add borders toggle
	opts = append(opts, columnOption{
		id:      colOptionBorders,
		name:    "Column borders",
		enabled: m.colBordersOn,
	})
	m.colOptionsList = opts
	m.colOptionsIdx = 0
	m.currentView = viewColumnOptions
	return m, nil
}

func (m model) handleColumnOptionsInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	isColumn := func(idx int) bool {
		return idx >= 0 && idx < len(m.colOptionsList) && m.colOptionsList[idx].id != colOptionBorders
	}

	switch msg.String() {
	case "esc":
		m.currentView = m.colOptionsReturnView
		if m.colOptionsDirty {
			m.colOptionsDirty = false
			return m, m.saveColumnOptions()
		}
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "down":
		if m.colOptionsIdx < len(m.colOptionsList)-1 {
			m.colOptionsIdx++
		}
		return m, nil
	case "up":
		if m.colOptionsIdx > 0 {
			m.colOptionsIdx--
		}
		return m, nil
	case "j":
		// Move current column down in order
		if isColumn(m.colOptionsIdx) && isColumn(m.colOptionsIdx+1) {
			m.colOptionsList[m.colOptionsIdx], m.colOptionsList[m.colOptionsIdx+1] =
				m.colOptionsList[m.colOptionsIdx+1], m.colOptionsList[m.colOptionsIdx]
			m.colOptionsIdx++
			m.syncColumnOrderFromOptions()
			m.colOptionsDirty = true
			m.queueColGen++
			m.taskColGen++
		}
		return m, nil
	case "k":
		// Move current column up in order
		if isColumn(m.colOptionsIdx) && isColumn(m.colOptionsIdx-1) {
			m.colOptionsList[m.colOptionsIdx], m.colOptionsList[m.colOptionsIdx-1] =
				m.colOptionsList[m.colOptionsIdx-1], m.colOptionsList[m.colOptionsIdx]
			m.colOptionsIdx--
			m.syncColumnOrderFromOptions()
			m.colOptionsDirty = true
			m.queueColGen++
			m.taskColGen++
		}
		return m, nil
	case " ", "enter":
		if m.colOptionsIdx >= 0 && m.colOptionsIdx < len(m.colOptionsList) {
			opt := &m.colOptionsList[m.colOptionsIdx]
			if opt.id == colOptionBorders {
				opt.enabled = !opt.enabled
				m.colBordersOn = opt.enabled
				m.colOptionsDirty = true
				m.queueColGen++
				m.taskColGen++
			} else if m.colOptionsReturnView == viewTasks {
				// Tasks view: no visibility toggle (all columns always shown)
				return m, nil
			} else {
				opt.enabled = !opt.enabled
				if opt.enabled {
					delete(m.hiddenColumns, opt.id)
				} else {
					if m.hiddenColumns == nil {
						m.hiddenColumns = map[int]bool{}
					}
					m.hiddenColumns[opt.id] = true
				}
				m.colOptionsDirty = true
				m.queueColGen++
			}
		}
		return m, nil
	}
	return m, nil
}

// syncColumnOrderFromOptions updates m.columnOrder or m.taskColumnOrder
// from the current colOptionsList (excluding the borders toggle).
func (m *model) syncColumnOrderFromOptions() {
	order := make([]int, 0, len(m.colOptionsList))
	for _, opt := range m.colOptionsList {
		if opt.id != colOptionBorders {
			order = append(order, opt.id)
		}
	}
	if m.colOptionsReturnView == viewTasks {
		m.taskColumnOrder = order
	} else {
		m.columnOrder = order
	}
}

func (m model) handleHideAddressedKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue {
		return m, nil
	}
	m.hideAddressed = !m.hideAddressed
	m.queueColGen++
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
