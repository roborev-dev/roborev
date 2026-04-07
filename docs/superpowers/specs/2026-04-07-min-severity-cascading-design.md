# Min-Severity Cascading + Review-Level Filter

**Status:** Draft
**Date:** 2026-04-07

## Summary

Make `review_min_severity`, `refine_min_severity`, and `fix_min_severity` configurable in both global `~/.roborev/config.toml` and per-repo `.roborev.toml`, walking the same explicit → repo → global → "" cascade. Add a brand-new `review_min_severity` knob that filters reviews at prompt time the same way refine and fix already do — when all findings are below threshold, the agent emits `SEVERITY_THRESHOLD_MET` and the review verdict becomes pass.

## Motivation

Today:

- `RefineMinSeverity` and `FixMinSeverity` exist on `RepoConfig` only. There is no global default — every repo that wants the same threshold must repeat it.
- Reviews have no severity filter at all. Agents emit every finding they spot, and verdict parsing flips to fail on any severity-labeled bullet. There is no way to say "I only care about medium and above" at the review tier.
- `CI.MinSeverity` already cascades global → per-repo, but it lives under the `[ci]` table and only governs CI synthesis output, not the underlying review jobs.

The user wants three things, all driven by the same goal of reducing noise:

1. Promote `refine_min_severity` to a global setting.
2. Promote `fix_min_severity` to a global setting (consistency, avoid drift).
3. Add `review_min_severity` (global + per-repo) so low-severity findings never show up in the review output to begin with.

## Non-goals

- Retroactive filtering of stored review output. Past reviews stay as written.
- Per-agent severity overrides. One knob per workflow.
- Changing CI synthesis (`[ci] min_severity`) — it already cascades and lives in its own namespace.
- Per–review-type severity overrides for security/design. The new `review_min_severity` applies uniformly to standard, security, and design reviews.

## Design

### 1. Config schema

**Global `Config` (`internal/config/config.go`)** — add three top-level fields:

```go
ReviewMinSeverity string `toml:"review_min_severity" comment:"Minimum severity for reviews: critical, high, medium, or low. Empty disables filtering."`
RefineMinSeverity string `toml:"refine_min_severity" comment:"Minimum severity for refine: critical, high, medium, or low. Empty disables filtering."`
FixMinSeverity    string `toml:"fix_min_severity"    comment:"Minimum severity for fix: critical, high, medium, or low. Empty disables filtering."`
```

**`RepoConfig`** — `RefineMinSeverity` and `FixMinSeverity` already exist. Add the missing one:

```go
ReviewMinSeverity string `toml:"review_min_severity" comment:"Minimum severity for reviews in this repo: critical, high, medium, or low."`
```

Validation: all four valid values are `critical | high | medium | low | ""`. Reuse the existing `NormalizeMinSeverity`.

### 2. Resolution chain

Three sibling resolvers, all walking the same cascade:

```
explicit (CLI flag) → repo .roborev.toml → global config.toml → ""
```

```go
// internal/config/config.go

// NEW
func ResolveReviewMinSeverity(explicit, repoPath string, globalCfg *Config) (string, error)

// UPDATED — adds globalCfg parameter
func ResolveRefineMinSeverity(explicit, repoPath string, globalCfg *Config) (string, error)
func ResolveFixMinSeverity(explicit, repoPath string, globalCfg *Config) (string, error)
```

Implementation pattern (mirrors the existing per-repo-only versions):

```go
func ResolveReviewMinSeverity(explicit, repoPath string, globalCfg *Config) (string, error) {
    if strings.TrimSpace(explicit) != "" {
        return NormalizeMinSeverity(explicit)
    }
    if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil &&
        strings.TrimSpace(repoCfg.ReviewMinSeverity) != "" {
        return NormalizeMinSeverity(repoCfg.ReviewMinSeverity)
    }
    if globalCfg != nil && strings.TrimSpace(globalCfg.ReviewMinSeverity) != "" {
        return NormalizeMinSeverity(globalCfg.ReviewMinSeverity)
    }
    return "", nil
}
```

Refine and fix follow the identical shape, just reading their respective fields.

**Caller updates (signature change ripples):**

| File | Change |
|------|--------|
| `cmd/roborev/refine.go:384` | `cfg` is already loaded at line 351 — pass it into `ResolveRefineMinSeverity` |
| `cmd/roborev/fix.go:825` (`fixSingleJob`) | Load `cfg, _ := config.LoadGlobal()` near the top of the function (the existing call to `resolveFixAgent` loads it internally but doesn't return it, so a second `LoadGlobal()` call is the simplest fix). Pass into `ResolveFixMinSeverity`. |
| `cmd/roborev/fix.go:1013` (`runFixBatch`) | `cfg` is currently loaded at line 1030 (after the resolver call). Move the `cfg, _ := config.LoadGlobal()` call up before line 1013 and pass into `ResolveFixMinSeverity`. |
| `cmd/roborev/review.go` | New `--min-severity` flag — validates via `NormalizeMinSeverity`, sends the **canonical lowercase return value** (NOT the raw user input) as `min_severity` in the `EnqueueRequest` POST body. Does NOT call the resolver for the daemon path — see §4. For `--local` mode: `runLocalReview` gains a `minSeverity string` parameter, calls `config.ResolveReviewMinSeverity(flag, repoPath, cfg)` (fail-fast on error), and passes the result into `pb.Build`/`pb.BuildDirty`. |
| `internal/daemon/server.go:673` (`EnqueueRequest`) | Add `MinSeverity string` with `json:"min_severity,omitempty"` tag so the daemon can receive the CLI value. |
| `internal/daemon/server.go:handleEnqueue` | Validate `req.MinSeverity` via `config.NormalizeMinSeverity` near line 866 (mirrors the existing `ResolveReviewReasoning` pattern), reject invalid input with 400, and store the **normalized return value** (`normalizedMinSev`) into all four review-branch `EnqueueOpts` literals (lines ~955, ~979, ~1056, ~1108) — never raw `req.MinSeverity`. The storage layer also applies `normalizeMinSeverityForWrite` as the last-line guarantee (see §4). |
| `internal/daemon/worker.go:398/401` | New call to `ResolveReviewMinSeverity` before building the review prompt — see §5 for the snippet. |
| `internal/daemon/server.go handleFixJob` | Resolves `ResolveFixMinSeverity` at enqueue time and bakes the severity instruction into the stored fix prompt — see §3.5. |

### 3. Storage: per-job override column

To support `roborev review --min-severity=high` end-to-end, the daemon worker needs to know the per-invocation override. The CLI enqueues a job and exits; the worker picks it up later. The cleanest mirror of how `reasoning`, `model`, and `agent` are already plumbed is a new column on `review_jobs`.

**SQLite migration** (`internal/storage/db.go`) — append a new idempotent `ALTER TABLE` to the `migrate()` chain, following the same `pragma_table_info` guard pattern used for `reasoning`, `agentic`, etc.:

```sql
ALTER TABLE review_jobs ADD COLUMN min_severity TEXT NOT NULL DEFAULT ''
```

**PostgreSQL schema + migration** (`internal/storage/postgres.go` and `internal/storage/schemas/`):

PG is trickier than SQLite because `EnsureSchema()` loads the base schema from an embedded SQL file (`//go:embed schemas/postgres_v10.sql`), then only runs the `currentVersion < N` migrations if an existing install is behind. A fresh install with `currentVersion == 0` takes the "insert version directly" branch (lines 170–188) and **skips all migration blocks**. So adding a new column requires both:

1. Create `internal/storage/schemas/postgres_v11.sql` — copy `postgres_v10.sql` and add `min_severity TEXT NOT NULL DEFAULT ''` to the `review_jobs` table definition. Fresh installs read this file.
2. Update the embed directive from `//go:embed schemas/postgres_v10.sql` → `//go:embed schemas/postgres_v11.sql` (line 22).
3. Bump `pgSchemaVersion` from 10 to 11 (line 17).
4. Add a new `if currentVersion < 11 { ... }` block alongside the existing v10 ALTERs so upgraded installs get the column:

```sql
ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS min_severity TEXT NOT NULL DEFAULT ''
```

**Model field** (`internal/storage/models.go`) — add to `ReviewJob`:

```go
MinSeverity string `json:"min_severity,omitempty"`
```

**Hydration field** (`internal/storage/hydration.go`) — add to `reviewJobScanFields`:

```go
MinSeverity string
```

Plain `string`, not `sql.NullString` — all queries use `COALESCE(j.min_severity, '')` so the scan target is never null. In `applyReviewJobScan`, copy through directly: `job.MinSeverity = fields.MinSeverity`.

**SQL site checklist** — every place that touches `review_jobs` columns needs `min_severity` added to its column list. This is the same plumbing pattern that `reasoning`, `model`, `provider`, and `requested_model` already follow:

| File | Site (line ≈) | What |
|------|---------------|------|
| `internal/storage/jobs.go` | `EnqueueJob` INSERT (~123) | Add column to INSERT, add value via `normalizeMinSeverityForWrite(opts.MinSeverity)`, add `MinSeverity` to `EnqueueOpts` struct (~42), copy onto returned `ReviewJob` |
| `internal/storage/jobs.go` | `ClaimJob` SELECT (~209) | Add `j.min_severity` to column list and `&fields.MinSeverity` to scan |
| `internal/storage/jobs.go` | `ListJobs` SELECT (~782) | Same |
| `internal/storage/jobs.go` | `GetJobByID` SELECT (~871) | Same |
| `internal/storage/reviews.go` | `GetReviewByJobID` SELECT (~20) | Same |
| `internal/storage/reviews.go` | `GetReviewByCommitSHA` SELECT (~52) | Same |
| `internal/storage/reviews.go` | `GetJobsWithReviewsByIDs` SELECT (~307) | Same |
| `internal/storage/sync.go` | `GetJobsToSync` SELECT (~304) | Add column with `COALESCE(j.min_severity, '')`, add to scan, add to `SyncableJob` struct |
| `internal/storage/sync.go` | `UpsertPulledJob` INSERT + ON CONFLICT (~582) | Add column to INSERT, add `normalizeMinSeverityForWrite(j.MinSeverity)` to params and ON CONFLICT update, add to `PulledJob` struct |
| `internal/storage/postgres.go` | v11 migration (~291) | New `ALTER TABLE ... ADD COLUMN IF NOT EXISTS min_severity` block |
| `internal/storage/postgres.go` | `UpsertJob` INSERT (~554) | Add column to INSERT and ON CONFLICT update list, add `normalizeMinSeverityForWrite(j.MinSeverity)` to params (used in tests only; production sync uses `BatchUpsertJobs`) |
| `internal/storage/postgres.go` | `BatchUpsertJobs` INSERT (~945) | Add `min_severity` to INSERT column list and ON CONFLICT update, add `normalizeMinSeverityForWrite(j.MinSeverity)` to params. This is the **production sync push path** (`SyncWorker.pushChangesWithStats` → `BatchUpsertJobs`). |
| `internal/storage/postgres.go` | `PullJobs` SELECT (~656) | Add `COALESCE(j.min_severity, '')` to column list and scan into `PulledJob` |

The `SyncableJob` and `PulledJob` structs (in `sync.go` and `postgres.go` respectively) each gain a `MinSeverity string` field. Tests that build these structs by hand may need updating.

### 3.5. Daemon background fix path

`internal/daemon/server.go handleFixJob` builds fix prompts via `buildFixPrompt`, `buildFixPromptWithInstructions`, and `buildRebasePrompt` (all in `server.go`), then stores them on the job at enqueue time. None of these helpers currently inject `SeverityInstruction`, so daemon-enqueued fix jobs (the "Fix" button in the TUI) ignore both global and per-repo severity settings.

**Skip task/insights parents.** `handleFixJob` only rejects parents where `parentJob.IsFixJob()` is true; task and insights parents (`JobTypeTask`, `JobTypeInsights`) flow through and get fix prompts built from their free-form output. The CLI fix path at `cmd/roborev/fix.go:824` already guards severity resolution with `if !job.IsTaskJob() { ... }` because task/insights outputs do not have severity-labeled findings — telling the agent to "ignore findings below high" against a free-form analysis would suppress the very content the user wants acted on. The daemon path must mirror this exactly:

```go
var fixMinSev string
if !parentJob.IsTaskJob() {
    // Resolve against the worktree checkout when the parent has one,
    // otherwise the main repo. Worktree-backed jobs may carry a
    // different .roborev.toml on a feature branch.
    effectivePath := parentJob.RepoPath
    if parentJob.WorktreePath != "" &&
        gitpkg.ValidateWorktreeForRepo(parentJob.WorktreePath, parentJob.RepoPath) {
        effectivePath = parentJob.WorktreePath
    }
    // cfg already loaded at line ~2430 as `cfg := s.configWatcher.Config()`
    resolved, err := config.ResolveFixMinSeverity("", effectivePath, cfg)
    if err != nil {
        writeError(w, http.StatusBadRequest, fmt.Sprintf("resolve fix min-severity: %v", err))
        return
    }
    fixMinSev = resolved
}
```

Two things to notice:

1. **Worktree-aware path resolution.** `parentJob.WorktreePath` is set on jobs that ran in an isolated worktree (the field is documented in `internal/storage/models.go:78` as "Worktree checkout path (empty = use RepoPath)"). Worker code at `internal/daemon/worker.go:367–375` already uses an `effectiveRepoPath = job.RepoPath; if job.WorktreePath != "" && gitpkg.ValidateWorktreeForRepo(...) { effectiveRepoPath = job.WorktreePath }` pattern. The fix-job resolver call must use the same pattern, or daemon/TUI fixes will read `.roborev.toml` from the main checkout and ignore per-branch overrides on the worktree. The validation guard is important: a stale `WorktreePath` that no longer points to a valid worktree must fall back to `RepoPath`, matching the worker behavior.

2. **Task/insights skip.** When `parentJob.IsTaskJob()` is true, `fixMinSev` stays empty and no `SeverityInstruction` gets injected — the agent receives the original free-form prompt unchanged. This matches `cmd/roborev/fix.go:824` exactly. Without this guard, a "Fix" click on an insights/task review would tell the agent to ignore everything in its own output that wasn't severity-labeled, which is the opposite of the user's intent.

Resolver errors surface as 400 Bad Request — a misconfigured `fix_min_severity` should block the fix from being enqueued, not silently disable the filter. This mirrors the worker fail-fast behavior in §5.

Then thread `fixMinSev` into the prompt builders that are findings-driven. Each helper gains a trailing `minSeverity string` parameter and emits `config.SeverityInstruction(minSeverity)` once after the existing preamble (a no-op when `fixMinSev` is empty, which is what skipped task/insights parents will pass):

- `buildFixPrompt(reviewOutput, minSeverity)`
- `buildFixPromptWithInstructions(reviewOutput, instructions, minSeverity)`

**`buildRebasePrompt` is intentionally NOT threaded with `minSeverity`.** The rebase prompt at `internal/daemon/server.go:2734` only carries the prior stale patch (`buildRebasePrompt(stalePatch *string)`) — it does not include the original review findings. The agent's job is to re-apply the same intent against current HEAD ("achieve the same changes but adapted to the current state"). Telling that agent to "ignore findings below high" gives it no findings to map against and creates a real risk: if a new severity threshold would have demoted some of the prior fix's hunks below the bar, the rebase agent could silently drop those hunks, dropping work that was already validated. The rebase contract is "preserve the previous patch's effect," not "re-evaluate scope." Rebase therefore stays threshold-free; if the user wants to re-scope under a new severity, they should generate a fresh fix from the parent review, not rebase.

Why bake at enqueue (not store-and-resolve-at-worker-time): `handleFixJob` already bakes `agent`, `model`, and `reasoning` into the job at enqueue time (see line ~2431 `reasoning := "standard"`). Fix prompts are pre-built and stored on the job; the worker uses them verbatim via `UsesStoredPrompt()`. Storing `min_severity` separately and re-resolving in the worker would require rebuilding the whole prompt at execution time, which contradicts the stored-prompt model. The cost of baking is that a stale fix job rerun won't pick up later config changes — same trade-off the existing reasoning/model fields make. The user can always re-click "Fix" in the TUI to regenerate.

### 4. CLI flag for `roborev review`

Add `--min-severity` to `cmd/roborev/review.go`. The flag value:

1. Validates via `NormalizeMinSeverity` (early error on bad input).
2. The CLI sends the **normalized return value** (canonical lowercase) to the daemon in the enqueue request body — NOT the raw user input.
3. The daemon stores the canonical value on the enqueued `ReviewJob.MinSeverity` field.
4. Empty (flag not set) means "fall back to cascade resolution at worker time."

**Why normalize at the boundary:** `config.SeverityInstruction(minSeverity)` looks up its argument in a strict map (`severityAbove` at `internal/config/config.go:1372`) that only accepts canonical lowercase keys (`critical`, `high`, `medium`, `low`). It returns `""` for anything else. The worker uses `job.MinSeverity` directly when non-empty (it does NOT re-normalize), so if anything stored `"HIGH"` or `" high "` the worker would pass that through to `SeverityInstruction`, get back `""`, and silently disable the filter.

**The storage invariant:** `review_jobs.min_severity` always holds either an empty string or a canonical lowercase value (`critical`, `high`, `medium`, `low`). The worker depends on this. Three distinct trust boundaries enforce it, because there are three distinct write paths into the column:

1. **CLI boundary** (`cmd/roborev/review.go`) — user-facing entry: `NormalizeMinSeverity` runs on the user's flag value, errors out on bad input, and the canonical return value is what gets sent on the wire. Surfaces typos immediately as helpful error messages.

2. **Daemon enqueue boundary** (`internal/daemon/server.go handleEnqueue`) — HTTP entry: `EnqueueRequest` is a JSON-over-HTTP API that any client can hit (a future automation script, an MCP wrapper, a custom curl). The daemon validates `req.MinSeverity` via `NormalizeMinSeverity`, returns 400 Bad Request on invalid input, and stores the **return value** into `EnqueueOpts.MinSeverity`. Same pattern `handleEnqueue` follows for reasoning at line 866.

3. **Storage write boundary** (`internal/storage/jobs.go`, `internal/storage/sync.go`, `internal/storage/postgres.go`) — sync entry: jobs flow into `review_jobs.min_severity` via at least three distinct write sites — `EnqueueJob` (called from `handleEnqueue` and from CLI direct paths), `UpsertPulledJob` (called by `syncworker.go` when pulling from Postgres), and PG `UpsertJob` (called when pushing to Postgres). The CLI and daemon boundaries above only cover the first one; sync ingest is a third path that bypasses HTTP entirely and copies values straight from a remote machine that may pre-date this change or have been corrupted. Without normalization at the storage layer, a synced `"HIGH"` value would silently disable the filter on the local machine — exactly the bug this section is supposed to prevent.

The fix is a small storage-layer helper that every write site calls:

```go
// internal/storage/min_severity.go
package storage

import "github.com/roborev/internal/config"

// normalizeMinSeverityForWrite returns the canonical lowercase value or empty
// string. Invalid input is dropped (returned as empty), not rejected — sync
// ingest from a stale or corrupted remote machine should not fail the entire
// sync over a single bad column. Local user-facing entry points (CLI, daemon)
// validate fail-fast; this storage helper is the last-line guarantee for the
// stored invariant and is intentionally lossy on bad input.
func normalizeMinSeverityForWrite(value string) string {
    normalized, err := config.NormalizeMinSeverity(value)
    if err != nil {
        // Bad value somehow reached the storage layer. Drop it so the
        // stored invariant holds; the worker will then fall back to the
        // cascade resolution. Log so the dropped value is recoverable
        // from operator-side logs if needed.
        return ""
    }
    return normalized
}
```

Apply at every write site that touches `min_severity`:

| Site | Wrap |
|------|------|
| `EnqueueJob` (`internal/storage/jobs.go:~123`) | `normalizeMinSeverityForWrite(opts.MinSeverity)` in the INSERT params |
| `UpsertPulledJob` (`internal/storage/sync.go:579`) | `normalizeMinSeverityForWrite(j.MinSeverity)` in the INSERT params and the ON CONFLICT update |
| `UpsertJob` (`internal/storage/postgres.go:553`) | `normalizeMinSeverityForWrite(j.MinSeverity)` in the INSERT params and the ON CONFLICT update |

Why drop instead of error on the storage path: sync runs in a background worker that processes batches; a single corrupt row from a stale remote machine should not halt sync for everything else. The worst case post-drop is "filter falls back to cascade," which matches the empty-column behavior — strictly safer than the current "silently disable filter on read."

**Worker stays simple:** with the storage invariant enforced at every write site, the worker reads `job.MinSeverity` and passes it verbatim to `pb.Build` / `pb.BuildDirty`. No re-normalization, no extra validation. The hot path stays uncluttered and reflects the now-truly-enforced invariant: **anything in `review_jobs.min_severity` is canonical or empty.**

**Wire path:**

The CLI doesn't call `EnqueueJob` directly — it POSTs an `EnqueueRequest` JSON body to the daemon's `/api/enqueue` endpoint. Threading the value through requires four edits:

1. **`internal/daemon/server.go` `EnqueueRequest` struct (line 673)** — add `MinSeverity string \`json:"min_severity,omitempty"\``.
2. **`internal/daemon/server.go` `handleEnqueue` validation (~line 866, near `ResolveReviewReasoning`)** — call `normalizedMinSev, err := config.NormalizeMinSeverity(req.MinSeverity)` and return 400 Bad Request on error. This is the daemon trust boundary (see "Two trust boundaries" above).
3. **`internal/daemon/server.go` `handleEnqueue` review-branch `EnqueueOpts` literals (lines ~955, ~979, ~1056, ~1108)** — pass `MinSeverity: normalizedMinSev` through (use the normalized value, NOT the raw `req.MinSeverity`). The fix-job literal at ~2484 is handled separately in §3.5 (severity is baked into the stored prompt there, not stored on a column, so it doesn't need to set `MinSeverity`).
4. **`cmd/roborev/review.go`** — add the flag, validate via `NormalizeMinSeverity` (early error), set `min_severity` on the JSON body using the normalized return value.

Task/insights jobs at line ~955 pass `MinSeverity: normalizedMinSev` for storage consistency even though their stored prompts ignore severity (see §5). This keeps the column meaningful on rerun paths.

**`--local` mode (daemon-bypass):** `cmd/roborev/review.go` also has a `--local` flag that calls `runLocalReview()` (line 368) which invokes `pb.Build` / `pb.BuildDirty` directly without the daemon or storage layer (lines 431/433). This path must also honor the cascade, otherwise adding the trailing `minSeverity string` parameter to `Build`/`BuildDirty` would either fail to compile or silently pass the empty string and disable the filter under `--local`.

Inside `runLocalReview`:

```go
// cfg is already loaded at line 370.
minSev, err := config.ResolveReviewMinSeverity(minSeverityFlag, repoPath, cfg)
if err != nil {
    return fmt.Errorf("resolve min-severity: %w", err)
}
// Later, when calling Build/BuildDirty:
reviewPrompt, err = pb.Build(repoPath, gitRef, 0, cfg.ReviewContextCount, a.Name(), reviewType, minSev)
// ... or pb.BuildDirty with the same trailing arg
```

`runLocalReview` needs the `minSeverityFlag` value plumbed in as a new parameter (matching how `agent`, `model`, etc. are already passed). Errors surface fast, matching `cmd/roborev/refine.go:387` and `cmd/roborev/fix.go:828`.

**Why the CLI does NOT call `ResolveReviewMinSeverity`:** doing so would freeze the cascade result at enqueue time. If the user later edits global config and reruns the same job (via `roborev rerun`), they expect the new global value to take effect. Storing only the explicit override on the job and letting the worker re-resolve preserves that behavior. This matches how `Reasoning` and `Model` are handled today.

Note: this differs from the daemon fix path (§3.5), which bakes severity into the prompt at enqueue. The asymmetry is intentional — review jobs build their prompt at worker time from git data, so re-resolution is free; fix jobs build their prompt at enqueue time from review output, so re-resolution would mean rebuilding the prompt.

### 5. Worker prompt injection

`internal/daemon/worker.go` resolves the effective severity right before building the prompt. The `cfg` snapshot is already taken at line 343 (`cfg := wp.cfgGetter.Config()`):

```go
// In the dirty/normal review branch (around lines 396–402),
// after the UsesStoredPrompt branch has been ruled out:
//
// Job-level override wins; otherwise walk the cascade.
// cfg is already snapshotted at line 343.
minSev := job.MinSeverity
if minSev == "" {
    resolved, resErr := config.ResolveReviewMinSeverity("", effectiveRepoPath, cfg)
    if resErr != nil {
        // Invalid configured severity is a visible error — do NOT silently
        // disable the filter. Fail the job the same way a bad reasoning
        // level or bad agent name would.
        log.Printf("[%s] Error resolving min-severity: %v", workerID, resErr)
        wp.failOrRetry(workerID, job, job.Agent, fmt.Sprintf("resolve min-severity: %v", resErr))
        return
    }
    minSev = resolved
}

if job.DiffContent != nil {
    reviewPrompt, err = pb.BuildDirty(effectiveRepoPath, *job.DiffContent, job.RepoID,
        cfg.ReviewContextCount, job.Agent, job.ReviewType, minSev)
} else {
    reviewPrompt, err = pb.Build(effectiveRepoPath, job.GitRef, job.RepoID,
        cfg.ReviewContextCount, job.Agent, job.ReviewType, minSev)
}
```

Both `pb.Build` and `pb.BuildDirty` need a new trailing `minSeverity string` parameter.

**Fail-fast rationale:** A typo in `review_min_severity` in global or per-repo config must not silently disable filtering — that would confuse the user into thinking their reviews are being filtered when they are not. Validation at resolve time is the trust boundary; past it, the worker treats any error as unrecoverable for this job. This matches the existing behavior in `cmd/roborev/refine.go:387` and `cmd/roborev/fix.go:828`, which both return resolver errors to the caller instead of swallowing them.

The `handleFixJob` callsite in §3.5 uses the same principle: a non-nil error from `ResolveFixMinSeverity` should return `writeError` to the caller (400 Bad Request) rather than proceeding with an empty filter. Update §3.5's snippet accordingly — no `_` discard on the error.

**Skipped job kinds:** `task`, `insights`, `compact`, and `fix` jobs all return `true` from `UsesStoredPrompt()`. They use pre-built prompts stored on the job and bypass `prompt.Build` / `prompt.BuildDirty` entirely. The `UsesStoredPrompt` branch in `worker.go` handles them and is not modified by this change. Severity handling for fix jobs has two distinct paths: (a) the daemon "Fix" button enqueues a fix job — §3.5 bakes severity into the stored prompt at enqueue time, the worker then runs it verbatim; (b) the `roborev fix` CLI invokes the agent directly via `fixJobDirect()` without ever touching the worker — `cmd/roborev/fix.go` already injects severity into `buildGenericFixPrompt` / `buildBatchFixPrompt`, the only change there is loading global `cfg` so the cascade can reach it. Task/insights/compact jobs intentionally have free-form prompts and never receive the severity instruction.

### 5.5. CI batch review path

`roborev ci` runs reviews without the daemon or worker — it calls `internal/review.RunBatch` which dispatches `runSingle` per agent x review-type. `runSingle` calls `builder.Build` directly (line 152), bypassing the worker's severity resolution. Without changes, adding the `minSeverity` parameter to `Build` would require passing `""` here, silently disabling severity filtering for all CI reviews.

Add `MinSeverity string` to `BatchConfig`. In `runSingle`, pass `cfg.MinSeverity` as the trailing argument to `builder.Build`. In `cmd/roborev/ci.go`, resolve via `config.ResolveReviewMinSeverity("", repoPath, cfg)` and set `BatchConfig.MinSeverity` to the result.

### 6. Prompt builder injection point

`internal/prompt/prompt.go`:

- `Builder.Build(repoPath, gitRef, repoID, contextCount, agentName, reviewType, minSeverity string)` — add the trailing param, thread into `buildSinglePrompt` / `buildRangePrompt`.
- `Builder.BuildDirty(...)` — same trailing param, inline injection.
- Internal helpers `buildSinglePrompt` (line 450) and `buildRangePrompt` (line 567) gain the trailing `minSeverity string` parameter.

**Injection site:** append the severity instruction to `requiredPrefix` immediately after `GetSystemPrompt(...)`, before any optional/trimmable sections:

```go
requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"
if inst := config.SeverityInstruction(minSeverity); inst != "" {
    requiredPrefix += inst + "\n"
}
```

Why `requiredPrefix`, not `optionalContext`: both `buildSinglePrompt` and `buildRangePrompt` compute `requiredLen := len(requiredPrefix) + currentRequired.Len() + currentOverflow.Len()` and use that to budget the diff (lines 508–509 and 626–627). `optionalContext` gets trimmed to fit remaining budget after the diff. If the severity instruction were in `optionalContext`, a large diff could silently truncate it — effectively disabling the filter without the user noticing. Putting it in `requiredPrefix` guarantees it's always included and correctly shrinks the diff budget instead.

For `BuildDirty`, the same logic applies: inject after `GetSystemPrompt(...)` construction (line 185) before `optionalContext` is built. `BuildDirty` has a similar budget calculation around `promptCap` that must account for the new prefix.

The pattern matches `BuildAddressPrompt` line 944–948, adapted for the required/optional split used by the single and range builders.

Affects all review types (standard, security, design) — the system prompt picker is unchanged, the severity instruction is appended after it.

### 7. Verdict handling

`internal/storage/verdict.go` — `ParseVerdict` currently flows: `hasSeverityLabel` → fail, else scan for pass phrases → pass, else fail. `SEVERITY_THRESHOLD_MET` is neither a pass phrase nor a severity label, so today it lands in the default fail branch.

Add the marker check **after** the `hasSeverityLabel` gate, not before it:

```go
func ParseVerdict(output string) string {
    // Severity labels are authoritative: if the agent listed actual findings,
    // don't let a stray marker mention flip to pass.
    if hasSeverityLabel(output) {
        return verdictFail
    }

    // Marker signals pass ONLY when no severity labels are present.
    // The SeverityInstruction contract is that the agent emits the marker
    // when all findings are sub-threshold — in that case there should be
    // no severity bullets at all.
    if strings.Contains(output, config.SeverityThresholdMarker) {
        return verdictPass
    }

    // ... existing pass-phrase loop unchanged
}
```

**Why the ordering matters:** If the marker check were at the top, a chatty agent that both mentions `SEVERITY_THRESHOLD_MET` and lists severity-labeled findings would flip to pass, silently suppressing real bugs. Putting the check after `hasSeverityLabel` keeps severity detection authoritative for mixed outputs and only trusts the marker when the output is clean.

This applies uniformly — if a refine or fix run somehow produces output that gets parsed by `ParseVerdict`, the same logic applies.

`SeverityThresholdMarker` lives in `internal/config`. `verdict.go` does not currently import `internal/config`, so this change adds a new import. There is no import cycle: `internal/storage` already imports `internal/config` from `db.go`, `sync.go`, and `syncworker.go`, and `internal/config` does not import `internal/storage` anywhere.

### 8. TUI surface

No new TUI work. The verdict change in §7 means `SEVERITY_THRESHOLD_MET` reviews flow through the normal "pass" rendering path. The `Verdict` field on `ReviewJob` is computed from the same code path; existing color/icon logic handles it for free.

## Test plan

| File | Test |
|------|------|
| `internal/config/config_test.go` | Update the `resolverFunc` type alias (line ~3130) and `runTests` helper (line ~3132) to accept the new `(explicit, repoPath string, globalCfg *Config)` signature. Extend the table with a `globalConfig` field and new cases: global-wins-when-repo-empty, repo-wins-over-global, explicit-wins-over-both, empty-everywhere. Add a third `runTests` call for the new `ResolveReviewMinSeverity`. |
| `internal/prompt/prompt_test.go` | New cases verifying `Build` and `BuildDirty` inject `SeverityInstruction` when `minSeverity != ""` and skip injection when empty. Verify all three review types (`""`, `security`, `design`). |
| `internal/storage/verdict_test.go` | New cases: marker alone → pass; marker plus severity-labeled findings → **fail** (severity-label detection is authoritative for mixed outputs); marker plus narrative text but no severity labels → pass; output with no marker and no findings → existing pass-phrase logic unchanged. |
| `internal/storage/db_migration_test.go` | Verify the new SQLite ALTER runs idempotently and adds the column with default `''`. |
| `internal/storage/postgres_test.go` | Verify v10→v11 migration adds `min_severity` and that `pgSchemaVersion` is 11. |
| `internal/storage/jobs_test.go` | Round-trip: enqueue with `EnqueueOpts{MinSeverity: "high"}`, claim, list, get-by-id — value preserved across all hydration paths. |
| `internal/daemon/worker_test.go` | (a) Worker resolves cascade when `job.MinSeverity == ""`: stub `cfgGetter` to return a config with `ReviewMinSeverity = "medium"`; verify the prompt contains `SeverityInstruction("medium")` text. (b) Worker fails the job with a clear error when the configured severity is invalid (e.g. `ReviewMinSeverity = "mideum"`) — does NOT silently proceed with empty filter. |
| `internal/daemon/server_fix_test.go` (or `server_test.go`) | (a) `handleFixJob` bakes severity instruction into stored prompt when global/repo config sets `fix_min_severity`. Use the existing daemon test scaffolding. (b) `handleFixJob` resolves config from `parentJob.WorktreePath` when present and valid — set up a parent job with a worktree path that has its own `.roborev.toml` with a different `fix_min_severity`, verify the stored prompt reflects the worktree value, not the main repo's. (c) `handleFixJob` SKIPS severity injection when the parent job is `JobTypeTask` or `JobTypeInsights`, even when global `fix_min_severity` is set — verify the stored prompt does NOT contain `SeverityInstruction` text. (d) **Legacy `IsTaskJob()` fallback shape**: build a parent job with `JobType == ""` and no `CommitID`/`DiffContent`/range-style `GitRef` so the legacy heuristic in `internal/storage/models.go:106` returns `true`. Verify `handleFixJob` still skips severity injection. This guards parity with sync-pulled jobs from older machines that pre-date the explicit `job_type` column. (e) Worktree-aware path resolution falls back to `RepoPath` when `WorktreePath` is set but no longer valid (mirror the worker's `ValidateWorktreeForRepo` guard). (f) `buildRebasePrompt` is NOT threaded with `minSeverity`: a `req.StaleJobID > 0` rebase request must produce a stored prompt that does NOT contain `SeverityInstruction` text, even when global `fix_min_severity = "high"` is set. |
| `cmd/roborev/review_test.go` | New cases: (a) `--min-severity=high` stores the value on the enqueued job; (b) bad values error before enqueue at the CLI; (c) `--min-severity=HIGH` (mixed case) stores the canonical lowercase `"high"` on the job — verifies normalization happens at the CLI boundary; (d) `--min-severity=" high "` (leading/trailing whitespace) likewise stores `"high"`; (e) `--local --min-severity=high` injects the severity instruction into the prompt without going through the daemon (use `runLocalReview` with a stub agent that captures the prompt); (f) `--local` with a bad `review_min_severity` in global config fails fast instead of running the review. |
| `internal/daemon/server_test.go` | (a) `handleEnqueue` accepts a valid `min_severity` and stores the canonical lowercase form on the job; (b) `handleEnqueue` accepts a mixed-case `min_severity=HIGH` and stores `"high"` (verifies the daemon trust boundary normalizes too, not just the CLI); (c) `handleEnqueue` rejects an invalid `min_severity=mideum` with 400 Bad Request — guards the case where a non-CLI client (sync, MCP, custom script) bypasses CLI normalization. |
| `cmd/roborev/fix_test.go` | Sanity check that loading `cfg` for `fixSingleJob` / `runFixBatch` doesn't break the existing happy path. The existing `IsTaskJob` skip in `cmd/roborev/fix.go:824` already has coverage; the daemon-side parity test lives in `server_fix_test.go` (case c above). |
| `internal/review/batch_test.go` | Verify `runSingle` injects severity instruction when `BatchConfig.MinSeverity` is set, and skips when empty. |

All tests use `assert`/`require` from testify per project convention.

## Migration / compatibility

- New SQLite column has `DEFAULT ''`, so old rows are valid immediately.
- New PG column same. Sync from older machines is safe via `COALESCE(j.min_severity, '')`.
- Existing per-repo `refine_min_severity` and `fix_min_severity` keys keep working unchanged — the new global tier is the lowest-precedence fallback.
- No CLI flag is removed. `roborev refine --min-severity` and `roborev fix --min-severity` keep their current behavior.
- Schema version bump in PG (v10 → v11). One direction works seamlessly: an upgraded local writing to an upgraded PG. If a stale machine writes a job after another machine has migrated, the column simply receives the default `''`. If the user runs an old binary against a newer PG, writes still succeed because the column has a default. This matches how prior column-add migrations have been handled (e.g., `provider`, `worktree_path`).

## Open questions

None remaining — CLI flag for review confirmed as Option A (new column).

## Files touched

| File | Change |
|------|--------|
| `internal/config/config.go` | 3 new global fields (`ReviewMinSeverity`, `RefineMinSeverity`, `FixMinSeverity`), 1 new repo field (`ReviewMinSeverity`), 1 new resolver (`ResolveReviewMinSeverity`), updated signatures on `ResolveRefineMinSeverity` and `ResolveFixMinSeverity` to accept `globalCfg *Config` |
| `internal/config/config_test.go` | Extend severity resolver test table to cover global tier + new review resolver |
| `internal/storage/db.go` | Append idempotent ALTER to add `min_severity` column to `review_jobs` |
| `internal/storage/postgres.go` | Bump `pgSchemaVersion` 10→11, update `//go:embed` directive to `postgres_v11.sql`, add v11 migration block, add `min_severity` to `UpsertJob` INSERT (test-only path), `BatchUpsertJobs` INSERT (**production sync push path**), and `PullJobs` SELECT, add `MinSeverity` field to `PulledJob` struct |
| `internal/storage/schemas/postgres_v11.sql` | New file copied from `postgres_v10.sql` with `min_severity TEXT NOT NULL DEFAULT ''` added to the `review_jobs` table definition |
| `internal/storage/sync.go` | Add `min_severity` to `GetJobsToSync` SELECT and `UpsertPulledJob` INSERT, add `MinSeverity` field to `SyncableJob` struct |
| `internal/storage/models.go` | `ReviewJob.MinSeverity` field |
| `internal/storage/hydration.go` | `MinSeverity string` field on `reviewJobScanFields` (plain string — queries use `COALESCE`), copy-through in `applyReviewJobScan` |
| `internal/storage/jobs.go` | `EnqueueOpts.MinSeverity`, INSERT in `EnqueueJob`, SELECT/scan in `ClaimJob` / `ListJobs` / `GetJobByID` |
| `internal/storage/reviews.go` | Add `j.min_severity` to SELECTs in `GetReviewByJobID`, `GetReviewByCommitSHA`, `GetJobsWithReviewsByIDs`, plus their scans |
| `internal/storage/verdict.go` | Marker → pass shortcut at top of `ParseVerdict`, new import of `internal/config` |
| `internal/storage/verdict_test.go` | New marker cases |
| `internal/storage/db_migration_test.go` | Idempotent ALTER coverage for `min_severity` |
| `internal/storage/postgres_test.go` | v11 migration coverage |
| `internal/prompt/prompt.go` | `minSeverity` parameter on `Build` / `BuildDirty` / `buildSinglePrompt` / `buildRangePrompt`, inject `SeverityInstruction` after guidelines |
| `internal/prompt/prompt_test.go` | Injection coverage for all three review types (default, security, design) |
| `internal/daemon/worker.go` | Resolve effective severity from `job.MinSeverity` → cascade, thread into `pb.Build` / `pb.BuildDirty` calls |
| `internal/daemon/server.go` | `EnqueueRequest` struct: add `MinSeverity string` field with `min_severity` JSON tag. `handleEnqueue`: validate `req.MinSeverity` via `config.NormalizeMinSeverity` near the existing `ResolveReviewReasoning` call (~line 866), return 400 Bad Request on invalid input, then pass the **normalized** value through all four review-branch `EnqueueOpts` literals (prompt/dirty/range/single). `handleFixJob`: skip resolution entirely when `parentJob.IsTaskJob()` (task/insights parents — mirrors `cmd/roborev/fix.go:824`); otherwise resolve `ResolveFixMinSeverity` against `parentJob.WorktreePath` when present and valid (via `gitpkg.ValidateWorktreeForRepo`), falling back to `parentJob.RepoPath` (mirrors `internal/daemon/worker.go:367–375`); pass result into `buildFixPrompt` / `buildFixPromptWithInstructions` only — `buildRebasePrompt` stays threshold-free because rebase prompts carry only a stale patch (no findings to filter against). Each findings-driven helper gains a trailing `minSeverity string` parameter and emits `SeverityInstruction` once (no-op when empty). |
| `internal/daemon/server_test.go` (or new test) | Verify: (a) `handleEnqueue` stores `MinSeverity` on the job when request includes `min_severity`; (b) `handleFixJob` bakes severity instruction into the stored prompt when configured |
| `cmd/roborev/review.go` | New `--min-severity` flag, validate via `NormalizeMinSeverity` (early error), set `min_severity` field on the `EnqueueRequest` JSON body sent to the daemon **using the normalized return value** (not the raw user input — see §4 for why this matters). Also thread the flag into `runLocalReview`: new `minSeverity string` parameter, call `ResolveReviewMinSeverity` with already-loaded `cfg` (fail-fast), pass result into `pb.Build`/`pb.BuildDirty`. The resolver always returns canonical values, so `runLocalReview` doesn't need a separate normalization step. |
| `cmd/roborev/review_test.go` | Flag handling: valid value stored, mixed-case input normalized to lowercase, whitespace trimmed, bad value errors before enqueue |
| `internal/review/batch.go` | Add `MinSeverity string` to `BatchConfig`, thread into `runSingle`'s `builder.Build` call. The CI batch review path (`roborev ci`) runs reviews without the daemon/worker, so it must resolve severity independently. |
| `cmd/roborev/ci.go` | Resolve `ResolveReviewMinSeverity("", repoPath, cfg)` and pass into `BatchConfig.MinSeverity` |
| `cmd/roborev/refine.go` | Pass already-loaded `cfg` into `ResolveRefineMinSeverity` |
| `cmd/roborev/fix.go` | Load `cfg` (or hoist existing load) and pass into `ResolveFixMinSeverity` at both `fixSingleJob` and `runFixBatch` callsites |
