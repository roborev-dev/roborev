package tui

import (
	"errors"
	"time"

	"github.com/atotto/clipboard"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/streamfmt"
)

type viewKind int

const (
	viewQueue viewKind = iota
	viewReview
	viewKindPrompt
	viewFilter
	viewKindComment
	viewCommitMsg
	viewHelp
	viewLog
	viewTasks               // Background fix tasks view
	viewKindWorktreeConfirm // Confirm creating a worktree to apply patch
	viewPatch               // Patch viewer for fix jobs
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

// helpItem is a single help-bar entry with a key label and description.
type helpItem struct {
	key  string
	desc string
}

// columnWidths stores pre-computed column widths for the queue view layout.
type columnWidths struct {
	ref    int
	branch int
	repo   int
	agent  int
}

// logOutputMsg delivers output lines from the daemon
type logOutputMsg struct {
	lines     []logLine
	hasMore   bool // true if job is still running
	err       error
	newOffset int64                // byte offset for next fetch
	append    bool                 // true = append lines, false = replace
	seq       uint64               // fetch sequence number for stale detection
	fmtr      *streamfmt.Formatter // formatter used for rendering (persist for incremental reuse)
}

// logTickMsg triggers a refresh of the log output
type logTickMsg struct{}

type tickMsg time.Time
type jobsMsg struct {
	jobs    []storage.ReviewJob
	hasMore bool
	append  bool             // true to append to existing jobs, false to replace
	seq     int              // fetch sequence number — stale responses (seq < model.fetchSeq) are discarded
	stats   storage.JobStats // aggregate counts from server
}
type statusMsg storage.DaemonStatus
type reviewMsg struct {
	review     *storage.Review
	responses  []storage.Response // Responses for this review
	jobID      int64              // The job ID that was requested (for race condition detection)
	branchName string             // Pre-computed branch name (empty if not applicable)
}
type promptMsg struct {
	review *storage.Review
	jobID  int64 // The job ID that was requested (for stale response detection)
}
type addressedMsg bool
type addressedResultMsg struct {
	jobID      int64 // job ID for queue view rollback
	reviewID   int64 // review ID for review view rollback
	reviewView bool  // true if from review view (rollback currentReview)
	oldState   bool
	newState   bool   // the requested state (for pendingAddressed validation)
	seq        uint64 // request sequence number (for distinguishing same-state rapid toggles)
	err        error
}
type cancelResultMsg struct {
	jobID         int64
	oldState      storage.JobStatus
	oldFinishedAt *time.Time
	err           error
}
type rerunResultMsg struct {
	jobID         int64
	oldState      storage.JobStatus
	oldStartedAt  *time.Time
	oldFinishedAt *time.Time
	oldError      string
	err           error
}
type errMsg error
type jobsErrMsg struct {
	err error
	seq int // fetch sequence number for staleness check
}
type paginationErrMsg struct {
	err error
	seq int // fetch sequence number for staleness check
}
type updateCheckMsg struct {
	version    string // Latest version if available, empty if up to date
	isDevBuild bool   // True if running a dev build
}
type reposMsg struct {
	repos []repoFilterItem
}
type branchesMsg struct {
	backfillCount int // Number of branches successfully backfilled to the database
}
type repoBranchesMsg struct {
	repoIdx      int                // Which repo in filterTree
	rootPaths    []string           // Repo identity (for stale message detection)
	branches     []branchFilterItem // Branch data
	err          error              // Non-nil on fetch failure
	expandOnLoad bool               // Set expanded=true when branches arrive
	searchSeq    int                // Search generation; stale errors don't set fetchFailed
}
type commentResultMsg struct {
	jobID int64
	err   error
}
type clipboardResultMsg struct {
	err  error
	view viewKind // The view where copy was triggered (for flash attribution)
}
type commitMsgMsg struct {
	jobID   int64
	content string
	err     error
}
type reconnectMsg struct {
	newAddr string // New daemon address if found, empty if not found
	version string // Daemon version (to avoid sync call in Update)
	err     error
}

type fixJobsMsg struct {
	jobs []storage.ReviewJob
	err  error
}

type fixTriggerResultMsg struct {
	job     *storage.ReviewJob
	err     error
	warning string // non-fatal issue (e.g. failed to mark stale job)
}

type applyPatchResultMsg struct {
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

type patchMsg struct {
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

// option func(*options) is a functional option for TUI.
type option func(*options)

// withRepoFilter locks the TUI filter to a specific repo.
func withRepoFilter(repo string) option {
	return func(o *options) { o.repoFilter = repo }
}

// withBranchFilter locks the TUI filter to a specific branch.
func withBranchFilter(branch string) option {
	return func(o *options) { o.branchFilter = branch }
}

// withExternalIODisabled disables daemon/config/git calls in newModel.
func withExternalIODisabled() option {
	return func(o *options) { o.disableExternalIO = true }
}

// options holds optional overrides for the TUI model, set from CLI flags.
type options struct {
	repoFilter        string // --repo flag: lock filter to this repo path
	branchFilter      string // --branch flag: lock filter to this branch
	disableExternalIO bool   // tests: disable daemon/config/git calls
}

// errNoLog is a sentinel error returned when the job log API
// returns 404 (job has no log output yet or was never started).
var errNoLog = errors.New("no log available")
