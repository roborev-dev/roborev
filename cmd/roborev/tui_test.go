package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

// testServerAddr is a placeholder address used in tests that don't make real HTTP calls.
// Tests that need actual HTTP should use httptest.NewServer and pass ts.URL.
const testServerAddr = "http://test.invalid:9999"

// setupTuiTestEnv isolates the test from the production roborev environment
// by setting ROBOREV_DATA_DIR to a temp directory. This prevents tests from
// reading production daemon.json or affecting production state.
func setupTuiTestEnv(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)
	t.Cleanup(func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	})
}

// mockConnError creates a connection error (url.Error) for testing
func mockConnError(msg string) error {
	return &url.Error{Op: "Get", URL: testServerAddr, Err: errors.New(msg)}
}

// stripANSI removes ANSI escape sequences from a string
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// mockServerModel creates an httptest.Server and a tuiModel pointed at it.
func mockServerModel(t *testing.T, handler http.HandlerFunc) (*httptest.Server, tuiModel) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, newTuiModel(ts.URL)
}

// pressKey simulates pressing a rune key and returns the updated tuiModel.
func pressKey(m tuiModel, r rune) (tuiModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return updated.(tuiModel), cmd
}

// pressKeys simulates pressing multiple rune keys.
func pressKeys(m tuiModel, runes []rune) (tuiModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes})
	return updated.(tuiModel), cmd
}

// pressSpecial simulates pressing a special key (Enter, Escape, etc.).
func pressSpecial(m tuiModel, key tea.KeyType) (tuiModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: key})
	return updated.(tuiModel), cmd
}

// updateModel sends a message to the model and returns the updated tuiModel.
func updateModel(t *testing.T, m tuiModel, msg tea.Msg) (tuiModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	newModel, ok := updated.(tuiModel)
	if !ok {
		t.Fatalf("Model type assertion failed: got %T", updated)
	}
	return newModel, cmd
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool { return &b }

// makeJob creates a storage.ReviewJob with the given ID and sensible defaults.
// Optional functional options can override specific fields.
func makeJob(id int64, opts ...func(*storage.ReviewJob)) storage.ReviewJob {
	j := storage.ReviewJob{ID: id, Status: storage.JobStatusDone}
	for _, opt := range opts {
		opt(&j)
	}
	return j
}

func withStatus(s storage.JobStatus) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Status = s }
}

func withRef(ref string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.GitRef = ref }
}

func withAgent(agent string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Agent = agent }
}

func withBranch(branch string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Branch = branch }
}

func withAddressed(b *bool) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Addressed = b }
}

func withRepoPath(path string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.RepoPath = path }
}

func withRepoName(name string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.RepoName = name }
}

func withFinishedAt(t *time.Time) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.FinishedAt = t }
}

func withEnqueuedAt(t time.Time) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.EnqueuedAt = t }
}

func withModel(model string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Model = model }
}

func withError(err string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Error = err }
}

func withVerdict(v string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Verdict = &v }
}

func withReviewType(rt string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.ReviewType = rt }
}

// makeReview creates a storage.Review linked to the given job.
func makeReview(id int64, job *storage.ReviewJob, opts ...func(*storage.Review)) *storage.Review {
	r := &storage.Review{
		ID:    id,
		JobID: job.ID,
		Job:   job,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func withReviewOutput(output string) func(*storage.Review) {
	return func(r *storage.Review) { r.Output = output }
}

func withReviewAddressed(addressed bool) func(*storage.Review) {
	return func(r *storage.Review) { r.Addressed = addressed }
}

func withReviewAgent(agent string) func(*storage.Review) {
	return func(r *storage.Review) { r.Agent = agent }
}

func withReviewPrompt(prompt string) func(*storage.Review) {
	return func(r *storage.Review) { r.Prompt = prompt }
}

func TestTUIFetchJobsSuccess(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" {
			t.Errorf("Expected /api/jobs, got %s", r.URL.Path)
		}
		jobs := []storage.ReviewJob{{ID: 1, GitRef: "abc123", Agent: "test"}}
		json.NewEncoder(w).Encode(map[string]interface{}{"jobs": jobs})
	})
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
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cmd := m.fetchJobs()
	msg := cmd()

	_, ok := msg.(tuiJobsErrMsg)
	if !ok {
		t.Fatalf("Expected tuiJobsErrMsg for 500, got %T: %v", msg, msg)
	}
}

func TestTUIHTTPTimeout(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		// Delay much longer than client timeout to avoid flaky timing on fast machines
		time.Sleep(500 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{}})
	})
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
		makeJob(3), makeJob(2), makeJob(1),
	}
	m.selectedIdx = 1
	m.selectedJobID = 2

	// New jobs added at the top (newer jobs first)
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(5), makeJob(4), makeJob(3), makeJob(2), makeJob(1),
	}}

	m, _ = updateModel(t, m, newJobs)

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
		makeJob(3), makeJob(2), makeJob(1),
	}
	m.selectedIdx = 2
	m.selectedJobID = 1

	// Job ID=1 is removed
	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{
		makeJob(3), makeJob(2),
	}}

	m, _ = updateModel(t, m, newJobs)

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
		makeJob(5), makeJob(4), makeJob(3),
	}}

	m, _ = updateModel(t, m, newJobs)

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
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0
	m.selectedJobID = 1

	newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{}}

	m, _ = updateModel(t, m, newJobs)

	// Empty list should have selectedIdx=-1 (no valid selection)
	if m.selectedIdx != -1 {
		t.Errorf("Expected selectedIdx=-1, got %d", m.selectedIdx)
	}
	if m.selectedJobID != 0 {
		t.Errorf("Expected selectedJobID=0, got %d", m.selectedJobID)
	}
}

func TestTUISelectionMaintainedOnLargeBatch(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Initial state with 1 job selected
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// 30 new jobs added at the top (simulating large batch)
	newJobs := make([]storage.ReviewJob, 31)
	for i := 0; i < 30; i++ {
		newJobs[i] = makeJob(int64(31 - i)) // IDs 31, 30, 29, ..., 2
	}
	newJobs[30] = makeJob(1) // Original job at the end

	m, _ = updateModel(t, m, tuiJobsMsg{jobs: newJobs})

	// Should still follow job ID=1, now at index 30
	if m.selectedJobID != 1 {
		t.Errorf("Expected selectedJobID=1, got %d", m.selectedJobID)
	}
	if m.selectedIdx != 30 {
		t.Errorf("Expected selectedIdx=30 (ID=1 at end), got %d", m.selectedIdx)
	}
}

func TestTUIGetVisibleJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
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
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
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

func TestTUITickNoRefreshWhileLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with loadingJobs true
	m.jobs = []storage.ReviewJob{makeJob(1), makeJob(2), makeJob(3)}
	m.loadingJobs = true

	// Simulate tick
	m2, _ := updateModel(t, m, tuiTickMsg(time.Now()))

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
	m2, _ := updateModel(t, m, tuiJobsMsg{
		jobs:    []storage.ReviewJob{makeJob(1)},
		hasMore: false,
		append:  false,
	})

	// loadingJobs should be cleared
	if m2.loadingJobs {
		t.Error("loadingJobs should be false after non-append JobsMsg")
	}
}

func TestTUIJobsMsgAppendKeepsLoadingJobs(t *testing.T) {
	m := newTuiModel("http://localhost")

	// Set up with loadingJobs true (shouldn't normally happen with append, but test the logic)
	m.jobs = []storage.ReviewJob{makeJob(1)}
	m.loadingJobs = true

	// Simulate jobs response (append mode - pagination)
	m2, _ := updateModel(t, m, tuiJobsMsg{
		jobs:    []storage.ReviewJob{makeJob(2)},
		hasMore: false,
		append:  true,
	})

	// loadingJobs should NOT be cleared by append (it's for pagination, not full refresh)
	if !m2.loadingJobs {
		t.Error("loadingJobs should remain true after append JobsMsg")
	}
}

func TestTUIHideAddressedDefaultFromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("hide_addressed_by_default = true\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m := newTuiModel("http://localhost")
	if !m.hideAddressed {
		t.Error("hideAddressed should be true when config sets hide_addressed_by_default = true")
	}
}

func TestTUIHideAddressedToggle(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue

	// Initial state: hideAddressed is false (TestMain isolates from real config)
	if m.hideAddressed {
		t.Error("hideAddressed should be false initially")
	}

	// Press 'h' to toggle
	m2, _ := pressKey(m, 'h')

	if !m2.hideAddressed {
		t.Error("hideAddressed should be true after pressing 'h'")
	}

	// Press 'h' again to toggle back
	m3, _ := pressKey(m2, 'h')

	if m3.hideAddressed {
		t.Error("hideAddressed should be false after pressing 'h' again")
	}
}

func TestTUIHideAddressedFiltersJobs(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(true))),          // hidden: addressed
		makeJob(2, withAddressed(boolPtr(false))),         // visible
		makeJob(3, withStatus(storage.JobStatusFailed)),   // hidden: failed
		makeJob(4, withStatus(storage.JobStatusCanceled)), // hidden: canceled
		makeJob(5, withAddressed(boolPtr(false))),         // visible
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

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(true))),  // will be hidden
		makeJob(2, withAddressed(boolPtr(false))), // will be visible
		makeJob(3, withAddressed(boolPtr(false))), // will be visible
	}

	// Select the first job (addressed)
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Toggle hide addressed
	m2, _ := pressKey(m, 'h')

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

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
		makeJob(2, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Simulate jobs refresh where job 1 is now addressed

	m2, _ := updateModel(t, m, tuiJobsMsg{
		jobs: []storage.ReviewJob{
			makeJob(1, withAddressed(boolPtr(true))),  // now addressed (hidden)
			makeJob(2, withAddressed(boolPtr(false))), // still visible
		},
		hasMore: false,
	})

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

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),       // visible
		makeJob(2, withAddressed(boolPtr(true))),        // hidden
		makeJob(3, withStatus(storage.JobStatusFailed)), // hidden
		makeJob(4, withAddressed(boolPtr(false))),       // visible
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Navigate down - should skip jobs 2 and 3
	m2, _ := pressKey(m, 'j')

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

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoPath("/repo/a"), withAddressed(boolPtr(false))),       // visible: matches repo, not addressed
		makeJob(2, withRepoPath("/repo/b"), withAddressed(boolPtr(false))),       // hidden: wrong repo
		makeJob(3, withRepoPath("/repo/a"), withAddressed(boolPtr(true))),        // hidden: addressed
		makeJob(4, withRepoPath("/repo/a"), withStatus(storage.JobStatusFailed)), // hidden: failed
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
	m2, _ := updateModel(t, m, tuiJobsErrMsg{err: fmt.Errorf("connection refused")})

	if m2.loadingJobs {
		t.Error("loadingJobs should be cleared on job fetch error")
	}
	if m2.err == nil {
		t.Error("err should be set on job fetch error")
	}
}

func TestTUIHideAddressedClearRepoFilterRefetches(t *testing.T) {
	// Scenario: hide addressed enabled, then filter by repo, then press escape
	// to clear the repo filter. Should trigger a refetch to show all unaddressed reviews.
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true
	m.activeRepoFilter = []string{"/repo/a"}
	m.filterStack = []string{"repo"}
	m.loadingJobs = false // Simulate that initial load has completed

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoPath("/repo/a"), withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Press escape to clear the repo filter
	m2, cmd := pressSpecial(m, tea.KeyEscape)

	// Repo filter should be cleared
	if m2.activeRepoFilter != nil {
		t.Errorf("Expected activeRepoFilter to be nil, got %v", m2.activeRepoFilter)
	}

	// hideAddressed should still be true
	if !m2.hideAddressed {
		t.Error("hideAddressed should still be true after clearing repo filter")
	}

	// Filter stack should be empty
	if len(m2.filterStack) != 0 {
		t.Errorf("Expected empty filter stack, got %v", m2.filterStack)
	}

	// jobs should be preserved (so fetchJobs limit stays large enough)
	if len(m2.jobs) != 1 {
		t.Errorf("Expected jobs to be preserved after escape, got %d jobs", len(m2.jobs))
	}

	// A refetch command should be returned
	if cmd == nil {
		t.Error("Expected a refetch command when clearing repo filter with hide-addressed active")
	}

	// loadingJobs should be set
	if !m2.loadingJobs {
		t.Error("loadingJobs should be set when refetching after clearing repo filter")
	}
}

func TestTUIHideAddressedEnableTriggersRefetch(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = false

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Toggle hide addressed ON
	m2, cmd := pressKey(m, 'h')

	// hideAddressed should be enabled
	if !m2.hideAddressed {
		t.Error("hideAddressed should be true after pressing 'h'")
	}

	// A command should be returned to fetch all jobs
	if cmd == nil {
		t.Error("Command should be returned to fetch all jobs when enabling hideAddressed")
	}
}

func TestTUIHideAddressedDisableRefetches(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue
	m.hideAddressed = true // Already enabled

	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Toggle hide addressed OFF
	m2, cmd := pressKey(m, 'h')

	// hideAddressed should be disabled
	if m2.hideAddressed {
		t.Error("hideAddressed should be false after pressing 'h' to disable")
	}

	// Disabling triggers a refetch to get previously-filtered addressed jobs
	if cmd == nil {
		t.Error("Expected a refetch command when disabling hideAddressed")
	}
}

func TestTUIHideAddressedMalformedConfigNotOverwritten(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Write malformed TOML that LoadGlobal will fail to parse
	malformed := []byte(`this is not valid toml {{{`)
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, malformed, 0644); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}

	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue

	// Toggle hide addressed ON
	m2, _ := pressKey(m, 'h')

	// In-session toggle should still work
	if !m2.hideAddressed {
		t.Error("hideAddressed should be true after pressing 'h'")
	}

	// Malformed config file must not have been overwritten
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != string(malformed) {
		t.Errorf("malformed config was overwritten:\n  before: %q\n  after:  %q", malformed, got)
	}

	// Toggle back OFF — still works in-session
	m3, _ := pressKey(m2, 'h')
	if m3.hideAddressed {
		t.Error("hideAddressed should be false after pressing 'h' again")
	}
}

func TestTUIHideAddressedValidConfigNotMutated(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Write a valid config with the hide-addressed default enabled
	validConfig := []byte("hide_addressed_by_default = true\n")
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, validConfig, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m := newTuiModel("http://localhost")
	m.currentView = tuiViewQueue

	// Verify the default was loaded
	if !m.hideAddressed {
		t.Fatal("hideAddressed should be true from config")
	}

	// Toggle hide addressed OFF
	m2, _ := pressKey(m, 'h')
	if m2.hideAddressed {
		t.Error("hideAddressed should be false after pressing 'h'")
	}

	// Toggle hide addressed back ON
	m3, _ := pressKey(m2, 'h')
	if !m3.hideAddressed {
		t.Error("hideAddressed should be true after pressing 'h' again")
	}

	// Valid config file must not have been mutated by either toggle
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != string(validConfig) {
		t.Errorf("valid config was mutated:\n  before: %q\n  after:  %q", validConfig, got)
	}
}

func TestTUIIsJobVisibleRespectsPendingAddressed(t *testing.T) {
	m := newTuiModel("http://localhost")
	m.hideAddressed = true

	// Job with Addressed=false but pendingAddressed=true should be hidden
	m.jobs = []storage.ReviewJob{
		makeJob(1, withAddressed(boolPtr(false))),
	}
	m.pendingAddressed[1] = pendingState{newState: true, seq: 1}

	if m.isJobVisible(m.jobs[0]) {
		t.Error("Job with pendingAddressed=true should be hidden when hideAddressed is active")
	}

	// Job with Addressed=true but pendingAddressed=false should be visible
	m.jobs = []storage.ReviewJob{
		makeJob(2, withAddressed(boolPtr(true))),
	}
	m.pendingAddressed[2] = pendingState{newState: false, seq: 1}

	if !m.isJobVisible(m.jobs[0]) {
		t.Error("Job with pendingAddressed=false should be visible even if job.Addressed is true")
	}

	// Job with no pendingAddressed entry falls back to job.Addressed
	m.jobs = []storage.ReviewJob{
		makeJob(3, withAddressed(boolPtr(true))),
	}
	delete(m.pendingAddressed, 3)

	if m.isJobVisible(m.jobs[0]) {
		t.Error("Job with Addressed=true and no pending entry should be hidden")
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
	m.currentReview = makeReview(1, &storage.ReviewJob{}, withReviewOutput("Test review content"))

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
	m.currentReview = makeReview(1, &storage.ReviewJob{}, withReviewOutput("Test review content"))

	output := m.renderReviewView()
	if strings.Contains(output, "Update available") {
		t.Error("Update notification should not appear in review view")
	}
}

func TestTUIVersionMismatchDetection(t *testing.T) {
	setupTuiTestEnv(t)

	t.Run("detects version mismatch", func(t *testing.T) {
		m := newTuiModel(testServerAddr)

		// Simulate receiving status with different version
		status := tuiStatusMsg(storage.DaemonStatus{
			Version: "different-version",
		})

		m2, _ := updateModel(t, m, status)

		if !m2.versionMismatch {
			t.Error("Expected versionMismatch=true when daemon version differs")
		}
		if m2.daemonVersion != "different-version" {
			t.Errorf("Expected daemonVersion='different-version', got %q", m2.daemonVersion)
		}
	})

	t.Run("no mismatch when versions match", func(t *testing.T) {
		m := newTuiModel(testServerAddr)

		// Simulate receiving status with same version as TUI
		status := tuiStatusMsg(storage.DaemonStatus{
			Version: version.Version,
		})

		m2, _ := updateModel(t, m, status)

		if m2.versionMismatch {
			t.Error("Expected versionMismatch=false when versions match")
		}
	})

	t.Run("displays error banner in queue view when mismatched", func(t *testing.T) {
		m := newTuiModel(testServerAddr)
		m.width = 100
		m.height = 30
		m.currentView = tuiViewQueue
		m.versionMismatch = true
		m.daemonVersion = "old-version"

		output := m.View()

		if !strings.Contains(output, "VERSION MISMATCH") {
			t.Error("Expected queue view to show VERSION MISMATCH error")
		}
		if !strings.Contains(output, "old-version") {
			t.Error("Expected error to show daemon version")
		}
	})

	t.Run("displays error banner in review view when mismatched", func(t *testing.T) {
		m := newTuiModel(testServerAddr)
		m.width = 100
		m.height = 30
		m.currentView = tuiViewReview
		m.versionMismatch = true
		m.daemonVersion = "old-version"
		m.currentReview = &storage.Review{
			ID:     1,
			Output: "Test review",
			Job: &storage.ReviewJob{
				ID:       1,
				GitRef:   "abc123",
				RepoName: "test",
				Agent:    "test",
			},
		}

		output := m.View()

		if !strings.Contains(output, "VERSION MISMATCH") {
			t.Error("Expected review view to show VERSION MISMATCH error")
		}
	})
}

func TestTUIConfigReloadFlash(t *testing.T) {
	setupTuiTestEnv(t)
	m := newTuiModel(testServerAddr)

	t.Run("no flash on first status fetch", func(t *testing.T) {
		// First status fetch with a ConfigReloadCounter should NOT flash
		status1 := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 1,
		})

		m2, _ := updateModel(t, m, status1)

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
		m := newTuiModel(testServerAddr)
		m.statusFetchedOnce = true
		m.lastConfigReloadCounter = 1

		// Second status with different ConfigReloadCounter should flash
		status2 := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 2,
		})

		m2, _ := updateModel(t, m, status2)

		if m2.flashMessage != "Config reloaded" {
			t.Errorf("Expected flash 'Config reloaded', got %q", m2.flashMessage)
		}
		if m2.lastConfigReloadCounter != 2 {
			t.Errorf("Expected lastConfigReloadCounter updated to 2, got %d", m2.lastConfigReloadCounter)
		}
	})

	t.Run("flash when ConfigReloadCounter changes from zero to non-zero", func(t *testing.T) {
		// Model has fetched status once but daemon hadn't reloaded yet
		m := newTuiModel(testServerAddr)
		m.statusFetchedOnce = true
		m.lastConfigReloadCounter = 0 // No reload had occurred

		// Now config is reloaded
		status := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 1,
		})

		m2, _ := updateModel(t, m, status)

		if m2.flashMessage != "Config reloaded" {
			t.Errorf("Expected flash when ConfigReloadCounter goes from 0 to 1, got %q", m2.flashMessage)
		}
	})

	t.Run("no flash when ConfigReloadCounter unchanged", func(t *testing.T) {
		m := newTuiModel(testServerAddr)
		m.statusFetchedOnce = true
		m.lastConfigReloadCounter = 1

		// Same counter
		status := tuiStatusMsg(storage.DaemonStatus{
			Version:             "1.0.0",
			ConfigReloadCounter: 1,
		})

		m2, _ := updateModel(t, m, status)

		if m2.flashMessage != "" {
			t.Errorf("Expected no flash when counter unchanged, got %q", m2.flashMessage)
		}
	})
}

func TestTUIReconnectOnConsecutiveErrors(t *testing.T) {
	setupTuiTestEnv(t)
	t.Run("triggers reconnection after 3 consecutive connection errors", func(t *testing.T) {
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 2 // Already had 2 connection errors

		// Third connection error should trigger reconnection
		m2, cmd := updateModel(t, m, tuiJobsErrMsg{err: mockConnError("connection refused")})

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
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 1 // Only 1 error so far

		m2, cmd := updateModel(t, m, tuiJobsErrMsg{err: mockConnError("connection refused")})

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
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 2 // 2 connection errors

		// Application error (404, parse error, etc.) should not increment counter
		m2, cmd := updateModel(t, m, tuiErrMsg(fmt.Errorf("no review found")))

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
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 2

		// Non-connection error (like parse error) should not increment
		m2, _ := updateModel(t, m, tuiJobsErrMsg{err: fmt.Errorf("invalid JSON response")})

		if m2.consecutiveErrors != 2 {
			t.Errorf("Expected consecutiveErrors unchanged at 2, got %d", m2.consecutiveErrors)
		}
	})

	t.Run("pagination errors also trigger reconnection", func(t *testing.T) {
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 2

		// Connection error in pagination should trigger reconnection
		m2, cmd := updateModel(t, m, tuiPaginationErrMsg{err: mockConnError("connection refused")})

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
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 2

		// Connection error via tuiErrMsg (from fetchStatus, fetchReview, etc.)
		m2, cmd := updateModel(t, m, tuiErrMsg(mockConnError("connection refused")))

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
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 2

		// Application error via tuiErrMsg should not increment
		m2, cmd := updateModel(t, m, tuiErrMsg(fmt.Errorf("review not found")))

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
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 5

		m2, _ := updateModel(t, m, tuiJobsMsg{jobs: []storage.ReviewJob{}, hasMore: false})

		if m2.consecutiveErrors != 0 {
			t.Errorf("Expected consecutiveErrors=0 after success, got %d", m2.consecutiveErrors)
		}
	})

	t.Run("resets error count on successful status fetch", func(t *testing.T) {
		m := newTuiModel(testServerAddr)
		m.consecutiveErrors = 5

		m2, _ := updateModel(t, m, tuiStatusMsg(storage.DaemonStatus{Version: "1.0.0"}))

		if m2.consecutiveErrors != 0 {
			t.Errorf("Expected consecutiveErrors=0 after status success, got %d", m2.consecutiveErrors)
		}
	})

	t.Run("updates server address on successful reconnection", func(t *testing.T) {
		m := newTuiModel(testServerAddr)
		m.reconnecting = true

		m2, cmd := updateModel(t, m, tuiReconnectMsg{newAddr: "http://127.0.0.1:7374", version: "2.0.0"})

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
		m := newTuiModel(testServerAddr)
		m.reconnecting = true
		m.consecutiveErrors = 3

		// Same address - no change needed
		m2, cmd := updateModel(t, m, tuiReconnectMsg{newAddr: testServerAddr})

		if m2.serverAddr != testServerAddr {
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
		m := newTuiModel(testServerAddr)
		m.reconnecting = true
		m.consecutiveErrors = 3

		m2, cmd := updateModel(t, m, tuiReconnectMsg{err: fmt.Errorf("no daemon found")})

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

func TestTUIStatusDisplaysCorrectly(t *testing.T) {
	// Test that the queue view renders status correctly
	m := newTuiModel("http://localhost")
	m.width = 200
	m.height = 30
	m.currentView = tuiViewQueue

	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoName("repo"), withRepoPath("/path"), withRef("abc"), withStatus(storage.JobStatusRunning)),
		makeJob(2, withRepoName("repo"), withRepoPath("/path"), withRef("def"), withStatus(storage.JobStatusQueued)),
		makeJob(3, withRepoName("repo"), withRepoPath("/path"), withRef("ghi")),
		makeJob(4, withRepoName("repo"), withRepoPath("/path"), withRef("jkl"), withStatus(storage.JobStatusFailed)),
		makeJob(5, withRepoName("repo"), withRepoPath("/path"), withRef("mno"), withStatus(storage.JobStatusCanceled)),
	}
	m.selectedIdx = 0

	output := m.View()
	if len(output) == 0 {
		t.Error("Expected non-empty view output")
	}

	// Verify all status strings appear in output
	for _, status := range []string{"running", "queued", "done", "failed", "canceled"} {
		if !strings.Contains(output, status) {
			t.Errorf("Expected output to contain status '%s'", status)
		}
	}
}
