# Add `verdict_bool` to `reviews` table

## Problem

Every time a review is read from the database, the system calls `ParseVerdict(output)` to derive a pass/fail verdict from the raw LLM text output. This is:

- Wasteful: `ParseVerdict` runs on every read, even though the output never changes after write
- Fragile in principle: if `ParseVerdict` logic changes, historical verdicts could silently change

The verdict is already deterministic at write time. Store it once.

## Solution

Add a `verdict_bool INTEGER` column to the `reviews` table, populated by `ParseVerdict()` at `CompleteJob()` / `CompleteFixJob()` time. On read, use the stored value when present; fall back to `ParseVerdict()` for legacy rows (NULL).

No prompt changes. No agent changes. No API changes.

## Implementation Steps

### Step 1: Add migration and model field

**Files:**
- `internal/storage/models.go` -- add `VerdictBool *int` field to `Review` struct
- `internal/storage/db.go` -- add migration in `migrate()` to `ALTER TABLE reviews ADD COLUMN verdict_bool INTEGER`

**Tests:** Existing migration tests cover that new columns don't break DB open. Add a case in `internal/storage/db_test.go` confirming the column exists after open.

**Verify:** `go test ./internal/storage/...`

### Step 2: Populate `verdict_bool` at write time

**Files:**
- `internal/storage/jobs.go` -- in both `CompleteJob()` and `CompleteFixJob()`, compute `ParseVerdict(finalOutput)` and include `verdict_bool` in the INSERT

**Logic:** Map `"P"` to `1`, `"F"` to `0`. Always populate (the read side already skips verdicts for task jobs).

**Tests:** `CompleteJob()` then query `SELECT verdict_bool FROM reviews WHERE job_id = ?` and assert the value matches `ParseVerdict()` of the output.

**Verify:** `go test ./internal/storage/...`

### Step 3: Read `verdict_bool` on retrieval, skip `ParseVerdict()` when present

**Files:**
- `internal/storage/reviews.go` -- in `GetReviewByJobID()`, `GetReviewByCommitSHA()`, and `GetJobsWithReviewsByIDs()`: add `rv.verdict_bool` to SELECT, scan it, and use it instead of calling `ParseVerdict(r.Output)` when non-NULL

**Fallback:** If `verdict_bool` IS NULL (legacy rows), fall back to `ParseVerdict()`. Pre-migration data works transparently.

**Tests:** Test both paths -- a review with `verdict_bool` set returns the stored value, and a review with NULL `verdict_bool` derives it from output.

**Verify:** `go test ./internal/storage/...` then `go test ./...`

## Design Decisions

**Why not backfill legacy rows?** Legacy rows stay NULL and use the `ParseVerdict()` fallback forever. The fallback is one `if` check and `ParseVerdict` is fast. No reason to add migration complexity.

**Why `INTEGER` not `BOOLEAN`?** SQLite has no boolean type. `1` = pass, `0` = fail, `NULL` = legacy/unknown. This matches the existing `addressed INTEGER` pattern.

**Why store on the `reviews` table, not `review_jobs`?** The verdict belongs to the review output, not the job. The `review_jobs.Verdict` field remains a computed/joined convenience field for API compatibility.

## Files Changed

| File | Change |
|------|--------|
| `internal/storage/models.go` | Add `VerdictBool` field to `Review` |
| `internal/storage/db.go` | Add migration |
| `internal/storage/jobs.go` | Populate at `CompleteJob` / `CompleteFixJob` |
| `internal/storage/reviews.go` | Read and use stored verdict |
| `internal/storage/db_test.go` | Migration + write tests |
| `internal/storage/reviews_test.go` | Read path tests |
