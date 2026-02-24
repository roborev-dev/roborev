package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIFetchReviewNotFound(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.fetchReview(999)
	msg := cmd()

	errMsg, ok := msg.(tuiErrMsg)
	if !ok {
		t.Fatalf("Expected tuiErrMsg for 404, got %T: %v", msg, msg)
	}
	if errMsg.Error() != "no review found" {
		t.Errorf("Expected 'no review found', got: %v", errMsg)
	}
}

func TestTUIFetchReviewServerError(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cmd := m.fetchReview(1)
	msg := cmd()

	errMsg, ok := msg.(tuiErrMsg)
	if !ok {
		t.Fatalf("Expected tuiErrMsg for 500, got %T: %v", msg, msg)
	}
	if errMsg.Error() != "fetch review: 500 Internal Server Error" {
		t.Errorf("Expected status in error, got: %v", errMsg)
	}
}

func TestTUIFetchReviewFallbackSHAResponses(t *testing.T) {
	// Test that when job_id responses are empty, TUI falls back to SHA-based responses
	requestedPaths := []string{}
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.String())

		if r.URL.Path == "/api/review" {
			// Return a review for a single commit (not a range or dirty)
			review := storage.Review{
				ID:     1,
				JobID:  42,
				Agent:  "test",
				Output: "No issues found.",
				Job: &storage.ReviewJob{
					ID:       42,
					GitRef:   "abc123def456", // Single commit SHA (not a range)
					RepoPath: "/test/repo",
				},
			}
			json.NewEncoder(w).Encode(review)
			return
		}

		if r.URL.Path == "/api/comments" {
			jobID := r.URL.Query().Get("job_id")
			sha := r.URL.Query().Get("sha")

			if jobID != "" {
				// Job ID query returns empty responses
				json.NewEncoder(w).Encode(map[string]any{
					"responses": []storage.Response{},
				})
				return
			}
			if sha != "" {
				// SHA fallback query returns legacy responses
				json.NewEncoder(w).Encode(map[string]any{
					"responses": []storage.Response{
						{ID: 1, Responder: "user", Response: "Legacy response from SHA lookup"},
					},
				})
				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.fetchReview(42)
	msg := cmd()

	reviewMsg, ok := msg.(tuiReviewMsg)
	if !ok {
		t.Fatalf("Expected tuiReviewMsg, got %T: %v", msg, msg)
	}

	// Should have fetched both job_id and sha responses
	foundJobIDRequest := false
	foundSHARequest := false
	for _, path := range requestedPaths {
		if strings.Contains(path, "job_id=42") {
			foundJobIDRequest = true
		}
		if strings.Contains(path, "sha=abc123def456") {
			foundSHARequest = true
		}
	}

	if !foundJobIDRequest {
		t.Error("Expected request for job_id responses")
	}
	if !foundSHARequest {
		t.Error("Expected fallback request for SHA responses when job_id returned empty")
	}

	// Should have the legacy response from SHA fallback
	if len(reviewMsg.responses) != 1 {
		t.Fatalf("Expected 1 response from SHA fallback, got %d", len(reviewMsg.responses))
	}
	if reviewMsg.responses[0].Response != "Legacy response from SHA lookup" {
		t.Errorf("Expected legacy response, got: %s", reviewMsg.responses[0].Response)
	}
}

func TestTUIFetchReviewNoFallbackForRangeReview(t *testing.T) {
	// Test that SHA fallback is NOT used for range reviews (abc..def format)
	requestedPaths := []string{}
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.String())

		if r.URL.Path == "/api/review" {
			// Return a review for a commit range (not a single commit)
			review := storage.Review{
				ID:     1,
				JobID:  42,
				Agent:  "test",
				Output: "No issues found.",
				Job: &storage.ReviewJob{
					ID:       42,
					GitRef:   "abc123..def456", // Range review
					RepoPath: "/test/repo",
				},
			}
			json.NewEncoder(w).Encode(review)
			return
		}

		if r.URL.Path == "/api/comments" {
			// Return empty responses for job_id
			json.NewEncoder(w).Encode(map[string]any{
				"responses": []storage.Response{},
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.fetchReview(42)
	msg := cmd()

	_, ok := msg.(tuiReviewMsg)
	if !ok {
		t.Fatalf("Expected tuiReviewMsg, got %T: %v", msg, msg)
	}

	// Should NOT have made a SHA fallback request for range review
	for _, path := range requestedPaths {
		if strings.Contains(path, "sha=") {
			t.Error("Should not make SHA fallback request for range review")
		}
	}
}

func TestTUIReviewNavigation(t *testing.T) {
	tests := []struct {
		name          string
		initialJobs   []storage.ReviewJob
		initialIdx    int
		initialID     int64
		initialScroll int
		key           any // rune or tea.KeyType
		wantIdx       int
		wantJobID     int64
		wantScroll    int
		wantFlash     string
		wantCmd       bool // Expect a command (fetch)
	}{
		{
			name: "J Next skips queued/running",
			initialJobs: []storage.ReviewJob{
				makeJob(1),
				makeJob(2, withStatus(storage.JobStatusQueued)),
				makeJob(3, withStatus(storage.JobStatusRunning)),
				makeJob(4, withStatus(storage.JobStatusFailed)),
				makeJob(5),
			},
			initialIdx:    0,
			initialID:     1,
			initialScroll: 5,
			key:           'j',
			wantIdx:       3,
			wantJobID:     4,
			wantScroll:    0,
			wantCmd:       false, // Failed job = inline, no fetch
		},
		{
			name: "K Prev skips queued/running",
			initialJobs: []storage.ReviewJob{
				makeJob(1),
				makeJob(2, withStatus(storage.JobStatusQueued)),
				makeJob(3, withStatus(storage.JobStatusRunning)),
				makeJob(4, withStatus(storage.JobStatusFailed)),
				makeJob(5),
			},
			initialIdx:    4,
			initialID:     5,
			initialScroll: 10,
			key:           'k',
			wantIdx:       3,
			wantJobID:     4,
			wantScroll:    0,
			wantCmd:       false,
		},
		{
			name: "Left Arrow acts like K (Prev)",
			initialJobs: []storage.ReviewJob{
				makeJob(1),
				makeJob(2),
				makeJob(3),
			},
			initialIdx: 1,
			initialID:  2,
			key:        tea.KeyLeft,
			wantIdx:    0,
			wantJobID:  1,
			wantCmd:    true,
		},
		{
			name: "Right Arrow acts like J (Next)",
			initialJobs: []storage.ReviewJob{
				makeJob(1),
				makeJob(2),
				makeJob(3),
			},
			initialIdx: 1,
			initialID:  2,
			key:        tea.KeyRight,
			wantIdx:    2,
			wantJobID:  3,
			wantCmd:    true,
		},
		{
			name: "Boundary Start (K/Right)",
			initialJobs: []storage.ReviewJob{
				makeJob(1),
				makeJob(2, withStatus(storage.JobStatusQueued)),
				makeJob(3),
			},
			initialIdx: 0,
			initialID:  1,
			key:        'k',
			wantIdx:    0,
			wantJobID:  1,
			wantFlash:  "No newer review",
		},
		{
			name: "Boundary End (J/Left)",
			initialJobs: []storage.ReviewJob{
				makeJob(1),
				makeJob(2, withStatus(storage.JobStatusQueued)),
				makeJob(3),
			},
			initialIdx: 2,
			initialID:  3,
			key:        'j',
			wantIdx:    2,
			wantJobID:  3,
			wantFlash:  "No older review",
		},
		{
			name: "Navigate to Failed Job Inline",
			initialJobs: []storage.ReviewJob{
				makeJob(1),
				makeJob(2, withStatus(storage.JobStatusFailed), withAgent("codex"), withError("something went wrong")),
			},
			initialIdx: 0,
			initialID:  1,
			key:        'j',
			wantIdx:    1,
			wantJobID:  2,
			wantCmd:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTuiModel("http://localhost")
			m.jobs = tt.initialJobs
			m.selectedIdx = tt.initialIdx
			m.selectedJobID = tt.initialID
			m.currentView = tuiViewReview
			// Setup current review to match initial selection
			m.currentReview = makeReview(10, &tt.initialJobs[tt.initialIdx])
			m.reviewScroll = tt.initialScroll

			var m2 tuiModel
			var cmd tea.Cmd

			switch k := tt.key.(type) {
			case rune:
				m2, cmd = pressKey(m, k)
			case tea.KeyType:
				m2, cmd = pressSpecial(m, k)
			}

			assertSelection(t, m2, tt.wantIdx, tt.wantJobID)

			if tt.wantScroll != -1 && m2.reviewScroll != tt.wantScroll {
				t.Errorf("reviewScroll: got %d, want %d", m2.reviewScroll, tt.wantScroll)
			}

			if tt.wantFlash != "" {
				if m2.flashMessage != tt.wantFlash {
					t.Errorf("flashMessage: got %q, want %q", m2.flashMessage, tt.wantFlash)
				}
				if m2.flashView != tuiViewReview {
					t.Errorf("flashView: got %d, want %d", m2.flashView, tuiViewReview)
				}
				if m2.flashExpiresAt.IsZero() {
					t.Error("flashExpiresAt should be set")
				}
				if !m2.flashExpiresAt.After(time.Now()) {
					t.Error("flashExpiresAt should be in the future")
				}
			}

			if tt.wantCmd && cmd == nil {
				t.Error("expected command, got nil")
			} else if !tt.wantCmd && cmd != nil {
				t.Error("expected no command, got one")
			}

			// Specific check for failed job inline content
			if tt.name == "Navigate to Failed Job Inline" {
				if m2.currentReview == nil {
					t.Fatal("Expected currentReview to be set for failed job")
				}
				if !strings.Contains(m2.currentReview.Output, "something went wrong") {
					t.Errorf("Expected output to contain error, got '%s'", m2.currentReview.Output)
				}
				if m2.currentReview.Agent != "codex" {
					t.Errorf("Expected agent to be 'codex', got '%s'", m2.currentReview.Agent)
				}
			}
		})
	}
}

func TestTUIReviewStaleResponseIgnored(t *testing.T) {
	// Test that stale review responses are ignored (race condition fix)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2 // Currently viewing job 2
	m.currentView = tuiViewReview
	m.currentReview = makeReview(20, &storage.ReviewJob{ID: 2}, withReviewOutput("Review for job 2"))

	// Simulate a stale response arriving for job 1 (user navigated away)
	staleMsg := tuiReviewMsg{
		review: makeReview(10, &storage.ReviewJob{ID: 1}, withReviewOutput("Stale review for job 1")),
		jobID:  1, // This doesn't match selectedJobID (2)
	}

	m2, _ := updateModel(t, m, staleMsg)

	// Should ignore the stale response
	if m2.currentReview.Output != "Review for job 2" {
		t.Errorf("Expected stale response to be ignored, got output: %s", m2.currentReview.Output)
	}
	if m2.currentReview.ID != 20 {
		t.Errorf("Expected review ID to remain 20, got %d", m2.currentReview.ID)
	}
}

func TestTUIReviewMsgWithMatchingJobID(t *testing.T) {
	// Test that review responses with matching job ID are accepted
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue // Still in queue view, waiting for fetch

	validMsg := tuiReviewMsg{
		review: makeReview(10, &storage.ReviewJob{ID: 1}, withReviewOutput("New review")),
		jobID:  1,
	}

	m2, _ := updateModel(t, m, validMsg)

	// Should accept the response and switch to review view
	if m2.currentView != tuiViewReview {
		t.Errorf("Expected to switch to review view, got %d", m2.currentView)
	}
	if m2.currentReview == nil || m2.currentReview.Output != "New review" {
		t.Error("Expected currentReview to be updated")
	}
	if m2.reviewScroll != 0 {
		t.Errorf("Expected reviewScroll to be 0, got %d", m2.reviewScroll)
	}
}

func TestTUISelectionSyncInReviewView(t *testing.T) {
	// Test that selectedIdx syncs with currentReview.Job.ID when jobs refresh
	m := newTuiModel("http://localhost")

	// Initial state: viewing review for job 2
	m.jobs = []storage.ReviewJob{
		makeJob(3),
		makeJob(2),
		makeJob(1),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewReview
	m.currentReview = makeReview(20, &storage.ReviewJob{ID: 2})

	// New job arrives at the top, shifting indices
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(4), // New job at top
		makeJob(3),
		makeJob(2), // Now at index 2
		makeJob(1),
	}}

	m2, _ := updateModel(t, m, newJobs)

	// selectedIdx should sync with currentReview.Job.ID (2), now at index 2
	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2 (synced with review job), got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2, got %d", m2.selectedJobID)
	}
}

func TestTUIJobsRefreshDuringReviewNavigation(t *testing.T) {
	// Test that jobs refresh during review navigation doesn't reset selection
	// This tests the race condition fix: user navigates to job 3, but jobs refresh
	// arrives before the review loads. Selection should stay on job 3, not revert
	// to the currently displayed review's job (job 2).
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewReview
	m.currentReview = makeReview(20, &storage.ReviewJob{ID: 2}, withReviewOutput("Review for job 2"))

	// Simulate user navigating to next review (job 3)
	// This updates selectedIdx and selectedJobID but doesn't update currentReview yet
	m.selectedIdx = 2
	m.selectedJobID = 3

	// Before the review for job 3 arrives, a jobs refresh comes in
	refreshedJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}}

	m2, _ := updateModel(t, m, refreshedJobs)

	// Selection should stay on job 3 (user's navigation intent), not revert to job 2
	if m2.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3 (user's navigation), got %d", m2.selectedJobID)
	}
	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2 (job 3's index), got %d", m2.selectedIdx)
	}

	// currentReview should still be the old one (review for job 3 hasn't loaded)
	if m2.currentReview.Job.ID != 2 {
		t.Errorf("Expected currentReview to still be job 2, got job %d", m2.currentReview.Job.ID)
	}

	// Now when the review for job 3 arrives, it should be accepted
	newReviewMsg := tuiReviewMsg{
		review: makeReview(30, &storage.ReviewJob{ID: 3}, withReviewOutput("Review for job 3")),
		jobID:  3,
	}

	m3, _ := updateModel(t, m2, newReviewMsg)

	if m3.currentReview.ID != 30 {
		t.Errorf("Expected new review ID=30, got %d", m3.currentReview.ID)
	}
	if m3.currentReview.Output != "Review for job 3" {
		t.Errorf("Expected new review output, got %s", m3.currentReview.Output)
	}
}

func TestTUIEmptyRefreshWhileViewingReview(t *testing.T) {
	// Test that transient empty jobs refresh doesn't break selection
	// when viewing a review. Selection should restore to displayed review
	// when jobs repopulate.
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewReview
	m.currentReview = makeReview(20, &storage.ReviewJob{ID: 2}, withReviewOutput("Review for job 2"))

	// Transient empty refresh arrives
	emptyJobs := tuiJobsMsg{jobs: []storage.ReviewJob{}}

	m2, _ := updateModel(t, m, emptyJobs)

	// selectedJobID should be preserved (not cleared) while viewing a review
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2 preserved during empty refresh, got %d", m2.selectedJobID)
	}

	// Jobs repopulate
	repopulatedJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}}

	m3, _ := updateModel(t, m2, repopulatedJobs)

	// Selection should restore to job 2 (the displayed review)
	if m3.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2 after repopulate, got %d", m3.selectedJobID)
	}
	if m3.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1 (job 2's index), got %d", m3.selectedIdx)
	}
}

func TestTUIEmptyRefreshSeedsFromCurrentReview(t *testing.T) {
	// Test that if selectedJobID somehow becomes 0 while viewing a review,
	// it gets seeded from the current review when jobs repopulate
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{}
	m.selectedIdx = 0
	m.selectedJobID = 0 // Somehow cleared
	m.currentView = tuiViewReview
	m.currentReview = makeReview(20, &storage.ReviewJob{ID: 2}, withReviewOutput("Review for job 2"))

	// Jobs repopulate
	repopulatedJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(1),
		makeJob(2),
		makeJob(3),
	}}

	m2, _ := updateModel(t, m, repopulatedJobs)

	// Selection should be seeded from currentReview.Job.ID
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2 (seeded from currentReview), got %d", m2.selectedJobID)
	}
	if m2.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1 (job 2's index), got %d", m2.selectedIdx)
	}
}

func TestTUIReviewMsgSetsBranchName(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{
		makeJob(1),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Receive review message with branch name
	msg := tuiReviewMsg{
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
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc123..def456")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Receive review message with empty branch (range commits don't have branches)
	msg := tuiReviewMsg{
		review:     makeReview(10, &storage.ReviewJob{ID: 1, GitRef: "abc123..def456"}, withReviewOutput("Review text")),
		jobID:      1,
		branchName: "", // Empty for ranges
	}

	m2, _ := updateModel(t, m, msg)

	if m2.currentBranch != "" {
		t.Errorf("Expected currentBranch to be empty for range, got '%s'", m2.currentBranch)
	}
}

func TestTUIRenderReviewViewWithBranchAndAddressed(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentBranch = "feature/test"
	m.currentReview = &storage.Review{
		ID:        10,
		Output:    "Some review output",
		Addressed: true,
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234",
			RepoName: "myrepo",
			Agent:    "codex",
		},
	}

	output := m.View()

	// Should contain branch info
	if !strings.Contains(output, "on feature/test") {
		t.Error("Expected output to contain 'on feature/test'")
	}

	// Should contain [ADDRESSED]
	if !strings.Contains(output, "[ADDRESSED]") {
		t.Error("Expected output to contain '[ADDRESSED]'")
	}

	// Should contain repo name and ref
	if !strings.Contains(output, "myrepo") {
		t.Error("Expected output to contain 'myrepo'")
	}
	if !strings.Contains(output, "abc1234") {
		t.Error("Expected output to contain 'abc1234'")
	}
}

func TestTUIRenderReviewViewWithModel(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     10,
		Agent:  "codex",
		Output: "Some review output",
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234",
			RepoName: "myrepo",
			Agent:    "codex",
			Model:    "o3",
		},
	}

	output := m.View()

	// Should contain agent with model in format "(codex: o3)"
	if !strings.Contains(output, "(codex: o3)") {
		t.Errorf("Expected output to contain '(codex: o3)', got:\n%s", output)
	}
}

func TestTUIRenderReviewViewWithoutModel(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     10,
		Agent:  "codex",
		Output: "Some review output",
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234",
			RepoName: "myrepo",
			Agent:    "codex",
			Model:    "", // No model set
		},
	}

	output := m.View()

	// Should contain just the agent "(codex)" without model
	if !strings.Contains(output, "(codex)") {
		t.Errorf("Expected output to contain '(codex)', got:\n%s", output)
	}
	// Should NOT contain the colon separator that would indicate a model
	if strings.Contains(output, "(codex:") {
		t.Error("Expected output NOT to contain '(codex:' when no model is set")
	}
}

func TestTUIRenderPromptViewWithModel(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewPrompt
	m.currentReview = &storage.Review{
		ID:     10,
		Agent:  "codex",
		Prompt: "Review this code",
		Job: &storage.ReviewJob{
			ID:     1,
			GitRef: "abc1234",
			Agent:  "codex",
			Model:  "o3",
		},
	}

	output := m.View()

	// Should contain job ID
	if !strings.Contains(output, "#1") {
		t.Errorf("Expected Prompt view to contain '#1', got:\n%s", output)
	}
	// Should contain agent with model in format "(codex: o3)"
	if !strings.Contains(output, "(codex: o3)") {
		t.Errorf("Expected Prompt view to contain '(codex: o3)', got:\n%s", output)
	}
}

func TestTUIRenderPromptViewWithoutModel(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewPrompt
	m.currentReview = &storage.Review{
		ID:     10,
		Agent:  "codex",
		Prompt: "Review this code",
		Job: &storage.ReviewJob{
			ID:     1,
			GitRef: "abc1234",
			Agent:  "codex",
			Model:  "", // No model set
		},
	}

	output := m.View()

	// Should contain just the agent "(codex)" without model
	if !strings.Contains(output, "(codex)") {
		t.Errorf("Expected Prompt view to contain '(codex)', got:\n%s", output)
	}
	// Should NOT contain the colon separator that would indicate a model
	if strings.Contains(output, "(codex:") {
		t.Error("Expected Prompt view NOT to contain '(codex:' when no model is set")
	}
}

func TestTUIRenderReviewViewNoBranchForRange(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentBranch = "" // Empty for range
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "Some review output",
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc123..def456",
			RepoName: "myrepo",
			Agent:    "codex",
		},
	}

	output := m.View()

	// Should NOT contain "on " prefix when no branch
	if strings.Contains(output, " on ") {
		t.Error("Expected output to NOT contain ' on ' for range commits")
	}

	// Should contain the range ref
	if !strings.Contains(output, "abc123..def456") {
		t.Error("Expected output to contain the range ref")
	}
}

func TestTUIRenderReviewViewNoBlankLineWithoutVerdict(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "Line 1\nLine 2\nLine 3",
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234",
			RepoName: "myrepo",
			Agent:    "codex",
			Verdict:  nil, // No verdict
		},
	}

	output := m.View()
	lines := strings.Split(output, "\n")

	// Line 0: Title
	if !strings.Contains(lines[0], "Review") {
		t.Errorf("Line 0 should contain 'Review', got: %s", lines[0])
	}

	// Line 1: Location (ref)
	if len(lines) > 1 && !strings.Contains(lines[1], "abc1234") {
		t.Errorf("Line 1 should contain ref 'abc1234', got: %s", lines[1])
	}

	// Content should appear after the header (no verdict line inserted)
	foundContent := false
	for _, line := range lines[2:] {
		if strings.Contains(stripANSI(line), "Line 1") {
			foundContent = true
			break
		}
	}
	if !foundContent {
		t.Errorf("Content should contain 'Line 1' after header, output:\n%s", output)
	}
}

func TestTUIRenderReviewViewVerdictOnLine2(t *testing.T) {
	verdictPass := "P"
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "Line 1\nLine 2\nLine 3",
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234",
			RepoName: "myrepo",
			Agent:    "codex",
			Verdict:  &verdictPass,
		},
	}

	output := m.View()
	lines := strings.Split(output, "\n")

	// Line 0: Title
	if !strings.Contains(lines[0], "Review") {
		t.Errorf("Line 0 should contain 'Review', got: %s", lines[0])
	}

	// Line 1: Location (ref)
	if len(lines) > 1 && !strings.Contains(lines[1], "abc1234") {
		t.Errorf("Line 1 should contain ref 'abc1234', got: %s", lines[1])
	}

	// Line 2: Verdict
	if len(lines) > 2 && !strings.Contains(lines[2], "Verdict") {
		t.Errorf("Line 2 should contain 'Verdict', got: %s", lines[2])
	}

	// Content should appear after verdict line
	foundContent := false
	for _, line := range lines[3:] {
		if strings.Contains(stripANSI(line), "Line 1") {
			foundContent = true
			break
		}
	}
	if !foundContent {
		t.Errorf("Content should contain 'Line 1' after verdict, output:\n%s", output)
	}
}

func TestTUIRenderReviewViewAddressedWithoutVerdict(t *testing.T) {
	// Test that [ADDRESSED] appears on line 2 when no verdict is present
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:        10,
		Output:    "Line 1\n\nLine 2\n\nLine 3",
		Addressed: true,
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234",
			RepoName: "myrepo",
			Agent:    "codex",
			Verdict:  nil, // No verdict
		},
	}

	output := m.View()
	lines := strings.Split(output, "\n")

	// Require at least 4 lines: title, location, addressed, content
	if len(lines) < 4 {
		t.Fatalf("Expected at least 4 lines, got %d:\n%s", len(lines), output)
	}

	// Line 0: Title
	if !strings.Contains(lines[0], "Review") {
		t.Errorf("Line 0 should contain 'Review', got: %s", lines[0])
	}

	// Line 1: Location (ref)
	if !strings.Contains(lines[1], "abc1234") {
		t.Errorf("Line 1 should contain ref 'abc1234', got: %s", lines[1])
	}

	// Line 2: [ADDRESSED] (no verdict)
	if !strings.Contains(lines[2], "[ADDRESSED]") {
		t.Errorf("Line 2 should contain '[ADDRESSED]', got: %s", lines[2])
	}
	if strings.Contains(lines[2], "Verdict") {
		t.Errorf("Line 2 should not contain 'Verdict' when no verdict is set, got: %s", lines[2])
	}

	// Content should appear after addressed line
	foundContent := false
	for _, line := range lines[3:] {
		if strings.Contains(stripANSI(line), "Line 1") {
			foundContent = true
			break
		}
	}
	if !foundContent {
		t.Errorf("Content should contain 'Line 1' after addressed line, output:\n%s", output)
	}
}

func TestTUIBranchClearedOnFailedJobNavigation(t *testing.T) {
	// Test that navigating from a successful review with branch to a failed job clears the branch
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
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
	if m2.currentView != tuiViewReview {
		t.Errorf("Expected to stay in review view, got %d", m2.currentView)
	}
	if m2.currentReview == nil || !strings.Contains(m2.currentReview.Output, "Job failed") {
		t.Error("Expected currentReview to show failed job error")
	}
}

func TestTUIBranchClearedOnFailedJobEnter(t *testing.T) {
	// Test that pressing Enter on a failed job clears the branch
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewQueue
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
	if m2.currentView != tuiViewReview {
		t.Errorf("Expected review view, got %d", m2.currentView)
	}
}

func TestTUIRenderFailedJobNoBranchShown(t *testing.T) {
	// Test that failed jobs don't show stale branch in rendered output
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewReview
	m.currentBranch = "" // Should be cleared
	m.currentReview = &storage.Review{
		Agent:  "codex",
		Output: "Job failed:\n\nsome error",
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234",
			RepoName: "myrepo",
			Agent:    "codex",
			Status:   storage.JobStatusFailed,
		},
	}

	output := m.View()

	// Should NOT contain "on " when branch is cleared
	if strings.Contains(output, " on ") {
		t.Error("Failed job should not show branch in output")
	}
}

func TestTUIRenderQueueViewBranchFilterOnlyNoPanic(t *testing.T) {
	// Test that renderQueueView doesn't panic when branch filter is active
	// but repo filter is empty (regression test for index out of range)
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	m.currentView = tuiViewQueue
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

func TestTUIVisibleLinesCalculationNoVerdict(t *testing.T) {
	// Test that visibleLines = height - 4 when no verdict (title + location + status + help)
	// Help text is ~106 chars, so use width >= 110 to avoid wrapping
	m := newTuiModel("http://localhost")
	m.width = 120
	m.height = 10 // Small height to test calculation
	m.currentView = tuiViewReview
	// Create 20 lines of content to ensure scrolling.
	// Glamour with WithPreservedNewLines renders this as 21 lines
	// (1 leading blank + 20 content lines).
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\nL11\nL12\nL13\nL14\nL15\nL16\nL17\nL18\nL19\nL20",
		Job: &storage.ReviewJob{
			ID:      1,
			GitRef:  "abc1234",
			Agent:   "codex",
			Verdict: nil, // No verdict
		},
	}

	output := m.View()

	// With height=10, no verdict, wide terminal: visibleLines = 10 - 5 = 5
	// Non-content: title (1) + location (1) + status line (1) + help (2) = 5
	// Glamour produces 21 rendered lines; only 5 visible.
	visibleContentLines := 5
	totalRenderedLines := 21

	// Count content lines (glamour indents with spaces, so check trimmed)
	contentCount := 0
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(stripANSI(line))
		if len(trimmed) >= 2 && trimmed[0] == 'L' && trimmed[1] >= '0' && trimmed[1] <= '9' {
			contentCount++
		}
	}

	if contentCount == 0 {
		t.Error("Expected at least some content lines visible")
	}

	// Should show scroll indicator
	expected := fmt.Sprintf("[1-%d of %d lines]", visibleContentLines, totalRenderedLines)
	if !strings.Contains(output, expected) {
		t.Errorf("Expected scroll indicator '%s', output: %s", expected, output)
	}
}

func TestTUIVisibleLinesCalculationWithVerdict(t *testing.T) {
	// Test that visibleLines = height - 5 when verdict present (title + location + verdict + status + help)
	// Help text is ~106 chars, so use width >= 110 to avoid wrapping
	verdictPass := "P"
	m := newTuiModel("http://localhost")
	m.width = 120
	m.height = 10 // Small height to test calculation
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\nL11\nL12\nL13\nL14\nL15\nL16\nL17\nL18\nL19\nL20",
		Job: &storage.ReviewJob{
			ID:      1,
			GitRef:  "abc1234",
			Agent:   "codex",
			Verdict: &verdictPass,
		},
	}

	output := m.View()

	// With height=10, verdict, wide terminal: visibleLines = 10 - 6 = 4
	// Non-content: title (1) + location (1) + verdict (1) + status line (1) + help (2) = 6
	// Glamour produces 21 rendered lines.
	visibleContentLines := 4
	totalRenderedLines := 21

	expected := fmt.Sprintf("[1-%d of %d lines]", visibleContentLines, totalRenderedLines)
	if !strings.Contains(output, expected) {
		t.Errorf("Expected scroll indicator '%s', output: %s", expected, output)
	}
}

func TestTUIVisibleLinesCalculationNarrowTerminal(t *testing.T) {
	// Test that visibleLines accounts for help text wrapping at narrow terminals
	// Two help lines, each wraps to 2 lines at width=50
	m := newTuiModel("http://localhost")
	m.width = 50
	m.height = 10
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\nL11\nL12\nL13\nL14\nL15\nL16\nL17\nL18\nL19\nL20",
		Job: &storage.ReviewJob{
			ID:      1,
			GitRef:  "abc1234",
			Agent:   "codex",
			Verdict: nil, // No verdict
		},
	}

	output := m.View()

	// With height=10, no verdict, narrow terminal (each help line wraps to 2):
	// visibleLines = 10 - 7 = 3
	// Non-content: title (1) + location (1) + status line (1) + help (4) = 7
	// Glamour produces 21 rendered lines.
	visibleContentLines := 3
	totalRenderedLines := 21

	expected := fmt.Sprintf("[1-%d of %d lines]", visibleContentLines, totalRenderedLines)
	if !strings.Contains(output, expected) {
		t.Errorf("Expected scroll indicator '%s', output: %s", expected, output)
	}
}

func TestTUIVisibleLinesCalculationNarrowTerminalWithVerdict(t *testing.T) {
	// Test narrow terminal with verdict - validates extra header line branch
	// Two help lines, each wraps to 2 lines at width=50
	verdictFail := "F"
	m := newTuiModel("http://localhost")
	m.width = 50
	m.height = 10
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\nL11\nL12\nL13\nL14\nL15\nL16\nL17\nL18\nL19\nL20",
		Job: &storage.ReviewJob{
			ID:      1,
			GitRef:  "abc1234",
			Agent:   "codex",
			Verdict: &verdictFail,
		},
	}

	output := m.View()

	// With height=10, verdict present, narrow terminal (each help line wraps to 2):
	// visibleLines = 10 - 8 = 2
	// Non-content: title (1) + location (1) + verdict (1) + status line (1) + help (4) = 8
	// Glamour produces 21 rendered lines.
	visibleContentLines := 2
	totalRenderedLines := 21

	expected := fmt.Sprintf("[1-%d of %d lines]", visibleContentLines, totalRenderedLines)
	if !strings.Contains(output, expected) {
		t.Errorf("Expected scroll indicator '%s', output: %s", expected, output)
	}

	// Should show verdict
	if !strings.Contains(output, "Verdict") {
		t.Error("Expected output to contain verdict")
	}
}

func TestTUIVisibleLinesCalculationLongTitleWraps(t *testing.T) {
	// Test that long titles and location lines correctly wrap and reduce visible lines
	// New layout:
	// - Title: "Review #1 very-long-repository-name-here (claude-code)" = ~54 chars, ceil(54/50) = 2 lines
	// - Location line: "very-long-repository-name-here abc1234567890..de on feature/very-long-branch-name" = 81 chars, ceil(81/50) = 2 lines
	// - Addressed line: 1 line (since Addressed=true)
	// - Status line: 1 line
	// - Help: 2 lines, each wrapping to 2 = 4 lines
	// Non-content: 2 + 2 + 1 + 1 + 4 = 10
	// visibleLines = 12 - 10 = 2
	m := newTuiModel("http://localhost")
	m.width = 50
	m.height = 12
	m.currentView = tuiViewReview
	m.currentBranch = "feature/very-long-branch-name"

	m.currentReview = &storage.Review{
		ID:        10,
		Output:    "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\nL11\nL12\nL13\nL14\nL15\nL16\nL17\nL18\nL19\nL20",
		Addressed: true,
		Agent:     "claude-code",
		Job: &storage.ReviewJob{
			ID:       1,
			GitRef:   "abc1234567890..def5678901234", // Range ref (17 chars via shortRef)
			RepoName: "very-long-repository-name-here",
			Agent:    "claude-code",
			Verdict:  nil, // No verdict
		},
	}

	output := m.View()

	// visibleLines = 12 - 10 = 2
	// Glamour produces 21 rendered lines.
	visibleContentLines := 2
	totalRenderedLines := 21

	expected := fmt.Sprintf("[1-%d of %d lines]", visibleContentLines, totalRenderedLines)
	if !strings.Contains(output, expected) {
		t.Errorf("Expected scroll indicator '%s', output: %s", expected, output)
	}

	// Should contain the long repo name and branch
	if !strings.Contains(output, "very-long-repository-name-here") {
		t.Error("Expected output to contain long repo name")
	}
	if !strings.Contains(output, "feature/very-long-branch-name") {
		t.Error("Expected output to contain long branch name")
	}
	if !strings.Contains(output, "[ADDRESSED]") {
		t.Error("Expected output to contain [ADDRESSED]")
	}
}

func TestTUIReviewViewAddressedRollbackOnError(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with review view showing an unaddressed review
	m.currentView = tuiViewReview
	m.currentReview = makeReview(42, &storage.ReviewJob{ID: 100})

	// Simulate optimistic update (what happens when 'a' is pressed in review view)
	m.currentReview.Addressed = true
	m.pendingAddressed[100] = pendingState{newState: true, seq: 1} // Track pending state

	// Error result from server (reviewID must match currentReview.ID for rollback)
	errMsg := tuiAddressedResultMsg{
		reviewID:   42,  // Must match currentReview.ID
		jobID:      100, // Must match for isCurrentRequest check
		reviewView: true,
		oldState:   false, // Was false before optimistic update
		newState:   true,  // The requested state (matches pendingAddressed)
		seq:        1,     // Must match pending seq to be treated as current
		err:        fmt.Errorf("server error"),
	}

	m, _ = updateModel(t, m, errMsg)

	// Should have rolled back to false
	if m.currentReview.Addressed != false {
		t.Errorf("Expected currentReview.Addressed=false after rollback, got %v", m.currentReview.Addressed)
	}
	if m.err == nil {
		t.Error("Expected error to be set")
	}
}

func TestTUIReviewViewAddressedSuccessNoRollback(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with review view
	m.currentView = tuiViewReview
	m.currentReview = makeReview(42, &storage.ReviewJob{})

	// Simulate optimistic update
	m.currentReview.Addressed = true

	// Success result (err is nil)
	successMsg := tuiAddressedResultMsg{
		reviewView: true,
		oldState:   false,
		seq:        1, // Not strictly needed for success but included for consistency
		err:        nil,
	}

	m, _ = updateModel(t, m, successMsg)

	// Should stay true (no rollback on success)
	if m.currentReview.Addressed != true {
		t.Errorf("Expected currentReview.Addressed=true after success, got %v", m.currentReview.Addressed)
	}
	if m.err != nil {
		t.Errorf("Expected no error, got %v", m.err)
	}
}

func TestTUIReviewViewNavigateAwayBeforeError(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Setup: jobs in queue with addressed=false
	addrA := false
	addrB := false
	m.jobs = []storage.ReviewJob{
		{ID: 100, Status: storage.JobStatusDone, Addressed: &addrA}, // Job for review A
		{ID: 200, Status: storage.JobStatusDone, Addressed: &addrB}, // Job for review B
	}

	// User views review A, toggles addressed (optimistic update)
	m.currentView = tuiViewReview
	m.currentReview = makeReview(42, &storage.ReviewJob{ID: 100})
	m.currentReview.Addressed = true                               // Optimistic update to review
	*m.jobs[0].Addressed = true                                    // Optimistic update to job in queue
	m.pendingAddressed[100] = pendingState{newState: true, seq: 1} // Track pending state for job A

	// User navigates to review B before error response arrives
	m.currentReview = makeReview(99, &storage.ReviewJob{ID: 200})

	// Error arrives for review A's toggle
	errMsg := tuiAddressedResultMsg{
		reviewID:   42,  // Review A
		jobID:      100, // Job A
		reviewView: true,
		oldState:   false,
		newState:   true, // The requested state (matches pendingAddressed)
		seq:        1,    // Must match pending seq to be treated as current
		err:        fmt.Errorf("server error"),
	}

	m, _ = updateModel(t, m, errMsg)

	// Review B should be unchanged (still false)
	if m.currentReview.Addressed != false {
		t.Errorf("Review B should be unchanged, got Addressed=%v", m.currentReview.Addressed)
	}

	// Job A in queue should be rolled back to false
	if *m.jobs[0].Addressed != false {
		t.Errorf("Job A should be rolled back, got Addressed=%v", *m.jobs[0].Addressed)
	}

	// Job B in queue should be unchanged
	if *m.jobs[1].Addressed != false {
		t.Errorf("Job B should be unchanged, got Addressed=%v", *m.jobs[1].Addressed)
	}
}

func TestTUIReviewViewToggleSyncsQueueJob(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Setup: job in queue with addressed=false
	addr := false
	m.jobs = []storage.ReviewJob{
		{ID: 100, Status: storage.JobStatusDone, Addressed: &addr},
	}

	// User views review for job 100 and presses 'a'
	m.currentView = tuiViewReview
	m.currentReview = makeReview(42, &storage.ReviewJob{ID: 100})

	// Simulate the optimistic update that happens when 'a' is pressed
	oldState := m.currentReview.Addressed
	newState := !oldState
	m.currentReview.Addressed = newState
	m.setJobAddressed(100, newState)

	// Both should be updated
	if m.currentReview.Addressed != true {
		t.Errorf("Expected currentReview.Addressed=true, got %v", m.currentReview.Addressed)
	}
	if *m.jobs[0].Addressed != true {
		t.Errorf("Expected job.Addressed=true, got %v", *m.jobs[0].Addressed)
	}
}

func TestTUIReviewViewErrorWithoutJobID(t *testing.T) {
	// Test that review-view errors without jobID are still handled if
	// pendingReviewAddressed matches
	m := newTuiModel("http://localhost")

	// Review without an associated job (Job is nil)
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 42}

	// Simulate optimistic update (what happens when 'a' is pressed)
	m.currentReview.Addressed = true
	m.pendingReviewAddressed[42] = pendingState{newState: true, seq: 1} // Track pending state by review ID

	// Error arrives for this toggle (no jobID since Job was nil)
	errMsg := tuiAddressedResultMsg{
		reviewID:   42,
		jobID:      0, // No job
		reviewView: true,
		oldState:   false,
		newState:   true, // Matches pendingReviewAddressed
		seq:        1,    // Matches pending seq
		err:        fmt.Errorf("server error"),
	}

	m2, _ := updateModel(t, m, errMsg)

	// Should have rolled back to false
	if m2.currentReview.Addressed != false {
		t.Errorf("Expected currentReview.Addressed=false after rollback, got %v", m2.currentReview.Addressed)
	}

	// Error should be set
	if m2.err == nil {
		t.Error("Expected error to be set")
	}

	// pendingReviewAddressed should be cleared
	if _, ok := m2.pendingReviewAddressed[42]; ok {
		t.Error("pendingReviewAddressed should be cleared after error")
	}
}

func TestTUIReviewViewStaleErrorWithoutJobID(t *testing.T) {
	// Test that stale review-view errors without jobID are ignored
	m := newTuiModel("http://localhost")

	// Review without an associated job
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 42}

	// User toggled to true, then back to false
	// pendingReviewAddressed is now false (from the second toggle)
	m.currentReview.Addressed = false
	m.pendingReviewAddressed[42] = pendingState{newState: false, seq: 1}

	// A stale error arrives from the earlier toggle to true
	staleErrorMsg := tuiAddressedResultMsg{
		reviewID:   42,
		jobID:      0, // No job
		reviewView: true,
		oldState:   false, // What it was before the stale toggle
		newState:   true,  // Stale: pendingReviewAddressed is false, not true
		seq:        0,     // Stale: doesn't match pending seq (1)
		err:        fmt.Errorf("network error"),
	}

	m2, _ := updateModel(t, m, staleErrorMsg)

	// State should NOT be rolled back (stale error)
	if m2.currentReview.Addressed != false {
		t.Errorf("Expected addressed to remain false, got %v", m2.currentReview.Addressed)
	}

	// Error should NOT be set (stale error)
	if m2.err != nil {
		t.Error("Error should not be set for stale error response")
	}

	// pendingReviewAddressed should still be set (not cleared by stale response)
	if _, ok := m2.pendingReviewAddressed[42]; !ok {
		t.Error("pendingReviewAddressed should not be cleared by stale response")
	}
}

func TestTUIReviewViewSameStateLateError(t *testing.T) {
	// Test: true (seq 1) → false (seq 2) → true (seq 3), with late error from first true
	// The late error has newState=true which matches current pending newState,
	// but sequence numbers now distinguish same-state toggles.
	m := newTuiModel("http://localhost")

	// Review without an associated job
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 42}

	// Sequence: toggle true (seq 1) → toggle false (seq 2) → toggle true (seq 3)
	// After third toggle, state is true and pendingReviewAddressed has seq 3
	m.currentReview.Addressed = true
	m.pendingReviewAddressed[42] = pendingState{newState: true, seq: 3} // Third toggle

	// A late error arrives from the FIRST toggle (seq 1)
	// This error has newState=true which matches current pending newState,
	// but seq doesn't match, so it should be treated as stale and ignored.
	lateErrorMsg := tuiAddressedResultMsg{
		reviewID:   42,
		jobID:      0,
		reviewView: true,
		oldState:   false, // First toggle was from false to true
		newState:   true,  // Same newState as current pending...
		seq:        1,     // ...but different seq, so this is stale
		err:        fmt.Errorf("network error from first toggle"),
	}

	m2, _ := updateModel(t, m, lateErrorMsg)

	// With sequence numbers, the late error should be IGNORED (not rolled back)
	// because seq: 1 != pending seq: 3
	if m2.currentReview.Addressed != true {
		t.Errorf("Expected addressed to stay true (late error should be ignored), got %v", m2.currentReview.Addressed)
	}

	// Error should NOT be set (stale error)
	if m2.err != nil {
		t.Errorf("Error should not be set for stale error response, got %v", m2.err)
	}

	// pendingReviewAddressed should still be set (not cleared by stale response)
	if _, ok := m2.pendingReviewAddressed[42]; !ok {
		t.Error("pendingReviewAddressed should not be cleared by stale response")
	}
}

func TestTUIEscapeFromReviewTriggersRefreshWithHideAddressed(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = true
	m.loadingJobs = false

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1})

	// Press escape to return to queue view
	m2, cmd := pressSpecial(m, tea.KeyEscape)

	if m2.currentView != tuiViewQueue {
		t.Error("Expected to return to queue view")
	}
	if !m2.loadingJobs {
		t.Error("Expected loadingJobs to be true when escaping with hideAddressed active")
	}
	if cmd == nil {
		t.Error("Expected a command to be returned for refresh")
	}
}

func TestTUIEscapeFromReviewNoRefreshWithoutHideAddressed(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = false
	m.loadingJobs = false

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1})

	// Press escape to return to queue view
	m2, cmd := pressSpecial(m, tea.KeyEscape)

	if m2.currentView != tuiViewQueue {
		t.Error("Expected to return to queue view")
	}
	if m2.loadingJobs {
		t.Error("Should not trigger refresh when hideAddressed is not active")
	}
	if cmd != nil {
		t.Error("Should not return a command when hideAddressed is not active")
	}
}

func TestTUICommitMsgViewNavigationFromQueue(t *testing.T) {
	// Test that pressing escape in commit message view returns to the originating view (queue)
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{makeJob(1, withRef("abc123"))}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.commitMsgJobID = 1               // Set to match incoming message (normally set by 'm' key handler)
	m.commitMsgFromView = tuiViewQueue // Track where we came from

	// Simulate receiving commit message content (sets view to CommitMsg)
	m2, _ := updateModel(t, m, tuiCommitMsgMsg{jobID: 1, content: "test message"})

	if m2.currentView != tuiViewCommitMsg {
		t.Errorf("Expected tuiViewCommitMsg, got %d", m2.currentView)
	}

	// Press escape to go back
	m3, _ := pressSpecial(m2, tea.KeyEscape)

	if m3.currentView != tuiViewQueue {
		t.Errorf("Expected to return to tuiViewQueue, got %d", m3.currentView)
	}
	if m3.commitMsgContent != "" {
		t.Error("Expected commitMsgContent to be cleared")
	}
}

func TestTUICommitMsgViewNavigationFromReview(t *testing.T) {
	// Test that pressing escape in commit message view returns to the originating view (review)
	m := newTuiModel("http://localhost")
	j := makeJob(1, withRef("abc123"))
	m.jobs = []storage.ReviewJob{j}
	m.currentReview = makeReview(1, &j)
	m.currentView = tuiViewReview
	m.commitMsgFromView = tuiViewReview
	m.commitMsgContent = "test message"
	m.currentView = tuiViewCommitMsg

	// Press escape to go back
	m2, _ := pressSpecial(m, tea.KeyEscape)

	if m2.currentView != tuiViewReview {
		t.Errorf("Expected to return to tuiViewReview, got %d", m2.currentView)
	}
}

func TestTUICommitMsgViewNavigationWithQ(t *testing.T) {
	// Test that pressing 'q' in commit message view also returns to originating view
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewCommitMsg
	m.commitMsgFromView = tuiViewReview
	m.commitMsgContent = "test message"

	// Press 'q' to go back
	m2, _ := pressKey(m, 'q')

	if m2.currentView != tuiViewReview {
		t.Errorf("Expected to return to tuiViewReview after 'q', got %d", m2.currentView)
	}
}

func TestFetchCommitMsgJobTypeDetection(t *testing.T) {
	// Test that fetchCommitMsg correctly identifies job types and returns appropriate errors
	// This is critical: Prompt field is populated for ALL jobs (stores review prompt),
	// so we must use IsTaskJob() to identify task jobs, not Prompt != ""

	m := newTuiModel("http://localhost")

	tests := []struct {
		name        string
		job         storage.ReviewJob
		expectError string // empty means no early error (will try git lookup)
	}{
		{
			name: "regular commit with Prompt populated should not error early",
			job: storage.ReviewJob{
				ID:       1,
				JobType:  storage.JobTypeReview,
				GitRef:   "abc123def456",               // valid commit SHA
				Prompt:   "You are a code reviewer...", // review prompt is stored for all jobs
				CommitID: func() *int64 { id := int64(123); return &id }(),
			},
			expectError: "", // should attempt git lookup, not return "task jobs" error
		},
		{
			name: "run task (GitRef=prompt) should error",
			job: storage.ReviewJob{
				ID:      2,
				JobType: storage.JobTypeTask,
				GitRef:  "prompt",
				Prompt:  "Explain this codebase",
			},
			expectError: "no commit message for task jobs",
		},
		{
			name: "run task (GitRef=run) should error",
			job: storage.ReviewJob{
				ID:      8,
				JobType: storage.JobTypeTask,
				GitRef:  "run",
				Prompt:  "Do something",
			},
			expectError: "no commit message for task jobs",
		},
		{
			name: "analyze task should error",
			job: storage.ReviewJob{
				ID:      9,
				JobType: storage.JobTypeTask,
				GitRef:  "analyze",
				Prompt:  "Analyze these files",
			},
			expectError: "no commit message for task jobs",
		},
		{
			name: "custom label task should error",
			job: storage.ReviewJob{
				ID:      10,
				JobType: storage.JobTypeTask,
				GitRef:  "my-custom-task",
				Prompt:  "Do my custom task",
			},
			expectError: "no commit message for task jobs",
		},
		{
			name: "dirty job (JobType=dirty) should error",
			job: storage.ReviewJob{
				ID:      3,
				JobType: storage.JobTypeDirty,
				GitRef:  "dirty",
			},
			expectError: "no commit message for uncommitted changes",
		},
		{
			name: "dirty job with DiffContent should error",
			job: storage.ReviewJob{
				ID:          4,
				JobType:     storage.JobTypeDirty,
				GitRef:      "some-ref",
				DiffContent: func() *string { s := "diff content"; return &s }(),
			},
			expectError: "no commit message for uncommitted changes",
		},
		{
			name: "empty GitRef should error with missing ref message",
			job: storage.ReviewJob{
				ID:     5,
				GitRef: "",
			},
			expectError: "no git reference available for this job",
		},
		{
			name: "empty GitRef with Prompt (backward compat run job) should error with missing ref",
			job: storage.ReviewJob{
				ID:     6,
				GitRef: "",
				Prompt: "Explain this codebase", // older run job without GitRef=prompt
			},
			expectError: "no git reference available for this job",
		},
		{
			name: "dirty job with nil DiffContent but JobType=dirty should error",
			job: storage.ReviewJob{
				ID:          7,
				JobType:     storage.JobTypeDirty,
				GitRef:      "dirty",
				DiffContent: nil,
			},
			expectError: "no commit message for uncommitted changes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := m.fetchCommitMsg(&tt.job)
			msg := cmd()

			result, ok := msg.(tuiCommitMsgMsg)
			if !ok {
				t.Fatalf("Expected tuiCommitMsgMsg, got %T", msg)
			}

			if tt.expectError != "" {
				if result.err == nil {
					t.Errorf("Expected error %q, got nil", tt.expectError)
				} else if result.err.Error() != tt.expectError {
					t.Errorf("Expected error %q, got %q", tt.expectError, result.err.Error())
				}
			} else {
				// For valid commits, we expect a git error (repo doesn't exist in test)
				// but NOT the "task jobs" or "uncommitted changes" error
				if result.err != nil {
					errMsg := result.err.Error()
					if errMsg == "no commit message for task jobs" {
						t.Errorf("Regular commit with Prompt should not be detected as task job")
					}
					if errMsg == "no commit message for uncommitted changes" {
						t.Errorf("Regular commit should not be detected as uncommitted changes")
					}
					// Other errors (like git errors) are expected in test environment
				}
			}
		})
	}
}

func TestTUIHelpViewToggleFromQueue(t *testing.T) {
	// Test that '?' opens help from queue and pressing '?' again returns to queue
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue

	// Press '?' to open help
	m2, _ := pressKey(m, '?')

	if m2.currentView != tuiViewHelp {
		t.Errorf("Expected tuiViewHelp, got %d", m2.currentView)
	}
	if m2.helpFromView != tuiViewQueue {
		t.Errorf("Expected helpFromView to be tuiViewQueue, got %d", m2.helpFromView)
	}

	// Press '?' again to close help
	m3, _ := pressKey(m2, '?')

	if m3.currentView != tuiViewQueue {
		t.Errorf("Expected to return to tuiViewQueue, got %d", m3.currentView)
	}
}

func TestTUIHelpViewToggleFromReview(t *testing.T) {
	// Test that '?' opens help from review and escape returns to review
	m := newTuiModel("http://localhost")
	j := makeJob(1, withRef("abc123"))
	m.currentReview = makeReview(1, &j)
	m.currentView = tuiViewReview

	// Press '?' to open help
	m2, _ := pressKey(m, '?')

	if m2.currentView != tuiViewHelp {
		t.Errorf("Expected tuiViewHelp, got %d", m2.currentView)
	}
	if m2.helpFromView != tuiViewReview {
		t.Errorf("Expected helpFromView to be tuiViewReview, got %d", m2.helpFromView)
	}

	// Press escape to close help
	m3, _ := pressSpecial(m2, tea.KeyEscape)

	if m3.currentView != tuiViewReview {
		t.Errorf("Expected to return to tuiViewReview, got %d", m3.currentView)
	}
}

// mockClipboard implements ClipboardWriter for testing
type mockClipboard struct {
	lastText string
	err      error
}

func (m *mockClipboard) WriteText(text string) error {
	if m.err != nil {
		return m.err
	}
	m.lastText = text
	return nil
}

func TestTUIYankCopyFromReviewView(t *testing.T) {
	mock := &mockClipboard{}

	m := newTuiModel("http://localhost")
	m.clipboard = mock
	m.currentView = tuiViewReview
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("This is the review content to copy"))

	// Press 'y' to yank/copy
	m, cmd := pressKey(m, 'y')

	// Should return a command to copy to clipboard
	if cmd == nil {
		t.Fatal("Expected a command to be returned")
	}

	// Execute the command to get the result
	msg := cmd()
	result, ok := msg.(tuiClipboardResultMsg)
	if !ok {
		t.Fatalf("Expected tuiClipboardResultMsg, got %T", msg)
	}

	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}

	expectedContent := "Review #1\n\nThis is the review content to copy"
	if mock.lastText != expectedContent {
		t.Errorf("Expected clipboard to contain review with header, got %q", mock.lastText)
	}
}

func TestTUIYankCopyShowsFlashMessage(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("Review content"))
	m.width = 80
	m.height = 24

	// Simulate receiving a successful clipboard result (view captured at trigger time)
	m, _ = updateModel(t, m, tuiClipboardResultMsg{err: nil, view: tuiViewReview})

	if m.flashMessage != "Copied to clipboard" {
		t.Errorf("Expected flash message 'Copied to clipboard', got %q", m.flashMessage)
	}

	if m.flashExpiresAt.IsZero() {
		t.Error("Expected flashExpiresAt to be set")
	}

	if m.flashView != tuiViewReview {
		t.Errorf("Expected flashView to be tuiViewReview, got %v", m.flashView)
	}

	// Verify flash message appears in the rendered output
	output := m.renderReviewView()
	if !strings.Contains(output, "Copied to clipboard") {
		t.Error("Expected flash message to appear in rendered output")
	}
}

func TestTUIYankCopyShowsErrorOnFailure(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue

	// Simulate receiving a failed clipboard result
	m, _ = updateModel(t, m, tuiClipboardResultMsg{err: fmt.Errorf("clipboard not available"), view: tuiViewQueue})

	if m.err == nil {
		t.Error("Expected error to be set")
	}

	if !strings.Contains(m.err.Error(), "copy failed") {
		t.Errorf("Expected error to contain 'copy failed', got %q", m.err.Error())
	}
}

func TestTUIYankFlashViewNotAffectedByViewChange(t *testing.T) {
	// Test that flash message is attributed to the view where copy was triggered,
	// even if the user switches views before the clipboard result arrives.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.width = 80
	m.height = 24
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("Review content"))

	// User switches to review view before clipboard result arrives
	m.currentView = tuiViewReview

	// Clipboard result arrives with view captured at trigger time (queue)
	m, _ = updateModel(t, m, tuiClipboardResultMsg{err: nil, view: tuiViewQueue})

	// Flash should be attributed to queue view, not current (review) view
	if m.flashView != tuiViewQueue {
		t.Errorf("Expected flashView to be tuiViewQueue (trigger view), got %v", m.flashView)
	}

	// Flash should NOT appear in review view since it was triggered in queue
	output := m.renderReviewView()
	if strings.Contains(output, "Copied to clipboard") {
		t.Error("Flash message should not appear in review view when triggered from queue")
	}
}

func TestTUIYankFromQueueRequiresCompletedJob(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc123"), withAgent("test"), withStatus(storage.JobStatusRunning)),
		makeJob(2, withRef("def456"), withAgent("test")),
	}
	m.selectedIdx = 0

	// Press 'y' on running job - should not copy
	_, cmd := pressKey(m, 'y')
	if cmd != nil {
		t.Error("Expected no command for running job")
	}

	// Select completed job
	m.selectedIdx = 1
	_, cmd = pressKey(m, 'y')
	if cmd == nil {
		t.Error("Expected command for completed job")
	}
}

func TestTUIFetchReviewAndCopySuccess(t *testing.T) {
	mock := &mockClipboard{}

	// Create test server that returns a review
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review" {
			t.Errorf("Expected /api/review, got %s", r.URL.Path)
		}
		jobID := r.URL.Query().Get("job_id")
		if jobID != "123" {
			t.Errorf("Expected job_id=123, got %s", jobID)
		}
		review := storage.Review{
			ID:     1,
			JobID:  123,
			Agent:  "test",
			Output: "Review content for clipboard",
		}
		json.NewEncoder(w).Encode(review)
	})
	m.clipboard = mock

	// Execute fetchReviewAndCopy
	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(tuiClipboardResultMsg)
	if !ok {
		t.Fatalf("Expected tuiClipboardResultMsg, got %T", msg)
	}

	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}

	// Clipboard should contain header with JobID + review content
	expectedContent := "Review #123\n\nReview content for clipboard"
	if mock.lastText != expectedContent {
		t.Errorf("Expected clipboard to contain review with header, got %q", mock.lastText)
	}
}

func TestTUIFetchReviewAndCopy404(t *testing.T) {
	// Create test server that returns 404
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(tuiClipboardResultMsg)
	if !ok {
		t.Fatalf("Expected tuiClipboardResultMsg, got %T", msg)
	}

	if result.err == nil {
		t.Error("Expected error for 404 response")
	}

	if !strings.Contains(result.err.Error(), "no review found") {
		t.Errorf("Expected 'no review found' error, got %q", result.err.Error())
	}
}

func TestTUIFetchReviewAndCopyEmptyOutput(t *testing.T) {
	// Create test server that returns a review with empty output
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		review := storage.Review{
			ID:     1,
			JobID:  123,
			Agent:  "test",
			Output: "", // Empty output
		}
		json.NewEncoder(w).Encode(review)
	})

	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(tuiClipboardResultMsg)
	if !ok {
		t.Fatalf("Expected tuiClipboardResultMsg, got %T", msg)
	}

	if result.err == nil {
		t.Error("Expected error for empty output")
	}

	if !strings.Contains(result.err.Error(), "review has no content") {
		t.Errorf("Expected 'review has no content' error, got %q", result.err.Error())
	}
}

func TestTUIClipboardWriteFailurePropagates(t *testing.T) {
	mock := &mockClipboard{err: fmt.Errorf("clipboard unavailable: xclip not found")}

	m := newTuiModel("http://localhost")
	m.clipboard = mock
	m.currentView = tuiViewReview
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("Review content"))

	// Press 'y' to copy
	_, cmd := pressKey(m, 'y')
	if cmd == nil {
		t.Fatal("Expected command to be returned")
	}

	// Execute the command
	msg := cmd()
	result, ok := msg.(tuiClipboardResultMsg)
	if !ok {
		t.Fatalf("Expected tuiClipboardResultMsg, got %T", msg)
	}

	// Error should propagate
	if result.err == nil {
		t.Error("Expected clipboard write error to propagate")
	}

	if !strings.Contains(result.err.Error(), "clipboard unavailable") {
		t.Errorf("Expected clipboard error message, got %q", result.err.Error())
	}
}

func TestTUIFetchReviewAndCopyClipboardFailure(t *testing.T) {
	mock := &mockClipboard{err: fmt.Errorf("clipboard unavailable: pbcopy not found")}

	// Create test server that returns a valid review
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		review := storage.Review{
			ID:     1,
			JobID:  123,
			Agent:  "test",
			Output: "Review content",
		}
		json.NewEncoder(w).Encode(review)
	})
	m.clipboard = mock

	// Fetch succeeds but clipboard write fails
	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(tuiClipboardResultMsg)
	if !ok {
		t.Fatalf("Expected tuiClipboardResultMsg, got %T", msg)
	}

	if result.err == nil {
		t.Error("Expected clipboard write error after successful fetch")
	}

	if !strings.Contains(result.err.Error(), "clipboard unavailable") {
		t.Errorf("Expected clipboard error message, got %q", result.err.Error())
	}
}

func TestTUIFetchReviewAndCopyJobInjection(t *testing.T) {
	mock := &mockClipboard{}

	// Create test server that returns a review WITHOUT Job populated
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		review := storage.Review{
			ID:     42,
			JobID:  123,
			Agent:  "test",
			Output: "Review content",
			// Job is intentionally nil
		}
		json.NewEncoder(w).Encode(review)
	})
	m.clipboard = mock

	// Pass a job parameter - this should be injected when review.Job is nil
	j := makeJob(123, withRepoPath("/path/to/repo"), withRef("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"))

	cmd := m.fetchReviewAndCopy(123, &j)
	msg := cmd()

	result, ok := msg.(tuiClipboardResultMsg)
	if !ok {
		t.Fatalf("Expected tuiClipboardResultMsg, got %T", msg)
	}

	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}

	// Clipboard should contain header with injected job info (job ID, truncated SHA)
	expectedContent := "Review #123 /path/to/repo a1b2c3d\n\nReview content"
	if mock.lastText != expectedContent {
		t.Errorf("Expected clipboard with injected job info, got %q", mock.lastText)
	}
}

func TestFormatClipboardContent(t *testing.T) {
	tests := []struct {
		name     string
		review   *storage.Review
		expected string
	}{
		{
			name:     "nil review",
			review:   nil,
			expected: "",
		},
		{
			name: "empty output",
			review: &storage.Review{
				ID:     1,
				Output: "",
			},
			expected: "",
		},
		{
			name: "review with JobID only (no job struct)",
			review: &storage.Review{
				ID:     99, // review.ID is different from JobID
				JobID:  42,
				Output: "Content here",
			},
			expected: "Review #42\n\nContent here",
		},
		{
			name: "review with JobID 0 but review ID set (legacy fallback)",
			review: &storage.Review{
				ID:     77,
				JobID:  0,
				Output: "Content here",
			},
			expected: "Review #77\n\nContent here",
		},
		{
			name: "review with all IDs 0 and no job struct (no header)",
			review: &storage.Review{
				ID:     0,
				JobID:  0,
				Output: "Content here",
			},
			expected: "Content here",
		},
		{
			name: "review with job - full SHA truncated",
			review: &storage.Review{
				ID:     99,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       99,
					RepoPath: "/Users/test/myrepo",
					GitRef:   "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", // exactly 40 hex chars
				},
			},
			expected: "Review #99 /Users/test/myrepo a1b2c3d\n\nReview content",
		},
		{
			name: "long branch name not truncated",
			review: &storage.Review{
				ID:     101,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       101,
					RepoPath: "/repo",
					GitRef:   "feature/very-long-branch-name-that-exceeds-forty-characters",
				},
			},
			expected: "Review #101 /repo feature/very-long-branch-name-that-exceeds-forty-characters\n\nReview content",
		},
		{
			name: "review with job - range not truncated",
			review: &storage.Review{
				ID:     100,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       100,
					RepoPath: "/path/to/repo",
					GitRef:   "abc1234..def5678",
				},
			},
			expected: "Review #100 /path/to/repo abc1234..def5678\n\nReview content",
		},
		{
			name: "always uses job ID from Job struct",
			review: &storage.Review{
				ID:     999, // review.ID is ignored when Job is present with valid ID
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       555,
					RepoPath: "/repo/path",
					GitRef:   "abcdef1234567890abcdef1234567890abcdef12",
				},
			},
			expected: "Review #555 /repo/path abcdef1\n\nReview content",
		},
		{
			name: "Job present but Job.ID is 0 falls back to JobID with context",
			review: &storage.Review{
				ID:     999,
				JobID:  123,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       0, // zero ID, should fall back to JobID
					RepoPath: "/repo/path",
					GitRef:   "abc1234",
				},
			},
			expected: "Review #123 /repo/path abc1234\n\nReview content",
		},
		{
			name: "Job present but Job.ID is 0 falls back to review.ID with context",
			review: &storage.Review{
				ID:     999,
				JobID:  0,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       0, // zero ID, should fall back to review.ID
					RepoPath: "/repo/path",
					GitRef:   "abc1234",
				},
			},
			expected: "Review #999 /repo/path abc1234\n\nReview content",
		},
		{
			name: "short git ref not truncated",
			review: &storage.Review{
				ID:     10,
				Output: "Content",
				Job: &storage.ReviewJob{
					ID:       10,
					RepoPath: "/repo",
					GitRef:   "abc1234",
				},
			},
			expected: "Review #10 /repo abc1234\n\nContent",
		},
		{
			name: "uppercase SHA truncated",
			review: &storage.Review{
				ID:     102,
				Output: "Content",
				Job: &storage.ReviewJob{
					ID:       102,
					RepoPath: "/repo",
					GitRef:   "ABCDEF1234567890ABCDEF1234567890ABCDEF12", // uppercase 40 hex chars
				},
			},
			expected: "Review #102 /repo ABCDEF1\n\nContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatClipboardContent(tt.review)
			if got != tt.expected {
				t.Errorf("formatClipboardContent() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestTUILogOutputPreservesLinesOnEmptyResponse(t *testing.T) {
	// When a completed job's incremental poll returns no new data,
	// the TUI should preserve existing lines. In the incremental
	// flow, this is an append with no new lines (offset unchanged).
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logFetchSeq = 1
	m.height = 30

	// Set up initial lines as if we had been streaming output
	m.logLines = []logLine{
		{text: "Line 1"},
		{text: "Line 2"},
		{text: "Line 3"},
	}

	// Simulate job completion: incremental poll returns no new
	// lines (append mode, offset unchanged).
	emptyMsg := tuiLogOutputMsg{
		lines:   []logLine{},
		hasMore: false,
		err:     nil,
		append:  true,
		seq:     1,
	}

	m2, _ := updateModel(t, m, emptyMsg)

	// Lines should be preserved (not cleared)
	if len(m2.logLines) != 3 {
		t.Fatalf("Expected 3 lines preserved, got %d", len(m2.logLines))
	}

	// Streaming should stop
	if m2.logStreaming {
		t.Error("Expected logStreaming to be false after job completes")
	}

	// Verify the original content is still there
	if m2.logLines[0].text != "Line 1" {
		t.Errorf("Expected 'Line 1', got %q", m2.logLines[0].text)
	}
}

func TestTUILogOutputUpdatesLinesWhenStreaming(t *testing.T) {
	// Test that when streaming and new lines arrive, they are updated
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.height = 30

	// Set up initial lines
	m.logLines = []logLine{
		{text: "Old line"},
	}

	// New lines arrive while still streaming
	newMsg := tuiLogOutputMsg{
		lines: []logLine{
			{text: "Old line"},
			{text: "New line"},
		},
		hasMore: true, // Still streaming
		err:     nil,
	}

	m2, _ := updateModel(t, m, newMsg)

	// Lines should be updated
	if len(m2.logLines) != 2 {
		t.Errorf("Expected 2 lines, got %d", len(m2.logLines))
	}

	// Streaming should continue
	if !m2.logStreaming {
		t.Error("Expected logStreaming to be true while job is running")
	}
}

func TestTUILogOutputErrNoLogShowsJobError(t *testing.T) {
	// When a failed job has no log file, the flash message should
	// contain the stored error instead of a generic message.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 42
	m.logFromView = tuiViewQueue
	m.logStreaming = false
	m.height = 30
	m.jobs = []storage.ReviewJob{
		makeJob(42,
			withStatus(storage.JobStatusFailed),
			withError("agent timeout after 300s"),
		),
	}

	msg := tuiLogOutputMsg{err: errNoLog}
	m2, _ := updateModel(t, m, msg)

	// Should return to previous view.
	if m2.currentView != tuiViewQueue {
		t.Errorf("expected queue view, got %d", m2.currentView)
	}
	// Flash should contain the stored error.
	if !strings.Contains(m2.flashMessage, "agent timeout") {
		t.Errorf("flash should contain job error, got %q",
			m2.flashMessage)
	}
	if !strings.Contains(m2.flashMessage, "42") {
		t.Errorf("flash should contain job ID, got %q",
			m2.flashMessage)
	}
}

func TestTUILogOutputErrNoLogGenericForNonFailed(t *testing.T) {
	// For a done (non-failed) job with no log, the flash should
	// be the generic "No log available" message.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 10
	m.logFromView = tuiViewQueue
	m.height = 30
	m.jobs = []storage.ReviewJob{
		makeJob(10, withStatus(storage.JobStatusDone)),
	}

	msg := tuiLogOutputMsg{err: errNoLog}
	m2, _ := updateModel(t, m, msg)

	if m2.currentView != tuiViewQueue {
		t.Errorf("expected queue view, got %d", m2.currentView)
	}
	if m2.flashMessage != "No log available for this job" {
		t.Errorf("expected generic flash, got %q", m2.flashMessage)
	}
}

func TestTUILogOutputRunningJobKeepsWaiting(t *testing.T) {
	// Running job with empty first fetch should keep logLines nil
	// so the UI shows "Waiting for output..." instead of "(no output)".
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logLines = nil // first fetch
	m.height = 30

	// Empty lines, still streaming.
	msg := tuiLogOutputMsg{
		lines:   nil,
		hasMore: true,
	}
	m2, _ := updateModel(t, m, msg)

	if m2.logLines != nil {
		t.Errorf("logLines should stay nil for running empty fetch, got %v",
			m2.logLines)
	}
	if !m2.logStreaming {
		t.Error("logStreaming should remain true")
	}
}

func TestTUILogVisibleLinesWithCommandHeader(t *testing.T) {
	// logVisibleLines() should account for the command-line header
	// when the job has a known agent with a command line.
	m := newTuiModel("http://localhost")
	m.height = 30
	m.logJobID = 1

	// Job without agent (no command line header).
	m.jobs = []storage.ReviewJob{
		makeJob(1, withAgent("")),
	}
	noHeader := m.logVisibleLines()

	// Job with "test" agent (has a command line).
	m.jobs = []storage.ReviewJob{
		makeJob(1, withAgent("test")),
	}
	withHeader := m.logVisibleLines()

	// With command header should have one fewer visible line.
	if noHeader-withHeader != 1 {
		t.Errorf("command header should reduce visible lines by 1: "+
			"noHeader=%d withHeader=%d", noHeader, withHeader)
	}
}

func TestTUILogPagingUsesLogVisibleLines(t *testing.T) {
	// pgdown/end/g in log view should use logVisibleLines() for
	// scroll calculations, correctly accounting for headers.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.height = 20
	m.logScroll = 0
	m.logFollow = false

	// Use "test" agent to get command header.
	m.jobs = []storage.ReviewJob{
		makeJob(1, withAgent("test")),
	}

	// Create enough lines to need scrolling.
	for i := range 50 {
		m.logLines = append(m.logLines,
			logLine{text: fmt.Sprintf("line %d", i)})
	}

	visLines := m.logVisibleLines()

	// pgdown should advance by logVisibleLines().
	m2, _ := pressSpecial(m, tea.KeyPgDown)
	if m2.logScroll != visLines {
		t.Errorf("pgdown: expected scroll=%d, got %d",
			visLines, m2.logScroll)
	}

	// end should set scroll to max using logVisibleLines().
	m3, _ := pressSpecial(m, tea.KeyEnd)
	expectedMax := max(50-visLines, 0)
	if m3.logScroll != expectedMax {
		t.Errorf("end: expected scroll=%d, got %d",
			expectedMax, m3.logScroll)
	}

	// 'g' from top should jump to bottom using logVisibleLines().
	m4, _ := pressKeys(m, []rune{'g'})
	if m4.logScroll != expectedMax {
		t.Errorf("g from top: expected scroll=%d, got %d",
			expectedMax, m4.logScroll)
	}

	// pgup from mid-scroll should retreat by logVisibleLines().
	mMid := m
	mMid.logScroll = 2 * visLines // start 2 pages down
	m5, _ := pressSpecial(mMid, tea.KeyPgUp)
	if m5.logScroll != visLines {
		t.Errorf("pgup: expected scroll=%d, got %d",
			visLines, m5.logScroll)
	}

	// pgup at top should clamp to 0.
	m6, _ := pressSpecial(m, tea.KeyPgUp)
	if m6.logScroll != 0 {
		t.Errorf("pgup at top: expected scroll=0, got %d",
			m6.logScroll)
	}
}

func TestTUILogPagingNoHeader(t *testing.T) {
	// Same paging test but without command header (no agent).
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.height = 20
	m.logScroll = 0
	m.logFollow = false

	// No agent → no command header.
	m.jobs = []storage.ReviewJob{
		makeJob(1, withAgent("")),
	}

	for i := range 50 {
		m.logLines = append(m.logLines,
			logLine{text: fmt.Sprintf("line %d", i)})
	}

	visLines := m.logVisibleLines()
	expectedMax := max(50-visLines, 0)

	m2, _ := pressSpecial(m, tea.KeyPgDown)
	if m2.logScroll != visLines {
		t.Errorf("pgdown no-header: expected scroll=%d, got %d",
			visLines, m2.logScroll)
	}

	m3, _ := pressSpecial(m, tea.KeyEnd)
	if m3.logScroll != expectedMax {
		t.Errorf("end no-header: expected scroll=%d, got %d",
			expectedMax, m3.logScroll)
	}
}

func TestTUILogOutputIgnoredWhenNotInLogView(t *testing.T) {
	// Test that log output messages are ignored when not in log view.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue // Not in log view
	m.logJobID = 1

	// Existing lines from a previous log session.
	m.logLines = []logLine{
		{text: "Previous session line"},
	}

	// New lines arrive (stale message from previous log session).
	msg := tuiLogOutputMsg{
		lines: []logLine{
			{text: "Should be ignored"},
		},
		hasMore: false,
		err:     nil,
	}

	m2, _ := updateModel(t, m, msg)

	// Lines should not be updated since we're not in log view.
	if len(m2.logLines) != 1 {
		t.Fatalf("Expected 1 line (unchanged), got %d", len(m2.logLines))
	}
	if m2.logLines[0].text != "Previous session line" {
		t.Errorf("Lines should not be updated when not in log view")
	}
}

func TestTUILogOutputAppendMode(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.height = 30

	// Simulate first fetch (replace mode).
	m.logLines = []logLine{
		{text: "Line 1"},
		{text: "Line 2"},
	}
	m.logOffset = 100

	// Incremental append with new lines.
	msg := tuiLogOutputMsg{
		lines: []logLine{
			{text: "Line 3"},
			{text: "Line 4"},
		},
		hasMore:   true,
		newOffset: 200,
		append:    true,
	}

	m2, _ := updateModel(t, m, msg)

	if len(m2.logLines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(m2.logLines))
	}
	if m2.logLines[0].text != "Line 1" {
		t.Errorf("first line should be preserved")
	}
	if m2.logLines[2].text != "Line 3" {
		t.Errorf("appended line should be Line 3, got %q",
			m2.logLines[2].text)
	}
	if m2.logOffset != 200 {
		t.Errorf("logOffset = %d, want 200", m2.logOffset)
	}
}

func TestTUILogOutputAppendNoNewLines(t *testing.T) {
	// When append=true but no new lines, existing lines
	// should be preserved unchanged.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.height = 30
	m.logOffset = 100

	m.logLines = []logLine{
		{text: "Existing"},
	}

	msg := tuiLogOutputMsg{
		hasMore:   true,
		newOffset: 100,
		append:    true,
	}

	m2, _ := updateModel(t, m, msg)

	if len(m2.logLines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(m2.logLines))
	}
	if m2.logLines[0].text != "Existing" {
		t.Errorf("existing line should be preserved")
	}
}

func TestTUILogOutputReplaceMode(t *testing.T) {
	// When append=false, lines should be fully replaced.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = false
	m.height = 30

	m.logLines = []logLine{
		{text: "Old line 1"},
		{text: "Old line 2"},
	}

	msg := tuiLogOutputMsg{
		lines: []logLine{
			{text: "New line 1"},
		},
		hasMore:   false,
		newOffset: 50,
		append:    false,
	}

	m2, _ := updateModel(t, m, msg)

	if len(m2.logLines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(m2.logLines))
	}
	if m2.logLines[0].text != "New line 1" {
		t.Errorf("line should be replaced")
	}
	if m2.logOffset != 50 {
		t.Errorf("logOffset = %d, want 50", m2.logOffset)
	}
}

func TestTUILogOutputStaleSeqDropped(t *testing.T) {
	// Messages with a stale seq should be silently dropped.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logFetchSeq = 5
	m.logLoading = true // current-seq fetch is in-flight
	m.height = 30

	m.logLines = []logLine{{text: "Current"}}

	msg := tuiLogOutputMsg{
		lines:     []logLine{{text: "Stale data"}},
		hasMore:   true,
		newOffset: 999,
		append:    false,
		seq:       3, // older than m.logFetchSeq
	}

	m2, _ := updateModel(t, m, msg)

	// Lines and offset should be unchanged.
	if len(m2.logLines) != 1 || m2.logLines[0].text != "Current" {
		t.Errorf("stale msg should not update lines")
	}
	if m2.logOffset != 0 {
		t.Errorf(
			"stale msg should not update offset, got %d",
			m2.logOffset,
		)
	}
	// logLoading must remain true — stale responses must not
	// clear the in-flight guard for the current session.
	if !m2.logLoading {
		t.Error("stale msg should not clear logLoading")
	}
}

func TestTUILogOutputOffsetReset(t *testing.T) {
	// When server resets offset (newOffset < previous offset),
	// lines should be replaced, not appended.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logFetchSeq = 1
	m.logOffset = 500
	m.height = 30

	m.logLines = []logLine{
		{text: "Old line 1"},
		{text: "Old line 2"},
	}

	// Server reset: newOffset=100, append=false (reset path).
	msg := tuiLogOutputMsg{
		lines: []logLine{
			{text: "Reset line 1"},
		},
		hasMore:   true,
		newOffset: 100,
		append:    false,
		seq:       1,
	}

	m2, _ := updateModel(t, m, msg)

	if len(m2.logLines) != 1 {
		t.Fatalf("expected 1 line after reset, got %d",
			len(m2.logLines))
	}
	if m2.logLines[0].text != "Reset line 1" {
		t.Errorf("expected reset content, got %q",
			m2.logLines[0].text)
	}
	if m2.logOffset != 100 {
		t.Errorf("logOffset = %d, want 100", m2.logOffset)
	}
}

func TestTUILogLoadingGuard(t *testing.T) {
	// When logLoading is true, tuiLogTickMsg should not start
	// another fetch.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logLoading = true
	m.height = 30

	m2, cmd := updateModel(t, m, tuiLogTickMsg{})

	// Should not issue a fetch command.
	if cmd != nil {
		t.Errorf("expected nil cmd when logLoading, got %T", cmd)
	}
	if !m2.logLoading {
		t.Error("logLoading should remain true")
	}
}

func TestTUILogOutputReplaceModeEmptyClearsStale(t *testing.T) {
	// Replace mode with zero lines should clear stale content
	// (e.g. after log truncation/reset).
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logFetchSeq = 1
	m.height = 30

	m.logLines = []logLine{
		{text: "Stale line 1"},
		{text: "Stale line 2"},
	}

	msg := tuiLogOutputMsg{
		lines:     nil,
		hasMore:   false,
		newOffset: 0,
		append:    false,
		seq:       1,
	}

	m2, _ := updateModel(t, m, msg)

	if m2.logLines == nil {
		t.Fatal("logLines should be empty slice, not nil")
	}
	if len(m2.logLines) != 0 {
		t.Errorf(
			"expected 0 lines after empty replace, got %d",
			len(m2.logLines),
		)
	}
}

func TestTUILogOutputPersistsFormatter(t *testing.T) {
	// The formatter returned in tuiLogOutputMsg should be
	// persisted into m.logFmtr for incremental reuse.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logFetchSeq = 1
	m.height = 30

	fmtr := newStreamFormatter(nil, true)
	fmtr.hasOutput = true // simulate accumulated state

	msg := tuiLogOutputMsg{
		lines:     []logLine{{text: "Line 1"}},
		hasMore:   true,
		newOffset: 100,
		append:    false,
		seq:       1,
		fmtr:      fmtr,
	}

	m2, _ := updateModel(t, m, msg)

	if m2.logFmtr != fmtr {
		t.Error("logFmtr should be persisted from msg")
	}
	if !m2.logFmtr.hasOutput {
		t.Error("persisted formatter should retain state")
	}
}

func TestTUILogErrorDroppedOutsideLogView(t *testing.T) {
	// A late-arriving error from an in-flight log fetch should
	// not set m.err when the user has navigated away.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue // user navigated back
	m.logFetchSeq = 3
	m.logLoading = true

	msg := tuiLogOutputMsg{
		err: fmt.Errorf("connection reset"),
		seq: 3,
	}

	m2, _ := updateModel(t, m, msg)

	if m2.err != nil {
		t.Errorf("error leaked into non-log view: %v", m2.err)
	}
	if m2.logLoading {
		t.Error("logLoading should be cleared")
	}
}

func TestTUILogViewLookupFixJob(t *testing.T) {
	// renderLogView should find jobs in fixJobs when opened
	// from the tasks view.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 42
	m.logFromView = tuiViewTasks
	m.logStreaming = true
	m.height = 30
	m.width = 80

	m.fixJobs = []storage.ReviewJob{
		{
			ID:     42,
			Status: storage.JobStatusRunning,
			Agent:  "codex",
			GitRef: "abc1234",
		},
	}
	m.logLines = []logLine{{text: "output"}}

	view := m.View()
	if !strings.Contains(view, "#42") {
		t.Error("log view should show job ID from fixJobs")
	}
	if !strings.Contains(view, "codex") {
		t.Error("log view should show agent from fixJobs")
	}
}

func TestTUILogCancelFixJob(t *testing.T) {
	// Pressing 'x' in log view should cancel fix jobs.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 42
	m.logFromView = tuiViewTasks
	m.logStreaming = true
	m.height = 30

	m.fixJobs = []storage.ReviewJob{
		{
			ID:     42,
			Status: storage.JobStatusRunning,
			Agent:  "codex",
		},
	}

	m2, cmd := pressKey(m, 'x')

	if m2.logStreaming {
		t.Error("streaming should stop after cancel")
	}
	if m2.fixJobs[0].Status != storage.JobStatusCanceled {
		t.Errorf(
			"fix job status should be canceled, got %s",
			m2.fixJobs[0].Status,
		)
	}
	if cmd == nil {
		t.Error("expected cancel command")
	}
}

func TestTUILogVisibleLinesFixJob(t *testing.T) {
	// logVisibleLines must account for the command-line header
	// when viewing a fix job (from m.fixJobs, not m.jobs).
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 42
	m.logFromView = tuiViewTasks
	m.height = 30

	// Fix job with agent "test" produces a non-empty command line.
	m.fixJobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusRunning, Agent: "test"},
	}
	// m.jobs is empty — job only exists in fixJobs.
	m.jobs = nil

	visWithCmd := m.logVisibleLines()

	// Rendered view should also show the Command: header.
	m.logLines = []logLine{{text: "output"}}
	m.width = 80
	view := m.View()
	hasCmd := strings.Contains(view, "Command:")

	if !hasCmd {
		t.Error("expected Command: header in rendered view")
	}

	// Remove the fix job so there's no command line.
	m.fixJobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusRunning},
	}
	visWithout := m.logVisibleLines()

	// With the command header, one fewer visible line.
	if visWithCmd != visWithout-1 {
		t.Errorf(
			"logVisibleLines mismatch: with cmd=%d, without=%d "+
				"(expected difference of 1)",
			visWithCmd, visWithout,
		)
	}
}

func TestTUILogNavFromTasks(t *testing.T) {
	// Left/right in log view opened from tasks should navigate
	// through fixJobs, not m.jobs.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewLog
	m.logJobID = 20
	m.logFromView = tuiViewTasks
	m.logStreaming = false
	m.height = 30
	m.fixSelectedIdx = 1

	m.fixJobs = []storage.ReviewJob{
		{ID: 10, Status: storage.JobStatusDone},
		{ID: 20, Status: storage.JobStatusRunning},
		{ID: 30, Status: storage.JobStatusFailed},
	}

	// Should NOT navigate into m.jobs
	m.jobs = []storage.ReviewJob{
		{ID: 100, Status: storage.JobStatusDone},
		{ID: 200, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0

	// Right arrow → next fix job (ID 30)
	m2, cmd := pressSpecial(m, tea.KeyRight)
	if cmd == nil {
		t.Fatal("expected command from right arrow nav")
	}
	if m2.fixSelectedIdx != 2 {
		t.Errorf(
			"fixSelectedIdx should be 2, got %d",
			m2.fixSelectedIdx,
		)
	}
	// selectedIdx should not have changed
	if m2.selectedIdx != 0 {
		t.Errorf(
			"selectedIdx should remain 0, got %d",
			m2.selectedIdx,
		)
	}

	// Left arrow from index 1 → prev fix job (ID 10)
	m3, cmd := pressSpecial(m, tea.KeyLeft)
	if cmd == nil {
		t.Fatal("expected command from left arrow nav")
	}
	if m3.fixSelectedIdx != 0 {
		t.Errorf(
			"fixSelectedIdx should be 0, got %d",
			m3.fixSelectedIdx,
		)
	}
}

// Branch filter tests

func TestReviewFixPanelOpenFromReview(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	done := storage.JobStatusDone
	job := storage.ReviewJob{ID: 1, Status: done}
	m.currentReview = &storage.Review{JobID: 1, Job: &job}
	m.jobs = []storage.ReviewJob{job}
	m.selectedIdx = 0
	m.selectedJobID = 1

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	got := m2.(tuiModel)

	if !got.reviewFixPanelOpen {
		t.Error("Expected reviewFixPanelOpen to be true")
	}
	if !got.reviewFixPanelFocused {
		t.Error("Expected reviewFixPanelFocused to be true")
	}
	if got.fixPromptJobID != 1 {
		t.Errorf("Expected fixPromptJobID=1, got %d", got.fixPromptJobID)
	}
	if got.currentView != tuiViewReview {
		t.Errorf("Expected to stay in tuiViewReview, got %v", got.currentView)
	}
}

func TestReviewFixPanelTabTogglesReviewFocus(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = true

	// Tab shifts focus to review
	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyTab})
	got := m2.(tuiModel)
	if got.reviewFixPanelFocused {
		t.Error("Expected reviewFixPanelFocused to be false after Tab")
	}

	// Tab again shifts focus back to fix panel
	m3, _ := got.handleKeyMsg(tea.KeyMsg{Type: tea.KeyTab})
	got2 := m3.(tuiModel)
	if !got2.reviewFixPanelFocused {
		t.Error("Expected reviewFixPanelFocused to be true after second Tab")
	}
}

func TestReviewFixPanelTextInput(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = true

	for _, ch := range "hello" {
		m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = m2.(tuiModel)
	}

	if m.fixPromptText != "hello" {
		t.Errorf("Expected fixPromptText='hello', got %q", m.fixPromptText)
	}
}

func TestReviewFixPanelTextNotCapturedWhenUnfocused(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = false // review has focus

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	got := m2.(tuiModel)
	if got.fixPromptText != "" {
		t.Errorf("Expected fixPromptText to remain empty, got %q", got.fixPromptText)
	}
}

func TestReviewFixPanelEscWhenFocusedClosesPanel(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = true
	m.fixPromptText = "some text"

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	got := m2.(tuiModel)
	if got.reviewFixPanelOpen {
		t.Error("Expected panel to close on Esc when focused")
	}
	if got.fixPromptText != "" {
		t.Error("Expected fixPromptText to be cleared on Esc")
	}
	if got.currentView != tuiViewReview {
		t.Errorf("Expected to stay in tuiViewReview, got %v", got.currentView)
	}
}

func TestReviewFixPanelEscWhenUnfocusedClosesPanel(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = false // review has focus
	done := storage.JobStatusDone
	m.currentReview = &storage.Review{Job: &storage.ReviewJob{Status: done}}
	m.reviewFromView = tuiViewQueue

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	got := m2.(tuiModel)
	if got.reviewFixPanelOpen {
		t.Error("Expected panel to close on Esc when unfocused")
	}
	// Should stay in review view (not navigate back to queue)
	if got.currentView != tuiViewReview {
		t.Errorf("Expected to stay in tuiViewReview, got %v", got.currentView)
	}
}

func TestReviewFixPanelPendingConsumedOnLoad(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.reviewFixPanelPending = true
	m.fixPromptJobID = 5
	m.selectedJobID = 5

	review := &storage.Review{ID: 1, JobID: 5}
	msg := tuiReviewMsg{review: review, jobID: 5}
	m2, _ := m.Update(msg)
	got := m2.(tuiModel)

	if got.reviewFixPanelPending {
		t.Error("Expected reviewFixPanelPending to be cleared")
	}
	if !got.reviewFixPanelOpen {
		t.Error("Expected reviewFixPanelOpen to be true")
	}
	if !got.reviewFixPanelFocused {
		t.Error("Expected reviewFixPanelFocused to be true")
	}
}

func TestReviewFixPanelEnterSubmitsAndNavigatesToTasks(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(storage.ReviewJob{ID: 1})
	})
	m.currentView = tuiViewReview
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = true
	m.fixPromptJobID = 1
	m.fixPromptText = "fix the lint errors"

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	got := m2.(tuiModel)

	if got.reviewFixPanelOpen {
		t.Error("Expected panel to close on Enter")
	}
	if got.fixPromptText != "" {
		t.Error("Expected fixPromptText to be cleared on Enter")
	}
	if got.fixPromptJobID != 0 {
		t.Errorf("Expected fixPromptJobID to be cleared, got %d", got.fixPromptJobID)
	}
	if got.currentView != tuiViewTasks {
		t.Errorf("Expected navigation to tuiViewTasks, got %v", got.currentView)
	}
}

func TestReviewFixPanelBackspaceDeletesRune(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = true
	m.fixPromptText = "hello"

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyBackspace})
	got := m2.(tuiModel)

	if got.fixPromptText != "hell" {
		t.Errorf("Expected fixPromptText='hell' after backspace, got %q", got.fixPromptText)
	}
}

func TestFixKeyFromQueueFetchesReviewWithPendingFlag(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/review" {
			review := storage.ReviewJob{ID: 42, Status: storage.JobStatusDone}
			json.NewEncoder(w).Encode(storage.Review{ID: 1, JobID: 42, Job: &review})
			return
		}
		if r.URL.Path == "/api/comments" {
			json.NewEncoder(w).Encode(map[string]any{"responses": []storage.Response{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	done := storage.JobStatusDone
	job := storage.ReviewJob{ID: 42, Status: done}
	m.currentView = tuiViewQueue
	m.jobs = []storage.ReviewJob{job}
	m.selectedIdx = 0
	m.selectedJobID = 42

	m2, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	got := m2.(tuiModel)

	if !got.reviewFixPanelPending {
		t.Error("Expected reviewFixPanelPending to be true after F from queue")
	}
	if got.selectedJobID != 42 {
		t.Errorf("Expected selectedJobID=42, got %d", got.selectedJobID)
	}
	if cmd == nil {
		t.Error("Expected a fetch command to be returned")
	}
}

func TestFixPanelClosedOnReviewNavNext(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	done := storage.JobStatusDone
	job1 := storage.ReviewJob{ID: 1, Status: done}
	job2 := storage.ReviewJob{ID: 2, Status: done}
	m.currentReview = &storage.Review{JobID: 1, Job: &job1}
	m.jobs = []storage.ReviewJob{job1, job2}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Open fix panel for job 1
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = false
	m.fixPromptJobID = 1
	m.fixPromptText = "some instructions"

	// Navigate to next review (right/j)
	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	got := m2.(tuiModel)

	if got.reviewFixPanelOpen {
		t.Error("Expected fix panel to be closed after navigating to next review")
	}
	if got.fixPromptJobID != 0 {
		t.Errorf("Expected fixPromptJobID=0, got %d", got.fixPromptJobID)
	}
	if got.fixPromptText != "" {
		t.Errorf("Expected fixPromptText cleared, got %q", got.fixPromptText)
	}
}

func TestFixPanelClosedOnReviewNavPrev(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	done := storage.JobStatusDone
	job1 := storage.ReviewJob{ID: 1, Status: done}
	job2 := storage.ReviewJob{ID: 2, Status: done}
	m.currentReview = &storage.Review{JobID: 2, Job: &job2}
	m.jobs = []storage.ReviewJob{job1, job2}
	m.selectedIdx = 1
	m.selectedJobID = 2

	// Open fix panel for job 2
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = false
	m.fixPromptJobID = 2
	m.fixPromptText = "fix it"

	// Navigate to previous review (left/k)
	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	got := m2.(tuiModel)

	if got.reviewFixPanelOpen {
		t.Error("Expected fix panel to be closed after navigating to prev review")
	}
	if got.fixPromptJobID != 0 {
		t.Errorf("Expected fixPromptJobID=0, got %d", got.fixPromptJobID)
	}
}

func TestFixPanelClosedOnQuitFromReview(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	done := storage.JobStatusDone
	job := storage.ReviewJob{ID: 1, Status: done}
	m.currentReview = &storage.Review{JobID: 1, Job: &job}
	m.jobs = []storage.ReviewJob{job}
	m.selectedIdx = 0
	m.selectedJobID = 1

	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = false
	m.fixPromptJobID = 1
	m.fixPromptText = "instructions"

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := m2.(tuiModel)

	if got.reviewFixPanelOpen {
		t.Error("Expected fix panel to be closed after q from review")
	}
	if got.fixPromptJobID != 0 {
		t.Errorf("Expected fixPromptJobID=0, got %d", got.fixPromptJobID)
	}
	if got.currentView == tuiViewReview {
		t.Error("Expected to leave review view")
	}
}

func TestFixPanelPendingNotConsumedByWrongReview(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.reviewFixPanelPending = true
	m.fixPromptJobID = 5
	m.selectedJobID = 10

	// A review for job 10 loads, but pending was for job 5
	review := &storage.Review{ID: 2, JobID: 10}
	msg := tuiReviewMsg{review: review, jobID: 10}
	m2, _ := m.Update(msg)
	got := m2.(tuiModel)

	if got.reviewFixPanelOpen {
		t.Error("Panel should not open for a different job than pending")
	}
	// Pending should remain since it wasn't consumed
	if !got.reviewFixPanelPending {
		t.Error("Expected reviewFixPanelPending to remain true")
	}
}

func TestFixPanelPendingClearedOnStaleFetch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.reviewFixPanelPending = true
	m.fixPromptJobID = 5
	m.selectedJobID = 10 // User navigated away

	// Stale review for job 5 arrives after user moved to job 10
	review := &storage.Review{ID: 1, JobID: 5}
	msg := tuiReviewMsg{review: review, jobID: 5}
	m2, _ := m.Update(msg)
	got := m2.(tuiModel)

	if got.reviewFixPanelPending {
		t.Error("Stale fetch should clear pending flag")
	}
	if got.fixPromptJobID != 0 {
		t.Errorf("Expected fixPromptJobID=0 after stale clear, got %d", got.fixPromptJobID)
	}
	if got.reviewFixPanelOpen {
		t.Error("Panel should not open from stale fetch")
	}
}

func TestFixPanelClosedOnPromptKey(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	done := storage.JobStatusDone
	job := storage.ReviewJob{ID: 1, Status: done}
	m.currentReview = &storage.Review{
		JobID:  1,
		Job:    &job,
		Prompt: "review prompt text",
	}
	m.jobs = []storage.ReviewJob{job}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Open fix panel
	m.reviewFixPanelOpen = true
	m.reviewFixPanelFocused = false
	m.fixPromptJobID = 1
	m.fixPromptText = "fix instructions"

	// Press 'p' to switch to prompt view
	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	got := m2.(tuiModel)

	if got.currentView != tuiViewPrompt {
		t.Errorf("Expected tuiViewPrompt, got %v", got.currentView)
	}
	if got.reviewFixPanelOpen {
		t.Error("Expected fix panel to be closed when switching to prompt view")
	}
	if got.fixPromptJobID != 0 {
		t.Errorf("Expected fixPromptJobID=0, got %d", got.fixPromptJobID)
	}
}

func TestFixPanelPendingClearedOnEscFromReview(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	done := storage.JobStatusDone
	job := storage.ReviewJob{ID: 1, Status: done}
	m.currentReview = &storage.Review{JobID: 1, Job: &job}
	m.jobs = []storage.ReviewJob{job}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Pending fix panel (fetch in flight) but panel not yet open
	m.reviewFixPanelPending = true
	m.fixPromptJobID = 1

	m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	got := m2.(tuiModel)

	if got.currentView == tuiViewReview {
		t.Error("Expected to leave review view on Esc")
	}
	if got.reviewFixPanelPending {
		t.Error("Expected reviewFixPanelPending to be cleared on Esc exit")
	}
	if got.fixPromptJobID != 0 {
		t.Errorf("Expected fixPromptJobID=0, got %d", got.fixPromptJobID)
	}
}
