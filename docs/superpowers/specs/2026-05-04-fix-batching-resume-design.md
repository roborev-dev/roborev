# `roborev fix`: count-bounded batching and session resume

**Date:** 2026-05-04
**Branch:** `feat/fix-batching-resume`
**Status:** Approved (pre-implementation)

## Overview

Two related additions to `roborev fix`:

1. **Count-bounded batching.** Today `roborev fix` runs the agent once per
   open review (default) or packs everything into a single prompt up to
   `max_prompt_size` (`--batch`). Add a third mode, `--batch-size N`, that
   concatenates up to `N` reviews per agent invocation while still
   respecting the existing size cap. The default behavior is unchanged.
2. **Session resume across invocations.** Add `--resume` / `--no-resume`.
   When `--resume` is passed, the CLI captures the agent's session ID
   from the streaming JSONL output of one invocation and passes it to
   the next via `agent.SessionAgent.WithSessionID`. Sessions are reused
   only **within a single `roborev fix` run** — no persistence across
   invocations. Default off.

The two features are independently useful and can be combined:

- `roborev fix --batch-size 5` — five reviews per agent call, no resume.
- `roborev fix --resume` — one review per agent call, sessions chained.
- `roborev fix --batch-size 5 --resume` — both.

Underlying agent infrastructure already exists: `agent.SessionAgent`,
`agent.ExtractSessionID`, `agent.IsValidResumeSessionID`, and per-agent
`WithSessionID` implementations on Codex and Claude Code. The daemon
already uses these. The CLI fix path does not yet use them.

## CLI Surface

```
roborev fix [--batch-size N] [--batch] [--resume | --no-resume] ...
```

| Flag | Behavior |
|---|---|
| (no flag) | One review per agent call. Existing default. |
| `--batch` | Pack all open reviews into one prompt, capped only by `max_prompt_size`. Existing behavior. |
| `--batch-size N` | Cap each agent invocation to ≤ N reviews. Still bounded by `max_prompt_size`. New. |
| `--resume` | Capture session ID after each call; pass via `WithSessionID` to the next call. New. |
| `--no-resume` | Explicit opt-out. New. |

### Resolution rules

- `--batch-size 0` → error: `--batch-size must be >= 1`.
- `--batch-size N >= 1` → activates batching. `N == 1` is allowed and
  precise: it means **one review per invocation through the batch code
  path** (uses `buildBatchFixPrompt` with the `# Batch Fix Request`
  header and the `roborev fix --batch-size` response wording). It is not
  the same as the default no-flag mode, which uses `buildGenericFixPrompt`
  and the `roborev fix` response wording.
- `--batch` and `--batch-size` together → error:
  `--batch and --batch-size are mutually exclusive`.
- `--resume` and `--no-resume` together → error:
  `--resume and --no-resume are mutually exclusive`.

### Help text examples

```
roborev fix                            # 1 review per agent call (default)
roborev fix --batch-size 5             # Up to 5 reviews per agent call
roborev fix --batch-size 5 --resume    # 5 per call, session resumed across calls
roborev fix --resume                   # 1 per call, session resumed across calls
roborev fix --batch                    # Pack until max_prompt_size (existing)
```

## Internal flow

### Two paths kept separate

| Invocation | Path | Prompt builder | Response text |
|---|---|---|---|
| `roborev fix` (no batch flag) | `runFixOpen` → `fixSingleJob` | `buildGenericFixPrompt` (`# Fix Request`) | `Fix applied via \`roborev fix\` command (commit: …)` |
| `roborev fix --batch-size N` | `runFixBatch` with count cap | `buildBatchFixPrompt` (`# Batch Fix Request`) | `Fix applied via \`roborev fix --batch-size\` (commit: …)` |
| `roborev fix --batch` | `runFixBatch` with no count cap | `buildBatchFixPrompt` | `Fix applied via \`roborev fix --batch\` (commit: …)` (unchanged) |

`runFixOpen` continues to re-query for newly-arrived open jobs after
each pass. `runFixBatch` continues to **not** re-query; it processes
the batch list once. This asymmetry is preserved deliberately —
`--batch-size` is bounded batch mode, not open-ended discovery mode.

### Refactors

1. **`splitIntoBatches` accepts an options struct.** This avoids a
   four-int signature that's easy to misread once a count cap is
   added.

   ```go
   type batchSplitOptions struct {
       MaxSize     int    // bytes, equivalent to today's maxSize
       MaxCount    int    // 0 = unbounded
       MinSeverity string
   }

   func splitIntoBatches(entries []batchEntry, opts batchSplitOptions) [][]batchEntry
   ```

   Greedy packing terminates a batch when **either** `MaxSize` would be
   exceeded **or** `MaxCount > 0` and `len(current) == MaxCount`. A
   single oversize entry still gets its own batch (existing
   semantics).

2. **`runFixBatch` accepts a count cap.** The flag layer translates:
   `--batch` → `MaxCount: 0`, `--batch-size N` → `MaxCount: N`.

3. **`fixSingleJob` accepts the tracker as a parameter** instead of
   calling `resolveFixAgent` itself. New signature:

   ```go
   func fixSingleJob(
       cmd *cobra.Command, repoRoot string, jobID int64,
       opts fixOptions, tracker *fixSessionTracker,
   ) error
   ```

   The caller resolves the agent once (into `tracker.base`) so resume
   works uniformly across both paths. `runFixWithSeen` and `runFixOpen`
   gain the same `*fixSessionTracker` parameter and pass it through.

### Shared `fixSessionTracker`

```go
type fixSessionTracker struct {
    enabled bool          // --resume passed
    base    agent.Agent   // resolved once (already WithAgentic/WithReasoning/WithModel)
    last    string        // most recent valid captured session ID
    warned  bool          // log "agent X doesn't support resume" only once
    quiet   bool          // suppress progress logs and warnings under --quiet
    log     func(string)  // injected; cmd.Printf in CLI, t.Logf in tests
}

// NextAgent returns the agent to use for the next invocation and a
// resuming flag indicating whether this call is actually resumed.
// The flag is the source of truth for the "Resuming session ..." log
// — relying on agent identity equality (currentAgent != base) is
// brittle.
//
// Decision order, in this exact sequence:
//   1. If !enabled, return (base, false). No warning.
//   2. If base does not implement SessionAgent, emit the
//      "doesn't support resume" warning once (respecting --quiet) and
//      return (base, false). The warning fires on the first call so it
//      surfaces even when no session ID is ever captured.
//   3. If last == "", return (base, false). No warning.
//   4. Otherwise return (sa.WithSessionID(last), true).
func (t *fixSessionTracker) NextAgent() (agent.Agent, bool)

// Capture stores id if it is non-empty AND passes
// agent.IsValidResumeSessionID. Empty/invalid IDs are silently
// dropped so the "Resuming session …" log can never lie.
func (t *fixSessionTracker) Capture(id string)

// Reset clears last. Called on agent error so a stale session token
// does not poison every subsequent batch.
func (t *fixSessionTracker) Reset()
```

All invocation paths call `tracker.NextAgent()` and never hold their
own `fixAgent` variable. This keeps resume behavior uniform across
single-job and batch paths.

### Session capture wiring

Both paths set up the same writer chain per invocation:

```
agent.Review(...)
    ↓ writes raw JSONL bytes
agent.NewSessionCaptureWriter(underlying, nil)   ← scans for session_id
    ↓ passthrough, exact bytes
streamfmt.Formatter (or io.Discard if --quiet)
    ↓
stdout
```

The wrapper currently lives at `internal/daemon/session_capture.go`.
**Move it** to `internal/agent/session_writer.go` and export it as:

```go
func NewSessionCaptureWriter(dst io.Writer, onCapture func(string)) *SessionCaptureWriter

func (w *SessionCaptureWriter) Write(p []byte) (int, error)
func (w *SessionCaptureWriter) Flush()
func (w *SessionCaptureWriter) SessionID() string
```

This is a narrow export. The daemon already imports `internal/agent`, so
it can switch to `agent.NewSessionCaptureWriter` with no cycle. The CLI
gets the same writer without having to depend on the daemon package.

The capture writer is **always** wired in, including under `--quiet`:

```go
underlying := io.Discard
var fmtr *streamfmt.Formatter
if !opts.quiet {
    fmtr = streamfmt.New(cmd.OutOrStdout(), ...)
    underlying = fmtr
}
capture := agent.NewSessionCaptureWriter(underlying, nil)
```

After the agent returns, **flush in this order**:

```go
capture.Flush()           // first: complete session extraction
if fmtr != nil {
    fmtr.Flush()          // second: presentation cleanup
}
```

The capture wrapper buffers partial raw lines, so flushing it first
guarantees session extraction is complete before `capture.SessionID()`
is read.

## Data flow walk-through

Concrete example: `roborev fix --batch-size 5 --resume`, agent is
`claude-code`, 12 open jobs (IDs 100–111).

### Setup phase (once)

1. Flag parsing rejects `--batch + --batch-size`,
   `--resume + --no-resume`, and `--batch-size 0`.
2. `resolveFixAgent` runs once →
   `base = ClaudeAgent{...}.WithAgentic(true).WithReasoning(...).WithModel(...)`.
3. Build `tracker := &fixSessionTracker{enabled: true, base: base, quiet: opts.quiet, log: cmd.Printf}`.
4. Discover open jobs (12), fetch each job + review + comments, build
   12 `batchEntry` values (skip passes/non-`done` as today).
5. Call
   `splitIntoBatches(entries, batchSplitOptions{MaxSize: 250_000, MaxCount: 5, MinSeverity: ""})`.
   Result: 3 batches of 5/5/2.

### Per-batch loop (3 iterations)

For batch `i` of `n`:

1. `currentAgent, resuming := tracker.NextAgent()`. Iteration 1 returns
   `(base, false)` (no `last` captured yet); iterations 2–3 return
   `(base.(SessionAgent).WithSessionID(last), true)` provided no error
   has triggered `tracker.Reset()` in between.
2. Build prompt via `buildBatchFixPrompt(batch, minSev)`.
3. Print "=== Batch i/n (jobs A, B, C) ===" header and per-job findings.
4. If `resuming`, print `Resuming session <shortID>` (respects
   `--quiet`). Driving the log from the explicit boolean — not from
   agent identity equality — handles the post-`Reset()` case correctly:
   after a reset, the next call returns `(base, false)` and the resume
   log is suppressed.
5. Wire output chain: `capture` wraps `fmtr` (or `io.Discard`).
6. Run `currentAgent.Review(ctx, repoPath, "HEAD", prompt, capture)`.
7. After return:
   ```go
   capture.Flush()
   if fmtr != nil { fmtr.Flush() }
   if err != nil {
       tracker.Reset()
       cmd.Printf("Warning: error in batch %d: %v\n", i+1, err)
       continue
   }
   tracker.Capture(capture.SessionID())
   ```
8. Standard post-run logic: detect new commit, enqueue review for fix
   commit, add response, close all jobs in batch.

### Single-job path (`roborev fix --resume`, no `--batch-size`)

Same tracker, same wrapper. `runFixOpen` loop:

```go
for _, jobID := range newIDs {
    if err := fixSingleJob(cmd, repoRoot, jobID, opts, tracker); err != nil {
        // existing error handling
    }
}
```

`fixSingleJob` receives the tracker, calls `tracker.NextAgent()` per
call, wires the capture wrapper the same way, and calls
`tracker.Capture(capture.SessionID())` (or `tracker.Reset()` on error).

### Behavior note: HEAD movement between resumed calls

After batch 1, the agent has committed and HEAD has moved. Batch 2
resumes the session, so the agent's conversational memory still thinks
it's at the prior HEAD. In practice agents re-read files on demand, so
this is benign — but we call it out here because reviewers will ask.

## Error handling and edge cases

### Resume-call failure cascade

Without care, a dead session ID poisons every subsequent batch. We
can't reliably distinguish "session expired" from "transient network
error," so on **any** agent error we call `tracker.Reset()`. Next call
starts fresh. A wasted session is cheap (agent re-builds context); a
poisoned session is not (every batch fails).

### Edge cases

| Case | Behavior |
|---|---|
| `--resume`, agent doesn't implement `SessionAgent` | `tracker.NextAgent()` returns `(base, false)` and emits the warning on the **first** call: `Warning: agent X does not support session resume; running fresh sessions`. Respects `--quiet`. Subsequent calls remain silent. |
| `--resume`, stream emits no `session_id` event | `last` stays empty; next `NextAgent()` returns `(base, false)`. Silent. Tracker keeps trying on subsequent calls. |
| Resume call fails | `tracker.Reset()`; `Warning: error in batch …` printed; loop continues with next batch fresh. |
| Ctrl-C between batches | `last` is in-process memory; dies with the process. Next `roborev fix` invocation starts fresh. Matches "within one fix invocation" scope. |
| `--batch-size N >= len(jobs)` | One batch of all jobs **only if** the concatenated prompt also fits under `max_prompt_size`; otherwise size splitting still applies and produces multiple smaller batches. |
| `--batch-size N` with explicit job IDs | Discovery skipped; count cap applies to the explicit list. |
| Empty repo / unborn HEAD | `fixJobDirect` already handles this. Unchanged. |
| Severity filter + mixed task/review jobs | Existing rule preserved: `minSev` is suppressed if **any** entry is a task job. Resolved once before splitting; applies to all batches consistently. |
| `Capture(id)` with id failing `IsValidResumeSessionID` | Dropped at the tracker boundary, not at the agent boundary. Prevents misleading "Resuming session …" logs. |

### Logging (when `--resume` is on)

- First call: silent (`NextAgent()` returns `(base, false)` because
  no session ID has been captured yet — unless the agent doesn't
  support resume, see below).
- Calls where `NextAgent()` returns `(_, true)`: print
  `Resuming session <shortID>`. `shortID` truncates the ID to its first
  12 characters.
- Agent doesn't support resume: `NextAgent()` emits the warning on
  the **first** call (so users learn immediately, not only after the
  first session capture would have happened) and never repeats it.

Both lines respect `--quiet`, matching the existing fix.go convention.

## Testing strategy

### Test agent enhancement

`internal/agent/test_agent.go` gains:

- `SessionID string` field, `WithSessionID(id)` method.
- A per-instance counter so each fresh call (no incoming `SessionID`)
  emits a new unique ID — `test-session-1`, `test-session-2`, ... The
  counter resets when a new test agent instance is constructed, so
  tests that build their own agent get a clean numbering sequence.
- A resumed call (incoming `SessionID != ""`) **echoes the same ID** in
  its streaming output. This is the assertion convention: tests can
  prove batch 2 received `test-session-1`, batch 3 received
  `test-session-1` (preserved chain), and so on.
- The streaming output emits
  `{"type":"session","id":"<id>"}\n` as the first line so
  `agent.ExtractSessionID` picks it up.
- A recording slice exposed via `TestAgent.Calls()` storing every
  `(SessionID, prompt)` pair the agent saw. Tests assert against this.

### Unit tests

| Test | Lives in | What it covers |
|---|---|---|
| `splitIntoBatches` with options struct | `cmd/roborev/fix_test.go` | size-only cap, count-only cap, both active (boundary at `min(N, size-fit)`), single oversize entry isolated, empty input, `MaxCount == 0` means unbounded |
| `fixSessionTracker` | `cmd/roborev/fix_test.go` | first `NextAgent()` returns `(base, false)`; after valid `Capture("id")`, returns `(base.(SessionAgent).WithSessionID("id"), true)`; invalid/empty IDs dropped; `Reset()` clears `last` so next `NextAgent()` returns `(base, false)`; non-`SessionAgent` base + tracker enabled → warns on **first** call (even before any capture) and returns `(base, false)`; warning fires only once across many calls; tracker disabled → always returns `(base, false)` and never warns |
| `agent.NewSessionCaptureWriter` (moved) | `internal/agent/session_writer_test.go` | passthrough exact bytes, captures first `session_id`/`thread_id`/`type:session` event, ignores subsequent events, `Flush()` recovers trailing partial line, returns `""` if none seen |
| `IsValidResumeSessionID` integration | reused | verify tracker rejects invalid IDs at capture time |
| Flag mutual exclusion | `cmd/roborev/fix_test.go` | `--batch + --batch-size` errors; `--resume + --no-resume` errors; `--batch-size 0` errors; `--batch-size 1 --resume` accepted |

### Integration tests

| Test | Setup | Assertion |
|---|---|---|
| `--batch-size N` count cap | 5 open jobs, `--batch-size 2` | 3 agent invocations; batch job-IDs grouped 2/2/1; all 5 jobs closed; one fix commit per batch |
| `--resume` single-job | 3 open jobs, no `--batch-size`, `--resume`, test agent emits `test-session-1` on call 1 | call 2 saw `WithSessionID("test-session-1")` and echoed it; call 3 saw `WithSessionID("test-session-1")` and echoed it (chain preserved); all 3 closed |
| `--batch-size N --resume` combined | 6 jobs, `--batch-size 3`, `--resume` | 2 invocations; invocation 2 received the session ID captured in invocation 1; both batches close all jobs |
| Resume cascade broken on error | 6 jobs, `--batch-size 2`, `--resume`, test agent fails on call 2 | call 3 starts fresh (no `WithSessionID` on call 3); call 3 captures its own new ID |
| `--resume` with non-session agent | force agent that doesn't implement `SessionAgent` | warning printed once (captured via `cmd.OutOrStdout()` — fix.go writes warnings to stdout via `cmd.Printf`, matching the existing convention); behavior identical to no-`--resume` |
| `--quiet` suppression | run with `--quiet --resume` | "Resuming session …" not printed; "agent doesn't support resume" warning not printed; existing fix-flow output suppression unchanged |
| Existing `--batch` regression | 10 jobs, `--batch` only | identical to today: one batch (or size-bounded splits), `# Batch Fix Request` prompt, `Fix applied via roborev fix --batch` response |

### Out of scope for tests

- Real Codex/Claude resume against actual binaries — that's an
  integration-suite concern (`//go:build integration` or `acp`). The
  CLI flow is agent-agnostic given the `SessionAgent` interface.
- TUI rendering of session IDs — already tested elsewhere, unaffected
  by this change.

## Out of scope

- Re-query loop in `runFixBatch` / `--batch-size`. Asymmetry with
  `runFixOpen` is preserved deliberately.
- Persisting session IDs across `roborev fix` invocations. Easy
  follow-up via DB or `~/.roborev/`, but not in this change.
- Parallel/concurrent batches. Different feature; would require
  per-batch worktrees and a worker pool.
- Daemon-side fix-job batching. The daemon already captures sessions
  per job; batching daemon-side is a separate feature.

## Rough implementation order

1. Move `sessionCaptureWriter` from `internal/daemon/` to
   `internal/agent/` and re-wire the daemon's existing import.
   Mechanical refactor with no behavior change.
2. Add `WithSessionID`, recording, and counter-based fresh IDs to the
   `test` agent.
3. Refactor `splitIntoBatches` to take `batchSplitOptions`. Update
   existing call site.
4. Add `--batch-size N` flag and the mutual-exclusion error with
   `--batch`. Wire into `runFixBatch` via `MaxCount`.
5. Add `--resume` / `--no-resume` flags and the mutual-exclusion error.
6. Implement `fixSessionTracker`. Wire into both `runFixOpen` /
   `fixSingleJob` and `runFixBatch` so all paths derive the agent via
   `tracker.NextAgent()`.
7. Wire `agent.NewSessionCaptureWriter` into both paths' output chains
   with the correct flush order.
8. Tests in the order listed in "Testing strategy" above.

Each step is independently testable and reviewable.
