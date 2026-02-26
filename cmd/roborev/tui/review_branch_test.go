package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIReviewMsgSetsBranchName(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(1),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = viewQueue

	// Receive review message with branch name
	msg := reviewMsg{
		review:     makeReview(10, &storage.ReviewJob{ID: 1}, withReviewOutput("Review text")),
		jobID:      1,
		branchName: "main",
	}

	m2, _ := updateModel(t, m, msg)

	if m2.currentBranch != "main" {
		t.Errorf("Expected currentBranch to be 'main', got '%s'", m2.currentBranch)
	}
}

func TestTUIReviewMsgEmptyBranchForRange(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc123..def456")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = viewQueue

	// Receive review message with empty branch (range commits don't have branches)
	msg := reviewMsg{
		review:     makeReview(10, &storage.ReviewJob{ID: 1, GitRef: "abc123..def456"}, withReviewOutput("Review text")),
		jobID:      1,
		branchName: "", // Empty for ranges
	}

	m2, _ := updateModel(t, m, msg)

	if m2.currentBranch != "" {
		t.Errorf("Expected currentBranch to be empty for range, got '%s'", m2.currentBranch)
	}
}

func TestTUIBranchClearedOnFailedJobNavigation(t *testing.T) {
	// Test that navigating from a successful review with branch to a failed job clears the branch
	m := newModel("http://localhost", withExternalIODisabled())
	m.width = 100
	m.height = 30
	m.currentView = viewReview
	m.currentBranch = "main" // Cached from previous review
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Set up jobs: current is done (idx 0), next is failed (idx 1)
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc123")),
		makeJob(2, withStatus(storage.JobStatusFailed), withRef("def456"), withError("some error")),
	}
	m.currentReview = makeReview(10, &m.jobs[0], withReviewOutput("Good review"))

	// Navigate down to failed job (j or down key in review view)
	m2, _ := pressKey(m, 'j')

	// Branch should be cleared
	if m2.currentBranch != "" {
		t.Errorf("Expected currentBranch to be cleared when navigating to failed job, got '%s'", m2.currentBranch)
	}

	// Should still be in review view showing the failed job
	if m2.currentView != viewReview {
		t.Errorf("Expected to stay in review view, got %d", m2.currentView)
	}
	if m2.currentReview == nil || !strings.Contains(m2.currentReview.Output, "Job failed") {
		t.Error("Expected currentReview to show failed job error")
	}
}

func TestTUIBranchClearedOnFailedJobEnter(t *testing.T) {
	// Test that pressing Enter on a failed job clears the branch
	m := newModel("http://localhost", withExternalIODisabled())
	m.width = 100
	m.height = 30
	m.currentView = viewQueue
	m.currentBranch = "feature/old" // Stale from previous review
	m.selectedIdx = 0
	m.selectedJobID = 1

	m.jobs = []storage.ReviewJob{
		makeJob(1, withStatus(storage.JobStatusFailed), withRef("abc123"), withError("build failed")),
	}

	// Press Enter to view the failed job
	m2, _ := pressSpecial(m, tea.KeyEnter)

	// Branch should be cleared
	if m2.currentBranch != "" {
		t.Errorf("Expected currentBranch to be cleared for failed job, got '%s'", m2.currentBranch)
	}

	// Should show review view with error
	if m2.currentView != viewReview {
		t.Errorf("Expected review view, got %d", m2.currentView)
	}
}

func TestTUIRenderQueueViewBranchFilterOnlyNoPanic(t *testing.T) {
	// Test that renderQueueView doesn't panic when branch filter is active
	// but repo filter is empty (regression test for index out of range)
	m := newModel("http://localhost", withExternalIODisabled())
	m.width = 100
	m.height = 30
	m.currentView = viewQueue
	m.activeBranchFilter = "feature"
	m.activeRepoFilter = nil // Empty repo filter
	m.filterStack = []string{"branch"}
	m.jobs = []storage.ReviewJob{
		makeJob(1, withBranch("feature"), withRepoName("test")),
	}

	// This should not panic
	output := m.View()

	// Should show branch filter indicator
	if !strings.Contains(output, "[b: feature]") {
		t.Error("Expected branch filter indicator in output")
	}
	// Should NOT show repo filter indicator (since no repo filter)
	if strings.Contains(output, "[f:") {
		t.Error("Should not show repo filter indicator when activeRepoFilter is empty")
	}
}
