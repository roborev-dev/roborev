package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIQueueViewNavigationUpDown(t *testing.T) {
	// Test up/down/j/k navigation in queue view
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
		makeJob(2, withStatus(storage.JobStatusQueued)),
		makeJob(3),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewQueue

	// 'j' in queue view moves down (higher index)
	m2, _ := pressKey(m, 'j')

	if m2.selectedIdx != 2 {
		t.Errorf("j key: expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("j key: expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	// 'k' in queue view moves up (lower index)
	m3, _ := pressKey(m2, 'k')

	if m3.selectedIdx != 1 {
		t.Errorf("k key: expected selectedIdx=1, got %d", m3.selectedIdx)
	}
}

func TestTUIQueueViewArrowsMatchUpDown(t *testing.T) {
	// Test that left/right in queue view work like k/j (up/down)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewQueue

	// 'left' in queue view should move down (like j)
	m2, _ := pressSpecial(m, tea.KeyLeft)

	if m2.selectedIdx != 2 {
		t.Errorf("Left arrow: expected selectedIdx=2, got %d", m2.selectedIdx)
	}

	// Reset
	m2.selectedIdx = 1
	m2.selectedJobID = 2

	// 'right' in queue view should move up (like k)
	m3, _ := pressSpecial(m2, tea.KeyRight)

	if m3.selectedIdx != 0 {
		t.Errorf("Right arrow: expected selectedIdx=0, got %d", m3.selectedIdx)
	}
}

func TestTUIQueueNavigationBoundaries(t *testing.T) {
	// Test flash messages when navigating at queue boundaries
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.hasMore = false // No more jobs to load

	// Press 'up' at top of queue - should show flash message
	m2, _ := pressSpecial(m, tea.KeyUp)

	if m2.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx to remain 0 at top, got %d", m2.selectedIdx)
	}
	if m2.flashMessage != "No newer review" {
		t.Errorf("Expected flash message 'No newer review', got %q", m2.flashMessage)
	}
	if m2.flashView != tuiViewQueue {
		t.Errorf("Expected flashView to be tuiViewQueue, got %d", m2.flashView)
	}
	if m2.flashExpiresAt.IsZero() || m2.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m2.flashExpiresAt)
	}

	// Now at bottom of queue
	m.selectedIdx = 2
	m.selectedJobID = 3
	m.flashMessage = "" // Clear

	// Press 'down' at bottom of queue - should show flash message
	m3, _ := pressSpecial(m, tea.KeyDown)

	if m3.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx to remain 2 at bottom, got %d", m3.selectedIdx)
	}
	if m3.flashMessage != "No older review" {
		t.Errorf("Expected flash message 'No older review', got %q", m3.flashMessage)
	}
	if m3.flashView != tuiViewQueue {
		t.Errorf("Expected flashView to be tuiViewQueue, got %d", m3.flashView)
	}
	if m3.flashExpiresAt.IsZero() || m3.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m3.flashExpiresAt)
	}
}

func TestTUIQueueNavigationBoundariesWithFilter(t *testing.T) {
	// Test flash messages at bottom when multi-repo filter is active (prevents auto-load).
	// Single-repo filters use server-side filtering and support pagination,
	// but multi-repo filters are client-side only so they disable pagination.
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoPath("/repo1")),
		makeJob(2, withRepoPath("/repo2")),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewQueue
	m.hasMore = true                                  // More jobs available but...
	m.activeRepoFilter = []string{"/repo1", "/repo2"} // Multi-repo filter prevents auto-load

	// Press 'down' - already at last job, should hit boundary
	m2, _ := pressSpecial(m, tea.KeyDown)

	// Should show flash since multi-repo filter prevents loading more
	if m2.flashMessage != "No older review" {
		t.Errorf("Expected flash message 'No older review' with multi-repo filter active, got %q", m2.flashMessage)
	}
	if m2.flashView != tuiViewQueue {
		t.Errorf("Expected flashView to be tuiViewQueue, got %d", m2.flashView)
	}
	if m2.flashExpiresAt.IsZero() || m2.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m2.flashExpiresAt)
	}
}

func TestTUIQueueJumpToTop(t *testing.T) {
	t.Run("unfiltered queue", func(t *testing.T) {
		m := newTuiModel("http://localhost")
		m.jobs = []storage.ReviewJob{
			makeJob(1),
			makeJob(2),
			makeJob(3),
		}
		m.selectedIdx = 2
		m.selectedJobID = 3
		m.currentView = tuiViewQueue

		m2, _ := pressKey(m, 'g')

		if m2.selectedIdx != 0 {
			t.Errorf("Expected selectedIdx=0 after jump to top, got %d", m2.selectedIdx)
		}
		if m2.selectedJobID != 1 {
			t.Errorf("Expected selectedJobID=1 after jump to top, got %d", m2.selectedJobID)
		}
	})

	t.Run("with active filter skips hidden jobs", func(t *testing.T) {
		m := newTuiModel("http://localhost")
		m.jobs = []storage.ReviewJob{
			makeJob(1, withRepoPath("/repo/alpha")),
			makeJob(2, withRepoPath("/repo/beta")),
			makeJob(3, withRepoPath("/repo/beta")),
		}
		m.activeRepoFilter = []string{"/repo/beta"}
		m.selectedIdx = 2
		m.selectedJobID = 3
		m.currentView = tuiViewQueue

		m2, _ := pressKey(m, 'g')

		// Job 1 is filtered out; first visible is job 2 at index 1
		if m2.selectedIdx != 1 {
			t.Errorf("Expected selectedIdx=1 (first visible), got %d", m2.selectedIdx)
		}
		if m2.selectedJobID != 2 {
			t.Errorf("Expected selectedJobID=2 (first visible), got %d", m2.selectedJobID)
		}
	})

	t.Run("empty queue is no-op", func(t *testing.T) {
		m := newTuiModel("http://localhost")
		m.jobs = []storage.ReviewJob{}
		m.selectedIdx = 0
		m.selectedJobID = 0
		m.currentView = tuiViewQueue

		m2, _ := pressKey(m, 'g')

		if m2.selectedIdx != 0 {
			t.Errorf("Expected selectedIdx unchanged at 0, got %d", m2.selectedIdx)
		}
		if m2.selectedJobID != 0 {
			t.Errorf("Expected selectedJobID unchanged at 0, got %d", m2.selectedJobID)
		}
	})
}

func TestTUINavigateDownTriggersLoadMore(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up at last job with more available
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false // Must be false to allow pagination
	m.currentView = tuiViewQueue

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
	m := newTuiModel("http://localhost")

	// Set up at last job with filter active
	m.jobs = []storage.ReviewJob{makeJob(1, withRepoPath("/path/to/repo"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeRepoFilter = []string{"/path/to/repo"} // Filter active
	m.currentView = tuiViewQueue

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
	m := newTuiModel("http://localhost")

	// Start with 50 jobs
	initialJobs := make([]storage.ReviewJob, 50)
	for i := 0; i < 50; i++ {
		initialJobs[i] = makeJob(int64(50 - i))
	}
	m.jobs = initialJobs
	m.selectedIdx = 0
	m.selectedJobID = 50
	m.hasMore = true

	// Append 25 more jobs
	moreJobs := make([]storage.ReviewJob, 25)
	for i := 0; i < 25; i++ {
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
	m := newTuiModel("http://localhost")

	// Simulate user has paginated to 100 jobs
	jobs := make([]storage.ReviewJob, 100)
	for i := 0; i < 100; i++ {
		jobs[i] = makeJob(int64(100 - i))
	}
	m.jobs = jobs
	m.selectedIdx = 50
	m.selectedJobID = 50

	// Refresh arrives (replace mode, not append)
	refreshedJobs := make([]storage.ReviewJob, 100)
	for i := 0; i < 100; i++ {
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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

func TestTUIResizeDuringPaginationNoRefetch(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with jobs loaded and pagination in flight
	m.jobs = []storage.ReviewJob{makeJob(1), makeJob(2), makeJob(3)}
	m.hasMore = true
	m.loadingMore = true // Pagination in progress
	m.heightDetected = false
	m.height = 24 // Default height

	// Simulate WindowSizeMsg arriving while pagination is in flight
	m2, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 80})

	// Height should be updated
	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}
	if !m2.heightDetected {
		t.Error("heightDetected should be true after WindowSizeMsg")
	}

	// Should NOT trigger a re-fetch because loadingMore is true
	if cmd != nil {
		t.Error("Should not return command when loadingMore is true (pagination in flight)")
	}
}

func TestTUIResizeTriggersRefetchWhenNeeded(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with few jobs loaded, more available, no pagination in flight
	m.jobs = []storage.ReviewJob{makeJob(1), makeJob(2), makeJob(3)}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false // Must be false to allow refetch
	m.heightDetected = false
	m.height = 24 // Default height

	// Simulate WindowSizeMsg arriving - tall terminal can show more jobs
	m2, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 80})

	// Height should be updated
	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}
	if !m2.heightDetected {
		t.Error("heightDetected should be true after WindowSizeMsg")
	}

	// Should trigger a re-fetch because we can show more rows than we have jobs
	// newVisibleRows = 80 - 9 + 10 = 81, which is > len(jobs)=3
	if cmd == nil {
		t.Error("Should return fetchJobs command when terminal can show more jobs")
	}
}

func TestTUIResizeNoRefetchWhenEnoughJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with enough jobs to fill the terminal
	jobs := make([]storage.ReviewJob, 100)
	for i := range jobs {
		jobs[i] = makeJob(int64(i + 1))
	}
	m.jobs = jobs
	m.hasMore = true
	m.loadingMore = false
	m.heightDetected = true
	m.height = 60

	// Simulate WindowSizeMsg - terminal grows but we already have enough jobs
	// newVisibleRows = 80 - 9 + 10 = 81, which is < len(jobs)=100
	m2, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 80})

	// Height should be updated
	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}

	// Should NOT trigger a re-fetch because we have enough jobs already
	if cmd != nil {
		t.Error("Should not return command when we already have enough jobs to fill screen")
	}
}

func TestTUIResizeRefetchOnLaterResize(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with few jobs, height already detected from earlier resize
	m.jobs = []storage.ReviewJob{makeJob(1), makeJob(2), makeJob(3)}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false // Must be false to allow refetch
	m.heightDetected = true
	m.height = 30

	// Simulate terminal growing larger - can now show more jobs
	// newVisibleRows = 80 - 9 + 10 = 81, which is > len(jobs)=3
	m2, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 80})

	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}

	// Should trigger a re-fetch because terminal can show more jobs than we have
	if cmd == nil {
		t.Error("Should return fetchJobs command when terminal grows and can show more jobs")
	}

	// loadingJobs should be set
	if !m2.loadingJobs {
		t.Error("loadingJobs should be true after resize triggers fetch")
	}
}

func TestTUIResizeNoRefetchWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with few jobs, but loadingJobs is already true (fetch in progress)
	m.jobs = []storage.ReviewJob{makeJob(1), makeJob(2), makeJob(3)}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = true // Already fetching
	m.heightDetected = true
	m.height = 30

	// Simulate terminal growing larger
	m2, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 80})

	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}

	// Should NOT trigger another fetch because loadingJobs is true
	if cmd != nil {
		t.Error("Should not return command when loadingJobs is true (fetch already in progress)")
	}
}

func TestTUIJobsMsgHideAddressedUnderfilledViewportAutoPaginates(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	m.height = 29 // queueVisibleRows = 20
	m.loadingJobs = true

	// 13 visible (done + unaddressed), 12 hidden (failed) in this page.
	jobs := make([]storage.ReviewJob, 0, 25)
	var id int64 = 200
	for i := 0; i < 13; i++ {
		jobs = append(jobs, makeJob(id, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))))
		id--
	}
	for i := 0; i < 12; i++ {
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
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	m.height = 29 // queueVisibleRows = 21
	m.loadingJobs = true

	// 21 visible rows already available (plus hidden jobs).
	jobs := make([]storage.ReviewJob, 0, 26)
	var id int64 = 300
	for i := 0; i < 21; i++ {
		jobs = append(jobs, makeJob(id, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))))
		id--
	}
	for i := 0; i < 5; i++ {
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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
	m := newTuiModel("http://localhost")
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

func TestTUIQueueViewSameStateLateError(t *testing.T) {
	// Test: true (seq 1) → false (seq 2) → true (seq 3), with late error from first true
	// Same as TestTUIReviewViewSameStateLateError but for queue view using pendingAddressed
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}

	// Sequence: toggle true (seq 1) → toggle false (seq 2) → toggle true (seq 3)
	// After third toggle, state is true and pendingAddressed has seq 3
	*m.jobs[0].Addressed = true                                  // Optimistic update from third toggle
	m.pendingAddressed[1] = pendingState{newState: true, seq: 3} // Third toggle

	// A late error arrives from the FIRST toggle (seq 1)
	// This error has newState=true which matches current pending newState,
	// but seq doesn't match, so it should be treated as stale and ignored.
	lateErrorMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: false, // First toggle was from false to true
		newState: true,  // Same newState as current pending...
		seq:      1,     // ...but different seq, so this is stale
		err:      fmt.Errorf("network error from first toggle"),
	}

	m2, _ := updateModel(t, m, lateErrorMsg)

	// With sequence numbers, the late error should be IGNORED (not rolled back)
	// because seq: 1 != pending seq: 3
	if m2.jobs[0].Addressed == nil || *m2.jobs[0].Addressed != true {
		t.Errorf("Expected addressed to stay true (late error should be ignored), got %v", m2.jobs[0].Addressed)
	}

	// Error should NOT be set (stale error)
	if m2.err != nil {
		t.Errorf("Error should not be set for stale error response, got %v", m2.err)
	}

	// pendingAddressed should still be set (not cleared by stale response)
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should not be cleared by stale response")
	}
}

func TestTUIStaleErrorResponseIgnored(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(true))),
	}

	// User toggles to false, then back to true (pendingAddressed=true)
	// The job.Addressed was updated optimistically to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}
	*m.jobs[0].Addressed = true // Optimistic update already applied

	// A stale error response arrives from the earlier toggle to false
	staleErrorMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: true,  // What it was before the (stale) toggle
		newState: false, // Stale request was for false, but pending is now true
		seq:      0,     // Stale: doesn't match pending seq (1)
		err:      fmt.Errorf("network error"),
	}

	m2, _ := updateModel(t, m, staleErrorMsg)

	// pendingAddressed should NOT be cleared (stale error)
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should not be cleared by stale error response")
	}
	if m2.pendingAddressed[1].newState != true {
		t.Error("pendingAddressed value should remain true")
	}

	// Job state should NOT be rolled back (stale error)
	if m2.jobs[0].Addressed == nil || *m2.jobs[0].Addressed != true {
		t.Error("Job addressed state should not be rolled back by stale error")
	}

	// Error should NOT be set (stale error is silently ignored)
	if m2.err != nil {
		t.Error("Error should not be set for stale error response")
	}
}

func TestTUIPendingReviewAddressedClearedOnSuccess(t *testing.T) {
	// pendingReviewAddressed (for reviews without jobs) should be cleared on success
	// because the race condition only affects jobs list refresh, not review-only items
	m := newTuiModel("http://localhost")

	// Track a review-only pending state (no job ID)
	m.pendingReviewAddressed[42] = pendingState{newState: true, seq: 1}

	// Success response for review-only (jobID=0, reviewID=42)
	successMsg := tuiAddressedResultMsg{
		jobID:      0, // No job - this is review-only
		reviewID:   42,
		reviewView: true,
		oldState:   false,
		newState:   true,
		seq:        1,
		err:        nil,
	}

	m2, _ := updateModel(t, m, successMsg)

	// pendingReviewAddressed SHOULD be cleared on success (no race condition for review-only)
	if _, ok := m2.pendingReviewAddressed[42]; ok {
		t.Error("pendingReviewAddressed should be cleared on success for review-only items")
	}
}

func TestTUIPendingAddressedClearsWhenServerNilMatchesFalse(t *testing.T) {
	// When pending newState is false and server Addressed is nil,
	// treat nil as false and clear the pending state
	m := newTuiModel("http://localhost")

	// Job with nil Addressed (e.g., partial payload or non-done status that became done)
	m.jobs = []storage.ReviewJob{
		makeJob(1),
	}

	// User had toggled to false (unaddressed)
	m.pendingAddressed[1] = pendingState{newState: false, seq: 1}

	// Jobs refresh arrives with nil Addressed (should match false)
	jobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			makeJob(1),
		},
	}

	m2, _ := updateModel(t, m, jobsMsg)

	// pendingAddressed should be cleared because nil == false matches newState=false
	if _, ok := m2.pendingAddressed[1]; ok {
		t.Error("pendingAddressed should be cleared when server nil matches pending false")
	}
}

func TestTUIPendingAddressedNotClearsWhenServerNilMismatchesTrue(t *testing.T) {
	// When pending newState is true and server Addressed is nil,
	// do NOT clear (nil != true)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
	}

	// User toggled to true (addressed)
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Jobs refresh arrives with nil Addressed (doesn't match true)
	jobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			makeJob(1),
		},
	}

	m2, _ := updateModel(t, m, jobsMsg)

	// pendingAddressed should NOT be cleared because nil != true
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should NOT be cleared when server nil mismatches pending true")
	}

	// Job should show as addressed due to pending state re-applied
	if m2.jobs[0].Addressed == nil || !*m2.jobs[0].Addressed {
		t.Error("Job should show as addressed due to pending state")
	}
}

func TestTUIPendingAddressedNotClearedByStaleResponse(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// A stale response comes back for a previous toggle to false
	// (this could happen if user rapidly toggles)
	staleMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: true,
		newState: false, // This response was for a toggle to false
		seq:      0,     // Stale: doesn't match pending seq (1)
		err:      nil,
	}

	m2, _ := updateModel(t, m, staleMsg)

	// pendingAddressed should NOT be cleared because newState (false) != current pending (true)
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should not be cleared by stale response with mismatched newState")
	}
	if m2.pendingAddressed[1].newState != true {
		t.Error("pendingAddressed value should remain true")
	}
}

func TestTUIPendingAddressedNotClearedOnSuccess(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Success response comes back
	successMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: false,
		newState: true,
		seq:      1,
		err:      nil,
	}

	m2, _ := updateModel(t, m, successMsg)

	// pendingAddressed should NOT be cleared on success - it waits for jobs refresh
	// to confirm the update. This prevents race condition where stale jobs response
	// arrives after success and briefly shows old state.
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should NOT be cleared on success response")
	}
}

func TestTUIPendingAddressedClearedByJobsRefresh(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Jobs refresh arrives with server data confirming the update

	jobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			makeJob(1, withAddressed(boolPtr(true))),
		},
	}

	m2, _ := updateModel(t, m, jobsMsg)

	// pendingAddressed should be cleared because server data matches pending state
	if _, ok := m2.pendingAddressed[1]; ok {
		t.Error("pendingAddressed should be cleared when jobs refresh confirms update")
	}
}

func TestTUIPendingAddressedNotClearedByStaleJobsRefresh(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Stale jobs refresh arrives with old data (from request sent before update)
	staleJobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			makeJob(1, withAddressed(boolPtr(false))), // Still false!
		},
	}

	m2, _ := updateModel(t, m, staleJobsMsg)

	// pendingAddressed should NOT be cleared because server data doesn't match
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should NOT be cleared when jobs refresh has stale data")
	}

	// Job should still show as addressed (pending state re-applied)
	if m2.jobs[0].Addressed == nil || !*m2.jobs[0].Addressed {
		t.Error("Job should still show as addressed due to pending state")
	}
}

func TestTUIPendingAddressedClearedOnCurrentError(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(true))),
	}

	// User toggles addressed to false
	m.pendingAddressed[1] = pendingState{newState: false, seq: 1}

	// Error response comes back for the current request (seq matches pending)
	errorMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: true,
		newState: false, // Matches pendingAddressed[1]
		seq:      1,     // Matches pending seq
		err:      fmt.Errorf("server error"),
	}

	m2, _ := updateModel(t, m, errorMsg)

	// pendingAddressed should be cleared on error (we're rolling back)
	if _, ok := m2.pendingAddressed[1]; ok {
		t.Error("pendingAddressed should be cleared on current error")
	}

	// Job state should be rolled back
	if m2.jobs[0].Addressed == nil || *m2.jobs[0].Addressed != true {
		t.Error("Job addressed state should be rolled back to oldState on error")
	}

	// Error should be set
	if m2.err == nil {
		t.Error("Error should be set on current error")
	}
}
