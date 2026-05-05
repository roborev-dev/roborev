# Agent Rate-Limit and Session-Cap Handling

## Problem

Long-running `roborev fix` sessions keep iterating fruitlessly when the
configured agent hits its session-level rate limit (e.g. Claude Code's
5-hour usage cap). Every subsequent job hits the same wall and the loop
prints warnings without aborting.

The daemon worker already has a quota-detection layer (`isQuotaError` in
`internal/daemon/worker.go`, plus per-agent in-process cooldown), but it
has two gaps:

1. The pattern set only catches Gemini-style ("exhausted your capacity")
   and Codex-style ("resource exhausted") wording. Claude's session-cap
   message is not recognized, so it falls through to generic
   transient-retry and then to a plain failure.
2. `roborev fix` runs the fix agent in-process via `fixJobDirect`
   (`cmd/roborev/fix.go:262`). It does not go through the daemon worker
   at all, so even a perfect daemon-side classifier would not help the
   foreground loop.

## Goals

- Detect agent-level rate-limit and session-cap failures consistently in
  both the daemon worker and the foreground `roborev fix` loop.
- For the foreground fix loop, abort the entire session immediately when
  the configured agent hits a session-cap, surfacing the reset time (or
  a conservative fallback message) so the user knows when to retry.
- Preserve the daemon's existing behavior for daemon-owned review jobs:
  cooldown the agent, retry, then fail over to the configured backup
  agent if one is set. CI flows benefit from best-effort completion.
- Keep the detection table conservative: high-confidence patterns only,
  one unit test per pattern, with logging for unmatched non-zero exits
  so new variants can be observed and added incrementally.

## Non-goals

- Auto-failover for foreground `roborev fix`. Strict abort is the default;
  if user demand surfaces, an `--auto-failover` flag or config setting can
  be added later.
- Cross-machine coordination of cooldowns (the existing in-memory cooldown
  is per-daemon-process; that is unchanged).
- Surfacing daemon cooldown state to the CLI through a new API. The
  classifier is shared via a Go package import, not via HTTP.

## Architecture

### New package: `internal/agentlimit`

A small, dependency-free package shared by the daemon worker and the CLI
fix loop. Pure classification logic plus duration parsing — no I/O, no
process state.

```go
package agentlimit

type Kind int

const (
    KindNone     Kind = iota // no rate-limit signal
    KindTransient            // 429-style; retry locally, no cooldown
    KindQuota                // hard quota exhaustion (existing behavior)
    KindSession              // session-level cap (e.g. Claude 5-hour)
)

type Classification struct {
    Kind        Kind
    Agent       string        // canonical agent name (alias-resolved by caller)
    ResetAt     time.Time     // zero if not parseable from the message
    CooldownFor time.Duration // fallback when ResetAt is zero
    Message     string        // raw error text (for logs / user display)
}

// Classify inspects an agent error message for rate-limit signatures.
// agent is the canonical agent name (caller resolves aliases). Returns
// KindNone when no signature matches.
func Classify(agent, errMsg string) Classification
```

The pattern table lives in the package as a slice of per-agent rules:

```go
type rule struct {
    Agents     []string         // agents this rule applies to ("*" = any)
    Pattern    *regexp.Regexp   // or substring (case-insensitive)
    Kind       Kind
    ParseReset func(match []string) (time.Time, time.Duration)
}
```

Day-1 rules — copied verbatim from the existing `isQuotaError` set so
behavior for Gemini and Codex is unchanged:

| Agent | Pattern (case-insensitive) | Kind | Reset parsing |
|-------|----------------------------|------|---------------|
| any | `resource exhausted` | Quota | (none) |
| any | `quota exhausted` / `quota_exhausted` | Quota | (none) |
| any | `exhausted your capacity` | Quota | `reset after <duration>` if present |
| any | `capacity exhausted` / `capacity_exhausted` | Quota | (none) |

The `Transient` kind exists in the API and is exercised in tests, but
no production rule produces it on day one. Adding a generic
`rate.?limit` pattern was considered and rejected: it overmatches benign
errors and the existing in-call retry layers (`runStreamingCLI` and the
agents' own retry logic) already handle short-lived 429s.

Claude-specific rules are deliberately omitted at launch — see
[Detection without a captured Claude message](#detection-without-a-captured-claude-message).

The duration parser currently inline in `worker.go` (the helper that
extracts a cooldown duration from the gemini "reset after Xs" wording)
moves into this package as `parseResetDuration(msg) time.Duration`, so
both consumers share it.

### Daemon worker (`internal/daemon/worker.go`) changes

The existing `isQuotaError` helper (line 974) and inline duration parsing
are replaced with calls to `agentlimit.Classify`. Behavior is preserved:

- `KindQuota` or `KindSession`: cooldown the canonical agent until
  `ResetAt` (or `now + CooldownFor`, defaulting to 30m if both are zero).
  Continue with normal retry; on retry exhaustion, fail over to the
  configured backup agent if one exists. This keeps CI / daemon-owned
  review flows best-effort, as today.
- `KindTransient`: do not cooldown; let normal retry handle it.
- `KindNone`: unchanged (generic failure path).

The `cooldownAgent`/`isAgentInCooldown` helpers stay where they are —
only the *trigger* moves to the shared classifier.

### CLI fix loop (`cmd/roborev/fix.go`) changes

`fixJobDirect` returns errors from `params.Agent.Review()` as today.
`runFixOpen` (line 456) and `runFixBatch` (line 1000) are the loop
drivers — both call `fixJobDirect` (directly or via `fixSingleJob`) and
process the result.

After each `fixJobDirect` error, the caller runs:

```go
cls := agentlimit.Classify(canonicalAgent(params.Agent.Name()), err.Error())
switch cls.Kind {
case agentlimit.KindSession, agentlimit.KindQuota:
    // Strict abort. No auto-failover for foreground fix.
    return newAgentLimitError(cls)
case agentlimit.KindTransient, agentlimit.KindNone:
    // Existing per-job error handling (warn-and-continue in discovery
    // mode, return error in explicit-IDs mode).
}
```

`newAgentLimitError` formats a user-visible message:

```
agent claude-code hit a session limit. Cooldown until 5:42 PM (in 2h 13m).
Re-run `roborev fix ...` after that, or pass --agent <other> to switch.
```

If `ResetAt` is zero, fall back to `CooldownFor` (or "an unknown reset
time; try again later" if that is also zero), and the suggestion to
override `--agent` stands.

The CLI exits non-zero with the standard `1` exit code — no new exit
code. Scripts that want to detect this case can grep for the message.

### Detection without a captured Claude message

We have not captured Claude Code's actual session-cap output yet
(checked `~/.roborev/errors.log`, no Claude-tagged limit messages).
Adding a guess-pattern risks both false negatives (wording differs
slightly from what we guessed) and false positives (a benign error
mentioning the word "limit" gets classified as a hard cap).

The framework ships without a Claude-specific rule. Two safeguards make
the next observed cap actionable:

1. **Unmatched non-zero exit logging.** When the daemon worker or CLI
   fix loop receives an agent error with `Classify().Kind == KindNone`,
   log a single WARN line containing the agent name and the first 200
   chars of the error message. This produces a recoverable sample the
   moment the next cap fires.
2. **Test-only mock pattern.** The unit tests include a synthetic
   "claude session limit" pattern fixture so the `KindSession` branch
   has full test coverage end-to-end (worker cooldown path and CLI
   abort path). Real Claude patterns get added to the production rule
   table once captured.

Once a real Claude message is observed, adding the rule is a one-line
table change plus a unit test fixture.

### Reset-time parsing

Two formats need to be supported in the shared parser:

- Relative duration: `reset after 48m20s`, `try again in 2h13m`,
  `retry after 5m`. Existing worker.go logic handles this for Gemini —
  it moves into `agentlimit.parseResetDuration` verbatim.
- Absolute time: `resets at 5:42 PM`, `try again at 17:42 UTC`. New
  parser, returns `time.Time` interpreted in the local timezone with a
  same-day or next-day disambiguation (next-day if the parsed time is
  already in the past).

If neither format matches, both `ResetAt` and `CooldownFor` are zero —
the caller (worker or CLI) applies its own default fallback (30m
cooldown for the daemon, "unknown reset time" message for the CLI).

## Data flow

```
Foreground:  roborev fix → fixJobDirect → Agent.Review → error
                                              │
                                              ▼
                                     agentlimit.Classify
                                              │
                                  ┌───────────┴───────────┐
                                  ▼                       ▼
                          KindSession/Quota         KindTransient/None
                                  │                       │
                                  ▼                       ▼
                          Abort fix loop with      Continue per existing
                          reset-time error          per-job handling

Daemon:      worker job → Agent.Review → error
                                  │
                                  ▼
                         agentlimit.Classify
                                  │
                       ┌──────────┴──────────┐
                       ▼                     ▼
              KindSession/Quota          KindTransient/None
                       │                     │
                       ▼                     ▼
              cooldownAgent(...)      Existing retry path
              + retry + failover
              (existing behavior)
```

## Testing strategy

### `internal/agentlimit` unit tests

Table-driven, one row per pattern. Each row asserts:

- The expected `Kind`.
- The expected `Agent` value.
- The expected `ResetAt` / `CooldownFor` for messages that include reset
  info, and zero values for those that do not.

Negative cases: messages that should *not* match (e.g. an unrelated
error containing the word "limit") map to `KindNone`.

### Worker integration test

A new test in `internal/daemon/worker_test.go` exercises the
end-to-end daemon path:

- Configure a worker pool with a mock agent that returns a session-limit
  error message on first call.
- Enqueue a job. Run a worker tick.
- Assert: the agent is now in cooldown, the cooldown expiry matches the
  parsed reset, and a subsequent job for the same agent is skipped per
  existing `(quota cooldown active)` logic.

This covers the daemon-side wiring without depending on Claude's exact
wording — the test pattern is registered as a fixture in the test file.

### CLI fix abort test

A new test in `cmd/roborev/fix_test.go` (or `fix_mock_test.go`):

- Use the existing `test` agent infrastructure to inject an error that
  classifies as `KindSession`.
- Run `runFixBatch` with two job IDs.
- Assert: the loop aborts after the first job, the returned error
  surfaces the parsed reset time, and the second job was *not*
  processed.

### Logging behavior

A small assertion in either the worker or CLI test confirms that an
unmatched non-zero exit (`KindNone`) emits a WARN with a truncated
error preview, so the unmatched-pattern surfacing path is covered.

## Migration / rollout

- The change is internal (no public API surface). No flag-gating, no
  config opt-in.
- `isQuotaError` becomes a thin wrapper over `agentlimit.Classify` if
  any tests still reference it directly; otherwise it is removed in the
  same PR.
- Existing behavior for Gemini and Codex is preserved bit-for-bit by
  copying their patterns verbatim into the new rule table — verified
  by a regression test that runs each known-failing message through
  the new classifier and checks it still returns `KindQuota`.

## Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Pattern matching is brittle; agents change wording. | Conservative patterns + unit tests + WARN log on unmatched non-zero exits so new variants surface and can be added one-by-one. |
| Misclassifying a benign error as `KindSession` aborts the user's fix loop unnecessarily. | Patterns require specific phrasing (e.g. "exhausted", "session limit"), not generic words like "limit" or "rate". Negative test cases lock this in. |
| Reset-time parsing produces a wrong `time.Time` (e.g. timezone mishandling). | Local-timezone interpretation with explicit same-day/next-day disambiguation, plus unit tests for boundary cases. |
| The CLI abort message is unactionable if neither `ResetAt` nor `CooldownFor` is parseable. | Fallback message includes the raw agent error text and the `--agent` override hint, so the user has both context and a way out. |

## Out of scope (potential follow-ups)

- `--auto-failover` flag for `roborev fix` to mirror daemon failover.
- A `roborev status` field showing in-cooldown agents and their
  expiries.
- Persisting cooldown state across daemon restarts (today it is purely
  in-memory).
- Adding rate-limit handling for the CI poller and `roborev refine`
  paths beyond what they inherit transitively from the daemon and the
  fix loop.
