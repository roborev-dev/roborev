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
| `cmd/roborev/refine.go:384` | Pass `cfg` (already loaded at line 351) into `ResolveRefineMinSeverity` |
| `cmd/roborev/fix.go:825` | Load global cfg if not already, pass into `ResolveFixMinSeverity` |
| `cmd/roborev/fix.go:1013` | Same |
| `cmd/roborev/review.go` | New `--min-severity` flag — validates via `NormalizeMinSeverity`, stores raw value on `ReviewJob.MinSeverity`. Does NOT call the resolver — see §4. |
| `internal/daemon/worker.go:398/401` | New call to `ResolveReviewMinSeverity` before building the review prompt |

### 3. Storage: per-job override column

To support `roborev review --min-severity=high` end-to-end, the daemon worker needs to know the per-invocation override. The CLI enqueues a job and exits; the worker picks it up later. The cleanest mirror of how `reasoning`, `model`, and `agent` are already plumbed is a new column on `review_jobs`.

**SQLite migration** (`internal/storage/db.go`) — append a new idempotent `ALTER TABLE` to the `migrate()` chain, following the same `pragma_table_info` guard pattern used for `reasoning`, `agentic`, etc.:

```sql
ALTER TABLE review_jobs ADD COLUMN min_severity TEXT NOT NULL DEFAULT ''
```

**PostgreSQL migration** (`internal/storage/postgres.go`) — bump `pgSchemaVersion` from 10 to 11, add a new `if currentVersion < 11 { ... }` block:

```sql
ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS min_severity TEXT NOT NULL DEFAULT ''
```

Update the PG sync `INSERT` (around line 555) and `SELECT` (around line 659) lists to include `min_severity`. Use `COALESCE(j.min_severity, '')` on the SELECT side for safety, matching the established pattern for `model`, `provider`, and `requested_model`.

**Model field** (`internal/storage/models.go`) — add to `ReviewJob`:

```go
MinSeverity string `json:"min_severity,omitempty"`
```

**Job CRUD** (`internal/storage/jobs.go`) — wire `min_severity` into the `INSERT` statement, the row scanner, and any `EnqueueJob`-style helpers. Existing patterns from `reasoning` and `model` apply line-for-line.

### 4. CLI flag for `roborev review`

Add `--min-severity` to `cmd/roborev/review.go`. The flag value:

1. Validates via `NormalizeMinSeverity` (early error on bad input).
2. Gets stored as the literal user-supplied value on the enqueued `ReviewJob.MinSeverity` field.
3. Empty (flag not set) means "fall back to cascade resolution at worker time."

Why the CLI does NOT call `ResolveReviewMinSeverity`: doing so would freeze the cascade result at enqueue time. If the user later edits global config and reruns the same job (via `roborev rerun`), they expect the new global value to take effect. Storing only the explicit override on the job and letting the worker re-resolve preserves that behavior. This matches how `Reasoning` and `Model` are handled today.

### 5. Worker prompt injection

`internal/daemon/worker.go` resolves the effective severity right before building the prompt:

```go
// Job-level override wins; otherwise walk the cascade.
minSev := job.MinSeverity
if minSev == "" {
    resolved, err := config.ResolveReviewMinSeverity("", effectiveRepoPath, cfg)
    if err == nil {
        minSev = resolved
    }
    // resolution errors are non-fatal: empty string = no filter
}

reviewPrompt, err = pb.Build(effectiveRepoPath, job.GitRef, job.RepoID,
    cfg.ReviewContextCount, job.Agent, job.ReviewType, minSev)
```

Both `pb.Build` and `pb.BuildDirty` need a new trailing `minSeverity string` parameter.

**Skipped job kinds:** `task` and `compact` jobs use stored prompts and bypass `prompt.go` entirely. They keep their stored prompts unchanged — custom prompts should not be silently mutated. `fix` jobs already have their own resolution path via `BuildAddressPrompt`.

### 6. Prompt builder injection point

`internal/prompt/prompt.go`:

- `Builder.Build(repoPath, gitRef, repoID, contextCount, agentName, reviewType, minSeverity string)` — add the trailing param. Inside, after the system prompt and project guidelines but before the diff, inject `config.SeverityInstruction(minSeverity)` if non-empty. Same pattern `BuildAddressPrompt` uses at line 944.
- `Builder.BuildDirty(...)` — same trailing param, same injection between guidelines and the dirty diff section.
- Internal helpers `buildSinglePrompt` and `buildRangePrompt` thread `minSeverity` through.

Affects all review types (standard, security, design) — the system prompt picker is unchanged, the severity instruction is appended after it.

### 7. Verdict handling

`internal/storage/verdict.go` — `ParseVerdict` currently only sees severity-labeled bullets and returns fail for the marker case, since `SEVERITY_THRESHOLD_MET` is neither a pass phrase nor a severity label.

Add an early check at the top of `ParseVerdict`:

```go
if strings.Contains(output, SeverityThresholdMarker) {
    return verdictPass
}
```

The marker is a contract: the agent only emits it when the severity instruction told it to and it judges all findings sub-threshold. Trust the marker. This applies uniformly — if a refine or fix run somehow produces output that gets parsed by `ParseVerdict`, the same logic applies.

`SeverityThresholdMarker` lives in `internal/config`, which `internal/storage` already imports for `NormalizeMinSeverity`, so no new import cycles.

### 8. TUI surface

No new TUI work. The verdict change in §7 means `SEVERITY_THRESHOLD_MET` reviews flow through the normal "pass" rendering path. The `Verdict` field on `ReviewJob` is computed from the same code path; existing color/icon logic handles it for free.

## Test plan

| File | Test |
|------|------|
| `internal/config/config_test.go` | Extend the table-driven `runTests` in the existing `TestResolve*MinSeverity` block to cover global tier for refine/fix and the new review resolver. New cases: explicit-wins, repo-wins, global-wins, empty-everywhere, invalid-input-error. |
| `internal/prompt/prompt_test.go` | New cases verifying `Build` and `BuildDirty` inject `SeverityInstruction` when `minSeverity != ""` and skip injection when empty. Verify all three review types (`""`, `security`, `design`). |
| `internal/storage/verdict_test.go` | New cases: marker alone → pass; marker + sub-threshold severity bullets → pass; marker + above-threshold severity bullets → still pass (we trust the marker). |
| `internal/storage/db_migration_test.go` | Verify the new ALTER runs idempotently and adds the column with default `''`. |
| `internal/storage/postgres_test.go` | Verify v10→v11 migration adds the column and that `pgSchemaVersion` was bumped. |
| `cmd/roborev/review_test.go` | New case: `--min-severity=high` stores the value on the enqueued job; bad values error before enqueue. |
| `cmd/roborev/refine_test.go` (or fix_test.go) | Sanity check that the new `cfg` parameter doesn't break existing flows when global is unset. |

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
| `internal/config/config.go` | 3 new global fields, 1 new repo field, 1 new resolver, 2 updated resolvers |
| `internal/config/config_test.go` | Extend severity resolver test table |
| `internal/storage/db.go` | Append idempotent ALTER to add `min_severity` column |
| `internal/storage/postgres.go` | Bump `pgSchemaVersion` to 11, add v11 migration, update sync INSERT/SELECT |
| `internal/storage/models.go` | `ReviewJob.MinSeverity` field |
| `internal/storage/jobs.go` | INSERT/scan plumbing |
| `internal/storage/verdict.go` | Marker → pass shortcut in `ParseVerdict` |
| `internal/storage/verdict_test.go` | New marker cases |
| `internal/storage/db_migration_test.go` | Migration #19 idempotency |
| `internal/storage/postgres_test.go` | v7 migration coverage |
| `internal/prompt/prompt.go` | `minSeverity` parameter on `Build` / `BuildDirty` and helpers; injection |
| `internal/prompt/prompt_test.go` | Injection coverage |
| `internal/daemon/worker.go` | Resolve + thread `minSeverity` into builder calls |
| `cmd/roborev/review.go` | New `--min-severity` flag, validate, store on job |
| `cmd/roborev/review_test.go` | Flag handling |
| `cmd/roborev/refine.go` | Pass `cfg` into `ResolveRefineMinSeverity` |
| `cmd/roborev/fix.go` | Load + pass `cfg` into `ResolveFixMinSeverity` (two callsites) |
