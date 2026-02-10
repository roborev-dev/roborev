package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIAddressReviewSuccess(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		if req["job_id"].(float64) != 100 {
			t.Errorf("Expected job_id 100, got %v", req["job_id"])
		}
		if req["addressed"].(bool) != true {
			t.Errorf("Expected addressed true, got %v", req["addressed"])
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	})
	cmd := m.addressReview(42, 100, true, false, 1) // reviewID=42, jobID=100, newState=true, oldState=false
	msg := cmd()

	result, ok := msg.(tuiAddressedResultMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedResultMsg, got %T: %v", msg, msg)
	}
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

	result, ok := msg.(tuiAddressedResultMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedResultMsg for 404, got %T: %v", msg, msg)
	}
	if result.err == nil || result.err.Error() != "review not found" {
		t.Errorf("Expected 'review not found' error, got: %v", result.err)
	}
}

func TestTUIToggleAddressedForJobSuccess(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/review/address" {
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)
			if req["job_id"].(float64) != 1 {
				t.Errorf("Expected job_id 1, got %v", req["job_id"])
			}
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
		} else {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	currentState := false
	cmd := m.toggleAddressedForJob(1, &currentState)
	msg := cmd()

	addressed, ok := msg.(tuiAddressedMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedMsg, got %T: %v", msg, msg)
	}
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

	errMsg, ok := msg.(tuiErrMsg)
	if !ok {
		t.Fatalf("Expected tuiErrMsg, got %T: %v", msg, msg)
	}
	if errMsg.Error() != "no review for this job" {
		t.Errorf("Expected 'no review for this job', got: %v", errMsg)
	}
}

func TestTUIAddressFromReviewViewWithHideAddressed(t *testing.T) {
	// When hideAddressed is on and user marks a review as addressed from review view,
	// selectedIdx should NOT change immediately. The findNextViewableJob/findPrevViewableJob
	// functions start from selectedIdx +/- 1, so left/right navigation will naturally
	// find the correct adjacent visible jobs. Selection only moves on escape.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = true

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
		makeJob(2, withAddressed(boolPtr(false))),
		makeJob(3, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 1 // Currently viewing job 2
	m.selectedJobID = 2
	m.currentReview = makeReview(10, &m.jobs[1])

	// Press 'a' to mark as addressed - selection stays at current position
	m2, _ := pressKey(m, 'a')

	// Selection stays at index 1 so left/right navigation works correctly from current position
	if m2.selectedIdx != 1 {
		t.Errorf("After 'a': expected selectedIdx=1 (unchanged), got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 2 {
		t.Errorf("After 'a': expected selectedJobID=2 (unchanged), got %d", m2.selectedJobID)
	}
	// Still viewing the current review
	if m2.currentView != tuiViewReview {
		t.Errorf("After 'a': expected view=review (unchanged), got %d", m2.currentView)
	}

	// Press escape to return to queue - NOW selection moves via normalizeSelectionIfHidden
	m3, _ := pressSpecial(m2, tea.KeyEscape)

	// Selection moved to next visible job (job 3, index 2)
	if m3.selectedIdx != 2 {
		t.Errorf("After escape: expected selectedIdx=2, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 3 {
		t.Errorf("After escape: expected selectedJobID=3, got %d", m3.selectedJobID)
	}
	if m3.currentView != tuiViewQueue {
		t.Errorf("After escape: expected view=queue, got %d", m3.currentView)
	}
}

func TestTUIAddressFromReviewViewFallbackToPrev(t *testing.T) {
	// When at end of list and marking addressed, should fall back to previous visible job
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = true

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
		makeJob(2, withAddressed(boolPtr(false))),
		makeJob(3, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 2 // Currently viewing job 3 (last one)
	m.selectedJobID = 3
	m.currentReview = makeReview(10, &m.jobs[2])

	// Press 'a' then escape
	m2, _ := pressKey(m, 'a')
	m3, _ := pressSpecial(m2, tea.KeyEscape)

	// Should fall back to previous visible job (job 2, index 1)
	if m3.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1 (prev visible job), got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2, got %d", m3.selectedJobID)
	}
}

func TestTUIAddressFromReviewViewExitWithQ(t *testing.T) {
	// Same as TestTUIAddressFromReviewViewWithHideAddressed but exits with 'q'
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = true

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
		makeJob(2, withAddressed(boolPtr(false))),
		makeJob(3, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentReview = makeReview(10, &m.jobs[1])

	// Press 'a' to mark as addressed
	m2, _ := pressKey(m, 'a')

	// Press 'q' to return to queue - selection should normalize
	m3, _ := pressKey(m2, 'q')

	if m3.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2 (next visible job), got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m3.selectedJobID)
	}
	if m3.currentView != tuiViewQueue {
		t.Errorf("Expected view=queue, got %d", m3.currentView)
	}
}

func TestTUIAddressFromReviewViewExitWithCtrlC(t *testing.T) {
	// Same as TestTUIAddressFromReviewViewExitWithQ but exits with ctrl+c
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = true

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
		makeJob(2, withAddressed(boolPtr(false))),
		makeJob(3, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentReview = makeReview(10, &m.jobs[1])

	// Press 'a' to mark as addressed
	m2, _ := pressKey(m, 'a')

	// Press ctrl+c to return to queue - selection should normalize
	m3, _ := pressSpecial(m2, tea.KeyCtrlC)

	if m3.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2 (next visible job), got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m3.selectedJobID)
	}
	if m3.currentView != tuiViewQueue {
		t.Errorf("Expected view=queue, got %d", m3.currentView)
	}
}

// addressRequest is used to decode and validate POST body in tests
type addressRequest struct {
	JobID     int64 `json:"job_id"`
	Addressed bool  `json:"addressed"`
}

func TestTUIAddressReviewInBackgroundSuccess(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review/address" || r.Method != http.MethodPost {
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req addressRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}
		if req.JobID != 42 {
			t.Errorf("Expected job_id=42, got %d", req.JobID)
		}
		if req.Addressed != true {
			t.Errorf("Expected addressed=true, got %v", req.Addressed)
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	})
	cmd := m.addressReviewInBackground(42, true, false, 1) // jobID=42, newState=true, oldState=false
	msg := cmd()

	result, ok := msg.(tuiAddressedResultMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedResultMsg, got %T: %v", msg, msg)
	}
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
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := m.addressReviewInBackground(42, true, false, 1)
	msg := cmd()

	result, ok := msg.(tuiAddressedResultMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedResultMsg, got %T: %v", msg, msg)
	}
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
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	cmd := m.addressReviewInBackground(42, true, false, 1)
	msg := cmd()

	result, ok := msg.(tuiAddressedResultMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedResultMsg, got %T: %v", msg, msg)
	}
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
	m := newTuiModel("http://localhost")

	// Initial state with job addressed=false
	addressed := false
	m.jobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusDone, Addressed: &addressed},
	}
	m.selectedIdx = 0
	m.selectedJobID = 42

	// First, simulate the optimistic update (what happens when 'a' is pressed)
	*m.jobs[0].Addressed = true
	m.pendingAddressed[42] = pendingState{newState: true, seq: 1} // Track pending state

	// Simulate error result from background update
	// This would happen if server returned error after optimistic update
	errMsg := tuiAddressedResultMsg{
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
}

func TestTUIAddressedSuccessNoRollback(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state
	addressed := false
	m.jobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusDone, Addressed: &addressed},
	}

	// Simulate optimistic update
	*m.jobs[0].Addressed = true

	// Success result (err is nil)
	successMsg := tuiAddressedResultMsg{
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
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
		makeJob(2, withAddressed(boolPtr(false))),
		makeJob(3, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2

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
	m := newTuiModel("http://localhost")

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
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/job/cancel" {
			t.Errorf("Expected /api/job/cancel, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		var req struct {
			JobID int64 `json:"job_id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.JobID != 42 {
			t.Errorf("Expected job_id=42, got %d", req.JobID)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	})
	oldFinishedAt := time.Now().Add(-1 * time.Hour)
	cmd := m.cancelJob(42, storage.JobStatusRunning, &oldFinishedAt)
	msg := cmd()

	result, ok := msg.(tuiCancelResultMsg)
	if !ok {
		t.Fatalf("Expected tuiCancelResultMsg, got %T: %v", msg, msg)
	}
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

	result, ok := msg.(tuiCancelResultMsg)
	if !ok {
		t.Fatalf("Expected tuiCancelResultMsg, got %T: %v", msg, msg)
	}
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
	m := newTuiModel("http://localhost")

	// Setup: running job with no FinishedAt (still running)
	startTime := time.Now().Add(-5 * time.Minute)
	m.jobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusRunning, StartedAt: &startTime, FinishedAt: nil},
	}
	m.selectedIdx = 0
	m.selectedJobID = 42

	// Simulate the optimistic update that would have happened
	now := time.Now()
	m.jobs[0].Status = storage.JobStatusCanceled
	m.jobs[0].FinishedAt = &now

	// Simulate cancel error result - should rollback both status and FinishedAt
	errResult := tuiCancelResultMsg{
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
	m := newTuiModel("http://localhost")

	// Setup: job with an existing FinishedAt (unusual but possible edge case)
	startTime := time.Now().Add(-5 * time.Minute)
	originalFinished := time.Now().Add(-2 * time.Minute)
	m.jobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusQueued, StartedAt: &startTime, FinishedAt: &originalFinished},
	}
	m.selectedIdx = 0
	m.selectedJobID = 42

	// Simulate the optimistic update that would have happened
	now := time.Now()
	m.jobs[0].Status = storage.JobStatusCanceled
	m.jobs[0].FinishedAt = &now

	// Simulate cancel error result - should rollback to original FinishedAt
	errResult := tuiCancelResultMsg{
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
	m := newTuiModel("http://localhost")

	// Setup: running job with no FinishedAt
	startTime := time.Now().Add(-5 * time.Minute)
	m.jobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusRunning, StartedAt: &startTime, FinishedAt: nil},
	}
	m.selectedIdx = 0
	m.selectedJobID = 42
	m.currentView = tuiViewQueue

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
			m := newTuiModel("http://localhost")
			finishedAt := time.Now().Add(-1 * time.Hour)
			m.jobs = []storage.ReviewJob{
				{ID: 1, Status: status, FinishedAt: &finishedAt},
			}
			m.selectedIdx = 0
			m.currentView = tuiViewQueue

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
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc1234")),
		makeJob(2, withRef("def5678")),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.width = 80
	m.height = 24

	// 1. Open respond for Job 1
	m, _ = pressKey(m, 'c')

	if m.currentView != tuiViewComment {
		t.Fatalf("Expected tuiViewComment, got %v", m.currentView)
	}
	if m.commentJobID != 1 {
		t.Fatalf("Expected commentJobID=1, got %d", m.commentJobID)
	}

	// 2. Type some text
	m.commentText = "My draft response"

	// 3. Simulate failed submission - press enter then receive error
	m.currentView = m.commentFromView // Simulate what happens on enter
	errMsg := tuiCommentResultMsg{jobID: 1, err: fmt.Errorf("network error")}
	m, _ = updateModel(t, m, errMsg)

	// Text should be preserved after error
	if m.commentText != "My draft response" {
		t.Errorf("Expected text preserved after error, got %q", m.commentText)
	}
	if m.commentJobID != 1 {
		t.Errorf("Expected commentJobID preserved after error, got %d", m.commentJobID)
	}

	// 4. Re-open respond for Job 1 (Retry) - text should still be there
	m.currentView = tuiViewQueue
	m.selectedIdx = 0
	m, _ = pressKey(m, 'c')

	if m.commentText != "My draft response" {
		t.Errorf("Expected text preserved on retry for same job, got %q", m.commentText)
	}

	// 5. Go back to queue and switch to Job 2 - text should be cleared
	m.currentView = tuiViewQueue
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
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc1234")),
		makeJob(2, withRef("def5678")),
	}

	// User submitted response for job 1, then started drafting for job 2
	m.commentJobID = 2
	m.commentText = "New draft for job 2"

	// Success message arrives for job 1 (the old submission)
	successMsg := tuiCommentResultMsg{jobID: 1, err: nil}
	m, _ = updateModel(t, m, successMsg)

	// Draft for job 2 should NOT be cleared
	if m.commentText != "New draft for job 2" {
		t.Errorf("Expected draft preserved for different job, got %q", m.commentText)
	}
	if m.commentJobID != 2 {
		t.Errorf("Expected commentJobID=2 preserved, got %d", m.commentJobID)
	}

	// Now success for job 2 should clear
	successMsg = tuiCommentResultMsg{jobID: 2, err: nil}
	m, _ = updateModel(t, m, successMsg)

	if m.commentText != "" {
		t.Errorf("Expected text cleared for matching job, got %q", m.commentText)
	}
	if m.commentJobID != 0 {
		t.Errorf("Expected commentJobID=0 after success, got %d", m.commentJobID)
	}
}

func TestTUIRespondBackspaceMultiByte(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewComment
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
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewComment
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

	// Verify visual width alignment: all content lines should end with "|"
	// and have consistent visual width
	lines := strings.Split(stripANSI(output), "\n")
	var expectedWidth int
	for _, line := range lines {
		if strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|") {
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
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewComment
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
