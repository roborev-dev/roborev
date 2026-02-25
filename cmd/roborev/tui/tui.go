package tui

import (
	"errors"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/streamfmt"
)

// Tick intervals for adaptive polling
const (
	tickIntervalActive = 2 * time.Second  // Poll frequently when jobs are running/pending
	tickIntervalIdle   = 10 * time.Second // Poll less when queue is idle
)

// TUI styles using AdaptiveColor for light/dark terminal support.
// Light colors are chosen for dark-on-light terminals; Dark colors for light-on-dark.
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "125", Dark: "205"}) // Magenta/Pink

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}) // Gray

	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "153", Dark: "24"}) // Light blue background

	queuedStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "226"}) // Yellow/Gold
	runningStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "25", Dark: "33"})   // Blue
	doneStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"})   // Green
	failedStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}) // Red
	canceledStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "166", Dark: "208"}) // Orange

	passStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "46"})   // Green
	failStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "196"}) // Red
	addressedStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "30", Dark: "51"})   // Cyan

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}) // Gray

	helpKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}) // Gray (matches status/scroll text)
	helpDescStyle = lipgloss.NewStyle().
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
				styled[i] = helpKeyStyle.Render(item.key) + " " + helpDescStyle.Render(item.desc)
			} else {
				styled[i] = helpKeyStyle.Render(item.key)
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

type model struct {
	serverAddr       string
	daemonVersion    string
	client           *http.Client
	glamourStyle     gansi.StyleConfig // detected once at init
	jobs             []storage.ReviewJob
	jobStats         storage.JobStats // aggregate done/addressed/unaddressed from server
	status           storage.DaemonStatus
	selectedIdx      int
	selectedJobID    int64 // Track selected job by ID to maintain position on refresh
	currentView      viewKind
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
	hasMore        bool     // true if there are more jobs to load
	loadingMore    bool     // true if currently loading more jobs (pagination)
	loadingJobs    bool     // true if currently loading jobs (full refresh)
	heightDetected bool     // true after first WindowSizeMsg (real terminal height known)
	fetchSeq       int      // incremented on filter changes; stale fetch responses are discarded
	paginateNav    viewKind // non-zero: auto-navigate in this view after pagination loads

	// Unified tree filter modal state
	filterTree        []treeFilterNode  // Tree of repos (each may have branch children)
	filterFlatList    []flatFilterEntry // Flattened visible rows for navigation
	filterSelectedIdx int               // Currently highlighted row in flat list
	filterSearch      string            // Search/filter text typed by user
	filterSearchSeq   int               // Incremented on search text changes; gates stale fetchFailed
	filterBranchMode  bool              // True when opened via 'b' key (auto-expand repo to branches)

	// Comment modal state
	commentText     string   // The response text being typed
	commentJobID    int64    // Job ID we're responding to
	commentCommit   string   // Short commit SHA for display
	commentFromView viewKind // View to return to after comment modal closes

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
	flashView      viewKind // View where flash was triggered (only show in same view)

	// Track config reload notifications
	lastConfigReloadCounter uint64                 // Last known ConfigReloadCounter from daemon status
	statusFetchedOnce       bool                   // True after first successful status fetch (for flash logic)
	pendingReviewAddressed  map[int64]pendingState // review ID -> pending state (for reviews without jobs)
	addressedSeq            uint64                 // monotonic counter for request sequencing

	// Daemon reconnection state
	consecutiveErrors int  // Count of consecutive connection failures
	reconnecting      bool // True if currently attempting reconnection

	// Commit message view state
	commitMsgContent  string   // Formatted commit message(s) content
	commitMsgScroll   int      // Scroll position in commit message view
	commitMsgJobID    int64    // Job ID for the commit message being viewed
	commitMsgFromView viewKind // View to return to after closing commit message view

	// Help view state
	helpFromView viewKind // View to return to after closing help
	helpScroll   int      // Scroll position in help view

	// Log view state
	logJobID     int64                // Job being viewed
	logLines     []logLine            // Buffer of output lines
	logScroll    int                  // Scroll position
	logStreaming bool                 // True if job is still running
	logFromView  viewKind             // View to return to
	logFollow    bool                 // True if auto-scrolling to bottom (follow mode)
	logOffset    int64                // Byte offset for next incremental fetch
	logFmtr      *streamfmt.Formatter // Persistent formatter across polls
	logLoading   bool                 // True while a fetch is in-flight
	logFetchSeq  uint64               // Monotonic seq to drop stale responses

	// Glamour markdown render cache (pointer so View's value receiver can update it)
	mdCache *markdownCache

	clipboard ClipboardWriter

	// Review view navigation
	reviewFromView viewKind // View to return to when exiting review (queue or tasks)

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

func newModel(serverAddr string, opts ...option) model {
	var opt options
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

	return model{
		serverAddr:             serverAddr,
		daemonVersion:          daemonVersion,
		client:                 &http.Client{Timeout: 10 * time.Second},
		glamourStyle:           streamfmt.GlamourStyle(),
		jobs:                   []storage.ReviewJob{},
		currentView:            viewQueue,
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

func (m model) Init() tea.Cmd {
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
func (m *model) getDisplayName(repoPath, defaultName string) string {
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
func (m *model) updateDisplayNameCache(jobs []storage.ReviewJob) {
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
func (m *model) getBranchForJob(job storage.ReviewJob) string {
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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	case tea.WindowSizeMsg:
		return m.handleWindowSizeMsg(msg)
	case tickMsg:
		return m.handleTickMsg(msg)
	case logTickMsg:
		return m.handleLogTickMsg(msg)
	case jobsMsg:
		return m.handleJobsMsg(msg)
	case statusMsg:
		return m.handleStatusMsg(msg)
	case updateCheckMsg:
		return m.handleUpdateCheckMsg(msg)
	case reviewMsg:
		return m.handleReviewMsg(msg)
	case promptMsg:
		return m.handlePromptMsg(msg)
	case logOutputMsg:
		return m.handleLogOutputMsg(msg)
	case addressedMsg:
		return m.handleAddressedToggleMsg(msg)
	case addressedResultMsg:
		return m.handleAddressedResultMsg(msg)
	case cancelResultMsg:
		return m.handleCancelResultMsg(msg)
	case rerunResultMsg:
		return m.handleRerunResultMsg(msg)
	case reposMsg:
		return m.handleReposMsg(msg)
	case repoBranchesMsg:
		return m.handleRepoBranchesMsg(msg)
	case branchesMsg:
		return m.handleBranchesMsg(msg)
	case commentResultMsg:
		return m.handleCommentResultMsg(msg)
	case clipboardResultMsg:
		return m.handleClipboardResultMsg(msg)
	case commitMsgMsg:
		return m.handleCommitMsgMsg(msg)
	case jobsErrMsg:
		return m.handleJobsErrMsg(msg)
	case paginationErrMsg:
		return m.handlePaginationErrMsg(msg)
	case errMsg:
		return m.handleErrMsg(msg)
	case reconnectMsg:
		return m.handleReconnectMsg(msg)
	case fixJobsMsg:
		return m.handleFixJobsMsg(msg)
	case fixTriggerResultMsg:
		return m.handleFixTriggerResultMsg(msg)
	case patchMsg:
		return m.handlePatchResultMsg(msg)
	case applyPatchResultMsg:
		return m.handleApplyPatchResultMsg(msg)
	}
	return m, nil
}

func (m model) View() string {
	if m.currentView == viewKindComment {
		return m.renderRespondView()
	}
	if m.currentView == viewFilter {
		return m.renderFilterView()
	}
	if m.currentView == viewCommitMsg {
		return m.renderCommitMsgView()
	}
	if m.currentView == viewHelp {
		return m.renderHelpView()
	}
	if m.currentView == viewLog {
		return m.renderLogView()
	}
	if m.currentView == viewTasks {
		return m.renderTasksView()
	}
	if m.currentView == viewKindWorktreeConfirm {
		return m.renderWorktreeConfirmView()
	}
	if m.currentView == viewPatch {
		return m.renderPatchView()
	}
	if m.currentView == viewKindPrompt && m.currentReview != nil {
		return m.renderPromptView()
	}
	if m.currentView == viewReview && m.currentReview != nil {
		return m.renderReviewView()
	}
	return m.renderQueueView()
}

// helpLines builds the help content lines from shortcut definitions.

// helpMaxScroll returns the maximum scroll offset for the help view.

// Config holds resolved parameters for running the TUI.
type Config struct {
	ServerAddr   string
	RepoFilter   string
	BranchFilter string
}

// Run starts the interactive TUI.
func Run(cfg Config) error {
	var opts []option
	if cfg.RepoFilter != "" {
		opts = append(opts, withRepoFilter(cfg.RepoFilter))
	}
	if cfg.BranchFilter != "" {
		opts = append(opts, withBranchFilter(cfg.BranchFilter))
	}
	p := tea.NewProgram(
		newModel(cfg.ServerAddr, opts...),
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}

// renderTasksView renders the background fix tasks list.

// fetchPatch fetches the patch for a fix job from the daemon.

// renderWorktreeConfirmView renders the worktree creation confirmation modal.
