# `roborev fix` Count-Bounded Batching and Session Resume — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `--batch-size N` (count-bounded batching) and `--resume`/`--no-resume` (session resume across invocations within a single fix run) to `roborev fix`.

**Architecture:** Layer count-cap onto the existing `runFixBatch` plumbing via `batchSplitOptions{MaxSize, MaxCount, MinSeverity}`. Move `sessionCaptureWriter` from `internal/daemon/` to `internal/agent/` and use it in both fix paths. Introduce a shared `fixSessionTracker` that returns `(agent.Agent, bool)` so logging is driven by an explicit "is resumed?" flag rather than agent identity.

**Tech Stack:** Go (stdlib + Cobra). `agent.SessionAgent`, `agent.ExtractSessionID`, `agent.IsValidResumeSessionID` already exist; both Codex and Claude Code already implement `WithSessionID`. Tests use `testify`, the existing `mockDaemonBuilder`, and the `test` agent.

**Reference design:** [`docs/superpowers/specs/2026-05-04-fix-batching-resume-design.md`](../specs/2026-05-04-fix-batching-resume-design.md). The plan follows the spec's "Rough implementation order" with each step expanded into bite-sized TDD tasks.

---

## File Structure

| Path | Status | Responsibility |
|---|---|---|
| `internal/agent/session_writer.go` | Create | Exported `SessionCaptureWriter` (moved from daemon). Transparent JSONL passthrough; captures the first `session_id`/`thread_id`/`type:session` event. |
| `internal/agent/session_writer_test.go` | Create | Unit tests moved from `internal/daemon/session_capture_test.go` and rewritten to use the exported API. |
| `internal/daemon/session_capture.go` | Delete | Replaced by the moved file in the agent package. |
| `internal/daemon/session_capture_test.go` | Delete | Replaced by the moved test file. |
| `internal/daemon/worker.go` | Modify | Switch `newSessionCaptureWriter(...)` at line 577 to `agent.NewSessionCaptureWriter(...)`. |
| `internal/agent/test_agent.go` | Modify | Add `SessionID`, `WithSessionID(id)`, per-instance counter, recording slice, session-event emission. |
| `cmd/roborev/fix.go` | Modify | Add `--batch-size`, `--resume`, `--no-resume` flags + mutual-exclusion errors; refactor `splitIntoBatches` to options struct; wire `fixSessionTracker` and capture writer into both paths. |
| `cmd/roborev/fix_session.go` | Create | `fixSessionTracker` type and helpers. Kept separate so the file is small and focused. |
| `cmd/roborev/fix_session_test.go` | Create | Unit tests for `fixSessionTracker`. |
| `cmd/roborev/fix_test.go` | Modify | Update existing `TestSplitIntoBatches` to options-struct call sites; add new test cases for `MaxCount`; add flag-validation tests. |
| `cmd/roborev/fix_integration_test.go` | Modify | New integration tests for `--batch-size N`, `--resume`, combined flags, cascade-on-error, non-session agent, `--quiet` suppression, and an existing `--batch` regression test. |

---

## Task 1: Move `sessionCaptureWriter` from `daemon` to `agent` package

**Why first:** This is a mechanical refactor with no behavior change. Lands cleanly on its own and unblocks Tasks 7–10.

**Files:**
- Create: `internal/agent/session_writer.go`
- Create: `internal/agent/session_writer_test.go`
- Delete: `internal/daemon/session_capture.go`
- Delete: `internal/daemon/session_capture_test.go`
- Modify: `internal/daemon/worker.go:577-581`

- [ ] **Step 1: Create `internal/agent/session_writer.go`**

```go
package agent

import (
	"bytes"
	"io"
	"sync"
)

// SessionCaptureWriter is a transparent passthrough writer that scans
// the byte stream for the first agent session ID JSON event
// (session_id / sessionId / sessionID / type:session / thread.started)
// and surfaces it via SessionID(). After capture, scanning stops and
// the writer continues to forward bytes unchanged.
//
// Use Flush() once writes are complete so any trailing partial line
// is also examined.
type SessionCaptureWriter struct {
	dst       io.Writer
	onCapture func(string)
	mu        sync.Mutex
	buf       bytes.Buffer
	sessionID string
}

// NewSessionCaptureWriter wraps dst and reports the first captured
// session ID via onCapture (if non-nil). Bytes pass through to dst
// unchanged.
func NewSessionCaptureWriter(dst io.Writer, onCapture func(string)) *SessionCaptureWriter {
	return &SessionCaptureWriter{dst: dst, onCapture: onCapture}
}

func (w *SessionCaptureWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.capture(p[:n])
	}
	return n, err
}

// Flush examines any buffered partial line. Call this before reading
// SessionID() to ensure capture is complete.
func (w *SessionCaptureWriter) Flush() {
	var captured string

	w.capture(nil)

	w.mu.Lock()
	if w.sessionID == "" && w.buf.Len() > 0 {
		if sessionID := ExtractSessionID(w.buf.String()); sessionID != "" {
			w.sessionID = sessionID
			captured = sessionID
		}
	}
	w.mu.Unlock()

	if captured != "" && w.onCapture != nil {
		w.onCapture(captured)
	}
}

// SessionID returns the first captured session ID, or "" if none.
func (w *SessionCaptureWriter) SessionID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionID
}

func (w *SessionCaptureWriter) capture(p []byte) {
	var captured string

	w.mu.Lock()
	if w.sessionID != "" {
		w.mu.Unlock()
		return
	}
	if len(p) > 0 {
		_, _ = w.buf.Write(p)
	}
	for w.sessionID == "" {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := string(data[:idx+1])
		w.buf.Next(idx + 1)
		if sessionID := ExtractSessionID(line); sessionID != "" {
			w.sessionID = sessionID
			w.buf.Reset()
			captured = sessionID
		}
	}
	w.mu.Unlock()

	if captured != "" && w.onCapture != nil {
		w.onCapture(captured)
	}
}
```

- [ ] **Step 2: Create `internal/agent/session_writer_test.go`**

```go
package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionCaptureWriter_StopsBufferingAfterCapture(t *testing.T) {
	var dst bytes.Buffer
	var captured []string

	w := NewSessionCaptureWriter(&dst, func(sessionID string) {
		captured = append(captured, sessionID)
	})

	first := `{"type":"system","subtype":"init","session_id":"session-123"}` + "\n"
	_, err := w.Write([]byte(first))
	require.NoError(t, err)
	require.Equal(t, "session-123", w.SessionID())
	require.Equal(t, []string{"session-123"}, captured)
	require.Equal(t, 0, w.buf.Len())

	trailing := strings.Repeat("later output\n", 128)
	_, err = w.Write([]byte(trailing))
	require.NoError(t, err)
	require.Len(t, captured, 1, "callback fires only once")
	require.Equal(t, 0, w.buf.Len(), "buffer is not retained after capture")
	require.Equal(t, first+trailing, dst.String(), "destination receives full stream")
}

func TestSessionCaptureWriter_FlushCapturesPartialLine(t *testing.T) {
	var dst bytes.Buffer
	var captured string

	w := NewSessionCaptureWriter(&dst, func(sessionID string) {
		captured = sessionID
	})

	line := `{"type":"thread.started","thread_id":"thread-456"}`
	_, err := w.Write([]byte(line))
	require.NoError(t, err)
	require.Empty(t, w.SessionID(), "no newline yet, no capture")

	w.Flush()

	require.Equal(t, "thread-456", w.SessionID())
	require.Equal(t, "thread-456", captured)
}

func TestSessionCaptureWriter_NoSessionEvent(t *testing.T) {
	var dst bytes.Buffer
	w := NewSessionCaptureWriter(&dst, nil)

	payload := "plain text output\nwith multiple lines\nbut no session\n"
	_, err := w.Write([]byte(payload))
	require.NoError(t, err)
	w.Flush()

	require.Empty(t, w.SessionID())
	require.Equal(t, payload, dst.String())
}
```

- [ ] **Step 3: Run new tests to verify they pass against the new file**

```bash
go test ./internal/agent/ -run SessionCaptureWriter -v
```

Expected: all three subtests PASS.

- [ ] **Step 4: Update `internal/daemon/worker.go:577-581` to use the agent-package writer**

Find the block:
```go
sessionWriter := newSessionCaptureWriter(agentOutput, func(sessionID string) {
    if err := wp.db.SaveJobSessionID(job.ID, workerID, sessionID); err != nil {
        log.Printf("[%s] Error saving session ID for job %d: %v", workerID, job.ID, err)
    }
})
agentOutput = sessionWriter
```

Replace with:
```go
sessionWriter := agent.NewSessionCaptureWriter(agentOutput, func(sessionID string) {
    if err := wp.db.SaveJobSessionID(job.ID, workerID, sessionID); err != nil {
        log.Printf("[%s] Error saving session ID for job %d: %v", workerID, job.ID, err)
    }
})
agentOutput = sessionWriter
```

The daemon already imports `github.com/roborev-dev/roborev/internal/agent`; no import change required.

- [ ] **Step 5: Delete the now-unused daemon files**

```bash
rm internal/daemon/session_capture.go internal/daemon/session_capture_test.go
```

- [ ] **Step 6: Build, vet, and run the full test suite**

```bash
go build ./...
go vet ./...
go test ./...
```

Expected: build OK, vet clean, all tests pass.

- [ ] **Step 7: Commit**

```bash
go fmt ./...
git add internal/agent/session_writer.go internal/agent/session_writer_test.go internal/daemon/session_capture.go internal/daemon/session_capture_test.go internal/daemon/worker.go
git commit -m "$(cat <<'EOF'
refactor: move sessionCaptureWriter to internal/agent

Hoist the JSONL session-capture writer out of the daemon package so
both the daemon worker and the foreground roborev fix CLI can use it
without a daemon dependency. Behavior is unchanged; the daemon worker
keeps the same callback signature.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add session capabilities to the `test` agent

**Why:** Lets later tests exercise resume without hitting Codex/Claude binaries.

**Files:**
- Modify: `internal/agent/test_agent.go:13-99`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/test_agent.go` (we will create a dedicated test file in Step 4 below; this step writes the test inline first to drive the implementation):

```go
// internal/agent/test_agent_session_test.go
package agent

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTestAgent_FreshCallsEmitDistinctSessionIDs(t *testing.T) {
	a := NewTestAgent()

	var buf1 bytes.Buffer
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt 1", &buf1)
	require.NoError(t, err)
	id1 := ExtractSessionID(buf1.String())
	assert.Equal(t, "test-session-1", id1)

	var buf2 bytes.Buffer
	_, err = a.Review(context.Background(), "/repo", "def", "prompt 2", &buf2)
	require.NoError(t, err)
	id2 := ExtractSessionID(buf2.String())
	assert.Equal(t, "test-session-2", id2)

	calls := a.Calls()
	require.Len(t, calls, 2)
	assert.Empty(t, calls[0].SessionID, "first call had no incoming session")
	assert.Empty(t, calls[1].SessionID, "second call had no incoming session")
}

func TestTestAgent_WithSessionIDEchoesReceivedID(t *testing.T) {
	a := NewTestAgent()
	resumed := a.WithSessionID("test-session-1").(*TestAgent)

	var buf bytes.Buffer
	_, err := resumed.Review(context.Background(), "/repo", "abc", "prompt", &buf)
	require.NoError(t, err)

	id := ExtractSessionID(buf.String())
	assert.Equal(t, "test-session-1", id, "resumed call echoes the incoming session ID")

	calls := resumed.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "test-session-1", calls[0].SessionID)
}

func TestTestAgent_NewInstanceResetsCounter(t *testing.T) {
	a1 := NewTestAgent()
	var buf bytes.Buffer
	_, _ = a1.Review(context.Background(), "/repo", "abc", "p", &buf)
	assert.Equal(t, "test-session-1", ExtractSessionID(buf.String()))

	a2 := NewTestAgent()
	buf.Reset()
	_, _ = a2.Review(context.Background(), "/repo", "abc", "p", &buf)
	assert.Equal(t, "test-session-1", ExtractSessionID(buf.String()),
		"new instance starts its own counter at 1")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/agent/ -run TestTestAgent -v
```

Expected: compilation error (`a.WithSessionID` undefined, `a.Calls()` undefined).

- [ ] **Step 3: Implement the changes in `internal/agent/test_agent.go`**

Replace the entire file with:

```go
package agent

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/roborev-dev/roborev/internal/git"
)

// TestAgentCall records a single Review() invocation for assertion in tests.
type TestAgentCall struct {
	SessionID string // SessionID set on the agent at the time of the call ("" = fresh)
	Prompt    string
}

// TestAgent is a mock agent for testing that returns predictable output.
//
// Each Review() call emits a synthetic session event as the first line
// of streamed output so callers wired through SessionCaptureWriter pick
// up a session ID. Fresh calls (SessionID == "") emit a counter-based
// new ID like "test-session-1". Resumed calls (SessionID != "") echo
// the incoming ID. Counters are per-instance.
type TestAgent struct {
	Delay     time.Duration  // Simulated processing delay
	Output    string         // Fixed output to return
	Fail      bool           // If true, returns an error
	Reasoning ReasoningLevel // Reasoning level (for testing)
	SessionID string         // Incoming session ID for resume; "" = fresh

	// shared across clones produced by With* methods so the counter and
	// recording are consistent regardless of which clone Review() is called on.
	state *testAgentState
}

type testAgentState struct {
	mu       sync.Mutex
	counter  int
	calls    []TestAgentCall
}

// NewTestAgent creates a new test agent with its own per-instance counter.
func NewTestAgent() *TestAgent {
	return &TestAgent{
		Delay:     100 * time.Millisecond,
		Output:    "Test review output: This commit looks good. No issues found.",
		Reasoning: ReasoningStandard,
		state:     &testAgentState{},
	}
}

func (a *TestAgent) clone() *TestAgent {
	st := a.state
	if st == nil {
		st = &testAgentState{}
	}
	return &TestAgent{
		Delay:     a.Delay,
		Output:    a.Output,
		Fail:      a.Fail,
		Reasoning: a.Reasoning,
		SessionID: a.SessionID,
		state:     st,
	}
}

// WithReasoning returns a copy of the agent with the specified reasoning level
func (a *TestAgent) WithReasoning(level ReasoningLevel) Agent {
	c := a.clone()
	c.Reasoning = level
	return c
}

// WithAgentic returns the agent unchanged (agentic mode not applicable for test agent)
func (a *TestAgent) WithAgentic(agentic bool) Agent {
	return a
}

// WithModel returns the agent unchanged (model selection not supported for test agent).
func (a *TestAgent) WithModel(model string) Agent {
	return a
}

// WithSessionID returns a copy configured to resume the given session.
// Implements SessionAgent.
func (a *TestAgent) WithSessionID(sessionID string) Agent {
	c := a.clone()
	c.SessionID = sessionID
	return c
}

func (a *TestAgent) CommandLine() string {
	return "test"
}

func (a *TestAgent) Name() string {
	return "test"
}

// Calls returns a copy of every Review invocation recorded by this
// agent (and any clones that share its state).
func (a *TestAgent) Calls() []TestAgentCall {
	if a.state == nil {
		return nil
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	out := make([]TestAgentCall, len(a.state.calls))
	copy(out, a.state.calls)
	return out
}

func (a *TestAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Respect context cancellation
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(a.Delay):
	}

	if a.state == nil {
		a.state = &testAgentState{}
	}

	// Determine the session ID to advertise: echo incoming, or mint fresh.
	var sessionID string
	a.state.mu.Lock()
	if a.SessionID != "" {
		sessionID = a.SessionID
	} else {
		a.state.counter++
		sessionID = fmt.Sprintf("test-session-%d", a.state.counter)
	}
	a.state.calls = append(a.state.calls, TestAgentCall{
		SessionID: a.SessionID,
		Prompt:    prompt,
	})
	a.state.mu.Unlock()

	if a.Fail {
		return "", fmt.Errorf("test agent configured to fail")
	}

	shortSHA := git.ShortSHA(commitSHA)
	sessionLine := fmt.Sprintf(`{"type":"session","id":%q}`+"\n", sessionID)
	body := fmt.Sprintf("%s\n\nCommit: %s\nRepo: %s", a.Output, shortSHA, repoPath)
	streamed := sessionLine + body

	if output != nil {
		if _, err := output.Write([]byte(streamed)); err != nil {
			return "", fmt.Errorf("write output: %w", err)
		}
	}
	return body, nil
}

// FakeAgent implements Agent for tests outside the agent package.
type FakeAgent struct {
	NameStr  string
	ReviewFn func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error)
}

func (a *FakeAgent) Name() string { return a.NameStr }
func (a *FakeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	if a.ReviewFn != nil {
		return a.ReviewFn(ctx, repoPath, commitSHA, prompt, output)
	}
	return "", nil
}
func (a *FakeAgent) WithReasoning(level ReasoningLevel) Agent { return a }
func (a *FakeAgent) WithAgentic(agentic bool) Agent           { return a }
func (a *FakeAgent) WithModel(model string) Agent             { return a }
func (a *FakeAgent) CommandLine() string                      { return "" }

func init() {
	Register(NewTestAgent())
}
```

- [ ] **Step 4: Move the inline test into its own file**

Move the test code from Step 1 into a real file at `internal/agent/test_agent_session_test.go` (not appended inline anywhere). Then re-run:

```bash
go test ./internal/agent/ -run TestTestAgent -v
```

Expected: all three subtests PASS.

- [ ] **Step 5: Verify the existing Review() behavior still streams the body**

```bash
go test ./internal/agent/ -v
```

Expected: all existing agent tests still pass. (The streamed prefix line is JSON the existing tests do not assert against.)

- [ ] **Step 6: Verify the broader test suite still passes**

```bash
go test ./...
```

Expected: PASS. Streaming a synthetic session line at the start of `Review` is benign for callers that ignore session IDs.

If a test fails because it asserts on exact agent stream content (rather than substring), update the assertion to use `strings.Contains` or to strip the leading session line before comparison. The `body` returned by `Review()` is unchanged (it never included the session line); only the bytes written to `output` now include the session line as a JSONL prefix.

- [ ] **Step 7: Commit**

```bash
go fmt ./...
git add internal/agent/test_agent.go internal/agent/test_agent_session_test.go
git commit -m "$(cat <<'EOF'
test: add session capabilities to the test agent

Adds WithSessionID, a per-instance counter that mints distinct fresh
IDs (test-session-1, -2, ...), recording of every Review call's
incoming session ID, and emission of a synthetic
{"type":"session","id":"..."} line so SessionCaptureWriter picks
the ID up. Resumed calls echo the received ID; fresh calls mint a
new one. Lets later tests exercise --resume without invoking real
Codex or Claude binaries.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Refactor `splitIntoBatches` to use `batchSplitOptions`

**Why:** Once a count cap is added, a four-int signature is easy to misread. The options struct also makes the existing `--batch` and new `--batch-size` call sites self-documenting.

**Files:**
- Modify: `cmd/roborev/fix.go:1198-1227` (splitIntoBatches definition)
- Modify: `cmd/roborev/fix.go:1090-1091` (existing call site inside `runFixBatch`)
- Modify: `cmd/roborev/fix_test.go:1447-1571` (existing `TestSplitIntoBatches` subtests)

- [ ] **Step 1: Update the existing tests to use the new options-struct call signature**

Replace every call in `cmd/roborev/fix_test.go` matching `splitIntoBatches(entries, <maxSize>, <minSev>)` with `splitIntoBatches(entries, batchSplitOptions{MaxSize: <maxSize>, MinSeverity: <minSev>})`.

Specifically these call sites (all inside `TestSplitIntoBatches`):

| Line | Old | New |
|---|---|---|
| 1462 | `splitIntoBatches(entries, 100000, "")` | `splitIntoBatches(entries, batchSplitOptions{MaxSize: 100000})` |
| 1475 | `splitIntoBatches(entries, maxSize, "")` | `splitIntoBatches(entries, batchSplitOptions{MaxSize: maxSize})` |
| 1491 | `splitIntoBatches(entries, 1000, "")` | `splitIntoBatches(entries, batchSplitOptions{MaxSize: 1000})` |
| 1506 | `splitIntoBatches(nil, 100000, "")` | `splitIntoBatches(nil, batchSplitOptions{MaxSize: 100000})` |
| 1520 | `splitIntoBatches(entries, maxSize, "")` | `splitIntoBatches(entries, batchSplitOptions{MaxSize: maxSize})` |
| 1534 | `splitIntoBatches(entries, 100000, "")` | `splitIntoBatches(entries, batchSplitOptions{MaxSize: 100000})` |
| 1549 | `splitIntoBatches(entries, 1200, "")` | `splitIntoBatches(entries, batchSplitOptions{MaxSize: 1200})` |
| 1550 | `splitIntoBatches(entries, 1200, "high")` | `splitIntoBatches(entries, batchSplitOptions{MaxSize: 1200, MinSeverity: "high"})` |

- [ ] **Step 2: Run the tests to verify they fail (compile error)**

```bash
go test ./cmd/roborev/ -run TestSplitIntoBatches -v
```

Expected: compilation error — `batchSplitOptions` undefined.

- [ ] **Step 3: Update `splitIntoBatches` and its call sites in `cmd/roborev/fix.go`**

Replace the existing function (around lines 1194–1227) with:

```go
// batchSplitOptions configures how splitIntoBatches groups entries.
// Both caps are upper bounds; MaxCount = 0 means "no count cap".
type batchSplitOptions struct {
	MaxSize     int    // total prompt bytes per batch, including overhead
	MaxCount    int    // entries per batch (0 = unbounded)
	MinSeverity string // forwarded to overhead calculation
}

// splitIntoBatches groups entries into batches respecting opts.
// Greedy packing: a batch terminates when adding the next entry would
// exceed MaxSize, OR when MaxCount > 0 and the batch already has
// MaxCount entries. A single oversized entry still gets its own batch.
func splitIntoBatches(
	entries []batchEntry, opts batchSplitOptions,
) [][]batchEntry {
	overhead := batchPromptOverhead +
		len(config.SeverityInstruction(opts.MinSeverity))
	var batches [][]batchEntry
	var current []batchEntry
	currentSize := 0

	for _, e := range entries {
		entrySize := batchEntrySize(len(current)+1, e)

		countFull := opts.MaxCount > 0 && len(current) >= opts.MaxCount
		sizeFull := len(current) > 0 && currentSize+entrySize > opts.MaxSize
		if countFull || sizeFull {
			batches = append(batches, current)
			current = nil
			currentSize = 0
			entrySize = batchEntrySize(1, e)
		}

		current = append(current, e)
		if currentSize == 0 {
			currentSize = overhead
		}
		currentSize += entrySize
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}
```

Update the existing call site inside `runFixBatch` (around line 1091). Replace:

```go
batches := splitIntoBatches(entries, maxSize, minSev)
```

with:

```go
batches := splitIntoBatches(entries, batchSplitOptions{
	MaxSize:     maxSize,
	MinSeverity: minSev,
})
```

(The `MaxCount` field is added in Task 5; for this refactor it stays zero, preserving today's `--batch` behavior.)

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/roborev/ -run TestSplitIntoBatches -v
```

Expected: all subtests PASS.

- [ ] **Step 5: Add new subtests for `MaxCount`**

Append inside the existing `TestSplitIntoBatches` function in `cmd/roborev/fix_test.go`, just before the closing `}` of the function:

```go
	t.Run("count cap forces multiple batches when size would allow one", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 100),
			makeEntry(2, 100),
			makeEntry(3, 100),
			makeEntry(4, 100),
			makeEntry(5, 100),
		}
		batches := splitIntoBatches(entries, batchSplitOptions{
			MaxSize:  100000,
			MaxCount: 2,
		})
		require.Len(t, batches, 3)
		assert.Len(t, batches[0], 2)
		assert.Len(t, batches[1], 2)
		assert.Len(t, batches[2], 1)
	})

	t.Run("count cap and size cap, count cap dominates", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 100),
			makeEntry(2, 100),
			makeEntry(3, 100),
		}
		batches := splitIntoBatches(entries, batchSplitOptions{
			MaxSize:  100000,
			MaxCount: 1,
		})
		require.Len(t, batches, 3)
		for _, b := range batches {
			assert.Len(t, b, 1)
		}
	})

	t.Run("size cap and count cap, size cap dominates", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 500),
			makeEntry(2, 500),
			makeEntry(3, 500),
		}
		batches := splitIntoBatches(entries, batchSplitOptions{
			MaxSize:  1000,
			MaxCount: 100,
		})
		assert.GreaterOrEqual(t, len(batches), 2,
			"size cap forces a split even though count cap is far higher")
	})

	t.Run("count cap = 0 means unbounded", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 100),
			makeEntry(2, 100),
			makeEntry(3, 100),
			makeEntry(4, 100),
		}
		batches := splitIntoBatches(entries, batchSplitOptions{
			MaxSize:  100000,
			MaxCount: 0,
		})
		require.Len(t, batches, 1)
		assert.Len(t, batches[0], 4)
	})
```

Add the missing import to `cmd/roborev/fix_test.go` if not already present:

```go
	"github.com/stretchr/testify/require"
```

- [ ] **Step 6: Run new and existing tests**

```bash
go test ./cmd/roborev/ -run TestSplitIntoBatches -v
```

Expected: all subtests PASS, including the four new ones.

- [ ] **Step 7: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix.go cmd/roborev/fix_test.go
git commit -m "$(cat <<'EOF'
refactor: convert splitIntoBatches to batchSplitOptions

Replace splitIntoBatches(entries, maxSize, minSeverity) with an
options struct so adding a MaxCount field in a later commit doesn't
yield a four-int signature. MaxCount = 0 preserves today's
--batch behavior; the new field is exercised in this commit's added
subtests so the count-cap path is verified before the CLI flag wires
it in.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `--batch-size N` flag with mutual-exclusion errors

**Why:** Wires the count-cap parameter into the CLI surface. No behavior change for existing `--batch` users yet — the size cap still dominates because `MaxCount` is plumbed through but defaults to 0 unless the new flag is set.

**Files:**
- Modify: `cmd/roborev/fix.go:39-205` (flag declarations and `RunE` body)
- Modify: `cmd/roborev/fix.go:957-958` (`runFixBatch` signature)
- Modify: `cmd/roborev/fix.go:1090-1093` (call to `splitIntoBatches` in `runFixBatch`)
- Modify: `cmd/roborev/fix.go:1156-1170` (response-text strings inside `runFixBatch`)
- Modify: `cmd/roborev/fix_test.go` (new flag-validation subtests)

- [ ] **Step 1: Write failing flag-validation tests**

Append to `cmd/roborev/fix_test.go`:

```go
func TestFixCmd_BatchAndBatchSizeMutuallyExclusive(t *testing.T) {
	cmd := fixCmd()
	cmd.SetArgs([]string{"--batch", "--batch-size", "5"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--batch and --batch-size are mutually exclusive")
}

func TestFixCmd_BatchSizeMustBePositive(t *testing.T) {
	cmd := fixCmd()
	cmd.SetArgs([]string{"--batch-size", "0"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--batch-size must be >= 1")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/roborev/ -run "TestFixCmd_Batch" -v
```

Expected: tests fail because `--batch-size` flag is undefined.

- [ ] **Step 3: Add the `--batch-size` flag and validation in `cmd/roborev/fix.go`**

Inside `fixCmd()` (around line 39), in the variable block:

```go
var (
    agentName   string
    model       string
    reasoning   string
    minSeverity string
    quiet       bool
    open        bool // deprecated, silently ignored
    unaddressed bool // deprecated, silently ignored
    allBranches bool
    newestFirst bool
    branch      string
    batch       bool
    batchSize   int  // <-- NEW
    list        bool
)
```

Inside `RunE`, immediately after the existing mutual-exclusion checks (after the `--list` block, around line 110), add:

```go
if batch && batchSize > 0 {
    return fmt.Errorf("--batch and --batch-size are mutually exclusive")
}
if batchSize < 0 {
    return fmt.Errorf("--batch-size must be >= 1")
}
if cmd.Flags().Changed("batch-size") && batchSize == 0 {
    return fmt.Errorf("--batch-size must be >= 1")
}
```

Promote `batch || batchSize > 0` to the same code path as the existing `if batch { ... }` block. Replace the existing block (around lines 132-156):

```go
if batch {
    var jobIDs []int64
    for _, arg := range args {
        var id int64
        if _, err := fmt.Sscanf(arg, "%d", &id); err != nil {
            return fmt.Errorf("invalid job ID %q: must be a number", arg)
        }
        jobIDs = append(jobIDs, id)
    }
    if len(jobIDs) > 0 && (branch != "" || allBranches || newestFirst) {
        return fmt.Errorf("--branch, --all-branches, and --newest-first cannot be used with explicit job IDs")
    }
    // If no args, discover unaddressed jobs
    if len(jobIDs) == 0 {
        roots, err := resolveCurrentRepoRoots()
        if err != nil {
            return err
        }
        effectiveBranch := resolveCurrentBranchFilter(
            roots.worktreeRoot, branch, allBranches,
        )
        return runFixBatch(cmd, nil, effectiveBranch, allBranches, branch != "", newestFirst, opts)
    }
    return runFixBatch(cmd, jobIDs, "", false, false, false, opts)
}
```

With:

```go
if batch || batchSize > 0 {
    var jobIDs []int64
    for _, arg := range args {
        var id int64
        if _, err := fmt.Sscanf(arg, "%d", &id); err != nil {
            return fmt.Errorf("invalid job ID %q: must be a number", arg)
        }
        jobIDs = append(jobIDs, id)
    }
    if len(jobIDs) > 0 && (branch != "" || allBranches || newestFirst) {
        return fmt.Errorf("--branch, --all-branches, and --newest-first cannot be used with explicit job IDs")
    }
    if len(jobIDs) == 0 {
        roots, err := resolveCurrentRepoRoots()
        if err != nil {
            return err
        }
        effectiveBranch := resolveCurrentBranchFilter(
            roots.worktreeRoot, branch, allBranches,
        )
        return runFixBatch(cmd, nil, effectiveBranch, allBranches, branch != "", newestFirst, batchSize, opts)
    }
    return runFixBatch(cmd, jobIDs, "", false, false, false, batchSize, opts)
}
```

Register the new flag (after the existing `cmd.Flags().BoolVar(&batch, ...)` line around line 197):

```go
cmd.Flags().IntVar(&batchSize, "batch-size", 0, "concatenate up to N reviews per agent invocation (cap by count, still bounded by max_prompt_size)")
```

Update the `Long` help text examples block (around lines 71-80):

```go
		Long: `Run an agent to address findings from one or more completed reviews.

This is a single-pass fix: the agent applies changes and commits, but
does not re-review or iterate. Use 'roborev refine' for an automated
loop that re-reviews fixes and retries until reviews pass.

The agent runs synchronously in your terminal, streaming output as it
works. The review output is printed first so you can see what needs
fixing. When complete, the job is closed.

With no arguments, discovers and fixes all open completed jobs on the
current branch.

Examples:
  roborev fix                            # 1 review per agent call (default)
  roborev fix 123                        # Fix a single job
  roborev fix 123 124 125                # Fix multiple jobs sequentially
  roborev fix --agent claude-code 123    # Use a specific agent
  roborev fix --branch main              # Fix all open jobs on main
  roborev fix --all-branches             # Fix all open jobs across all branches
  roborev fix --batch-size 5             # Up to 5 reviews per agent call
  roborev fix --batch                    # Pack until max_prompt_size
  roborev fix --list                     # List open jobs without fixing
`,
```

- [ ] **Step 4: Update `runFixBatch` signature to accept `batchSize`**

Replace the function declaration (around line 957):

```go
func runFixBatch(cmd *cobra.Command, jobIDs []int64, branch string, allBranches, explicitBranch, newestFirst bool, opts fixOptions) error {
```

With:

```go
func runFixBatch(cmd *cobra.Command, jobIDs []int64, branch string, allBranches, explicitBranch, newestFirst bool, batchSize int, opts fixOptions) error {
```

Update the `splitIntoBatches` call inside `runFixBatch` (around line 1090). Replace:

```go
	maxSize := config.ResolveMaxPromptSize(roots.worktreeRoot, cfg)
	batches := splitIntoBatches(entries, batchSplitOptions{
		MaxSize:     maxSize,
		MinSeverity: minSev,
	})
```

With:

```go
	maxSize := config.ResolveMaxPromptSize(roots.worktreeRoot, cfg)
	batches := splitIntoBatches(entries, batchSplitOptions{
		MaxSize:     maxSize,
		MaxCount:    batchSize,
		MinSeverity: minSev,
	})
```

Update the response-text strings to reflect the activating flag (around line 1156-1160). Replace:

```go
		responseText := "Fix applied via `roborev fix --batch`"
		if result.CommitCreated {
			responseText = fmt.Sprintf("Fix applied via `roborev fix --batch` (commit: %s)", git.ShortSHA(result.NewCommitSHA))
		}
```

With:

```go
		flagLabel := "--batch"
		if batchSize > 0 {
			flagLabel = "--batch-size"
		}
		responseText := fmt.Sprintf("Fix applied via `roborev fix %s`", flagLabel)
		if result.CommitCreated {
			responseText = fmt.Sprintf("Fix applied via `roborev fix %s` (commit: %s)", flagLabel, git.ShortSHA(result.NewCommitSHA))
		}
```

- [ ] **Step 5: Run flag validation tests**

```bash
go test ./cmd/roborev/ -run "TestFixCmd_Batch" -v
```

Expected: PASS.

- [ ] **Step 6: Run the full fix test suite to catch regressions**

```bash
go test ./cmd/roborev/ -v
```

Expected: PASS. The existing batch tests continue to pass because `batchSize == 0` results in `MaxCount: 0` (unbounded), preserving today's `--batch` behavior.

- [ ] **Step 7: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix.go cmd/roborev/fix_test.go
git commit -m "$(cat <<'EOF'
feat: add roborev fix --batch-size N

Adds a count-bounded batching mode that concatenates up to N reviews
into one agent invocation. Mutually exclusive with --batch. Routes
through the same runFixBatch path; size-cap (max_prompt_size) still
applies on top of the count cap. Response text reflects the
activating flag (--batch vs --batch-size).

Default behavior (no flag) is unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Add `--resume` and `--no-resume` flags with mutual-exclusion error

**Why:** Wires the resume opt-in into the CLI surface. Behavior is added in Tasks 6–8; this task only parses and validates the flags and threads `resume` into `fixOptions`.

**Files:**
- Modify: `cmd/roborev/fix.go:39-205` (flag declarations and `RunE` body)
- Modify: `cmd/roborev/fix.go:207-213` (`fixOptions` struct)
- Modify: `cmd/roborev/fix_test.go` (new flag-validation subtests)

- [ ] **Step 1: Write failing flag-validation tests**

Append to `cmd/roborev/fix_test.go`:

```go
func TestFixCmd_ResumeAndNoResumeMutuallyExclusive(t *testing.T) {
	cmd := fixCmd()
	cmd.SetArgs([]string{"--resume", "--no-resume"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--resume and --no-resume are mutually exclusive")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/roborev/ -run TestFixCmd_Resume -v
```

Expected: fails — flags undefined.

- [ ] **Step 3: Add the flags and validation in `cmd/roborev/fix.go`**

Add the two booleans in the variable block inside `fixCmd()`:

```go
    resume   bool
    noResume bool
```

Add validation in `RunE` after the existing mutual-exclusion block:

```go
if resume && noResume {
    return fmt.Errorf("--resume and --no-resume are mutually exclusive")
}
```

Register the flags after the existing flag declarations (around line 198):

```go
cmd.Flags().BoolVar(&resume, "resume", false, "resume the agent's session ID across calls within this run")
cmd.Flags().BoolVar(&noResume, "no-resume", false, "explicitly disable session resume (default off)")
```

Add a `resume` field to `fixOptions` (around line 207):

```go
type fixOptions struct {
    agentName   string
    model       string
    reasoning   string
    minSeverity string
    quiet       bool
    resume      bool // <-- NEW
}
```

Populate it in `RunE` where `opts` is constructed (around line 124):

```go
opts := fixOptions{
    agentName:   agentName,
    model:       model,
    reasoning:   reasoning,
    minSeverity: minSeverity,
    quiet:       quiet,
    resume:      resume, // --no-resume is a noop today; explicit override happens here
}
```

(`--no-resume` need not be threaded into `fixOptions`; its only job is to error out when combined with `--resume`. If the user passes only `--no-resume`, `resume` stays `false`, which is the existing default.)

- [ ] **Step 4: Run tests**

```bash
go test ./cmd/roborev/ -run TestFixCmd_Resume -v
```

Expected: PASS.

- [ ] **Step 5: Run the full fix test suite**

```bash
go test ./cmd/roborev/ -v
```

Expected: PASS. (No behavior change yet; tracker wiring lands in Task 6+.)

- [ ] **Step 6: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix.go cmd/roborev/fix_test.go
git commit -m "$(cat <<'EOF'
feat: add --resume and --no-resume flags to roborev fix

Adds the flag surface and mutual-exclusion validation. resume is
threaded into fixOptions for use by the upcoming session tracker;
no-resume serves only to error out when paired with --resume so
script authors can't accidentally pass both. Behavior is unchanged
in this commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Implement `fixSessionTracker`

**Files:**
- Create: `cmd/roborev/fix_session.go`
- Create: `cmd/roborev/fix_session_test.go`

- [ ] **Step 1: Write failing tests**

Create `cmd/roborev/fix_session_test.go`:

```go
package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nonSessionAgent implements agent.Agent but not agent.SessionAgent.
type nonSessionAgent struct{}

func (a *nonSessionAgent) Name() string { return "non-session" }
func (a *nonSessionAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	return "", nil
}
func (a *nonSessionAgent) WithReasoning(level agent.ReasoningLevel) agent.Agent { return a }
func (a *nonSessionAgent) WithAgentic(agentic bool) agent.Agent                 { return a }
func (a *nonSessionAgent) WithModel(model string) agent.Agent                   { return a }
func (a *nonSessionAgent) CommandLine() string                                  { return "non-session" }

func TestFixSessionTracker_DisabledAlwaysReturnsBase(t *testing.T) {
	base := agent.NewTestAgent()
	logged := &strings.Builder{}
	tr := &fixSessionTracker{
		enabled: false,
		base:    base,
		log:     func(s string) { logged.WriteString(s) },
	}

	a, resuming := tr.NextAgent()
	assert.Same(t, agent.Agent(base), a)
	assert.False(t, resuming)

	tr.Capture("test-session-1")
	a, resuming = tr.NextAgent()
	assert.Same(t, agent.Agent(base), a, "disabled tracker ignores Capture")
	assert.False(t, resuming)
	assert.Empty(t, logged.String(), "disabled tracker never logs")
}

func TestFixSessionTracker_EnabledFirstCallReturnsBase(t *testing.T) {
	base := agent.NewTestAgent()
	tr := &fixSessionTracker{enabled: true, base: base, log: func(string) {}}

	a, resuming := tr.NextAgent()
	assert.Same(t, agent.Agent(base), a)
	assert.False(t, resuming)
}

func TestFixSessionTracker_AfterCaptureReturnsResumedAgent(t *testing.T) {
	base := agent.NewTestAgent()
	tr := &fixSessionTracker{enabled: true, base: base, log: func(string) {}}

	tr.Capture("test-session-1")
	a, resuming := tr.NextAgent()
	require.True(t, resuming)
	resumed, ok := a.(*agent.TestAgent)
	require.True(t, ok)
	assert.Equal(t, "test-session-1", resumed.SessionID)
}

func TestFixSessionTracker_InvalidIDsDropped(t *testing.T) {
	base := agent.NewTestAgent()
	tr := &fixSessionTracker{enabled: true, base: base, log: func(string) {}}

	tr.Capture("")
	tr.Capture("contains spaces")
	tr.Capture(strings.Repeat("x", 200)) // exceeds 128-char limit

	a, resuming := tr.NextAgent()
	assert.Same(t, agent.Agent(base), a)
	assert.False(t, resuming)
}

func TestFixSessionTracker_ResetClearsLast(t *testing.T) {
	base := agent.NewTestAgent()
	tr := &fixSessionTracker{enabled: true, base: base, log: func(string) {}}

	tr.Capture("test-session-1")
	tr.Reset()

	a, resuming := tr.NextAgent()
	assert.Same(t, agent.Agent(base), a)
	assert.False(t, resuming)
}

func TestFixSessionTracker_NonSessionAgentWarnsOnce(t *testing.T) {
	base := &nonSessionAgent{}
	logged := &strings.Builder{}
	tr := &fixSessionTracker{
		enabled: true,
		base:    base,
		log:     func(s string) { logged.WriteString(s) },
	}

	for range 5 {
		a, resuming := tr.NextAgent()
		assert.Same(t, agent.Agent(base), a)
		assert.False(t, resuming)
	}

	out := logged.String()
	count := strings.Count(out, "does not support session resume")
	assert.Equal(t, 1, count, "warning fires exactly once across many calls")
}

func TestFixSessionTracker_NonSessionAgentWarningRespectsQuiet(t *testing.T) {
	base := &nonSessionAgent{}
	logged := &strings.Builder{}
	tr := &fixSessionTracker{
		enabled: true,
		base:    base,
		quiet:   true,
		log:     func(s string) { logged.WriteString(s) },
	}

	tr.NextAgent()
	tr.NextAgent()
	assert.Empty(t, logged.String(), "no warning when --quiet")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/roborev/ -run TestFixSessionTracker -v
```

Expected: compile error (`fixSessionTracker` undefined).

- [ ] **Step 3: Create `cmd/roborev/fix_session.go`**

```go
package main

import (
	"fmt"

	"github.com/roborev-dev/roborev/internal/agent"
)

// fixSessionTracker keeps the most-recent agent session ID across
// calls within a single roborev fix run and produces the agent to
// invoke next. See spec for full contract.
type fixSessionTracker struct {
	enabled bool          // --resume passed
	base    agent.Agent   // resolved once (already WithAgentic/WithReasoning/WithModel)
	last    string        // most recent valid captured session ID
	warned  bool          // log "agent X doesn't support resume" only once
	quiet   bool          // suppress progress logs and warnings under --quiet
	log     func(string)  // injected logger; cmd.Printf in CLI, no-op in tests
}

// NextAgent returns the agent to use for the next invocation along
// with a resuming flag indicating whether this call is actually
// resumed. The flag is the source of truth for "Resuming session ..."
// — relying on agent identity equality is brittle.
//
// Decision order (kept exact for contract clarity):
//  1. !enabled                        → (base, false)
//  2. !SessionAgent                   → warn once (respect quiet); (base, false)
//  3. last == ""                      → (base, false)
//  4. otherwise                       → (sa.WithSessionID(last), true)
func (t *fixSessionTracker) NextAgent() (agent.Agent, bool) {
	if !t.enabled {
		return t.base, false
	}
	sa, ok := t.base.(agent.SessionAgent)
	if !ok {
		if !t.warned {
			t.warned = true
			if !t.quiet && t.log != nil {
				t.log(fmt.Sprintf(
					"Warning: agent %s does not support session resume; running fresh sessions\n",
					t.base.Name(),
				))
			}
		}
		return t.base, false
	}
	if t.last == "" {
		return t.base, false
	}
	return sa.WithSessionID(t.last), true
}

// Capture stores id only if it is non-empty AND passes the resume ID
// validator. Empty/invalid IDs are silently dropped so the
// "Resuming session ..." log can never lie.
func (t *fixSessionTracker) Capture(id string) {
	if id == "" {
		return
	}
	if !agent.IsValidResumeSessionID(id) {
		return
	}
	t.last = id
}

// Reset clears the tracked session ID. Called on any agent error so a
// stale session token does not poison every subsequent batch.
func (t *fixSessionTracker) Reset() {
	t.last = ""
}

// shortSessionID returns the first 12 characters of id for log lines.
func shortSessionID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/roborev/ -run TestFixSessionTracker -v
```

Expected: all subtests PASS.

- [ ] **Step 5: Verify the full suite still passes**

```bash
go test ./...
```

Expected: PASS. The tracker is not yet wired into any path, so behavior is unchanged.

- [ ] **Step 6: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix_session.go cmd/roborev/fix_session_test.go
git commit -m "$(cat <<'EOF'
feat: add fixSessionTracker for roborev fix --resume

Tracks the most recent agent session ID across calls and produces
the next agent to invoke (base or base.WithSessionID(last)). Returns
an explicit resuming bool so logging is not driven by agent identity.
Validates captured IDs at the tracker boundary so "Resuming session
..." logs can never lie. Reset() clears state on agent error so a
stale session does not poison subsequent calls.

Not wired in yet; that lands in the next two commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Wire tracker + capture writer into the `runFixBatch` path

**Files:**
- Modify: `cmd/roborev/fix.go:957-1176` (`runFixBatch` signature and per-batch loop)
- Modify: `cmd/roborev/fix.go:39-205` (`fixCmd RunE` — construct tracker, pass to `runFixBatch`)

- [ ] **Step 1: Add `tracker *fixSessionTracker` to `runFixBatch`'s signature**

Replace the function declaration (around line 957, where Task 4 left it):

```go
func runFixBatch(cmd *cobra.Command, jobIDs []int64, branch string, allBranches, explicitBranch, newestFirst bool, batchSize int, opts fixOptions) error {
```

With:

```go
func runFixBatch(cmd *cobra.Command, jobIDs []int64, branch string, allBranches, explicitBranch, newestFirst bool, batchSize int, opts fixOptions, tracker *fixSessionTracker) error {
```

Delete the existing block in `runFixBatch` that resolves the agent (around lines 1061-1066):

```go
	// Resolve agent once
	fixAgent, err := resolveFixAgent(roots.worktreeRoot, opts)
	if err != nil {
		return err
	}
```

Agent resolution now happens in `RunE`, where the tracker is built. `tracker.base` holds the resolved agent.

- [ ] **Step 2: Replace per-batch agent and capture wiring**

Inside the `for i, batch := range batches` loop (around lines 1093-1147), replace the existing block:

```go
		if !opts.quiet {
			cmd.Printf("\n=== Batch %d/%d (jobs %s) ===\n\n", i+1, len(batches), formatJobIDs(batchJobIDs))
			w := cmd.OutOrStdout()
			for _, e := range batch {
				cmd.Printf("Job %d findings:\n", e.jobID)
				cmd.Println(strings.Repeat("-", 60))
				streamfmt.PrintMarkdownOrPlain(w, e.review.Output)
				cmd.Println(strings.Repeat("-", 60))
				cmd.Println()
			}
			cmd.Printf("Running fix agent (%s) to apply changes...\n\n", fixAgent.Name())
		}

		prompt := buildBatchFixPrompt(batch, minSev)

		var out io.Writer
		var fmtr *streamfmt.Formatter
		if opts.quiet {
			out = io.Discard
		} else {
			fmtr = streamfmt.New(cmd.OutOrStdout(), streamfmt.WriterIsTerminal(cmd.OutOrStdout()))
			out = fmtr
		}

		result, err := fixJobDirect(ctx, fixJobParams{
			RepoRoot: roots.worktreeRoot,
			Agent:    fixAgent,
			Output:   out,
		}, prompt)
		if fmtr != nil {
			fmtr.Flush()
		}
		if err != nil {
			cmd.Printf("Warning: error in batch %d: %v\n", i+1, err)
			continue
		}
```

With:

```go
		currentAgent, resuming := tracker.NextAgent()

		if !opts.quiet {
			cmd.Printf("\n=== Batch %d/%d (jobs %s) ===\n\n", i+1, len(batches), formatJobIDs(batchJobIDs))
			w := cmd.OutOrStdout()
			for _, e := range batch {
				cmd.Printf("Job %d findings:\n", e.jobID)
				cmd.Println(strings.Repeat("-", 60))
				streamfmt.PrintMarkdownOrPlain(w, e.review.Output)
				cmd.Println(strings.Repeat("-", 60))
				cmd.Println()
			}
			if resuming {
				cmd.Printf("Resuming session %s\n", shortSessionID(tracker.last))
			}
			cmd.Printf("Running fix agent (%s) to apply changes...\n\n", currentAgent.Name())
		}

		prompt := buildBatchFixPrompt(batch, minSev)

		var underlying io.Writer = io.Discard
		var fmtr *streamfmt.Formatter
		if !opts.quiet {
			fmtr = streamfmt.New(cmd.OutOrStdout(), streamfmt.WriterIsTerminal(cmd.OutOrStdout()))
			underlying = fmtr
		}
		capture := agent.NewSessionCaptureWriter(underlying, nil)

		result, err := fixJobDirect(ctx, fixJobParams{
			RepoRoot: roots.worktreeRoot,
			Agent:    currentAgent,
			Output:   capture,
		}, prompt)
		// Flush capture FIRST so session extraction completes before reading SessionID.
		capture.Flush()
		if fmtr != nil {
			fmtr.Flush()
		}
		if err != nil {
			tracker.Reset()
			cmd.Printf("Warning: error in batch %d: %v\n", i+1, err)
			continue
		}
		tracker.Capture(capture.SessionID())
```

- [ ] **Step 3: Hoist roots, agent resolution, and tracker construction in `RunE` once for all fix paths**

After `opts` is built and the `if list { ... }` block, add a single tracker-construction block. Then pass `tracker` to every `runFixBatch` call site. (Task 8 will pass the same `tracker` to `runFixOpen` / `runFix`.)

Insert immediately after `opts := fixOptions{...}`:

```go
roots, err := resolveCurrentRepoRoots()
if err != nil {
    return err
}
base, err := resolveFixAgent(roots.worktreeRoot, opts)
if err != nil {
    return err
}
tracker := &fixSessionTracker{
    enabled: opts.resume,
    base:    base,
    quiet:   opts.quiet,
    log:     func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) },
}
```

Replace the existing `if batch || batchSize > 0` block body so the inner `resolveCurrentRepoRoots` call goes away (we already have `roots`):

```go
if batch || batchSize > 0 {
    var jobIDs []int64
    for _, arg := range args {
        var id int64
        if _, err := fmt.Sscanf(arg, "%d", &id); err != nil {
            return fmt.Errorf("invalid job ID %q: must be a number", arg)
        }
        jobIDs = append(jobIDs, id)
    }
    if len(jobIDs) > 0 && (branch != "" || allBranches || newestFirst) {
        return fmt.Errorf("--branch, --all-branches, and --newest-first cannot be used with explicit job IDs")
    }
    if len(jobIDs) == 0 {
        effectiveBranch := resolveCurrentBranchFilter(roots.worktreeRoot, branch, allBranches)
        return runFixBatch(cmd, nil, effectiveBranch, allBranches, branch != "", newestFirst, batchSize, opts, tracker)
    }
    return runFixBatch(cmd, jobIDs, "", false, false, false, batchSize, opts, tracker)
}
```

(Task 8 then updates the remaining `runFixOpen(...)` and `runFix(...)` calls, which can also use the same `roots` and `tracker` — no further duplication.)

- [ ] **Step 4: Build and run the existing `--batch` tests to verify no regression**

```bash
go build ./...
go test ./cmd/roborev/ -v
```

Expected: PASS. The capture writer is transparent; the existing batch path stays functionally identical when `opts.resume` is false. Default `roborev fix` (no flag) is also unchanged because that path is still on `runFixOpen` (Task 8 wires it).

- [ ] **Step 5: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix.go cmd/roborev/fix_integration_test.go
git commit -m "$(cat <<'EOF'
feat: wire fixSessionTracker and capture writer into runFixBatch

Each batch invocation now derives its agent via tracker.NextAgent(),
streams through agent.NewSessionCaptureWriter, captures the resulting
session ID on success, and resets on error so a dead session cannot
cascade. capture.Flush() runs before fmtr.Flush() so session
extraction completes before SessionID() is read.

When --resume is off (default), tracker.NextAgent always returns
(base, false), making the change transparent for existing --batch
users.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Wire tracker + capture writer into the `runFix` / `runFixOpen` / `fixSingleJob` path

**Files:**
- Modify: `cmd/roborev/fix.go:366-410` (`runFix`, `runFixWithSeen`)
- Modify: `cmd/roborev/fix.go:412-501` (`runFixOpen`)
- Modify: `cmd/roborev/fix.go:798-947` (`fixSingleJob`)
- Modify: `cmd/roborev/fix.go:39-205` (`fixCmd RunE` — call sites)

- [ ] **Step 1: Add the tracker as a parameter to `runFix`, `runFixWithSeen`, `runFixOpen`, `fixSingleJob`**

Update the four function signatures and call them through. Replace `runFix` (around line 366):

```go
func runFix(cmd *cobra.Command, jobIDs []int64, opts fixOptions) error {
	return runFixWithSeen(cmd, jobIDs, opts, nil)
}
```

With:

```go
func runFix(cmd *cobra.Command, jobIDs []int64, opts fixOptions, tracker *fixSessionTracker) error {
	return runFixWithSeen(cmd, jobIDs, opts, nil, tracker)
}
```

Replace `runFixWithSeen` signature (around line 370):

```go
func runFixWithSeen(cmd *cobra.Command, jobIDs []int64, opts fixOptions, seen map[int64]bool) error {
```

With:

```go
func runFixWithSeen(cmd *cobra.Command, jobIDs []int64, opts fixOptions, seen map[int64]bool, tracker *fixSessionTracker) error {
```

Inside `runFixWithSeen`, update the `fixSingleJob` call (around line 387):

```go
err := fixSingleJob(cmd, roots.worktreeRoot, jobID, opts)
```

To:

```go
err := fixSingleJob(cmd, roots.worktreeRoot, jobID, opts, tracker)
```

Replace `runFixOpen` signature (around line 424):

```go
func runFixOpen(cmd *cobra.Command, branch string, allBranches, explicitBranch, newestFirst bool, opts fixOptions) error {
```

With:

```go
func runFixOpen(cmd *cobra.Command, branch string, allBranches, explicitBranch, newestFirst bool, opts fixOptions, tracker *fixSessionTracker) error {
```

And update the `runFixWithSeen` call inside `runFixOpen` (around line 497):

```go
if err := runFixWithSeen(cmd, newIDs, opts, seen); err != nil {
```

To:

```go
if err := runFixWithSeen(cmd, newIDs, opts, seen, tracker); err != nil {
```

Replace `fixSingleJob` signature (around line 798):

```go
func fixSingleJob(cmd *cobra.Command, repoRoot string, jobID int64, opts fixOptions) error {
```

With:

```go
func fixSingleJob(cmd *cobra.Command, repoRoot string, jobID int64, opts fixOptions, tracker *fixSessionTracker) error {
```

- [ ] **Step 2: Replace agent resolution and capture wiring inside `fixSingleJob`**

Inside `fixSingleJob`, replace the existing block (around lines 845-848):

```go
	// Resolve agent
	fixAgent, err := resolveFixAgent(repoRoot, opts)
	if err != nil {
		return err
	}
```

With:

```go
	currentAgent, resuming := tracker.NextAgent()
```

And the I/O setup block (around lines 875-898):

```go
	if !opts.quiet {
		cmd.Printf("Running fix agent (%s) to apply changes...\n\n", fixAgent.Name())
	}

	// Set up output
	var out io.Writer
	var fmtr *streamfmt.Formatter
	if opts.quiet {
		out = io.Discard
	} else {
		fmtr = streamfmt.New(cmd.OutOrStdout(), streamfmt.WriterIsTerminal(cmd.OutOrStdout()))
		out = fmtr
	}

	result, err := fixJobDirect(ctx, fixJobParams{
		RepoRoot: repoRoot,
		Agent:    fixAgent,
		Output:   out,
	}, buildGenericFixPrompt(review.Output, minSev, comments))
	if fmtr != nil {
		fmtr.Flush()
	}
	if err != nil {
		return err
	}
```

With:

```go
	if !opts.quiet {
		if resuming {
			cmd.Printf("Resuming session %s\n", shortSessionID(tracker.last))
		}
		cmd.Printf("Running fix agent (%s) to apply changes...\n\n", currentAgent.Name())
	}

	// Set up output
	var underlying io.Writer = io.Discard
	var fmtr *streamfmt.Formatter
	if !opts.quiet {
		fmtr = streamfmt.New(cmd.OutOrStdout(), streamfmt.WriterIsTerminal(cmd.OutOrStdout()))
		underlying = fmtr
	}
	capture := agent.NewSessionCaptureWriter(underlying, nil)

	result, err := fixJobDirect(ctx, fixJobParams{
		RepoRoot: repoRoot,
		Agent:    currentAgent,
		Output:   capture,
	}, buildGenericFixPrompt(review.Output, minSev, comments))
	// Flush capture FIRST so session extraction completes before reading SessionID.
	capture.Flush()
	if fmtr != nil {
		fmtr.Flush()
	}
	if err != nil {
		tracker.Reset()
		return err
	}
	tracker.Capture(capture.SessionID())
```

- [ ] **Step 3: Update the remaining `runFixOpen(...)` and `runFix(...)` call sites in `RunE`**

`roots` and `tracker` were hoisted once in Task 7 Step 3, so the two remaining call sites just need updated arguments — no additional construction.

Replace (around line 170):
```go
roots, err := resolveCurrentRepoRoots()
if err != nil {
    return err
}
effectiveBranch := resolveCurrentBranchFilter(
    roots.worktreeRoot, branch, allBranches,
)
return runFixOpen(cmd, effectiveBranch, allBranches, branch != "", newestFirst, opts)
```

With:
```go
effectiveBranch := resolveCurrentBranchFilter(
    roots.worktreeRoot, branch, allBranches,
)
return runFixOpen(cmd, effectiveBranch, allBranches, branch != "", newestFirst, opts, tracker)
```

(The `roots` block is removed because `roots` is now in scope from Task 7's hoist.)

Replace (around line 183):
```go
return runFix(cmd, jobIDs, opts)
```

With:
```go
return runFix(cmd, jobIDs, opts, tracker)
```

- [ ] **Step 4: Build and run the full test suite**

```bash
go build ./...
go test ./...
```

Expected: PASS. Default behavior is unchanged because `opts.resume` is false (so tracker returns `(base, false)` every call), and the capture writer is transparent.

- [ ] **Step 5: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix.go
git commit -m "$(cat <<'EOF'
feat: wire fixSessionTracker into runFix / runFixOpen / fixSingleJob

The single-review path now uses the same tracker + capture writer
chain as the batch path. resolveFixAgent is hoisted out of
fixSingleJob into fixCmd's RunE so the tracker can be constructed
once. fixSingleJob returns the new currentAgent from
tracker.NextAgent() and resets the tracker on agent error.

Default (no --resume) is unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Integration tests for `--batch-size`, `--resume`, and combinations

**Files:**
- Modify: `cmd/roborev/fix_integration_test.go`

**Harness primer** — the existing test harness (in `cmd/roborev/helpers_test.go` and `cmd/roborev/main_test_helpers_test.go`) provides:

| Helper | Purpose |
|---|---|
| `createTestRepo(t, files map[string]string) *TestGitRepo` | Real temp git repo. `repo.Dir` is the path. |
| `newMockDaemonBuilder(t).WithJobs(jobs).WithReview(id, output).Build()` | Stands up an `httptest` daemon and patches `serverAddr` automatically. No `pointDaemonAt` needed. |
| `runWithOutput(t, dir, fn)` | Runs `fn(cmd)` with a captured-output cobra command. |
| `agent.Register` / `agent.Unregister` | Swap the registered `test` agent. |

**Approach:** in each test, replace the registered `test` agent with a fresh `*TestAgent` we hold a reference to, then restore at the end via `t.Cleanup`. Construct the tracker explicitly and call `runFixWithSeen` (or `runFixBatch`) directly so we exercise the flag-translated parameters without going through `ensureDaemon`.

- [ ] **Step 1: Add a helper for swapping the test agent in tests**

Append to `cmd/roborev/fix_integration_test.go`:

```go
// withFreshTestAgent registers a brand-new *agent.TestAgent under the
// name "test", returning the instance so the test can read its
// recorded calls. Restores the default test agent in cleanup.
func withFreshTestAgent(t *testing.T) *agent.TestAgent {
	t.Helper()
	a := agent.NewTestAgent()
	a.Delay = 0 // tests don't want the 100ms baked-in delay
	agent.Register(a)
	t.Cleanup(func() {
		agent.Unregister("test")
		agent.Register(agent.NewTestAgent())
	})
	return a
}
```

(Add `"github.com/roborev-dev/roborev/internal/agent"` to the imports.)

- [ ] **Step 2: Add the `--batch-size` count-cap integration test**

Append to `cmd/roborev/fix_integration_test.go`:

```go
func TestRunFixBatch_CountCapGroupsIntoTwoTwoOne(t *testing.T) {
	tester := withFreshTestAgent(t)

	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	jobs := []storage.ReviewJob{
		{ID: 1, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		{ID: 2, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		{ID: 3, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		{ID: 4, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		{ID: 5, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
	}
	_ = newMockDaemonBuilder(t).
		WithJobs(jobs).
		WithReview(1, "F1: missing error handling").
		WithReview(2, "F2: unused variable").
		WithReview(3, "F3: deadlock").
		WithReview(4, "F4: race").
		WithReview(5, "F5: leak").
		Build()

	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		opts := fixOptions{agentName: "test", quiet: true}
		base, err := resolveFixAgent(repo.Dir, opts)
		if err != nil {
			return err
		}
		tracker := &fixSessionTracker{
			base: base,
			log:  func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) },
		}
		return runFixBatch(cmd, []int64{1, 2, 3, 4, 5}, "", false, false, false, 2, opts, tracker)
	})
	require.NoError(t, err)

	calls := tester.Calls()
	require.Len(t, calls, 3, "5 jobs / batch-size 2 = 3 invocations")
	assert.Contains(t, calls[0].Prompt, "F1")
	assert.Contains(t, calls[0].Prompt, "F2")
	assert.Contains(t, calls[1].Prompt, "F3")
	assert.Contains(t, calls[1].Prompt, "F4")
	assert.Contains(t, calls[2].Prompt, "F5")
	assert.NotContains(t, calls[2].Prompt, "F4")
}
```

**Important:** because `runFixBatch` now takes a tracker, **its signature also needs the tracker added in Task 7**. Update Task 7 Step 1 to include `tracker *fixSessionTracker` as a parameter to `runFixBatch` (replacing the inline construction shown there). The fix-cmd `RunE` calls in Task 4 Step 3 then become:

```go
return runFixBatch(cmd, nil, effectiveBranch, allBranches, branch != "", newestFirst, batchSize, opts, tracker)
```

Construct the tracker in `RunE` the same way Task 8 Step 3 does (resolveFixAgent → tracker struct), and pass it through. This keeps all paths uniform — Task 8 already does this for `runFixOpen` / `runFix`.

- [ ] **Step 3: Add the `--resume` chain test (single-job mode)**

Append to `cmd/roborev/fix_integration_test.go`:

```go
func TestRunFix_ResumeChainsSessionAcrossSingleJobCalls(t *testing.T) {
	tester := withFreshTestAgent(t)

	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	jobs := []storage.ReviewJob{
		{ID: 11, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		{ID: 12, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		{ID: 13, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
	}
	_ = newMockDaemonBuilder(t).
		WithJobs(jobs).
		WithReview(11, "Issue 1").
		WithReview(12, "Issue 2").
		WithReview(13, "Issue 3").
		Build()

	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		opts := fixOptions{agentName: "test", quiet: true, resume: true}
		base, err := resolveFixAgent(repo.Dir, opts)
		if err != nil {
			return err
		}
		tracker := &fixSessionTracker{
			enabled: true,
			base:    base,
			quiet:   true,
			log:     func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) },
		}
		return runFix(cmd, []int64{11, 12, 13}, opts, tracker)
	})
	require.NoError(t, err)

	calls := tester.Calls()
	require.Len(t, calls, 3)
	assert.Empty(t, calls[0].SessionID, "first call is fresh")
	assert.Equal(t, "test-session-1", calls[1].SessionID, "second call resumes session 1")
	assert.Equal(t, "test-session-1", calls[2].SessionID,
		"third call resumes session 1 — chain preserved because the test agent echoes incoming IDs")
}
```

- [ ] **Step 4: Add the `--batch-size --resume` combined test**

```go
func TestRunFix_BatchSizeAndResumeCombined(t *testing.T) {
	tester := withFreshTestAgent(t)

	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	jobs := make([]storage.ReviewJob, 6)
	builder := newMockDaemonBuilder(t)
	for i := range 6 {
		jobs[i] = storage.ReviewJob{ID: int64(20 + i), Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"}
		builder = builder.WithReview(int64(20+i), fmt.Sprintf("issue %d", i))
	}
	_ = builder.WithJobs(jobs).Build()

	_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		opts := fixOptions{agentName: "test", quiet: true, resume: true}
		base, err := resolveFixAgent(repo.Dir, opts)
		if err != nil {
			return err
		}
		tracker := &fixSessionTracker{
			enabled: true,
			base:    base,
			quiet:   true,
			log:     func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) },
		}
		return runFixBatch(cmd, []int64{20, 21, 22, 23, 24, 25}, "", false, false, false, 3, opts, tracker)
	})
	require.NoError(t, err)

	calls := tester.Calls()
	require.Len(t, calls, 2, "6 jobs / batch-size 3 = 2 invocations")
	assert.Empty(t, calls[0].SessionID)
	assert.Equal(t, "test-session-1", calls[1].SessionID)
}
```

- [ ] **Step 5: Add the cascade-on-error test**

```go
func TestRunFix_ResumeCascadeBrokenOnError(t *testing.T) {
	tester := withFreshTestAgent(t)
	callIdx := 0

	// Replace the registered "test" agent with a wrapper that fails on call 2.
	failer := &failOnNthAgent{inner: tester, failOn: 2, calls: &callIdx}
	agent.Unregister("test")
	agent.Register(&namedAgent{name: "test", inner: failer})
	t.Cleanup(func() {
		agent.Unregister("test")
		agent.Register(agent.NewTestAgent())
	})

	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	jobs := make([]storage.ReviewJob, 6)
	builder := newMockDaemonBuilder(t)
	for i := range 6 {
		jobs[i] = storage.ReviewJob{ID: int64(30 + i), Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"}
		builder = builder.WithReview(int64(30+i), fmt.Sprintf("issue %d", i))
	}
	_ = builder.WithJobs(jobs).Build()

	_, _ = runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		opts := fixOptions{agentName: "test", quiet: true, resume: true}
		base, err := resolveFixAgent(repo.Dir, opts)
		if err != nil {
			return err
		}
		tracker := &fixSessionTracker{
			enabled: true,
			base:    base,
			quiet:   true,
			log:     func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) },
		}
		// Errors are expected mid-run; runFixBatch logs and continues.
		return runFixBatch(cmd, []int64{30, 31, 32, 33, 34, 35}, "", false, false, false, 2, opts, tracker)
	})

	calls := tester.Calls()
	require.GreaterOrEqual(t, len(calls), 3,
		"3 invocations expected: success, fail (still records), success-fresh")
	assert.Empty(t, calls[0].SessionID, "first call fresh")
	assert.Equal(t, "test-session-1", calls[1].SessionID, "second call attempted resume before failing")
	assert.Empty(t, calls[2].SessionID, "third call starts fresh after Reset()")
}

// failOnNthAgent wraps an Agent and returns an error on the failOn-th
// call (1-based). Other methods pass through.
type failOnNthAgent struct {
	inner  *agent.TestAgent
	failOn int
	calls  *int
}

func (a *failOnNthAgent) Name() string { return a.inner.Name() }
func (a *failOnNthAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	*a.calls++
	if *a.calls == a.failOn {
		// Still record the call so we can assert SessionID was passed in.
		_, _ = a.inner.Review(ctx, repoPath, commitSHA, prompt, io.Discard)
		return "", fmt.Errorf("simulated failure on call %d", a.failOn)
	}
	return a.inner.Review(ctx, repoPath, commitSHA, prompt, output)
}
func (a *failOnNthAgent) WithReasoning(level agent.ReasoningLevel) agent.Agent {
	return &failOnNthAgent{inner: a.inner.WithReasoning(level).(*agent.TestAgent), failOn: a.failOn, calls: a.calls}
}
func (a *failOnNthAgent) WithAgentic(agentic bool) agent.Agent {
	return a
}
func (a *failOnNthAgent) WithModel(model string) agent.Agent {
	return a
}
func (a *failOnNthAgent) WithSessionID(id string) agent.Agent {
	return &failOnNthAgent{inner: a.inner.WithSessionID(id).(*agent.TestAgent), failOn: a.failOn, calls: a.calls}
}
func (a *failOnNthAgent) CommandLine() string { return a.inner.CommandLine() }

// namedAgent overrides Name() to keep the registry happy after substitution.
type namedAgent struct {
	name  string
	inner agent.Agent
}

func (a *namedAgent) Name() string { return a.name }
func (a *namedAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	return a.inner.Review(ctx, repoPath, commitSHA, prompt, output)
}
func (a *namedAgent) WithReasoning(level agent.ReasoningLevel) agent.Agent {
	return &namedAgent{name: a.name, inner: a.inner.WithReasoning(level)}
}
func (a *namedAgent) WithAgentic(agentic bool) agent.Agent {
	return &namedAgent{name: a.name, inner: a.inner.WithAgentic(agentic)}
}
func (a *namedAgent) WithModel(model string) agent.Agent {
	return &namedAgent{name: a.name, inner: a.inner.WithModel(model)}
}
func (a *namedAgent) WithSessionID(id string) agent.Agent {
	if sa, ok := a.inner.(agent.SessionAgent); ok {
		return &namedAgent{name: a.name, inner: sa.WithSessionID(id)}
	}
	return a
}
func (a *namedAgent) CommandLine() string { return a.inner.CommandLine() }
```

- [ ] **Step 6: Add the non-session-agent warning test**

```go
func TestRunFix_ResumeWithNonSessionAgentWarnsOnce(t *testing.T) {
	// Register a stub agent that does NOT implement SessionAgent.
	stub := &nonSessionStub{}
	agent.Unregister("test")
	agent.Register(&namedAgent{name: "test", inner: stub})
	t.Cleanup(func() {
		agent.Unregister("test")
		agent.Register(agent.NewTestAgent())
	})

	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	_ = newMockDaemonBuilder(t).
		WithJobs([]storage.ReviewJob{
			{ID: 50, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
			{ID: 51, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		}).
		WithReview(50, "f").
		WithReview(51, "f").
		Build()

	out, _ := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		opts := fixOptions{agentName: "test", resume: true}
		base, err := resolveFixAgent(repo.Dir, opts)
		if err != nil {
			return err
		}
		tracker := &fixSessionTracker{
			enabled: true,
			base:    base,
			log:     func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) },
		}
		return runFix(cmd, []int64{50, 51}, opts, tracker)
	})

	count := strings.Count(out, "does not support session resume")
	assert.Equal(t, 1, count, "warning fires exactly once across multiple calls")
}

type nonSessionStub struct{ calls int }

func (a *nonSessionStub) Name() string { return "test" }
func (a *nonSessionStub) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	a.calls++
	if output != nil {
		_, _ = output.Write([]byte("plain output"))
	}
	return "ok", nil
}
func (a *nonSessionStub) WithReasoning(level agent.ReasoningLevel) agent.Agent { return a }
func (a *nonSessionStub) WithAgentic(agentic bool) agent.Agent                 { return a }
func (a *nonSessionStub) WithModel(model string) agent.Agent                   { return a }
func (a *nonSessionStub) CommandLine() string                                  { return "test" }
```

- [ ] **Step 7: Add the `--quiet` suppression test**

```go
func TestRunFix_QuietSuppressesResumeAndUnsupportedWarnings(t *testing.T) {
	stub := &nonSessionStub{}
	agent.Unregister("test")
	agent.Register(&namedAgent{name: "test", inner: stub})
	t.Cleanup(func() {
		agent.Unregister("test")
		agent.Register(agent.NewTestAgent())
	})

	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	_ = newMockDaemonBuilder(t).
		WithJobs([]storage.ReviewJob{
			{ID: 60, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
			{ID: 61, Status: storage.JobStatusDone, RepoPath: repo.Dir, GitRef: "abc123", Agent: "test"},
		}).
		WithReview(60, "f").
		WithReview(61, "f").
		Build()

	out, _ := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		opts := fixOptions{agentName: "test", quiet: true, resume: true}
		base, err := resolveFixAgent(repo.Dir, opts)
		if err != nil {
			return err
		}
		tracker := &fixSessionTracker{
			enabled: true,
			base:    base,
			quiet:   true,
			log:     func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) },
		}
		return runFix(cmd, []int64{60, 61}, opts, tracker)
	})

	assert.NotContains(t, out, "does not support session resume")
	assert.NotContains(t, out, "Resuming session")
}
```

- [ ] **Step 8: Run the new integration tests**

```bash
go test ./cmd/roborev/ -run "TestRunFix_|TestRunFixBatch_" -v
```

Expected: all PASS. If `WithJobs` is missing on the mock builder, add it as a small wrapper around the existing `/api/jobs` route — see `mockDaemonBuilder` in `cmd/roborev/` for how `WithReview` is implemented and follow the same pattern.

- [ ] **Step 9: Verify the existing `--batch` regression**

```bash
go test ./cmd/roborev/ -v
```

Expected: all existing `--batch` and default-mode tests still pass. Behavior is unchanged when `--resume` and `--batch-size` are both absent.

- [ ] **Step 10: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix_integration_test.go
git commit -m "$(cat <<'EOF'
test: integration tests for --batch-size and --resume

Adds:
- count-cap groups 5 jobs / batch-size 2 into 3 invocations
- --resume chains session ID across single-job calls
- --batch-size + --resume combined (6/3 = 2 invocations)
- cascade is broken on error: post-error call starts fresh
- non-session agent warns exactly once
- --quiet suppresses both Resuming and unsupported-agent warnings

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Final verification

- [ ] **Step 1: Run the full suite, vet, lint**

```bash
go fmt ./...
go vet ./...
go test ./...
make lint
```

Expected: all green.

- [ ] **Step 2: Manually exercise help text**

```bash
go run ./cmd/roborev fix --help
```

Verify the examples block reflects the new flags as specified in Task 4.

- [ ] **Step 3: Manual smoke test against a real repo (optional but recommended)**

```bash
# In a repo with at least 3 open done jobs, none yet closed:
go run ./cmd/roborev fix --batch-size 2 --resume --agent test --quiet
go run ./cmd/roborev fix --batch --agent test --batch-size 5  # should error
go run ./cmd/roborev fix --resume --no-resume --agent test     # should error
```

- [ ] **Step 4: No commit** — Task 10 is verification only.

---

## Self-Review Notes

This plan was self-reviewed against the spec before publication.

- **Spec coverage:** Every spec section maps to at least one task — flag surface (Tasks 4, 5), refactors (Tasks 1, 3), tracker (Task 6), wiring (Tasks 7, 8), error handling (Task 6 unit + Task 9 cascade), test agent enhancement (Task 2), unit tests (Tasks 3, 6), integration tests (Task 9), out-of-scope items deliberately not implemented.
- **Placeholder scan:** No "TBD"/"TODO"/"implement later" remain. The Task 7 Step 4 scaffold test is marked `t.Skip` with an explicit reason (the full version lives in Task 9) — that's a deliberate ordering, not a placeholder.
- **Type consistency:** `splitIntoBatches` signature is `splitIntoBatches(entries []batchEntry, opts batchSplitOptions)` everywhere; `runFixBatch` takes `batchSize int` consistently; `fixSessionTracker.NextAgent()` returns `(agent.Agent, bool)` everywhere it's called; `agent.NewSessionCaptureWriter` capitalization is consistent across producer (Task 1) and consumers (Tasks 7, 8).
