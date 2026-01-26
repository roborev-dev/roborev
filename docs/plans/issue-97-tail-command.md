# Implementation Plan: Issue #97 - Tail Command for Streaming Agent Output

## Overview

This feature adds a `t` key binding in the TUI to view streaming output of running agents in real-time, with `x` to cancel the job. The implementation requires:

1. **Output capture infrastructure** in the daemon worker
2. **Output buffer** with memory limits and thread-safe access
3. **Output normalization** to convert agent-specific formats (especially Claude's stream-json) to readable text
4. **New API endpoint** to stream/fetch captured output
5. **TUI tail view** with scrolling and cancel support

---

## 1. Output Buffer Design

### Location: `internal/daemon/outputbuffer.go` (new file)

Create a thread-safe ring buffer for capturing agent output per job, with memory limits.

### Design:

```go
// OutputBuffer stores streaming output for running jobs with memory limits.
type OutputBuffer struct {
    mu         sync.RWMutex
    buffers    map[int64]*JobOutput // jobID -> output
    maxPerJob  int                  // max bytes per job (default: 512KB)
    maxTotal   int                  // max total bytes across all jobs (default: 4MB)
    totalBytes int                  // current total bytes
}

// JobOutput stores output for a single job
type JobOutput struct {
    lines      []OutputLine // Ring buffer of lines
    writeIdx   int          // Next write position
    count      int          // Lines in buffer (up to maxLines)
    totalBytes int          // Bytes used by this job
    startTime  time.Time    // When output capture started
}

// OutputLine represents a single line of normalized output
type OutputLine struct {
    Timestamp time.Time `json:"ts"`
    Text      string    `json:"text"`
    Type      string    `json:"type"` // "text", "tool", "thinking", "error"
}
```

### Key features:
- **Per-job limit**: 512KB default, configurable
- **Total limit**: 4MB default across all jobs
- **Ring buffer**: Oldest lines evicted when limit reached
- **Line-based**: Store by lines for efficient scrolling
- **Automatic cleanup**: Remove buffer when job completes/fails

---

## 2. Output Normalization Strategy

### Location: `internal/daemon/normalize.go` (new file)

Different agents produce output in different formats. Normalize to human-readable text.

### Claude Code (stream-json format)

The Claude agent outputs JSON like:
```json
{"type": "assistant", "message": {"content": "..."}}
{"type": "result", "result": "..."}
{"type": "tool_use", "name": "Read", ...}
```

**Normalization:**
- `assistant` messages → extract content as text
- `result` messages → extract result as text
- `tool_use` → `[Tool: Read]` indicator
- `tool_result` → `[Tool result: N bytes]` (abbreviated)

### OpenCode / Other Agents

Plain text with ANSI codes:
- Strip ANSI escape sequences
- Filter tool call JSON lines
- Pass through readable content

### Normalizer Registry

```go
var normalizers = map[string]OutputNormalizer{
    "claude-code": NormalizeClaudeOutput,
    "opencode":    NormalizeOpenCodeOutput,
    // Default for others: strip ANSI, pass through
}
```

---

## 3. Worker Integration

### Location: `internal/daemon/worker.go`

### Changes:
1. Add `outputBuffers *OutputBuffer` to WorkerPool struct
2. Initialize in NewWorkerPool with memory limits
3. In `processJob()` at line 320, create output writer and pass to `a.Review()`:

```go
// Create output writer for tail command
outputWriter := wp.outputBuffers.Writer(job.ID, agentName)
defer wp.outputBuffers.CloseJob(job.ID)

output, err := a.Review(ctx, job.RepoPath, job.GitRef, reviewPrompt, outputWriter)
```

4. Add `GetJobOutput(jobID)` method to expose output for API

---

## 4. API Endpoint Design

### Location: `internal/daemon/server.go`

### New endpoint: `GET /api/job/output?job_id=123`

**Query Parameters:**
- `job_id` (required): Job ID to tail
- `stream` (optional): "1" for SSE streaming

**Non-streaming response:**
```json
{
    "job_id": 123,
    "status": "running",
    "lines": [
        {"ts": "2024-01-25T10:30:00Z", "text": "Analyzing code...", "type": "text"},
        {"ts": "2024-01-25T10:30:01Z", "text": "[Tool: Read]", "type": "tool"}
    ],
    "has_more": true
}
```

**Streaming response (newline-delimited JSON):**
```json
{"type": "line", "ts": "...", "text": "...", "line_type": "text"}
{"type": "complete", "status": "done"}
```

---

## 5. TUI Tail View Implementation

### Location: `cmd/roborev/tui.go`

### New view type:
```go
const (
    // ... existing views ...
    tuiViewTail  // NEW
)
```

### New model fields:
```go
tailJobID      int64        // Job being tailed
tailLines      []tailLine   // Buffer of output lines
tailScroll     int          // Scroll position
tailStreaming  bool         // True if actively streaming
tailFromView   tuiView      // View to return to
```

### Key bindings:
- `t` on running job → enter tail view
- `x` in tail view → cancel job
- `↑/↓/j/k` → scroll
- `pgup/pgdn` → page scroll
- `g/G` → top/bottom
- `q/esc` → return to queue

### Rendering:
- Title showing job ID and streaming status
- Timestamped output lines with type indicators
- Status line showing position and live/complete state
- Help bar with available keys

---

## 6. Thread Safety Considerations

- **OutputBuffer**: RWMutex for buffer map, per-job mutex for fine-grained locking
- **Worker**: Output writer created per-job, single goroutine owner
- **API**: Channel-based subscription for streaming
- **TUI**: bubbletea handles all UI on single goroutine, async HTTP via tea.Cmd

---

## 7. File Changes Summary

### New Files
1. `internal/daemon/outputbuffer.go` - Buffer implementation
2. `internal/daemon/normalize.go` - Output normalizers
3. `internal/daemon/outputbuffer_test.go` - Buffer tests
4. `internal/daemon/normalize_test.go` - Normalizer tests

### Modified Files
5. `internal/daemon/worker.go` - Add output capture
6. `internal/daemon/server.go` - Add API endpoint
7. `cmd/roborev/tui.go` - Add tail view

---

## 8. Implementation Order

### Phase 1: Output Buffer
- Implement core buffer with memory limits
- Unit tests for buffer operations

### Phase 2: Normalization
- Implement normalizers for Claude, OpenCode, generic
- Unit tests for parsing edge cases

### Phase 3: Worker Integration
- Wire up output capture in processJob
- Test with real agents

### Phase 4: API Endpoint
- Implement polling mode first
- Add streaming mode
- Integration tests

### Phase 5: TUI View
- Implement basic view with polling
- Add scrolling and navigation
- Polish UI

---

## 9. Testing Strategy

### Unit Tests
- OutputBuffer: append, eviction, subscribe, memory limits
- Normalizers: Claude stream-json parsing, ANSI stripping

### Integration Tests
- API endpoint: polling and streaming modes
- Worker: output capture during real agent execution

### Manual Testing
- Test with Claude Code (stream-json)
- Test with OpenCode (plain text + ANSI)
- Test memory limits with large output
- Test cancel during tail
