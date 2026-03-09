package tui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
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
		if got := tt.v.String(); got != tt.want {
			t.Errorf("viewKind(%d).String() = %q, want %q",
				tt.v, got, tt.want)
		}
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
		if got := parseViewKind(tt.s); got != tt.want {
			t.Errorf("parseViewKind(%q) = %d, want %d",
				tt.s, got, tt.want)
		}
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
		if isQ != tt.wantQuery || isM != tt.wantMutate {
			t.Errorf("isControlCommand(%q) = (%v, %v), want (%v, %v)",
				tt.cmd, isQ, isM, tt.wantQuery, tt.wantMutate)
		}
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
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}

	data, ok := resp.Data.(stateSnapshot)
	if !ok {
		t.Fatalf("expected stateSnapshot, got %T", resp.Data)
	}
	if data.View != "queue" {
		t.Errorf("view = %q, want %q", data.View, "queue")
	}
	if data.JobCount != 2 {
		t.Errorf("job count = %d, want 2", data.JobCount)
	}
	if !data.HideClosed {
		t.Error("hide_closed should be true")
	}
}

func TestBuildFilterResponse(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.activeRepoFilter = []string{"/repo"}
	m.activeBranchFilter = "main"
	m.lockedRepoFilter = true
	m.filterStack = []string{"repo", "branch"}

	resp := m.buildFilterResponse()
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
}

func TestBuildJobsResponse(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(1, withAgent("claude-code"), withRepoPath("/r")),
		makeJob(2, withAgent("codex"), withRepoPath("/r")),
	}

	resp := m.buildJobsResponse()
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}

	jobs, ok := resp.Data.([]jobSnapshot)
	if !ok {
		t.Fatalf("expected []jobSnapshot, got %T", resp.Data)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}
	if jobs[0].Agent != "claude-code" {
		t.Errorf("job[0].Agent = %q, want %q",
			jobs[0].Agent, "claude-code")
	}
}

func TestBuildSelectedResponse_NoSelection(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.selectedIdx = -1

	resp := m.buildSelectedResponse()
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	data := resp.Data.(selectedSnapshot)
	if data.Job != nil {
		t.Error("expected nil job when nothing selected")
	}
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
	if data.Job == nil {
		t.Fatal("expected non-nil job")
	}
	if data.Job.ID != 42 {
		t.Errorf("job ID = %d, want 42", data.Job.ID)
	}
	if !data.HasReview {
		t.Error(
			"expected has_review=true for done job with closed field",
		)
	}
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
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if len(updated.activeRepoFilter) != 1 ||
		updated.activeRepoFilter[0] != repo {
		t.Errorf("repo filter = %v, want [%s]",
			updated.activeRepoFilter, repo)
	}
	if updated.activeBranchFilter != branch {
		t.Errorf("branch filter = %q, want %q",
			updated.activeBranchFilter, branch)
	}
	if updated.selectedIdx != -1 {
		t.Errorf("selectedIdx = %d, want -1", updated.selectedIdx)
	}
}

func TestHandleCtrlSetFilter_LockedRepo(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.lockedRepoFilter = true

	params, _ := json.Marshal(map[string]string{"repo": "/test/repo"})
	_, resp, _ := m.handleCtrlSetFilter(params)
	if resp.OK {
		t.Fatal("expected error for locked repo filter")
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleCtrlSetFilter_LockedBranch(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.lockedBranchFilter = true

	params, _ := json.Marshal(map[string]string{"branch": "main"})
	_, resp, _ := m.handleCtrlSetFilter(params)
	if resp.OK {
		t.Fatal("expected error for locked branch filter")
	}
}

func TestHandleCtrlSetFilter_ClearRepo(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.activeRepoFilter = []string{"/old"}
	m.filterStack = []string{"repo"}

	empty := ""
	params, _ := json.Marshal(map[string]*string{"repo": &empty})
	updated, resp, _ := m.handleCtrlSetFilter(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if updated.activeRepoFilter != nil {
		t.Errorf("expected nil repo filter, got %v",
			updated.activeRepoFilter)
	}
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
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if updated.activeRepoFilter != nil {
		t.Errorf("repo filter should be nil, got %v",
			updated.activeRepoFilter)
	}
	if updated.activeBranchFilter != "" {
		t.Errorf("branch filter should be empty, got %q",
			updated.activeBranchFilter)
	}
}

func TestHandleCtrlSetHideClosed(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.hideClosed = false

	params, _ := json.Marshal(map[string]bool{"hide_closed": true})
	updated, resp, _ := m.handleCtrlSetHideClosed(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if !updated.hideClosed {
		t.Error("hideClosed should be true")
	}
}

func TestHandleCtrlSelectJob(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(10), makeJob(20), makeJob(30),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 20})
	updated, resp, _ := m.handleCtrlSelectJob(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if updated.selectedIdx != 1 {
		t.Errorf("selectedIdx = %d, want 1", updated.selectedIdx)
	}
	if updated.selectedJobID != 20 {
		t.Errorf("selectedJobID = %d, want 20",
			updated.selectedJobID)
	}
}

func TestHandleCtrlSelectJob_NotFound(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{makeJob(1)}

	params, _ := json.Marshal(map[string]int64{"job_id": 999})
	_, resp, _ := m.handleCtrlSelectJob(params)
	if resp.OK {
		t.Fatal("expected error for missing job")
	}
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
	if resp.OK {
		t.Fatal("expected error for hidden job")
	}
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
	if resp.OK {
		t.Fatal("expected error for closed-hidden job")
	}
}

func TestHandleCtrlSetView(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.currentView = viewQueue

	params, _ := json.Marshal(map[string]string{"view": "queue"})
	updated, resp, _ := m.handleCtrlSetView(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if updated.currentView != viewQueue {
		t.Errorf("view = %d, want %d",
			updated.currentView, viewQueue)
	}
}

func TestHandleCtrlSetView_Invalid(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())

	params, _ := json.Marshal(map[string]string{"view": "review"})
	_, resp, _ := m.handleCtrlSetView(params)
	if resp.OK {
		t.Fatal("expected error for unsettable view")
	}
}

func TestHandleCtrlSetView_TasksDisabled(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.tasksEnabled = false

	params, _ := json.Marshal(map[string]string{"view": "tasks"})
	_, resp, _ := m.handleCtrlSetView(params)
	if resp.OK {
		t.Fatal("expected error when tasks workflow disabled")
	}
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
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for close operation")
	}
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
	// Select job 1 (index 0)
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Close job 2, which is not the selected job
	params, _ := json.Marshal(map[string]any{
		"job_id": int64(2),
		"closed": true,
	})
	updated, resp, _ := m.handleCtrlCloseReview(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	// Selection should remain on job 1
	if updated.selectedJobID != 1 {
		t.Errorf("selectedJobID = %d, want 1 (unchanged)",
			updated.selectedJobID)
	}
	if updated.selectedIdx != 0 {
		t.Errorf("selectedIdx = %d, want 0 (unchanged)",
			updated.selectedIdx)
	}
}

func TestHandleCtrlCloseReview_NoReview(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(5, withStatus(storage.JobStatusRunning)),
	}

	params, _ := json.Marshal(map[string]any{"job_id": int64(5)})
	_, resp, _ := m.handleCtrlCloseReview(params)
	if resp.OK {
		t.Fatal("expected error for job without review")
	}
}

func TestHandleCtrlCancelJob(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(7, withStatus(storage.JobStatusRunning)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 7})
	updated, resp, cmd := m.handleCtrlCancelJob(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for cancel")
	}
	if updated.jobs[0].Status != storage.JobStatusCanceled {
		t.Errorf("status = %s, want canceled",
			updated.jobs[0].Status)
	}
}

func TestHandleCtrlCancelJob_NonSelectedNoReflow(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.hideClosed = true
	m.jobs = []storage.ReviewJob{
		makeJob(1, withClosed(boolPtr(false))),
		makeJob(2, withStatus(storage.JobStatusRunning)),
		makeJob(3, withClosed(boolPtr(false))),
	}
	// Select job 1 (index 0)
	m.selectedIdx = 0
	m.selectedJobID = 1

	// Cancel job 2, which is not the selected job
	params, _ := json.Marshal(map[string]int64{"job_id": 2})
	updated, resp, _ := m.handleCtrlCancelJob(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	// Selection should remain on job 1
	if updated.selectedJobID != 1 {
		t.Errorf("selectedJobID = %d, want 1 (unchanged)",
			updated.selectedJobID)
	}
	if updated.selectedIdx != 0 {
		t.Errorf("selectedIdx = %d, want 0 (unchanged)",
			updated.selectedIdx)
	}
}

func TestHandleCtrlCancelJob_WrongStatus(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(7, withStatus(storage.JobStatusDone)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 7})
	_, resp, _ := m.handleCtrlCancelJob(params)
	if resp.OK {
		t.Fatal("expected error for non-cancellable job")
	}
}

func TestHandleCtrlRerunJob(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(8, withStatus(storage.JobStatusFailed)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 8})
	updated, resp, cmd := m.handleCtrlRerunJob(params)
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for rerun")
	}
	if updated.jobs[0].Status != storage.JobStatusQueued {
		t.Errorf("status = %s, want queued",
			updated.jobs[0].Status)
	}
}

func TestHandleCtrlRerunJob_WrongStatus(t *testing.T) {
	m := newModel(testServerAddr, withExternalIODisabled())
	m.jobs = []storage.ReviewJob{
		makeJob(8, withStatus(storage.JobStatusRunning)),
	}

	params, _ := json.Marshal(map[string]int64{"job_id": 8})
	_, resp, _ := m.handleCtrlRerunJob(params)
	if resp.OK {
		t.Fatal("expected error for non-rerunnable job")
	}
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
	if _, ok := updated.(model); !ok {
		t.Fatalf("Update returned %T, want model", updated)
	}

	select {
	case resp := <-respCh:
		if !resp.OK {
			t.Errorf("expected OK, got error: %s", resp.Error)
		}
	default:
		t.Fatal("no response received on channel")
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
	if um.selectedJobID != 2 {
		t.Errorf("selectedJobID = %d, want 2", um.selectedJobID)
	}

	select {
	case resp := <-respCh:
		if !resp.OK {
			t.Errorf("expected OK, got error: %s", resp.Error)
		}
	default:
		t.Fatal("no response received on channel")
	}
}

// --- Integration test with real Unix socket ---

func TestControlSocketRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Use a real HTTP server that handles all TUI Init() requests
	ts := controlTestServer(t)

	m := newModel(ts.URL, withExternalIODisabled())

	// Provide a pipe for stdin so the program doesn't block on TTY.
	r, w, _ := os.Pipe()
	t.Cleanup(func() { w.Close(); r.Close() })
	p := tea.NewProgram(m,
		tea.WithoutRenderer(),
		tea.WithInput(r),
	)
	go func() { _, _ = p.Run() }()
	t.Cleanup(func() { p.Kill() })

	// Give the program time to complete Init() and reach its
	// steady-state event loop.
	time.Sleep(500 * time.Millisecond)

	cleanup, err := startControlListener(socketPath, p)
	if err != nil {
		t.Fatalf("startControlListener: %v", err)
	}
	t.Cleanup(cleanup)

	// Test get-state
	resp := sendControlCommand(t, socketPath,
		`{"command":"get-state"}`)
	if !resp.OK {
		t.Fatalf("get-state failed: %s", resp.Error)
	}

	// Test get-jobs
	resp = sendControlCommand(t, socketPath,
		`{"command":"get-jobs"}`)
	if !resp.OK {
		t.Fatalf("get-jobs failed: %s", resp.Error)
	}

	// Test set-hide-closed mutation
	resp = sendControlCommand(t, socketPath,
		`{"command":"set-hide-closed","params":{"hide_closed":true}}`)
	if !resp.OK {
		t.Fatalf("set-hide-closed failed: %s", resp.Error)
	}

	// Verify state via get-state
	resp = sendControlCommand(t, socketPath,
		`{"command":"get-state"}`)
	if !resp.OK {
		t.Fatalf("get-state after mutation: %s", resp.Error)
	}

	// Test unknown command
	resp = sendControlCommand(t, socketPath,
		`{"command":"nope"}`)
	if resp.OK {
		t.Error("expected error for unknown command")
	}
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
	if err != nil {
		t.Fatalf("startControlListener: %v", err)
	}
	t.Cleanup(cleanup)

	resp := sendControlCommand(t, socketPath, `{not json}`)
	if resp.OK {
		t.Error("expected error for invalid JSON")
	}
}

func TestControlSocketPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	cleanup, err := startControlListener(
		socketPath, newTestProgram(t),
	)
	if err != nil {
		t.Fatalf("startControlListener: %v", err)
	}
	t.Cleanup(cleanup)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Type()&os.ModeSocket == 0 {
		t.Errorf("expected socket type, got %s",
			info.Mode().Type())
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		t.Errorf("socket permissions %o allow group/other access",
			perm)
	}
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
	if err := removeStaleSocket(path); err != nil {
		t.Errorf("expected nil for nonexistent path, got: %v", err)
	}
}

func TestRemoveStaleSocket_RegularFileRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	err := removeStaleSocket(path)
	if err == nil {
		t.Fatal("expected error for regular file, got nil")
	}
	// File must still exist
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("regular file was deleted")
	}
}

func TestRemoveStaleSocket_StaleSocketRemoved(t *testing.T) {
	// Use a short path to stay within the Unix socket length limit.
	path := shortSocketPath(t, "stale")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	if err := removeStaleSocket(path); err != nil {
		t.Fatalf("expected stale socket to be removed: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("stale socket was not removed")
	}
}

func TestRemoveStaleSocket_LiveSocketRefused(t *testing.T) {
	path := shortSocketPath(t, "live")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	err = removeStaleSocket(path)
	if err == nil {
		t.Fatal("expected error for live socket, got nil")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("live socket was deleted")
	}
}

func TestRemoveStaleSocket_IncompatibleSocketRefused(t *testing.T) {
	path := shortSocketPath(t, "dgram")
	// Create a DGRAM socket — dial with STREAM will fail with a
	// non-ECONNREFUSED error, which should NOT be treated as stale.
	fd, err := syscall.Socket(
		syscall.AF_UNIX, syscall.SOCK_DGRAM, 0,
	)
	if err != nil {
		t.Fatalf("create dgram socket fd: %v", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(
		fd, &syscall.SockaddrUnix{Name: path},
	); err != nil {
		t.Fatalf("bind dgram socket: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	err = removeStaleSocket(path)
	if err == nil {
		t.Fatal("expected error for incompatible socket, got nil")
	}
	// The socket must not have been removed.
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("incompatible socket was deleted")
	}
}

func TestStartControlListener_CreatesParentDir(t *testing.T) {
	base := shortSocketPath(t, "dir")
	// Remove the file shortSocketPath created, use it as a subdir.
	os.Remove(base)
	socketPath := filepath.Join(base, "sub", "t.sock")

	cleanup, err := startControlListener(
		socketPath, newTestProgram(t),
	)
	if err != nil {
		t.Fatalf("expected listener to create dirs: %v", err)
	}
	cleanup()
}

// shortSocketPath returns a temporary socket path short enough for
// the Unix socket 104-byte name limit on macOS.
func shortSocketPath(t *testing.T, prefix string) string {
	t.Helper()
	f, err := os.CreateTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// --- Runtime metadata tests ---

func TestTUIRuntimeWriteAndRead(t *testing.T) {
	tmpDir := setupTuiTestEnv(t)

	info := TUIRuntimeInfo{
		PID:          12345,
		SocketPath:   filepath.Join(tmpDir, "tui.12345.sock"),
		ServerAddr:   "http://127.0.0.1:7373",
		RepoFilter:   []string{"/repo"},
		BranchFilter: "main",
	}

	if err := WriteTUIRuntime(info); err != nil {
		t.Fatalf("WriteTUIRuntime: %v", err)
	}

	runtimes, err := ListAllTUIRuntimes()
	if err != nil {
		t.Fatalf("ListAllTUIRuntimes: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("got %d runtimes, want 1", len(runtimes))
	}
	if runtimes[0].PID != 12345 {
		t.Errorf("PID = %d, want 12345", runtimes[0].PID)
	}
	if runtimes[0].SocketPath != info.SocketPath {
		t.Errorf("SocketPath = %q, want %q",
			runtimes[0].SocketPath, info.SocketPath)
	}
}

func TestCleanupStaleTUIRuntimes(t *testing.T) {
	tmpDir := setupTuiTestEnv(t)

	// Write a runtime with a PID that definitely doesn't exist
	info := TUIRuntimeInfo{
		PID:        999999999,
		SocketPath: filepath.Join(tmpDir, "tui.999999999.sock"),
		ServerAddr: "http://127.0.0.1:7373",
	}
	if err := WriteTUIRuntime(info); err != nil {
		t.Fatalf("WriteTUIRuntime: %v", err)
	}

	// Create a fake socket file
	if err := os.WriteFile(
		info.SocketPath, []byte{}, 0600,
	); err != nil {
		t.Fatalf("create fake socket: %v", err)
	}

	cleaned := CleanupStaleTUIRuntimes()
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1", cleaned)
	}

	runtimes, _ := ListAllTUIRuntimes()
	if len(runtimes) != 0 {
		t.Errorf("expected 0 runtimes after cleanup, got %d",
			len(runtimes))
	}
	if _, err := os.Stat(info.SocketPath); !os.IsNotExist(err) {
		t.Error("expected socket file to be removed")
	}
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

			if err := WriteTUIRuntime(info); err != nil {
				t.Fatalf("WriteTUIRuntime: %v", err)
			}

			runtimes, err := ListAllTUIRuntimes()
			if err != nil {
				t.Fatalf("ListAllTUIRuntimes: %v", err)
			}
			if len(runtimes) != 1 {
				t.Fatalf(
					"got %d runtimes, want 1", len(runtimes),
				)
			}

			rt := runtimes[0]
			if len(rt.RepoFilter) != len(tt.wantRepo) {
				t.Errorf("RepoFilter = %v, want %v",
					rt.RepoFilter, tt.wantRepo)
			} else {
				for i := range tt.wantRepo {
					if rt.RepoFilter[i] != tt.wantRepo[i] {
						t.Errorf(
							"RepoFilter[%d] = %q, want %q",
							i, rt.RepoFilter[i],
							tt.wantRepo[i],
						)
					}
				}
			}
			if rt.BranchFilter != tt.wantBranch {
				t.Errorf("BranchFilter = %q, want %q",
					rt.BranchFilter, tt.wantBranch)
			}

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
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(
		time.Now().Add(5 * time.Second),
	); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	_, err = fmt.Fprintf(conn, "%s\n", command)
	if err != nil {
		t.Fatalf("write command: %v", err)
	}

	buf := make([]byte, 64*1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var resp controlResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v",
			string(buf[:n]), err)
	}
	return resp
}
