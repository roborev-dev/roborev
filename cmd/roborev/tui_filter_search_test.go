package main

import (
	"errors"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIFilterSearch(t *testing.T) {
	testNodes := []treeFilterNode{
		{name: "repo-alpha", count: 5},
		{name: "repo-beta", count: 3},
		{name: "something-else", count: 2},
	}

	cases := []struct {
		query         string
		expectedCount int
		description   string
	}{
		{"", 4, "No search (all visible)"},
		{"repo", 2, "Search 'repo'"},
		{"alpha", 1, "Search 'alpha'"},
		{"xyz", 0, "No matches"},
		{"all", 1, "Search 'all'"},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			m := initFilterModel(testNodes)
			m.filterSearch = tc.query
			m.rebuildFilterFlatList()
			if len(m.filterFlatList) != tc.expectedCount {
				t.Errorf("Expected %d visible, got %d", tc.expectedCount, len(m.filterFlatList))
			}
			if tc.query == "all" && len(m.filterFlatList) > 0 {
				if m.filterFlatList[0].repoIdx != -1 {
					t.Error("Expected the visible item to be the All entry")
				}
			}
		})
	}
}

func TestTUIFilterSearchSequential(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-alpha", 5),
		makeNode("repo-beta", 3),
		makeNode("other", 2),
	})

	if len(m.filterFlatList) != 4 {
		t.Fatalf("Initial: expected 4 entries, got %d", len(m.filterFlatList))
	}

	m.filterSearch = "repo"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 2 {
		t.Errorf("Step 1: expected 2 entries (repo-*), got %d", len(m.filterFlatList))
	}

	m.filterSearch = "alpha"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 {
		t.Errorf("Step 2: expected 1 entry (repo-alpha), got %d", len(m.filterFlatList))
	}
	if len(m.filterFlatList) > 0 {
		entry := m.filterFlatList[0]
		if entry.repoIdx == -1 {
			t.Error("Step 2: expected repo-alpha, got All")
		} else if m.filterTree[entry.repoIdx].name != "repo-alpha" {
			t.Errorf("Step 2: expected repo-alpha, got %s", m.filterTree[entry.repoIdx].name)
		}
	}

	m.filterSearch = ""
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 4 {
		t.Errorf("Step 3: expected 4 entries after clear, got %d", len(m.filterFlatList))
	}
}

func TestTUIFilterTypingSearch(t *testing.T) {
	m := initTestModel(
		withCurrentView(tuiViewFilter),
		withFilterTree([]treeFilterNode{makeNode("repo-a", 5)}),
		withFilterSelectedIdx(1),
	)

	steps := []struct {
		action func(m tuiModel) (tuiModel, tea.Cmd)
		assert func(t *testing.T, m tuiModel)
	}{
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) { return pressKey(m, 'a') },
			assert: func(t *testing.T, m tuiModel) {
				assertSearch(t, m, "a")
				if m.filterSelectedIdx != 0 {
					t.Errorf("Expected filterSelectedIdx reset to 0, got %d", m.filterSelectedIdx)
				}
			},
		},
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) { return pressKey(m, 'b') },
			assert: func(t *testing.T, m tuiModel) { assertSearch(t, m, "ab") },
		},
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) { return pressSpecial(m, tea.KeyBackspace) },
			assert: func(t *testing.T, m tuiModel) { assertSearch(t, m, "a") },
		},
	}

	for _, step := range steps {
		m, _ = step.action(m)
		step.assert(t, m)
	}
}

func TestTUIFilterTypingHAndL(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "highlight",
			rootPaths: []string{"/path/to/highlight"},
			count:     3,
			children:  []branchFilterItem{{name: "main", count: 3}},
		},
	})
	m.filterSelectedIdx = 1

	m2, _ := pressKey(m, 'h')
	if m2.filterSearch != "h" {
		t.Errorf("Expected filterSearch='h', got '%s'", m2.filterSearch)
	}

	m3, _ := pressKey(m2, 'l')
	if m3.filterSearch != "hl" {
		t.Errorf("Expected filterSearch='hl', got '%s'", m3.filterSearch)
	}
}

func TestTUIFilterSearchByRepoPath(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{name: "backend", rootPaths: []string{"/path/to/backend-dev", "/path/to/backend-prod"}, count: 2},
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
	})

	m.filterSearch = "backend-dev"
	m.rebuildFilterFlatList()

	if len(m.filterFlatList) != 1 {
		t.Errorf("Expected 1 visible (backend), got %d", len(m.filterFlatList))
	}
	if len(m.filterFlatList) > 0 && m.filterFlatList[0].repoIdx != 0 {
		t.Errorf("Expected to find 'backend' group at repoIdx 0")
	}
}

func TestTUIFilterSearchByDisplayName(t *testing.T) {
	m := initFilterModel([]treeFilterNode{

		{name: "My Project", rootPaths: []string{"/home/user/my-project-repo"}, count: 2},

		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},

		{name: "Backend Services", rootPaths: []string{"/srv/api-server", "/srv/worker-daemon"}, count: 2},
	})

	m.filterSearch = "my project"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 {
		t.Errorf("Search 'my project': expected 1 visible, got %d", len(m.filterFlatList))
	}

	m.filterSearch = "my-project-repo"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 {
		t.Errorf("Search 'my-project-repo': expected 1 visible, got %d", len(m.filterFlatList))
	}

	m.filterSearch = "backend"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 {
		t.Errorf("Search 'backend': expected 1 visible, got %d", len(m.filterFlatList))
	}

	m.filterSearch = "api-server"
	m.rebuildFilterFlatList()
	if len(m.filterFlatList) != 1 {
		t.Errorf("Search 'api-server': expected 1 visible, got %d", len(m.filterFlatList))
	}
}

func TestTUIFilterBackspaceMultiByte(t *testing.T) {
	m := initTestModel(
		withCurrentView(tuiViewFilter),
		withFilterTree([]treeFilterNode{makeNode("repo-a", 10)}),
	)

	steps := []struct {
		action func(m tuiModel) (tuiModel, tea.Cmd)
		assert func(t *testing.T, m tuiModel)
	}{
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) {
				m, _ = pressKey(m, 'a')
				m, _ = pressKeys(m, []rune("\xf0\x9f\x98\x8a"))
				return pressKey(m, 'b')
			},
			assert: func(t *testing.T, m tuiModel) { assertSearch(t, m, "a\xf0\x9f\x98\x8ab") },
		},
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) { return pressSpecial(m, tea.KeyBackspace) },
			assert: func(t *testing.T, m tuiModel) { assertSearch(t, m, "a\xf0\x9f\x98\x8a") },
		},
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) { return pressSpecial(m, tea.KeyBackspace) },
			assert: func(t *testing.T, m tuiModel) { assertSearch(t, m, "a") },
		},
	}

	for _, step := range steps {
		m, _ = step.action(m)
		step.assert(t, m)
	}
}

func TestTUIRightArrowDuringSearchLoad(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/to/repo-a"},
			count:     5,
			loading:   true,
		},
	})
	m.filterSelectedIdx = 1

	m2, _ := pressSpecial(m, tea.KeyRight)

	if !m2.filterTree[0].expanded {
		t.Error("Expected expanded=true after right-arrow on loading repo")
	}

	m3, _ := updateModel(t, m2, tuiRepoBranchesMsg{
		repoIdx:      0,
		rootPaths:    []string{"/path/to/repo-a"},
		branches:     []branchFilterItem{{name: "main", count: 3}},
		expandOnLoad: false,
	})

	if !m3.filterTree[0].expanded {
		t.Error("User right-arrow intent lost: expanded should be true")
	}

	if len(m3.filterFlatList) != 3 {
		t.Errorf("Expected 3 flat entries (user expanded), got %d",
			len(m3.filterFlatList))
	}
}

func TestTUISearchFetchProgressiveLoading(t *testing.T) {
	m := initFilterModel(nil)

	nodes := make([]treeFilterNode, 8)
	for i := range nodes {
		nodes[i] = treeFilterNode{
			name:      fmt.Sprintf("repo-%d", i),
			rootPaths: []string{fmt.Sprintf("/path/repo-%d", i)},
			count:     1,
		}
	}
	setupFilterTree(&m, nodes)
	m.filterSearch = "test"

	m.fetchUnloadedBranches()
	if countLoading(&m) != maxSearchBranchFetches {
		t.Fatalf("Expected %d loading, got %d",
			maxSearchBranchFetches, countLoading(&m))
	}

	for i := range 5 {
		if !m.filterTree[i].loading {
			t.Errorf("Expected repo-%d loading=true", i)
		}
	}
	for i := 5; i < 8; i++ {
		if m.filterTree[i].loading {
			t.Errorf("Expected repo-%d loading=false", i)
		}
	}

	m2, cmd := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/repo-0"},
		branches:  []branchFilterItem{{name: "main", count: 1}},
	})
	if cmd == nil {
		t.Error("Expected top-up fetch command after completion")
	}
	if countLoading(&m2) != maxSearchBranchFetches {
		t.Errorf("Expected %d loading after top-up, got %d",
			maxSearchBranchFetches, countLoading(&m2))
	}
	if !m2.filterTree[5].loading {
		t.Error("Expected repo-5 to start loading via top-up")
	}

	for i := 1; i < 8; i++ {
		if m2.filterTree[i].loading {
			m2, _ = updateModel(t, m2, tuiRepoBranchesMsg{
				repoIdx:   i,
				rootPaths: []string{fmt.Sprintf("/path/repo-%d", i)},
				branches:  []branchFilterItem{{name: "main", count: 1}},
			})
		}
	}
	if countLoaded(&m2) != 8 {
		t.Errorf("Expected all 8 repos loaded, got %d", countLoaded(&m2))
	}
	if countLoading(&m2) != 0 {
		t.Errorf("Expected 0 loading after all complete, got %d",
			countLoading(&m2))
	}
}

func TestTUISearchFetchErrorNoRetryLoop(t *testing.T) {
	m := initFilterModel(nil)

	nodes := make([]treeFilterNode, 3)
	for i := range nodes {
		nodes[i] = treeFilterNode{
			name:      fmt.Sprintf("repo-%d", i),
			rootPaths: []string{fmt.Sprintf("/path/repo-%d", i)},
			count:     1,
		}
	}
	setupFilterTree(&m, nodes)
	m.filterSearch = "test"

	m.fetchUnloadedBranches()
	if countLoading(&m) != 3 {
		t.Fatalf("Expected 3 loading, got %d", countLoading(&m))
	}

	m2, cmd := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/repo-0"},
		err:       errors.New("server error"),
	})

	if m2.filterTree[0].loading {
		t.Error("Expected loading=false after error")
	}
	if !m2.filterTree[0].fetchFailed {
		t.Error("Expected fetchFailed=true after error")
	}

	if cmd != nil {
		t.Error("Expected nil top-up cmd (no eligible repos to fetch)")
	}

	cmd2 := m2.fetchUnloadedBranches()
	if cmd2 != nil {
		t.Error("Expected nil cmd — failed repo should not be retried")
	}

	m2.filterSelectedIdx = 1
	m3, cmd3 := pressSpecial(m2, tea.KeyRight)
	if !m3.filterTree[0].loading {
		t.Error("Expected loading=true after manual retry")
	}
	if m3.filterTree[0].fetchFailed {
		t.Error("Expected fetchFailed=false after manual retry")
	}
	if cmd3 == nil {
		t.Error("Expected fetch command from manual retry")
	}
}

func TestTUIFetchFailedResetsOnSearchClear(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/repo-a"},
			count:     5,
		},
	})

	m.filterTree[0].fetchFailed = true

	m.filterSearch = "test"
	cmd := m.fetchUnloadedBranches()
	if cmd != nil {
		t.Error("Expected nil cmd — fetchFailed should block")
	}

	m.filterSearch = ""
	m.rebuildFilterFlatList()
	if m.filterTree[0].fetchFailed {
		t.Error("Expected fetchFailed=false after search cleared")
	}

	m.filterSearch = "test"
	cmd2 := m.fetchUnloadedBranches()
	if cmd2 == nil {
		t.Error("Expected fetch cmd in new search session")
	}
}

func TestTUILateErrorAfterSearchClear(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		{
			name:      "repo-a",
			rootPaths: []string{"/path/repo-a"},
			count:     5,
			loading:   true,
		},
	})

	m.filterSearch = ""
	m.rebuildFilterFlatList()

	m2, _ := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/path/repo-a"},
		err:       errors.New("timeout"),
	})

	if m2.filterTree[0].fetchFailed {
		t.Error("Late error after search clear should not set fetchFailed")
	}

	m2.filterSearch = "test"
	cmd := m2.fetchUnloadedBranches()
	if cmd == nil {
		t.Error("Expected fetch cmd — repo should be eligible")
	}
}

// TestTUIStaleSearchErrorIgnored verifies that an error from a
// previous search session does not set fetchFailed in the current
// session. Scenario: type "f" → clear → type "m" → old error
// arrives with stale searchSeq.
func TestTUIStaleSearchErrorIgnored(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/b"}, count: 2},
	})

	m, _ = pressKey(m, 'f')
	staleSeq := m.filterSearchSeq
	if staleSeq != 1 {
		t.Fatalf("Expected filterSearchSeq=1 after typing, got %d", staleSeq)
	}

	m.filterTree[0].loading = true

	m, _ = pressSpecial(m, tea.KeyBackspace)
	m, _ = pressKey(m, 'm')
	newSeq := m.filterSearchSeq
	if newSeq != 3 {
		t.Fatalf("Expected filterSearchSeq=3, got %d", newSeq)
	}

	m, cmd := updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/a"},
		err:       fmt.Errorf("connection refused"),
		searchSeq: staleSeq,
	})

	if m.filterTree[0].fetchFailed {
		t.Error(
			"fetchFailed should not be set by a stale search error",
		)
	}

	if cmd == nil {
		t.Error("Expected top-up fetch cmd for repo in new session")
	}
}

// TestTUISearchBeforeReposLoad verifies that when the user types
// search text before repos have loaded, fetchUnloadedBranches is
// triggered once repos arrive via tuiReposMsg.
func TestTUISearchBeforeReposLoad(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter

	m, _ = pressKey(m, 'f')
	if m.filterSearch != "f" {
		t.Fatalf("Expected filterSearch='f', got %q", m.filterSearch)
	}

	m2, cmd := updateModel(t, m, tuiReposMsg{
		repos: []repoFilterItem{
			{name: "repo-a", rootPaths: []string{"/a"}, count: 3},
			{name: "repo-b", rootPaths: []string{"/b"}, count: 2},
		},
	})

	if len(m2.filterTree) != 2 {
		t.Fatalf("Expected 2 repos, got %d", len(m2.filterTree))
	}

	if cmd == nil {
		t.Fatal("Expected fetchUnloadedBranches cmd after repos load with active search")
	}

	anyLoading := false
	for _, node := range m2.filterTree {
		if node.loading {
			anyLoading = true
			break
		}
	}
	if !anyLoading {
		t.Error("Expected at least one repo to be loading after repos load with active search")
	}
}

// TestTUISearchEditClearsFetchFailed verifies that changing search
// text (non-empty → non-empty) clears fetchFailed so previously
// failed repos are retried with the new search.
func TestTUISearchEditClearsFetchFailed(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/b"}, count: 2},
	})

	m, _ = pressKey(m, 'a')
	seqA := m.filterSearchSeq

	m, _ = updateModel(t, m, tuiRepoBranchesMsg{
		repoIdx:   0,
		rootPaths: []string{"/a"},
		err:       fmt.Errorf("timeout"),
		searchSeq: seqA,
	})
	if !m.filterTree[0].fetchFailed {
		t.Fatal("Expected fetchFailed=true after error in current session")
	}

	m, cmd := pressKey(m, 'b')

	if m.filterTree[0].fetchFailed {
		t.Error("fetchFailed should be cleared when search text changes")
	}

	if cmd == nil {
		t.Error("Expected fetch cmd for previously-failed repo after search edit")
	}
}
