package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIFilterNavigation(t *testing.T) {
	cases := []struct {
		startIdx    int
		key         rune
		expectedIdx int
		description string
	}{
		{0, 'j', 1, "Navigate down from 0"},
		{1, 'j', 2, "Navigate down from 1"},
		{2, 'j', 2, "Navigate down at boundary"},
		{2, 'k', 1, "Navigate up from 2"},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			m := initFilterModel([]treeFilterNode{
				makeNode("repo-a", 5),
				makeNode("repo-b", 3),
			})
			m.filterSelectedIdx = tc.startIdx

			m2, _ := pressKey(m, tc.key)
			if m2.filterSelectedIdx != tc.expectedIdx {
				t.Errorf("Expected filterSelectedIdx=%d, got %d", tc.expectedIdx, m2.filterSelectedIdx)
			}
		})
	}
}

func TestTUIFilterNavigationSequential(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 1),
		makeNode("repo-b", 1),
		makeNode("repo-c", 1),
	})

	keys := []rune{'j', 'j', 'j', 'k'}

	m2 := m
	for _, k := range keys {
		m2, _ = pressKey(m2, k)
	}

	if m2.filterSelectedIdx != 2 {
		t.Errorf("Expected final index 2 (repo-b), got %d", m2.filterSelectedIdx)
	}
}

func TestTUIFilterToZeroVisibleJobs(t *testing.T) {
	m := initFilterModel([]treeFilterNode{
		makeNode("repo-a", 2),
		makeNode("repo-b", 0),
	})

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	m.filterSelectedIdx = 2

	m2, cmd := pressSpecial(m, tea.KeyEnter)

	if len(m2.activeRepoFilter) != 1 || m2.activeRepoFilter[0] != "/path/to/repo-b" {
		t.Errorf("Expected activeRepoFilter=['/path/to/repo-b'], got %v", m2.activeRepoFilter)
	}
	if cmd == nil {
		t.Error("Expected fetchJobs command to be returned")
	}

	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 pending refetch, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 pending refetch, got %d", m2.selectedJobID)
	}

	m3, _ := updateModel(t, m2, tuiJobsMsg{jobs: []storage.ReviewJob{}})

	if m3.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 after receiving empty jobs, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 after receiving empty jobs, got %d", m3.selectedJobID)
	}
}

func TestTUIMultiPathFilterStatusCounts(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.height = 20
	m.daemonVersion = "test"

	addrTrue := true
	addrFalse := false

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoPath: "/path/to/backend-dev", Status: storage.JobStatusDone, Addressed: &addrTrue},
		{ID: 2, RepoPath: "/path/to/backend-prod", Status: storage.JobStatusDone, Addressed: &addrFalse},
		{ID: 3, RepoPath: "/path/to/backend-prod", Status: storage.JobStatusDone, Addressed: &addrFalse},
		{ID: 4, RepoPath: "/path/to/frontend", Status: storage.JobStatusDone, Addressed: &addrTrue},
		{ID: 5, RepoPath: "/path/to/frontend", Status: storage.JobStatusDone, Addressed: &addrTrue},
	}

	m.activeRepoFilter = []string{"/path/to/backend-dev", "/path/to/backend-prod"}

	output := m.renderQueueView()

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

func TestTUIBranchFilterApplied(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withBranch("main")),
		makeJob(2, withRepoName("repo-a"), withBranch("feature")),
		makeJob(3, withRepoName("repo-b"), withBranch("main")),
		{ID: 4, RepoName: "repo-b", Branch: ""},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

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

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withBranch("main")),
		{ID: 2, RepoName: "repo-a", Branch: ""},
		{ID: 3, RepoName: "repo-b", Branch: ""},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

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

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a"), withBranch("main")),
		makeJob(2, withRepoName("repo-a"), withRepoPath("/path/to/repo-a"), withBranch("feature")),
		makeJob(3, withRepoName("repo-b"), withRepoPath("/path/to/repo-b"), withBranch("main")),
		makeJob(4, withRepoName("repo-b"), withRepoPath("/path/to/repo-b"), withBranch("feature")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

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

func TestTUINavigateDownNoLoadMoreWhenBranchFiltered(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeBranchFilter = "feature"
	m.currentView = tuiViewQueue

	m2, cmd := pressSpecial(m, tea.KeyDown)

	if m2.loadingMore {
		t.Error("loadingMore should not be set when branch filter is active")
	}
	if cmd != nil {
		t.Error("Should not return command when branch filter is active")
	}
}

func TestTUINavigateJKeyNoLoadMoreWhenBranchFiltered(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeBranchFilter = "feature"
	m.currentView = tuiViewQueue

	m2, cmd := pressKey(m, 'j')

	if m2.loadingMore {
		t.Error("loadingMore should not be set when branch filter is active (j key)")
	}
	if cmd != nil {
		t.Error("Should not return command when branch filter is active (j key)")
	}
}

func TestTUIPageDownNoLoadMoreWhenBranchFiltered(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeBranchFilter = "feature"
	m.currentView = tuiViewQueue
	m.height = 20

	m2, cmd := pressSpecial(m, tea.KeyPgDown)

	if m2.loadingMore {
		t.Error("loadingMore should not be set when branch filter is active (pgdown)")
	}
	if cmd != nil {
		t.Error("Should not return command when branch filter is active (pgdown)")
	}
}

func TestTUIBranchFilterClearTriggersRefetch(t *testing.T) {

	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.currentView = tuiViewQueue
	m.activeBranchFilter = "feature"
	m.filterStack = []string{"branch"}
	m.jobs = []storage.ReviewJob{makeJob(1, withBranch("feature"))}
	m.loadingJobs = false

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

func TestTUIQueueNavigationWithFilter(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

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
	m.activeRepoFilter = []string{"/path/to/repo-a"}

	m2, _ := pressKey(m, 'j')

	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	m3, _ := pressKey(m2, 'j')

	if m3.selectedIdx != 4 {
		t.Errorf("Expected selectedIdx=4, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 5 {
		t.Errorf("Expected selectedJobID=5, got %d", m3.selectedJobID)
	}

	m4, _ := pressKey(m3, 'k')

	if m4.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m4.selectedIdx)
	}
}

func TestTUIJobsRefreshWithFilter(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.selectedIdx = 2
	m.selectedJobID = 3
	m.activeRepoFilter = []string{"/path/to/repo-a"}

	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}}

	m2, _ := updateModel(t, m, newJobs)

	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	newJobs = tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
	}}

	m3, _ := updateModel(t, m2, newJobs)

	if m3.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx=0, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 1 {
		t.Errorf("Expected selectedJobID=1, got %d", m3.selectedJobID)
	}
}

func TestTUIRefreshWithZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.activeRepoFilter = []string{"/path/to/repo-b"}
	m.selectedIdx = 0
	m.selectedJobID = 1

	newJobs := []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m2, _ := updateModel(t, m, tuiJobsMsg{jobs: newJobs})

	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 for zero visible jobs after refresh, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 for zero visible jobs after refresh, got %d", m2.selectedJobID)
	}
}

func TestTUIActionsNoOpWithZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}
	m.activeRepoFilter = []string{"/path/to/repo-b"}
	m.selectedIdx = -1
	m.selectedJobID = 0
	m.currentView = tuiViewQueue

	m2, cmd := pressSpecial(m, tea.KeyEnter)
	if cmd != nil {
		t.Error("Expected no command for enter with no visible jobs")
	}
	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected to stay in queue view, got %d", m2.currentView)
	}

	_, cmd = pressKey(m, 'x')
	if cmd != nil {
		t.Error("Expected no command for cancel with no visible jobs")
	}

	_, cmd = pressKey(m, 'a')
	if cmd != nil {
		t.Error("Expected no command for address with no visible jobs")
	}
}

func TestTUIBKeyOpensBranchFilter(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.jobs = []storage.ReviewJob{makeJob(1, withRepoName("repo-a"))}
	m.selectedIdx = 0

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

func TestTUIFilterOpenBatchesBackfill(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.branchBackfillDone = false
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

func TestTUIFilterCwdRepoSortsFirst(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter
	m.cwdRepoRoot = "/path/to/repo-b"

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
		{name: "repo-c", rootPaths: []string{"/path/to/repo-c"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos}

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

func TestTUIFilterNoCwdNoReorder(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewFilter

	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 3},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 2},
		{name: "repo-c", rootPaths: []string{"/path/to/repo-c"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos}

	m2, _ := updateModel(t, m, msg)

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

func TestTUIBKeyNoOpOutsideQueue(t *testing.T) {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	m.currentView = tuiViewReview

	m2, cmd := pressKey(m, 'b')

	if m2.currentView != tuiViewReview {
		t.Errorf("Expected view to remain tuiViewReview, got %d", m2.currentView)
	}
	if m2.filterBranchMode {
		t.Error("Expected filterBranchMode to remain false when pressing b outside queue")
	}
	if cmd != nil {
		t.Error("Expected no command when pressing b outside queue")
	}
}
