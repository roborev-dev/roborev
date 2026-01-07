package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wesm/roborev/internal/storage"
)

func TestTUIFetchJobsSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" {
			t.Errorf("Expected /api/jobs, got %s", r.URL.Path)
		}
		jobs := []storage.ReviewJob{{ID: 1, GitRef: "abc123", Agent: "test"}}
		json.NewEncoder(w).Encode(map[string]interface{}{"jobs": jobs})
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	cmd := m.fetchJobs()
	msg := cmd()

	jobs, ok := msg.(tuiJobsMsg)
	if !ok {
		t.Fatalf("Expected tuiJobsMsg, got %T: %v", msg, msg)
	}
	if len(jobs) != 1 || jobs[0].ID != 1 {
		t.Errorf("Unexpected jobs: %+v", jobs)
	}
}

func TestTUIFetchJobsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	cmd := m.fetchJobs()
	msg := cmd()

	_, ok := msg.(tuiErrMsg)
	if !ok {
		t.Fatalf("Expected tuiErrMsg for 500, got %T: %v", msg, msg)
	}
}

func TestTUIFetchReviewNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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

func TestTUIAddressReviewSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		if req["review_id"].(float64) != 42 {
			t.Errorf("Expected review_id 42, got %v", req["review_id"])
		}
		if req["addressed"].(bool) != true {
			t.Errorf("Expected addressed true, got %v", req["addressed"])
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	cmd := m.addressReview(42, true)
	msg := cmd()

	addressed, ok := msg.(tuiAddressedMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedMsg, got %T: %v", msg, msg)
	}
	if !bool(addressed) {
		t.Error("Expected addressed to be true")
	}
}

func TestTUIAddressReviewNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	cmd := m.addressReview(999, true)
	msg := cmd()

	errMsg, ok := msg.(tuiErrMsg)
	if !ok {
		t.Fatalf("Expected tuiErrMsg for 404, got %T: %v", msg, msg)
	}
	if errMsg.Error() != "review not found" {
		t.Errorf("Expected 'review not found', got: %v", errMsg)
	}
}

func TestTUIToggleAddressedForJobSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/review" {
			review := storage.Review{ID: 10, Addressed: false}
			json.NewEncoder(w).Encode(review)
		} else if r.URL.Path == "/api/review/address" {
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
		}
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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

func TestTUIHTTPTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay longer than client timeout
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	// Override with short timeout for test
	m.client.Timeout = 50 * time.Millisecond

	cmd := m.fetchJobs()
	msg := cmd()

	_, ok := msg.(tuiErrMsg)
	if !ok {
		t.Fatalf("Expected tuiErrMsg for timeout, got %T: %v", msg, msg)
	}
}

func TestTUISelectionMaintainedOnInsert(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with 3 jobs, select the middle one (ID=2)
	m.jobs = []storage.ReviewJob{
		{ID: 3}, {ID: 2}, {ID: 1},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2

	// New jobs added at the top (newer jobs first)
	newJobs := tuiJobsMsg([]storage.ReviewJob{
		{ID: 5}, {ID: 4}, {ID: 3}, {ID: 2}, {ID: 1},
	})

	updated, _ := m.Update(newJobs)
	m = updated.(tuiModel)

	// Should still be on job ID=2, now at index 3
	if m.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2, got %d", m.selectedJobID)
	}
	if m.selectedIdx != 3 {
		t.Errorf("Expected selectedIdx=3 (ID=2 moved), got %d", m.selectedIdx)
	}
}

func TestTUISelectionClampsOnRemoval(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with 3 jobs, select the last one (ID=1)
	m.jobs = []storage.ReviewJob{
		{ID: 3}, {ID: 2}, {ID: 1},
	}
	m.selectedIdx = 2
	m.selectedJobID = 1

	// Job ID=1 is removed
	newJobs := tuiJobsMsg([]storage.ReviewJob{
		{ID: 3}, {ID: 2},
	})

	updated, _ := m.Update(newJobs)
	m = updated.(tuiModel)

	// Should clamp to last valid index and update selectedJobID
	if m.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1 (clamped), got %d", m.selectedIdx)
	}
	if m.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2 (new selection), got %d", m.selectedJobID)
	}
}

func TestTUISelectionFirstJobOnEmpty(t *testing.T) {
	m := newTuiModel("http://localhost")

	// No prior selection (empty jobs list, zero selectedJobID)
	m.jobs = []storage.ReviewJob{}
	m.selectedIdx = 0
	m.selectedJobID = 0

	// Jobs arrive
	newJobs := tuiJobsMsg([]storage.ReviewJob{
		{ID: 5}, {ID: 4}, {ID: 3},
	})

	updated, _ := m.Update(newJobs)
	m = updated.(tuiModel)

	// Should select first job
	if m.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx=0, got %d", m.selectedIdx)
	}
	if m.selectedJobID != 5 {
		t.Errorf("Expected selectedJobID=5 (first job), got %d", m.selectedJobID)
	}
}

func TestTUISelectionEmptyList(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Had jobs, now empty
	m.jobs = []storage.ReviewJob{{ID: 1}}
	m.selectedIdx = 0
	m.selectedJobID = 1

	newJobs := tuiJobsMsg([]storage.ReviewJob{})

	updated, _ := m.Update(newJobs)
	m = updated.(tuiModel)

	if m.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx=0, got %d", m.selectedIdx)
	}
	if m.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0, got %d", m.selectedJobID)
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

	// Simulate error result from background update
	// This would happen if server returned error after optimistic update
	errMsg := tuiAddressedResultMsg{
		jobID:    42,
		oldState: false, // Was false before optimistic update
		err:      fmt.Errorf("server error"),
	}

	// First, simulate the optimistic update (what happens when 'a' is pressed)
	*m.jobs[0].Addressed = true

	// Now handle the error result - should rollback
	updated, _ := m.Update(errMsg)
	m = updated.(tuiModel)

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
		err:      nil,
	}

	updated, _ := m.Update(successMsg)
	m = updated.(tuiModel)

	// Should stay true (no rollback on success)
	if m.jobs[0].Addressed == nil || *m.jobs[0].Addressed != true {
		t.Errorf("Expected addressed=true after success, got %v", m.jobs[0].Addressed)
	}
	if m.err != nil {
		t.Errorf("Expected no error, got %v", m.err)
	}
}

func TestTUISelectionMaintainedOnLargeBatch(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with 1 job selected
	m.jobs = []storage.ReviewJob{{ID: 1}}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// 30 new jobs added at the top (simulating large batch)
	newJobs := make([]storage.ReviewJob, 31)
	for i := 0; i < 30; i++ {
		newJobs[i] = storage.ReviewJob{ID: int64(31 - i)} // IDs 31, 30, 29, ..., 2
	}
	newJobs[30] = storage.ReviewJob{ID: 1} // Original job at the end

	updated, _ := m.Update(tuiJobsMsg(newJobs))
	m = updated.(tuiModel)

	// Should still follow job ID=1, now at index 30
	if m.selectedJobID != 1 {
		t.Errorf("Expected selectedJobID=1, got %d", m.selectedJobID)
	}
	if m.selectedIdx != 30 {
		t.Errorf("Expected selectedIdx=30 (ID=1 at end), got %d", m.selectedIdx)
	}
}
