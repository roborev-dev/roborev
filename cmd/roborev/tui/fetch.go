package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/streamfmt"
	"github.com/roborev-dev/roborev/internal/update"
)

func (m model) tick() tea.Cmd {
	return tea.Tick(m.tickInterval(), func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// tickInterval returns the appropriate polling interval based on queue activity.
// Uses faster polling when jobs are running or pending, slower when idle.
func (m model) tickInterval() time.Duration {
	// Before first status fetch, use active interval to be responsive on startup
	if !m.statusFetchedOnce {
		return tickIntervalActive
	}
	// Poll frequently when there's activity
	if m.status.RunningJobs > 0 || m.status.QueuedJobs > 0 {
		return tickIntervalActive
	}
	return tickIntervalIdle
}

func (m model) fetchJobs() tea.Cmd {
	// Fetch enough to fill the visible area plus a buffer for smooth scrolling.
	// Use minimum of 100 only before first WindowSizeMsg (when height is default 24)
	visibleRows := m.queueVisibleRows() + queuePrefetchBuffer
	if !m.heightDetected {
		visibleRows = max(100, visibleRows)
	}
	currentJobCount := len(m.jobs)
	seq := m.fetchSeq

	return func() tea.Msg {
		// Build URL with server-side filters where possible, falling back to
		// limit=0 (no pagination) only when client-side filtering is required.
		params := neturl.Values{}

		// Repo filter: single repo can use API filter; multiple repos need client-side
		needsAllJobs := false
		if len(m.activeRepoFilter) == 1 {
			params.Set("repo", m.activeRepoFilter[0])
		} else if len(m.activeRepoFilter) > 1 {
			needsAllJobs = true // Multiple repos (shared display name) - filter client-side
		}

		// Branch filter: use server-side for real branch names.
		// branchNone is a client-side sentinel for empty/NULL branches and can't be
		// sent to the server, so it falls through to client-side filtering.
		if m.activeBranchFilter != "" && m.activeBranchFilter != branchNone {
			params.Set("branch", m.activeBranchFilter)
		} else if m.activeBranchFilter == branchNone {
			needsAllJobs = true
		}

		// Addressed filter: use server-side to avoid fetching all jobs.
		// Skip for client-side filtered views (needsAllJobs) so we get
		// all jobs for accurate client-side metrics counting.
		if m.hideAddressed && !needsAllJobs {
			params.Set("addressed", "false")
		}

		// Exclude fix jobs — they belong in the Tasks view, not the queue
		params.Set("exclude_job_type", "fix")

		// Set limit: use pagination unless we need client-side filtering (multi-repo)
		if needsAllJobs {
			params.Set("limit", "0")
		} else {
			limit := max(currentJobCount,
				// Maintain paginated view on refresh
				visibleRows)
			params.Set("limit", fmt.Sprintf("%d", limit))
		}

		url := fmt.Sprintf("%s/api/jobs?%s", m.serverAddr, params.Encode())
		resp, err := m.client.Get(url)
		if err != nil {
			return jobsErrMsg{err: err, seq: seq}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return jobsErrMsg{err: fmt.Errorf("fetch jobs: %s", resp.Status), seq: seq}
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
			Stats   storage.JobStats    `json:"stats"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return jobsErrMsg{err: err, seq: seq}
		}
		return jobsMsg{jobs: result.Jobs, hasMore: result.HasMore, append: false, seq: seq, stats: result.Stats}
	}
}

func (m model) fetchMoreJobs() tea.Cmd {
	seq := m.fetchSeq
	return func() tea.Msg {
		// Only fetch more when not doing client-side filtering that loads all jobs
		if len(m.activeRepoFilter) > 1 || m.activeBranchFilter == branchNone {
			return nil // Multi-repo or "(none)" branch filter loads everything
		}
		offset := len(m.jobs)
		params := neturl.Values{}
		params.Set("limit", "50")
		params.Set("offset", fmt.Sprintf("%d", offset))
		if len(m.activeRepoFilter) == 1 {
			params.Set("repo", m.activeRepoFilter[0])
		}
		if m.activeBranchFilter != "" && m.activeBranchFilter != branchNone {
			params.Set("branch", m.activeBranchFilter)
		}
		if m.hideAddressed {
			params.Set("addressed", "false")
		}
		params.Set("exclude_job_type", "fix")
		url := fmt.Sprintf("%s/api/jobs?%s", m.serverAddr, params.Encode())
		resp, err := m.client.Get(url)
		if err != nil {
			return paginationErrMsg{err: err, seq: seq}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return paginationErrMsg{err: fmt.Errorf("fetch more jobs: %s", resp.Status), seq: seq}
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return paginationErrMsg{err: err, seq: seq}
		}
		return jobsMsg{jobs: result.Jobs, hasMore: result.HasMore, append: true, seq: seq}
	}
}

func (m model) fetchStatus() tea.Cmd {
	return func() tea.Msg {
		var status storage.DaemonStatus
		if err := m.getJSON("/api/status", &status); err != nil {
			return errMsg(err)
		}
		return statusMsg(status)
	}
}

func (m model) checkForUpdate() tea.Cmd {
	return func() tea.Msg {
		info, err := update.CheckForUpdate(false) // Use cache
		if err != nil || info == nil {
			return updateCheckMsg{} // No update or error
		}
		return updateCheckMsg{version: info.LatestVersion, isDevBuild: info.IsDevBuild}
	}
}

// tryReconnect attempts to find a running daemon at a new address.
// This is called after consecutive connection failures to handle daemon restarts.
func (m model) tryReconnect() tea.Cmd {
	return func() tea.Msg {
		info, err := daemon.GetAnyRunningDaemon()
		if err != nil {
			return reconnectMsg{err: err}
		}
		newAddr := fmt.Sprintf("http://%s", info.Addr)
		return reconnectMsg{newAddr: newAddr, version: info.Version}
	}
}

func (m model) fetchRepos() tea.Cmd {
	// Capture values for use in goroutine
	client := m.client
	serverAddr := m.serverAddr
	activeBranchFilter := m.activeBranchFilter // Constrain repos by active branch filter

	return func() tea.Msg {
		// Build URL with optional branch filter (URL-encoded)
		// Skip sending branch for branchNone sentinel - it's a client-side filter
		reposURL := serverAddr + "/api/repos"
		if activeBranchFilter != "" && activeBranchFilter != branchNone {
			params := neturl.Values{}
			params.Set("branch", activeBranchFilter)
			reposURL += "?" + params.Encode()
		}

		resp, err := client.Get(reposURL)
		if err != nil {
			return errMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return errMsg(fmt.Errorf("fetch repos: %s", resp.Status))
		}

		var reposResult struct {
			Repos []struct {
				Name     string `json:"name"`
				RootPath string `json:"root_path"`
				Count    int    `json:"count"`
			} `json:"repos"`
			TotalCount int `json:"total_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&reposResult); err != nil {
			return errMsg(err)
		}

		// Aggregate repos by display name
		displayNameMap := make(map[string]*repoFilterItem)
		var displayNameOrder []string // Preserve order for stable display
		for _, r := range reposResult.Repos {
			displayName := config.GetDisplayName(r.RootPath)
			if displayName == "" {
				displayName = r.Name
			}
			if item, ok := displayNameMap[displayName]; ok {
				item.rootPaths = append(item.rootPaths, r.RootPath)
				item.count += r.Count
			} else {
				displayNameMap[displayName] = &repoFilterItem{
					name:      displayName,
					rootPaths: []string{r.RootPath},
					count:     r.Count,
				}
				displayNameOrder = append(displayNameOrder, displayName)
			}
		}
		repos := make([]repoFilterItem, len(displayNameOrder))
		for i, name := range displayNameOrder {
			repos[i] = *displayNameMap[name]
		}
		return reposMsg{repos: repos}
	}
}

// fetchBranchesForRepo fetches branches for a specific repo in the tree filter.
// Returns repoBranchesMsg with the branch data (or err set on failure).
// When expand is true, the handler sets expanded=true on the tree node.
// searchSeq is the search generation at dispatch time; the error handler
// uses it to avoid marking fetchFailed for stale search sessions.
func (m model) fetchBranchesForRepo(
	rootPaths []string, repoIdx int, expand bool, searchSeq int,
) tea.Cmd {
	client := m.client
	serverAddr := m.serverAddr

	errMsg := func(err error) repoBranchesMsg {
		return repoBranchesMsg{
			repoIdx:      repoIdx,
			rootPaths:    rootPaths,
			err:          err,
			expandOnLoad: expand,
			searchSeq:    searchSeq,
		}
	}

	return func() tea.Msg {
		branchURL := serverAddr + "/api/branches"
		if len(rootPaths) > 0 {
			params := neturl.Values{}
			for _, repoPath := range rootPaths {
				if repoPath != "" {
					params.Add("repo", repoPath)
				}
			}
			if len(params) > 0 {
				branchURL += "?" + params.Encode()
			}
		}

		resp, err := client.Get(branchURL)
		if err != nil {
			return errMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return errMsg(
				fmt.Errorf("fetch branches for repo: %s", resp.Status),
			)
		}

		var branchResult struct {
			Branches []struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			} `json:"branches"`
			TotalCount int `json:"total_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&branchResult); err != nil {
			return errMsg(err)
		}

		branches := make([]branchFilterItem, len(branchResult.Branches))
		for i, b := range branchResult.Branches {
			branches[i] = branchFilterItem{
				name:  b.Name,
				count: b.Count,
			}
		}

		return repoBranchesMsg{
			repoIdx:      repoIdx,
			rootPaths:    rootPaths,
			branches:     branches,
			expandOnLoad: expand,
			searchSeq:    searchSeq,
		}
	}
}

func (m model) backfillBranches() tea.Cmd {
	// Capture values for use in goroutine
	machineID := m.status.MachineID
	client := m.client
	serverAddr := m.serverAddr

	return func() tea.Msg {
		var backfillCount int

		// First, check if there are any NULL branches via the API
		resp, err := client.Get(serverAddr + "/api/branches")
		if err != nil {
			return errMsg(err)
		}
		var checkResult struct {
			NullsRemaining int `json:"nulls_remaining"`
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return errMsg(fmt.Errorf("check branches for backfill: %s", resp.Status))
		}
		if err := json.NewDecoder(resp.Body).Decode(&checkResult); err != nil {
			resp.Body.Close()
			return errMsg(fmt.Errorf("decode branches response: %w", err))
		}
		resp.Body.Close()

		// If there are NULL branches, fetch all jobs to backfill
		if checkResult.NullsRemaining > 0 {
			resp, err := client.Get(serverAddr + "/api/jobs")
			if err != nil {
				return errMsg(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return errMsg(fmt.Errorf("fetch jobs for backfill: %s", resp.Status))
			}

			var jobsResult struct {
				Jobs []storage.ReviewJob `json:"jobs"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&jobsResult); err != nil {
				return errMsg(err)
			}

			// Find jobs that need backfill
			type backfillJob struct {
				id     int64
				branch string
			}
			var toBackfill []backfillJob

			for _, job := range jobsResult.Jobs {
				if job.Branch != "" {
					continue // Already has branch
				}
				// Mark task jobs (run, analyze, custom) or dirty jobs with no-branch sentinel
				if job.IsTaskJob() || job.IsDirtyJob() {
					toBackfill = append(toBackfill, backfillJob{id: job.ID, branch: branchNone})
					continue
				}
				// Mark remote jobs with no-branch sentinel (can't look up)
				if job.RepoPath == "" || (machineID != "" && job.SourceMachineID != "" && job.SourceMachineID != machineID) {
					toBackfill = append(toBackfill, backfillJob{id: job.ID, branch: branchNone})
					continue
				}

				sha := job.GitRef
				if idx := strings.Index(sha, ".."); idx != -1 {
					sha = sha[idx+2:]
				}
				branch := git.GetBranchName(job.RepoPath, sha)
				if branch == "" {
					branch = branchNone // Mark as attempted but not found
				}
				toBackfill = append(toBackfill, backfillJob{id: job.ID, branch: branch})
			}

			// Persist to database
			for _, bf := range toBackfill {
				reqBody, _ := json.Marshal(map[string]any{
					"job_id": bf.id,
					"branch": bf.branch,
				})
				resp, err := client.Post(serverAddr+"/api/job/update-branch", "application/json", bytes.NewReader(reqBody))
				if err == nil {
					if resp.StatusCode == http.StatusOK {
						var updateResult struct {
							Updated bool `json:"updated"`
						}
						if json.NewDecoder(resp.Body).Decode(&updateResult) == nil && updateResult.Updated {
							backfillCount++
						}
					}
					resp.Body.Close()
				}
			}
		}

		return branchesMsg{backfillCount: backfillCount}
	}
}

// loadReview fetches a review from the server by job ID.
// Used by fetchReview, fetchReviewForPrompt, and fetchReviewAndCopy.
func (m model) loadReview(jobID int64) (*storage.Review, error) {
	var review storage.Review
	if err := m.getJSON(fmt.Sprintf("/api/review?job_id=%d", jobID), &review); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("no review found")
		}
		return nil, fmt.Errorf("fetch review: %w", err)
	}
	return &review, nil
}

// loadResponses fetches responses for a job, merging legacy SHA-based responses.
func (m model) loadResponses(jobID int64, review *storage.Review) []storage.Response {
	var responses []storage.Response

	// Fetch responses by job ID
	var jobResult struct {
		Responses []storage.Response `json:"responses"`
	}
	if err := m.getJSON(fmt.Sprintf("/api/comments?job_id=%d", jobID), &jobResult); err == nil {
		responses = jobResult.Responses
	}

	// Also fetch legacy responses by SHA for single commits (not ranges or dirty reviews)
	// and merge with job responses to preserve full history during migration
	if review.Job != nil && !strings.Contains(review.Job.GitRef, "..") && review.Job.GitRef != "dirty" {
		var shaResult struct {
			Responses []storage.Response `json:"responses"`
		}
		if err := m.getJSON(fmt.Sprintf("/api/comments?sha=%s", review.Job.GitRef), &shaResult); err == nil {
			// Merge and dedupe by ID
			seen := make(map[int64]bool)
			for _, r := range responses {
				seen[r.ID] = true
			}
			for _, r := range shaResult.Responses {
				if !seen[r.ID] {
					seen[r.ID] = true
					responses = append(responses, r)
				}
			}
			// Sort merged responses by CreatedAt for chronological order
			sort.Slice(responses, func(i, j int) bool {
				return responses[i].CreatedAt.Before(responses[j].CreatedAt)
			})
		}
	}

	return responses
}

func (m model) fetchReview(jobID int64) tea.Cmd {
	return func() tea.Msg {
		review, err := m.loadReview(jobID)
		if err != nil {
			return errMsg(err)
		}

		responses := m.loadResponses(jobID, review)

		// Compute branch name for single commits (not ranges)
		var branchName string
		if review.Job != nil && review.Job.RepoPath != "" && !strings.Contains(review.Job.GitRef, "..") {
			branchName = git.GetBranchName(review.Job.RepoPath, review.Job.GitRef)
		}

		return reviewMsg{review: review, responses: responses, jobID: jobID, branchName: branchName}
	}
}

func (m model) fetchReviewForPrompt(jobID int64) tea.Cmd {
	return func() tea.Msg {
		review, err := m.loadReview(jobID)
		if err != nil {
			return errMsg(err)
		}
		return promptMsg{review: review, jobID: jobID}
	}
}

// fetchJobLog fetches raw JSONL from /api/job/log, renders it
// through streamFormatter, and returns pre-styled logLines.
// Uses incremental fetching: only new bytes since logOffset are
// downloaded and rendered, reusing the persistent logFmtr state.
func (m model) fetchJobLog(jobID int64) tea.Cmd {
	addr := m.serverAddr
	width := m.width
	client := m.client
	style := m.glamourStyle
	offset := m.logOffset
	fmtr := m.logFmtr
	seq := m.logFetchSeq
	return func() tea.Msg {
		url := fmt.Sprintf(
			"%s/api/job/log?job_id=%d&offset=%d",
			addr, jobID, offset,
		)
		resp, err := client.Get(url)
		if err != nil {
			return logOutputMsg{err: err, seq: seq}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return logOutputMsg{err: errNoLog, seq: seq}
		}
		if resp.StatusCode != http.StatusOK {
			return logOutputMsg{
				err: fmt.Errorf("fetch log: %s", resp.Status),
				seq: seq,
			}
		}

		// Determine if job is still running from header
		jobStatus := resp.Header.Get("X-Job-Status")
		hasMore := jobStatus == "running"

		// Parse new offset from response header
		newOffset := offset
		if v := resp.Header.Get("X-Log-Offset"); v != "" {
			if parsed, perr := strconv.ParseInt(
				v, 10, 64,
			); perr == nil {
				newOffset = parsed
			}
		}

		// Server reset offset (log truncated/rotated) — force
		// full replace even if we sent a nonzero offset.
		isIncremental := offset > 0 && fmtr != nil
		if newOffset < offset {
			isIncremental = false
		}

		// No new data — return early with current state
		if newOffset == offset && isIncremental {
			return logOutputMsg{
				hasMore:   hasMore,
				newOffset: newOffset,
				append:    true,
				seq:       seq,
			}
		}

		// Render JSONL through streamFormatter. Use pre-computed
		// glamour style to avoid terminal queries from goroutine.
		var buf bytes.Buffer
		var renderFmtr *streamfmt.Formatter
		if isIncremental {
			// Reuse persistent formatter — redirect its output
			// to a fresh buffer for this batch only.
			fmtr.SetWriter(&buf)
			renderFmtr = fmtr
		} else {
			renderFmtr = streamfmt.NewWithWidth(
				&buf, width, style,
			)
		}

		if err := streamfmt.RenderLogWith(
			resp.Body, renderFmtr, &buf,
		); err != nil {
			return logOutputMsg{err: err, seq: seq}
		}

		// Split rendered output into lines
		raw := buf.String()
		var lines []logLine
		if raw != "" {
			for s := range strings.SplitSeq(raw, "\n") {
				lines = append(lines, logLine{text: s})
			}
			// Remove trailing empty line from final newline
			if len(lines) > 0 &&
				lines[len(lines)-1].text == "" {
				lines = lines[:len(lines)-1]
			}
		}

		return logOutputMsg{
			lines:     lines,
			hasMore:   hasMore,
			newOffset: newOffset,
			append:    isIncremental,
			seq:       seq,
			fmtr:      renderFmtr,
		}
	}
}

func (m model) fetchReviewAndCopy(jobID int64, job *storage.ReviewJob) tea.Cmd {
	view := m.currentView // Capture view at trigger time
	return func() tea.Msg {
		review, err := m.loadReview(jobID)
		if err != nil {
			return clipboardResultMsg{err: err, view: view}
		}

		if review.Output == "" {
			return clipboardResultMsg{err: fmt.Errorf("review has no content"), view: view}
		}

		// Attach job info if not already present (for header formatting)
		if review.Job == nil && job != nil {
			review.Job = job
		}

		content := formatClipboardContent(review)
		err = m.clipboard.WriteText(content)
		return clipboardResultMsg{err: err, view: view}
	}
}

// fetchCommitMsg fetches commit message(s) for a job.
// For single commits, returns the commit message.
// For ranges, returns all commit messages in the range.
// For dirty reviews or prompt jobs, returns an error.
func (m model) fetchCommitMsg(job *storage.ReviewJob) tea.Cmd {
	jobID := job.ID
	return func() tea.Msg {
		// Handle task jobs first (run, analyze, custom labels)
		// Check this before dirty to handle backward compatibility with older run jobs
		if job.IsTaskJob() {
			return commitMsgMsg{
				jobID: jobID,
				err:   fmt.Errorf("no commit message for task jobs"),
			}
		}

		// Handle dirty reviews (uncommitted changes)
		if job.DiffContent != nil || job.IsDirtyJob() {
			return commitMsgMsg{
				jobID: jobID,
				err:   fmt.Errorf("no commit message for uncommitted changes"),
			}
		}

		// Handle missing GitRef (could be from incomplete job data or older versions)
		if job.GitRef == "" {
			return commitMsgMsg{
				jobID: jobID,
				err:   fmt.Errorf("no git reference available for this job"),
			}
		}

		// Check if this is a range (contains "..")
		if strings.Contains(job.GitRef, "..") {
			// Fetch all commits in range
			commits, err := git.GetRangeCommits(job.RepoPath, job.GitRef)
			if err != nil {
				return commitMsgMsg{jobID: jobID, err: err}
			}
			if len(commits) == 0 {
				return commitMsgMsg{
					jobID: jobID,
					err:   fmt.Errorf("no commits in range %s", job.GitRef),
				}
			}

			// Fetch info for each commit
			var content strings.Builder
			fmt.Fprintf(&content, "Commits in %s (%d commits):\n\n", job.GitRef, len(commits))

			for i, sha := range commits {
				info, err := git.GetCommitInfo(job.RepoPath, sha)
				if err != nil {
					fmt.Fprintf(&content, "%d. %s: (error: %v)\n\n", i+1, git.ShortSHA(sha), err)
					continue
				}
				fmt.Fprintf(&content, "%d. %s %s\n", i+1, git.ShortSHA(info.SHA), info.Subject)
				fmt.Fprintf(&content, "   Author: %s | %s\n", info.Author, info.Timestamp.Format("2006-01-02 15:04"))
				if info.Body != "" {
					// Indent body
					bodyLines := strings.SplitSeq(info.Body, "\n")
					for line := range bodyLines {
						content.WriteString("   " + line + "\n")
					}
				}
				content.WriteString("\n")
			}

			return commitMsgMsg{jobID: jobID, content: sanitizeForDisplay(content.String())}
		}

		// Single commit
		info, err := git.GetCommitInfo(job.RepoPath, job.GitRef)
		if err != nil {
			return commitMsgMsg{jobID: jobID, err: err}
		}

		var content strings.Builder
		fmt.Fprintf(&content, "Commit: %s\n", info.SHA)
		fmt.Fprintf(&content, "Author: %s\n", info.Author)
		fmt.Fprintf(&content, "Date:   %s\n\n", info.Timestamp.Format("2006-01-02 15:04:05 -0700"))
		content.WriteString(info.Subject + "\n")
		if info.Body != "" {
			content.WriteString("\n" + info.Body + "\n")
		}

		return commitMsgMsg{jobID: jobID, content: sanitizeForDisplay(content.String())}
	}
}
func (m model) fetchPatch(jobID int64) tea.Cmd {
	return func() tea.Msg {
		url := m.serverAddr + fmt.Sprintf("/api/job/patch?job_id=%d", jobID)
		resp, err := m.client.Get(url)
		if err != nil {
			return patchMsg{jobID: jobID, err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return patchMsg{jobID: jobID, err: fmt.Errorf("no patch available (HTTP %d)", resp.StatusCode)}
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return patchMsg{jobID: jobID, err: err}
		}
		return patchMsg{jobID: jobID, patch: string(data)}
	}
}
func (m model) fetchJobByID(jobID int64) (*storage.ReviewJob, error) {
	var result struct {
		Jobs []storage.ReviewJob `json:"jobs"`
	}
	if err := m.getJSON(fmt.Sprintf("/api/jobs?id=%d", jobID), &result); err != nil {
		return nil, err
	}
	for i := range result.Jobs {
		if result.Jobs[i].ID == jobID {
			return &result.Jobs[i], nil
		}
	}
	return nil, fmt.Errorf("job %d not found", jobID)
}

// fetchFixJobs fetches fix jobs from the daemon.
func (m model) fetchFixJobs() tea.Cmd {
	return func() tea.Msg {
		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		err := m.getJSON("/api/jobs?job_type=fix&limit=200", &result)
		if err != nil {
			return fixJobsMsg{err: err}
		}
		return fixJobsMsg{jobs: result.Jobs}
	}
}
