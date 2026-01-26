package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"github.com/atotto/clipboard"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/update"
	"github.com/roborev-dev/roborev/internal/version"
)

// Tick intervals for adaptive polling
const (
	tickIntervalActive = 2 * time.Second  // Poll frequently when jobs are running/pending
	tickIntervalIdle   = 10 * time.Second // Poll less when queue is idle
)

// TUI styles using AdaptiveColor for light/dark terminal support.
// Light colors are chosen for dark-on-light terminals; Dark colors for light-on-dark.
var (
	tuiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "125", Dark: "205"}) // Magenta/Pink

	tuiStatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}) // Gray

	tuiSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "127", Dark: "212"}) // Magenta/Pink

	tuiQueuedStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "226"}) // Yellow/Gold
	tuiRunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "25", Dark: "33"})   // Blue
	tuiDoneStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"})   // Green
	tuiFailedStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}) // Red
	tuiCanceledStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "166", Dark: "208"}) // Orange

	tuiPassStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"})   // Green
	tuiFailStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}) // Red
	tuiAddressedStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "30", Dark: "51"})   // Cyan

	tuiHelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}) // Gray
)

// fullSHAPattern matches a 40-character hex git SHA (not ranges or branch names)
var fullSHAPattern = regexp.MustCompile(`(?i)^[0-9a-f]{40}$`)

// ansiEscapePattern matches ANSI escape sequences (colors, cursor movement, etc.)
// Handles CSI sequences (\x1b[...X) and OSC sequences terminated by BEL (\x07) or ST (\x1b\\)
var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\]([^\x07\x1b]|\x1b[^\\])*(\x07|\x1b\\)`)

// sanitizeForDisplay strips ANSI escape sequences and control characters from text
// to prevent terminal injection when displaying untrusted content (e.g., commit messages).
func sanitizeForDisplay(s string) string {
	// Strip ANSI escape sequences
	s = ansiEscapePattern.ReplaceAllString(s, "")
	// Strip control characters except newline (\n) and tab (\t)
	var result strings.Builder
	result.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			result.WriteRune(r)
		}
	}
	return result.String()
}

type tuiView int

const (
	tuiViewQueue tuiView = iota
	tuiViewReview
	tuiViewPrompt
	tuiViewFilter
	tuiViewBranchFilter
	tuiViewComment
	tuiViewCommitMsg
	tuiViewHelp
	tuiViewTail
)

// repoFilterItem represents a repo (or group of repos with same display name) in the filter modal
type repoFilterItem struct {
	name      string   // Display name. Empty string means "All repos"
	rootPaths []string // Repo paths that share this display name. Empty for "All repos"
	count     int
}

// branchFilterItem represents a branch in the filter modal
type branchFilterItem struct {
	name  string // Branch name. Empty string means "All branches"
	count int
}

type tuiModel struct {
	serverAddr    string
	daemonVersion string
	client        *http.Client
	jobs            []storage.ReviewJob
	status          storage.DaemonStatus
	selectedIdx     int
	selectedJobID   int64 // Track selected job by ID to maintain position on refresh
	currentView     tuiView
	currentReview      *storage.Review
	currentResponses   []storage.Response // Responses for current review (fetched with review)
	currentBranch      string             // Cached branch name for current review (computed on load)
	reviewScroll       int
	promptScroll    int
	promptFromQueue bool // true if prompt view was entered from queue (not review)
	width           int
	height          int
	err               error
	updateAvailable   string // Latest version if update available, empty if up to date
	updateIsDevBuild  bool   // True if running a dev build
	versionMismatch   bool   // True if daemon version doesn't match TUI version

	// Pagination state
	hasMore        bool // true if there are more jobs to load
	loadingMore    bool // true if currently loading more jobs (pagination)
	loadingJobs    bool // true if currently loading jobs (full refresh)
	pendingRefetch bool // true if filter changed while loading, needs refetch when done
	heightDetected bool // true after first WindowSizeMsg (real terminal height known)

	// Repo filter modal state
	filterRepos       []repoFilterItem // Available repos with counts
	filterSelectedIdx int              // Currently highlighted repo in filter list
	filterSearch      string           // Search/filter text typed by user

	// Branch filter modal state
	filterBranches          []branchFilterItem // Available branches with counts
	branchFilterSelectedIdx int                // Currently highlighted branch in filter list
	branchFilterSearch      string             // Search/filter text typed by user

	// Comment modal state
	commentText     string  // The response text being typed
	commentJobID    int64   // Job ID we're responding to
	commentCommit   string  // Short commit SHA for display
	commentFromView tuiView // View to return to after comment modal closes

	// Active filter (applied to queue view)
	activeRepoFilter   []string // Empty = show all, otherwise repo root_paths to filter by
	activeBranchFilter string   // Empty = show all, otherwise branch name to filter by
	filterStack        []string // Order of applied filters: "repo", "branch" - for escape to pop in order
	hideAddressed      bool     // When true, hide jobs with addressed reviews

	// Display name cache (keyed by repo path)
	displayNames map[string]string

	// Branch name cache (keyed by job ID) - caches derived branches to avoid repeated git calls
	branchNames map[int64]string

	// Track if branch backfill has run this session (one-time migration)
	branchBackfillDone bool

	// Pending addressed state changes (prevents flash during refresh race)
	// Each pending entry stores the requested state and a sequence number to
	// distinguish between multiple requests for the same state (e.g., true→false→true)
	pendingAddressed       map[int64]pendingState // job ID -> pending state

	// Flash message (temporary status message shown briefly)
	flashMessage   string
	flashExpiresAt time.Time
	flashView      tuiView // View where flash was triggered (only show in same view)

	// Track config reload notifications
	lastConfigReloadCounter uint64 // Last known ConfigReloadCounter from daemon status
	statusFetchedOnce       bool   // True after first successful status fetch (for flash logic)
	pendingReviewAddressed map[int64]pendingState // review ID -> pending state (for reviews without jobs)
	addressedSeq           uint64                 // monotonic counter for request sequencing

	// Daemon reconnection state
	consecutiveErrors int  // Count of consecutive connection failures
	reconnecting      bool // True if currently attempting reconnection

	// Commit message view state
	commitMsgContent  string  // Formatted commit message(s) content
	commitMsgScroll   int     // Scroll position in commit message view
	commitMsgJobID    int64   // Job ID for the commit message being viewed
	commitMsgFromView tuiView // View to return to after closing commit message view

	// Help view state
	helpFromView tuiView // View to return to after closing help

	// Tail view state
	tailJobID     int64      // Job being tailed
	tailLines     []tailLine // Buffer of output lines
	tailScroll    int        // Scroll position
	tailStreaming bool       // True if job is still running
	tailFromView  tuiView    // View to return to
	tailFollow    bool       // True if auto-scrolling to bottom (follow mode)
}

// pendingState tracks a pending addressed toggle with sequence number
type pendingState struct {
	newState bool
	seq      uint64
}

// tailLine represents a single line of agent output in the tail view
type tailLine struct {
	timestamp time.Time
	text      string
	lineType  string // "text", "tool", "thinking", "error"
}

// tuiTailOutputMsg delivers output lines from the daemon
type tuiTailOutputMsg struct {
	lines   []tailLine
	hasMore bool // true if job is still running
	err     error
}

// tuiTailTickMsg triggers a refresh of the tail output
type tuiTailTickMsg struct{}

type tuiTickMsg time.Time
type tuiJobsMsg struct {
	jobs    []storage.ReviewJob
	hasMore bool
	append  bool // true to append to existing jobs, false to replace
}
type tuiStatusMsg storage.DaemonStatus
type tuiReviewMsg struct {
	review     *storage.Review
	responses  []storage.Response // Responses for this review
	jobID      int64              // The job ID that was requested (for race condition detection)
	branchName string             // Pre-computed branch name (empty if not applicable)
}
type tuiPromptMsg *storage.Review
type tuiAddressedMsg bool
type tuiAddressedResultMsg struct {
	jobID      int64  // job ID for queue view rollback
	reviewID   int64  // review ID for review view rollback
	reviewView bool   // true if from review view (rollback currentReview)
	oldState   bool
	newState   bool   // the requested state (for pendingAddressed validation)
	seq        uint64 // request sequence number (for distinguishing same-state rapid toggles)
	err        error
}
type tuiCancelResultMsg struct {
	jobID         int64
	oldState      storage.JobStatus
	oldFinishedAt *time.Time
	err           error
}
type tuiRerunResultMsg struct {
	jobID         int64
	oldState      storage.JobStatus
	oldStartedAt  *time.Time
	oldFinishedAt *time.Time
	oldError      string
	err           error
}
type tuiErrMsg error
type tuiJobsErrMsg struct{ err error }       // Job fetch error (clears loadingJobs)
type tuiPaginationErrMsg struct{ err error } // Pagination-specific error (clears loadingMore)
type tuiUpdateCheckMsg struct {
	version    string // Latest version if available, empty if up to date
	isDevBuild bool   // True if running a dev build
}
type tuiReposMsg struct {
	repos      []repoFilterItem
	totalCount int
}
type tuiBranchesMsg struct {
	branches       []branchFilterItem
	totalCount     int
	backfillCount  int // Number of branches successfully backfilled to the database
	nullsRemaining int // Number of jobs still without branch info (for backfill gating)
}
type tuiCommentResultMsg struct {
	jobID int64
	err   error
}
type tuiClipboardResultMsg struct {
	err  error
	view tuiView // The view where copy was triggered (for flash attribution)
}
type tuiCommitMsgMsg struct {
	jobID   int64
	content string
	err     error
}
type tuiReconnectMsg struct {
	newAddr string // New daemon address if found, empty if not found
	version string // Daemon version (to avoid sync call in Update)
	err     error
}

// ClipboardWriter is an interface for clipboard operations (allows mocking in tests)
type ClipboardWriter interface {
	WriteText(text string) error
}

// realClipboard implements ClipboardWriter using the system clipboard
type realClipboard struct{}

func (r *realClipboard) WriteText(text string) error {
	return clipboard.WriteAll(text)
}

// clipboardWriter is the clipboard implementation used by the TUI (can be overridden for tests)
var clipboardWriter ClipboardWriter = &realClipboard{}

// isConnectionError checks if an error indicates a network/connection failure
// (as opposed to an application-level error like 404 or invalid response).
// Only connection errors should trigger reconnection attempts.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	// Check for url.Error (wraps network errors from http.Client)
	var urlErr *neturl.Error
	if errors.As(err, &urlErr) {
		return true
	}
	// Check for net.Error (timeout, connection refused, etc.)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

func newTuiModel(serverAddr string) tuiModel {
	// Read daemon version from runtime file (authoritative source)
	daemonVersion := "?"
	if info, err := daemon.GetAnyRunningDaemon(); err == nil && info.Version != "" {
		daemonVersion = info.Version
	}
	return tuiModel{
		serverAddr:             serverAddr,
		daemonVersion:          daemonVersion,
		client:                 &http.Client{Timeout: 10 * time.Second},
		jobs:                   []storage.ReviewJob{},
		currentView:            tuiViewQueue,
		width:                  80, // sensible defaults until we get WindowSizeMsg
		height:                 24,
		loadingJobs:            true,                           // Init() calls fetchJobs, so mark as loading
		displayNames:           make(map[string]string),        // Cache display names to avoid disk reads on render
		branchNames:            make(map[int64]string),         // Cache derived branch names to avoid git calls on render
		pendingAddressed:       make(map[int64]pendingState),   // Track pending addressed changes (by job ID)
		pendingReviewAddressed: make(map[int64]pendingState),   // Track pending addressed changes (by review ID)
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(
		tea.WindowSize(), // request initial window size
		m.tick(),
		m.fetchJobs(),
		m.fetchStatus(),
		m.checkForUpdate(),
	)
}

// getDisplayName returns the display name for a repo, using the cache.
// Falls back to loading from config on cache miss, then to the provided default name.
func (m *tuiModel) getDisplayName(repoPath, defaultName string) string {
	if repoPath == "" {
		return defaultName
	}
	if displayName, ok := m.displayNames[repoPath]; ok {
		if displayName != "" {
			return displayName
		}
		return defaultName
	}
	// Cache miss - load from config (handles reviews for repos not in jobs list)
	displayName := config.GetDisplayName(repoPath)
	m.displayNames[repoPath] = displayName
	if displayName != "" {
		return displayName
	}
	return defaultName
}

// updateDisplayNameCache refreshes display names for the given repo paths.
// Called on each jobs fetch to pick up config changes without restart.
func (m *tuiModel) updateDisplayNameCache(jobs []storage.ReviewJob) {
	for _, job := range jobs {
		if job.RepoPath == "" {
			continue
		}
		// Always refresh to pick up config changes
		m.displayNames[job.RepoPath] = config.GetDisplayName(job.RepoPath)
	}
}

// getBranchForJob returns the branch name for a job, falling back to git lookup
// if the stored branch is empty and the repo is available locally.
// Results are cached to avoid repeated git calls on render.
func (m *tuiModel) getBranchForJob(job storage.ReviewJob) string {
	// Use stored branch if available
	if job.Branch != "" {
		return job.Branch
	}

	// Check cache for previously derived branch (if cache exists)
	if m.branchNames != nil {
		if cached, ok := m.branchNames[job.ID]; ok {
			return cached
		}
	}

	// For dirty or prompt jobs, no branch makes sense
	if job.GitRef == "dirty" || job.GitRef == "run" || job.GitRef == "prompt" {
		return ""
	}

	// Fall back to git lookup if repo path exists locally and we have a SHA
	// Only try if repo path is set and is not from a remote machine
	if job.RepoPath == "" || (m.status.MachineID != "" && job.SourceMachineID != "" && job.SourceMachineID != m.status.MachineID) {
		// Don't cache - repo might become available later
		return ""
	}

	// Check if repo exists locally before attempting git lookup
	// Return early on any error (not exists, permission denied, I/O failure)
	// to avoid caching incorrect results
	if _, err := os.Stat(job.RepoPath); err != nil {
		return ""
	}

	// For ranges (SHA..SHA), use the end SHA
	sha := job.GitRef
	if idx := strings.Index(sha, ".."); idx != -1 {
		sha = sha[idx+2:]
	}

	branch := git.GetBranchName(job.RepoPath, sha)
	// Cache result (including empty for detached HEAD / commit not on branch)
	// We only skip caching above when repo doesn't exist yet
	if m.branchNames != nil {
		m.branchNames[job.ID] = branch
	}
	return branch
}

func (m tuiModel) tick() tea.Cmd {
	return tea.Tick(m.tickInterval(), func(t time.Time) tea.Msg {
		return tuiTickMsg(t)
	})
}

// tickInterval returns the appropriate polling interval based on queue activity.
// Uses faster polling when jobs are running or pending, slower when idle.
func (m tuiModel) tickInterval() time.Duration {
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

func (m tuiModel) fetchJobs() tea.Cmd {
	// Calculate limit based on terminal height - fetch enough to fill the visible area
	// Reserve 9 lines for header/footer, add buffer for safety
	// Use minimum of 100 only before first WindowSizeMsg (when height is default 24)
	visibleRows := m.height - 9 + 10
	if !m.heightDetected {
		visibleRows = max(100, visibleRows)
	}
	currentJobCount := len(m.jobs)

	return func() tea.Msg {
		// Determine limit:
		// - No limit (limit=0) when filtering to show full repo/addressed history
		// - If we've paginated beyond visible area, maintain current view size
		// - Otherwise fetch enough to fill visible area
		var url string
		if len(m.activeRepoFilter) == 1 {
			// Single repo filter - use API filter
			url = fmt.Sprintf("%s/api/jobs?limit=0&repo=%s", m.serverAddr, neturl.QueryEscape(m.activeRepoFilter[0]))
		} else if len(m.activeRepoFilter) > 1 {
			// Multiple repos (shared display name) - fetch all, filter client-side
			url = fmt.Sprintf("%s/api/jobs?limit=0", m.serverAddr)
		} else if m.hideAddressed {
			// Fetch all jobs when hiding addressed - client-side filtering needs full dataset
			url = fmt.Sprintf("%s/api/jobs?limit=0", m.serverAddr)
		} else {
			limit := visibleRows
			if currentJobCount > visibleRows {
				limit = currentJobCount // Maintain paginated view on refresh
			}
			url = fmt.Sprintf("%s/api/jobs?limit=%d", m.serverAddr, limit)
		}
		resp, err := m.client.Get(url)
		if err != nil {
			return tuiJobsErrMsg{err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiJobsErrMsg{err: fmt.Errorf("fetch jobs: %s", resp.Status)}
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return tuiJobsErrMsg{err: err}
		}
		return tuiJobsMsg{jobs: result.Jobs, hasMore: result.HasMore, append: false}
	}
}

func (m tuiModel) fetchMoreJobs() tea.Cmd {
	return func() tea.Msg {
		// Only fetch more when not filtering (filtered view loads all)
		if len(m.activeRepoFilter) > 0 {
			return nil
		}
		offset := len(m.jobs)
		url := fmt.Sprintf("%s/api/jobs?limit=50&offset=%d", m.serverAddr, offset)
		resp, err := m.client.Get(url)
		if err != nil {
			return tuiPaginationErrMsg{err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiPaginationErrMsg{err: fmt.Errorf("fetch more jobs: %s", resp.Status)}
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return tuiPaginationErrMsg{err: err}
		}
		return tuiJobsMsg{jobs: result.Jobs, hasMore: result.HasMore, append: true}
	}
}

func (m tuiModel) fetchStatus() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Get(m.serverAddr + "/api/status")
		if err != nil {
			return tuiErrMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiErrMsg(fmt.Errorf("fetch status: %s", resp.Status))
		}

		var status storage.DaemonStatus
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			return tuiErrMsg(err)
		}
		return tuiStatusMsg(status)
	}
}

func (m tuiModel) checkForUpdate() tea.Cmd {
	return func() tea.Msg {
		info, err := update.CheckForUpdate(false) // Use cache
		if err != nil || info == nil {
			return tuiUpdateCheckMsg{} // No update or error
		}
		return tuiUpdateCheckMsg{version: info.LatestVersion, isDevBuild: info.IsDevBuild}
	}
}

// tryReconnect attempts to find a running daemon at a new address.
// This is called after consecutive connection failures to handle daemon restarts.
func (m tuiModel) tryReconnect() tea.Cmd {
	return func() tea.Msg {
		info, err := daemon.GetAnyRunningDaemon()
		if err != nil {
			return tuiReconnectMsg{err: err}
		}
		newAddr := fmt.Sprintf("http://%s", info.Addr)
		return tuiReconnectMsg{newAddr: newAddr, version: info.Version}
	}
}

func (m tuiModel) fetchRepos() tea.Cmd {
	// Capture values for use in goroutine
	client := m.client
	serverAddr := m.serverAddr
	activeBranchFilter := m.activeBranchFilter // Constrain repos by active branch filter

	return func() tea.Msg {
		// Build URL with optional branch filter (URL-encoded)
		reposURL := serverAddr + "/api/repos"
		if activeBranchFilter != "" {
			params := neturl.Values{}
			params.Set("branch", activeBranchFilter)
			reposURL += "?" + params.Encode()
		}

		resp, err := client.Get(reposURL)
		if err != nil {
			return tuiErrMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiErrMsg(fmt.Errorf("fetch repos: %s", resp.Status))
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
			return tuiErrMsg(err)
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
		return tuiReposMsg{repos: repos, totalCount: reposResult.TotalCount}
	}
}

func (m tuiModel) fetchBranches() tea.Cmd {
	// Capture values for use in goroutine
	machineID := m.status.MachineID
	client := m.client
	serverAddr := m.serverAddr
	backfillDone := m.branchBackfillDone
	activeRepoFilter := m.activeRepoFilter // Constrain branches by active repo filter

	return func() tea.Msg {
		var backfillCount int

		// Check if backfill is needed (only if not already done this session)
		if !backfillDone {
			// First, check if there are any NULL branches via the API
			resp, err := client.Get(serverAddr + "/api/branches")
			if err != nil {
				return tuiErrMsg(err)
			}
			var checkResult struct {
				NullsRemaining int `json:"nulls_remaining"`
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				return tuiErrMsg(fmt.Errorf("check branches for backfill: %s", resp.Status))
			}
			if err := json.NewDecoder(resp.Body).Decode(&checkResult); err != nil {
				resp.Body.Close()
				return tuiErrMsg(fmt.Errorf("decode branches response: %w", err))
			}
			resp.Body.Close()

			// If there are NULL branches, fetch all jobs to backfill
			if checkResult.NullsRemaining > 0 {
				resp, err := client.Get(serverAddr + "/api/jobs")
				if err != nil {
					return tuiErrMsg(err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					return tuiErrMsg(fmt.Errorf("fetch jobs for backfill: %s", resp.Status))
				}

				var jobsResult struct {
					Jobs []storage.ReviewJob `json:"jobs"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&jobsResult); err != nil {
					return tuiErrMsg(err)
				}

				// Find jobs that need backfill
				type backfillJob struct {
					id     int64
					branch string
				}
				var toBackfill []backfillJob

				for _, job := range jobsResult.Jobs {
					if job.Branch != "" {
						continue // Already has branch (including "(none)" sentinel)
					}
					// Mark dirty/prompt jobs with "(none)" sentinel
					if job.GitRef == "dirty" || job.GitRef == "run" || job.GitRef == "prompt" {
						toBackfill = append(toBackfill, backfillJob{id: job.ID, branch: "(none)"})
						continue
					}
					// Mark remote jobs with "(none)" sentinel (can't look up)
					if job.RepoPath == "" || (machineID != "" && job.SourceMachineID != "" && job.SourceMachineID != machineID) {
						toBackfill = append(toBackfill, backfillJob{id: job.ID, branch: "(none)"})
						continue
					}

					sha := job.GitRef
					if idx := strings.Index(sha, ".."); idx != -1 {
						sha = sha[idx+2:]
					}
					branch := git.GetBranchName(job.RepoPath, sha)
					if branch == "" {
						branch = "(none)" // Mark as attempted but not found
					}
					toBackfill = append(toBackfill, backfillJob{id: job.ID, branch: branch})
				}

				// Persist to database
				for _, bf := range toBackfill {
					reqBody, _ := json.Marshal(map[string]interface{}{
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
		}

		// Now fetch branches from server with optional repo filter
		branchURL := serverAddr + "/api/branches"
		if len(activeRepoFilter) > 0 {
			params := neturl.Values{}
			for _, repoPath := range activeRepoFilter {
				if repoPath != "" { // Skip empty paths
					params.Add("repo", repoPath)
				}
			}
			if len(params) > 0 {
				branchURL += "?" + params.Encode()
			}
		}

		resp, err := client.Get(branchURL)
		if err != nil {
			return tuiErrMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiErrMsg(fmt.Errorf("fetch branches: %s", resp.Status))
		}

		var branchResult struct {
			Branches []struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			} `json:"branches"`
			TotalCount     int `json:"total_count"`
			NullsRemaining int `json:"nulls_remaining"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&branchResult); err != nil {
			return tuiErrMsg(err)
		}

		// Convert to branchFilterItem
		branches := make([]branchFilterItem, len(branchResult.Branches))
		for i, b := range branchResult.Branches {
			branches[i] = branchFilterItem{
				name:  b.Name,
				count: b.Count,
			}
		}

		return tuiBranchesMsg{
			branches:       branches,
			totalCount:     branchResult.TotalCount,
			backfillCount:  backfillCount,
			nullsRemaining: branchResult.NullsRemaining,
		}
	}
}

func (m tuiModel) fetchReview(jobID int64) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Get(fmt.Sprintf("%s/api/review?job_id=%d", m.serverAddr, jobID))
		if err != nil {
			return tuiErrMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiErrMsg(fmt.Errorf("no review found"))
		}
		if resp.StatusCode != http.StatusOK {
			return tuiErrMsg(fmt.Errorf("fetch review: %s", resp.Status))
		}

		var review storage.Review
		if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
			return tuiErrMsg(err)
		}

		// Fetch responses for this job
		var responses []storage.Response
		respResp, err := m.client.Get(fmt.Sprintf("%s/api/comments?job_id=%d", m.serverAddr, jobID))
		if err == nil {
			defer respResp.Body.Close()
			if respResp.StatusCode == http.StatusOK {
				var result struct {
					Responses []storage.Response `json:"responses"`
				}
				json.NewDecoder(respResp.Body).Decode(&result)
				responses = result.Responses
			}
		}

		// Also fetch legacy responses by SHA for single commits (not ranges or dirty reviews)
		// and merge with job responses to preserve full history during migration
		if review.Job != nil && !strings.Contains(review.Job.GitRef, "..") && review.Job.GitRef != "dirty" {
			shaResp, err := m.client.Get(fmt.Sprintf("%s/api/comments?sha=%s", m.serverAddr, review.Job.GitRef))
			if err == nil {
				defer shaResp.Body.Close()
				if shaResp.StatusCode == http.StatusOK {
					var result struct {
						Responses []storage.Response `json:"responses"`
					}
					json.NewDecoder(shaResp.Body).Decode(&result)
					// Merge and dedupe by ID
					seen := make(map[int64]bool)
					for _, r := range responses {
						seen[r.ID] = true
					}
					for _, r := range result.Responses {
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
		}

		// Compute branch name for single commits (not ranges)
		var branchName string
		if review.Job != nil && review.Job.RepoPath != "" && !strings.Contains(review.Job.GitRef, "..") {
			branchName = git.GetBranchName(review.Job.RepoPath, review.Job.GitRef)
		}

		return tuiReviewMsg{review: &review, responses: responses, jobID: jobID, branchName: branchName}
	}
}

func (m tuiModel) fetchReviewForPrompt(jobID int64) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Get(fmt.Sprintf("%s/api/review?job_id=%d", m.serverAddr, jobID))
		if err != nil {
			return tuiErrMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiErrMsg(fmt.Errorf("no review found"))
		}
		if resp.StatusCode != http.StatusOK {
			return tuiErrMsg(fmt.Errorf("fetch review: %s", resp.Status))
		}

		var review storage.Review
		if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
			return tuiErrMsg(err)
		}
		return tuiPromptMsg(&review)
	}
}

func (m tuiModel) fetchTailOutput(jobID int64) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Get(fmt.Sprintf("%s/api/job/output?job_id=%d", m.serverAddr, jobID))
		if err != nil {
			return tuiTailOutputMsg{err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiTailOutputMsg{err: fmt.Errorf("fetch output: %s", resp.Status)}
		}

		var result struct {
			JobID   int64  `json:"job_id"`
			Status  string `json:"status"`
			Lines   []struct {
				TS       string `json:"ts"`
				Text     string `json:"text"`
				LineType string `json:"line_type"`
			} `json:"lines"`
			HasMore bool `json:"has_more"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return tuiTailOutputMsg{err: err}
		}

		lines := make([]tailLine, len(result.Lines))
		for i, l := range result.Lines {
			ts, err := time.Parse(time.RFC3339Nano, l.TS)
			if err != nil {
				// Fallback to current time if timestamp is invalid
				ts = time.Now()
			}
			lines[i] = tailLine{timestamp: ts, text: l.Text, lineType: l.LineType}
		}

		return tuiTailOutputMsg{lines: lines, hasMore: result.HasMore}
	}
}

// formatClipboardContent prepares review content for clipboard with a header line
func formatClipboardContent(review *storage.Review) string {
	if review == nil || review.Output == "" {
		return ""
	}

	// Build header: "Review #ID /repo/path abc1234"
	// Always use job ID for consistency with queue and review screen display
	// ID priority: Job.ID → JobID → review.ID
	var id int64
	if review.Job != nil && review.Job.ID != 0 {
		id = review.Job.ID
	} else if review.JobID != 0 {
		id = review.JobID
	} else {
		id = review.ID
	}

	var header string
	if id != 0 {
		if review.Job != nil && review.Job.RepoPath != "" {
			// Include repo path and git ref when available
			gitRef := review.Job.GitRef
			// Truncate SHA to 7 chars if it's a full 40-char hex SHA (not a range or branch name)
			if fullSHAPattern.MatchString(gitRef) {
				gitRef = gitRef[:7]
			}
			header = fmt.Sprintf("Review #%d %s %s\n\n", id, review.Job.RepoPath, gitRef)
		} else {
			header = fmt.Sprintf("Review #%d\n\n", id)
		}
	}

	return header + review.Output
}

func (m tuiModel) copyToClipboard(review *storage.Review) tea.Cmd {
	view := m.currentView // Capture view at trigger time
	content := formatClipboardContent(review)
	return func() tea.Msg {
		if content == "" {
			return tuiClipboardResultMsg{err: fmt.Errorf("no content to copy"), view: view}
		}
		err := clipboardWriter.WriteText(content)
		return tuiClipboardResultMsg{err: err, view: view}
	}
}

func (m tuiModel) fetchReviewAndCopy(jobID int64, job *storage.ReviewJob) tea.Cmd {
	view := m.currentView // Capture view at trigger time
	return func() tea.Msg {
		resp, err := m.client.Get(fmt.Sprintf("%s/api/review?job_id=%d", m.serverAddr, jobID))
		if err != nil {
			return tuiClipboardResultMsg{err: err, view: view}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiClipboardResultMsg{err: fmt.Errorf("no review found"), view: view}
		}
		if resp.StatusCode != http.StatusOK {
			return tuiClipboardResultMsg{err: fmt.Errorf("fetch review: %s", resp.Status), view: view}
		}

		var review storage.Review
		if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
			return tuiClipboardResultMsg{err: err, view: view}
		}

		if review.Output == "" {
			return tuiClipboardResultMsg{err: fmt.Errorf("review has no content"), view: view}
		}

		// Attach job info if not already present (for header formatting)
		if review.Job == nil && job != nil {
			review.Job = job
		}

		content := formatClipboardContent(&review)
		err = clipboardWriter.WriteText(content)
		return tuiClipboardResultMsg{err: err, view: view}
	}
}

// fetchCommitMsg fetches commit message(s) for a job.
// For single commits, returns the commit message.
// For ranges, returns all commit messages in the range.
// For dirty reviews or prompt jobs, returns an error.
func (m tuiModel) fetchCommitMsg(job *storage.ReviewJob) tea.Cmd {
	jobID := job.ID
	return func() tea.Msg {
		// Handle prompt/run jobs first (GitRef == "prompt" indicates a run task, not a commit review)
		// Check this before dirty to handle backward compatibility with older run jobs
		if job.GitRef == "prompt" {
			return tuiCommitMsgMsg{
				jobID: jobID,
				err:   fmt.Errorf("no commit message for run tasks"),
			}
		}

		// Handle dirty reviews (uncommitted changes)
		if job.DiffContent != nil || job.GitRef == "dirty" {
			return tuiCommitMsgMsg{
				jobID: jobID,
				err:   fmt.Errorf("no commit message for uncommitted changes"),
			}
		}

		// Handle missing GitRef (could be from incomplete job data or older versions)
		if job.GitRef == "" {
			return tuiCommitMsgMsg{
				jobID: jobID,
				err:   fmt.Errorf("no git reference available for this job"),
			}
		}

		// Check if this is a range (contains "..")
		if strings.Contains(job.GitRef, "..") {
			// Fetch all commits in range
			commits, err := git.GetRangeCommits(job.RepoPath, job.GitRef)
			if err != nil {
				return tuiCommitMsgMsg{jobID: jobID, err: err}
			}
			if len(commits) == 0 {
				return tuiCommitMsgMsg{
					jobID: jobID,
					err:   fmt.Errorf("no commits in range %s", job.GitRef),
				}
			}

			// Fetch info for each commit
			var content strings.Builder
			content.WriteString(fmt.Sprintf("Commits in %s (%d commits):\n\n", job.GitRef, len(commits)))

			for i, sha := range commits {
				info, err := git.GetCommitInfo(job.RepoPath, sha)
				if err != nil {
					content.WriteString(fmt.Sprintf("%d. %s: (error: %v)\n\n", i+1, sha[:7], err))
					continue
				}
				content.WriteString(fmt.Sprintf("%d. %s %s\n", i+1, info.SHA[:7], info.Subject))
				content.WriteString(fmt.Sprintf("   Author: %s | %s\n", info.Author, info.Timestamp.Format("2006-01-02 15:04")))
				if info.Body != "" {
					// Indent body
					bodyLines := strings.Split(info.Body, "\n")
					for _, line := range bodyLines {
						content.WriteString("   " + line + "\n")
					}
				}
				content.WriteString("\n")
			}

			return tuiCommitMsgMsg{jobID: jobID, content: sanitizeForDisplay(content.String())}
		}

		// Single commit
		info, err := git.GetCommitInfo(job.RepoPath, job.GitRef)
		if err != nil {
			return tuiCommitMsgMsg{jobID: jobID, err: err}
		}

		var content strings.Builder
		content.WriteString(fmt.Sprintf("Commit: %s\n", info.SHA))
		content.WriteString(fmt.Sprintf("Author: %s\n", info.Author))
		content.WriteString(fmt.Sprintf("Date:   %s\n\n", info.Timestamp.Format("2006-01-02 15:04:05 -0700")))
		content.WriteString(info.Subject + "\n")
		if info.Body != "" {
			content.WriteString("\n" + info.Body + "\n")
		}

		return tuiCommitMsgMsg{jobID: jobID, content: sanitizeForDisplay(content.String())}
	}
}

func (m tuiModel) addressReview(reviewID, jobID int64, newState, oldState bool, seq uint64) tea.Cmd {
	return func() tea.Msg {
		reqBody, err := json.Marshal(map[string]interface{}{
			"job_id":    jobID,
			"addressed": newState,
		})
		if err != nil {
			return tuiAddressedResultMsg{reviewID: reviewID, jobID: jobID, reviewView: true, oldState: oldState, newState: newState, seq: seq, err: err}
		}
		resp, err := m.client.Post(m.serverAddr+"/api/review/address", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			return tuiAddressedResultMsg{reviewID: reviewID, jobID: jobID, reviewView: true, oldState: oldState, newState: newState, seq: seq, err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiAddressedResultMsg{reviewID: reviewID, jobID: jobID, reviewView: true, oldState: oldState, newState: newState, seq: seq, err: fmt.Errorf("review not found")}
		}
		if resp.StatusCode != http.StatusOK {
			return tuiAddressedResultMsg{reviewID: reviewID, jobID: jobID, reviewView: true, oldState: oldState, newState: newState, seq: seq, err: fmt.Errorf("mark review: %s", resp.Status)}
		}
		return tuiAddressedResultMsg{reviewID: reviewID, jobID: jobID, reviewView: true, oldState: oldState, newState: newState, seq: seq, err: nil}
	}
}

// addressReviewInBackground updates addressed status by job ID.
// Used for optimistic updates from queue view - UI already updated, this syncs to server.
// On error, returns tuiAddressedResultMsg with oldState for rollback.
func (m tuiModel) addressReviewInBackground(jobID int64, newState, oldState bool, seq uint64) tea.Cmd {
	return func() tea.Msg {
		reqBody, err := json.Marshal(map[string]interface{}{
			"job_id":    jobID,
			"addressed": newState,
		})
		if err != nil {
			return tuiAddressedResultMsg{jobID: jobID, oldState: oldState, newState: newState, seq: seq, err: err}
		}
		resp, err := m.client.Post(m.serverAddr+"/api/review/address", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			return tuiAddressedResultMsg{jobID: jobID, oldState: oldState, newState: newState, seq: seq, err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiAddressedResultMsg{jobID: jobID, oldState: oldState, newState: newState, seq: seq, err: fmt.Errorf("no review for this job")}
		}
		if resp.StatusCode != http.StatusOK {
			return tuiAddressedResultMsg{jobID: jobID, oldState: oldState, newState: newState, seq: seq, err: fmt.Errorf("mark review: %s", resp.Status)}
		}
		return tuiAddressedResultMsg{jobID: jobID, oldState: oldState, newState: newState, seq: seq, err: nil}
	}
}

func (m tuiModel) toggleAddressedForJob(jobID int64, currentState *bool) tea.Cmd {
	return func() tea.Msg {
		// Toggle the state
		newState := true
		if currentState != nil && *currentState {
			newState = false
		}

		reqBody, err := json.Marshal(map[string]interface{}{
			"job_id":    jobID,
			"addressed": newState,
		})
		if err != nil {
			return tuiErrMsg(err)
		}
		resp, err := m.client.Post(m.serverAddr+"/api/review/address", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			return tuiErrMsg(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiErrMsg(fmt.Errorf("no review for this job"))
		}
		if resp.StatusCode != http.StatusOK {
			return tuiErrMsg(fmt.Errorf("mark review: %s", resp.Status))
		}
		return tuiAddressedMsg(newState)
	}
}

// updateSelectedJobID updates the tracked job ID after navigation
func (m *tuiModel) updateSelectedJobID() {
	if m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
		m.selectedJobID = m.jobs[m.selectedIdx].ID
	}
}

// setJobAddressed updates the addressed state for a job by ID.
// Handles nil pointer by allocating if necessary.
func (m *tuiModel) setJobAddressed(jobID int64, state bool) {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			if m.jobs[i].Addressed == nil {
				m.jobs[i].Addressed = new(bool)
			}
			*m.jobs[i].Addressed = state
			return
		}
	}
}

// setJobStatus updates the status for a job by ID
func (m *tuiModel) setJobStatus(jobID int64, status storage.JobStatus) {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Status = status
			return
		}
	}
}

// setJobFinishedAt updates the FinishedAt for a job by ID
func (m *tuiModel) setJobFinishedAt(jobID int64, finishedAt *time.Time) {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].FinishedAt = finishedAt
			return
		}
	}
}

// setJobStartedAt updates the StartedAt for a job by ID
func (m *tuiModel) setJobStartedAt(jobID int64, startedAt *time.Time) {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].StartedAt = startedAt
			return
		}
	}
}

// setJobError updates the Error for a job by ID
func (m *tuiModel) setJobError(jobID int64, errMsg string) {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Error = errMsg
			return
		}
	}
}

// findNextViewableJob finds the next job that can be viewed (done or failed).
// Respects active filters. Returns the index or -1 if none found.
func (m *tuiModel) findNextViewableJob() int {
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
func (m *tuiModel) findPrevViewableJob() int {
	for i := m.selectedIdx - 1; i >= 0; i-- {
		job := m.jobs[i]
		if (job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed) &&
			m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// normalizeSelectionIfHidden adjusts selectedIdx/selectedJobID if the current
// selection is hidden (e.g., marked addressed with hideAddressed filter active).
// Call this when returning to queue view from review view.
func (m *tuiModel) normalizeSelectionIfHidden() {
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

// cancelJob sends a cancel request to the server
func (m tuiModel) cancelJob(jobID int64, oldStatus storage.JobStatus, oldFinishedAt *time.Time) tea.Cmd {
	return func() tea.Msg {
		reqBody, err := json.Marshal(map[string]interface{}{
			"job_id": jobID,
		})
		if err != nil {
			return tuiCancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: err}
		}
		resp, err := m.client.Post(m.serverAddr+"/api/job/cancel", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			return tuiCancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiCancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: fmt.Errorf("job not cancellable")}
		}
		if resp.StatusCode != http.StatusOK {
			return tuiCancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: fmt.Errorf("cancel job: %s", resp.Status)}
		}
		return tuiCancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: nil}
	}
}

// rerunJob sends a rerun request to the server for failed/canceled jobs
func (m tuiModel) rerunJob(jobID int64, oldStatus storage.JobStatus, oldStartedAt, oldFinishedAt *time.Time, oldError string) tea.Cmd {
	return func() tea.Msg {
		reqBody, err := json.Marshal(map[string]interface{}{
			"job_id": jobID,
		})
		if err != nil {
			return tuiRerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: err}
		}
		resp, err := m.client.Post(m.serverAddr+"/api/job/rerun", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			return tuiRerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiRerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: fmt.Errorf("job not rerunnable")}
		}
		if resp.StatusCode != http.StatusOK {
			return tuiRerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: fmt.Errorf("rerun job: %s", resp.Status)}
		}
		return tuiRerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: nil}
	}
}

// getVisibleFilterRepos returns repos that match the current search filter
func (m *tuiModel) getVisibleFilterRepos() []repoFilterItem {
	if m.filterSearch == "" {
		return m.filterRepos
	}
	search := strings.ToLower(m.filterSearch)
	var visible []repoFilterItem
	for _, r := range m.filterRepos {
		// Always include "All repos" option, filter others by search
		if r.name == "" {
			visible = append(visible, r)
			continue
		}
		// Search by display name
		if strings.Contains(strings.ToLower(r.name), search) {
			visible = append(visible, r)
			continue
		}
		// Also search by underlying repo path basenames
		for _, p := range r.rootPaths {
			if strings.Contains(strings.ToLower(filepath.Base(p)), search) {
				visible = append(visible, r)
				break
			}
		}
	}
	return visible
}

// filterNavigateUp moves selection up in the filter modal
func (m *tuiModel) filterNavigateUp() {
	if m.filterSelectedIdx > 0 {
		m.filterSelectedIdx--
	}
}

// filterNavigateDown moves selection down in the filter modal
func (m *tuiModel) filterNavigateDown() {
	visible := m.getVisibleFilterRepos()
	if m.filterSelectedIdx < len(visible)-1 {
		m.filterSelectedIdx++
	}
}

// getSelectedFilterRepo returns the currently selected repo in the filter modal
func (m *tuiModel) getSelectedFilterRepo() *repoFilterItem {
	visible := m.getVisibleFilterRepos()
	if m.filterSelectedIdx >= 0 && m.filterSelectedIdx < len(visible) {
		return &visible[m.filterSelectedIdx]
	}
	return nil
}

// branchFilterNavigateUp moves selection up in the branch filter modal
func (m *tuiModel) branchFilterNavigateUp() {
	if m.branchFilterSelectedIdx > 0 {
		m.branchFilterSelectedIdx--
	}
}

// branchFilterNavigateDown moves selection down in the branch filter modal
func (m *tuiModel) branchFilterNavigateDown() {
	visible := m.getVisibleFilterBranches()
	if m.branchFilterSelectedIdx < len(visible)-1 {
		m.branchFilterSelectedIdx++
	}
}

// getSelectedFilterBranch returns the currently selected branch in the filter modal
func (m *tuiModel) getSelectedFilterBranch() *branchFilterItem {
	visible := m.getVisibleFilterBranches()
	if m.branchFilterSelectedIdx >= 0 && m.branchFilterSelectedIdx < len(visible) {
		return &visible[m.branchFilterSelectedIdx]
	}
	return nil
}

// getVisibleFilterBranches returns branches that match the current search filter
func (m *tuiModel) getVisibleFilterBranches() []branchFilterItem {
	if m.branchFilterSearch == "" {
		return m.filterBranches
	}
	searchLower := strings.ToLower(m.branchFilterSearch)
	var visible []branchFilterItem
	for _, b := range m.filterBranches {
		if b.name == "" || strings.Contains(strings.ToLower(b.name), searchLower) {
			visible = append(visible, b)
		}
	}
	return visible
}

// repoMatchesFilter checks if a repo path matches the active filter
func (m tuiModel) repoMatchesFilter(repoPath string) bool {
	for _, p := range m.activeRepoFilter {
		if p == repoPath {
			return true
		}
	}
	return false
}

// isJobVisible checks if a job passes all active filters
func (m tuiModel) isJobVisible(job storage.ReviewJob) bool {
	if len(m.activeRepoFilter) > 0 && !m.repoMatchesFilter(job.RepoPath) {
		return false
	}
	if m.activeBranchFilter != "" && !m.branchMatchesFilter(job) {
		return false
	}
	if m.hideAddressed {
		// Hide addressed reviews, failed jobs, and canceled jobs
		// Check pendingAddressed first for optimistic updates (avoids flash on filter)
		if pending, ok := m.pendingAddressed[job.ID]; ok {
			if pending.newState {
				return false
			}
		} else if job.Addressed != nil && *job.Addressed {
			return false
		}
		if job.Status == storage.JobStatusFailed || job.Status == storage.JobStatusCanceled {
			return false
		}
	}
	return true
}

// branchMatchesFilter checks if a job's branch matches the active branch filter
func (m tuiModel) branchMatchesFilter(job storage.ReviewJob) bool {
	branch := m.getBranchForJob(job)
	if branch == "" {
		branch = "(none)"
	}
	return branch == m.activeBranchFilter
}

// pushFilter adds a filter type to the stack (or moves it to the end if already present)
func (m *tuiModel) pushFilter(filterType string) {
	// Remove if already present
	m.removeFilterFromStack(filterType)
	// Add to end
	m.filterStack = append(m.filterStack, filterType)
}

// popFilter removes the most recent filter from the stack and clears its value
// Returns the filter type that was popped, or empty string if stack was empty
func (m *tuiModel) popFilter() string {
	if len(m.filterStack) == 0 {
		return ""
	}
	// Pop the last filter
	last := m.filterStack[len(m.filterStack)-1]
	m.filterStack = m.filterStack[:len(m.filterStack)-1]
	// Clear the corresponding filter value
	switch last {
	case "repo":
		m.activeRepoFilter = nil
	case "branch":
		m.activeBranchFilter = ""
	}
	return last
}

// removeFilterFromStack removes a filter type from the stack without clearing its value
func (m *tuiModel) removeFilterFromStack(filterType string) {
	var newStack []string
	for _, f := range m.filterStack {
		if f != filterType {
			newStack = append(newStack, f)
		}
	}
	m.filterStack = newStack
}

// getVisibleJobs returns jobs filtered by active filters (repo, branch, addressed)
func (m tuiModel) getVisibleJobs() []storage.ReviewJob {
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

// getVisibleSelectedIdx returns the index within visible jobs for the current selection
// Returns -1 if selectedIdx is -1 or doesn't match any visible job
func (m tuiModel) getVisibleSelectedIdx() int {
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

// findNextVisibleJob finds the next job index in m.jobs that matches active filters
// Returns -1 if no next visible job exists
func (m tuiModel) findNextVisibleJob(currentIdx int) int {
	for i := currentIdx + 1; i < len(m.jobs); i++ {
		if m.isJobVisible(m.jobs[i]) {
			return i
		}
	}
	return -1
}

// findPrevVisibleJob finds the previous job index in m.jobs that matches active filters
// Returns -1 if no previous visible job exists
func (m tuiModel) findPrevVisibleJob(currentIdx int) int {
	for i := currentIdx - 1; i >= 0; i-- {
		if m.isJobVisible(m.jobs[i]) {
			return i
		}
	}
	return -1
}

// findFirstVisibleJob finds the first job index that matches active filters
func (m tuiModel) findFirstVisibleJob() int {
	for i, job := range m.jobs {
		if m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// findLastVisibleJob finds the last job index that matches active filters
func (m tuiModel) findLastVisibleJob() int {
	for i := len(m.jobs) - 1; i >= 0; i-- {
		if m.isJobVisible(m.jobs[i]) {
			return i
		}
	}
	return -1
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle comment view first (it captures most keys for typing)
		if m.currentView == tuiViewComment {
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
					// Return to previous view but keep text until submit succeeds
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
				// Handle typing (supports non-ASCII runes and newlines)
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

		// Handle filter view (it captures most keys for typing)
		if m.currentView == tuiViewFilter {
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
						// "All projects" - remove repo filter from stack
						m.activeRepoFilter = nil
						m.removeFilterFromStack("repo")
					} else {
						// Apply repo filter and push onto stack
						m.activeRepoFilter = selected.rootPaths
						m.pushFilter("repo")
					}
					m.currentView = tuiViewQueue
					m.filterSearch = ""
					// Invalidate selection until refetch completes - prevents
					// actions on stale jobs list before new data arrives
					m.selectedIdx = -1
					m.selectedJobID = 0
					// Refetch jobs with the new filter applied at the API level
					m.loadingJobs = true
					return m, m.fetchJobs()
				}
				return m, nil
			case "backspace":
				if len(m.filterSearch) > 0 {
					runes := []rune(m.filterSearch)
					m.filterSearch = string(runes[:len(runes)-1])
					m.filterSelectedIdx = 0 // Reset selection when search changes
				}
				return m, nil
			default:
				// Handle typing for search (supports non-ASCII runes)
				if len(msg.Runes) > 0 {
					for _, r := range msg.Runes {
						if unicode.IsPrint(r) && !unicode.IsControl(r) {
							m.filterSearch += string(r)
							m.filterSelectedIdx = 0 // Reset selection when search changes
						}
					}
				}
				return m, nil
			}
		}

		// Handle branch filter modal
		if m.currentView == tuiViewBranchFilter {
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
						// "All branches" - remove branch filter from stack
						m.activeBranchFilter = ""
						m.removeFilterFromStack("branch")
					} else {
						// Apply branch filter and push onto stack
						m.activeBranchFilter = selected.name
						m.pushFilter("branch")
					}
					m.currentView = tuiViewQueue
					m.branchFilterSearch = ""
					// Selection stays valid - client-side filtering
					return m, nil
				}
				return m, nil
			case "backspace":
				if len(m.branchFilterSearch) > 0 {
					runes := []rune(m.branchFilterSearch)
					m.branchFilterSearch = string(runes[:len(runes)-1])
					m.branchFilterSelectedIdx = 0 // Reset selection when search changes
				}
				return m, nil
			default:
				// Handle typing for search (supports non-ASCII runes)
				if len(msg.Runes) > 0 {
					for _, r := range msg.Runes {
						if unicode.IsPrint(r) && !unicode.IsControl(r) {
							m.branchFilterSearch += string(r)
							m.branchFilterSelectedIdx = 0 // Reset selection when search changes
						}
					}
				}
				return m, nil
			}
		}

		// Handle tail view
		if m.currentView == tuiViewTail {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc", "q":
				m.currentView = m.tailFromView
				m.tailStreaming = false
				return m, nil
			case "x":
				// Cancel the job from tail view
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
				m.tailFollow = false // Stop auto-scroll when user scrolls up
				if m.tailScroll > 0 {
					m.tailScroll--
				}
				return m, nil
			case "down", "j":
				m.tailScroll++
				return m, nil
			case "pgup":
				m.tailFollow = false // Stop auto-scroll when user scrolls up
				visibleLines := m.height - 4 // Match renderTailView reservedLines
				if visibleLines < 1 {
					visibleLines = 1
				}
				m.tailScroll -= visibleLines
				if m.tailScroll < 0 {
					m.tailScroll = 0
				}
				return m, tea.ClearScreen
			case "pgdown":
				visibleLines := m.height - 4 // Match renderTailView reservedLines
				if visibleLines < 1 {
					visibleLines = 1
				}
				m.tailScroll += visibleLines
				return m, tea.ClearScreen
			case "home":
				m.tailFollow = false // Stop auto-scroll when going to top
				m.tailScroll = 0
				return m, nil
			case "end":
				m.tailFollow = true // Resume auto-scroll when going to bottom
				visibleLines := m.height - 4 // Match renderTailView reservedLines
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
				// Toggle between top and bottom
				visibleLines := m.height - 4 // Match renderTailView reservedLines
				if visibleLines < 1 {
					visibleLines = 1
				}
				maxScroll := len(m.tailLines) - visibleLines
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.tailScroll == 0 {
					// At top, go to bottom
					m.tailFollow = true
					m.tailScroll = maxScroll
				} else {
					// Not at top, go to top
					m.tailFollow = false
					m.tailScroll = 0
				}
				return m, tea.ClearScreen
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			if m.currentView == tuiViewReview {
				m.currentView = tuiViewQueue
				m.currentReview = nil
				m.reviewScroll = 0
				m.normalizeSelectionIfHidden()
				return m, nil
			}
			if m.currentView == tuiViewPrompt {
				// Go back to where we came from
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

		case "up":
			if m.currentView == tuiViewQueue {
				// Navigate to previous visible job (respects filter)
				prevIdx := m.findPrevVisibleJob(m.selectedIdx)
				if prevIdx >= 0 {
					m.selectedIdx = prevIdx
					m.updateSelectedJobID()
				} else {
					m.flashMessage = "No newer review"
					m.flashExpiresAt = time.Now().Add(2 * time.Second)
					m.flashView = tuiViewQueue
				}
			} else if m.currentView == tuiViewReview {
				if m.reviewScroll > 0 {
					m.reviewScroll--
				}
			} else if m.currentView == tuiViewPrompt {
				if m.promptScroll > 0 {
					m.promptScroll--
				}
			} else if m.currentView == tuiViewCommitMsg {
				if m.commitMsgScroll > 0 {
					m.commitMsgScroll--
				}
			}

		case "k", "right":
			if m.currentView == tuiViewQueue {
				// Navigate to previous visible job (respects filter)
				prevIdx := m.findPrevVisibleJob(m.selectedIdx)
				if prevIdx >= 0 {
					m.selectedIdx = prevIdx
					m.updateSelectedJobID()
				}
			} else if m.currentView == tuiViewReview {
				// Navigate to previous review (lower index)
				prevIdx := m.findPrevViewableJob()
				if prevIdx >= 0 {
					m.selectedIdx = prevIdx
					m.updateSelectedJobID()
					m.reviewScroll = 0
					job := m.jobs[prevIdx]
					if job.Status == storage.JobStatusDone {
						return m, m.fetchReview(job.ID)
					} else if job.Status == storage.JobStatusFailed {
						m.currentBranch = "" // Clear stale branch from previous review
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
			} else if m.currentView == tuiViewPrompt {
				if m.promptScroll > 0 {
					m.promptScroll--
				}
			}

		case "down":
			if m.currentView == tuiViewQueue {
				// Navigate to next visible job (respects filter)
				nextIdx := m.findNextVisibleJob(m.selectedIdx)
				if nextIdx >= 0 {
					m.selectedIdx = nextIdx
					m.updateSelectedJobID()
				} else if m.hasMore && !m.loadingMore && !m.loadingJobs && len(m.activeRepoFilter) == 0 {
					// At bottom with more jobs available - load them
					m.loadingMore = true
					return m, m.fetchMoreJobs()
				} else if !m.hasMore || len(m.activeRepoFilter) > 0 {
					// Truly at the bottom - no more to load or filter prevents auto-load
					m.flashMessage = "No older review"
					m.flashExpiresAt = time.Now().Add(2 * time.Second)
					m.flashView = tuiViewQueue
				}
			} else if m.currentView == tuiViewReview {
				m.reviewScroll++
			} else if m.currentView == tuiViewPrompt {
				m.promptScroll++
			} else if m.currentView == tuiViewCommitMsg {
				m.commitMsgScroll++
			}

		case "j", "left":
			if m.currentView == tuiViewQueue {
				// Navigate to next visible job (respects filter)
				nextIdx := m.findNextVisibleJob(m.selectedIdx)
				if nextIdx >= 0 {
					m.selectedIdx = nextIdx
					m.updateSelectedJobID()
				} else if m.hasMore && !m.loadingMore && !m.loadingJobs && len(m.activeRepoFilter) == 0 {
					// At bottom with more jobs available - load them
					m.loadingMore = true
					return m, m.fetchMoreJobs()
				}
			} else if m.currentView == tuiViewReview {
				// Navigate to next review (higher index)
				nextIdx := m.findNextViewableJob()
				if nextIdx >= 0 {
					m.selectedIdx = nextIdx
					m.updateSelectedJobID()
					m.reviewScroll = 0
					job := m.jobs[nextIdx]
					if job.Status == storage.JobStatusDone {
						return m, m.fetchReview(job.ID)
					} else if job.Status == storage.JobStatusFailed {
						m.currentBranch = "" // Clear stale branch from previous review
						m.currentReview = &storage.Review{
							Agent:  job.Agent,
							Output: "Job failed:\n\n" + job.Error,
							Job:    &job,
						}
					}
				} else {
					m.flashMessage = "No older review"
					m.flashExpiresAt = time.Now().Add(2 * time.Second)
					m.flashView = tuiViewReview
				}
			} else if m.currentView == tuiViewPrompt {
				m.promptScroll++
			}

		case "pgup":
			pageSize := max(1, m.height-10)
			if m.currentView == tuiViewQueue {
				// Move up by pageSize visible jobs
				for i := 0; i < pageSize; i++ {
					prevIdx := m.findPrevVisibleJob(m.selectedIdx)
					if prevIdx < 0 {
						break
					}
					m.selectedIdx = prevIdx
				}
				m.updateSelectedJobID()
			} else if m.currentView == tuiViewReview {
				m.reviewScroll = max(0, m.reviewScroll-pageSize)
				return m, tea.ClearScreen
			} else if m.currentView == tuiViewPrompt {
				m.promptScroll = max(0, m.promptScroll-pageSize)
				return m, tea.ClearScreen
			}

		case "pgdown":
			pageSize := max(1, m.height-10)
			if m.currentView == tuiViewQueue {
				// Move down by pageSize visible jobs
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
				// If we hit the end, try to load more
				if reachedEnd && m.hasMore && !m.loadingMore && !m.loadingJobs && len(m.activeRepoFilter) == 0 {
					m.loadingMore = true
					return m, m.fetchMoreJobs()
				}
			} else if m.currentView == tuiViewReview {
				m.reviewScroll += pageSize
				return m, tea.ClearScreen
			} else if m.currentView == tuiViewPrompt {
				m.promptScroll += pageSize
				return m, tea.ClearScreen
			}

		case "enter":
			if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := m.jobs[m.selectedIdx]
				if job.Status == storage.JobStatusDone {
					return m, m.fetchReview(job.ID)
				} else if job.Status == storage.JobStatusFailed {
					// Show error inline for failed jobs
					m.currentBranch = "" // Clear stale branch from previous review
					m.currentReview = &storage.Review{
						Agent:  job.Agent,
						Output: "Job failed:\n\n" + job.Error,
						Job:    &job,
					}
					m.currentView = tuiViewReview
					m.reviewScroll = 0
					return m, nil
				} else {
					// Queued, running, or canceled - show flash notification
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
			}

		case "p":
			if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := m.jobs[m.selectedIdx]
				if job.Status == storage.JobStatusDone {
					// Fetch review and go directly to prompt view
					m.promptFromQueue = true
					return m, m.fetchReviewForPrompt(job.ID)
				} else if job.Status == storage.JobStatusRunning && job.Prompt != "" {
					// Show prompt from job directly for running jobs
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
				// Toggle back: go to review if came from review, queue if came from queue
				if m.promptFromQueue {
					m.currentView = tuiViewQueue
					m.currentReview = nil
					m.promptScroll = 0
				} else {
					m.currentView = tuiViewReview
					m.promptScroll = 0
				}
			}

		case "a":
			// Toggle addressed status (optimistic update - UI updates immediately)
			if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.ID > 0 {
				oldState := m.currentReview.Addressed
				newState := !oldState
				m.addressedSeq++ // Increment sequence for this request
				seq := m.addressedSeq
				m.currentReview.Addressed = newState // Optimistic update
				// Also update the job in queue so it's consistent when returning
				var jobID int64
				if m.currentReview.Job != nil {
					jobID = m.currentReview.Job.ID
					m.setJobAddressed(jobID, newState)
					m.pendingAddressed[jobID] = pendingState{newState: newState, seq: seq}
				} else {
					// No job associated - track by review ID instead
					m.pendingReviewAddressed[m.currentReview.ID] = pendingState{newState: newState, seq: seq}
				}
				// Don't update selectedIdx here - keep it pointing at the current (now hidden) job.
				// The findNextViewableJob/findPrevViewableJob functions start searching from
				// selectedIdx +/- 1, so left/right navigation will naturally find the correct
				// adjacent visible jobs. Moving selectedIdx would cause navigation to skip a job.
				return m, m.addressReview(m.currentReview.ID, jobID, newState, oldState, seq)
			} else if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := &m.jobs[m.selectedIdx]
				if job.Status == storage.JobStatusDone && job.Addressed != nil {
					oldState := *job.Addressed
					newState := !oldState
					m.addressedSeq++ // Increment sequence for this request
					seq := m.addressedSeq
					*job.Addressed = newState // Optimistic update
					m.pendingAddressed[job.ID] = pendingState{newState: newState, seq: seq}
					// If hiding addressed and we just marked as addressed, move to next visible job
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

		case "x":
			// Cancel a running or queued job (optimistic update)
			if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := &m.jobs[m.selectedIdx]
				if job.Status == storage.JobStatusRunning || job.Status == storage.JobStatusQueued {
					oldStatus := job.Status
					oldFinishedAt := job.FinishedAt // Save for rollback
					job.Status = storage.JobStatusCanceled // Optimistic update
					now := time.Now()
					job.FinishedAt = &now // Stop elapsed time from ticking
					return m, m.cancelJob(job.ID, oldStatus, oldFinishedAt)
				}
			}

		case "r":
			// Rerun a completed, failed, or canceled job (optimistic update)
			if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := &m.jobs[m.selectedIdx]
				if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed || job.Status == storage.JobStatusCanceled {
					oldStatus := job.Status
					oldStartedAt := job.StartedAt
					oldFinishedAt := job.FinishedAt
					oldError := job.Error
					job.Status = storage.JobStatusQueued // Optimistic update
					job.StartedAt = nil
					job.FinishedAt = nil
					job.Error = ""
					return m, m.rerunJob(job.ID, oldStatus, oldStartedAt, oldFinishedAt, oldError)
				}
			}

		case "t":
			// Tail running job output
			if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := m.jobs[m.selectedIdx]
				if job.Status == storage.JobStatusRunning {
					m.tailJobID = job.ID
					m.tailLines = nil
					m.tailScroll = 0
					m.tailStreaming = true
					m.tailFollow = true // Start in follow mode (auto-scroll)
					m.tailFromView = tuiViewQueue
					m.currentView = tuiViewTail
					return m, tea.Batch(tea.ClearScreen, m.fetchTailOutput(job.ID))
				} else if job.Status == storage.JobStatusQueued {
					m.flashMessage = "Job is queued - not yet running"
					m.flashExpiresAt = time.Now().Add(2 * time.Second)
					m.flashView = tuiViewQueue
				}
			}

		case "f":
			// Open filter modal
			if m.currentView == tuiViewQueue {
				m.filterRepos = nil // Clear previous repos (will show loading)
				m.filterSelectedIdx = 0
				m.filterSearch = ""
				m.currentView = tuiViewFilter
				return m, m.fetchRepos()
			}

		case "b":
			// Open branch filter modal
			if m.currentView == tuiViewQueue {
				m.filterBranches = nil // Clear previous branches (will show loading)
				m.branchFilterSelectedIdx = 0
				m.branchFilterSearch = ""
				m.currentView = tuiViewBranchFilter
				return m, m.fetchBranches()
			}

		case "h":
			// Toggle hide addressed
			if m.currentView == tuiViewQueue {
				m.hideAddressed = !m.hideAddressed
				// Update selection to first visible job immediately
				if len(m.jobs) > 0 {
					if m.selectedIdx < 0 || m.selectedIdx >= len(m.jobs) || !m.isJobVisible(m.jobs[m.selectedIdx]) {
						// Selection invalid or hidden, move to first visible
						m.selectedIdx = m.findFirstVisibleJob()
						m.updateSelectedJobID()
					}
					// Verify getVisibleSelectedIdx will find this job
					if m.getVisibleSelectedIdx() < 0 && m.findFirstVisibleJob() >= 0 {
						m.selectedIdx = m.findFirstVisibleJob()
						m.updateSelectedJobID()
					}
				}
				if m.hideAddressed {
					// Fetch all jobs when enabling filter (need full dataset for client-side filtering)
					return m, m.fetchJobs()
				}
			}

		case "c":
			// Open comment modal (from queue or review view)
			if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := m.jobs[m.selectedIdx]
				// Only allow responding to completed or failed reviews
				if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed {
					// Only clear text if opening for a different job (preserve for retry)
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
				// Only clear text if opening for a different job (preserve for retry)
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

		case "y":
			// Yank (copy) review content to clipboard
			if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.Output != "" {
				// Copy from review view - we already have the content
				return m, m.copyToClipboard(m.currentReview)
			} else if m.currentView == tuiViewQueue && len(m.jobs) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.jobs) {
				job := m.jobs[m.selectedIdx]
				// Only allow copying from completed or failed jobs
				if job.Status == storage.JobStatusDone || job.Status == storage.JobStatusFailed {
					// Need to fetch review first, then copy
					return m, m.fetchReviewAndCopy(job.ID, &job)
				} else {
					// Queued, running, or canceled - show flash notification
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
			}

		case "m":
			// Show commit message(s) for the selected job
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

		case "?":
			// Toggle help modal
			if m.currentView == tuiViewHelp {
				m.currentView = m.helpFromView
				return m, nil
			}
			if m.currentView == tuiViewQueue || m.currentView == tuiViewReview {
				m.helpFromView = m.currentView
				m.currentView = tuiViewHelp
				return m, nil
			}

		case "esc":
			if m.currentView == tuiViewQueue && len(m.filterStack) > 0 {
				// Pop the most recent filter from the stack
				popped := m.popFilter()
				if popped == "repo" {
					// Repo filter is server-side, need to refetch
					m.jobs = nil
					m.hasMore = false
					m.selectedIdx = -1
					m.selectedJobID = 0
					if m.loadingJobs || m.loadingMore {
						m.pendingRefetch = true
						return m, nil
					}
					m.loadingJobs = true
					return m, m.fetchJobs()
				}
				// Branch filter is client-side, no refetch needed
				return m, nil
			} else if m.currentView == tuiViewQueue && m.hideAddressed {
				// Clear hide-addressed filter (no project/branch filter active)
				m.hideAddressed = false
				m.jobs = nil
				m.hasMore = false
				m.selectedIdx = -1
				m.selectedJobID = 0
				// If already loading (full refresh or pagination), queue a refetch
				// to avoid out-of-order responses mixing stale data
				if m.loadingJobs || m.loadingMore {
					m.pendingRefetch = true
					return m, nil
				}
				m.loadingJobs = true
				return m, m.fetchJobs()
			} else if m.currentView == tuiViewReview {
				m.currentView = tuiViewQueue
				m.currentReview = nil
				m.reviewScroll = 0
				m.normalizeSelectionIfHidden()
				// If hiding addressed, trigger refresh to ensure clean state
				// (avoids timing issues where addressed job briefly appears)
				if m.hideAddressed && !m.loadingJobs {
					m.loadingJobs = true
					return m, m.fetchJobs()
				}
			} else if m.currentView == tuiViewPrompt {
				// Go back to where we came from
				if m.promptFromQueue {
					m.currentView = tuiViewQueue
					m.currentReview = nil
					m.promptScroll = 0
				} else {
					m.currentView = tuiViewReview
					m.promptScroll = 0
				}
			} else if m.currentView == tuiViewCommitMsg {
				// Go back to originating view
				m.currentView = m.commitMsgFromView
				m.commitMsgContent = ""
				m.commitMsgScroll = 0
			} else if m.currentView == tuiViewHelp {
				// Go back to previous view
				m.currentView = m.helpFromView
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.heightDetected = true

		// If terminal can show more jobs than we have, re-fetch to fill screen
		// Gate on !loadingMore and !loadingJobs to avoid race conditions
		if !m.loadingMore && !m.loadingJobs && len(m.jobs) > 0 && m.hasMore && len(m.activeRepoFilter) == 0 {
			newVisibleRows := m.height - 9 + 10
			if newVisibleRows > len(m.jobs) {
				m.loadingJobs = true
				return m, m.fetchJobs()
			}
		}

	case tuiTickMsg:
		// Skip job refresh while pagination or another refresh is in flight
		if m.loadingMore || m.loadingJobs {
			return m, tea.Batch(m.tick(), m.fetchStatus())
		}
		// Don't set loadingJobs for background refreshes - avoids flickering
		// "Loading..." when there are no jobs. loadingJobs is only set for
		// initial load or user-initiated actions (filter changes, etc.)
		return m, tea.Batch(m.tick(), m.fetchJobs(), m.fetchStatus())

	case tuiTailTickMsg:
		// Refresh tail output if still in tail view and streaming
		if m.currentView == tuiViewTail && m.tailStreaming && m.tailJobID > 0 {
			return m, m.fetchTailOutput(m.tailJobID)
		}

	case tuiJobsMsg:
		m.loadingMore = false
		if !msg.append {
			m.loadingJobs = false
		}
		m.consecutiveErrors = 0 // Reset on successful fetch

		// If filter changed while this fetch was in flight, discard stale data
		// and trigger a fresh fetch with the current filter state
		if m.pendingRefetch {
			m.pendingRefetch = false
			m.loadingJobs = true
			return m, m.fetchJobs()
		}

		m.hasMore = msg.hasMore

		// Update display name cache for new jobs
		m.updateDisplayNameCache(msg.jobs)

		if msg.append {
			// Append mode: add new jobs to existing list
			m.jobs = append(m.jobs, msg.jobs...)
		} else {
			// Replace mode: full refresh
			m.jobs = msg.jobs
		}

		// Clear pending addressed states that server has confirmed (data matches pending)
		// This must happen before re-applying, so we can check the raw server state
		for jobID, pending := range m.pendingAddressed {
			for i := range m.jobs {
				if m.jobs[i].ID == jobID {
					// Check if server state matches pending state
					// Treat nil as false (unaddressed) for comparison
					serverState := m.jobs[i].Addressed != nil && *m.jobs[i].Addressed
					if serverState == pending.newState {
						delete(m.pendingAddressed, jobID)
					}
					break
				}
			}
		}

		// Apply any remaining pending addressed changes to prevent flash during race
		// condition (server data is stale, from request sent before update completed)
		for i := range m.jobs {
			if pending, ok := m.pendingAddressed[m.jobs[i].ID]; ok {
				newState := pending.newState
				m.jobs[i].Addressed = &newState
			}
		}

		if len(m.jobs) == 0 {
			m.selectedIdx = -1
			// Only clear selectedJobID if not viewing a review - preserves
			// selection through transient empty refreshes
			if m.currentView != tuiViewReview || m.currentReview == nil || m.currentReview.Job == nil {
				m.selectedJobID = 0
			}
		} else if m.selectedJobID > 0 {
			// Try to find the selected job by ID - this preserves the user's
			// selection even if they've navigated to a new review that hasn't
			// loaded yet (selectedJobID tracks intent, currentReview is display)
			found := false
			for i, job := range m.jobs {
				if job.ID == m.selectedJobID {
					m.selectedIdx = i
					found = true
					break
				}
			}

			if !found {
				// Job was removed - clamp index to valid range
				m.selectedIdx = max(0, min(len(m.jobs)-1, m.selectedIdx))
				// If any filter is active, ensure we're on a visible job
				if len(m.activeRepoFilter) > 0 || m.hideAddressed {
					firstVisible := m.findFirstVisibleJob()
					if firstVisible >= 0 {
						m.selectedIdx = firstVisible
						m.selectedJobID = m.jobs[firstVisible].ID
					} else {
						// No visible jobs for this filter
						m.selectedIdx = -1
						m.selectedJobID = 0
					}
				} else {
					m.selectedJobID = m.jobs[m.selectedIdx].ID
				}
			} else if !m.isJobVisible(m.jobs[m.selectedIdx]) {
				// Job exists but is not visible (filtered by repo or hidden)
				firstVisible := m.findFirstVisibleJob()
				if firstVisible >= 0 {
					m.selectedIdx = firstVisible
					m.selectedJobID = m.jobs[firstVisible].ID
				} else {
					// No visible jobs for this filter
					m.selectedIdx = -1
					m.selectedJobID = 0
				}
			}
		} else if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.Job != nil {
			// selectedJobID is 0 but we're viewing a review - seed from current review
			// (can happen after transient empty refresh cleared selectedJobID)
			targetID := m.currentReview.Job.ID
			for i, job := range m.jobs {
				if job.ID == targetID {
					m.selectedIdx = i
					m.selectedJobID = targetID
					break
				}
			}
			// If not found, fall through to select first job
			if m.selectedJobID == 0 {
				m.selectedIdx = 0
				m.selectedJobID = m.jobs[0].ID
			}
		} else {
			// No job was selected yet, select first visible job
			firstVisible := m.findFirstVisibleJob()
			if firstVisible >= 0 {
				m.selectedIdx = firstVisible
				m.selectedJobID = m.jobs[firstVisible].ID
			} else if len(m.activeRepoFilter) == 0 && len(m.jobs) > 0 {
				// No filter, just select first job
				m.selectedIdx = 0
				m.selectedJobID = m.jobs[0].ID
			} else {
				// No visible jobs
				m.selectedIdx = -1
				m.selectedJobID = 0
			}
		}

	case tuiStatusMsg:
		m.status = storage.DaemonStatus(msg)
		m.consecutiveErrors = 0 // Reset on successful fetch
		if m.status.Version != "" {
			m.daemonVersion = m.status.Version
			// Check for version mismatch between TUI and daemon
			m.versionMismatch = m.daemonVersion != version.Version
		}
		// Show flash notification when config is reloaded
		// Use counter (not timestamp) to detect reloads that happen within the same second
		if m.statusFetchedOnce && m.status.ConfigReloadCounter != m.lastConfigReloadCounter {
			m.flashMessage = "Config reloaded"
			m.flashExpiresAt = time.Now().Add(5 * time.Second)
			m.flashView = m.currentView
		}
		m.lastConfigReloadCounter = m.status.ConfigReloadCounter
		m.statusFetchedOnce = true

	case tuiUpdateCheckMsg:
		m.updateAvailable = msg.version
		m.updateIsDevBuild = msg.isDevBuild

	case tuiReviewMsg:
		// Ignore stale responses from rapid navigation
		if msg.jobID != m.selectedJobID {
			return m, nil
		}
		m.consecutiveErrors = 0 // Reset on successful fetch
		m.currentReview = msg.review
		m.currentResponses = msg.responses
		m.currentBranch = msg.branchName
		m.currentView = tuiViewReview
		m.reviewScroll = 0

	case tuiPromptMsg:
		m.consecutiveErrors = 0 // Reset on successful fetch
		m.currentReview = msg
		m.currentView = tuiViewPrompt
		m.promptScroll = 0

	case tuiTailOutputMsg:
		m.consecutiveErrors = 0
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if m.currentView == tuiViewTail {
			// Only update lines if we have new content, or if job is still streaming
			// This preserves the output when job completes (buffer gets closed on server)
			if len(msg.lines) > 0 || msg.hasMore {
				m.tailLines = msg.lines
			}
			m.tailStreaming = msg.hasMore
			// Auto-scroll to bottom only if in follow mode
			if m.tailFollow && len(m.tailLines) > 0 {
				visibleLines := m.height - 4 // Match renderTailView reservedLines
				if visibleLines < 1 {
					visibleLines = 1
				}
				maxScroll := len(m.tailLines) - visibleLines
				if maxScroll < 0 {
					maxScroll = 0
				}
				m.tailScroll = maxScroll
			}
			// Continue polling if still streaming
			if m.tailStreaming {
				return m, tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
					return tuiTailTickMsg{}
				})
			}
		}

	case tuiAddressedMsg:
		if m.currentReview != nil {
			m.currentReview.Addressed = bool(msg)
		}

	case tuiAddressedResultMsg:
		// Check if this response is still current by comparing sequence numbers.
		// Stale responses (from rapid toggles) should be ignored entirely.
		// A response is current if its seq matches the pending seq for that job/review.
		isCurrentRequest := false
		if msg.jobID > 0 {
			if pending, ok := m.pendingAddressed[msg.jobID]; ok && pending.seq == msg.seq {
				isCurrentRequest = true
			}
		} else if msg.reviewView && msg.reviewID > 0 {
			// Review-view response without jobID: check pendingReviewAddressed
			if pending, ok := m.pendingReviewAddressed[msg.reviewID]; ok && pending.seq == msg.seq {
				isCurrentRequest = true
			}
		}

		if msg.err != nil {
			// Only rollback on error if this is the current request
			if isCurrentRequest {
				if msg.reviewView {
					// Rollback review view only if still viewing the same review
					if m.currentReview != nil && m.currentReview.ID == msg.reviewID {
						m.currentReview.Addressed = msg.oldState
					}
				}
				// Rollback the job in queue and clear pending state
				if msg.jobID > 0 {
					m.setJobAddressed(msg.jobID, msg.oldState)
					delete(m.pendingAddressed, msg.jobID)
				} else if msg.reviewID > 0 {
					delete(m.pendingReviewAddressed, msg.reviewID)
				}
				m.err = msg.err
			}
			// Stale error responses are silently ignored
		} else {
			// Success handling differs by type:
			// - For jobs (jobID > 0): don't clear here. Let the jobs refresh handler
			//   clear it when server data confirms the update. This prevents a race
			//   where we clear pending, then a stale jobs response arrives and briefly
			//   shows the old state.
			// - For review-only (no jobID): clear immediately. The race condition doesn't
			//   apply because pendingReviewAddressed isn't affected by jobs refresh.
			if isCurrentRequest && msg.jobID == 0 && msg.reviewID > 0 {
				delete(m.pendingReviewAddressed, msg.reviewID)
			}
		}

	case tuiCancelResultMsg:
		if msg.err != nil {
			// Rollback optimistic update on error (both status and finishedAt)
			m.setJobStatus(msg.jobID, msg.oldState)
			m.setJobFinishedAt(msg.jobID, msg.oldFinishedAt)
			m.err = msg.err
		}

	case tuiRerunResultMsg:
		if msg.err != nil {
			// Rollback optimistic update on error (status, timestamps, and error)
			m.setJobStatus(msg.jobID, msg.oldState)
			m.setJobStartedAt(msg.jobID, msg.oldStartedAt)
			m.setJobFinishedAt(msg.jobID, msg.oldFinishedAt)
			m.setJobError(msg.jobID, msg.oldError)
			m.err = msg.err
		}

	case tuiReposMsg:
		m.consecutiveErrors = 0 // Reset on successful fetch
		// Populate filter repos with "All repos" as first option
		m.filterRepos = []repoFilterItem{{name: "", count: msg.totalCount}}
		m.filterRepos = append(m.filterRepos, msg.repos...)
		// Pre-select current filter if active
		if len(m.activeRepoFilter) > 0 {
			for i, r := range m.filterRepos {
				if len(r.rootPaths) == len(m.activeRepoFilter) && len(r.rootPaths) > 0 {
					// Check if all paths match
					match := true
					for j, p := range r.rootPaths {
						if p != m.activeRepoFilter[j] {
							match = false
							break
						}
					}
					if match {
						m.filterSelectedIdx = i
						break
					}
				}
			}
		}

	case tuiBranchesMsg:
		m.consecutiveErrors = 0 // Reset on successful fetch
		// Only mark backfill done when no NULL branches remain
		// Track whether all branches are filled - reset if new NULLs appear
		m.branchBackfillDone = true // Mark done after first attempt (don't retry unresolvable NULLs)
		// Populate filter branches with "All branches" as first option
		m.filterBranches = []branchFilterItem{{name: "", count: msg.totalCount}}
		m.filterBranches = append(m.filterBranches, msg.branches...)
		// Pre-select current filter if active
		if m.activeBranchFilter != "" {
			for i, b := range m.filterBranches {
				if b.name == m.activeBranchFilter {
					m.branchFilterSelectedIdx = i
					break
				}
			}
		}
		// Show flash message if branches were backfilled
		if msg.backfillCount > 0 {
			m.flashMessage = fmt.Sprintf("Backfilled branch info for %d jobs", msg.backfillCount)
			m.flashExpiresAt = time.Now().Add(5 * time.Second)
			m.flashView = tuiViewBranchFilter
		}

	case tuiCommentResultMsg:
		if msg.err != nil {
			m.err = msg.err
			// Keep commentText and commentJobID so user can retry
		} else {
			// Success - clear the response state only if still for the same job
			// (user may have started a new draft for a different job while this was in flight)
			if m.commentJobID == msg.jobID {
				m.commentText = ""
				m.commentJobID = 0
			}
			// Refresh the review to show the new response (if viewing a review)
			if m.currentView == tuiViewReview && m.currentReview != nil && m.currentReview.JobID == msg.jobID {
				return m, m.fetchReview(msg.jobID)
			}
		}

	case tuiClipboardResultMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("copy failed: %w", msg.err)
		} else {
			m.flashMessage = "Copied to clipboard"
			m.flashExpiresAt = time.Now().Add(2 * time.Second)
			m.flashView = msg.view // Use view from trigger time, not current view
		}

	case tuiCommitMsgMsg:
		// Ignore stale messages (job changed while fetching)
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
		m.currentView = tuiViewCommitMsg

	case tuiJobsErrMsg:
		m.err = msg.err
		m.loadingJobs = false // Clear loading state so refreshes can resume

		// Only count connection errors for reconnection (not 404s, parse errors, etc.)
		if isConnectionError(msg.err) {
			m.consecutiveErrors++
		}

		// If filter changed while loading, retry immediately with current filter state
		if m.pendingRefetch {
			m.pendingRefetch = false
			m.loadingJobs = true
			return m, m.fetchJobs()
		}

		// Try to reconnect after consecutive connection failures
		if m.consecutiveErrors >= 3 && !m.reconnecting {
			m.reconnecting = true
			return m, m.tryReconnect()
		}

	case tuiPaginationErrMsg:
		m.err = msg.err
		m.loadingMore = false // Clear loading state so user can retry pagination

		// Only count connection errors for reconnection
		if isConnectionError(msg.err) {
			m.consecutiveErrors++
		}

		// If filter changed while pagination was in flight, trigger fresh fetch
		if m.pendingRefetch {
			m.pendingRefetch = false
			m.loadingJobs = true
			return m, m.fetchJobs()
		}

		// Try to reconnect after consecutive connection failures
		if m.consecutiveErrors >= 3 && !m.reconnecting {
			m.reconnecting = true
			return m, m.tryReconnect()
		}

	case tuiErrMsg:
		m.err = msg
		// Count connection errors for reconnection (status/review/repo fetches use tuiErrMsg)
		if isConnectionError(msg) {
			m.consecutiveErrors++
			// Try to reconnect after consecutive connection failures
			if m.consecutiveErrors >= 3 && !m.reconnecting {
				m.reconnecting = true
				return m, m.tryReconnect()
			}
		}

	case tuiReconnectMsg:
		m.reconnecting = false
		if msg.err == nil && msg.newAddr != "" && msg.newAddr != m.serverAddr {
			// Found daemon at new address - switch to it
			m.serverAddr = msg.newAddr
			m.consecutiveErrors = 0
			m.err = nil
			// Update daemon version from reconnect result (avoid sync call)
			if msg.version != "" {
				m.daemonVersion = msg.version
			}
			// Trigger immediate refresh
			m.loadingJobs = true
			return m, tea.Batch(m.fetchJobs(), m.fetchStatus())
		}
		// Reconnection failed or same address - will retry on next tick
	}

	return m, nil
}

func (m tuiModel) View() string {
	if m.currentView == tuiViewComment {
		return m.renderRespondView()
	}
	if m.currentView == tuiViewFilter {
		return m.renderFilterView()
	}
	if m.currentView == tuiViewBranchFilter {
		return m.renderBranchFilterView()
	}
	if m.currentView == tuiViewCommitMsg {
		return m.renderCommitMsgView()
	}
	if m.currentView == tuiViewHelp {
		return m.renderHelpView()
	}
	if m.currentView == tuiViewTail {
		return m.renderTailView()
	}
	if m.currentView == tuiViewPrompt && m.currentReview != nil {
		return m.renderPromptView()
	}
	if m.currentView == tuiViewReview && m.currentReview != nil {
		return m.renderReviewView()
	}
	return m.renderQueueView()
}

func (m tuiModel) renderQueueView() string {
	var b strings.Builder

	// Title with version, optional update notification, and filter indicators (in stack order)
	title := fmt.Sprintf("roborev queue (%s)", version.Version)
	for _, filterType := range m.filterStack {
		switch filterType {
		case "repo":
			if len(m.activeRepoFilter) > 0 {
				filterName := m.getDisplayName(m.activeRepoFilter[0], filepath.Base(m.activeRepoFilter[0]))
				title += fmt.Sprintf(" [f: %s]", filterName)
			}
		case "branch":
			if m.activeBranchFilter != "" {
				title += fmt.Sprintf(" [b: %s]", m.activeBranchFilter)
			}
		}
	}
	if m.hideAddressed {
		title += " [hiding addressed]"
	}
	b.WriteString(tuiTitleStyle.Render(title))
	b.WriteString("\x1b[K\n") // Clear to end of line

	// Status line - show filtered counts when filter is active
	var statusLine string
	if len(m.activeRepoFilter) > 0 {
		// Calculate counts from visible jobs (handles multi-path client-side filtering)
		var done, failed, canceled int
		for _, job := range m.jobs {
			if !m.repoMatchesFilter(job.RepoPath) {
				continue
			}
			switch job.Status {
			case storage.JobStatusDone:
				done++
			case storage.JobStatusFailed:
				failed++
			case storage.JobStatusCanceled:
				canceled++
			}
		}
		statusLine = fmt.Sprintf("Daemon: %s | Done: %d | Failed: %d | Canceled: %d",
			m.daemonVersion, done, failed, canceled)
	} else {
		statusLine = fmt.Sprintf("Daemon: %s | Workers: %d/%d | Done: %d | Failed: %d | Canceled: %d",
			m.daemonVersion,
			m.status.ActiveWorkers, m.status.MaxWorkers,
			m.status.CompletedJobs, m.status.FailedJobs,
			m.status.CanceledJobs)
	}
	b.WriteString(tuiStatusStyle.Render(statusLine))
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
	// Reserve lines for: title(1) + status(2) + header(2) + scroll indicator(1) + status/update(1) + help(2)
	reservedLines := 9
	visibleRows := m.height - reservedLines
	if visibleRows < 3 {
		visibleRows = 3 // Show at least 3 jobs
	}

	// Track scroll indicator state for later
	var scrollInfo string
	start := 0
	end := 0

	if len(visibleJobList) == 0 {
		if m.loadingJobs || m.loadingMore || m.pendingRefetch {
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
		idWidth := 2 // minimum width
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
			idWidth, "ID",
			colWidths.ref, "Ref",
			colWidths.branch, "Branch",
			colWidths.repo, "Repo",
			colWidths.agent, "Agent",
			"Status", "P/F", "Queued", "Elapsed", "Addr'd")
		b.WriteString(tuiStatusStyle.Render(header))
		b.WriteString("\x1b[K\n") // Clear to end of line
		b.WriteString("  " + strings.Repeat("-", min(m.width-4, 200)))
		b.WriteString("\x1b[K\n") // Clear to end of line

		// Determine which jobs to show, keeping selected item visible
		start = 0
		end = len(visibleJobList)

		if len(visibleJobList) > visibleRows {
			// Center the selected item when possible
			start = visibleSelectedIdx - visibleRows/2
			if start < 0 {
				start = 0
			}
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
				line = tuiSelectedStyle.Render("> " + line)
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
			} else if m.hasMore && len(m.activeRepoFilter) == 0 {
				scrollInfo = fmt.Sprintf("[showing %d-%d of %d+] scroll down to load more", start+1, end, len(visibleJobList))
			} else if len(visibleJobList) > visibleRows {
				scrollInfo = fmt.Sprintf("[showing %d-%d of %d]", start+1, end, len(visibleJobList))
			}
		}
	}

	// Always emit scroll indicator line (blank if no scroll info) to maintain consistent height
	if scrollInfo != "" {
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n") // Clear scroll indicator line

	// Status line: flash message (temporary)
	if m.flashMessage != "" && time.Now().Before(m.flashExpiresAt) && m.flashView == tuiViewQueue {
		flashStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"}) // Green
		b.WriteString(flashStyle.Render(m.flashMessage))
	}
	b.WriteString("\x1b[K\n") // Clear to end of line

	// Version mismatch error (persistent, red)
	if m.versionMismatch {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}).Bold(true) // Red
		b.WriteString(errorStyle.Render(fmt.Sprintf("VERSION MISMATCH: TUI %s != Daemon %s - restart TUI or daemon", version.Version, m.daemonVersion)))
		b.WriteString("\x1b[K\n")
	}

	// Help (two lines)
	helpLine1 := "↑/↓: navigate | enter: review | y: copy | m: commit msg | q: quit | ?: help"
	helpLine2 := "f: filter | b: branch | h: hide addressed | a: toggle addressed | x: cancel"
	if len(m.activeRepoFilter) > 0 || m.activeBranchFilter != "" || m.hideAddressed {
		helpLine2 += " | esc: clear filters"
	}
	b.WriteString(tuiHelpStyle.Render(helpLine1))
	b.WriteString("\x1b[K\n") // Clear to end of line
	b.WriteString(tuiHelpStyle.Render(helpLine2))
	b.WriteString("\x1b[K") // Clear to end of line (no newline at end)
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

type columnWidths struct {
	ref    int
	branch int
	repo   int
	agent  int
}

func (m tuiModel) calculateColumnWidths(idWidth int) columnWidths {
	// Fixed widths: ID (idWidth), Status (8), P/F (3), Queued (12), Elapsed (8), Addr'd (6)
	// Status width 8 accommodates "canceled" (longest status)
	// Plus spacing: 2 (prefix) + 9 spaces between columns (one more for branch)
	fixedWidth := 2 + idWidth + 8 + 3 + 12 + 8 + 6 + 9

	// Available width for flexible columns (ref, branch, repo, agent)
	// Don't artificially inflate - if terminal is too narrow, columns will be tiny
	availableWidth := max(4, m.width-fixedWidth) // At least 4 chars total for columns

	// Distribute available width: ref (15%), branch (35%), repo (35%), agent (15%)
	refWidth := max(1, availableWidth*15/100)
	branchWidth := max(1, availableWidth*35/100)
	repoWidth := max(1, availableWidth*35/100)
	agentWidth := max(1, availableWidth*15/100)

	// Scale down if total exceeds available (can happen due to rounding with small values)
	total := refWidth + branchWidth + repoWidth + agentWidth
	if total > availableWidth && availableWidth > 0 {
		refWidth = max(1, availableWidth*15/100)
		branchWidth = max(1, availableWidth*35/100)
		repoWidth = max(1, availableWidth*35/100)
		agentWidth = availableWidth - refWidth - branchWidth - repoWidth // Give remainder to agent
		if agentWidth < 1 {
			agentWidth = 1
		}
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

func (m tuiModel) renderJobLine(job storage.ReviewJob, selected bool, idWidth int, colWidths columnWidths) string {
	ref := shortJobRef(job)
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
			styledStatus = tuiQueuedStyle.Render(status)
		case storage.JobStatusRunning:
			styledStatus = tuiRunningStyle.Render(status)
		case storage.JobStatusDone:
			styledStatus = tuiDoneStyle.Render(status)
		case storage.JobStatusFailed:
			styledStatus = tuiFailedStyle.Render(status)
		case storage.JobStatusCanceled:
			styledStatus = tuiCanceledStyle.Render(status)
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
			verdict = tuiPassStyle.Render(v)
		} else {
			verdict = tuiFailStyle.Render(v)
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
			addr = "true"
		} else {
			addr = "false"
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

// wrapText wraps text to the specified width, preserving existing line breaks
// and breaking at word boundaries when possible
func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 100
	}

	var result []string
	for _, line := range strings.Split(text, "\n") {
		if len(line) <= width {
			result = append(result, line)
			continue
		}

		// Wrap long lines
		for len(line) > width {
			// Find a good break point (space) near the width
			breakPoint := width
			for i := width; i > width/2; i-- {
				if i < len(line) && line[i] == ' ' {
					breakPoint = i
					break
				}
			}

			result = append(result, line[:breakPoint])
			line = strings.TrimLeft(line[breakPoint:], " ")
		}
		if len(line) > 0 {
			result = append(result, line)
		}
	}

	return result
}

func (m tuiModel) renderReviewView() string {
	var b strings.Builder

	review := m.currentReview

	// Build title string and compute its length for line calculation
	var title string
	var titleLen int
	var locationLineLen int
	if review.Job != nil {
		ref := shortJobRef(*review.Job)
		idStr := fmt.Sprintf("#%d ", review.Job.ID)
		// Use cached display name, falling back to RepoName, then basename of RepoPath
		defaultName := review.Job.RepoName
		if defaultName == "" && review.Job.RepoPath != "" {
			defaultName = filepath.Base(review.Job.RepoPath)
		}
		repoStr := m.getDisplayName(review.Job.RepoPath, defaultName)

		agentStr := formatAgentLabel(review.Agent, review.Job.Model)

		title = fmt.Sprintf("Review %s%s (%s)", idStr, repoStr, agentStr)
		titleLen = runewidth.StringWidth(title)

		b.WriteString(tuiTitleStyle.Render(title))
		b.WriteString("\x1b[K") // Clear to end of line

		// Show location line: repo path (or identity/name), git ref, and branch
		b.WriteString("\n")
		locationLine := review.Job.RepoPath
		if locationLine == "" {
			// No local path - use repo name/identity as fallback
			locationLine = review.Job.RepoName
		}
		if locationLine != "" {
			locationLine += " " + ref
		} else {
			locationLine = ref
		}
		if m.currentBranch != "" {
			locationLine += " on " + m.currentBranch
		}
		locationLineLen = runewidth.StringWidth(locationLine)
		b.WriteString(tuiStatusStyle.Render(locationLine))
		b.WriteString("\x1b[K") // Clear to end of line

		// Show verdict and addressed status on next line
		hasVerdict := review.Job.Verdict != nil && *review.Job.Verdict != ""
		if hasVerdict || review.Addressed {
			b.WriteString("\n")
			if hasVerdict {
				v := *review.Job.Verdict
				if v == "P" {
					b.WriteString(tuiPassStyle.Render("Verdict: Pass"))
				} else {
					b.WriteString(tuiFailStyle.Render("Verdict: Fail"))
				}
			}
			// Show [ADDRESSED] with distinct color (after verdict if present)
			if review.Addressed {
				if hasVerdict {
					b.WriteString(" ")
				}
				b.WriteString(tuiAddressedStyle.Render("[ADDRESSED]"))
			}
			b.WriteString("\x1b[K") // Clear to end of line
		}
		b.WriteString("\n")
	} else {
		title = "Review"
		titleLen = len(title)
		b.WriteString(tuiTitleStyle.Render(title))
		b.WriteString("\x1b[K\n") // Clear to end of line
	}

	// Build content: review output + responses
	var content strings.Builder
	content.WriteString(review.Output)

	// Append responses if any
	if len(m.currentResponses) > 0 {
		content.WriteString("\n\n--- Comments ---\n")
		for _, r := range m.currentResponses {
			timestamp := r.CreatedAt.Format("Jan 02 15:04")
			content.WriteString(fmt.Sprintf("\n[%s] %s:\n", timestamp, r.Responder))
			content.WriteString(r.Response)
			content.WriteString("\n")
		}
	}

	// Wrap text to terminal width minus padding
	wrapWidth := max(20, min(m.width-4, 200))
	lines := wrapText(content.String(), wrapWidth)

	// Compute title line count based on actual title length
	titleLines := 1
	if m.width > 0 && titleLen > m.width {
		titleLines = (titleLen + m.width - 1) / m.width
	}

	// Help text wraps at narrow terminals
	const helpText = "↑/↓: scroll | j/k: prev/next | a: addressed | y: copy | m: commit msg | ?: help | esc/q: back"
	helpLines := 1
	if m.width > 0 && m.width < len(helpText) {
		helpLines = (len(helpText) + m.width - 1) / m.width
	}

	// Compute location line count (repo path + ref + branch can wrap)
	locationLines := 0
	if review.Job != nil {
		locationLines = 1
		if m.width > 0 && locationLineLen > m.width {
			locationLines = (locationLineLen + m.width - 1) / m.width
		}
	}

	// headerHeight = title + location line + status line (1) + help + verdict/addressed (0|1)
	headerHeight := titleLines + locationLines + 1 + helpLines
	hasVerdict := review.Job != nil && review.Job.Verdict != nil && *review.Job.Verdict != ""
	if hasVerdict || review.Addressed {
		headerHeight++ // Add 1 for verdict/addressed line
	}
	visibleLines := m.height - headerHeight
	if visibleLines < 1 {
		visibleLines = 1
	}

	// Clamp scroll position to valid range
	maxScroll := len(lines) - visibleLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	start := m.reviewScroll
	if start > maxScroll {
		start = maxScroll
	}
	if start < 0 {
		start = 0
	}
	end := min(start+visibleLines, len(lines))

	linesWritten := 0
	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\x1b[K\n") // Clear to end of line before newline
		linesWritten++
	}

	// Pad with clear-to-end-of-line sequences to prevent ghost text
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Status line: flash message (temporary) takes priority over scroll indicator
	if m.flashMessage != "" && time.Now().Before(m.flashExpiresAt) && m.flashView == tuiViewReview {
		flashStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"}) // Green
		b.WriteString(flashStyle.Render(m.flashMessage))
	} else if len(lines) > visibleLines {
		scrollInfo := fmt.Sprintf("[%d-%d of %d lines]", start+1, end, len(lines))
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n") // Clear status line

	// Version mismatch error (persistent, red)
	if m.versionMismatch {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}).Bold(true)
		b.WriteString(errorStyle.Render(fmt.Sprintf("VERSION MISMATCH: TUI %s != Daemon %s - restart TUI or daemon", version.Version, m.daemonVersion)))
		b.WriteString("\x1b[K\n")
	}

	b.WriteString(tuiHelpStyle.Render(helpText))
	b.WriteString("\x1b[K") // Clear help line
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) renderPromptView() string {
	var b strings.Builder

	// Clear screen and move cursor to home position to prevent artifacts on scroll
	b.WriteString("\x1b[2J\x1b[H")

	review := m.currentReview
	if review.Job != nil {
		ref := shortJobRef(*review.Job)
		agentStr := formatAgentLabel(review.Agent, review.Job.Model)
		title := fmt.Sprintf("Prompt: %s (%s)", ref, agentStr)
		b.WriteString(tuiTitleStyle.Render(title))
	} else {
		b.WriteString(tuiTitleStyle.Render("Prompt"))
	}
	b.WriteString("\x1b[K\n") // Clear to end of line

	// Wrap text to terminal width minus padding
	wrapWidth := max(20, min(m.width-4, 200))
	lines := wrapText(review.Prompt, wrapWidth)

	// Reserve: title(1) + scroll indicator(1) + help(1) + margin(1)
	visibleLines := m.height - 4
	if visibleLines < 1 {
		visibleLines = 1
	}

	// Clamp scroll position to valid range
	maxScroll := len(lines) - visibleLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	start := m.promptScroll
	if start > maxScroll {
		start = maxScroll
	}
	if start < 0 {
		start = 0
	}
	end := min(start+visibleLines, len(lines))

	linesWritten := 0
	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\x1b[K\n") // Clear to end of line before newline
		linesWritten++
	}

	// Pad with clear-to-end-of-line sequences to prevent ghost text
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Scroll indicator
	if len(lines) > visibleLines {
		scrollInfo := fmt.Sprintf("[%d-%d of %d lines]", start+1, end, len(lines))
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n") // Clear scroll indicator line

	b.WriteString(tuiHelpStyle.Render("up/down: scroll | p: back to review | esc/q: back"))
	b.WriteString("\x1b[K") // Clear help line
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) renderFilterView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("Filter by Repository"))
	b.WriteString("\x1b[K\n\x1b[K\n") // Clear title and blank line

	// Show loading state if repos haven't been fetched yet
	if m.filterRepos == nil {
		b.WriteString(tuiStatusStyle.Render("Loading repos..."))
		b.WriteString("\x1b[K\n")
		// Pad to fill terminal height: title(1) + blank(1) + loading(1) + padding + help(1)
		// We've written 3 lines so far (title, blank, loading)
		linesWritten := 3
		for linesWritten < m.height-1 { // -1 for help line at bottom
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
		b.WriteString(tuiHelpStyle.Render("esc: cancel"))
		b.WriteString("\x1b[K")
		b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts
		return b.String()
	}

	// Search box
	searchDisplay := m.filterSearch
	if searchDisplay == "" {
		searchDisplay = tuiStatusStyle.Render("Type to search...")
	}
	b.WriteString(fmt.Sprintf("Search: %s", searchDisplay))
	b.WriteString("\x1b[K\n\x1b[K\n") // Clear search and blank line

	visible := m.getVisibleFilterRepos()

	// Calculate visible rows
	// Reserve: title(1) + blank(1) + search(1) + blank(1) + scroll-info(1) + blank(1) + help(1) = 7
	reservedLines := 7
	visibleRows := m.height - reservedLines
	if visibleRows < 0 {
		visibleRows = 0
	}

	// Determine which repos to show, keeping selected item visible
	start := 0
	end := len(visible)
	needsScroll := len(visible) > visibleRows && visibleRows > 0
	if needsScroll {
		start = m.filterSelectedIdx - visibleRows/2
		if start < 0 {
			start = 0
		}
		end = start + visibleRows
		if end > len(visible) {
			end = len(visible)
			start = end - visibleRows
			if start < 0 {
				start = 0
			}
		}
	} else if visibleRows > 0 {
		// No scrolling needed, show all (up to visibleRows)
		if end > visibleRows {
			end = visibleRows
		}
	} else {
		// No room for repos
		end = 0
	}

	repoLinesWritten := 0
	for i := start; i < end; i++ {
		repo := visible[i]
		var line string
		if repo.name == "" {
			line = fmt.Sprintf("All repos (%d)", repo.count)
		} else {
			// repo.name is already the display name (aggregated in fetchRepos)
			line = fmt.Sprintf("%s (%d)", repo.name, repo.count)
		}

		if i == m.filterSelectedIdx {
			b.WriteString(tuiSelectedStyle.Render("> " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\x1b[K\n") // Clear to end of line before newline
		repoLinesWritten++
	}

	if len(visible) == 0 {
		b.WriteString(tuiStatusStyle.Render("  No matching repos"))
		b.WriteString("\x1b[K\n")
		repoLinesWritten++
	} else if visibleRows == 0 {
		b.WriteString(tuiStatusStyle.Render("  (terminal too small)"))
		b.WriteString("\x1b[K\n")
		repoLinesWritten++
	}

	// Pad with clear-to-end-of-line sequences to prevent ghost text
	for repoLinesWritten < visibleRows {
		b.WriteString("\x1b[K\n")
		repoLinesWritten++
	}

	if needsScroll {
		scrollInfo := fmt.Sprintf("[showing %d-%d of %d]", start+1, end, len(visible))
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n") // Clear scroll indicator line

	b.WriteString(tuiHelpStyle.Render("up/down: navigate | enter: select | esc: cancel | type to search"))
	b.WriteString("\x1b[K") // Clear help line
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) renderBranchFilterView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("Filter by Branch"))
	b.WriteString("\x1b[K\n\x1b[K\n") // Clear title and blank line

	// Show loading state if branches haven't been fetched yet
	if m.filterBranches == nil {
		b.WriteString(tuiStatusStyle.Render("Loading branches..."))
		b.WriteString("\x1b[K\n")
		// Pad to fill terminal height
		linesWritten := 3
		for linesWritten < m.height-1 {
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
		b.WriteString(tuiHelpStyle.Render("esc: cancel"))
		b.WriteString("\x1b[K")
		b.WriteString("\x1b[J")
		return b.String()
	}

	// Search box
	searchDisplay := m.branchFilterSearch
	if searchDisplay == "" {
		searchDisplay = tuiStatusStyle.Render("Type to search...")
	}
	b.WriteString(fmt.Sprintf("Search: %s", searchDisplay))
	b.WriteString("\x1b[K\n\x1b[K\n")

	visible := m.getVisibleFilterBranches()

	// Calculate visible rows
	reservedLines := 7
	visibleRows := m.height - reservedLines
	if visibleRows < 0 {
		visibleRows = 0
	}

	// Determine which branches to show, keeping selected item visible
	start := 0
	end := len(visible)
	needsScroll := len(visible) > visibleRows && visibleRows > 0
	if needsScroll {
		start = m.branchFilterSelectedIdx - visibleRows/2
		if start < 0 {
			start = 0
		}
		end = start + visibleRows
		if end > len(visible) {
			end = len(visible)
			start = end - visibleRows
			if start < 0 {
				start = 0
			}
		}
	} else if visibleRows > 0 {
		if end > visibleRows {
			end = visibleRows
		}
	} else {
		end = 0
	}

	branchLinesWritten := 0
	for i := start; i < end; i++ {
		branch := visible[i]
		var line string
		if branch.name == "" {
			line = fmt.Sprintf("All branches (%d)", branch.count)
		} else {
			line = fmt.Sprintf("%s (%d)", branch.name, branch.count)
		}

		if i == m.branchFilterSelectedIdx {
			b.WriteString(tuiSelectedStyle.Render("> " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\x1b[K\n")
		branchLinesWritten++
	}

	if len(visible) == 0 {
		b.WriteString(tuiStatusStyle.Render("  No matching branches"))
		b.WriteString("\x1b[K\n")
		branchLinesWritten++
	} else if visibleRows == 0 {
		b.WriteString(tuiStatusStyle.Render("  (terminal too small)"))
		b.WriteString("\x1b[K\n")
		branchLinesWritten++
	}

	// Pad with clear-to-end-of-line sequences
	for branchLinesWritten < visibleRows {
		b.WriteString("\x1b[K\n")
		branchLinesWritten++
	}

	if needsScroll {
		scrollInfo := fmt.Sprintf("[showing %d-%d of %d]", start+1, end, len(visible))
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n")

	b.WriteString(tuiHelpStyle.Render("up/down: navigate | enter: select | esc: cancel | type to search"))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J")

	return b.String()
}

func (m tuiModel) renderRespondView() string {
	var b strings.Builder

	title := "Add Comment"
	if m.commentCommit != "" {
		title = fmt.Sprintf("Add Comment (%s)", m.commentCommit)
	}
	b.WriteString(tuiTitleStyle.Render(title))
	b.WriteString("\x1b[K\n\x1b[K\n") // Clear title and blank line

	b.WriteString(tuiStatusStyle.Render("Enter your comment (e.g., \"This is a known issue, can be ignored\")"))
	b.WriteString("\x1b[K\n\x1b[K\n")

	// Simple text box with border
	boxWidth := m.width - 4
	if boxWidth < 20 {
		boxWidth = 20
	}

	b.WriteString("+-" + strings.Repeat("-", boxWidth-2) + "-+\n")

	// Wrap text display to box width
	textLinesWritten := 0
	maxTextLines := m.height - 10 // Reserve space for chrome
	if maxTextLines < 3 {
		maxTextLines = 3
	}

	if m.commentText == "" {
		// Show placeholder (styled, but we pad manually to avoid ANSI issues)
		placeholder := "Type your comment..."
		padded := placeholder + strings.Repeat(" ", boxWidth-2-len(placeholder))
		b.WriteString("| " + tuiStatusStyle.Render(padded) + " |\x1b[K\n")
		textLinesWritten++
	} else {
		lines := strings.Split(m.commentText, "\n")
		for _, line := range lines {
			if textLinesWritten >= maxTextLines {
				break
			}
			// Expand tabs to spaces (4-space tabs) for consistent width calculation
			line = strings.ReplaceAll(line, "\t", "    ")
			// Truncate lines that are too long (use visual width for wide characters)
			line = runewidth.Truncate(line, boxWidth-2, "")
			// Pad based on visual width, not rune count
			padding := boxWidth - 2 - runewidth.StringWidth(line)
			if padding < 0 {
				padding = 0
			}
			b.WriteString(fmt.Sprintf("| %s%s |\x1b[K\n", line, strings.Repeat(" ", padding)))
			textLinesWritten++
		}
	}

	// Pad with empty lines if needed
	for textLinesWritten < 3 {
		b.WriteString(fmt.Sprintf("| %-*s |\x1b[K\n", boxWidth-2, ""))
		textLinesWritten++
	}

	b.WriteString("+-" + strings.Repeat("-", boxWidth-2) + "-+\x1b[K\n")

	// Pad remaining space
	linesWritten := 6 + textLinesWritten // title, blank, help, blank, top border, bottom border
	for linesWritten < m.height-1 {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	b.WriteString(tuiHelpStyle.Render("enter: submit | esc: cancel"))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) submitComment(jobID int64, text string) tea.Cmd {
	return func() tea.Msg {
		commenter := os.Getenv("USER")
		if commenter == "" {
			commenter = "anonymous"
		}

		payload := map[string]interface{}{
			"job_id":    jobID,
			"commenter": commenter,
			"comment":   strings.TrimSpace(text),
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return tuiCommentResultMsg{jobID: jobID, err: fmt.Errorf("marshal request: %w", err)}
		}

		resp, err := m.client.Post(
			fmt.Sprintf("%s/api/comment", m.serverAddr),
			"application/json",
			bytes.NewReader(body),
		)
		if err != nil {
			return tuiCommentResultMsg{jobID: jobID, err: fmt.Errorf("submit comment: %w", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			return tuiCommentResultMsg{jobID: jobID, err: fmt.Errorf("submit comment: HTTP %d", resp.StatusCode)}
		}

		return tuiCommentResultMsg{jobID: jobID, err: nil}
	}
}

func (m tuiModel) renderCommitMsgView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("Commit Message"))
	b.WriteString("\x1b[K\n") // Clear to end of line

	if m.commitMsgContent == "" {
		b.WriteString(tuiStatusStyle.Render("Loading commit message..."))
		b.WriteString("\x1b[K\n")
		// Pad to fill terminal
		linesWritten := 2
		for linesWritten < m.height-1 {
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
		b.WriteString(tuiHelpStyle.Render("esc/q: back"))
		b.WriteString("\x1b[K")
		b.WriteString("\x1b[J")
		return b.String()
	}

	// Wrap text to terminal width minus padding
	wrapWidth := max(20, min(m.width-4, 200))
	lines := wrapText(m.commitMsgContent, wrapWidth)

	// Reserve: title(1) + scroll indicator(1) + help(1) + margin(1)
	visibleLines := m.height - 4
	if visibleLines < 1 {
		visibleLines = 1
	}

	// Clamp scroll position to valid range
	maxScroll := len(lines) - visibleLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	start := m.commitMsgScroll
	if start > maxScroll {
		start = maxScroll
	}
	if start < 0 {
		start = 0
	}
	end := min(start+visibleLines, len(lines))

	linesWritten := 0
	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\x1b[K\n") // Clear to end of line before newline
		linesWritten++
	}

	// Pad with clear-to-end-of-line sequences to prevent ghost text
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Scroll indicator
	if len(lines) > visibleLines {
		scrollInfo := fmt.Sprintf("[%d-%d of %d lines]", start+1, end, len(lines))
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n") // Clear scroll indicator line

	b.WriteString(tuiHelpStyle.Render("up/down: scroll | esc/q: back"))
	b.WriteString("\x1b[K") // Clear help line
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) renderTailView() string {
	var b strings.Builder

	// Title with job info
	var title string
	for _, job := range m.jobs {
		if job.ID == m.tailJobID {
			repoName := m.getDisplayName(job.RepoPath, job.RepoName)
			shortRef := job.GitRef
			if len(shortRef) > 7 {
				shortRef = shortRef[:7]
			}
			title = fmt.Sprintf("Tail: %s %s (#%d)", repoName, shortRef, job.ID)
			break
		}
	}
	if title == "" {
		title = fmt.Sprintf("Tail: Job #%d", m.tailJobID)
	}
	if m.tailStreaming {
		title += " " + tuiRunningStyle.Render("● live")
	} else {
		title += " " + tuiDoneStyle.Render("● complete")
	}
	b.WriteString(tuiTitleStyle.Render(title))
	b.WriteString("\x1b[K\n")

	// Calculate visible area
	reservedLines := 4 // title + separator + status + help
	visibleLines := m.height - reservedLines
	if visibleLines < 1 {
		visibleLines = 1
	}

	// Clamp scroll
	maxScroll := len(m.tailLines) - visibleLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := m.tailScroll
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}

	// Separator
	b.WriteString(strings.Repeat("─", m.width))
	b.WriteString("\x1b[K\n")

	// Render lines
	linesWritten := 0
	if len(m.tailLines) == 0 {
		b.WriteString(tuiStatusStyle.Render("Waiting for output..."))
		b.WriteString("\x1b[K\n")
		linesWritten++
	} else {
		end := scroll + visibleLines
		if end > len(m.tailLines) {
			end = len(m.tailLines)
		}
		for i := scroll; i < end; i++ {
			line := m.tailLines[i]
			// Format with timestamp
			ts := line.timestamp.Format("15:04:05")

			// Truncate raw text BEFORE styling to avoid cutting ANSI codes
			// Account for timestamp prefix (8 chars + 1 space = 9)
			lineText := line.text
			maxTextWidth := m.width - 9
			if maxTextWidth > 3 && runewidth.StringWidth(lineText) > maxTextWidth {
				lineText = runewidth.Truncate(lineText, maxTextWidth-3, "...")
			}

			var text string
			switch line.lineType {
			case "tool":
				text = fmt.Sprintf("%s %s", tuiStatusStyle.Render(ts), tuiQueuedStyle.Render(lineText))
			case "error":
				text = fmt.Sprintf("%s %s", tuiStatusStyle.Render(ts), tuiFailedStyle.Render(lineText))
			default:
				text = fmt.Sprintf("%s %s", tuiStatusStyle.Render(ts), lineText)
			}
			b.WriteString(text)
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
	}

	// Pad remaining lines
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Status line with position and follow mode
	var status string
	if len(m.tailLines) > visibleLines {
		// Calculate actual displayed range (not including padding)
		displayEnd := scroll + visibleLines
		if displayEnd > len(m.tailLines) {
			displayEnd = len(m.tailLines)
		}
		status = fmt.Sprintf("[%d-%d of %d lines]", scroll+1, displayEnd, len(m.tailLines))
	} else {
		status = fmt.Sprintf("[%d lines]", len(m.tailLines))
	}
	if m.tailFollow {
		status += " " + tuiRunningStyle.Render("[following]")
	} else {
		status += " " + tuiStatusStyle.Render("[paused - G to follow]")
	}
	b.WriteString(tuiStatusStyle.Render(status))
	b.WriteString("\x1b[K\n")

	// Help
	help := "↑/↓: scroll | g: toggle top/bottom | x: cancel | esc/q: back"
	b.WriteString(tuiHelpStyle.Render(help))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J") // Clear to end of screen

	return b.String()
}

func (m tuiModel) renderHelpView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("Keyboard Shortcuts"))
	b.WriteString("\x1b[K\n\x1b[K\n")

	// Define shortcuts in groups
	shortcuts := []struct {
		group string
		keys  []struct{ key, desc string }
	}{
		{
			group: "Navigation",
			keys: []struct{ key, desc string }{
				{"↑/k", "Move up / previous review"},
				{"↓/j", "Move down / next review"},
				{"PgUp/PgDn", "Scroll by page"},
				{"enter", "View review details"},
				{"esc", "Go back / clear filter"},
				{"q", "Quit"},
			},
		},
		{
			group: "Actions",
			keys: []struct{ key, desc string }{
				{"a", "Mark as addressed"},
				{"c", "Add comment"},
				{"t", "Tail running job output"},
				{"x", "Cancel job"},
				{"r", "Re-run job"},
				{"y", "Copy review to clipboard"},
				{"m", "Show commit message(s)"},
			},
		},
		{
			group: "Filtering",
			keys: []struct{ key, desc string }{
				{"f", "Filter by repository"},
				{"h", "Toggle hide addressed"},
			},
		},
		{
			group: "Review View",
			keys: []struct{ key, desc string }{
				{"p", "View prompt"},
				{"↑/↓", "Scroll content"},
				{"←/→", "Previous / next review"},
			},
		},
	}

	// Calculate visible area
	// Reserve: title(1) + blank(1) + padding + help(1)
	reservedLines := 3
	visibleLines := m.height - reservedLines
	if visibleLines < 5 {
		visibleLines = 5
	}

	linesWritten := 0
	for _, g := range shortcuts {
		if linesWritten >= visibleLines-2 {
			break
		}
		// Group header
		b.WriteString(tuiSelectedStyle.Render(g.group))
		b.WriteString("\x1b[K\n")
		linesWritten++

		for _, k := range g.keys {
			if linesWritten >= visibleLines {
				break
			}
			line := fmt.Sprintf("  %-12s %s", k.key, k.desc)
			b.WriteString(line)
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
		// Blank line between groups
		if linesWritten < visibleLines {
			b.WriteString("\x1b[K\n")
			linesWritten++
		}
	}

	// Pad remaining space
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	b.WriteString(tuiHelpStyle.Render("esc/q/?: close"))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J") // Clear to end of screen

	return b.String()
}

func tuiCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal UI for monitoring reviews",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running (and restart if version mismatch)
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon error: %w", err)
			}

			if addr == "" {
				addr = getDaemonAddr()
			} else if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
				addr = "http://" + addr
			}
			p := tea.NewProgram(newTuiModel(addr), tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "daemon address (default: auto-detect)")

	return cmd
}
