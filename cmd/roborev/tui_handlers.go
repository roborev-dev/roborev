package main

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

// handleKeyMsg dispatches key events to view-specific handlers.
func (m tuiModel) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Fix panel captures input when focused in review view
	if m.currentView == tuiViewReview && m.reviewFixPanelOpen && m.reviewFixPanelFocused {
		return m.handleReviewFixPanelKey(msg)
	}

	// Modal views that capture most keys for typing
	switch m.currentView {
	case tuiViewComment:
		return m.handleCommentKey(msg)
	case tuiViewFilter:
		return m.handleFilterKey(msg)
	case tuiViewLog:
		return m.handleLogKey(msg)
	case tuiViewWorktreeConfirm:
		return m.handleWorktreeConfirmKey(msg)
	case tuiViewTasks:
		return m.handleTasksKey(msg)
	case tuiViewPatch:
		return m.handlePatchKey(msg)
	}

	// Global keys shared across queue/review/prompt/commitMsg/help views
	return m.handleGlobalKey(msg)
}

// handleCommentKey handles key input in the comment modal.
func (m tuiModel) handleCommentKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
func (m tuiModel) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = tuiViewQueue
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
		m.currentView = tuiViewQueue
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
func (m *tuiModel) clearFetchFailed() {
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
// tuiRepoBranchesMsg handler.
func (m *tuiModel) fetchUnloadedBranches() tea.Cmd {
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
func (m tuiModel) handleLogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		m.currentView = tuiViewHelp
		m.helpScroll = 0
		return m, nil
	}
	return m, nil
}

// handleGlobalKey handles keys shared across queue, review, prompt, commit msg, and help views.
func (m tuiModel) handleGlobalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m tuiModel) handleQuitKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewReview {
		returnTo := m.reviewFromView
		if returnTo == 0 {
			returnTo = tuiViewQueue
		}
		m.closeFixPanel()
		m.currentView = returnTo
		m.currentReview = nil
		m.reviewScroll = 0
		m.paginateNav = 0
		if returnTo == tuiViewQueue {
			m.normalizeSelectionIfHidden()
		}
		return m, nil
	}
	if m.currentView == tuiViewPrompt {
		m.paginateNav = 0
		if m.promptFromQueue {
			m.currentView = tuiViewQueue
			m.currentReview = nil
			m.promptScroll = 0
		} else {
			m.currentView = tuiViewReview
			m.promptScroll = 0
		}
		return m, nil
	}
	if m.currentView == tuiViewCommitMsg {
		m.currentView = m.commitMsgFromView
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
		return m, nil
	}
	if m.currentView == tuiViewHelp {
		m.currentView = m.helpFromView
		return m, nil
	}
	return m, tea.Quit
}

func (m tuiModel) handleHomeKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case tuiViewQueue:
		firstVisible := m.findFirstVisibleJob()
		if firstVisible >= 0 {
			m.selectedIdx = firstVisible
			m.updateSelectedJobID()
		}
	case tuiViewReview:
		m.reviewScroll = 0
	case tuiViewPrompt:
		m.promptScroll = 0
	case tuiViewCommitMsg:
		m.commitMsgScroll = 0
	case tuiViewHelp:
		m.helpScroll = 0
	}
	return m, nil
}

func (m tuiModel) handleUpKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case tuiViewQueue:
		prevIdx := m.findPrevVisibleJob(m.selectedIdx)
		if prevIdx >= 0 {
			m.selectedIdx = prevIdx
			m.updateSelectedJobID()
		} else {
			m.flashMessage = "No newer review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = tuiViewQueue
		}
	case tuiViewReview:
		if m.reviewScroll > 0 {
			m.reviewScroll--
		}
	case tuiViewPrompt:
		if m.promptScroll > 0 {
			m.promptScroll--
		}
	case tuiViewCommitMsg:
		if m.commitMsgScroll > 0 {
			m.commitMsgScroll--
		}
	case tuiViewHelp:
		if m.helpScroll > 0 {
			m.helpScroll--
		}
	}
	return m, nil
}

func (m tuiModel) handlePrevKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case tuiViewQueue:
		prevIdx := m.findPrevVisibleJob(m.selectedIdx)
		if prevIdx >= 0 {
			m.selectedIdx = prevIdx
			m.updateSelectedJobID()
		}
	case tuiViewReview:
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
			m.flashView = tuiViewReview
		}
	case tuiViewPrompt:
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
			m.flashView = tuiViewPrompt
		}
	case tuiViewLog:
		if m.logFromView == tuiViewTasks {
			idx := m.findPrevLoggableFixJob()
			if idx >= 0 {
				m.fixSelectedIdx = idx
				job := m.fixJobs[idx]
				m.logStreaming = false
				return m.openLogView(
					job.ID, job.Status, tuiViewTasks,
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
		m.flashView = tuiViewLog
	}
	return m, nil
}

func (m tuiModel) handleDownKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case tuiViewQueue:
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
			m.flashView = tuiViewQueue
		}
	case tuiViewReview:
		m.reviewScroll++
		if m.mdCache != nil && m.reviewScroll > m.mdCache.lastReviewMaxScroll {
			m.reviewScroll = m.mdCache.lastReviewMaxScroll
		}
	case tuiViewPrompt:
		m.promptScroll++
		if m.mdCache != nil && m.promptScroll > m.mdCache.lastPromptMaxScroll {
			m.promptScroll = m.mdCache.lastPromptMaxScroll
		}
	case tuiViewCommitMsg:
		m.commitMsgScroll++
	case tuiViewHelp:
		m.helpScroll = min(m.helpScroll+1, m.helpMaxScroll())
	}
	return m, nil
}

func (m tuiModel) handleNextKey() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case tuiViewQueue:
		nextIdx := m.findNextVisibleJob(m.selectedIdx)
		if nextIdx >= 0 {
			m.selectedIdx = nextIdx
			m.updateSelectedJobID()
		} else if m.canPaginate() {
			m.loadingMore = true
			return m, m.fetchMoreJobs()
		}
	case tuiViewReview:
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
			m.paginateNav = tuiViewReview
			return m, m.fetchMoreJobs()
		} else {
			m.flashMessage = "No older review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = tuiViewReview
		}
	case tuiViewPrompt:
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
			m.paginateNav = tuiViewPrompt
			return m, m.fetchMoreJobs()
		} else {
			m.flashMessage = "No older review"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = tuiViewPrompt
		}
	case tuiViewLog:
		if m.logFromView == tuiViewTasks {
			idx := m.findNextLoggableFixJob()
			if idx >= 0 {
				m.fixSelectedIdx = idx
				job := m.fixJobs[idx]
				m.logStreaming = false
				return m.openLogView(
					job.ID, job.Status, tuiViewTasks,
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
				m.paginateNav = tuiViewLog
				return m, m.fetchMoreJobs()
			}
		}
		m.flashMessage = "No older log"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = tuiViewLog
	}
	return m, nil
}

func (m tuiModel) handlePageUpKey() (tea.Model, tea.Cmd) {
	pageSize := max(1, m.height-10)
	switch m.currentView {
	case tuiViewQueue:
		for range pageSize {
			prevIdx := m.findPrevVisibleJob(m.selectedIdx)
			if prevIdx < 0 {
				break
			}
			m.selectedIdx = prevIdx
		}
		m.updateSelectedJobID()
	case tuiViewReview:
		if m.mdCache != nil && m.reviewScroll > m.mdCache.lastReviewMaxScroll {
			m.reviewScroll = m.mdCache.lastReviewMaxScroll
		}
		m.reviewScroll = max(0, m.reviewScroll-pageSize)
		return m, tea.ClearScreen
	case tuiViewPrompt:
		if m.mdCache != nil && m.promptScroll > m.mdCache.lastPromptMaxScroll {
			m.promptScroll = m.mdCache.lastPromptMaxScroll
		}
		m.promptScroll = max(0, m.promptScroll-pageSize)
		return m, tea.ClearScreen
	case tuiViewHelp:
		m.helpScroll = max(0, m.helpScroll-pageSize)
	}
	return m, nil
}

func (m tuiModel) handlePageDownKey() (tea.Model, tea.Cmd) {
	pageSize := max(1, m.height-10)
	switch m.currentView {
	case tuiViewQueue:
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
	case tuiViewReview:
		m.reviewScroll += pageSize
		if m.mdCache != nil && m.reviewScroll > m.mdCache.lastReviewMaxScroll {
			m.reviewScroll = m.mdCache.lastReviewMaxScroll
		}
		return m, tea.ClearScreen
	case tuiViewPrompt:
		m.promptScroll += pageSize
		if m.mdCache != nil && m.promptScroll > m.mdCache.lastPromptMaxScroll {
			m.promptScroll = m.mdCache.lastPromptMaxScroll
		}
		return m, tea.ClearScreen
	case tuiViewHelp:
		m.helpScroll = min(m.helpScroll+pageSize, m.helpMaxScroll())
	}
	return m, nil
}

func (m tuiModel) handleEnterKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
		return m, nil
	}
	job := m.jobs[m.selectedIdx]
	switch job.Status {
	case storage.JobStatusDone:
		m.reviewFromView = tuiViewQueue
		return m, m.fetchReview(job.ID)
	case storage.JobStatusFailed:
		m.currentBranch = ""
		m.currentReview = &storage.Review{
			Agent:  job.Agent,
			Output: "Job failed:\n\n" + job.Error,
			Job:    &job,
		}
		m.reviewFromView = tuiViewQueue
		m.currentView = tuiViewReview
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
	m.flashView = tuiViewQueue
	return m, nil
}

func (m tuiModel) handlePromptKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
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
			m.currentView = tuiViewPrompt
			m.promptScroll = 0
			m.promptFromQueue = true
			return m, nil
		}
	} else if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.Prompt != "" {
		m.closeFixPanel()
		m.currentView = tuiViewPrompt
		m.promptScroll = 0
		m.promptFromQueue = false
	} else if m.currentView == tuiViewPrompt {
		if m.promptFromQueue {
			m.currentView = tuiViewQueue
			m.currentReview = nil
			m.promptScroll = 0
		} else {
			m.currentView = tuiViewReview
			m.promptScroll = 0
		}
	}
	return m, nil
}

func (m tuiModel) handleAddressedKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.ID > 0 {
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
	} else if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
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

func (m tuiModel) handleCancelKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
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

func (m tuiModel) handleRerunKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
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

func (m tuiModel) handleLogKey2() (tea.Model, tea.Cmd) {
	// From prompt view: view log for the job being viewed
	if m.currentView == tuiViewPrompt && m.currentReview != nil && m.currentReview.Job != nil {
		job := m.currentReview.Job
		return m.openLogView(job.ID, job.Status, m.reviewFromView)
	}

	if m.currentView != tuiViewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
		return m, nil
	}
	job := m.jobs[m.selectedIdx]
	switch job.Status {
	case storage.JobStatusQueued:
		m.flashMessage = "Job is queued - not yet running"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = tuiViewQueue
		return m, nil
	default:
		return m.openLogView(job.ID, job.Status, tuiViewQueue)
	}
}

// openLogView opens the log view for a job of any status.
// Running jobs stream with follow; completed jobs show a static view.
func (m tuiModel) openLogView(
	jobID int64, status storage.JobStatus, fromView tuiView,
) (tea.Model, tea.Cmd) {
	m.logJobID = jobID
	m.logLines = nil
	m.logScroll = 0
	m.logFromView = fromView
	m.currentView = tuiViewLog
	m.logOffset = 0
	m.logFmtr = newStreamFormatterWithWidth(
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

func (m tuiModel) handleFilterOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue {
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
	m.currentView = tuiViewFilter
	if !m.branchBackfillDone {
		return m, tea.Batch(m.fetchRepos(), m.backfillBranches())
	}
	return m, m.fetchRepos()
}

func (m tuiModel) handleBranchFilterOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue {
		return m, nil
	}
	// Block branch filter when locked via CLI flag
	if m.lockedBranchFilter {
		return m, nil
	}
	m.filterBranchMode = true
	return m.handleFilterOpenKey()
}

func (m tuiModel) handleHideAddressedKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue {
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

func (m tuiModel) handleCommentOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		job := m.jobs[m.selectedIdx]
		if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed {
			if m.commentJobID != job.ID {
				m.commentText = ""
			}
			m.commentJobID = job.ID
			m.commentCommit = git.ShortSHA(job.GitRef)
			m.commentFromView = tuiViewQueue
			m.currentView = tuiViewComment
		}
		return m, nil
	} else if m.currentView == tuiViewReview && m.currentReview != nil {
		if m.commentJobID != m.currentReview.JobID {
			m.commentText = ""
		}
		m.commentJobID = m.currentReview.JobID
		m.commentCommit = ""
		if m.currentReview.Job != nil {
			m.commentCommit = git.ShortSHA(
				m.currentReview.Job.GitRef)
		}
		m.commentFromView = tuiViewReview
		m.currentView = tuiViewComment
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleCopyKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.Output != "" {
		return m, m.copyToClipboard(m.currentReview)
	} else if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
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
		m.flashView = tuiViewQueue
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleCommitMsgKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		job := m.jobs[m.selectedIdx]
		m.commitMsgFromView = m.currentView
		m.commitMsgJobID = job.ID
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
		return m, m.fetchCommitMsg(&job)
	} else if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.Job != nil {
		job := m.currentReview.Job
		m.commitMsgFromView = m.currentView
		m.commitMsgJobID = job.ID
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
		return m, m.fetchCommitMsg(job)
	}
	return m, nil
}

func (m tuiModel) handleHelpKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewHelp {
		m.currentView = m.helpFromView
		return m, nil
	}
	if m.currentView == tuiViewQueue || m.currentView == tuiViewReview || m.currentView == tuiViewPrompt || m.currentView == tuiViewLog {
		m.helpFromView = m.currentView
		m.currentView = tuiViewHelp
		m.helpScroll = 0
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleEscKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewQueue && len(m.filterStack) > 0 {
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
	} else if m.currentView == tuiViewQueue && m.hideAddressed {
		m.hideAddressed = false
		m.hasMore = false
		m.selectedIdx = -1
		m.selectedJobID = 0
		m.fetchSeq++
		m.loadingJobs = true
		return m, m.fetchJobs()
	} else if m.currentView == tuiViewReview {
		// If fix panel is open (unfocused), esc closes it rather than leaving the review
		if m.reviewFixPanelOpen {
			m.closeFixPanel()
			return m, nil
		}
		m.closeFixPanel()
		returnTo := m.reviewFromView
		if returnTo == 0 {
			returnTo = tuiViewQueue
		}
		m.currentView = returnTo
		m.currentReview = nil
		m.reviewScroll = 0
		m.paginateNav = 0
		if returnTo == tuiViewQueue {
			m.normalizeSelectionIfHidden()
			if m.hideAddressed && !m.loadingJobs {
				m.loadingJobs = true
				return m, m.fetchJobs()
			}
		}
	} else if m.currentView == tuiViewPrompt {
		m.paginateNav = 0
		if m.promptFromQueue {
			m.currentView = tuiViewQueue
			m.currentReview = nil
			m.promptScroll = 0
		} else {
			m.currentView = tuiViewReview
			m.promptScroll = 0
		}
	} else if m.currentView == tuiViewCommitMsg {
		m.currentView = m.commitMsgFromView
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
	} else if m.currentView == tuiViewHelp {
		m.currentView = m.helpFromView
	}
	return m, nil
}

// handleJobsMsg processes job list updates from the server.
func (m tuiModel) handleJobsMsg(msg tuiJobsMsg) (tea.Model, tea.Cmd) {
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
		if m.currentView != tuiViewReview || m.currentReview == nil || m.currentReview.Job == nil {
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
	} else if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.Job != nil {
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
	if m.currentView == tuiViewQueue &&
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
		case tuiViewReview:
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
		case tuiViewPrompt:
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
		case tuiViewLog:
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
func (m tuiModel) handleStatusMsg(msg tuiStatusMsg) (tea.Model, tea.Cmd) {
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
func (m tuiModel) handleAddressedResultMsg(msg tuiAddressedResultMsg) (tea.Model, tea.Cmd) {
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
func (m tuiModel) handleReconnectMsg(msg tuiReconnectMsg) (tea.Model, tea.Cmd) {
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
func (m tuiModel) handleFixKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue && m.currentView != tuiViewReview {
		return m, nil
	}

	// Get the selected job
	var job storage.ReviewJob
	if m.currentView == tuiViewReview {
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

	if m.currentView == tuiViewReview {
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
	m.reviewFromView = tuiViewQueue
	m.selectedJobID = job.ID
	return m, m.fetchReview(job.ID)
}

// handleReviewFixPanelKey handles key input when the inline fix panel is focused.
func (m tuiModel) handleReviewFixPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		m.currentView = tuiViewTasks
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
func (m tuiModel) handleTabKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewReview && m.reviewFixPanelOpen && !m.reviewFixPanelFocused {
		m.reviewFixPanelFocused = true
	}
	return m, nil
}

// handleToggleTasksKey switches between queue and tasks view.
func (m tuiModel) handleToggleTasksKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewTasks {
		m.currentView = tuiViewQueue
		return m, nil
	}
	if m.currentView == tuiViewQueue {
		m.currentView = tuiViewTasks
		return m, m.fetchFixJobs()
	}
	return m, nil
}

// handleWorktreeConfirmKey handles key input in the worktree creation confirmation modal.
func (m tuiModel) handleWorktreeConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "n":
		m.currentView = tuiViewTasks
		m.worktreeConfirmJobID = 0
		m.worktreeConfirmBranch = ""
		return m, nil
	case "enter", "y":
		jobID := m.worktreeConfirmJobID
		m.currentView = tuiViewTasks
		m.worktreeConfirmJobID = 0
		m.worktreeConfirmBranch = ""
		return m, m.applyFixPatchInWorktree(jobID)
	}
	return m, nil
}

// handleTasksKey handles key input in the tasks view.
func (m tuiModel) handleTasksKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc", "T":
		m.currentView = tuiViewQueue
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
					m.reviewFromView = tuiViewTasks
					m.currentView = tuiViewPrompt
					m.promptScroll = 0
					m.promptFromQueue = false
					return m, nil
				}
				// No prompt yet, go straight to log view
				return m.openLogView(job.ID, job.Status, tuiViewTasks)
			case job.HasViewableOutput():
				m.selectedJobID = job.ID
				m.reviewFromView = tuiViewTasks
				return m, m.fetchReview(job.ID)
			case job.Status == storage.JobStatusFailed:
				return m.openLogView(job.ID, job.Status, tuiViewTasks)
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
				m.flashView = tuiViewTasks
				return m, nil
			}
			return m.openLogView(job.ID, job.Status, tuiViewTasks)
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
	case "r":
		// Refresh
		return m, m.fetchFixJobs()
	case "?":
		m.fixShowHelp = !m.fixShowHelp
		return m, nil
	}
	return m, nil
}

// handlePatchKey handles key input in the patch viewer.
func (m tuiModel) handlePatchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = tuiViewTasks
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
func (m *tuiModel) closeFixPanel() {
	m.reviewFixPanelOpen = false
	m.reviewFixPanelFocused = false
	m.reviewFixPanelPending = false
	m.fixPromptText = ""
	m.fixPromptJobID = 0
}

// handleConnectionError tracks consecutive connection errors and triggers reconnection.
func (m *tuiModel) handleConnectionError(err error) tea.Cmd {
	if isConnectionError(err) {
		m.consecutiveErrors++
		if m.consecutiveErrors >= 3 && !m.reconnecting {
			m.reconnecting = true
			return m.tryReconnect()
		}
	}
	return nil
}
