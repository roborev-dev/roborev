# Auto Design Review — Design Spec

Date: 2026-04-17

## Motivation

Design reviews today require an explicit opt-in: the user runs `roborev review --type design`, or lists `"design"` in the `[ci].review_types` config so every PR gets one. Neither extreme fits most workflows. Blanket on-every-PR design reviews are expensive and noisy for trivial changes; fully manual reviews are easy to forget, especially for schema changes and new packages where a design review is most valuable.

This spec proposes an opt-in automatic mode that decides per-commit whether a design review is warranted, using fast path/size/message heuristics first and a lightweight classifier agent as fallback for ambiguous cases.

## Goals

- Per-commit decision that runs in both the daemon's enqueue path (local post-commit hook / `roborev review`) and the CI poller.
- Heuristic short-circuits for obvious cases (trivial diffs, doc-only changes, schema files, new packages) so most commits skip the classifier.
- Classifier agent used only for ambiguous middle cases.
- User-visible outcome: design reviews that were skipped appear in the queue as `skipped` job rows with a reason, so the decision is auditable and doesn't look like a daemon bug.
- Opt-in via config; existing explicit paths (`--type design`, `"design"` in `review_types`) are preserved unchanged.

## Success criteria

- **No duplicate auto-design outcomes per commit.** Even under concurrent auto-design producers (CI poller + local hook firing for the same commit), at most one auto-design-tagged row exists per `(repo, commit, review_type=design)`. Explicit (source=NULL) design reviews from `--type design` or user reruns can coexist with the auto-design row.
- **No added latency from the heuristic engine.** The synchronous work during `/api/enqueue` is heuristic evaluation + (in the ambiguous case) a single DB insert for the classify row; classifier agent calls run asynchronously in worker jobs. Target: `autotype.Classify` itself adds < 25 ms per call in the heuristic-trigger / heuristic-skip / ambiguous-fallthrough paths (benchmarked locally in `internal/review/autotype/bench_test.go`). The broader enqueue path also performs git metadata collection (`git.GetCommitInfo` / `GetFilesChanged` / `GetDiff`) and a DB INSERT — those costs are already present in the existing review-enqueue path and are not re-benchmarked here.
- **CI batches complete correctly when they include auto-skipped design outcomes.** A PR batch containing a `skipped` design row must still be recognized as terminal by `storage.GetStaleBatches`, `ReconcileBatch`, and the sync query, and must be synthesized into the PR comment exactly once.
- **Classifier hit rate is observable.** `GET /api/status` exposes an `auto_design` subobject with counts of `triggered_heuristic`, `skipped_heuristic`, `triggered_classifier`, `skipped_classifier`, and `classifier_failed` since daemon start, so we can tune heuristics later. (See §Observability for the Go struct.)
- **Opt-in is truly opt-in.** With `enabled = false` (the default), none of the new logic is exercised — no extra queries, no new job rows, no config-load warnings. Tests explicitly assert this.
- **Repo config can disable a globally-enabled default.** `enabled` is a tri-state (`*bool`) at the repo layer so a repo can set `enabled = false` to opt out of a globally-enabled policy.

## Non-goals

- Replacing the existing `--type design` flag or the `review_types` CI matrix. Those remain the way to force a design review unconditionally.
- Auto-deciding other review types (security, refine, fix). Only design is in scope.
- Any change to the standard review flow. The standard review always runs; auto mode only decides whether to *add* a design review.
- Training a custom classifier, fine-tuning, or running any model outside the existing agent framework.

## Architecture

### New package

`internal/review/autotype/` — single source of truth for the decision.

```go
type Method string

const (
    MethodHeuristic  Method = "heuristic"
    MethodClassifier Method = "classifier"
)

type Decision struct {
    Run    bool
    Reason string
    Method Method
}

type Input struct {
    RepoPath     string
    GitRef       string
    Diff         string
    Message      string
    ChangedFiles []string
}

type Heuristics struct {
    MinDiffLines           int
    LargeDiffLines         int
    LargeFileCount         int
    TriggerPaths           []string
    SkipPaths              []string
    TriggerMessagePatterns []string
    SkipMessagePatterns    []string
}

type Classifier interface {
    Decide(ctx context.Context, in Input) (yes bool, reason string, err error)
}

func Classify(ctx context.Context, in Input, h Heuristics, cls Classifier) (Decision, error)
```

`Classifier` is a narrow interface so agent invocation can be stubbed in tests without a live worker. The `autotype` package depends only on the Go standard library and `doublestar` — it does not import `internal/config`; callers resolve config to `Heuristics` first.

### Call sites

- `internal/daemon/server.go` enqueue handler — after the standard review is enqueued, if `auto_design_review.enabled` resolves to true for the repo, run `autotype.Classify`. The heuristic part runs synchronously; ambiguous results enqueue a `classify` job.
- `internal/daemon/ci_poller.go` — on each poll tick, when auto mode is enabled and `"design"` is not already in `review_types`, same path.

### New job type: `classify`

When heuristics are ambiguous, the daemon enqueues a `classify` job (short prompt, fast reasoning). When the worker completes the classify job, it converts the classify row **in place** via an UPDATE — it does NOT insert a separate row. The classify row already occupies the auto-design dedup slot (`source='auto_design'`, `review_type='design'`), so any separate INSERT would hit `ON CONFLICT DO NOTHING` and silently disappear. The two outcomes:

- `{"design_review": true, ...}` → UPDATE the classify row: `job_type='review'`, `status='queued'`, `worker_id=NULL`, `started_at=NULL`, `error=NULL`. A worker claims it and runs the design review as if it had been enqueued directly.
- `{"design_review": false, ...}` → UPDATE the classify row: `job_type='review'`, `status='skipped'`, `skip_reason` populated. Terminal.

The UPDATE is scoped to the active execution attempt (`status='running' AND worker_id=?`) so stale workers whose classify jobs were canceled, reclaimed, or retried cannot resurrect them.

Keeps the enqueue endpoint snappy (no synchronous agent call on the hot path) and reuses the worker pool's retry/failover/cooldown machinery.

### Skipped as a first-class terminal state

`skipped` is a new terminal state. The broader job model already recognizes `'applied'` and `'rebased'` as terminals alongside `'done'/'failed'/'canceled'` (see `internal/storage/models.go`), but the CI batch + sync queries were written before those existed and hard-code their own narrower `'done'/'failed'/'canceled'` set. Those specific queries become wrong the moment a `skipped` design row enters a batch. We must update each one. Line numbers drift — implementers should grep for the literal string `status NOT IN` / `status IN` across `internal/storage/` and `internal/daemon/` to catch shifts.

| Call site | Current filter | Fix |
|---|---|---|
| `internal/storage/ci.go` — `GetStaleBatches` | `j.status NOT IN ('done','failed','canceled')` | Add `'skipped'` — batches whose only remaining job is a skipped design row are terminal |
| `internal/storage/ci.go` — sibling incomplete-batch-jobs query (near `GetStaleBatches`) | `j.status NOT IN ('done','failed','canceled')` (same clause, different function) | Add `'skipped'` |
| `internal/storage/ci.go` — `HasMeaningfulBatchResult` | `j.status IN ('done','failed')` | Add `'skipped'` — a skipped auto-design row counts as a "meaningful terminal outcome" worth posting |
| `internal/storage/ci.go` — `GetExpiredBatches` (two clauses) | `j.status IN ('done','failed')` and `j.status NOT IN ('done','failed','canceled')` | Add `'skipped'` to both |
| `internal/storage/ci.go` — `ReconcileBatch` count | `SUM(CASE WHEN j.status = 'done' THEN 1 ELSE 0 END)` + `status IN ('failed','canceled')` count | Count `'skipped'` toward completed; not toward failed |
| `internal/storage/sync.go` — syncable-jobs query | `j.status IN ('done','failed','canceled')` | Include `'skipped'` so skipped rows sync to Postgres |
| `internal/storage/jobs.go` — `RerunJob` UPDATE | `WHERE id = ? AND status IN ('done','failed','canceled')` | Include `'skipped'` so users can rerun a skipped design row. Rerun moves `skipped → queued` in place (see §Rerun semantics) |
| `internal/daemon/ci_poller.go` — `reconcileStaleBatches` | No direct filter; relies on `GetStaleBatches` + `ReconcileBatch` | Fixed transitively |
| CI synthesis / PR comment (`internal/review/synthesis.go` + `internal/daemon/ci_poller.go::postBatchResults`) | Renders per-job verdicts; expects non-empty output | Render skipped rows as a distinct category (e.g. "auto-skipped: `<reason>`") so synthesis doesn't fail on an empty verdict |

Workers never claim `skipped` jobs — they are terminal from creation. The TUI renders them dimmed, grouped alongside the commit's other review rows. The implementation plan dedicates a stage to terminal-state plumbing before the enqueue/CI producer paths are wired, to avoid shipping a half-integrated state.

**Daemon status summaries** (`/api/status`, `roborev status`, `GetJobCounts`) gain a `skipped` count alongside the existing queued/running/done/failed/canceled/applied/rebased counts. Without this, skipped auto-design outcomes would be invisible in aggregate views — only the per-commit queue would show them. The implementation plan adds this as part of the storage terminal-state stage.

## Decision flow

```
User commits (or CI poll tick)
         │
         ▼
Standard review enqueued (unchanged)
         │
         ▼
 auto_design_review.enabled?
   │           │
  no          yes
   │           │
   ▼           ▼
done      Run heuristics
             │
   ┌─────────┼─────────┐
   ▼         ▼         ▼
 SKIP     TRIGGER   AMBIGUOUS
   │         │         │
   ▼         ▼         ▼
insert   insert     insert
skipped  auto_design  classify
design    design       job
row       job          │
(NEW)     (NEW)        ▼
                    worker runs
                    classifier
                        │
                ┌───────┴───────┐
                ▼               ▼
            verdict=yes     verdict=no or failure
                │               │
                ▼               ▼
           UPDATE          UPDATE
           classify row    classify row
           → job_type      → status=skipped
             =review,      → skip_reason
             status        (same row — no
             =queued        separate INSERT)
           (same row)
```

The two new-INSERT paths (heuristic SKIP / TRIGGER) create auto-design rows directly; the AMBIGUOUS path inserts a classify row whose eventual conversion is an UPDATE (not another INSERT) so it doesn't collide with the auto-design unique index.

### Idempotency (atomic, not read-then-write)

A read-then-insert pattern (`SELECT COUNT(*) ... INSERT`) is racy — two concurrent producers can both observe "no row yet" and both insert. We need atomicity at the database level.

**Mechanism:** a new column `source` on `review_jobs` tags rows produced by the auto-design path. A partial unique index covers only those rows, keyed by `(repo_id, commit_id, review_type)`. Every auto-design insert uses `INSERT ... ON CONFLICT DO NOTHING` against this index. If a second producer races, its insert is a no-op; it sees zero rows affected and returns without erroring.

Rows created by **explicit** paths (`roborev review --type design`, `"design"` in `review_types`) leave `source` NULL, so they are outside the unique index's scope and never collide with auto-generated rows or with each other. This preserves the spec's invariants:

- `--type design` always enqueues, even if an auto-generated `skipped` or `done` design row exists.
- Rerunning a `skipped` design row UPDATEs it in place (`status: skipped → queued`) via the existing `RerunJob` path. `source` is preserved (still `auto_design`); the auto-design dedup slot remains occupied by the same row across the rerun. See §Rerun semantics.
- Two concurrent auto-design producers deduplicate against each other via the index.

```sql
-- SQLite
-- Column: NULL for explicit/user rows, 'auto_design' for auto-dispatched rows.
ALTER TABLE review_jobs ADD COLUMN source TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup
ON review_jobs(repo_id, commit_id, review_type)
WHERE source = 'auto_design';

-- Parallel index for commit-less rows (dirty/range).
CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup_ref
ON review_jobs(repo_id, git_ref, review_type)
WHERE source = 'auto_design' AND commit_id IS NULL;
```

**What writes `source = 'auto_design'`:**
- The enqueue handler's auto-design heuristic short-circuits (trigger → `status='queued'` design row; skip → `status='skipped'` design row).
- The enqueue handler / CI poller's ambiguous path inserts a single classify row with `source='auto_design'`; the classify worker later converts that same row in place (UPDATE) into a queued design review or a skipped design row.
- The CI poller's auto-design path (same semantics).

(No code path inserts a second auto-design row "following up" on a classify — the dedup index would reject it.)

**What leaves `source` NULL:**
- `roborev review --type design` (CLI / hook).
- CI with `"design"` in `review_types`.

(Reruns of existing rows do not change `source`. An auto-design row stays `source = 'auto_design'` across reruns; an explicit row stays `source = NULL` across reruns.)

The `HasAutoDesignSlotForCommit` query is still useful as a fast check (and is limited to `source = 'auto_design'` rows so it matches the dedup index's scope), but it is no longer the dedup mechanism — it's a performance shortcut. The unique index is authoritative.

### Interaction with existing controls

- `roborev review --type design` on the CLI continues to enqueue a design review unconditionally — the auto path is only triggered for *implicit* design reviews, never for explicit ones.
- `"design"` in `[ci].review_types` continues to mean "always run design review on every PR" — auto mode is bypassed.

### Rerun semantics

Reruns reuse the existing `RerunJob` UPDATE-in-place mechanism — the row's `status` moves back to `queued` and the worker processes it as a fresh run. This is consistent with how reruns of `done|failed|canceled` rows already work.

- Rerunning a `skipped` design row moves it from `skipped → queued` via `RerunJob`. The row's `source` field is preserved (still `auto_design`); the audit trail is the status transition, not a separate row. The `RerunJob` UPDATE's WHERE clause must be extended to accept `'skipped'` in addition to the current `('done','failed','canceled')`.
- Rerunning the *standard* review on a commit does NOT re-run auto-design. The auto-design outcome is already recorded (either a `done|failed` design review or a `skipped` row), and the dedup index prevents re-enqueue of another auto-design row. To re-evaluate after tuning heuristics, use `roborev review --type design <sha>` to explicitly run a new design review.
- `roborev review --type design <sha>` inserts with `source = NULL` — never blocked by the dedup index, regardless of whether an auto-design row exists for that commit. Users can always force a fresh design review.

### Commit-range CI reviews

The goals say "per-commit decision," but the CI poller currently enqueues reviews at the range level. Resolution: when a PR contains multiple commits, the CI poller runs the auto-design decision **per commit** (iterating over `prCommits`), not once over the range. Each commit gets its own classify job or design outcome. Deduplication is per-commit, so a PR that adds 3 commits may produce 0, 1, 2, or 3 design reviews depending on heuristics.

### Observability

The daemon maintains a set of in-memory counters on the `Server` struct (protected by an RWMutex), incremented at each auto-design decision point and exposed via `GET /api/status`:

```go
type AutoDesignStatus struct {
    Enabled             bool  `json:"enabled"`
    TriggeredHeuristic  int64 `json:"triggered_heuristic"`
    SkippedHeuristic    int64 `json:"skipped_heuristic"`
    TriggeredClassifier int64 `json:"triggered_classifier"`
    SkippedClassifier   int64 `json:"skipped_classifier"`
    ClassifierFailed    int64 `json:"classifier_failed"`
}
```

**Counters cover every auto-design decision source**, so `/api/status` reports the true daemon-wide activity:

- Enqueue-handler path (`maybeDispatchAutoDesign` in `internal/daemon/server.go`) → `TriggeredHeuristic` or `SkippedHeuristic` on heuristic decisions.
- CI poller path (the heuristic branch in `internal/daemon/ci_poller.go`) → same counters. Without this, CI-driven auto-design would be invisible in the status output.
- Classifier worker path (`applyClassifyVerdict` in `internal/daemon/worker.go`) → `TriggeredClassifier` or `SkippedClassifier` based on the classifier's yes/no.
- Classifier failure path (`completeClassifyAsSkip`) → `ClassifierFailed`.

The increments should live in a single shared helper (e.g. on the `Server`'s metrics struct with methods like `m.recordHeuristic(decision)` and `m.recordClassifierVerdict(yes)`) so the enqueue handler, CI poller, and worker all call the same code path.

The `auto_design` subobject is absent from `/api/status` when auto-design-review is disabled everywhere — both the global config and every registered repo. Used to tune heuristics after rollout. (Counters are intentionally in-memory only — the activity log continues to capture individual decisions as narrative events for debugging.)

### Enabled flag is tri-state

`enabled` in `RepoConfig.AutoDesignReview` is `*bool` (not `bool`), so the per-repo override can be:

- Unset → fall through to global.
- `true` → enable even if global is off.
- `false` → disable even if global is on.

With a plain `bool`, setting it to the zero value (`false`) is indistinguishable from "not set," preventing a repo from opting out of a globally-enabled default.

## Heuristics

The heuristics run on the resolved diff, commit message, and file list. Each rule is a pure function of `Input` and config. Precedence is fixed and explicit:

1. **Trigger rules** fire first; first match → `Run=true`.
2. **Skip rules** fire next; first match → `Run=false`.
3. If neither fires, fall through to the classifier.

### Trigger rules

| Rule | Default | Reason string |
|---|---|---|
| Any changed file matches `trigger_paths` | `**/migrations/**`, `**/schema/**`, `**/*.sql`, `docs/superpowers/specs/**`, `docs/design/**`, `docs/plans/**`, `**/*-design.md`, `**/*-plan.md` | "touches `<path>` (design-relevant)" |
| Total lines changed ≥ `large_diff_lines` | 500 | "large change (≥500 lines)" |
| Files changed ≥ `large_file_count` | 10 | "wide-reaching change (≥10 files)" |
| Commit subject matches any `trigger_message_patterns` regex | `\b(refactor&#124;redesign&#124;rewrite&#124;architect&#124;breaking)\b` (Go regex; `\|` would match a literal pipe) | "commit message mentions `<match>`" |

### Skip rules

| Rule | Default | Reason string |
|---|---|---|
| Total lines changed < `min_diff_lines` | 10 | "trivial diff (<10 lines)" |
| All changed files match some entry in `skip_paths` | `**/*.md`, `**/*_test.go`, `**/*.spec.*`, `**/testdata/**` | "doc/test-only change" |
| Commit subject matches any `skip_message_patterns` regex | `^(docs&#124;test&#124;style&#124;chore)(\(.+\))?:` (Go regex; `\|` would match a literal pipe) | "conventional marker: `<match>`" |

### Path matching

Path globs use `github.com/bmatcuk/doublestar/v4`'s `Match(pattern, path)` function. Doublestar is used only as the glob engine — we do not use its negation feature. Each list is a flat set of positive globs.

Rationale: Go's `path.Match` does not support `**` (recursive wildcard) — `*` never crosses `/` and pattern/input must have matching segment counts. Writing patterns like `**/migrations/**` requires a library. Doublestar is the de-facto choice (widely used, zero deps, matches bash/gitignore `**` semantics).

### Commit-message patterns

Commit-message patterns use Go's `regexp` package. Invalid patterns cause a loud error at `Classify()` entry (not silent drop), returned to the caller.

## Classifier

### Prompt shape

Compact prompt, capped at `classifier_max_prompt_size` (20 KB default). Contains:

- Commit subject + body. The body is fetched from git (`git log -1 --format=%B <ref>`) rather than relying on `review_jobs.commit_subject`, which stores only the subject line. Breaking-change notes, `BREAKING CHANGE:` footers, and rationale in the body materially affect classifier decisions, so they must be included.
- `git diff --stat` output (file-level summary).
- The diff itself, truncated at the cap with an explicit marker; if the diff exceeds the cap, only the stat and a head snippet are included.

No project guidelines, no past-review context. This is a routing decision, not a review.

System prompt (new, in `internal/prompt/templates.go`):

```
You are a routing classifier. Given a code change, decide whether it warrants
a design review.

A design review is warranted when the change:
- Introduces new packages, modules, or external interfaces
- Modifies public APIs, data schemas, or protocols
- Changes architectural boundaries (package dependencies, cross-cutting concerns)
- Alters invariants, state machines, or concurrency primitives
- Introduces a new dependency with non-trivial surface area

A design review is NOT warranted when the change is:
- An isolated bugfix with no interface change
- Test-only or documentation-only (unless docs describe new design)
- Formatting, rename, or lint cleanup
- Straightforward performance or readability improvements within existing
  boundaries

Return your verdict as a single JSON object matching the provided schema.
```

### Output schema

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["design_review", "reason"],
  "properties": {
    "design_review": { "type": "boolean" },
    "reason":        { "type": "string" }
  }
}
```

### SchemaAgent capability

New optional interface in `internal/agent/`:

```go
type SchemaAgent interface {
    Agent
    ClassifyWithSchema(
        ctx context.Context,
        repoPath, gitRef, prompt string,
        schema json.RawMessage,
        out io.Writer, // progress/log lines; not the final result
    ) (json.RawMessage, error)
}
```

Initial implementations:

- **`claude-code`**: spawns `claude -p --json-schema '<schema>'` and reads the final assistant message.
- **`codex`**: writes the schema to a temp file and invokes `codex exec --output-schema <schema.json> --output-last-message <result.json>`; reads `<result.json>`.

Agents that do not implement `SchemaAgent` (gemini, copilot, cursor, kiro, kilo, droid, opencode, pi) cause a config-load error when selected as `classify_agent`. Error message lists valid choices. Gemini's CLI does not currently support schema-constrained output (see [gemini-cli#13388](https://github.com/google-gemini/gemini-cli/issues/13388)); when it does, an implementation can be added.

### Failure modes

Every failure below converts the classify row in place via `MarkClassifyAsSkippedDesign` — same UPDATE used for the "no" verdict, just with a different reason string. No separate skipped-design row is inserted; the classify row itself becomes the terminal skipped design row.

| Failure | Behavior |
|---|---|
| Classifier timeout (default 60 s) | Classify row UPDATEd to `status='skipped'`, `skip_reason="classifier timed out"` |
| Retries exhausted (3x) | Same, `skip_reason="classifier failed: <last error>"` |
| Returned JSON malformed despite the flag | Treated as an agent failure → normal retry path; after retries, UPDATE with `skip_reason="classifier returned invalid JSON"` |
| Quota exhaustion | Existing failover path via `classify_backup_agent` / `classify_backup_model` before giving up |
| Agent lost `SchemaAgent` capability between config load and dispatch | UPDATE with `skip_reason="classify_agent <name> does not support structured output"` (defensive; should be caught at config load) |

**Default policy: skip on any classifier failure.** A broken classifier should not spam design reviews; the user can always run `roborev review --type design` explicitly.

### Heuristic-input acquisition failures

The heuristic engine needs a diff, changed-files list, and commit message. The enqueue handler and CI poller acquire these via `git.GetDiff`, `git.GetFilesChanged`, and `git.GetCommitInfo` (plus the existing job fields `DiffContent` and `CommitSubject` as fallbacks). Each can fail (missing SHA, corrupt repo, deleted commit, transient git error). The policy:

- `git.GetDiff` fails → treat the diff as empty for heuristic evaluation. An empty diff will not hit trigger heuristics (`large_diff_lines`, `trigger_paths` file checks) and will usually fall into classifier-ambiguous, where the classifier job has a second chance to fetch the diff under its own timeout.
- `git.GetFilesChanged` fails → same: empty file list, no path-based triggers match, falls through.
- `git.GetCommitInfo` fails → `classifierCommitMessage` returns the stored `CommitSubject`, which is always populated on the job row at enqueue time.
- If the heuristic engine itself returns an error (invalid regex in config, etc.) → log and default to **skip**, not trigger, with reason "auto-design: heuristic error". Same philosophy as classifier failures: a broken heuristic pipeline should not spam design reviews.

Net effect: partial input-acquisition failures degrade gracefully to "ambiguous → classifier", and total heuristic-pipeline failures degrade to "skip with diagnostic reason". Neither falls into a retry-forever loop; both leave a visible audit trail in the activity log and (for skips) in `skip_reason`.

On failure, the classify row is converted in place (same UPDATE as the "no" outcome): `job_type='review'`, `status='skipped'`, `skip_reason` populated with the failure text. This is deliberately NOT a `FailJob` call — the classifier has reached a routing decision (defaulting to skip) even though the underlying agent call failed. Marking the row `skipped` rather than `failed` keeps CI batch accounting accurate: a classifier failure that degrades to skip shouldn't cause the PR batch to report as failed.

## Config

### Global `~/.roborev/config.toml`

```toml
# Classify workflow fields live at the top level of the global Config,
# matching the existing workflow-config convention (fix_agent, fix_model,
# review_agent, review_backup_agent, …) that lookupFieldByTag reflects over.
# Only the enable flag, heuristic knobs, and classifier limits live under
# [auto_design_review].
classify_agent          = "claude-code"
classify_model          = ""
classify_reasoning      = "fast"
classify_backup_agent   = "codex"
classify_backup_model   = ""

[auto_design_review]
enabled = false

# Heuristic thresholds
min_diff_lines   = 10
large_diff_lines = 500
large_file_count = 10

# Path globs (doublestar syntax: `*`, `**`, `?`). No negation.
trigger_paths = [
  "**/migrations/**",
  "**/schema/**",
  "**/*.sql",
  "docs/superpowers/specs/**",
  "docs/design/**",
  "docs/plans/**",
  "**/*-design.md",
  "**/*-plan.md",
]
skip_paths = [
  "**/*.md",
  "**/*_test.go",
  "**/*.spec.*",
  "**/testdata/**",
]

# Commit-message regexes (Go regex syntax)
trigger_message_patterns = ['\b(refactor|redesign|rewrite|architect|breaking)\b']
skip_message_patterns    = ['^(docs|test|style|chore)(\(.+\))?:']

# Classifier limits
classifier_timeout_seconds = 60
classifier_max_prompt_size = 20480
```

### Per-repo override `.roborev.toml`

The per-repo file mirrors the global layout:

- Under `[auto_design_review]`: `enabled` (tri-state `*bool` at the repo layer), heuristic knobs (`min_diff_lines`, `large_diff_lines`, `large_file_count`, `trigger_paths`, `skip_paths`, `trigger_message_patterns`, `skip_message_patterns`), and the classifier limits (`classifier_timeout_seconds`, `classifier_max_prompt_size`).
- At the top level (alongside `fix_agent`, `review_agent`, etc.): `classify_agent`, `classify_model`, `classify_reasoning`, `classify_backup_agent`, `classify_backup_model`.

Any set field overrides the corresponding global value. Unset fields fall through. List fields are replaced wholesale (same behavior as `exclude_patterns` today).

### Resolve functions

Added to `internal/config/config.go`, following the existing `Resolve*` naming:

```go
func ResolveAutoDesignEnabled(repoPath string, globalCfg *Config) bool
func ResolveAutoDesignHeuristics(repoPath string, globalCfg *Config) AutoDesignHeuristics
func ResolveClassifyAgent(cliAgent, repoPath string, globalCfg *Config) (string, error)
func ResolveClassifyModel(cliModel, repoPath string, globalCfg *Config) string
func ResolveClassifyReasoning(cliReasoning, repoPath string, globalCfg *Config) string
func ResolveBackupClassifyAgent(repoPath string, globalCfg *Config) string
func ResolveBackupClassifyModel(repoPath string, globalCfg *Config) string
func ResolveClassifierTimeout(repoPath string, globalCfg *Config) time.Duration
func ResolveClassifierMaxPromptSize(repoPath string, globalCfg *Config) int
```

`ResolveClassifyAgent` validates that the resolved agent implements `SchemaAgent`; returns a typed error naming valid choices if not. The check runs every time the function is called (at enqueue time and once on daemon startup for fail-fast behavior), so a user who changes `classify_agent` in config after the daemon has started still sees the validation.

### Workflow registry

`"classify"` is added to the existing `lookupFieldByTag` workflow set (alongside `review`, `refine`, `fix`, `security`, `design`). This keeps the reasoning-level/backup-chain pattern consistent.

## Database schema

### Migration (SQLite, `internal/storage/db.go`)

All schema changes, applied as a single idempotent migration call (the db.go file uses column-existence checks rather than numbered migration versions):

- Rebuild `review_jobs` to extend the `status` CHECK constraint with `'skipped'`. (`job_type` is a plain `TEXT NOT NULL DEFAULT 'review'` column with no CHECK constraint, so `'classify'` is accepted without a schema change — it's only a Go-side convention in `internal/storage/models.go`.)
- Add column `review_jobs.skip_reason TEXT` (nullable).
- Add column `review_jobs.source TEXT` (nullable). `source = 'auto_design'` tags rows produced by the auto-design path; `NULL` means explicit/user-initiated.
- Create two partial unique indexes scoped to `source = 'auto_design'`:
  - `idx_review_jobs_auto_design_dedup` on `(repo_id, commit_id, review_type)`
  - `idx_review_jobs_auto_design_dedup_ref` on `(repo_id, git_ref, review_type)` for rows where `commit_id IS NULL`

SQLite can't `ALTER` a CHECK constraint, so the migration follows the table-rebuild pattern the existing migrations use. The same SQL is also reflected in the top-level `schema` constant so fresh databases get the final shape directly.

### Postgres mirror (`internal/storage/postgres.go`)

Same column additions (via `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`), constraint updates (drop + re-add), and partial unique indexes. Gated by the existing schema-version bump.

### Model changes (`internal/storage/models.go`)

```go
const JobStatusSkipped JobStatus = "skipped"

const JobTypeClassify = "classify"

type ReviewJob struct {
    // ... existing fields ...
    SkipReason string `json:"skip_reason,omitempty"`
    Source     string `json:"source,omitempty"` // "auto_design" for auto-dispatched rows, empty for explicit
}
```

(Plain `string` fields match the existing pattern for nullable text columns like `Error` and `WorktreePath`; the SQL `COALESCE(..., '')` in reads handles NULL.)

### New query

`internal/storage/jobs.go`:

```go
func (db *DB) HasAutoDesignSlotForCommit(repoID int64, sha string) (bool, error)
```

Returns true when the auto-design dedup slot is occupied for `(repo_id, commit_sha)`. The query is the exact complement of the partial unique index: it matches rows with `review_type = 'design'` AND `source = 'auto_design'`, covering every kind of row that occupies the slot:

- queued classify jobs (`job_type = 'classify'`, `review_type = 'design'`, `source = 'auto_design'`)
- queued / running / done design reviews (`job_type = 'review'`, `review_type = 'design'`, `source = 'auto_design'`)
- skipped design rows (`status = 'skipped'`, `review_type = 'design'`, `source = 'auto_design'`)

Explicit (`source = NULL`) design rows — from `roborev review --type design`, CI `review_types`, or user reruns — are deliberately outside this query's scope because they are also outside the unique index's scope and are allowed to coexist with auto-design rows.

This is a performance shortcut, not the authoritative dedup mechanism. A `true` return guarantees an `INSERT ... ON CONFLICT DO NOTHING` against the auto-design index would be a no-op.

## TUI rendering

`cmd/roborev/tui/render_queue.go`: skipped jobs rendered dimmed with a distinct status glyph; `skip_reason` shown in the detail panel. No new view — skipped rows live in the same queue as regular jobs, filtered by status like any other.

## Testing strategy

### `internal/review/autotype/autotype_test.go`

Table-driven tests covering each rule in isolation, then combinations to verify precedence.

- **Path matching**: doublestar patterns with `**` across depths; verify `*` doesn't cross `/`.
- **Diff-size rules**: `min_diff_lines` boundary (9/10/11), `large_diff_lines` boundary (499/500/501), `large_file_count` boundary.
- **Message regexes**: conventional commit subject variations; word-boundary keyword matching.
- **Precedence**: trigger beats skip; within trigger list, first match wins; fallthrough to "classifier needed" when nothing matches.
- **Mocked classifier**: stub `Classifier.Decide` returning canned yes/no/parse-error/timeout; verify final `Decision.Run` / `Reason` / `Method`.
- **Edge cases**: empty diff; binary files in changed-files list; paths with spaces/unicode; invalid regex in config → loud error; empty lists (both trigger and skip).

### `internal/prompt/classify_test.go`

- Diff below cap → full diff embedded.
- Diff above cap → `--stat` + truncated head with explicit truncation marker.
- Prompt contains schema representation in agent-specific form.

### Agent capability tests

- `internal/agent/claude_test.go`: `ClassifyWithSchema` invokes `claude -p --json-schema '…'`, parses the final message; known-bad outputs (empty stream, non-JSON) produce a typed error.
- `internal/agent/codex_test.go`: `ClassifyWithSchema` writes schema + output-last-message temp files, reads result, cleans up on success and failure.
- `internal/config/config_test.go`: `ResolveClassifyAgent` errors clearly when the selected agent doesn't implement `SchemaAgent` (via the capability validator registered at init time), naming the valid list. (`ResolveClassifyAgent` is a config-layer function; the agent package only supplies the validator.)

### Storage tests

- `internal/storage/db_test.go`: the auto-design-review migration runs idempotently on a fresh DB and upgrades an existing DB (insert pre-migration row → run migration → row + new columns survive; unique indexes enforce dedup).
- `internal/storage/jobs_test.go`: insert a `status=skipped, review_type=design, skip_reason=...` row, re-read, confirm round-trip.
- `ClaimJob` never returns a `skipped` job (terminal-state check).
- `job_type = "classify"` jobs are claimed by workers like any other.
- `HasAutoDesignSlotForCommit` returns true for every row kind that occupies the auto-design dedup slot: queued classify jobs, queued/running/done design reviews, and skipped design rows (all with `source = 'auto_design'`). Explicit (`source = NULL`) rows do not count.

### Enqueue-handler tests (heuristic path)

`internal/daemon/server_integration_test.go` or similar server-level integration tests, since heuristics run inside the enqueue handler (not the worker):

- Heuristic-trigger path: standard review + design review both appear (design row inserted directly via `EnqueueAutoDesignJob`, no classify row).
- Heuristic-skip path: standard review appears; a skipped design row appears with populated `skip_reason`; no classify row was created.
- Dedup: enqueue the same commit twice from the local hook; the second attempt hits `ON CONFLICT DO NOTHING`, only one auto-design row exists.

### CI poller tests (heuristic + per-commit decision)

`internal/daemon/ci_poller_test.go`:

- `auto_design_review.enabled = true` and `"design"` not in `review_types`: poller calls `Classify` per commit; trigger/skip/classify rows enqueued only when the per-commit decision warrants it.
- `"design"` in `review_types`: auto mode bypassed — design review always runs (preserves existing behavior).
- Dedup across producers: poller and local hook both reach the same commit; only one auto-design row exists.

### Worker integration tests (classify path only)

`internal/daemon/worker_test.go`:

- Classifier-yes path: seed a classify row, run `wp.processJob`; after `PromoteClassifyToDesignReview`, the same row is `job_type='review', status='queued'`, `source='auto_design'`, `worker_id` cleared. No second row exists.
- Classifier-no path: same, but `MarkClassifyAsSkippedDesign` leaves the row `status='skipped'`, `job_type='review'`, with `skip_reason` populated.
- Classifier-failure path: after retries and backup-agent fallback, the same classify row is `status='skipped'` with `skip_reason` carrying the failure text — NOT `status='failed'`. Standard review unaffected.

### Fixtures

- `testutil.ClassifierStub{Verdict, Reason, Err}` implementing the classifier interface, for deterministic worker tests.
- Reuse `workerTestContext`; add `createAndClaimClassifyJob()` mirroring `createAndClaimJob()`.

### Out of scope

- End-to-end tests with real `claude` / `codex` CLIs. Agent wrappers are tested with mocked invocations; this matches the existing agent-wrapper test pattern.
- Load / performance tests. Classifier is cheap and serial; no new hot path.

## Rollout

- Shipped off by default (`enabled = false`). No behavior change for existing users.
- Docs: add a `docs/auto-design-review.md` with config examples and tuning guidance. Users enable by editing `~/.roborev/config.toml` (or per-repo `.roborev.toml`).
- **Out of scope**: adding an opt-in prompt to `roborev init`. The UX for surfacing the feature to new users is a separate UX decision and doesn't block shipping the decision engine. Tracked separately if/when we decide to prompt.

### Validating the heuristic-engine target

Success criterion §1 promises that `autotype.Classify` itself runs in under 25 ms per call. This gets verified by a small Go benchmark in `internal/review/autotype/bench_test.go` run locally before the feature is enabled:

- `BenchmarkClassify_HeuristicTrigger` — trigger_paths match, no diff parsing beyond file-list checks.
- `BenchmarkClassify_HeuristicSkip` — 30-line diff of `.md` files, falls into skip_paths.
- `BenchmarkClassify_Ambiguous` — 50-line diff outside any trigger/skip list, reaches the classifier-required branch (no agent call; returns `ErrNeedsClassifier`).

The benchmark target is wall-clock time per iteration of `autotype.Classify` in isolation. It deliberately does NOT include git metadata collection, the DB insert, or any agent invocation — those costs are either pre-existing in the enqueue path (git/DB) or async (agents). If enqueue-path end-to-end latency becomes a concern in practice, a separate higher-level benchmark can be added as a follow-up.
