package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/streamfmt"
)

// collectMsgs recursively extracts all tea.Msg values from a Cmd,
// expanding BatchMsg into individual messages. Note: this executes
// each Cmd function, so only use with side-effect-free commands
// (e.g., mouse toggles, clear screen) or in tests with
// withExternalIODisabled().
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var all []tea.Msg
		for _, c := range batch {
			all = append(all, collectMsgs(c)...)
		}
		return all
	}
	return []tea.Msg{msg}
}

// hasMsgType returns true if any message in the slice has the given type name.
func hasMsgType(msgs []tea.Msg, typeName string) bool {
	for _, msg := range msgs {
		if fmt.Sprintf("%T", msg) == typeName {
			return true
		}
	}
	return false
}

func TestTUILogVisibleLinesWithCommandHeader(t *testing.T) {
	// logVisibleLines() should account for the command-line header
	// when the job has a known agent with a command line.
	m := newModel("http://localhost", withExternalIODisabled())
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
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewLog
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
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewLog
	m.logJobID = 1
	m.height = 20
	m.logScroll = 0
	m.logFollow = false

	// No agent -> no command header.
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

func TestTUILogLoadingGuard(t *testing.T) {
	// When logLoading is true, logTickMsg should not start
	// another fetch.
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewLog
	m.logJobID = 1
	m.logStreaming = true
	m.logLoading = true
	m.height = 30

	m2, cmd := updateModel(t, m, logTickMsg{})

	// Should not issue a fetch command.
	if cmd != nil {
		t.Errorf("expected nil cmd when logLoading, got %T", cmd)
	}
	if !m2.logLoading {
		t.Error("logLoading should remain true")
	}
}

func TestTUILogErrorDroppedOutsideLogView(t *testing.T) {
	// A late-arriving error from an in-flight log fetch should
	// not set m.err when the user has navigated away.
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewQueue // user navigated back
	m.logFetchSeq = 3
	m.logLoading = true

	msg := logOutputMsg{
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
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewLog
	m.logJobID = 42
	m.logFromView = viewTasks
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
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewLog
	m.logJobID = 42
	m.logFromView = viewTasks
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
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewLog
	m.logJobID = 42
	m.logFromView = viewTasks
	m.height = 30

	// Fix job with agent "test" produces a non-empty command line.
	m.fixJobs = []storage.ReviewJob{
		{ID: 42, Status: storage.JobStatusRunning, Agent: "test"},
	}
	// m.jobs is empty -- job only exists in fixJobs.
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
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewLog
	m.logJobID = 20
	m.logFromView = viewTasks
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

	// Right arrow -> next fix job (ID 30)
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

	// Left arrow from index 1 -> prev fix job (ID 10)
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

func TestTUILogOutputTable(t *testing.T) {
	dummyFmtr := &streamfmt.Formatter{}

	tests := []struct {
		name             string
		initialView      viewKind
		initialJobStatus storage.JobStatus
		initialJobError  string
		initialLogLines  []logLine
		initialStreaming bool
		initialFetchSeq  uint64
		initialLoading   bool
		initialOffset    int64
		initialFmtr      *streamfmt.Formatter

		msg logOutputMsg

		wantView       viewKind
		wantLinesLen   int
		wantLines      []string
		wantLinesNil   bool
		wantLinesEmpty bool
		wantStreaming  bool
		wantFlashMsg   string
		wantLoading    bool
		wantOffset     int64
		wantFmtr       *streamfmt.Formatter
	}{
		{
			name:             "updates formatter from message",
			initialView:      viewLog,
			initialStreaming: true,
			msg:              logOutputMsg{fmtr: dummyFmtr, hasMore: true, append: true},
			wantView:         viewLog,
			wantFmtr:         dummyFmtr,
			wantStreaming:    true,
			wantLinesNil:     true,
		},
		{
			name:             "persists formatter when message has none",
			initialView:      viewLog,
			initialStreaming: true,
			initialFmtr:      dummyFmtr,
			msg:              logOutputMsg{hasMore: true, append: true},
			wantView:         viewLog,
			wantFmtr:         dummyFmtr,
			wantStreaming:    true,
			wantLinesNil:     true,
		},
		{
			name:             "preserves lines on empty response",
			initialView:      viewLog,
			initialStreaming: true,
			initialFetchSeq:  1,
			initialLogLines:  []logLine{{text: "Line 1"}, {text: "Line 2"}, {text: "Line 3"}},
			msg:              logOutputMsg{lines: []logLine{}, hasMore: false, err: nil, append: true, seq: 1},
			wantView:         viewLog,
			wantLinesLen:     3,
			wantLines:        []string{"Line 1", "Line 2", "Line 3"},
			wantStreaming:    false,
		},
		{
			name:             "updates lines when streaming",
			initialView:      viewLog,
			initialStreaming: true,
			initialLogLines:  []logLine{{text: "Old line"}},
			msg:              logOutputMsg{lines: []logLine{{text: "Old line"}, {text: "New line"}}, hasMore: true, err: nil},
			wantView:         viewLog,
			wantLinesLen:     2,
			wantLines:        []string{"Old line", "New line"},
			wantStreaming:    true,
		},
		{
			name:             "err no log shows job error",
			initialView:      viewLog,
			initialJobStatus: storage.JobStatusFailed,
			initialJobError:  "agent timeout after 300s",
			initialStreaming: false,
			msg:              logOutputMsg{err: errNoLog},
			wantView:         viewQueue,
			wantFlashMsg:     "agent timeout",
			wantLinesNil:     true,
		},
		{
			name:             "err no log generic for non failed",
			initialView:      viewLog,
			initialJobStatus: storage.JobStatusDone,
			msg:              logOutputMsg{err: errNoLog},
			wantView:         viewQueue,
			wantFlashMsg:     "No log available for this job",
			wantLinesNil:     true,
		},
		{
			name:             "running job keeps waiting",
			initialView:      viewLog,
			initialStreaming: true,
			initialLogLines:  nil,
			msg:              logOutputMsg{lines: nil, hasMore: true},
			wantView:         viewLog,
			wantLinesLen:     0,
			wantLinesNil:     true,
			wantStreaming:    true,
		},
		{
			name:            "ignored when not in log view",
			initialView:     viewQueue,
			initialLogLines: []logLine{{text: "Previous session line"}},
			msg:             logOutputMsg{lines: []logLine{{text: "Should be ignored"}}, hasMore: false, err: nil},
			wantView:        viewQueue,
			wantLinesLen:    1,
			wantLines:       []string{"Previous session line"},
		},
		{
			name:             "append mode",
			initialView:      viewLog,
			initialStreaming: true,
			initialLogLines:  []logLine{{text: "Line 1"}, {text: "Line 2"}},
			initialOffset:    100,
			msg:              logOutputMsg{lines: []logLine{{text: "Line 3"}, {text: "Line 4"}}, hasMore: true, newOffset: 200, append: true},
			wantView:         viewLog,
			wantLinesLen:     4,
			wantLines:        []string{"Line 1", "Line 2", "Line 3", "Line 4"},
			wantStreaming:    true,
			wantOffset:       200,
		},
		{
			name:             "append no new lines",
			initialView:      viewLog,
			initialStreaming: true,
			initialLogLines:  []logLine{{text: "Existing"}},
			initialOffset:    100,
			msg:              logOutputMsg{hasMore: true, newOffset: 100, append: true},
			wantView:         viewLog,
			wantLinesLen:     1,
			wantLines:        []string{"Existing"},
			wantStreaming:    true,
			wantOffset:       100,
		},
		{
			name:             "replace mode",
			initialView:      viewLog,
			initialStreaming: false,
			initialLogLines:  []logLine{{text: "Old line 1"}, {text: "Old line 2"}},
			msg:              logOutputMsg{lines: []logLine{{text: "New line 1"}}, hasMore: false, newOffset: 50, append: false},
			wantView:         viewLog,
			wantLinesLen:     1,
			wantLines:        []string{"New line 1"},
			wantStreaming:    false,
			wantOffset:       50,
		},
		{
			name:             "stale seq dropped",
			initialView:      viewLog,
			initialStreaming: true,
			initialFetchSeq:  5,
			initialLoading:   true,
			initialLogLines:  []logLine{{text: "Current"}},
			msg:              logOutputMsg{lines: []logLine{{text: "Stale data"}}, hasMore: true, newOffset: 999, append: false, seq: 3},
			wantView:         viewLog,
			wantLinesLen:     1,
			wantLines:        []string{"Current"},
			wantStreaming:    true,
			wantLoading:      true,
			wantOffset:       0,
		},
		{
			name:             "offset reset",
			initialView:      viewLog,
			initialStreaming: true,
			initialFetchSeq:  1,
			initialOffset:    500,
			initialLogLines:  []logLine{{text: "Old line 1"}, {text: "Old line 2"}},
			msg:              logOutputMsg{lines: []logLine{{text: "Reset line 1"}}, hasMore: true, newOffset: 100, append: false, seq: 1},
			wantView:         viewLog,
			wantLinesLen:     1,
			wantLines:        []string{"Reset line 1"},
			wantStreaming:    true,
			wantOffset:       100,
		},
		{
			name:             "replace mode empty clears stale",
			initialView:      viewLog,
			initialStreaming: true,
			initialFetchSeq:  1,
			initialLogLines:  []logLine{{text: "Stale line 1"}, {text: "Stale line 2"}},
			msg:              logOutputMsg{lines: nil, hasMore: false, newOffset: 0, append: false, seq: 1},
			wantView:         viewLog,
			wantLinesLen:     0,
			wantLinesEmpty:   true,
			wantStreaming:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel("http://localhost", withExternalIODisabled())
			m.currentView = tt.initialView
			m.logJobID = 42
			m.logFromView = viewQueue
			m.logStreaming = tt.initialStreaming
			m.logFetchSeq = tt.initialFetchSeq
			m.logLoading = tt.initialLoading
			m.logOffset = tt.initialOffset
			m.logFmtr = tt.initialFmtr
			m.height = 30

			if tt.initialJobStatus != "" || tt.initialJobError != "" {
				m.jobs = []storage.ReviewJob{
					makeJob(42, withStatus(tt.initialJobStatus), withError(tt.initialJobError)),
				}
			}

			if tt.initialLogLines != nil {
				m.logLines = tt.initialLogLines
			}

			m2, _ := updateModel(t, m, tt.msg)

			if m2.currentView != tt.wantView {
				t.Errorf("Expected view %d, got %d", tt.wantView, m2.currentView)
			}
			if tt.wantLinesNil {
				if m2.logLines != nil {
					t.Errorf("Expected logLines to be nil, got %#v", m2.logLines)
				}
			} else if tt.wantLinesEmpty {
				if m2.logLines == nil || len(m2.logLines) != 0 {
					t.Errorf("Expected logLines to be an empty slice, got %#v", m2.logLines)
				}
			} else if m2.logLines == nil {
				t.Errorf("Expected logLines to not be nil")
			}
			if len(m2.logLines) != tt.wantLinesLen {
				t.Errorf("Expected %d lines, got %d", tt.wantLinesLen, len(m2.logLines))
			}
			for i, line := range tt.wantLines {
				if i < len(m2.logLines) && m2.logLines[i].text != line {
					t.Errorf("Line %d expected %q, got %q", i, line, m2.logLines[i].text)
				}
			}
			if m2.logStreaming != tt.wantStreaming {
				t.Errorf("Expected streaming %v, got %v", tt.wantStreaming, m2.logStreaming)
			}
			if tt.wantFlashMsg != "" && !strings.Contains(m2.flashMessage, tt.wantFlashMsg) {
				t.Errorf("Expected flash message to contain %q, got %q", tt.wantFlashMsg, m2.flashMessage)
			}
			if m2.logLoading != tt.wantLoading {
				t.Errorf("Expected logLoading %v, got %v", tt.wantLoading, m2.logLoading)
			}
			if m2.logOffset != tt.wantOffset {
				t.Errorf("Expected logOffset %d, got %d", tt.wantOffset, m2.logOffset)
			}
			if tt.wantFmtr != nil && m2.logFmtr != tt.wantFmtr {
				t.Errorf("Expected logFmtr to match pointer %p, got %p", tt.wantFmtr, m2.logFmtr)
			}
		})
	}
}

func TestMouseDisabledInContentViews(t *testing.T) {
	// Transitioning from an interactive view (queue) to a content view
	// (log, review, patch, prompt, commitMsg) should emit DisableMouse.
	// Transitioning back should emit EnableMouseCellMotion.
	contentViews := []struct {
		name     string
		view     viewKind
		exitTo   viewKind       // expected view after exit (0 = viewQueue)
		enterKey rune           // key to enter the view from queue (0 = use special key)
		exitKey  rune           // key to exit back (0 = use esc)
		setup    func(m *model) // additional setup needed
	}{
		{
			name:     "log view",
			view:     viewLog,
			enterKey: 'l',
			setup: func(m *model) {
				m.jobs = []storage.ReviewJob{
					makeJob(1, withStatus(storage.JobStatusRunning)),
				}
				m.selectedIdx = 0
			},
		},
		{
			name:     "review view via enter",
			view:     viewReview,
			enterKey: 0, // uses enter key
			setup: func(m *model) {
				m.jobs = []storage.ReviewJob{
					makeJob(1, withStatus(storage.JobStatusDone)),
				}
				m.selectedIdx = 0
				// Pre-populate review to avoid HTTP fetch
				m.currentReview = &storage.Review{
					ID:     1,
					JobID:  1,
					Output: "test review",
					Job:    &m.jobs[0],
				}
			},
		},
		{
			name:   "patch view",
			view:   viewPatch,
			exitTo: viewTasks,
			setup: func(m *model) {
				m.patchText = "diff --git a/f.go b/f.go"
				m.patchJobID = 1
			},
		},
		{
			name: "commit message view",
			view: viewCommitMsg,
			setup: func(m *model) {
				m.commitMsgContent = "feat: test"
				m.commitMsgJobID = 1
				m.commitMsgFromView = viewQueue
			},
		},
	}

	for _, tc := range contentViews {
		// Enter subtest: only run for views that can be entered
		// synchronously via keypress. Review, patch, and commitMsg
		// require async HTTP fetches that complete later.
		if tc.enterKey != 0 {
			t.Run(tc.name+" enter disables mouse", func(t *testing.T) {
				m := newModel("http://localhost", withExternalIODisabled())
				m.currentView = viewQueue
				m.height = 30
				m.width = 80
				if tc.setup != nil {
					tc.setup(&m)
				}

				var updated model
				var cmd tea.Cmd
				if tc.enterKey != 0 {
					updated, cmd = pressKey(m, tc.enterKey)
				} else {
					updated, cmd = pressSpecial(m, tea.KeyEnter)
				}

				if updated.currentView == viewQueue {
					t.Fatalf("view did not change from queue (setup may be incomplete)")
				}

				msgs := collectMsgs(cmd)
				if !hasMsgType(msgs, "tea.disableMouseMsg") {
					types := make([]string, len(msgs))
					for i, msg := range msgs {
						types[i] = fmt.Sprintf("%T", msg)
					}
					t.Errorf("expected disableMouseMsg in cmd, got types: %v", types)
				}
			})
		}

		t.Run(tc.name+" exit enables mouse", func(t *testing.T) {
			m := newModel("http://localhost", withExternalIODisabled())
			m.currentView = tc.view
			m.height = 30
			m.width = 80
			if tc.setup != nil {
				tc.setup(&m)
			}
			// Set up state needed for specific views
			if tc.view == viewLog {
				m.logJobID = 1
				m.logFromView = viewQueue
			}
			if tc.view == viewReview {
				m.reviewFromView = viewQueue
			}

			var updated model
			var cmd tea.Cmd
			if tc.exitKey != 0 {
				updated, cmd = pressKey(m, tc.exitKey)
			} else {
				updated, cmd = pressSpecial(m, tea.KeyEscape)
			}

			expectedView := tc.exitTo
			if expectedView == 0 {
				expectedView = viewQueue
			}
			if updated.currentView != expectedView {
				t.Fatalf("expected view %d after exit, got %d", expectedView, updated.currentView)
			}

			msgs := collectMsgs(cmd)
			if !hasMsgType(msgs, "tea.enableMouseCellMotionMsg") {
				types := make([]string, len(msgs))
				for i, msg := range msgs {
					types[i] = fmt.Sprintf("%T", msg)
				}
				t.Errorf("expected enableMouseCellMotionMsg in cmd, got types: %v", types)
			}
		})
	}
}

func TestMouseNotToggledWithinContentViews(t *testing.T) {
	// Transitioning between content views (e.g., review -> prompt)
	// should not emit mouse toggle commands since both views have
	// mouse disabled.
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewReview
	m.height = 30
	m.width = 80
	m.jobs = []storage.ReviewJob{
		makeJob(1, withStatus(storage.JobStatusDone)),
	}
	m.selectedIdx = 0
	m.currentReview = &storage.Review{
		ID:     1,
		JobID:  1,
		Output: "test",
		Prompt: "test prompt",
		Job:    &m.jobs[0],
	}
	m.reviewFromView = viewQueue

	// Press 'p' to go from review -> prompt
	updated, cmd := pressKey(m, 'p')
	if updated.currentView != viewKindPrompt {
		t.Skipf("did not switch to prompt view, got %d", updated.currentView)
	}

	msgs := collectMsgs(cmd)
	hasDisable := hasMsgType(msgs, "tea.disableMouseMsg")
	hasEnable := hasMsgType(msgs, "tea.enableMouseCellMotionMsg")
	if hasDisable || hasEnable {
		t.Errorf("should not toggle mouse between content views, got disable=%v enable=%v", hasDisable, hasEnable)
	}
}
