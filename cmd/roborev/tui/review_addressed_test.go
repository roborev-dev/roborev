package tui

import (
	"fmt"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestTUIReviewViewAddressedRollbackOnError(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())

	// Initial state with review view showing an unaddressed review
	m.currentView = viewReview
	m.currentReview = makeReview(42, &storage.ReviewJob{ID: 100})

	// Simulate optimistic update (what happens when 'a' is pressed in review view)
	m.currentReview.Addressed = true
	m.pendingAddressed[100] = pendingState{newState: true, seq: 1} // Track pending state

	// Error result from server (reviewID must match currentReview.ID for rollback)
	errMsg := addressedResultMsg{
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
	m := newModel("http://localhost", withExternalIODisabled())

	// Initial state with review view
	m.currentView = viewReview
	m.currentReview = makeReview(42, &storage.ReviewJob{})

	// Simulate optimistic update
	m.currentReview.Addressed = true

	// Success result (err is nil)
	successMsg := addressedResultMsg{
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
	m := newModel("http://localhost", withExternalIODisabled())

	// Setup: jobs in queue with addressed=false
	addrA := false
	addrB := false
	m.jobs = []storage.ReviewJob{
		{ID: 100, Status: storage.JobStatusDone, Addressed: &addrA}, // Job for review A
		{ID: 200, Status: storage.JobStatusDone, Addressed: &addrB}, // Job for review B
	}

	// User views review A, toggles addressed (optimistic update)
	m.currentView = viewReview
	m.currentReview = makeReview(42, &storage.ReviewJob{ID: 100})
	m.currentReview.Addressed = true                               // Optimistic update to review
	*m.jobs[0].Addressed = true                                    // Optimistic update to job in queue
	m.pendingAddressed[100] = pendingState{newState: true, seq: 1} // Track pending state for job A

	// User navigates to review B before error response arrives
	m.currentReview = makeReview(99, &storage.ReviewJob{ID: 200})

	// Error arrives for review A's toggle
	errMsg := addressedResultMsg{
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
	m := newModel("http://localhost", withExternalIODisabled())

	// Setup: job in queue with addressed=false
	addr := false
	m.jobs = []storage.ReviewJob{
		{ID: 100, Status: storage.JobStatusDone, Addressed: &addr},
	}

	// User views review for job 100 and presses 'a'
	m.currentView = viewReview
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
	m := newModel("http://localhost", withExternalIODisabled())

	// Review without an associated job (Job is nil)
	m.currentView = viewReview
	m.currentReview = &storage.Review{ID: 42}

	// Simulate optimistic update (what happens when 'a' is pressed)
	m.currentReview.Addressed = true
	m.pendingReviewAddressed[42] = pendingState{newState: true, seq: 1} // Track pending state by review ID

	// Error arrives for this toggle (no jobID since Job was nil)
	errMsg := addressedResultMsg{
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
	m := newModel("http://localhost", withExternalIODisabled())

	// Review without an associated job
	m.currentView = viewReview
	m.currentReview = &storage.Review{ID: 42}

	// User toggled to true, then back to false
	// pendingReviewAddressed is now false (from the second toggle)
	m.currentReview.Addressed = false
	m.pendingReviewAddressed[42] = pendingState{newState: false, seq: 1}

	// A stale error arrives from the earlier toggle to true
	staleErrorMsg := addressedResultMsg{
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
	// Test: true (seq 1) -> false (seq 2) -> true (seq 3), with late error from first true
	// The late error has newState=true which matches current pending newState,
	// but sequence numbers now distinguish same-state toggles.
	m := newModel("http://localhost", withExternalIODisabled())

	// Review without an associated job
	m.currentView = viewReview
	m.currentReview = &storage.Review{ID: 42}

	// Sequence: toggle true (seq 1) -> toggle false (seq 2) -> toggle true (seq 3)
	// After third toggle, state is true and pendingReviewAddressed has seq 3
	m.currentReview.Addressed = true
	m.pendingReviewAddressed[42] = pendingState{newState: true, seq: 3} // Third toggle

	// A late error arrives from the FIRST toggle (seq 1)
	// This error has newState=true which matches current pending newState,
	// but seq doesn't match, so it should be treated as stale and ignored.
	lateErrorMsg := addressedResultMsg{
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
