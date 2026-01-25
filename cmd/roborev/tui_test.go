package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/storage"
)

// mockConnError creates a connection error (url.Error) for testing
func mockConnError(msg string) error {
	return &url.Error{Op: "Get", URL: "http://localhost:7373", Err: errors.New(msg)}
}

// stripANSI removes ANSI escape sequences from a string
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

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
	if len(jobs.jobs) != 1 || jobs.jobs[0].ID != 1 {
		t.Errorf("Unexpected jobs: %+v", jobs.jobs)
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

	_, ok := msg.(tuiJobsErrMsg)
	if !ok {
		t.Fatalf("Expected tuiJobsErrMsg for 500, got %T: %v", msg, msg)
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
	if result.reviewID != 42 {
		t.Errorf("Expected reviewID=42, got %d", result.reviewID)
	}
}

func TestTUIAddressReviewNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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

func TestTUIAddressFromReviewViewWithHideAddressed(t *testing.T) {
	// When hideAddressed is on and user marks a review as addressed from review view,
	// selectedIdx should NOT change immediately. The findNextViewableJob/findPrevViewableJob
	// functions start from selectedIdx +/- 1, so left/right navigation will naturally
	// find the correct adjacent visible jobs. Selection only moves on escape.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = true
	addr1, addr2, addr3 := false, false, false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addr1},
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addr2},
		{ID: 3, Status: storage.JobStatusDone, Addressed: &addr3},
	}
	m.selectedIdx = 1 // Currently viewing job 2
	m.selectedJobID = 2
	m.currentReview = &storage.Review{
		ID:       10,
		JobID:    2,
		Addressed: false,
		Job:      &m.jobs[1],
	}

	// Press 'a' to mark as addressed - selection stays at current position
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := updated.(tuiModel)

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
	updated2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := updated2.(tuiModel)

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
	addr1, addr2, addr3 := false, false, false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addr1},
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addr2},
		{ID: 3, Status: storage.JobStatusDone, Addressed: &addr3},
	}
	m.selectedIdx = 2 // Currently viewing job 3 (last one)
	m.selectedJobID = 3
	m.currentReview = &storage.Review{
		ID:       10,
		JobID:    3,
		Addressed: false,
		Job:      &m.jobs[2],
	}

	// Press 'a' then escape
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := updated.(tuiModel)
	updated2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := updated2.(tuiModel)

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
	addr1, addr2, addr3 := false, false, false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addr1},
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addr2},
		{ID: 3, Status: storage.JobStatusDone, Addressed: &addr3},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentReview = &storage.Review{
		ID:       10,
		JobID:    2,
		Addressed: false,
		Job:      &m.jobs[1],
	}

	// Press 'a' to mark as addressed
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := updated.(tuiModel)

	// Press 'q' to return to queue - selection should normalize
	updated2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m3 := updated2.(tuiModel)

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
	addr1, addr2, addr3 := false, false, false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addr1},
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addr2},
		{ID: 3, Status: storage.JobStatusDone, Addressed: &addr3},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentReview = &storage.Review{
		ID:       10,
		JobID:    2,
		Addressed: false,
		Job:      &m.jobs[1],
	}

	// Press 'a' to mark as addressed
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := updated.(tuiModel)

	// Press ctrl+c to return to queue - selection should normalize
	updated2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m3 := updated2.(tuiModel)

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
	ReviewID  int64 `json:"review_id"`
	Addressed bool  `json:"addressed"`
}

func TestTUIAddressReviewInBackgroundSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/review" && r.Method == http.MethodGet:
			if r.URL.Query().Get("job_id") != "42" {
				t.Errorf("Expected job_id=42, got %s", r.URL.Query().Get("job_id"))
			}
			review := storage.Review{ID: 10, Addressed: false}
			json.NewEncoder(w).Encode(review)
		case r.URL.Path == "/api/review/address" && r.Method == http.MethodPost:
			var req addressRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("Failed to decode request body: %v", err)
			}
			if req.ReviewID != 10 {
				t.Errorf("Expected review_id=10, got %d", req.ReviewID)
			}
			if req.Addressed != true {
				t.Errorf("Expected addressed=true, got %v", req.Addressed)
			}
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
		default:
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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
	// reviewID is intentionally 0 for queue view commands (only jobID is set)
	if result.reviewID != 0 {
		t.Errorf("Expected reviewID=0 for queue view, got %d", result.reviewID)
	}
}

func TestTUIAddressReviewInBackgroundNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/review" {
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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

func TestTUIAddressReviewInBackgroundFetchError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/review" {
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	cmd := m.addressReviewInBackground(42, true, false, 1)
	msg := cmd()

	result, ok := msg.(tuiAddressedResultMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedResultMsg, got %T: %v", msg, msg)
	}
	if result.err == nil {
		t.Error("Expected error for 500 response")
	}
	if result.jobID != 42 {
		t.Errorf("Expected jobID=42 for rollback, got %d", result.jobID)
	}
}

func TestTUIAddressReviewInBackgroundBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/review" {
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte("not valid json"))
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	cmd := m.addressReviewInBackground(42, true, false, 1)
	msg := cmd()

	result, ok := msg.(tuiAddressedResultMsg)
	if !ok {
		t.Fatalf("Expected tuiAddressedResultMsg, got %T: %v", msg, msg)
	}
	if result.err == nil {
		t.Error("Expected error for bad JSON")
	}
	if result.jobID != 42 {
		t.Errorf("Expected jobID=42 for rollback, got %d", result.jobID)
	}
}

func TestTUIAddressReviewInBackgroundAddressError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/review" && r.Method == http.MethodGet:
			review := storage.Review{ID: 10, Addressed: false}
			json.NewEncoder(w).Encode(review)
		case r.URL.Path == "/api/review/address" && r.Method == http.MethodPost:
			// Validate request body before returning error
			var req addressRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("Failed to decode request body: %v", err)
			}
			if req.ReviewID != 10 {
				t.Errorf("Expected review_id=10, got %d", req.ReviewID)
			}
			if req.Addressed != true {
				t.Errorf("Expected addressed=true, got %v", req.Addressed)
			}
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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

func TestTUIHTTPTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay much longer than client timeout to avoid flaky timing on fast machines
		time.Sleep(500 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
	// Override with short timeout for test (10x shorter than server delay)
	m.client.Timeout = 50 * time.Millisecond

	cmd := m.fetchJobs()
	msg := cmd()

	_, ok := msg.(tuiJobsErrMsg)
	if !ok {
		t.Fatalf("Expected tuiJobsErrMsg for timeout, got %T: %v", msg, msg)
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
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 5}, {ID: 4}, {ID: 3}, {ID: 2}, {ID: 1},
	}}

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
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 3}, {ID: 2},
	}}

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
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 5}, {ID: 4}, {ID: 3},
	}}

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

	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{}}

	updated, _ := m.Update(newJobs)
	m = updated.(tuiModel)

	// Empty list should have selectedIdx=-1 (no valid selection)
	if m.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1, got %d", m.selectedIdx)
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
		seq:      1, // Not strictly needed for success (no rollback) but included for consistency
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

	updated, _ := m.Update(tuiJobsMsg{jobs: newJobs})
	m = updated.(tuiModel)

	// Should still follow job ID=1, now at index 30
	if m.selectedJobID != 1 {
		t.Errorf("Expected selectedJobID=1, got %d", m.selectedJobID)
	}
	if m.selectedIdx != 30 {
		t.Errorf("Expected selectedIdx=30 (ID=1 at end), got %d", m.selectedIdx)
	}
}

func TestTUIReviewViewAddressedRollbackOnError(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with review view showing an unaddressed review
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:       42,
		Addressed: false,
		Job:      &storage.ReviewJob{ID: 100},
	}

	// Simulate optimistic update (what happens when 'a' is pressed in review view)
	m.currentReview.Addressed = true
	m.pendingAddressed[100] = pendingState{newState: true, seq: 1} // Track pending state

	// Error result from server (reviewID must match currentReview.ID for rollback)
	errMsg := tuiAddressedResultMsg{
		reviewID:   42,   // Must match currentReview.ID
		jobID:      100,  // Must match for isCurrentRequest check
		reviewView: true,
		oldState:   false, // Was false before optimistic update
		newState:   true,  // The requested state (matches pendingAddressed)
		seq:        1,     // Must match pending seq to be treated as current
		err:        fmt.Errorf("server error"),
	}

	updated, _ := m.Update(errMsg)
	m = updated.(tuiModel)

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
	m.currentReview = &storage.Review{ID: 42, Addressed: false}

	// Simulate optimistic update
	m.currentReview.Addressed = true

	// Success result (err is nil)
	successMsg := tuiAddressedResultMsg{
		reviewView: true,
		oldState:   false,
		seq:        1, // Not strictly needed for success but included for consistency
		err:        nil,
	}

	updated, _ := m.Update(successMsg)
	m = updated.(tuiModel)

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
	m.currentReview = &storage.Review{ID: 42, Addressed: false, Job: &storage.ReviewJob{ID: 100}}
	m.currentReview.Addressed = true  // Optimistic update to review
	*m.jobs[0].Addressed = true       // Optimistic update to job in queue
	m.pendingAddressed[100] = pendingState{newState: true, seq: 1}    // Track pending state for job A

	// User navigates to review B before error response arrives
	m.currentReview = &storage.Review{ID: 99, Addressed: false, Job: &storage.ReviewJob{ID: 200}}

	// Error arrives for review A's toggle
	errMsg := tuiAddressedResultMsg{
		reviewID:   42,    // Review A
		jobID:      100,   // Job A
		reviewView: true,
		oldState:   false,
		newState:   true,  // The requested state (matches pendingAddressed)
		seq:        1,     // Must match pending seq to be treated as current
		err:        fmt.Errorf("server error"),
	}

	updated, _ := m.Update(errMsg)
	m = updated.(tuiModel)

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

func TestTUISetJobAddressedHelper(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Test with nil Addressed pointer - should allocate
	m.jobs = []storage.ReviewJob{
		{ID: 100, Status: storage.JobStatusDone, Addressed: nil},
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

func TestTUIReviewViewToggleSyncsQueueJob(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Setup: job in queue with addressed=false
	addr := false
	m.jobs = []storage.ReviewJob{
		{ID: 100, Status: storage.JobStatusDone, Addressed: &addr},
	}

	// User views review for job 100 and presses 'a'
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:        42,
		Addressed: false,
		Job:       &storage.ReviewJob{ID: 100},
	}

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

func TestTUICancelJobSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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

	updated, _ := m.Update(errResult)
	m2 := updated.(tuiModel)

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

	updated, _ := m.Update(errResult)
	m2 := updated.(tuiModel)

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
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m2 := updated.(tuiModel)

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
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			m2 := updated.(tuiModel)

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

func TestTUIFilterOpenModal(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a"},
		{ID: 2, RepoName: "repo-b"},
		{ID: 3, RepoName: "repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Press 'f' to open filter modal
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m2 := updated.(tuiModel)

	if m2.currentView != tuiViewFilter {
		t.Errorf("Expected tuiViewFilter, got %d", m2.currentView)
	}
	// filterRepos should be nil (loading state) until async fetch completes
	if m2.filterRepos != nil {
		t.Errorf("Expected filterRepos=nil (loading), got %d repos", len(m2.filterRepos))
	}
	if m2.filterSelectedIdx != 0 {
		t.Errorf("Expected filterSelectedIdx=0 (All repos), got %d", m2.filterSelectedIdx)
	}
	if m2.filterSearch != "" {
		t.Errorf("Expected empty filterSearch, got '%s'", m2.filterSearch)
	}
	if cmd == nil {
		t.Error("Expected a fetch command to be returned")
	}
}

func TestTUIFilterReposMsg(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter

	// Simulate receiving repos from API
	repos := []repoFilterItem{
		{name: "repo-a", count: 2},
		{name: "repo-b", count: 1},
		{name: "repo-c", count: 1},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 4}

	updated, _ := m.Update(msg)
	m2 := updated.(tuiModel)

	// Should have: All repos (prepended), then the 3 repos from API
	if len(m2.filterRepos) != 4 {
		t.Fatalf("Expected 4 filter repos, got %d", len(m2.filterRepos))
	}
	if m2.filterRepos[0].name != "" || m2.filterRepos[0].count != 4 {
		t.Errorf("Expected All repos with count 4, got name='%s' count=%d", m2.filterRepos[0].name, m2.filterRepos[0].count)
	}
	if m2.filterRepos[1].name != "repo-a" || m2.filterRepos[1].count != 2 {
		t.Errorf("Expected repo-a with count 2, got name='%s' count=%d", m2.filterRepos[1].name, m2.filterRepos[1].count)
	}
	if m2.filterRepos[2].name != "repo-b" || m2.filterRepos[2].count != 1 {
		t.Errorf("Expected repo-b with count 1, got name='%s' count=%d", m2.filterRepos[2].name, m2.filterRepos[2].count)
	}
	if m2.filterRepos[3].name != "repo-c" || m2.filterRepos[3].count != 1 {
		t.Errorf("Expected repo-c with count 1, got name='%s' count=%d", m2.filterRepos[3].name, m2.filterRepos[3].count)
	}
}

func TestTUIFilterSearch(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.filterRepos = []repoFilterItem{
		{name: "", count: 10},
		{name: "repo-alpha", count: 5},
		{name: "repo-beta", count: 3},
		{name: "something-else", count: 2},
	}

	// No search - all visible
	visible := m.getVisibleFilterRepos()
	if len(visible) != 4 {
		t.Errorf("No search: expected 4 visible, got %d", len(visible))
	}

	// Search for "repo"
	m.filterSearch = "repo"
	visible = m.getVisibleFilterRepos()
	if len(visible) != 3 { // All repos + repo-alpha + repo-beta
		t.Errorf("Search 'repo': expected 3 visible, got %d", len(visible))
	}

	// Search for "alpha"
	m.filterSearch = "alpha"
	visible = m.getVisibleFilterRepos()
	if len(visible) != 2 { // All repos + repo-alpha
		t.Errorf("Search 'alpha': expected 2 visible, got %d", len(visible))
	}

	// Search for "xyz" - no matches
	m.filterSearch = "xyz"
	visible = m.getVisibleFilterRepos()
	if len(visible) != 1 { // Only "All repos" always included
		t.Errorf("Search 'xyz': expected 1 visible (All repos), got %d", len(visible))
	}
}

func TestTUIFilterNavigation(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterRepos = []repoFilterItem{
		{name: "", count: 10},
		{name: "repo-a", count: 5},
		{name: "repo-b", count: 3},
	}
	m.filterSelectedIdx = 0

	// Navigate down
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)
	if m2.filterSelectedIdx != 1 {
		t.Errorf("j key: expected filterSelectedIdx=1, got %d", m2.filterSelectedIdx)
	}

	// Navigate down again
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m3 := updated.(tuiModel)
	if m3.filterSelectedIdx != 2 {
		t.Errorf("j key: expected filterSelectedIdx=2, got %d", m3.filterSelectedIdx)
	}

	// Navigate down at boundary - should stay at 2
	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m4 := updated.(tuiModel)
	if m4.filterSelectedIdx != 2 {
		t.Errorf("j key at boundary: expected filterSelectedIdx=2, got %d", m4.filterSelectedIdx)
	}

	// Navigate up
	updated, _ = m4.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m5 := updated.(tuiModel)
	if m5.filterSelectedIdx != 1 {
		t.Errorf("k key: expected filterSelectedIdx=1, got %d", m5.filterSelectedIdx)
	}
}

func TestTUIFilterSelectRepo(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a"},
		{ID: 2, RepoName: "repo-b"},
		{ID: 3, RepoName: "repo-a"},
	}
	m.currentView = tuiViewFilter
	m.filterRepos = []repoFilterItem{
		{name: "", rootPaths: nil, count: 3},
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 2},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 1},
	}
	m.filterSelectedIdx = 1 // repo-a

	// Press enter to select
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(tuiModel)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if len(m2.activeRepoFilter) != 1 || m2.activeRepoFilter[0] != "/path/to/repo-a" {
		t.Errorf("Expected activeRepoFilter=['/path/to/repo-a'], got %v", m2.activeRepoFilter)
	}
	// Selection is invalidated until refetch completes (prevents race condition)
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterClearWithEsc(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}

	// Press Esc to clear filter
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	// Selection is invalidated until refetch completes (prevents race condition)
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterClearWithEscLayered(t *testing.T) {
	// Test that escape clears filters one layer at a time:
	// 1. First escape clears project filter (keeps hide-addressed)
	// 2. Second escape clears hide-addressed
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.hideAddressed = true

	// First Esc: clear project filter, keep hide-addressed
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if !m2.hideAddressed {
		t.Error("Expected hideAddressed to remain true after first escape")
	}

	// Second Esc: clear hide-addressed
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := updated.(tuiModel)

	if m3.hideAddressed {
		t.Error("Expected hideAddressed to be false after second escape")
	}
}

func TestTUIFilterClearHideAddressedOnly(t *testing.T) {
	// Test that escape clears hide-addressed when no project filter is active
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	// No project filter active

	// Esc should clear hide-addressed
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

	if m2.hideAddressed {
		t.Error("Expected hideAddressed to be false after escape")
	}
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 (invalidated pending refetch), got %d", m2.selectedIdx)
	}
}

func TestTUIFilterEscapeWhileLoadingQueuesPendingRefetch(t *testing.T) {
	// Test that escape while loading sets pendingRefetch instead of starting new fetch
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.loadingJobs = true // Already loading

	// Press Esc while loading - should set pendingRefetch, not start new fetch
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if !m2.pendingRefetch {
		t.Error("Expected pendingRefetch to be true when escape pressed while loading")
	}
	if cmd != nil {
		t.Error("Expected no command when escape pressed while loading (should queue refetch instead)")
	}

	// Simulate jobs fetch completing - should trigger another fetch
	updated, cmd = m2.Update(tuiJobsMsg{jobs: []storage.ReviewJob{{ID: 2}}, hasMore: false})
	m3 := updated.(tuiModel)

	if m3.pendingRefetch {
		t.Error("Expected pendingRefetch to be cleared after jobs message")
	}
	if !m3.loadingJobs {
		t.Error("Expected loadingJobs to be true (refetch triggered)")
	}
	if cmd == nil {
		t.Error("Expected a fetch command after jobs message with pendingRefetch")
	}
}

func TestTUIFilterEscapeWhileLoadingErrorTriggersRefetch(t *testing.T) {
	// Test that pendingRefetch triggers a retry even when fetch fails
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.loadingJobs = true
	m.pendingRefetch = true // Simulates escape pressed while loading

	// Simulate jobs fetch failing
	updated, cmd := m.Update(tuiJobsErrMsg{err: fmt.Errorf("network error")})
	m2 := updated.(tuiModel)

	if m2.pendingRefetch {
		t.Error("Expected pendingRefetch to be cleared after error")
	}
	if !m2.loadingJobs {
		t.Error("Expected loadingJobs to be true (refetch triggered after error)")
	}
	if cmd == nil {
		t.Error("Expected a fetch command after error with pendingRefetch")
	}
	if m2.err == nil {
		t.Error("Expected error to be set")
	}
}

func TestTUIFilterEscapeWhilePaginationDiscardsAppend(t *testing.T) {
	// Test that escape while pagination is in flight queues refetch and
	// discards stale append response
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.loadingMore = true  // Pagination in flight
	m.loadingJobs = false // Not a full refresh

	// Press Esc while pagination loading - should set pendingRefetch
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

	if len(m2.activeRepoFilter) != 0 {
		t.Errorf("Expected activeRepoFilter to be cleared, got %v", m2.activeRepoFilter)
	}
	if !m2.pendingRefetch {
		t.Error("Expected pendingRefetch to be true when escape pressed during pagination")
	}
	if cmd != nil {
		t.Error("Expected no command when escape pressed while pagination in flight")
	}

	// Simulate stale pagination response arriving - should be discarded and trigger fresh fetch
	updated, cmd = m2.Update(tuiJobsMsg{
		jobs:    []storage.ReviewJob{{ID: 99, RepoName: "stale"}},
		hasMore: true,
		append:  true, // This is a pagination append
	})
	m3 := updated.(tuiModel)

	if m3.pendingRefetch {
		t.Error("Expected pendingRefetch to be cleared")
	}
	if !m3.loadingJobs {
		t.Error("Expected loadingJobs to be true (fresh fetch triggered)")
	}
	if cmd == nil {
		t.Error("Expected a fetch command after stale append discarded")
	}
	// Stale data should NOT have been appended
	if len(m3.jobs) > 0 {
		for _, job := range m3.jobs {
			if job.ID == 99 {
				t.Error("Stale pagination data should have been discarded, not appended")
			}
		}
	}
}

func TestTUIFilterEscapeWhilePaginationErrorTriggersRefetch(t *testing.T) {
	// Test that pendingRefetch triggers a retry when pagination fails
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.loadingMore = true
	m.loadingJobs = false
	m.pendingRefetch = true // Simulates escape pressed while pagination loading

	// Simulate pagination fetch failing
	updated, cmd := m.Update(tuiPaginationErrMsg{err: fmt.Errorf("network error")})
	m2 := updated.(tuiModel)

	if m2.pendingRefetch {
		t.Error("Expected pendingRefetch to be cleared after pagination error")
	}
	if !m2.loadingJobs {
		t.Error("Expected loadingJobs to be true (refetch triggered after pagination error)")
	}
	if m2.loadingMore {
		t.Error("Expected loadingMore to be false after pagination error")
	}
	if cmd == nil {
		t.Error("Expected a fetch command after pagination error with pendingRefetch")
	}
	if m2.err == nil {
		t.Error("Expected error to be set")
	}
}

func TestTUIFilterEscapeCloses(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterSearch = "test"
	m.filterRepos = []repoFilterItem{{name: "", count: 1}}

	// Press 'esc' to close without selecting
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected tuiViewQueue, got %d", m2.currentView)
	}
	if m2.filterSearch != "" {
		t.Errorf("Expected filterSearch to be cleared, got '%s'", m2.filterSearch)
	}
}

func TestTUIFilterTypingSearch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterRepos = []repoFilterItem{
		{name: "", count: 10},
		{name: "repo-a", count: 5},
	}
	m.filterSelectedIdx = 1

	// Type 'a'
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := updated.(tuiModel)

	if m2.filterSearch != "a" {
		t.Errorf("Expected filterSearch='a', got '%s'", m2.filterSearch)
	}
	if m2.filterSelectedIdx != 0 {
		t.Errorf("Expected filterSelectedIdx reset to 0, got %d", m2.filterSelectedIdx)
	}

	// Type 'b'
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m3 := updated.(tuiModel)

	if m3.filterSearch != "ab" {
		t.Errorf("Expected filterSearch='ab', got '%s'", m3.filterSearch)
	}

	// Backspace
	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m4 := updated.(tuiModel)

	if m4.filterSearch != "a" {
		t.Errorf("Expected filterSearch='a' after backspace, got '%s'", m4.filterSearch)
	}
}

func TestTUIQueueNavigationWithFilter(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Jobs from two repos, interleaved
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
		{ID: 3, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 4, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
		{ID: 5, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.activeRepoFilter = []string{"/path/to/repo-a"} // Filter to only repo-a jobs

	// Navigate down - should skip repo-b jobs
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

	// Should jump from ID=1 (idx 0) to ID=3 (idx 2), skipping ID=2 (repo-b)
	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	// Navigate down again - should go to ID=5
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m3 := updated.(tuiModel)

	if m3.selectedIdx != 4 {
		t.Errorf("Expected selectedIdx=4, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 5 {
		t.Errorf("Expected selectedJobID=5, got %d", m3.selectedJobID)
	}

	// Navigate up - should go back to ID=3
	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m4 := updated.(tuiModel)

	if m4.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m4.selectedIdx)
	}
}

func TestTUIGetVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
		{ID: 3, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}

	// No filter - all jobs visible
	visible := m.getVisibleJobs()
	if len(visible) != 3 {
		t.Errorf("No filter: expected 3 visible, got %d", len(visible))
	}

	// Filter to repo-a
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	visible = m.getVisibleJobs()
	if len(visible) != 2 {
		t.Errorf("Filter repo-a: expected 2 visible, got %d", len(visible))
	}
	if visible[0].ID != 1 || visible[1].ID != 3 {
		t.Errorf("Expected IDs 1 and 3, got %d and %d", visible[0].ID, visible[1].ID)
	}

	// Filter to non-existent repo
	m.activeRepoFilter = []string{"/path/to/repo-xyz"}
	visible = m.getVisibleJobs()
	if len(visible) != 0 {
		t.Errorf("Filter repo-xyz: expected 0 visible, got %d", len(visible))
	}
}

func TestTUIGetVisibleSelectedIdx(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
		{ID: 3, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}

	// No filter, valid selection
	m.selectedIdx = 1
	if idx := m.getVisibleSelectedIdx(); idx != 1 {
		t.Errorf("No filter, selectedIdx=1: expected 1, got %d", idx)
	}

	// No filter, selectedIdx=-1 returns -1
	m.selectedIdx = -1
	if idx := m.getVisibleSelectedIdx(); idx != -1 {
		t.Errorf("No filter, selectedIdx=-1: expected -1, got %d", idx)
	}

	// With filter, selectedIdx=-1 returns -1
	m.activeRepoFilter = []string{"/path/to/repo-a"}
	m.selectedIdx = -1
	if idx := m.getVisibleSelectedIdx(); idx != -1 {
		t.Errorf("Filter active, selectedIdx=-1: expected -1, got %d", idx)
	}

	// With filter, selection matches visible job (job ID=3 is second visible in repo-a)
	m.selectedIdx = 2 // index in m.jobs for job ID=3
	if idx := m.getVisibleSelectedIdx(); idx != 1 {
		t.Errorf("Filter active, selectedIdx=2 (ID=3): expected visible idx 1, got %d", idx)
	}

	// With filter, selection doesn't match filter - returns -1
	m.selectedIdx = 1 // index in m.jobs for job ID=2 (repo-b, not visible)
	if idx := m.getVisibleSelectedIdx(); idx != -1 {
		t.Errorf("Filter active, selection not visible: expected -1, got %d", idx)
	}
}

func TestTUIJobsRefreshWithFilter(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with filter active
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
		{ID: 3, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.selectedIdx = 2
	m.selectedJobID = 3
	m.activeRepoFilter = []string{"/path/to/repo-a"}

	// Jobs refresh - same jobs
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
		{ID: 3, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}}

	updated, _ := m.Update(newJobs)
	m2 := updated.(tuiModel)

	// Selection should be maintained
	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	// Now the selected job is removed
	newJobs = tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-b", RepoPath: "/path/to/repo-b"},
	}}

	updated, _ = m2.Update(newJobs)
	m3 := updated.(tuiModel)

	// Should select first visible job (ID=1, repo-a)
	if m3.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx=0, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 1 {
		t.Errorf("Expected selectedJobID=1, got %d", m3.selectedJobID)
	}
}

func TestTUIFilterPreselectsCurrent(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.activeRepoFilter = []string{"/path/to/repo-b"} // Already filtering to repo-b

	// Simulate receiving repos from API (should pre-select repo-b)
	repos := []repoFilterItem{
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 1},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 1},
	}
	msg := tuiReposMsg{repos: repos, totalCount: 2}

	updated, _ := m.Update(msg)
	m2 := updated.(tuiModel)

	// filterRepos should be: All repos, repo-a, repo-b
	// repo-b should be at index 2, which should be pre-selected
	if m2.filterSelectedIdx != 2 {
		t.Errorf("Expected filterSelectedIdx=2 (repo-b), got %d", m2.filterSelectedIdx)
	}
}

func TestTUIFilterToZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Jobs only in repo-a
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewFilter
	m.filterRepos = []repoFilterItem{
		{name: "", rootPaths: nil, count: 2},
		{name: "repo-a", rootPaths: []string{"/path/to/repo-a"}, count: 2},
		{name: "repo-b", rootPaths: []string{"/path/to/repo-b"}, count: 0}, // No jobs
	}
	m.filterSelectedIdx = 2 // Select repo-b

	// Press enter to select repo-b (triggers refetch)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(tuiModel)

	// Filter should be applied and a fetchJobs command should be returned
	if len(m2.activeRepoFilter) != 1 || m2.activeRepoFilter[0] != "/path/to/repo-b" {
		t.Errorf("Expected activeRepoFilter=['/path/to/repo-b'], got %v", m2.activeRepoFilter)
	}
	if cmd == nil {
		t.Error("Expected fetchJobs command to be returned")
	}
	// Selection is invalidated until refetch completes (prevents race condition)
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 pending refetch, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 pending refetch, got %d", m2.selectedJobID)
	}

	// Simulate receiving empty jobs from API (repo-b has no jobs)
	updated2, _ := m2.Update(tuiJobsMsg{jobs: []storage.ReviewJob{}})
	m3 := updated2.(tuiModel)

	// Now selection should be cleared since no jobs
	if m3.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 after receiving empty jobs, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 after receiving empty jobs, got %d", m3.selectedJobID)
	}
}

func TestTUIFilterAggregatedDisplayName(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Jobs from two repos that share a display name
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "backend-dev", RepoPath: "/path/to/backend-dev", Status: storage.JobStatusDone},
		{ID: 2, RepoName: "backend-prod", RepoPath: "/path/to/backend-prod", Status: storage.JobStatusDone},
		{ID: 3, RepoName: "frontend", RepoPath: "/path/to/frontend", Status: storage.JobStatusFailed},
	}
	m.currentView = tuiViewFilter
	// Aggregated group: "backend" covers both backend-dev and backend-prod
	m.filterRepos = []repoFilterItem{
		{name: "", rootPaths: nil, count: 3},
		{name: "backend", rootPaths: []string{"/path/to/backend-dev", "/path/to/backend-prod"}, count: 2},
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
	}
	m.filterSelectedIdx = 1 // Select "backend" group

	// Press enter to select
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(tuiModel)

	// Should have both paths in the filter
	if len(m2.activeRepoFilter) != 2 {
		t.Errorf("Expected 2 paths in activeRepoFilter, got %d", len(m2.activeRepoFilter))
	}

	// Both backend repos should be visible
	if !m2.repoMatchesFilter("/path/to/backend-dev") {
		t.Error("Expected backend-dev to match filter")
	}
	if !m2.repoMatchesFilter("/path/to/backend-prod") {
		t.Error("Expected backend-prod to match filter")
	}
	// Frontend should not be visible
	if m2.repoMatchesFilter("/path/to/frontend") {
		t.Error("Expected frontend to NOT match filter")
	}
}

func TestTUIFilterSearchByRepoPath(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.filterRepos = []repoFilterItem{
		{name: "", rootPaths: nil, count: 3},
		{name: "backend", rootPaths: []string{"/path/to/backend-dev", "/path/to/backend-prod"}, count: 2},
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
	}

	// Search by underlying repo path basename (not display name)
	m.filterSearch = "backend-dev"
	visible := m.getVisibleFilterRepos()

	// Should find the "backend" group (contains backend-dev path)
	if len(visible) != 2 { // "All repos" + "backend"
		t.Errorf("Expected 2 visible repos (All + backend), got %d", len(visible))
	}
	if visible[1].name != "backend" {
		t.Errorf("Expected to find 'backend' group, got '%s'", visible[1].name)
	}
}

func TestTUIFilterSearchByDisplayName(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.filterRepos = []repoFilterItem{
		{name: "", rootPaths: nil, count: 5},
		// Display name "My Project" differs from path basename "my-project-repo"
		{name: "My Project", rootPaths: []string{"/home/user/my-project-repo"}, count: 2},
		// Display name matches path basename
		{name: "frontend", rootPaths: []string{"/path/to/frontend"}, count: 1},
		// Display name "Backend Services" differs from path basenames
		{name: "Backend Services", rootPaths: []string{"/srv/api-server", "/srv/worker-daemon"}, count: 2},
	}

	// Search by display name (should match "My Project")
	m.filterSearch = "my project"
	visible := m.getVisibleFilterRepos()
	if len(visible) != 2 { // "All repos" + "My Project"
		t.Errorf("Search 'my project': expected 2 visible, got %d", len(visible))
	}
	if len(visible) > 1 && visible[1].name != "My Project" {
		t.Errorf("Expected to find 'My Project', got '%s'", visible[1].name)
	}

	// Search by raw repo path basename (should still match "My Project")
	m.filterSearch = "my-project-repo"
	visible = m.getVisibleFilterRepos()
	if len(visible) != 2 { // "All repos" + "My Project"
		t.Errorf("Search 'my-project-repo': expected 2 visible, got %d", len(visible))
	}
	if len(visible) > 1 && visible[1].name != "My Project" {
		t.Errorf("Expected to find 'My Project' via path, got '%s'", visible[1].name)
	}

	// Search by partial display name (should match "Backend Services")
	m.filterSearch = "backend"
	visible = m.getVisibleFilterRepos()
	if len(visible) != 2 { // "All repos" + "Backend Services"
		t.Errorf("Search 'backend': expected 2 visible, got %d", len(visible))
	}
	if len(visible) > 1 && visible[1].name != "Backend Services" {
		t.Errorf("Expected to find 'Backend Services', got '%s'", visible[1].name)
	}

	// Search by path basename of grouped repo (should match "Backend Services")
	m.filterSearch = "api-server"
	visible = m.getVisibleFilterRepos()
	if len(visible) != 2 { // "All repos" + "Backend Services"
		t.Errorf("Search 'api-server': expected 2 visible, got %d", len(visible))
	}
	if len(visible) > 1 && visible[1].name != "Backend Services" {
		t.Errorf("Expected to find 'Backend Services' via path, got '%s'", visible[1].name)
	}
}

func TestTUIMultiPathFilterStatusCounts(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.height = 20
	m.daemonVersion = "test"

	// Jobs from multiple repos
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoPath: "/path/to/backend-dev", Status: storage.JobStatusDone},
		{ID: 2, RepoPath: "/path/to/backend-prod", Status: storage.JobStatusDone},
		{ID: 3, RepoPath: "/path/to/backend-prod", Status: storage.JobStatusFailed},
		{ID: 4, RepoPath: "/path/to/frontend", Status: storage.JobStatusDone},
		{ID: 5, RepoPath: "/path/to/frontend", Status: storage.JobStatusCanceled},
	}

	// Multi-path filter (backend group)
	m.activeRepoFilter = []string{"/path/to/backend-dev", "/path/to/backend-prod"}

	output := m.renderQueueView()

	// Status line should show counts only for backend repos (2 done, 1 failed, 0 canceled)
	// Not frontend (1 done, 1 canceled)
	if !strings.Contains(output, "Done: 2") {
		t.Errorf("Expected status to show 'Done: 2' for filtered repos, got: %s", output)
	}
	if !strings.Contains(output, "Failed: 1") {
		t.Errorf("Expected status to show 'Failed: 1' for filtered repos, got: %s", output)
	}
	if !strings.Contains(output, "Canceled: 0") {
		t.Errorf("Expected status to show 'Canceled: 0' for filtered repos, got: %s", output)
	}
}

func TestTUIRefreshWithZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Start with jobs in repo-a, filter active for repo-b
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	m.activeRepoFilter = []string{"/path/to/repo-b"} // Filter to repo with no jobs
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Simulate jobs refresh
	newJobs := []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
		{ID: 2, RepoName: "repo-a", RepoPath: "/path/to/repo-a"},
	}
	updated, _ := m.Update(tuiJobsMsg{jobs: newJobs})
	m2 := updated.(tuiModel)

	// Selection should be cleared since no jobs match filter
	if m2.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1 for zero visible jobs after refresh, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0 for zero visible jobs after refresh, got %d", m2.selectedJobID)
	}
}

func TestTUIActionsNoOpWithZeroVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Setup: filter active with no matching jobs
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoName: "repo-a", RepoPath: "/path/to/repo-a", Status: storage.JobStatusDone},
	}
	m.activeRepoFilter = []string{"/path/to/repo-b"}
	m.selectedIdx = -1
	m.selectedJobID = 0
	m.currentView = tuiViewQueue

	// Press enter - should be no-op
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(tuiModel)
	if cmd != nil {
		t.Error("Expected no command for enter with no visible jobs")
	}
	if m2.currentView != tuiViewQueue {
		t.Errorf("Expected to stay in queue view, got %d", m2.currentView)
	}

	// Press 'x' (cancel) - should be no-op
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Error("Expected no command for cancel with no visible jobs")
	}

	// Press 'a' (address) - should be no-op
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd != nil {
		t.Error("Expected no command for address with no visible jobs")
	}
}

func TestTUIFilterViewSmallTerminal(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterRepos = []repoFilterItem{
		{name: "", count: 10},
		{name: "repo-a", count: 5},
		{name: "repo-b", count: 3},
		{name: "repo-c", count: 2},
	}
	m.filterSelectedIdx = 0

	t.Run("tiny terminal shows message", func(t *testing.T) {
		m.height = 5 // Less than reservedLines (7)
		output := m.renderFilterView()

		if !strings.Contains(output, "(terminal too small)") {
			t.Errorf("Expected 'terminal too small' message for height=5, got: %s", output)
		}
		// Should not contain any repo names
		if strings.Contains(output, "repo-a") {
			t.Error("Should not render repo names when terminal too small")
		}
	})

	t.Run("exactly reservedLines shows no repos", func(t *testing.T) {
		m.height = 7 // Exactly reservedLines, visibleRows = 0
		output := m.renderFilterView()

		if !strings.Contains(output, "(terminal too small)") {
			t.Errorf("Expected 'terminal too small' message for height=7, got: %s", output)
		}
	})

	t.Run("one row available", func(t *testing.T) {
		m.height = 8 // reservedLines + 1 = visibleRows of 1
		output := m.renderFilterView()

		if strings.Contains(output, "(terminal too small)") {
			t.Error("Should not show 'terminal too small' when 1 row available")
		}
		// Should show exactly one repo line (All repos)
		if !strings.Contains(output, "All repos") {
			t.Error("Should show 'All repos' when 1 row available")
		}
		// Should show scroll info since 4 repos > 1 visible row
		if !strings.Contains(output, "[showing 1-1 of 4]") {
			t.Errorf("Expected scroll info '[showing 1-1 of 4]', got: %s", output)
		}
	})

	t.Run("fits all repos without scroll", func(t *testing.T) {
		m.height = 15 // reservedLines(7) + 8 = visibleRows of 8, enough for 4 repos
		output := m.renderFilterView()

		// Should show all repos
		if !strings.Contains(output, "All repos") {
			t.Error("Should show 'All repos'")
		}
		if !strings.Contains(output, "repo-a") {
			t.Error("Should show 'repo-a'")
		}
		if !strings.Contains(output, "repo-c") {
			t.Error("Should show 'repo-c'")
		}
		// Should NOT show scroll info
		if strings.Contains(output, "[showing") {
			t.Error("Should not show scroll info when all repos fit")
		}
	})

	t.Run("needs scrolling shows scroll info", func(t *testing.T) {
		m.height = 9  // visibleRows = 2
		m.filterSelectedIdx = 2 // Select repo-b
		output := m.renderFilterView()

		// Should show scroll info
		if !strings.Contains(output, "[showing") {
			t.Error("Expected scroll info when repos exceed visible rows")
		}
		// Selected item (repo-b) should be visible
		if !strings.Contains(output, "repo-b") {
			t.Error("Selected repo should be visible in scroll window")
		}
	})
}

func TestTUIFilterViewScrollWindow(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterRepos = []repoFilterItem{
		{name: "", count: 20},
		{name: "repo-1", count: 5},
		{name: "repo-2", count: 4},
		{name: "repo-3", count: 3},
		{name: "repo-4", count: 2},
		{name: "repo-5", count: 1},
	}
	m.height = 10 // visibleRows = 3

	t.Run("scroll keeps selected item visible at top", func(t *testing.T) {
		m.filterSelectedIdx = 0
		output := m.renderFilterView()

		if !strings.Contains(output, "[showing 1-3 of 6]") {
			t.Errorf("Expected '[showing 1-3 of 6]' for top selection, got: %s", output)
		}
	})

	t.Run("scroll keeps selected item visible at bottom", func(t *testing.T) {
		m.filterSelectedIdx = 5 // repo-5
		output := m.renderFilterView()

		if !strings.Contains(output, "[showing 4-6 of 6]") {
			t.Errorf("Expected '[showing 4-6 of 6]' for bottom selection, got: %s", output)
		}
		if !strings.Contains(output, "repo-5") {
			t.Error("repo-5 should be visible when selected")
		}
	})

	t.Run("scroll centers selected item in middle", func(t *testing.T) {
		m.filterSelectedIdx = 3 // repo-3
		output := m.renderFilterView()

		// With 3 visible rows and selecting item 3 (0-indexed), centering puts start at 2
		if !strings.Contains(output, "repo-3") {
			t.Error("repo-3 should be visible when selected")
		}
	})
}

// Tests for j/k and left/right review navigation

func TestTUIReviewNavigationJNext(t *testing.T) {
	// Test 'j' navigates to next viewable job (higher index) in review view
	m := newTuiModel("http://localhost")

	// Setup: 5 jobs, middle ones are queued/running (not viewable)
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusQueued},
		{ID: 3, Status: storage.JobStatusRunning},
		{ID: 4, Status: storage.JobStatusFailed},
		{ID: 5, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 10, Job: &storage.ReviewJob{ID: 1}}
	m.reviewScroll = 5 // Ensure scroll resets

	// Press 'j' - should skip to job 4 (failed, viewable)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 3 {
		t.Errorf("Expected selectedIdx=3 (job ID=4), got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 4 {
		t.Errorf("Expected selectedJobID=4, got %d", m2.selectedJobID)
	}
	if m2.reviewScroll != 0 {
		t.Errorf("Expected reviewScroll to reset to 0, got %d", m2.reviewScroll)
	}
	// For failed jobs, currentReview is set inline (no fetch command)
	if m2.currentReview == nil {
		t.Error("Expected currentReview to be set for failed job")
	}
	if cmd != nil {
		t.Error("Expected no command for failed job (inline display)")
	}
}

func TestTUIReviewNavigationKPrev(t *testing.T) {
	// Test 'k' navigates to previous viewable job (lower index) in review view
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusQueued},
		{ID: 3, Status: storage.JobStatusRunning},
		{ID: 4, Status: storage.JobStatusFailed},
		{ID: 5, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 4
	m.selectedJobID = 5
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 50, Job: &storage.ReviewJob{ID: 5}}
	m.reviewScroll = 10

	// Press 'k' - should skip to job 4 (failed, viewable)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 3 {
		t.Errorf("Expected selectedIdx=3 (job ID=4), got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 4 {
		t.Errorf("Expected selectedJobID=4, got %d", m2.selectedJobID)
	}
	if m2.reviewScroll != 0 {
		t.Errorf("Expected reviewScroll to reset to 0, got %d", m2.reviewScroll)
	}
}

func TestTUIReviewNavigationLeftRight(t *testing.T) {
	// Test left/right arrows mirror j/k in review view
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 20, Job: &storage.ReviewJob{ID: 2}}

	// Press 'left' - should navigate to next (higher index), like 'j'
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 2 {
		t.Errorf("Left arrow: expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("Left arrow: expected selectedJobID=3, got %d", m2.selectedJobID)
	}
	// Should trigger fetch for done job
	if cmd == nil {
		t.Error("Left arrow: expected fetch command for done job")
	}

	// Reset and test 'right' - should navigate to prev (lower index), like 'k'
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentReview = &storage.Review{ID: 20, Job: &storage.ReviewJob{ID: 2}}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m3 := updated.(tuiModel)

	if m3.selectedIdx != 0 {
		t.Errorf("Right arrow: expected selectedIdx=0, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 1 {
		t.Errorf("Right arrow: expected selectedJobID=1, got %d", m3.selectedJobID)
	}
	if cmd == nil {
		t.Error("Right arrow: expected fetch command for done job")
	}
}

func TestTUIReviewNavigationBoundaries(t *testing.T) {
	// Test navigation at boundaries (first/last viewable job)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusQueued}, // Not viewable
		{ID: 3, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 10, Job: &storage.ReviewJob{ID: 1}}

	// Press 'k' (right) at first viewable job - should show flash message
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx to remain 0 at boundary, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 1 {
		t.Errorf("Expected selectedJobID to remain 1 at boundary, got %d", m2.selectedJobID)
	}
	if cmd != nil {
		t.Error("Expected no command at boundary")
	}
	if m2.flashMessage != "No newer review" {
		t.Errorf("Expected flash message 'No newer review', got %q", m2.flashMessage)
	}
	if m2.flashView != tuiViewReview {
		t.Errorf("Expected flashView to be tuiViewReview, got %d", m2.flashView)
	}
	if m2.flashExpiresAt.IsZero() || m2.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m2.flashExpiresAt)
	}

	// Now at last viewable job
	m.selectedIdx = 2
	m.selectedJobID = 3
	m.currentReview = &storage.Review{ID: 30, Job: &storage.ReviewJob{ID: 3}}

	// Press 'j' (left) at last viewable job - should show flash message
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m3 := updated.(tuiModel)

	if m3.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx to remain 2 at boundary, got %d", m3.selectedIdx)
	}
	if m3.selectedJobID != 3 {
		t.Errorf("Expected selectedJobID to remain 3 at boundary, got %d", m3.selectedJobID)
	}
	if cmd != nil {
		t.Error("Expected no command at boundary")
	}
	if m3.flashMessage != "No older review" {
		t.Errorf("Expected flash message 'No older review', got %q", m3.flashMessage)
	}
	if m3.flashView != tuiViewReview {
		t.Errorf("Expected flashView to be tuiViewReview, got %d", m3.flashView)
	}
	if m3.flashExpiresAt.IsZero() || m3.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m3.flashExpiresAt)
	}
}

func TestTUIReviewNavigationFailedJobInline(t *testing.T) {
	// Test that navigating to a failed job displays error inline
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusFailed, Agent: "codex", Error: "something went wrong"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 10, Job: &storage.ReviewJob{ID: 1}}

	// Press 'j' - should navigate to failed job and display inline
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1, got %d", m2.selectedIdx)
	}
	if m2.currentReview == nil {
		t.Fatal("Expected currentReview to be set for failed job")
	}
	if m2.currentReview.Agent != "codex" {
		t.Errorf("Expected agent='codex', got '%s'", m2.currentReview.Agent)
	}
	if !strings.Contains(m2.currentReview.Output, "something went wrong") {
		t.Errorf("Expected output to contain error, got '%s'", m2.currentReview.Output)
	}
	// No fetch command for failed jobs - displayed inline
	if cmd != nil {
		t.Error("Expected no command for failed job (inline display)")
	}
}

func TestTUIReviewStaleResponseIgnored(t *testing.T) {
	// Test that stale review responses are ignored (race condition fix)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2 // Currently viewing job 2
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 20, Output: "Review for job 2", Job: &storage.ReviewJob{ID: 2}}

	// Simulate a stale response arriving for job 1 (user navigated away)
	staleMsg := tuiReviewMsg{
		review: &storage.Review{ID: 10, Output: "Stale review for job 1", Job: &storage.ReviewJob{ID: 1}},
		jobID:  1, // This doesn't match selectedJobID (2)
	}

	updated, _ := m.Update(staleMsg)
	m2 := updated.(tuiModel)

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
		{ID: 1, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue // Still in queue view, waiting for fetch

	validMsg := tuiReviewMsg{
		review: &storage.Review{ID: 10, Output: "New review", Job: &storage.ReviewJob{ID: 1}},
		jobID:  1,
	}

	updated, _ := m.Update(validMsg)
	m2 := updated.(tuiModel)

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
		{ID: 3, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 1, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 20, Job: &storage.ReviewJob{ID: 2}}

	// New job arrives at the top, shifting indices
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 4, Status: storage.JobStatusDone}, // New job at top
		{ID: 3, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone}, // Now at index 2
		{ID: 1, Status: storage.JobStatusDone},
	}}

	updated, _ := m.Update(newJobs)
	m2 := updated.(tuiModel)

	// selectedIdx should sync with currentReview.Job.ID (2), now at index 2
	if m2.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx=2 (synced with review job), got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2, got %d", m2.selectedJobID)
	}
}

func TestTUIQueueViewNavigationUpDown(t *testing.T) {
	// Test up/down/j/k navigation in queue view
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusQueued},
		{ID: 3, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewQueue

	// 'j' in queue view moves down (higher index)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 2 {
		t.Errorf("j key: expected selectedIdx=2, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 3 {
		t.Errorf("j key: expected selectedJobID=3, got %d", m2.selectedJobID)
	}

	// 'k' in queue view moves up (lower index)
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m3 := updated.(tuiModel)

	if m3.selectedIdx != 1 {
		t.Errorf("k key: expected selectedIdx=1, got %d", m3.selectedIdx)
	}
}

func TestTUIQueueViewArrowsMatchUpDown(t *testing.T) {
	// Test that left/right in queue view work like k/j (up/down)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewQueue

	// 'left' in queue view should move down (like j)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 2 {
		t.Errorf("Left arrow: expected selectedIdx=2, got %d", m2.selectedIdx)
	}

	// Reset
	m2.selectedIdx = 1
	m2.selectedJobID = 2

	// 'right' in queue view should move up (like k)
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRight})
	m3 := updated.(tuiModel)

	if m3.selectedIdx != 0 {
		t.Errorf("Right arrow: expected selectedIdx=0, got %d", m3.selectedIdx)
	}
}

func TestTUIQueueNavigationBoundaries(t *testing.T) {
	// Test flash messages when navigating at queue boundaries
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.hasMore = false // No more jobs to load

	// Press 'up' at top of queue - should show flash message
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m2 := updated.(tuiModel)

	if m2.selectedIdx != 0 {
		t.Errorf("Expected selectedIdx to remain 0 at top, got %d", m2.selectedIdx)
	}
	if m2.flashMessage != "No newer review" {
		t.Errorf("Expected flash message 'No newer review', got %q", m2.flashMessage)
	}
	if m2.flashView != tuiViewQueue {
		t.Errorf("Expected flashView to be tuiViewQueue, got %d", m2.flashView)
	}
	if m2.flashExpiresAt.IsZero() || m2.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m2.flashExpiresAt)
	}

	// Now at bottom of queue
	m.selectedIdx = 2
	m.selectedJobID = 3
	m.flashMessage = "" // Clear

	// Press 'down' at bottom of queue - should show flash message
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m3 := updated.(tuiModel)

	if m3.selectedIdx != 2 {
		t.Errorf("Expected selectedIdx to remain 2 at bottom, got %d", m3.selectedIdx)
	}
	if m3.flashMessage != "No older review" {
		t.Errorf("Expected flash message 'No older review', got %q", m3.flashMessage)
	}
	if m3.flashView != tuiViewQueue {
		t.Errorf("Expected flashView to be tuiViewQueue, got %d", m3.flashView)
	}
	if m3.flashExpiresAt.IsZero() || m3.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m3.flashExpiresAt)
	}
}

func TestTUIQueueNavigationBoundariesWithFilter(t *testing.T) {
	// Test flash messages at bottom when filter is active (prevents auto-load)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, RepoPath: "/repo1"},
		{ID: 2, Status: storage.JobStatusDone, RepoPath: "/repo2"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.hasMore = true // More jobs available but...
	m.activeRepoFilter = []string{"/repo1"} // Filter is active, prevents auto-load

	// Press 'down' - only job 1 matches filter, so we're at bottom
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m2 := updated.(tuiModel)

	// Should show flash since filter prevents loading more
	if m2.flashMessage != "No older review" {
		t.Errorf("Expected flash message 'No older review' with filter active, got %q", m2.flashMessage)
	}
	if m2.flashView != tuiViewQueue {
		t.Errorf("Expected flashView to be tuiViewQueue, got %d", m2.flashView)
	}
	if m2.flashExpiresAt.IsZero() || m2.flashExpiresAt.Before(time.Now()) {
		t.Errorf("Expected flashExpiresAt to be set in the future, got %v", m2.flashExpiresAt)
	}
}

func TestTUIJobsRefreshDuringReviewNavigation(t *testing.T) {
	// Test that jobs refresh during review navigation doesn't reset selection
	// This tests the race condition fix: user navigates to job 3, but jobs refresh
	// arrives before the review loads. Selection should stay on job 3, not revert
	// to the currently displayed review's job (job 2).
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 20, Output: "Review for job 2", Job: &storage.ReviewJob{ID: 2}}

	// Simulate user navigating to next review (job 3)
	// This updates selectedIdx and selectedJobID but doesn't update currentReview yet
	m.selectedIdx = 2
	m.selectedJobID = 3

	// Before the review for job 3 arrives, a jobs refresh comes in
	refreshedJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}}

	updated, _ := m.Update(refreshedJobs)
	m2 := updated.(tuiModel)

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
		review: &storage.Review{ID: 30, Output: "Review for job 3", Job: &storage.ReviewJob{ID: 3}},
		jobID:  3,
	}

	updated, _ = m2.Update(newReviewMsg)
	m3 := updated.(tuiModel)

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
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{ID: 20, Output: "Review for job 2", Job: &storage.ReviewJob{ID: 2}}

	// Transient empty refresh arrives
	emptyJobs := tuiJobsMsg{jobs: []storage.ReviewJob{}}

	updated, _ := m.Update(emptyJobs)
	m2 := updated.(tuiModel)

	// selectedJobID should be preserved (not cleared) while viewing a review
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2 preserved during empty refresh, got %d", m2.selectedJobID)
	}

	// Jobs repopulate
	repopulatedJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}}

	updated, _ = m2.Update(repopulatedJobs)
	m3 := updated.(tuiModel)

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
	m.currentReview = &storage.Review{ID: 20, Output: "Review for job 2", Job: &storage.ReviewJob{ID: 2}}

	// Jobs repopulate
	repopulatedJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
		{ID: 2, Status: storage.JobStatusDone},
		{ID: 3, Status: storage.JobStatusDone},
	}}

	updated, _ := m.Update(repopulatedJobs)
	m2 := updated.(tuiModel)

	// Selection should be seeded from currentReview.Job.ID
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2 (seeded from currentReview), got %d", m2.selectedJobID)
	}
	if m2.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1 (job 2's index), got %d", m2.selectedIdx)
	}
}

func TestTUICalculateColumnWidths(t *testing.T) {
	// Test that column widths fit within terminal for usable sizes (>= 80)
	// Narrower terminals may overflow - users should widen their terminal
	tests := []struct {
		name           string
		termWidth      int
		idWidth        int
		expectOverflow bool // true if overflow is acceptable for this width
	}{
		{
			name:           "wide terminal",
			termWidth:      200,
			idWidth:        3,
			expectOverflow: false,
		},
		{
			name:           "medium terminal",
			termWidth:      100,
			idWidth:        3,
			expectOverflow: false,
		},
		{
			name:           "standard terminal",
			termWidth:      80,
			idWidth:        3,
			expectOverflow: false,
		},
		{
			name:           "narrow terminal - overflow acceptable",
			termWidth:      60,
			idWidth:        3,
			expectOverflow: true, // Fixed columns alone need ~48 chars
		},
		{
			name:           "very narrow terminal - overflow expected",
			termWidth:      40,
			idWidth:        3,
			expectOverflow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tuiModel{width: tt.termWidth}
			widths := m.calculateColumnWidths(tt.idWidth)

			// All columns must have positive widths
			if widths.ref < 1 {
				t.Errorf("ref width %d < 1", widths.ref)
			}
			if widths.repo < 1 {
				t.Errorf("repo width %d < 1", widths.repo)
			}
			if widths.agent < 1 {
				t.Errorf("agent width %d < 1", widths.agent)
			}

			// Fixed widths: ID (idWidth), Status (10), Queued (12), Elapsed (8), Addr'd (6)
			// Plus spacing: 2 (prefix) + 7 spaces between columns
			fixedWidth := 2 + tt.idWidth + 10 + 12 + 8 + 6 + 7
			flexibleTotal := widths.ref + widths.repo + widths.agent
			totalWidth := fixedWidth + flexibleTotal

			if !tt.expectOverflow && totalWidth > tt.termWidth {
				t.Errorf("total width %d exceeds terminal width %d", totalWidth, tt.termWidth)
			}

			// Even with overflow, flexible columns should be minimal
			if tt.expectOverflow && flexibleTotal > 15 {
				t.Errorf("narrow terminal should minimize flexible columns, got %d", flexibleTotal)
			}
		})
	}
}

func TestTUICalculateColumnWidthsProportions(t *testing.T) {
	// On wide terminals, columns should use higher minimums
	m := tuiModel{width: 200}
	widths := m.calculateColumnWidths(3)

	if widths.ref < 10 {
		t.Errorf("wide terminal ref width %d < 10", widths.ref)
	}
	if widths.repo < 15 {
		t.Errorf("wide terminal repo width %d < 15", widths.repo)
	}
	if widths.agent < 10 {
		t.Errorf("wide terminal agent width %d < 10", widths.agent)
	}
}

func TestTUIRenderJobLineTruncation(t *testing.T) {
	m := tuiModel{width: 80}
	// Use a git range - shortRef truncates ranges to 17 chars max, then renderJobLine
	// truncates further based on colWidths.ref. Use a range longer than 17 chars.
	job := storage.ReviewJob{
		ID:         1,
		GitRef:     "abcdef1234567..ghijkl7890123", // 28 char range, shortRef -> 17 chars
		RepoName:   "very-long-repository-name-that-exceeds-width",
		Agent:      "super-long-agent-name",
		Status:     storage.JobStatusDone,
		EnqueuedAt: time.Now(),
	}

	// Use narrow column widths to force truncation
	// ref=10 will truncate the 17-char shortRef output
	colWidths := columnWidths{
		ref:   10,
		repo:  15,
		agent: 10,
	}

	line := m.renderJobLine(job, false, 3, colWidths)

	// Check that truncated values contain "..."
	if !strings.Contains(line, "...") {
		t.Errorf("Expected truncation with '...' in line: %s", line)
	}

	// The line should contain truncated versions, not full strings
	// shortRef reduces "abcdef1234567..ghijkl7890123" to "abcdef1234567..gh" (17 chars)
	// then renderJobLine truncates to colWidths.ref (10)
	if strings.Contains(line, "abcdef1234567..gh") {
		t.Error("Full git ref (after shortRef) should have been truncated")
	}
	if strings.Contains(line, "very-long-repository-name-that-exceeds-width") {
		t.Error("Full repo name should have been truncated")
	}
	if strings.Contains(line, "super-long-agent-name") {
		t.Error("Full agent name should have been truncated")
	}
}

func TestTUIRenderJobLineLength(t *testing.T) {
	// Test that rendered line length respects column widths
	m := tuiModel{width: 100}
	job := storage.ReviewJob{
		ID:         123,
		GitRef:     "abc1234..def5678901234567890", // Long range
		RepoName:   "my-very-long-repository-name-here",
		Agent:      "claude-code-agent",
		Status:     storage.JobStatusDone,
		EnqueuedAt: time.Now(),
	}

	idWidth := 4
	colWidths := columnWidths{
		ref:   12,
		repo:  15,
		agent: 10,
	}

	line := m.renderJobLine(job, false, idWidth, colWidths)

	// Fixed widths: ID (idWidth=4), Status (10), Queued (12), Elapsed (8), Addr'd (varies)
	// Plus spacing between columns
	// The line should not be excessively long
	// Note: line includes ANSI codes for status styling, so we check a reasonable max
	maxExpectedLen := idWidth + colWidths.ref + colWidths.repo + colWidths.agent + 10 + 12 + 8 + 10 + 20 // generous margin for spacing and ANSI
	if len(line) > maxExpectedLen {
		t.Errorf("Line length %d exceeds expected max %d: %s", len(line), maxExpectedLen, line)
	}

	// Verify truncation happened - original values should not appear
	if strings.Contains(line, "my-very-long-repository-name-here") {
		t.Error("Repo name should have been truncated")
	}
	if strings.Contains(line, "claude-code-agent") {
		t.Error("Agent name should have been truncated")
	}
}

func TestTUIRenderJobLineNoTruncation(t *testing.T) {
	m := tuiModel{width: 200}
	job := storage.ReviewJob{
		ID:         1,
		GitRef:     "abc1234",
		RepoName:   "myrepo",
		Agent:      "test",
		Status:     storage.JobStatusDone,
		EnqueuedAt: time.Now(),
	}

	// Use wide column widths - no truncation needed
	colWidths := columnWidths{
		ref:   20,
		repo:  20,
		agent: 15,
	}

	line := m.renderJobLine(job, false, 3, colWidths)

	// Short values should not be truncated
	if strings.Contains(line, "...") {
		t.Errorf("Short values should not be truncated: %s", line)
	}

	// Original values should appear
	if !strings.Contains(line, "abc1234") {
		t.Error("Git ref should appear untruncated")
	}
	if !strings.Contains(line, "myrepo") {
		t.Error("Repo name should appear untruncated")
	}
	if !strings.Contains(line, "test") {
		t.Error("Agent name should appear untruncated")
	}
}

func TestTUIPaginationAppendMode(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Start with 50 jobs
	initialJobs := make([]storage.ReviewJob, 50)
	for i := 0; i < 50; i++ {
		initialJobs[i] = storage.ReviewJob{ID: int64(50 - i)}
	}
	m.jobs = initialJobs
	m.selectedIdx = 0
	m.selectedJobID = 50
	m.hasMore = true

	// Append 25 more jobs
	moreJobs := make([]storage.ReviewJob, 25)
	for i := 0; i < 25; i++ {
		moreJobs[i] = storage.ReviewJob{ID: int64(i + 1)} // IDs 1-25 (older)
	}
	appendMsg := tuiJobsMsg{jobs: moreJobs, hasMore: false, append: true}

	updated, _ := m.Update(appendMsg)
	m2 := updated.(tuiModel)

	// Should now have 75 jobs
	if len(m2.jobs) != 75 {
		t.Errorf("Expected 75 jobs after append, got %d", len(m2.jobs))
	}

	// hasMore should be updated
	if m2.hasMore {
		t.Error("hasMore should be false after append with hasMore=false")
	}

	// loadingMore should be cleared
	if m2.loadingMore {
		t.Error("loadingMore should be cleared after append")
	}

	// Selection should be maintained
	if m2.selectedJobID != 50 {
		t.Errorf("Expected selectedJobID=50 maintained, got %d", m2.selectedJobID)
	}
}

func TestTUIPaginationRefreshMaintainsView(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Simulate user has paginated to 100 jobs
	jobs := make([]storage.ReviewJob, 100)
	for i := 0; i < 100; i++ {
		jobs[i] = storage.ReviewJob{ID: int64(100 - i)}
	}
	m.jobs = jobs
	m.selectedIdx = 50
	m.selectedJobID = 50

	// Refresh arrives (replace mode, not append)
	refreshedJobs := make([]storage.ReviewJob, 100)
	for i := 0; i < 100; i++ {
		refreshedJobs[i] = storage.ReviewJob{ID: int64(101 - i)} // New job at top
	}
	refreshMsg := tuiJobsMsg{jobs: refreshedJobs, hasMore: true, append: false}

	updated, _ := m.Update(refreshMsg)
	m2 := updated.(tuiModel)

	// Should still have 100 jobs
	if len(m2.jobs) != 100 {
		t.Errorf("Expected 100 jobs after refresh, got %d", len(m2.jobs))
	}

	// Selection should find job ID=50 at new index
	if m2.selectedJobID != 50 {
		t.Errorf("Expected selectedJobID=50 maintained, got %d", m2.selectedJobID)
	}
	if m2.selectedIdx != 51 {
		t.Errorf("Expected selectedIdx=51 (shifted by new job), got %d", m2.selectedIdx)
	}
}

func TestTUILoadingMoreClearedOnPaginationError(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.loadingMore = true

	// Pagination error arrives (only pagination errors clear loadingMore)
	errMsg := tuiPaginationErrMsg{err: fmt.Errorf("network error")}
	updated, _ := m.Update(errMsg)
	m2 := updated.(tuiModel)

	// loadingMore should be cleared so user can retry
	if m2.loadingMore {
		t.Error("loadingMore should be cleared on pagination error")
	}

	// Error should be set
	if m2.err == nil {
		t.Error("err should be set")
	}
}

func TestTUILoadingMoreNotClearedOnGenericError(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.loadingMore = true

	// Generic error arrives (should NOT clear loadingMore)
	errMsg := tuiErrMsg(fmt.Errorf("some other error"))
	updated, _ := m.Update(errMsg)
	m2 := updated.(tuiModel)

	// loadingMore should remain true - only pagination errors clear it
	if !m2.loadingMore {
		t.Error("loadingMore should NOT be cleared on generic error")
	}

	// Error should still be set
	if m2.err == nil {
		t.Error("err should be set")
	}
}

func TestTUINavigateDownTriggersLoadMore(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up at last job with more available
	m.jobs = []storage.ReviewJob{{ID: 1}}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false // Must be false to allow pagination
	m.currentView = tuiViewQueue

	// Press down at bottom - should trigger load more
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m2 := updated.(tuiModel)

	if !m2.loadingMore {
		t.Error("loadingMore should be set when navigating past last job")
	}
	if cmd == nil {
		t.Error("Should return fetchMoreJobs command")
	}
}

func TestTUINavigateDownNoLoadMoreWhenFiltered(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up at last job with filter active
	m.jobs = []storage.ReviewJob{{ID: 1, RepoPath: "/path/to/repo"}}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.hasMore = true
	m.loadingMore = false
	m.activeRepoFilter = []string{"/path/to/repo"} // Filter active
	m.currentView = tuiViewQueue

	// Press down at bottom - should NOT trigger load more (filtered view loads all)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m2 := updated.(tuiModel)

	if m2.loadingMore {
		t.Error("loadingMore should not be set when filter is active")
	}
	if cmd != nil {
		t.Error("Should not return command when filter is active")
	}
}

func TestTUIResizeDuringPaginationNoRefetch(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with jobs loaded and pagination in flight
	m.jobs = []storage.ReviewJob{{ID: 1}, {ID: 2}, {ID: 3}}
	m.hasMore = true
	m.loadingMore = true // Pagination in progress
	m.heightDetected = false
	m.height = 24 // Default height

	// Simulate WindowSizeMsg arriving while pagination is in flight
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	m2 := updated.(tuiModel)

	// Height should be updated
	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}
	if !m2.heightDetected {
		t.Error("heightDetected should be true after WindowSizeMsg")
	}

	// Should NOT trigger a re-fetch because loadingMore is true
	if cmd != nil {
		t.Error("Should not return command when loadingMore is true (pagination in flight)")
	}
}

func TestTUIResizeTriggersRefetchWhenNeeded(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with few jobs loaded, more available, no pagination in flight
	m.jobs = []storage.ReviewJob{{ID: 1}, {ID: 2}, {ID: 3}}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false // Must be false to allow refetch
	m.heightDetected = false
	m.height = 24 // Default height

	// Simulate WindowSizeMsg arriving - tall terminal can show more jobs
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	m2 := updated.(tuiModel)

	// Height should be updated
	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}
	if !m2.heightDetected {
		t.Error("heightDetected should be true after WindowSizeMsg")
	}

	// Should trigger a re-fetch because we can show more rows than we have jobs
	// newVisibleRows = 80 - 9 + 10 = 81, which is > len(jobs)=3
	if cmd == nil {
		t.Error("Should return fetchJobs command when terminal can show more jobs")
	}
}

func TestTUIResizeNoRefetchWhenEnoughJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with enough jobs to fill the terminal
	jobs := make([]storage.ReviewJob, 100)
	for i := range jobs {
		jobs[i] = storage.ReviewJob{ID: int64(i + 1)}
	}
	m.jobs = jobs
	m.hasMore = true
	m.loadingMore = false
	m.heightDetected = true
	m.height = 60

	// Simulate WindowSizeMsg - terminal grows but we already have enough jobs
	// newVisibleRows = 80 - 9 + 10 = 81, which is < len(jobs)=100
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	m2 := updated.(tuiModel)

	// Height should be updated
	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}

	// Should NOT trigger a re-fetch because we have enough jobs already
	if cmd != nil {
		t.Error("Should not return command when we already have enough jobs to fill screen")
	}
}

func TestTUIResizeRefetchOnLaterResize(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with few jobs, height already detected from earlier resize
	m.jobs = []storage.ReviewJob{{ID: 1}, {ID: 2}, {ID: 3}}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = false // Must be false to allow refetch
	m.heightDetected = true
	m.height = 30

	// Simulate terminal growing larger - can now show more jobs
	// newVisibleRows = 80 - 9 + 10 = 81, which is > len(jobs)=3
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	m2 := updated.(tuiModel)

	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}

	// Should trigger a re-fetch because terminal can show more jobs than we have
	if cmd == nil {
		t.Error("Should return fetchJobs command when terminal grows and can show more jobs")
	}

	// loadingJobs should be set
	if !m2.loadingJobs {
		t.Error("loadingJobs should be true after resize triggers fetch")
	}
}

func TestTUIResizeNoRefetchWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with few jobs, but loadingJobs is already true (fetch in progress)
	m.jobs = []storage.ReviewJob{{ID: 1}, {ID: 2}, {ID: 3}}
	m.hasMore = true
	m.loadingMore = false
	m.loadingJobs = true // Already fetching
	m.heightDetected = true
	m.height = 30

	// Simulate terminal growing larger
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	m2 := updated.(tuiModel)

	if m2.height != 80 {
		t.Errorf("Expected height 80, got %d", m2.height)
	}

	// Should NOT trigger another fetch because loadingJobs is true
	if cmd != nil {
		t.Error("Should not return command when loadingJobs is true (fetch already in progress)")
	}
}

func TestTUITickNoRefreshWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with loadingJobs true
	m.jobs = []storage.ReviewJob{{ID: 1}, {ID: 2}, {ID: 3}}
	m.loadingJobs = true

	// Simulate tick
	updated, _ := m.Update(tuiTickMsg(time.Now()))
	m2 := updated.(tuiModel)

	// loadingJobs should still be true (not reset by tick)
	if !m2.loadingJobs {
		t.Error("loadingJobs should remain true when tick skips refresh")
	}
}

func TestTUITickInterval(t *testing.T) {
	tests := []struct {
		name              string
		statusFetchedOnce bool
		runningJobs       int
		queuedJobs        int
		wantInterval      time.Duration
	}{
		{
			name:              "before first status fetch uses active interval",
			statusFetchedOnce: false,
			runningJobs:       0,
			queuedJobs:        0,
			wantInterval:      tickIntervalActive,
		},
		{
			name:              "running jobs uses active interval",
			statusFetchedOnce: true,
			runningJobs:       1,
			queuedJobs:        0,
			wantInterval:      tickIntervalActive,
		},
		{
			name:              "queued jobs uses active interval",
			statusFetchedOnce: true,
			runningJobs:       0,
			queuedJobs:        3,
			wantInterval:      tickIntervalActive,
		},
		{
			name:              "idle queue uses idle interval",
			statusFetchedOnce: true,
			runningJobs:       0,
			queuedJobs:        0,
			wantInterval:      tickIntervalIdle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTuiModel("http://localhost")
			m.statusFetchedOnce = tt.statusFetchedOnce
			m.status.RunningJobs = tt.runningJobs
			m.status.QueuedJobs = tt.queuedJobs

			got := m.tickInterval()
			if got != tt.wantInterval {
				t.Errorf("tickInterval() = %v, want %v", got, tt.wantInterval)
			}
		})
	}
}

func TestTUIJobsMsgClearsLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with loadingJobs true
	m.loadingJobs = true

	// Simulate jobs response (not append)
	updated, _ := m.Update(tuiJobsMsg{
		jobs:    []storage.ReviewJob{{ID: 1}},
		hasMore: false,
		append:  false,
	})
	m2 := updated.(tuiModel)

	// loadingJobs should be cleared
	if m2.loadingJobs {
		t.Error("loadingJobs should be false after non-append JobsMsg")
	}
}

func TestTUIJobsMsgAppendKeepsLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with loadingJobs true (shouldn't normally happen with append, but test the logic)
	m.jobs = []storage.ReviewJob{{ID: 1}}
	m.loadingJobs = true

	// Simulate jobs response (append mode - pagination)
	updated, _ := m.Update(tuiJobsMsg{
		jobs:    []storage.ReviewJob{{ID: 2}},
		hasMore: false,
		append:  true,
	})
	m2 := updated.(tuiModel)

	// loadingJobs should NOT be cleared by append (it's for pagination, not full refresh)
	if !m2.loadingJobs {
		t.Error("loadingJobs should remain true after append JobsMsg")
	}
}

func TestTUIHideAddressedToggle(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue

	// Initial state: hideAddressed is false
	if m.hideAddressed {
		t.Error("hideAddressed should be false initially")
	}

	// Press 'h' to toggle
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m2 := updated.(tuiModel)

	if !m2.hideAddressed {
		t.Error("hideAddressed should be true after pressing 'h'")
	}

	// Press 'h' again to toggle back
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m3 := updated.(tuiModel)

	if m3.hideAddressed {
		t.Error("hideAddressed should be false after pressing 'h' again")
	}
}

func TestTUIHideAddressedFiltersJobs(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true

	addressedTrue := true
	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedTrue},  // hidden: addressed
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // visible
		{ID: 3, Status: storage.JobStatusFailed},                           // hidden: failed
		{ID: 4, Status: storage.JobStatusCanceled},                         // hidden: canceled
		{ID: 5, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // visible
	}

	// Check visibility
	if m.isJobVisible(m.jobs[0]) {
		t.Error("Addressed job should be hidden")
	}
	if !m.isJobVisible(m.jobs[1]) {
		t.Error("Non-addressed job should be visible")
	}
	if m.isJobVisible(m.jobs[2]) {
		t.Error("Failed job should be hidden")
	}
	if m.isJobVisible(m.jobs[3]) {
		t.Error("Canceled job should be hidden")
	}
	if !m.isJobVisible(m.jobs[4]) {
		t.Error("Non-addressed job should be visible")
	}

	// getVisibleJobs should only return 2 jobs
	visible := m.getVisibleJobs()
	if len(visible) != 2 {
		t.Errorf("Expected 2 visible jobs, got %d", len(visible))
	}
	if visible[0].ID != 2 || visible[1].ID != 5 {
		t.Errorf("Expected visible jobs 2 and 5, got %d and %d", visible[0].ID, visible[1].ID)
	}
}

func TestTUIHideAddressedSelectionMovesToVisible(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue

	addressedTrue := true
	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedTrue},  // will be hidden
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // will be visible
		{ID: 3, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // will be visible
	}

	// Select the first job (addressed)
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Toggle hide addressed
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m2 := updated.(tuiModel)

	// Selection should move to first visible job (ID=2)
	if m2.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1, got %d", m2.selectedIdx)
	}
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2, got %d", m2.selectedJobID)
	}
}

func TestTUIHideAddressedRefreshRevalidatesSelection(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Simulate jobs refresh where job 1 is now addressed
	addressedTrue := true
	updated, _ := m.Update(tuiJobsMsg{
		jobs: []storage.ReviewJob{
			{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedTrue},  // now addressed (hidden)
			{ID: 2, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // still visible
		},
		hasMore: false,
	})
	m2 := updated.(tuiModel)

	// Selection should move to job 2 since job 1 is now hidden
	if m2.selectedJobID != 2 {
		t.Errorf("Expected selectedJobID=2, got %d", m2.selectedJobID)
	}
	if m2.selectedIdx != 1 {
		t.Errorf("Expected selectedIdx=1, got %d", m2.selectedIdx)
	}
}

func TestTUIHideAddressedNavigationSkipsHidden(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true

	addressedTrue := true
	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // visible
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addressedTrue},  // hidden
		{ID: 3, Status: storage.JobStatusFailed},                           // hidden
		{ID: 4, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // visible
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Navigate down - should skip jobs 2 and 3
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

	if m2.selectedJobID != 4 {
		t.Errorf("Expected selectedJobID=4, got %d", m2.selectedJobID)
	}
	if m2.selectedIdx != 3 {
		t.Errorf("Expected selectedIdx=3, got %d", m2.selectedIdx)
	}
}

func TestTUIHideAddressedWithRepoFilter(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	m.activeRepoFilter = []string{"/repo/a"}

	addressedTrue := true
	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, RepoPath: "/repo/a", Status: storage.JobStatusDone, Addressed: &addressedFalse}, // visible: matches repo, not addressed
		{ID: 2, RepoPath: "/repo/b", Status: storage.JobStatusDone, Addressed: &addressedFalse}, // hidden: wrong repo
		{ID: 3, RepoPath: "/repo/a", Status: storage.JobStatusDone, Addressed: &addressedTrue},  // hidden: addressed
		{ID: 4, RepoPath: "/repo/a", Status: storage.JobStatusFailed},                           // hidden: failed
	}

	// Only job 1 should be visible
	visible := m.getVisibleJobs()
	if len(visible) != 1 {
		t.Errorf("Expected 1 visible job, got %d", len(visible))
	}
	if visible[0].ID != 1 {
		t.Errorf("Expected visible job ID=1, got %d", visible[0].ID)
	}
}

func TestTUIAddressedToggleMovesSelectionWithHideActive(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addressedFalse},
		{ID: 3, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}
	m.selectedIdx = 1
	m.selectedJobID = 2

	// Simulate marking job 2 as addressed
	addressedTrue := true
	m.jobs[1].Addressed = &addressedTrue

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

func TestTUINewModelLoadingJobsTrue(t *testing.T) {
	// newTuiModel should initialize loadingJobs to true since Init() calls fetchJobs
	m := newTuiModel("http://localhost")
	if !m.loadingJobs {
		t.Error("loadingJobs should be true in new model")
	}
}

func TestTUIJobsErrMsgClearsLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.loadingJobs = true

	// Simulate job fetch error
	updated, _ := m.Update(tuiJobsErrMsg{err: fmt.Errorf("connection refused")})
	m2 := updated.(tuiModel)

	if m2.loadingJobs {
		t.Error("loadingJobs should be cleared on job fetch error")
	}
	if m2.err == nil {
		t.Error("err should be set on job fetch error")
	}
}

func TestTUIPaginationBlockedWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.loadingJobs = true
	m.hasMore = true
	m.loadingMore = false

	// Set up at last job
	m.jobs = []storage.ReviewJob{{ID: 1}}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Try to navigate down (would normally trigger pagination)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

	// Pagination should NOT be triggered because loadingJobs is true
	if m2.loadingMore {
		t.Error("loadingMore should not be set while loadingJobs is true")
	}
	if cmd != nil {
		t.Error("No command should be returned when pagination is blocked")
	}
}

func TestTUIPaginationAllowedWhenNotLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.loadingJobs = false
	m.hasMore = true
	m.loadingMore = false

	// Set up at last job
	m.jobs = []storage.ReviewJob{{ID: 1}}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Navigate down - should trigger pagination
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

	// Pagination SHOULD be triggered
	if !m2.loadingMore {
		t.Error("loadingMore should be set when pagination is allowed")
	}
	if cmd == nil {
		t.Error("Command should be returned to fetch more jobs")
	}
}

func TestTUIPageDownBlockedWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.loadingJobs = true
	m.hasMore = true
	m.loadingMore = false
	m.height = 30

	// Set up with one job
	m.jobs = []storage.ReviewJob{{ID: 1}}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Try pgdown (would normally trigger pagination at end)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m2 := updated.(tuiModel)

	// Pagination should NOT be triggered
	if m2.loadingMore {
		t.Error("loadingMore should not be set on pgdown while loadingJobs is true")
	}
	if cmd != nil {
		t.Error("No command should be returned when pagination is blocked")
	}
}

func TestTUIHideAddressedEnableTriggersRefetch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = false

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Toggle hide addressed ON
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m2 := updated.(tuiModel)

	// hideAddressed should be enabled
	if !m2.hideAddressed {
		t.Error("hideAddressed should be true after pressing 'h'")
	}

	// A command should be returned to fetch all jobs
	if cmd == nil {
		t.Error("Command should be returned to fetch all jobs when enabling hideAddressed")
	}
}

func TestTUIHideAddressedDisableNoRefetch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true // Already enabled

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Toggle hide addressed OFF
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m2 := updated.(tuiModel)

	// hideAddressed should be disabled
	if m2.hideAddressed {
		t.Error("hideAddressed should be false after pressing 'h' to disable")
	}

	// No command should be returned when disabling (no need to refetch)
	if cmd != nil {
		t.Error("No command should be returned when disabling hideAddressed")
	}
}

func TestTUIReviewMsgSetsBranchName(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Receive review message with branch name
	msg := tuiReviewMsg{
		review:     &storage.Review{ID: 10, Output: "Review text", Job: &storage.ReviewJob{ID: 1}},
		jobID:      1,
		branchName: "main",
	}

	updated, _ := m.Update(msg)
	m2 := updated.(tuiModel)

	if m2.currentBranch != "main" {
		t.Errorf("Expected currentBranch to be 'main', got '%s'", m2.currentBranch)
	}
}

func TestTUIReviewMsgEmptyBranchForRange(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{
		{ID: 1, GitRef: "abc123..def456", Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue

	// Receive review message with empty branch (range commits don't have branches)
	msg := tuiReviewMsg{
		review:     &storage.Review{ID: 10, Output: "Review text", Job: &storage.ReviewJob{ID: 1, GitRef: "abc123..def456"}},
		jobID:      1,
		branchName: "", // Empty for ranges
	}

	updated, _ := m.Update(msg)
	m2 := updated.(tuiModel)

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

	// First line should be the title
	if !strings.Contains(lines[0], "Review") {
		t.Errorf("First line should contain 'Review', got: %s", lines[0])
	}

	// Second line should be content (Line 1), not blank
	if len(lines) > 1 && strings.TrimSpace(lines[1]) == "" {
		t.Error("Second line should not be blank when no verdict is present")
	}
	if len(lines) > 1 && !strings.Contains(lines[1], "Line 1") {
		t.Errorf("Second line should contain content 'Line 1', got: %s", lines[1])
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

	// First line should be the title
	if !strings.Contains(lines[0], "Review") {
		t.Errorf("First line should contain 'Review', got: %s", lines[0])
	}

	// Second line should be the verdict
	if len(lines) > 1 && !strings.Contains(lines[1], "Verdict") {
		t.Errorf("Second line should contain 'Verdict', got: %s", lines[1])
	}

	// Third line should be content
	if len(lines) > 2 && !strings.Contains(lines[2], "Line 1") {
		t.Errorf("Third line should contain content 'Line 1', got: %s", lines[2])
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
		{ID: 1, Status: storage.JobStatusDone, GitRef: "abc123"},
		{ID: 2, Status: storage.JobStatusFailed, GitRef: "def456", Error: "some error"},
	}
	m.currentReview = &storage.Review{
		ID:     10,
		Output: "Good review",
		Job:    &m.jobs[0],
	}

	// Navigate down to failed job (j or down key in review view)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m2 := updated.(tuiModel)

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
		{ID: 1, Status: storage.JobStatusFailed, GitRef: "abc123", Error: "build failed"},
	}

	// Press Enter to view the failed job
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(tuiModel)

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

func TestTUIVisibleLinesCalculationNoVerdict(t *testing.T) {
	// Test that visibleLines = height - 3 when no verdict (title + status + help)
	// Help text is ~106 chars, so use width >= 110 to avoid wrapping
	m := newTuiModel("http://localhost")
	m.width = 120
	m.height = 10 // Small height to test calculation
	m.currentView = tuiViewReview
	// Create 20 lines of content to ensure scrolling
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

	// With height=10, no verdict, wide terminal: visibleLines = 10 - 3 = 7
	// Non-content: title (1) + status line (1) + help (1) = 3
	// Count content lines (L1 through L7)
	contentCount := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "L") && len(stripANSI(line)) <= 3 {
			contentCount++
		}
	}

	expectedContent := 7
	if contentCount != expectedContent {
		t.Errorf("Expected %d content lines with height=10 and no verdict, got %d", expectedContent, contentCount)
	}

	// Should show scroll indicator since we have 20 lines but only showing 7
	if !strings.Contains(output, "[1-7 of 20 lines]") {
		t.Errorf("Expected scroll indicator '[1-7 of 20 lines]', output: %s", output)
	}
}

func TestTUIVisibleLinesCalculationWithVerdict(t *testing.T) {
	// Test that visibleLines = height - 4 when verdict present (title + verdict + status + help)
	// Help text is ~106 chars, so use width >= 110 to avoid wrapping
	verdictPass := "P"
	m := newTuiModel("http://localhost")
	m.width = 120
	m.height = 10 // Small height to test calculation
	m.currentView = tuiViewReview
	// Create 20 lines of content to ensure scrolling
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

	// With height=10, verdict, wide terminal: visibleLines = 10 - 4 = 6
	// Non-content: title (1) + verdict (1) + status line (1) + help (1) = 4
	contentCount := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "L") && len(stripANSI(line)) <= 3 {
			contentCount++
		}
	}

	expectedContent := 6
	if contentCount != expectedContent {
		t.Errorf("Expected %d content lines with height=10 and verdict, got %d", expectedContent, contentCount)
	}

	// Should show scroll indicator since we have 20 lines but only showing 6
	if !strings.Contains(output, "[1-6 of 20 lines]") {
		t.Errorf("Expected scroll indicator '[1-6 of 20 lines]', output: %s", output)
	}
}

func TestTUIVisibleLinesCalculationNarrowTerminal(t *testing.T) {
	// Test that visibleLines accounts for help text wrapping at narrow terminals
	// Help text is ~91 chars, at width=50 it wraps to 2 lines: ceil(91/50) = 2
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

	// With height=10, no verdict, narrow terminal (help wraps to 2 lines):
	// visibleLines = 10 - 4 = 6
	// Non-content: title (1) + status line (1) + help (2) = 4
	contentCount := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "L") && len(stripANSI(line)) <= 3 {
			contentCount++
		}
	}

	expectedContent := 6
	if contentCount != expectedContent {
		t.Errorf("Expected %d content lines with height=10 and narrow terminal (help wraps), got %d", expectedContent, contentCount)
	}

	// Should show scroll indicator
	if !strings.Contains(output, "[1-6 of 20 lines]") {
		t.Errorf("Expected scroll indicator '[1-6 of 20 lines]', output: %s", output)
	}
}

func TestTUIVisibleLinesCalculationNarrowTerminalWithVerdict(t *testing.T) {
	// Test narrow terminal with verdict - validates extra header line branch
	// Help text is ~91 chars, at width=50 it wraps to 2 lines: ceil(91/50) = 2
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

	// With height=10, verdict present, narrow terminal (help wraps to 2 lines):
	// visibleLines = 10 - 5 = 5
	// Non-content: title (1) + verdict (1) + status line (1) + help (2) = 5
	contentCount := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "L") && len(stripANSI(line)) <= 3 {
			contentCount++
		}
	}

	expectedContent := 5
	if contentCount != expectedContent {
		t.Errorf("Expected %d content lines with height=10, verdict, and narrow terminal, got %d", expectedContent, contentCount)
	}

	// Should show scroll indicator
	if !strings.Contains(output, "[1-5 of 20 lines]") {
		t.Errorf("Expected scroll indicator '[1-5 of 20 lines]', output: %s", output)
	}

	// Should show verdict
	if !strings.Contains(output, "Verdict") {
		t.Error("Expected output to contain verdict")
	}
}

func TestTUIVisibleLinesCalculationLongTitleWraps(t *testing.T) {
	// Test that long titles (repo/branch/range) correctly wrap and reduce visible lines
	// Title: "Review #1 very-long-repository-name-here abc1234..def5678 (claude-code) on feature/very-long-branch-name [ADDRESSED]"
	// That's about 120 chars, at width=50 it wraps to 3 lines: ceil(120/50) = 3
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

	// Calculate expected title length:
	// "Review #1 very-long-repository-name-here abc1234..def5678 (claude-code) on feature/very-long-branch-name [ADDRESSED]"
	// = 7 + 3 + 31 + 17 + 14 + 34 + 12 = ~118 chars
	// At width=50: ceil(118/50) = 3 title lines
	// Help at width=50: ceil(91/50) = 2 help lines
	// Non-content: title (3) + status line (1) + help (2) = 6
	// visibleLines = 12 - 6 = 6
	contentCount := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "L") && len(stripANSI(line)) <= 3 {
			contentCount++
		}
	}

	expectedContent := 6
	if contentCount != expectedContent {
		t.Errorf("Expected %d content lines with long wrapping title, got %d", expectedContent, contentCount)
	}

	// Should show scroll indicator with correct range
	if !strings.Contains(output, "[1-6 of 20 lines]") {
		t.Errorf("Expected scroll indicator '[1-6 of 20 lines]', output: %s", output)
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

func TestTUIFetchReviewFallbackSHAResponses(t *testing.T) {
	// Test that when job_id responses are empty, TUI falls back to SHA-based responses
	requestedPaths := []string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.String())

		if r.URL.Path == "/api/review" {
			// Return a review for a single commit (not a range or dirty)
			review := storage.Review{
				ID:    1,
				JobID: 42,
				Agent: "test",
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
				json.NewEncoder(w).Encode(map[string]interface{}{
					"responses": []storage.Response{},
				})
				return
			}
			if sha != "" {
				// SHA fallback query returns legacy responses
				json.NewEncoder(w).Encode(map[string]interface{}{
					"responses": []storage.Response{
						{ID: 1, Responder: "user", Response: "Legacy response from SHA lookup"},
					},
				})
				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.String())

		if r.URL.Path == "/api/review" {
			// Return a review for a commit range (not a single commit)
			review := storage.Review{
				ID:    1,
				JobID: 42,
				Agent: "test",
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
			json.NewEncoder(w).Encode(map[string]interface{}{
				"responses": []storage.Response{},
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)
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

func TestTUIIsJobVisibleRespectsPendingAddressed(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.hideAddressed = true

	addressedFalse := false
	addressedTrue := true

	// Job with Addressed=false but pendingAddressed=true should be hidden
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	if m.isJobVisible(m.jobs[0]) {
		t.Error("Job with pendingAddressed=true should be hidden when hideAddressed is active")
	}

	// Job with Addressed=true but pendingAddressed=false should be visible
	m.jobs = []storage.ReviewJob{
		{ID: 2, Status: storage.JobStatusDone, Addressed: &addressedTrue},
	}
	m.pendingAddressed[2] = pendingState{newState: false, seq: 1}

	if !m.isJobVisible(m.jobs[0]) {
		t.Error("Job with pendingAddressed=false should be visible even if job.Addressed is true")
	}

	// Job with no pendingAddressed entry falls back to job.Addressed
	m.jobs = []storage.ReviewJob{
		{ID: 3, Status: storage.JobStatusDone, Addressed: &addressedTrue},
	}
	delete(m.pendingAddressed, 3)

	if m.isJobVisible(m.jobs[0]) {
		t.Error("Job with Addressed=true and no pending entry should be hidden")
	}
}

func TestTUIEscapeFromReviewTriggersRefreshWithHideAddressed(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.hideAddressed = true
	m.loadingJobs = false

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}
	m.currentReview = &storage.Review{ID: 1, JobID: 1}

	// Press escape to return to queue view
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

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

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}
	m.currentReview = &storage.Review{ID: 1, JobID: 1}

	// Press escape to return to queue view
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

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

func TestTUIPendingAddressedNotClearedByStaleResponse(t *testing.T) {
	m := newTuiModel("http://localhost")

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// A stale response comes back for a previous toggle to false
	// (this could happen if user rapidly toggles)
	staleMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: true,
		newState: false, // This response was for a toggle to false
		seq:      0,     // Stale: doesn't match pending seq (1)
		err:      nil,
	}

	updated, _ := m.Update(staleMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should NOT be cleared because newState (false) != current pending (true)
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should not be cleared by stale response with mismatched newState")
	}
	if m2.pendingAddressed[1].newState != true {
		t.Error("pendingAddressed value should remain true")
	}
}

func TestTUIPendingAddressedNotClearedOnSuccess(t *testing.T) {
	m := newTuiModel("http://localhost")

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Success response comes back
	successMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: false,
		newState: true,
		seq:      1,
		err:      nil,
	}

	updated, _ := m.Update(successMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should NOT be cleared on success - it waits for jobs refresh
	// to confirm the update. This prevents race condition where stale jobs response
	// arrives after success and briefly shows old state.
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should NOT be cleared on success response")
	}
}

func TestTUIPendingAddressedClearedByJobsRefresh(t *testing.T) {
	m := newTuiModel("http://localhost")

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Jobs refresh arrives with server data confirming the update
	addressedTrue := true
	jobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedTrue},
		},
	}

	updated, _ := m.Update(jobsMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should be cleared because server data matches pending state
	if _, ok := m2.pendingAddressed[1]; ok {
		t.Error("pendingAddressed should be cleared when jobs refresh confirms update")
	}
}

func TestTUIPendingAddressedNotClearedByStaleJobsRefresh(t *testing.T) {
	m := newTuiModel("http://localhost")

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}

	// User toggles addressed to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Stale jobs refresh arrives with old data (from request sent before update)
	staleJobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse}, // Still false!
		},
	}

	updated, _ := m.Update(staleJobsMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should NOT be cleared because server data doesn't match
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should NOT be cleared when jobs refresh has stale data")
	}

	// Job should still show as addressed (pending state re-applied)
	if m2.jobs[0].Addressed == nil || !*m2.jobs[0].Addressed {
		t.Error("Job should still show as addressed due to pending state")
	}
}

func TestTUIPendingAddressedClearedOnCurrentError(t *testing.T) {
	m := newTuiModel("http://localhost")

	addressedTrue := true
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedTrue},
	}

	// User toggles addressed to false
	m.pendingAddressed[1] = pendingState{newState: false, seq: 1}

	// Error response comes back for the current request (seq matches pending)
	errorMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: true,
		newState: false, // Matches pendingAddressed[1]
		seq:      1,     // Matches pending seq
		err:      fmt.Errorf("server error"),
	}

	updated, _ := m.Update(errorMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should be cleared on error (we're rolling back)
	if _, ok := m2.pendingAddressed[1]; ok {
		t.Error("pendingAddressed should be cleared on current error")
	}

	// Job state should be rolled back
	if m2.jobs[0].Addressed == nil || *m2.jobs[0].Addressed != true {
		t.Error("Job addressed state should be rolled back to oldState on error")
	}

	// Error should be set
	if m2.err == nil {
		t.Error("Error should be set on current error")
	}
}

func TestTUIStaleErrorResponseIgnored(t *testing.T) {
	m := newTuiModel("http://localhost")

	addressedTrue := true
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedTrue},
	}

	// User toggles to false, then back to true (pendingAddressed=true)
	// The job.Addressed was updated optimistically to true
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}
	*m.jobs[0].Addressed = true // Optimistic update already applied

	// A stale error response arrives from the earlier toggle to false
	staleErrorMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: true,  // What it was before the (stale) toggle
		newState: false, // Stale request was for false, but pending is now true
		seq:      0,     // Stale: doesn't match pending seq (1)
		err:      fmt.Errorf("network error"),
	}

	updated, _ := m.Update(staleErrorMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should NOT be cleared (stale error)
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should not be cleared by stale error response")
	}
	if m2.pendingAddressed[1].newState != true {
		t.Error("pendingAddressed value should remain true")
	}

	// Job state should NOT be rolled back (stale error)
	if m2.jobs[0].Addressed == nil || *m2.jobs[0].Addressed != true {
		t.Error("Job addressed state should not be rolled back by stale error")
	}

	// Error should NOT be set (stale error is silently ignored)
	if m2.err != nil {
		t.Error("Error should not be set for stale error response")
	}
}

func TestTUIQueueViewSameStateLateError(t *testing.T) {
	// Test: true (seq 1) → false (seq 2) → true (seq 3), with late error from first true
	// Same as TestTUIReviewViewSameStateLateError but for queue view using pendingAddressed
	m := newTuiModel("http://localhost")

	addressedFalse := false
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: &addressedFalse},
	}

	// Sequence: toggle true (seq 1) → toggle false (seq 2) → toggle true (seq 3)
	// After third toggle, state is true and pendingAddressed has seq 3
	*m.jobs[0].Addressed = true // Optimistic update from third toggle
	m.pendingAddressed[1] = pendingState{newState: true, seq: 3} // Third toggle

	// A late error arrives from the FIRST toggle (seq 1)
	// This error has newState=true which matches current pending newState,
	// but seq doesn't match, so it should be treated as stale and ignored.
	lateErrorMsg := tuiAddressedResultMsg{
		jobID:    1,
		oldState: false, // First toggle was from false to true
		newState: true,  // Same newState as current pending...
		seq:      1,     // ...but different seq, so this is stale
		err:      fmt.Errorf("network error from first toggle"),
	}

	updated, _ := m.Update(lateErrorMsg)
	m2 := updated.(tuiModel)

	// With sequence numbers, the late error should be IGNORED (not rolled back)
	// because seq: 1 != pending seq: 3
	if m2.jobs[0].Addressed == nil || *m2.jobs[0].Addressed != true {
		t.Errorf("Expected addressed to stay true (late error should be ignored), got %v", m2.jobs[0].Addressed)
	}

	// Error should NOT be set (stale error)
	if m2.err != nil {
		t.Errorf("Error should not be set for stale error response, got %v", m2.err)
	}

	// pendingAddressed should still be set (not cleared by stale response)
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should not be cleared by stale response")
	}
}

func TestTUIReviewViewErrorWithoutJobID(t *testing.T) {
	// Test that review-view errors without jobID are still handled if
	// pendingReviewAddressed matches
	m := newTuiModel("http://localhost")

	// Review without an associated job (Job is nil)
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:        42,
		Addressed: false,
		Job:       nil, // No job associated
	}

	// Simulate optimistic update (what happens when 'a' is pressed)
	m.currentReview.Addressed = true
	m.pendingReviewAddressed[42] = pendingState{newState: true, seq: 1} // Track pending state by review ID

	// Error arrives for this toggle (no jobID since Job was nil)
	errMsg := tuiAddressedResultMsg{
		reviewID:   42,
		jobID:      0,    // No job
		reviewView: true,
		oldState:   false,
		newState:   true, // Matches pendingReviewAddressed
		seq:        1,    // Matches pending seq
		err:        fmt.Errorf("server error"),
	}

	updated, _ := m.Update(errMsg)
	m2 := updated.(tuiModel)

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
	m.currentReview = &storage.Review{
		ID:        42,
		Addressed: false,
		Job:       nil,
	}

	// User toggled to true, then back to false
	// pendingReviewAddressed is now false (from the second toggle)
	m.currentReview.Addressed = false
	m.pendingReviewAddressed[42] = pendingState{newState: false, seq: 1}

	// A stale error arrives from the earlier toggle to true
	staleErrorMsg := tuiAddressedResultMsg{
		reviewID:   42,
		jobID:      0,     // No job
		reviewView: true,
		oldState:   false, // What it was before the stale toggle
		newState:   true,  // Stale: pendingReviewAddressed is false, not true
		seq:        0,     // Stale: doesn't match pending seq (1)
		err:        fmt.Errorf("network error"),
	}

	updated, _ := m.Update(staleErrorMsg)
	m2 := updated.(tuiModel)

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
	m.currentReview = &storage.Review{
		ID:        42,
		Addressed: false,
		Job:       nil,
	}

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

	updated, _ := m.Update(lateErrorMsg)
	m2 := updated.(tuiModel)

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

func TestTUIEmptyQueueRendersPaddedHeight(t *testing.T) {
	// Test that empty queue view pads output to fill terminal height
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{} // Empty queue
	m.loadingJobs = false          // Not loading, so should show "No jobs in queue"

	output := m.View()

	// Count total lines (including empty ones from padding)
	lines := strings.Split(output, "\n")

	// Strip ANSI codes and count non-empty content
	// The output should fill most of the terminal height
	// Accounting for: title(1) + status(2) + content/padding + scroll(1) + update(1) + help(2)
	// Minimum expected lines is close to m.height
	if len(lines) < m.height-3 {
		t.Errorf("Empty queue should pad to near terminal height, got %d lines for height %d", len(lines), m.height)
	}

	// Should contain the "No jobs in queue" message
	if !strings.Contains(output, "No jobs in queue") {
		t.Error("Expected 'No jobs in queue' message in output")
	}
}

func TestTUIEmptyQueueWithFilterRendersPaddedHeight(t *testing.T) {
	// Test that empty queue with filter pads output correctly
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{}
	m.activeRepoFilter = []string{"/some/repo"} // Filter active but no matching jobs
	m.loadingJobs = false                       // Not loading, so should show "No jobs matching filters"

	output := m.View()

	lines := strings.Split(output, "\n")
	if len(lines) < m.height-3 {
		t.Errorf("Empty filtered queue should pad to near terminal height, got %d lines for height %d", len(lines), m.height)
	}

	// Should contain the filter message
	if !strings.Contains(output, "No jobs matching filters") {
		t.Error("Expected 'No jobs matching filters' message in output")
	}
}

func TestTUILoadingJobsShowsLoadingMessage(t *testing.T) {
	// Test that loading state shows "Loading..." instead of "No jobs" messages
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{}
	m.loadingJobs = true // Loading in progress

	output := m.View()

	if !strings.Contains(output, "Loading...") {
		t.Error("Expected 'Loading...' message when loadingJobs is true")
	}
	if strings.Contains(output, "No jobs in queue") {
		t.Error("Should not show 'No jobs in queue' while loading")
	}
	if strings.Contains(output, "No jobs matching filters") {
		t.Error("Should not show 'No jobs matching filters' while loading")
	}
}

func TestTUILoadingShowsForPendingRefetch(t *testing.T) {
	// Test that "Loading..." shows when pendingRefetch is set (e.g., after escape during pagination)
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{} // Empty after filter clear
	m.loadingJobs = false
	m.loadingMore = false
	m.pendingRefetch = true // Refetch queued but not yet started

	output := m.View()

	if !strings.Contains(output, "Loading...") {
		t.Error("Expected 'Loading...' message when pendingRefetch is true")
	}
	if strings.Contains(output, "No jobs in queue") {
		t.Error("Should not show 'No jobs in queue' while refetch pending")
	}
}

func TestTUILoadingShowsForLoadingMore(t *testing.T) {
	// Test that "Loading..." shows when loadingMore is set on empty queue
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 20
	m.jobs = []storage.ReviewJob{} // Empty after filter clear
	m.loadingJobs = false
	m.loadingMore = true // Pagination in flight

	output := m.View()

	if !strings.Contains(output, "Loading...") {
		t.Error("Expected 'Loading...' message when loadingMore is true")
	}
}

func TestTUIFilterLoadingRendersPaddedHeight(t *testing.T) {
	// Test that filter loading state pads output to fill terminal height
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 20
	m.currentView = tuiViewFilter
	m.filterRepos = nil // Loading state (repos not fetched yet)

	output := m.View()

	lines := strings.Split(output, "\n")
	// Filter loading should fill most of the terminal height
	if len(lines) < m.height-3 {
		t.Errorf("Filter loading should pad to near terminal height, got %d lines for height %d", len(lines), m.height)
	}

	// Should contain loading message
	if !strings.Contains(output, "Loading repos...") {
		t.Error("Expected 'Loading repos...' message in output")
	}
}

func TestTUIQueueNoScrollIndicatorPads(t *testing.T) {
	// Test that queue view with few jobs (no scroll indicator) still maintains height
	m := newTuiModel("http://localhost")
	m.width = 100
	m.height = 30
	// Add just 2 jobs - should not need scroll indicator
	m.jobs = []storage.ReviewJob{
		{ID: 1, GitRef: "abc123", Agent: "test", Status: "done"},
		{ID: 2, GitRef: "def456", Agent: "test", Status: "done"},
	}

	output := m.View()

	lines := strings.Split(output, "\n")
	// Even with few jobs, output should be close to terminal height
	if len(lines) < m.height-5 {
		t.Errorf("Queue with few jobs should maintain height, got %d lines for height %d", len(lines), m.height)
	}
}

func TestTUIPendingReviewAddressedClearedOnSuccess(t *testing.T) {
	// pendingReviewAddressed (for reviews without jobs) should be cleared on success
	// because the race condition only affects jobs list refresh, not review-only items
	m := newTuiModel("http://localhost")

	// Track a review-only pending state (no job ID)
	m.pendingReviewAddressed[42] = pendingState{newState: true, seq: 1}

	// Success response for review-only (jobID=0, reviewID=42)
	successMsg := tuiAddressedResultMsg{
		jobID:      0, // No job - this is review-only
		reviewID:   42,
		reviewView: true,
		oldState:   false,
		newState:   true,
		seq:        1,
		err:        nil,
	}

	updated, _ := m.Update(successMsg)
	m2 := updated.(tuiModel)

	// pendingReviewAddressed SHOULD be cleared on success (no race condition for review-only)
	if _, ok := m2.pendingReviewAddressed[42]; ok {
		t.Error("pendingReviewAddressed should be cleared on success for review-only items")
	}
}

func TestTUIPendingAddressedClearsWhenServerNilMatchesFalse(t *testing.T) {
	// When pending newState is false and server Addressed is nil,
	// treat nil as false and clear the pending state
	m := newTuiModel("http://localhost")

	// Job with nil Addressed (e.g., partial payload or non-done status that became done)
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: nil},
	}

	// User had toggled to false (unaddressed)
	m.pendingAddressed[1] = pendingState{newState: false, seq: 1}

	// Jobs refresh arrives with nil Addressed (should match false)
	jobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			{ID: 1, Status: storage.JobStatusDone, Addressed: nil},
		},
	}

	updated, _ := m.Update(jobsMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should be cleared because nil == false matches newState=false
	if _, ok := m2.pendingAddressed[1]; ok {
		t.Error("pendingAddressed should be cleared when server nil matches pending false")
	}
}

func TestTUIPendingAddressedNotClearsWhenServerNilMismatchesTrue(t *testing.T) {
	// When pending newState is true and server Addressed is nil,
	// do NOT clear (nil != true)
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, Addressed: nil},
	}

	// User toggled to true (addressed)
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	// Jobs refresh arrives with nil Addressed (doesn't match true)
	jobsMsg := tuiJobsMsg{
		jobs: []storage.ReviewJob{
			{ID: 1, Status: storage.JobStatusDone, Addressed: nil},
		},
	}

	updated, _ := m.Update(jobsMsg)
	m2 := updated.(tuiModel)

	// pendingAddressed should NOT be cleared because nil != true
	if _, ok := m2.pendingAddressed[1]; !ok {
		t.Error("pendingAddressed should NOT be cleared when server nil mismatches pending true")
	}

	// Job should show as addressed due to pending state re-applied
	if m2.jobs[0].Addressed == nil || !*m2.jobs[0].Addressed {
		t.Error("Job should show as addressed due to pending state")
	}
}

func TestTUIRespondTextPreservation(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, GitRef: "abc1234"},
		{ID: 2, Status: storage.JobStatusDone, GitRef: "def5678"},
	}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.width = 80
	m.height = 24

	// 1. Open respond for Job 1
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(tuiModel)

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
	updated, _ = m.Update(errMsg)
	m = updated.(tuiModel)

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
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(tuiModel)

	if m.commentText != "My draft response" {
		t.Errorf("Expected text preserved on retry for same job, got %q", m.commentText)
	}

	// 5. Go back to queue and switch to Job 2 - text should be cleared
	m.currentView = tuiViewQueue
	m.selectedIdx = 1
	m.selectedJobID = 2
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(tuiModel)

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
		{ID: 1, Status: storage.JobStatusDone, GitRef: "abc1234"},
		{ID: 2, Status: storage.JobStatusDone, GitRef: "def5678"},
	}

	// User submitted response for job 1, then started drafting for job 2
	m.commentJobID = 2
	m.commentText = "New draft for job 2"

	// Success message arrives for job 1 (the old submission)
	successMsg := tuiCommentResultMsg{jobID: 1, err: nil}
	updated, _ := m.Update(successMsg)
	m = updated.(tuiModel)

	// Draft for job 2 should NOT be cleared
	if m.commentText != "New draft for job 2" {
		t.Errorf("Expected draft preserved for different job, got %q", m.commentText)
	}
	if m.commentJobID != 2 {
		t.Errorf("Expected commentJobID=2 preserved, got %d", m.commentJobID)
	}

	// Now success for job 2 should clear
	successMsg = tuiCommentResultMsg{jobID: 2, err: nil}
	updated, _ = m.Update(successMsg)
	m = updated.(tuiModel)

	if m.commentText != "" {
		t.Errorf("Expected text cleared for matching job, got %q", m.commentText)
	}
	if m.commentJobID != 0 {
		t.Errorf("Expected commentJobID=0 after success, got %d", m.commentJobID)
	}
}

func TestTUIFilterBackspaceMultiByte(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewFilter
	m.filterRepos = []repoFilterItem{{name: "", count: 10}}

	// Type an emoji (multi-byte character)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("😊")})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = updated.(tuiModel)

	if m.filterSearch != "a😊b" {
		t.Errorf("Expected filterSearch='a😊b', got %q", m.filterSearch)
	}

	// Backspace should remove 'b'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(tuiModel)
	if m.filterSearch != "a😊" {
		t.Errorf("Expected filterSearch='a😊' after first backspace, got %q", m.filterSearch)
	}

	// Backspace should remove the entire emoji, not corrupt it
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(tuiModel)
	if m.filterSearch != "a" {
		t.Errorf("Expected filterSearch='a' after second backspace, got %q", m.filterSearch)
	}
}

func TestTUIRespondBackspaceMultiByte(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewComment
	m.commentJobID = 1

	// Type text with multi-byte characters
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Hello 世界")})
	m = updated.(tuiModel)

	if m.commentText != "Hello 世界" {
		t.Errorf("Expected commentText='Hello 世界', got %q", m.commentText)
	}

	// Backspace should remove '界' (one character), not corrupt it
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(tuiModel)
	if m.commentText != "Hello 世" {
		t.Errorf("Expected commentText='Hello 世' after backspace, got %q", m.commentText)
	}

	// Backspace should remove '世'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(tuiModel)
	if m.commentText != "Hello " {
		t.Errorf("Expected commentText='Hello ' after second backspace, got %q", m.commentText)
	}
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
	// Save original clipboard writer and restore after test
	originalClipboard := clipboardWriter
	mock := &mockClipboard{}
	clipboardWriter = mock
	defer func() { clipboardWriter = originalClipboard }()

	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     1,
		JobID:  1,
		Agent:  "test",
		Output: "This is the review content to copy",
	}

	// Press 'y' to yank/copy
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(tuiModel)

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
	m.currentReview = &storage.Review{
		ID:     1,
		JobID:  1,
		Agent:  "test",
		Output: "Review content",
	}
	m.width = 80
	m.height = 24

	// Simulate receiving a successful clipboard result (view captured at trigger time)
	updated, _ := m.Update(tuiClipboardResultMsg{err: nil, view: tuiViewReview})
	m = updated.(tuiModel)

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
	updated, _ := m.Update(tuiClipboardResultMsg{err: fmt.Errorf("clipboard not available"), view: tuiViewQueue})
	m = updated.(tuiModel)

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
	m.currentReview = &storage.Review{
		ID:     1,
		JobID:  1,
		Agent:  "test",
		Output: "Review content",
	}

	// User switches to review view before clipboard result arrives
	m.currentView = tuiViewReview

	// Clipboard result arrives with view captured at trigger time (queue)
	updated, _ := m.Update(tuiClipboardResultMsg{err: nil, view: tuiViewQueue})
	m = updated.(tuiModel)

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
		{ID: 1, GitRef: "abc123", Agent: "test", Status: storage.JobStatusRunning},
		{ID: 2, GitRef: "def456", Agent: "test", Status: storage.JobStatusDone},
	}
	m.selectedIdx = 0

	// Press 'y' on running job - should not copy
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd != nil {
		t.Error("Expected no command for running job")
	}

	// Select completed job
	m.selectedIdx = 1
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Error("Expected command for completed job")
	}
}

func TestTUIFlashMessageAppearsInQueueView(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.width = 80
	m.height = 24
	m.flashMessage = "Copied to clipboard"
	m.flashExpiresAt = time.Now().Add(2 * time.Second)
	m.flashView = tuiViewQueue // Flash was triggered in queue view

	output := m.renderQueueView()
	if !strings.Contains(output, "Copied to clipboard") {
		t.Error("Expected flash message to appear in queue view")
	}
}

func TestTUIFlashMessageNotShownInDifferentView(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.width = 80
	m.height = 24
	m.flashMessage = "Copied to clipboard"
	m.flashExpiresAt = time.Now().Add(2 * time.Second)
	m.flashView = tuiViewQueue // Flash was triggered in queue view, not review view
	m.currentReview = &storage.Review{
		ID:     1,
		Output: "Test review content",
	}

	output := m.renderReviewView()
	if strings.Contains(output, "Copied to clipboard") {
		t.Error("Flash message should not appear when viewing different view than where it was triggered")
	}
}

func TestTUIUpdateNotificationInQueueView(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.width = 80
	m.height = 24
	m.updateAvailable = "1.2.3"

	output := m.renderQueueView()
	if !strings.Contains(output, "Update available: 1.2.3") {
		t.Error("Expected update notification in queue view")
	}
	if !strings.Contains(output, "run 'roborev update'") {
		t.Error("Expected update instructions in queue view")
	}

	// Verify update notification appears on line 3 (index 2) - above the table
	// Layout: line 0 = title, line 1 = status, line 2 = update notification
	lines := strings.Split(output, "\n")
	if len(lines) < 3 {
		t.Fatalf("Expected at least 3 lines, got %d", len(lines))
	}
	// Line 2 (third line) should contain the update notification
	if !strings.Contains(lines[2], "Update available") {
		t.Errorf("Expected update notification on line 3 (index 2), got: %q", lines[2])
	}
}

func TestTUIUpdateNotificationDevBuild(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.width = 80
	m.height = 24
	m.updateAvailable = "1.2.3"
	m.updateIsDevBuild = true

	output := m.renderQueueView()
	if !strings.Contains(output, "Dev build") {
		t.Error("Expected 'Dev build' in notification for dev builds")
	}
	if !strings.Contains(output, "roborev update --force") {
		t.Error("Expected --force flag in update instructions for dev builds")
	}
}

func TestTUIUpdateNotificationNotInReviewView(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.width = 80
	m.height = 24
	m.updateAvailable = "1.2.3"
	m.currentReview = &storage.Review{
		ID:     1,
		Output: "Test review content",
	}

	output := m.renderReviewView()
	if strings.Contains(output, "Update available") {
		t.Error("Update notification should not appear in review view")
	}
}

func TestTUIFetchReviewAndCopySuccess(t *testing.T) {
	// Save original clipboard writer and restore after test
	originalClipboard := clipboardWriter
	mock := &mockClipboard{}
	clipboardWriter = mock
	defer func() { clipboardWriter = originalClipboard }()

	// Create test server that returns a review
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)

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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)

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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review := storage.Review{
			ID:     1,
			JobID:  123,
			Agent:  "test",
			Output: "", // Empty output
		}
		json.NewEncoder(w).Encode(review)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)

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
	// Save original clipboard writer and restore after test
	originalClipboard := clipboardWriter
	mock := &mockClipboard{err: fmt.Errorf("clipboard unavailable: xclip not found")}
	clipboardWriter = mock
	defer func() { clipboardWriter = originalClipboard }()

	m := newTuiModel("http://localhost")
	m.currentView = tuiViewReview
	m.currentReview = &storage.Review{
		ID:     1,
		JobID:  1,
		Agent:  "test",
		Output: "Review content",
	}

	// Press 'y' to copy
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
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
	// Save original clipboard writer and restore after test
	originalClipboard := clipboardWriter
	mock := &mockClipboard{err: fmt.Errorf("clipboard unavailable: pbcopy not found")}
	clipboardWriter = mock
	defer func() { clipboardWriter = originalClipboard }()

	// Create test server that returns a valid review
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review := storage.Review{
			ID:     1,
			JobID:  123,
			Agent:  "test",
			Output: "Review content",
		}
		json.NewEncoder(w).Encode(review)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)

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
	// Save original clipboard writer and restore after test
	originalClipboard := clipboardWriter
	mock := &mockClipboard{}
	clipboardWriter = mock
	defer func() { clipboardWriter = originalClipboard }()

	// Create test server that returns a review WITHOUT Job populated
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review := storage.Review{
			ID:     42,
			JobID:  123,
			Agent:  "test",
			Output: "Review content",
			// Job is intentionally nil
		}
		json.NewEncoder(w).Encode(review)
	}))
	defer ts.Close()

	m := newTuiModel(ts.URL)

	// Pass a job parameter - this should be injected when review.Job is nil
	job := &storage.ReviewJob{
		ID:       123,
		RepoPath: "/path/to/repo",
		GitRef:   "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", // 40 hex chars
	}

	cmd := m.fetchReviewAndCopy(123, job)
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

func TestTUIConfigReloadFlash(t *testing.T) {
	m := newTuiModel("http://localhost:7373")

	t.Run("no flash on first status fetch", func(t *testing.T) {
		// First status fetch with a ConfigReloadCounter should NOT flash
		status1 := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 1,
		})

		updated, _ := m.Update(status1)
		m2 := updated.(tuiModel)

		if m2.flashMessage != "" {
			t.Errorf("Expected no flash on first fetch, got %q", m2.flashMessage)
		}
		if !m2.statusFetchedOnce {
			t.Error("Expected statusFetchedOnce to be true after first fetch")
		}
		if m2.lastConfigReloadCounter != 1 {
			t.Errorf("Expected lastConfigReloadCounter to be 1, got %d", m2.lastConfigReloadCounter)
		}
	})

	t.Run("flash on config reload after first fetch", func(t *testing.T) {
		// Start with a model that has already fetched status once
		m := newTuiModel("http://localhost:7373")
		m.statusFetchedOnce = true
		m.lastConfigReloadCounter = 1

		// Second status with different ConfigReloadCounter should flash
		status2 := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 2,
		})

		updated, _ := m.Update(status2)
		m2 := updated.(tuiModel)

		if m2.flashMessage != "Config reloaded" {
			t.Errorf("Expected flash 'Config reloaded', got %q", m2.flashMessage)
		}
		if m2.lastConfigReloadCounter != 2 {
			t.Errorf("Expected lastConfigReloadCounter updated to 2, got %d", m2.lastConfigReloadCounter)
		}
	})

	t.Run("flash when ConfigReloadCounter changes from zero to non-zero", func(t *testing.T) {
		// Model has fetched status once but daemon hadn't reloaded yet
		m := newTuiModel("http://localhost:7373")
		m.statusFetchedOnce = true
		m.lastConfigReloadCounter = 0 // No reload had occurred

		// Now config is reloaded
		status := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 1,
		})

		updated, _ := m.Update(status)
		m2 := updated.(tuiModel)

		if m2.flashMessage != "Config reloaded" {
			t.Errorf("Expected flash when ConfigReloadCounter goes from 0 to 1, got %q", m2.flashMessage)
		}
	})

	t.Run("no flash when ConfigReloadCounter unchanged", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.statusFetchedOnce = true
		m.lastConfigReloadCounter = 1

		// Same counter
		status := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 1,
		})

		updated, _ := m.Update(status)
		m2 := updated.(tuiModel)

		if m2.flashMessage != "" {
			t.Errorf("Expected no flash when counter unchanged, got %q", m2.flashMessage)
		}
	})
}

func TestTUIReconnectOnConsecutiveErrors(t *testing.T) {
	t.Run("triggers reconnection after 3 consecutive connection errors", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 2 // Already had 2 connection errors

		// Third connection error should trigger reconnection
		updated, cmd := m.Update(tuiJobsErrMsg{err: mockConnError("connection refused")})
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 3 {
			t.Errorf("Expected consecutiveErrors=3, got %d", m2.consecutiveErrors)
		}
		if !m2.reconnecting {
			t.Error("Expected reconnecting=true after 3 consecutive errors")
		}
		if cmd == nil {
			t.Error("Expected reconnect command to be returned")
		}
	})

	t.Run("does not trigger reconnection before 3 errors", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 1 // Only 1 error so far

		updated, cmd := m.Update(tuiJobsErrMsg{err: mockConnError("connection refused")})
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 2 {
			t.Errorf("Expected consecutiveErrors=2, got %d", m2.consecutiveErrors)
		}
		if m2.reconnecting {
			t.Error("Expected reconnecting=false before 3 consecutive errors")
		}
		if cmd != nil {
			t.Error("Expected no command before 3 errors")
		}
	})

	t.Run("does not count application errors for reconnection", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 2 // 2 connection errors

		// Application error (404, parse error, etc.) should not increment counter
		updated, cmd := m.Update(tuiErrMsg(fmt.Errorf("no review found")))
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 2 {
			t.Errorf("Expected consecutiveErrors unchanged at 2, got %d", m2.consecutiveErrors)
		}
		if m2.reconnecting {
			t.Error("Expected reconnecting=false for application errors")
		}
		if cmd != nil {
			t.Error("Expected no reconnect command for application errors")
		}
	})

	t.Run("does not count non-connection errors in jobs fetch", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 2

		// Non-connection error (like parse error) should not increment
		updated, _ := m.Update(tuiJobsErrMsg{err: fmt.Errorf("invalid JSON response")})
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 2 {
			t.Errorf("Expected consecutiveErrors unchanged at 2, got %d", m2.consecutiveErrors)
		}
	})

	t.Run("pagination errors also trigger reconnection", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 2

		// Connection error in pagination should trigger reconnection
		updated, cmd := m.Update(tuiPaginationErrMsg{err: mockConnError("connection refused")})
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 3 {
			t.Errorf("Expected consecutiveErrors=3, got %d", m2.consecutiveErrors)
		}
		if !m2.reconnecting {
			t.Error("Expected reconnecting=true for pagination connection errors")
		}
		if cmd == nil {
			t.Error("Expected reconnect command")
		}
	})

	t.Run("status/review connection errors trigger reconnection", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 2

		// Connection error via tuiErrMsg (from fetchStatus, fetchReview, etc.)
		updated, cmd := m.Update(tuiErrMsg(mockConnError("connection refused")))
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 3 {
			t.Errorf("Expected consecutiveErrors=3, got %d", m2.consecutiveErrors)
		}
		if !m2.reconnecting {
			t.Error("Expected reconnecting=true for tuiErrMsg connection errors")
		}
		if cmd == nil {
			t.Error("Expected reconnect command")
		}
	})

	t.Run("status/review application errors do not trigger reconnection", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 2

		// Application error via tuiErrMsg should not increment
		updated, cmd := m.Update(tuiErrMsg(fmt.Errorf("review not found")))
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 2 {
			t.Errorf("Expected consecutiveErrors unchanged at 2, got %d", m2.consecutiveErrors)
		}
		if m2.reconnecting {
			t.Error("Expected reconnecting=false for application errors")
		}
		if cmd != nil {
			t.Error("Expected no reconnect command for application errors")
		}
	})

	t.Run("resets error count on successful jobs fetch", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 5

		updated, _ := m.Update(tuiJobsMsg{jobs: []storage.ReviewJob{}, hasMore: false})
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 0 {
			t.Errorf("Expected consecutiveErrors=0 after success, got %d", m2.consecutiveErrors)
		}
	})

	t.Run("resets error count on successful status fetch", func(t *testing.T) {
		m := newTuiModel("http://localhost:7373")
		m.consecutiveErrors = 5

		updated, _ := m.Update(tuiStatusMsg(storage.DaemonStatus{Version: "1.0.0"}))
		m2 := updated.(tuiModel)

		if m2.consecutiveErrors != 0 {
			t.Errorf("Expected consecutiveErrors=0 after status success, got %d", m2.consecutiveErrors)
		}
	})

	t.Run("updates server address on successful reconnection", func(t *testing.T) {
		m := newTuiModel("http://127.0.0.1:7373")
		m.reconnecting = true

		updated, cmd := m.Update(tuiReconnectMsg{newAddr: "http://127.0.0.1:7374", version: "2.0.0"})
		m2 := updated.(tuiModel)

		if m2.serverAddr != "http://127.0.0.1:7374" {
			t.Errorf("Expected serverAddr updated to new address, got %s", m2.serverAddr)
		}
		if m2.consecutiveErrors != 0 {
			t.Errorf("Expected consecutiveErrors reset to 0, got %d", m2.consecutiveErrors)
		}
		if m2.reconnecting {
			t.Error("Expected reconnecting=false after successful reconnection")
		}
		if m2.err != nil {
			t.Error("Expected error cleared after successful reconnection")
		}
		if m2.daemonVersion != "2.0.0" {
			t.Errorf("Expected daemonVersion updated to 2.0.0, got %s", m2.daemonVersion)
		}
		if cmd == nil {
			t.Error("Expected fetch commands to be returned after reconnection")
		}
	})

	t.Run("handles reconnection to same address", func(t *testing.T) {
		m := newTuiModel("http://127.0.0.1:7373")
		m.reconnecting = true
		m.consecutiveErrors = 3

		// Same address - no change needed
		updated, cmd := m.Update(tuiReconnectMsg{newAddr: "http://127.0.0.1:7373"})
		m2 := updated.(tuiModel)

		if m2.serverAddr != "http://127.0.0.1:7373" {
			t.Errorf("Expected serverAddr unchanged, got %s", m2.serverAddr)
		}
		if m2.reconnecting {
			t.Error("Expected reconnecting=false after reconnect attempt")
		}
		// Error count not reset since address is same (will retry on next tick)
		if cmd != nil {
			t.Error("Expected no fetch commands when address unchanged")
		}
	})

	t.Run("handles failed reconnection", func(t *testing.T) {
		m := newTuiModel("http://127.0.0.1:7373")
		m.reconnecting = true
		m.consecutiveErrors = 3

		updated, cmd := m.Update(tuiReconnectMsg{err: fmt.Errorf("no daemon found")})
		m2 := updated.(tuiModel)

		if m2.reconnecting {
			t.Error("Expected reconnecting=false after failed reconnection")
		}
		// Error count preserved - will retry on next tick
		if m2.consecutiveErrors != 3 {
			t.Errorf("Expected consecutiveErrors preserved, got %d", m2.consecutiveErrors)
		}
		if cmd != nil {
			t.Error("Expected no commands on failed reconnection")
		}
	})
}

func TestTUICommitMsgViewNavigationFromQueue(t *testing.T) {
	// Test that pressing escape in commit message view returns to the originating view (queue)
	m := newTuiModel("http://localhost")
	m.jobs = []storage.ReviewJob{{ID: 1, GitRef: "abc123", Status: storage.JobStatusDone}}
	m.selectedIdx = 0
	m.selectedJobID = 1
	m.currentView = tuiViewQueue
	m.commitMsgJobID = 1              // Set to match incoming message (normally set by 'm' key handler)
	m.commitMsgFromView = tuiViewQueue // Track where we came from

	// Simulate receiving commit message content (sets view to CommitMsg)
	updated, _ := m.Update(tuiCommitMsgMsg{jobID: 1, content: "test message"})
	m2 := updated.(tuiModel)

	if m2.currentView != tuiViewCommitMsg {
		t.Errorf("Expected tuiViewCommitMsg, got %d", m2.currentView)
	}

	// Press escape to go back
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := updated.(tuiModel)

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
	job := &storage.ReviewJob{ID: 1, GitRef: "abc123", Status: storage.JobStatusDone}
	m.jobs = []storage.ReviewJob{*job}
	m.currentReview = &storage.Review{ID: 1, JobID: 1, Job: job}
	m.currentView = tuiViewReview
	m.commitMsgFromView = tuiViewReview
	m.commitMsgContent = "test message"
	m.currentView = tuiViewCommitMsg

	// Press escape to go back
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(tuiModel)

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
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m2 := updated.(tuiModel)

	if m2.currentView != tuiViewReview {
		t.Errorf("Expected to return to tuiViewReview after 'q', got %d", m2.currentView)
	}
}

func TestFetchCommitMsgJobTypeDetection(t *testing.T) {
	// Test that fetchCommitMsg correctly identifies job types and returns appropriate errors
	// This is critical: Prompt field is populated for ALL jobs (stores review prompt),
	// so we must check GitRef == "prompt" to identify run tasks, not Prompt != ""

	m := newTuiModel("http://localhost")

	tests := []struct {
		name        string
		job         storage.ReviewJob
		expectError string // empty means no early error (will try git lookup)
	}{
		{
			name: "regular commit with Prompt populated should not error early",
			job: storage.ReviewJob{
				ID:     1,
				GitRef: "abc123def456",                      // valid commit SHA
				Prompt: "You are a code reviewer...",        // review prompt is stored for all jobs
			},
			expectError: "", // should attempt git lookup, not return "run tasks" error
		},
		{
			name: "run task (GitRef=prompt) should error",
			job: storage.ReviewJob{
				ID:     2,
				GitRef: "prompt",
				Prompt: "Explain this codebase",
			},
			expectError: "no commit message for run tasks",
		},
		{
			name: "dirty job (GitRef=dirty) should error",
			job: storage.ReviewJob{
				ID:     3,
				GitRef: "dirty",
			},
			expectError: "no commit message for uncommitted changes",
		},
		{
			name: "dirty job with DiffContent should error",
			job: storage.ReviewJob{
				ID:         4,
				GitRef:     "some-ref",
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
			name: "dirty job with nil DiffContent but GitRef=dirty should error",
			job: storage.ReviewJob{
				ID:         7,
				GitRef:     "dirty",
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
				// but NOT the "run tasks" or "uncommitted changes" error
				if result.err != nil {
					errMsg := result.err.Error()
					if errMsg == "no commit message for run tasks" {
						t.Errorf("Regular commit with Prompt should not be detected as run task")
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
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m2 := updated.(tuiModel)

	if m2.currentView != tuiViewHelp {
		t.Errorf("Expected tuiViewHelp, got %d", m2.currentView)
	}
	if m2.helpFromView != tuiViewQueue {
		t.Errorf("Expected helpFromView to be tuiViewQueue, got %d", m2.helpFromView)
	}

	// Press '?' again to close help
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m3 := updated.(tuiModel)

	if m3.currentView != tuiViewQueue {
		t.Errorf("Expected to return to tuiViewQueue, got %d", m3.currentView)
	}
}

func TestTUIHelpViewToggleFromReview(t *testing.T) {
	// Test that '?' opens help from review and escape returns to review
	m := newTuiModel("http://localhost")
	job := &storage.ReviewJob{ID: 1, GitRef: "abc123", Status: storage.JobStatusDone}
	m.currentReview = &storage.Review{ID: 1, JobID: 1, Job: job}
	m.currentView = tuiViewReview

	// Press '?' to open help
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m2 := updated.(tuiModel)

	if m2.currentView != tuiViewHelp {
		t.Errorf("Expected tuiViewHelp, got %d", m2.currentView)
	}
	if m2.helpFromView != tuiViewReview {
		t.Errorf("Expected helpFromView to be tuiViewReview, got %d", m2.helpFromView)
	}

	// Press escape to close help
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := updated.(tuiModel)

	if m3.currentView != tuiViewReview {
		t.Errorf("Expected to return to tuiViewReview, got %d", m3.currentView)
	}
}

func TestSanitizeForDisplay(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text unchanged",
			input:    "Hello, world!",
			expected: "Hello, world!",
		},
		{
			name:     "preserves newlines and tabs",
			input:    "Line1\n\tIndented",
			expected: "Line1\n\tIndented",
		},
		{
			name:     "strips ANSI color codes",
			input:    "\x1b[31mred text\x1b[0m",
			expected: "red text",
		},
		{
			name:     "strips cursor movement",
			input:    "\x1b[2Jhello\x1b[H",
			expected: "hello",
		},
		{
			name:     "strips OSC sequences (title set with BEL)",
			input:    "\x1b]0;Evil Title\x07normal text",
			expected: "normal text",
		},
		{
			name:     "strips OSC sequences (title set with ST)",
			input:    "\x1b]0;Evil Title\x1b\\normal text",
			expected: "normal text",
		},
		{
			name:     "strips control characters",
			input:    "hello\x00world\x07\x08test",
			expected: "helloworldtest",
		},
		{
			name:     "handles complex escape sequence",
			input:    "\x1b[1;32mBold Green\x1b[0m and \x1b[4munderline\x1b[24m",
			expected: "Bold Green and underline",
		},
		{
			name:     "empty string unchanged",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeForDisplay(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeForDisplay(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
