package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

// setupFilterTree is a helper that sets filterTree and rebuilds the flat list.
func setupFilterTree(m *tuiModel, nodes []treeFilterNode) {
	m.filterTree = nodes
	m.rebuildFilterFlatList()
}

func TestTUIFilterOpenModal(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a")),
		makeJob(2, withRepoName("repo-b")),
		makeJob(3, withRepoName("repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Press 'f' to open filter modal
	m2, cmd := pressKey(m, 'f')

	if m2.currentView != tuiViewFilter {
		t.Errorf("Expected tuiViewFilter, got %d", m2.currentView)
	}
	// filterTree should be nil (loading state) until async fetch completes
	if m2.filterTree != nil {
		t.Errorf("Expected filterTree=nil (loading), got %d nodes", len(m2.filterTree))
	}
	if m2.filterSelectedIdx != 0 {
		t.Errorf("Expected filterSelectedIdx=0, got %d", m2.filterSelectedIdx)
	}
	if m2.filterSearch != "" {
		t.Errorf("Expected empty filterSearch, got '%s'", m2.filterSearch)
	}
	if cmd == nil {
		t.Error("Expected a fetch command to be returned")
	}
}

func TestTUIFilterReposMsg(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter

	// Simulate receiving repos from API
	repos := []repoFilterItem{
		{name: "repo-a", count: 2},
		{name: "repo-b", count: 1},
		{name: "repo-c", count: 1},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 4}

	m2, _ := updateModel(t, m, msg)

	// Should have 3 tree nodes (one per repo)
	if len(m2.filterTree) != 3 {
		t.Fatalf("Expected 3 tree nodes, got %d", len(m2.filterTree))
	}
	if m2.filterTree[0].name != "repo-a" || m2.filterTree[0].count != 2 {
		t.Errorf("Expected repo-a with count 2, got name='%s' count=%d", m2.filterTree[0].name, m2.filterTree[0].count)
	}
	if m2.filterTree[1].name != "repo-b" || m2.filterTree[1].count != 1 {
		t.Errorf("Expected repo-b with count 1, got name='%s' count=%d", m2.filterTree[1].name, m2.filterTree[1].count)
	}
	if m2.filterTree[2].name != "repo-c" || m2.filterTree[2].count != 1 {
		t.Errorf("Expected repo-c with count 1, got name='%s' count=%d", m2.filterTree[2].name, m2.filterTree[2].count)
	}
	// Flat list should have: All + 3 repos = 4 entries
	if len(m2.filterFlatList) != 4 {
		t.Errorf("Expected 4 flat list entries, got %d", len(m2.filterFlatList))
	}
}

func TestTUIFilterSearch(t *testing.T) {
	m := newTuiModel("http://localhost")

	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-alpha", count: 5},
		{name: "repo-beta", count: 3},
		{name: "something-else", count: 2},
	})

	// No search - all visible (All + 3 repos)
	if len(m.filterFlatList) != 4 {
		t.Errorf("No search: expected 4 visible, got %d", len(m.filterFlatList))
	}

	// Search for "repo"
	m.filterSearch = "repo"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 2 { // repo-alpha + repo-beta (All is excluded because "all" doesn't contain "repo")
		t.Errorf("Search 'repo': expected 2 visible, got %d", len(m.filterFlatList))
	}

	// Search for "alpha"
	m.filterSearch = "alpha"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 { // repo-alpha only
		t.Errorf("Search 'alpha': expected 1 visible, got %d", len(m.filterFlatList))
	}

	// Search for "xyz" - no matches
	m.filterSearch = "xyz"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 0 {
		t.Errorf("Search 'xyz': expected 0 visible, got %d", len(m.filterFlatList))
	}

	// Search for "all" - should show All row
	m.filterSearch = "all"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 { // Just All
		t.Errorf("Search 'all': expected 1 visible, got %d", len(m.filterFlatList))
	}
	if m.filterFlatList[0].repoIdx != -1 {
		t.Error("Expected the visible item to be the All entry")
	}
}

func TestTUIFilterNavigation(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", count: 5},
		{name: "repo-b", count: 3},
	})
	// Flat list: All, repo-a, repo-b = 3 entries
	m.filterSelectedIdx = 0

	// Navigate down
	m2, _ := pressKey(m, 'j')
	if m2.filterSelectedIdx != 1 {
		t.Errorf("j key: expected filterSelectedIdx=1, got %d", m2.filterSelectedIdx)
	}

	// Navigate down again
	m3, _ := pressKey(m2, 'j')
	if m3.filterSelectedIdx != 2 {
		t.Errorf("j key: expected filterSelectedIdx=2, got %d", m3.filterSelectedIdx)
	}

	// Navigate down at boundary - should stay at 2
	m4, _ := pressKey(m3, 'j')
	if m4.filterSelectedIdx != 2 {
		t.Errorf("j key at boundary: expected filterSelectedIdx=2, got %d", m4.filterSelectedIdx)
	}

	// Navigate up
	m5, _ := pressKey(m4, 'k')
	if m5.filterSelectedIdx != 1 {
		t.Errorf("k key: expected filterSelectedIdx=1, got %d", m5.filterSelectedIdx)
	}
}

func TestTUIFilterSelectRepo(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a")),
		makeJob(2, withRepoName("repo-b")),
		makeJob(3, withRepoName("repo-a")),
	}
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 2},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 1},
	})
	// Flat list: All(0), repo-a(1), repo-b(2)
	m.filterSelectedIdx = 1 // repo-a

	// Press enter to select
	m2, _ := pressSpecial(m, tea.KeyEnter)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if len(m2.activeRepoFilter) != 1 || m2.activeRepoFilter[0] != "/path/to/repo-a" {
		t.Errorf("Expected activeRepoFilter=['/path/to/repo-a'], got %v", m2.activeRepoFilter)
	}
	// Selection is invalidated until refetch completes (prevents race condition)
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterSelectAll(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.activeBranchFilter = "main"
	m.filterStack = []string{"repo", "branch"}

	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 2},
	})
	m.filterSelectedIdx = 0 // "All"

	m2, _ := pressSpecial(m, tea.KeyEnter)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if m2.activeBranchFilter != "" {
		t.Errorf("Expected activeBranchFilter to be cleared, got '%s'", m2.activeBranchFilter)
	}
	if len(m2.filterStack) != 0 {
		t.Errorf("Expected filterStack to be cleared, got %v", m2.filterStack)
	}
}

func TestTUIFilterClearWithEsc(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"} // Push to stack so escape can pop it

	// Press Esc to clear filter
	m2, _ := pressSpecial(m, tea.KeyEscape)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	// Selection is invalidated until refetch completes (prevents race condition)
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterClearWithEscLayered(t *testing.T) {
	// Test that escape clears filters one layer at a time:
	// 1. First escape clears project filter (keeps hide-addressed)
	// 2. Second escape clears hide-addressed
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"} // Push to stack so escape can pop it
	m.hideAddressed = true

	// First Esc: clear project filter, keep hide-addressed
	m2, _ := pressSpecial(m, tea.KeyEscape)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if !m2.hideAddressed {
		t.Error("Expected hideAddressed to remain true after first escape")
	}

	// Second Esc: clear hide-addressed
	m3, _ := pressSpecial(m2, tea.KeyEscape)

	if m3.hideAddressed {
		t.Error("Expected hideAddressed to be false after second escape")
	}
}

func TestTUIFilterClearHideAddressedOnly(t *testing.T) {
	// Test that escape clears hide-addressed when no project filter is active
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	// No project filter active

	// Esc should clear hide-addressed
	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.hideAddressed {
		t.Error("Expected hideAddressed to be false after escape")
	}
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterEscapeWhileLoadingFiresNewFetch(t *testing.T) {
	// Test that escape while loading fires a new fetch immediately and
	// increments fetchSeq so the stale response is discarded
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"} // Push to stack so escape can pop it
	m.loadingJobs = true             // Already loading
	oldSeq := m.fetchSeq

	// Press Esc while loading - should fire new fetch and bump seq
	m2, cmd := pressSpecial(m, tea.KeyEscape)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if m2.fetchSeq <= oldSeq {
		t.Error("Expected fetchSeq to be incremented")
	}
	if cmd == nil {
		t.Error("Expected a fetch command when escape pressed (fetchSeq ensures stale discard)")
	}

	// Simulate stale jobs fetch response arriving with old seq - should be discarded
	m3, _ := updateModel(t, m2, tuiJobsMsg{jobs: []storage.ReviewJob{makeJob(2)}, hasMore: false, seq: oldSeq})

	// loadingJobs should still be true because stale response was discarded
	if !m3.loadingJobs {
		t.Error("Expected loadingJobs to still be true (stale response discarded)")
	}
}

func TestTUIFilterEscapeWhilePaginationDiscardsAppend(t *testing.T) {
	// Test that escape while pagination is in flight fires a new fetch and
	// discards stale append response via fetchSeq
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"} // Push to stack so escape can pop it
	m.loadingMore = true             // Pagination in flight
	m.loadingJobs = false            // Not a full refresh
	oldSeq := m.fetchSeq

	// Press Esc while pagination loading - should fire new fetch and bump seq
	m2, cmd := pressSpecial(m, tea.KeyEscape)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if m2.fetchSeq <= oldSeq {
		t.Error("Expected fetchSeq to be incremented")
	}
	if cmd == nil {
		t.Error("Expected a fetch command when escape pressed")
	}

	// Simulate stale pagination response arriving with old seq - should be discarded
	m3, _ := updateModel(t, m2, tuiJobsMsg{
		jobs:    []storage.ReviewJob{makeJob(99, withRepoName("stale"))},
		hasMore: true,
		append:  true, // This is a pagination append
		seq:     oldSeq,
	})

	// Stale data should NOT have been appended
	for _, job := range m3.jobs {
		if job.ID == 99 {
			t.Error("Stale pagination data should have been discarded, not appended")
		}
	}
}

func TestTUIFilterEscapeCloses(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterSearch = "test"
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", count: 1},
	})

	// Press 'esc' to close without selecting
	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if m2.filterSearch != "" {
		t.Errorf("Expected filterSearch to be cleared, got '%s'", m2.filterSearch)
	}
}

func TestTUIFilterTypingSearch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", count: 5},
	})
	m.filterSelectedIdx = 1

	// Type 'a'
	m2, _ := pressKey(m, 'a')

	if m2.filterSearch != "a" {
		t.Errorf("Expected filterSearch='a', got '%s'", m2.filterSearch)
	}
	if m2.filterSelectedIdx != 0 {
		t.Errorf("Expected filterSelectedIdx reset to 0, got %d", m2.filterSelectedIdx)
	}

	// Type 'b'
	m3, _ := pressKey(m2, 'b')

	if m3.filterSearch != "ab" {
		t.Errorf("Expected filterSearch='ab', got '%s'", m3.filterSearch)
	}

	// Backspace
	m4, _ := pressSpecial(m3, tea.KeyBackspace)

	if m4.filterSearch != "a" {
		t.Errorf("Expected filterSearch='a' after backspace, got '%s'", m4.filterSearch)
	}
}

func TestTUIFilterPreselectsCurrent(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.activeRepoFilter = []string{"/path/to/repo-b"} // Already filtering to repo-b

	// Simulate receiving repos from API (should pre-select repo-b)
	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 1},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 2}

	m2, _ := updateModel(t, m, msg)

	// Flat list: All(0), repo-a(1), repo-b(2)
	// repo-b should be at index 2, which should be pre-selected
	if m2.filterSelectedIdx != 2 {
		t.Errorf("Expected filterSelectedIdx=2 (repo-b), got %d", m2.filterSelectedIdx)
	}
}

func TestTUIFilterToZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Jobs only in repo-a
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 2},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 0}, // No jobs
	})
	// Flat list: All(0), repo-a(1), repo-b(2)
	m.filterSelectedIdx = 2 // Select repo-b

	// Press enter to select repo-b (triggers refetch)
	m2, cmd := pressSpecial(m, tea.KeyEnter)

	// Filter should be applied and a fetchJobs command should be returned
	if len(m2.activeRepoFilter) != 1 || m2.activeRepoFilter[0] != "/path/to/repo-b" {
		t.Errorf("Expected activeRepoFilter=['/path/to/repo-b'], got %v", m2.activeRepoFilter)
	}
	if cmd == nil {
		t.Error("Expected fetchJobs command to be returned")
	}
	// Selection is invalidated until refetch completes (prevents race condition)
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 pending refetch, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 pending refetch, got %d", m2.selectedJobID)
	}

	// Simulate receiving empty jobs from API (repo-b has no jobs)
	m3, _ := updateModel(t, m2, tuiJobsMsg{jobs: []storage.ReviewJob{}})

	// Now selection should be cleared since no jobs
	if m3.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 after receiving empty jobs, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 after receiving empty jobs, got %d", m3.selectedJobID)
	}
}

func TestTUIFilterAggregatedDisplayName(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Jobs from two repos that share a display name
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("backend-dev"), withRepoPath("/path/to/backend-dev")),
		makeJob(2, withRepoName("backend-prod"), withRepoPath("/path/to/backend-prod")),
		makeJob(3, withRepoName("frontend"), withRepoPath("/path/to/frontend"), withStatus(storage.JobStatusFailed)),
	}
	m.currentView = tuiViewFilter
	// Aggregated group: "backend" covers both backend-dev and backend-prod
	setupFilterTree(&m, []treeFilterNode{
		{name: "backend", rootPaths: []string{"/path/to/backend-dev", "/path/to/backend-prod"}, count: 2},
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
	})
	// Flat list: All(0), backend(1), frontend(2)
	m.filterSelectedIdx = 1 // Select "backend" group

	// Press enter to select
	m2, _ := pressSpecial(m, tea.KeyEnter)

	// Should have both paths in the filter
	if len(m2.activeRepoFilter) != 2 {
		t.Errorf("Expected 2 paths in activeRepoFilter, got %d", len(m2.activeRepoFilter))
	}

	// Both backend repos should be visible
	if !m2.repoMatchesFilter("/path/to/backend-dev") {
		t.Error("Expected backend-dev to match filter")
	}
	if !m2.repoMatchesFilter("/path/to/backend-prod") {
		t.Error("Expected backend-prod to match filter")
	}
	// Frontend should not be visible
	if m2.repoMatchesFilter("/path/to/frontend") {
		t.Error("Expected frontend to NOT match filter")
	}
}

func TestTUIFilterSearchByRepoPath(t *testing.T) {
	m := newTuiModel("http://localhost")
	setupFilterTree(&m, []treeFilterNode{
		{name: "backend", rootPaths: []string{"/path/to/backend-dev", "/path/to/backend-prod"}, count: 2},
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
	})

	// Search by underlying repo path basename (not display name)
	m.filterSearch = "backend-dev"
	m.rebuildFilterFlatList()

	// Should find the "backend" group (contains backend-dev path)
	if len(m.filterFlatList) != 1 { // "backend" only (All doesn't match "backend-dev")
		t.Errorf("Expected 1 visible (backend), got %d", len(m.filterFlatList))
	}
	if len(m.filterFlatList) > 0 && m.filterFlatList[0].repoIdx != 0 {
		t.Errorf("Expected to find 'backend' group at repoIdx 0")
	}
}

func TestTUIFilterSearchByDisplayName(t *testing.T) {
	m := newTuiModel("http://localhost")
	setupFilterTree(&m, []treeFilterNode{
		// Display name "My Project" differs from path basename "my-project-repo"
		{name: "My Project", rootPaths: []string{"/home/user/my-project-repo"}, count: 2},
		// Display name matches path basename
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
		// Display name "Backend Services" differs from path basenames
		{name: "Backend Services", rootPaths: []string{"/srv/api-server", "/srv/worker-daemon"}, count: 2},
	})

	// Search by display name (should match "My Project")
	m.filterSearch = "my project"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 { // "My Project"
		t.Errorf("Search 'my project': expected 1 visible, got %d", len(m.filterFlatList))
	}

	// Search by raw repo path basename (should still match "My Project")
	m.filterSearch = "my-project-repo"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 { // "My Project"
		t.Errorf("Search 'my-project-repo': expected 1 visible, got %d", len(m.filterFlatList))
	}

	// Search by partial display name (should match "Backend Services")
	m.filterSearch = "backend"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 { // "Backend Services"
		t.Errorf("Search 'backend': expected 1 visible, got %d", len(m.filterFlatList))
	}

	// Search by path basename of grouped repo (should match "Backend Services")
	m.filterSearch = "api-server"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 { // "Backend Services"
		t.Errorf("Search 'api-server': expected 1 visible, got %d", len(m.filterFlatList))
	}
}

func TestTUIMultiPathFilterStatusCounts(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.height = 20
	m.daemonVersion = "test"

	addrTrue := true
	addrFalse := false

	// Jobs from multiple repos
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoPath: "/path/to/backend-dev", Status: storage.JobStatusDone, Addressed: &addrTrue},
		{ID: 2, RepoPath: "/path/to/backend-prod", Status: storage.JobStatusDone, Addressed: &addrFalse},
		{ID: 3, RepoPath: "/path/to/backend-prod", Status: storage.JobStatusDone, Addressed: &addrFalse},
		{ID: 4, RepoPath: "/path/to/frontend", Status: storage.JobStatusDone, Addressed: &addrTrue},
		{ID: 5, RepoPath: "/path/to/frontend", Status: storage.JobStatusDone, Addressed: &addrTrue},
	}

	// Multi-path filter (backend group)
	m.activeRepoFilter = []string{"/path/to/backend-dev", "/path/to/backend-prod"}

	output := m.renderQueueView()

	// Status line should show counts only for backend repos (3 done, 1 addressed, 2 unaddressed)
	// Not frontend (2 done, 2 addressed)
	if !strings.Contains(output, "Done: 3") {
		t.Errorf("Expected status to show 'Done: 3' for filtered repos, got: %s", output)
	}
	if !strings.Contains(output, "Addressed: 1") {
		t.Errorf("Expected status to show 'Addressed: 1' for filtered repos, got: %s", output)
	}
	if !strings.Contains(output, "Unaddressed: 2") {
		t.Errorf("Expected status to show 'Unaddressed: 2' for filtered repos, got: %s", output)
	}
}

func TestTUIFilterViewSmallTerminal(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", count: 5},
		{name: "repo-b", count: 3},
		{name: "repo-c", count: 2},
	})
	// Flat list: All + 3 repos = 4 entries
	m.filterSelectedIdx = 0

	t.Run("tiny terminal shows message", func(t *testing.T) {
		m.height = 5 // Less than reservedLines (7)
		output := m.renderFilterView()

		if !strings.Contains(output, "(terminal too small)") {
			t.Errorf("Expected 'terminal too small' message for height=5, got: %s", output)
		}
		// Should not contain any repo names
		if strings.Contains(output, "repo-a") {
			t.Error("Should not render repo names when terminal too small")
		}
	})

	t.Run("exactly reservedLines shows no items", func(t *testing.T) {
		m.height = 7 // Exactly reservedLines, visibleRows = 0
		output := m.renderFilterView()

		if !strings.Contains(output, "(terminal too small)") {
			t.Errorf("Expected 'terminal too small' message for height=7, got: %s", output)
		}
	})

	t.Run("one row available", func(t *testing.T) {
		m.height = 8 // reservedLines + 1 = visibleRows of 1
		output := m.renderFilterView()

		if strings.Contains(output, "(terminal too small)") {
			t.Error("Should not show 'terminal too small' when 1 row available")
		}
		// Should show exactly one item (All)
		if !strings.Contains(output, "All") {
			t.Error("Should show 'All' when 1 row available")
		}
		// Should show scroll info since 4 entries > 1 visible row
		if !strings.Contains(output, "[showing 1-1 of 4]") {
			t.Errorf("Expected scroll info '[showing 1-1 of 4]', got: %s", output)
		}
	})

	t.Run("fits all items without scroll", func(t *testing.T) {
		m.height = 15 // reservedLines(7) + 8 = visibleRows of 8, enough for 4 entries
		output := m.renderFilterView()

		// Should show all items
		if !strings.Contains(output, "All") {
			t.Error("Should show 'All'")
		}
		if !strings.Contains(output, "repo-a") {
			t.Error("Should show 'repo-a'")
		}
		if !strings.Contains(output, "repo-c") {
			t.Error("Should show 'repo-c'")
		}
		// Should NOT show scroll info
		if strings.Contains(output, "[showing") {
			t.Error("Should not show scroll info when all items fit")
		}
	})

	t.Run("needs scrolling shows scroll info", func(t *testing.T) {
		m.height = 9            // visibleRows = 2
		m.filterSelectedIdx = 2 // Select repo-b
		output := m.renderFilterView()

		// Should show scroll info
		if !strings.Contains(output, "[showing") {
			t.Error("Expected scroll info when items exceed visible rows")
		}
		// Selected item (repo-b) should be visible
		if !strings.Contains(output, "repo-b") {
			t.Error("Selected repo should be visible in scroll window")
		}
	})
}

func TestTUIFilterViewScrollWindow(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-1", count: 5},
		{name: "repo-2", count: 4},
		{name: "repo-3", count: 3},
		{name: "repo-4", count: 2},
		{name: "repo-5", count: 1},
	})
	// Flat list: All + 5 repos = 6 entries
	m.height = 10 // visibleRows = 3

	t.Run("scroll keeps selected item visible at top", func(t *testing.T) {
		m.filterSelectedIdx = 0
		output := m.renderFilterView()

		if !strings.Contains(output, "[showing 1-3 of 6]") {
			t.Errorf("Expected '[showing 1-3 of 6]' for top selection, got: %s", output)
		}
	})

	t.Run("scroll keeps selected item visible at bottom", func(t *testing.T) {
		m.filterSelectedIdx = 5 // repo-5
		output := m.renderFilterView()

		if !strings.Contains(output, "[showing 4-6 of 6]") {
			t.Errorf("Expected '[showing 4-6 of 6]' for bottom selection, got: %s", output)
		}
		if !strings.Contains(output, "repo-5") {
			t.Error("repo-5 should be visible when selected")
		}
	})

	t.Run("scroll centers selected item in middle", func(t *testing.T) {
		m.filterSelectedIdx = 3 // repo-3
		output := m.renderFilterView()

		// With 3 visible rows and selecting item 3 (0-indexed), centering puts start at 2
		if !strings.Contains(output, "repo-3") {
			t.Error("repo-3 should be visible when selected")
		}
	})
}

// Tests for j/k and left/right review navigation

func TestTUIFilterLoadingRendersPaddedHeight(t *testing.T) {
	// Test that filter loading state pads output to fill terminal height
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 20
	m.currentView = tuiViewFilter
	m.filterTree = nil // Loading state (tree not built yet)

	output := m.View()

	lines := strings.Split(output, "\n")
	// Filter loading should fill most of the terminal height
	if len(lines) < m.height-3 {
		t.Errorf("Filter loading should pad to near terminal height, got %d lines for height %d", len(lines), m.height)
	}

	// Should contain loading message
	if !strings.Contains(output, "Loading repos...") {
		t.Error("Expected 'Loading repos...' message in output")
	}
}

func TestTUIFilterBackspaceMultiByte(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", count: 10},
	})

	// Type an emoji (multi-byte character)
	m, _ = pressKey(m, 'a')
	m, _ = pressKeys(m, []rune("\xf0\x9f\x98\x8a"))
	m, _ = pressKey(m, 'b')

	if m.filterSearch != "a\xf0\x9f\x98\x8ab" {
		t.Errorf("Expected filterSearch='a\\xf0\\x9f\\x98\\x8ab', got %q", m.filterSearch)
	}

	// Backspace should remove 'b'
	m, _ = pressSpecial(m, tea.KeyBackspace)
	if m.filterSearch != "a\xf0\x9f\x98\x8a" {
		t.Errorf("Expected filterSearch='a\\xf0\\x9f\\x98\\x8a' after first backspace, got %q", m.filterSearch)
	}

	// Backspace should remove the entire emoji, not corrupt it
	m, _ = pressSpecial(m, tea.KeyBackspace)
	if m.filterSearch != "a" {
		t.Errorf("Expected filterSearch='a' after second backspace, got %q", m.filterSearch)
	}
}

func TestTUIBranchFilterApplied(t *testing.T) {
	// Test that branch filter correctly filters jobs
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withBranch("main")),
		makeJob(2, withRepoName("repo-a"), withBranch("feature")),
		makeJob(3, withRepoName("repo-b"), withBranch("main")),
		{ID: 4, RepoName: "repo-b", Branch: ""}, // No branch
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Apply branch filter
	m.activeBranchFilter = "main"

	visible := m.getVisibleJobs()
	if len(visible) != 2 {
		t.Errorf("Expected 2 visible jobs with branch=main, got %d", len(visible))
	}
	for _, job := range visible {
		if job.Branch != "main" {
			t.Errorf("Expected all visible jobs to have branch=main, got %s", job.Branch)
		}
	}
}

func TestTUIBranchFilterNone(t *testing.T) {
	// Test filtering for jobs with no branch
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withBranch("main")),
		{ID: 2, RepoName: "repo-a", Branch: ""},
		{ID: 3, RepoName: "repo-b", Branch: ""},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Apply branch filter for "(none)"
	m.activeBranchFilter = "(none)"

	visible := m.getVisibleJobs()
	if len(visible) != 2 {
		t.Errorf("Expected 2 visible jobs with no branch, got %d", len(visible))
	}
	for _, job := range visible {
		if job.Branch != "" {
			t.Errorf("Expected all visible jobs to have empty branch, got %s", job.Branch)
		}
	}
}

func TestTUIBranchFilterCombinedWithRepoFilter(t *testing.T) {
	// Test that branch and repo filters work together
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a"), withBranch("main")),
		makeJob(2, withRepoName("repo-a"), withRepoPath("/path/to/repo-a"), withBranch("feature")),
		makeJob(3, withRepoName("repo-b"), withRepoPath("/path/to/repo-b"), withBranch("main")),
		makeJob(4, withRepoName("repo-b"), withRepoPath("/path/to/repo-b"), withBranch("feature")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Apply both filters
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.activeBranchFilter = "main"

	visible := m.getVisibleJobs()
	if len(visible) != 1 {
		t.Errorf("Expected 1 visible job (repo-a + main), got %d", len(visible))
	}
	if len(visible) > 0 && (visible[0].RepoPath != "/path/to/repo-a" || visible[0].Branch != "main") {
		t.Errorf("Expected repo-a with main branch, got %s with %s", visible[0].RepoPath, visible[0].Branch)
	}
}

// Filter stack tests

func TestTUIFilterStackPush(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Push repo filter
	m.pushFilter("repo")
	if len(m.filterStack) != 1 || m.filterStack[0] != "repo" {
		t.Errorf("Expected filterStack=['repo'], got %v", m.filterStack)
	}

	// Push branch filter
	m.pushFilter("branch")
	if len(m.filterStack) != 2 || m.filterStack[0] != "repo" || m.filterStack[1] != "branch" {
		t.Errorf("Expected filterStack=['repo', 'branch'], got %v", m.filterStack)
	}
}

func TestTUIFilterStackPushMovesDuplicate(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Push repo then branch
	m.pushFilter("repo")
	m.pushFilter("branch")

	// Push repo again - should move to end
	m.pushFilter("repo")
	if len(m.filterStack) != 2 || m.filterStack[0] != "branch" || m.filterStack[1] != "repo" {
		t.Errorf("Expected filterStack=['branch', 'repo'] after re-pushing repo, got %v", m.filterStack)
	}
}

func TestTUIFilterStackPopClearsValue(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.activeRepoFilter = []string{"/path/to/repo"}
	m.activeBranchFilter = "main"
	m.filterStack = []string{"repo", "branch"}

	// Pop should remove branch (last)
	popped := m.popFilter()
	if popped != "branch" {
		t.Errorf("Expected popped='branch', got %s", popped)
	}
	if m.activeBranchFilter != "" {
		t.Errorf("Expected activeBranchFilter to be cleared, got %s", m.activeBranchFilter)
	}
	if len(m.activeRepoFilter) != 1 {
		t.Errorf("Expected activeRepoFilter to remain, got %v", m.activeRepoFilter)
	}

	// Pop again should remove repo
	popped = m.popFilter()
	if popped != "repo" {
		t.Errorf("Expected popped='repo', got %s", popped)
	}
	if len(m.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m.activeRepoFilter)
	}

	// Pop on empty stack
	popped = m.popFilter()
	if popped != "" {
		t.Errorf("Expected popped='' on empty stack, got %s", popped)
	}
}

func TestTUIFilterStackEscapeOrder(t *testing.T) {
	// Test that escape pops filters in stack order (LIFO)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a"), withBranch("main")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Apply repo filter first, then branch filter
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"}
	m.activeBranchFilter = "main"
	m.filterStack = append(m.filterStack, "branch")

	// First escape - should clear branch filter
	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.activeBranchFilter != "" {
		t.Errorf("Expected branch filter to be cleared first, got %s", m2.activeBranchFilter)
	}
	if len(m2.activeRepoFilter) == 0 {
		t.Error("Expected repo filter to remain after first escape")
	}
	if len(m2.filterStack) != 1 || m2.filterStack[0] != "repo" {
		t.Errorf("Expected filterStack=['repo'] after first escape, got %v", m2.filterStack)
	}

	// Second escape - should clear repo filter
	m3, _ := pressSpecial(m2, tea.KeyEscape)

	if len(m3.activeRepoFilter) != 0 {
		t.Errorf("Expected repo filter to be cleared, got %v", m3.activeRepoFilter)
	}
	if len(m3.filterStack) != 0 {
		t.Errorf("Expected filterStack to be empty, got %v", m3.filterStack)
	}
}

func TestTUIFilterStackTitleBarOrder(t *testing.T) {
	// Test that title bar shows filters in stack order
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("myrepo"), withRepoPath("/path/to/myrepo"), withBranch("feature")),
	}
	m.currentView = tuiViewQueue

	// Apply filters in order: branch first, then repo
	m.activeBranchFilter = "feature"
	m.filterStack = []string{"branch"}
	m.activeRepoFilter = []string{"/path/to/myrepo"}
	m.filterStack = append(m.filterStack, "repo")

	output := m.View()

	// Should contain both filters in the title
	if !strings.Contains(output, "[b: feature]") {
		t.Error("Expected output to contain [b: feature]")
	}
	if !strings.Contains(output, "[f: myrepo]") {
		t.Error("Expected output to contain [f: myrepo]")
	}

	// Branch should appear before repo (stack order)
	bIdx := strings.Index(output, "[b: feature]")
	fIdx := strings.Index(output, "[f: myrepo]")
	if bIdx > fIdx {
		t.Error("Expected branch filter to appear before repo filter in title (stack order)")
	}
}

func TestTUIFilterStackReverseOrder(t *testing.T) {
	// Test title bar with reverse order (repo first, then branch)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("myrepo"), withRepoPath("/path/to/myrepo"), withBranch("develop")),
	}
	m.currentView = tuiViewQueue

	// Apply filters in order: repo first, then branch
	m.activeRepoFilter = []string{"/path/to/myrepo"}
	m.filterStack = []string{"repo"}
	m.activeBranchFilter = "develop"
	m.filterStack = append(m.filterStack, "branch")

	output := m.View()

	// Repo should appear before branch (stack order)
	fIdx := strings.Index(output, "[f: myrepo]")
	bIdx := strings.Index(output, "[b: develop]")
	if fIdx > bIdx {
		t.Error("Expected repo filter to appear before branch filter in title (stack order)")
	}
}

func TestTUITreeFilterExpandCollapse(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			children: []branchFilterItem{
				{name: "main", count: 3},
				{name: "feature", count: 2},
			},
		},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 3},
	})
	// Flat list: All(0), repo-a(1), repo-b(2) -- both collapsed
	if len(m.filterFlatList) != 3 {
		t.Fatalf("Expected 3 entries initially, got %d", len(m.filterFlatList))
	}

	// Select repo-a and expand with right arrow
	m.filterSelectedIdx = 1
	m2, _ := pressKey(m, 'l') // right/l

	// repo-a should now be expanded
	if !m2.filterTree[0].expanded {
		t.Error("Expected repo-a to be expanded after right arrow")
	}
	// Flat list: All(0), repo-a(1), main(2), feature(3), repo-b(4)
	if len(m2.filterFlatList) != 5 {
		t.Errorf("Expected 5 entries after expand, got %d", len(m2.filterFlatList))
	}

	// Navigate to "main" branch and collapse parent with left arrow
	m2.filterSelectedIdx = 2   // main
	m3, _ := pressKey(m2, 'h') // left/h

	if m3.filterTree[0].expanded {
		t.Error("Expected repo-a to be collapsed after left arrow on branch")
	}
	// Selection should move to parent repo
	if len(m3.filterFlatList) != 3 {
		t.Errorf("Expected 3 entries after collapse, got %d", len(m3.filterFlatList))
	}
	// Selected should be repo-a (index 1 in flat list)
	if m3.filterSelectedIdx != 1 {
		t.Errorf("Expected selection to move to parent (idx 1), got %d", m3.filterSelectedIdx)
	}
}

func TestTUITreeFilterSelectBranch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			expanded:  true,
			children: []branchFilterItem{
				{name: "main", count: 3},
				{name: "feature", count: 2},
			},
		},
	})
	// Flat list: All(0), repo-a(1), main(2), feature(3)
	m.filterSelectedIdx = 3 // Select "feature" branch
	m.jobs = []storage.ReviewJob{
		makeJob(1, withBranch("main")),
		makeJob(2, withBranch("feature")),
	}

	m2, cmd := pressSpecial(m, tea.KeyEnter)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if len(m2.activeRepoFilter) != 1 || m2.activeRepoFilter[0] != "/path/to/repo-a" {
		t.Errorf("Expected activeRepoFilter=['/path/to/repo-a'], got %v", m2.activeRepoFilter)
	}
	if m2.activeBranchFilter != "feature" {
		t.Errorf("Expected activeBranchFilter='feature', got '%s'", m2.activeBranchFilter)
	}
	// Both repo and branch should be in filter stack
	foundRepo := false
	foundBranch := false
	for _, f := range m2.filterStack {
		if f == "repo" {
			foundRepo = true
		}
		if f == "branch" {
			foundBranch = true
		}
	}
	if !foundRepo {
		t.Errorf("Expected 'repo' to be in filterStack, got %v", m2.filterStack)
	}
	if !foundBranch {
		t.Errorf("Expected 'branch' to be in filterStack, got %v", m2.filterStack)
	}
	if cmd == nil {
		t.Error("Expected fetchJobs command to be returned")
	}
}

func TestTUITreeFilterLazyLoadBranches(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 5},
	})
	// No children loaded yet
	m.filterSelectedIdx = 1 // repo-a

	// Press right to expand -- should trigger lazy load
	m2, cmd := pressKey(m, 'l')

	if !m2.filterTree[0].loading {
		t.Error("Expected loading=true after right arrow on repo with no children")
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command")
	}

	// Simulate receiving branches
	m3, _ := updateModel(t, m2, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/to/repo-a"},
		branches:  []branchFilterItem{{name: "main", count: 3}, {name: "dev", count: 2}},
	})

	if m3.filterTree[0].loading {
		t.Error("Expected loading=false after receiving branches")
	}
	if !m3.filterTree[0].expanded {
		t.Error("Expected expanded=true after receiving branches")
	}
	if len(m3.filterTree[0].children) != 2 {
		t.Errorf("Expected 2 children, got %d", len(m3.filterTree[0].children))
	}
	// Flat list: All(0), repo-a(1), main(2), dev(3)
	if len(m3.filterFlatList) != 4 {
		t.Errorf("Expected 4 flat entries after branch load, got %d", len(m3.filterFlatList))
	}
}

func TestTUITreeFilterSearchExpandsMatchingBranches(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			children: []branchFilterItem{
				{name: "main", count: 3},
				{name: "feature-xyz", count: 2},
			},
		},
		{
			name:      "repo-b",
			rootPaths: []string{"/path/to/repo-b"},
			count:     3,
			children: []branchFilterItem{
				{name: "main", count: 3},
			},
		},
	})

	// Search for "xyz" -- should auto-expand repo-a to show feature-xyz
	m.filterSearch = "xyz"
	m.rebuildFilterFlatList()

	// Should show: repo-a (parent of matching branch), feature-xyz
	if len(m.filterFlatList) != 2 {
		t.Errorf("Expected 2 entries (repo-a + feature-xyz), got %d", len(m.filterFlatList))
	}
	if len(m.filterFlatList) >= 2 {
		if m.filterFlatList[0].repoIdx != 0 || m.filterFlatList[0].branchIdx != -1 {
			t.Error("Expected first entry to be repo-a")
		}
		if m.filterFlatList[1].repoIdx != 0 || m.filterFlatList[1].branchIdx != 1 {
			t.Error("Expected second entry to be feature-xyz (branchIdx=1)")
		}
	}
}

func TestTUIRemoveFilterFromStack(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.filterStack = []string{"repo", "branch", "other"}

	m.removeFilterFromStack("branch")
	if len(m.filterStack) != 2 || m.filterStack[0] != "repo" || m.filterStack[1] != "other" {
		t.Errorf("Expected filterStack=['repo', 'other'], got %v", m.filterStack)
	}

	// Remove non-existent filter should be no-op
	m.removeFilterFromStack("nonexistent")
	if len(m.filterStack) != 2 {
		t.Errorf("Expected filterStack length to remain 2, got %d", len(m.filterStack))
	}
}

func TestTUINavigateDownNoLoadMoreWhenBranchFiltered(t *testing.T) {
	// Test that pagination is disabled when branch filter is active
	// (since branch filtering fetches all jobs upfront)
	m := newTuiModel("http://localhost")

	// Set up at last job with branch filter active
	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeBranchFilter = "feature" // Branch filter active
	m.currentView = tuiViewQueue

	// Press down at bottom - should NOT trigger load more (filtered view loads all)
	m2, cmd := pressSpecial(m, tea.KeyDown)

	if m2.loadingMore {
		t.Error("loadingMore should not be set when branch filter is active")
	}
	if cmd != nil {
		t.Error("Should not return command when branch filter is active")
	}
}

func TestTUINavigateJKeyNoLoadMoreWhenBranchFiltered(t *testing.T) {
	// Test that j/left key pagination is disabled when branch filter is active
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeBranchFilter = "feature"
	m.currentView = tuiViewQueue

	// Press j at bottom - should NOT trigger load more
	m2, cmd := pressKey(m, 'j')

	if m2.loadingMore {
		t.Error("loadingMore should not be set when branch filter is active (j key)")
	}
	if cmd != nil {
		t.Error("Should not return command when branch filter is active (j key)")
	}
}

func TestTUIPageDownNoLoadMoreWhenBranchFiltered(t *testing.T) {
	// Test that pgdown pagination is disabled when branch filter is active
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeBranchFilter = "feature"
	m.currentView = tuiViewQueue
	m.height = 20 // Ensure page size calc works

	// Press pgdown at bottom - should NOT trigger load more
	m2, cmd := pressSpecial(m, tea.KeyPgDown)

	if m2.loadingMore {
		t.Error("loadingMore should not be set when branch filter is active (pgdown)")
	}
	if cmd != nil {
		t.Error("Should not return command when branch filter is active (pgdown)")
	}
}

func TestTUIWindowResizeNoLoadMoreWhenMultiRepoFiltered(t *testing.T) {
	// Test that window resize doesn't trigger pagination when multi-repo filter is active.
	// Single-repo and branch filters support server-side pagination,
	// but multi-repo filters are client-side only and disable pagination.
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{makeJob(1, withRepoPath("/repo1"))}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false
	m.activeRepoFilter = []string{"/repo1", "/repo2"}
	m.currentView = tuiViewQueue
	m.height = 10

	// Resize to larger window - should NOT trigger load more
	m2, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})

	if m2.loadingMore {
		t.Error("loadingMore should not be set when multi-repo filter is active (window resize)")
	}
	if m2.loadingJobs {
		t.Error("loadingJobs should not be set when multi-repo filter is active (window resize)")
	}
	// Window resize returns nil command when not triggering fetch
	_ = cmd
}

func TestTUITreeFilterSelectBranchTriggersRefetch(t *testing.T) {
	// Test that selecting a branch in the tree filter triggers a refetch
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     8,
			expanded:  true,
			children: []branchFilterItem{
				{name: "main", count: 5},
				{name: "feature", count: 3},
			},
		},
	})
	// Flat list: All(0), repo-a(1), main(2), feature(3)
	m.filterSelectedIdx = 3 // Select "feature"
	m.jobs = []storage.ReviewJob{
		makeJob(1, withBranch("main")),
		makeJob(2, withBranch("feature")),
	}
	m.loadingJobs = false

	// Press Enter to select
	m2, cmd := pressSpecial(m, tea.KeyEnter)

	if !m2.loadingJobs {
		t.Error("loadingJobs should be true after selecting branch filter")
	}
	if cmd == nil {
		t.Error("Should return fetchJobs command when selecting branch filter")
	}
}

func TestTUIBranchFilterClearTriggersRefetch(t *testing.T) {
	// Test that clearing a branch filter triggers a refetch
	m := newTuiModel("http://localhost")

	m.currentView = tuiViewQueue
	m.activeBranchFilter = "feature"
	m.filterStack = []string{"branch"}
	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.loadingJobs = false

	// Press Escape to clear filter
	m2, cmd := pressSpecial(m, tea.KeyEscape)

	if m2.activeBranchFilter != "" {
		t.Errorf("Expected activeBranchFilter to be cleared, got '%s'", m2.activeBranchFilter)
	}
	if !m2.loadingJobs {
		t.Error("loadingJobs should be true after clearing branch filter")
	}
	if cmd == nil {
		t.Error("Should return fetchJobs command when clearing branch filter")
	}
}

// Backfill gating tests

func TestTUIBranchBackfillDoneSetWhenNoNullsRemain(t *testing.T) {
	// Test that branchBackfillDone is set only when nullsRemaining is 0
	m := newTuiModel("http://localhost")
	m.branchBackfillDone = false

	// Receive message with no NULLs remaining
	m2, _ := updateModel(t, m, tuiBranchesMsg{
		branches:       []branchFilterItem{{name: "main", count: 5}},
		totalCount:     5,
		backfillCount:  0,
		nullsRemaining: 0,
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to be true when nullsRemaining is 0")
	}
}

func TestTUIBranchBackfillDoneSetEvenWhenNullsRemain(t *testing.T) {
	// Test that branchBackfillDone IS set even when nullsRemaining > 0
	// Backfill is a one-time migration operation - new jobs have branches set at enqueue time
	m := newTuiModel("http://localhost")
	m.branchBackfillDone = false

	// Receive message with some NULLs remaining
	m2, _ := updateModel(t, m, tuiBranchesMsg{
		branches:       []branchFilterItem{{name: "main", count: 5}, {name: "(none)", count: 3}},
		totalCount:     8,
		backfillCount:  2,
		nullsRemaining: 3, // Some legacy jobs still have NULL branches
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to be true after first fetch (one-time operation)")
	}
}

func TestTUIBranchBackfillIsOneTimeOperation(t *testing.T) {
	// Test that backfill is a one-time operation - branchBackfillDone stays true once set
	m := newTuiModel("http://localhost")
	m.branchBackfillDone = false

	// First fetch: some NULLs remain, backfillDone should be set anyway (one-time operation)
	m2, _ := updateModel(t, m, tuiBranchesMsg{
		branches:       []branchFilterItem{{name: "main", count: 5}, {name: "(none)", count: 2}},
		totalCount:     7,
		backfillCount:  1,
		nullsRemaining: 2,
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to be true after first fetch")
	}

	// Second fetch: branchBackfillDone stays true
	m3, _ := updateModel(t, m2, tuiBranchesMsg{
		branches:       []branchFilterItem{{name: "main", count: 7}},
		totalCount:     7,
		backfillCount:  0,
		nullsRemaining: 0,
	})

	if !m3.branchBackfillDone {
		t.Error("Expected branchBackfillDone to remain true after subsequent fetches")
	}
}

func TestTUIBranchBackfillDoneStaysTrueAfterNewJobs(t *testing.T) {
	// Test that branchBackfillDone stays true even if NULLs appear in stats
	// Backfill is a one-time migration - new jobs should have branches set at enqueue time
	// Any "(none)" count represents legacy jobs that weren't backfilled, not new work to do
	m := newTuiModel("http://localhost")
	m.branchBackfillDone = true // Previously marked as done

	// Receive message with NULLs (legacy jobs that weren't backfilled)
	m2, _ := updateModel(t, m, tuiBranchesMsg{
		branches:       []branchFilterItem{{name: "main", count: 5}, {name: "(none)", count: 2}},
		totalCount:     7,
		backfillCount:  0,
		nullsRemaining: 2,
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to remain true (one-time operation)")
	}
}

func TestTUIQueueNavigationWithFilter(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Jobs from two repos, interleaved
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(4, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(5, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"} // Filter to only repo-a jobs

	// Navigate down - should skip repo-b jobs
	m2, _ := pressKey(m, 'j')

	// Should jump from ID=1 (idx 0) to ID=3 (idx 2), skipping ID=2 (repo-b)
	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	// Navigate down again - should go to ID=5
	m3, _ := pressKey(m2, 'j')

	if m3.selectedIdx != 4 {
		t.Errorf("Expected selectedIdx=4, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 5 {
		t.Errorf("Expected selectedJobID=5, got %d", m3.selectedJobID)
	}

	// Navigate up - should go back to ID=3
	m4, _ := pressKey(m3, 'k')

	if m4.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m4.selectedIdx)
	}
}

func TestTUIJobsRefreshWithFilter(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with filter active
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 2
	m.selectedJobID = 3
	m.activeRepoFilter = []string{"/path/to/repo-a"}

	// Jobs refresh - same jobs
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}}

	m2, _ := updateModel(t, m, newJobs)

	// Selection should be maintained
	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	// Now the selected job is removed
	newJobs = tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
	}}

	m3, _ := updateModel(t, m2, newJobs)

	// Should select first visible job (ID=1, repo-a)
	if m3.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx=0, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 1 {
		t.Errorf("Expected selectedJobID=1, got %d", m3.selectedJobID)
	}
}

func TestTUIRefreshWithZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Start with jobs in repo-a, filter active for repo-b
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.activeRepoFilter = []string{"/path/to/repo-b"} // Filter to repo with no jobs
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Simulate jobs refresh
	newJobs := []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m2, _ := updateModel(t, m, tuiJobsMsg{jobs: newJobs})

	// Selection should be cleared since no jobs match filter
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 for zero visible jobs after refresh, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 for zero visible jobs after refresh, got %d", m2.selectedJobID)
	}
}

func TestTUIActionsNoOpWithZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Setup: filter active with no matching jobs
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.activeRepoFilter = []string{"/path/to/repo-b"}
	m.selectedIdx = -1
	m.selectedJobID = 0
	m.currentView = tuiViewQueue

	// Press enter - should be no-op
	m2, cmd := pressSpecial(m, tea.KeyEnter)
	if cmd != nil {
		t.Error("Expected no command for enter with no visible jobs")
	}
	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected to stay in queue view, got %d", m2.currentView)
	}

	// Press 'x' (cancel) - should be no-op
	_, cmd = pressKey(m, 'x')
	if cmd != nil {
		t.Error("Expected no command for cancel with no visible jobs")
	}

	// Press 'a' (address) - should be no-op
	_, cmd = pressKey(m, 'a')
	if cmd != nil {
		t.Error("Expected no command for address with no visible jobs")
	}
}

func TestTUITreeFilterCollapseOnExpandedRepo(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			expanded:  true,
			children: []branchFilterItem{
				{name: "main", count: 3},
				{name: "feature", count: 2},
			},
		},
	})
	// Flat list: All(0), repo-a(1), main(2), feature(3)
	m.filterSelectedIdx = 1 // Select repo-a

	// Press left to collapse
	m2, _ := pressKey(m, 'h')

	if m2.filterTree[0].expanded {
		t.Error("Expected repo-a to be collapsed after left arrow on expanded repo")
	}
	// Flat list should be: All(0), repo-a(1)
	if len(m2.filterFlatList) != 2 {
		t.Errorf("Expected 2 entries after collapse, got %d", len(m2.filterFlatList))
	}
}

func TestTUIBKeyOpensBranchFilter(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.jobs = []storage.ReviewJob{makeJob(1, withRepoName("repo-a"))}
	m.selectedIdx = 0

	// Press 'b' - should open filter view with filterBranchMode set
	m2, cmd := pressKey(m, 'b')

	if m2.currentView != tuiViewFilter {
		t.Errorf("Expected tuiViewFilter, got %d", m2.currentView)
	}
	if !m2.filterBranchMode {
		t.Error("Expected filterBranchMode to be true")
	}
	if cmd == nil {
		t.Error("Expected a fetch command to be returned")
	}
}

func TestTUIBKeyAutoExpandsCwdRepo(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	m.cwdRepoRoot = "/path/to/repo-b"

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
		{name: "repo-c", rootPaths: []string{"/path/to/repo-c"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 6}

	m2, cmd := updateModel(t, m, msg)

	// repo-b is sorted to index 0 (cwd repo), so it should be the target
	if !m2.filterTree[0].loading {
		t.Error("Expected cwd repo to have loading=true")
	}
	if m2.filterTree[0].name != "repo-b" {
		t.Errorf("Expected target repo to be 'repo-b', got '%s'", m2.filterTree[0].name)
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command")
	}
}

func TestTUIBKeyPositionsCursorOnBranch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 5},
	})

	msg := tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/to/repo-a"},
		branches:  []branchFilterItem{{name: "main", count: 3}, {name: "dev", count: 2}},
	}

	m2, _ := updateModel(t, m, msg)

	// filterBranchMode should be cleared
	if m2.filterBranchMode {
		t.Error("Expected filterBranchMode to be false after branches arrived")
	}
	// Cursor should be on the first branch (main)
	// Flat list: All(0), repo-a(1), main(2), dev(3)
	if m2.filterSelectedIdx != 2 {
		t.Errorf("Expected filterSelectedIdx=2 (first branch), got %d", m2.filterSelectedIdx)
	}
	if len(m2.filterFlatList) > 2 && m2.filterFlatList[2].branchIdx != 0 {
		t.Errorf("Expected entry at idx 2 to be branchIdx=0, got %d", m2.filterFlatList[2].branchIdx)
	}
}

func TestTUIBKeyFallsBackToFirstRepo(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	// No active filter, no cwd repo

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 5}

	m2, cmd := updateModel(t, m, msg)

	// Should expand the first repo (index 0)
	if !m2.filterTree[0].loading {
		t.Error("Expected first repo to have loading=true")
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command")
	}
}

func TestTUIBKeyEscapeClearsBranchMode(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", count: 1},
	})

	// Press escape to close filter
	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.filterBranchMode {
		t.Error("Expected filterBranchMode to be cleared on escape")
	}
	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
}

func TestTUIBKeyUsesActiveRepoFilter(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	m.activeRepoFilter = []string{"/path/to/repo-b"}
	m.cwdRepoRoot = "/path/to/repo-a" // cwd is different from active filter

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 5}

	m2, cmd := updateModel(t, m, msg)

	// Should use active repo filter (repo-b) over cwd (repo-a)
	// repo-a is at index 0 (cwd sorted first), repo-b at index 1
	if !m2.filterTree[1].loading {
		t.Error("Expected repo-b (active filter) to have loading=true")
	}
	if m2.filterTree[1].name != "repo-b" {
		t.Errorf("Expected target repo to be 'repo-b', got '%s'", m2.filterTree[1].name)
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command")
	}
}

func TestTUIFilterOpenBatchesBackfill(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.branchBackfillDone = false
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0

	// Press 'f' - should return batch of fetchRepos + fetchBranches
	m2, cmd := pressKey(m, 'f')

	if m2.currentView != tuiViewFilter {
		t.Errorf("Expected tuiViewFilter, got %d", m2.currentView)
	}
	if cmd == nil {
		t.Error("Expected a command to be returned")
	}
}

func TestTUIFilterOpenSkipsBackfillWhenDone(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.branchBackfillDone = true
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0

	// Press 'f' - should return only fetchRepos (no backfill needed)
	m2, cmd := pressKey(m, 'f')

	if m2.currentView != tuiViewFilter {
		t.Errorf("Expected tuiViewFilter, got %d", m2.currentView)
	}
	if cmd == nil {
		t.Error("Expected a command to be returned")
	}
}

func TestTUIFilterCwdRepoSortsFirst(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.cwdRepoRoot = "/path/to/repo-b"

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
		{name: "repo-c", rootPaths: []string{"/path/to/repo-c"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 6}

	m2, _ := updateModel(t, m, msg)

	if len(m2.filterTree) != 3 {
		t.Fatalf("Expected 3 tree nodes, got %d", len(m2.filterTree))
	}
	if m2.filterTree[0].name != "repo-b" {
		t.Errorf("Expected cwd repo 'repo-b' at index 0, got '%s'", m2.filterTree[0].name)
	}
	if m2.filterTree[1].name != "repo-a" {
		t.Errorf("Expected 'repo-a' at index 1, got '%s'", m2.filterTree[1].name)
	}
	if m2.filterTree[2].name != "repo-c" {
		t.Errorf("Expected 'repo-c' at index 2, got '%s'", m2.filterTree[2].name)
	}
}

func TestTUIFilterCwdBranchSortsFirst(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.cwdRepoRoot = "/path/to/repo-a"
	m.cwdBranch = "feature"

	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 5},
	})

	msg := tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/to/repo-a"},
		branches: []branchFilterItem{
			{name: "main", count: 3},
			{name: "develop", count: 1},
			{name: "feature", count: 1},
		},
	}

	m2, _ := updateModel(t, m, msg)

	children := m2.filterTree[0].children
	if len(children) != 3 {
		t.Fatalf("Expected 3 children, got %d", len(children))
	}
	if children[0].name != "feature" {
		t.Errorf("Expected cwd branch 'feature' at index 0, got '%s'", children[0].name)
	}
	if children[1].name != "main" {
		t.Errorf("Expected 'main' at index 1, got '%s'", children[1].name)
	}
	if children[2].name != "develop" {
		t.Errorf("Expected 'develop' at index 2, got '%s'", children[2].name)
	}
}

func TestTUIFilterNoCwdNoReorder(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	// cwdRepoRoot and cwdBranch are empty (not in a git repo)

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
		{name: "repo-c", rootPaths: []string{"/path/to/repo-c"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 6}

	m2, _ := updateModel(t, m, msg)

	// Original API order should be preserved
	if m2.filterTree[0].name != "repo-a" {
		t.Errorf("Expected 'repo-a' at index 0, got '%s'", m2.filterTree[0].name)
	}
	if m2.filterTree[1].name != "repo-b" {
		t.Errorf("Expected 'repo-b' at index 1, got '%s'", m2.filterTree[1].name)
	}
	if m2.filterTree[2].name != "repo-c" {
		t.Errorf("Expected 'repo-c' at index 2, got '%s'", m2.filterTree[2].name)
	}
}
