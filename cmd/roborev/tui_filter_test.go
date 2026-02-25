package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

// Helper function to initialize model for filter tests
func initFilterModel(nodes []treeFilterNode) tuiModel {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	if nodes != nil {
		setupFilterTree(&m, nodes)
	}
	return m
}

// Helper function to create a treeFilterNode for tests
func makeNode(name string, count int) treeFilterNode {
	return treeFilterNode{
		name:      name,
		rootPaths: []string{"/path/to/" + name},
		count:     count,
	}
}

func TestTUIFilterOpenModal(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a")),
		makeJob(2, withRepoName("repo-b")),
		makeJob(3, withRepoName("repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	m2, cmd := pressKey(m, 'f')

	if m2.currentView != tuiViewFilter {
		t.Errorf("Expected tuiViewFilter, got %d", m2.currentView)
	}

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
	m := initFilterModel(nil)

	repos := []repoFilterItem{
		{name: "repo-a", count: 2},
		{name: "repo-b", count: 1},
		{name: "repo-c", count: 1},
	}
	msg := tuiReposMsg{repos: repos}

	m2, _ := updateModel(t, m, msg)

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

	if len(m2.filterFlatList) != 4 {
		t.Errorf("Expected 4 flat list entries, got %d", len(m2.filterFlatList))
	}
}

func TestTUIFilterSelectRepo(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 2),
		makeNode("repo-b", 1),
	})

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a")),
		makeJob(2, withRepoName("repo-b")),
		makeJob(3, withRepoName("repo-a")),
	}

	m.filterSelectedIdx = 1

	m2, _ := pressSpecial(m, tea.KeyEnter)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if len(m2.activeRepoFilter) != 1 || m2.activeRepoFilter[0] != "/path/to/repo-a" {
		t.Errorf("Expected activeRepoFilter=['/path/to/repo-a'], got %v", m2.activeRepoFilter)
	}

	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterSelectAll(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 2),
	})
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.activeBranchFilter = "main"
	m.filterStack = []string{"repo", "branch"}
	m.filterSelectedIdx = 0

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

func TestTUIFilterPreselectsCurrent(t *testing.T) {
	m := initFilterModel(nil)
	m.activeRepoFilter = []string{"/path/to/repo-b"}

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 1},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos}

	m2, _ := updateModel(t, m, msg)

	if m2.filterSelectedIdx != 2 {
		t.Errorf("Expected filterSelectedIdx=2 (repo-b), got %d", m2.filterSelectedIdx)
	}
}

func TestTUIFilterPreselectsMultiPathReordered(t *testing.T) {
	m := initFilterModel(nil)

	m.activeRepoFilter = []string{"/path/b", "/path/a"}

	repos := []repoFilterItem{
		{name: "other", rootPaths: []string{"/path/c"}, count: 1},
		{name: "multi", rootPaths: []string{"/path/a", "/path/b"}, count: 2},
	}
	m2, _ := updateModel(t, m, tuiReposMsg{repos: repos})

	if m2.filterSelectedIdx != 2 {
		t.Errorf("Expected filterSelectedIdx=2 (multi), got %d",
			m2.filterSelectedIdx)
	}
}

func TestTUIFilterAggregatedDisplayName(t *testing.T) {

	m := initFilterModel([]treeFilterNode{
		{name: "backend", rootPaths: []string{"/path/to/backend-dev", "/path/to/backend-prod"}, count: 2},
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
	})

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("backend-dev"), withRepoPath("/path/to/backend-dev")),
		makeJob(2, withRepoName("backend-prod"), withRepoPath("/path/to/backend-prod")),
		makeJob(3, withRepoName("frontend"), withRepoPath("/path/to/frontend"), withStatus(storage.JobStatusFailed)),
	}

	m.filterSelectedIdx = 1

	m2, _ := pressSpecial(m, tea.KeyEnter)

	if len(m2.activeRepoFilter) != 2 {
		t.Errorf("Expected 2 paths in activeRepoFilter, got %d", len(m2.activeRepoFilter))
	}

	if !m2.repoMatchesFilter("/path/to/backend-dev") {
		t.Error("Expected backend-dev to match filter")
	}
	if !m2.repoMatchesFilter("/path/to/backend-prod") {
		t.Error("Expected backend-prod to match filter")
	}

	if m2.repoMatchesFilter("/path/to/frontend") {
		t.Error("Expected frontend to NOT match filter")
	}
}

func TestTUIFilterViewSmallTerminal(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 5),
		makeNode("repo-b", 3),
		makeNode("repo-c", 2),
	})

	m.filterSelectedIdx = 0

	t.Run("tiny terminal shows message", func(t *testing.T) {
		m.height = 5
		output := m.renderFilterView()

		if !strings.Contains(output, "(terminal too small)") {
			t.Errorf("Expected 'terminal too small' message for height=5, got: %s", output)
		}

		if strings.Contains(output, "repo-a") {
			t.Error("Should not render repo names when terminal too small")
		}
	})

	t.Run("exactly reservedLines shows no items", func(t *testing.T) {
		m.height = 7
		output := m.renderFilterView()

		if !strings.Contains(output, "(terminal too small)") {
			t.Errorf("Expected 'terminal too small' message for height=8, got: %s", output)
		}
	})

	t.Run("one row available", func(t *testing.T) {
		m.height = 8
		output := m.renderFilterView()

		if strings.Contains(output, "(terminal too small)") {
			t.Error("Should not show 'terminal too small' when 1 row available")
		}

		if !strings.Contains(output, "All") {
			t.Error("Should show 'All' when 1 row available")
		}

		if !strings.Contains(output, "[showing 1-1 of 4]") {
			t.Errorf("Expected scroll info '[showing 1-1 of 4]', got: %s", output)
		}
	})

	t.Run("fits all items without scroll", func(t *testing.T) {
		m.height = 15
		output := m.renderFilterView()

		if !strings.Contains(output, "All") {
			t.Error("Should show 'All'")
		}
		if !strings.Contains(output, "repo-a") {
			t.Error("Should show 'repo-a'")
		}
		if !strings.Contains(output, "repo-c") {
			t.Error("Should show 'repo-c'")
		}

		if strings.Contains(output, "[showing") {
			t.Error("Should not show scroll info when all items fit")
		}
	})

	t.Run("needs scrolling shows scroll info", func(t *testing.T) {
		m.height = 9
		m.filterSelectedIdx = 2
		output := m.renderFilterView()

		if !strings.Contains(output, "[showing") {
			t.Error("Expected scroll info when items exceed visible rows")
		}

		if !strings.Contains(output, "repo-b") {
			t.Error("Selected repo should be visible in scroll window")
		}
	})
}

func TestTUIFilterViewScrollWindow(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-1", 5),
		makeNode("repo-2", 4),
		makeNode("repo-3", 3),
		makeNode("repo-4", 2),
		makeNode("repo-5", 1),
	})

	m.height = 10

	t.Run("scroll keeps selected item visible at top", func(t *testing.T) {
		m.filterSelectedIdx = 0
		output := m.renderFilterView()

		if !strings.Contains(output, "[showing 1-3 of 6]") {
			t.Errorf("Expected '[showing 1-3 of 6]' for top selection, got: %s", output)
		}
	})

	t.Run("scroll keeps selected item visible at bottom", func(t *testing.T) {
		m.filterSelectedIdx = 5
		output := m.renderFilterView()

		if !strings.Contains(output, "[showing 4-6 of 6]") {
			t.Errorf("Expected '[showing 4-6 of 6]' for bottom selection, got: %s", output)
		}
		if !strings.Contains(output, "repo-5") {
			t.Error("repo-5 should be visible when selected")
		}
	})

	t.Run("scroll centers selected item in middle", func(t *testing.T) {
		m.filterSelectedIdx = 3
		output := m.renderFilterView()

		if !strings.Contains(output, "repo-3") {
			t.Error("repo-3 should be visible when selected")
		}
	})
}

func TestTUIFilterLoadingRendersPaddedHeight(t *testing.T) {

	m := initFilterModel(nil)
	m.width = 100
	m.height = 20

	output := m.View()

	lines := strings.Split(output, "\n")

	if len(lines) < m.height-3 {
		t.Errorf("Filter loading should pad to near terminal height, got %d lines for height %d", len(lines), m.height)
	}

	if !strings.Contains(output, "Loading repos...") {
		t.Error("Expected 'Loading repos...' message in output")
	}
}

func TestTUIRightArrowRetriesAfterFailedLoad(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,

			expanded: true,
			loading:  false,
		},
	})
	m.filterSelectedIdx = 1

	m2, cmd := pressSpecial(m, tea.KeyRight)

	if !m2.filterTree[0].loading {
		t.Error("Expected loading=true for retry fetch")
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command for retry")
	}
}

func TestTUIWindowResizeNoLoadMoreWhenMultiRepoFiltered(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{makeJob(1, withRepoPath("/repo1"))}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false
	m.activeRepoFilter = []string{"/repo1", "/repo2"}
	m.currentView = tuiViewQueue
	m.height = 10
	m.heightDetected = false

	m2, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})

	if m2.loadingMore {
		t.Error("loadingMore should not be set when multi-repo filter is active (window resize)")
	}
	if m2.loadingJobs {
		t.Error("loadingJobs should not be set when multi-repo filter is active (window resize)")
	}
	if m2.height != 50 {
		t.Errorf("Expected height to be 50 after resize, got %d", m2.height)
	}
	if !m2.heightDetected {
		t.Error("Expected heightDetected to be true after resize")
	}

	_ = cmd
}

func TestTUIBKeyFallsBackToFirstRepo(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.filterBranchMode = true

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
	}
	msg := tuiReposMsg{repos: repos}

	m2, cmd := updateModel(t, m, msg)

	if !m2.filterTree[0].loading {
		t.Error("Expected first repo to have loading=true")
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command")
	}
}

func TestTUIBKeyUsesActiveRepoFilter(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	m.activeRepoFilter = []string{"/path/to/repo-b"}
	m.cwdRepoRoot = "/path/to/repo-a"

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
	}
	msg := tuiReposMsg{repos: repos}

	m2, cmd := updateModel(t, m, msg)

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

func TestTUIBKeyUsesMultiPathActiveRepoFilter(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	m.activeRepoFilter = []string{"/path/a", "/path/b"}
	m.cwdRepoRoot = "/path/to/other"

	repos := []repoFilterItem{
		{name: "other", rootPaths: []string{"/path/to/other"}, count: 1},
		{name: "multi-root", rootPaths: []string{"/path/a", "/path/b"}, count: 5},
	}
	msg := tuiReposMsg{repos: repos}

	m2, cmd := updateModel(t, m, msg)

	if !m2.filterTree[1].loading {
		t.Error("Expected multi-root repo to have loading=true")
	}
	if m2.filterTree[1].name != "multi-root" {
		t.Errorf("Expected target 'multi-root', got '%s'", m2.filterTree[1].name)
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command")
	}
}

func TestTUIFilterOpenSkipsBackfillWhenDone(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.branchBackfillDone = true
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0

	m2, cmd := pressKey(m, 'f')

	if m2.currentView != tuiViewFilter {
		t.Errorf("Expected tuiViewFilter, got %d", m2.currentView)
	}
	if cmd == nil {
		t.Error("Expected a command to be returned")
	}
}
