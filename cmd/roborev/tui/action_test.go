package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/go-cmp/cmp"
	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIAddressReviewSuccess(t *testing.T) {
	_, m := mockServerModel(t, expectJSONPost(t, "", addressRequest{JobID: 100, Addressed: true}, map[string]bool{"success": true}))
	cmd := m.addressReview(42, 100, true, false, 1) // reviewID=42, jobID=100, newState=true, oldState=false
	msg := cmd()

	result := assertMsgType[addressedResultMsg](t, msg)
	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}
	if !result.reviewView {
		t.Error("Expected reviewView to be true")
	}
	if result.jobID != 100 {
		t.Errorf("Expected jobID=100, got %d", result.jobID)
	}
}

func TestTUIAddressReviewNotFound(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.addressReview(999, 100, true, false, 1) // reviewID=999, jobID=100, newState=true, oldState=false
	msg := cmd()

	result := assertMsgType[addressedResultMsg](t, msg)
	if result.err == nil || result.err.Error() != "review not found" {
		t.Errorf("Expected 'review not found' error, got: %v", result.err)
	}
}

func TestTUIToggleAddressedForJobSuccess(t *testing.T) {
	_, m := mockServerModel(t, expectJSONPost(t, "/api/review/address", addressRequest{JobID: 1, Addressed: true}, map[string]bool{"success": true}))
	currentState := false
	cmd := m.toggleAddressedForJob(1, &currentState)
	msg := cmd()

	addressed := assertMsgType[addressedMsg](t, msg)
	if !bool(addressed) {
		t.Error("Expected toggled state to be true (was false)")
	}
}

func TestTUIToggleAddressedNoReview(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.toggleAddressedForJob(999, nil)
	msg := cmd()

	errMsg := assertMsgType[errMsg](t, msg)
	if errMsg.Error() != "no review for this job" {
		t.Errorf("Expected 'no review for this job', got: %v", errMsg)
	}
}

func TestTUIAddressFromReviewView_Navigation(t *testing.T) {
	cases := []struct {
		name         string
		initialIdx   int
		initialJobID int64
		actions      func(model) model
		expectedIdx  int
		expectedJob  int64
		expectedView viewKind
	}{
		{
			name:         "NextVisible",
			initialIdx:   1, // Viewing job 2
			initialJobID: 2,
			actions: func(m model) model {
				// Press 'a' to mark as addressed
				m2, _ := pressKey(m, 'a')
				// Selection stays at index 1 so left/right navigation works correctly from current position
				assertSelection(t, m2, 1, 2)
				assertView(t, m2, viewReview)

				// Press escape to return to queue
				m3, _ := pressSpecial(m2, tea.KeyEscape)
				return m3
			},
			expectedIdx:  2, // Moves to job 3
			expectedJob:  3,
			expectedView: viewQueue,
		},
		{
			name:         "FallbackPrev",
			initialIdx:   2, // Viewing job 3 (last)
			initialJobID: 3,
			actions: func(m model) model {
				m2, _ := pressKey(m, 'a')
				m3, _ := pressSpecial(m2, tea.KeyEscape)
				return m3
			},
			expectedIdx:  1, // Moves back to job 2
			expectedJob:  2,
			expectedView: viewQueue,
		},
		{
			name:         "ExitWithQ",
			initialIdx:   1, // Viewing job 2
			initialJobID: 2,
			actions: func(m model) model {
				m2, _ := pressKey(m, 'a')
				m3, _ := pressKey(m2, 'q')
				return m3
			},
			expectedIdx:  2, // Moves to job 3
			expectedJob:  3,
			expectedView: viewQueue,
		},
		{
			name:         "ExitWithCtrlC",
			initialIdx:   1, // Viewing job 2
			initialJobID: 2,
			actions: func(m model) model {
				m2, _ := pressKey(m, 'a')
				m3, _ := pressSpecial(m2, tea.KeyCtrlC)
				return m3
			},
			expectedIdx:  2, // Moves to job 3
			expectedJob:  3,
			expectedView: viewQueue,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobs := []storage.ReviewJob{
				makeJob(1, withAddressed(boolPtr(false))),
				makeJob(2, withAddressed(boolPtr(false))),
				makeJob(3, withAddressed(boolPtr(false))),
			}

			m := setupTestModel(jobs, func(m *model) {
				m.currentView = viewReview
				m.hideAddressed = true
				m.selectedIdx = tc.initialIdx
				m.selectedJobID = tc.initialJobID
				m.currentReview = makeReview(10, &m.jobs[tc.initialIdx])
			})

			m2 := tc.actions(m)

			assertSelection(t, m2, tc.expectedIdx, tc.expectedJob)
			assertView(t, m2, tc.expectedView)
		})
	}
}

// addressRequest is used to decode and validate POST body in tests
type addressRequest struct {
	JobID     int64 `json:"job_id"`
	Addressed bool  `json:"addressed"`
}

func TestTUIAddressReviewInBackgroundSuccess(t *testing.T) {
	_, m := mockServerModel(t, expectJSONPost(t, "/api/review/address", addressRequest{JobID: 42, Addressed: true}, map[string]bool{"success": true}))
	cmd := m.addressReviewInBackground(42, true, false, 1) // jobID=42, newState=true, oldState=false
	msg := cmd()

	result := assertMsgType[addressedResultMsg](t, msg)
	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}
	if result.jobID != 42 {
		t.Errorf("Expected jobID=42, got %d", result.jobID)
	}
	if result.oldState != false {
		t.Errorf("Expected oldState=false, got %v", result.oldState)
	}
	if result.reviewView {
		t.Error("Expected reviewView=false for queue view command")
	}
}

func TestTUIAddressReviewInBackgroundNotFound(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review/address" || r.Method != http.MethodPost {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.addressReviewInBackground(42, true, false, 1)
	msg := cmd()

	result := assertMsgType[addressedResultMsg](t, msg)
	if result.err == nil || !strings.Contains(result.err.Error(), "no review") {
		t.Errorf("Expected error containing 'no review', got: %v", result.err)
	}
	if result.jobID != 42 {
		t.Errorf("Expected jobID=42 for rollback, got %d", result.jobID)
	}
	if result.oldState != false {
		t.Errorf("Expected oldState=false for rollback, got %v", result.oldState)
	}
}

func TestTUIAddressReviewInBackgroundServerError(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review/address" || r.Method != http.MethodPost {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	cmd := m.addressReviewInBackground(42, true, false, 1)
	msg := cmd()

	result := assertMsgType[addressedResultMsg](t, msg)
	if result.err == nil {
		t.Error("Expected error for address 500 response")
	}
	if result.jobID != 42 {
		t.Errorf("Expected jobID=42 for rollback, got %d", result.jobID)
	}
	if result.oldState != false {
		t.Errorf("Expected oldState=false for rollback, got %v", result.oldState)
	}
}

func TestTUIAddressedRollbackOnError(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(42, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))),
	}, func(m *model) {
		m.selectedIdx = 0
		m.selectedJobID = 42
		m.jobStats = storage.JobStats{Done: 1, Addressed: 1, Unaddressed: 0}
	})

	// First, simulate the optimistic update (what happens when 'a' is pressed)
	*m.jobs[0].Addressed = true
	m.pendingAddressed[42] = pendingState{newState: true, seq: 1} // Track pending state

	// Simulate error result from background update
	// This would happen if server returned error after optimistic update
	errMsg := addressedResultMsg{
		jobID:    42,
		oldState: false, // Was false before optimistic update
		newState: true,  // The requested state (matches pendingAddressed)
		seq:      1,     // Must match pending seq to be treated as current
		err:      fmt.Errorf("server error"),
	}

	// Now handle the error result - should rollback
	m, _ = updateModel(t, m, errMsg)

	// Should have rolled back to false
	if m.jobs[0].Addressed == nil || *m.jobs[0].Addressed != false {
		t.Errorf("Expected addressed=false after rollback, got %v", m.jobs[0].Addressed)
	}
	if m.err == nil {
		t.Error("Expected error to be set")
	}
	// Stats should be rolled back too
	assertJobStats(t, m, 0, 1)
}

func TestTUIAddressedRollbackAfterPollRefresh(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(42, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))),
	}, func(m *model) {
		m.currentView = viewQueue
		m.selectedIdx = 0
		m.selectedJobID = 42
		m.jobStats = storage.JobStats{Done: 1, Addressed: 0, Unaddressed: 1}
		m.pendingAddressed = make(map[int64]pendingState)
	})

	// Step 1: optimistic toggle → addressed
	result, _ := m.handleAddressedKey()
	m = result.(model)
	assertJobStats(t, m, 1, 0)

	// Step 2: poll arrives with server truth (still unaddressed)
	pollMsg := jobsMsg{
		jobs: []storage.ReviewJob{
			makeJob(42, withStatus(storage.JobStatusDone),
				withAddressed(boolPtr(false))),
		},
		stats: storage.JobStats{Done: 1, Addressed: 0, Unaddressed: 1},
	}
	m, _ = updateModel(t, m, pollMsg)
	// Pending delta should be re-applied on top of server stats
	assertJobStats(t, m, 1, 0)

	// Step 3: error arrives → rollback
	errMsg := addressedResultMsg{
		jobID:    42,
		oldState: false,
		newState: true,
		seq:      1,
		err:      fmt.Errorf("server error"),
	}
	m, _ = updateModel(t, m, errMsg)
	assertJobStats(t, m, 0, 1)
}

func TestTUIAddressedPollConfirmsNoDoubleCount(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(42, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))),
	}, func(m *model) {
		m.currentView = viewQueue
		m.selectedIdx = 0
		m.selectedJobID = 42
		m.jobStats = storage.JobStats{Done: 1, Addressed: 0, Unaddressed: 1}
		m.pendingAddressed = make(map[int64]pendingState)
	})

	// Step 1: optimistic toggle → addressed
	result, _ := m.handleAddressedKey()
	m = result.(model)
	assertJobStats(t, m, 1, 0)

	// Step 2: poll arrives with server already reflecting the change
	pollMsg := jobsMsg{
		jobs: []storage.ReviewJob{
			makeJob(42, withStatus(storage.JobStatusDone),
				withAddressed(boolPtr(true))),
		},
		stats: storage.JobStats{Done: 1, Addressed: 1, Unaddressed: 0},
	}
	m, _ = updateModel(t, m, pollMsg)
	// Pending should be cleared (server confirmed), no double-counting
	assertJobStats(t, m, 1, 0)
}

func TestTUIAddressedSuccessNoRollback(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(42, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))),
	})

	// Simulate optimistic update
	*m.jobs[0].Addressed = true

	// Success result (err is nil)
	successMsg := addressedResultMsg{
		jobID:    42,
		oldState: false,
		seq:      1, // Not strictly needed for success (no rollback) but included for consistency
		err:      nil,
	}

	m, _ = updateModel(t, m, successMsg)

	// Should stay true (no rollback on success)
	if m.jobs[0].Addressed == nil || *m.jobs[0].Addressed != true {
		t.Errorf("Expected addressed=true after success, got %v", m.jobs[0].Addressed)
	}
	if m.err != nil {
		t.Errorf("Expected no error, got %v", m.err)
	}
}

func TestTUIAddressedToggleMovesSelectionWithHideActive(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
		makeJob(2, withAddressed(boolPtr(false))),
		makeJob(3, withAddressed(boolPtr(false))),
	}, func(m *model) {
		m.currentView = viewQueue
		m.hideAddressed = true
		m.selectedIdx = 1
		m.selectedJobID = 2
	})

	// Simulate marking job 2 as addressed

	m.jobs[1].Addressed = boolPtr(true)

	// Verify job 2 is now hidden
	if m.isJobVisible(m.jobs[1]) {
		t.Error("Job 2 should be hidden after marking as addressed")
	}

	// Simulate what happens in 'a' handler - selection should move
	// Since job 2 is now hidden, find next visible
	nextIdx := m.findNextVisibleJob(m.selectedIdx)
	if nextIdx != 2 {
		t.Errorf("Expected next visible job at index 2, got %d", nextIdx)
	}
}

func TestTUISetJobAddressedHelper(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())

	// Test with nil Addressed pointer - should allocate
	m.jobs = []storage.ReviewJob{
		makeJob(100),
	}

	m.setJobAddressed(100, true)

	if m.jobs[0].Addressed == nil {
		t.Fatal("Expected Addressed to be allocated")
	}
	if *m.jobs[0].Addressed != true {
		t.Errorf("Expected Addressed=true, got %v", *m.jobs[0].Addressed)
	}

	// Test toggle back
	m.setJobAddressed(100, false)
	if *m.jobs[0].Addressed != false {
		t.Errorf("Expected Addressed=false, got %v", *m.jobs[0].Addressed)
	}

	// Test with non-existent job ID - should be no-op
	m.setJobAddressed(999, true)
	if *m.jobs[0].Addressed != false {
		t.Errorf("Non-existent job should not affect existing job")
	}
}

func TestTUICancelJobSuccess(t *testing.T) {
	type cancelRequest struct {
		JobID int64 `json:"job_id"`
	}
	_, m := mockServerModel(t, expectJSONPost(t, "/api/job/cancel", cancelRequest{JobID: 42}, map[string]any{"success": true}))
	oldFinishedAt := time.Now().Add(-1 * time.Hour)
	cmd := m.cancelJob(42, storage.JobStatusRunning, &oldFinishedAt)
	msg := cmd()

	result := assertMsgType[cancelResultMsg](t, msg)
	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}
	if result.jobID != 42 {
		t.Errorf("Expected jobID=42, got %d", result.jobID)
	}
	if result.oldState != storage.JobStatusRunning {
		t.Errorf("Expected oldState=running, got %s", result.oldState)
	}
	if result.oldFinishedAt == nil || !result.oldFinishedAt.Equal(oldFinishedAt) {
		t.Errorf("Expected oldFinishedAt to be preserved")
	}
}

func TestTUICancelJobNotFound(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	})
	cmd := m.cancelJob(99, storage.JobStatusQueued, nil)
	msg := cmd()

	result := assertMsgType[cancelResultMsg](t, msg)
	if result.err == nil {
		t.Error("Expected error for 404, got nil")
	}
	if result.oldState != storage.JobStatusQueued {
		t.Errorf("Expected oldState=queued for rollback, got %s", result.oldState)
	}
	if result.oldFinishedAt != nil {
		t.Errorf("Expected oldFinishedAt=nil for queued job, got %v", result.oldFinishedAt)
	}
}

func TestTUICancelRollbackOnError(t *testing.T) {
	// Setup: running job with no FinishedAt (still running)
	startTime := time.Now().Add(-5 * time.Minute)
	m := setupTestModel([]storage.ReviewJob{
		makeJob(42, withStatus(storage.JobStatusRunning), withStartedAt(startTime), withFinishedAt(nil)),
	}, func(m *model) {
		m.selectedIdx = 0
		m.selectedJobID = 42
	})

	// Simulate the optimistic update that would have happened
	now := time.Now()
	m.jobs[0].Status = storage.JobStatusCanceled
	m.jobs[0].FinishedAt = &now

	// Simulate cancel error result - should rollback both status and FinishedAt
	errResult := cancelResultMsg{
		jobID:         42,
		oldState:      storage.JobStatusRunning,
		oldFinishedAt: nil, // Was nil before optimistic update
		err:           fmt.Errorf("server error"),
	}

	m2, _ := updateModel(t, m, errResult)

	if m2.jobs[0].Status != storage.JobStatusRunning {
		t.Errorf("Expected status to rollback to 'running', got '%s'", m2.jobs[0].Status)
	}
	if m2.jobs[0].FinishedAt != nil {
		t.Errorf("Expected FinishedAt to rollback to nil, got %v", m2.jobs[0].FinishedAt)
	}
	if m2.err == nil {
		t.Error("Expected error to be set")
	}
}

func TestTUICancelRollbackWithNonNilFinishedAt(t *testing.T) {
	// Test rollback when original FinishedAt is non-nil (edge case: corrupted state
	// or queued job that somehow has a timestamp)
	startTime := time.Now().Add(-5 * time.Minute)
	originalFinished := time.Now().Add(-2 * time.Minute)
	m := setupTestModel([]storage.ReviewJob{
		makeJob(42, withStatus(storage.JobStatusQueued), withStartedAt(startTime), withFinishedAt(&originalFinished)),
	}, func(m *model) {
		m.selectedIdx = 0
		m.selectedJobID = 42
	})

	// Simulate the optimistic update that would have happened
	now := time.Now()
	m.jobs[0].Status = storage.JobStatusCanceled
	m.jobs[0].FinishedAt = &now

	// Simulate cancel error result - should rollback to original FinishedAt
	errResult := cancelResultMsg{
		jobID:         42,
		oldState:      storage.JobStatusQueued,
		oldFinishedAt: &originalFinished, // Was non-nil before optimistic update
		err:           fmt.Errorf("server error"),
	}

	m2, _ := updateModel(t, m, errResult)

	if m2.jobs[0].Status != storage.JobStatusQueued {
		t.Errorf("Expected status to rollback to 'queued', got '%s'", m2.jobs[0].Status)
	}
	if m2.jobs[0].FinishedAt == nil {
		t.Error("Expected FinishedAt to rollback to original non-nil value, got nil")
	} else if !m2.jobs[0].FinishedAt.Equal(originalFinished) {
		t.Errorf("Expected FinishedAt to rollback to %v, got %v", originalFinished, *m2.jobs[0].FinishedAt)
	}
	if m2.err == nil {
		t.Error("Expected error to be set")
	}
}

func TestTUICancelOptimisticUpdate(t *testing.T) {
	// Setup: running job with no FinishedAt
	startTime := time.Now().Add(-5 * time.Minute)
	m := setupTestModel([]storage.ReviewJob{
		makeJob(42, withStatus(storage.JobStatusRunning), withStartedAt(startTime), withFinishedAt(nil)),
	}, func(m *model) {
		m.selectedIdx = 0
		m.selectedJobID = 42
		m.currentView = viewQueue
	})

	// Simulate pressing 'x' key
	beforeUpdate := time.Now()
	m2, cmd := pressKey(m, 'x')

	// Should have optimistically set status to canceled
	if m2.jobs[0].Status != storage.JobStatusCanceled {
		t.Errorf("Expected status 'canceled', got '%s'", m2.jobs[0].Status)
	}

	// Should have set FinishedAt to stop elapsed time from ticking
	if m2.jobs[0].FinishedAt == nil {
		t.Error("Expected FinishedAt to be set during optimistic cancel")
	} else if m2.jobs[0].FinishedAt.Before(beforeUpdate) {
		t.Error("Expected FinishedAt to be set to current time")
	}

	// Should return a command (the cancel HTTP request)
	if cmd == nil {
		t.Error("Expected a command to be returned for the cancel request")
	}
}

func TestTUICancelOnlyRunningOrQueued(t *testing.T) {
	// Test that pressing 'x' on done/failed/canceled jobs is a no-op
	testCases := []storage.JobStatus{
		storage.JobStatusDone,
		storage.JobStatusFailed,
		storage.JobStatusCanceled,
	}

	for _, status := range testCases {
		t.Run(string(status), func(t *testing.T) {
			finishedAt := time.Now().Add(-1 * time.Hour)
			m := setupTestModel([]storage.ReviewJob{
				makeJob(1, withStatus(status), withFinishedAt(&finishedAt)),
			}, func(m *model) {
				m.selectedIdx = 0
				m.currentView = viewQueue
			})

			// Simulate pressing 'x' key
			m2, cmd := pressKey(m, 'x')

			// Status should not change
			if m2.jobs[0].Status != status {
				t.Errorf("Expected status to remain '%s', got '%s'", status, m2.jobs[0].Status)
			}

			// FinishedAt should not change
			if m2.jobs[0].FinishedAt == nil || !m2.jobs[0].FinishedAt.Equal(finishedAt) {
				t.Errorf("Expected FinishedAt to remain unchanged")
			}

			// No command should be returned (no HTTP request triggered)
			if cmd != nil {
				t.Errorf("Expected no command for non-cancellable job, got %v", cmd)
			}
		})
	}
}

// Tests for filter functionality

func TestTUIRespondTextPreservation(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(1, withRef("abc1234")),
		makeJob(2, withRef("def5678")),
	}, func(m *model) {
		m.selectedIdx = 0
		m.selectedJobID = 1
		m.width = 80
		m.height = 24
	})

	// 1. Open respond for Job 1
	m, _ = pressKey(m, 'c')

	if m.currentView != viewKindComment {
		t.Fatalf("Expected viewKindComment, got %v", m.currentView)
	}
	if m.commentJobID != 1 {
		t.Fatalf("Expected commentJobID=1, got %d", m.commentJobID)
	}

	// 2. Type some text
	m.commentText = "My draft response"

	// 3. Simulate failed submission - press enter then receive error
	m.currentView = m.commentFromView // Simulate what happens on enter
	errMsg := commentResultMsg{jobID: 1, err: fmt.Errorf("network error")}
	m, _ = updateModel(t, m, errMsg)

	// Text should be preserved after error
	if m.commentText != "My draft response" {
		t.Errorf("Expected text preserved after error, got %q", m.commentText)
	}
	if m.commentJobID != 1 {
		t.Errorf("Expected commentJobID preserved after error, got %d", m.commentJobID)
	}

	// 4. Re-open respond for Job 1 (Retry) - text should still be there
	m.currentView = viewQueue
	m.selectedIdx = 0
	m, _ = pressKey(m, 'c')

	if m.commentText != "My draft response" {
		t.Errorf("Expected text preserved on retry for same job, got %q", m.commentText)
	}

	// 5. Go back to queue and switch to Job 2 - text should be cleared
	m.currentView = viewQueue
	m.selectedIdx = 1
	m.selectedJobID = 2
	m, _ = pressKey(m, 'c')

	if m.commentText != "" {
		t.Errorf("Expected text cleared for different job, got %q", m.commentText)
	}
	if m.commentJobID != 2 {
		t.Errorf("Expected commentJobID=2, got %d", m.commentJobID)
	}
}

func TestTUIRespondSuccessClearsOnlyMatchingJob(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(1, withRef("abc1234")),
		makeJob(2, withRef("def5678")),
	}, func(m *model) {
		m.commentJobID = 2
		m.commentText = "New draft for job 2"
	})

	// Success message arrives for job 1 (the old submission)
	successMsg := commentResultMsg{jobID: 1, err: nil}
	m, _ = updateModel(t, m, successMsg)

	// Draft for job 2 should NOT be cleared
	if m.commentText != "New draft for job 2" {
		t.Errorf("Expected draft preserved for different job, got %q", m.commentText)
	}
	if m.commentJobID != 2 {
		t.Errorf("Expected commentJobID=2 preserved, got %d", m.commentJobID)
	}

	// Now success for job 2 should clear
	successMsg = commentResultMsg{jobID: 2, err: nil}
	m, _ = updateModel(t, m, successMsg)

	if m.commentText != "" {
		t.Errorf("Expected text cleared for matching job, got %q", m.commentText)
	}
	if m.commentJobID != 0 {
		t.Errorf("Expected commentJobID=0 after success, got %d", m.commentJobID)
	}
}

func TestTUIRespondBackspaceMultiByte(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewKindComment
	m.commentJobID = 1

	// Type text with multi-byte characters
	m, _ = pressKeys(m, []rune("Hello 世界"))

	if m.commentText != "Hello 世界" {
		t.Errorf("Expected commentText='Hello 世界', got %q", m.commentText)
	}

	// Backspace should remove '界' (one character), not corrupt it
	m, _ = pressSpecial(m, tea.KeyBackspace)
	if m.commentText != "Hello 世" {
		t.Errorf("Expected commentText='Hello 世' after backspace, got %q", m.commentText)
	}

	// Backspace should remove '世'
	m, _ = pressSpecial(m, tea.KeyBackspace)
	if m.commentText != "Hello " {
		t.Errorf("Expected commentText='Hello ' after second backspace, got %q", m.commentText)
	}
}

func isValidUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false
		}
		i += size
	}
	return true
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func TestTUIRespondViewTruncationMultiByte(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewKindComment
	m.commentJobID = 1
	m.width = 30
	m.height = 20

	// Set text with multi-byte characters that would be truncated
	// The box has boxWidth-2 available space for text
	m.commentText = "あいうえおかきくけこさしすせそ" // 15 Japanese characters (30 cells wide)

	// Render should not panic or corrupt characters
	output := m.renderRespondView()

	// The output should contain valid UTF-8 and not have corrupted characters
	if !isValidUTF8(output) {
		t.Error("Rendered output contains invalid UTF-8")
	}

	// Should contain at least the start of the text (may be truncated)
	if !containsRune(output, 'あ') {
		t.Error("Expected output to contain the first character")
	}

	// Verify visual width alignment: all content lines should end with "│"
	// and have consistent visual width
	lines := strings.Split(stripANSI(output), "\n")
	var expectedWidth int
	for _, line := range lines {
		if strings.HasPrefix(line, "│") && strings.HasSuffix(line, "│") {
			// This is a content line - verify right border alignment
			// All content lines should have the same visual width
			width := runewidth.StringWidth(line)
			if expectedWidth == 0 {
				expectedWidth = width // Set from first line
			}
			if width != expectedWidth {
				t.Errorf("Line visual width %d != expected %d: %q", width, expectedWidth, line)
			}
		}
	}
	if expectedWidth == 0 {
		t.Error("No content lines found in output")
	}
}

func TestTUIRespondViewTabExpansion(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewKindComment
	m.commentJobID = 1
	m.width = 40
	m.height = 20

	// Set text with tabs
	m.commentText = "a\tb\tc"

	output := m.renderRespondView()
	plainOutput := stripANSI(output)

	// Tabs should be expanded to spaces
	if strings.Contains(plainOutput, "\t") {
		t.Error("Output should not contain literal tabs")
	}

	// Verify the text appears with expanded tabs (4 spaces each)
	// "a    b    c" should be in the output
	if !strings.Contains(plainOutput, "a    b    c") {
		t.Errorf("Expected tabs expanded to 4 spaces, got: %q", plainOutput)
	}
}

func TestCancelKeyMovesSelectionWithHideAddressed(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(1, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))),
		makeJob(2, withStatus(storage.JobStatusRunning)),
		makeJob(3, withStatus(storage.JobStatusDone), withAddressed(boolPtr(false))),
	}, func(m *model) {
		m.currentView = viewQueue
		m.hideAddressed = true
		m.selectedIdx = 1
		m.selectedJobID = 2
	})

	result, _ := m.handleCancelKey()
	m2 := result.(model)

	// Job 2 should now be canceled
	if m2.jobs[1].Status != storage.JobStatusCanceled {
		t.Fatalf("expected canceled, got %s", m2.jobs[1].Status)
	}
	// Cursor should have moved away from the now-hidden job
	if m2.selectedIdx == 1 {
		t.Error("cursor should move off canceled job")
	}
	if !m2.isJobVisible(m2.jobs[m2.selectedIdx]) {
		t.Error("cursor should land on a visible job")
	}
}

func TestAddressedKeyUpdatesStatsOptimistically(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(1, withStatus(storage.JobStatusDone),
			withAddressed(boolPtr(false))),
		makeJob(2, withStatus(storage.JobStatusDone),
			withAddressed(boolPtr(false))),
	}, func(m *model) {
		m.currentView = viewQueue
		m.selectedIdx = 0
		m.selectedJobID = 1
		m.jobStats = storage.JobStats{
			Done: 2, Addressed: 0, Unaddressed: 2,
		}
		m.pendingAddressed = make(map[int64]pendingState)
	})

	// Mark job 1 as addressed
	result, _ := m.handleAddressedKey()
	m2 := result.(model)

	assertJobStats(t, m2, 1, 1)
}

func TestAddressedKeyUpdatesStatsFromReviewView(t *testing.T) {
	m := setupTestModel([]storage.ReviewJob{
		makeJob(1, withStatus(storage.JobStatusDone),
			withAddressed(boolPtr(false))),
	}, func(m *model) {
		m.currentView = viewReview
		m.currentReview = &storage.Review{
			ID:        42,
			Addressed: false,
			Job: &storage.ReviewJob{
				ID:     1,
				Status: storage.JobStatusDone,
			},
		}
		m.jobStats = storage.JobStats{
			Done: 1, Addressed: 0, Unaddressed: 1,
		}
		m.pendingAddressed = make(map[int64]pendingState)
		m.pendingReviewAddressed = make(map[int64]pendingState)
	})

	result, _ := m.handleAddressedKey()
	m2 := result.(model)

	assertJobStats(t, m2, 1, 0)
}

func setupTestModel(jobs []storage.ReviewJob, opts ...func(*model)) model {
	m := newModel("http://localhost", withExternalIODisabled())
	m.jobs = jobs
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func assertSelection(t *testing.T, m model, idx int, jobID int64) {
	t.Helper()
	if m.selectedIdx != idx {
		t.Errorf("Expected selectedIdx=%d, got %d", idx, m.selectedIdx)
	}
	if m.selectedJobID != jobID {
		t.Errorf("Expected selectedJobID=%d, got %d", jobID, m.selectedJobID)
	}
}

func assertView(t *testing.T, m model, view viewKind) {
	t.Helper()
	if m.currentView != view {
		t.Errorf("Expected view=%d, got %d", view, m.currentView)
	}
}

func withStartedAt(t time.Time) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.StartedAt = &t }
}

func withFinishedAt(t *time.Time) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.FinishedAt = t }
}

// expectJSONPost is a helper to mock expected POST requests and respond with JSON.
func expectJSONPost[Req any, Res any](t *testing.T, path string, expected Req, response Res) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if path != "" && r.URL.Path != path {
			t.Errorf("Expected path %s, got %s", path, r.URL.Path)
		}

		var req Req
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if diff := cmp.Diff(expected, req); diff != "" {
			t.Errorf("Request payload mismatch (-want +got):\n%s", diff)
			http.Error(w, "payload mismatch", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(response)
	}
}

// assertMsgType is a helper to assert the type of a tea.Msg and return it.
func assertMsgType[T any](t *testing.T, msg tea.Msg) T {
	t.Helper()
	result, ok := msg.(T)
	if !ok {
		t.Fatalf("Expected %T, got %T: %v", new(T), msg, msg)
	}
	return result
}

// assertJobStats is a helper to assert the jobStats of a model.
func assertJobStats(t *testing.T, m model, addressed, unaddressed int) {
	t.Helper()
	if m.jobStats.Addressed != addressed {
		t.Fatalf("expected Addressed=%d, got %d", addressed, m.jobStats.Addressed)
	}
	if m.jobStats.Unaddressed != unaddressed {
		t.Fatalf("expected Unaddressed=%d, got %d", unaddressed, m.jobStats.Unaddressed)
	}
}
