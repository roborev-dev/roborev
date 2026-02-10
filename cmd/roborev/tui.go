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

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/update"
	"github.com/roborev-dev/roborev/internal/version"
	"github.com/spf13/cobra"
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
				Background(lipgloss.AdaptiveColor{Light: "153", Dark: "24"}) // Light blue background

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

// queuePrefetchBuffer is the number of extra rows to fetch beyond what's visible,
// providing a buffer for smooth scrolling without needing immediate pagination.
const queuePrefetchBuffer = 10

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
	serverAddr       string
	daemonVersion    string
	client           *http.Client
	jobs             []storage.ReviewJob
	jobStats         storage.JobStats // aggregate done/addressed/unaddressed from server
	status           storage.DaemonStatus
	selectedIdx      int
	selectedJobID    int64 // Track selected job by ID to maintain position on refresh
	currentView      tuiView
	currentReview    *storage.Review
	currentResponses []storage.Response // Responses for current review (fetched with review)
	currentBranch    string             // Cached branch name for current review (computed on load)
	reviewScroll     int
	promptScroll     int
	promptFromQueue  bool // true if prompt view was entered from queue (not review)
	width            int
	height           int
	err              error
	updateAvailable  string // Latest version if update available, empty if up to date
	updateIsDevBuild bool   // True if running a dev build
	versionMismatch  bool   // True if daemon version doesn't match TUI version

	// Pagination state
	hasMore        bool    // true if there are more jobs to load
	loadingMore    bool    // true if currently loading more jobs (pagination)
	loadingJobs    bool    // true if currently loading jobs (full refresh)
	heightDetected bool    // true after first WindowSizeMsg (real terminal height known)
	fetchSeq       int     // incremented on filter changes; stale fetch responses are discarded
	paginateNav    tuiView // non-zero: auto-navigate in this view after pagination loads

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
	pendingAddressed map[int64]pendingState // job ID -> pending state

	// Flash message (temporary status message shown briefly)
	flashMessage   string
	flashExpiresAt time.Time
	flashView      tuiView // View where flash was triggered (only show in same view)

	// Track config reload notifications
	lastConfigReloadCounter uint64                 // Last known ConfigReloadCounter from daemon status
	statusFetchedOnce       bool                   // True after first successful status fetch (for flash logic)
	pendingReviewAddressed  map[int64]pendingState // review ID -> pending state (for reviews without jobs)
	addressedSeq            uint64                 // monotonic counter for request sequencing

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
	helpScroll   int     // Scroll position in help view

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
	append  bool             // true to append to existing jobs, false to replace
	seq     int              // fetch sequence number — stale responses (seq < model.fetchSeq) are discarded
	stats   storage.JobStats // aggregate counts from server
}
type tuiStatusMsg storage.DaemonStatus
type tuiReviewMsg struct {
	review     *storage.Review
	responses  []storage.Response // Responses for this review
	jobID      int64              // The job ID that was requested (for race condition detection)
	branchName string             // Pre-computed branch name (empty if not applicable)
}
type tuiPromptMsg struct {
	review *storage.Review
	jobID  int64 // The job ID that was requested (for stale response detection)
}
type tuiAddressedMsg bool
type tuiAddressedResultMsg struct {
	jobID      int64 // job ID for queue view rollback
	reviewID   int64 // review ID for review view rollback
	reviewView bool  // true if from review view (rollback currentReview)
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
type tuiJobsErrMsg struct {
	err error
	seq int // fetch sequence number for staleness check
}
type tuiPaginationErrMsg struct {
	err error
	seq int // fetch sequence number for staleness check
}
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

	// Load preferences from config
	hideAddressed := false
	autoFilterRepo := false
	if cfg, err := config.LoadGlobal(); err == nil {
		hideAddressed = cfg.HideAddressedByDefault
		autoFilterRepo = cfg.AutoFilterRepo
	}
	// Note: Silently ignore config load errors - TUI should work with defaults

	// Auto-filter to current repo if enabled
	var activeRepoFilter []string
	var filterStack []string
	if autoFilterRepo {
		if repoRoot, err := git.GetMainRepoRoot("."); err == nil && repoRoot != "" {
			activeRepoFilter = []string{repoRoot}
			filterStack = []string{filterTypeRepo}
		}
	}

	return tuiModel{
		serverAddr:             serverAddr,
		daemonVersion:          daemonVersion,
		client:                 &http.Client{Timeout: 10 * time.Second},
		jobs:                   []storage.ReviewJob{},
		currentView:            tuiViewQueue,
		width:                  80, // sensible defaults until we get WindowSizeMsg
		height:                 24,
		loadingJobs:            true, // Init() calls fetchJobs, so mark as loading
		hideAddressed:          hideAddressed,
		activeRepoFilter:       activeRepoFilter,
		filterStack:            filterStack,
		displayNames:           make(map[string]string),      // Cache display names to avoid disk reads on render
		branchNames:            make(map[int64]string),       // Cache derived branch names to avoid git calls on render
		pendingAddressed:       make(map[int64]pendingState), // Track pending addressed changes (by job ID)
		pendingReviewAddressed: make(map[int64]pendingState), // Track pending addressed changes (by review ID)
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

	// For task jobs (run, analyze, custom) or dirty jobs, no branch makes sense
	if job.IsTaskJob() || job.IsDirtyJob() {
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

		// Set limit: use pagination unless we need client-side filtering (multi-repo)
		if needsAllJobs {
			params.Set("limit", "0")
		} else {
			limit := visibleRows
			if currentJobCount > visibleRows {
				limit = currentJobCount // Maintain paginated view on refresh
			}
			params.Set("limit", fmt.Sprintf("%d", limit))
		}

		url := fmt.Sprintf("%s/api/jobs?%s", m.serverAddr, params.Encode())
		resp, err := m.client.Get(url)
		if err != nil {
			return tuiJobsErrMsg{err: err, seq: seq}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiJobsErrMsg{err: fmt.Errorf("fetch jobs: %s", resp.Status), seq: seq}
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
			Stats   storage.JobStats    `json:"stats"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return tuiJobsErrMsg{err: err, seq: seq}
		}
		return tuiJobsMsg{jobs: result.Jobs, hasMore: result.HasMore, append: false, seq: seq, stats: result.Stats}
	}
}

func (m tuiModel) fetchMoreJobs() tea.Cmd {
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
		url := fmt.Sprintf("%s/api/jobs?%s", m.serverAddr, params.Encode())
		resp, err := m.client.Get(url)
		if err != nil {
			return tuiPaginationErrMsg{err: err, seq: seq}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return tuiPaginationErrMsg{err: fmt.Errorf("fetch more jobs: %s", resp.Status), seq: seq}
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return tuiPaginationErrMsg{err: err, seq: seq}
		}
		return tuiJobsMsg{jobs: result.Jobs, hasMore: result.HasMore, append: true, seq: seq}
	}
}

func (m tuiModel) fetchStatus() tea.Cmd {
	return func() tea.Msg {
		var status storage.DaemonStatus
		if err := m.getJSON("/api/status", &status); err != nil {
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
		// Skip sending branch for branchNone sentinel - it's a client-side filter
		reposURL := serverAddr + "/api/repos"
		if activeBranchFilter != "" && activeBranchFilter != branchNone {
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

// loadReview fetches a review from the server by job ID.
// Used by fetchReview, fetchReviewForPrompt, and fetchReviewAndCopy.
func (m tuiModel) loadReview(jobID int64) (*storage.Review, error) {
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
func (m tuiModel) loadResponses(jobID int64, review *storage.Review) []storage.Response {
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

func (m tuiModel) fetchReview(jobID int64) tea.Cmd {
	return func() tea.Msg {
		review, err := m.loadReview(jobID)
		if err != nil {
			return tuiErrMsg(err)
		}

		responses := m.loadResponses(jobID, review)

		// Compute branch name for single commits (not ranges)
		var branchName string
		if review.Job != nil && review.Job.RepoPath != "" && !strings.Contains(review.Job.GitRef, "..") {
			branchName = git.GetBranchName(review.Job.RepoPath, review.Job.GitRef)
		}

		return tuiReviewMsg{review: review, responses: responses, jobID: jobID, branchName: branchName}
	}
}

func (m tuiModel) fetchReviewForPrompt(jobID int64) tea.Cmd {
	return func() tea.Msg {
		review, err := m.loadReview(jobID)
		if err != nil {
			return tuiErrMsg(err)
		}
		return tuiPromptMsg{review: review, jobID: jobID}
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
			JobID  int64  `json:"job_id"`
			Status string `json:"status"`
			Lines  []struct {
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
		review, err := m.loadReview(jobID)
		if err != nil {
			return tuiClipboardResultMsg{err: err, view: view}
		}

		if review.Output == "" {
			return tuiClipboardResultMsg{err: fmt.Errorf("review has no content"), view: view}
		}

		// Attach job info if not already present (for header formatting)
		if review.Job == nil && job != nil {
			review.Job = job
		}

		content := formatClipboardContent(review)
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
		// Handle task jobs first (run, analyze, custom labels)
		// Check this before dirty to handle backward compatibility with older run jobs
		if job.IsTaskJob() {
			return tuiCommitMsgMsg{
				jobID: jobID,
				err:   fmt.Errorf("no commit message for task jobs"),
			}
		}

		// Handle dirty reviews (uncommitted changes)
		if job.DiffContent != nil || job.IsDirtyJob() {
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

// postAddressed sends an addressed state change to the server.
// Translates "not found" to a context-specific error message.
func (m tuiModel) postAddressed(jobID int64, newState bool, notFoundMsg string) error {
	err := m.postJSON("/api/review/address", map[string]interface{}{
		"job_id":    jobID,
		"addressed": newState,
	}, nil)
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("%s", notFoundMsg)
	}
	if err != nil {
		return fmt.Errorf("mark review: %w", err)
	}
	return nil
}

func (m tuiModel) addressReview(reviewID, jobID int64, newState, oldState bool, seq uint64) tea.Cmd {
	return func() tea.Msg {
		err := m.postAddressed(jobID, newState, "review not found")
		return tuiAddressedResultMsg{reviewID: reviewID, jobID: jobID, reviewView: true, oldState: oldState, newState: newState, seq: seq, err: err}
	}
}

// addressReviewInBackground updates addressed status by job ID.
// Used for optimistic updates from queue view - UI already updated, this syncs to server.
// On error, returns tuiAddressedResultMsg with oldState for rollback.
func (m tuiModel) addressReviewInBackground(jobID int64, newState, oldState bool, seq uint64) tea.Cmd {
	return func() tea.Msg {
		err := m.postAddressed(jobID, newState, "no review for this job")
		return tuiAddressedResultMsg{jobID: jobID, oldState: oldState, newState: newState, seq: seq, err: err}
	}
}

func (m tuiModel) toggleAddressedForJob(jobID int64, currentState *bool) tea.Cmd {
	return func() tea.Msg {
		newState := true
		if currentState != nil && *currentState {
			newState = false
		}
		if err := m.postAddressed(jobID, newState, "no review for this job"); err != nil {
			return tuiErrMsg(err)
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

// Job mutation setters (setJobAddressed, setJobStatus, setJobFinishedAt,
// setJobStartedAt, setJobError) and mutateJob are in tui_helpers.go

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

// findNextPromptableJob finds the next job that has a viewable prompt (done or running with prompt).
// Respects active filters. Returns the index or -1 if none found.
func (m *tuiModel) findNextPromptableJob() int {
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
func (m *tuiModel) findPrevPromptableJob() int {
	for i := m.selectedIdx - 1; i >= 0; i-- {
		job := m.jobs[i]
		if m.isJobVisible(job) &&
			(job.Status == storage.JobStatusDone || (job.Status == storage.JobStatusRunning && job.Prompt != "")) {
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
		err := m.postJSON("/api/job/cancel", map[string]interface{}{"job_id": jobID}, nil)
		return tuiCancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: err}
	}
}

// rerunJob sends a rerun request to the server for failed/canceled jobs
func (m tuiModel) rerunJob(jobID int64, oldStatus storage.JobStatus, oldStartedAt, oldFinishedAt *time.Time, oldError string) tea.Cmd {
	return func() tea.Msg {
		err := m.postJSON("/api/job/rerun", map[string]interface{}{"job_id": jobID}, nil)
		return tuiRerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: err}
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
		branch = branchNone
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
	case filterTypeRepo:
		m.activeRepoFilter = nil
	case filterTypeBranch:
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

// queueVisibleRows returns how many queue rows fit in the current terminal.
func (m tuiModel) queueVisibleRows() int {
	// Keep in sync with renderQueueView reserved lines.
	const reservedLines = 8
	visibleRows := m.height - reservedLines
	if visibleRows < 3 {
		visibleRows = 3
	}
	return visibleRows
}

// canPaginate returns true if more jobs can be loaded via pagination.
// False when already loading, using client-side filters that load all jobs,
// or when there are no more jobs to fetch.
func (m tuiModel) canPaginate() bool {
	return m.hasMore && !m.loadingMore && !m.loadingJobs &&
		len(m.activeRepoFilter) <= 1 && m.activeBranchFilter != branchNone
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
		return m.handleKeyMsg(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.heightDetected = true

		// If terminal can show more jobs than we have, re-fetch to fill screen
		// Gate on !loadingMore and !loadingJobs to avoid race conditions
		if !m.loadingMore && !m.loadingJobs && len(m.jobs) > 0 && m.hasMore && len(m.activeRepoFilter) <= 1 {
			newVisibleRows := m.queueVisibleRows() + queuePrefetchBuffer
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
		return m, tea.Batch(m.tick(), m.fetchJobs(), m.fetchStatus())

	case tuiTailTickMsg:
		if m.currentView == tuiViewTail && m.tailStreaming && m.tailJobID > 0 {
			return m, m.fetchTailOutput(m.tailJobID)
		}

	case tuiJobsMsg:
		return m.handleJobsMsg(msg)

	case tuiStatusMsg:
		return m.handleStatusMsg(msg)

	case tuiUpdateCheckMsg:
		m.updateAvailable = msg.version
		m.updateIsDevBuild = msg.isDevBuild

	case tuiReviewMsg:
		if msg.jobID != m.selectedJobID {
			return m, nil
		}
		m.consecutiveErrors = 0
		m.currentReview = msg.review
		m.currentResponses = msg.responses
		m.currentBranch = msg.branchName
		m.currentView = tuiViewReview
		m.reviewScroll = 0

	case tuiPromptMsg:
		if msg.jobID != m.selectedJobID {
			return m, nil
		}
		m.consecutiveErrors = 0
		m.currentReview = msg.review
		m.currentView = tuiViewPrompt
		m.promptScroll = 0

	case tuiTailOutputMsg:
		m.consecutiveErrors = 0
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if m.currentView == tuiViewTail {
			if len(msg.lines) > 0 || msg.hasMore {
				m.tailLines = msg.lines
			}
			m.tailStreaming = msg.hasMore
			if m.tailFollow && len(m.tailLines) > 0 {
				visibleLines := m.height - 4
				if visibleLines < 1 {
					visibleLines = 1
				}
				maxScroll := len(m.tailLines) - visibleLines
				if maxScroll < 0 {
					maxScroll = 0
				}
				m.tailScroll = maxScroll
			}
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
		return m.handleAddressedResultMsg(msg)

	case tuiCancelResultMsg:
		if msg.err != nil {
			m.setJobStatus(msg.jobID, msg.oldState)
			m.setJobFinishedAt(msg.jobID, msg.oldFinishedAt)
			m.err = msg.err
		}

	case tuiRerunResultMsg:
		if msg.err != nil {
			m.setJobStatus(msg.jobID, msg.oldState)
			m.setJobStartedAt(msg.jobID, msg.oldStartedAt)
			m.setJobFinishedAt(msg.jobID, msg.oldFinishedAt)
			m.setJobError(msg.jobID, msg.oldError)
			m.err = msg.err
		}

	case tuiReposMsg:
		m.consecutiveErrors = 0
		m.filterRepos = []repoFilterItem{{name: "", count: msg.totalCount}}
		m.filterRepos = append(m.filterRepos, msg.repos...)
		if len(m.activeRepoFilter) > 0 || m.activeBranchFilter != "" {
			for i, r := range m.filterRepos {
				if len(r.rootPaths) == len(m.activeRepoFilter) && len(r.rootPaths) > 0 {
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
		m.consecutiveErrors = 0
		m.branchBackfillDone = true
		m.filterBranches = []branchFilterItem{{name: "", count: msg.totalCount}}
		m.filterBranches = append(m.filterBranches, msg.branches...)
		if m.activeBranchFilter != "" {
			for i, b := range m.filterBranches {
				if b.name == m.activeBranchFilter {
					m.branchFilterSelectedIdx = i
					break
				}
			}
		}
		if msg.backfillCount > 0 {
			m.flashMessage = fmt.Sprintf("Backfilled branch info for %d jobs", msg.backfillCount)
			m.flashExpiresAt = time.Now().Add(5 * time.Second)
			m.flashView = tuiViewBranchFilter
		}

	case tuiCommentResultMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			if m.commentJobID == msg.jobID {
				m.commentText = ""
				m.commentJobID = 0
			}
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
			m.flashView = msg.view
		}

	case tuiCommitMsgMsg:
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
		if msg.seq < m.fetchSeq {
			return m, nil
		}
		m.err = msg.err
		m.loadingJobs = false
		if cmd := m.handleConnectionError(msg.err); cmd != nil {
			return m, cmd
		}

	case tuiPaginationErrMsg:
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

	case tuiErrMsg:
		m.err = msg
		if cmd := m.handleConnectionError(msg); cmd != nil {
			return m, cmd
		}

	case tuiReconnectMsg:
		return m.handleReconnectMsg(msg)
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
		case filterTypeRepo:
			if len(m.activeRepoFilter) > 0 {
				filterName := m.getDisplayName(m.activeRepoFilter[0], filepath.Base(m.activeRepoFilter[0]))
				title += fmt.Sprintf(" [f: %s]", filterName)
			}
		case filterTypeBranch:
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

	// Status line - use server-side aggregate counts for paginated views,
	// fall back to client-side counting for multi-repo filters (which load all jobs)
	var statusLine string
	var done, addressed, unaddressed int
	if len(m.activeRepoFilter) > 1 || m.activeBranchFilter == branchNone {
		// Client-side filtered views load all jobs, so count locally
		for _, job := range m.jobs {
			if len(m.activeRepoFilter) > 0 && !m.repoMatchesFilter(job.RepoPath) {
				continue
			}
			if m.activeBranchFilter == branchNone && job.Branch != "" {
				continue
			}
			if job.Status == storage.JobStatusDone {
				done++
				if job.Addressed != nil {
					if *job.Addressed {
						addressed++
					} else {
						unaddressed++
					}
				}
			}
		}
	} else {
		done = m.jobStats.Done
		addressed = m.jobStats.Addressed
		unaddressed = m.jobStats.Unaddressed
	}
	if len(m.activeRepoFilter) > 0 || m.activeBranchFilter != "" {
		statusLine = fmt.Sprintf("Daemon: %s | Done: %d | Addressed: %d | Unaddressed: %d",
			m.daemonVersion, done, addressed, unaddressed)
	} else {
		statusLine = fmt.Sprintf("Daemon: %s | Workers: %d/%d | Done: %d | Addressed: %d | Unaddressed: %d",
			m.daemonVersion,
			m.status.ActiveWorkers, m.status.MaxWorkers,
			done, addressed, unaddressed)
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
	// Reserve lines for: title(1) + status(2) + header(2) + scroll indicator(1) + status/update(1) + help(1)
	reservedLines := 8
	visibleRows := m.height - reservedLines
	if visibleRows < 3 {
		visibleRows = 3 // Show at least 3 jobs
	}

	// Track scroll indicator state for later
	var scrollInfo string
	start := 0
	end := 0

	if len(visibleJobList) == 0 {
		if m.loadingJobs || m.loadingMore {
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
		idWidth := 5 // minimum width (fits "JobID" header)
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
			idWidth, "JobID",
			colWidths.ref, "Ref",
			colWidths.branch, "Branch",
			colWidths.repo, "Repo",
			colWidths.agent, "Agent",
			"Status", "P/F", "Queued", "Elapsed", "Addressed")
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
				// Pad line to full terminal width for background styling
				lineWidth := lipgloss.Width(line)
				paddedLine := "> " + line
				if padding := m.width - lineWidth - 2; padding > 0 {
					paddedLine += strings.Repeat(" ", padding)
				}
				line = tuiSelectedStyle.Render(paddedLine)
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
			} else if m.hasMore && len(m.activeRepoFilter) <= 1 {
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
	// Version mismatch takes priority over flash messages (it's persistent and important)
	if m.versionMismatch {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}).Bold(true) // Red
		b.WriteString(errorStyle.Render(fmt.Sprintf("VERSION MISMATCH: TUI %s != Daemon %s - restart TUI or daemon", version.Version, m.daemonVersion)))
	} else if m.flashMessage != "" && time.Now().Before(m.flashExpiresAt) && m.flashView == tuiViewQueue {
		flashStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"}) // Green
		b.WriteString(flashStyle.Render(m.flashMessage))
	}
	b.WriteString("\x1b[K\n") // Clear to end of line

	// Help
	helpLine := "↑/↓: navigate | enter: review | a: addressed | f: filter | h: hide | ?: help | q: quit"
	b.WriteString(tuiHelpStyle.Render(helpLine))
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
	// Fixed widths: ID (idWidth), Status (8), P/F (3), Queued (12), Elapsed (8), Addressed (9)
	// Status width 8 accommodates "canceled" (longest status)
	// Plus spacing: 2 (prefix) + 9 spaces between columns (one more for branch)
	fixedWidth := 2 + idWidth + 8 + 3 + 12 + 8 + 9 + 9

	// Available width for flexible columns (ref, branch, repo, agent)
	// Don't artificially inflate - if terminal is too narrow, columns will be tiny
	availableWidth := max(4, m.width-fixedWidth) // At least 4 chars total for columns

	// Distribute available width: ref (20%), branch (32%), repo (33%), agent (15%)
	refWidth := max(1, availableWidth*20/100)
	branchWidth := max(1, availableWidth*32/100)
	repoWidth := max(1, availableWidth*33/100)
	agentWidth := max(1, availableWidth*15/100)

	// Scale down if total exceeds available (can happen due to rounding with small values)
	total := refWidth + branchWidth + repoWidth + agentWidth
	if total > availableWidth && availableWidth > 0 {
		refWidth = max(1, availableWidth*20/100)
		branchWidth = max(1, availableWidth*32/100)
		repoWidth = max(1, availableWidth*33/100)
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
	// Show review type tag for non-standard review types (e.g., [security])
	if !config.IsDefaultReviewType(job.ReviewType) {
		ref = ref + " [" + job.ReviewType + "]"
	}
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
			if selected {
				addr = "true"
			} else {
				addr = tuiAddressedStyle.Render("true")
			}
		} else {
			if selected {
				addr = "false"
			} else {
				addr = tuiQueuedStyle.Render("false")
			}
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

// commandLineForJob computes the representative agent command line from job parameters.
// Returns empty string if the agent is not available.
func commandLineForJob(job *storage.ReviewJob) string {
	if job == nil {
		return ""
	}
	a, err := agent.Get(job.Agent)
	if err != nil {
		return ""
	}
	reasoning := strings.ToLower(strings.TrimSpace(job.Reasoning))
	if reasoning == "" {
		reasoning = "thorough"
	}
	cmd := a.WithReasoning(agent.ParseReasoningLevel(reasoning)).WithAgentic(job.Agentic).WithModel(job.Model).CommandLine()
	return stripControlChars(cmd)
}

// stripControlChars removes all control characters including C0 (\x00-\x1f),
// DEL (\x7f), and C1 (\x80-\x9f) from a string to prevent terminal escape
// injection and line/tab spoofing in single-line display contexts.
func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wrapText is in tui_helpers.go (uses runewidth for correct Unicode/wide char handling)

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
	const helpText = "↑/↓: scroll | ←/→: prev/next | a: addressed | y: copy | ?: help | esc: back"
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

	// Status line: version mismatch (persistent) takes priority, then flash message, then scroll indicator
	if m.versionMismatch {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}).Bold(true) // Red
		b.WriteString(errorStyle.Render(fmt.Sprintf("VERSION MISMATCH: TUI %s != Daemon %s - restart TUI or daemon", version.Version, m.daemonVersion)))
	} else if m.flashMessage != "" && time.Now().Before(m.flashExpiresAt) && m.flashView == tuiViewReview {
		flashStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"}) // Green
		b.WriteString(flashStyle.Render(m.flashMessage))
	} else if len(lines) > visibleLines {
		scrollInfo := fmt.Sprintf("[%d-%d of %d lines]", start+1, end, len(lines))
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n") // Clear status line

	b.WriteString(tuiHelpStyle.Render(helpText))
	b.WriteString("\x1b[K") // Clear help line
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) renderPromptView() string {
	var b strings.Builder

	review := m.currentReview
	if review.Job != nil {
		ref := shortJobRef(*review.Job)
		idStr := fmt.Sprintf("#%d ", review.Job.ID)
		agentStr := formatAgentLabel(review.Agent, review.Job.Model)
		title := fmt.Sprintf("Prompt %s%s (%s)", idStr, ref, agentStr)
		b.WriteString(tuiTitleStyle.Render(title))
	} else {
		b.WriteString(tuiTitleStyle.Render("Prompt"))
	}
	b.WriteString("\x1b[K\n") // Clear to end of line

	// Show command line (computed from job params, dimmed, below title, truncated to fit)
	headerLines := 1
	if cmdLine := commandLineForJob(review.Job); cmdLine != "" {
		cmdText := "Command: " + cmdLine
		if m.width > 0 && runewidth.StringWidth(cmdText) > m.width {
			cmdText = runewidth.Truncate(cmdText, m.width, "…")
		}
		b.WriteString(tuiStatusStyle.Render(cmdText))
		b.WriteString("\x1b[K\n")
		headerLines++
	}

	// Wrap text to terminal width minus padding
	wrapWidth := max(20, min(m.width-4, 200))
	lines := wrapText(review.Prompt, wrapWidth)

	// Reserve: title(1) + command(0-1) + scroll indicator(1) + help(1) + margin(1)
	visibleLines := m.height - 3 - headerLines
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

	b.WriteString(tuiHelpStyle.Render("↑/↓: scroll | ←/→: prev/next | p: toggle prompt/review | ?: help | esc: back"))
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

		err := m.postJSON("/api/comment", map[string]interface{}{
			"job_id":    jobID,
			"commenter": commenter,
			"comment":   strings.TrimSpace(text),
		}, nil)
		if err != nil {
			return tuiCommentResultMsg{jobID: jobID, err: fmt.Errorf("submit comment: %w", err)}
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

// helpLines builds the help content lines from shortcut definitions.
func helpLines() []string {
	shortcuts := []struct {
		group string
		keys  []struct{ key, desc string }
	}{
		{
			group: "Queue View",
			keys: []struct{ key, desc string }{
				{"↑/k, ↓/j", "Navigate jobs"},
				{"g/Home", "Jump to top"},
				{"PgUp/PgDn", "Page through list"},
				{"enter", "View review"},
				{"p", "View prompt"},
				{"t", "Tail running job output"},
				{"m", "View commit message"},
			},
		},
		{
			group: "Actions",
			keys: []struct{ key, desc string }{
				{"a", "Toggle addressed"},
				{"c", "Add comment"},
				{"y", "Copy review to clipboard"},
				{"x", "Cancel running/queued job"},
				{"r", "Re-run completed/failed job"},
			},
		},
		{
			group: "Filtering",
			keys: []struct{ key, desc string }{
				{"f", "Filter by repository"},
				{"b", "Filter by branch"},
				{"h", "Toggle hide addressed/failed"},
				{"esc", "Clear filters (one at a time)"},
			},
		},
		{
			group: "Review View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Scroll content"},
				{"←/→", "Previous / next review"},
				{"PgUp/PgDn", "Page through content"},
				{"p", "Switch to prompt view"},
				{"a", "Toggle addressed"},
				{"c", "Add comment"},
				{"y", "Copy review to clipboard"},
				{"m", "View commit message"},
				{"esc/q", "Back to queue"},
			},
		},
		{
			group: "Prompt View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Scroll content"},
				{"←/→", "Previous / next prompt"},
				{"PgUp/PgDn", "Page through content"},
				{"p", "Switch to review / back to queue"},
				{"esc/q", "Back to queue"},
			},
		},
		{
			group: "Tail View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Scroll output"},
				{"PgUp/PgDn", "Page through output"},
				{"g", "Toggle follow mode / jump to top"},
				{"x", "Cancel job"},
				{"esc/q", "Back to queue"},
			},
		},
		{
			group: "General",
			keys: []struct{ key, desc string }{
				{"?", "Toggle this help"},
				{"q", "Quit (from queue view)"},
			},
		},
	}

	var lines []string
	for i, g := range shortcuts {
		lines = append(lines, "\x00group:"+g.group)
		for _, k := range g.keys {
			lines = append(lines, fmt.Sprintf("  %-14s %s", k.key, k.desc))
		}
		if i < len(shortcuts)-1 {
			lines = append(lines, "")
		}
	}
	return lines
}

// helpMaxScroll returns the maximum scroll offset for the help view.
func (m tuiModel) helpMaxScroll() int {
	reservedLines := 3 // title + blank + help hint
	visibleLines := m.height - reservedLines
	if visibleLines < 5 {
		visibleLines = 5
	}
	maxScroll := len(helpLines()) - visibleLines
	if maxScroll < 0 {
		return 0
	}
	return maxScroll
}

func (m tuiModel) renderHelpView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("Keyboard Shortcuts"))
	b.WriteString("\x1b[K\n\x1b[K\n")

	allLines := helpLines()

	// Calculate visible area: title(1) + blank(1) + help(1)
	reservedLines := 3
	visibleLines := m.height - reservedLines
	if visibleLines < 5 {
		visibleLines = 5
	}

	// Clamp scroll
	maxScroll := len(allLines) - visibleLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := m.helpScroll
	if scroll > maxScroll {
		scroll = maxScroll
	}

	// Render visible window
	end := scroll + visibleLines
	if end > len(allLines) {
		end = len(allLines)
	}
	linesWritten := 0
	for _, line := range allLines[scroll:end] {
		if strings.HasPrefix(line, "\x00group:") {
			b.WriteString(tuiSelectedStyle.Render(strings.TrimPrefix(line, "\x00group:")))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Pad remaining space
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	helpHint := "↑/↓: scroll | esc/q/?: close"
	b.WriteString(tuiHelpStyle.Render(helpHint))
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
