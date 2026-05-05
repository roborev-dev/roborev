# Agent Rate-Limit and Session-Cap Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a shared rate-limit classifier so the daemon worker keeps its current cooldown-then-failover behavior for Gemini/Codex quota errors and the foreground `roborev fix` loop aborts with a reset-time message instead of looping fruitlessly when the configured agent hits a quota or session cap.

**Architecture:** A new `internal/agentlimit` package owns the detection logic — pattern table, kind enum, reset parsing — with no I/O. The daemon worker calls it via a swappable `classify` field on `WorkerPool` (default `agentlimit.Classify`), and the CLI fix loop calls it via a new `classify` field on `fixOptions`. Tests inject stubs that return literal `agentlimit.Classification` values for deterministic outcomes.

**Tech Stack:** Go 1.x stdlib only (no new dependencies), testify (`require`/`assert`) for test assertions per project conventions.

**Spec:** `docs/superpowers/specs/2026-05-05-agent-rate-limit-handling-design.md`

---

## File Structure

**Create:**
- `internal/agentlimit/agentlimit.go` — `Kind`, `Classification`, `rule`, `defaultRules`, `Classify`, `classifyWithRules`, helper `ruleAppliesToAgent`
- `internal/agentlimit/parse.go` — `ParseResetDuration`, `ParseResetTime`, range-clamp helpers
- `internal/agentlimit/agentlimit_test.go` — table tests for `Classify` (nine production patterns + negatives), test for `classifyWithRules` with synthetic `KindSession` rule
- `internal/agentlimit/parse_test.go` — table tests for both parsers

**Modify:**
- `internal/daemon/worker.go` — add `classify` field on `WorkerPool`; replace `isQuotaError(...)` and `parseQuotaCooldown(...)` call sites at lines 792-796 with `wp.classify(...)`; add unmatched-error WARN log; delete the now-unused `isQuotaError` and `parseQuotaCooldown` helpers (and their tests in `worker_test.go`)
- `internal/daemon/worker_test.go` — drop `TestIsQuotaError`/`TestParseQuotaCooldown` (covered in agentlimit package); add `TestFailOrRetryInner_SessionLimitCoolsDownAndFailsOver` driven by injected classifier; add `TestFailOrRetryInner_QuotaPatternRegression` covering all nine substrings end-to-end
- `cmd/roborev/fix.go` — add `classify agentlimit.ClassifyFunc` and `nowFn func() time.Time` fields on `fixOptions` (defaults wired in `NewFixCmd`); add `agentLimitError` type with `Error()` formatter; in `fixSingleJob` and `runFixBatch`, after each `fixJobDirect` error, classify and either return the abort error (KindQuota/KindSession) or log a WARN (KindNone with non-empty error) before the existing per-job handling
- `cmd/roborev/fix_test.go` — add `TestFixSingleJobAbortsOnSessionLimit` and `TestRunFixBatchAbortsOnQuotaError` using a stub classifier injected via `fixOptions`

**Note on the daemon-vs-foreground distinction (load-bearing):** the daemon worker keeps its existing behavior for Gemini/Codex (cooldown → immediate failover-or-fail, no retries). The foreground fix loop is where the new abort behavior lives — it applies to KindQuota too, not just future KindSession, and that change is intentional (spec migration section).

---

## Task 1: Create `agentlimit` package skeleton with types

**Files:**
- Create: `internal/agentlimit/agentlimit.go`
- Test: `internal/agentlimit/agentlimit_test.go`

- [ ] **Step 1: Write failing test for type round-trip**

```go
// internal/agentlimit/agentlimit_test.go
package agentlimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassificationZeroValue(t *testing.T) {
	var c Classification
	assert := assert.New(t)
	assert.Equal(KindNone, c.Kind)
	assert.Equal("", c.Agent)
	assert.True(c.ResetAt.IsZero())
	assert.Equal(time.Duration(0), c.CooldownFor)
	assert.Equal("", c.Message)
}
```

- [ ] **Step 2: Run test, confirm compile failure**

Run: `go test ./internal/agentlimit/ -run TestClassificationZeroValue`
Expected: FAIL — `package agentlimit` does not exist.

- [ ] **Step 3: Create the package with types**

```go
// internal/agentlimit/agentlimit.go
// Package agentlimit classifies agent CLI error messages as rate-limit,
// quota, or session-cap signals so the daemon worker and the foreground
// roborev fix loop can react consistently. Pure logic — no I/O.
package agentlimit

import "time"

// Kind labels a classified agent error.
type Kind int

const (
	KindNone      Kind = iota // no rate-limit signal recognized
	KindTransient             // 429-style; retry locally, no cooldown
	KindQuota                 // hard quota exhaustion (Gemini/Codex today)
	KindSession               // session-level cap (e.g. Claude 5-hour)
)

// Classification is the result of inspecting an agent error.
type Classification struct {
	Kind        Kind
	Agent       string        // canonical agent name (caller resolves aliases)
	ResetAt     time.Time     // zero if not parseable from the message
	CooldownFor time.Duration // zero if not parseable; caller applies its own fallback
	Message     string        // raw error text (for logs / user display)
}

// ClassifyFunc is the function shape used by callers that want to inject
// a stub in tests.
type ClassifyFunc func(agent, errMsg string) Classification
```

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./internal/agentlimit/ -run TestClassificationZeroValue -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentlimit/agentlimit.go internal/agentlimit/agentlimit_test.go
git commit -m "feat(agentlimit): create package with Kind and Classification types"
```

---

## Task 2: Implement `ParseResetDuration`

**Files:**
- Create: `internal/agentlimit/parse.go`
- Test: `internal/agentlimit/parse_test.go`

- [ ] **Step 1: Write failing tests for relative-duration parsing**

```go
// internal/agentlimit/parse_test.go
package agentlimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseResetDuration(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want time.Duration
	}{
		{"none", "agent failed", 0},
		{"reset after seconds clamped to min", "quota will reset after 5s.", 1 * time.Minute},
		{"reset after minutes", "quota will reset after 48m20s.", 48*time.Minute + 20*time.Second},
		{"reset after hours", "reset after 2h13m.", 2*time.Hour + 13*time.Minute},
		{"reset after with trailing punct", "reset after 1h.. retrying", 1 * time.Hour},
		{"reset after invalid", "reset after notaduration", 0},
		{"reset after negative", "reset after -5m", 0},
		{"reset after huge clamped to max", "reset after 100h", 24 * time.Hour},
		{"case insensitive", "RESET AFTER 30m", 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ParseResetDuration(tc.msg))
		})
	}
}
```

- [ ] **Step 2: Run tests, confirm compile failure**

Run: `go test ./internal/agentlimit/ -run TestParseResetDuration`
Expected: FAIL — `ParseResetDuration` undefined.

- [ ] **Step 3: Implement `ParseResetDuration`**

This lifts the logic from `internal/daemon/worker.go:1015` (`parseQuotaCooldown`), with the difference that it returns `0` instead of a caller-supplied fallback when nothing parses.

```go
// internal/agentlimit/parse.go
package agentlimit

import (
	"strings"
	"time"
)

// minCooldown / maxCooldown clamp parsed durations to a sane range so a
// pathological message ("reset after 1ms" or "reset after 100h") does not
// produce a useless cooldown.
const (
	minCooldown = 1 * time.Minute
	maxCooldown = 24 * time.Hour
)

// ParseResetDuration extracts a Go-format duration from a "reset after
// <dur>" substring in errMsg (case-insensitive). Returns 0 if no such
// substring is present or the duration is unparseable. Clamps positive
// values to [minCooldown, maxCooldown].
func ParseResetDuration(errMsg string) time.Duration {
	lower := strings.ToLower(errMsg)
	idx := strings.Index(lower, "reset after ")
	if idx < 0 {
		return 0
	}
	rest := errMsg[idx+len("reset after "):]
	token := rest
	if sp := strings.IndexAny(rest, " \t\n,;)"); sp > 0 {
		token = rest[:sp]
	}
	token = strings.TrimRight(token, ".,;:)]}")
	d, err := time.ParseDuration(token)
	if err != nil || d <= 0 {
		return 0
	}
	if d < minCooldown {
		return minCooldown
	}
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/agentlimit/ -run TestParseResetDuration -v`
Expected: PASS for all nine cases.

- [ ] **Step 5: Commit**

```bash
git add internal/agentlimit/parse.go internal/agentlimit/parse_test.go
git commit -m "feat(agentlimit): port reset-duration parsing from worker.parseQuotaCooldown"
```

---

## Task 3: Implement `ParseResetTime`

**Files:**
- Modify: `internal/agentlimit/parse.go`
- Modify: `internal/agentlimit/parse_test.go`

This adds an absolute-time parser for messages that say "resets at 5:42 PM" / "try again at 17:42 UTC". No production rule uses it day 1 — Gemini and Codex emit relative durations — but the framework supports it so adding a Claude rule later is a one-line table change without parser work.

- [ ] **Step 1: Write failing tests for absolute-time parsing**

```go
// internal/agentlimit/parse_test.go — append below TestParseResetDuration
func TestParseResetTime(t *testing.T) {
	// Use a fixed "now" so same-day vs next-day rollover is deterministic.
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, loc) // noon UTC
	cases := []struct {
		name    string
		msg     string
		wantErr bool
		want    time.Time
	}{
		{"none", "agent failed", true, time.Time{}},
		{
			name: "resets at later today",
			msg:  "limit resets at 5:42 PM",
			want: time.Date(2026, 5, 5, 17, 42, 0, 0, loc),
		},
		{
			name: "try again at later today 24h",
			msg:  "try again at 17:42",
			want: time.Date(2026, 5, 5, 17, 42, 0, 0, loc),
		},
		{
			name: "resets at earlier today rolls to next day",
			msg:  "limit resets at 9:00 AM",
			want: time.Date(2026, 5, 6, 9, 0, 0, 0, loc),
		},
		{
			name: "case insensitive",
			msg:  "LIMIT RESETS AT 6:00 pm",
			want: time.Date(2026, 5, 5, 18, 0, 0, 0, loc),
		},
		{"unparseable token", "resets at moonrise", true, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseResetTimeAt(tc.msg, now)
			if tc.wantErr {
				assert.True(t, got.IsZero(), "expected zero time, got %v", got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
```

- [ ] **Step 2: Run tests, confirm compile failure**

Run: `go test ./internal/agentlimit/ -run TestParseResetTime`
Expected: FAIL — `parseResetTimeAt` undefined.

- [ ] **Step 3: Implement `ParseResetTime` and `parseResetTimeAt`**

Append to `internal/agentlimit/parse.go`:

```go
// ParseResetTime extracts an absolute reset time from messages like
// "resets at 5:42 PM" or "try again at 17:42". Interprets the parsed
// clock time in the local timezone. Returns the zero time.Time if no
// recognized phrase is present or the time is unparseable.
//
// If the parsed clock time is earlier than now-on-the-same-day, the
// returned time rolls forward by 24 hours so callers that compute
// "time until reset" never get a negative duration.
func ParseResetTime(errMsg string) time.Time {
	return parseResetTimeAt(errMsg, time.Now())
}

// parseResetTimeAt is ParseResetTime with an injectable clock for tests.
func parseResetTimeAt(errMsg string, now time.Time) time.Time {
	lower := strings.ToLower(errMsg)
	var idx int
	switch {
	case strings.Contains(lower, "resets at "):
		idx = strings.Index(lower, "resets at ") + len("resets at ")
	case strings.Contains(lower, "try again at "):
		idx = strings.Index(lower, "try again at ") + len("try again at ")
	default:
		return time.Time{}
	}
	// Consume up to the next non-time character. Allow digits, ':', and
	// AM/PM markers (with optional spaces before them).
	rest := errMsg[idx:]
	end := len(rest)
	for i, r := range rest {
		// Stop at sentence-ending punctuation or newline.
		if r == '.' || r == ',' || r == ';' || r == '\n' || r == ')' {
			end = i
			break
		}
	}
	token := strings.TrimSpace(rest[:end])

	formats := []string{
		"3:04 PM", "3:04 pm",
		"3:04PM", "3:04pm",
		"15:04",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, token); err == nil {
			candidate := time.Date(
				now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), 0, 0, now.Location(),
			)
			if candidate.Before(now) {
				candidate = candidate.Add(24 * time.Hour)
			}
			return candidate
		}
	}
	return time.Time{}
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/agentlimit/ -run TestParseResetTime -v`
Expected: PASS for all six cases.

- [ ] **Step 5: Commit**

```bash
git add internal/agentlimit/parse.go internal/agentlimit/parse_test.go
git commit -m "feat(agentlimit): add absolute reset-time parser with same-day rollover"
```

---

## Task 4: Implement `Classify` and `classifyWithRules` with the nine production patterns

**Files:**
- Modify: `internal/agentlimit/agentlimit.go`
- Modify: `internal/agentlimit/agentlimit_test.go`

- [ ] **Step 1: Write failing tests for production patterns + negatives**

Append to `internal/agentlimit/agentlimit_test.go`:

```go
func TestClassifyProductionPatterns(t *testing.T) {
	// All nine substrings from the original isQuotaError set must
	// produce KindQuota. This is the byte-for-byte regression test
	// for current Gemini and Codex detection.
	patterns := []string{
		"resource exhausted",
		"quota exceeded",
		"quota_exceeded",
		"quota exhausted",
		"quota_exhausted",
		"insufficient_quota",
		"exhausted your capacity",
		"capacity exhausted",
		"capacity_exhausted",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			cls := Classify("gemini", "agent failed: "+p+", retrying...")
			assert := assert.New(t)
			assert.Equal(KindQuota, cls.Kind, "expected KindQuota for %q", p)
			assert.Equal("gemini", cls.Agent)
			assert.Contains(cls.Message, p)
		})
	}
}

func TestClassifyExtractsCooldownDuration(t *testing.T) {
	cls := Classify(
		"gemini",
		"You have exhausted your capacity on this model. Your quota will reset after 48m20s.",
	)
	assert := assert.New(t)
	assert.Equal(KindQuota, cls.Kind)
	assert.Equal(48*time.Minute+20*time.Second, cls.CooldownFor)
	assert.True(cls.ResetAt.IsZero(), "no absolute time in this message")
}

func TestClassifyNegativeCases(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"empty", ""},
		{"unrelated error", "exit status 1: file not found"},
		{"benign mention of limit", "limit set to 100 in config"},
		{"benign rate limit (transient, no rule produces it day 1)", "429 rate limit, retrying"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := Classify("gemini", tc.msg)
			assert.Equal(t, KindNone, cls.Kind, "expected KindNone for %q", tc.msg)
		})
	}
}

func TestClassifyWithRulesIsolatesSyntheticPattern(t *testing.T) {
	// Synthetic rule used only inside this test — does not pollute defaultRules.
	syntheticRules := []rule{
		{Agents: []string{"*"}, Substring: "test-claude session limit", Kind: KindSession},
	}
	cls := classifyWithRules(
		"claude-code",
		"5-hour test-claude session limit reached",
		syntheticRules,
	)
	assert := assert.New(t)
	assert.Equal(KindSession, cls.Kind)
	assert.Equal("claude-code", cls.Agent)

	// Same message via the production Classify must not match —
	// the synthetic rule is not in defaultRules.
	cls2 := Classify("claude-code", "5-hour test-claude session limit reached")
	assert.Equal(KindNone, cls2.Kind, "synthetic rule must not leak into defaultRules")
}
```

- [ ] **Step 2: Run tests, confirm compile failure**

Run: `go test ./internal/agentlimit/`
Expected: FAIL — `Classify`, `classifyWithRules`, `rule` undefined.

- [ ] **Step 3: Implement `Classify`, `classifyWithRules`, and `defaultRules`**

Append to `internal/agentlimit/agentlimit.go`:

```go
import "strings"

// rule is one substring → kind mapping. The Agents slice restricts the
// rule to specific canonical agent names; "*" applies to any agent.
type rule struct {
	Agents    []string // canonical agent names; "*" = any
	Substring string   // case-insensitive substring match on the error message
	Kind      Kind
}

// defaultRules is the production rule table. The nine quota substrings
// are copied from the original isQuotaError set in
// internal/daemon/worker.go so detection for Gemini and Codex is
// byte-for-byte unchanged.
//
// Claude session-cap patterns are intentionally absent — see the design
// doc's "Detection without a captured Claude message" section. Once a
// real session-cap message is captured, a single KindSession rule plus
// a unit-test fixture is sufficient to enable Claude detection.
var defaultRules = []rule{
	{Agents: []string{"*"}, Substring: "resource exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota exceeded", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota_exceeded", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota_exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "insufficient_quota", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "exhausted your capacity", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "capacity exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "capacity_exhausted", Kind: KindQuota},
}

// Classify inspects an agent error message and returns a Classification
// describing whether (and how) the agent is rate-limited. The agent
// argument is the canonical agent name; the caller is responsible for
// resolving any aliases (e.g. "claude" → "claude-code") before calling.
//
// Returns Kind == KindNone when no rule matches.
func Classify(agent, errMsg string) Classification {
	return classifyWithRules(agent, errMsg, defaultRules)
}

// classifyWithRules is Classify with an explicit rule slice. Unexported;
// used inside the package's own tests so synthetic fixtures (e.g. a
// KindSession pattern) do not leak into defaultRules.
func classifyWithRules(agent, errMsg string, rules []rule) Classification {
	if errMsg == "" {
		return Classification{Kind: KindNone, Agent: agent, Message: errMsg}
	}
	lower := strings.ToLower(errMsg)
	for _, r := range rules {
		if !ruleAppliesToAgent(r, agent) {
			continue
		}
		if !strings.Contains(lower, r.Substring) {
			continue
		}
		return Classification{
			Kind:        r.Kind,
			Agent:       agent,
			ResetAt:     ParseResetTime(errMsg),
			CooldownFor: ParseResetDuration(errMsg),
			Message:     errMsg,
		}
	}
	return Classification{Kind: KindNone, Agent: agent, Message: errMsg}
}

func ruleAppliesToAgent(r rule, agent string) bool {
	for _, a := range r.Agents {
		if a == "*" || a == agent {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run all tests in the package, confirm pass**

Run: `go test ./internal/agentlimit/ -v`
Expected: PASS for all four test functions.

- [ ] **Step 5: Run full unit suite to confirm no regression**

Run: `go test ./...`
Expected: PASS (or only pre-existing failures unrelated to this change).

- [ ] **Step 6: Commit**

```bash
git add internal/agentlimit/agentlimit.go internal/agentlimit/agentlimit_test.go
git commit -m "feat(agentlimit): add Classify with nine production quota patterns"
```

---

## Task 5: Replace `isQuotaError` + `parseQuotaCooldown` in the daemon worker

**Files:**
- Modify: `internal/daemon/worker.go`
- Modify: `internal/daemon/worker_test.go`

The behavior change here is *zero* for any currently-supported agent. The plan is to add an injectable `classify` function on `WorkerPool`, route the existing call site through it, then delete the now-dead helpers and their unit tests.

- [ ] **Step 1: Add `classify` field on `WorkerPool`**

Modify `internal/daemon/worker.go` around the existing fields (the cooldowns block, line 46-48):

```go
// Add to imports:
//   "github.com/roborev-dev/roborev/internal/agentlimit"

// Add to the WorkerPool struct (alongside agentCooldowns):
	// classify is the rate-limit/quota classifier. Defaults to
	// agentlimit.Classify; tests substitute a stub via NewWorkerPool's
	// caller setting it after construction (test-only access).
	classify agentlimit.ClassifyFunc
```

In `NewWorkerPool` (line 59-74), set the default:

```go
return &WorkerPool{
	db:             db,
	cfgGetter:      cfgGetter,
	broadcaster:    broadcaster,
	errorLog:       errorLog,
	activityLog:    activityLog,
	numWorkers:     numWorkers,
	stopCh:         make(chan struct{}),
	readyCh:        make(chan struct{}),
	runningJobs:    make(map[int64]context.CancelFunc),
	pendingCancels: make(map[int64]bool),
	agentCooldowns: make(map[string]time.Time),
	outputBuffers:  NewOutputBuffer(512*1024, 4*1024*1024),
	classify:       agentlimit.Classify,
}
```

- [ ] **Step 2: Replace the call site in `failOrRetryInner`**

Modify `internal/daemon/worker.go:789-803` (the `failOrRetryInner` function's quota and context-window branches). Replace the existing quota-detection block:

Old:

```go
	// Quota errors skip retries entirely — cool down the agent and
	// attempt failover or fail with a quota-prefixed error.
	if agentError && isQuotaError(errorMsg) {
		dur := parseQuotaCooldown(errorMsg, defaultCooldown)
		wp.cooldownAgent(agentName, time.Now().Add(dur))
		log.Printf("[%s] Agent %s quota exhausted, cooldown %v",
			workerID, agentName, dur)
		wp.failoverOrFail(workerID, job, agentName, errorMsg)
		return
	}
```

New:

```go
	// Quota and session-limit errors skip retries entirely — cool down
	// the agent and attempt failover or fail. Behavior matches the
	// original isQuotaError branch; classification now lives in
	// internal/agentlimit so the CLI fix loop can share it.
	if agentError {
		cls := wp.classify(agent.CanonicalName(agentName), errorMsg)
		if cls.Kind == agentlimit.KindQuota || cls.Kind == agentlimit.KindSession {
			dur := defaultCooldown
			if cls.CooldownFor > 0 {
				dur = cls.CooldownFor
			}
			if !cls.ResetAt.IsZero() {
				if until := time.Until(cls.ResetAt); until > 0 {
					dur = until
				}
			}
			wp.cooldownAgent(agentName, time.Now().Add(dur))
			log.Printf("[%s] Agent %s quota exhausted, cooldown %v",
				workerID, agentName, dur)
			wp.failoverOrFail(workerID, job, agentName, errorMsg)
			return
		}
	}
```

The `isContextWindowError` branch immediately below stays unchanged — that's a separate concern.

- [ ] **Step 3: Delete the now-unused helpers**

Remove from `internal/daemon/worker.go`:
- The entire `isQuotaError` function (lines 971-993)
- The entire `parseQuotaCooldown` function (lines 1013-1039)

The constants `defaultCooldown`, `minCooldown`, and `maxCooldown` stay — `defaultCooldown` is still used by the new code at the call site, and `min`/`maxCooldown` were duplicated into `internal/agentlimit/parse.go` so the daemon-side ones are now unused only in `parseQuotaCooldown`. Since `parseQuotaCooldown` is gone, also remove `minCooldown` and `maxCooldown` from `worker.go` if they have no other references.

Verify by grep:

```bash
rg -n "minCooldown|maxCooldown" internal/daemon/
```

Expected: zero matches in `internal/daemon/`. If matches remain, leave the constants in place.

- [ ] **Step 4: Update existing unit tests in `worker_test.go`**

Delete the unit-level `TestIsQuotaError` (around line 946) and `TestParseQuotaCooldown` (around line 1014) blocks entirely — the same coverage is now in `internal/agentlimit/agentlimit_test.go` and `internal/agentlimit/parse_test.go`.

The integration-level tests `TestFailOrRetryInner_QuotaSkipsRetries`, `TestFailOrRetryInner_QuotaExhaustedVariant`, `TestFailOrRetryInner_NonQuotaStillRetries` (around lines 1190-1271) stay — they exercise the quota-skip-retries path, which is the exact behavior we are preserving. They will continue to pass via the default `agentlimit.Classify`.

- [ ] **Step 5: Run the daemon test suite, confirm pass**

Run: `go test ./internal/daemon/ -run "TestFailOrRetry" -v`
Expected: PASS — all `TestFailOrRetryInner_*` tests still pass via the default classifier.

Run: `go test ./internal/daemon/`
Expected: PASS overall (no regressions).

- [ ] **Step 6: Format and commit**

Run:
```bash
go fmt ./...
go vet ./...
```

Expected: clean. Then:

```bash
git add internal/daemon/worker.go internal/daemon/worker_test.go
git commit -m "refactor(daemon): route quota detection through internal/agentlimit"
```

---

## Task 6: Add a daemon-side test that exercises the injected classifier for KindSession

**Files:**
- Modify: `internal/daemon/worker_test.go`

This proves the `classify` injection works end-to-end against the existing worker test infrastructure. Without it, day 1 we have no positive coverage of the `KindSession` branch on the daemon side.

- [ ] **Step 1: Write failing test using the existing `workerTestContext` helper**

Append to `internal/daemon/worker_test.go`:

```go
func TestFailOrRetryInner_SessionLimitCoolsDownAndFailsOver(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJob()

	// Stub classifier: any error message containing the marker yields
	// KindSession with a 1-hour CooldownFor.
	tc.wp.classify = func(agent, msg string) agentlimit.Classification {
		if strings.Contains(msg, "MARKER-SESSION-LIMIT") {
			return agentlimit.Classification{
				Kind:        agentlimit.KindSession,
				Agent:       agent,
				CooldownFor: 1 * time.Hour,
				Message:     msg,
			}
		}
		return agentlimit.Classification{Kind: agentlimit.KindNone, Agent: agent, Message: msg}
	}

	tc.wp.failOrRetryAgent("worker-0", job, "test", "boom MARKER-SESSION-LIMIT")

	assert := assert.New(t)
	assert.True(tc.wp.isAgentCoolingDown("test"), "agent should be in cooldown")

	// retry_count should not have advanced — quota/session errors skip retries.
	got, err := tc.wp.db.GetJobRetryCount(job.ID)
	require.NoError(t, err)
	assert.Equal(0, got, "session-limit error must not consume a retry slot")
}
```

Add the imports at the top of `worker_test.go` if not already present:

```go
import (
	// ...existing...
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/agentlimit"
)
```

- [ ] **Step 2: Run test, confirm pass on first try (the wiring from Task 5 already supports this)**

Run: `go test ./internal/daemon/ -run TestFailOrRetryInner_SessionLimitCoolsDownAndFailsOver -v`
Expected: PASS.

If FAIL with "agent should be in cooldown": the classifier injection from Task 5 is not being honored; revisit `failOrRetryInner` to confirm `wp.classify(...)` is the call.

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/worker_test.go
git commit -m "test(daemon): cover session-limit cooldown via injected classifier"
```

---

## Task 7: Add unmatched-error WARN log on the daemon side

**Files:**
- Modify: `internal/daemon/worker.go`
- Modify: `internal/daemon/worker_test.go`

When the classifier returns `KindNone` for an agent error, log a single WARN line so the next time a Claude (or any other) session-cap fires we have a captured sample to add a rule for.

- [ ] **Step 1: Write failing test**

Append to `internal/daemon/worker_test.go`:

```go
func TestFailOrRetryInner_UnmatchedAgentErrorLogsWarn(t *testing.T) {
	tc := newWorkerTestContext(t, 1)

	// Capture log output by swapping the standard logger's writer.
	var buf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(origOutput) })

	job := tc.createAndClaimJob()
	tc.wp.classify = func(agent, msg string) agentlimit.Classification {
		return agentlimit.Classification{Kind: agentlimit.KindNone, Agent: agent, Message: msg}
	}

	tc.wp.failOrRetryAgent("worker-0", job, "test", "some brand new error wording from a future agent")

	logged := buf.String()
	assert := assert.New(t)
	assert.Contains(logged, "unclassified agent error", "expected WARN line for unmatched error")
	assert.Contains(logged, "test", "log line should include agent name")
	assert.Contains(logged, "some brand new error wording", "log line should include error preview")
}
```

Add imports if not already present:

```go
	"bytes"
	"log"
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/daemon/ -run TestFailOrRetryInner_UnmatchedAgentErrorLogsWarn -v`
Expected: FAIL — log output does not contain "unclassified agent error".

- [ ] **Step 3: Refactor `failOrRetryInner` to classify once and add the WARN log**

Replace the agent-error block from Task 5 with a single switch that
covers all classifier kinds. This avoids double-classifying and adds
the `KindNone` WARN log in the same site. `truncateRunes` already lives
at `internal/daemon/classifier.go:108`; flatten newlines inline so the
log line stays on one row, then call the existing helper.

```go
func (wp *WorkerPool) failOrRetryInner(workerID string, job *storage.ReviewJob, agentName string, errorMsg string, agentError bool) {
	if agentError {
		cls := wp.classify(agent.CanonicalName(agentName), errorMsg)
		switch cls.Kind {
		case agentlimit.KindQuota, agentlimit.KindSession:
			dur := defaultCooldown
			if cls.CooldownFor > 0 {
				dur = cls.CooldownFor
			}
			if !cls.ResetAt.IsZero() {
				if until := time.Until(cls.ResetAt); until > 0 {
					dur = until
				}
			}
			wp.cooldownAgent(agentName, time.Now().Add(dur))
			log.Printf("[%s] Agent %s quota exhausted, cooldown %v",
				workerID, agentName, dur)
			wp.failoverOrFail(workerID, job, agentName, errorMsg)
			return
		case agentlimit.KindNone:
			if errorMsg != "" {
				preview := strings.ReplaceAll(errorMsg, "\n", " ")
				preview = strings.ReplaceAll(preview, "\r", "")
				log.Printf("[%s] unclassified agent error from %s: %s",
					workerID, agentName, truncateRunes(preview, 200))
			}
			// fall through to context-window / retry handling
		case agentlimit.KindTransient:
			// fall through to retry handling
		}
	}
	if agentError && isContextWindowError(errorMsg) {
		wp.failoverOrFailNonRetryableAgent(workerID, job, agentName, errorMsg)
		return
	}
	// ...existing retry path unchanged from Task 5...
}
```

The test from Task 6 returns `KindSession` so it hits the cooldown
branch; the new test from this task returns `KindNone` and hits the
WARN-log branch.

- [ ] **Step 4: Run the new test and the regression suite**

Run: `go test ./internal/daemon/ -run TestFailOrRetryInner -v`
Expected: PASS for all `TestFailOrRetryInner_*` tests, including the new `_UnmatchedAgentErrorLogsWarn`.

- [ ] **Step 5: Format and commit**

```bash
go fmt ./...
go vet ./...
git add internal/daemon/worker.go internal/daemon/worker_test.go
git commit -m "feat(daemon): log WARN for unclassified agent errors"
```

---

## Task 8: Add classifier hook and abort-error type to the CLI fix loop

**Files:**
- Modify: `cmd/roborev/fix.go`

This task only adds plumbing. The next two tasks wire it into the actual loops with tests.

- [ ] **Step 1: Extend `fixOptions` and add the abort-error type**

Modify `cmd/roborev/fix.go` near the existing `fixOptions` struct (line 224):

```go
import "github.com/roborev-dev/roborev/internal/agentlimit"

type fixOptions struct {
	agentName   string
	model       string
	reasoning   string
	minSeverity string
	quiet       bool
	resume      bool

	// classify is the rate-limit classifier. Defaults to
	// agentlimit.Classify in NewFixCmd; tests inject a stub to drive
	// deterministic KindQuota / KindSession outcomes without depending
	// on real agent error wording.
	classify agentlimit.ClassifyFunc
}

// agentLimitError is returned by the fix loop when the configured agent
// hits a quota or session limit. The fix command surfaces it as the
// process exit error so users see the reset time and a hint to retry.
type agentLimitError struct {
	Classification agentlimit.Classification
}

func (e *agentLimitError) Error() string {
	return formatAgentLimitMessage(e.Classification, time.Now())
}

// formatAgentLimitMessage builds the user-facing abort message. Pulled
// out so tests can assert against it without depending on time.Now.
// The label ("quota" / "session limit" / "rate limit") is derived from
// cls.Kind so a Gemini/Codex KindQuota abort doesn't mis-report itself
// as a session-cap.
func formatAgentLimitMessage(cls agentlimit.Classification, now time.Time) string {
	label := agentLimitLabel(cls.Kind)
	var dur time.Duration
	switch {
	case !cls.ResetAt.IsZero():
		dur = cls.ResetAt.Sub(now)
	case cls.CooldownFor > 0:
		dur = cls.CooldownFor
	}
	switch {
	case dur > 0 && !cls.ResetAt.IsZero():
		return fmt.Sprintf(
			"agent %s hit a %s. Cooldown until %s (in %s). "+
				"Re-run after that, or pass --agent <other> to switch.",
			cls.Agent,
			label,
			cls.ResetAt.Format("3:04 PM"),
			dur.Round(time.Minute),
		)
	case dur > 0:
		return fmt.Sprintf(
			"agent %s hit a %s. Cooldown for ~%s. "+
				"Re-run after that, or pass --agent <other> to switch.",
			cls.Agent,
			label,
			dur.Round(time.Minute),
		)
	default:
		flat := strings.ReplaceAll(cls.Message, "\n", " ")
		return fmt.Sprintf(
			"agent %s hit a %s (unknown reset time). "+
				"Re-run later, or pass --agent <other> to switch. "+
				"Original error: %s",
			cls.Agent,
			label,
			truncateString(flat, 200),
		)
	}
}

func agentLimitLabel(k agentlimit.Kind) string {
	switch k {
	case agentlimit.KindSession:
		return "session limit"
	case agentlimit.KindQuota:
		return "quota limit"
	case agentlimit.KindTransient:
		return "rate limit"
	default:
		return "rate limit"
	}
}
```

`truncateString` already lives at `cmd/roborev/fix.go:796` — reuse it.
It does not flatten newlines, so callers that need a single-line preview
(this one and the WARN log paths in Tasks 9-10) flatten with
`strings.ReplaceAll(s, "\n", " ")` before calling.

- [ ] **Step 2: Default the classifier where `fixOptions` is built**

There is a single construction site for `fixOptions` in the production
flow, at `cmd/roborev/fix.go:137` (inside the `fix` cobra command's
`RunE`). Add `classify: agentlimit.Classify` to the struct literal:

```go
opts := fixOptions{
	agentName:   agentName,
	model:       model,
	reasoning:   reasoning,
	minSeverity: minSeverity,
	quiet:       quiet,
	resume:      resume,
	classify:    agentlimit.Classify,
}
```

Tests that build `fixOptions` directly are responsible for setting
`classify` (or leaving it nil and relying on the test to never reach
an error path). To make accidental nil dereferences obvious, add a
single nil-guard at the top of `fixSingleJob` and `runFixBatch`:

```go
if opts.classify == nil {
	opts.classify = agentlimit.Classify
}
```

This mirrors how `streamfmt` and other defaults are handled elsewhere in
the file and keeps behavior identical when callers do not set
`classify`.

- [ ] **Step 3: Format and confirm package builds**

```bash
go fmt ./...
go vet ./...
go build ./...
```

Expected: clean build (no usage of `agentLimitError` yet — only declarations).

- [ ] **Step 4: Commit**

```bash
git add cmd/roborev/fix.go
git commit -m "feat(fix): add classifier hook and agentLimitError plumbing"
```

---

## Task 9: Wire abort path into `fixSingleJob`

**Files:**
- Modify: `cmd/roborev/fix.go`
- Modify: `cmd/roborev/fix_test.go`

- [ ] **Step 1: Write failing test**

Append to `cmd/roborev/fix_test.go`:

This follows the `agent.FakeAgent` registration pattern used by
`TestFixSingleJobRecoversPostFixDaemonCalls` (`cmd/roborev/fix_test.go:565`)
to make the `test` agent return a synthetic error, and the `newMockServer`
+ `patchServerAddr` pattern used by `TestFixSingleJob` (line 529) for
daemon plumbing.

```go
func TestFixSingleJobAbortsOnSessionLimit(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"main.go": "package main\n"})

	originalAgent, err := agent.Get("test")
	require.NoError(t, err, "get test agent")
	agent.Register(&agent.FakeAgent{
		NameStr: "test",
		ReviewFn: func(_ context.Context, _, _, _ string, _ io.Writer) (string, error) {
			return "", errors.New("simulated claude session cap")
		},
	})
	t.Cleanup(func() { agent.Register(originalAgent) })

	ts, _ := newMockServer(t, MockServerOpts{
		ReviewOutput: "## Issues\n- Found issue",
		OnJobs: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusDone,
					Agent:  "test",
				}},
			})
		},
	})
	patchServerAddr(t, ts.URL)

	cmd, _ := newTestCmd(t)

	resetAt := time.Now().Add(2*time.Hour + 13*time.Minute)
	classifyCalls := 0
	opts := fixOptions{
		agentName: "test",
		reasoning: "fast",
		classify: func(_, msg string) agentlimit.Classification {
			classifyCalls++
			return agentlimit.Classification{
				Kind:    agentlimit.KindSession,
				Agent:   "claude-code",
				ResetAt: resetAt,
				Message: msg,
			}
		},
	}

	tracker := &fixSessionTracker{base: agent.NewTestAgent(), out: io.Discard}
	err = fixSingleJob(cmd, repo.Dir, 99, opts, tracker)
	require.Error(t, err, "expected abort error, got nil")

	var lim *agentLimitError
	require.ErrorAs(t, err, &lim, "expected agentLimitError, got %T: %v", err, err)

	assert := assert.New(t)
	assert.Equal(agentlimit.KindSession, lim.Classification.Kind)
	assert.Equal(1, classifyCalls, "classifier should be called exactly once")
	assert.Contains(err.Error(), "claude-code")
	assert.Contains(err.Error(), "session limit")
}
```

Add any missing imports to the top of `fix_test.go` (most are already
present — `errors` and `agentlimit` are likely the only new ones):

```go
import (
	"errors"

	"github.com/roborev-dev/roborev/internal/agentlimit"
)
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./cmd/roborev/ -run TestFixSingleJobAbortsOnSessionLimit -v`
Expected: FAIL — either compile error (no abort path yet) or assertion failure.

- [ ] **Step 3: Add the abort branch in `fixSingleJob`**

Modify `cmd/roborev/fix.go` — find the `result, err := fixJobDirect(...)` call inside `fixSingleJob` (around line 926). Replace the existing error block:

Old:

```go
	if err != nil {
		tracker.Reset()
		return err
	}
```

New:

```go
	if err != nil {
		tracker.Reset()
		cls := opts.classify(agent.CanonicalName(currentAgent.Name()), err.Error())
		switch cls.Kind {
		case agentlimit.KindQuota, agentlimit.KindSession:
			return &agentLimitError{Classification: cls}
		case agentlimit.KindNone:
			if err.Error() != "" && !opts.quiet {
				flat := strings.ReplaceAll(err.Error(), "\n", " ")
				cmd.PrintErrf(
					"warning: unclassified agent error from %s: %s\n",
					currentAgent.Name(),
					truncateString(flat, 200),
				)
			}
		}
		return err
	}
```

The `KindTransient` case falls through to the default `return err` — same as today. The `KindNone` warn-print is the CLI mirror of the daemon-side WARN log.

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./cmd/roborev/ -run TestFixSingleJobAbortsOnSessionLimit -v`
Expected: PASS.

- [ ] **Step 5: Run the broader fix suite to confirm no regression**

Run: `go test ./cmd/roborev/ -run TestFix -v`
Expected: PASS for all `TestFix*` tests.

- [ ] **Step 6: Commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix.go cmd/roborev/fix_test.go
git commit -m "feat(fix): abort fixSingleJob on KindQuota or KindSession"
```

---

## Task 10: Wire abort path into `runFixBatch`

**Files:**
- Modify: `cmd/roborev/fix.go`
- Modify: `cmd/roborev/fix_test.go`

The batch loop is the long-running shape that originally surfaced the bug — without an abort, the CLI prints warnings and continues for every queued job.

- [ ] **Step 1: Write failing test**

This follows the `newMockDaemonBuilder` pattern from
`TestFixBatchSkipsPassVerdict` (`cmd/roborev/fix_test.go:2353`). The
mock daemon serves two job IDs; the `FakeAgent` returns a synthetic
quota error; the test asserts the batch aborts after the first job
without calling the daemon for the second.

```go
func TestRunFixBatchAbortsOnQuotaError(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"main.go": "package main\n"})

	originalAgent, err := agent.Get("test")
	require.NoError(t, err, "get test agent")
	agentCalls := 0
	agent.Register(&agent.FakeAgent{
		NameStr: "test",
		ReviewFn: func(_ context.Context, _, _, _ string, _ io.Writer) (string, error) {
			agentCalls++
			return "", errors.New("agent failed: you have exhausted your capacity")
		},
	})
	t.Cleanup(func() { agent.Register(originalAgent) })

	var mu sync.Mutex
	var reviewedJobIDs []int64
	_ = newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			id := r.URL.Query().Get("id")
			switch id {
			case "10", "20":
				idNum := int64(10)
				if id == "20" {
					idNum = 20
				}
				writeJSON(w, map[string]any{
					"jobs": []storage.ReviewJob{{
						ID:     idNum,
						Status: storage.JobStatusDone,
						Agent:  "test",
					}},
				})
			default:
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
			}
		}).
		WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
			jobID := r.URL.Query().Get("job_id")
			mu.Lock()
			if jobID == "10" {
				reviewedJobIDs = append(reviewedJobIDs, 10)
			} else if jobID == "20" {
				reviewedJobIDs = append(reviewedJobIDs, 20)
			}
			mu.Unlock()
			writeJSON(w, storage.Review{
				JobID:  10,
				Output: "## Issues\n- Bug",
			})
		}).
		WithHandler("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		Build()

	classifyCalls := 0
	opts := fixOptions{
		agentName: "test",
		reasoning: "fast",
		classify: func(_, msg string) agentlimit.Classification {
			classifyCalls++
			return agentlimit.Classification{
				Kind:        agentlimit.KindQuota,
				Agent:       "gemini",
				CooldownFor: 30 * time.Minute,
				Message:     msg,
			}
		},
	}

	_, err = runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		// batchSize=1 forces one job per agent call. With batchSize=0,
		// runFixBatch may pack both jobs into a single batch, making
		// agentCalls=1 ambiguous between "aborted before second batch"
		// and "both jobs in one batch". batchSize=1 is unambiguous.
		return runFixBatch(
			cmd,
			[]int64{10, 20},
			"",
			false, false, false,
			1,
			opts,
			&fixSessionTracker{base: agent.NewTestAgent(), out: io.Discard},
		)
	})
	require.Error(t, err, "expected batch to abort with agentLimitError")

	var lim *agentLimitError
	require.ErrorAs(t, err, &lim, "expected agentLimitError, got %T: %v", err, err)

	assert := assert.New(t)
	assert.Equal(agentlimit.KindQuota, lim.Classification.Kind)
	assert.Equal(1, classifyCalls, "classifier should be called exactly once")
	// agentCalls is the load-bearing assertion: with batchSize=1 and two
	// jobs, two agent invocations would mean the loop continued past the
	// abort. Exactly one means the abort short-circuits the second batch.
	assert.Equal(1, agentCalls, "fix agent should be invoked exactly once before abort")
	assert.Contains(err.Error(), "quota limit", "Gemini KindQuota label should be 'quota limit', not 'session limit'")

	// Note: do NOT assert `reviewedJobIDs` does not contain job 20 —
	// runFixBatch prefetches every job's review at the start of the
	// function (before any agent call), so /api/review fires for all
	// queued jobs regardless of when the loop aborts. The agentCalls
	// counter above is the correct measure of abort behavior.
}
```

The exact handler keys (`"10"`, `"20"`) and the `reviewedJobIDs` shape
mirror `TestFixBatchSkipsPassVerdict`. If `runWithOutput`'s signature
differs from what's shown above, copy it verbatim from the nearest
existing batch test in the same file.

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./cmd/roborev/ -run TestRunFixBatchAbortsOnQuotaError -v`
Expected: FAIL.

- [ ] **Step 3: Add the abort branch in `runFixBatch`**

Modify `cmd/roborev/fix.go` — locate the `fixJobDirect` call inside `runFixBatch` (around line 1170) and the surrounding error handling. Apply the same pattern as Task 9: classify on error, abort on `KindQuota`/`KindSession`, warn on `KindNone`, fall through otherwise. Reuse the same `agentLimitError` type and `truncateString` helper.

The exact site:

```go
result, err := fixJobDirect(ctx, fixJobParams{
	RepoRoot: repoRoot,
	Agent:    currentAgent,
	Output:   capture,
}, prompt)
// ...flush + tracker bookkeeping unchanged...
if err != nil {
	cls := opts.classify(agent.CanonicalName(currentAgent.Name()), err.Error())
	switch cls.Kind {
	case agentlimit.KindQuota, agentlimit.KindSession:
		return &agentLimitError{Classification: cls}
	case agentlimit.KindNone:
		if err.Error() != "" && !opts.quiet {
			flat := strings.ReplaceAll(err.Error(), "\n", " ")
			cmd.PrintErrf(
				"warning: unclassified agent error from %s: %s\n",
				currentAgent.Name(),
				truncateString(flat, 200),
			)
		}
	}
	// existing per-job handling continues (warn-and-continue or return)
	// ...
}
```

Make sure the abort `return` happens *before* the existing per-job warn-and-continue logic so the second job is never attempted.

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./cmd/roborev/ -run TestRunFixBatchAbortsOnQuotaError -v`
Expected: PASS — classifier called exactly once, second job untouched.

- [ ] **Step 5: Run the full fix suite**

Run: `go test ./cmd/roborev/ -run TestFix -v`
Expected: PASS for all `TestFix*` tests, including the discovery-mode tests that exercise the warn-and-continue path with non-limit errors.

- [ ] **Step 6: Run the entire test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Format and commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/fix.go cmd/roborev/fix_test.go
git commit -m "feat(fix): abort runFixBatch on KindQuota or KindSession"
```

---

## Task 11: Final verification and lint

**Files:**
- (verification only)

- [ ] **Step 1: Confirm dead code is gone**

```bash
rg -n "isQuotaError|parseQuotaCooldown" .
```

Expected: zero matches outside of `docs/` and possibly unrelated history.

- [ ] **Step 2: Run lint and full tests**

```bash
make lint
go test ./...
```

Expected: clean lint, all tests pass.

- [ ] **Step 3: Read the diff start-to-end**

```bash
git log --oneline main..HEAD
git diff main...HEAD
```

Look for: leftover TODO comments, unused imports, dead helpers, inconsistent naming between `agentlimit.ClassifyFunc` declarations and call sites.

- [ ] **Step 4: Manual smoke (if a quota-exhausted agent is available)**

If a Gemini account in known-exhausted state is reachable, run:

```bash
roborev fix
```

Expected: the fix loop aborts after the first job with a message like

```
agent gemini hit a quota limit. Cooldown for ~30m. Re-run after that, or pass --agent <other> to switch.
```

If no exhausted agent is reachable, this step is informational only — coverage is in the unit tests.

---

## Spec coverage check (self-review)

| Spec section / requirement | Implementing task |
|---|---|
| `internal/agentlimit` package with `Kind`, `Classification`, `Classify`, `classifyWithRules` | Task 1, Task 4 |
| Day-1 rule table = nine `isQuotaError` substrings | Task 4 |
| `parseResetDuration` (lifted from `parseQuotaCooldown`) | Task 2 |
| `parseResetTime` (absolute time, same-day rollover) | Task 3 |
| Daemon worker: classifier replaces `isQuotaError`, behavior preserved | Task 5 |
| Daemon worker: classifier injection (test stub) | Task 5 (`classify` field), Task 6 (positive test) |
| Daemon worker: unmatched-error WARN log | Task 7 |
| `fixOptions.classify` hook | Task 8 |
| `agentLimitError` type | Task 8 |
| Abort in `fixSingleJob` on `KindQuota`/`KindSession` | Task 9 |
| Abort in `runFixBatch` on `KindQuota`/`KindSession` | Task 10 |
| CLI unmatched-error warn line | Task 9, Task 10 |
| No exported test helper (cross-package tests use struct literals) | Task 9, Task 10 (tests construct `Classification` literals) |
| Existing `TestFailOrRetryInner_QuotaSkipsRetries` and friends still pass | Task 5 (preserved by routing through default `agentlimit.Classify`) |

---

## Out of scope (per spec)

These are deliberately *not* in this plan:

- A production Claude session-cap rule. Adding it is a one-line table change in `defaultRules` plus a unit test fixture, taken in a follow-up PR once a real session-cap message is captured. The unmatched-error WARN log from Task 7 / Tasks 9-10 will surface a sample.
- `--auto-failover` flag for `roborev fix`.
- `roborev status` field showing in-cooldown agents.
- Persisting cooldown state across daemon restarts.
- Any new HTTP API surface.
