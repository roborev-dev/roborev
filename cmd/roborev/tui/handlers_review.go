package tui

import (
	"fmt"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

func (m model) handlePromptKey() (tea.Model, tea.Cmd) {
	if job, ok := m.selectedJob(); m.currentView == viewQueue && ok {
		if job.Status == storage.JobStatusDone {
			m.promptFromQueue = true
			return m, m.fetchReviewForPrompt(job.ID)
		} else if job.Status == storage.JobStatusRunning && job.Prompt != "" {
			m.currentReview = &storage.Review{
				Agent:  job.Agent,
				Prompt: job.Prompt,
				Job:    job,
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
	} else if job, ok := m.selectedJob(); m.currentView == viewQueue && ok {
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
	job, ok := m.selectedJob()
	if m.currentView != viewQueue || !ok {
		return m, nil
	}
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
	job, ok := m.selectedJob()
	if m.currentView != viewQueue || !ok {
		return m, nil
	}
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

	job, ok := m.selectedJob()
	if m.currentView != viewQueue || !ok {
		return m, nil
	}
	switch job.Status {
	case storage.JobStatusQueued:
		m.setFlash("Job is queued - not yet running", 2*time.Second, viewQueue)
		return m, nil
	default:
		return m.openLogView(job.ID, job.Status, viewQueue)
	}
}

func (m model) handleCommentOpenKey() (tea.Model, tea.Cmd) {
	if job, ok := m.selectedJob(); m.currentView == viewQueue && ok {
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
	} else if job, ok := m.selectedJob(); m.currentView == viewQueue && ok {
		if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed {
			return m, m.fetchReviewAndCopy(job.ID, job)
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
		m.setFlash(fmt.Sprintf("Job #%d is %s — no review to copy", job.ID, status), 2*time.Second, viewQueue)
		return m, nil
	}
	return m, nil
}

func (m model) handleCommitMsgKey() (tea.Model, tea.Cmd) {
	if job, ok := m.selectedJob(); m.currentView == viewQueue && ok {
		m.commitMsgFromView = m.currentView
		m.commitMsgJobID = job.ID
		m.commitMsgContent = ""
		m.commitMsgScroll = 0
		return m, m.fetchCommitMsg(job)
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
	} else if sel, ok := m.selectedJob(); ok {
		job = *sel
	} else {
		return m, nil
	}

	// Only allow fix on completed review jobs (not fix jobs —
	// fix-of-fix chains are not supported).
	if job.IsFixJob() {
		m.setFlash("Cannot fix a fix job", 2*time.Second, m.currentView)
		return m, nil
	}
	if job.Status != storage.JobStatusDone {
		m.setFlash("Can only fix completed reviews", 2*time.Second, m.currentView)
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

// closeFixPanel resets all inline fix panel state. Call this when
// leaving review view or navigating to a different review.
func (m *model) closeFixPanel() {
	m.reviewFixPanelOpen = false
	m.reviewFixPanelFocused = false
	m.reviewFixPanelPending = false
	m.fixPromptText = ""
	m.fixPromptJobID = 0
}
