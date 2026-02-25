package main

import (
	"errors"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

// setupFilterTree is a helper that sets filterTree and rebuilds the flat list.
func setupFilterTree(m *tuiModel, nodes []treeFilterNode) {
	m.filterTree = nodes
	m.rebuildFilterFlatList()
}

func TestTUITreeFilterExpandCollapse(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			children: []branchFilterItem{
				{name: "main", count: 3},
				{name: "feature", count: 2},
			},
		},
		makeNode("repo-b", 3),
	})

	if len(m.filterFlatList) != 3 {
		t.Fatalf("Expected 3 entries initially, got %d", len(m.filterFlatList))
	}

	m.filterSelectedIdx = 1
	m2, _ := pressSpecial(m, tea.KeyRight)

	if !m2.filterTree[0].expanded {
		t.Error("Expected repo-a to be expanded after right arrow")
	}

	if len(m2.filterFlatList) != 5 {
		t.Errorf("Expected 5 entries after expand, got %d", len(m2.filterFlatList))
	}

	m2.filterSelectedIdx = 2
	m3, _ := pressSpecial(m2, tea.KeyLeft)

	if m3.filterTree[0].expanded {
		t.Error("Expected repo-a to be collapsed after left arrow on branch")
	}

	if len(m3.filterFlatList) != 3 {
		t.Errorf("Expected 3 entries after collapse, got %d", len(m3.filterFlatList))
	}

	if m3.filterSelectedIdx != 1 {
		t.Errorf("Expected selection to move to parent (idx 1), got %d", m3.filterSelectedIdx)
	}
}

func TestTUITreeFilterSelectBranch(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
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

	m.filterSelectedIdx = 3
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
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 5),
	})

	m.filterSelectedIdx = 1

	m2, cmd := pressSpecial(m, tea.KeyRight)

	if !m2.filterTree[0].loading {
		t.Error("Expected loading=true after right arrow on repo with no children")
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command")
	}

	m3, _ := updateModel(t, m2, tuiRepoBranchesMsg{
		repoIdx:      0,
		rootPaths:    []string{"/path/to/repo-a"},
		branches:     []branchFilterItem{{name: "main", count: 3}, {name: "dev", count: 2}},
		expandOnLoad: true,
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

	if len(m3.filterFlatList) != 4 {
		t.Errorf("Expected 4 flat entries after branch load, got %d", len(m3.filterFlatList))
	}
}

func TestTUITreeFilterBranchFetchFailureClearsLoading(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			loading:   true,
		},
	})
	m.filterSelectedIdx = 1

	m2, _ := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/to/repo-a"},
		err:       errors.New("connection refused"),
	})

	if m2.filterTree[0].loading {
		t.Error("Expected loading=false after fetch failure")
	}
	if m2.filterTree[0].expanded {
		t.Error("Expected expanded=false after fetch failure")
	}
	if m2.err == nil {
		t.Error("Expected error to be set")
	}
}

func TestTUITreeFilterBranchFetchFailureOutOfView(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.filterBranchMode = true
	setupFilterTree(&m, []treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			loading:   true,
		},
	})

	m2, _ := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/to/repo-a"},
		err:       errors.New("server error"),
	})

	if m2.err == nil {
		t.Error("Expected error to be set even when not in filter view")
	}

	if m2.filterTree[0].loading {
		t.Error("Expected loading=false after fetch failure from queue view")
	}

	if m2.filterBranchMode {
		t.Error("Expected filterBranchMode=false after fetch failure")
	}
}

func TestTUITreeFilterBranchFetchConnectionErrorTriggersReconnect(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			loading:   true,
		},
	})
	m.consecutiveErrors = 2

	m2, cmd := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/to/repo-a"},
		err:       mockConnError("connection refused"),
	})

	if m2.consecutiveErrors != 3 {
		t.Errorf("Expected consecutiveErrors=3, got %d", m2.consecutiveErrors)
	}
	if !m2.reconnecting {
		t.Error("Expected reconnecting=true after 3 consecutive errors")
	}
	if cmd == nil {
		t.Error("Expected reconnect command to be returned")
	}
	if m2.filterTree[0].loading {
		t.Error("Expected loading=false after connection error")
	}
}

func TestTUITreeFilterSearchTriggersLazyBranchLoad(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
		},
		{
			name:      "repo-b",
			rootPaths: []string{"/path/to/repo-b"},
			count:     3,
			children:  []branchFilterItem{{name: "main", count: 3}},
		},
	})

	m2, cmd := pressKey(m, 'f')

	if !m2.filterTree[0].loading {
		t.Error("Expected repo-a loading=true after search with unloaded branches")
	}
	if m2.filterTree[1].loading {
		t.Error("Expected repo-b loading=false (branches already loaded)")
	}
	if cmd == nil {
		t.Error("Expected fetchBranchesForRepo command for unloaded repo")
	}
}

func TestTUITreeFilterSearchExpandsMatchingBranches(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
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

	m.filterSearch = "xyz"
	m.rebuildFilterFlatList()

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

func TestTUISearchTriggeredLoadDoesNotExpand(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 5),
	})

	m2, _ := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:      0,
		rootPaths:    []string{"/path/to/repo-a"},
		branches:     []branchFilterItem{{name: "main", count: 3}},
		expandOnLoad: false,
	})

	if m2.filterTree[0].expanded {
		t.Error("Search-triggered load should not set expanded=true")
	}

	m2.filterSearch = "main"
	m2.rebuildFilterFlatList()

	if len(m2.filterFlatList) != 2 {
		t.Errorf("Expected 2 entries during search, got %d",
			len(m2.filterFlatList))
	}

	m2.filterSearch = ""
	m2.rebuildFilterFlatList()

	if len(m2.filterFlatList) != 2 {
		t.Errorf("Expected 2 entries after clearing search, got %d",
			len(m2.filterFlatList))
	}
}

func TestTUILeftArrowCollapsesDuringSearch(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			children: []branchFilterItem{
				{name: "feature-xyz", count: 3},
				{name: "main", count: 2},
			},
		},
	})

	m.filterSearch = "xyz"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 2 {
		t.Fatalf("Expected 2 entries (repo-a + feature-xyz), got %d",
			len(m.filterFlatList))
	}

	m.filterSelectedIdx = 0
	m2, _ := pressSpecial(m, tea.KeyLeft)

	if !m2.filterTree[0].userCollapsed {
		t.Error("Expected userCollapsed=true after left-arrow during search")
	}

	if len(m2.filterFlatList) != 1 {
		t.Errorf("Expected 1 entry after collapse during search, got %d",
			len(m2.filterFlatList))
	}

	m2.filterSelectedIdx = 0
	m3, _ := pressSpecial(m2, tea.KeyRight)

	if m3.filterTree[0].userCollapsed {
		t.Error("Expected userCollapsed=false after right-arrow re-expand")
	}
	if !m3.filterTree[0].expanded {
		t.Error("Expected expanded=true after right-arrow")
	}
}

func TestTUIUserCollapsedResetsWhenSearchClears(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			children: []branchFilterItem{
				{name: "feature-xyz", count: 3},
			},
		},
	})

	m.filterSearch = "xyz"
	m.filterTree[0].userCollapsed = true
	m.rebuildFilterFlatList()

	if len(m.filterFlatList) != 1 {
		t.Fatalf("Expected 1 entry with userCollapsed, got %d",
			len(m.filterFlatList))
	}

	m.filterSearch = ""
	m.rebuildFilterFlatList()
	if m.filterTree[0].userCollapsed {
		t.Error("Expected userCollapsed=false after search cleared")
	}
}

func TestRootPathsMatchOrderIndependent(t *testing.T) {
	tests := []struct {
		name  string
		a, b  []string
		match bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"single equal", []string{"/a"}, []string{"/a"}, true},
		{"single differ", []string{"/a"}, []string{"/b"}, false},
		{"same order", []string{"/a", "/b"}, []string{"/a", "/b"}, true},
		{"diff order", []string{"/b", "/a"}, []string{"/a", "/b"}, true},
		{"diff length", []string{"/a"}, []string{"/a", "/b"}, false},
		{
			"three paths reordered",
			[]string{"/c", "/a", "/b"},
			[]string{"/a", "/b", "/c"},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rootPathsMatch(tt.a, tt.b)
			if got != tt.match {
				t.Errorf("rootPathsMatch(%v, %v) = %v, want %v",
					tt.a, tt.b, got, tt.match)
			}
		})
	}
}

func TestTUIBranchResponseReorderedRootPaths(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "multi-root",
			rootPaths: []string{"/path/b", "/path/a"},
			count:     5,
			loading:   true,
		},
	})

	m2, _ := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:      0,
		rootPaths:    []string{"/path/a", "/path/b"},
		branches:     []branchFilterItem{{name: "main", count: 5}},
		expandOnLoad: true,
	})

	if m2.filterTree[0].loading {
		t.Error("Expected loading=false (message should be accepted)")
	}
	if len(m2.filterTree[0].children) != 1 {
		t.Errorf("Expected 1 child, got %d", len(m2.filterTree[0].children))
	}
	if !m2.filterTree[0].expanded {
		t.Error("Expected expanded=true")
	}

	m3 := initFilterModel([]treeFilterNode{
		{
			name:      "other-repo",
			rootPaths: []string{"/path/c"},
			count:     3,
			loading:   true,
		},
	})

	m4, _ := updateModel(t, m3, tuiRepoBranchesMsg{
		repoIdx:      0,
		rootPaths:    []string{"/path/d"},
		branches:     []branchFilterItem{{name: "main", count: 3}},
		expandOnLoad: true,
	})

	if !m4.filterTree[0].loading {
		t.Error("Expected loading=true (stale message should be rejected)")
	}
	if m4.filterTree[0].children != nil {
		t.Error("Expected children=nil (stale message should not apply)")
	}
}

func TestTUIFetchUnloadedBranchesCapped(t *testing.T) {
	m := initFilterModel(nil)

	nodes := make([]treeFilterNode, 10)
	for i := range nodes {
		nodes[i] = treeFilterNode{
			name:      fmt.Sprintf("repo-%d", i),
			rootPaths: []string{fmt.Sprintf("/path/repo-%d", i)},
			count:     1,
		}
	}
	setupFilterTree(&m, nodes)

	m.filterSearch = "test"
	cmd := m.fetchUnloadedBranches()
	if cmd == nil {
		t.Fatal("Expected command from fetchUnloadedBranches")
	}

	loading := countLoading(&m)
	if loading != maxSearchBranchFetches {
		t.Errorf("Expected %d loading after first batch, got %d",
			maxSearchBranchFetches, loading)
	}

	cmd2 := m.fetchUnloadedBranches()
	if cmd2 != nil {
		t.Error("Expected nil cmd when max already in-flight")
	}
	if countLoading(&m) != maxSearchBranchFetches {
		t.Error("Loading count should not change")
	}
}

func TestTUIManualExpandFailureDoesNotBlockSearch(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/repo-a"},
			count:     5,
			loading:   true,
		},
	})

	m2, _ := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:      0,
		rootPaths:    []string{"/path/repo-a"},
		err:          errors.New("connection refused"),
		expandOnLoad: true,
	})

	if m2.filterTree[0].fetchFailed {
		t.Error("Manual expand failure should not set fetchFailed")
	}

	m2.filterSearch = "test"
	cmd := m2.fetchUnloadedBranches()
	if cmd == nil {
		t.Error("Expected fetch cmd — repo should be eligible after manual failure")
	}
	if !m2.filterTree[0].loading {
		t.Error("Expected loading=true from search auto-fetch")
	}
}

func TestTUITreeFilterSelectBranchTriggersRefetch(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())
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

	m.filterSelectedIdx = 3
	m.jobs = []storage.ReviewJob{
		makeJob(1, withBranch("main")),
		makeJob(2, withBranch("feature")),
	}
	m.loadingJobs = false

	m2, cmd := pressSpecial(m, tea.KeyEnter)

	if !m2.loadingJobs {
		t.Error("loadingJobs should be true after selecting branch filter")
	}
	if cmd == nil {
		t.Error("Should return fetchJobs command when selecting branch filter")
	}
}

func TestTUIBranchBackfillDoneSetWhenNoNullsRemain(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.branchBackfillDone = false

	m2, _ := updateModel(t, m, tuiBranchesMsg{
		backfillCount: 0,
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to be true when nullsRemaining is 0")
	}
}

func TestTUIBranchBackfillDoneSetEvenWhenNullsRemain(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.branchBackfillDone = false

	m2, _ := updateModel(t, m, tuiBranchesMsg{
		backfillCount: 2,
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to be true after first fetch (one-time operation)")
	}
}

func TestTUIBranchBackfillIsOneTimeOperation(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.branchBackfillDone = false

	m2, _ := updateModel(t, m, tuiBranchesMsg{
		backfillCount: 1,
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to be true after first fetch")
	}

	m3, _ := updateModel(t, m2, tuiBranchesMsg{
		backfillCount: 0,
	})

	if !m3.branchBackfillDone {
		t.Error("Expected branchBackfillDone to remain true after subsequent fetches")
	}
}

func TestTUIBranchBackfillDoneStaysTrueAfterNewJobs(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.branchBackfillDone = true

	m2, _ := updateModel(t, m, tuiBranchesMsg{
		backfillCount: 0,
	})

	if !m2.branchBackfillDone {
		t.Error("Expected branchBackfillDone to remain true (one-time operation)")
	}
}

func TestTUITreeFilterCollapseOnExpandedRepo(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
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

	m.filterSelectedIdx = 1

	m2, _ := pressSpecial(m, tea.KeyLeft)

	if m2.filterTree[0].expanded {
		t.Error("Expected repo-a to be collapsed after left arrow on expanded repo")
	}

	if len(m2.filterFlatList) != 2 {
		t.Errorf("Expected 2 entries after collapse, got %d", len(m2.filterFlatList))
	}
}

func TestTUIBKeyAutoExpandsCwdRepo(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	m.cwdRepoRoot = "/path/to/repo-b"

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
		{name: "repo-c", rootPaths: []string{"/path/to/repo-c"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos}

	m2, cmd := updateModel(t, m, msg)

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
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 5},
	})

	msg := tuiRepoBranchesMsg{
		repoIdx:      0,
		rootPaths:    []string{"/path/to/repo-a"},
		branches:     []branchFilterItem{{name: "main", count: 3}, {name: "dev", count: 2}},
		expandOnLoad: true,
	}

	m2, _ := updateModel(t, m, msg)

	if m2.filterBranchMode {
		t.Error("Expected filterBranchMode to be false after branches arrived")
	}

	if m2.filterSelectedIdx != 2 {
		t.Errorf("Expected filterSelectedIdx=2 (first branch), got %d", m2.filterSelectedIdx)
	}
	if len(m2.filterFlatList) > 2 && m2.filterFlatList[2].branchIdx != 0 {
		t.Errorf("Expected entry at idx 2 to be branchIdx=0, got %d", m2.filterFlatList[2].branchIdx)
	}
}

func TestTUIBKeyEscapeClearsBranchMode(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", count: 1},
	})

	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.filterBranchMode {
		t.Error("Expected filterBranchMode to be cleared on escape")
	}
	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
}

func TestTUIFilterCwdBranchSortsFirst(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
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

func TestTUIFilterEnterClearsBranchMode(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.filterBranchMode = true
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 5,
			children: []branchFilterItem{{name: "main", count: 3}}},
	})

	m.filterSelectedIdx = 1

	m2, _ := pressSpecial(m, tea.KeyEnter)

	if m2.filterBranchMode {
		t.Error("Expected filterBranchMode to be cleared on Enter")
	}
	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue after Enter, got %d", m2.currentView)
	}
}

func TestTUILockedBranchPreservedOnRepoSelect(t *testing.T) {

	nodes := []treeFilterNode{
		makeNode("repo-a", 3),
	}
	m := initFilterModel(nodes)
	m.activeBranchFilter = "locked-branch"
	m.filterStack = []string{"branch"}
	m.lockedBranchFilter = true

	m.filterSelectedIdx = 1
	m2, _ := pressKey(m, '\r')

	if m2.activeBranchFilter != "locked-branch" {
		t.Errorf(
			"Locked branch filter changed: got %q, want %q",
			m2.activeBranchFilter, "locked-branch",
		)
	}
}

func TestTUILockedRepoPreservedOnBranchSelect(t *testing.T) {

	node := makeNode("repo-a", 2)
	node.children = []branchFilterItem{
		{name: "feature-x", count: 2},
	}
	node.expanded = true
	m := initFilterModel([]treeFilterNode{node})
	m.activeRepoFilter = []string{"/locked/repo"}
	m.filterStack = []string{"repo"}
	m.lockedRepoFilter = true

	m.filterSelectedIdx = 1
	m2, _ := pressKey(m, '\r')

	if len(m2.activeRepoFilter) != 1 ||
		m2.activeRepoFilter[0] != "/locked/repo" {
		t.Errorf(
			"Locked repo filter changed: got %v, want [/locked/repo]",
			m2.activeRepoFilter,
		)
	}
}
