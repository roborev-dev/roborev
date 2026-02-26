package tui

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/streamfmt"
	"github.com/roborev-dev/roborev/internal/version"
)

// handleKeyMsg dispatches key events to view-specific handlers.
func (m model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Fix panel captures input when focused in review view
	if m.currentView == viewReview && m.reviewFixPanelOpen && m.reviewFixPanelFocused {
		return m.handleReviewFixPanelKey(msg)
	}

	// Modal views that capture most keys for typing
	switch m.currentView {
	case viewKindComment:
		return m.handleCommentKey(msg)
	case viewFilter:
		return m.handleFilterKey(msg)
	case viewLog:
		return m.handleLogKey(msg)
	case viewKindWorktreeConfirm:
		return m.handleWorktreeConfirmKey(msg)
	case viewTasks:
		return m.handleTasksKey(msg)
	case viewPatch:
		return m.handlePatchKey(msg)
	}

	// Global keys shared across queue/review/prompt/commitMsg/help views
	return m.handleGlobalKey(msg)
}

func (m model) handleMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.currentView == viewTasks {
			if m.fixSelectedIdx > 0 {
				m.fixSelectedIdx--
			}
			return m, nil
		}
		return m.handleUpKey()
	case tea.MouseButtonWheelDown:
		if m.currentView == viewTasks {
			if m.fixSelectedIdx < len(m.fixJobs)-1 {
				m.fixSelectedIdx++
			}
			return m, nil
		}
		return m.handleDownKey()
	case tea.MouseButtonLeft:
		switch m.currentView {
		case viewQueue:
			m.handleQueueMouseClick(msg.X, msg.Y)
		case viewTasks:
			m.handleTasksMouseClick(msg.Y)
		}
		return m, nil
	default:
		return m, nil
	}
}

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

// handleCommentKey handles key input in the comment modal.
func (m model) handleCommentKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.currentView = m.commentFromView
		m.commentText = ""
		m.commentJobID = 0
		return m, nil
	case "enter":
		if strings.TrimSpace(m.commentText) != "" {
			text := m.commentText
			jobID := m.commentJobID
			m.currentView = m.commentFromView
			return m, m.submitComment(jobID, text)
		}
		return m, nil
	case "backspace":
		if len(m.commentText) > 0 {
			runes := []rune(m.commentText)
			m.commentText = string(runes[:len(runes)-1])
		}
		return m, nil
	default:
		if msg.String() == "shift+enter" || msg.String() == "ctrl+j" {
			m.commentText += "\n"
		} else if len(msg.Runes) > 0 {
			for _, r := range msg.Runes {
				if unicode.IsPrint(r) || r == '\n' || r == '\t' {
					m.commentText += string(r)
				}
			}
		}
		return m, nil
	}
}

// handleFilterKey handles key input in the unified tree filter modal.
func (m model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = viewQueue
		m.filterSearch = ""
		m.filterBranchMode = false
		return m, nil
	case "up", "k":
		m.filterNavigateUp()
		return m, nil
	case "down", "j":
		m.filterNavigateDown()
		return m, nil
	case "right":
		// Expand a collapsed repo node, or retry a failed load
		entry := m.getSelectedFilterEntry()
		if entry != nil && entry.repoIdx >= 0 && entry.branchIdx == -1 {
			node := &m.filterTree[entry.repoIdx]
			needsFetch := node.children == nil && !node.loading
			if !node.expanded || needsFetch {
				node.userCollapsed = false
				if len(node.children) > 0 {
					node.expanded = true
					m.rebuildFilterFlatList()
				} else if !node.loading {
					node.expanded = true
					node.loading = true
					node.fetchFailed = false
					return m, m.fetchBranchesForRepo(
						node.rootPaths, entry.repoIdx, true, m.filterSearchSeq,
					)
				} else {
					// Load in-flight (search-triggered); mark
					// expanded so children show on arrival.
					node.expanded = true
				}
			}
		}
		return m, nil
	case "left":
		// Collapse an expanded repo, or if on a branch, collapse parent
		entry := m.getSelectedFilterEntry()
		if entry != nil {
			collapse := func(idx int) {
				m.filterTree[idx].expanded = false
				if m.filterSearch != "" {
					m.filterTree[idx].userCollapsed = true
				}
				m.rebuildFilterFlatList()
			}
			if entry.branchIdx >= 0 {
				// On a branch: collapse parent and move selection to parent
				collapse(entry.repoIdx)
				for i, e := range m.filterFlatList {
					if e.repoIdx == entry.repoIdx && e.branchIdx == -1 {
						m.filterSelectedIdx = i
						break
					}
				}
			} else if entry.repoIdx >= 0 {
				node := &m.filterTree[entry.repoIdx]
				if node.expanded ||
					(m.filterSearch != "" && !node.userCollapsed) {
					collapse(entry.repoIdx)
				}
			}
		}
		return m, nil
	case "enter":
		entry := m.getSelectedFilterEntry()
		if entry == nil {
			return m, nil
		}
		if entry.repoIdx == -1 {
			// "All" -- clear unlocked filters only
			if !m.lockedRepoFilter {
				m.activeRepoFilter = nil
				m.removeFilterFromStack(filterTypeRepo)
			}
			if !m.lockedBranchFilter {
				m.activeBranchFilter = ""
				m.removeFilterFromStack(filterTypeBranch)
			}
		} else if entry.branchIdx == -1 {
			// Repo node -- filter by repo only
			node := m.filterTree[entry.repoIdx]
			if !m.lockedRepoFilter {
				m.activeRepoFilter = node.rootPaths
				m.pushFilter(filterTypeRepo)
			}
			if !m.lockedBranchFilter {
				m.activeBranchFilter = ""
				m.removeFilterFromStack(filterTypeBranch)
			}
		} else {
			// Branch node -- filter by repo + branch
			node := m.filterTree[entry.repoIdx]
			branch := node.children[entry.branchIdx]
			if !m.lockedRepoFilter {
				m.activeRepoFilter = node.rootPaths
				m.pushFilter(filterTypeRepo)
			}
			if !m.lockedBranchFilter {
				m.activeBranchFilter = branch.name
				m.pushFilter(filterTypeBranch)
			}
		}
		m.currentView = viewQueue
		m.filterSearch = ""
		m.filterBranchMode = false
		m.hasMore = false
		m.selectedIdx = -1
		m.selectedJobID = 0
		m.fetchSeq++
		m.loadingJobs = true
		return m, m.fetchJobs()
	case "backspace":
		if len(m.filterSearch) > 0 {
			runes := []rune(m.filterSearch)
			m.filterSearch = string(runes[:len(runes)-1])
			m.filterSearchSeq++
			m.clearFetchFailed()
			m.filterSelectedIdx = 0
			m.rebuildFilterFlatList()
		}
		return m, m.fetchUnloadedBranches()
	default:
		if len(msg.Runes) > 0 {
			for _, r := range msg.Runes {
				if unicode.IsPrint(r) && !unicode.IsControl(r) {
					m.filterSearch += string(r)
					m.filterSelectedIdx = 0
				}
			}
			m.filterSearchSeq++
			m.clearFetchFailed()
			m.rebuildFilterFlatList()
		}
		return m, m.fetchUnloadedBranches()
	}
}

// clearFetchFailed resets fetchFailed on all tree nodes so that
// changed search text retries previously failed repos.
func (m *model) clearFetchFailed() {
	for i := range m.filterTree {
		m.filterTree[i].fetchFailed = false
	}
}

// maxSearchBranchFetches is the maximum number of concurrent
// search-triggered branch fetches. Counts both already in-flight
// and newly started requests.
const maxSearchBranchFetches = 5

// fetchUnloadedBranches triggers branch fetches for repos that
// haven't loaded branches yet while search text is active. Without
// this, searching for a branch name only matches already-expanded
// repos. At most maxSearchBranchFetches total requests are allowed
// in-flight at once; completions trigger top-up fetches via the
// repoBranchesMsg handler.
func (m *model) fetchUnloadedBranches() tea.Cmd {
	if m.filterSearch == "" {
		return nil
	}
	inFlight := 0
	for i := range m.filterTree {
		if m.filterTree[i].loading {
			inFlight++
		}
	}
	slots := maxSearchBranchFetches - inFlight
	if slots <= 0 {
		return nil
	}
	var cmds []tea.Cmd
	for i := range m.filterTree {
		node := &m.filterTree[i]
		if node.children == nil && !node.loading && !node.fetchFailed {
			node.loading = true
			cmds = append(cmds, m.fetchBranchesForRepo(
				node.rootPaths, i, false, m.filterSearchSeq,
			))
			if len(cmds) >= slots {
				break
			}
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// handleLogKey handles key input in the log view.
func (m model) handleLogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = m.logFromView
		m.logStreaming = false
		return m, nil
	case "x":
		if m.logJobID > 0 && m.logStreaming {
			if job := m.logViewLookupJob(); job != nil &&
				job.Status == storage.JobStatusRunning {
				oldStatus := job.Status
				oldFinishedAt := job.FinishedAt
				job.Status = storage.JobStatusCanceled
				now := time.Now()
				job.FinishedAt = &now
				m.logStreaming = false
				return m, m.cancelJob(
					job.ID, oldStatus, oldFinishedAt,
				)
			}
		}
		return m, nil
	case "up", "k":
		m.logFollow = false
		if m.logScroll > 0 {
			m.logScroll--
		}
		return m, nil
	case "down", "j":
		m.logScroll++
		return m, nil
	case "pgup":
		m.logFollow = false
		m.logScroll -= m.logVisibleLines()
		if m.logScroll < 0 {
			m.logScroll = 0
		}
		return m, tea.ClearScreen
	case "pgdown":
		m.logScroll += m.logVisibleLines()
		return m, tea.ClearScreen
	case "home":
		m.logFollow = false
		m.logScroll = 0
		return m, nil
	case "end":
		m.logFollow = true
		maxScroll := max(len(m.logLines)-m.logVisibleLines(), 0)
		m.logScroll = maxScroll
		return m, nil
	case "g", "G":
		maxScroll := max(len(m.logLines)-m.logVisibleLines(), 0)
		if m.logScroll == 0 {
			m.logFollow = true
			m.logScroll = maxScroll
		} else {
			m.logFollow = false
			m.logScroll = 0
		}
		return m, tea.ClearScreen
	case "left":
		return m.handlePrevKey()
	case "right":
		return m.handleNextKey()
	case "?":
		m.helpFromView = m.currentView
		m.currentView = viewHelp
		m.helpScroll = 0
		return m, nil
	}
	return m, nil
}

// handleGlobalKey handles keys shared across queue, review, prompt, commit msg, and help views.
func (m model) handleGlobalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m.handleQuitKey()
	case "home", "g":
		return m.handleHomeKey()
	case "up":
		return m.handleUpKey()
	case "k", "left":
		return m.handlePrevKey()
	case "down":
		return m.handleDownKey()
	case "j", "right":
		return m.handleNextKey()
	case "pgup":
		return m.handlePageUpKey()
	case "pgdown":
		return m.handlePageDownKey()
	case "enter":
		return m.handleEnterKey()
	case "p":
		return m.handlePromptKey()
	case "a":
		return m.handleAddressedKey()
	case "x":
		return m.handleCancelKey()
	case "r":
		return m.handleRerunKey()
	case "l", "t":
		return m.handleLogKey2()
	case "f":
		return m.handleFilterOpenKey()
	case "b":
		return m.handleBranchFilterOpenKey()
	case "h":
		return m.handleHideAddressedKey()
	case "c":
		return m.handleCommentOpenKey()
	case "y":
		return m.handleCopyKey()
	case "m":
		return m.handleCommitMsgKey()
	case "?":
		return m.handleHelpKey()
	case "esc":
		return m.handleEscKey()
	case "F":
		return m.handleFixKey()
	case "T":
		return m.handleToggleTasksKey()
	case "tab":
		return m.handleTabKey()
	}
	return m, nil
}

func (m model) handleQuitKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewReview {
		returnTo := m.reviewFromView
		if returnTo == 0 {
			returnTo = viewQueue
		}
		m.closeFixPanel()
		m.currentView = returnTo
		m.currentReview = nil
		m.reviewScroll = 0
		m.paginateNav = 0
		if returnTo == viewQueue {
			m.normalizeSelectionIfHidden()
		}
		return m, nil
	}
	if m.currentView == viewKindPrompt {
		m.paginateNav = 0
		if m.promptFromQueue {
			m.currentView = viewQueue
			m.currentReview = nil
			m.promptScroll = 0
		} else {
			m.currentView = viewReview
			m.promptScroll = 0
		}
		return m, nil
	}
	if m.currentView == viewCommitMsg {
		m.currentView = m.commitMsgFromView
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
		return m, nil
	}
	if m.currentView == viewHelp {
		m.currentView = m.helpFromView
		return m, nil
	}
	return m, tea.Quit
}

func (m model) handleHomeKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case viewQueue:
		firstVisible := m.findFirstVisibleJob()
		if firstVisible >= 0 {
			m.selectedIdx = firstVisible
			m.updateSelectedJobID()
		}
	case viewReview:
		m.reviewScroll = 0
	case viewKindPrompt:
		m.promptScroll = 0
	case viewCommitMsg:
		m.commitMsgScroll = 0
	case viewHelp:
		m.helpScroll = 0
	}
	return m, nil
}

func (m model) handleUpKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case viewQueue:
		prevIdx := m.findPrevVisibleJob(m.selectedIdx)
		if prevIdx >= 0 {
			m.selectedIdx = prevIdx
			m.updateSelectedJobID()
		} else {
			m.flashMessage = "No newer review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = viewQueue
		}
	case viewReview:
		if m.reviewScroll > 0 {
			m.reviewScroll--
		}
	case viewKindPrompt:
		if m.promptScroll > 0 {
			m.promptScroll--
		}
	case viewCommitMsg:
		if m.commitMsgScroll > 0 {
			m.commitMsgScroll--
		}
	case viewHelp:
		if m.helpScroll > 0 {
			m.helpScroll--
		}
	}
	return m, nil
}

func (m model) handlePrevKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case viewQueue:
		prevIdx := m.findPrevVisibleJob(m.selectedIdx)
		if prevIdx >= 0 {
			m.selectedIdx = prevIdx
			m.updateSelectedJobID()
		}
	case viewReview:
		prevIdx := m.findPrevViewableJob()
		if prevIdx >= 0 {
			m.closeFixPanel()
			m.selectedIdx = prevIdx
			m.updateSelectedJobID()
			m.reviewScroll = 0
			job := m.jobs[prevIdx]
			switch job.Status {
			case storage.JobStatusDone:
				return m, m.fetchReview(job.ID)
			case storage.JobStatusFailed:
				m.currentBranch = ""
				m.currentReview = &storage.Review{
					Agent:  job.Agent,
					Output: "Job failed:\n\n" + job.Error,
					Job:    &job,
				}
			}
		} else {
			m.flashMessage = "No newer review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = viewReview
		}
	case viewKindPrompt:
		prevIdx := m.findPrevPromptableJob()
		if prevIdx >= 0 {
			m.selectedIdx = prevIdx
			m.updateSelectedJobID()
			m.promptScroll = 0
			job := m.jobs[prevIdx]
			if job.Status == storage.JobStatusDone {
				return m, m.fetchReviewForPrompt(job.ID)
			} else if job.Status == storage.JobStatusRunning && job.Prompt != "" {
				m.currentReview = &storage.Review{
					Agent:  job.Agent,
					Prompt: job.Prompt,
					Job:    &job,
				}
			}
		} else {
			m.flashMessage = "No newer review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = viewKindPrompt
		}
	case viewLog:
		if m.logFromView == viewTasks {
			idx := m.findPrevLoggableFixJob()
			if idx >= 0 {
				m.fixSelectedIdx = idx
				job := m.fixJobs[idx]
				m.logStreaming = false
				return m.openLogView(
					job.ID, job.Status, viewTasks,
				)
			}
		} else {
			prevIdx := m.findPrevLoggableJob()
			if prevIdx >= 0 {
				m.selectedIdx = prevIdx
				m.updateSelectedJobID()
				job := m.jobs[prevIdx]
				m.logStreaming = false
				return m.openLogView(
					job.ID, job.Status, m.logFromView,
				)
			}
		}
		m.flashMessage = "No newer log"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = viewLog
	}
	return m, nil
}

func (m model) handleDownKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case viewQueue:
		nextIdx := m.findNextVisibleJob(m.selectedIdx)
		if nextIdx >= 0 {
			m.selectedIdx = nextIdx
			m.updateSelectedJobID()
		} else if m.canPaginate() {
			m.loadingMore = true
			return m, m.fetchMoreJobs()
		} else if !m.hasMore || len(m.activeRepoFilter) > 1 {
			m.flashMessage = "No older review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = viewQueue
		}
	case viewReview:
		m.reviewScroll++
		if m.mdCache != nil && m.reviewScroll > m.mdCache.lastReviewMaxScroll {
			m.reviewScroll = m.mdCache.lastReviewMaxScroll
		}
	case viewKindPrompt:
		m.promptScroll++
		if m.mdCache != nil && m.promptScroll > m.mdCache.lastPromptMaxScroll {
			m.promptScroll = m.mdCache.lastPromptMaxScroll
		}
	case viewCommitMsg:
		m.commitMsgScroll++
	case viewHelp:
		m.helpScroll = min(m.helpScroll+1, m.helpMaxScroll())
	}
	return m, nil
}

func (m model) handleNextKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case viewQueue:
		nextIdx := m.findNextVisibleJob(m.selectedIdx)
		if nextIdx >= 0 {
			m.selectedIdx = nextIdx
			m.updateSelectedJobID()
		} else if m.canPaginate() {
			m.loadingMore = true
			return m, m.fetchMoreJobs()
		}
	case viewReview:
		nextIdx := m.findNextViewableJob()
		if nextIdx >= 0 {
			m.closeFixPanel()
			m.selectedIdx = nextIdx
			m.updateSelectedJobID()
			m.reviewScroll = 0
			job := m.jobs[nextIdx]
			switch job.Status {
			case storage.JobStatusDone:
				return m, m.fetchReview(job.ID)
			case storage.JobStatusFailed:
				m.currentBranch = ""
				m.currentReview = &storage.Review{
					Agent:  job.Agent,
					Output: "Job failed:\n\n" + job.Error,
					Job:    &job,
				}
			}
		} else if m.canPaginate() {
			m.loadingMore = true
			m.paginateNav = viewReview
			return m, m.fetchMoreJobs()
		} else {
			m.flashMessage = "No older review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = viewReview
		}
	case viewKindPrompt:
		nextIdx := m.findNextPromptableJob()
		if nextIdx >= 0 {
			m.selectedIdx = nextIdx
			m.updateSelectedJobID()
			m.promptScroll = 0
			job := m.jobs[nextIdx]
			if job.Status == storage.JobStatusDone {
				return m, m.fetchReviewForPrompt(job.ID)
			} else if job.Status == storage.JobStatusRunning && job.Prompt != "" {
				m.currentReview = &storage.Review{
					Agent:  job.Agent,
					Prompt: job.Prompt,
					Job:    &job,
				}
			}
		} else if m.canPaginate() {
			m.loadingMore = true
			m.paginateNav = viewKindPrompt
			return m, m.fetchMoreJobs()
		} else {
			m.flashMessage = "No older review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = viewKindPrompt
		}
	case viewLog:
		if m.logFromView == viewTasks {
			idx := m.findNextLoggableFixJob()
			if idx >= 0 {
				m.fixSelectedIdx = idx
				job := m.fixJobs[idx]
				m.logStreaming = false
				return m.openLogView(
					job.ID, job.Status, viewTasks,
				)
			}
		} else {
			nextIdx := m.findNextLoggableJob()
			if nextIdx >= 0 {
				m.selectedIdx = nextIdx
				m.updateSelectedJobID()
				job := m.jobs[nextIdx]
				m.logStreaming = false
				return m.openLogView(
					job.ID, job.Status, m.logFromView,
				)
			} else if m.canPaginate() {
				m.loadingMore = true
				m.paginateNav = viewLog
				return m, m.fetchMoreJobs()
			}
		}
		m.flashMessage = "No older log"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = viewLog
	}
	return m, nil
}

func (m model) handlePageUpKey() (tea.Model, tea.Cmd) {
	pageSize := max(1, m.height-10)
	switch m.currentView {
	case viewQueue:
		for range pageSize {
			prevIdx := m.findPrevVisibleJob(m.selectedIdx)
			if prevIdx < 0 {
				break
			}
			m.selectedIdx = prevIdx
		}
		m.updateSelectedJobID()
	case viewReview:
		if m.mdCache != nil && m.reviewScroll > m.mdCache.lastReviewMaxScroll {
			m.reviewScroll = m.mdCache.lastReviewMaxScroll
		}
		m.reviewScroll = max(0, m.reviewScroll-pageSize)
		return m, tea.ClearScreen
	case viewKindPrompt:
		if m.mdCache != nil && m.promptScroll > m.mdCache.lastPromptMaxScroll {
			m.promptScroll = m.mdCache.lastPromptMaxScroll
		}
		m.promptScroll = max(0, m.promptScroll-pageSize)
		return m, tea.ClearScreen
	case viewHelp:
		m.helpScroll = max(0, m.helpScroll-pageSize)
	}
	return m, nil
}

func (m model) handlePageDownKey() (tea.Model, tea.Cmd) {
	pageSize := max(1, m.height-10)
	switch m.currentView {
	case viewQueue:
		reachedEnd := false
		for range pageSize {
			nextIdx := m.findNextVisibleJob(m.selectedIdx)
			if nextIdx < 0 {
				reachedEnd = true
				break
			}
			m.selectedIdx = nextIdx
		}
		m.updateSelectedJobID()
		if reachedEnd && m.canPaginate() {
			m.loadingMore = true
			return m, m.fetchMoreJobs()
		}
	case viewReview:
		m.reviewScroll += pageSize
		if m.mdCache != nil && m.reviewScroll > m.mdCache.lastReviewMaxScroll {
			m.reviewScroll = m.mdCache.lastReviewMaxScroll
		}
		return m, tea.ClearScreen
	case viewKindPrompt:
		m.promptScroll += pageSize
		if m.mdCache != nil && m.promptScroll > m.mdCache.lastPromptMaxScroll {
			m.promptScroll = m.mdCache.lastPromptMaxScroll
		}
		return m, tea.ClearScreen
	case viewHelp:
		m.helpScroll = min(m.helpScroll+pageSize, m.helpMaxScroll())
	}
	return m, nil
}

func (m model) handleEnterKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
		return m, nil
	}
	job := m.jobs[m.selectedIdx]
	switch job.Status {
	case storage.JobStatusDone:
		m.reviewFromView = viewQueue
		return m, m.fetchReview(job.ID)
	case storage.JobStatusFailed:
		m.currentBranch = ""
		m.currentReview = &storage.Review{
			Agent:  job.Agent,
			Output: "Job failed:\n\n" + job.Error,
			Job:    &job,
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
	m.flashMessage = fmt.Sprintf("Job #%d is %s — no review yet", job.ID, status)
	m.flashExpiresAt = time.Now().Add(2 * time.Second)
	m.flashView = viewQueue
	return m, nil
}

func (m model) handlePromptKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		job := m.jobs[m.selectedIdx]
		if job.Status == storage.JobStatusDone {
			m.promptFromQueue = true
			return m, m.fetchReviewForPrompt(job.ID)
		} else if job.Status == storage.JobStatusRunning && job.Prompt != "" {
			m.currentReview = &storage.Review{
				Agent:  job.Agent,
				Prompt: job.Prompt,
				Job:    &job,
			}
			m.currentView = viewKindPrompt
			m.promptScroll = 0
			m.promptFromQueue = true
			return m, nil
		}
	} else if m.currentView == viewReview && m.currentReview != nil && m.currentReview.Prompt != "" {
		m.closeFixPanel()
		m.currentView = viewKindPrompt
		m.promptScroll = 0
		m.promptFromQueue = false
	} else if m.currentView == viewKindPrompt {
		if m.promptFromQueue {
			m.currentView = viewQueue
			m.currentReview = nil
			m.promptScroll = 0
		} else {
			m.currentView = viewReview
			m.promptScroll = 0
		}
	}
	return m, nil
}

func (m model) handleAddressedKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewReview && m.currentReview != nil && m.currentReview.ID > 0 {
		oldState := m.currentReview.Addressed
		newState := !oldState
		m.addressedSeq++
		seq := m.addressedSeq
		m.currentReview.Addressed = newState
		var jobID int64
		if m.currentReview.Job != nil {
			jobID = m.currentReview.Job.ID
			m.setJobAddressed(jobID, newState)
			m.pendingAddressed[jobID] = pendingState{newState: newState, seq: seq}
			m.applyStatsDelta(newState)
		} else {
			m.pendingReviewAddressed[m.currentReview.ID] = pendingState{newState: newState, seq: seq}
		}
		return m, m.addressReview(m.currentReview.ID, jobID, newState, oldState, seq)
	} else if m.currentView == viewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		job := &m.jobs[m.selectedIdx]
		if job.Status == storage.JobStatusDone && job.Addressed != nil {
			oldState := *job.Addressed
			newState := !oldState
			m.addressedSeq++
			seq := m.addressedSeq
			*job.Addressed = newState
			m.pendingAddressed[job.ID] = pendingState{newState: newState, seq: seq}
			m.applyStatsDelta(newState)
			if m.hideAddressed && newState {
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
			return m, m.addressReviewInBackground(job.ID, newState, oldState, seq)
		}
	}
	return m, nil
}

func (m model) handleCancelKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
		return m, nil
	}
	job := &m.jobs[m.selectedIdx]
	if job.Status == storage.JobStatusRunning || job.Status == storage.JobStatusQueued {
		oldStatus := job.Status
		oldFinishedAt := job.FinishedAt
		job.Status = storage.JobStatusCanceled
		now := time.Now()
		job.FinishedAt = &now
		// Canceled jobs are hidden when hideAddressed is active
		if m.hideAddressed {
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
		return m, m.cancelJob(job.ID, oldStatus, oldFinishedAt)
	}
	return m, nil
}

func (m model) handleRerunKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
		return m, nil
	}
	job := &m.jobs[m.selectedIdx]
	if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed || job.Status == storage.JobStatusCanceled {
		oldStatus := job.Status
		oldStartedAt := job.StartedAt
		oldFinishedAt := job.FinishedAt
		oldError := job.Error
		job.Status = storage.JobStatusQueued
		job.StartedAt = nil
		job.FinishedAt = nil
		job.Error = ""
		return m, m.rerunJob(job.ID, oldStatus, oldStartedAt, oldFinishedAt, oldError)
	}
	return m, nil
}

func (m model) handleLogKey2() (tea.Model, tea.Cmd) {
	// From prompt view: view log for the job being viewed
	if m.currentView == viewKindPrompt && m.currentReview != nil && m.currentReview.Job != nil {
		job := m.currentReview.Job
		return m.openLogView(job.ID, job.Status, m.reviewFromView)
	}

	if m.currentView != viewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
		return m, nil
	}
	job := m.jobs[m.selectedIdx]
	switch job.Status {
	case storage.JobStatusQueued:
		m.flashMessage = "Job is queued - not yet running"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = viewQueue
		return m, nil
	default:
		return m.openLogView(job.ID, job.Status, viewQueue)
	}
}

// openLogView opens the log view for a job of any status.
// Running jobs stream with follow; completed jobs show a static view.
func (m model) openLogView(
	jobID int64, status storage.JobStatus, fromView viewKind,
) (tea.Model, tea.Cmd) {
	m.logJobID = jobID
	m.logLines = nil
	m.logScroll = 0
	m.logFromView = fromView
	m.currentView = viewLog
	m.logOffset = 0
	m.logFmtr = streamfmt.NewWithWidth(
		io.Discard, m.width, m.glamourStyle,
	)
	m.logFetchSeq++
	m.logLoading = true

	if status == storage.JobStatusRunning {
		m.logStreaming = true
		m.logFollow = true
	} else {
		m.logStreaming = false
		m.logFollow = false
	}

	return m, tea.Batch(tea.ClearScreen, m.fetchJobLog(jobID))
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

func (m model) handleCommentOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		job := m.jobs[m.selectedIdx]
		if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed {
			if m.commentJobID != job.ID {
				m.commentText = ""
			}
			m.commentJobID = job.ID
			m.commentCommit = git.ShortSHA(job.GitRef)
			m.commentFromView = viewQueue
			m.currentView = viewKindComment
		}
		return m, nil
	} else if m.currentView == viewReview && m.currentReview != nil {
		if m.commentJobID != m.currentReview.JobID {
			m.commentText = ""
		}
		m.commentJobID = m.currentReview.JobID
		m.commentCommit = ""
		if m.currentReview.Job != nil {
			m.commentCommit = git.ShortSHA(
				m.currentReview.Job.GitRef)
		}
		m.commentFromView = viewReview
		m.currentView = viewKindComment
		return m, nil
	}
	return m, nil
}

func (m model) handleCopyKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewReview && m.currentReview != nil && m.currentReview.Output != "" {
		return m, m.copyToClipboard(m.currentReview)
	} else if m.currentView == viewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		job := m.jobs[m.selectedIdx]
		if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed {
			return m, m.fetchReviewAndCopy(job.ID, &job)
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
		m.flashMessage = fmt.Sprintf("Job #%d is %s — no review to copy", job.ID, status)
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = viewQueue
		return m, nil
	}
	return m, nil
}

func (m model) handleCommitMsgKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		job := m.jobs[m.selectedIdx]
		m.commitMsgFromView = m.currentView
		m.commitMsgJobID = job.ID
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
		return m, m.fetchCommitMsg(&job)
	} else if m.currentView == viewReview && m.currentReview != nil && m.currentReview.Job != nil {
		job := m.currentReview.Job
		m.commitMsgFromView = m.currentView
		m.commitMsgJobID = job.ID
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
		return m, m.fetchCommitMsg(job)
	}
	return m, nil
}

func (m model) handleHelpKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewHelp {
		m.currentView = m.helpFromView
		return m, nil
	}
	if m.currentView == viewQueue || m.currentView == viewReview || m.currentView == viewKindPrompt || m.currentView == viewLog {
		m.helpFromView = m.currentView
		m.currentView = viewHelp
		m.helpScroll = 0
		return m, nil
	}
	return m, nil
}

func (m model) handleEscKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewQueue && len(m.filterStack) > 0 {
		popped := m.popFilter()
		if popped == filterTypeRepo || popped == filterTypeBranch {
			m.hasMore = false
			m.selectedIdx = -1
			m.selectedJobID = 0
			m.fetchSeq++
			m.loadingJobs = true
			return m, m.fetchJobs()
		}
		return m, nil
	} else if m.currentView == viewQueue && m.hideAddressed {
		m.hideAddressed = false
		m.hasMore = false
		m.selectedIdx = -1
		m.selectedJobID = 0
		m.fetchSeq++
		m.loadingJobs = true
		return m, m.fetchJobs()
	} else if m.currentView == viewReview {
		// If fix panel is open (unfocused), esc closes it rather than leaving the review
		if m.reviewFixPanelOpen {
			m.closeFixPanel()
			return m, nil
		}
		m.closeFixPanel()
		returnTo := m.reviewFromView
		if returnTo == 0 {
			returnTo = viewQueue
		}
		m.currentView = returnTo
		m.currentReview = nil
		m.reviewScroll = 0
		m.paginateNav = 0
		if returnTo == viewQueue {
			m.normalizeSelectionIfHidden()
			if m.hideAddressed && !m.loadingJobs {
				m.loadingJobs = true
				return m, m.fetchJobs()
			}
		}
	} else if m.currentView == viewKindPrompt {
		m.paginateNav = 0
		if m.promptFromQueue {
			m.currentView = viewQueue
			m.currentReview = nil
			m.promptScroll = 0
		} else {
			m.currentView = viewReview
			m.promptScroll = 0
		}
	} else if m.currentView == viewCommitMsg {
		m.currentView = m.commitMsgFromView
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
	} else if m.currentView == viewHelp {
		m.currentView = m.helpFromView
	}
	return m, nil
}

// handleJobsMsg processes job list updates from the server.
func (m model) handleJobsMsg(msg jobsMsg) (tea.Model, tea.Cmd) {
	// Discard stale responses from before a filter change.
	if msg.seq < m.fetchSeq {
		m.paginateNav = 0
		m.loadingMore = false
		return m, nil
	}

	if msg.append || m.paginateNav == 0 {
		m.loadingMore = false
	}
	if !msg.append {
		m.loadingJobs = false
	}
	m.consecutiveErrors = 0

	m.hasMore = msg.hasMore

	m.updateDisplayNameCache(msg.jobs)

	if msg.append {
		m.jobs = append(m.jobs, msg.jobs...)
	} else {
		m.jobs = msg.jobs
	}

	// Clear pending addressed states that server has confirmed
	for jobID, pending := range m.pendingAddressed {
		found := false
		for i := range m.jobs {
			if m.jobs[i].ID == jobID {
				found = true
				serverState := m.jobs[i].Addressed != nil && *m.jobs[i].Addressed
				if serverState == pending.newState {
					delete(m.pendingAddressed, jobID)
				}
				break
			}
		}
		// When hideAddressed is active, addressed jobs are filtered
		// out of the response. If a pending "mark addressed" job is
		// absent from the response, that confirms the server absorbed
		// the change — clear it to prevent delta double-counting.
		if !found && m.hideAddressed && pending.newState {
			delete(m.pendingAddressed, jobID)
		}
	}

	if !msg.append {
		m.jobStats = msg.stats
		// Re-apply only unconfirmed pending deltas so that
		// rollback math stays correct without double-counting
		// entries the server has already absorbed.
		for _, pending := range m.pendingAddressed {
			m.applyStatsDelta(pending.newState)
		}
	}

	// Apply any remaining pending addressed changes to prevent flash
	for i := range m.jobs {
		if pending, ok := m.pendingAddressed[m.jobs[i].ID]; ok {
			newState := pending.newState
			m.jobs[i].Addressed = &newState
		}
	}

	// Selection management
	if len(m.jobs) == 0 {
		m.selectedIdx = -1
		if m.currentView != viewReview || m.currentReview == nil || m.currentReview.Job == nil {
			m.selectedJobID = 0
		}
	} else if m.selectedJobID > 0 {
		found := false
		for i, job := range m.jobs {
			if job.ID == m.selectedJobID {
				m.selectedIdx = i
				found = true
				break
			}
		}

		if !found {
			m.selectedIdx = max(0, min(len(m.jobs)-1, m.selectedIdx))
			if len(m.activeRepoFilter) > 0 || m.hideAddressed {
				firstVisible := m.findFirstVisibleJob()
				if firstVisible >= 0 {
					m.selectedIdx = firstVisible
					m.selectedJobID = m.jobs[firstVisible].ID
				} else {
					m.selectedIdx = -1
					m.selectedJobID = 0
				}
			} else {
				m.selectedJobID = m.jobs[m.selectedIdx].ID
			}
		} else if !m.isJobVisible(m.jobs[m.selectedIdx]) {
			firstVisible := m.findFirstVisibleJob()
			if firstVisible >= 0 {
				m.selectedIdx = firstVisible
				m.selectedJobID = m.jobs[firstVisible].ID
			} else {
				m.selectedIdx = -1
				m.selectedJobID = 0
			}
		}
	} else if m.currentView == viewReview && m.currentReview != nil && m.currentReview.Job != nil {
		targetID := m.currentReview.Job.ID
		for i, job := range m.jobs {
			if job.ID == targetID {
				m.selectedIdx = i
				m.selectedJobID = targetID
				break
			}
		}
		if m.selectedJobID == 0 {
			m.selectedIdx = 0
			m.selectedJobID = m.jobs[0].ID
		}
	} else {
		firstVisible := m.findFirstVisibleJob()
		if firstVisible >= 0 {
			m.selectedIdx = firstVisible
			m.selectedJobID = m.jobs[firstVisible].ID
		} else if len(m.activeRepoFilter) == 0 && len(m.jobs) > 0 {
			m.selectedIdx = 0
			m.selectedJobID = m.jobs[0].ID
		} else {
			m.selectedIdx = -1
			m.selectedJobID = 0
		}
	}

	// Auto-paginate when hide-addressed hides too many jobs
	if m.currentView == viewQueue &&
		m.hideAddressed &&
		m.canPaginate() &&
		len(m.getVisibleJobs()) < m.queueVisibleRows() {
		m.loadingMore = true
		return m, m.fetchMoreJobs()
	}

	// Auto-navigate after pagination triggered from review/prompt view
	if msg.append && m.paginateNav != 0 && m.currentView == m.paginateNav {
		nav := m.paginateNav
		m.paginateNav = 0
		switch nav {
		case viewReview:
			nextIdx := m.findNextViewableJob()
			if nextIdx >= 0 {
				m.selectedIdx = nextIdx
				m.updateSelectedJobID()
				m.reviewScroll = 0
				job := m.jobs[nextIdx]
				switch job.Status {
				case storage.JobStatusDone:
					return m, m.fetchReview(job.ID)
				case storage.JobStatusFailed:
					m.currentBranch = ""
					m.currentReview = &storage.Review{
						Agent:  job.Agent,
						Output: "Job failed:\n\n" + job.Error,
						Job:    &job,
					}
				}
			}
		case viewKindPrompt:
			nextIdx := m.findNextPromptableJob()
			if nextIdx >= 0 {
				m.selectedIdx = nextIdx
				m.updateSelectedJobID()
				m.promptScroll = 0
				job := m.jobs[nextIdx]
				if job.Status == storage.JobStatusDone {
					return m, m.fetchReviewForPrompt(job.ID)
				} else if job.Status == storage.JobStatusRunning && job.Prompt != "" {
					m.currentReview = &storage.Review{
						Agent:  job.Agent,
						Prompt: job.Prompt,
						Job:    &job,
					}
				}
			}
		case viewLog:
			nextIdx := m.findNextLoggableJob()
			if nextIdx >= 0 {
				m.selectedIdx = nextIdx
				m.updateSelectedJobID()
				job := m.jobs[nextIdx]
				return m.openLogView(
					job.ID, job.Status, m.logFromView,
				)
			}
		}
	} else if !msg.append && !m.loadingMore {
		m.paginateNav = 0
	}

	return m, nil
}

// handleStatusMsg processes daemon status updates.
func (m model) handleStatusMsg(msg statusMsg) (tea.Model, tea.Cmd) {
	m.status = storage.DaemonStatus(msg)
	m.consecutiveErrors = 0
	if m.status.Version != "" {
		m.daemonVersion = m.status.Version
		m.versionMismatch = m.daemonVersion != version.Version
	}
	if m.statusFetchedOnce && m.status.ConfigReloadCounter != m.lastConfigReloadCounter {
		m.flashMessage = "Config reloaded"
		m.flashExpiresAt = time.Now().Add(5 * time.Second)
		m.flashView = m.currentView
	}
	m.lastConfigReloadCounter = m.status.ConfigReloadCounter
	m.statusFetchedOnce = true
	return m, nil
}

// handleAddressedResultMsg processes the result of an addressed toggle API call.
func (m model) handleAddressedResultMsg(msg addressedResultMsg) (tea.Model, tea.Cmd) {
	isCurrentRequest := false
	if msg.jobID > 0 {
		if pending, ok := m.pendingAddressed[msg.jobID]; ok && pending.seq == msg.seq {
			isCurrentRequest = true
		}
	} else if msg.reviewView && msg.reviewID > 0 {
		if pending, ok := m.pendingReviewAddressed[msg.reviewID]; ok && pending.seq == msg.seq {
			isCurrentRequest = true
		}
	}

	if msg.err != nil {
		if isCurrentRequest {
			if msg.reviewView {
				if m.currentReview != nil && m.currentReview.ID == msg.reviewID {
					m.currentReview.Addressed = msg.oldState
				}
			}
			if msg.jobID > 0 {
				m.setJobAddressed(msg.jobID, msg.oldState)
				delete(m.pendingAddressed, msg.jobID)
				// Reverse the optimistic stats delta
				m.applyStatsDelta(msg.oldState)
			} else if msg.reviewID > 0 {
				delete(m.pendingReviewAddressed, msg.reviewID)
			}
			m.err = msg.err
		}
	} else {
		if isCurrentRequest && msg.jobID == 0 && msg.reviewID > 0 {
			delete(m.pendingReviewAddressed, msg.reviewID)
		}
	}
	return m, nil
}

// handleReconnectMsg processes daemon reconnection attempts.
func (m model) handleReconnectMsg(msg reconnectMsg) (tea.Model, tea.Cmd) {
	m.reconnecting = false
	if msg.err == nil && msg.newAddr != "" && msg.newAddr != m.serverAddr {
		m.serverAddr = msg.newAddr
		m.consecutiveErrors = 0
		m.err = nil
		if msg.version != "" {
			m.daemonVersion = msg.version
		}
		m.clearFetchFailed()
		m.loadingJobs = true
		cmds := []tea.Cmd{m.fetchJobs(), m.fetchStatus()}
		if cmd := m.fetchUnloadedBranches(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// handleFixKey opens the fix prompt modal for the currently selected job.
func (m model) handleFixKey() (tea.Model, tea.Cmd) {
	if m.currentView != viewQueue && m.currentView != viewReview {
		return m, nil
	}

	// Get the selected job
	var job storage.ReviewJob
	if m.currentView == viewReview {
		if m.currentReview == nil || m.currentReview.Job == nil {
			return m, nil
		}
		job = *m.currentReview.Job
	} else {
		if len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
			return m, nil
		}
		job = m.jobs[m.selectedIdx]
	}

	// Only allow fix on completed review jobs (not fix jobs —
	// fix-of-fix chains are not supported).
	if job.IsFixJob() {
		m.flashMessage = "Cannot fix a fix job"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = m.currentView
		return m, nil
	}
	if job.Status != storage.JobStatusDone {
		m.flashMessage = "Can only fix completed reviews"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = m.currentView
		return m, nil
	}

	if m.currentView == viewReview {
		// Open inline fix panel within review view
		m.fixPromptJobID = job.ID
		m.fixPromptText = ""
		m.reviewFixPanelOpen = true
		m.reviewFixPanelFocused = true
		return m, nil
	}

	// Fetch the review and open the inline fix panel when it loads
	m.fixPromptJobID = job.ID
	m.fixPromptText = ""
	m.reviewFixPanelPending = true
	m.reviewFromView = viewQueue
	m.selectedJobID = job.ID
	return m, m.fetchReview(job.ID)
}

// handleReviewFixPanelKey handles key input when the inline fix panel is focused.
func (m model) handleReviewFixPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.reviewFixPanelOpen = false
		m.reviewFixPanelFocused = false
		m.fixPromptText = ""
		m.fixPromptJobID = 0
		return m, nil
	case "tab":
		m.reviewFixPanelFocused = false
		return m, nil
	case "enter":
		jobID := m.fixPromptJobID
		prompt := m.fixPromptText
		m.reviewFixPanelOpen = false
		m.reviewFixPanelFocused = false
		m.fixPromptText = ""
		m.fixPromptJobID = 0
		m.currentView = viewTasks
		return m, m.triggerFix(jobID, prompt, "")
	case "backspace":
		if len(m.fixPromptText) > 0 {
			runes := []rune(m.fixPromptText)
			m.fixPromptText = string(runes[:len(runes)-1])
		}
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			for _, r := range msg.Runes {
				if unicode.IsPrint(r) {
					m.fixPromptText += string(r)
				}
			}
		}
		return m, nil
	}
}

// handleTabKey shifts focus to the fix panel when it is open in review view.
func (m model) handleTabKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewReview && m.reviewFixPanelOpen && !m.reviewFixPanelFocused {
		m.reviewFixPanelFocused = true
	}
	return m, nil
}

// handleToggleTasksKey switches between queue and tasks view.
func (m model) handleToggleTasksKey() (tea.Model, tea.Cmd) {
	if m.currentView == viewTasks {
		m.currentView = viewQueue
		return m, nil
	}
	if m.currentView == viewQueue {
		m.currentView = viewTasks
		return m, m.fetchFixJobs()
	}
	return m, nil
}

// handleWorktreeConfirmKey handles key input in the worktree creation confirmation modal.
func (m model) handleWorktreeConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "n":
		m.currentView = viewTasks
		m.worktreeConfirmJobID = 0
		m.worktreeConfirmBranch = ""
		return m, nil
	case "enter", "y":
		jobID := m.worktreeConfirmJobID
		m.currentView = viewTasks
		m.worktreeConfirmJobID = 0
		m.worktreeConfirmBranch = ""
		return m, m.applyFixPatchInWorktree(jobID)
	}
	return m, nil
}

// handleTasksKey handles key input in the tasks view.
func (m model) handleTasksKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc", "T":
		m.currentView = viewQueue
		return m, nil
	case "up", "k":
		if m.fixSelectedIdx > 0 {
			m.fixSelectedIdx--
		}
		return m, nil
	case "down", "j":
		if m.fixSelectedIdx < len(m.fixJobs)-1 {
			m.fixSelectedIdx++
		}
		return m, nil
	case "enter":
		// View task: prompt for running, review for done/applied, log for failed
		if len(m.fixJobs) > 0 && m.fixSelectedIdx < len(m.fixJobs) {
			job := m.fixJobs[m.fixSelectedIdx]
			switch {
			case job.Status == storage.JobStatusRunning:
				if job.Prompt != "" {
					m.currentReview = &storage.Review{
						Agent:  job.Agent,
						Prompt: job.Prompt,
						Job:    &job,
					}
					m.reviewFromView = viewTasks
					m.currentView = viewKindPrompt
					m.promptScroll = 0
					m.promptFromQueue = false
					return m, nil
				}
				// No prompt yet, go straight to log view
				return m.openLogView(job.ID, job.Status, viewTasks)
			case job.HasViewableOutput():
				m.selectedJobID = job.ID
				m.reviewFromView = viewTasks
				return m, m.fetchReview(job.ID)
			case job.Status == storage.JobStatusFailed:
				return m.openLogView(job.ID, job.Status, viewTasks)
			}
		}
		return m, nil
	case "l", "t":
		// View agent log for any non-queued job
		if len(m.fixJobs) > 0 && m.fixSelectedIdx < len(m.fixJobs) {
			job := m.fixJobs[m.fixSelectedIdx]
			if job.Status == storage.JobStatusQueued {
				m.flashMessage = "Job is queued - not yet running"
				m.flashExpiresAt = time.Now().Add(2 * time.Second)
				m.flashView = viewTasks
				return m, nil
			}
			return m.openLogView(job.ID, job.Status, viewTasks)
		}
		return m, nil
	case "A":
		// Apply patch (handled in Phase 5)
		if len(m.fixJobs) > 0 && m.fixSelectedIdx < len(m.fixJobs) {
			job := m.fixJobs[m.fixSelectedIdx]
			if job.Status == storage.JobStatusDone {
				return m, m.applyFixPatch(job.ID)
			}
		}
		return m, nil
	case "R":
		// Manually trigger rebase for a completed or rebased fix job
		if len(m.fixJobs) > 0 && m.fixSelectedIdx < len(m.fixJobs) {
			job := m.fixJobs[m.fixSelectedIdx]
			if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusRebased {
				return m, m.triggerRebase(job.ID)
			}
		}
		return m, nil
	case "x":
		// Cancel fix job
		if len(m.fixJobs) > 0 && m.fixSelectedIdx < len(m.fixJobs) {
			job := &m.fixJobs[m.fixSelectedIdx]
			if job.Status == storage.JobStatusRunning || job.Status == storage.JobStatusQueued {
				oldStatus := job.Status
				oldFinishedAt := job.FinishedAt
				job.Status = storage.JobStatusCanceled
				now := time.Now()
				job.FinishedAt = &now
				return m, m.cancelJob(job.ID, oldStatus, oldFinishedAt)
			}
		}
		return m, nil
	case "p":
		// View patch for completed fix jobs
		if len(m.fixJobs) > 0 && m.fixSelectedIdx < len(m.fixJobs) {
			job := m.fixJobs[m.fixSelectedIdx]
			if job.HasViewableOutput() {
				return m, m.fetchPatch(job.ID)
			}
			m.flashMessage = "Patch not yet available"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = m.currentView
		}
		return m, nil
	case "P":
		// Open parent review for this fix task
		if len(m.fixJobs) > 0 && m.fixSelectedIdx < len(m.fixJobs) {
			job := m.fixJobs[m.fixSelectedIdx]
			if job.ParentJobID == nil || *job.ParentJobID == 0 {
				m.flashMessage = "No parent review for this task"
				m.flashExpiresAt = time.Now().Add(2 * time.Second)
				m.flashView = viewTasks
				return m, nil
			}
			m.selectedJobID = *job.ParentJobID
			m.reviewFromView = viewTasks
			return m, m.fetchReview(*job.ParentJobID)
		}
		return m, nil
	case "?":
		m.fixShowHelp = !m.fixShowHelp
		return m, nil
	}
	return m, nil
}

// handlePatchKey handles key input in the patch viewer.
func (m model) handlePatchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = viewTasks
		m.patchText = ""
		m.patchScroll = 0
		m.patchJobID = 0
		return m, nil
	case "up", "k":
		if m.patchScroll > 0 {
			m.patchScroll--
		}
		return m, nil
	case "down", "j":
		m.patchScroll++
		return m, nil
	case "pgup":
		visibleLines := max(m.height-4, 1)
		m.patchScroll = max(0, m.patchScroll-visibleLines)
		return m, tea.ClearScreen
	case "pgdown":
		visibleLines := max(m.height-4, 1)
		m.patchScroll += visibleLines
		return m, tea.ClearScreen
	case "home", "g":
		m.patchScroll = 0
		return m, nil
	case "end", "G":
		lines := strings.Split(m.patchText, "\n")
		visibleRows := max(m.height-4, 1)
		m.patchScroll = max(len(lines)-visibleRows, 0)
		return m, nil
	}
	return m, nil
}

// closeFixPanel resets all inline fix panel state. Call this when
// leaving review view or navigating to a different review.
func (m *model) closeFixPanel() {
	m.reviewFixPanelOpen = false
	m.reviewFixPanelFocused = false
	m.reviewFixPanelPending = false
	m.fixPromptText = ""
	m.fixPromptJobID = 0
}

// handleConnectionError tracks consecutive connection errors and triggers reconnection.
func (m *model) handleConnectionError(err error) tea.Cmd {
	if isConnectionError(err) {
		m.consecutiveErrors++
		if m.consecutiveErrors >= 3 && !m.reconnecting {
			m.reconnecting = true
			return m.tryReconnect()
		}
	}
	return nil
}

// handleWindowSizeMsg processes terminal resize events.
func (m model) handleWindowSizeMsg(
	msg tea.WindowSizeMsg,
) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.heightDetected = true

	// If terminal can show more jobs than we have, re-fetch to fill
	if !m.loadingMore && !m.loadingJobs &&
		len(m.jobs) > 0 && m.hasMore &&
		len(m.activeRepoFilter) <= 1 {
		newVisibleRows := m.queueVisibleRows() + queuePrefetchBuffer
		if newVisibleRows > len(m.jobs) {
			m.loadingJobs = true
			return m, m.fetchJobs()
		}
	}

	// Width change in log view requires full re-render
	if m.currentView == viewLog && m.logLines != nil {
		m.logOffset = 0
		m.logLines = nil
		m.logFmtr = streamfmt.NewWithWidth(
			io.Discard, msg.Width, m.glamourStyle,
		)
		m.logFetchSeq++
		m.logLoading = true
		return m, m.fetchJobLog(m.logJobID)
	}

	return m, nil
}

// handleTickMsg processes periodic tick events for adaptive polling.
func (m model) handleTickMsg(
	_ tickMsg,
) (tea.Model, tea.Cmd) {
	// Skip job refresh while pagination or another refresh is in flight
	if m.loadingMore || m.loadingJobs {
		return m, tea.Batch(m.tick(), m.fetchStatus())
	}
	cmds := []tea.Cmd{m.tick(), m.fetchJobs(), m.fetchStatus()}
	if m.currentView == viewTasks || m.hasActiveFixJobs() {
		cmds = append(cmds, m.fetchFixJobs())
	}
	return m, tea.Batch(cmds...)
}

// handleLogTickMsg processes log stream polling ticks.
func (m model) handleLogTickMsg(
	_ logTickMsg,
) (tea.Model, tea.Cmd) {
	if m.currentView == viewLog && m.logStreaming &&
		m.logJobID > 0 && !m.logLoading {
		m.logLoading = true
		return m, m.fetchJobLog(m.logJobID)
	}
	return m, nil
}

// handleUpdateCheckMsg processes version update check results.
func (m model) handleUpdateCheckMsg(
	msg updateCheckMsg,
) (tea.Model, tea.Cmd) {
	m.updateAvailable = msg.version
	m.updateIsDevBuild = msg.isDevBuild
	return m, nil
}

// handleReviewMsg processes review fetch results.
func (m model) handleReviewMsg(
	msg reviewMsg,
) (tea.Model, tea.Cmd) {
	if msg.jobID != m.selectedJobID {
		// Stale fetch -- clear pending fix panel if it was
		// for this (now-discarded) review.
		if m.reviewFixPanelPending &&
			m.fixPromptJobID == msg.jobID {
			m.reviewFixPanelPending = false
			m.fixPromptJobID = 0
		}
		return m, nil
	}
	m.consecutiveErrors = 0
	m.currentReview = msg.review
	m.currentResponses = msg.responses
	m.currentBranch = msg.branchName
	m.currentView = viewReview
	m.reviewScroll = 0
	if m.reviewFixPanelPending &&
		m.fixPromptJobID == msg.review.JobID {
		m.reviewFixPanelPending = false
		m.reviewFixPanelOpen = true
		m.reviewFixPanelFocused = true
	}
	return m, nil
}

// handlePromptMsg processes prompt fetch results.
func (m model) handlePromptMsg(
	msg promptMsg,
) (tea.Model, tea.Cmd) {
	if msg.jobID != m.selectedJobID {
		return m, nil
	}
	m.consecutiveErrors = 0
	m.currentReview = msg.review
	m.currentView = viewKindPrompt
	m.promptScroll = 0
	return m, nil
}

// handleLogOutputMsg processes log output from the daemon.
func (m model) handleLogOutputMsg(
	msg logOutputMsg,
) (tea.Model, tea.Cmd) {
	// Drop stale responses from previous log sessions.
	if msg.seq != m.logFetchSeq {
		return m, nil
	}
	m.logLoading = false
	m.consecutiveErrors = 0
	// If the user navigated away while a fetch was in-flight, drop it.
	if m.currentView != viewLog {
		return m, nil
	}
	if msg.err != nil {
		if errors.Is(msg.err, errNoLog) {
			flash := "No log available for this job"
			if job := m.logViewLookupJob(); job != nil &&
				job.Status == storage.JobStatusFailed &&
				job.Error != "" {
				flash = fmt.Sprintf(
					"Job #%d failed: %s",
					m.logJobID, job.Error,
				)
			}
			m.flashMessage = flash
			m.flashExpiresAt = time.Now().Add(5 * time.Second)
			m.flashView = m.logFromView
			m.currentView = m.logFromView
			m.logStreaming = false
			return m, nil
		}
		m.err = msg.err
		return m, nil
	}
	if m.currentView == viewLog {
		// Persist formatter state for incremental polls
		if msg.fmtr != nil {
			m.logFmtr = msg.fmtr
		}

		if msg.append {
			if len(msg.lines) > 0 {
				m.logLines = append(
					m.logLines, msg.lines...,
				)
			}
		} else if len(msg.lines) > 0 {
			m.logLines = msg.lines
		} else if m.logLines == nil {
			if !msg.hasMore {
				m.logLines = []logLine{}
			}
		} else if msg.newOffset == 0 {
			m.logLines = []logLine{}
		}
		m.logOffset = msg.newOffset
		m.logStreaming = msg.hasMore
		if m.logFollow && len(m.logLines) > 0 {
			visibleLines := m.logVisibleLines()
			maxScroll := max(len(m.logLines)-visibleLines, 0)
			m.logScroll = maxScroll
		}
		if m.logStreaming {
			return m, tea.Tick(
				500*time.Millisecond,
				func(t time.Time) tea.Msg {
					return logTickMsg{}
				},
			)
		}
	}
	return m, nil
}

// handleAddressedToggleMsg processes addressed state toggle messages.
func (m model) handleAddressedToggleMsg(
	msg addressedMsg,
) (tea.Model, tea.Cmd) {
	if m.currentReview != nil {
		m.currentReview.Addressed = bool(msg)
	}
	return m, nil
}

// handleCancelResultMsg processes job cancellation results.
func (m model) handleCancelResultMsg(
	msg cancelResultMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setJobStatus(msg.jobID, msg.oldState)
		m.setJobFinishedAt(msg.jobID, msg.oldFinishedAt)
		m.err = msg.err
	}
	return m, nil
}

// handleRerunResultMsg processes job re-run results.
func (m model) handleRerunResultMsg(
	msg rerunResultMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setJobStatus(msg.jobID, msg.oldState)
		m.setJobStartedAt(msg.jobID, msg.oldStartedAt)
		m.setJobFinishedAt(msg.jobID, msg.oldFinishedAt)
		m.setJobError(msg.jobID, msg.oldError)
		m.err = msg.err
	}
	return m, nil
}

// handleReposMsg processes repo list results for the filter modal.
func (m model) handleReposMsg(
	msg reposMsg,
) (tea.Model, tea.Cmd) {
	m.consecutiveErrors = 0
	// Build filterTree from repos (all collapsed, no children)
	m.filterTree = make([]treeFilterNode, len(msg.repos))
	for i, r := range msg.repos {
		m.filterTree[i] = treeFilterNode{
			name:      r.name,
			rootPaths: r.rootPaths,
			count:     r.count,
		}
	}
	// Move cwd repo to first position for quick access
	if m.cwdRepoRoot != "" && len(m.filterTree) > 1 {
		moveToFront(m.filterTree, func(n treeFilterNode) bool {
			return slices.Contains(n.rootPaths, m.cwdRepoRoot)
		})
	}
	m.rebuildFilterFlatList()
	// Pre-select active filter if any
	if len(m.activeRepoFilter) > 0 {
		for i, entry := range m.filterFlatList {
			if entry.repoIdx >= 0 && entry.branchIdx == -1 &&
				rootPathsMatch(
					m.filterTree[entry.repoIdx].rootPaths,
					m.activeRepoFilter,
				) {
				m.filterSelectedIdx = i
				break
			}
		}
	}
	// Auto-expand repo to branches when opened via 'b' key
	if m.filterBranchMode && len(m.filterTree) > 0 {
		targetIdx := 0
		if len(m.activeRepoFilter) > 0 {
			for i, node := range m.filterTree {
				if rootPathsMatch(
					node.rootPaths, m.activeRepoFilter,
				) {
					targetIdx = i
					goto foundTarget
				}
			}
		}
		if m.cwdRepoRoot != "" {
			for i, node := range m.filterTree {
				for _, p := range node.rootPaths {
					if p == m.cwdRepoRoot {
						targetIdx = i
						goto foundTarget
					}
				}
			}
		}
	foundTarget:
		m.filterTree[targetIdx].loading = true
		for i, entry := range m.filterFlatList {
			if entry.repoIdx == targetIdx &&
				entry.branchIdx == -1 {
				m.filterSelectedIdx = i
				break
			}
		}
		return m, m.fetchBranchesForRepo(
			m.filterTree[targetIdx].rootPaths,
			targetIdx, true, m.filterSearchSeq,
		)
	}
	// If user typed search before repos loaded, kick off fetches
	if cmd := m.fetchUnloadedBranches(); cmd != nil {
		return m, cmd
	}
	return m, nil
}

// handleRepoBranchesMsg processes branch list results for a repo.
func (m model) handleRepoBranchesMsg(
	msg repoBranchesMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.filterBranchMode = false
		if msg.repoIdx >= 0 &&
			msg.repoIdx < len(m.filterTree) &&
			rootPathsMatch(
				m.filterTree[msg.repoIdx].rootPaths,
				msg.rootPaths,
			) {
			m.filterTree[msg.repoIdx].loading = false
			if !msg.expandOnLoad && m.filterSearch != "" &&
				msg.searchSeq == m.filterSearchSeq {
				m.filterTree[msg.repoIdx].fetchFailed = true
			}
		}
		if cmd := m.handleConnectionError(msg.err); cmd != nil {
			return m, cmd
		}
		return m, m.fetchUnloadedBranches()
	}
	// Verify filter view, repoIdx valid, and identity matches
	if m.currentView == viewFilter &&
		msg.repoIdx >= 0 &&
		msg.repoIdx < len(m.filterTree) &&
		rootPathsMatch(
			m.filterTree[msg.repoIdx].rootPaths,
			msg.rootPaths,
		) {
		m.consecutiveErrors = 0
		m.filterTree[msg.repoIdx].loading = false
		m.filterTree[msg.repoIdx].children = msg.branches
		if msg.expandOnLoad {
			m.filterTree[msg.repoIdx].expanded = true
		}
		// Move cwd branch to first position if this is cwd repo
		if m.cwdBranch != "" && len(msg.branches) > 1 {
			isCwdRepo := slices.Contains(
				m.filterTree[msg.repoIdx].rootPaths,
				m.cwdRepoRoot,
			)
			if isCwdRepo {
				moveToFront(
					m.filterTree[msg.repoIdx].children,
					func(b branchFilterItem) bool {
						return b.name == m.cwdBranch
					},
				)
			}
		}
		m.rebuildFilterFlatList()
		// Auto-position on first branch when opened via 'b'
		if m.filterBranchMode {
			m.filterBranchMode = false
			for i, entry := range m.filterFlatList {
				if entry.repoIdx == msg.repoIdx &&
					entry.branchIdx >= 0 {
					m.filterSelectedIdx = i
					break
				}
			}
		}
		if cmd := m.fetchUnloadedBranches(); cmd != nil {
			return m, cmd
		}
	}
	return m, nil
}

// handleBranchesMsg processes branch backfill completion.
func (m model) handleBranchesMsg(
	msg branchesMsg,
) (tea.Model, tea.Cmd) {
	m.consecutiveErrors = 0
	m.branchBackfillDone = true
	if msg.backfillCount > 0 {
		m.flashMessage = fmt.Sprintf(
			"Backfilled branch info for %d jobs",
			msg.backfillCount,
		)
		m.flashExpiresAt = time.Now().Add(5 * time.Second)
		m.flashView = viewFilter
	}
	return m, nil
}

// handleCommentResultMsg processes comment submission results.
func (m model) handleCommentResultMsg(
	msg commentResultMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
	} else {
		if m.commentJobID == msg.jobID {
			m.commentText = ""
			m.commentJobID = 0
		}
		if m.currentView == viewReview &&
			m.currentReview != nil &&
			m.currentReview.JobID == msg.jobID {
			return m, m.fetchReview(msg.jobID)
		}
	}
	return m, nil
}

// handleClipboardResultMsg processes clipboard copy results.
func (m model) handleClipboardResultMsg(
	msg clipboardResultMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = fmt.Errorf("copy failed: %w", msg.err)
	} else {
		m.flashMessage = "Copied to clipboard"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = msg.view
	}
	return m, nil
}

// handleCommitMsgMsg processes commit message fetch results.
func (m model) handleCommitMsgMsg(
	msg commitMsgMsg,
) (tea.Model, tea.Cmd) {
	if msg.jobID != m.commitMsgJobID {
		return m, nil
	}
	if msg.err != nil {
		m.flashMessage = msg.err.Error()
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = m.currentView
		return m, nil
	}
	m.commitMsgContent = msg.content
	m.commitMsgScroll = 0
	m.currentView = viewCommitMsg
	return m, nil
}

// handleJobsErrMsg processes job fetch errors.
func (m model) handleJobsErrMsg(
	msg jobsErrMsg,
) (tea.Model, tea.Cmd) {
	if msg.seq < m.fetchSeq {
		return m, nil
	}
	m.err = msg.err
	m.loadingJobs = false
	if cmd := m.handleConnectionError(msg.err); cmd != nil {
		return m, cmd
	}
	return m, nil
}

// handlePaginationErrMsg processes pagination fetch errors.
func (m model) handlePaginationErrMsg(
	msg paginationErrMsg,
) (tea.Model, tea.Cmd) {
	if msg.seq < m.fetchSeq {
		m.loadingMore = false
		m.paginateNav = 0
		return m, nil
	}
	m.err = msg.err
	m.loadingMore = false
	m.paginateNav = 0
	if cmd := m.handleConnectionError(msg.err); cmd != nil {
		return m, cmd
	}
	return m, nil
}

// handleErrMsg processes generic error messages.
func (m model) handleErrMsg(
	msg errMsg,
) (tea.Model, tea.Cmd) {
	m.err = msg
	if cmd := m.handleConnectionError(msg); cmd != nil {
		return m, cmd
	}
	return m, nil
}

// handleFixJobsMsg processes fix job list results.
func (m model) handleFixJobsMsg(
	msg fixJobsMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
	} else {
		m.fixJobs = msg.jobs
		if m.fixSelectedIdx >= len(m.fixJobs) &&
			len(m.fixJobs) > 0 {
			m.fixSelectedIdx = len(m.fixJobs) - 1
		}
	}
	return m, nil
}

// handleFixTriggerResultMsg processes fix job trigger results.
func (m model) handleFixTriggerResultMsg(
	msg fixTriggerResultMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.flashMessage = fmt.Sprintf(
			"Fix failed: %v", msg.err,
		)
		m.flashExpiresAt = time.Now().Add(3 * time.Second)
		m.flashView = viewTasks
	} else if msg.warning != "" {
		m.flashMessage = msg.warning
		m.flashExpiresAt = time.Now().Add(5 * time.Second)
		m.flashView = viewTasks
		return m, m.fetchFixJobs()
	} else {
		m.flashMessage = fmt.Sprintf(
			"Fix job #%d enqueued", msg.job.ID,
		)
		m.flashExpiresAt = time.Now().Add(3 * time.Second)
		m.flashView = viewTasks
		return m, m.fetchFixJobs()
	}
	return m, nil
}

// handlePatchResultMsg processes patch fetch results.
func (m model) handlePatchResultMsg(
	msg patchMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.flashMessage = fmt.Sprintf(
			"Patch fetch failed: %v", msg.err,
		)
		m.flashExpiresAt = time.Now().Add(3 * time.Second)
		m.flashView = viewTasks
	} else {
		m.patchText = msg.patch
		m.patchJobID = msg.jobID
		m.patchScroll = 0
		m.currentView = viewPatch
	}
	return m, nil
}

// handleApplyPatchResultMsg processes patch application results.
func (m model) handleApplyPatchResultMsg(
	msg applyPatchResultMsg,
) (tea.Model, tea.Cmd) {
	if msg.needWorktree {
		m.worktreeConfirmJobID = msg.jobID
		m.worktreeConfirmBranch = msg.branch
		m.currentView = viewKindWorktreeConfirm
		return m, nil
	}
	if msg.rebase {
		m.flashMessage = fmt.Sprintf(
			"Patch for job #%d doesn't apply cleanly"+
				" - triggering rebase", msg.jobID,
		)
		m.flashExpiresAt = time.Now().Add(5 * time.Second)
		m.flashView = viewTasks
		return m, tea.Batch(
			m.triggerRebase(msg.jobID), m.fetchFixJobs(),
		)
	} else if msg.commitFailed {
		detail := fmt.Sprintf(
			"Job #%d: %v", msg.jobID, msg.err,
		)
		if msg.worktreeDir != "" {
			detail += fmt.Sprintf(
				" (worktree kept at %s)", msg.worktreeDir,
			)
		}
		m.flashMessage = detail
		m.flashExpiresAt = time.Now().Add(8 * time.Second)
		m.flashView = viewTasks
	} else if msg.err != nil {
		m.flashMessage = fmt.Sprintf(
			"Apply failed: %v", msg.err,
		)
		m.flashExpiresAt = time.Now().Add(3 * time.Second)
		m.flashView = viewTasks
	} else {
		m.flashMessage = fmt.Sprintf(
			"Patch from job #%d applied and committed",
			msg.jobID,
		)
		m.flashExpiresAt = time.Now().Add(3 * time.Second)
		m.flashView = viewTasks
		cmds := []tea.Cmd{m.fetchFixJobs()}
		if msg.parentJobID > 0 {
			cmds = append(
				cmds,
				m.markParentAddressed(msg.parentJobID),
			)
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}
