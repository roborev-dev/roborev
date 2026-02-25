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
	"reflect"
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
func setupTuiTestEnv(t *testing.T) string {
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
	return tmpDir
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
	return ts, newTuiModel(ts.URL, WithExternalIODisabled())
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

func withEnqueuedAt(t time.Time) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.EnqueuedAt = t }
}

func withError(err string) func(*storage.ReviewJob) {
	return func(j *storage.ReviewJob) { j.Error = err }
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

func withReviewAgent(agent string) func(*storage.Review) {
	return func(r *storage.Review) { r.Agent = agent }
}

func TestTUIFetchJobsSuccess(t *testing.T) {
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/jobs" {
			t.Errorf("Expected /api/jobs, got %s", r.URL.Path)
		}
		jobs := []storage.ReviewJob{{ID: 1, GitRef: "abc123", Agent: "test"}}
		json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
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
		json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{}})
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

func TestTUIGetVisibleJobs(t *testing.T) {
	m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
	// Shared setup
	jobs := []storage.ReviewJob{
		makeJob(1, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
		makeJob(2, withRepoName("repo-b"), withRepoPath("/path/to/repo-b")),
		makeJob(3, withRepoName("repo-a"), withRepoPath("/path/to/repo-a")),
	}

	tests := []struct {
		name        string
		selectedIdx int
		filter      []string
		want        int
	}{
		{"No filter, valid selection", 1, nil, 1},
		{"No filter, no selection", -1, nil, -1},
		{"Filter active, valid visible selection", 2, []string{"/path/to/repo-a"}, 1}, // index 2 is job ID 3, which is 2nd in filtered list (index 1)
		{"Filter active, selection hidden", 1, []string{"/path/to/repo-a"}, -1},       // index 1 is job ID 2, not in repo-a
		{"Filter active, selectedIdx -1", -1, []string{"/path/to/repo-a"}, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTuiModel(testServerAddr, WithExternalIODisabled())
			m.jobs = jobs
			m.selectedIdx = tt.selectedIdx
			if tt.filter != nil {
				m.activeRepoFilter = tt.filter
			}

			if got := m.getVisibleSelectedIdx(); got != tt.want {
				t.Errorf("getVisibleSelectedIdx() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTUITickNoRefreshWhileLoadingJobs(t *testing.T) {
	m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
			m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
	m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
	m := newTuiModel(testServerAddr, WithExternalIODisabled())

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

func TestTUINewModelLoadingJobsTrue(t *testing.T) {
	// newTuiModel should initialize loadingJobs to true since Init() calls fetchJobs
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
	if !m.loadingJobs {
		t.Error("loadingJobs should be true in new model")
	}
}

func TestTUIJobsErrMsgClearsLoadingJobs(t *testing.T) {
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

func TestTUIHideAddressedMalformedConfigNotOverwritten(t *testing.T) {
	tmpDir := setupTuiTestEnv(t)

	// Write malformed TOML that LoadGlobal will fail to parse
	malformed := []byte(`this is not valid toml {{{`)
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, malformed, 0644); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}

	m := newTuiModel(testServerAddr)
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

func TestTUIIsJobVisibleRespectsPendingAddressed(t *testing.T) {
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

func TestTUIUpdateNotificationInQueueView(t *testing.T) {
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
	_ = setupTuiTestEnv(t)

	t.Run("detects version mismatch", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
		m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
	_ = setupTuiTestEnv(t)
	m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
	_ = setupTuiTestEnv(t)

	type testCase struct {
		name              string
		initialErrors     int
		reconnecting      bool
		initialErr        error
		msg               tea.Msg
		wantErrors        int
		wantReconnecting  bool
		wantCmd           bool
		wantServerAddr    string
		wantDaemonVersion string
		wantErrNil        bool
	}

	tests := []testCase{
		{
			name:             "triggers reconnection after 3 consecutive connection errors",
			initialErrors:    2,
			msg:              tuiJobsErrMsg{err: mockConnError("connection refused")},
			wantErrors:       3,
			wantReconnecting: true,
			wantCmd:          true,
		},
		{
			name:             "does not trigger reconnection before 3 errors",
			initialErrors:    1,
			msg:              tuiJobsErrMsg{err: mockConnError("connection refused")},
			wantErrors:       2,
			wantReconnecting: false,
			wantCmd:          false,
		},
		{
			name:             "does not count application errors for reconnection",
			initialErrors:    2,
			msg:              tuiErrMsg(fmt.Errorf("no review found")),
			wantErrors:       2,
			wantReconnecting: false,
			wantCmd:          false,
		},
		{
			name:             "does not count non-connection errors in jobs fetch",
			initialErrors:    2,
			msg:              tuiJobsErrMsg{err: fmt.Errorf("invalid JSON response")},
			wantErrors:       2,
			wantReconnecting: false,
			wantCmd:          false,
		},
		{
			name:             "pagination errors also trigger reconnection",
			initialErrors:    2,
			msg:              tuiPaginationErrMsg{err: mockConnError("connection refused")},
			wantErrors:       3,
			wantReconnecting: true,
			wantCmd:          true,
		},
		{
			name:             "status/review connection errors trigger reconnection",
			initialErrors:    2,
			msg:              tuiErrMsg(mockConnError("connection refused")),
			wantErrors:       3,
			wantReconnecting: true,
			wantCmd:          true,
		},
		{
			name:             "status/review application errors do not trigger reconnection",
			initialErrors:    2,
			msg:              tuiErrMsg(fmt.Errorf("review not found")),
			wantErrors:       2,
			wantReconnecting: false,
			wantCmd:          false,
		},
		{
			name:             "resets error count on successful jobs fetch",
			initialErrors:    5,
			msg:              tuiJobsMsg{jobs: []storage.ReviewJob{}, hasMore: false},
			wantErrors:       0,
			wantReconnecting: false,
			wantCmd:          false,
		},
		{
			name:             "resets error count on successful status fetch",
			initialErrors:    5,
			msg:              tuiStatusMsg(storage.DaemonStatus{Version: "1.0.0"}),
			wantErrors:       0,
			wantReconnecting: false,
			wantCmd:          false,
		},
		{
			name:              "updates server address on successful reconnection",
			initialErrors:     0,
			reconnecting:      true,
			initialErr:        errors.New("connection refused"),
			msg:               tuiReconnectMsg{newAddr: "http://127.0.0.1:7374", version: "2.0.0"},
			wantErrors:        0,
			wantReconnecting:  false,
			wantCmd:           true,
			wantServerAddr:    "http://127.0.0.1:7374",
			wantDaemonVersion: "2.0.0",
			wantErrNil:        true,
		},
		{
			name:             "handles reconnection to same address",
			initialErrors:    3,
			reconnecting:     true,
			msg:              tuiReconnectMsg{newAddr: testServerAddr},
			wantErrors:       3,
			wantReconnecting: false,
			wantCmd:          false,
			wantServerAddr:   testServerAddr,
		},
		{
			name:             "handles failed reconnection",
			initialErrors:    3,
			reconnecting:     true,
			msg:              tuiReconnectMsg{err: fmt.Errorf("no daemon found")},
			wantErrors:       3,
			wantReconnecting: false,
			wantCmd:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTuiModel(testServerAddr, WithExternalIODisabled())
			m.consecutiveErrors = tt.initialErrors
			m.reconnecting = tt.reconnecting
			m.err = tt.initialErr

			m2, cmd := updateModel(t, m, tt.msg)

			if m2.consecutiveErrors != tt.wantErrors {
				t.Errorf("consecutiveErrors = %d, want %d", m2.consecutiveErrors, tt.wantErrors)
			}
			if m2.reconnecting != tt.wantReconnecting {
				t.Errorf("reconnecting = %v, want %v", m2.reconnecting, tt.wantReconnecting)
			}
			if (cmd != nil) != tt.wantCmd {
				t.Errorf("cmd returned = %v, want %v", cmd != nil, tt.wantCmd)
			}
			if tt.wantServerAddr != "" && m2.serverAddr != tt.wantServerAddr {
				t.Errorf("serverAddr = %q, want %q", m2.serverAddr, tt.wantServerAddr)
			}
			if tt.wantDaemonVersion != "" && m2.daemonVersion != tt.wantDaemonVersion {
				t.Errorf("daemonVersion = %q, want %q", m2.daemonVersion, tt.wantDaemonVersion)
			}
			if tt.wantErrNil && m2.err != nil {
				t.Errorf("expected err to be nil, got %v", m2.err)
			}
		})
	}
}

func TestTUIStatusDisplaysCorrectly(t *testing.T) {
	// Test that the queue view renders status correctly
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

func TestHandleFixKeyRejectsFixJob(t *testing.T) {
	m := newTuiModel(testServerAddr, WithExternalIODisabled())
	m.currentView = tuiViewQueue
	m.jobs = []storage.ReviewJob{
		{
			ID:      10,
			Status:  storage.JobStatusDone,
			JobType: storage.JobTypeFix,
		},
	}
	m.selectedIdx = 0

	result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	updated := result.(tuiModel)

	if cmd != nil {
		t.Error("Expected nil cmd for rejected fix-of-fix")
	}
	if updated.flashMessage != "Cannot fix a fix job" {
		t.Errorf(
			"Expected flash 'Cannot fix a fix job', got %q",
			updated.flashMessage,
		)
	}
	if updated.currentView != tuiViewQueue {
		t.Errorf(
			"Expected view to stay on queue, got %d",
			updated.currentView,
		)
	}
}

func TestTUIFixTriggerResultMsg(t *testing.T) {
	t.Run("warning shows flash and triggers refresh", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
		m.currentView = tuiViewTasks
		m.width = 80
		m.height = 24

		msg := tuiFixTriggerResultMsg{
			job:     &storage.ReviewJob{ID: 42},
			warning: "rebase job #42 enqueued but failed to mark #10 as rebased: server error",
		}

		result, cmd := m.Update(msg)
		updated := result.(tuiModel)

		if !strings.Contains(updated.flashMessage, "failed to mark") {
			t.Errorf("expected warning in flash, got %q", updated.flashMessage)
		}
		if updated.flashView != tuiViewTasks {
			t.Errorf("expected flash view tasks, got %v", updated.flashView)
		}
		if cmd == nil {
			t.Error("expected refresh cmd, got nil")
		}
	})

	t.Run("success shows enqueued flash and triggers refresh", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
		m.currentView = tuiViewTasks
		m.width = 80
		m.height = 24

		msg := tuiFixTriggerResultMsg{
			job: &storage.ReviewJob{ID: 99},
		}

		result, cmd := m.Update(msg)
		updated := result.(tuiModel)

		if !strings.Contains(updated.flashMessage, "#99 enqueued") {
			t.Errorf("expected enqueued flash, got %q", updated.flashMessage)
		}
		if cmd == nil {
			t.Error("expected refresh cmd, got nil")
		}
	})

	t.Run("error shows failure flash with no refresh", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
		m.currentView = tuiViewTasks
		m.width = 80
		m.height = 24

		msg := tuiFixTriggerResultMsg{
			err: fmt.Errorf("connection refused"),
		}

		result, cmd := m.Update(msg)
		updated := result.(tuiModel)

		if !strings.Contains(updated.flashMessage, "Fix failed") {
			t.Errorf("expected failure flash, got %q", updated.flashMessage)
		}
		if cmd != nil {
			t.Error("expected no cmd on error, got non-nil")
		}
	})
}
func TestTUISelection(t *testing.T) {
	t.Run("MaintainedOnInsert", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
		assertSelection(t, m, 3, 2)

	})
	t.Run("ClampsOnRemoval", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
		assertSelection(t, m, 1, 2)

	})
	t.Run("FirstJobOnEmpty", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())

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
		assertSelection(t, m, 0, 5)

	})
	t.Run("EmptyList", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())

		// Had jobs, now empty
		m.jobs = []storage.ReviewJob{makeJob(1)}
		m.selectedIdx = 0
		m.selectedJobID = 1

		newJobs := tuiJobsMsg{jobs: []storage.ReviewJob{}}

		m, _ = updateModel(t, m, newJobs)

		// Empty list should have selectedIdx=-1 (no valid selection)
		assertSelection(t, m, -1, 0)

	})
	t.Run("MaintainedOnLargeBatch", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())

		// Initial state with 1 job selected
		m.jobs = []storage.ReviewJob{makeJob(1)}
		m.selectedIdx = 0
		m.selectedJobID = 1

		// 30 new jobs added at the top (simulating large batch)
		newJobs := make([]storage.ReviewJob, 31)
		for i := range 30 {
			newJobs[i] = makeJob(int64(31 - i)) // IDs 31, 30, 29, ..., 2
		}
		newJobs[30] = makeJob(1) // Original job at the end

		m, _ = updateModel(t, m, tuiJobsMsg{jobs: newJobs})

		// Should still follow job ID=1, now at index 30
		assertSelection(t, m, 30, 1)

	})
}

func TestTUIHideAddressed(t *testing.T) {
	t.Run("DefaultFromConfig", func(t *testing.T) {
		tmpDir := setupTuiTestEnv(t)

		configPath := filepath.Join(tmpDir, "config.toml")
		if err := os.WriteFile(configPath, []byte("hide_addressed_by_default = true\n"), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		m := newTuiModel(testServerAddr)
		if !m.hideAddressed {
			t.Error("hideAddressed should be true when config sets hide_addressed_by_default = true")
		}

	})
	t.Run("Toggle", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
	t.Run("FiltersJobs", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
	t.Run("SelectionMovesToVisible", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
		assertSelection(t, m2, 1, 2)

	})
	t.Run("RefreshRevalidatesSelection", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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
		assertSelection(t, m2, 1, 2)

	})
	t.Run("NavigationSkipsHidden", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

		assertSelection(t, m2, 3, 4)

	})
	t.Run("WithRepoFilter", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
	t.Run("ClearRepoFilterRefetches", func(t *testing.T) {
		// Scenario: hide addressed enabled, then filter by repo, then press escape
		// to clear the repo filter. Should trigger a refetch to show all unaddressed reviews.
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
	t.Run("EnableTriggersRefetch", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
	t.Run("DisableRefetches", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
	t.Run("ValidConfigNotMutated", func(t *testing.T) {
		tmpDir := setupTuiTestEnv(t)

		// Write a valid config with the hide-addressed default enabled
		validConfig := []byte("hide_addressed_by_default = true\n")
		configPath := filepath.Join(tmpDir, "config.toml")
		if err := os.WriteFile(configPath, validConfig, 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		m := newTuiModel(testServerAddr)
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

	})
}

func TestTUIFlashMessage(t *testing.T) {
	t.Run("AppearsInQueueView", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
	t.Run("NotShownInDifferentView", func(t *testing.T) {
		m := newTuiModel(testServerAddr, WithExternalIODisabled())
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

	})
}

func TestNewTuiModelOptions(t *testing.T) {
	tests := []struct {
		name                 string
		opts                 []tuiOption
		expectedRepoFilter   []string
		expectedBranchFilter string
		expectedFilterStack  []string
		expectedLockedRepo   bool
		expectedLockedBranch bool
		expectedDaemonVer    string
	}{
		{
			name:                 "WithExternalIODisabled only",
			opts:                 []tuiOption{WithExternalIODisabled()},
			expectedRepoFilter:   nil,
			expectedBranchFilter: "",
			expectedFilterStack:  nil,
			expectedLockedRepo:   false,
			expectedLockedBranch: false,
			expectedDaemonVer:    "?",
		},
		{
			name:                 "With RepoFilter",
			opts:                 []tuiOption{WithExternalIODisabled(), WithRepoFilter("/path/to/repo")},
			expectedRepoFilter:   []string{"/path/to/repo"},
			expectedBranchFilter: "",
			expectedFilterStack:  []string{filterTypeRepo},
			expectedLockedRepo:   true,
			expectedLockedBranch: false,
			expectedDaemonVer:    "?",
		},
		{
			name:                 "With BranchFilter",
			opts:                 []tuiOption{WithExternalIODisabled(), WithBranchFilter("feature-branch")},
			expectedRepoFilter:   nil,
			expectedBranchFilter: "feature-branch",
			expectedFilterStack:  []string{filterTypeBranch},
			expectedLockedRepo:   false,
			expectedLockedBranch: true,
			expectedDaemonVer:    "?",
		},
		{
			name:                 "With RepoFilter and BranchFilter",
			opts:                 []tuiOption{WithExternalIODisabled(), WithRepoFilter("/path/to/repo"), WithBranchFilter("feature-branch")},
			expectedRepoFilter:   []string{"/path/to/repo"},
			expectedBranchFilter: "feature-branch",
			expectedFilterStack:  []string{filterTypeRepo, filterTypeBranch},
			expectedLockedRepo:   true,
			expectedLockedBranch: true,
			expectedDaemonVer:    "?",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTuiModel(testServerAddr, tc.opts...)

			if !reflect.DeepEqual(m.activeRepoFilter, tc.expectedRepoFilter) {
				t.Errorf("activeRepoFilter mismatch: got %v, want %v", m.activeRepoFilter, tc.expectedRepoFilter)
			}
			if m.activeBranchFilter != tc.expectedBranchFilter {
				t.Errorf("activeBranchFilter mismatch: got %q, want %q", m.activeBranchFilter, tc.expectedBranchFilter)
			}
			if !reflect.DeepEqual(m.filterStack, tc.expectedFilterStack) {
				t.Errorf("filterStack mismatch: got %v, want %v", m.filterStack, tc.expectedFilterStack)
			}
			if m.lockedRepoFilter != tc.expectedLockedRepo {
				t.Errorf("lockedRepoFilter mismatch: got %v, want %v", m.lockedRepoFilter, tc.expectedLockedRepo)
			}
			if m.lockedBranchFilter != tc.expectedLockedBranch {
				t.Errorf("lockedBranchFilter mismatch: got %v, want %v", m.lockedBranchFilter, tc.expectedLockedBranch)
			}
			if m.daemonVersion != tc.expectedDaemonVer {
				t.Errorf("daemonVersion mismatch: got %q, want %q", m.daemonVersion, tc.expectedDaemonVer)
			}
		})
	}
}
