package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/atotto/clipboard"
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
	"github.com/roborev-dev/roborev/internal/update"
	"github.com/roborev-dev/roborev/internal/version"
	"github.com/roborev-dev/roborev/internal/worktree"
	godiff "github.com/sourcegraph/go-diff/diff"
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

// helpItem is a single help-bar entry with a key label and description.
type helpItem struct {
	key  string
	desc string
}

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

type tuiView int

const (
	tuiViewQueue tuiView = iota
	tuiViewReview
	tuiViewPrompt
	tuiViewFilter
	tuiViewComment
	tuiViewCommitMsg
	tuiViewHelp
	tuiViewLog
	tuiViewTasks           // Background fix tasks view
	tuiViewWorktreeConfirm // Confirm creating a worktree to apply patch
	tuiViewPatch           // Patch viewer for fix jobs
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

// treeFilterNode represents a repo node in the unified tree filter
type treeFilterNode struct {
	name          string             // Display name
	rootPaths     []string           // Repo paths (for API calls)
	count         int                // Total job count
	expanded      bool               // Whether children are visible
	userCollapsed bool               // User explicitly collapsed during search
	children      []branchFilterItem // Branch children (lazy-loaded)
	loading       bool               // True while branch fetch is in-flight
	fetchFailed   bool               // True after a search-triggered fetch failed
}

// flatFilterEntry represents a single row in the flattened tree filter view
type flatFilterEntry struct {
	repoIdx   int // Index into filterTree (-1 for "All")
	branchIdx int // Index into children (-1 for repo-level)
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

// pendingState tracks a pending addressed toggle with sequence number
type pendingState struct {
	newState bool
	seq      uint64
}

// logLine represents a single pre-rendered line of agent output in the log view.
// Text is already styled via streamFormatter (ANSI codes included).
type logLine struct {
	text string
}

// tuiLogOutputMsg delivers output lines from the daemon
type tuiLogOutputMsg struct {
	lines     []logLine
	hasMore   bool // true if job is still running
	err       error
	newOffset int64            // byte offset for next fetch
	append    bool             // true = append lines, false = replace
	seq       uint64           // fetch sequence number for stale detection
	fmtr      *streamFormatter // formatter used for rendering (persist for incremental reuse)
}

// tuiLogTickMsg triggers a refresh of the log output
type tuiLogTickMsg struct{}

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
	repos []repoFilterItem
}
type tuiBranchesMsg struct {
	backfillCount int // Number of branches successfully backfilled to the database
}
type tuiRepoBranchesMsg struct {
	repoIdx      int                // Which repo in filterTree
	rootPaths    []string           // Repo identity (for stale message detection)
	branches     []branchFilterItem // Branch data
	err          error              // Non-nil on fetch failure
	expandOnLoad bool               // Set expanded=true when branches arrive
	searchSeq    int                // Search generation; stale errors don't set fetchFailed
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

type tuiFixJobsMsg struct {
	jobs []storage.ReviewJob
	err  error
}

type tuiFixTriggerResultMsg struct {
	job     *storage.ReviewJob
	err     error
	warning string // non-fatal issue (e.g. failed to mark stale job)
}

type tuiApplyPatchResultMsg struct {
	jobID        int64
	parentJobID  int64 // Parent review job (to mark addressed on success)
	success      bool
	commitFailed bool // True only when patch applied but git commit failed (working tree is dirty)
	err          error
	rebase       bool   // True if patch didn't apply and needs rebase
	needWorktree bool   // True if branch is not checked out and needs a worktree
	branch       string // Branch name (for worktree creation prompt)
	worktreeDir  string // Non-empty if a temp worktree was kept for recovery
}

type tuiPatchMsg struct {
	jobID int64
	patch string
	err   error
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

// tuiOption func(*tuiOptions) is a functional option for TUI.
type tuiOption func(*tuiOptions)

// WithRepoFilter locks the TUI filter to a specific repo.
func WithRepoFilter(repo string) tuiOption {
	return func(o *tuiOptions) { o.repoFilter = repo }
}

// WithBranchFilter locks the TUI filter to a specific branch.
func WithBranchFilter(branch string) tuiOption {
	return func(o *tuiOptions) { o.branchFilter = branch }
}

// WithExternalIODisabled disables daemon/config/git calls in newTuiModel.
func WithExternalIODisabled() tuiOption {
	return func(o *tuiOptions) { o.disableExternalIO = true }
}

// tuiOptions holds optional overrides for the TUI model, set from CLI flags.
type tuiOptions struct {
	repoFilter        string // --repo flag: lock filter to this repo path
	branchFilter      string // --branch flag: lock filter to this branch
	disableExternalIO bool   // tests: disable daemon/config/git calls
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
		params.Set("exclude_job_type", "fix")
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
		return tuiReposMsg{repos: repos}
	}
}

// fetchBranchesForRepo fetches branches for a specific repo in the tree filter.
// Returns tuiRepoBranchesMsg with the branch data (or err set on failure).
// When expand is true, the handler sets expanded=true on the tree node.
// searchSeq is the search generation at dispatch time; the error handler
// uses it to avoid marking fetchFailed for stale search sessions.
func (m tuiModel) fetchBranchesForRepo(
	rootPaths []string, repoIdx int, expand bool, searchSeq int,
) tea.Cmd {
	client := m.client
	serverAddr := m.serverAddr

	errMsg := func(err error) tuiRepoBranchesMsg {
		return tuiRepoBranchesMsg{
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

		return tuiRepoBranchesMsg{
			repoIdx:      repoIdx,
			rootPaths:    rootPaths,
			branches:     branches,
			expandOnLoad: expand,
			searchSeq:    searchSeq,
		}
	}
}

func (m tuiModel) backfillBranches() tea.Cmd {
	// Capture values for use in goroutine
	machineID := m.status.MachineID
	client := m.client
	serverAddr := m.serverAddr

	return func() tea.Msg {
		var backfillCount int

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

		return tuiBranchesMsg{backfillCount: backfillCount}
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

// errNoLog is a sentinel error returned when the job log API
// returns 404 (no log file on disk for the requested job).
var errNoLog = errors.New("no log available")

// fetchJobLog fetches raw JSONL from /api/job/log, renders it
// through streamFormatter, and returns pre-styled logLines.
// Uses incremental fetching: only new bytes since logOffset are
// downloaded and rendered, reusing the persistent logFmtr state.
func (m tuiModel) fetchJobLog(jobID int64) tea.Cmd {
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
			return tuiLogOutputMsg{err: err, seq: seq}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return tuiLogOutputMsg{err: errNoLog, seq: seq}
		}
		if resp.StatusCode != http.StatusOK {
			return tuiLogOutputMsg{
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
			return tuiLogOutputMsg{
				hasMore:   hasMore,
				newOffset: newOffset,
				append:    true,
				seq:       seq,
			}
		}

		// Render JSONL through streamFormatter. Use pre-computed
		// glamour style to avoid terminal queries from goroutine.
		var buf bytes.Buffer
		var renderFmtr *streamFormatter
		if isIncremental {
			// Reuse persistent formatter — redirect its output
			// to a fresh buffer for this batch only.
			fmtr.w = &buf
			renderFmtr = fmtr
		} else {
			renderFmtr = newStreamFormatterWithWidth(
				&buf, width, style,
			)
		}

		if err := renderJobLogWith(
			resp.Body, renderFmtr, &buf,
		); err != nil {
			return tuiLogOutputMsg{err: err, seq: seq}
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

		return tuiLogOutputMsg{
			lines:     lines,
			hasMore:   hasMore,
			newOffset: newOffset,
			append:    isIncremental,
			seq:       seq,
			fmtr:      renderFmtr,
		}
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
			// Truncate SHA if it's a full 40-char hex SHA (not a range or branch name)
			if fullSHAPattern.MatchString(gitRef) {
				gitRef = git.ShortSHA(gitRef)
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
		err := m.clipboard.WriteText(content)
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
		err = m.clipboard.WriteText(content)
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

			return tuiCommitMsgMsg{jobID: jobID, content: sanitizeForDisplay(content.String())}
		}

		// Single commit
		info, err := git.GetCommitInfo(job.RepoPath, job.GitRef)
		if err != nil {
			return tuiCommitMsgMsg{jobID: jobID, err: err}
		}

		var content strings.Builder
		fmt.Fprintf(&content, "Commit: %s\n", info.SHA)
		fmt.Fprintf(&content, "Author: %s\n", info.Author)
		fmt.Fprintf(&content, "Date:   %s\n\n", info.Timestamp.Format("2006-01-02 15:04:05 -0700"))
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
	err := m.postJSON("/api/review/address", map[string]any{
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
		newState := currentState == nil || !*currentState

		if err := m.postAddressed(jobID, newState, "no review for this job"); err != nil {
			return tuiErrMsg(err)
		}
		return tuiAddressedMsg(newState)
	}
}

// markParentAddressed marks the parent review job as addressed after a fix is applied.
func (m tuiModel) markParentAddressed(parentJobID int64) tea.Cmd {
	return func() tea.Msg {
		err := m.postAddressed(parentJobID, true, "parent review not found")
		if err != nil {
			return tuiErrMsg(err)
		}
		return nil
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

// findNextLoggableJob finds the next job that has a log
// (running, done, or failed). Respects active filters.
func (m *tuiModel) findNextLoggableJob() int {
	for i := m.selectedIdx + 1; i < len(m.jobs); i++ {
		job := m.jobs[i]
		if job.Status != storage.JobStatusQueued &&
			m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// findPrevLoggableJob finds the previous job that has a log
// (running, done, or failed). Respects active filters.
func (m *tuiModel) findPrevLoggableJob() int {
	for i := m.selectedIdx - 1; i >= 0; i-- {
		job := m.jobs[i]
		if job.Status != storage.JobStatusQueued &&
			m.isJobVisible(job) {
			return i
		}
	}
	return -1
}

// findNextLoggableFixJob finds the next fix job that has a log.
func (m *tuiModel) findNextLoggableFixJob() int {
	for i := m.fixSelectedIdx + 1; i < len(m.fixJobs); i++ {
		if m.fixJobs[i].Status != storage.JobStatusQueued {
			return i
		}
	}
	return -1
}

// findPrevLoggableFixJob finds the previous fix job that has a
// log.
func (m *tuiModel) findPrevLoggableFixJob() int {
	for i := m.fixSelectedIdx - 1; i >= 0; i-- {
		if m.fixJobs[i].Status != storage.JobStatusQueued {
			return i
		}
	}
	return -1
}

// logViewLookupJob finds the job being viewed in the log view.
// Searches m.jobs first, then m.fixJobs for jobs opened from
// the tasks view.
func (m *tuiModel) logViewLookupJob() *storage.ReviewJob {
	for i := range m.jobs {
		if m.jobs[i].ID == m.logJobID {
			return &m.jobs[i]
		}
	}
	for i := range m.fixJobs {
		if m.fixJobs[i].ID == m.logJobID {
			return &m.fixJobs[i]
		}
	}
	return nil
}

// logVisibleLines returns the number of content lines visible in the
// log view, accounting for title, optional command line, separator,
// status, and help bar.
func (m *tuiModel) logVisibleLines() int {
	// title + separator + status + help(N)
	helpRows := m.logHelpRows()
	reserved := 3 + len(reflowHelpRows(helpRows, m.width))
	// Check if command line header is shown
	if job := m.logViewLookupJob(); job != nil {
		if commandLineForJob(job) != "" {
			reserved++
		}
	}
	return max(m.height-reserved, 1)
}

// logHelpRows returns the help row items for the log view.
func (m *tuiModel) logHelpRows() [][]helpItem {
	helpRow := []helpItem{
		{"↑/↓", "scroll"}, {"←/→", "prev/next"}, {"g", "toggle top/bottom"},
	}
	if m.logStreaming {
		helpRow = append(helpRow, helpItem{"x", "cancel"})
	}
	helpRow = append(helpRow, helpItem{"esc/q", "back"})
	return [][]helpItem{helpRow}
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
		err := m.postJSON("/api/job/cancel", map[string]any{"job_id": jobID}, nil)
		return tuiCancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: err}
	}
}

// rerunJob sends a rerun request to the server for failed/canceled jobs
func (m tuiModel) rerunJob(jobID int64, oldStatus storage.JobStatus, oldStartedAt, oldFinishedAt *time.Time, oldError string) tea.Cmd {
	return func() tea.Msg {
		err := m.postJSON("/api/job/rerun", map[string]any{"job_id": jobID}, nil)
		return tuiRerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: err}
	}
}

// moveToFront moves the first element matching the predicate to position 0,
// shifting the preceding elements down by one. No-op if no match or match is already first.
func moveToFront[T any](items []T, match func(T) bool) {
	for i := 1; i < len(items); i++ {
		if match(items[i]) {
			m := items[i]
			copy(items[1:i+1], items[0:i])
			items[0] = m
			return
		}
	}
}

// rebuildFilterFlatList rebuilds the flat list of visible filter entries from the tree.
// When search is active, auto-expands repos with matching branches and hides non-matching items.
// Clamps filterSelectedIdx after rebuild.
func (m *tuiModel) rebuildFilterFlatList() {
	var flat []flatFilterEntry
	search := strings.ToLower(m.filterSearch)

	// Reset search state when not searching
	if search == "" {
		for i := range m.filterTree {
			m.filterTree[i].userCollapsed = false
			m.filterTree[i].fetchFailed = false
		}
	}

	// Always include "All" as first entry
	if search == "" || strings.Contains("all", search) {
		flat = append(flat, flatFilterEntry{repoIdx: -1, branchIdx: -1})
	}

	for i, node := range m.filterTree {
		repoNameMatch := search == "" ||
			strings.Contains(strings.ToLower(node.name), search)
		// Also check repo path basenames
		if !repoNameMatch {
			for _, p := range node.rootPaths {
				if strings.Contains(strings.ToLower(filepath.Base(p)), search) {
					repoNameMatch = true
					break
				}
			}
		}

		// Check if any children match the search
		childMatches := false
		if search != "" && !repoNameMatch {
			for _, child := range node.children {
				if strings.Contains(strings.ToLower(child.name), search) {
					childMatches = true
					break
				}
			}
		}

		if !repoNameMatch && !childMatches {
			continue
		}

		// Add repo node
		flat = append(flat, flatFilterEntry{repoIdx: i, branchIdx: -1})

		// Add children if expanded or if search matched children
		// (user can override search auto-expansion via left-arrow)
		showChildren := node.expanded ||
			(search != "" && childMatches && !node.userCollapsed)
		if showChildren && len(node.children) > 0 {
			for j, child := range node.children {
				if search != "" && !repoNameMatch {
					// Only show matching children when repo didn't match
					if !strings.Contains(strings.ToLower(child.name), search) {
						continue
					}
				}
				flat = append(flat, flatFilterEntry{repoIdx: i, branchIdx: j})
			}
		}
	}

	m.filterFlatList = flat

	// Clamp selection
	if len(flat) == 0 {
		m.filterSelectedIdx = 0
	} else if m.filterSelectedIdx >= len(flat) {
		m.filterSelectedIdx = len(flat) - 1
	}
}

// filterNavigateUp moves selection up in the tree filter
func (m *tuiModel) filterNavigateUp() {
	if m.filterSelectedIdx > 0 {
		m.filterSelectedIdx--
	}
}

// filterNavigateDown moves selection down in the tree filter
func (m *tuiModel) filterNavigateDown() {
	if m.filterSelectedIdx < len(m.filterFlatList)-1 {
		m.filterSelectedIdx++
	}
}

// getSelectedFilterEntry returns the currently selected flat entry, or nil
func (m *tuiModel) getSelectedFilterEntry() *flatFilterEntry {
	if m.filterSelectedIdx >= 0 && m.filterSelectedIdx < len(m.filterFlatList) {
		return &m.filterFlatList[m.filterSelectedIdx]
	}
	return nil
}

// filterTreeTotalCount returns the total job count across all repos in the tree
func (m *tuiModel) filterTreeTotalCount() int {
	total := 0
	for _, node := range m.filterTree {
		total += node.count
	}
	return total
}

// rootPathsMatch returns true if two rootPaths slices contain the
// same paths (order-independent). This handles the case where the
// tree is rebuilt with a different path ordering while a branch
// fetch is in-flight.
func rootPathsMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) <= 1 {
		return len(a) == 0 || a[0] == b[0]
	}
	as := make([]string, len(a))
	bs := make([]string, len(b))
	copy(as, a)
	copy(bs, b)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// repoMatchesFilter checks if a repo path matches the active filter.
func (m tuiModel) repoMatchesFilter(repoPath string) bool {
	return slices.Contains(m.activeRepoFilter, repoPath)
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

// popFilter walks the stack from end to start, removes the first
// unlocked filter, clears its value, and returns the filter type.
// Returns empty string if no unlocked filter exists.
func (m *tuiModel) popFilter() string {
	for i := len(m.filterStack) - 1; i >= 0; i-- {
		ft := m.filterStack[i]
		if ft == filterTypeRepo && m.lockedRepoFilter {
			continue
		}
		if ft == filterTypeBranch && m.lockedBranchFilter {
			continue
		}
		m.filterStack = append(
			m.filterStack[:i], m.filterStack[i+1:]...,
		)
		switch ft {
		case filterTypeRepo:
			m.activeRepoFilter = nil
		case filterTypeBranch:
			m.activeBranchFilter = ""
		}
		return ft
	}
	return ""
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

		// Width change in log view requires full re-render
		// because streamFormatter is width-aware.
		if m.currentView == tuiViewLog && m.logLines != nil {
			m.logOffset = 0
			m.logLines = nil
			m.logFmtr = newStreamFormatterWithWidth(
				io.Discard, msg.Width, m.glamourStyle,
			)
			m.logFetchSeq++
			m.logLoading = true
			return m, m.fetchJobLog(m.logJobID)
		}

	case tuiTickMsg:
		// Skip job refresh while pagination or another refresh is in flight
		if m.loadingMore || m.loadingJobs {
			return m, tea.Batch(m.tick(), m.fetchStatus())
		}
		cmds := []tea.Cmd{m.tick(), m.fetchJobs(), m.fetchStatus()}
		// Refresh fix jobs when viewing tasks or when fix jobs are in progress
		if m.currentView == tuiViewTasks || m.hasActiveFixJobs() {
			cmds = append(cmds, m.fetchFixJobs())
		}
		return m, tea.Batch(cmds...)

	case tuiLogTickMsg:
		if m.currentView == tuiViewLog && m.logStreaming &&
			m.logJobID > 0 && !m.logLoading {
			m.logLoading = true
			return m, m.fetchJobLog(m.logJobID)
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
			// Stale fetch -- clear pending fix panel if it was
			// for this (now-discarded) review.
			if m.reviewFixPanelPending && m.fixPromptJobID == msg.jobID {
				m.reviewFixPanelPending = false
				m.fixPromptJobID = 0
			}
			return m, nil
		}
		m.consecutiveErrors = 0
		m.currentReview = msg.review
		m.currentResponses = msg.responses
		m.currentBranch = msg.branchName
		m.currentView = tuiViewReview
		m.reviewScroll = 0
		if m.reviewFixPanelPending && m.fixPromptJobID == msg.review.JobID {
			m.reviewFixPanelPending = false
			m.reviewFixPanelOpen = true
			m.reviewFixPanelFocused = true
		}

	case tuiPromptMsg:
		if msg.jobID != m.selectedJobID {
			return m, nil
		}
		m.consecutiveErrors = 0
		m.currentReview = msg.review
		m.currentView = tuiViewPrompt
		m.promptScroll = 0

	case tuiLogOutputMsg:
		// Drop stale responses from previous log sessions.
		// Clear logLoading only for the current seq — stale
		// responses must not affect the in-flight guard.
		if msg.seq != m.logFetchSeq {
			return m, nil
		}
		m.logLoading = false
		m.consecutiveErrors = 0
		// If the user navigated away from the log view while
		// a fetch was in-flight, drop the result silently.
		if m.currentView != tuiViewLog {
			return m, nil
		}
		if msg.err != nil {
			if errors.Is(msg.err, errNoLog) {
				// For failed jobs with stored error, show
				// that instead of generic "No log available".
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
		if m.currentView == tuiViewLog {
			// Persist formatter state so incremental polls
			// reuse accumulated state (lastWasTool, rendered
			// command IDs, etc.).
			if msg.fmtr != nil {
				m.logFmtr = msg.fmtr
			}

			if msg.append {
				// Incremental: append new lines if any.
				if len(msg.lines) > 0 {
					m.logLines = append(
						m.logLines, msg.lines...,
					)
				}
			} else if len(msg.lines) > 0 {
				// Full replace with content.
				m.logLines = msg.lines
			} else if m.logLines == nil {
				if !msg.hasMore {
					// Completed job with empty log.
					m.logLines = []logLine{}
				}
				// Else: still streaming, no data yet — keep
				// nil so UI shows "Waiting for output...".
			} else if msg.newOffset == 0 {
				// Server reset offset to 0 with no content —
				// clear stale lines from previous log state.
				m.logLines = []logLine{}
			}
			// Else: replace mode, no lines, but logLines exists
			// and offset nonzero — preserve existing lines
			// (e.g. final streaming poll with no new data).
			m.logOffset = msg.newOffset
			m.logStreaming = msg.hasMore
			if m.logFollow && len(m.logLines) > 0 {
				visibleLines := m.logVisibleLines()
				maxScroll := max(len(m.logLines)-visibleLines, 0)
				m.logScroll = maxScroll
			}
			if m.logStreaming {
				return m, tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
					return tuiLogTickMsg{}
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
					rootPathsMatch(m.filterTree[entry.repoIdx].rootPaths, m.activeRepoFilter) {
					m.filterSelectedIdx = i
					break
				}
			}
		}
		// Auto-expand repo to branches when opened via 'b' key
		if m.filterBranchMode && len(m.filterTree) > 0 {
			targetIdx := 0
			if len(m.activeRepoFilter) > 0 {
				// Priority 1: active repo filter (any size)
				for i, node := range m.filterTree {
					if rootPathsMatch(node.rootPaths, m.activeRepoFilter) {
						targetIdx = i
						goto foundTarget
					}
				}
			}
			if m.cwdRepoRoot != "" {
				// Priority 2: cwd repo
				for i, node := range m.filterTree {
					for _, p := range node.rootPaths {
						if p == m.cwdRepoRoot {
							targetIdx = i
							goto foundTarget
						}
					}
				}
			}
			// Priority 3: first repo (targetIdx already 0)
		foundTarget:
			m.filterTree[targetIdx].loading = true
			// Position cursor on the target repo while branches load
			for i, entry := range m.filterFlatList {
				if entry.repoIdx == targetIdx && entry.branchIdx == -1 {
					m.filterSelectedIdx = i
					break
				}
			}
			return m, m.fetchBranchesForRepo(m.filterTree[targetIdx].rootPaths, targetIdx, true, m.filterSearchSeq)
		}
		// If user typed search before repos loaded, kick off branch
		// fetches now so search results include branch matches.
		if cmd := m.fetchUnloadedBranches(); cmd != nil {
			return m, cmd
		}

	case tuiRepoBranchesMsg:
		// Surface errors regardless of view/staleness so connection
		// tracking and reconnect logic always fire.
		if msg.err != nil {
			m.err = msg.err
			m.filterBranchMode = false
			// Clear loading if the tree entry is still valid.
			// Only mark fetchFailed for search-triggered loads to
			// prevent retry loops; manual failures should not block
			// later search auto-loading.
			if msg.repoIdx >= 0 && msg.repoIdx < len(m.filterTree) &&
				rootPathsMatch(m.filterTree[msg.repoIdx].rootPaths, msg.rootPaths) {
				m.filterTree[msg.repoIdx].loading = false
				if !msg.expandOnLoad && m.filterSearch != "" &&
					msg.searchSeq == m.filterSearchSeq {
					m.filterTree[msg.repoIdx].fetchFailed = true
				}
			}
			if cmd := m.handleConnectionError(msg.err); cmd != nil {
				return m, cmd
			}
			// Top-up: fetch next batch of unloaded repos
			return m, m.fetchUnloadedBranches()
		}
		// Verify we're still in filter view, repoIdx is valid, and the repo identity matches
		// (the tree may have been rebuilt while the fetch was in-flight)
		if m.currentView == tuiViewFilter && msg.repoIdx >= 0 && msg.repoIdx < len(m.filterTree) &&
			rootPathsMatch(m.filterTree[msg.repoIdx].rootPaths, msg.rootPaths) {
			m.consecutiveErrors = 0
			m.filterTree[msg.repoIdx].loading = false
			m.filterTree[msg.repoIdx].children = msg.branches
			if msg.expandOnLoad {
				m.filterTree[msg.repoIdx].expanded = true
			}
			// Move cwd branch to first position if this is the cwd repo
			if m.cwdBranch != "" && len(msg.branches) > 1 {
				isCwdRepo := slices.Contains(m.filterTree[msg.repoIdx].rootPaths, m.cwdRepoRoot)
				if isCwdRepo {
					moveToFront(m.filterTree[msg.repoIdx].children, func(b branchFilterItem) bool {
						return b.name == m.cwdBranch
					})
				}
			}
			m.rebuildFilterFlatList()
			// Auto-position cursor on first branch when opened via 'b' key
			if m.filterBranchMode {
				m.filterBranchMode = false
				for i, entry := range m.filterFlatList {
					if entry.repoIdx == msg.repoIdx && entry.branchIdx >= 0 {
						m.filterSelectedIdx = i
						break
					}
				}
			}
			// Top-up: fetch next batch of unloaded repos
			if cmd := m.fetchUnloadedBranches(); cmd != nil {
				return m, cmd
			}
		}

	case tuiBranchesMsg:
		m.consecutiveErrors = 0
		m.branchBackfillDone = true
		if msg.backfillCount > 0 {
			m.flashMessage = fmt.Sprintf("Backfilled branch info for %d jobs", msg.backfillCount)
			m.flashExpiresAt = time.Now().Add(5 * time.Second)
			m.flashView = tuiViewFilter
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

	case tuiFixJobsMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.fixJobs = msg.jobs
			if m.fixSelectedIdx >= len(m.fixJobs) && len(m.fixJobs) > 0 {
				m.fixSelectedIdx = len(m.fixJobs) - 1
			}
		}

	case tuiFixTriggerResultMsg:
		if msg.err != nil {
			m.err = msg.err
			m.flashMessage = fmt.Sprintf("Fix failed: %v", msg.err)
			m.flashExpiresAt = time.Now().Add(3 * time.Second)
			m.flashView = tuiViewTasks
		} else if msg.warning != "" {
			m.flashMessage = msg.warning
			m.flashExpiresAt = time.Now().Add(5 * time.Second)
			m.flashView = tuiViewTasks
			return m, m.fetchFixJobs()
		} else {
			m.flashMessage = fmt.Sprintf("Fix job #%d enqueued", msg.job.ID)
			m.flashExpiresAt = time.Now().Add(3 * time.Second)
			m.flashView = tuiViewTasks
			// Refresh tasks list
			return m, m.fetchFixJobs()
		}

	case tuiPatchMsg:
		if msg.err != nil {
			m.flashMessage = fmt.Sprintf("Patch fetch failed: %v", msg.err)
			m.flashExpiresAt = time.Now().Add(3 * time.Second)
			m.flashView = tuiViewTasks
		} else {
			m.patchText = msg.patch
			m.patchJobID = msg.jobID
			m.patchScroll = 0
			m.currentView = tuiViewPatch
		}

	case tuiApplyPatchResultMsg:
		if msg.needWorktree {
			m.worktreeConfirmJobID = msg.jobID
			m.worktreeConfirmBranch = msg.branch
			m.currentView = tuiViewWorktreeConfirm
			return m, nil
		}
		if msg.rebase {
			m.flashMessage = fmt.Sprintf("Patch for job #%d doesn't apply cleanly - triggering rebase", msg.jobID)
			m.flashExpiresAt = time.Now().Add(5 * time.Second)
			m.flashView = tuiViewTasks
			return m, tea.Batch(m.triggerRebase(msg.jobID), m.fetchFixJobs())
		} else if msg.commitFailed {
			// Patch applied to working tree but commit failed — working tree is dirty
			detail := fmt.Sprintf("Job #%d: %v", msg.jobID, msg.err)
			if msg.worktreeDir != "" {
				detail += fmt.Sprintf(" (worktree kept at %s)", msg.worktreeDir)
			}
			m.flashMessage = detail
			m.flashExpiresAt = time.Now().Add(8 * time.Second)
			m.flashView = tuiViewTasks
		} else if msg.err != nil {
			m.flashMessage = fmt.Sprintf("Apply failed: %v", msg.err)
			m.flashExpiresAt = time.Now().Add(3 * time.Second)
			m.flashView = tuiViewTasks
		} else {
			m.flashMessage = fmt.Sprintf("Patch from job #%d applied and committed", msg.jobID)
			m.flashExpiresAt = time.Now().Add(3 * time.Second)
			m.flashView = tuiViewTasks
			// Refresh tasks list to show updated status, and mark parent addressed
			cmds := []tea.Cmd{m.fetchFixJobs()}
			if msg.parentJobID > 0 {
				cmds = append(cmds, m.markParentAddressed(msg.parentJobID))
			}
			return m, tea.Batch(cmds...)
		}
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

func (m tuiModel) submitComment(jobID int64, text string) tea.Cmd {
	return func() tea.Msg {
		commenter := os.Getenv("USER")
		if commenter == "" {
			commenter = "anonymous"
		}

		err := m.postJSON("/api/comment", map[string]any{
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
func (m tuiModel) fetchPatch(jobID int64) tea.Cmd {
	return func() tea.Msg {
		url := m.serverAddr + fmt.Sprintf("/api/job/patch?job_id=%d", jobID)
		resp, err := m.client.Get(url)
		if err != nil {
			return tuiPatchMsg{jobID: jobID, err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return tuiPatchMsg{jobID: jobID, err: fmt.Errorf("no patch available (HTTP %d)", resp.StatusCode)}
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return tuiPatchMsg{jobID: jobID, err: err}
		}
		return tuiPatchMsg{jobID: jobID, patch: string(data)}
	}
}

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

// fetchJobByID fetches a single job by ID from the daemon API.
func (m tuiModel) fetchJobByID(jobID int64) (*storage.ReviewJob, error) {
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

// hasActiveFixJobs returns true if any fix jobs are queued or running.
func (m tuiModel) hasActiveFixJobs() bool {
	for _, j := range m.fixJobs {
		if j.Status == storage.JobStatusQueued || j.Status == storage.JobStatusRunning {
			return true
		}
	}
	return false
}

// fetchFixJobs fetches fix jobs from the daemon.
func (m tuiModel) fetchFixJobs() tea.Cmd {
	return func() tea.Msg {
		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		err := m.getJSON("/api/jobs?job_type=fix&limit=200", &result)
		if err != nil {
			return tuiFixJobsMsg{err: err}
		}
		return tuiFixJobsMsg{jobs: result.Jobs}
	}
}

// triggerFix triggers a background fix job for a parent review.
func (m tuiModel) triggerFix(parentJobID int64, prompt, gitRef string) tea.Cmd {
	return func() tea.Msg {
		req := map[string]any{
			"parent_job_id": parentJobID,
		}
		if prompt != "" {
			req["prompt"] = prompt
		}
		if gitRef != "" {
			req["git_ref"] = gitRef
		}
		var job storage.ReviewJob
		err := m.postJSON("/api/job/fix", req, &job)
		if err != nil {
			return tuiFixTriggerResultMsg{err: err}
		}
		return tuiFixTriggerResultMsg{job: &job}
	}
}

// applyFixPatch fetches and applies the patch for a completed fix job.
// It resolves the target directory from the branch's worktree, or signals
// needWorktree if the branch is not checked out anywhere.
func (m tuiModel) applyFixPatch(jobID int64) tea.Cmd {
	return func() tea.Msg {
		patch, jobDetail, msg := m.fetchPatchAndJob(jobID)
		if msg != nil {
			return *msg
		}

		// Resolve the target directory: if the branch has its own worktree,
		// apply the patch there instead of the main repo path.
		targetDir, checkedOut, wtErr := git.WorktreePathForBranch(jobDetail.RepoPath, jobDetail.Branch)
		if wtErr != nil {
			return tuiApplyPatchResultMsg{jobID: jobID, err: wtErr}
		}
		if !checkedOut {
			return tuiApplyPatchResultMsg{
				jobID:        jobID,
				needWorktree: true,
				branch:       jobDetail.Branch,
			}
		}

		return m.checkApplyCommitPatch(jobID, jobDetail, targetDir, patch)
	}
}

// applyFixPatchInWorktree creates a temporary worktree for the branch, applies the
// patch there, commits, and removes the worktree. The commit persists on the branch.
func (m tuiModel) applyFixPatchInWorktree(jobID int64) tea.Cmd {
	return func() tea.Msg {
		patch, jobDetail, msg := m.fetchPatchAndJob(jobID)
		if msg != nil {
			return *msg
		}

		// Create a temporary worktree on the branch.
		wtDir, err := os.MkdirTemp("", "roborev-apply-")
		if err != nil {
			return tuiApplyPatchResultMsg{jobID: jobID, err: fmt.Errorf("create temp dir: %w", err)}
		}

		removeWorktree := func() {
			if err := exec.Command("git", "-C", jobDetail.RepoPath, "worktree", "remove", "--force", wtDir).Run(); err != nil {
				os.RemoveAll(wtDir)
				_ = exec.Command("git", "-C", jobDetail.RepoPath, "worktree", "prune").Run()
			}
		}

		cmd := exec.Command("git", "-C", jobDetail.RepoPath, "worktree", "add", wtDir, jobDetail.Branch)
		if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
			os.RemoveAll(wtDir)
			return tuiApplyPatchResultMsg{jobID: jobID,
				err: fmt.Errorf("git worktree add: %w: %s", cmdErr, out)}
		}

		result := m.checkApplyCommitPatch(jobID, jobDetail, wtDir, patch)

		// Keep the worktree if patch was applied but commit failed, so the user can recover.
		if result.commitFailed {
			result.worktreeDir = wtDir
		} else {
			removeWorktree()
		}

		return result
	}
}

// fetchPatchAndJob fetches the patch content and job details for a fix job.
// Returns nil msg on success; a non-nil msg should be returned to the TUI immediately.
func (m tuiModel) fetchPatchAndJob(jobID int64) (string, *storage.ReviewJob, *tuiApplyPatchResultMsg) {
	url := m.serverAddr + fmt.Sprintf("/api/job/patch?job_id=%d", jobID)
	resp, err := m.client.Get(url)
	if err != nil {
		return "", nil, &tuiApplyPatchResultMsg{jobID: jobID, err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, &tuiApplyPatchResultMsg{jobID: jobID, err: fmt.Errorf("no patch available")}
	}

	patchData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, &tuiApplyPatchResultMsg{jobID: jobID, err: err}
	}
	patch := string(patchData)
	if patch == "" {
		return "", nil, &tuiApplyPatchResultMsg{jobID: jobID, err: fmt.Errorf("empty patch")}
	}

	jobDetail, jErr := m.fetchJobByID(jobID)
	if jErr != nil {
		return "", nil, &tuiApplyPatchResultMsg{jobID: jobID, err: jErr}
	}

	return patch, jobDetail, nil
}

// checkApplyCommitPatch validates, applies, commits, and marks a patch as applied.
// Shared by both applyFixPatch (existing worktree) and applyFixPatchInWorktree (temp worktree).
func (m tuiModel) checkApplyCommitPatch(jobID int64, jobDetail *storage.ReviewJob, targetDir, patch string) tuiApplyPatchResultMsg {
	// Check for uncommitted changes in files the patch touches
	patchedFiles, pfErr := patchFiles(patch)
	if pfErr != nil {
		return tuiApplyPatchResultMsg{jobID: jobID, err: pfErr}
	}
	dirty, dirtyErr := dirtyPatchFiles(targetDir, patchedFiles)
	if dirtyErr != nil {
		return tuiApplyPatchResultMsg{jobID: jobID,
			err: fmt.Errorf("checking dirty files: %w", dirtyErr)}
	}
	if len(dirty) > 0 {
		return tuiApplyPatchResultMsg{jobID: jobID,
			err: fmt.Errorf("uncommitted changes in patch files: %s — stash or commit first", strings.Join(dirty, ", "))}
	}

	// Dry-run check — only trigger rebase on actual merge conflicts
	if err := worktree.CheckPatch(targetDir, patch); err != nil {
		var conflictErr *worktree.PatchConflictError
		if errors.As(err, &conflictErr) {
			return tuiApplyPatchResultMsg{jobID: jobID, rebase: true, err: err}
		}
		return tuiApplyPatchResultMsg{jobID: jobID, err: err}
	}

	// Apply the patch
	if err := worktree.ApplyPatch(targetDir, patch); err != nil {
		return tuiApplyPatchResultMsg{jobID: jobID, err: err}
	}

	var parentJobID int64
	if jobDetail.ParentJobID != nil {
		parentJobID = *jobDetail.ParentJobID
	}

	// Stage and commit
	commitMsg := fmt.Sprintf("fix: apply roborev fix job #%d", jobID)
	if parentJobID > 0 {
		ref := git.ShortSHA(jobDetail.GitRef)
		commitMsg = fmt.Sprintf("fix: apply roborev fix for %s (job #%d)", ref, jobID)
	}
	if err := commitPatch(targetDir, patch, commitMsg); err != nil {
		return tuiApplyPatchResultMsg{jobID: jobID, parentJobID: parentJobID, success: true,
			commitFailed: true, err: fmt.Errorf("patch applied but commit failed: %w", err)}
	}

	// Mark the fix job as applied on the server
	if err := m.postJSON("/api/job/applied", map[string]any{"job_id": jobID}, nil); err != nil {
		return tuiApplyPatchResultMsg{jobID: jobID, parentJobID: parentJobID, success: true,
			err: fmt.Errorf("patch applied and committed but failed to mark applied: %w", err)}
	}

	return tuiApplyPatchResultMsg{jobID: jobID, parentJobID: parentJobID, success: true}
}

// commitPatch stages only the files touched by patch and commits them.
func commitPatch(repoPath, patch, message string) error {
	files, err := patchFiles(patch)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found in patch")
	}
	args := append([]string{"-C", repoPath, "add", "--"}, files...)
	addCmd := exec.Command("git", args...)
	addCmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, out)
	}
	commitArgs := append(
		[]string{"-C", repoPath, "commit", "--only", "-m", message, "--"},
		files...,
	)
	commitCmd := exec.Command("git", commitArgs...)
	commitCmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, out)
	}
	return nil
}

// dirtyPatchFiles returns the subset of files that have uncommitted changes.
func dirtyPatchFiles(repoPath string, files []string) ([]string, error) {
	// git diff --name-only shows unstaged changes; --cached shows staged
	cmd := exec.Command("git", "-C", repoPath, "diff", "--name-only", "HEAD", "--")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	dirty := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			dirty[line] = true
		}
	}
	var overlap []string
	for _, f := range files {
		if dirty[f] {
			overlap = append(overlap, f)
		}
	}
	return overlap, nil
}

// patchFiles extracts the list of file paths touched by a unified diff.
func patchFiles(patch string) ([]string, error) {
	fileDiffs, err := godiff.ParseMultiFileDiff([]byte(patch))
	if err != nil {
		return nil, fmt.Errorf("parse patch: %w", err)
	}
	seen := map[string]bool{}
	addFile := func(name, prefix string) {
		name = strings.TrimPrefix(name, prefix)
		if name != "" && name != "/dev/null" {
			seen[name] = true
		}
	}
	for _, fd := range fileDiffs {
		addFile(fd.OrigName, "a/") // old path (stages deletion for renames)
		addFile(fd.NewName, "b/")  // new path (stages addition for renames)
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	return files, nil
}

// triggerRebase triggers a new fix job that re-applies a stale patch to the current HEAD.
// The server looks up the stale patch from the DB, avoiding large client-to-server transfers.
func (m tuiModel) triggerRebase(staleJobID int64) tea.Cmd {
	return func() tea.Msg {
		// Find the parent job ID (the original review this fix was for)
		staleJob, fetchErr := m.fetchJobByID(staleJobID)
		if fetchErr != nil {
			return tuiFixTriggerResultMsg{err: fmt.Errorf("stale job %d not found: %w", staleJobID, fetchErr)}
		}

		// Use the original parent job ID if this was already a fix job
		parentJobID := staleJobID
		if staleJob.ParentJobID != nil {
			parentJobID = *staleJob.ParentJobID
		}

		// Let the server build the rebase prompt from the stale job's patch
		req := map[string]any{
			"parent_job_id": parentJobID,
			"stale_job_id":  staleJobID,
		}
		var newJob storage.ReviewJob
		if err := m.postJSON("/api/job/fix", req, &newJob); err != nil {
			return tuiFixTriggerResultMsg{err: fmt.Errorf("trigger rebase: %w", err)}
		}
		// Mark the stale job as rebased now that the new job exists.
		// Skip if already rebased (e.g. retry via R on a rebased job).
		var warning string
		if staleJob.Status != storage.JobStatusRebased {
			if err := m.postJSON(
				"/api/job/rebased",
				map[string]any{"job_id": staleJobID},
				nil,
			); err != nil {
				warning = fmt.Sprintf(
					"rebase job #%d enqueued but failed to mark #%d as rebased: %v",
					newJob.ID, staleJobID, err,
				)
			}
		}
		return tuiFixTriggerResultMsg{job: &newJob, warning: warning}
	}
}

// truncateString is defined in fix.go
