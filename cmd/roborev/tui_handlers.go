package main

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

// handleKeyMsg dispatches key events to view-specific handlers.
func (m tuiModel) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Modal views that capture most keys for typing
	switch m.currentView {
	case tuiViewComment:
		return m.handleCommentKey(msg)
	case tuiViewFilter:
		return m.handleFilterKey(msg)
	case tuiViewBranchFilter:
		return m.handleBranchFilterKey(msg)
	case tuiViewTail:
		return m.handleTailKey(msg)
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

// handleFilterKey handles key input in the repo filter modal.
func (m tuiModel) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = tuiViewQueue
		m.filterSearch = ""
		return m, nil
	case "up", "k":
		m.filterNavigateUp()
		return m, nil
	case "down", "j":
		m.filterNavigateDown()
		return m, nil
	case "enter":
		selected := m.getSelectedFilterRepo()
		if selected != nil {
			if len(selected.rootPaths) == 0 {
				m.activeRepoFilter = nil
				m.removeFilterFromStack(filterTypeRepo)
			} else {
				m.activeRepoFilter = selected.rootPaths
				m.pushFilter(filterTypeRepo)
			}
			m.currentView = tuiViewQueue
			m.filterSearch = ""
			m.selectedIdx = -1
			m.selectedJobID = 0
			m.fetchSeq++
			m.loadingJobs = true
			return m, m.fetchJobs()
		}
		return m, nil
	case "backspace":
		if len(m.filterSearch) > 0 {
			runes := []rune(m.filterSearch)
			m.filterSearch = string(runes[:len(runes)-1])
			m.filterSelectedIdx = 0
		}
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			for _, r := range msg.Runes {
				if unicode.IsPrint(r) && !unicode.IsControl(r) {
					m.filterSearch += string(r)
					m.filterSelectedIdx = 0
				}
			}
		}
		return m, nil
	}
}

// handleBranchFilterKey handles key input in the branch filter modal.
func (m tuiModel) handleBranchFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = tuiViewQueue
		m.branchFilterSearch = ""
		return m, nil
	case "up", "k":
		m.branchFilterNavigateUp()
		return m, nil
	case "down", "j":
		m.branchFilterNavigateDown()
		return m, nil
	case "enter":
		selected := m.getSelectedFilterBranch()
		if selected != nil {
			if selected.name == "" {
				m.activeBranchFilter = ""
				m.removeFilterFromStack(filterTypeBranch)
			} else {
				m.activeBranchFilter = selected.name
				m.pushFilter(filterTypeBranch)
			}
			m.currentView = tuiViewQueue
			m.branchFilterSearch = ""
			m.hasMore = false
			m.selectedIdx = -1
			m.selectedJobID = 0
			m.fetchSeq++
			m.loadingJobs = true
			return m, m.fetchJobs()
		}
		return m, nil
	case "backspace":
		if len(m.branchFilterSearch) > 0 {
			runes := []rune(m.branchFilterSearch)
			m.branchFilterSearch = string(runes[:len(runes)-1])
			m.branchFilterSelectedIdx = 0
		}
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			for _, r := range msg.Runes {
				if unicode.IsPrint(r) && !unicode.IsControl(r) {
					m.branchFilterSearch += string(r)
					m.branchFilterSelectedIdx = 0
				}
			}
		}
		return m, nil
	}
}

// handleTailKey handles key input in the tail view.
func (m tuiModel) handleTailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.currentView = m.tailFromView
		m.tailStreaming = false
		return m, nil
	case "x":
		if m.tailJobID > 0 && m.tailStreaming {
			for i := range m.jobs {
				if m.jobs[i].ID == m.tailJobID {
					job := &m.jobs[i]
					if job.Status == storage.JobStatusRunning {
						oldStatus := job.Status
						oldFinishedAt := job.FinishedAt
						job.Status = storage.JobStatusCanceled
						now := time.Now()
						job.FinishedAt = &now
						m.tailStreaming = false
						return m, m.cancelJob(job.ID, oldStatus, oldFinishedAt)
					}
					break
				}
			}
		}
		return m, nil
	case "up", "k":
		m.tailFollow = false
		if m.tailScroll > 0 {
			m.tailScroll--
		}
		return m, nil
	case "down", "j":
		m.tailScroll++
		return m, nil
	case "pgup":
		m.tailFollow = false
		visibleLines := m.height - 4
		if visibleLines < 1 {
			visibleLines = 1
		}
		m.tailScroll -= visibleLines
		if m.tailScroll < 0 {
			m.tailScroll = 0
		}
		return m, tea.ClearScreen
	case "pgdown":
		visibleLines := m.height - 4
		if visibleLines < 1 {
			visibleLines = 1
		}
		m.tailScroll += visibleLines
		return m, tea.ClearScreen
	case "home":
		m.tailFollow = false
		m.tailScroll = 0
		return m, nil
	case "end":
		m.tailFollow = true
		visibleLines := m.height - 4
		if visibleLines < 1 {
			visibleLines = 1
		}
		maxScroll := len(m.tailLines) - visibleLines
		if maxScroll < 0 {
			maxScroll = 0
		}
		m.tailScroll = maxScroll
		return m, nil
	case "g", "G":
		visibleLines := m.height - 4
		if visibleLines < 1 {
			visibleLines = 1
		}
		maxScroll := len(m.tailLines) - visibleLines
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.tailScroll == 0 {
			m.tailFollow = true
			m.tailScroll = maxScroll
		} else {
			m.tailFollow = false
			m.tailScroll = 0
		}
		return m, tea.ClearScreen
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
	case "k", "right":
		return m.handlePrevKey()
	case "down":
		return m.handleDownKey()
	case "j", "left":
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
	case "t":
		return m.handleTailKey2()
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
	}
	return m, nil
}

func (m tuiModel) handleQuitKey() (tea.Model, tea.Cmd) {
	if m.currentView == tuiViewReview {
		m.currentView = tuiViewQueue
		m.currentReview = nil
		m.reviewScroll = 0
		m.paginateNav = 0
		m.normalizeSelectionIfHidden()
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
			m.selectedIdx = prevIdx
			m.updateSelectedJobID()
			m.reviewScroll = 0
			job := m.jobs[prevIdx]
			if job.Status == storage.JobStatusDone {
				return m, m.fetchReview(job.ID)
			} else if job.Status == storage.JobStatusFailed {
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
	case tuiViewPrompt:
		m.promptScroll++
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
			m.selectedIdx = nextIdx
			m.updateSelectedJobID()
			m.reviewScroll = 0
			job := m.jobs[nextIdx]
			if job.Status == storage.JobStatusDone {
				return m, m.fetchReview(job.ID)
			} else if job.Status == storage.JobStatusFailed {
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
	}
	return m, nil
}

func (m tuiModel) handlePageUpKey() (tea.Model, tea.Cmd) {
	pageSize := max(1, m.height-10)
	switch m.currentView {
	case tuiViewQueue:
		for i := 0; i < pageSize; i++ {
			prevIdx := m.findPrevVisibleJob(m.selectedIdx)
			if prevIdx < 0 {
				break
			}
			m.selectedIdx = prevIdx
		}
		m.updateSelectedJobID()
	case tuiViewReview:
		m.reviewScroll = max(0, m.reviewScroll-pageSize)
		return m, tea.ClearScreen
	case tuiViewPrompt:
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
		for i := 0; i < pageSize; i++ {
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
		return m, tea.ClearScreen
	case tuiViewPrompt:
		m.promptScroll += pageSize
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
	if job.Status == storage.JobStatusDone {
		return m, m.fetchReview(job.ID)
	} else if job.Status == storage.JobStatusFailed {
		m.currentBranch = ""
		m.currentReview = &storage.Review{
			Agent:  job.Agent,
			Output: "Job failed:\n\n" + job.Error,
			Job:    &job,
		}
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

func (m tuiModel) handleTailKey2() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue || len(m.jobs) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) {
		return m, nil
	}
	job := m.jobs[m.selectedIdx]
	if job.Status == storage.JobStatusRunning {
		m.tailJobID = job.ID
		m.tailLines = nil
		m.tailScroll = 0
		m.tailStreaming = true
		m.tailFollow = true
		m.tailFromView = tuiViewQueue
		m.currentView = tuiViewTail
		return m, tea.Batch(tea.ClearScreen, m.fetchTailOutput(job.ID))
	} else if job.Status == storage.JobStatusQueued {
		m.flashMessage = "Job is queued - not yet running"
		m.flashExpiresAt = time.Now().Add(2 * time.Second)
		m.flashView = tuiViewQueue
	}
	return m, nil
}

func (m tuiModel) handleFilterOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue {
		return m, nil
	}
	m.filterRepos = nil
	m.filterSelectedIdx = 0
	m.filterSearch = ""
	m.currentView = tuiViewFilter
	return m, m.fetchRepos()
}

func (m tuiModel) handleBranchFilterOpenKey() (tea.Model, tea.Cmd) {
	if m.currentView != tuiViewQueue {
		return m, nil
	}
	m.filterBranches = nil
	m.branchFilterSelectedIdx = 0
	m.branchFilterSearch = ""
	m.currentView = tuiViewBranchFilter
	return m, m.fetchBranches()
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
			m.commentCommit = job.GitRef
			if len(m.commentCommit) > 7 {
				m.commentCommit = m.commentCommit[:7]
			}
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
			m.commentCommit = m.currentReview.Job.GitRef
			if len(m.commentCommit) > 7 {
				m.commentCommit = m.commentCommit[:7]
			}
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
	if m.currentView == tuiViewQueue || m.currentView == tuiViewReview || m.currentView == tuiViewPrompt || m.currentView == tuiViewTail {
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
		m.currentView = tuiViewQueue
		m.currentReview = nil
		m.reviewScroll = 0
		m.paginateNav = 0
		m.normalizeSelectionIfHidden()
		if m.hideAddressed && !m.loadingJobs {
			m.loadingJobs = true
			return m, m.fetchJobs()
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
	if !msg.append {
		m.jobStats = msg.stats
	}

	m.updateDisplayNameCache(msg.jobs)

	if msg.append {
		m.jobs = append(m.jobs, msg.jobs...)
	} else {
		m.jobs = msg.jobs
	}

	// Clear pending addressed states that server has confirmed
	for jobID, pending := range m.pendingAddressed {
		for i := range m.jobs {
			if m.jobs[i].ID == jobID {
				serverState := m.jobs[i].Addressed != nil && *m.jobs[i].Addressed
				if serverState == pending.newState {
					delete(m.pendingAddressed, jobID)
				}
				break
			}
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
		if nav == tuiViewReview {
			nextIdx := m.findNextViewableJob()
			if nextIdx >= 0 {
				m.selectedIdx = nextIdx
				m.updateSelectedJobID()
				m.reviewScroll = 0
				job := m.jobs[nextIdx]
				if job.Status == storage.JobStatusDone {
					return m, m.fetchReview(job.ID)
				} else if job.Status == storage.JobStatusFailed {
					m.currentBranch = ""
					m.currentReview = &storage.Review{
						Agent:  job.Agent,
						Output: "Job failed:\n\n" + job.Error,
						Job:    &job,
					}
				}
			}
		} else if nav == tuiViewPrompt {
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
		m.loadingJobs = true
		return m, tea.Batch(m.fetchJobs(), m.fetchStatus())
	}
	return m, nil
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
