package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIFilterClearWithEsc(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"}

	m2, _ := pressSpecial(m, tea.KeyEscape)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}

	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterClearWithEscLayered(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"}
	m.hideAddressed = true

	m2, _ := pressSpecial(m, tea.KeyEscape)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if !m2.hideAddressed {
		t.Error("Expected hideAddressed to remain true after first escape")
	}

	m3, _ := pressSpecial(m2, tea.KeyEscape)

	if m3.hideAddressed {
		t.Error("Expected hideAddressed to be false after second escape")
	}
}

func TestTUIFilterClearHideAddressedOnly(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.hideAddressed = true

	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.hideAddressed {
		t.Error("Expected hideAddressed to be false after escape")
	}
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterEscapeWhileLoadingFiresNewFetch(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"}
	m.loadingJobs = true
	oldSeq := m.fetchSeq

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

	m3, _ := updateModel(t, m2, tuiJobsMsg{jobs: []storage.ReviewJob{makeJob(2)}, hasMore: false, seq: oldSeq})

	if !m3.loadingJobs {
		t.Error("Expected loadingJobs to still be true (stale response discarded)")
	}
}

func TestTUIFilterEscapeWhilePaginationDiscardsAppend(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.filterStack = []string{"repo"}
	m.loadingMore = true
	m.loadingJobs = false
	oldSeq := m.fetchSeq

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

	m3, _ := updateModel(t, m2, tuiJobsMsg{
		jobs:    []storage.ReviewJob{makeJob(99, withRepoName("stale"))},
		hasMore: true,
		append:  true,
		seq:     oldSeq,
	})

	for _, job := range m3.jobs {
		if job.ID == 99 {
			t.Error("Stale pagination data should have been discarded, not appended")
		}
	}
}

func TestTUIFilterEscapeCloses(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 1),
	})
	m.filterSearch = "test"

	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if m2.filterSearch != "" {
		t.Errorf("Expected filterSearch to be cleared, got '%s'", m2.filterSearch)
	}
}

func TestTUIFilterStackPush(t *testing.T) {
	m := initFilterModel(nil)

	m.pushFilter("repo")
	if len(m.filterStack) != 1 || m.filterStack[0] != "repo" {
		t.Errorf("Expected filterStack=['repo'], got %v", m.filterStack)
	}

	m.pushFilter("branch")
	if len(m.filterStack) != 2 || m.filterStack[0] != "repo" || m.filterStack[1] != "branch" {
		t.Errorf("Expected filterStack=['repo', 'branch'], got %v", m.filterStack)
	}
}

func TestTUIFilterStackPushMovesDuplicate(t *testing.T) {
	m := initFilterModel(nil)

	m.pushFilter("repo")
	m.pushFilter("branch")

	m.pushFilter("repo")
	if len(m.filterStack) != 2 || m.filterStack[0] != "branch" || m.filterStack[1] != "repo" {
		t.Errorf("Expected filterStack=['branch', 'repo'] after re-pushing repo, got %v", m.filterStack)
	}
}

func TestTUIFilterStackPopClearsValue(t *testing.T) {
	m := initFilterModel(nil)

	m.activeRepoFilter = []string{"/path/to/repo"}
	m.activeBranchFilter = "main"
	m.filterStack = []string{"repo", "branch"}

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

	popped = m.popFilter()
	if popped != "repo" {
		t.Errorf("Expected popped='repo', got %s", popped)
	}
	if len(m.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m.activeRepoFilter)
	}

	popped = m.popFilter()
	if popped != "" {
		t.Errorf("Expected popped='' on empty stack, got %s", popped)
	}
}

func TestTUIFilterStackEscapeOrder(t *testing.T) {
	m := initTestModel(
		withTestJobs(makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a"), withBranch("main"))),
		withSelection(0, 1),
		withCurrentView(tuiViewQueue),
		withActiveRepoFilter([]string{"/path/to/repo-a"}),
		withActiveBranchFilter("main"),
		withFilterStack("repo", "branch"),
	)

	steps := []struct {
		action func(m tuiModel) (tuiModel, tea.Cmd)
		assert func(t *testing.T, m tuiModel)
	}{
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) { return pressSpecial(m, tea.KeyEscape) },
			assert: func(t *testing.T, m tuiModel) {
				if m.activeBranchFilter != "" {
					t.Errorf("Expected branch filter to be cleared first, got %s", m.activeBranchFilter)
				}
				if len(m.activeRepoFilter) == 0 {
					t.Error("Expected repo filter to remain after first escape")
				}
				if len(m.filterStack) != 1 || m.filterStack[0] != "repo" {
					t.Errorf("Expected filterStack=['repo'] after first escape, got %v", m.filterStack)
				}
			},
		},
		{
			action: func(m tuiModel) (tuiModel, tea.Cmd) { return pressSpecial(m, tea.KeyEscape) },
			assert: func(t *testing.T, m tuiModel) {
				if len(m.activeRepoFilter) != 0 {
					t.Errorf("Expected repo filter to be cleared, got %v", m.activeRepoFilter)
				}
				if len(m.filterStack) != 0 {
					t.Errorf("Expected filterStack to be empty, got %v", m.filterStack)
				}
			},
		},
	}

	for _, step := range steps {
		m, _ = step.action(m)
		step.assert(t, m)
	}
}

func TestTUIFilterStackTitleBarOrder(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("myrepo"), withRepoPath("/path/to/myrepo"), withBranch("feature")),
	}
	m.currentView = tuiViewQueue

	m.activeBranchFilter = "feature"
	m.filterStack = []string{"branch"}
	m.activeRepoFilter = []string{"/path/to/myrepo"}
	m.filterStack = append(m.filterStack, "repo")

	output := m.View()

	if !strings.Contains(output, "[b: feature]") {
		t.Error("Expected output to contain [b: feature]")
	}
	if !strings.Contains(output, "[f: myrepo]") {
		t.Error("Expected output to contain [f: myrepo]")
	}

	bIdx := strings.Index(output, "[b: feature]")
	fIdx := strings.Index(output, "[f: myrepo]")
	if bIdx > fIdx {
		t.Error("Expected branch filter to appear before repo filter in title (stack order)")
	}
}

func TestTUIFilterStackReverseOrder(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("myrepo"), withRepoPath("/path/to/myrepo"), withBranch("develop")),
	}
	m.currentView = tuiViewQueue

	m.activeRepoFilter = []string{"/path/to/myrepo"}
	m.filterStack = []string{"repo"}
	m.activeBranchFilter = "develop"
	m.filterStack = append(m.filterStack, "branch")

	output := m.View()

	fIdx := strings.Index(output, "[f: myrepo]")
	bIdx := strings.Index(output, "[b: develop]")
	if fIdx > bIdx {
		t.Error("Expected repo filter to appear before branch filter in title (stack order)")
	}
}

func TestTUIRemoveFilterFromStack(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.filterStack = []string{"repo", "branch", "other"}

	m.removeFilterFromStack("branch")
	if len(m.filterStack) != 2 || m.filterStack[0] != "repo" || m.filterStack[1] != "other" {
		t.Errorf("Expected filterStack=['repo', 'other'], got %v", m.filterStack)
	}

	m.removeFilterFromStack("nonexistent")
	if len(m.filterStack) != 2 {
		t.Errorf("Expected filterStack length to remain 2, got %d", len(m.filterStack))
	}
}

// TestTUIReconnectClearsFetchFailed verifies that a successful
// daemon reconnect clears fetchFailed and retriggers branch
// fetches when search is active.
func TestTUIReconnectClearsFetchFailed(t *testing.T) {
	m := newTuiModel("http://localhost:7373", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	setupFilterTree(&m, []treeFilterNode{
		{name: "repo-a", rootPaths: []string{"/a"}, count: 3},
	})

	m.filterSearch = "test"
	m.filterTree[0].fetchFailed = true

	m2, cmd := updateModel(t, m, tuiReconnectMsg{
		newAddr: "http://localhost:7374",
	})

	if m2.filterTree[0].fetchFailed {
		t.Error("fetchFailed should be cleared on reconnect")
	}
	if cmd == nil {
		t.Fatal("Expected commands after reconnect")
	}

	if !m2.filterTree[0].loading {
		t.Error("Expected repo to be loading after reconnect with active search")
	}

	m3 := newTuiModel("http://localhost:7373", WithExternalIODisabled())
	m3.currentView = tuiViewFilter
	setupFilterTree(&m3, []treeFilterNode{
		{name: "repo-b", rootPaths: []string{"/b"}, count: 2},
	})
	m3.filterTree[0].fetchFailed = true

	m4, _ := updateModel(t, m3, tuiReconnectMsg{
		newAddr: "http://localhost:7374",
	})

	if m4.filterTree[0].fetchFailed {
		t.Error("fetchFailed should be cleared on reconnect even without search")
	}

	if m4.filterTree[0].loading {
		t.Error("Should not trigger branch fetch on reconnect without active search")
	}
}

func TestTUILockedFilterModalBlocksAll(t *testing.T) {

	nodes := []treeFilterNode{
		makeNode("repo-a", 3),
	}
	m := initFilterModel(nodes)
	m.activeRepoFilter = []string{"/locked/repo"}
	m.activeBranchFilter = "locked-branch"
	m.filterStack = []string{"repo", "branch"}
	m.lockedRepoFilter = true
	m.lockedBranchFilter = true

	m.filterSelectedIdx = 0
	m2, _ := pressKey(m, '\r')

	if m2.activeRepoFilter == nil {
		t.Error("Locked repo filter was cleared by All")
	}
	if m2.activeBranchFilter == "" {
		t.Error("Locked branch filter was cleared by All")
	}
}

func TestTUIPopFilterSkipsLockedWalksBack(t *testing.T) {

	m := initFilterModel(nil)
	m.activeRepoFilter = []string{"/unlocked/repo"}
	m.activeBranchFilter = "locked-branch"
	m.filterStack = []string{"repo", "branch"}
	m.lockedBranchFilter = true

	popped := m.popFilter()
	if popped != "repo" {
		t.Errorf("Expected popped='repo', got %q", popped)
	}
	if m.activeRepoFilter != nil {
		t.Errorf("Expected repo filter cleared, got %v", m.activeRepoFilter)
	}
	if m.activeBranchFilter != "locked-branch" {
		t.Errorf("Locked branch filter was modified: %q", m.activeBranchFilter)
	}

	if len(m.filterStack) != 1 || m.filterStack[0] != "branch" {
		t.Errorf("Expected filterStack=[branch], got %v", m.filterStack)
	}
}

func TestTUIPopFilterAllLocked(t *testing.T) {

	m := initFilterModel(nil)
	m.activeRepoFilter = []string{"/locked/repo"}
	m.activeBranchFilter = "locked-branch"
	m.filterStack = []string{"repo", "branch"}
	m.lockedRepoFilter = true
	m.lockedBranchFilter = true

	popped := m.popFilter()
	if popped != "" {
		t.Errorf("Expected empty pop on all-locked stack, got %q", popped)
	}
	if len(m.filterStack) != 2 {
		t.Errorf("Stack should be unchanged, got %v", m.filterStack)
	}
}

func TestTUIEscapeWithLockedFilters(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("r"), withRepoPath("/r"), withBranch("b")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.activeRepoFilter = []string{"/r"}
	m.activeBranchFilter = "b"
	m.filterStack = []string{"repo", "branch"}
	m.lockedBranchFilter = true

	m2, _ := pressSpecial(m, tea.KeyEscape)
	if m2.activeBranchFilter != "b" {
		t.Errorf("Locked branch cleared by escape: %q", m2.activeBranchFilter)
	}
	if m2.activeRepoFilter != nil {
		t.Errorf("Unlocked repo not cleared: %v", m2.activeRepoFilter)
	}

	m3, _ := pressSpecial(m2, tea.KeyEscape)
	if m3.activeBranchFilter != "b" {
		t.Errorf("Locked branch cleared by second escape: %q", m3.activeBranchFilter)
	}
}
