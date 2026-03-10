package tui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Unit tests for control types ---

func TestViewKindString(t *testing.T) {
	tests := []struct {
		v    viewKind
		want string
	}{
		{viewQueue, "queue"},
		{viewReview, "review"},
		{viewTasks, "tasks"},
		{viewLog, "log"},
		{viewFilter, "filter"},
		{viewPatch, "patch"},
		{viewKind(999), "unknown"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.v.String(),
			"viewKind(%d).String()", tt.v)
	}
}

func TestParseViewKind(t *testing.T) {
	tests := []struct {
		s    string
		want viewKind
	}{
		{"queue", viewQueue},
		{"tasks", viewTasks},
		{"invalid", viewKind(-1)},
		{"review", viewKind(-1)}, // only queue and tasks are settable
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseViewKind(tt.s),
			"parseViewKind(%q)", tt.s)
	}
}

func TestIsControlCommand(t *testing.T) {
	tests := []struct {
		cmd        string
		wantQuery  bool
		wantMutate bool
	}{
		{"get-state", true, false},
		{"get-filter", true, false},
		{"set-filter", false, true},
		{"cancel-job", false, true},
		{"bogus", false, false},
	}
	for _, tt := range tests {
		isQ, isM := isControlCommand(tt.cmd)
		assert.Equal(t, tt.wantQuery, isQ,
			"isControlCommand(%q) query", tt.cmd)
		assert.Equal(t, tt.wantMutate, isM,
			"isControlCommand(%q) mutate", tt.cmd)
	}
}

// --- Unit tests for query handlers ---

func TestBuildStateResponse(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRepoPath("/a")),
		makeJob(2, withRepoPath("/b")),
	}
	m.selectedJobID = 1
	m.hideClosed = true

	resp := m.buildStateResponse()
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)

	data, ok := resp.Data.(stateSnapshot)
	require.True(t, ok, "expected stateSnapshot, got %T", resp.Data)
	assert.Equal(t, "queue", data.View)
	assert.Equal(t, 2, data.JobCount)
	assert.True(t, data.HideClosed)
}

func TestBuildFilterResponse(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.activeRepoFilter = []string{"/repo"}
	m.activeBranchFilter = "main"
	m.lockedRepoFilter = true
	m.filterStack = []string{"repo", "branch"}

	resp := m.buildFilterResponse()
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
}

func TestBuildJobsResponse(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(1, withAgent("claude-code"), withRepoPath("/r")),
		makeJob(2, withAgent("codex"), withRepoPath("/r")),
	}

	resp := m.buildJobsResponse()
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)

	jobs, ok := resp.Data.([]jobSnapshot)
	require.True(t, ok, "expected []jobSnapshot, got %T", resp.Data)
	require.Len(t, jobs, 2)
	assert.Equal(t, "claude-code", jobs[0].Agent)
}

func TestBuildSelectedResponse_NoSelection(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.selectedIdx = -1

	resp := m.buildSelectedResponse()
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	data := resp.Data.(selectedSnapshot)
	assert.Nil(t, data.Job)
}

func TestBuildSelectedResponse_WithSelection(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(42, withAgent("codex"), withClosed(boolPtr(false))),
	}
	m.selectedIdx = 0
	m.selectedJobID = 42

	resp := m.buildSelectedResponse()
	data := resp.Data.(selectedSnapshot)
	require.NotNil(t, data.Job)
	assert.EqualValues(t, 42, data.Job.ID)
	assert.True(t, data.HasReview,
		"expected has_review=true for done job with closed field")
}

// --- Unit tests for mutation handlers ---

func TestHandleCtrlSetFilter(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	repo := "/test/repo"
	branch := "feature"

	params, _ := json.Marshal(map[string]string{
		"repo":   repo,
		"branch": branch,
	})
	updated, resp, _ := m.handleCtrlSetFilter(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.Equal(t, []string{repo}, updated.activeRepoFilter)
	assert.Equal(t, branch, updated.activeBranchFilter)
	assert.Equal(t, -1, updated.selectedIdx)
}

func TestHandleCtrlSetFilter_LockedRepo(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.lockedRepoFilter = true

	params, _ := json.Marshal(map[string]string{"repo": "/test/repo"})
	_, resp, _ := m.handleCtrlSetFilter(params)
	require.False(t, resp.OK, "expected error for locked repo filter")
	assert.NotEmpty(t, resp.Error)
}

func TestHandleCtrlSetFilter_LockedBranch(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.lockedBranchFilter = true

	params, _ := json.Marshal(map[string]string{"branch": "main"})
	_, resp, _ := m.handleCtrlSetFilter(params)
	require.False(t, resp.OK, "expected error for locked branch filter")
}

func TestHandleCtrlSetFilter_ClearRepo(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.activeRepoFilter = []string{"/old"}
	m.filterStack = []string{"repo"}

	empty := ""
	params, _ := json.Marshal(map[string]*string{"repo": &empty})
	updated, resp, _ := m.handleCtrlSetFilter(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.Nil(t, updated.activeRepoFilter)
}

func TestHandleCtrlClearFilter(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.activeRepoFilter = []string{"/repo"}
	m.activeBranchFilter = "main"
	m.filterStack = []string{"repo", "branch"}

	params, _ := json.Marshal(map[string]bool{
		"repo": true, "branch": true,
	})
	updated, resp, _ := m.handleCtrlClearFilter(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.Nil(t, updated.activeRepoFilter)
	assert.Empty(t, updated.activeBranchFilter)
}

func TestHandleCtrlSetHideClosed(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.hideClosed = false

	params, _ := json.Marshal(map[string]bool{"hide_closed": true})
	updated, resp, _ := m.handleCtrlSetHideClosed(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.True(t, updated.hideClosed)
}

func TestHandleCtrlSelectJob(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(10), makeJob(20), makeJob(30),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 20})
	updated, resp, _ := m.handleCtrlSelectJob(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.Equal(t, 1, updated.selectedIdx)
	assert.EqualValues(t, 20, updated.selectedJobID)
}

func TestHandleCtrlSelectJob_NotFound(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{makeJob(1)}

	params, _ := json.Marshal(map[string]int64{"job_id": 999})
	_, resp, _ := m.handleCtrlSelectJob(params)
	require.False(t, resp.OK, "expected error for missing job")
}

func TestHandleCtrlSelectJob_HiddenByFilter(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(10, withRepoPath("/visible")),
		makeJob(20, withRepoPath("/hidden")),
	}
	m.activeRepoFilter = []string{"/visible"}

	params, _ := json.Marshal(map[string]int64{"job_id": 20})
	_, resp, _ := m.handleCtrlSelectJob(params)
	require.False(t, resp.OK, "expected error for hidden job")
}

func TestHandleCtrlSelectJob_HiddenByClosed(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.hideClosed = true
	m.jobs = []storage.ReviewJob{
		makeJob(10, withClosed(boolPtr(false))),
		makeJob(20, withClosed(boolPtr(true))),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 20})
	_, resp, _ := m.handleCtrlSelectJob(params)
	require.False(t, resp.OK, "expected error for closed-hidden job")
}

func TestHandleCtrlSetView(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.currentView = viewQueue

	params, _ := json.Marshal(map[string]string{"view": "queue"})
	updated, resp, _ := m.handleCtrlSetView(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.Equal(t, viewQueue, updated.currentView)
}

func TestHandleCtrlSetView_Invalid(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())

	params, _ := json.Marshal(map[string]string{"view": "review"})
	_, resp, _ := m.handleCtrlSetView(params)
	require.False(t, resp.OK, "expected error for unsettable view")
}

func TestHandleCtrlSetView_TasksDisabled(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.tasksEnabled = false

	params, _ := json.Marshal(map[string]string{"view": "tasks"})
	_, resp, _ := m.handleCtrlSetView(params)
	require.False(t, resp.OK,
		"expected error when tasks workflow disabled")
}

func TestHandleCtrlCloseReview(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	closed := false
	m.jobs = []storage.ReviewJob{
		makeJob(5, withClosed(&closed)),
	}

	closedTrue := true
	params, _ := json.Marshal(map[string]any{
		"job_id": int64(5),
		"closed": closedTrue,
	})
	_, resp, cmd := m.handleCtrlCloseReview(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.NotNil(t, cmd, "expected non-nil cmd for close operation")
}

func TestHandleCtrlCloseReview_NonSelectedNoReflow(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.hideClosed = true
	closed := false
	m.jobs = []storage.ReviewJob{
		makeJob(1, withClosed(boolPtr(false))),
		makeJob(2, withClosed(&closed)),
		makeJob(3, withClosed(boolPtr(false))),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Close job 2, which is not the selected job
	params, _ := json.Marshal(map[string]any{
		"job_id": int64(2),
		"closed": true,
	})
	updated, resp, _ := m.handleCtrlCloseReview(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.EqualValues(t, 1, updated.selectedJobID,
		"selection should remain on job 1")
	assert.Equal(t, 0, updated.selectedIdx,
		"selectedIdx should remain 0")
}

func TestHandleCtrlCloseReview_NoReview(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(5, withStatus(storage.JobStatusRunning)),
	}

	params, _ := json.Marshal(map[string]any{"job_id": int64(5)})
	_, resp, _ := m.handleCtrlCloseReview(params)
	require.False(t, resp.OK, "expected error for job without review")
}

func TestHandleCtrlCancelJob(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(7, withStatus(storage.JobStatusRunning)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 7})
	updated, resp, cmd := m.handleCtrlCancelJob(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.NotNil(t, cmd, "expected non-nil cmd for cancel")
	assert.Equal(t, storage.JobStatusCanceled, updated.jobs[0].Status)
}

func TestHandleCtrlCancelJob_NonSelectedNoReflow(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.hideClosed = true
	m.jobs = []storage.ReviewJob{
		makeJob(1, withClosed(boolPtr(false))),
		makeJob(2, withStatus(storage.JobStatusRunning)),
		makeJob(3, withClosed(boolPtr(false))),
	}
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Cancel job 2, which is not the selected job
	params, _ := json.Marshal(map[string]int64{"job_id": 2})
	updated, resp, _ := m.handleCtrlCancelJob(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.EqualValues(t, 1, updated.selectedJobID,
		"selection should remain on job 1")
	assert.Equal(t, 0, updated.selectedIdx,
		"selectedIdx should remain 0")
}

func TestHandleCtrlCancelJob_WrongStatus(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(7, withStatus(storage.JobStatusDone)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 7})
	_, resp, _ := m.handleCtrlCancelJob(params)
	require.False(t, resp.OK, "expected error for non-cancellable job")
}

func TestHandleCtrlRerunJob(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(8, withStatus(storage.JobStatusFailed)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 8})
	updated, resp, cmd := m.handleCtrlRerunJob(params)
	require.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	assert.NotNil(t, cmd, "expected non-nil cmd for rerun")
	assert.Equal(t, storage.JobStatusQueued, updated.jobs[0].Status)
}

func TestHandleCtrlRerunJob_WrongStatus(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(8, withStatus(storage.JobStatusRunning)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 8})
	_, resp, _ := m.handleCtrlRerunJob(params)
	require.False(t, resp.OK, "expected error for non-rerunnable job")
}

// --- Control message routing through Update() ---

func TestUpdateRoutesControlQuery(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{makeJob(1)}

	respCh := make(chan controlResponse, 1)
	msg := controlQueryMsg{
		req:    controlRequest{Command: "get-state"},
		respCh: respCh,
	}

	updated, _ := m.Update(msg)
	_, ok := updated.(model)
	require.True(t, ok, "Update returned %T, want model", updated)

	select {
	case resp := <-respCh:
		assert.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	default:
		require.Fail(t, "no response received on channel")
	}
}

func TestUpdateRoutesControlMutation(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{makeJob(1), makeJob(2)}

	params, _ := json.Marshal(map[string]int64{"job_id": 2})
	respCh := make(chan controlResponse, 1)
	msg := controlMutationMsg{
		req: controlRequest{
			Command: "select-job", Params: params,
		},
		respCh: respCh,
	}

	updated, _ := m.Update(msg)
	um := updated.(model)
	assert.EqualValues(t, 2, um.selectedJobID)

	select {
	case resp := <-respCh:
		assert.True(t, resp.OK, "expected OK, got error: %s", resp.Error)
	default:
		require.Fail(t, "no response received on channel")
	}
}

// --- Integration test with real Unix socket ---

func TestControlSocketRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	ts := controlTestServer(t)

	m := newModel(ts.URL, withExternalIODisabled())

	// Provide a pipe for stdin so the program doesn't block on TTY.
	r, w, _ := os.Pipe()
	p := tea.NewProgram(m,
		tea.WithoutRenderer(),
		tea.WithInput(r),
	)
	runDone := make(chan struct{})
	go func() { _, _ = p.Run(); close(runDone) }()
	t.Cleanup(func() {
		// Close the write end first so bubbletea's readLoop
		// sees EOF and exits before Kill's shutdown closes the
		// cancel reader, avoiding a data race on the fd.
		w.Close()
		p.Kill()
		<-runDone
		// Bubbletea does not close custom readers passed via
		// WithInput, so close the read end after Run exits.
		r.Close()
	})

	// Give the program time to complete Init() and reach its
	// steady-state event loop.
	time.Sleep(500 * time.Millisecond)

	cleanup, err := startControlListener(socketPath, p)
	require.NoError(t, err, "startControlListener")
	t.Cleanup(cleanup)

	// Test get-state
	resp := sendControlCommand(t, socketPath,
		`{"command":"get-state"}`)
	require.True(t, resp.OK, "get-state failed: %s", resp.Error)

	// Test get-jobs
	resp = sendControlCommand(t, socketPath,
		`{"command":"get-jobs"}`)
	require.True(t, resp.OK, "get-jobs failed: %s", resp.Error)

	// Test set-hide-closed mutation
	resp = sendControlCommand(t, socketPath,
		`{"command":"set-hide-closed","params":{"hide_closed":true}}`)
	require.True(t, resp.OK,
		"set-hide-closed failed: %s", resp.Error)

	// Verify state via get-state
	resp = sendControlCommand(t, socketPath,
		`{"command":"get-state"}`)
	require.True(t, resp.OK,
		"get-state after mutation: %s", resp.Error)

	// Test unknown command
	resp = sendControlCommand(t, socketPath,
		`{"command":"nope"}`)
	assert.False(t, resp.OK, "expected error for unknown command")
}

// controlTestServer returns an httptest.Server that handles all
// TUI Init() API requests with valid empty responses.
func controlTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/jobs":
				json.NewEncoder(w).Encode(map[string]any{
					"jobs":     []any{},
					"has_more": false,
					"stats":    storage.JobStats{},
				})
			case "/api/status":
				json.NewEncoder(w).Encode(
					storage.DaemonStatus{Version: "test"},
				)
			default:
				json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
				})
			}
		},
	))
	t.Cleanup(ts.Close)
	return ts
}

func TestControlSocketInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Invalid JSON is rejected at the socket handler level before
	// Program.Send(), so no real HTTP server needed.
	cleanup, err := startControlListener(
		socketPath, newTestProgram(t),
	)
	require.NoError(t, err, "startControlListener")
	t.Cleanup(cleanup)

	resp := sendControlCommand(t, socketPath, `{not json}`)
	assert.False(t, resp.OK, "expected error for invalid JSON")
}

// newTestProgram creates a tea.Program backed by a mock HTTP server
// so that Init() commands complete quickly.
func newTestProgram(t *testing.T) *tea.Program {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{})
		},
	))
	t.Cleanup(ts.Close)
	m := newModel(ts.URL, withExternalIODisabled())
	p := tea.NewProgram(m, tea.WithoutRenderer())
	go func() { _, _ = p.Run() }()
	t.Cleanup(func() { p.Kill() })
	time.Sleep(100 * time.Millisecond)
	return p
}

// --- Stale socket safety tests ---

func TestRemoveStaleSocket_NonexistentPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nosuch.sock")
	assert.NoError(t, removeStaleSocket(path))
}

func TestRemoveStaleSocket_RegularFileRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0600))

	err := removeStaleSocket(path)
	require.Error(t, err, "expected error for regular file")
	assert.FileExists(t, path, "regular file should not be deleted")
}

func TestRemoveStaleSocket_StaleSocketRemoved(t *testing.T) {
	// Use a short path to stay within the Unix socket length limit.
	path := shortSocketPath(t, "stale")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	ln.Close()

	require.NoError(t, removeStaleSocket(path),
		"expected stale socket to be removed")
	assert.NoFileExists(t, path, "stale socket should be removed")
}

func TestRemoveStaleSocket_LiveSocketRefused(t *testing.T) {
	path := shortSocketPath(t, "live")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer ln.Close()

	err = removeStaleSocket(path)
	require.Error(t, err, "expected error for live socket")
	assert.FileExists(t, path, "live socket should not be deleted")
}

func TestStartControlListener_CreatesParentDir(t *testing.T) {
	base := shortSocketPath(t, "dir")
	// Remove the file shortSocketPath created, use it as a subdir.
	os.Remove(base)
	socketPath := filepath.Join(base, "sub", "t.sock")

	cleanup, err := startControlListener(
		socketPath, newTestProgram(t),
	)
	require.NoError(t, err, "expected listener to create dirs")
	cleanup()
}

// shortSocketPath returns a temporary socket path short enough for
// the Unix socket 104-byte name limit on macOS.
func shortSocketPath(t *testing.T, prefix string) string {
	t.Helper()
	f, err := os.CreateTemp("", prefix)
	require.NoError(t, err)
	path := f.Name()
	f.Close()
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// --- Runtime metadata tests ---

func TestTUIRuntimeWriteAndRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	tmpDir := setupTuiTestEnv(t)

	info := TUIRuntimeInfo{
		PID:          12345,
		SocketPath:   filepath.Join(tmpDir, "tui.12345.sock"),
		ServerAddr:   "http://127.0.0.1:7373",
		RepoFilter:   []string{"/repo"},
		BranchFilter: "main",
	}

	require.NoError(WriteTUIRuntime(info))

	runtimes, err := ListAllTUIRuntimes()
	require.NoError(err)
	require.Len(runtimes, 1)
	assert.Equal(12345, runtimes[0].PID)
	assert.Equal(info.SocketPath, runtimes[0].SocketPath)
}

func TestCleanupStaleTUIRuntimes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	setupTuiTestEnv(t)

	// Create a real stale socket (listener closed, PID dead).
	sockPath := shortSocketPath(t, "stale")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(err)
	ln.Close() // leave stale socket file behind

	info := TUIRuntimeInfo{
		PID:        999999999,
		SocketPath: sockPath,
		ServerAddr: "http://127.0.0.1:7373",
	}
	require.NoError(WriteTUIRuntime(info))

	cleaned := CleanupStaleTUIRuntimes()
	assert.Equal(1, cleaned)

	runtimes, _ := ListAllTUIRuntimes()
	assert.Empty(runtimes)
	assert.NoFileExists(sockPath,
		"stale socket file should be removed")
}

func TestCleanupStaleTUIRuntimes_NonSocketPreserved(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	tmpDir := setupTuiTestEnv(t)

	// Write metadata pointing to a regular file (not a socket).
	// Cleanup should remove the metadata but not the file.
	filePath := filepath.Join(tmpDir, "tui.999999999.sock")
	require.NoError(os.WriteFile(filePath, []byte{}, 0600))

	info := TUIRuntimeInfo{
		PID:        999999999,
		SocketPath: filePath,
		ServerAddr: "http://127.0.0.1:7373",
	}
	require.NoError(WriteTUIRuntime(info))

	cleaned := CleanupStaleTUIRuntimes()
	assert.Equal(1, cleaned)

	// Metadata should be gone.
	runtimes, _ := ListAllTUIRuntimes()
	assert.Empty(runtimes)
	// The regular file must NOT have been deleted.
	assert.FileExists(filePath,
		"regular file should not be removed")
}

func TestRuntimeMetadataReflectsModelFilters(t *testing.T) {
	setupTuiTestEnv(t)

	tests := []struct {
		name       string
		opts       []option
		wantRepo   []string
		wantBranch string
	}{
		{
			name:       "cli repo flag",
			opts:       []option{withRepoFilter("/cli/repo")},
			wantRepo:   []string{"/cli/repo"},
			wantBranch: "",
		},
		{
			name: "auto-filter repo",
			opts: []option{
				withAutoFilterRepo("/auto/repo"),
			},
			wantRepo:   []string{"/auto/repo"},
			wantBranch: "",
		},
		{
			name: "auto-filter branch",
			opts: []option{
				withAutoFilterBranch("feat/auto"),
			},
			wantRepo:   nil,
			wantBranch: "feat/auto",
		},
		{
			name: "cli overrides auto-filter",
			opts: []option{
				withAutoFilterBranch("feat/auto"),
				withBranchFilter("feat/cli"),
			},
			wantRepo:   nil,
			wantBranch: "feat/cli",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			opts := append(
				[]option{withExternalIODisabled()},
				tt.opts...,
			)
			m := newModel(testServerAddr, opts...)

			// Use buildTUIRuntimeInfo — the same helper that
			// Run() calls — so this test breaks if Run() stops
			// using it or if the helper regresses.
			info := buildTUIRuntimeInfo(
				m, "/tmp/test.sock", testServerAddr,
			)

			require.NoError(WriteTUIRuntime(info))

			runtimes, err := ListAllTUIRuntimes()
			require.NoError(err)
			require.Len(runtimes, 1)

			rt := runtimes[0]
			assert.Equal(tt.wantRepo, rt.RepoFilter)
			assert.Equal(tt.wantBranch, rt.BranchFilter)

			// Clean up for next subtest
			RemoveTUIRuntime(info.SocketPath)
		})
	}
}

// --- Helper ---

func sendControlCommand(
	t *testing.T, socketPath, command string,
) controlResponse {
	t.Helper()

	conn, err := net.DialTimeout(
		"unix", socketPath, 2*time.Second,
	)
	require.NoError(t, err, "dial socket")
	defer conn.Close()

	require.NoError(t,
		conn.SetDeadline(time.Now().Add(5*time.Second)),
		"set deadline")

	_, err = fmt.Fprintf(conn, "%s\n", command)
	require.NoError(t, err, "write command")

	buf := make([]byte, 64*1024)
	n, err := conn.Read(buf)
	require.NoError(t, err, "read response")

	var resp controlResponse
	require.NoError(t,
		json.Unmarshal(buf[:n], &resp),
		"unmarshal response %q", string(buf[:n]))
	return resp
}
