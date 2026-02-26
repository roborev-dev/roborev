package tui

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/streamfmt"
	"github.com/roborev-dev/roborev/internal/version"
)

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
		m.setFlash("Config reloaded", 5*time.Second, m.currentView)
	}
	m.lastConfigReloadCounter = m.status.ConfigReloadCounter
	m.statusFetchedOnce = true
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
		m.setFlash(fmt.Sprintf(
			"Backfilled branch info for %d jobs",
			msg.backfillCount,
		), 5*time.Second, viewFilter)
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
			m.setFlash(flash, 5*time.Second, m.logFromView)
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

// handleCommitMsgMsg processes commit message fetch results.
func (m model) handleCommitMsgMsg(
	msg commitMsgMsg,
) (tea.Model, tea.Cmd) {
	if msg.jobID != m.commitMsgJobID {
		return m, nil
	}
	if msg.err != nil {
		m.setFlash(msg.err.Error(), 2*time.Second, m.currentView)
		return m, nil
	}
	m.commitMsgContent = msg.content
	m.commitMsgScroll = 0
	m.currentView = viewCommitMsg
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
		m.setFlash("Copied to clipboard", 2*time.Second, msg.view)
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

// handleFixTriggerResultMsg processes fix job trigger results.
func (m model) handleFixTriggerResultMsg(
	msg fixTriggerResultMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.setFlash(fmt.Sprintf(
			"Fix failed: %v", msg.err,
		), 3*time.Second, viewTasks)
	} else if msg.warning != "" {
		m.setFlash(msg.warning, 5*time.Second, viewTasks)
		return m, m.fetchFixJobs()
	} else {
		m.setFlash(fmt.Sprintf(
			"Fix job #%d enqueued", msg.job.ID,
		), 3*time.Second, viewTasks)
		return m, m.fetchFixJobs()
	}
	return m, nil
}

// handlePatchResultMsg processes patch fetch results.
func (m model) handlePatchResultMsg(
	msg patchMsg,
) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setFlash(fmt.Sprintf(
			"Patch fetch failed: %v", msg.err,
		), 3*time.Second, viewTasks)
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
		m.setFlash(fmt.Sprintf(
			"Patch for job #%d doesn't apply cleanly"+
				" - triggering rebase", msg.jobID,
		), 5*time.Second, viewTasks)
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
		m.setFlash(detail, 8*time.Second, viewTasks)
	} else if msg.err != nil {
		m.setFlash(fmt.Sprintf(
			"Apply failed: %v", msg.err,
		), 3*time.Second, viewTasks)
	} else {
		m.setFlash(fmt.Sprintf(
			"Patch from job #%d applied and committed",
			msg.jobID,
		), 3*time.Second, viewTasks)
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
