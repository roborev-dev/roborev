# Auto Design Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Opt-in per-commit decision (heuristics + classifier fallback) for whether a commit warrants a design review, applied in both the local enqueue path and the CI poller.

**Architecture:** Single helper package `internal/review/autotype/` called from the daemon's enqueue handler and the CI poller. Heuristics run synchronously during enqueue; ambiguous cases enqueue a new `job_type = "classify"` job tagged `source = "auto_design"`. When the worker completes the classify job, it **converts the same row in place** (UPDATE, never INSERT) into either a queued design review (`job_type="review"`, `status="queued"`) or a terminal skipped design row (`status="skipped"`, `skip_reason` populated). The in-place conversion avoids a partial-unique-index collision with the classify row the worker is replacing. A new `SchemaAgent` agent capability lets claude-code (`--json-schema`) and codex (`--output-schema`) return JSON conforming to an embedded schema; agents without that capability are rejected at `ResolveClassifyAgent`.

**Tech Stack:** Go 1.25, `github.com/bmatcuk/doublestar/v4`, SQLite (modernc), PostgreSQL (pgx), Cobra, Bubbletea.

**Spec:** `docs/superpowers/specs/2026-04-17-auto-design-review-design.md`

**Deviation from spec:** Classify workflow fields (`classify_agent`, `classify_model`, `classify_reasoning`, `classify_backup_agent`, `classify_backup_model`) live at the **top level** of `Config` / `RepoConfig` rather than nested under `[auto_design_review]`, to match the existing workflow-config pattern (`fix_agent`, `review_agent`, etc.) that `lookupFieldByTag` reflects over. The `[auto_design_review]` section holds only the `enabled` flag and heuristic knobs.

**Stages (per spec rework after review):**

1. **Spike** — verify `claude --json-schema` and `codex exec --output-schema` behave as assumed (Task 0).
2. **Terminal-state plumbing** — teach every hardcoded `('done','failed','canceled')` filter to also accept `'skipped'` (Tasks 2b, 2c, 2d, 2e) BEFORE any skipped row is produced in real code paths.
3. **Storage/schema** — migrations, model fields, dedup unique index (Tasks 2, 3, 3a, 3b, 4).
4. **Config + resolvers** — new section, tri-state enabled flag, classify workflow fields, Resolve functions including timeout + max-prompt (Tasks 11-14).
5. **autotype** — heuristics + orchestration (Tasks 5-10).
6. **Classifier agent capability** — SchemaAgent interface, claude/codex implementations, classify prompt (Tasks 15-18).
7. **Daemon integration** — classifier bridge, worker classify handler, insert/enqueue helpers, enqueue-handler wiring, CI poller wiring (Tasks 19-23).
8. **TUI + PR synthesis + observability** — render skipped rows (Task 24), include skipped rows in PR synthesis (Task 24a), decision counters on `/api/status` (Task 24b).
9. **End-to-end verification + docs** — full test suite + lint (Task 25), user-facing docs (Task 26).

Task numbering below largely preserves the original ordering; new tasks are inserted with letter suffixes (2b, 2c, 2d, 2e, 3a, 3b, 24a, 24b) at the right position.

---

## Phase 0 — Spike: verify agent CLI flags actually work

### Task 0: CLI flag compatibility spike

**Files:** (none committed; this is a confirmation step whose output informs Tasks 16 and 17).

- [ ] **Step 1: Validate `claude --json-schema`**

Run (in any test directory):
```bash
printf 'Return a JSON value with field "ok" set to true.' | claude -p --output-format stream-json --verbose --json-schema '{"type":"object","additionalProperties":false,"required":["ok"],"properties":{"ok":{"type":"boolean"}}}' --dangerously-skip-permissions
```

Expected: stream-json output ending with a `result` event whose `result` field is JSON matching the schema, e.g. `{"ok": true}`. If the CLI errors with "unknown flag" or similar, update your `claude` CLI (>= the version that shipped `--json-schema`, see https://code.claude.com/docs/en/cli-reference) before continuing with Task 16.

- [ ] **Step 2: Validate `codex exec --output-schema`**

Run:
```bash
tmp=$(mktemp -d)
cat > "$tmp/schema.json" <<'EOF'
{"type":"object","additionalProperties":false,"required":["ok"],"properties":{"ok":{"type":"boolean"}}}
EOF
printf 'Return a JSON value with field "ok" set to true.' | codex exec --output-schema "$tmp/schema.json" --output-last-message "$tmp/out.json" --sandbox read-only
cat "$tmp/out.json"
```

Expected: `out.json` contains `{"ok": true}` (or similar). Known issue: some codex-family models silently ignore `--output-schema` (https://github.com/openai/codex/issues/4181). If this reproduces, pick a model slug that starts with `gpt-5` (not a codex preset) or remove codex from the classifier implementation plan and update Task 17 accordingly.

- [ ] **Step 3: Record findings**

If either command requires a different model/version/flag than Tasks 16/17 assume, update those tasks' command lines before you start implementing them. No commit — this task only produces knowledge.

---

## Phase 1 — Foundation

### Task 1: Add doublestar dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get github.com/bmatcuk/doublestar/v4@v4.9.1
```

Expected: `go.mod` gains a line `github.com/bmatcuk/doublestar/v4 v4.9.1` under `require`; `go.sum` updates.

- [ ] **Step 2: Verify build still passes**

Run:
```bash
go build ./...
```
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add doublestar/v4 for ** glob matching"
```

---

### Task 2: Add storage model constants and SkipReason field

**Files:**
- Modify: `internal/storage/models.go`
- Test: `internal/storage/models_test.go` (create if absent)

- [ ] **Step 1: Write failing test for new constants**

Create or append to `internal/storage/models_test.go`:

```go
package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobStatusSkipped(t *testing.T) {
	assert.Equal(t, JobStatus("skipped"), JobStatusSkipped)
}

func TestJobTypeClassify(t *testing.T) {
	assert.Equal(t, "classify", JobTypeClassify)
}

func TestReviewJobHasSkipReason(t *testing.T) {
	j := ReviewJob{}
	j.SkipReason = "trivial diff"
	assert.Equal(t, "trivial diff", j.SkipReason)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
go test ./internal/storage/ -run 'TestJobStatusSkipped|TestJobTypeClassify|TestReviewJobHasSkipReason'
```
Expected: compilation error (`undefined: JobStatusSkipped`, `undefined: JobTypeClassify`, `j.SkipReason undefined`).

- [ ] **Step 3: Add constants and field**

Edit `internal/storage/models.go`:

Append inside the existing `const (...)` block for `JobStatus`:
```go
JobStatusSkipped  JobStatus = "skipped"
```

Append inside the existing `const (...)` block for job types:
```go
JobTypeClassify = "classify" // Routing classifier that decides whether to enqueue a design review
```

Add a field on `ReviewJob` struct (place right after `OutputPrefix`):
```go
SkipReason    string    `json:"skip_reason,omitempty"` // Reason a design review was skipped (status=skipped only)
```

- [ ] **Step 4: Verify tests pass**

Run:
```bash
go test ./internal/storage/ -run 'TestJobStatusSkipped|TestJobTypeClassify|TestReviewJobHasSkipReason'
```
Expected: PASS.

- [ ] **Step 5: Run full storage test suite to verify no regressions**

Run:
```bash
go test ./internal/storage/
```
Expected: PASS (existing tests should still pass since the new field is optional).

- [ ] **Step 6: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/models.go internal/storage/models_test.go
git commit -m "storage: add JobStatusSkipped, JobTypeClassify, SkipReason field"
```

---

### Task 3: SQLite migration — extend status CHECK + add skip_reason column

Note: `job_type` is a plain `TEXT NOT NULL DEFAULT 'review'` column in the existing schema — it has NO CHECK constraint. So `'classify'` is accepted without a schema change. The new `JobTypeClassify` constant (added in Task 2) is only a Go-side convention.

**Files:**
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/db_test.go`

- [ ] **Step 1: Write failing test — new CHECK constraints accepted**

Append to `internal/storage/db_test.go`:

```go
func TestMigration19_SkippedStatusAndClassifyType(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// repo + commit
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO repos (root_path, name) VALUES ('/tmp/r', 'r')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO commits (repo_id, sha, author, subject, timestamp)
		 VALUES (1, 'deadbeef', 'a', 's', '2026-04-17T00:00:00Z')`)
	require.NoError(t, err)

	// status=skipped accepted
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO review_jobs (repo_id, commit_id, git_ref, status,
		 review_type, skip_reason) VALUES (1, 1, 'deadbeef', 'skipped',
		 'design', 'trivial diff')`)
	require.NoError(t, err)

	// job_type=classify accepted
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO review_jobs (repo_id, commit_id, git_ref, status,
		 job_type) VALUES (1, 1, 'deadbeef', 'queued', 'classify')`)
	require.NoError(t, err)

	// skip_reason column round-trip
	var reason string
	err = db.QueryRowContext(context.Background(),
		`SELECT skip_reason FROM review_jobs WHERE status = 'skipped'`,
	).Scan(&reason)
	require.NoError(t, err)
	assert.Equal(t, "trivial diff", reason)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run TestMigration19_SkippedStatusAndClassifyType -v
```
Expected: FAIL with CHECK constraint violation or `no such column: skip_reason`.

- [ ] **Step 3: Add the migration function**

Append to `internal/storage/db.go`:

```go
// migrateReviewJobsConstraintsForAutoDesign rebuilds review_jobs to:
//   - Add 'skipped' to the status CHECK constraint
//   - Add the skip_reason TEXT column
// (job_type has no CHECK constraint in the existing schema, so 'classify'
// is accepted without a rebuild.)
// Idempotent: inspects the current table SQL and skips if both updates are
// already present.
func (db *DB) migrateReviewJobsConstraintsForAutoDesign() error {
	ctx := context.Background()

	var origSQL string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='review_jobs'`,
	).Scan(&origSQL); err != nil {
		return fmt.Errorf("read review_jobs schema: %w", err)
	}
	alreadyDone := strings.Contains(origSQL, "'skipped'") &&
		strings.Contains(origSQL, "skip_reason")
	if alreadyDone {
		return nil
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			return
		}
	}()

	if _, err := tx.Exec(`DROP TABLE IF EXISTS review_jobs_new`); err != nil {
		return fmt.Errorf("cleanup stale temp table: %w", err)
	}

	rows, err := tx.Query(`SELECT name FROM pragma_table_info('review_jobs')`)
	if err != nil {
		return fmt.Errorf("read columns: %w", err)
	}
	var cols []string
	hasSkipReason := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		cols = append(cols, name)
		if name == "skip_reason" {
			hasSkipReason = true
		}
	}
	rows.Close()

	// Read current SQL and update the CHECK constraint.
	newSQL := strings.Replace(origSQL,
		"CHECK(status IN ('queued','running','done','failed','canceled','applied','rebased'))",
		"CHECK(status IN ('queued','running','done','failed','canceled','applied','rebased','skipped'))",
		1)
	// Add skip_reason column if missing.
	if !hasSkipReason {
		// Insert `skip_reason TEXT` before the closing `)`.
		lastParen := strings.LastIndex(newSQL, ")")
		if lastParen < 0 {
			return fmt.Errorf("malformed review_jobs schema")
		}
		newSQL = newSQL[:lastParen] + ",\n  skip_reason TEXT" + newSQL[lastParen:]
	}

	// Rename to temp table.
	replaced := false
	for _, pattern := range []string{
		`CREATE TABLE "review_jobs"`,
		`CREATE TABLE review_jobs`,
	} {
		if strings.Contains(newSQL, pattern) {
			newSQL = strings.Replace(newSQL, pattern, `CREATE TABLE review_jobs_new`, 1)
			replaced = true
			break
		}
	}
	if !replaced {
		return fmt.Errorf("cannot find CREATE TABLE statement in schema: %s",
			origSQL[:min(len(origSQL), 80)])
	}

	if _, err := tx.Exec(newSQL); err != nil {
		return fmt.Errorf("create new table: %w", err)
	}

	// Copy existing columns (skip_reason will be NULL for old rows).
	colList := strings.Join(cols, ", ")
	copySQL := fmt.Sprintf(`INSERT INTO review_jobs_new (%s) SELECT %s FROM review_jobs`,
		colList, colList)
	if _, err := tx.Exec(copySQL); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE review_jobs`); err != nil {
		return fmt.Errorf("drop old table: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE review_jobs_new RENAME TO review_jobs`); err != nil {
		return fmt.Errorf("rename table: %w", err)
	}

	// Recreate the same indexes the other rebuild migrations recreate.
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_review_jobs_status ON review_jobs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_review_jobs_repo ON review_jobs(repo_id)`,
		`CREATE INDEX IF NOT EXISTS idx_review_jobs_git_ref ON review_jobs(git_ref)`,
		`CREATE INDEX IF NOT EXISTS idx_review_jobs_branch ON review_jobs(branch)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_uuid ON review_jobs(uuid)`,
	} {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("recreate index: %w", err)
		}
	}

	return tx.Commit()
}
```

- [ ] **Step 4: Register the migration**

In `internal/storage/db.go`, find the `migrate()` method and add a call to the new function near the bottom of the method (after existing migrations, before the final `return nil`):

```go
	// Auto design review support: extends status CHECK constraint,
	// adds skip_reason and source columns, creates dedup indexes.
	// (job_type has no CHECK constraint; 'classify' is accepted as-is.)
	if err := db.migrateReviewJobsConstraintsForAutoDesign(); err != nil {
		return fmt.Errorf("migrate review_jobs constraints for auto design: %w", err)
	}
```

- [ ] **Step 5: Also include skip_reason in the fresh schema**

In `internal/storage/db.go`, update the `schema` constant's `review_jobs` block:
- Change the `status` CHECK to include `'skipped'`:
  ```
  status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed','canceled','applied','rebased','skipped')) DEFAULT 'queued',
  ```
- Add the column near the bottom of the table:
  ```
  skip_reason TEXT,
  ```

- [ ] **Step 6: Run the test**

Run:
```bash
go test ./internal/storage/ -run TestMigration19_SkippedStatusAndClassifyType -v
```
Expected: PASS.

- [ ] **Step 7: Run full storage suite + test that migration is idempotent**

Run:
```bash
go test ./internal/storage/
```
Expected: PASS.

Add a second-run idempotency test (append to `db_test.go`):

```go
func TestMigration19_Idempotent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// First run happens implicitly at openTestDB; run again manually.
	require.NoError(t, db.migrateReviewJobsConstraintsForAutoDesign())
	require.NoError(t, db.migrateReviewJobsConstraintsForAutoDesign())

	// Still accepts skipped + classify.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO repos (root_path, name) VALUES ('/tmp/r2', 'r2')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO commits (repo_id, sha, author, subject, timestamp)
		 VALUES (1, 'abc', 'a', 's', '2026-04-17T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO review_jobs (repo_id, commit_id, git_ref, status)
		 VALUES (1, 1, 'abc', 'skipped')`)
	require.NoError(t, err)
}
```

Run:
```bash
go test ./internal/storage/ -run TestMigration19
```
Expected: both PASS.

- [ ] **Step 8: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/db.go internal/storage/db_test.go
git commit -m "storage: migrate review_jobs for skipped status, classify job type, skip_reason"
```

---

### Task 2b: Teach `GetStaleBatches` to treat `skipped` as terminal

**Files:**
- Modify: `internal/storage/ci.go`
- Modify: `internal/storage/ci_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/storage/ci_test.go`:

```go
func TestGetStaleBatches_TreatsSkippedAsTerminal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Set up a batch whose only job is status=skipped.
	repoID := createRepo(t, db, "/tmp/repo-skipped-batch").ID
	commitID := createCommit(t, db, repoID, "abc123").ID
	var jobID int64
	err := db.QueryRow(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type)
		VALUES (?, ?, 'abc123', 'skipped', 'design') RETURNING id
	`, repoID, commitID).Scan(&jobID)
	require.NoError(t, err)

	var batchID int64
	err = db.QueryRow(`
		INSERT INTO ci_pr_batches (github_repo, pr_number, head_sha, total_jobs, completed_jobs)
		VALUES ('x/y', 1, 'abc123', 1, 0) RETURNING id
	`).Scan(&batchID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ci_pr_batch_jobs (batch_id, job_id) VALUES (?, ?)`, batchID, jobID)
	require.NoError(t, err)

	batches, err := db.GetStaleBatches()
	require.NoError(t, err)

	found := false
	for _, b := range batches {
		if b.ID == batchID {
			found = true
		}
	}
	assert.True(t, found, "skipped-only batch must be considered terminal")
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run TestGetStaleBatches_TreatsSkippedAsTerminal
```
Expected: FAIL — current query filters skipped jobs out of the terminal set.

- [ ] **Step 3: Update the query**

In `internal/storage/ci.go`, find the `GetStaleBatches` function and change the NOT IN clause:

```go
AND j.status NOT IN ('done', 'failed', 'canceled', 'skipped')
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/storage/ -run TestGetStaleBatches
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/ci.go internal/storage/ci_test.go
git commit -m "storage: treat skipped as terminal in GetStaleBatches"
```

---

### Task 2c: Teach `ReconcileBatch` to count `skipped` toward completion

**Files:**
- Modify: `internal/storage/ci.go`
- Modify: `internal/storage/ci_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/storage/ci_test.go`:

```go
func TestReconcileBatch_CountsSkippedAsCompleted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-recon").ID
	commitID := createCommit(t, db, repoID, "cafef00d").ID
	var jobID int64
	err := db.QueryRow(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type)
		VALUES (?, ?, 'cafef00d', 'skipped', 'design') RETURNING id
	`, repoID, commitID).Scan(&jobID)
	require.NoError(t, err)

	var batchID int64
	err = db.QueryRow(`
		INSERT INTO ci_pr_batches (github_repo, pr_number, head_sha, total_jobs)
		VALUES ('x/y', 2, 'cafef00d', 1) RETURNING id
	`).Scan(&batchID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ci_pr_batch_jobs (batch_id, job_id) VALUES (?, ?)`, batchID, jobID)
	require.NoError(t, err)

	batch, err := db.ReconcileBatch(batchID)
	require.NoError(t, err)
	assert.Equal(t, 1, batch.CompletedJobs)
	assert.Equal(t, 0, batch.FailedJobs)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run TestReconcileBatch_CountsSkippedAsCompleted
```
Expected: FAIL — skipped isn't counted anywhere.

- [ ] **Step 3: Update the query**

In `internal/storage/ci.go`, `ReconcileBatch` counting query — change `'done'` to `('done', 'skipped')`:

```go
err = tx.QueryRow(`
    SELECT
        COALESCE(SUM(CASE WHEN j.status IN ('done','skipped') THEN 1 ELSE 0 END), 0),
        COALESCE(SUM(CASE WHEN j.status IN ('failed', 'canceled') THEN 1 ELSE 0 END), 0)
    FROM ci_pr_batch_jobs bj
    JOIN review_jobs j ON j.id = bj.job_id
    WHERE bj.batch_id = ?`, batchID).Scan(&completed, &failed)
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/storage/ -run TestReconcileBatch
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/ci.go internal/storage/ci_test.go
git commit -m "storage: count skipped toward ReconcileBatch completed tally"
```

---

### Task 2d: Sync `skipped` rows to Postgres

**Files:**
- Modify: `internal/storage/sync.go`
- Modify: `internal/storage/sync_queries_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/storage/sync_queries_test.go`:

```go
func TestSyncableJobs_IncludesSkipped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-sync-skipped").ID
	commitID := createCommit(t, db, repoID, "deadf00d").ID
	_, err := db.Exec(`
		INSERT INTO review_jobs
		  (repo_id, commit_id, git_ref, status, review_type, uuid, source_machine_id)
		VALUES (?, ?, 'deadf00d', 'skipped', 'design', 'test-uuid-1', 'test-machine')
	`, repoID, commitID)
	require.NoError(t, err)

	jobs, err := db.GetSyncableJobs("test-machine", 100)
	require.NoError(t, err)
	found := false
	for _, j := range jobs {
		if j.UUID == "test-uuid-1" {
			found = true
			assert.Equal(t, "skipped", string(j.Status))
		}
	}
	assert.True(t, found, "expected skipped job to be syncable")
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run TestSyncableJobs_IncludesSkipped
```
Expected: FAIL.

- [ ] **Step 3: Update the query**

In `internal/storage/sync.go`, the syncable-jobs query — include `'skipped'`:

```sql
WHERE j.status IN ('done', 'failed', 'canceled', 'skipped')
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/storage/ -run TestSyncableJobs
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/sync.go internal/storage/sync_queries_test.go
git commit -m "storage: sync skipped rows to postgres"
```

---

### Task 2e: Remaining terminal-state filter call sites

**Files:**
- Modify: `internal/storage/ci.go`
- Modify: `internal/storage/jobs.go`
- Modify: `internal/storage/ci_test.go`
- Modify: `internal/storage/jobs_test.go`

Tasks 2b-2d covered `GetStaleBatches`, `ReconcileBatch`, and the sync query. grep on the storage package surfaces additional call sites that also need to accept `'skipped'`:

- `internal/storage/ci.go` — a second query in/near `GetStaleBatches` using `status NOT IN ('done','failed','canceled')` (identify via `grep -n "status NOT IN" internal/storage/ci.go`)
- `internal/storage/ci.go` — `HasMeaningfulBatchResult`'s `status IN ('done','failed')` clause (note: `IsBatchExpired` checks batch age only and has no status filter — don't touch it)
- `internal/storage/ci.go` — `GetExpiredBatches`'s two clauses (`status IN ('done','failed')` and `status NOT IN ('done','failed','canceled')`)
- `internal/storage/jobs.go` — `RerunJob`'s `WHERE ... status IN ('done','failed','canceled')` (required for the spec's §Rerun semantics — users rerun skipped design rows in place)

- [ ] **Step 1: Enumerate the call sites**

Run:
```bash
grep -n "status NOT IN\|status IN" internal/storage/ci.go internal/storage/jobs.go
```

Expected: the list above plus `GetStaleBatches`/`ReconcileBatch`/sync (already fixed) and `CancelJob`'s `status IN ('queued','running')` (NOT terminal — leave alone). Every other terminal filter should be updated in this task.

- [ ] **Step 2: Write failing test for RerunJob**

Append to `internal/storage/jobs_test.go`:

```go
func TestRerunJob_AcceptsSkipped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-rerun-skipped").ID
	commitID := createCommit(t, db, repoID, "feed").ID

	// Insert an auto-design skipped row.
	res, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source, skip_reason)
		VALUES (?, ?, 'feed', 'skipped', 'design', 'auto_design', 'trivial')
	`, repoID, commitID)
	require.NoError(t, err)
	jobID, _ := res.LastInsertId()

	// Rerun should accept the skipped row and move it to queued.
	require.NoError(t, db.RerunJob(jobID, RerunOpts{}))

	j, err := db.GetJobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusQueued, j.Status)
	assert.Equal(t, "auto_design", j.Source, "source preserved across rerun")
}
```

(The exact `RerunJob` signature may differ — match the existing one. If it takes individual args instead of an opts struct, pass equivalent values.)

- [ ] **Step 3: Write failing test for `HasMeaningfulBatchResult`**

Append to `internal/storage/ci_test.go`:

```go
func TestHasMeaningfulBatchResult_CountsSkipped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-meaningful").ID
	commitID := createCommit(t, db, repoID, "dead").ID

	var jobID int64
	err := db.QueryRow(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type)
		VALUES (?, ?, 'dead', 'skipped', 'design') RETURNING id
	`, repoID, commitID).Scan(&jobID)
	require.NoError(t, err)

	var batchID int64
	err = db.QueryRow(`
		INSERT INTO ci_pr_batches (github_repo, pr_number, head_sha, total_jobs)
		VALUES ('x/y', 3, 'dead', 1) RETURNING id
	`).Scan(&batchID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ci_pr_batch_jobs (batch_id, job_id) VALUES (?, ?)`, batchID, jobID)
	require.NoError(t, err)

	meaningful, err := db.HasMeaningfulBatchResult(batchID)
	require.NoError(t, err)
	assert.True(t, meaningful, "batch with a skipped auto-design row has a meaningful terminal outcome")
}
```

- [ ] **Step 4: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run 'TestRerunJob_AcceptsSkipped|TestHasMeaningfulBatchResult_CountsSkipped'
```
Expected: both FAIL.

- [ ] **Step 5: Apply the filter updates**

In `internal/storage/ci.go`, add `'skipped'` to:

- `HasMeaningfulBatchResult` → `status IN ('done','failed','skipped')`
- `GetExpiredBatches` → both the `status IN ('done','failed','skipped')` qualifying clause and the `status NOT IN ('done','failed','canceled','skipped')` non-terminal clause
- The sibling incomplete-batch-jobs query near `GetStaleBatches` → `status NOT IN ('done','failed','canceled','skipped')`

(`IsBatchExpired` checks only batch age and has no status filter — leave it alone.)

In `internal/storage/jobs.go`, update the `RerunJob` UPDATE's WHERE to:

```go
WHERE id = ? AND status IN ('done', 'failed', 'canceled', 'skipped')
```

- [ ] **Step 6: Run the tests**

Run:
```bash
go test ./internal/storage/
```
Expected: PASS.

- [ ] **Step 7: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/ci.go internal/storage/jobs.go internal/storage/ci_test.go internal/storage/jobs_test.go
git commit -m "storage: include skipped in remaining terminal-state filters + rerun"
```

---

### Task 2f: Include `skipped` in daemon status aggregates

**Files:**
- Modify: `internal/storage/jobs.go` (`GetJobCounts`)
- Modify: `internal/storage/jobs_test.go`
- Modify: `internal/storage/models.go` (`DaemonStatus` struct — add `SkippedJobs int`). `GetStatusOutput` in `internal/daemon/huma_types.go` wraps `storage.DaemonStatus` directly, so the field flows through without touching huma_types.go.
- Modify: `internal/daemon/huma_handlers.go` (status handler — extend the `GetJobCounts` destructure + set `SkippedJobs` on the response)
- Modify: `internal/daemon/huma_routes_test.go` (assertion if present)
- Modify: `e2e_test.go` (assertion if present)
- Modify: `cmd/roborev/status.go` (CLI output: add a `Skipped:` line)

Checklist when landing the storage signature change: run `grep -rn 'GetJobCounts(' --include='*.go' .` and update EVERY caller — adding `GetJobCounts` a return value breaks every existing destructure. At the time this plan was written the callers are:

- `internal/storage/jobs.go` — the function itself
- `internal/storage/db_filter_test.go`
- `internal/storage/db_job_test.go`
- `internal/daemon/huma_handlers.go` (the live `/api/status` path; not `server.go`, which predates the huma refactor and no longer owns this endpoint)
- `internal/daemon/server_jobs_test.go` (several destructures)
- `e2e_test.go` at repo root (several destructures)

Add the matching file paths to the task's `git add` line when committing.

Per spec §"Skipped as a first-class terminal state", skipped rows must appear in aggregate status surfaces, not only in the per-commit queue. Without this, users see a classify job transition to status=skipped but no change in the `roborev status` summary counts.

- [ ] **Step 1: Write failing test**

Append to `internal/storage/jobs_test.go`:

```go
func TestGetJobCounts_IncludesSkipped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-counts").ID
	commitID := createCommit(t, db, repoID, "abc").ID

	// Seed one skipped row.
	_, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source, skip_reason)
		VALUES (?, ?, 'abc', 'skipped', 'design', 'auto_design', 'trivial')
	`, repoID, commitID)
	require.NoError(t, err)

	queued, running, done, failed, canceled, applied, rebased, skipped, err := db.GetJobCounts()
	require.NoError(t, err)
	assert.Equal(t, 0, queued)
	assert.Equal(t, 0, running)
	assert.Equal(t, 0, done)
	assert.Equal(t, 0, failed)
	assert.Equal(t, 0, canceled)
	assert.Equal(t, 0, applied)
	assert.Equal(t, 0, rebased)
	assert.Equal(t, 1, skipped)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run TestGetJobCounts_IncludesSkipped
```
Expected: FAIL — current `GetJobCounts` signature doesn't return a `skipped` count.

- [ ] **Step 3: Extend `GetJobCounts` and every caller**

In `internal/storage/jobs.go`, change the signature to return `skipped int` as the eighth return value; extend the query to include `COALESCE(SUM(CASE WHEN status = 'skipped' THEN 1 ELSE 0 END), 0) AS skipped`.

Update every call site (find them with `grep -rn 'GetJobCounts(' .`):
- `internal/storage/models.go` — add `SkippedJobs int \`json:"skipped_jobs"\`` to the `DaemonStatus` struct.
- `internal/daemon/huma_handlers.go` — at the `GetJobCounts` call site (~line 300), accept the new return value and set `resp.Body.SkippedJobs = skipped` in the response builder. `GetStatusOutput{Body: storage.DaemonStatus}` wraps `DaemonStatus` directly, so the new field flows through automatically.
- `cmd/roborev/status.go` — CLI output formatter. Add a `Skipped:` line alongside Queued/Running/Done/Failed.
- `internal/daemon/huma_routes_test.go` / `e2e_test.go` — if these construct expected `GetStatusOutput` values, extend them.

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/storage/ ./internal/daemon/ ./cmd/roborev/... .
```
Expected: PASS. The trailing `.` (repo root package) catches `e2e_test.go` callers; the `cmd/roborev/...` coverage catches any breakage in the CLI status formatter.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/jobs.go internal/storage/jobs_test.go internal/storage/models.go \
        internal/storage/db_filter_test.go internal/storage/db_job_test.go \
        internal/daemon/huma_handlers.go internal/daemon/huma_routes_test.go \
        internal/daemon/server_jobs_test.go \
        e2e_test.go \
        cmd/roborev/status.go
git commit -m "storage+daemon+cli: include skipped count in status aggregates"
```

---

### Task 3a: Postgres mirror migration (next version)

Only relevant when Postgres sync is enabled. If a repo never opts into sync, this task still runs harmlessly — it just adds column/enum updates that no existing data triggers.

**Version numbering:** `internal/storage/postgres.go` currently has `pgSchemaVersion = 11` (check `grep -n "pgSchemaVersion =" internal/storage/postgres.go` at landing time — it may have advanced). Bump to the next integer (e.g. 12) when wiring this migration in. The version tracker table is `schema_version`, not `roborev_schema`.

**Files:**
- Modify: `internal/storage/postgres.go` (bump `pgSchemaVersion` and add the migration step)
- Modify: `internal/storage/postgres_test.go`

- [ ] **Step 1: Write failing test**

Place this in a separate `_postgres_test.go` file (or a `_test.go` with a build tag at the TOP of the file — build tags cannot appear mid-file):

This test exercises the real upgrade path: first bring up a database at the OLD schema version, then call `EnsureSchema` and assert the upgrade SQL ran. A test that only uses `openTestPgPool` would bootstrap at the new version and prove nothing about the `currentVersion < NEW_VERSION` branch.

```go
//go:build postgres

package storage

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresMigration_SkipReasonAndClassify(t *testing.T) {
	// Step 1: bootstrap a DB at the previous schema version (v11) by
	// executing the old embedded schema directly, then seed schema_version.
	// Implementation note: openTestPgPool currently runs EnsureSchema
	// automatically — use a test helper that skips that (or manually
	// truncate/reset + execute postgres_v11.sql on a fresh pool).
	prevVersion := pgSchemaVersion - 1 // the version THIS task advances from

	oldPool := openTestPgPoolRawAtVersion(t, prevVersion) // new helper, see below
	ctx := context.Background()

	// Seed a repo + a row under the old schema shape (no source/skip_reason).
	// oldPool is a *PgPool; reach into the raw pgx pool via Pool().
	var repoID int
	require.NoError(t, oldPool.Pool().QueryRow(ctx,
		`INSERT INTO roborev.repos (identity) VALUES ($1) RETURNING id`,
		"git@example.com:owner/test-repo.git").Scan(&repoID))
	_, err := oldPool.Pool().Exec(ctx, `
		INSERT INTO roborev.review_jobs
		  (uuid, repo_id, git_ref, agent, status, enqueued_at, source_machine_id)
		VALUES ($1, $2, 'abc', 'test', 'done', NOW(), $3)
	`, uuid.New().String(), repoID, uuid.New().String())
	require.NoError(t, err)

	// Step 2: trigger the migration.
	pg := openTestPgPool(t) // runs EnsureSchema on the same database
	defer pg.Close()

	// Step 3: pre-existing row survived.
	var n int
	require.NoError(t, pg.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM roborev.review_jobs WHERE git_ref = 'abc'`).Scan(&n))
	require.Equal(t, 1, n)

	// Step 4: new columns/status work post-migration.
	machineID := uuid.New().String()
	_, err = pg.Pool().Exec(ctx, `
		INSERT INTO roborev.review_jobs
		  (uuid, repo_id, git_ref, agent, job_type, review_type, status, enqueued_at,
		   source_machine_id, skip_reason, source)
		VALUES ($1, $2, 'after', 'test', 'review', 'design', 'skipped', NOW(), $3, 'trivial', 'auto_design')
	`, uuid.New().String(), repoID, machineID)
	require.NoError(t, err)
}
```

**New test helper** in `internal/storage/postgres_test.go` (or `internal/storage/postgres_test_helpers.go`):

```go
// openTestPgPoolRawAtVersion bootstraps a fresh Postgres test pool at the
// given older schema version by running only the schemas/postgres_v<version>.sql
// embedded file and setting schema_version accordingly. It deliberately does
// NOT call EnsureSchema, so the subsequent openTestPgPool(t) call triggers
// the upgrade path under test.
func openTestPgPoolRawAtVersion(t *testing.T, version int) *PgPool { /* ... */ }
```

(Implementation mirrors `openTestPgPool` but loads the specific-version schema file rather than the embed-current one.)

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
TEST_POSTGRES_URL=<your-test-postgres> go test -tags postgres ./internal/storage/ -run TestPostgresMigration_SkipReasonAndClassify
```
Expected: FAIL.

- [ ] **Step 3: Add the migration**

In `internal/storage/postgres.go`:

1. Bump `pgSchemaVersion` to the next integer (call it `NEW_VERSION` below).
2. Inside the existing `else if currentVersion < pgSchemaVersion { ... }` block, add a new gated step using the same pattern as the existing `if currentVersion < N { ... }` chains. Example alongside the other steps:

```go
if currentVersion < NEW_VERSION {
	// Auto design review support — skip_reason, source columns, and
	// dedup indexes. (job_type has no CHECK constraint; 'classify' is
	// accepted as-is.)
	if _, err = p.pool.Exec(ctx, `ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS skip_reason TEXT`); err != nil {
		return fmt.Errorf("migrate to vNEW (add skip_reason): %w", err)
	}
	if _, err = p.pool.Exec(ctx, `ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS source TEXT`); err != nil {
		return fmt.Errorf("migrate to vNEW (add source): %w", err)
	}
	// Replace status CHECK to include 'skipped'.
	if _, err = p.pool.Exec(ctx, `ALTER TABLE review_jobs DROP CONSTRAINT IF EXISTS review_jobs_status_check`); err != nil {
		return fmt.Errorf("migrate to vNEW (drop status check): %w", err)
	}
	if _, err = p.pool.Exec(ctx, `ALTER TABLE review_jobs ADD CONSTRAINT review_jobs_status_check
		CHECK (status IN ('queued','running','done','failed','canceled','applied','rebased','skipped'))`); err != nil {
		return fmt.Errorf("migrate to vNEW (add status check): %w", err)
	}
	if _, err = p.pool.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup
		ON review_jobs(repo_id, commit_id, review_type)
		WHERE source = 'auto_design'`); err != nil {
		return fmt.Errorf("migrate to vNEW (add dedup index): %w", err)
	}
	if _, err = p.pool.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup_ref
		ON review_jobs(repo_id, git_ref, review_type)
		WHERE source = 'auto_design' AND commit_id IS NULL`); err != nil {
		return fmt.Errorf("migrate to vNEW (add dedup ref index): %w", err)
	}
}
```

The outer block's existing `UPDATE schema_version` / `INSERT INTO schema_version` logic handles bumping the version — no separate version write is needed inside the step.

3. **Add an embedded schema file** `internal/storage/schemas/postgres_vNEW.sql` mirroring the prior file (`postgres_v11.sql`) with the new columns (`skip_reason TEXT`, `source TEXT`), updated status CHECK constraint (adds `'skipped'`), and the two partial unique indexes. This is what fresh databases initialize from; the migration above upgrades existing databases.

4. **Update the `//go:embed` directive** in `internal/storage/postgres.go` (search for `//go:embed schemas/postgres_v11.sql`) to point at the new file. Without this change, fresh-database bootstraps will still initialize from the v11 schema.

5. Adjust constraint names if the actual schema uses a different convention; the `IF EXISTS` variants tolerate either.

- [ ] **Step 4: Run the test**

Run:
```bash
TEST_POSTGRES_URL=<your-test-postgres> go test -tags postgres ./internal/storage/ -run TestPostgresMigration_SkipReasonAndClassify
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/postgres.go internal/storage/postgres_test.go \
        internal/storage/schemas/postgres_vNEW.sql
git commit -m "storage: postgres migration — skip_reason, source, skipped status, dedup indexes"
```

---

### Task 3b: Auto-design dedup unique index

**Files:**
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/db_test.go`

Adds a `source` column to `review_jobs` and creates a partial unique index scoped to auto-generated rows (`source = 'auto_design'`). Explicit/user-initiated design reviews leave `source` NULL and so are not affected by the dedup — preserving the spec's rerun and `--type design` semantics. Replaces the read-then-write pattern with `INSERT ... ON CONFLICT DO NOTHING`.

- [ ] **Step 1: Write failing test**

Append to `internal/storage/db_test.go`:

```go
func TestAutoDesignDedup_AutoRowsDedup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-dedup").ID
	commitID := createCommit(t, db, repoID, "beef").ID

	// First auto-generated insert succeeds.
	_, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		VALUES (?, ?, 'beef', 'queued', 'design', 'auto_design')
	`, repoID, commitID)
	require.NoError(t, err)

	// Second auto-generated insert collides.
	_, err = db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		VALUES (?, ?, 'beef', 'queued', 'design', 'auto_design')
	`, repoID, commitID)
	require.Error(t, err, "second auto_design insert must collide with the unique index")

	// ON CONFLICT DO NOTHING is a no-op.
	res, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		VALUES (?, ?, 'beef', 'queued', 'design', 'auto_design')
		ON CONFLICT DO NOTHING
	`, repoID, commitID)
	require.NoError(t, err)
	n, _ := res.RowsAffected()
	assert.EqualValues(t, 0, n)
}

func TestAutoDesignDedup_ExplicitRowsNotAffected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-dedup-explicit").ID
	commitID := createCommit(t, db, repoID, "cafe").ID

	// An auto-generated skipped design row exists.
	_, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source, skip_reason)
		VALUES (?, ?, 'cafe', 'skipped', 'design', 'auto_design', 'trivial')
	`, repoID, commitID)
	require.NoError(t, err)

	// A manual `roborev review --type design` insert (source NULL) succeeds.
	_, err = db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type)
		VALUES (?, ?, 'cafe', 'queued', 'design')
	`, repoID, commitID)
	require.NoError(t, err, "explicit design insert must not collide with auto_design row")

	// A rerun (also source NULL) also succeeds — reruns go outside the dedup.
	_, err = db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type)
		VALUES (?, ?, 'cafe', 'queued', 'design')
	`, repoID, commitID)
	require.NoError(t, err, "explicit rerun must not collide")
}

func TestAutoDesignDedup_CommitlessIndex(t *testing.T) {
	// Dirty/range reviews arrive without a commit_id. The second dedup
	// index keys on git_ref in that case.
	db := openTestDB(t)
	defer db.Close()

	repoID := createRepo(t, db, "/tmp/repo-dedup-commitless").ID

	// First commit-less auto_design insert succeeds.
	_, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		VALUES (?, NULL, 'dirty', 'queued', 'design', 'auto_design')
	`, repoID)
	require.NoError(t, err)

	// Second auto_design insert with the same git_ref collides.
	_, err = db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		VALUES (?, NULL, 'dirty', 'queued', 'design', 'auto_design')
	`, repoID)
	require.Error(t, err, "second commit-less auto_design insert must collide")

	// ON CONFLICT DO NOTHING is a no-op.
	res, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		VALUES (?, NULL, 'dirty', 'queued', 'design', 'auto_design')
		ON CONFLICT DO NOTHING
	`, repoID)
	require.NoError(t, err)
	n, _ := res.RowsAffected()
	assert.EqualValues(t, 0, n)

	// Explicit commit-less design insert (source=NULL) succeeds even when
	// an auto_design row exists for the same git_ref.
	_, err = db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type)
		VALUES (?, NULL, 'dirty', 'queued', 'design')
	`, repoID)
	require.NoError(t, err, "explicit commit-less insert must not collide with auto_design")

	// A different git_ref does not collide.
	_, err = db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		VALUES (?, NULL, 'dirty-other', 'queued', 'design', 'auto_design')
	`, repoID)
	require.NoError(t, err, "different git_ref must not collide")
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run TestAutoDesignDedup_UniqueIndex
```
Expected: FAIL — index doesn't exist yet.

- [ ] **Step 3: Add `source` column + indexes to the migration**

Extend `migrateReviewJobsConstraintsForAutoDesign` in `internal/storage/db.go`. **Tighten the idempotency check first** — Task 3's early-return examined only `skipped` + `skip_reason`, so a DB that already passed Task 3 would skip this task's source/index work. Update the guard:

Drop Task 3's early-return entirely and make every step conditional (column probes + `CREATE INDEX IF NOT EXISTS`). That way a partially-migrated DB — any combination of status-CHECK updated, source added, first dedup index added, but second missing — converges on the final shape without a guard that can short-circuit past pending work.

Replace the early-return block from Task 3 with structured column/index checks:

```go
// Replace Task 3's early-return check with per-feature idempotency:
// each step probes for its own presence and skips only its own work.
// CREATE INDEX IF NOT EXISTS is naturally idempotent. No shared
// "already done" flag — a partially migrated DB still converges.
```

Then extend the migration body. The source-column probe + ALTER and both `CREATE INDEX IF NOT EXISTS` statements below all run unconditionally (modulo their own `IF NOT EXISTS` / column probe):

```go
// Add the source column if missing.
var hasSource int
if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name='source'`).Scan(&hasSource); err != nil {
    return fmt.Errorf("probe source column: %w", err)
}
if hasSource == 0 {
    if _, err := tx.Exec(`ALTER TABLE review_jobs ADD COLUMN source TEXT`); err != nil {
        return fmt.Errorf("add source column: %w", err)
    }
}

// Partial unique index scoped to auto-generated rows.
if _, err := tx.Exec(`
    CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup
    ON review_jobs(repo_id, commit_id, review_type)
    WHERE source = 'auto_design'
`); err != nil {
    return fmt.Errorf("create auto-design dedup index: %w", err)
}

// Parallel index for commit-less (range/dirty) auto rows.
if _, err := tx.Exec(`
    CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup_ref
    ON review_jobs(repo_id, git_ref, review_type)
    WHERE source = 'auto_design' AND commit_id IS NULL
`); err != nil {
    return fmt.Errorf("create auto-design dedup ref index: %w", err)
}
```

Add to the top-level `schema` constant so fresh databases get both:

```sql
-- in review_jobs:
source TEXT,
-- after the review_jobs block, alongside the other CREATE INDEX lines:
CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup
  ON review_jobs(repo_id, commit_id, review_type)
  WHERE source = 'auto_design';
CREATE UNIQUE INDEX IF NOT EXISTS idx_review_jobs_auto_design_dedup_ref
  ON review_jobs(repo_id, git_ref, review_type)
  WHERE source = 'auto_design' AND commit_id IS NULL;
```

Also add `Source string` to the `ReviewJob` struct in `internal/storage/models.go` (after `SkipReason`):

```go
Source string `json:"source,omitempty"` // "auto_design" for auto-dispatched rows; empty for explicit/user rows
```

- [ ] **Step 4: Confirm Postgres coverage is in Task 3a**

No action needed in Task 3b for Postgres — Task 3a's migration block already adds the `source` column and both partial unique indexes in the same `if currentVersion < NEW_VERSION { ... }` step, and the new `postgres_vNEW.sql` schema file carries them for fresh databases. If Task 3a landed first (as the stage list recommends), the Postgres side is complete before this task runs.

- [ ] **Step 5: Run the test**

Run:
```bash
go test ./internal/storage/ -run TestAutoDesignDedup
```
Expected: PASS.

- [ ] **Step 6: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/db.go internal/storage/models.go internal/storage/db_test.go
git commit -m "storage: unique dedup index for auto design review outcomes"
```

---

### Task 4: HasAutoDesignSlotForCommit query

**Files:**
- Modify: `internal/storage/jobs.go`
- Modify: `internal/storage/jobs_test.go` (or create)

- [ ] **Step 1: Write failing test**

Append to `internal/storage/jobs_test.go`:

```go
func TestHasAutoDesignSlotForCommit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// No slot occupied → false.
	repoID := createRepo(t, db, "/tmp/repo-hasdesign").ID
	createCommit(t, db, repoID, "cafef00d")
	has, err := db.HasAutoDesignSlotForCommit(repoID, "cafef00d")
	require.NoError(t, err)
	assert.False(t, has)

	// Auto-design queued design job occupies the slot.
	_, err = db.DB.ExecContext(context.Background(),
		`INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, source)
		 VALUES (?, (SELECT id FROM commits WHERE sha=?), ?, 'queued', 'design', 'auto_design')`,
		repoID, "cafef00d", "cafef00d")
	require.NoError(t, err)
	has, err = db.HasAutoDesignSlotForCommit(repoID, "cafef00d")
	require.NoError(t, err)
	assert.True(t, has)

	// Explicit (source=NULL) design job for another commit does NOT occupy the slot.
	repoIDExplicit := createRepo(t, db, "/tmp/repo-hasdesign-explicit").ID
	createCommit(t, db, repoIDExplicit, "beef")
	_, err = db.DB.ExecContext(context.Background(),
		`INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type)
		 VALUES (?, (SELECT id FROM commits WHERE sha=?), ?, 'queued', 'design')`,
		repoIDExplicit, "beef", "beef")
	require.NoError(t, err)
	has, err = db.HasAutoDesignSlotForCommit(repoIDExplicit, "beef")
	require.NoError(t, err)
	assert.False(t, has, "explicit source=NULL design rows must not count as slot-occupied")

	// Classify job occupies the slot even before a design row exists.
	repoIDCls := createRepo(t, db, "/tmp/repo-hasdesign-cls").ID
	createCommit(t, db, repoIDCls, "abc")
	_, err = db.DB.ExecContext(context.Background(),
		`INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, job_type, review_type, source)
		 VALUES (?, (SELECT id FROM commits WHERE sha=?), ?, 'queued', 'classify', 'design', 'auto_design')`,
		repoIDCls, "abc", "abc")
	require.NoError(t, err)
	has, err = db.HasAutoDesignSlotForCommit(repoIDCls, "abc")
	require.NoError(t, err)
	assert.True(t, has, "queued classify job must count as slot-occupied")

	// Skipped auto-design row also counts.
	repoIDSk := createRepo(t, db, "/tmp/repo-hasdesign-skipped").ID
	createCommit(t, db, repoIDSk, "feed")
	_, err = db.DB.ExecContext(context.Background(),
		`INSERT INTO review_jobs (repo_id, commit_id, git_ref, status, review_type, skip_reason, source)
		 VALUES (?, (SELECT id FROM commits WHERE sha=?), ?, 'skipped', 'design', 'trivial', 'auto_design')`,
		repoIDSk, "feed", "feed")
	require.NoError(t, err)
	has, err = db.HasAutoDesignSlotForCommit(repoIDSk, "feed")
	require.NoError(t, err)
	assert.True(t, has)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/storage/ -run TestHasAutoDesignSlotForCommit
```
Expected: FAIL — `db.HasAutoDesignSlotForCommit undefined`.

- [ ] **Step 3: Implement the query**

Append to `internal/storage/jobs.go`:

```go
// HasAutoDesignSlotForCommit reports whether the auto-design dedup slot is
// already occupied for (repo_id, commit_sha). Returns true when any of these
// rows exist with source='auto_design':
//
//   - a queued classify job (job_type='classify', review_type='design')
//   - a queued/running/done design review (job_type='review', review_type='design')
//   - a skipped design row (status='skipped', review_type='design')
//
// Classify jobs and design reviews all share the same (repo_id, commit_id,
// review_type='design', source='auto_design') slot in the partial unique
// index; this helper's query is the exact complement of that index, so a
// `true` return guarantees an `INSERT ... ON CONFLICT DO NOTHING` would be
// a no-op.
//
// This is a performance shortcut; correctness is enforced by the unique
// index itself.
func (db *DB) HasAutoDesignSlotForCommit(repoID int64, sha string) (bool, error) {
	var n int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM review_jobs rj
		JOIN commits c ON rj.commit_id = c.id
		WHERE rj.repo_id = ?
		  AND c.sha = ?
		  AND rj.review_type = 'design'
		  AND rj.source = 'auto_design'
	`, repoID, sha).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("query auto-design slot: %w", err)
	}
	return n > 0, nil
}
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/storage/ -run TestHasAutoDesignSlotForCommit
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/jobs.go internal/storage/jobs_test.go
git commit -m "storage: add HasAutoDesignSlotForCommit dedup query"
```

---

## Phase 2 — autotype package (heuristics)

### Task 5: autotype package skeleton

**Files:**
- Create: `internal/review/autotype/autotype.go`
- Create: `internal/review/autotype/autotype_test.go`

- [ ] **Step 1: Write failing test for Decision/Input types**

Create `internal/review/autotype/autotype_test.go`:

```go
package autotype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecisionZeroValue(t *testing.T) {
	var d Decision
	assert.False(t, d.Run)
	assert.Empty(t, d.Reason)
	assert.Empty(t, d.Method)
}

func TestInputZeroValue(t *testing.T) {
	var in Input
	assert.Empty(t, in.RepoPath)
	assert.Empty(t, in.GitRef)
	assert.Empty(t, in.Diff)
	assert.Empty(t, in.Message)
	assert.Nil(t, in.ChangedFiles)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/review/autotype/
```
Expected: compilation failure — package does not exist.

- [ ] **Step 3: Create package with types**

Create `internal/review/autotype/autotype.go`:

```go
// Package autotype decides whether a commit warrants a design review, using
// cheap heuristics first and an optional classifier for ambiguous cases.
package autotype

import "context"

// Method identifies which layer produced a Decision.
type Method string

const (
	MethodHeuristic  Method = "heuristic"
	MethodClassifier Method = "classifier"
)

// Decision is the final verdict for a single commit.
type Decision struct {
	Run    bool   // true = enqueue a design review; false = skip
	Reason string // human-readable explanation
	Method Method // which layer produced this decision
}

// Input is what the heuristics and classifier operate on.
type Input struct {
	RepoPath     string   // repo root on disk
	GitRef       string   // commit SHA, range, or "dirty"
	Diff         string   // resolved unified diff (may be empty for dirty)
	Message      string   // commit subject + body
	ChangedFiles []string // relative paths of files touched
}

// Classifier returns a yes/no + reason for ambiguous cases. Implementations
// typically wrap a SchemaAgent call.
type Classifier interface {
	Decide(ctx context.Context, in Input) (yes bool, reason string, err error)
}

// needsClassifier is a sentinel internal signal; Classify uses it to distinguish
// "no heuristic matched; consult classifier" from a concrete Decision.
type needsClassifier struct{}
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/review/autotype/
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/review/autotype/autotype.go internal/review/autotype/autotype_test.go
git commit -m "autotype: package skeleton with Decision, Input, Classifier types"
```

---

### Task 6: Path matcher wrapper (doublestar)

**Files:**
- Create: `internal/review/autotype/paths.go`
- Modify: `internal/review/autotype/autotype_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/review/autotype/autotype_test.go`:

```go
import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnyMatch(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"empty patterns", nil, "foo.go", false},
		{"exact file", []string{"foo.go"}, "foo.go", true},
		{"simple star", []string{"*.go"}, "foo.go", true},
		{"star does not cross /", []string{"*.go"}, "a/foo.go", false},
		{"doublestar crosses /", []string{"**/*.go"}, "a/b/foo.go", true},
		{"migrations anywhere", []string{"**/migrations/**"}, "internal/db/migrations/001.sql", true},
		{"migrations top-level", []string{"**/migrations/**"}, "migrations/001.sql", true},
		{"schema file", []string{"**/*.sql"}, "db/schema.sql", true},
		{"no match", []string{"**/migrations/**"}, "internal/db/001.sql", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := AnyMatch(c.patterns, c.path)
			assert.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestAllMatch(t *testing.T) {
	assert := assert.New(t)
	// all docs
	got, err := AllMatch([]string{"**/*.md", "**/testdata/**"},
		[]string{"README.md", "internal/testdata/x.json"})
	assert.NoError(err)
	assert.True(got)

	// one file doesn't match
	got, err = AllMatch([]string{"**/*.md"},
		[]string{"README.md", "foo.go"})
	assert.NoError(err)
	assert.False(got)

	// empty file list → false (nothing to match)
	got, err = AllMatch([]string{"**/*.md"}, nil)
	assert.NoError(err)
	assert.False(got)
}

func TestAnyMatchInvalidPattern(t *testing.T) {
	_, err := AnyMatch([]string{"["}, "foo")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/review/autotype/
```
Expected: FAIL — `AnyMatch undefined`, `AllMatch undefined`.

- [ ] **Step 3: Implement path matching**

Create `internal/review/autotype/paths.go`:

```go
package autotype

import (
	"fmt"

	"github.com/bmatcuk/doublestar/v4"
)

// AnyMatch reports whether any of the given patterns matches path using
// doublestar (bash-globstar) semantics. Returns an error if a pattern is
// malformed.
func AnyMatch(patterns []string, path string) (bool, error) {
	for _, p := range patterns {
		ok, err := doublestar.Match(p, path)
		if err != nil {
			return false, fmt.Errorf("invalid glob %q: %w", p, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// AllMatch reports whether every path in paths matches at least one pattern
// in patterns. Returns false for an empty path list (nothing to match).
// Returns an error if any pattern is malformed.
func AllMatch(patterns []string, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	for _, path := range paths {
		ok, err := AnyMatch(patterns, path)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/review/autotype/
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/review/autotype/paths.go internal/review/autotype/autotype_test.go
git commit -m "autotype: AnyMatch/AllMatch helpers using doublestar"
```

---

### Task 7: Diff-size heuristic helpers

**Files:**
- Create: `internal/review/autotype/size.go`
- Create: `internal/review/autotype/size_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/review/autotype/size_test.go`:

```go
package autotype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCountChangedLines(t *testing.T) {
	cases := []struct {
		name string
		diff string
		want int
	}{
		{"empty", "", 0},
		{"no changed lines", "--- a/f\n+++ b/f\n@@ -0,0 +0,0 @@\n", 0},
		{"one add", "+foo\n", 1},
		{"one delete", "-foo\n", 1},
		{"file markers ignored", "+++ b/new\n--- a/old\n+real_add\n-real_del\n", 2},
		{"comment in diff", "@@ -1,2 +1,2 @@\n-old\n+new\n context\n", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, CountChangedLines(c.diff))
		})
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/review/autotype/ -run TestCountChangedLines
```
Expected: FAIL — `CountChangedLines undefined`.

- [ ] **Step 3: Implement**

Create `internal/review/autotype/size.go`:

```go
package autotype

import "strings"

// CountChangedLines counts `+` and `-` lines in a unified diff, excluding
// the `+++` and `---` file marker lines. Useful as a cheap proxy for
// "diff size" when deciding whether a change is trivial or large.
func CountChangedLines(diff string) int {
	if diff == "" {
		return 0
	}
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if strings.HasPrefix(line, "+++") {
				continue
			}
			n++
		case '-':
			if strings.HasPrefix(line, "---") {
				continue
			}
			n++
		}
	}
	return n
}
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/review/autotype/ -run TestCountChangedLines
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/review/autotype/size.go internal/review/autotype/size_test.go
git commit -m "autotype: CountChangedLines helper"
```

---

### Task 8: Message-pattern matcher

**Files:**
- Create: `internal/review/autotype/message.go`
- Create: `internal/review/autotype/message_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/review/autotype/message_test.go`:

```go
package autotype

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchMessage_Triggers(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		msg      string
		want     string // matched pattern or ""
	}{
		{"no patterns", nil, "anything", ""},
		{"refactor word", []string{`\b(refactor|redesign)\b`}, "refactor: rework auth layer", "refactor"},
		{"breaking", []string{`\b(refactor|redesign|rewrite|architect|breaking)\b`}, "feat!: breaking change to API", "breaking"},
		{"no match", []string{`\brefactor\b`}, "fix: null deref", ""},
		{"case sensitive by default", []string{`\brefactor\b`}, "Refactor: ...", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := MatchMessage(c.patterns, c.msg)
			require.NoError(t, err)
			if c.want == "" {
				assert.Empty(t, m)
			} else {
				assert.True(t, strings.Contains(m, c.want), "got match %q, want contains %q", m, c.want)
			}
		})
	}
}

func TestMatchMessage_ConventionalSkip(t *testing.T) {
	assert := assert.New(t)
	patterns := []string{`^(docs|test|style|chore)(\(.+\))?:`}

	for _, msg := range []string{
		"docs: update README",
		"docs(readme): tweak wording",
		"test: add coverage",
		"test(auth): ...",
		"style: gofmt",
		"chore: bump deps",
	} {
		m, err := MatchMessage(patterns, msg)
		assert.NoError(err)
		assert.NotEmpty(m, "expected match for %q", msg)
	}

	for _, msg := range []string{
		"feat: add X",
		"fix: null deref",
		"docs: fix and docs: too", // still matches at start
	} {
		m, err := MatchMessage(patterns, msg)
		assert.NoError(err)
		if msg == "docs: fix and docs: too" {
			assert.NotEmpty(m)
		} else {
			assert.Empty(m, "expected no match for %q", msg)
		}
	}
}

func TestMatchMessage_InvalidRegex(t *testing.T) {
	_, err := MatchMessage([]string{"["}, "anything")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/review/autotype/ -run TestMatchMessage
```
Expected: FAIL — `MatchMessage undefined`.

- [ ] **Step 3: Implement**

Create `internal/review/autotype/message.go`:

```go
package autotype

import (
	"fmt"
	"regexp"
)

// MatchMessage returns the first matching substring from msg for the first
// pattern in patterns that matches. Returns "" (no error) when no pattern
// matches. Returns an error if a pattern is an invalid regex.
//
// Only the subject line (first line) of msg is considered.
func MatchMessage(patterns []string, msg string) (string, error) {
	subject := msg
	for i, c := range msg {
		if c == '\n' {
			subject = msg[:i]
			break
		}
	}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return "", fmt.Errorf("invalid regex %q: %w", p, err)
		}
		if m := re.FindString(subject); m != "" {
			return m, nil
		}
	}
	return "", nil
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/review/autotype/ -run TestMatchMessage
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/review/autotype/message.go internal/review/autotype/message_test.go
git commit -m "autotype: MatchMessage helper for commit-subject regexes"
```

---

### Task 9: Heuristics config struct + defaults

**Files:**
- Create: `internal/review/autotype/config.go`
- Create: `internal/review/autotype/config_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/review/autotype/config_test.go`:

```go
package autotype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultHeuristics(t *testing.T) {
	h := DefaultHeuristics()
	assert := assert.New(t)
	assert.Equal(10, h.MinDiffLines)
	assert.Equal(500, h.LargeDiffLines)
	assert.Equal(10, h.LargeFileCount)
	assert.NotEmpty(h.TriggerPaths)
	assert.NotEmpty(h.SkipPaths)
	assert.NotEmpty(h.TriggerMessagePatterns)
	assert.NotEmpty(h.SkipMessagePatterns)
	assert.Contains(h.TriggerPaths, "**/migrations/**")
	assert.Contains(h.SkipPaths, "**/*.md")
}

func TestHeuristicsValidate(t *testing.T) {
	h := DefaultHeuristics()
	assert.NoError(t, h.Validate())

	bad := h
	bad.TriggerPaths = append([]string{"["}, bad.TriggerPaths...)
	assert.Error(t, bad.Validate())

	bad2 := h
	bad2.TriggerMessagePatterns = []string{"["}
	assert.Error(t, bad2.Validate())
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/review/autotype/ -run 'TestDefaultHeuristics|TestHeuristicsValidate'
```
Expected: FAIL — `DefaultHeuristics undefined`.

- [ ] **Step 3: Implement**

Create `internal/review/autotype/config.go`:

```go
package autotype

import (
	"fmt"
	"regexp"

	"github.com/bmatcuk/doublestar/v4"
)

// Heuristics holds the thresholds and patterns consulted before falling back
// to the classifier. Zero-valued fields fall through to the corresponding
// default when Validate sees them unset.
type Heuristics struct {
	MinDiffLines   int
	LargeDiffLines int
	LargeFileCount int

	TriggerPaths []string
	SkipPaths    []string

	TriggerMessagePatterns []string
	SkipMessagePatterns    []string
}

// DefaultHeuristics returns the baked-in defaults. These are what ships when
// the user hasn't overridden anything in config.
func DefaultHeuristics() Heuristics {
	return Heuristics{
		MinDiffLines:   10,
		LargeDiffLines: 500,
		LargeFileCount: 10,
		TriggerPaths: []string{
			"**/migrations/**",
			"**/schema/**",
			"**/*.sql",
			"docs/superpowers/specs/**",
			"docs/design/**",
			"docs/plans/**",
			"**/*-design.md",
			"**/*-plan.md",
		},
		SkipPaths: []string{
			"**/*.md",
			"**/*_test.go",
			"**/*.spec.*",
			"**/testdata/**",
		},
		TriggerMessagePatterns: []string{
			`\b(refactor|redesign|rewrite|architect|breaking)\b`,
		},
		SkipMessagePatterns: []string{
			`^(docs|test|style|chore)(\(.+\))?:`,
		},
	}
}

// Validate compiles each regex and checks each glob. Returns an error with the
// offending pattern so misconfigurations surface loudly at load time.
func (h Heuristics) Validate() error {
	for _, p := range h.TriggerPaths {
		if _, err := doublestar.Match(p, ""); err != nil {
			return fmt.Errorf("invalid trigger_paths glob %q: %w", p, err)
		}
	}
	for _, p := range h.SkipPaths {
		if _, err := doublestar.Match(p, ""); err != nil {
			return fmt.Errorf("invalid skip_paths glob %q: %w", p, err)
		}
	}
	for _, p := range h.TriggerMessagePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("invalid trigger_message_patterns regex %q: %w", p, err)
		}
	}
	for _, p := range h.SkipMessagePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("invalid skip_message_patterns regex %q: %w", p, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/review/autotype/
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/review/autotype/config.go internal/review/autotype/config_test.go
git commit -m "autotype: Heuristics struct + DefaultHeuristics + Validate"
```

---

### Task 10: Classify orchestration + precedence

**Files:**
- Modify: `internal/review/autotype/autotype.go`
- Create: `internal/review/autotype/classify_test.go`

- [ ] **Step 1: Write failing test with stub classifier**

Create `internal/review/autotype/classify_test.go`:

```go
package autotype

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubClassifier struct {
	yes    bool
	reason string
	err    error
}

func (s stubClassifier) Decide(ctx context.Context, in Input) (bool, string, error) {
	return s.yes, s.reason, s.err
}

func newTestHeuristics() Heuristics { return DefaultHeuristics() }

func TestClassify_TriggerPath(t *testing.T) {
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"db/migrations/001.sql"},
		Diff:         "+CREATE TABLE foo (\n",
		Message:      "feat: add foo table",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.True(t, d.Run)
	assert.Equal(t, MethodHeuristic, d.Method)
	assert.Contains(t, d.Reason, "touches")
}

func TestClassify_TriggerLargeDiff(t *testing.T) {
	bigDiff := make([]byte, 0, 10000)
	for i := 0; i < 600; i++ {
		bigDiff = append(bigDiff, "+newline\n"...)
	}
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff:         string(bigDiff),
		Message:      "feat: expand",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.True(t, d.Run)
	assert.Equal(t, MethodHeuristic, d.Method)
	assert.Contains(t, d.Reason, "large")
}

func TestClassify_TriggerFileCount(t *testing.T) {
	files := make([]string, 15)
	for i := range files {
		files[i] = "src/f" + string(rune('a'+i)) + ".go"
	}
	d, err := Classify(context.Background(), Input{
		ChangedFiles: files,
		Diff:         "+x\n+y\n",
		Message:      "feat: spread",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.True(t, d.Run)
	assert.Contains(t, d.Reason, "wide-reaching")
}

func TestClassify_TriggerMessage(t *testing.T) {
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff:         "+x\n+y\n+z\n+a\n+b\n+c\n+d\n+e\n+f\n+g\n+h\n+i\n+j\n+k\n+l\n+m\n",
		Message:      "refactor: split auth module",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.True(t, d.Run)
	assert.Contains(t, d.Reason, "refactor")
}

func TestClassify_SkipTrivialDiff(t *testing.T) {
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff:         "+x\n",
		Message:      "fix: oneliner",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.False(t, d.Run)
	assert.Equal(t, MethodHeuristic, d.Method)
	assert.Contains(t, d.Reason, "trivial")
}

func TestClassify_SkipAllMatchPaths(t *testing.T) {
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"README.md", "CHANGELOG.md"},
		Diff: func() string {
			s := ""
			for i := 0; i < 30; i++ {
				s += "+line\n"
			}
			return s
		}(),
		Message: "feat: new section",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.False(t, d.Run)
	assert.Contains(t, d.Reason, "doc/test-only")
}

func TestClassify_SkipMessage(t *testing.T) {
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff: func() string {
			s := ""
			for i := 0; i < 30; i++ {
				s += "+line\n"
			}
			return s
		}(),
		Message: "chore: bump go.mod",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.False(t, d.Run)
	assert.Contains(t, d.Reason, "conventional marker")
}

func TestClassify_TriggerPathBeatsSkipMessage(t *testing.T) {
	// A `docs:` commit that adds a new spec should still trigger, because
	// trigger rules run before skip rules.
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"docs/superpowers/specs/new-spec.md"},
		Diff:         "+# Spec\n+body\n+more\n+more\n+more\n+more\n+more\n+more\n+more\n+more\n+more\n",
		Message:      "docs: add spec",
	}, newTestHeuristics(), nil)
	require.NoError(t, err)
	assert.True(t, d.Run)
	assert.Contains(t, d.Reason, "touches")
}

func TestClassify_ClassifierYes(t *testing.T) {
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff: func() string {
			s := ""
			for i := 0; i < 50; i++ {
				s += "+line\n"
			}
			return s
		}(),
		Message: "feat: revise API",
	}, newTestHeuristics(), stubClassifier{yes: true, reason: "public API change"})
	require.NoError(t, err)
	assert.True(t, d.Run)
	assert.Equal(t, MethodClassifier, d.Method)
	assert.Equal(t, "public API change", d.Reason)
}

func TestClassify_ClassifierNo(t *testing.T) {
	d, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff: func() string {
			s := ""
			for i := 0; i < 50; i++ {
				s += "+line\n"
			}
			return s
		}(),
		Message: "feat: rename var",
	}, newTestHeuristics(), stubClassifier{yes: false, reason: "local rename"})
	require.NoError(t, err)
	assert.False(t, d.Run)
	assert.Equal(t, MethodClassifier, d.Method)
	assert.Equal(t, "local rename", d.Reason)
}

func TestClassify_ClassifierNilWhenAmbiguous(t *testing.T) {
	_, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff: func() string {
			s := ""
			for i := 0; i < 50; i++ {
				s += "+line\n"
			}
			return s
		}(),
		Message: "feat: rename var",
	}, newTestHeuristics(), nil)
	assert.ErrorContains(t, err, "classifier required")
}

func TestClassify_ClassifierError(t *testing.T) {
	_, err := Classify(context.Background(), Input{
		ChangedFiles: []string{"src/foo.go"},
		Diff: func() string {
			s := ""
			for i := 0; i < 50; i++ {
				s += "+line\n"
			}
			return s
		}(),
		Message: "feat: rename var",
	}, newTestHeuristics(), stubClassifier{err: errors.New("boom")})
	assert.ErrorContains(t, err, "boom")
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/review/autotype/ -run 'TestClassify_'
```
Expected: FAIL — `Classify undefined`.

- [ ] **Step 3: Implement Classify**

Append to `internal/review/autotype/autotype.go`:

```go
import (
	"context"
	"fmt"
)

// Classify runs the heuristics in fixed order (trigger rules first, then
// skip rules, then classifier fallback) and returns a Decision.
//
// Precedence:
//  1. TRIGGER — any trigger_paths hit, diff size large, file count large,
//     or trigger_message_patterns match → Run=true.
//  2. SKIP — diff below MinDiffLines, all files match skip_paths, or
//     skip_message_patterns match → Run=false.
//  3. CLASSIFIER — ambiguous → delegate to cls.Decide.
//
// The heuristics validate patterns on first use; callers can also invoke
// h.Validate() at config-load time for fail-fast behavior.
func Classify(ctx context.Context, in Input, h Heuristics, cls Classifier) (Decision, error) {
	// --- TRIGGER rules ---
	for _, f := range in.ChangedFiles {
		ok, err := AnyMatch(h.TriggerPaths, f)
		if err != nil {
			return Decision{}, err
		}
		if ok {
			return Decision{
				Run:    true,
				Method: MethodHeuristic,
				Reason: fmt.Sprintf("touches %s (design-relevant)", f),
			}, nil
		}
	}

	lines := CountChangedLines(in.Diff)
	if h.LargeDiffLines > 0 && lines >= h.LargeDiffLines {
		return Decision{
			Run:    true,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("large change (%d lines)", lines),
		}, nil
	}
	if h.LargeFileCount > 0 && len(in.ChangedFiles) >= h.LargeFileCount {
		return Decision{
			Run:    true,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("wide-reaching change (%d files)", len(in.ChangedFiles)),
		}, nil
	}

	if m, err := MatchMessage(h.TriggerMessagePatterns, in.Message); err != nil {
		return Decision{}, err
	} else if m != "" {
		return Decision{
			Run:    true,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("commit message mentions %q", m),
		}, nil
	}

	// --- SKIP rules ---
	if h.MinDiffLines > 0 && lines < h.MinDiffLines {
		return Decision{
			Run:    false,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("trivial diff (%d lines)", lines),
		}, nil
	}

	allSkipped, err := AllMatch(h.SkipPaths, in.ChangedFiles)
	if err != nil {
		return Decision{}, err
	}
	if allSkipped {
		return Decision{
			Run:    false,
			Method: MethodHeuristic,
			Reason: "doc/test-only change",
		}, nil
	}

	if m, err := MatchMessage(h.SkipMessagePatterns, in.Message); err != nil {
		return Decision{}, err
	} else if m != "" {
		return Decision{
			Run:    false,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("conventional marker: %q", m),
		}, nil
	}

	// --- CLASSIFIER fallback ---
	if cls == nil {
		return Decision{}, fmt.Errorf("classifier required for ambiguous input but none provided")
	}
	yes, reason, err := cls.Decide(ctx, in)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Run:    yes,
		Method: MethodClassifier,
		Reason: reason,
	}, nil
}
```

Remove the unused `needsClassifier` sentinel struct if the compiler complains.

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/review/autotype/
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/review/autotype/autotype.go internal/review/autotype/classify_test.go
git commit -m "autotype: Classify orchestration with trigger/skip/classifier precedence"
```

---

## Phase 3 — Config surface

### Task 11: AutoDesignReviewConfig struct + TOML tags

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing test for the new struct**

Append to `internal/config/config_test.go`:

```go
func TestAutoDesignReviewConfig_Load(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "config.toml")
	err := os.WriteFile(cfgFile, []byte(`
[auto_design_review]
enabled = true
min_diff_lines = 20
large_diff_lines = 400
large_file_count = 8
trigger_paths = ["foo/**", "bar/**"]
skip_paths = ["**/*.md"]
trigger_message_patterns = ['\brefactor\b']
skip_message_patterns = ['^docs:']
classifier_timeout_seconds = 45
classifier_max_prompt_size = 32768
`), 0o644)
	require.NoError(t, err)

	cfg, err := LoadGlobalFrom(cfgFile)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.True(cfg.AutoDesignReview.Enabled)
	assert.Equal(20, cfg.AutoDesignReview.MinDiffLines)
	assert.Equal(400, cfg.AutoDesignReview.LargeDiffLines)
	assert.Equal(8, cfg.AutoDesignReview.LargeFileCount)
	assert.Equal([]string{"foo/**", "bar/**"}, cfg.AutoDesignReview.TriggerPaths)
	assert.Equal([]string{"**/*.md"}, cfg.AutoDesignReview.SkipPaths)
	assert.Equal([]string{`\brefactor\b`}, cfg.AutoDesignReview.TriggerMessagePatterns)
	assert.Equal([]string{`^docs:`}, cfg.AutoDesignReview.SkipMessagePatterns)
	assert.Equal(45, cfg.AutoDesignReview.ClassifierTimeoutSeconds)
	assert.Equal(32768, cfg.AutoDesignReview.ClassifierMaxPromptSize)
}
```

(Note: `LoadGlobalFrom` is expected to already exist — if not, use the canonical public loader. Check `internal/config/config.go` for the loader name; call it with the test path.)

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/config/ -run TestAutoDesignReviewConfig_Load
```
Expected: FAIL — `AutoDesignReview undefined`.

- [ ] **Step 3: Add the struct and Config field**

In `internal/config/config.go`, add (near the other nested-config structs like `CIConfig`, `SyncConfig`):

```go
// AutoDesignReviewConfig holds settings for the automatic design-review
// decision. Opt-in; when Enabled is false (default), the decision is never
// consulted and behavior matches pre-auto-design-review.
type AutoDesignReviewConfig struct {
	Enabled bool `toml:"enabled" comment:"Enable the automatic design review decider. Off by default."`

	MinDiffLines   int `toml:"min_diff_lines" comment:"Diffs below this line count skip design review automatically."`
	LargeDiffLines int `toml:"large_diff_lines" comment:"Diffs at or above this line count trigger a design review automatically."`
	LargeFileCount int `toml:"large_file_count" comment:"Commits touching at least this many files trigger a design review automatically."`

	TriggerPaths []string `toml:"trigger_paths" comment:"Doublestar globs; any changed file matching triggers a design review."`
	SkipPaths    []string `toml:"skip_paths" comment:"Doublestar globs; a commit whose changed files all match is skipped."`

	TriggerMessagePatterns []string `toml:"trigger_message_patterns" comment:"Regexes over the commit subject; a match triggers a design review."`
	SkipMessagePatterns    []string `toml:"skip_message_patterns" comment:"Regexes over the commit subject; a match skips the design review."`

	ClassifierTimeoutSeconds int `toml:"classifier_timeout_seconds" comment:"Per-classify-job timeout."`
	ClassifierMaxPromptSize  int `toml:"classifier_max_prompt_size" comment:"Cap on classifier prompt size in bytes."`
}
```

Add the field to `Config` (global) — find an appropriate spot alongside `CI CIConfig` / `Sync SyncConfig`:

```go
// Auto design review configuration (opt-in)
AutoDesignReview AutoDesignReviewConfig `toml:"auto_design_review"`
```

Add a **companion type for per-repo use** where `Enabled` is a tri-state pointer so a repo can disable a globally-enabled default:

```go
// AutoDesignReviewRepoConfig is the per-repo variant of AutoDesignReviewConfig.
// Identical to AutoDesignReviewConfig except Enabled is *bool, distinguishing
// "not set" from "explicitly false".
type AutoDesignReviewRepoConfig struct {
    Enabled *bool `toml:"enabled" comment:"Override the global auto design review setting for this repo. Omit to inherit."`

    MinDiffLines   int `toml:"min_diff_lines"`
    LargeDiffLines int `toml:"large_diff_lines"`
    LargeFileCount int `toml:"large_file_count"`

    TriggerPaths []string `toml:"trigger_paths"`
    SkipPaths    []string `toml:"skip_paths"`

    TriggerMessagePatterns []string `toml:"trigger_message_patterns"`
    SkipMessagePatterns    []string `toml:"skip_message_patterns"`

    ClassifierTimeoutSeconds int `toml:"classifier_timeout_seconds"`
    ClassifierMaxPromptSize  int `toml:"classifier_max_prompt_size"`
}
```

Add the per-repo field to `RepoConfig`:

```go
// Auto design review overrides for this repo (opt-in)
AutoDesignReview AutoDesignReviewRepoConfig `toml:"auto_design_review"`
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/config/ -run TestAutoDesignReviewConfig_Load
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: add AutoDesignReviewConfig to Config and RepoConfig"
```

---

### Task 12: Classify workflow agent/model/backup fields

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/config/config_test.go`:

```go
func TestClassifyWorkflowConfig_Load(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "config.toml")
	err := os.WriteFile(cfgFile, []byte(`
classify_agent = "claude-code"
classify_model = "haiku"
classify_reasoning = "fast"
classify_backup_agent = "codex"
classify_backup_model = "o-mini"
`), 0o644)
	require.NoError(t, err)

	cfg, err := LoadGlobalFrom(cfgFile)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Equal("claude-code", cfg.ClassifyAgent)
	assert.Equal("haiku", cfg.ClassifyModel)
	assert.Equal("fast", cfg.ClassifyReasoning)
	assert.Equal("codex", cfg.ClassifyBackupAgent)
	assert.Equal("o-mini", cfg.ClassifyBackupModel)
}

func TestClassifyWorkflowConfig_FoundByReflectionTag(t *testing.T) {
	cfg := &Config{
		ClassifyAgent:       "claude-code",
		ClassifyModel:       "haiku",
		ClassifyBackupAgent: "codex",
		ClassifyBackupModel: "o-mini",
	}
	assert.Equal(t, "claude-code", lookupFieldByTag(reflect.ValueOf(*cfg), "classify_agent"))
	assert.Equal(t, "haiku", lookupFieldByTag(reflect.ValueOf(*cfg), "classify_model"))
	assert.Equal(t, "codex", lookupFieldByTag(reflect.ValueOf(*cfg), "classify_backup_agent"))
	assert.Equal(t, "o-mini", lookupFieldByTag(reflect.ValueOf(*cfg), "classify_backup_model"))
}
```

Add `"reflect"` to the imports of `config_test.go` if not already present.

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/config/ -run 'TestClassifyWorkflowConfig'
```
Expected: FAIL — `ClassifyAgent undefined`.

- [ ] **Step 3: Add the fields**

In `internal/config/config.go`, add to `Config` (alongside the existing `FixAgent`, `FixModel`, etc.):

```go
// Classify workflow (routing classifier for auto design review)
ClassifyAgent       string `toml:"classify_agent" comment:"Agent for the design-review routing classifier. Must implement SchemaAgent capability."`
ClassifyModel       string `toml:"classify_model" comment:"Model for the classifier agent. Empty = agent default."`
ClassifyReasoning   string `toml:"classify_reasoning" comment:"Reasoning level for the classifier: fast, standard, medium, thorough, or maximum."`
ClassifyBackupAgent string `toml:"classify_backup_agent" comment:"Fallback classifier agent on quota exhaustion / failure."`
ClassifyBackupModel string `toml:"classify_backup_model" comment:"Fallback classifier model."`
```

Add the **same fields** to `RepoConfig`:

```go
// Classify workflow (per-repo overrides)
ClassifyAgent       string `toml:"classify_agent" comment:"Override classifier agent for this repo."`
ClassifyModel       string `toml:"classify_model" comment:"Override classifier model for this repo."`
ClassifyReasoning   string `toml:"classify_reasoning" comment:"Override classifier reasoning for this repo."`
ClassifyBackupAgent string `toml:"classify_backup_agent" comment:"Override classifier backup agent for this repo."`
ClassifyBackupModel string `toml:"classify_backup_model" comment:"Override classifier backup model for this repo."`
```

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./internal/config/ -run 'TestClassifyWorkflowConfig'
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: add classify workflow fields to Config and RepoConfig"
```

---

### Task 13: ResolveAutoDesignEnabled + ResolveAutoDesignHeuristics

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/config/config_test.go`:

```go
func TestResolveAutoDesignEnabled_Disabled(t *testing.T) {
	tmp := t.TempDir()
	assert.False(t, ResolveAutoDesignEnabled(tmp, &Config{}))
}

func TestResolveAutoDesignEnabled_GlobalEnabled(t *testing.T) {
	tmp := t.TempDir()
	cfg := &Config{AutoDesignReview: AutoDesignReviewConfig{Enabled: true}}
	assert.True(t, ResolveAutoDesignEnabled(tmp, cfg))
}

func TestResolveAutoDesignEnabled_RepoEnablesOverGlobalOff(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, ".roborev.toml"),
		[]byte("[auto_design_review]\nenabled = true\n"), 0o644))
	cfg := &Config{AutoDesignReview: AutoDesignReviewConfig{Enabled: false}}
	assert.True(t, ResolveAutoDesignEnabled(tmp, cfg))
}

func TestResolveAutoDesignEnabled_RepoDisablesOverGlobalOn(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, ".roborev.toml"),
		[]byte("[auto_design_review]\nenabled = false\n"), 0o644))
	cfg := &Config{AutoDesignReview: AutoDesignReviewConfig{Enabled: true}}
	assert.False(t, ResolveAutoDesignEnabled(tmp, cfg), "repo explicit false must override global true")
}

func TestResolveAutoDesignEnabled_RepoUnsetInherits(t *testing.T) {
	tmp := t.TempDir()
	// No `enabled` key in repo config → falls through.
	require.NoError(t, os.WriteFile(filepath.Join(tmp, ".roborev.toml"),
		[]byte("[auto_design_review]\nmin_diff_lines = 5\n"), 0o644))
	cfg := &Config{AutoDesignReview: AutoDesignReviewConfig{Enabled: true}}
	assert.True(t, ResolveAutoDesignEnabled(tmp, cfg), "repo without explicit enabled must inherit global")
}

func TestResolveAutoDesignHeuristics_Defaults(t *testing.T) {
	h := ResolveAutoDesignHeuristics(t.TempDir(), &Config{})
	assert := assert.New(t)
	assert.Equal(10, h.MinDiffLines)
	assert.Equal(500, h.LargeDiffLines)
	assert.NotEmpty(h.TriggerPaths)
}

func TestResolveAutoDesignHeuristics_GlobalOverride(t *testing.T) {
	cfg := &Config{AutoDesignReview: AutoDesignReviewConfig{
		MinDiffLines:   20,
		LargeDiffLines: 1000,
		TriggerPaths:   []string{"foo/**"},
	}}
	h := ResolveAutoDesignHeuristics(t.TempDir(), cfg)
	assert := assert.New(t)
	assert.Equal(20, h.MinDiffLines)
	assert.Equal(1000, h.LargeDiffLines)
	assert.Equal([]string{"foo/**"}, h.TriggerPaths)
}

func TestResolveAutoDesignHeuristics_RepoWins(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, ".roborev.toml"),
		[]byte(`[auto_design_review]
min_diff_lines = 5
trigger_paths = ["bar/**"]
`), 0o644))
	cfg := &Config{AutoDesignReview: AutoDesignReviewConfig{
		MinDiffLines: 20,
		TriggerPaths: []string{"foo/**"},
	}}
	h := ResolveAutoDesignHeuristics(tmp, cfg)
	assert.Equal(t, 5, h.MinDiffLines)
	assert.Equal(t, []string{"bar/**"}, h.TriggerPaths)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/config/ -run 'TestResolveAutoDesign'
```
Expected: FAIL.

- [ ] **Step 3: Add an autotype→config bridge helper**

Because `config` must not import `autotype` (layering: daemon imports both), we return a concrete `AutoDesignHeuristics` value defined in `config` and let callers convert. Add to `internal/config/config.go`:

```go
// AutoDesignHeuristics is the resolved heuristic configuration returned by
// ResolveAutoDesignHeuristics. Matches internal/review/autotype.Heuristics
// field-for-field so callers can construct one from the other; keeping the
// type here avoids a circular import.
type AutoDesignHeuristics struct {
	MinDiffLines   int
	LargeDiffLines int
	LargeFileCount int

	TriggerPaths []string
	SkipPaths    []string

	TriggerMessagePatterns []string
	SkipMessagePatterns    []string
}

// DefaultAutoDesignHeuristics returns the embedded fallback values. Keep in
// sync with autotype.DefaultHeuristics.
func DefaultAutoDesignHeuristics() AutoDesignHeuristics {
	return AutoDesignHeuristics{
		MinDiffLines:   10,
		LargeDiffLines: 500,
		LargeFileCount: 10,
		TriggerPaths: []string{
			"**/migrations/**",
			"**/schema/**",
			"**/*.sql",
			"docs/superpowers/specs/**",
			"docs/design/**",
			"docs/plans/**",
			"**/*-design.md",
			"**/*-plan.md",
		},
		SkipPaths: []string{
			"**/*.md",
			"**/*_test.go",
			"**/*.spec.*",
			"**/testdata/**",
		},
		TriggerMessagePatterns: []string{
			`\b(refactor|redesign|rewrite|architect|breaking)\b`,
		},
		SkipMessagePatterns: []string{
			`^(docs|test|style|chore)(\(.+\))?:`,
		},
	}
}

// ResolveAutoDesignEnabled returns true if auto design review is enabled for
// the given repo path. Tri-state: a per-repo explicit `enabled = false`
// overrides a globally-enabled default, and a per-repo `enabled = true`
// overrides a globally-disabled default. An unset per-repo field falls
// through to the global value.
func ResolveAutoDesignEnabled(repoPath string, globalCfg *Config) bool {
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.AutoDesignReview.Enabled != nil {
		return *repoCfg.AutoDesignReview.Enabled
	}
	if globalCfg != nil && globalCfg.AutoDesignReview.Enabled {
		return true
	}
	return false
}

// ResolveAutoDesignHeuristics merges defaults, global, and per-repo config.
// List fields replace wholesale; scalar fields fall through when zero.
func ResolveAutoDesignHeuristics(repoPath string, globalCfg *Config) AutoDesignHeuristics {
	h := DefaultAutoDesignHeuristics()
	if globalCfg != nil {
		h = overlayAutoDesign(h, globalCfg.AutoDesignReview)
	}
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		h = overlayAutoDesign(h, repoCfg.AutoDesignReview)
	}
	return h
}

func overlayAutoDesign(base AutoDesignHeuristics, over AutoDesignReviewConfig) AutoDesignHeuristics {
	if over.MinDiffLines > 0 {
		base.MinDiffLines = over.MinDiffLines
	}
	if over.LargeDiffLines > 0 {
		base.LargeDiffLines = over.LargeDiffLines
	}
	if over.LargeFileCount > 0 {
		base.LargeFileCount = over.LargeFileCount
	}
	if len(over.TriggerPaths) > 0 {
		base.TriggerPaths = over.TriggerPaths
	}
	if len(over.SkipPaths) > 0 {
		base.SkipPaths = over.SkipPaths
	}
	if len(over.TriggerMessagePatterns) > 0 {
		base.TriggerMessagePatterns = over.TriggerMessagePatterns
	}
	if len(over.SkipMessagePatterns) > 0 {
		base.SkipMessagePatterns = over.SkipMessagePatterns
	}
	return base
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/config/ -run 'TestResolveAutoDesign'
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: Resolve functions for auto design enablement + heuristics"
```

---

### Task 14: ResolveClassify* functions with SchemaAgent validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

Note: the validation function will be injected via a package-level hook so `config` doesn't depend on `agent`. Later in Task 15, the `agent` package sets that hook via `config.RegisterClassifyAgentValidator(...)`.

- [ ] **Step 1: Write failing tests**

Append to `internal/config/config_test.go`:

```go
func TestResolveClassifyAgent_Default(t *testing.T) {
	// Validator not registered → resolution works, validation is no-op.
	RegisterClassifyAgentValidator(nil)
	name, err := ResolveClassifyAgent("", t.TempDir(), &Config{})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", name)
}

func TestResolveClassifyAgent_CLIOverride(t *testing.T) {
	RegisterClassifyAgentValidator(nil)
	name, err := ResolveClassifyAgent("codex", t.TempDir(), &Config{})
	require.NoError(t, err)
	assert.Equal(t, "codex", name)
}

func TestResolveClassifyAgent_RepoOverridesGlobal(t *testing.T) {
	RegisterClassifyAgentValidator(nil)
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, ".roborev.toml"),
		[]byte("classify_agent = \"codex\"\n"), 0o644))
	name, err := ResolveClassifyAgent("", tmp, &Config{ClassifyAgent: "claude-code"})
	require.NoError(t, err)
	assert.Equal(t, "codex", name)
}

func TestResolveClassifyAgent_ValidatorReject(t *testing.T) {
	RegisterClassifyAgentValidator(func(name string) error {
		if name == "gemini" {
			return fmt.Errorf("agent %q does not support structured output; valid: claude-code, codex", name)
		}
		return nil
	})
	t.Cleanup(func() { RegisterClassifyAgentValidator(nil) })
	_, err := ResolveClassifyAgent("gemini", t.TempDir(), &Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structured output")
}

func TestResolveClassifyReasoning_Default(t *testing.T) {
	lvl := ResolveClassifyReasoning("", t.TempDir(), &Config{})
	assert.Equal(t, "fast", lvl)
}

func TestResolveClassifyReasoning_CLIOverride(t *testing.T) {
	lvl := ResolveClassifyReasoning("standard", t.TempDir(), &Config{ClassifyReasoning: "fast"})
	assert.Equal(t, "standard", lvl)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/config/ -run 'TestResolveClassify'
```
Expected: FAIL — `ResolveClassifyAgent undefined`.

- [ ] **Step 3: Implement**

Add to `internal/config/config.go`:

```go
// ClassifyAgentValidator is an injection point the agent package fills in
// via RegisterClassifyAgentValidator. It returns an error when the named
// agent does not implement structured-output (SchemaAgent) capability.
//
// If nil, validation is skipped (useful in tests and at the lowest layer).
var classifyAgentValidator func(name string) error

// RegisterClassifyAgentValidator is called by the agent package during init
// to provide SchemaAgent support verification.
func RegisterClassifyAgentValidator(fn func(name string) error) {
	classifyAgentValidator = fn
}

const DefaultClassifyAgent = "claude-code"
const DefaultClassifyReasoning = "fast"

// ResolveClassifyAgent returns the agent name to use for classification.
// Priority: CLI flag > per-repo classify_agent > global classify_agent > default.
// Validates via the registered validator (SchemaAgent capability check).
func ResolveClassifyAgent(cliAgent, repoPath string, globalCfg *Config) (string, error) {
	name := cliAgent
	if name == "" {
		if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.ClassifyAgent != "" {
			name = repoCfg.ClassifyAgent
		}
	}
	if name == "" && globalCfg != nil && globalCfg.ClassifyAgent != "" {
		name = globalCfg.ClassifyAgent
	}
	if name == "" {
		name = DefaultClassifyAgent
	}
	if classifyAgentValidator != nil {
		if err := classifyAgentValidator(name); err != nil {
			return "", err
		}
	}
	return name, nil
}

// ResolveClassifyModel returns the model string for the classifier. Priority
// same as ResolveClassifyAgent. Empty is a valid value (agent uses its default).
func ResolveClassifyModel(cliModel, repoPath string, globalCfg *Config) string {
	if cliModel != "" {
		return cliModel
	}
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.ClassifyModel != "" {
		return repoCfg.ClassifyModel
	}
	if globalCfg != nil && globalCfg.ClassifyModel != "" {
		return globalCfg.ClassifyModel
	}
	return ""
}

// ResolveClassifyReasoning returns the reasoning level for the classifier.
// Defaults to "fast" since the classifier is a routing decision, not a review.
func ResolveClassifyReasoning(cliReasoning, repoPath string, globalCfg *Config) string {
	if cliReasoning != "" {
		return cliReasoning
	}
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.ClassifyReasoning != "" {
		return repoCfg.ClassifyReasoning
	}
	if globalCfg != nil && globalCfg.ClassifyReasoning != "" {
		return globalCfg.ClassifyReasoning
	}
	return DefaultClassifyReasoning
}

// ResolveBackupClassifyAgent returns the backup classify agent (no default).
func ResolveBackupClassifyAgent(repoPath string, globalCfg *Config) string {
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.ClassifyBackupAgent != "" {
		return repoCfg.ClassifyBackupAgent
	}
	if globalCfg != nil && globalCfg.ClassifyBackupAgent != "" {
		return globalCfg.ClassifyBackupAgent
	}
	return ""
}

// ResolveBackupClassifyModel returns the backup classify model (no default).
func ResolveBackupClassifyModel(repoPath string, globalCfg *Config) string {
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.ClassifyBackupModel != "" {
		return repoCfg.ClassifyBackupModel
	}
	if globalCfg != nil && globalCfg.ClassifyBackupModel != "" {
		return globalCfg.ClassifyBackupModel
	}
	return ""
}

const (
	defaultClassifierTimeoutSeconds = 60
	defaultClassifierMaxPromptSize  = 20 * 1024
)

// ResolveClassifierTimeout returns the per-classify-job timeout. Priority:
// repo auto_design_review.classifier_timeout_seconds > global > default (60s).
func ResolveClassifierTimeout(repoPath string, globalCfg *Config) time.Duration {
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.AutoDesignReview.ClassifierTimeoutSeconds > 0 {
		return time.Duration(repoCfg.AutoDesignReview.ClassifierTimeoutSeconds) * time.Second
	}
	if globalCfg != nil && globalCfg.AutoDesignReview.ClassifierTimeoutSeconds > 0 {
		return time.Duration(globalCfg.AutoDesignReview.ClassifierTimeoutSeconds) * time.Second
	}
	return defaultClassifierTimeoutSeconds * time.Second
}

// ResolveClassifierMaxPromptSize returns the cap on classify-prompt bytes.
// Priority: repo auto_design_review.classifier_max_prompt_size > global > default (20 KB).
func ResolveClassifierMaxPromptSize(repoPath string, globalCfg *Config) int {
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.AutoDesignReview.ClassifierMaxPromptSize > 0 {
		return repoCfg.AutoDesignReview.ClassifierMaxPromptSize
	}
	if globalCfg != nil && globalCfg.AutoDesignReview.ClassifierMaxPromptSize > 0 {
		return globalCfg.AutoDesignReview.ClassifierMaxPromptSize
	}
	return defaultClassifierMaxPromptSize
}
```

Add `"time"` to the config package imports.

Add tests for both:

```go
func TestResolveClassifierTimeout_Default(t *testing.T) {
	d := ResolveClassifierTimeout(t.TempDir(), &Config{})
	assert.Equal(t, 60*time.Second, d)
}

func TestResolveClassifierTimeout_GlobalOverride(t *testing.T) {
	cfg := &Config{AutoDesignReview: AutoDesignReviewConfig{ClassifierTimeoutSeconds: 45}}
	d := ResolveClassifierTimeout(t.TempDir(), cfg)
	assert.Equal(t, 45*time.Second, d)
}

func TestResolveClassifierMaxPromptSize_Default(t *testing.T) {
	n := ResolveClassifierMaxPromptSize(t.TempDir(), &Config{})
	assert.Equal(t, 20*1024, n)
}

func TestResolveClassifierMaxPromptSize_RepoOverride(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, ".roborev.toml"),
		[]byte("[auto_design_review]\nclassifier_max_prompt_size = 8192\n"), 0o644))
	n := ResolveClassifierMaxPromptSize(tmp, &Config{})
	assert.Equal(t, 8192, n)
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/config/ -run 'TestResolveClassify'
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: Resolve functions for classify workflow with validator hook"
```

---

## Phase 4 — Classifier agent capability

### Task 15: SchemaAgent interface + registry helper

**Files:**
- Modify: `internal/agent/agent.go`
- Create: `internal/agent/schema_agent.go`
- Create: `internal/agent/schema_agent_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/agent/schema_agent_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSchemaAgent struct {
	*TestAgent
	result json.RawMessage
	err    error
}

func (f *fakeSchemaAgent) ClassifyWithSchema(
	ctx context.Context, repoPath, gitRef, prompt string,
	schema json.RawMessage, out io.Writer,
) (json.RawMessage, error) {
	return f.result, f.err
}

func TestIsSchemaAgent(t *testing.T) {
	var a Agent = NewTestAgent()
	assert.False(t, IsSchemaAgent(a))

	var s Agent = &fakeSchemaAgent{TestAgent: NewTestAgent()}
	assert.True(t, IsSchemaAgent(s))
}

func TestValidateClassifyAgent_NotRegistered(t *testing.T) {
	err := ValidateClassifyAgent("no-such-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown agent")
}

func TestValidateClassifyAgent_NotSchema(t *testing.T) {
	// The test agent is in the registry but does not implement SchemaAgent.
	err := ValidateClassifyAgent("test")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "structured output")
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/agent/ -run 'TestIsSchemaAgent|TestValidateClassifyAgent'
```
Expected: FAIL.

- [ ] **Step 3: Implement**

Create `internal/agent/schema_agent.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/roborev-dev/roborev/internal/config"
)

// SchemaAgent is an optional Agent capability. Implementers return a single
// JSON document conforming to the given JSON Schema, via the underlying CLI's
// native structured-output mechanism (not via prompt nagging).
type SchemaAgent interface {
	Agent

	// ClassifyWithSchema runs one agent turn constrained by `schema` and
	// returns the raw JSON result. `out` receives progress/log lines but not
	// the structured result itself.
	ClassifyWithSchema(
		ctx context.Context,
		repoPath, gitRef, prompt string,
		schema json.RawMessage,
		out io.Writer,
	) (json.RawMessage, error)
}

// IsSchemaAgent reports whether a is a SchemaAgent.
func IsSchemaAgent(a Agent) bool {
	_, ok := a.(SchemaAgent)
	return ok
}

// ValidateClassifyAgent errors when the named agent isn't registered or isn't
// a SchemaAgent. Registered at init() time via config.RegisterClassifyAgentValidator.
func ValidateClassifyAgent(name string) error {
	a, ok := registry[name]
	if !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	if !IsSchemaAgent(a) {
		var valid []string
		for n, r := range registry {
			if IsSchemaAgent(r) {
				valid = append(valid, n)
			}
		}
		return fmt.Errorf(
			"agent %q does not support structured output (classify_agent must be one of: %v)",
			name, valid)
	}
	return nil
}

func init() {
	config.RegisterClassifyAgentValidator(ValidateClassifyAgent)
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/agent/ -run 'TestIsSchemaAgent|TestValidateClassifyAgent'
```
Expected: PASS.

- [ ] **Step 5: Run the full agent test suite to confirm no regressions**

Run:
```bash
go test ./internal/agent/
```
Expected: PASS.

- [ ] **Step 6: Re-run the config test that was previously pending validator registration**

Run:
```bash
go test ./internal/config/ -run TestResolveClassifyAgent
```
Expected: PASS (no validator → no-op, or default validator registered at init time).

- [ ] **Step 7: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/agent/schema_agent.go internal/agent/schema_agent_test.go
git commit -m "agent: SchemaAgent capability interface + ValidateClassifyAgent"
```

---

### Task 16: Claude Code ClassifyWithSchema implementation

**Files:**
- Modify: `internal/agent/claude.go`
- Modify: `internal/agent/claude_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/agent/claude_test.go`:

```go
func TestClaudeClassify_BuildsArgs(t *testing.T) {
	a := NewClaudeAgent("claude")
	got := a.classifyArgs([]byte(`{"type":"object"}`))
	assert := assert.New(t)
	assert.Contains(got, "-p")
	assert.Contains(got, "--json-schema")
	// schema passed as a flag arg on the same command line
	idx := -1
	for i, arg := range got {
		if arg == "--json-schema" {
			idx = i
			break
		}
	}
	require.NotEqual(t, -1, idx)
	require.Less(t, idx+1, len(got))
	assert.JSONEq(`{"type":"object"}`, got[idx+1])
}

func TestClaudeClassify_ParseResult(t *testing.T) {
	// Final assistant "result" event in stream-json.
	stream := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}
{"type":"result","result":"{\"design_review\":true,\"reason\":\"new package\"}"}
`
	out, err := parseClaudeClassifyStream(strings.NewReader(stream))
	require.NoError(t, err)
	assert.JSONEq(t, `{"design_review":true,"reason":"new package"}`, string(out))
}

func TestClaudeClassify_ParseResult_NoResult(t *testing.T) {
	stream := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
`
	_, err := parseClaudeClassifyStream(strings.NewReader(stream))
	assert.Error(t, err)
}

func TestClaudeClassify_ParseResult_InvalidJSON(t *testing.T) {
	stream := `{"type":"result","result":"not valid json"}` + "\n"
	_, err := parseClaudeClassifyStream(strings.NewReader(stream))
	assert.Error(t, err)
}
```

Add `"strings"` to imports if needed.

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/agent/ -run TestClaudeClassify
```
Expected: FAIL — `classifyArgs` / `parseClaudeClassifyStream` undefined.

- [ ] **Step 3: Implement**

Append to `internal/agent/claude.go`:

```go
// classifyArgs builds the argv for a schema-constrained one-shot classify call.
// Deliberately minimal — no --verbose, no session resume, no --effort.
func (a *ClaudeAgent) classifyArgs(schema json.RawMessage) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose",
		"--json-schema", string(schema),
		"--dangerously-skip-permissions",
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if eff := a.claudeEffort(); eff != "" {
		args = append(args, claudeEffortFlag, eff)
	}
	return args
}

// parseClaudeClassifyStream reads Claude's stream-json output and returns the
// final `result` field as raw JSON. Errors if no result event was emitted or
// if the result field isn't valid JSON.
func parseClaudeClassifyStream(r io.Reader) (json.RawMessage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
	var final string
	var found bool
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg claudeStreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Type == "result" && msg.Result != "" {
			final = msg.Result
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("no result event in claude stream")
	}
	// The result field is a JSON-encoded string whose content must itself be
	// JSON (matching the schema). Validate now.
	if !json.Valid([]byte(final)) {
		return nil, fmt.Errorf("claude result is not valid JSON: %q", final)
	}
	return json.RawMessage(final), nil
}

// ClassifyWithSchema runs a single constrained Claude Code invocation and
// returns the final JSON conforming to `schema`. Implements SchemaAgent.
func (a *ClaudeAgent) ClassifyWithSchema(
	ctx context.Context,
	repoPath, gitRef, prompt string,
	schema json.RawMessage,
	out io.Writer,
) (json.RawMessage, error) {
	args := a.classifyArgs(schema)
	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Split into two readers: one for parse, one to echo to out. We tee.
	// Simpler: read everything into a buffer (classifier stream is small).
	buf, readErr := io.ReadAll(stdout)
	if readErr != nil {
		_ = cmd.Wait()
		return nil, fmt.Errorf("read stdout: %w", readErr)
	}
	if out != nil {
		_, _ = out.Write(buf)
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("claude exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return parseClaudeClassifyStream(strings.NewReader(string(buf)))
}
```

Add `"bufio"` to the imports of `claude.go` if not already present.

- [ ] **Step 4: Verify the compile-time SchemaAgent assertion**

Append to `internal/agent/claude.go` (anywhere at file scope):

```go
// Compile-time assertion that ClaudeAgent implements SchemaAgent.
var _ SchemaAgent = (*ClaudeAgent)(nil)
```

- [ ] **Step 5: Run the tests**

Run:
```bash
go test ./internal/agent/ -run TestClaudeClassify
```
Expected: PASS.

Also re-run the full agent suite:

```bash
go test ./internal/agent/
```
Expected: PASS.

- [ ] **Step 6: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/agent/claude.go internal/agent/claude_test.go
git commit -m "agent: ClaudeAgent.ClassifyWithSchema via --json-schema"
```

---

### Task 17: Codex ClassifyWithSchema implementation

**Files:**
- Modify: `internal/agent/codex.go`
- Modify: `internal/agent/codex_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/agent/codex_test.go`:

```go
func TestCodexClassify_BuildsArgs(t *testing.T) {
	a := NewCodexAgent("codex")
	got := a.classifyArgs("/tmp/schema.json", "/tmp/out.json")
	assert := assert.New(t)
	assert.Contains(got, "exec")
	assert.Contains(got, "--output-schema")
	assert.Contains(got, "/tmp/schema.json")
	assert.Contains(got, "--output-last-message")
	assert.Contains(got, "/tmp/out.json")
}

func TestCodexClassify_ReadsLastMessage(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "result.json")
	require.NoError(t, os.WriteFile(out, []byte(`{"design_review":false,"reason":"local fix"}`), 0o644))
	got, err := readCodexLastMessage(out)
	require.NoError(t, err)
	assert.JSONEq(t, `{"design_review":false,"reason":"local fix"}`, string(got))
}

func TestCodexClassify_EmptyResultErrors(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "result.json")
	require.NoError(t, os.WriteFile(out, []byte(``), 0o644))
	_, err := readCodexLastMessage(out)
	assert.Error(t, err)
}

func TestCodexClassify_MissingFileErrors(t *testing.T) {
	_, err := readCodexLastMessage("/tmp/does-not-exist-for-classify-test")
	assert.Error(t, err)
}
```

Add `"os"`, `"path/filepath"` to imports.

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/agent/ -run TestCodexClassify
```
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `internal/agent/codex.go`:

```go
// classifyArgs builds argv for `codex exec` in schema mode.
func (a *CodexAgent) classifyArgs(schemaPath, outPath string) []string {
	args := []string{"exec",
		"--output-schema", schemaPath,
		"--output-last-message", outPath,
		"--sandbox", "read-only",
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	return args
}

// readCodexLastMessage reads codex's last-message output file and validates
// that it contains valid JSON.
func readCodexLastMessage(path string) (json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read codex output: %w", err)
	}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("codex last-message file is empty")
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("codex last-message is not valid JSON: %q", string(trimmed))
	}
	return json.RawMessage(trimmed), nil
}

// ClassifyWithSchema runs a single constrained codex exec invocation.
func (a *CodexAgent) ClassifyWithSchema(
	ctx context.Context,
	repoPath, gitRef, prompt string,
	schema json.RawMessage,
	out io.Writer,
) (json.RawMessage, error) {
	tmp, err := os.MkdirTemp("", "roborev-classify-*")
	if err != nil {
		return nil, fmt.Errorf("mkdir temp: %w", err)
	}
	defer os.RemoveAll(tmp)

	schemaPath := filepath.Join(tmp, "schema.json")
	outPath := filepath.Join(tmp, "result.json")
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		return nil, fmt.Errorf("write schema: %w", err)
	}

	args := a.classifyArgs(schemaPath, outPath)
	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)

	if out != nil {
		cmd.Stdout = out
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("codex exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return readCodexLastMessage(outPath)
}
```

Add imports: `"bytes"`, `"os"`, `"path/filepath"`.

- [ ] **Step 4: Add compile-time assertion**

Append to `internal/agent/codex.go`:

```go
var _ SchemaAgent = (*CodexAgent)(nil)
```

- [ ] **Step 5: Run the tests**

Run:
```bash
go test ./internal/agent/ -run TestCodexClassify
go test ./internal/agent/
```
Expected: PASS.

- [ ] **Step 6: Update ValidateClassifyAgent error list check**

Re-run the validator test from Task 15 to confirm that with claude-code + codex both implementing SchemaAgent, the "valid list" in the error message contains both names:

Run:
```bash
go test ./internal/agent/ -run TestValidateClassifyAgent -v
```
Expected: PASS (test just checks that a non-schema agent is rejected; the valid list is formatted but content-only asserted loosely via "structured output").

- [ ] **Step 7: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/agent/codex.go internal/agent/codex_test.go
git commit -m "agent: CodexAgent.ClassifyWithSchema via --output-schema"
```

---

### Task 18: Classifier prompt builder

**Files:**
- Create: `internal/prompt/classify.go`
- Create: `internal/prompt/classify_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/prompt/classify_test.go`:

```go
package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildClassifyPrompt_Small(t *testing.T) {
	p, err := BuildClassifyPrompt(ClassifyInput{
		Subject:    "feat: add user auth middleware",
		Body:       "Adds JWT validation to the API layer.",
		DiffStat:   " 2 files changed, 40 insertions(+)",
		Diff:       "+func Auth() {}\n+func Verify() {}\n",
		MaxBytes:   20000,
	})
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Contains(p, "feat: add user auth middleware")
	assert.Contains(p, "2 files changed")
	assert.Contains(p, "+func Auth()")
	// System prompt content
	assert.Contains(p, "routing classifier")
}

func TestBuildClassifyPrompt_Truncates(t *testing.T) {
	bigDiff := strings.Repeat("+line\n", 5000)
	p, err := BuildClassifyPrompt(ClassifyInput{
		Subject:  "refactor: large",
		DiffStat: " 10 files changed, 10000 insertions(+)",
		Diff:     bigDiff,
		MaxBytes: 1024,
	})
	require.NoError(t, err)
	assert.Contains(t, p, "truncated")
	assert.LessOrEqual(t, len(p), 4096) // some room for system prompt + wrapper
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/prompt/ -run TestBuildClassifyPrompt
```
Expected: FAIL.

- [ ] **Step 3: Implement**

Create `internal/prompt/classify.go`:

```go
package prompt

import (
	"fmt"
	"strings"
)

const classifySystemPrompt = `You are a routing classifier. Given a code change, decide whether it warrants a design review.

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
`

// ClassifyInput is the structured input a classify prompt needs.
type ClassifyInput struct {
	Subject  string // commit subject line
	Body     string // commit body (may be empty)
	DiffStat string // output of git --stat (compact file summary)
	Diff     string // full diff; truncated if it exceeds MaxBytes
	MaxBytes int    // cap on total prompt size; 0 = unlimited
}

// BuildClassifyPrompt constructs the compact classifier prompt.
func BuildClassifyPrompt(in ClassifyInput) (string, error) {
	var b strings.Builder
	b.WriteString(classifySystemPrompt)
	b.WriteString("\n---\n\n## Commit\n\n")
	b.WriteString("Subject: ")
	b.WriteString(in.Subject)
	b.WriteString("\n")
	if in.Body != "" {
		b.WriteString("\nBody:\n")
		b.WriteString(in.Body)
		b.WriteString("\n")
	}

	if in.DiffStat != "" {
		b.WriteString("\n## Stat\n\n")
		b.WriteString("```\n")
		b.WriteString(strings.TrimSpace(in.DiffStat))
		b.WriteString("\n```\n")
	}

	b.WriteString("\n## Diff\n\n")
	b.WriteString("```diff\n")
	diff := in.Diff
	if in.MaxBytes > 0 && b.Len()+len(diff)+20 > in.MaxBytes {
		// Truncate, keep head
		avail := in.MaxBytes - b.Len() - 40
		if avail < 0 {
			avail = 0
		}
		if avail < len(diff) {
			diff = diff[:avail]
			diff += "\n... (truncated)\n"
		}
	}
	b.WriteString(diff)
	if !strings.HasSuffix(diff, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")

	out := b.String()
	if in.MaxBytes > 0 && len(out) > in.MaxBytes*2 {
		return "", fmt.Errorf("classify prompt exceeds hard cap (%d > %d)", len(out), in.MaxBytes*2)
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/prompt/ -run TestBuildClassifyPrompt
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/prompt/classify.go internal/prompt/classify_test.go
git commit -m "prompt: BuildClassifyPrompt for routing classifier"
```

---

## Phase 5 — Daemon integration

### Task 19: Classifier bridge (autotype.Classifier over a SchemaAgent)

**Files:**
- Create: `internal/daemon/classifier.go`
- Create: `internal/daemon/classifier_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/daemon/classifier_test.go`:

```go
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/roborev-dev/roborev/internal/review/autotype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSchemaAgent struct {
	result json.RawMessage
	err    error
}

func (f *fakeSchemaAgent) Name() string                                { return "fake" }
func (f *fakeSchemaAgent) Review(context.Context, string, string, string, io.Writer) (string, error) {
	return "", nil
}
func (f *fakeSchemaAgent) WithReasoning(level string) any { return f }
func (f *fakeSchemaAgent) WithAgentic(bool) any          { return f }
func (f *fakeSchemaAgent) WithModel(string) any          { return f }
func (f *fakeSchemaAgent) CommandLine() string           { return "fake" }
func (f *fakeSchemaAgent) ClassifyWithSchema(
	context.Context, string, string, string, json.RawMessage, io.Writer,
) (json.RawMessage, error) {
	return f.result, f.err
}

func TestClassifierAdapter_Yes(t *testing.T) {
	ad := newClassifierAdapter(&fakeSchemaAgent{
		result: []byte(`{"design_review": true, "reason": "new package"}`),
	}, 20*1024)
	yes, reason, err := ad.Decide(context.Background(), autotype.Input{})
	require.NoError(t, err)
	assert.True(t, yes)
	assert.Equal(t, "new package", reason)
}

func TestClassifierAdapter_No(t *testing.T) {
	ad := newClassifierAdapter(&fakeSchemaAgent{
		result: []byte(`{"design_review": false, "reason": "local fix"}`),
	}, 20*1024)
	yes, reason, err := ad.Decide(context.Background(), autotype.Input{})
	require.NoError(t, err)
	assert.False(t, yes)
	assert.Equal(t, "local fix", reason)
}

func TestClassifierAdapter_AgentError(t *testing.T) {
	ad := newClassifierAdapter(&fakeSchemaAgent{err: errors.New("boom")}, 20*1024)
	_, _, err := ad.Decide(context.Background(), autotype.Input{})
	assert.ErrorContains(t, err, "boom")
}

func TestClassifierAdapter_InvalidJSON(t *testing.T) {
	ad := newClassifierAdapter(&fakeSchemaAgent{result: []byte(`not json`)}, 20*1024)
	_, _, err := ad.Decide(context.Background(), autotype.Input{})
	assert.ErrorContains(t, err, "invalid")
}

func TestClassifierAdapter_RespectsMaxBytes(t *testing.T) {
	// Sent prompt should be bounded; we can't easily observe the prompt from
	// outside, but we can ensure the adapter doesn't panic on tiny caps.
	ad := newClassifierAdapter(&fakeSchemaAgent{
		result: []byte(`{"design_review": false, "reason": "ok"}`),
	}, 512)
	_, _, err := ad.Decide(context.Background(), autotype.Input{
		Diff:    strings.Repeat("+line\n", 1000),
		Message: "feat: something",
	})
	require.NoError(t, err)
}
```

(You may need to match the real Agent interface signatures — copy them from `internal/agent/agent.go` for the fakes. The fakes here are sketches; ensure they satisfy both `agent.Agent` and `agent.SchemaAgent`.)

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/daemon/ -run TestClassifierAdapter
```
Expected: FAIL — `newClassifierAdapter` undefined.

- [ ] **Step 3: Implement**

Create `internal/daemon/classifier.go`:

```go
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/prompt"
	"github.com/roborev-dev/roborev/internal/review/autotype"
)

// classifySchema is embedded in every classify call. Kept in a var so tests
// can swap it out if needed.
var classifySchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["design_review", "reason"],
  "properties": {
    "design_review": {"type": "boolean"},
    "reason":        {"type": "string"}
  }
}`)

// classifierAdapter bridges autotype.Classifier to a SchemaAgent.
type classifierAdapter struct {
	agent    agent.SchemaAgent
	maxBytes int
}

func newClassifierAdapter(a agent.SchemaAgent, maxBytes int) *classifierAdapter {
	if maxBytes <= 0 {
		maxBytes = 20 * 1024
	}
	return &classifierAdapter{agent: a, maxBytes: maxBytes}
}

type classifyResult struct {
	DesignReview bool   `json:"design_review"`
	Reason       string `json:"reason"`
}

// Decide implements autotype.Classifier.
func (c *classifierAdapter) Decide(ctx context.Context, in autotype.Input) (bool, string, error) {
	subject, body := splitMessage(in.Message)
	p, err := prompt.BuildClassifyPrompt(prompt.ClassifyInput{
		Subject:  subject,
		Body:     body,
		DiffStat: deriveStat(in.Diff, in.ChangedFiles),
		Diff:     in.Diff,
		MaxBytes: c.maxBytes,
	})
	if err != nil {
		return false, "", fmt.Errorf("build prompt: %w", err)
	}

	raw, err := c.agent.ClassifyWithSchema(ctx, in.RepoPath, in.GitRef, p, classifySchema, io.Discard)
	if err != nil {
		return false, "", fmt.Errorf("classifier agent: %w", err)
	}

	var out classifyResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, "", fmt.Errorf("invalid classifier output: %w (%q)", err, string(raw))
	}
	return out.DesignReview, out.Reason, nil
}

// splitMessage returns (subject, body) from a commit message.
func splitMessage(msg string) (string, string) {
	for i, c := range msg {
		if c == '\n' {
			rest := msg[i+1:]
			// skip the mandatory blank line between subject and body
			for len(rest) > 0 && (rest[0] == '\n' || rest[0] == '\r') {
				rest = rest[1:]
			}
			return msg[:i], rest
		}
	}
	return msg, ""
}

// deriveStat renders a minimal stat from changed files when the real
// git --stat output isn't available. Sized to stay well under the prompt cap.
func deriveStat(diff string, files []string) string {
	if len(files) == 0 {
		return ""
	}
	lines := autotype.CountChangedLines(diff)
	return fmt.Sprintf(" %d files changed, ~%d line changes", len(files), lines)
}
```

Note: the fake `SchemaAgent` in the test needs to fully satisfy both interfaces. Adjust the test `fakeSchemaAgent` methods to match `agent.Agent` exactly (signatures like `WithReasoning(level agent.ReasoningLevel) agent.Agent`). If the test-file fake becomes verbose, move it to a helper file.

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/daemon/ -run TestClassifierAdapter
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/daemon/classifier.go internal/daemon/classifier_test.go
git commit -m "daemon: classifierAdapter bridging autotype to SchemaAgent"
```

---

### Task 20: Worker handler for `classify` job_type

**Prerequisite:** Land Task 21 first. Task 20's worker handler calls `wp.db.PromoteClassifyToDesignReview` and `wp.db.MarkClassifyAsSkippedDesign`, both defined in Task 21. Executing Task 20 in isolation would fail to compile. If you are working the plan strictly in order, swap the task numbers locally or treat Tasks 20+21 as a single unit.

**Files:**
- Modify: `internal/daemon/worker.go`
- Modify: `internal/daemon/worker_test.go`

- [ ] **Step 1: Understand current worker flow**

Read `internal/daemon/worker.go` focusing on the main job-processing loop. The worker currently dispatches on `job.JobType` to pick a prompt builder and review call. We need to add a branch for `JobTypeClassify`.

- [ ] **Step 2: Write failing integration test**

Append to `internal/daemon/worker_test.go`:

```go
// These tests follow the existing pattern in worker_test.go: the pool is
// NOT Start()ed — we invoke wp.processJob directly on a manually-claimed job.
func TestWorker_ClassifyJob_Yes_EnqueuesDesignReview(t *testing.T) {
	tc := newWorkerTestContext(t, 0)

	job := tc.createAndClaimClassifyJob(t, "feedcafe", "feat: new package", "+lots of new code\n")

	SetTestClassifierVerdict(true, "new package detected")
	t.Cleanup(func() { SetTestClassifierVerdict(false, "") })

	tc.Pool.processJob(testWorkerID, job)

	// The classify row is converted in place. Assert the same row is now
	// a queued design review with a clean execution slate.
	after, err := tc.DB.GetJobByID(job.ID)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Equal("review", after.JobType)
	assert.Equal(storage.JobStatusQueued, after.Status)
	assert.Equal("design", after.ReviewType)
	assert.Equal("auto_design", after.Source, "source preserved across promotion")
	assert.Empty(after.WorkerID, "worker_id cleared so a new worker can claim")
	assert.Nil(after.StartedAt, "started_at cleared")

	// Explicit uniqueness guard: a buggy implementation could UPDATE the
	// classify row AND also INSERT an extra auto-design row. Assert that
	// exactly one auto-design row exists for this commit.
	var n int
	require.NoError(t, tc.DB.QueryRow(
		`SELECT COUNT(*) FROM review_jobs rj JOIN commits c ON rj.commit_id = c.id
		 WHERE rj.repo_id = ? AND c.sha = ? AND rj.source = 'auto_design'`,
		tc.Repo.ID, "feedcafe").Scan(&n))
	assert.Equal(1, n, "exactly one auto_design row must exist (no second INSERT)")
}

func TestWorker_ClassifyJob_No_InsertsSkippedRow(t *testing.T) {
	tc := newWorkerTestContext(t, 0)

	job := tc.createAndClaimClassifyJob(t, "beefc0de", "fix: local rename", "+x\n")

	SetTestClassifierVerdict(false, "local rename only")
	t.Cleanup(func() { SetTestClassifierVerdict(false, "") })

	tc.Pool.processJob(testWorkerID, job)

	// Same row converted in place to a skipped design row.
	after, err := tc.DB.GetJobByID(job.ID)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Equal("review", after.JobType, "job_type flipped from classify to review")
	assert.Equal(storage.JobStatusSkipped, after.Status)
	assert.Equal("design", after.ReviewType)
	assert.Equal("auto_design", after.Source)
	assert.Equal("local rename only", after.SkipReason)

	// Uniqueness guard (same as the yes-path test).
	var n int
	require.NoError(t, tc.DB.QueryRow(
		`SELECT COUNT(*) FROM review_jobs rj JOIN commits c ON rj.commit_id = c.id
		 WHERE rj.repo_id = ? AND c.sha = ? AND rj.source = 'auto_design'`,
		tc.Repo.ID, "beefc0de").Scan(&n))
	assert.Equal(1, n, "exactly one auto_design row must exist (no second INSERT)")
}
```

This test uses helpers that don't exist yet:
- `tc.createAndClaimClassifyJob(t, sha, subject, diff)` — returns `*storage.ReviewJob`
- `tc.processClaimedJob(job)` — runs `Pool.processJob` on the already-claimed job
- `SetTestClassifierVerdict(yes bool, reason string)` — a package-level knob on the daemon that forces the classifier's verdict for test isolation

- [ ] **Step 3: Run to confirm failure**

Run:
```bash
go test ./internal/daemon/ -run TestWorker_ClassifyJob
```
Expected: FAIL — undefined helpers / no classify handling in worker.

- [ ] **Step 4: Add test helpers**

Create or extend `internal/daemon/worker_test_helpers.go` (or append to the existing test-helpers file):

```go
// SetTestClassifierVerdict overrides the classifier result for tests.
// Must be reset between tests.
var testClassifierVerdict struct {
	mu     sync.Mutex
	set    bool
	yes    bool
	reason string
}

func SetTestClassifierVerdict(yes bool, reason string) {
	testClassifierVerdict.mu.Lock()
	defer testClassifierVerdict.mu.Unlock()
	testClassifierVerdict.set = reason != "" || yes
	testClassifierVerdict.yes = yes
	testClassifierVerdict.reason = reason
}

func getTestClassifierVerdict() (bool, string, bool) {
	testClassifierVerdict.mu.Lock()
	defer testClassifierVerdict.mu.Unlock()
	return testClassifierVerdict.yes, testClassifierVerdict.reason, testClassifierVerdict.set
}
```

Extend `workerTestContext` with:

```go
// createAndClaimClassifyJob enqueues a classify job and claims it.
// The real fields on workerTestContext are Repo (*storage.Repo), TmpDir,
// DB (*storage.DB), and Pool (*WorkerPool) — see internal/daemon/worker_test.go.
func (c *workerTestContext) createAndClaimClassifyJob(
	t *testing.T, sha, subject, diff string,
) *storage.ReviewJob {
	t.Helper()
	commit, err := c.DB.GetOrCreateCommit(c.Repo.ID, sha, "Author", subject, time.Now())
	require.NoError(t, err)
	job, err := c.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID:      c.Repo.ID,
		CommitID:    commit.ID,
		GitRef:      sha,
		Agent:       "test",
		JobType:     storage.JobTypeClassify,
		ReviewType:  "design",
		DiffContent: diff,
		Prompt:      subject,
	})
	require.NoError(t, err)
	claimed, err := c.DB.ClaimJob("test-worker")
	require.NoError(t, err)
	require.Equal(t, job.ID, claimed.ID)
	return claimed
}

```

Tests call these as `tc.createAndClaimClassifyJob(t, "...", "...", "...")` and then invoke `tc.Pool.processJob(testWorkerID, job)` directly — this matches the existing pattern used throughout `worker_test.go`. The rest of the workerTestContext (DB, Repo, Pool, TmpDir, Broadcaster) is already established.

- [ ] **Step 5: Implement classify-job handling in the worker**

`processJob` in `internal/daemon/worker.go` is a linear function that branches on `job.IsFixJob()`, `job.UsesStoredPrompt()`, and `job.DiffContent != nil`. Add a classify short-circuit near the top, right after the agent-cooldown check (search for `wp.isAgentCoolingDown` — the classify branch goes immediately after):

```go
if job.JobType == storage.JobTypeClassify {
    wp.processClassifyJob(ctx, workerID, job)
    return
}
```

Add the method. Signature mirrors `processJob`: `(ctx, workerID, job)` with no return value (the worker loop doesn't consume one).

```go
// processClassifyJob runs the classifier for an ambiguous design-review
// decision and converts the same classify row in place via UPDATE:
//   - yes  → job_type='review', status='queued' (a worker claims it next)
//   - no   → status='skipped', skip_reason=<classifier reason>
//   - fail → status='skipped', skip_reason=<failure text>
// No separate row is ever inserted — the partial unique index would
// reject a second auto-design row for this commit.
func (wp *WorkerPool) processClassifyJob(ctx context.Context, workerID string, job *storage.ReviewJob) {
	cfg := wp.cfgGetter.Config()

	timeout := config.ResolveClassifierTimeout(job.RepoPath, cfg)
	classifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxBytes := config.ResolveClassifierMaxPromptSize(job.RepoPath, cfg)

	if yes, reason, set := getTestClassifierVerdict(); set {
		wp.applyClassifyVerdict(workerID, job, yes, reason)
		return
	}

	in := autotype.Input{
		RepoPath:     job.RepoPath,
		GitRef:       job.GitRef,
		Diff:         deref(job.DiffContent),
		Message:      classifierCommitMessage(job.RepoPath, job.GitRef, job.CommitSubject), // subject + body; falls back to subject on git error
		ChangedFiles: wp.changedFilesForJob(job),                  // shared helper with the enqueue path
	}

	primary, err := config.ResolveClassifyAgent("", job.RepoPath, cfg)
	if err != nil {
		wp.completeClassifyAsSkip(workerID, job, "classifier config: "+err.Error())
		return
	}
	backup := config.ResolveBackupClassifyAgent(job.RepoPath, cfg)

	// tryAgent runs the classifier against a specific (agent, model) pair.
	// The model is explicit so the backup uses classify_backup_model rather
	// than inheriting the primary's.
	tryAgent := func(name, model string) (bool, string, error) {
		ag, err := agent.GetAvailable(name)
		if err != nil {
			return false, "", fmt.Errorf("classifier %q unavailable: %w", name, err)
		}
		sa, ok := ag.(agent.SchemaAgent)
		if !ok {
			return false, "", fmt.Errorf("classify_agent %q is not a SchemaAgent", name)
		}
		sa = sa.WithReasoning(agent.ParseReasoningLevel(
			config.ResolveClassifyReasoning("", job.RepoPath, cfg),
		)).(agent.SchemaAgent)
		if model != "" {
			sa = sa.WithModel(model).(agent.SchemaAgent)
		}
		return newClassifierAdapter(sa, maxBytes).Decide(classifyCtx, in)
	}

	primaryModel := config.ResolveClassifyModel("", job.RepoPath, cfg)
	yes, reason, err := tryAgent(primary, primaryModel)
	if err != nil && backup != "" && backup != primary {
		backupModel := config.ResolveBackupClassifyModel(job.RepoPath, cfg)
		log.Printf("[%s] classifier primary %q (model=%q) failed (%v); trying backup %q (model=%q)",
			workerID, primary, primaryModel, err, backup, backupModel)
		yes, reason, err = tryAgent(backup, backupModel)
	}
	if err != nil {
		wp.completeClassifyAsSkip(workerID, job, "classifier error: "+err.Error())
		return
	}
	wp.applyClassifyVerdict(workerID, job, yes, reason)
}

// applyClassifyVerdict converts the classify row in place — a separate
// INSERT for the follow-up design/skipped row would conflict with the
// auto-design partial unique index, since the classify row already
// occupies the (repo_id, commit_id, review_type='design', source='auto_design')
// slot. Instead we UPDATE the row's job_type and status:
//   - yes: job_type='review', status='queued' → a worker claims it and
//     runs the design review.
//   - no:  status='skipped', skip_reason=reason → terminal, visible in TUI.
func (wp *WorkerPool) applyClassifyVerdict(workerID string, job *storage.ReviewJob, yes bool, reason string) {
	if yes {
		if err := wp.db.PromoteClassifyToDesignReview(job.ID, workerID); err != nil {
			log.Printf("[%s] PromoteClassifyToDesignReview for %d: %v", workerID, job.ID, err)
		}
		return
	}
	if err := wp.db.MarkClassifyAsSkippedDesign(job.ID, workerID, reason); err != nil {
		log.Printf("[%s] MarkClassifyAsSkippedDesign for %d: %v", workerID, job.ID, err)
	}
}

// completeClassifyAsSkip is the failure path: the classifier couldn't
// reach a substantive yes/no answer, so we degrade to skip with a
// reason. Structurally identical to the "no" branch above; the only
// difference is semantic (reason describes a failure, not a routing
// decision). Uses the same in-place UPDATE so we don't fight the
// unique index, and the classify job ends terminal (status='skipped')
// — NOT 'failed' — so CI batch accounting doesn't flag the batch as
// failed on every classifier hiccup.
func (wp *WorkerPool) completeClassifyAsSkip(workerID string, job *storage.ReviewJob, reason string) {
	if err := wp.db.MarkClassifyAsSkippedDesign(job.ID, workerID, reason); err != nil {
		log.Printf("[%s] MarkClassifyAsSkippedDesign for failed classify %d: %v", workerID, job.ID, err)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
```

`PromoteClassifyToDesignReview` and `MarkClassifyAsSkippedDesign` are new storage-layer UPDATE helpers — see Task 21.

`insertSkippedDesignRow` and `enqueueAutoDesignReview` (both added by Task 21) are used by the enqueue-handler integration (Task 22) for the direct heuristic-trigger and heuristic-skip paths, where no classify row exists yet.

`wp.changedFilesForJob(job)` — a small helper that shells out to `git diff-tree --name-only -r <sha>` for single-commit jobs and returns `nil` otherwise. Added either here or alongside the enqueue-handler integration (Task 22) wherever it lands first.

- [ ] **Step 6: Run the tests**

Run:
```bash
go test ./internal/daemon/ -run TestWorker_ClassifyJob
```
Expected: PASS (both subtests).

- [ ] **Step 7: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/daemon/worker.go internal/daemon/worker_test.go internal/daemon/worker_test_helpers.go
git commit -m "daemon: worker handler for classify job type"
```

---

### Task 21: auto-design storage helpers + classify-row lifecycle transitions

**Scope:** this task adds all storage-layer pieces the daemon needs for the auto-design flow. It covers five related additions, all in `internal/storage/`:

1. `InsertSkippedDesignJob` — INSERT with `source='auto_design'` + `ON CONFLICT DO NOTHING`.
2. `EnqueueAutoDesignJob` — same, but for queued rows (design review or classify).
3. `PromoteClassifyToDesignReview` — UPDATE classify row → queued design review (with active-attempt guard).
4. `MarkClassifyAsSkippedDesign` — UPDATE classify row → skipped design row (with active-attempt guard).
5. `ListJobsByStatus` — helper query used by the daemon integration + CI poller tests.

All five commit together because they share the `review_jobs` dedup/update semantics. The task is broader than its title suggests; keep that in mind when sequencing vs. Task 20 (the worker handler that consumes helpers 3 and 4) — land Task 21 first or treat 20+21 as a single unit.

**Files:**
- Modify: `internal/daemon/worker.go`
- Modify: `internal/daemon/worker_test.go`
- Modify: `internal/storage/jobs.go`

- [ ] **Step 1: Write tests** (extend the Task 20 tests to cover the exact row shapes)

Append to `internal/daemon/worker_test.go`:

```go
func TestInsertSkippedDesignRow_Fields(t *testing.T) {
	tc := newWorkerTestContext(t, 0)

	parent := tc.createAndClaimClassifyJob(t, "abc123", "feat", "+x\n")
	require.NoError(t, tc.Pool.insertSkippedDesignRow(parent, "trivial"))

	jobs, err := tc.DB.ListJobsByStatus(tc.Repo.ID, storage.JobStatusSkipped)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	j := jobs[0]
	assert.Equal(t, "design", j.ReviewType)
	assert.Equal(t, "trivial", j.SkipReason)
	assert.Equal(t, "abc123", j.GitRef)
	assert.Equal(t, "auto_design", j.Source)
}

func TestEnqueueAutoDesignReview_RespectsDedup(t *testing.T) {
	tc := newWorkerTestContext(t, 0)

	parent := tc.createAndClaimClassifyJob(t, "abc123", "feat", "+x\n")

	// Pre-insert an auto_design design review; enqueueAutoDesignReview must NOT create another.
	_, err := tc.DB.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     tc.Repo.ID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     "abc123",
		JobType:    storage.JobTypeReview,
		ReviewType: "design",
	})
	require.NoError(t, err)

	require.NoError(t, tc.Pool.enqueueAutoDesignReview(parent))

	var n int
	err = tc.DB.QueryRow(
		`SELECT COUNT(*) FROM review_jobs rj JOIN commits c ON rj.commit_id = c.id
		 WHERE rj.repo_id = ? AND c.sha = ? AND rj.review_type = 'design' AND rj.source = 'auto_design'`,
		tc.Repo.ID, "abc123").Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "expected exactly one auto_design design row")
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/daemon/ -run 'TestInsertSkippedDesignRow|TestEnqueueAutoDesignReview'
```
Expected: FAIL.

**Lifecycle helper tests** (in `internal/storage/jobs_test.go`, not the daemon
package — they exercise storage-layer UPDATEs directly):

```go
// Helper to seed a running classify row; used by several of the tests below.
// createRepo returns *Repo and createCommit returns *Commit in this codebase.
func seedRunningClassify(t *testing.T, db *DB, path, sha, workerID string) int64 {
	t.Helper()
	repo := createRepo(t, db, path)
	commit := createCommit(t, db, repo.ID, sha)
	var jobID int64
	require.NoError(t, db.QueryRow(`
		INSERT INTO review_jobs
		  (repo_id, commit_id, git_ref, status, job_type, review_type, source, worker_id, started_at)
		VALUES (?, ?, ?, 'running', 'classify', 'design', 'auto_design', ?, datetime('now'))
		RETURNING id
	`, repo.ID, commit.ID, sha, workerID).Scan(&jobID))
	return jobID
}

func TestPromoteClassifyToDesignReview_HappyPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	jobID := seedRunningClassify(t, db, "/tmp/repo-promote", "abc", "w1")

	// Plant a stale error from a prior classifier attempt — promotion
	// must clear it so it doesn't bleed into the design review's own
	// error field.
	_, err := db.Exec(`UPDATE review_jobs SET error = ? WHERE id = ?`,
		"classifier retry: timeout", jobID)
	require.NoError(t, err)

	require.NoError(t, db.PromoteClassifyToDesignReview(jobID, "w1"))

	j, err := db.GetJobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusQueued, j.Status)
	assert.Equal(t, "review", j.JobType)
	assert.Equal(t, "design", j.ReviewType)
	assert.Equal(t, "auto_design", j.Source)
	assert.Empty(t, j.WorkerID, "worker_id cleared so a new worker can claim")
	assert.Nil(t, j.StartedAt, "started_at cleared — next claim records a fresh start")
	assert.Empty(t, j.Error, "error cleared — classifier-era errors don't bleed into design review")
}

func TestPromoteClassifyToDesignReview_StaleWorkerNoOps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	jobID := seedRunningClassify(t, db, "/tmp/repo-promote-stale", "abc", "w1")

	// Stale worker "w2" attempts promotion — must affect zero rows.
	err := db.PromoteClassifyToDesignReview(jobID, "w2")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	j, err := db.GetJobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusRunning, j.Status, "row unchanged by stale worker")
	assert.Equal(t, "classify", j.JobType)
}

func TestPromoteClassifyToDesignReview_CanceledNoOps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Seed a canceled classify row (not running), owned by w1.
	repo := createRepo(t, db, "/tmp/repo-promote-cancel")
	commit := createCommit(t, db, repo.ID, "abc")
	var jobID int64
	require.NoError(t, db.QueryRow(`
		INSERT INTO review_jobs
		  (repo_id, commit_id, git_ref, status, job_type, review_type, source, worker_id)
		VALUES (?, ?, 'abc', 'canceled', 'classify', 'design', 'auto_design', 'w1')
		RETURNING id
	`, repo.ID, commit.ID).Scan(&jobID))

	err := db.PromoteClassifyToDesignReview(jobID, "w1")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMarkClassifyAsSkippedDesign_HappyPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	jobID := seedRunningClassify(t, db, "/tmp/repo-skip", "abc", "w1")
	require.NoError(t, db.MarkClassifyAsSkippedDesign(jobID, "w1", "trivial diff"))

	j, err := db.GetJobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusSkipped, j.Status)
	assert.Equal(t, "review", j.JobType)
	assert.Equal(t, "trivial diff", j.SkipReason)
}

func TestMarkClassifyAsSkippedDesign_StaleWorkerNoOps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	jobID := seedRunningClassify(t, db, "/tmp/repo-skip-stale", "abc", "w1")

	err := db.MarkClassifyAsSkippedDesign(jobID, "w-other", "some reason")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	j, err := db.GetJobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusRunning, j.Status, "row unchanged by stale worker")
	assert.Equal(t, "classify", j.JobType)
}

func TestMarkClassifyAsSkippedDesign_CanceledNoOps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Seed a canceled classify row owned by w1.
	repo := createRepo(t, db, "/tmp/repo-skip-cancel")
	commit := createCommit(t, db, repo.ID, "abc")
	var jobID int64
	require.NoError(t, db.QueryRow(`
		INSERT INTO review_jobs
		  (repo_id, commit_id, git_ref, status, job_type, review_type, source, worker_id)
		VALUES (?, ?, 'abc', 'canceled', 'classify', 'design', 'auto_design', 'w1')
		RETURNING id
	`, repo.ID, commit.ID).Scan(&jobID))

	err := db.MarkClassifyAsSkippedDesign(jobID, "w1", "some reason")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	j, err := db.GetJobByID(jobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusCanceled, j.Status, "row unchanged — canceled classify is not mutated")
}
```

Run:
```bash
go test ./internal/storage/ -run 'TestPromoteClassifyToDesignReview|TestMarkClassifyAsSkippedDesign'
```
Expected: all six PASS after the helpers land in Step 3.

- [ ] **Step 3: Implement helpers**

Append to `internal/daemon/worker.go`:

```go
// insertSkippedDesignRow writes a row that represents a design review we
// decided NOT to run, so the user can see the decision in the TUI.
// Dedup is enforced atomically by the auto-design unique index; a race with
// another producer yields zero rows inserted, which we treat as success.
func (wp *WorkerPool) insertSkippedDesignRow(parent *storage.ReviewJob, reason string) error {
	return wp.db.InsertSkippedDesignJob(storage.InsertSkippedDesignJobParams{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		SkipReason: reason,
	})
}

// enqueueAutoDesignReview enqueues a design review if one does not already
// exist for this commit. Dedup is enforced atomically by the auto-design
// unique index.
func (wp *WorkerPool) enqueueAutoDesignReview(parent *storage.ReviewJob) error {
	_, err := wp.db.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		JobType:    storage.JobTypeReview,
		ReviewType: "design",
	})
	return err
}
```

Add the storage-layer helper in `internal/storage/jobs.go`:

```go
type InsertSkippedDesignJobParams struct {
	RepoID     int64
	CommitID   int64 // 0 means no commit (range/dirty); binds as NULL
	GitRef     string
	Branch     string
	SkipReason string
}

// nullableCommitID binds a zero CommitID as SQL NULL, matching the convention
// used by EnqueueJob elsewhere in this file.
func nullableCommitID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// InsertSkippedDesignJob inserts a review_job row with status=skipped,
// review_type='design', and source='auto_design'. The auto_design source
// means it participates in the partial unique dedup index; ON CONFLICT
// DO NOTHING makes this a no-op when another auto-design producer already
// recorded the outcome.
func (db *DB) InsertSkippedDesignJob(p InsertSkippedDesignJobParams) error {
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO review_jobs
		  (repo_id, commit_id, git_ref, branch, status, review_type,
		   skip_reason, job_type, source)
		VALUES (?, ?, ?, ?, 'skipped', 'design', ?, 'review', 'auto_design')
		ON CONFLICT DO NOTHING
	`, p.RepoID, nullableCommitID(p.CommitID), p.GitRef, p.Branch, p.SkipReason)
	if err != nil {
		return fmt.Errorf("insert skipped design row: %w", err)
	}
	return nil
}

// EnqueueAutoDesignJob creates a job tagged source='auto_design' (either a
// design review or a classify job). ON CONFLICT DO NOTHING is safe against
// the auto-design unique index; explicit/user-initiated rows leave source
// NULL and use a different path.
//
// Returns the new row's id, or 0 if another producer won the race (no-op).
// Uses EnqueueOpts for consistency with DB.EnqueueJob, but only a subset of
// fields (RepoID, CommitID, GitRef, Branch, JobType, ReviewType) is consulted.
func (db *DB) EnqueueAutoDesignJob(p EnqueueOpts) (int64, error) {
	var id int64
	err := db.QueryRow(`
		INSERT INTO review_jobs
		  (repo_id, commit_id, git_ref, branch, status, job_type,
		   review_type, source)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, 'auto_design')
		ON CONFLICT DO NOTHING
		RETURNING id
	`, p.RepoID, nullableCommitID(p.CommitID), p.GitRef, p.Branch, p.JobType, p.ReviewType).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil // benign — another producer won
	}
	return id, err
}

// PromoteClassifyToDesignReview converts a classify row into a queued
// design review via UPDATE (not INSERT), so the follow-up does not
// collide with the partial unique index that already covers the
// classify row's slot.
//
// The WHERE clause pins this to the active execution attempt
// (status='running' AND worker_id=?), matching the lifecycle-guard
// pattern that CompleteJob / FailJob / CancelJob use. A stale worker
// whose job was canceled, reclaimed, or retried will affect zero rows
// and receive sql.ErrNoRows.
func (db *DB) PromoteClassifyToDesignReview(classifyJobID int64, workerID string) error {
	res, err := db.ExecContext(context.Background(), `
		UPDATE review_jobs
		SET job_type = 'review',
		    status = 'queued',
		    worker_id = NULL,
		    started_at = NULL,
		    finished_at = NULL,
		    prompt = NULL,
		    prompt_prebuilt = 0,
		    error = NULL,
		    updated_at = ?
		WHERE id = ?
		  AND job_type = 'classify'
		  AND source = 'auto_design'
		  AND status = 'running'
		  AND worker_id = ?
	`, time.Now().Format(time.RFC3339), classifyJobID, workerID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MarkClassifyAsSkippedDesign converts a classify row into a terminal
// skipped design row via UPDATE (not INSERT), for the same reason.
// Sets status='skipped', job_type='review', review_type='design' (already
// there), and populates skip_reason. Same active-attempt guard as
// PromoteClassifyToDesignReview.
func (db *DB) MarkClassifyAsSkippedDesign(classifyJobID int64, workerID, reason string) error {
	now := time.Now().Format(time.RFC3339)
	res, err := db.ExecContext(context.Background(), `
		UPDATE review_jobs
		SET job_type = 'review',
		    status = 'skipped',
		    skip_reason = ?,
		    finished_at = ?,
		    updated_at = ?
		WHERE id = ?
		  AND job_type = 'classify'
		  AND source = 'auto_design'
		  AND status = 'running'
		  AND worker_id = ?
	`, reason, now, now, classifyJobID, workerID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListJobsByStatus returns review jobs with the given status for a repo.
// Fields populated: ID, RepoID, CommitID, GitRef, Branch, Status, JobType,
// ReviewType, SkipReason, EnqueuedAt. Callers asserting on job_type require
// it to be selected; don't add fields to the assertion set without also
// extending this query + scan.
func (db *DB) ListJobsByStatus(repoID int64, status JobStatus) ([]ReviewJob, error) {
	rows, err := db.Query(`
		SELECT id, repo_id, COALESCE(commit_id, 0), git_ref, COALESCE(branch, ''),
		       status, COALESCE(job_type, 'review'), COALESCE(review_type, ''),
		       COALESCE(skip_reason, ''), enqueued_at
		FROM review_jobs
		WHERE repo_id = ? AND status = ?
		ORDER BY enqueued_at DESC
	`, repoID, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewJob
	for rows.Next() {
		var j ReviewJob
		var commitID int64
		var enq string
		if err := rows.Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &j.Branch,
			&j.Status, &j.JobType, &j.ReviewType, &j.SkipReason, &enq); err != nil {
			return nil, err
		}
		if commitID != 0 {
			id := commitID
			j.CommitID = &id
		}
		if t, err := time.Parse(time.RFC3339, enq); err == nil {
			j.EnqueuedAt = t
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/daemon/ -run 'TestInsertSkippedDesignRow|TestEnqueueAutoDesignReview'
go test ./internal/storage/
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/daemon/worker.go internal/daemon/worker_test.go internal/daemon/worker_test_helpers.go internal/storage/jobs.go
git commit -m "daemon+storage: enqueueAutoDesignReview and insertSkippedDesignRow with dedup"
```

---

### Task 22: Enqueue-handler integration (heuristics + classify-job dispatch)

**Files:**
- Modify: `internal/daemon/server.go`
- Modify: `internal/daemon/server_integration_test.go`

- [ ] **Step 1: Read the current enqueue handler**

The handler lives near the top of `internal/daemon/server.go` — look for the route registered as `POST /api/enqueue`. Note where a new review job is created after validation.

- [ ] **Step 2: Write failing tests**

Append to `internal/daemon/server_integration_test.go`:

```go
func TestEnqueueAutoDesign_TriggersHeuristic(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)

	// Commit that touches a migration — should trigger.
	srv.MakeCommit(t, "feat: add users table", map[string]string{
		"db/migrations/001_users.sql": "CREATE TABLE users (id INT);",
	})

	resp := srv.EnqueueReview(t, srv.HeadSHA())
	require.Equal(t, 201, resp.StatusCode)

	// Wait briefly for daemon to process the post-enqueue trigger.
	eventuallyHasDesignJob(t, srv.DB, srv.RepoID, srv.HeadSHA(), false /* wantSkipped: expect queued design row */)
}

func TestEnqueueAutoDesign_SkipsViaHeuristic(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)

	srv.MakeCommit(t, "docs: fix typo", map[string]string{
		"README.md": "tweak",
	})

	resp := srv.EnqueueReview(t, srv.HeadSHA())
	require.Equal(t, 201, resp.StatusCode)

	eventuallyHasDesignJob(t, srv.DB, srv.RepoID, srv.HeadSHA(), true /* wantSkipped: expect skipped design row */)
}

func TestEnqueueAutoDesign_DispatchesClassifyJob(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)

	// A commit that falls into the ambiguous middle.
	srv.MakeCommit(t, "feat: small helper", map[string]string{
		"src/helper.go": "package main\n\nfunc Helper() {}\n",
	})
	resp := srv.EnqueueReview(t, srv.HeadSHA())
	require.Equal(t, 201, resp.StatusCode)

	// Immediately after enqueue, a classify job should be queued.
	has := false
	for i := 0; i < 20; i++ {
		jobs, err := srv.DB.ListJobsByStatus(srv.RepoID, storage.JobStatusQueued)
		require.NoError(t, err)
		for _, j := range jobs {
			if j.JobType == storage.JobTypeClassify {
				has = true
				break
			}
		}
		if has {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.True(t, has)
}

func TestEnqueueAutoDesign_EmptyDiffContent_FetchesFromGit(t *testing.T) {
	// Normal commit review: DiffContent is empty on the job row, but the
	// heuristic path must still trigger on migration files via git.GetDiff.
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)

	commitSHA := srv.MakeCommit(t, "feat: schema change", map[string]string{
		"db/migrations/002.sql": "CREATE TABLE foo (id INT);",
	})

	resp := srv.EnqueueReview(t, commitSHA)
	require.Equal(t, 201, resp.StatusCode)

	// Trigger via trigger_paths must fire even though DiffContent is nil.
	eventuallyHasDesignJob(t, srv.DB, srv.RepoID, commitSHA, false /* wantSkipped: expect queued design row */)
}

func TestEnqueueAutoDesign_GitDiffFailure_DefersToClassifier(t *testing.T) {
	// When git.GetDiff returns an error for a live commit (simulated via
	// a test hook that returns a specific error for a sentinel ref), the
	// heuristic pipeline must NOT persist a skipped row with
	// "auto-design: heuristic error" — git errors are input-acquisition
	// failures, not heuristic-engine failures.
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)

	// Make a real commit (so the enqueue itself is valid), then inject
	// a git-fetch error just for the auto-design path. Server exposes a
	// test hook setAutoDesignGitDiffErr(func(repo, sha) error) that the
	// heuristic path consults before calling the real git.GetDiff.
	commitSHA := srv.MakeCommit(t, "feat: real", map[string]string{"src/a.go": "package a"})
	srv.setAutoDesignGitDiffErr(func(_, _ string) error {
		return fmt.Errorf("simulated git failure")
	})
	defer srv.setAutoDesignGitDiffErr(nil)

	// Server exposes setAutoDesignDoneCh() that returns a channel closed
	// after the synchronous auto-design dispatch finishes. Wait on it
	// instead of sleeping — deterministic under any scheduling.
	done := srv.setAutoDesignDoneCh()
	resp := srv.EnqueueReview(t, commitSHA)
	require.Equal(t, 201, resp.StatusCode)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-design dispatch did not complete")
	}

	// Negative: no skipped row was persisted under the "heuristic error"
	// misclassification.
	skipped, err := srv.DB.ListJobsByStatus(srv.RepoID, storage.JobStatusSkipped)
	require.NoError(t, err)
	for _, j := range skipped {
		assert.NotEqual(t, "auto-design: heuristic error", j.SkipReason,
			"git failure must not be misclassified as a heuristic-engine error")
	}

	// Positive: the path must defer to the classifier. An auto-design
	// classify row for this commit must exist.
	queued, err := srv.DB.ListJobsByStatus(srv.RepoID, storage.JobStatusQueued)
	require.NoError(t, err)
	sawClassify := false
	for _, j := range queued {
		if j.GitRef == commitSHA && j.JobType == storage.JobTypeClassify && j.Source == "auto_design" {
			sawClassify = true
			break
		}
	}
	assert.True(t, sawClassify, "git failure must defer to classifier — expected a classify job for %s", commitSHA)
}

func TestEnqueueAutoDesign_HeuristicCtxCanceled_DoesNotPersist(t *testing.T) {
	// A context cancellation DURING heuristic evaluation must not leave
	// a skipped row. Inject a hook that cancels the very context the
	// production code passes in, then returns that context's error.
	// Using `c` (the passed-in context) rather than the outer `ctx`
	// ensures we're testing cancellation on the same context the
	// real heuristic path receives.
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)
	commitSHA := srv.MakeCommit(t, "feat: x", map[string]string{"src/x.go": "package x"})

	ctx, cancel := context.WithCancel(context.Background())

	// The hook receives the ctx the production code actually uses for
	// the heuristic path. Cancel via the enclosed `cancel()` (which
	// cancels the parent of that derived ctx) and return c.Err() to
	// surface cancellation on the exact context under test.
	srv.setAutoDesignHeuristicHook(func(c context.Context) error {
		cancel()
		<-c.Done() // wait until the cancellation actually propagates to c
		return c.Err()
	})
	defer srv.setAutoDesignHeuristicHook(nil)

	done := srv.setAutoDesignDoneCh()
	resp := srv.EnqueueReviewCtx(ctx, t, commitSHA)
	// The enqueue request itself must still succeed. Heuristic-context
	// cancellation is opportunistic work; if it starts bubbling up and
	// turning the main request into a non-201, this assertion catches
	// that regression.
	require.Equal(t, 201, resp.StatusCode)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-design dispatch did not complete")
	}

	jobs, err := srv.DB.ListJobsByStatus(srv.RepoID, storage.JobStatusSkipped)
	require.NoError(t, err)
	for _, j := range jobs {
		assert.NotEqual(t, "auto-design: heuristic error", j.SkipReason,
			"canceled-during-heuristic must not persist a skipped row")
	}
}
```

Add test helpers:

```go
func enableAutoDesignReviewForRepo(t *testing.T, repoPath string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, ".roborev.toml"),
		[]byte(`
[auto_design_review]
enabled = true
`), 0o644))
}

// eventuallyHasDesignJob waits for an auto-design design-review row to
// appear and then asserts its precise shape.
//
// wantSkipped=true → we expect a skipped design row: status='skipped',
// review_type='design', source='auto_design', non-empty skip_reason,
// job_type='review'. Exact status match required.
//
// wantSkipped=false → we expect a non-skipped auto-design review row
// (status must be one of queued/running/done/failed — the row may have
// been claimed or completed by a worker between enqueue and assertion).
// Invariants are still job_type='review', review_type='design',
// source='auto_design', and empty skip_reason.
//
// A classify row (job_type='classify') does NOT satisfy either — callers
// asserting on heuristic trigger/skip must see the final decision, not
// a pending classify row.
func eventuallyHasDesignJob(t *testing.T, db *storage.DB, repoID int64, sha string, wantSkipped bool) {
	t.Helper()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		statuses := []storage.JobStatus{
			storage.JobStatusQueued, storage.JobStatusSkipped,
			storage.JobStatusDone, storage.JobStatusRunning,
			storage.JobStatusFailed, // included because a worker may flip the row to failed quickly
		}
		for _, st := range statuses {
			jobs, err := db.ListJobsByStatus(repoID, st)
			require.NoError(t, err)
			for _, j := range jobs {
				if j.GitRef != sha || j.ReviewType != "design" || j.Source != "auto_design" {
					continue
				}
				if j.JobType == storage.JobTypeClassify {
					continue // heuristic path should not leave a classify row; skip
				}
				if wantSkipped {
					assert.Equal(t, storage.JobStatusSkipped, j.Status)
					assert.NotEmpty(t, j.SkipReason)
				} else {
					// A worker may claim the design review quickly, so any
					// non-skipped status (queued/running/done/failed) is
					// acceptable. The invariant is "not skipped" + no
					// skip_reason — proving the heuristic produced a real
					// design review, not a skipped row.
					validNonSkipped := map[storage.JobStatus]bool{
						storage.JobStatusQueued:  true,
						storage.JobStatusRunning: true,
						storage.JobStatusDone:    true,
						storage.JobStatusFailed:  true,
					}
					assert.True(t, validNonSkipped[j.Status], "unexpected status for non-skipped path: %s", j.Status)
					assert.Empty(t, j.SkipReason)
				}
				assert.Equal(t, "review", j.JobType)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto-design design row not observed for %s (wantSkipped=%v)", sha, wantSkipped)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
```

- [ ] **Step 3: Run to confirm failure**

Run:
```bash
go test ./internal/daemon/ -run TestEnqueueAutoDesign
```
Expected: FAIL — auto design not wired into enqueue.

- [ ] **Step 4: Implement the integration**

In `internal/daemon/server.go`, inside the enqueue handler, after the primary review job is created and before returning 201, add:

```go
// Auto-design-review integration: only for explicit review jobs (not fix,
// not task, not compact). Skip when the caller explicitly set review_type —
// that's an explicit request, not auto-decide.
if job.JobType == storage.JobTypeReview && config.IsDefaultReviewType(req.ReviewType) {
    if err := s.maybeDispatchAutoDesign(r.Context(), job, req); err != nil {
        // Log but don't fail the request — auto-design is opportunistic.
        s.log.Printf("auto-design dispatch failed: %v", err)
    }
}
```

Add the method in the same file (or a new `server_auto_design.go`):

```go
func (s *Server) maybeDispatchAutoDesign(ctx context.Context, parent *storage.ReviewJob, req EnqueueRequest) error {
	cfg, _ := config.LoadGlobal()
	if !config.ResolveAutoDesignEnabled(parent.RepoPath, cfg) {
		return nil
	}

	// Optional fast-path dedup: if a design outcome already exists we can skip
	// the heuristic work entirely. The unique index still enforces dedup, so
	// this check is a performance optimization, not a correctness requirement.
	if parent.CommitID != nil {
		c, err := s.db.GetCommitByID(*parent.CommitID)
		if err == nil && c != nil {
			if has, err := s.db.HasAutoDesignSlotForCommit(parent.RepoID, c.SHA); err == nil && has {
				return nil
			}
		}
	}

	h := config.ResolveAutoDesignHeuristics(parent.RepoPath, cfg)
	hh := autotype.Heuristics{
		MinDiffLines:           h.MinDiffLines,
		LargeDiffLines:         h.LargeDiffLines,
		LargeFileCount:         h.LargeFileCount,
		TriggerPaths:           h.TriggerPaths,
		SkipPaths:              h.SkipPaths,
		TriggerMessagePatterns: h.TriggerMessagePatterns,
		SkipMessagePatterns:    h.SkipMessagePatterns,
	}

	// Gather heuristic inputs. ReviewJob.DiffContent is only populated for
	// dirty reviews; for normal commit reviews we have to fetch the diff
	// from git. If any of the git lookups fail (missing SHA, corrupt repo),
	// fall back to classifier routing — that's the spec's
	// "heuristic-input-failure" policy.
	diff := derefString(parent.DiffContent)
	if diff == "" && parent.GitRef != "" && parent.JobType == storage.JobTypeReview {
		var err error
		diff, err = git.GetDiff(parent.RepoPath, parent.GitRef)
		if err != nil {
			s.log.Printf("auto-design: git.GetDiff(%s) failed, deferring to classifier: %v", parent.GitRef, err)
			diff = "" // treat as ambiguous below
		}
	}

	in := autotype.Input{
		RepoPath:     parent.RepoPath,
		GitRef:       parent.GitRef,
		Diff:         diff,
		Message:      classifierCommitMessage(parent.RepoPath, parent.GitRef, parent.CommitSubject),
		ChangedFiles: s.changedFilesForJob(parent), // returns nil on git error; that's OK, heuristics will fall through
	}

	// Run heuristics only — no classifier yet.
	d, err := autotype.Classify(ctx, in, hh, autotype.ErrOnClassifier{})
	switch {
	case err == nil && d.Method == autotype.MethodHeuristic:
		if d.Run {
			return s.enqueueDesignFollowUp(parent)
		}
		return s.insertSkippedDesign(parent, d.Reason)
	case errors.Is(err, autotype.ErrNeedsClassifier):
		// Ambiguous (including "input acquisition partly failed"):
		// enqueue a classify job.
		return s.enqueueClassifyJob(parent)
	default:
		// Context cancellation / deadline (client disconnected or
		// request timed out) is a transient condition, NOT a broken
		// heuristic — don't persist a skipped row. Log and bail out;
		// the user's next enqueue for the same commit will retry.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.log.Printf("auto-design: context ended during heuristics (%v); skipping opportunistic work", err)
			return nil
		}
		// Any other error from the heuristic engine itself (e.g.,
		// invalid regex in config) is a config-level bug that WILL
		// recur on every enqueue. Log and default to skip rather than
		// spamming design reviews.
		s.log.Printf("auto-design: Classify error, skipping: %v", err)
		return s.insertSkippedDesign(parent, "auto-design: heuristic error")
	}
}
```

Add these thin helpers to `autotype` so the caller can distinguish "needs classifier" from other errors:

In `internal/review/autotype/autotype.go`, add:

```go
import "errors"

// ErrNeedsClassifier is returned by Classify when heuristics are inconclusive
// and the provided Classifier is ErrOnClassifier (used by callers that want
// to dispatch the classifier asynchronously rather than run it inline).
var ErrNeedsClassifier = errors.New("autotype: classifier required")

// ErrOnClassifier is a Classifier implementation that always returns
// ErrNeedsClassifier. Use from code paths that want to see "heuristic-only"
// decisions without blocking on a live agent.
type ErrOnClassifier struct{}

func (ErrOnClassifier) Decide(ctx context.Context, in Input) (bool, string, error) {
	return false, "", ErrNeedsClassifier
}
```

Update `Classify` so the classifier branch passes `ErrNeedsClassifier` through unchanged (it currently wraps errors — leave this one alone):

```go
	yes, reason, err := cls.Decide(ctx, in)
	if err != nil {
		if errors.Is(err, ErrNeedsClassifier) {
			return Decision{}, err
		}
		return Decision{}, err
	}
```

Enqueue-classify-job helper (in `server.go`):

```go
func (s *Server) enqueueClassifyJob(parent *storage.ReviewJob) error {
	_, err := s.db.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		JobType:    storage.JobTypeClassify,
		ReviewType: "design",
	})
	return err
}

func (s *Server) enqueueDesignFollowUp(parent *storage.ReviewJob) error {
	_, err := s.db.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		JobType:    storage.JobTypeReview,
		ReviewType: "design",
	})
	return err
}

func (s *Server) insertSkippedDesign(parent *storage.ReviewJob, reason string) error {
	return s.db.InsertSkippedDesignJob(storage.InsertSkippedDesignJobParams{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		SkipReason: reason,
	})
}

func (s *Server) changedFilesForJob(j *storage.ReviewJob) []string {
	// For single-commit reviews, shell out to git diff-tree; cache per call.
	if j.JobType != storage.JobTypeReview {
		return nil
	}
	files, err := git.GetFilesChanged(j.RepoPath, j.GitRef)
	if err != nil {
		return nil
	}
	return files
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
```

The classifier input uses existing `internal/git` APIs — no new helpers needed:

- `git.GetFilesChanged(repoPath, sha) ([]string, error)` for `ChangedFiles`
- `git.GetDiff(repoPath, sha, extraExcludes ...string) (string, error)` for `Diff`
- `git.GetCommitInfo(repoPath, sha) (*CommitInfo, error)` for `Message`

`GetCommitInfo` returns a struct with `Subject`, `Body` (already parsed), etc. Add a small adapter in the daemon package (not in `internal/git`) that stitches subject + body for the classifier input, with a fallback subject for transient git failures:

```go
// classifierCommitMessage returns "subject\n\nbody" for the classifier,
// or just "subject" if the commit has no body. On git lookup failure,
// returns fallbackSubject (typically the job's CommitSubject field)
// so the classifier still has some message context rather than nothing.
func classifierCommitMessage(repoPath, ref, fallbackSubject string) string {
	info, err := gitpkg.GetCommitInfo(repoPath, ref)
	if err != nil {
		return fallbackSubject
	}
	if info.Body == "" {
		return info.Subject
	}
	return info.Subject + "\n\n" + info.Body
}
```

The worker and enqueue-handler call sites pass `job.CommitSubject` / `parent.CommitSubject` as the fallback:

```go
Message: classifierCommitMessage(job.RepoPath, job.GitRef, job.CommitSubject),
```

- [ ] **Step 5: Run the tests**

Run:
```bash
go test ./internal/daemon/ -run TestEnqueueAutoDesign
```
Expected: PASS (all three subtests).

- [ ] **Step 6: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/daemon/server.go internal/daemon/server_integration_test.go \
        internal/review/autotype/autotype.go internal/git/
git commit -m "daemon: enqueue handler dispatches auto design review path"
```

---

### Task 23: CI poller integration

**Files:**
- Modify: `internal/daemon/ci_poller.go`
- Modify: `internal/daemon/ci_poller_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/daemon/ci_poller_test.go`:

```go
func TestCIPoller_AutoDesign_TriggersWhenHeuristicSaysYes(t *testing.T) {
	env := newCIPollerTestEnv(t)
	defer env.Close()
	env.EnableAutoDesignReview()
	commitSHA := env.AddCommit("feat: add users table", map[string]string{
		"db/migrations/001_users.sql": "CREATE TABLE users (id INT);",
	})
	env.StartMockGitHubPR(commitSHA)

	require.NoError(t, env.Poller.PollOnce(context.Background()))

	has, err := env.DB.HasAutoDesignSlotForCommit(env.RepoID, commitSHA)
	require.NoError(t, err)
	assert.True(t, has)

	jobs, err := env.DB.ListJobsByStatus(env.RepoID, storage.JobStatusQueued)
	require.NoError(t, err)
	foundDesign := false
	for _, j := range jobs {
		if j.ReviewType == "design" && j.GitRef == commitSHA && j.JobType == storage.JobTypeReview {
			foundDesign = true
		}
	}
	assert.True(t, foundDesign)
}

func TestCIPoller_AutoDesign_SkipsWhenHeuristicSaysNo(t *testing.T) {
	env := newCIPollerTestEnv(t)
	defer env.Close()
	env.EnableAutoDesignReview()
	commitSHA := env.AddCommit("docs: fix typo", map[string]string{
		"README.md": "tweak",
	})
	env.StartMockGitHubPR(commitSHA)

	require.NoError(t, env.Poller.PollOnce(context.Background()))

	jobs, err := env.DB.ListJobsByStatus(env.RepoID, storage.JobStatusSkipped)
	require.NoError(t, err)
	found := false
	for _, j := range jobs {
		if j.ReviewType == "design" && j.GitRef == commitSHA {
			found = true
			assert.NotEmpty(t, j.SkipReason)
		}
	}
	assert.True(t, found)
}

func TestCIPoller_AutoDesign_BypassedWhenDesignInReviewTypes(t *testing.T) {
	env := newCIPollerTestEnv(t)
	defer env.Close()
	env.EnableAutoDesignReview()
	env.SetCIReviewTypes([]string{"design"}) // explicit opt-in for every PR
	commitSHA := env.AddCommit("fix: null deref", map[string]string{
		"src/foo.go": "package main",
	})
	env.StartMockGitHubPR(commitSHA)

	require.NoError(t, env.Poller.PollOnce(context.Background()))

	// auto path is bypassed → no classify job, direct design job from matrix.
	jobs, err := env.DB.ListJobsByStatus(env.RepoID, storage.JobStatusQueued)
	require.NoError(t, err)
	for _, j := range jobs {
		assert.NotEqual(t, storage.JobTypeClassify, j.JobType, "auto path should be bypassed")
	}
}
```

Fill in the test bodies using existing CI-poller test helpers; see the existing file for the pattern.

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/daemon/ -run TestCIPoller_AutoDesign
```
Expected: FAIL.

- [ ] **Step 3: Implement**

In `internal/daemon/ci_poller.go`, where the poller builds the review matrix per PR, add:

```go
for _, commit := range prCommits {
	// If "design" is already in review_types, the cross-product path
	// handles it unconditionally — do NOT run auto-decide.
	if reviewTypesContainsDesign(reviewTypes) {
		continue
	}
	cfg, _ := config.LoadGlobal()
	if !config.ResolveAutoDesignEnabled(repo.RootPath, cfg) {
		continue
	}
	if has, _ := p.db.HasAutoDesignSlotForCommit(repo.ID, commit.SHA); has {
		continue
	}

	h := config.ResolveAutoDesignHeuristics(repo.RootPath, cfg)
	hh := autotype.Heuristics{
		MinDiffLines:           h.MinDiffLines,
		LargeDiffLines:         h.LargeDiffLines,
		LargeFileCount:         h.LargeFileCount,
		TriggerPaths:           h.TriggerPaths,
		SkipPaths:              h.SkipPaths,
		TriggerMessagePatterns: h.TriggerMessagePatterns,
		SkipMessagePatterns:    h.SkipMessagePatterns,
	}

	files, _ := git.GetFilesChanged(repo.RootPath, commit.SHA)
	diff, _ := git.GetDiff(repo.RootPath, commit.SHA)

	// Look up the local commit row so skipped/classify rows get CommitID
	// populated — otherwise HasAutoDesignSlotForCommit wouldn't see them
	// and a later local enqueue could create a duplicate auto-design
	// outcome for the same SHA.
	var commitID int64
	if c, err := p.db.GetCommitByRepoAndSHA(repo.ID, commit.SHA); err == nil && c != nil {
		commitID = c.ID
	}

	in := autotype.Input{
		RepoPath:     repo.RootPath,
		GitRef:       commit.SHA,
		Diff:         diff,
		Message:      classifierCommitMessage(repo.RootPath, commit.SHA, commit.Subject), // subject + body via GetCommitInfo; falls back to commit.Subject on error
		ChangedFiles: files,
	}

	d, err := autotype.Classify(ctx, in, hh, autotype.ErrOnClassifier{})
	switch {
	case err == nil && d.Run:
		_ = p.enqueueDesignReviewForCommit(repo, commit)
	case err == nil && !d.Run:
		_ = p.db.InsertSkippedDesignJob(storage.InsertSkippedDesignJobParams{
			RepoID:     repo.ID,
			CommitID:   commitID, // 0 = NULL if we couldn't resolve; kept consistent with the commit-backed index
			GitRef:     commit.SHA,
			SkipReason: d.Reason,
		})
	case errors.Is(err, autotype.ErrNeedsClassifier):
		_ = p.enqueueClassifyJob(repo, commit)
	default:
		p.logger.Printf("auto design classify failed: %v", err)
	}
}

func reviewTypesContainsDesign(types []string) bool {
	for _, t := range types {
		if t == "design" {
			return true
		}
	}
	return false
}
```

Add `enqueueDesignReviewForCommit` and `enqueueClassifyJob` helpers on the poller following the pattern already used in the file for standard enqueues.

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/daemon/ -run TestCIPoller_AutoDesign
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/daemon/ci_poller.go internal/daemon/ci_poller_test.go
git commit -m "daemon: CI poller runs auto design review when enabled"
```

---

## Phase 6 — TUI

### Task 24: Render skipped design rows

**Files:**
- Modify: `cmd/roborev/tui/render_queue.go`
- Modify: `cmd/roborev/tui/queue_test.go`

- [ ] **Step 1: Write failing test**

Append to `cmd/roborev/tui/queue_test.go`, following the `jobCells` pattern already used in that file:

```go
func TestJobCells_Skipped(t *testing.T) {
	// newQueueTestModel(opts ...queueTestModelOption) is the existing helper
	// in queue_test.go — not "newTestQueueModel".
	m := newQueueTestModel()
	j := storage.ReviewJob{
		ID:         42,
		ReviewType: "design",
		Status:     storage.JobStatusSkipped,
		SkipReason: "trivial diff",
		GitRef:     "abc123",
	}
	cells := m.jobCells(j)
	joined := strings.Join(cells, "|")
	assert := assert.New(t)
	assert.Contains(joined, "skipped")
	assert.Contains(joined, "design")
	assert.Contains(joined, "trivial")
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./cmd/roborev/tui/ -run TestJobCells_Skipped
```
Expected: FAIL — the status cell currently doesn't render the `skipped` state or `skip_reason`.

- [ ] **Step 3: Update `jobCells` / the status cell renderer**

In `cmd/roborev/tui/render_queue.go`, the row cells are assembled by the `(*queueModel).jobCells` method (called from `renderQueueView`). Find where it produces the status cell (the existing code handles queued/running/done/failed/canceled/applied/rebased) and add a `JobStatusSkipped` branch that renders with a dimmed style and includes `SkipReason`:

```go
case storage.JobStatusSkipped:
    style := dimStyle
    label := "skipped"
    if j.SkipReason != "" {
        label = fmt.Sprintf("skipped: %s", truncateReason(j.SkipReason, 40))
    }
    statusCell = style.Render(label)
```

Add `truncateReason(s string, max int) string` as a helper if not present.

- [ ] **Step 4: Run the test**

Run:
```bash
go test ./cmd/roborev/tui/ -run TestJobCells_Skipped
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add cmd/roborev/tui/render_queue.go cmd/roborev/tui/queue_test.go
git commit -m "tui: render skipped design review rows with reason"
```

---

### Task 24a: CI synthesis + PR comment includes skipped rows

**Files:**
- Modify: `internal/review/synthesis.go` (or wherever PR comments are composed)
- Modify: `internal/daemon/ci_poller.go` (where `postBatchResults` renders per-job outputs)
- Modify: corresponding `_test.go` files

Per the spec's §"Skipped as a first-class terminal state", a PR batch containing a skipped design row must still synthesize into a PR comment exactly once. The current synthesis path expects every batch job to have an output/verdict; a skipped row has neither (only `SkipReason`). Without this task, a PR whose auto-design path skipped will either crash synthesis or produce an empty section.

- [ ] **Step 1: Grep for the synthesis input shape**

Run:
```bash
grep -rn "BuildSynthesisPrompt\|SynthesisResult\|postBatchResults" internal/review/ internal/daemon/
```

Note the type of the per-job input (usually a slice of `(agent, review_type, output, verdict)` tuples). Skipped rows need a distinct representation there.

- [ ] **Step 2: Write failing test**

Append a test in `internal/review/synthesis_test.go` (or the CI-poller integration test) that runs `BuildSynthesisPrompt` against a batch containing one `done` design review and one `skipped` design row, and asserts:

- The synthesis prompt includes the done review's output.
- The synthesis prompt mentions the skipped row with its `skip_reason`.
- No crash / empty-string panic on the skipped row's missing output.

- [ ] **Step 3: Run to confirm failure**

Run:
```bash
go test ./internal/review/ -run TestBuildSynthesisPrompt_IncludesSkipped
```
Expected: FAIL — current code doesn't handle skipped rows.

- [ ] **Step 4: Extend the synthesis input and renderer**

In `internal/review/synthesis.go`, extend the per-job input struct with a `Skipped bool` and `SkipReason string` (or adjust existing fields). Update `BuildSynthesisPrompt` to render skipped rows as a distinct short section (e.g. "Auto-design-review skipped: trivial diff") rather than failing when `output` is empty.

In `internal/daemon/ci_poller.go::postBatchResults` (and any other caller that builds the synthesis input), include skipped rows in the per-job list with the new fields set.

- [ ] **Step 5: Run the tests**

Run:
```bash
go test ./internal/review/ ./internal/daemon/
```
Expected: PASS.

- [ ] **Step 6: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/review/synthesis.go internal/review/synthesis_test.go internal/daemon/ci_poller.go
git commit -m "review: synthesize skipped design rows into PR comment"
```

---

### Task 24b: Observability counters for auto-design decisions

**Files:**
- Modify: `internal/storage/models.go` (add `AutoDesignStatus` struct + `AutoDesign *AutoDesignStatus` field on `DaemonStatus`). `GetStatusOutput` wraps `DaemonStatus` directly, so the field flows through without touching `huma_types.go`.
- Modify: `internal/daemon/huma_handlers.go` (status handler — populate the subobject when auto-design is enabled)
- Modify: `internal/daemon/server.go` (define the shared metrics helper; enqueue handler's `maybeDispatchAutoDesign` uses it)
- Modify: `internal/daemon/ci_poller.go` (CI poller calls the same metrics helper at its heuristic decision point — without this, CI-driven auto-design decisions never increment the counters)
- Modify: `internal/daemon/worker.go` (classify worker increments on verdict / failure via the same helper)
- Modify: `internal/daemon/huma_routes_test.go` and `internal/daemon/ci_poller_test.go` (new assertions)

Per spec success criterion "Classifier hit rate is observable", the daemon exposes a counter of auto-design outcomes. Implementation is intentionally lightweight — in-memory counters, exposed under `GET /api/status` as an `auto_design` subobject; absent when the feature is disabled.

- [ ] **Step 1: Write failing test**

Append to `internal/daemon/huma_routes_test.go`:

```go
func TestAPIStatus_AutoDesignCounters_Enabled(t *testing.T) {
	// Enable auto_design_review for the test server via per-repo .roborev.toml,
	// trigger one heuristic-trigger (migration file) and one heuristic-skip (docs),
	// call /api/status, assert AutoDesign != nil, TriggeredHeuristic == 1,
	// SkippedHeuristic == 1.
	// Fill in setup using the existing huma-routes test fixture pattern.
}

func TestAPIStatus_AutoDesignCounters_DisabledOmitted(t *testing.T) {
	// With no repo enabling auto_design_review (and no global override),
	// call /api/status and verify the "auto_design" KEY is absent from
	// the JSON body — not just nil. Unmarshalling into *AutoDesignStatus
	// and checking != nil would also pass for the wire shape
	// `"auto_design": null`, which violates the spec's "absent when the
	// feature is disabled" invariant.
	//
	// Do this by unmarshaling into map[string]json.RawMessage:
	//   var raw map[string]json.RawMessage
	//   require.NoError(t, json.Unmarshal(bodyBytes, &raw))
	//   _, present := raw["auto_design"]
	//   assert.False(t, present, "auto_design key must be omitted, not set to null")
	//
	// This is the regression guard for the spec's "absent when the feature
	// is disabled" invariant.
}

// Worker-level integration tests for the classifier-path counters.
// These use the test classifier hook (SetTestClassifierVerdict) from
// Task 20 so the counters advance without invoking a real CLI.

func TestAPIStatus_AutoDesignCounters_ClassifierYes(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)
	commitSHA := srv.MakeCommit(t, "feat: something", map[string]string{"src/x.go": "package x"})

	// Seed a classify row directly (bypassing the heuristic decision so
	// we're only measuring the classifier-path counter).
	srv.CreateClassifyJob(t, commitSHA)
	SetTestClassifierVerdict(true, "new package")
	t.Cleanup(func() { SetTestClassifierVerdict(false, "") })

	srv.RunWorker(t) // processes the classify row end-to-end

	body := srv.APIStatus(t)
	require.NotNil(t, body.AutoDesign)
	assert.EqualValues(t, 1, body.AutoDesign.TriggeredClassifier)
	assert.EqualValues(t, 0, body.AutoDesign.SkippedClassifier)
	assert.EqualValues(t, 0, body.AutoDesign.ClassifierFailed)
}

func TestAPIStatus_AutoDesignCounters_ClassifierNo(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	enableAutoDesignReviewForRepo(t, srv.RepoPath)
	commitSHA := srv.MakeCommit(t, "fix: rename", map[string]string{"src/x.go": "package x"})

	srv.CreateClassifyJob(t, commitSHA)
	SetTestClassifierVerdict(false, "local rename")
	t.Cleanup(func() { SetTestClassifierVerdict(false, "") })

	srv.RunWorker(t)

	body := srv.APIStatus(t)
	require.NotNil(t, body.AutoDesign)
	assert.EqualValues(t, 0, body.AutoDesign.TriggeredClassifier)
	assert.EqualValues(t, 1, body.AutoDesign.SkippedClassifier)
}

func TestAPIStatus_AutoDesignCounters_ClassifierFailed(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	// Write both enabled and the small positive timeout inside the
	// [auto_design_review] section — ResolveClassifierTimeout only
	// honors values > 0, and the setting lives under that section,
	// not the TOML root.
	require.NoError(t, os.WriteFile(filepath.Join(srv.RepoPath, ".roborev.toml"),
		[]byte(`
[auto_design_review]
enabled = true
classifier_timeout_seconds = 1
`), 0o644))
	commitSHA := srv.MakeCommit(t, "feat: x", map[string]string{"src/x.go": "package x"})

	srv.CreateClassifyJob(t, commitSHA)
	// No SetTestClassifierVerdict: the default test agent's classify hook
	// blocks indefinitely, so the 1s timeout fires and drives
	// completeClassifyAsSkip.
	srv.RunWorker(t)

	body := srv.APIStatus(t)
	require.NotNil(t, body.AutoDesign)
	assert.EqualValues(t, 1, body.AutoDesign.ClassifierFailed)
}
```

- [ ] **Step 2: Run to confirm failure**

Run:
```bash
go test ./internal/daemon/ -run TestAPIStatus_AutoDesignCounters
```
Expected: BOTH fail until the handler populates AutoDesign conditionally.

- [ ] **Step 3: Add the counters**

In `internal/daemon/server.go` (or a new `server_auto_design.go` if it doesn't exist yet from Task 22), introduce a small `AutoDesignMetrics` struct with RWMutex + five int64 fields (`TriggeredHeuristic`, `SkippedHeuristic`, `TriggeredClassifier`, `SkippedClassifier`, `ClassifierFailed`). Hang an instance off `Server` and expose two methods:

```go
func (m *AutoDesignMetrics) RecordHeuristic(run bool)        // run=true → TriggeredHeuristic, else SkippedHeuristic
func (m *AutoDesignMetrics) RecordClassifier(run bool, failed bool) // failed=true → ClassifierFailed, else Triggered/SkippedClassifier
```

Every auto-design producer calls these methods. Increment from ALL of:

- `maybeDispatchAutoDesign` (enqueue handler, server.go) — heuristic decisions.
- The heuristic branch in `internal/daemon/ci_poller.go` — CI-driven heuristic decisions. **Without this call, CI-heuristic outcomes never increment the counters** and `/api/status` would misrepresent real daemon activity.
- `processClassifyJob`'s `applyClassifyVerdict` (worker.go) — classifier yes/no.
- `completeClassifyAsSkip` (worker.go) — classifier failure.

Add the response shape to `internal/storage/models.go`:

```go
type AutoDesignStatus struct {
	Enabled             bool  `json:"enabled"`
	TriggeredHeuristic  int64 `json:"triggered_heuristic"`
	SkippedHeuristic    int64 `json:"skipped_heuristic"`
	TriggeredClassifier int64 `json:"triggered_classifier"`
	SkippedClassifier   int64 `json:"skipped_classifier"`
	ClassifierFailed    int64 `json:"classifier_failed"`
}

// Add to DaemonStatus:
//   AutoDesign *AutoDesignStatus `json:"auto_design,omitempty"`
```

In `internal/daemon/huma_handlers.go`, populate `resp.Body.AutoDesign` in the status handler when the feature is effectively enabled for the daemon: either the global config has `auto_design_review.enabled = true`, OR at least one registered repo overrides `enabled = true` via its `.roborev.toml`. A globally-enabled default must still surface the subobject even before any repo is registered. When effectively disabled everywhere, leave `resp.Body.AutoDesign` nil so the JSON output omits the subobject (`omitempty`).

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/daemon/ -run TestAPIStatus_AutoDesign
```
Expected: PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
go fmt ./...
go vet ./...
git add internal/storage/models.go \
        internal/daemon/huma_handlers.go \
        internal/daemon/server.go internal/daemon/worker.go \
        internal/daemon/ci_poller.go internal/daemon/ci_poller_test.go \
        internal/daemon/huma_routes_test.go
git commit -m "daemon: auto-design decision counters on /api/status"
```

---

## Phase 7 — End-to-end verification

### Task 24c: Benchmark the heuristic engine

This benchmark is narrowly scoped: it measures the pure-Go heuristic work performed inside `autotype.Classify`. It does NOT include the enqueue handler's other synchronous work (git metadata collection via `GetCommitInfo`/`GetFilesChanged`/`GetDiff`, the DB dedup INSERT, JSON marshal). The spec's latency target is for the heuristic engine specifically; a full-path benchmark is deferred to a separate follow-up if/when enqueue latency becomes a concern in practice.

**Files:**
- Create: `internal/review/autotype/bench_test.go`

Per spec §"Validating the heuristic-engine target", ship a small benchmark that measures the pure-Go heuristic work `autotype.Classify` performs in isolation. The success criterion (< 25 ms per `autotype.Classify` call — heuristic engine only, NOT the full enqueue path) is verified by running this benchmark locally before enabling the feature.

- [ ] **Step 1: Write the benchmark**

Create `internal/review/autotype/bench_test.go`:

```go
package autotype

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type nilClassifier struct{}

func (nilClassifier) Decide(ctx context.Context, in Input) (bool, string, error) {
	return false, "", ErrNeedsClassifier
}

func BenchmarkClassify_HeuristicTrigger(b *testing.B) {
	in := Input{
		RepoPath:     "/tmp/repo",
		GitRef:       "abc",
		Diff:         "+CREATE TABLE foo (id INT);\n",
		Message:      "feat: add users table",
		ChangedFiles: []string{"db/migrations/001_users.sql"},
	}
	h := DefaultHeuristics()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Classify(context.Background(), in, h, nil)
	}
}

func BenchmarkClassify_HeuristicSkip(b *testing.B) {
	// 30 doc-only lines → falls into skip_paths.
	in := Input{
		RepoPath:     "/tmp/repo",
		GitRef:       "abc",
		Diff:         strings.Repeat("+doc line\n", 30),
		Message:      "docs: update README",
		ChangedFiles: []string{"README.md", "docs/intro.md"},
	}
	h := DefaultHeuristics()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Classify(context.Background(), in, h, nil)
	}
}

func BenchmarkClassify_Ambiguous(b *testing.B) {
	// Real-ish diff outside any heuristic rule → reaches classifier branch.
	// nilClassifier returns ErrNeedsClassifier so we measure heuristic-only
	// work + the branch decision, not any agent invocation.
	in := Input{
		RepoPath:     "/tmp/repo",
		GitRef:       "abc",
		Diff:         strings.Repeat("+x := do()\n", 50),
		Message:      "feat: small helper",
		ChangedFiles: []string{"src/helper.go"},
	}
	h := DefaultHeuristics()
	cls := nilClassifier{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Classify(context.Background(), in, h, cls)
		if err != nil && !errors.Is(err, ErrNeedsClassifier) {
			b.Fatal(err)
		}
	}
}
```

- [ ] **Step 2: Run the benchmark**

Run:
```bash
go test -run=^$ -bench BenchmarkClassify -benchmem ./internal/review/autotype/
```

Expected: each benchmark reports nanoseconds per iteration. The spec target is < 25 ms per call for the heuristic-only paths; current-hardware readings should be well below that (heuristic work is doublestar matches + regex compile+match + a line-count scan over a small diff).

Record the baseline numbers in the commit message; if any of the three exceeds 25 ms per iteration, investigate before proceeding — that's the whole reason this benchmark exists.

- [ ] **Step 3: Commit**

```bash
go fmt ./...
git add internal/review/autotype/bench_test.go
git commit -m "autotype: benchmark heuristic-only enqueue-path cost"
```

---

### Task 25: Full-build + test pass

- [ ] **Step 1: Run the whole test suite**

Run:
```bash
go test ./...
```
Expected: all tests PASS.

- [ ] **Step 2: Run go vet on the full tree**

Run:
```bash
go vet ./...
```
Expected: no output, exit 0.

- [ ] **Step 3: Run the linter**

Run:
```bash
make lint
```
Expected: no errors. If golangci-lint finds issues, fix them in the same commit.

- [ ] **Step 4: Build the binary**

Run:
```bash
go build ./...
```
Expected: no output, exit 0.

- [ ] **Step 5: Smoke test in a throwaway repo** (optional — developer discretion)

Create a throwaway git repo, run `roborev daemon run &`, enable auto-design-review in `~/.roborev/config.toml`, make a commit touching `db/migrations/001.sql`, and check that a design review appeared via `roborev status`.

- [ ] **Step 6: Final formatting commit if needed**

```bash
go fmt ./...
git status
# if any files changed
git add <changed files>
git commit -m "fmt: final cleanup"
```

---

## Phase 8 — Documentation

### Task 26: User-facing docs

**Files:**
- Create: `docs/auto-design-review.md`

- [ ] **Step 1: Write the doc**

Create `docs/auto-design-review.md`:

```markdown
# Auto Design Review

Off by default. When enabled, roborev decides per-commit whether the commit
warrants a design review, using fast path/size/message heuristics first and
a classifier agent fallback for ambiguous cases.

## Enabling

Add to `~/.roborev/config.toml` (global) or `.roborev.toml` (per-repo):

```toml
[auto_design_review]
enabled = true
```

That's it — the defaults pick up schema/migration files, large diffs, spec
paths, and common non-design commit prefixes.

## Classifier agent

The classifier requires an agent with native schema-constrained output.
Supported today:

- `claude-code` (via `--json-schema`)
- `codex` (via `codex exec --output-schema`)

Set via:

```toml
classify_agent = "claude-code"      # or "codex"
classify_reasoning = "fast"
classify_backup_agent = "codex"
```

If you configure an agent without schema support (gemini, copilot, cursor,
etc.), the daemon errors at startup with a clear message naming the valid
choices.

## Tuning

```toml
[auto_design_review]
min_diff_lines = 10
large_diff_lines = 500
large_file_count = 10

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

trigger_message_patterns = [
  '\b(refactor|redesign|rewrite|architect|breaking)\b',
]
skip_message_patterns = [
  '^(docs|test|style|chore)(\(.+\))?:',
]
```

List fields replace wholesale — if you set `trigger_paths` in repo config,
the global defaults are NOT merged.

## What you'll see

- Commits that **trigger**: a design review job appears alongside the
  standard review, normally queued or running.
- Commits that **skip via heuristic**: a `skipped` design row appears with a
  reason like "doc/test-only change" or "trivial diff".
- Commits that go through the **classifier**: a `classify` job runs briefly,
  then either a design review appears or a skipped row does.

## Interaction with existing controls

- `roborev review --type design` always runs — auto mode is not consulted.
- `"design"` in `[ci].review_types` always runs — auto mode bypassed.
- Auto mode only ever adds a design review; it never alters the standard
  review, security review, or any other job.
```

- [ ] **Step 2: Commit**

```bash
git add docs/auto-design-review.md
git commit -m "docs: add auto design review user guide"
```

---

## Self-review checklist

Before considering the plan complete:

1. **Spec coverage**: every goal and non-goal in the spec maps to at least one task above.
2. **No placeholders**: every task has real code, real commands, and specific commit messages.
3. **Types consistency**: `autotype.Decision`, `autotype.Input`, `autotype.Classifier.Decide`, `agent.SchemaAgent.ClassifyWithSchema`, `storage.JobStatusSkipped`, `storage.JobTypeClassify`, `storage.ReviewJob.SkipReason`, `storage.InsertSkippedDesignJobParams` — names match across tasks.
4. **Ordering**: each task leaves the tree buildable and tests passing before the next task's tests are added.
5. **Idempotency**: the migration is re-runnable; dedup queries exist for every enqueue path.
6. **Failure policy**: classifier failures default to skip (insertSkippedDesignRow with a reason), confirmed across Task 20 and Task 22.
