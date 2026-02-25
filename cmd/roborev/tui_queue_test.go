package main

import (
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIQueueNavigation(t *testing.T) {
	threeJobs := []storage.ReviewJob{
		makeJob(1),
		makeJob(2, withStatus(storage.JobStatusQueued)),
		makeJob(3),
	}

	tests := []struct {
		name         string
		jobs         []storage.ReviewJob
		activeFilter []string
		startIdx     int
		key          any // rune or tea.KeyType
		wantIdx      int
		wantJobID    int64
	}{
		{
			name:      "j moves down",
			jobs:      threeJobs,
			startIdx:  1,
			key:       'j',
			wantIdx:   2,
			wantJobID: 3,
		},
		{
			name:      "k moves up",
			jobs:      threeJobs,
			startIdx:  1,
			key:       'k',
			wantIdx:   0,
			wantJobID: 1,
		},
		{
			name:      "down arrow moves down",
			jobs:      threeJobs,
			startIdx:  1,
			key:       tea.KeyDown,
			wantIdx:   2,
			wantJobID: 3,
		},
		{
			name:      "up arrow moves up",
			jobs:      threeJobs,
			startIdx:  1,
			key:       tea.KeyUp,
			wantIdx:   0,
			wantJobID: 1,
		},
		{
			name:      "left arrow moves up",
			jobs:      threeJobs,
			startIdx:  1,
			key:       tea.KeyLeft,
			wantIdx:   0,
			wantJobID: 1,
		},
		{
			name:      "right arrow moves down",
			jobs:      threeJobs,
			startIdx:  1,
			key:       tea.KeyRight,
			wantIdx:   2,
			wantJobID: 3,
		},
		{
			name:      "g jumps to top (unfiltered)",
			jobs:      threeJobs,
			startIdx:  2,
			key:       'g',
			wantIdx:   0,
			wantJobID: 1,
		},
		{
			name: "g jumps to top (filtered)",
			jobs: []storage.ReviewJob{
				makeJob(1, withRepoPath("/repo/alpha")),
				makeJob(2, withRepoPath("/repo/beta")),
				makeJob(3, withRepoPath("/repo/beta")),
			},
			activeFilter: []string{"/repo/beta"},
			startIdx:     2,
			key:          'g',
			wantIdx:      1, // First visible job
			wantJobID:    2,
		},
		{
			name:      "g jumps to top (empty)",
			jobs:      []storage.ReviewJob{},
			startIdx:  0,
			key:       'g',
			wantIdx:   0,
			wantJobID: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := setupQueue(tt.jobs, tt.startIdx)
			if tt.activeFilter != nil {
				m.activeRepoFilter = tt.activeFilter
			}

			var m2 tuiModel
			switch k := tt.key.(type) {
			case rune:
				m2, _ = pressKey(m, k)
			case tea.KeyType:
				m2, _ = pressSpecial(m, k)
			}

			if m2.selectedIdx != tt.wantIdx {
				t.Errorf("expected selectedIdx %d, got %d", tt.wantIdx, m2.selectedIdx)
			}
			if m2.selectedJobID != tt.wantJobID {
				t.Errorf("expected selectedJobID %d, got %d", tt.wantJobID, m2.selectedJobID)
			}
		})
	}
}

func TestTUIQueueNavigationBoundaries(t *testing.T) {
	// Test flash messages when navigating at queue boundaries
	m := newQueueTestModel(
		withQueueTestJobs(makeJob(1), makeJob(2), makeJob(3)),
		withQueueTestSelection(0),
		withQueueTestFlags(false, false, false),
	)

	// Press 'up' at top of queue - should show flash message
	m2, _ := pressSpecial(m, tea.KeyUp)

	if m2.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx to remain 0 at top, got %d", m2.selectedIdx)
	}
	assertFlashMessage(t, m2, tuiViewQueue, "No newer review")

	// Now at bottom of queue
	m.selectedIdx = 2
	m.selectedJobID = 3
	m.flashMessage = "" // Clear

	// Press 'down' at bottom of queue - should show flash message
	m3, _ := pressSpecial(m, tea.KeyDown)

	if m3.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx to remain 2 at bottom, got %d", m3.selectedIdx)
	}
	assertFlashMessage(t, m3, tuiViewQueue, "No older review")
}

func TestTUIQueueNavigationBoundariesWithFilter(t *testing.T) {
	// Test flash messages at bottom when multi-repo filter is active (prevents auto-load).
	// Single-repo filters use server-side filtering and support pagination,
	// but multi-repo filters are client-side only so they disable pagination.
	m := newQueueTestModel(
		withQueueTestJobs(makeJob(1, withRepoPath("/repo1")), makeJob(2, withRepoPath("/repo2"))),
		withQueueTestSelection(1),
		withQueueTestFlags(true, false, false),
	)
	m.activeRepoFilter = []string{"/repo1", "/repo2"}

	// Press 'down' - already at last job, should hit boundary
	m2, _ := pressSpecial(m, tea.KeyDown)

	// Should show flash since multi-repo filter prevents loading more
	assertFlashMessage(t, m2, tuiViewQueue, "No older review")
}

func TestTUINavigateDownTriggersLoadMore(t *testing.T) {
	// Set up at last job with more available
	m := newQueueTestModel(
		withQueueTestJobs(makeJob(1)),
		withQueueTestSelection(0),
		withQueueTestFlags(true, false, false),
	)

	// Press down at bottom - should trigger load more
	m2, cmd := pressSpecial(m, tea.KeyDown)

	if !m2.loadingMore {
		t.Error("loadingMore should be set when navigating past last job")
	}
	if cmd == nil {
		t.Error("Should return fetchMoreJobs command")
	}
}

func TestTUINavigateDownNoLoadMoreWhenFiltered(t *testing.T) {
	// Set up at last job with filter active
	m := newQueueTestModel(
		withQueueTestJobs(makeJob(1, withRepoPath("/path/to/repo"))),
		withQueueTestSelection(0),
		withQueueTestFlags(true, false, false),
	)
	m.activeRepoFilter = []string{"/path/to/repo", "/path/to/repo2"}

	// Press down at bottom - should NOT trigger load more (filtered view loads all)
	m2, cmd := pressSpecial(m, tea.KeyDown)

	if m2.loadingMore {
		t.Error("loadingMore should not be set when filter is active")
	}
	if cmd != nil {
		t.Error("Should not return command when filter is active")
	}
}

func TestTUICalculateColumnWidths(t *testing.T) {
	// Test that column widths fit within terminal for usable sizes (>= 80)
	// Narrower terminals may overflow - users should widen their terminal
	tests := []struct {
		name           string
		termWidth      int
		idWidth        int
		expectOverflow bool // true if overflow is acceptable for this width
	}{
		{
			name:           "wide terminal",
			termWidth:      200,
			idWidth:        3,
			expectOverflow: false,
		},
		{
			name:           "medium terminal",
			termWidth:      100,
			idWidth:        3,
			expectOverflow: false,
		},
		{
			name:           "standard terminal",
			termWidth:      80,
			idWidth:        3,
			expectOverflow: false,
		},
		{
			name:           "narrow terminal - overflow acceptable",
			termWidth:      60,
			idWidth:        3,
			expectOverflow: true, // Fixed columns alone need ~48 chars
		},
		{
			name:           "very narrow terminal - overflow expected",
			termWidth:      40,
			idWidth:        3,
			expectOverflow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tuiModel{width: tt.termWidth}
			widths := m.calculateColumnWidths(tt.idWidth)

			// All columns must have positive widths
			if widths.ref < 1 {
				t.Errorf("ref width %d < 1", widths.ref)
			}
			if widths.repo < 1 {
				t.Errorf("repo width %d < 1", widths.repo)
			}
			if widths.agent < 1 {
				t.Errorf("agent width %d < 1", widths.agent)
			}

			// Fixed widths: ID (idWidth), Status (10), Queued (12), Elapsed (8), Addressed (9)
			// Plus spacing: 2 (prefix) + 7 spaces between columns
			fixedWidth := 2 + tt.idWidth + 10 + 12 + 8 + 9 + 7
			flexibleTotal := widths.ref + widths.repo + widths.agent
			totalWidth := fixedWidth + flexibleTotal

			if !tt.expectOverflow && totalWidth > tt.termWidth {
				t.Errorf("total width %d exceeds terminal width %d", totalWidth, tt.termWidth)
			}

			// Even with overflow, flexible columns should be minimal
			if tt.expectOverflow && flexibleTotal > 15 {
				t.Errorf("narrow terminal should minimize flexible columns, got %d", flexibleTotal)
			}
		})
	}
}

func TestTUICalculateColumnWidthsProportions(t *testing.T) {
	// On wide terminals, columns should use higher minimums
	m := tuiModel{width: 200}
	widths := m.calculateColumnWidths(3)

	if widths.ref < 10 {
		t.Errorf("wide terminal ref width %d < 10", widths.ref)
	}
	if widths.repo < 15 {
		t.Errorf("wide terminal repo width %d < 15", widths.repo)
	}
	if widths.agent < 10 {
		t.Errorf("wide terminal agent width %d < 10", widths.agent)
	}
}

func TestTUIRenderJobLineTruncation(t *testing.T) {
	m := tuiModel{width: 80}
	// Use a git range - shortRef truncates ranges to 17 chars max, then renderJobLine
	// truncates further based on colWidths.ref. Use a range longer than 17 chars.
	job := makeJob(1,
		withRef("abcdef1234567..ghijkl7890123"), // 28 char range, shortRef -> 17 chars
		withRepoName("very-long-repository-name-that-exceeds-width"),
		withAgent("super-long-agent-name"),
		withEnqueuedAt(time.Now()),
	)

	// Use narrow column widths to force truncation
	// ref=10 will truncate the 17-char shortRef output
	colWidths := columnWidths{
		ref:   10,
		repo:  15,
		agent: 10,
	}

	line := m.renderJobLine(job, false, 3, colWidths)

	// Check that truncated values contain "..."
	if !strings.Contains(line, "...") {
		t.Errorf("Expected truncation with '...' in line: %s", line)
	}

	// The line should contain truncated versions, not full strings
	// shortRef reduces "abcdef1234567..ghijkl7890123" to "abcdef1234567..gh" (17 chars)
	// then renderJobLine truncates to colWidths.ref (10)
	if strings.Contains(line, "abcdef1234567..gh") {
		t.Error("Full git ref (after shortRef) should have been truncated")
	}
	if strings.Contains(line, "very-long-repository-name-that-exceeds-width") {
		t.Error("Full repo name should have been truncated")
	}
	if strings.Contains(line, "super-long-agent-name") {
		t.Error("Full agent name should have been truncated")
	}
}

func TestTUIRenderJobLineLength(t *testing.T) {
	// Test that rendered line length respects column widths
	m := tuiModel{width: 100}
	job := makeJob(123,
		withRef("abc1234..def5678901234567890"), // Long range
		withRepoName("my-very-long-repository-name-here"),
		withAgent("claude-code-agent"),
		withEnqueuedAt(time.Now()),
	)

	idWidth := 4
	colWidths := columnWidths{
		ref:   12,
		repo:  15,
		agent: 10,
	}

	line := m.renderJobLine(job, false, idWidth, colWidths)

	// Fixed widths: ID (idWidth=4), Status (10), Queued (12), Elapsed (8), Addressed (varies)
	// Plus spacing between columns
	// The line should not be excessively long
	// Note: line includes ANSI codes for status styling, so we check a reasonable max
	maxExpectedLen := idWidth + colWidths.ref + colWidths.repo + colWidths.agent + 10 + 12 + 8 + 10 + 20 // generous margin for spacing and ANSI
	if len(line) > maxExpectedLen {
		t.Errorf("Line length %d exceeds expected max %d: %s", len(line), maxExpectedLen, line)
	}

	// Verify truncation happened - original values should not appear
	if strings.Contains(line, "my-very-long-repository-name-here") {
		t.Error("Repo name should have been truncated")
	}
	if strings.Contains(line, "claude-code-agent") {
		t.Error("Agent name should have been truncated")
	}
}

func TestTUIRenderJobLineNoTruncation(t *testing.T) {
	m := tuiModel{width: 200}
	job := makeJob(1,
		withRef("abc1234"),
		withRepoName("myrepo"),
		withAgent("test"),
		withEnqueuedAt(time.Now()),
	)

	// Use wide column widths - no truncation needed
	colWidths := columnWidths{
		ref:   20,
		repo:  20,
		agent: 15,
	}

	line := m.renderJobLine(job, false, 3, colWidths)

	// Short values should not be truncated
	if strings.Contains(line, "...") {
		t.Errorf("Short values should not be truncated: %s", line)
	}

	// Original values should appear
	if !strings.Contains(line, "abc1234") {
		t.Error("Git ref should appear untruncated")
	}
	if !strings.Contains(line, "myrepo") {
		t.Error("Repo name should appear untruncated")
	}
	if !strings.Contains(line, "test") {
		t.Error("Agent name should appear untruncated")
	}
}

func TestTUIRenderJobLineReviewTypeTag(t *testing.T) {
	m := tuiModel{width: 80}
	colWidths := columnWidths{ref: 30, repo: 15, agent: 10}

	tests := []struct {
		reviewType string
		wantTag    bool
	}{
		{"", false},
		{"default", false},
		{"general", false},
		{"review", false},
		{"security", true},
		{"design", true},
	}

	for _, tc := range tests {
		t.Run(tc.reviewType, func(t *testing.T) {
			job := makeJob(1, withRef("abc1234"), withReviewType(tc.reviewType))
			line := m.renderJobLine(job, false, 3, colWidths)
			hasTag := strings.Contains(line, "["+tc.reviewType+"]")
			if tc.wantTag && !hasTag {
				t.Errorf("expected [%s] tag in line: %s", tc.reviewType, line)
			}
			if !tc.wantTag && tc.reviewType != "" && hasTag {
				t.Errorf("unexpected [%s] tag in line: %s", tc.reviewType, line)
			}
		})
	}
}

func TestTUIPaginationAppendMode(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	// Start with 50 jobs
	initialJobs := make([]storage.ReviewJob, 50)
	for i := range 50 {
		initialJobs[i] = makeJob(int64(50 - i))
	}
	m.jobs = initialJobs
	m.selectedIdx = 0
	m.selectedJobID = 50
	m.hasMore = true

	// Append 25 more jobs
	moreJobs := make([]storage.ReviewJob, 25)
	for i := range 25 {
		moreJobs[i] = makeJob(int64(i + 1)) // IDs 1-25 (older)
	}
	appendMsg := tuiJobsMsg{jobs: moreJobs, hasMore: false, append: true}

	m2, _ := updateModel(t, m, appendMsg)

	// Should now have 75 jobs
	if len(m2.jobs) != 75 {
		t.Errorf("Expected 75 jobs after append, got %d", len(m2.jobs))
	}

	// hasMore should be updated
	if m2.hasMore {
		t.Error("hasMore should be false after append with hasMore=false")
	}

	// loadingMore should be cleared
	if m2.loadingMore {
		t.Error("loadingMore should be cleared after append")
	}

	// Selection should be maintained
	if m2.selectedJobID != 50 {
		t.Errorf("Expected selectedJobID=50 maintained, got %d", m2.selectedJobID)
	}
}

func TestTUIPaginationRefreshMaintainsView(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	// Simulate user has paginated to 100 jobs
	jobs := make([]storage.ReviewJob, 100)
	for i := range 100 {
		jobs[i] = makeJob(int64(100 - i))
	}
	m.jobs = jobs
	m.selectedIdx = 50
	m.selectedJobID = 50

	// Refresh arrives (replace mode, not append)
	refreshedJobs := make([]storage.ReviewJob, 100)
	for i := range 100 {
		refreshedJobs[i] = makeJob(int64(101 - i)) // New job at top
	}
	refreshMsg := tuiJobsMsg{jobs: refreshedJobs, hasMore: true, append: false}

	m2, _ := updateModel(t, m, refreshMsg)

	// Should still have 100 jobs
	if len(m2.jobs) != 100 {
		t.Errorf("Expected 100 jobs after refresh, got %d", len(m2.jobs))
	}

	// Selection should find job ID=50 at new index
	if m2.selectedJobID != 50 {
		t.Errorf("Expected selectedJobID=50 maintained, got %d", m2.selectedJobID)
	}
	if m2.selectedIdx != 51 {
		t.Errorf("Expected selectedIdx=51 (shifted by new job), got %d", m2.selectedIdx)
	}
}

func TestTUILoadingMoreClearedOnPaginationError(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.loadingMore = true

	// Pagination error arrives (only pagination errors clear loadingMore)
	errMsg := tuiPaginationErrMsg{err: fmt.Errorf("network error")}
	m2, _ := updateModel(t, m, errMsg)

	// loadingMore should be cleared so user can retry
	if m2.loadingMore {
		t.Error("loadingMore should be cleared on pagination error")
	}

	// Error should be set
	if m2.err == nil {
		t.Error("err should be set")
	}
}

func TestTUILoadingMoreNotClearedOnGenericError(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.loadingMore = true

	// Generic error arrives (should NOT clear loadingMore)
	errMsg := tuiErrMsg(fmt.Errorf("some other error"))
	m2, _ := updateModel(t, m, errMsg)

	// loadingMore should remain true - only pagination errors clear it
	if !m2.loadingMore {
		t.Error("loadingMore should NOT be cleared on generic error")
	}

	// Error should still be set
	if m2.err == nil {
		t.Error("err should be set")
	}
}

func TestTUIPaginationBlockedWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.loadingJobs = true
	m.hasMore = true
	m.loadingMore = false

	// Set up at last job
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Try to navigate down (would normally trigger pagination)
	m2, cmd := pressKey(m, 'j')

	// Pagination should NOT be triggered because loadingJobs is true
	if m2.loadingMore {
		t.Error("loadingMore should not be set while loadingJobs is true")
	}
	if cmd != nil {
		t.Error("No command should be returned when pagination is blocked")
	}
}

func TestTUIPaginationAllowedWhenNotLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.loadingJobs = false
	m.hasMore = true
	m.loadingMore = false

	// Set up at last job
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Navigate down - should trigger pagination
	m2, cmd := pressKey(m, 'j')

	// Pagination SHOULD be triggered
	if !m2.loadingMore {
		t.Error("loadingMore should be set when pagination is allowed")
	}
	if cmd == nil {
		t.Error("Command should be returned to fetch more jobs")
	}
}

func TestTUIPageDownBlockedWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.loadingJobs = true
	m.hasMore = true
	m.loadingMore = false
	m.height = 30

	// Set up with one job
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Try pgdown (would normally trigger pagination at end)
	m2, cmd := pressSpecial(m, tea.KeyPgDown)

	// Pagination should NOT be triggered
	if m2.loadingMore {
		t.Error("loadingMore should not be set on pgdown while loadingJobs is true")
	}
	if cmd != nil {
		t.Error("No command should be returned when pagination is blocked")
	}
}

func TestTUIResizeBehavior(t *testing.T) {
	tests := []struct {
		name                      string
		initialHeight             int
		jobsCount                 int
		loadingJobs               bool
		loadingMore               bool
		activeFilters             []string
		msg                       tea.WindowSizeMsg
		wantCmd                   bool
		wantLoading               bool
		checkRefetchOnLaterResize bool
	}{
		{
			name:          "During Pagination No Refetch",
			initialHeight: 20,
			jobsCount:     3,
			loadingJobs:   false,
			loadingMore:   true, // pagination in flight
			msg:           tea.WindowSizeMsg{Height: 40},
			wantCmd:       false,
			wantLoading:   false,
		},
		{
			name:          "Triggers Refetch When Needed",
			initialHeight: 20,
			jobsCount:     3,
			loadingJobs:   false,
			loadingMore:   false,
			msg:           tea.WindowSizeMsg{Height: 40},
			wantCmd:       true,
			wantLoading:   true,
		},
		{
			name:          "No Refetch When Enough Jobs",
			initialHeight: 20,
			jobsCount:     100, // lots of jobs
			loadingJobs:   false,
			loadingMore:   false,
			msg:           tea.WindowSizeMsg{Height: 40},
			wantCmd:       false,
			wantLoading:   false,
		},
		{
			name:                      "Refetch On Later Resize",
			initialHeight:             20,
			jobsCount:                 25, // enough for height 20 (visible=12+10=22), but not height 40 (visible=32+10=42)
			loadingJobs:               false,
			loadingMore:               false,
			msg:                       tea.WindowSizeMsg{Height: 20}, // same height first
			wantCmd:                   false,                         // intermediate state wantCmd=false
			wantLoading:               false,
			checkRefetchOnLaterResize: true,
		},
		{
			name:          "No Refetch While Loading Jobs",
			initialHeight: 20,
			jobsCount:     3,
			loadingJobs:   true, // fetch in progress
			loadingMore:   false,
			msg:           tea.WindowSizeMsg{Height: 40},
			wantCmd:       false,
			wantLoading:   true, // remains true
		},
		{
			name:          "No Refetch Multi-Repo Filter Active",
			initialHeight: 20,
			jobsCount:     3,
			loadingJobs:   false,
			loadingMore:   false,
			activeFilters: []string{"/repo1", "/repo2"},
			msg:           tea.WindowSizeMsg{Height: 40},
			wantCmd:       false,
			wantLoading:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := make([]storage.ReviewJob, tt.jobsCount)
			for i := 0; i < tt.jobsCount; i++ {
				jobs[i] = makeJob(int64(i + 1))
			}

			m := newQueueTestModel(
				withQueueTestJobs(jobs...),
				withQueueTestFlags(true, tt.loadingMore, tt.loadingJobs),
			)
			m.activeRepoFilter = tt.activeFilters
			m.height = tt.initialHeight
			m.heightDetected = false // ensure we can verify the msg updates this

			var cmd tea.Cmd

			if tt.checkRefetchOnLaterResize {
				m, cmd = updateModel(t, m, tt.msg)

				if cmd != nil {
					t.Error("Expected no fetch command on first resize, got one")
				}
				if m.height != tt.msg.Height {
					t.Errorf("Expected height to be %d, got %d", tt.msg.Height, m.height)
				}
				if !m.heightDetected {
					t.Error("Expected heightDetected to be true")
				}

				// Second resize that should trigger the refetch
				m, cmd = updateModel(t, m, tea.WindowSizeMsg{Height: 40})

				if cmd == nil {
					t.Error("Expected fetch command on second resize, got nil")
				}
				if m.height != 40 {
					t.Errorf("Expected height to be 40, got %d", m.height)
				}
				if !m.loadingJobs {
					t.Error("Expected loadingJobs to be true after second resize triggered fetch")
				}
				return
			} else {
				m, cmd = updateModel(t, m, tt.msg)
			}

			if tt.wantCmd && cmd == nil {
				t.Error("Expected fetch command, got nil")
			}
			if !tt.wantCmd && cmd != nil {
				t.Error("Expected no fetch command, got one")
			}
			if m.height != tt.msg.Height {
				t.Errorf("Expected height to be %d, got %d", tt.msg.Height, m.height)
			}
			if !m.heightDetected {
				t.Error("Expected heightDetected to be true")
			}
			if m.loadingJobs != tt.wantLoading {
				t.Errorf("Expected loadingJobs %v, got %v", tt.wantLoading, m.loadingJobs)
			}
		})
	}
}

func TestTUIJobsMsgHideAddressedUnderfilledViewportAutoPaginates(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	m.height = 29 // queueVisibleRows = 20
	m.loadingJobs = true

	// 13 visible (done + unaddressed), 12 hidden (failed) in this page.
	jobs := make([]storage.ReviewJob, 0, 25)
	var id int64 = 200
	for range 13 {
		jobs = append(jobs, makeJob(id, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))))
		id--
	}
	for range 12 {
		jobs = append(jobs, makeJob(id, withStatus(storage.JobStatusFailed)))
		id--
	}

	m2, cmd := updateModel(t, m, tuiJobsMsg{
		jobs:    jobs,
		hasMore: true,
		append:  false,
	})

	if got := len(m2.getVisibleJobs()); got != 13 {
		t.Fatalf("Expected 13 visible jobs in first page, got %d", got)
	}
	if !m2.loadingMore {
		t.Error("loadingMore should be true when hide-addressed page underfills viewport")
	}
	if cmd == nil {
		t.Error("Expected auto-pagination command when hide-addressed page underfills viewport")
	}
}

func TestTUIJobsMsgHideAddressedFilledViewportDoesNotAutoPaginate(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	m.height = 29 // queueVisibleRows = 21
	m.loadingJobs = true

	// 21 visible rows already available (plus hidden jobs).
	jobs := make([]storage.ReviewJob, 0, 26)
	var id int64 = 300
	for range 21 {
		jobs = append(jobs, makeJob(id, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))))
		id--
	}
	for range 5 {
		jobs = append(jobs, makeJob(id, withStatus(storage.JobStatusFailed)))
		id--
	}

	m2, cmd := updateModel(t, m, tuiJobsMsg{
		jobs:    jobs,
		hasMore: true,
		append:  false,
	})

	if got := len(m2.getVisibleJobs()); got < 21 {
		t.Fatalf("Expected at least 21 visible jobs, got %d", got)
	}
	if m2.loadingMore {
		t.Error("loadingMore should remain false when viewport is already filled")
	}
	if cmd != nil {
		t.Error("Did not expect auto-pagination command when viewport is already filled")
	}
}

func TestTUIEmptyQueueRendersPaddedHeight(t *testing.T) {
	// Test that empty queue view pads output to fill terminal height
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{} // Empty queue
	m.loadingJobs = false          // Not loading, so should show "No jobs in queue"

	output := m.View()

	// Count total lines (including empty ones from padding)
	lines := strings.Split(output, "\n")

	// Strip ANSI codes and count non-empty content
	// The output should fill most of the terminal height
	// Accounting for: title(1) + status(2) + content/padding + scroll(1) + update(1) + help(2)
	// Minimum expected lines is close to m.height
	if len(lines) < m.height-3 {
		t.Errorf("Empty queue should pad to near terminal height, got %d lines for height %d", len(lines), m.height)
	}

	// Should contain the "No jobs in queue" message
	if !strings.Contains(output, "No jobs in queue") {
		t.Error("Expected 'No jobs in queue' message in output")
	}
}

func TestTUIEmptyQueueWithFilterRendersPaddedHeight(t *testing.T) {
	// Test that empty queue with filter pads output correctly
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{}
	m.activeRepoFilter = []string{"/some/repo"} // Filter active but no matching jobs
	m.loadingJobs = false                       // Not loading, so should show "No jobs matching filters"

	output := m.View()

	lines := strings.Split(output, "\n")
	if len(lines) < m.height-3 {
		t.Errorf("Empty filtered queue should pad to near terminal height, got %d lines for height %d", len(lines), m.height)
	}

	// Should contain the filter message
	if !strings.Contains(output, "No jobs matching filters") {
		t.Error("Expected 'No jobs matching filters' message in output")
	}
}

func TestTUILoadingJobsShowsLoadingMessage(t *testing.T) {
	// Test that loading state shows "Loading..." instead of "No jobs" messages
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{}
	m.loadingJobs = true // Loading in progress

	output := m.View()

	if !strings.Contains(output, "Loading...") {
		t.Error("Expected 'Loading...' message when loadingJobs is true")
	}
	if strings.Contains(output, "No jobs in queue") {
		t.Error("Should not show 'No jobs in queue' while loading")
	}
	if strings.Contains(output, "No jobs matching filters") {
		t.Error("Should not show 'No jobs matching filters' while loading")
	}
}

func TestTUILoadingShowsForLoadingMore(t *testing.T) {
	// Test that "Loading..." shows when loadingMore is set on empty queue
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{} // Empty after filter clear
	m.loadingJobs = false
	m.loadingMore = true // Pagination in flight

	output := m.View()

	if !strings.Contains(output, "Loading...") {
		t.Error("Expected 'Loading...' message when loadingMore is true")
	}
}

func TestTUIQueueNoScrollIndicatorPads(t *testing.T) {
	// Test that queue view with few jobs (no scroll indicator) still maintains height
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.width = 100
	m.height = 30
	// Add just 2 jobs - should not need scroll indicator
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc123"), withAgent("test")),
		makeJob(2, withRef("def456"), withAgent("test")),
	}

	output := m.View()

	lines := strings.Split(output, "\n")
	// Even with few jobs, output should be close to terminal height
	if len(lines) < m.height-5 {
		t.Errorf("Queue with few jobs should maintain height, got %d lines for height %d", len(lines), m.height)
	}
}

func setupQueue(jobs []storage.ReviewJob, selectedIdx int) tuiModel {
	m := newQueueTestModel(
		withQueueTestJobs(jobs...),
		withQueueTestSelection(selectedIdx),
	)
	return m
}

func TestTUIJobAddressedTransitions(t *testing.T) {
	tests := []struct {
		name             string
		initialJobs      []storage.ReviewJob
		initialPending   map[int64]pendingState // map[ID]state
		msg              tea.Msg
		wantPending      bool  // Is pending state expected to remain?
		wantPendingState *bool // If remaining, expected newState? (nil to skip value check)
		wantAddressed    *bool // Expected job.Addressed state (nil to skip)
		wantError        bool  // Expected error in model
	}{
		{
			name:           "Late error ignored (same state, diff seq)",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))},
			initialPending: map[int64]pendingState{1: {newState: true, seq: 3}},
			msg: tuiAddressedResultMsg{
				jobID: 1, oldState: false, newState: true, seq: 1,
				err: fmt.Errorf("late error"),
			},
			wantPending:      true,
			wantPendingState: boolPtr(true),
			wantAddressed:    boolPtr(true), // Optimistically true
			wantError:        false,
		},
		{
			name:           "Stale error response ignored",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(true)))},
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg: tuiAddressedResultMsg{
				jobID: 1, oldState: true, newState: false, seq: 0,
				err: fmt.Errorf("network error"),
			},
			wantPending:      true,
			wantPendingState: boolPtr(true),
			wantAddressed:    boolPtr(true),
			wantError:        false,
		},
		{
			name:           "Cleared when server nil matches pending false",
			initialJobs:    []storage.ReviewJob{makeJob(1)},
			initialPending: map[int64]pendingState{1: {newState: false, seq: 1}},
			msg:            tuiJobsMsg{jobs: []storage.ReviewJob{makeJob(1)}}, // Addressed is nil
			wantPending:    false,
		},
		{
			name:           "Not cleared when server nil mismatches pending true",
			initialJobs:    []storage.ReviewJob{makeJob(1)},
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg:            tuiJobsMsg{jobs: []storage.ReviewJob{makeJob(1)}}, // Addressed is nil
			wantPending:    true,
			wantAddressed:  boolPtr(true),
		},
		{
			name:           "Not cleared by stale response (mismatched newState)",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))},
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg: tuiAddressedResultMsg{
				jobID: 1, oldState: true, newState: false, seq: 0,
			},
			wantPending:      true,
			wantPendingState: boolPtr(true),
		},
		{
			name:           "Not cleared on success (waits for refresh)",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))},
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg: tuiAddressedResultMsg{
				jobID: 1, oldState: false, newState: true, seq: 1,
			},
			wantPending: true,
		},
		{
			name:           "Cleared by jobs refresh",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))},
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg:            tuiJobsMsg{jobs: []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(true)))}},
			wantPending:    false,
			wantAddressed:  boolPtr(true),
		},
		{
			name:           "Not cleared by stale jobs refresh",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))},
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg:            tuiJobsMsg{jobs: []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))}},
			wantPending:    true,
			wantAddressed:  boolPtr(true),
		},
		{
			name:           "Cleared on current error",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(true)))},
			initialPending: map[int64]pendingState{1: {newState: false, seq: 1}},
			msg: tuiAddressedResultMsg{
				jobID: 1, oldState: true, newState: false, seq: 1,
				err: fmt.Errorf("server error"),
			},
			wantPending:   false,
			wantAddressed: boolPtr(true), // Rolled back to oldState (true)
			wantError:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := setupQueue(tt.initialJobs, 0)
			if tt.initialPending != nil {
				m.pendingAddressed = make(map[int64]pendingState, len(tt.initialPending))
				maps.Copy(m.pendingAddressed, tt.initialPending)
				for id, p := range tt.initialPending {
					for i := range m.jobs {
						if m.jobs[i].ID == id {
							val := p.newState
							m.jobs[i].Addressed = &val
						}
					}
				}
			}

			m2, _ := updateModel(t, m, tt.msg)

			for id, p := range tt.initialPending {
				val, exists := m2.pendingAddressed[id]
				if tt.wantPending && !exists {
					t.Errorf("expected pending state for job %d to remain", id)
				}
				if !tt.wantPending && exists {
					t.Errorf("expected pending state for job %d to be cleared", id)
				}
				if exists && tt.wantPendingState != nil {
					if val.newState != *tt.wantPendingState {
						t.Errorf("expected pending newState %v, got %v", *tt.wantPendingState, val.newState)
					}
				}
				if exists && val.seq != p.seq {
					t.Errorf("expected pending seq %d, got %d", p.seq, val.seq)
				}
			}

			if tt.wantAddressed != nil && len(m2.jobs) > 0 {
				got := m2.jobs[0].Addressed
				if got == nil {
					if *tt.wantAddressed {
						t.Error("expected addressed to be true, got nil")
					}
				} else if *got != *tt.wantAddressed {
					t.Errorf("expected addressed %v, got %v", *tt.wantAddressed, *got)
				}
			}

			if tt.wantError && m2.err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantError && m2.err != nil {
				t.Errorf("unexpected error: %v", m2.err)
			}
		})
	}
}

func TestTUIReviewAddressedTransitions(t *testing.T) {
	tests := []struct {
		name                 string
		initialReviewPending map[int64]pendingState // map[ReviewID]state for review-only cases
		msg                  tea.Msg
		wantPending          bool // Is pending state expected to remain?
	}{
		{
			name:                 "Pending review-only cleared on success",
			initialReviewPending: map[int64]pendingState{42: {newState: true, seq: 1}},
			msg: tuiAddressedResultMsg{
				jobID: 0, reviewID: 42, reviewView: true, oldState: false, newState: true, seq: 1,
			},
			wantPending: false, // Should be cleared from pendingReviewAddressed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := setupQueue(nil, 0)
			if tt.initialReviewPending != nil {
				m.pendingReviewAddressed = make(map[int64]pendingState, len(tt.initialReviewPending))
				maps.Copy(m.pendingReviewAddressed, tt.initialReviewPending)
			}

			m2, _ := updateModel(t, m, tt.msg)

			for id := range tt.initialReviewPending {
				_, exists := m2.pendingReviewAddressed[id]
				if tt.wantPending && !exists {
					t.Errorf("expected pending state for review %d to remain", id)
				}
				if !tt.wantPending && exists {
					t.Errorf("expected pending state for review %d to be cleared", id)
				}
			}
		})
	}
}

func TestTUIAddressedHideAddressedStats(t *testing.T) {
	tests := []struct {
		name           string
		initialJobs    []storage.ReviewJob
		initialPending map[int64]pendingState
		initialStats   storage.JobStats
		msg            tea.Msg
		wantPending    bool
		wantStats      *storage.JobStats
	}{
		{
			name:           "HideAddressed stats not double counted",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))},
			initialStats:   storage.JobStats{Done: 10, Addressed: 6, Unaddressed: 4}, // Pre-optimistic
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg: tuiJobsMsg{
				jobs:  []storage.ReviewJob{},                                    // Filtered out
				stats: storage.JobStats{Done: 10, Addressed: 6, Unaddressed: 4}, // Server matches optimistic
			},
			wantPending: false,
			wantStats:   &storage.JobStats{Addressed: 6, Unaddressed: 4},
		},
		{
			name:           "HideAddressed pending not cleared when server lags",
			initialJobs:    []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))},
			initialStats:   storage.JobStats{Done: 10, Addressed: 6, Unaddressed: 4}, // Pre-optimistic
			initialPending: map[int64]pendingState{1: {newState: true, seq: 1}},
			msg: tuiJobsMsg{
				jobs:  []storage.ReviewJob{makeJob(1, withAddressed(boolPtr(false)))}, // Server still old
				stats: storage.JobStats{Done: 10, Addressed: 5, Unaddressed: 5},
			},
			wantPending: true,
			wantStats:   &storage.JobStats{Addressed: 6, Unaddressed: 4}, // Re-applied delta
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := setupQueue(tt.initialJobs, 0)
			m.hideAddressed = true
			if tt.initialPending != nil {
				m.pendingAddressed = make(map[int64]pendingState, len(tt.initialPending))
				maps.Copy(m.pendingAddressed, tt.initialPending)
				for id, p := range tt.initialPending {
					for i := range m.jobs {
						if m.jobs[i].ID == id {
							val := p.newState
							m.jobs[i].Addressed = &val
						}
					}
				}
			}
			m.jobStats = tt.initialStats

			m2, _ := updateModel(t, m, tt.msg)

			for id := range tt.initialPending {
				_, exists := m2.pendingAddressed[id]
				if tt.wantPending && !exists {
					t.Errorf("expected pending state for job %d to remain", id)
				}
				if !tt.wantPending && exists {
					t.Errorf("expected pending state for job %d to be cleared", id)
				}
			}

			if tt.wantStats != nil {
				if m2.jobStats.Addressed != tt.wantStats.Addressed {
					t.Errorf("stats.Addressed = %d, want %d", m2.jobStats.Addressed, tt.wantStats.Addressed)
				}
				if m2.jobStats.Unaddressed != tt.wantStats.Unaddressed {
					t.Errorf("stats.Unaddressed = %d, want %d", m2.jobStats.Unaddressed, tt.wantStats.Unaddressed)
				}
			}
		})
	}
}

func TestTUIQueueNavigationSequences(t *testing.T) {
	// Test sequence of keypresses to ensure state is maintained
	threeJobs := []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}

	m := setupQueue(threeJobs, 0)

	// Sequence: j (down), j (down), k (up)
	// Start: idx=0
	m, _ = pressKey(m, 'j')
	if m.selectedIdx != 1 {
		t.Errorf("after 'j', expected selectedIdx 1, got %d", m.selectedIdx)
	}

	m, _ = pressKey(m, 'j')
	if m.selectedIdx != 2 {
		t.Errorf("after second 'j', expected selectedIdx 2, got %d", m.selectedIdx)
	}

	m, _ = pressKey(m, 'k')
	if m.selectedIdx != 1 {
		t.Errorf("after 'k', expected selectedIdx 1, got %d", m.selectedIdx)
	}

	// Sequence: j (down to bottom), g (top)
	m, _ = pressKey(m, 'j')
	if m.selectedIdx != 2 {
		t.Errorf("after 'j', expected selectedIdx 2, got %d", m.selectedIdx)
	}

	m, _ = pressKey(m, 'g')
	if m.selectedIdx != 0 {
		t.Errorf("after 'g', expected selectedIdx 0, got %d", m.selectedIdx)
	}

	// Sequence: Right (down/next), Left (up/prev)
	// We are at index 0
	m, _ = pressSpecial(m, tea.KeyRight)
	if m.selectedIdx != 1 {
		t.Errorf("after KeyRight, expected selectedIdx 1, got %d", m.selectedIdx)
	}

	m, _ = pressSpecial(m, tea.KeyLeft)
	if m.selectedIdx != 0 {
		t.Errorf("after KeyLeft, expected selectedIdx 0, got %d", m.selectedIdx)
	}
}

type queueTestModelOption func(*tuiModel)

func withQueueTestJobs(jobs ...storage.ReviewJob) queueTestModelOption {
	return func(m *tuiModel) {
		m.jobs = jobs
	}
}

func withQueueTestSelection(idx int) queueTestModelOption {
	return func(m *tuiModel) {
		m.selectedIdx = idx
		if len(m.jobs) > 0 && idx >= 0 && idx < len(m.jobs) {
			m.selectedJobID = m.jobs[idx].ID
		}
	}
}

func withQueueTestFlags(hasMore, loadingMore, loadingJobs bool) queueTestModelOption {
	return func(m *tuiModel) {
		m.hasMore = hasMore
		m.loadingMore = loadingMore
		m.loadingJobs = loadingJobs
	}
}

func newQueueTestModel(opts ...queueTestModelOption) tuiModel {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func assertFlashMessage(t *testing.T, m tuiModel, view tuiView, msg string) {
	t.Helper()
	if m.flashMessage != msg {
		t.Errorf("Expected flash message %q, got %q", msg, m.flashMessage)
	}
	if m.flashView != view {
		t.Errorf("Expected flashView to be %d, got %d", view, m.flashView)
	}
	if m.flashExpiresAt.IsZero() || m.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m.flashExpiresAt)
	}
}
