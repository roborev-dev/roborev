package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
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

	tuiHelpKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}) // Gray (matches status/scroll text)
	tuiHelpDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "248", Dark: "240"}) // Dimmer gray for descriptions
)

// reflowHelpRows redistributes items across rows so that when rendered
// as an aligned table (columns sized to the widest cell), the result
// fits within width. Each cell's visible width is key + space + desc,
// and non-first columns add 2 chars (▕ border + padding). If width is
// <= 0, rows are returned unchanged.
func reflowHelpRows(rows [][]helpItem, width int) [][]helpItem {
	if width <= 0 {
		return rows
	}

	// cellWidth returns the visible width of a help item (key + space + desc,
	// or just key when desc is empty).
	cellWidth := func(item helpItem) int {
		w := runewidth.StringWidth(item.key)
		if item.desc != "" {
			w += 1 + runewidth.StringWidth(item.desc)
		}
		return w
	}

	// Find the max items in any single input row.
	maxItemsPerRow := 0
	for _, row := range rows {
		if len(row) > maxItemsPerRow {
			maxItemsPerRow = len(row)
		}
	}

	// Try ncols from max down to 1. For each candidate, chunk every
	// input row into sub-rows of at most ncols items, compute aligned
	// column widths, and check if the total fits within width.
	for ncols := maxItemsPerRow; ncols >= 1; ncols-- {
		var candidate [][]helpItem
		for _, row := range rows {
			for i := 0; i < len(row); i += ncols {
				end := min(i+ncols, len(row))
				candidate = append(candidate, row[i:end])
			}
		}

		// Compute max column widths across all candidate rows.
		colW := make([]int, ncols)
		for _, crow := range candidate {
			for c, item := range crow {
				if w := cellWidth(item); w > colW[c] {
					colW[c] = w
				}
			}
		}

		// Total rendered width.
		total := 0
		for c, w := range colW {
			total += w
			if c > 0 {
				total += 2 // ▕ + padding
			}
		}

		if total <= width {
			return candidate
		}
	}

	// Fallback: one item per row.
	var result [][]helpItem
	for _, row := range rows {
		for _, item := range row {
			result = append(result, []helpItem{item})
		}
	}
	return result
}

// renderHelpTable renders helpItem entries as an aligned table.
// Keys and descriptions are two-tone gray, separated by a thin ▕ border
// that is hidden for column 0 and trailing empty cells.
func renderHelpTable(rows [][]helpItem, width int) string {
	rows = reflowHelpRows(rows, width)
	if len(rows) == 0 {
		return ""
	}

	borderColor := lipgloss.AdaptiveColor{Light: "248", Dark: "242"}
	cellStyle := lipgloss.NewStyle()
	// PaddingLeft gaps the ▕ from cell text.
	cellWithBorder := lipgloss.NewStyle().
		PaddingLeft(1).
		Border(lipgloss.Border{Left: "▕"}, false, false, false, true).
		BorderForeground(borderColor)

	// Pad rows to the same number of columns so the table aligns.
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}

	// Compute minimum visible width per column.
	colMinW := make([]int, maxCols)
	for _, row := range rows {
		for c, item := range row {
			w := runewidth.StringWidth(item.key)
			if item.desc != "" {
				w += 1 + runewidth.StringWidth(item.desc)
			}
			if w > colMinW[c] {
				colMinW[c] = w
			}
		}
	}

	// Track which cells have content for conditional borders.
	empty := make([][]bool, len(rows))

	t := table.New().
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderRow(false).
		StyleFunc(func(row, col int) lipgloss.Style {
			minW := 0
			if col < len(colMinW) {
				minW = colMinW[col]
			}
			if col == 0 || (row < len(empty) && col < len(empty[row]) && empty[row][col]) {
				return cellStyle.Width(minW)
			}
			return cellWithBorder.Width(minW + 2) // +2 for ▕ border + padding
		}).
		Wrap(false)

	for ri, row := range rows {
		styled := make([]string, maxCols)
		empty[ri] = make([]bool, maxCols)
		for i, item := range row {
			if item.desc != "" {
				styled[i] = tuiHelpKeyStyle.Render(item.key) + " " + tuiHelpDescStyle.Render(item.desc)
			} else {
				styled[i] = tuiHelpKeyStyle.Render(item.key)
			}
		}
		for i := len(row); i < maxCols; i++ {
			empty[ri][i] = true
		}
		t = t.Row(styled...)
	}

	return t.Render()
}

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

type tuiModel struct {
	serverAddr       string
	daemonVersion    string
	client           *http.Client
	glamourStyle     gansi.StyleConfig // detected once at init
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

	// CLI-locked filters: set via --repo/--branch flags, cannot be cleared by the user
	lockedRepoFilter   bool // true if repo filter was set via --repo flag
	lockedBranchFilter bool // true if branch filter was set via --branch flag

	// Pagination state
	hasMore        bool    // true if there are more jobs to load
	loadingMore    bool    // true if currently loading more jobs (pagination)
	loadingJobs    bool    // true if currently loading jobs (full refresh)
	heightDetected bool    // true after first WindowSizeMsg (real terminal height known)
	fetchSeq       int     // incremented on filter changes; stale fetch responses are discarded
	paginateNav    tuiView // non-zero: auto-navigate in this view after pagination loads

	// Unified tree filter modal state
	filterTree        []treeFilterNode  // Tree of repos (each may have branch children)
	filterFlatList    []flatFilterEntry // Flattened visible rows for navigation
	filterSelectedIdx int               // Currently highlighted row in flat list
	filterSearch      string            // Search/filter text typed by user
	filterSearchSeq   int               // Incremented on search text changes; gates stale fetchFailed
	filterBranchMode  bool              // True when opened via 'b' key (auto-expand repo to branches)

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

	// Repo root and branch detected from cwd at launch (for filter sort priority)
	cwdRepoRoot string
	cwdBranch   string

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

	// Log view state
	logJobID     int64            // Job being viewed
	logLines     []logLine        // Buffer of output lines
	logScroll    int              // Scroll position
	logStreaming bool             // True if job is still running
	logFromView  tuiView          // View to return to
	logFollow    bool             // True if auto-scrolling to bottom (follow mode)
	logOffset    int64            // Byte offset for next incremental fetch
	logFmtr      *streamFormatter // Persistent formatter across polls
	logLoading   bool             // True while a fetch is in-flight
	logFetchSeq  uint64           // Monotonic seq to drop stale responses

	// Glamour markdown render cache (pointer so View's value receiver can update it)
	mdCache *markdownCache

	clipboard ClipboardWriter

	// Review view navigation
	reviewFromView tuiView // View to return to when exiting review (queue or tasks)

	// Fix task state
	fixJobs        []storage.ReviewJob // Fix jobs for tasks view
	fixSelectedIdx int                 // Selected index in tasks view
	fixPromptText  string              // Editable fix prompt text
	fixPromptJobID int64               // Parent job ID for fix prompt modal
	fixShowHelp    bool                // Show help overlay in tasks view
	patchText      string              // Current patch text for patch viewer
	patchScroll    int                 // Scroll offset in patch viewer
	patchJobID     int64               // Job ID of the patch being viewed

	// Inline fix panel (review view)
	reviewFixPanelOpen    bool // true when fix panel is visible in review view
	reviewFixPanelFocused bool // true when keyboard focus is on the fix panel
	reviewFixPanelPending bool // true when 'F' from queue; panel opens on review load

	worktreeConfirmJobID  int64  // Job ID pending worktree-apply confirmation
	worktreeConfirmBranch string // Branch name for worktree confirmation prompt
}

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
	return errors.As(err, &netErr)
}

func newTuiModel(serverAddr string, opts ...tuiOption) tuiModel {
	var opt tuiOptions
	for _, o := range opts {
		o(&opt)
	}

	daemonVersion := "?"
	hideAddressed := false
	autoFilterRepo := false
	tabWidth := 2
	var cwdRepoRoot, cwdBranch string

	if !opt.disableExternalIO {
		// Read daemon version from runtime file
		if info, err := daemon.GetAnyRunningDaemon(); err == nil && info.Version != "" {
			daemonVersion = info.Version
		}

		// Load preferences from config
		if cfg, err := config.LoadGlobal(); err == nil {
			hideAddressed = cfg.HideAddressedByDefault
			autoFilterRepo = cfg.AutoFilterRepo
			if cfg.TabWidth > 0 {
				tabWidth = cfg.TabWidth
			}
		}

		// Detect current repo/branch for filter sort priority
		if repoRoot, err := git.GetMainRepoRoot("."); err == nil && repoRoot != "" {
			cwdRepoRoot = repoRoot
			cwdBranch = git.GetCurrentBranch(".")
		}
	}

	// Determine active filters: CLI flags take priority over auto-filter config
	var activeRepoFilter []string
	var filterStack []string
	var lockedRepo, lockedBranch bool

	if opt.repoFilter != "" {
		activeRepoFilter = []string{opt.repoFilter}
		filterStack = append(filterStack, filterTypeRepo)
		lockedRepo = true
	} else if autoFilterRepo && cwdRepoRoot != "" {
		activeRepoFilter = []string{cwdRepoRoot}
		filterStack = append(filterStack, filterTypeRepo)
	}

	var activeBranchFilter string
	if opt.branchFilter != "" {
		activeBranchFilter = opt.branchFilter
		filterStack = append(filterStack, filterTypeBranch)
		lockedBranch = true
	}

	return tuiModel{
		serverAddr:             serverAddr,
		daemonVersion:          daemonVersion,
		client:                 &http.Client{Timeout: 10 * time.Second},
		glamourStyle:           sfGlamourStyle(),
		jobs:                   []storage.ReviewJob{},
		currentView:            tuiViewQueue,
		width:                  80, // sensible defaults until we get WindowSizeMsg
		height:                 24,
		loadingJobs:            true, // Init() calls fetchJobs, so mark as loading
		hideAddressed:          hideAddressed,
		activeRepoFilter:       activeRepoFilter,
		activeBranchFilter:     activeBranchFilter,
		filterStack:            filterStack,
		lockedRepoFilter:       lockedRepo,
		lockedBranchFilter:     lockedBranch,
		cwdRepoRoot:            cwdRepoRoot,
		cwdBranch:              cwdBranch,
		displayNames:           make(map[string]string),      // Cache display names to avoid disk reads on render
		branchNames:            make(map[int64]string),       // Cache derived branch names to avoid git calls on render
		pendingAddressed:       make(map[int64]pendingState), // Track pending addressed changes (by job ID)
		pendingReviewAddressed: make(map[int64]pendingState), // Track pending addressed changes (by review ID)
		clipboard:              &realClipboard{},
		mdCache:                newMarkdownCache(tabWidth),
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

// queueHelpRows returns queue help table rows, omitting
// "f: filter" when both repo and branch filters are locked.
func (m tuiModel) queueHelpRows() [][]helpItem {
	row1 := []helpItem{
		{"x", "cancel"}, {"r", "rerun"}, {"l", "log"}, {"p", "prompt"},
		{"c", "comment"}, {"y", "copy"}, {"m", "commit msg"}, {"F", "fix"},
	}
	row2 := []helpItem{
		{"↑/↓", "navigate"}, {"↵", "review"}, {"a", "addressed"},
	}
	if !m.lockedRepoFilter || !m.lockedBranchFilter {
		row2 = append(row2, helpItem{"f", "filter"})
	}
	row2 = append(row2, helpItem{"h", "hide"}, helpItem{"T", "tasks"}, helpItem{"?", "help"}, helpItem{"q", "quit"})
	return [][]helpItem{row1, row2}
}

// queueHelpLines computes how many terminal lines the queue help
// footer occupies after reflowing items to fit within width.
func (m tuiModel) queueHelpLines() int {
	return len(reflowHelpRows(m.queueHelpRows(), m.width))
}

// queueVisibleRows returns how many queue rows fit in the current terminal.
func (m tuiModel) queueVisibleRows() int {
	// title(1) + status(2) + header(2) + scroll(1) + flash(1) + help(dynamic)
	reserved := 7 + m.queueHelpLines()
	visibleRows := max(m.height-reserved, 3)
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

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	case tea.WindowSizeMsg:
		return m.handleWindowSizeMsg(msg)
	case tuiTickMsg:
		return m.handleTickMsg(msg)
	case tuiLogTickMsg:
		return m.handleLogTickMsg(msg)
	case tuiJobsMsg:
		return m.handleJobsMsg(msg)
	case tuiStatusMsg:
		return m.handleStatusMsg(msg)
	case tuiUpdateCheckMsg:
		return m.handleUpdateCheckMsg(msg)
	case tuiReviewMsg:
		return m.handleReviewMsg(msg)
	case tuiPromptMsg:
		return m.handlePromptMsg(msg)
	case tuiLogOutputMsg:
		return m.handleLogOutputMsg(msg)
	case tuiAddressedMsg:
		return m.handleAddressedToggleMsg(msg)
	case tuiAddressedResultMsg:
		return m.handleAddressedResultMsg(msg)
	case tuiCancelResultMsg:
		return m.handleCancelResultMsg(msg)
	case tuiRerunResultMsg:
		return m.handleRerunResultMsg(msg)
	case tuiReposMsg:
		return m.handleReposMsg(msg)
	case tuiRepoBranchesMsg:
		return m.handleRepoBranchesMsg(msg)
	case tuiBranchesMsg:
		return m.handleBranchesMsg(msg)
	case tuiCommentResultMsg:
		return m.handleCommentResultMsg(msg)
	case tuiClipboardResultMsg:
		return m.handleClipboardResultMsg(msg)
	case tuiCommitMsgMsg:
		return m.handleCommitMsgMsg(msg)
	case tuiJobsErrMsg:
		return m.handleJobsErrMsg(msg)
	case tuiPaginationErrMsg:
		return m.handlePaginationErrMsg(msg)
	case tuiErrMsg:
		return m.handleErrMsg(msg)
	case tuiReconnectMsg:
		return m.handleReconnectMsg(msg)
	case tuiFixJobsMsg:
		return m.handleFixJobsMsg(msg)
	case tuiFixTriggerResultMsg:
		return m.handleFixTriggerResultMsg(msg)
	case tuiPatchMsg:
		return m.handlePatchResultMsg(msg)
	case tuiApplyPatchResultMsg:
		return m.handleApplyPatchResultMsg(msg)
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
	if m.currentView == tuiViewCommitMsg {
		return m.renderCommitMsgView()
	}
	if m.currentView == tuiViewHelp {
		return m.renderHelpView()
	}
	if m.currentView == tuiViewLog {
		return m.renderLogView()
	}
	if m.currentView == tuiViewTasks {
		return m.renderTasksView()
	}
	if m.currentView == tuiViewWorktreeConfirm {
		return m.renderWorktreeConfirmView()
	}
	if m.currentView == tuiViewPatch {
		return m.renderPatchView()
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
	var title strings.Builder
	fmt.Fprintf(&title, "roborev queue (%s)", version.Version)
	for _, filterType := range m.filterStack {
		switch filterType {
		case filterTypeRepo:
			if len(m.activeRepoFilter) > 0 {
				filterName := m.getDisplayName(m.activeRepoFilter[0], filepath.Base(m.activeRepoFilter[0]))
				fmt.Fprintf(&title, " [f: %s]", filterName)
			}
		case filterTypeBranch:
			if m.activeBranchFilter != "" {
				fmt.Fprintf(&title, " [b: %s]", m.activeBranchFilter)
			}
		}
	}
	if m.hideAddressed {
		title.WriteString(" [hiding addressed]")
	}
	b.WriteString(tuiTitleStyle.Render(title.String()))
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
	// title(1) + status(2) + header(2) + scroll(1) + flash(1) + help(dynamic)
	reservedLines := 7 + m.queueHelpLines()
	visibleRows := max(m.height-reservedLines,
		// Show at least 3 jobs
		3)

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
			start = max(visibleSelectedIdx-visibleRows/2, 0)
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
	b.WriteString(renderHelpTable(m.queueHelpRows(), m.width))
	b.WriteString("\x1b[K") // Clear to end of line (no newline at end)
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
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
		agentWidth = max(
			// Give remainder to agent
			availableWidth-refWidth-branchWidth-repoWidth, 1)
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

		// Show verdict and addressed status on next line (skip verdict for fix jobs)
		hasVerdict := review.Job.Verdict != nil && *review.Job.Verdict != "" && !review.Job.IsFixJob()
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
			fmt.Fprintf(&content, "\n[%s] %s:\n", timestamp, r.Responder)
			content.WriteString(r.Response)
			content.WriteString("\n")
		}
	}

	// Render markdown content with glamour (cached), falling back to plain text wrapping.
	// wrapWidth caps at 100 for readability; maxWidth uses actual terminal width for truncation.
	maxWidth := max(20, m.width-4)
	wrapWidth := min(maxWidth, 100)
	contentStr := content.String()
	var lines []string
	if m.mdCache != nil {
		lines = m.mdCache.getReviewLines(contentStr, wrapWidth, maxWidth, review.ID)
	} else {
		lines = sanitizeLines(wrapText(contentStr, wrapWidth))
	}

	// Compute title line count based on actual title length
	titleLines := 1
	if m.width > 0 && titleLen > m.width {
		titleLines = (titleLen + m.width - 1) / m.width
	}

	// Help table rows
	reviewHelpRows := [][]helpItem{
		{{"p", "prompt"}, {"c", "comment"}, {"m", "commit msg"}, {"a", "addressed"}, {"y", "copy"}, {"F", "fix"}},
		{{"↑/↓", "scroll"}, {"←/→", "prev/next"}, {"?", "commands"}, {"esc", "back"}},
	}
	helpLines := len(reflowHelpRows(reviewHelpRows, m.width))

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
	hasVerdict := review.Job != nil && review.Job.Verdict != nil && *review.Job.Verdict != "" && !review.Job.IsFixJob()
	if hasVerdict || review.Addressed {
		headerHeight++ // Add 1 for verdict/addressed line
	}
	panelReserve := 0
	if m.reviewFixPanelOpen {
		panelReserve = 5 // label + top border + input line + bottom border + help line
	}
	visibleLines := max(m.height-headerHeight-panelReserve, 1)

	// Clamp scroll position to valid range
	maxScroll := max(len(lines)-visibleLines, 0)
	if m.mdCache != nil {
		m.mdCache.lastReviewMaxScroll = maxScroll
	}
	start := max(min(m.reviewScroll, maxScroll), 0)
	end := min(start+visibleLines, len(lines))

	linesWritten := 0
	for i := start; i < end; i++ {
		line := lines[i]
		if m.width > 0 {
			line = xansi.Truncate(line, m.width, "")
		}
		b.WriteString(line)
		b.WriteString("\x1b[K\n") // Clear to end of line before newline
		linesWritten++
	}

	// Pad with clear-to-end-of-line sequences to prevent ghost text
	for linesWritten < visibleLines {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Render inline fix panel when open
	if m.reviewFixPanelOpen {
		innerWidth := max(m.width-4, 18) // box inner width; total visual width = innerWidth+2 (borders)

		if m.reviewFixPanelFocused {
			// Label line
			label := "Fix: enter instructions (or leave blank for default)"
			if runewidth.StringWidth(label) > m.width-1 {
				label = runewidth.Truncate(label, m.width-1, "")
			}
			b.WriteString(label)
			b.WriteString("\x1b[K\n")

			// Input content — show tail so cursor always visible
			inputDisplay := m.fixPromptText
			maxInputLen := innerWidth - 3 // " > " (3) + "_" (1) = 4 overhead, but Width handles right padding
			if runewidth.StringWidth(inputDisplay) > maxInputLen {
				runes := []rune(inputDisplay)
				for runewidth.StringWidth(string(runes)) > maxInputLen {
					runes = runes[1:]
				}
				inputDisplay = string(runes)
			}
			content := fmt.Sprintf(" > %s_", inputDisplay)
			boxStyle := lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.AdaptiveColor{Light: "125", Dark: "205"}). // magenta/pink (active)
				Width(innerWidth)
			for line := range strings.SplitSeq(strings.TrimRight(boxStyle.Render(content), "\n"), "\n") {
				b.WriteString(line)
				b.WriteString("\x1b[K\n")
			}
			b.WriteString(tuiHelpStyle.Render("tab: scroll review | enter: submit | esc: cancel"))
			b.WriteString("\x1b[K\n")
		} else {
			// Label line (dimmed)
			b.WriteString(tuiStatusStyle.Render("Fix (Tab to focus)"))
			b.WriteString("\x1b[K\n")

			inputDisplay := m.fixPromptText
			if inputDisplay == "" {
				inputDisplay = "(blank = default)"
			}
			if runewidth.StringWidth(inputDisplay) > innerWidth-2 {
				inputDisplay = runewidth.Truncate(inputDisplay, innerWidth-2, "")
			}
			content := " " + inputDisplay
			boxStyle := lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}). // gray (inactive)
				Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}).
				Width(innerWidth)
			for line := range strings.SplitSeq(strings.TrimRight(boxStyle.Render(content), "\n"), "\n") {
				b.WriteString(tuiStatusStyle.Render(line))
				b.WriteString("\x1b[K\n")
			}
			b.WriteString(tuiHelpStyle.Render("F: fix | tab: focus fix panel"))
			b.WriteString("\x1b[K\n")
		}
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

	b.WriteString(renderHelpTable(reviewHelpRows, m.width))
	b.WriteString("\x1b[K")
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

	// Render markdown content with glamour (cached), falling back to plain text wrapping.
	// wrapWidth caps at 100 for readability; maxWidth uses actual terminal width for truncation.
	maxWidth := max(20, m.width-4)
	wrapWidth := min(maxWidth, 100)
	var lines []string
	if m.mdCache != nil {
		lines = m.mdCache.getPromptLines(review.Prompt, wrapWidth, maxWidth, review.ID)
	} else {
		lines = sanitizeLines(wrapText(review.Prompt, wrapWidth))
	}

	// Reserve: title + command(0-1) + scroll indicator(1) + help(N) + margin(1)
	promptHelpRows := [][]helpItem{
		{{"↑/↓", "scroll"}, {"←/→", "prev/next"}, {"p", "toggle prompt/review"}, {"?", "commands"}, {"esc", "back"}},
	}
	promptHelpLines := len(reflowHelpRows(promptHelpRows, m.width))
	visibleLines := max(m.height-(2+promptHelpLines)-headerLines, 1)

	// Clamp scroll position to valid range
	maxScroll := max(len(lines)-visibleLines, 0)
	if m.mdCache != nil {
		m.mdCache.lastPromptMaxScroll = maxScroll
	}
	start := max(min(m.promptScroll, maxScroll), 0)
	end := min(start+visibleLines, len(lines))

	linesWritten := 0
	for i := start; i < end; i++ {
		line := lines[i]
		if m.width > 0 {
			line = xansi.Truncate(line, m.width, "")
		}
		b.WriteString(line)
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

	b.WriteString(renderHelpTable(promptHelpRows, m.width))
	b.WriteString("\x1b[K") // Clear help line
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) renderFilterView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("Filter"))
	b.WriteString("\x1b[K\n\x1b[K\n") // Clear title and blank line

	// Show loading state if tree hasn't been built yet
	if m.filterTree == nil {
		b.WriteString(tuiStatusStyle.Render("Loading repos..."))
		b.WriteString("\x1b[K\n")
		// Pad to fill terminal height: title(1) + blank(1) + loading(1) + padding + help(1)
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
	searchDisplay := m.filterSearch
	if searchDisplay == "" {
		searchDisplay = tuiStatusStyle.Render("Type to search...")
	}
	fmt.Fprintf(&b, "Search: %s", searchDisplay)
	b.WriteString("\x1b[K\n\x1b[K\n")

	flatList := m.filterFlatList

	// Calculate visible rows
	filterHelpRows := [][]helpItem{
		{{"↑/↓", "navigate"}, {"→/←", "expand/collapse"}, {"↵", "select"}, {"esc", "cancel"}, {"type to search", ""}},
	}
	filterHelpLines := len(reflowHelpRows(filterHelpRows, m.width))
	// Reserve: title(1) + blank(1) + search(1) + blank(1) + scroll-info(1) + blank(1) + help(N)
	reservedLines := 6 + filterHelpLines
	visibleRows := max(m.height-reservedLines, 0)

	// Determine which rows to show, keeping selected item visible
	start := 0
	end := len(flatList)
	needsScroll := len(flatList) > visibleRows && visibleRows > 0
	if needsScroll {
		start = max(m.filterSelectedIdx-visibleRows/2, 0)
		end = start + visibleRows
		if end > len(flatList) {
			end = len(flatList)
			start = max(end-visibleRows, 0)
		}
	} else if visibleRows > 0 {
		if end > visibleRows {
			end = visibleRows
		}
	} else {
		end = 0
	}

	linesWritten := 0
	for i := start; i < end; i++ {
		entry := flatList[i]
		var line string

		if entry.repoIdx == -1 {
			// "All" row
			line = fmt.Sprintf("  All (%d)", m.filterTreeTotalCount())
		} else if entry.branchIdx == -1 {
			// Repo node
			node := m.filterTree[entry.repoIdx]
			chevron := ">"
			if node.expanded {
				chevron = "v"
			}
			suffix := ""
			if node.loading {
				suffix = " ..."
			}
			line = fmt.Sprintf("  %s %s (%d)%s", chevron, sanitizeForDisplay(node.name), node.count, suffix)
		} else {
			// Branch node (indented)
			node := m.filterTree[entry.repoIdx]
			branch := node.children[entry.branchIdx]
			line = fmt.Sprintf("      %s (%d)", sanitizeForDisplay(branch.name), branch.count)
		}

		if i == m.filterSelectedIdx {
			b.WriteString(tuiSelectedStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	if len(flatList) == 0 {
		b.WriteString(tuiStatusStyle.Render("  No matching items"))
		b.WriteString("\x1b[K\n")
		linesWritten++
	} else if visibleRows == 0 {
		b.WriteString(tuiStatusStyle.Render("  (terminal too small)"))
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	// Pad with clear-to-end-of-line sequences to prevent ghost text
	for linesWritten < visibleRows {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	if needsScroll {
		scrollInfo := fmt.Sprintf("[showing %d-%d of %d]", start+1, end, len(flatList))
		b.WriteString(tuiStatusStyle.Render(scrollInfo))
	}
	b.WriteString("\x1b[K\n")

	b.WriteString(renderHelpTable(filterHelpRows, m.width))
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
	boxWidth := max(m.width-4, 20)

	b.WriteString("+-" + strings.Repeat("-", boxWidth-2) + "-+\n")

	// Wrap text display to box width
	textLinesWritten := 0
	maxTextLines := max(
		// Reserve space for chrome
		m.height-10, 3)

	if m.commentText == "" {
		// Show placeholder (styled, but we pad manually to avoid ANSI issues)
		placeholder := "Type your comment..."
		padded := placeholder + strings.Repeat(" ", boxWidth-2-len(placeholder))
		b.WriteString("| " + tuiStatusStyle.Render(padded) + " |\x1b[K\n")
		textLinesWritten++
	} else {
		lines := strings.SplitSeq(m.commentText, "\n")
		for line := range lines {
			if textLinesWritten >= maxTextLines {
				break
			}
			// Expand tabs to spaces (4-space tabs) for consistent width calculation
			line = strings.ReplaceAll(line, "\t", "    ")
			// Truncate lines that are too long (use visual width for wide characters)
			line = runewidth.Truncate(line, boxWidth-2, "")
			// Pad based on visual width, not rune count
			padding := max(boxWidth-2-runewidth.StringWidth(line), 0)
			fmt.Fprintf(&b, "| %s%s |\x1b[K\n", line, strings.Repeat(" ", padding))
			textLinesWritten++
		}
	}

	// Pad with empty lines if needed
	for textLinesWritten < 3 {
		fmt.Fprintf(&b, "| %-*s |\x1b[K\n", boxWidth-2, "")
		textLinesWritten++
	}

	b.WriteString("+-" + strings.Repeat("-", boxWidth-2) + "-+\x1b[K\n")

	// Pad remaining space
	linesWritten := 6 + textLinesWritten // title, blank, help, blank, top border, bottom border
	for linesWritten < m.height-1 {
		b.WriteString("\x1b[K\n")
		linesWritten++
	}

	b.WriteString(renderHelpTable([][]helpItem{
		{{"↵", "submit"}, {"esc", "cancel"}},
	}, m.width))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
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
	wrapWidth := max(20, min(m.width-4, 100))
	lines := wrapText(m.commitMsgContent, wrapWidth)

	// Reserve: title(1) + scroll indicator(1) + help(1) + margin(1)
	visibleLines := max(m.height-4, 1)

	// Clamp scroll position to valid range
	maxScroll := max(len(lines)-visibleLines, 0)
	start := max(min(m.commitMsgScroll, maxScroll), 0)
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

	b.WriteString(renderHelpTable([][]helpItem{
		{{"↑/↓", "scroll"}, {"esc/q", "back"}},
	}, m.width))
	b.WriteString("\x1b[K") // Clear help line
	b.WriteString("\x1b[J") // Clear to end of screen to prevent artifacts

	return b.String()
}

func (m tuiModel) renderLogView() string {
	var b strings.Builder

	// Title with job info (matches Prompt view format)
	var title string
	job := m.logViewLookupJob()
	if job != nil {
		ref := shortJobRef(*job)
		agentStr := formatAgentLabel(job.Agent, job.Model)
		title = fmt.Sprintf(
			"Log #%d %s (%s)", job.ID, ref, agentStr,
		)
	} else {
		title = fmt.Sprintf("Log #%d", m.logJobID)
	}
	if m.logStreaming {
		title += " " + tuiRunningStyle.Render("● live")
	} else {
		title += " " + tuiDoneStyle.Render("● complete")
	}
	b.WriteString(tuiTitleStyle.Render(title))
	b.WriteString("\x1b[K\n")

	// Show command line below title (dimmed, like Prompt view)
	headerLines := 1
	if cmdLine := commandLineForJob(job); cmdLine != "" {
		cmdText := "Command: " + cmdLine
		if m.width > 0 && runewidth.StringWidth(cmdText) > m.width {
			cmdText = runewidth.Truncate(cmdText, m.width, "…")
		}
		b.WriteString(tuiStatusStyle.Render(cmdText))
		b.WriteString("\x1b[K\n")
		headerLines++
	}

	// Calculate visible area (must match logVisibleLines())
	logHelp := m.logHelpRows()
	logHelpLines := len(reflowHelpRows(logHelp, m.width))
	reservedLines := (2 + logHelpLines) + headerLines // title + cmd(0-1) + sep + status + help(N)
	visibleLines := max(m.height-reservedLines, 1)

	// Clamp scroll
	maxScroll := max(len(m.logLines)-visibleLines, 0)
	scroll := max(min(m.logScroll, maxScroll), 0)

	// Separator
	b.WriteString(strings.Repeat("─", m.width))
	b.WriteString("\x1b[K\n")

	// Render lines (pre-styled by streamFormatter)
	linesWritten := 0
	if len(m.logLines) == 0 {
		if m.logLines == nil {
			b.WriteString(tuiStatusStyle.Render("Waiting for output..."))
		} else {
			b.WriteString(tuiStatusStyle.Render("(no output)"))
		}
		b.WriteString("\x1b[K\n")
		linesWritten++
	} else {
		end := min(scroll+visibleLines, len(m.logLines))
		for i := scroll; i < end; i++ {
			b.WriteString(m.logLines[i].text)
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
	if len(m.logLines) > visibleLines {
		// Calculate actual displayed range (not including padding)
		displayEnd := min(scroll+visibleLines, len(m.logLines))
		status = fmt.Sprintf("[%d-%d of %d lines]", scroll+1, displayEnd, len(m.logLines))
	} else {
		status = fmt.Sprintf("[%d lines]", len(m.logLines))
	}
	if m.logFollow {
		status += " " + tuiRunningStyle.Render("[following]")
	} else {
		status += " " + tuiStatusStyle.Render("[paused - G to follow]")
	}
	b.WriteString(tuiStatusStyle.Render(status))
	b.WriteString("\x1b[K\n")

	b.WriteString(renderHelpTable(logHelp, m.width))
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
				{"l", "View agent log"},
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
				{"F", "Trigger fix for selected review"},
				{"T", "Open Tasks view"},
			},
		},
		{
			group: "Filtering",
			keys: []struct{ key, desc string }{
				{"f", "Filter by repository/branch"},
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
				{"F", "Trigger fix (opens inline panel)"},
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
			group: "Log View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Scroll output"},
				{"←/→", "Previous / next log"},
				{"PgUp/PgDn", "Page through output"},
				{"g", "Toggle follow mode / jump to top"},
				{"x", "Cancel job"},
				{"esc/q", "Back to queue"},
			},
		},
		{
			group: "Tasks View",
			keys: []struct{ key, desc string }{
				{"↑/↓", "Navigate fix jobs"},
				{"A", "Apply patch from completed fix"},
				{"R", "Re-trigger fix (rebase)"},
				{"l", "View agent log"},
				{"x", "Cancel running/queued fix job"},
				{"esc/T", "Back to queue"},
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
	visibleLines := max(m.height-reservedLines, 5)
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
	visibleLines := max(m.height-reservedLines, 5)

	// Clamp scroll
	maxScroll := max(len(allLines)-visibleLines, 0)
	scroll := min(m.helpScroll, maxScroll)

	// Render visible window
	end := min(scroll+visibleLines, len(allLines))
	linesWritten := 0
	for _, line := range allLines[scroll:end] {
		if after, ok := strings.CutPrefix(line, "\x00group:"); ok {
			b.WriteString(tuiSelectedStyle.Render(after))
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

	b.WriteString(renderHelpTable([][]helpItem{
		{{"↑/↓", "scroll"}, {"esc/q/?", "close"}},
	}, m.width))
	b.WriteString("\x1b[K")
	b.WriteString("\x1b[J") // Clear to end of screen

	return b.String()
}

func tuiCmd() *cobra.Command {
	var addr string
	var repoFilter string
	var branchFilter string

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal UI for monitoring reviews",
		Long: `Interactive terminal UI for monitoring reviews.

Use --repo and --branch flags to launch the TUI pre-filtered, useful for
side-by-side working when you want to focus on a specific repo or branch.
When set via flags, the filter is locked and cannot be changed in the TUI.

Without a value, --repo resolves to the current repo and --branch resolves
to the current branch. Use = syntax for explicit values:
  roborev tui --repo                  # current repo
  roborev tui --repo=/path/to/repo    # explicit repo path
  roborev tui --branch                # current branch
  roborev tui --branch=feature-x      # explicit branch name
  roborev tui --repo --branch         # current repo + branch`,
		Args: cobra.NoArgs,
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

			// Resolve --repo and --branch flags
			if cmd.Flags().Changed("repo") {
				resolved, err := resolveRepoFlag(repoFilter)
				if err != nil {
					return fmt.Errorf("--repo: %w", err)
				}
				repoFilter = resolved
			}
			if cmd.Flags().Changed("branch") {
				branchRepo := "."
				if repoFilter != "" {
					branchRepo = repoFilter
				}
				resolved, err := resolveBranchFlag(branchFilter, branchRepo)
				if err != nil {
					return fmt.Errorf("--branch: %w", err)
				}
				branchFilter = resolved
			}

			var tuiOpts []tuiOption
			if repoFilter != "" {
				tuiOpts = append(tuiOpts, WithRepoFilter(repoFilter))
			}
			if branchFilter != "" {
				tuiOpts = append(tuiOpts, WithBranchFilter(branchFilter))
			}
			p := tea.NewProgram(newTuiModel(addr, tuiOpts...), tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "daemon address (default: auto-detect)")

	cmd.Flags().StringVar(&repoFilter, "repo", "", "lock filter to a repo (default: current repo)")
	cmd.Flag("repo").NoOptDefVal = "."

	cmd.Flags().StringVar(&branchFilter, "branch", "", "lock filter to a branch (default: current branch)")
	cmd.Flag("branch").NoOptDefVal = "HEAD"

	return cmd
}

// renderTasksView renders the background fix tasks list.
func (m tuiModel) renderTasksView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("roborev tasks (background fixes)"))
	b.WriteString("\x1b[K\n")

	// Help overlay
	if m.fixShowHelp {
		return m.renderTasksHelpOverlay(&b)
	}

	if len(m.fixJobs) == 0 {
		b.WriteString("\n  No fix tasks. Press F on a review to trigger a background fix.\n")
		b.WriteString("\n")
		b.WriteString(renderHelpTable([][]helpItem{
			{{"T", "back to queue"}, {"F", "fix review"}, {"q", "quit"}},
		}, m.width))
		b.WriteString("\x1b[K\x1b[J")
		return b.String()
	}

	// Column layout: status, job, parent are fixed; ref and subject split remaining space.
	const statusW = 8                                     // "canceled" is the longest
	const idW = 5                                         // "#" + 4-digit number
	const parentW = 11                                    // "fixes #NNNN"
	fixedW := 2 + statusW + 1 + idW + 1 + parentW + 1 + 1 // prefix + inter-column spaces
	flexW := max(m.width-fixedW, 15)
	// Ref gets 25% of flexible space, subject gets 75%
	refW := max(7, flexW*25/100)
	subjectW := max(5, flexW-refW-1)

	// Header
	header := fmt.Sprintf("  %-*s %-*s %-*s %-*s %s",
		statusW, "Status", idW, "Job", parentW, "Parent", refW, "Ref", "Subject")
	b.WriteString(tuiStatusStyle.Render(header))
	b.WriteString("\x1b[K\n")
	b.WriteString("  " + strings.Repeat("-", min(m.width-4, 200)))
	b.WriteString("\x1b[K\n")

	// Render each fix job
	tasksHelpRows := [][]helpItem{
		{{"↵", "view"}, {"p", "patch"}, {"A", "apply"}, {"l", "log"}, {"x", "cancel"}, {"r", "refresh"}, {"?", "help"}, {"T/esc", "back"}},
	}
	tasksHelpLines := len(reflowHelpRows(tasksHelpRows, m.width))
	visibleRows := m.height - (6 + tasksHelpLines) // title + header + separator + status + scroll + help(N)
	visibleRows = max(visibleRows, 1)
	startIdx := 0
	if m.fixSelectedIdx >= visibleRows {
		startIdx = m.fixSelectedIdx - visibleRows + 1
	}

	for i := startIdx; i < len(m.fixJobs) && i < startIdx+visibleRows; i++ {
		job := m.fixJobs[i]

		// Status label
		var statusLabel string
		var statusStyle lipgloss.Style
		switch job.Status {
		case storage.JobStatusQueued:
			statusLabel = "queued"
			statusStyle = tuiQueuedStyle
		case storage.JobStatusRunning:
			statusLabel = "running"
			statusStyle = tuiRunningStyle
		case storage.JobStatusDone:
			statusLabel = "ready"
			statusStyle = tuiDoneStyle
		case storage.JobStatusFailed:
			statusLabel = "failed"
			statusStyle = tuiFailedStyle
		case storage.JobStatusCanceled:
			statusLabel = "canceled"
			statusStyle = tuiCanceledStyle
		case storage.JobStatusApplied:
			statusLabel = "applied"
			statusStyle = tuiDoneStyle
		case storage.JobStatusRebased:
			statusLabel = "rebased"
			statusStyle = tuiCanceledStyle
		}

		parentRef := ""
		if job.ParentJobID != nil {
			parentRef = fmt.Sprintf("fixes #%d", *job.ParentJobID)
		}
		ref := job.GitRef
		if len(ref) > refW {
			ref = ref[:max(1, refW-3)] + "..."
		}
		subject := truncateString(job.CommitSubject, subjectW)

		if i == m.fixSelectedIdx {
			line := fmt.Sprintf("  %-*s #%-4d %-*s %-*s %s",
				statusW, statusLabel, job.ID, parentW, parentRef, refW, ref, subject)
			b.WriteString(tuiSelectedStyle.Render(line))
		} else {
			styledStatus := statusStyle.Render(fmt.Sprintf("%-*s", statusW, statusLabel))
			rest := fmt.Sprintf(" #%-4d %-*s %-*s %s",
				job.ID, parentW, parentRef, refW, ref, subject)
			b.WriteString("  " + styledStatus + rest)
		}
		b.WriteString("\x1b[K\n")
	}

	// Flash message
	if m.flashMessage != "" && time.Now().Before(m.flashExpiresAt) && m.flashView == tuiViewTasks {
		flashStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"})
		b.WriteString(flashStyle.Render(m.flashMessage))
	}
	b.WriteString("\x1b[K\n")

	// Help
	b.WriteString(renderHelpTable(tasksHelpRows, m.width))
	b.WriteString("\x1b[K\x1b[J")

	return b.String()
}

func (m tuiModel) renderTasksHelpOverlay(b *strings.Builder) string {
	help := []string{
		"",
		"  Task Status",
		"    queued     Waiting for a worker to pick up the job",
		"    running    Agent is working in an isolated worktree",
		"    ready      Patch captured and ready to apply to your working tree",
		"    failed     Agent failed (press enter or l to see error details)",
		"    applied    Patch was applied and committed to your working tree",
		"    canceled   Job was canceled by user",
		"",
		"  Keybindings",
		"    enter/l    View review output (ready) or error (failed) or log (running)",
		"    p          View the patch diff for a ready job",
		"    A          Apply patch from a ready job to your working tree",
		"    R          Re-run fix against current HEAD (when patch is stale)",
		"    F          Trigger a new fix from a review (from queue view)",
		"    x          Cancel a queued or running job",
		"    r          Refresh the task list",
		"    T/esc      Return to the main queue view",
		"    ?          Toggle this help",
		"",
		"  Workflow",
		"    1. Press F on a failing review to trigger a background fix",
		"    2. The agent runs in an isolated worktree (your files are untouched)",
		"    3. When status shows 'ready', press A to apply and commit the patch",
		"    4. If the patch is stale (code changed since), press R to re-run",
		"",
	}
	for _, line := range help {
		b.WriteString(line)
		b.WriteString("\x1b[K\n")
	}
	b.WriteString(tuiHelpStyle.Render("?: close help"))
	b.WriteString("\x1b[K\x1b[J")
	return b.String()
}

// fetchPatch fetches the patch for a fix job from the daemon.

func (m tuiModel) renderPatchView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render(fmt.Sprintf("patch for fix job #%d", m.patchJobID)))
	b.WriteString("\x1b[K\n")

	if m.patchText == "" {
		b.WriteString("\n  No patch available.\n")
	} else {
		lines := strings.Split(m.patchText, "\n")
		visibleRows := max(m.height-4, 1)
		maxScroll := max(len(lines)-visibleRows, 0)
		start := max(min(m.patchScroll, maxScroll), 0)
		end := min(start+visibleRows, len(lines))

		addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("34"))   // green
		delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("160"))  // red
		hdrStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("33"))   // blue
		metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // gray

		for _, line := range lines[start:end] {
			display := line
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				display = addStyle.Render(line)
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				display = delStyle.Render(line)
			case strings.HasPrefix(line, "@@"):
				display = hdrStyle.Render(line)
			case strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") ||
				strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++"):
				display = metaStyle.Render(line)
			}
			b.WriteString("  " + display)
			b.WriteString("\x1b[K\n")
		}

		if len(lines) > visibleRows {
			pct := 0
			if maxScroll > 0 {
				pct = start * 100 / maxScroll
			}
			b.WriteString(tuiHelpStyle.Render(fmt.Sprintf("  [%d%%]", pct)))
			b.WriteString("\x1b[K\n")
		}
	}

	b.WriteString(renderHelpTable([][]helpItem{
		{{"j/k/↑/↓", "scroll"}, {"esc", "back to tasks"}},
	}, m.width))
	b.WriteString("\x1b[K\x1b[J")
	return b.String()
}

// renderWorktreeConfirmView renders the worktree creation confirmation modal.
func (m tuiModel) renderWorktreeConfirmView() string {
	var b strings.Builder

	b.WriteString(tuiTitleStyle.Render("Create Worktree"))
	b.WriteString("\x1b[K\n\n")

	fmt.Fprintf(&b, "  Branch %q is not checked out anywhere.\n", m.worktreeConfirmBranch)
	b.WriteString("  Create a temporary worktree to apply and commit the patch?\n\n")
	b.WriteString("  The worktree will be removed after the commit.\n")
	b.WriteString("  The commit will persist on the branch.\n\n")

	b.WriteString(tuiHelpStyle.Render("y/enter: create worktree and apply | esc/n: cancel"))
	b.WriteString("\x1b[K\x1b[J")

	return b.String()
}

// truncateString is defined in fix.go
