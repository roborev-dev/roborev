# Min-Severity Cascading + Review-Level Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `review_min_severity` to global and per-repo config, promote `refine_min_severity`/`fix_min_severity` to global config, and wire severity filtering into the review prompt pipeline so the agent suppresses sub-threshold findings.

**Architecture:** Three config resolvers walk the cascade (explicit CLI flag -> repo `.roborev.toml` -> global `config.toml` -> `""`). A new `min_severity` column on `review_jobs` stores per-job overrides. The prompt builder injects `SeverityInstruction` into `requiredPrefix`. Verdict parsing recognizes `SEVERITY_THRESHOLD_MET` as pass (after the severity-label gate). A `normalizeMinSeverityForWrite` storage helper guards the column invariant at every write site.

**Tech Stack:** Go, SQLite, PostgreSQL, testify (assert/require)

**Spec:** `docs/superpowers/specs/2026-04-07-min-severity-cascading-design.md`

---

### Task 1: Config — Global severity fields and ResolveReviewMinSeverity

**Files:**
- Modify: `internal/config/config.go:53-140` (Config struct) — add 3 global fields
- Modify: `internal/config/config.go:576-591` (RepoConfig struct) — add ReviewMinSeverity
- Modify: `internal/config/config.go:1443-1473` — update resolver signatures, add new resolver
- Modify: `internal/config/config_test.go:3129-3198` — update test helper, add cases
- Modify: `cmd/roborev/refine.go:351,384` — pass globalCfg to updated resolver
- Modify: `cmd/roborev/fix.go:824-830,1013-1018` — pass globalCfg to updated resolver

- [ ] **Step 1: Write the failing tests**

Update the `resolverFunc` type alias and `runTests` helper in `internal/config/config_test.go` to accept `globalCfg *Config`, add global-tier test cases, and add a third `runTests` call for `ResolveReviewMinSeverity`.

```go
// Replace the existing resolverFunc type alias at line 3130
type resolverFunc func(explicit string, dir string, globalCfg *Config) (string, error)

// Replace the existing runTests helper at lines 3132-3195
runTests := func(
    t *testing.T, name string, fn resolverFunc,
    configKey string, makeGlobal func(string) *Config,
) {
    t.Run(name, func(t *testing.T) {
        tests := []struct {
            testName     string
            explicit     string
            repoConfig   string
            globalConfig *Config
            want         string
            wantErr      bool
        }{
            {
                "default when no config",
                "", "", nil, "", false,
            },
            {
                "repo config when explicit empty",
                "",
                fmt.Sprintf(`%s = "high"`, configKey),
                nil,
                "high", false,
            },
            {
                "explicit overrides repo config",
                "critical",
                fmt.Sprintf(`%s = "medium"`, configKey),
                nil,
                "critical", false,
            },
            {
                "explicit normalization",
                "HIGH", "", nil, "high", false,
            },
            {
                "invalid explicit",
                "bogus", "", nil, "", true,
            },
            {
                "invalid repo config",
                "",
                fmt.Sprintf(`%s = "invalid"`, configKey),
                nil,
                "", true,
            },
            {
                "global wins when repo empty",
                "", "", makeGlobal("medium"), "medium", false,
            },
            {
                "repo wins over global",
                "",
                fmt.Sprintf(`%s = "high"`, configKey),
                makeGlobal("medium"),
                "high", false,
            },
            {
                "explicit wins over both",
                "critical",
                fmt.Sprintf(`%s = "high"`, configKey),
                makeGlobal("medium"),
                "critical", false,
            },
            {
                "empty everywhere returns empty",
                "", "", &Config{}, "", false,
            },
        }

        for _, tt := range tests {
            t.Run(tt.testName, func(t *testing.T) {
                tmpDir := newTempRepo(t, tt.repoConfig)
                got, err := fn(tt.explicit, tmpDir, tt.globalConfig)
                if (err != nil) != tt.wantErr {
                    assert.Condition(t, func() bool {
                        return false
                    }, "error = %v, wantErr %v",
                        err, tt.wantErr)
                }
                if !tt.wantErr && got != tt.want {
                    assert.Condition(t, func() bool {
                        return false
                    }, "got %q, want %q", got, tt.want)
                }
            })
        }
    })
}

// Replace the existing two runTests calls at lines 3197-3198
runTests(t, "Fix", ResolveFixMinSeverity, "fix_min_severity",
    func(v string) *Config { return &Config{FixMinSeverity: v} })
runTests(t, "Refine", ResolveRefineMinSeverity, "refine_min_severity",
    func(v string) *Config { return &Config{RefineMinSeverity: v} })
runTests(t, "Review", ResolveReviewMinSeverity, "review_min_severity",
    func(v string) *Config { return &Config{ReviewMinSeverity: v} })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop -c go test ./internal/config/ -run TestResolveMinSeverity -v`
Expected: Compilation errors — `Config` has no field `FixMinSeverity`, `ResolveReviewMinSeverity` undefined, signature mismatches.

- [ ] **Step 3: Add global fields to Config struct**

In `internal/config/config.go`, after `DesignBackupModel` (line 138), add:

```go
	// Minimum severity thresholds (global defaults)
	ReviewMinSeverity string `toml:"review_min_severity" comment:"Minimum severity for reviews: critical, high, medium, or low. Empty disables filtering."`
	RefineMinSeverity string `toml:"refine_min_severity" comment:"Minimum severity for refine: critical, high, medium, or low. Empty disables filtering."`
	FixMinSeverity    string `toml:"fix_min_severity" comment:"Minimum severity for fix: critical, high, medium, or low. Empty disables filtering."`
```

- [ ] **Step 4: Add ReviewMinSeverity to RepoConfig**

In `internal/config/config.go`, after `RefineMinSeverity` (line 591), add:

```go
	ReviewMinSeverity          string   `toml:"review_min_severity" comment:"Minimum severity for reviews in this repo: critical, high, medium, or low."` // Minimum severity for review: critical, high, medium, low
```

- [ ] **Step 5: Update resolver signatures and add ResolveReviewMinSeverity**

In `internal/config/config.go`, replace `ResolveFixMinSeverity` (lines 1443-1457):

```go
// ResolveFixMinSeverity determines minimum severity for fix.
// Priority: explicit > per-repo config > global config > "" (no filter)
func ResolveFixMinSeverity(
	explicit string, repoPath string, globalCfg *Config,
) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return NormalizeMinSeverity(explicit)
	}
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil &&
		repoCfg != nil &&
		strings.TrimSpace(repoCfg.FixMinSeverity) != "" {
		return NormalizeMinSeverity(repoCfg.FixMinSeverity)
	}
	if globalCfg != nil && strings.TrimSpace(globalCfg.FixMinSeverity) != "" {
		return NormalizeMinSeverity(globalCfg.FixMinSeverity)
	}
	return "", nil
}
```

Replace `ResolveRefineMinSeverity` (lines 1459-1473):

```go
// ResolveRefineMinSeverity determines minimum severity for refine.
// Priority: explicit > per-repo config > global config > "" (no filter)
func ResolveRefineMinSeverity(
	explicit string, repoPath string, globalCfg *Config,
) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return NormalizeMinSeverity(explicit)
	}
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil &&
		repoCfg != nil &&
		strings.TrimSpace(repoCfg.RefineMinSeverity) != "" {
		return NormalizeMinSeverity(repoCfg.RefineMinSeverity)
	}
	if globalCfg != nil && strings.TrimSpace(globalCfg.RefineMinSeverity) != "" {
		return NormalizeMinSeverity(globalCfg.RefineMinSeverity)
	}
	return "", nil
}
```

Add after `ResolveRefineMinSeverity`:

```go
// ResolveReviewMinSeverity determines minimum severity for reviews.
// Priority: explicit > per-repo config > global config > "" (no filter)
func ResolveReviewMinSeverity(
	explicit string, repoPath string, globalCfg *Config,
) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return NormalizeMinSeverity(explicit)
	}
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil &&
		repoCfg != nil &&
		strings.TrimSpace(repoCfg.ReviewMinSeverity) != "" {
		return NormalizeMinSeverity(repoCfg.ReviewMinSeverity)
	}
	if globalCfg != nil && strings.TrimSpace(globalCfg.ReviewMinSeverity) != "" {
		return NormalizeMinSeverity(globalCfg.ReviewMinSeverity)
	}
	return "", nil
}
```

- [ ] **Step 6: Update callers in refine.go**

In `cmd/roborev/refine.go`, line 384, change:

```go
minSev, err := config.ResolveRefineMinSeverity(
    opts.minSeverity, repoPath,
)
```

to:

```go
minSev, err := config.ResolveRefineMinSeverity(
    opts.minSeverity, repoPath, cfg,
)
```

(`cfg` is already loaded at line 351.)

- [ ] **Step 7: Update callers in fix.go**

In `cmd/roborev/fix.go`, update `fixSingleJob` (lines 824-827). Before the `ResolveFixMinSeverity` call, load global config:

```go
var minSev string
if !job.IsTaskJob() {
    cfg, _ := config.LoadGlobal()
    minSev, err = config.ResolveFixMinSeverity(
        opts.minSeverity, repoRoot, cfg,
    )
    if err != nil {
        return fmt.Errorf("resolve min-severity: %w", err)
    }
}
```

In `cmd/roborev/fix.go`, update `runFixBatch` (lines 1013-1015). Move the existing `cfg, _ := config.LoadGlobal()` (currently at ~line 1030) up before the resolver call, or add a new one:

```go
batchCfg, _ := config.LoadGlobal()
minSev, err := config.ResolveFixMinSeverity(
    opts.minSeverity, roots.worktreeRoot, batchCfg,
)
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `nix develop -c go test ./internal/config/ -run TestResolveMinSeverity -v`
Expected: PASS — all cases including new global-tier cases.

Run: `nix develop -c go build ./...`
Expected: Build succeeds — all callers updated.

---

### Task 2: Storage — SQLite migration for min_severity column

**Files:**
- Modify: `internal/storage/db.go:754-764` — append new migration

- [ ] **Step 1: Write the migration**

In `internal/storage/db.go`, after the `worktree_path` migration block (line 764), before `migrateSyncColumns()` (line 767), add:

```go
	// Migration: add min_severity column to review_jobs if missing
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_jobs') WHERE name = 'min_severity'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check min_severity column: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(`ALTER TABLE review_jobs ADD COLUMN min_severity TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return fmt.Errorf("add min_severity column: %w", err)
		}
	}
```

- [ ] **Step 2: Verify migration runs idempotently**

Run: `nix develop -c go test ./internal/storage/ -run TestMigration -v -count=1`
Expected: PASS — existing migration tests still pass (opening a DB triggers `migrate()`; no crash means the ALTER is idempotent via the guard).

---

### Task 3: Storage — Model, hydration, and normalizeMinSeverityForWrite

**Files:**
- Modify: `internal/storage/models.go:49-92` — add MinSeverity field to ReviewJob
- Modify: `internal/storage/hydration.go:9-37` — add MinSeverity to scan fields
- Modify: `internal/storage/hydration.go:39-120` — add copy-through in applyReviewJobScan
- Create: `internal/storage/min_severity.go` — normalizeMinSeverityForWrite helper

- [ ] **Step 1: Add MinSeverity to ReviewJob struct**

In `internal/storage/models.go`, after `WorktreePath` (around line 79), add:

```go
	MinSeverity  string     `json:"min_severity,omitempty"`
```

- [ ] **Step 2: Add MinSeverity to reviewJobScanFields**

In `internal/storage/hydration.go`, after `WorktreePath string` (line 36), add:

```go
	MinSeverity string
```

(Plain `string`, not `sql.NullString` — all queries use `COALESCE(j.min_severity, '')` so the scan target is never null. Matches the `WorktreePath` pattern.)

- [ ] **Step 3: Add copy-through in applyReviewJobScan**

In `internal/storage/hydration.go`, after `job.WorktreePath = fields.WorktreePath` (line 119), add:

```go
	job.MinSeverity = fields.MinSeverity
```

- [ ] **Step 4: Create normalizeMinSeverityForWrite helper**

Create `internal/storage/min_severity.go`:

```go
package storage

import (
	"log"

	"github.com/roborev/internal/config"
)

// normalizeMinSeverityForWrite returns the canonical lowercase value or empty
// string. Invalid input is dropped (returned as empty), not rejected — sync
// ingest from a stale or corrupted remote machine should not fail the entire
// sync over a single bad column. Local user-facing entry points (CLI, daemon)
// validate fail-fast; this storage helper is the last-line guarantee for the
// stored invariant and is intentionally lossy on bad input.
func normalizeMinSeverityForWrite(value string) string {
	normalized, err := config.NormalizeMinSeverity(value)
	if err != nil {
		log.Printf("storage: dropping invalid min_severity %q for write", value)
		return ""
	}
	return normalized
}
```

- [ ] **Step 5: Verify build**

Run: `nix develop -c go build ./internal/storage/...`
Expected: Build succeeds.

---

### Task 4: Storage — Jobs CRUD plumbing

**Files:**
- Modify: `internal/storage/jobs.go:42-64` — add MinSeverity to EnqueueOpts
- Modify: `internal/storage/jobs.go:122-164` — EnqueueJob INSERT + returned struct
- Modify: `internal/storage/jobs.go:209-220` — ClaimJob SELECT
- Modify: `internal/storage/jobs.go:782-786` — ListJobs SELECT
- Modify: `internal/storage/jobs.go:871-874` — GetJobByID SELECT
- Test: `internal/storage/db_job_test.go` — round-trip test

- [ ] **Step 1: Write the failing round-trip test**

Add to `internal/storage/db_job_test.go`:

```go
func TestMinSeverityRoundTrip(t *testing.T) {
	assert := assert.New(t)
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	repo := createRepo(t, db, "/tmp/min-sev-test")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Enqueue with min_severity set
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:      repo.ID,
		CommitID:    commit.ID,
		GitRef:      "abc123",
		Agent:       "test",
		MinSeverity: "high",
	})
	require.NoError(t, err)
	assert.Equal("high", job.MinSeverity)

	// Claim preserves it
	claimed, err := db.ClaimJob("worker-1")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal("high", claimed.MinSeverity)

	// GetJobByID preserves it
	got, err := db.GetJobByID(job.ID)
	require.NoError(t, err)
	assert.Equal("high", got.MinSeverity)

	// ListJobs preserves it
	jobs, err := db.ListJobs(ListJobsOpts{})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal("high", jobs[0].MinSeverity)

	// Empty MinSeverity round-trips as empty
	job2, err := db.EnqueueJob(EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "def456",
		Agent:    "test",
	})
	require.NoError(t, err)
	assert.Equal("", job2.MinSeverity)
}

func TestMinSeverityNormalizesOnWrite(t *testing.T) {
	assert := assert.New(t)
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	repo := createRepo(t, db, "/tmp/min-sev-norm")
	commit := createCommit(t, db, repo.ID, "abc123")

	// Invalid value gets dropped to empty
	job, err := db.EnqueueJob(EnqueueOpts{
		RepoID:      repo.ID,
		CommitID:    commit.ID,
		GitRef:      "abc123",
		Agent:       "test",
		MinSeverity: "bogus",
	})
	require.NoError(t, err)
	assert.Equal("", job.MinSeverity)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop -c go test ./internal/storage/ -run TestMinSeverity -v`
Expected: Compilation error — `EnqueueOpts` has no field `MinSeverity`.

- [ ] **Step 3: Add MinSeverity to EnqueueOpts**

In `internal/storage/jobs.go`, after `WorktreePath` (line 63), add:

```go
	MinSeverity  string // Minimum severity filter (canonical: critical/high/medium/low or empty)
```

- [ ] **Step 4: Update EnqueueJob INSERT**

In `internal/storage/jobs.go`, update the INSERT at lines 122-132. Add `min_severity` to the column list and the normalized value to the params. The full statement becomes:

```go
	result, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, branch, session_id, agent, model, provider, requested_model, requested_provider, reasoning,
			status, job_type, review_type, patch_id, diff_content, prompt, agentic, output_prefix,
			parent_job_id, uuid, source_machine_id, updated_at, worktree_path, min_severity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		opts.RepoID, commitIDParam, gitRef, nullString(opts.Branch), nullString(opts.SessionID),
		opts.Agent, nullString(opts.Model), nullString(opts.Provider), nullString(opts.RequestedModel), nullString(opts.RequestedProvider), reasoning,
		jobType, opts.ReviewType, nullString(opts.PatchID),
		nullString(opts.DiffContent), nullString(opts.Prompt), agenticInt,
		nullString(opts.OutputPrefix), parentJobIDParam,
		uid, machineID, nowStr, opts.WorktreePath, normalizeMinSeverityForWrite(opts.MinSeverity))
```

And add to the returned struct (after `WorktreePath` at line 161):

```go
		MinSeverity:       normalizeMinSeverityForWrite(opts.MinSeverity),
```

- [ ] **Step 5: Update ClaimJob SELECT**

In `internal/storage/jobs.go`, at the `ClaimJob` SELECT (line 209-220), add `COALESCE(j.min_severity, '')` at the end of the column list.

Add `&fields.MinSeverity` to the `Scan` call after `&fields.WorktreePath`. (`ClaimJob` scans into the `reviewJobScanFields` hydration struct, then calls `applyReviewJobScan` — same as `ListJobs` and `GetJobByID`.)

- [ ] **Step 6: Update ListJobs SELECT**

In `internal/storage/jobs.go`, at the `ListJobs` SELECT (lines 782-786), add `COALESCE(j.min_severity, '')` at the end of the column list.

In the scan call, add `&fields.MinSeverity` after `&fields.WorktreePath`.

- [ ] **Step 7: Update GetJobByID SELECT**

In `internal/storage/jobs.go`, at the `GetJobByID` SELECT (lines 871-874), add `COALESCE(j.min_severity, '')` at the end of the column list.

In the scan call, add `&fields.MinSeverity` after `&fields.WorktreePath`. (All three query functions use the hydration struct and `applyReviewJobScan`.)

- [ ] **Step 8: Run tests to verify they pass**

Run: `nix develop -c go test ./internal/storage/ -run TestMinSeverity -v`
Expected: PASS

Run: `nix develop -c go test ./internal/storage/ -v -count=1`
Expected: PASS — all existing tests still pass with the new column.

---

### Task 5: Storage — Reviews queries

**Files:**
- Modify: `internal/storage/reviews.go:19-22` — GetReviewByJobID SELECT
- Modify: `internal/storage/reviews.go:51-54` — GetReviewByCommitSHA SELECT
- Modify: `internal/storage/reviews.go:307-309` — GetJobsWithReviewsByIDs SELECT

These queries return job columns alongside review data. Add `COALESCE(j.min_severity, '')` and the corresponding scan target in each.

- [ ] **Step 1: Update GetReviewByJobID**

In `internal/storage/reviews.go` at the SELECT (lines 19-22), add `COALESCE(j.min_severity, '')` to the job column list (after `j.token_usage`).

Add the scan target in the corresponding `Scan` call — match where `job.TokenUsage` is scanned; add `&job.MinSeverity` after it.

- [ ] **Step 2: Update GetReviewByCommitSHA**

Same pattern as step 1 — identical column list at lines 51-54.

- [ ] **Step 3: Update GetJobsWithReviewsByIDs**

In `internal/storage/reviews.go` at the SELECT (lines 307-309), add `COALESCE(j.min_severity, '')` after `j.review_type`.

Add `&job.MinSeverity` to the scan.

- [ ] **Step 4: Run existing tests**

Run: `nix develop -c go test ./internal/storage/ -v -count=1`
Expected: PASS — all existing tests pass with the extra column.

---

### Task 6: Storage — Sync and PostgreSQL

**Files:**
- Modify: `internal/storage/sync.go:262-295` — add MinSeverity to SyncableJob
- Modify: `internal/storage/sync.go:301-307` — GetJobsToSync SELECT
- Modify: `internal/storage/sync.go:581-607` — UpsertPulledJob INSERT
- Modify: `internal/storage/postgres.go:17,22-23` — bump pgSchemaVersion, embed directive
- Modify: `internal/storage/postgres.go:291-304` — add v11 migration block
- Modify: `internal/storage/postgres.go:554-577` — UpsertJob INSERT (test-only path)
- Modify: `internal/storage/postgres.go:609-639` — add MinSeverity to PulledJob
- Modify: `internal/storage/postgres.go:658-662` — PullJobs SELECT
- Modify: `internal/storage/postgres.go:945-972` — BatchUpsertJobs INSERT (production sync push path)
- Create: `internal/storage/schemas/postgres_v11.sql` — new schema with min_severity

- [ ] **Step 1: Add MinSeverity to SyncableJob struct**

In `internal/storage/sync.go`, after `WorktreePath string` in the `SyncableJob` struct (~line 293), add:

```go
	MinSeverity     string
```

- [ ] **Step 2: Update GetJobsToSync SELECT**

In `internal/storage/sync.go` at the GetJobsToSync SELECT (lines 301-307), add `COALESCE(j.min_severity, '')` after `COALESCE(j.worktree_path, '')`.

Add `&sj.MinSeverity` to the scan call after `&sj.WorktreePath`.

- [ ] **Step 3: Update UpsertPulledJob INSERT**

In `internal/storage/sync.go` at the UpsertPulledJob INSERT (lines 581-607):

Add `min_severity` to the column list (after `worktree_path`).
Add `normalizeMinSeverityForWrite(j.MinSeverity)` to the values (after the worktree_path value).
Add `min_severity = excluded.min_severity` to the ON CONFLICT update list.

- [ ] **Step 4: Create postgres_v11.sql**

Copy `internal/storage/schemas/postgres_v10.sql` to `internal/storage/schemas/postgres_v11.sql`.

In the new file, add `min_severity TEXT NOT NULL DEFAULT ''` to the `review_jobs` table definition, after the `worktree_path` column (around line 65 of the schema file):

```sql
  worktree_path TEXT,
  min_severity TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
```

- [ ] **Step 5: Update pgSchemaVersion and embed directive**

In `internal/storage/postgres.go`:

Change line 17: `const pgSchemaVersion = 10` to `const pgSchemaVersion = 11`

Change line 22-23: `//go:embed schemas/postgres_v10.sql` to `//go:embed schemas/postgres_v11.sql`

And update `var pgSchemaSQL string` on the next line (unchanged, just points to new file now).

- [ ] **Step 6: Add v11 migration block**

In `internal/storage/postgres.go`, after the `if currentVersion < 10` block (line 304), add:

```go
	if currentVersion < 11 {
		_, err = p.pool.Exec(ctx, `ALTER TABLE roborev.review_jobs ADD COLUMN IF NOT EXISTS min_severity TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return fmt.Errorf("v11 migration (min_severity): %w", err)
		}
		_, err = p.pool.Exec(ctx, `INSERT INTO roborev.schema_version (version) VALUES (11)
			ON CONFLICT (version) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("v11 version insert: %w", err)
		}
	}
```

- [ ] **Step 7: Add MinSeverity to PulledJob struct**

In `internal/storage/postgres.go`, after `WorktreePath string` in the `PulledJob` struct (~line 636), add:

```go
	MinSeverity     string
```

- [ ] **Step 8: Update UpsertJob INSERT**

In `internal/storage/postgres.go` at the UpsertJob INSERT (lines 554-577):

Add `min_severity` to the column list (after `worktree_path`).
Add `normalizeMinSeverityForWrite(j.MinSeverity)` to the values.
Add `min_severity = EXCLUDED.min_severity` to the ON CONFLICT update list.

Update the parameter placeholder numbering accordingly.

- [ ] **Step 9: Update BatchUpsertJobs INSERT (production sync push path)**

In `internal/storage/postgres.go` at `BatchUpsertJobs` (line 945), update the INSERT SQL:

Add `min_severity` to the column list (after `worktree_path`).
Add `normalizeMinSeverityForWrite(j.MinSeverity)` to the values (after the `j.SourceMachineID` value).
Add `min_severity = EXCLUDED.min_severity` to the ON CONFLICT update list (after `worktree_path`).

Update the parameter placeholder numbering: currently `$1` through `$21`, the new column becomes `$22` and `NOW()` for `updated_at` moves to inline (it's already inline, not a placeholder).

This is the **production sync push path** — `SyncWorker.pushChangesWithStats` calls `BatchUpsertJobs`, not `UpsertJob`. Without this change, all sync pushes would omit `min_severity` and other machines would see `''` for every synced job.

- [ ] **Step 10: Update PullJobs SELECT**

In `internal/storage/postgres.go` at the PullJobs SELECT (lines 658-662), add `COALESCE(j.min_severity, '')` after `COALESCE(j.worktree_path, '')`.

Add `&pj.MinSeverity` to the scan call after `&pj.WorktreePath`.

- [ ] **Step 11: Verify build**

Run: `nix develop -c go build ./internal/storage/...`
Expected: Build succeeds.

Run: `nix develop -c go test ./internal/storage/ -v -count=1`
Expected: PASS — SQLite tests pass. (PG tests require TEST_POSTGRES_URL and are skipped by default.)

---

### Task 7: Verdict — ParseVerdict SEVERITY_THRESHOLD_MET marker

**Files:**
- Modify: `internal/storage/verdict.go:56-77` — add marker check after hasSeverityLabel
- Test: `internal/storage/verdict_test.go` — add marker test cases

- [ ] **Step 1: Write the failing tests**

Add to `internal/storage/verdict_test.go` (append to the `verdictTests` slice or add a new category):

```go
// --- SeverityThresholdMarker: SEVERITY_THRESHOLD_MET handling ---
{
    name:   "ThresholdMarker/marker alone is pass",
    output: "SEVERITY_THRESHOLD_MET",
    want:   VerdictPass,
},
{
    name:   "ThresholdMarker/marker with surrounding text no findings",
    output: "All findings are below medium severity.\n\nSEVERITY_THRESHOLD_MET\n\nNo code changes needed.",
    want:   VerdictPass,
},
{
    name:   "ThresholdMarker/marker plus severity labels is fail",
    output: "- Medium — some issue\n\nSEVERITY_THRESHOLD_MET",
    want:   VerdictFail,
},
{
    name:   "ThresholdMarker/marker plus high severity is fail",
    output: "SEVERITY_THRESHOLD_MET\n\n- High: critical bug found",
    want:   VerdictFail,
},
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop -c go test ./internal/storage/ -run TestParseVerdict/ThresholdMarker -v`
Expected: FAIL — "marker alone is pass" returns "F" instead of "P".

- [ ] **Step 3: Add marker check to ParseVerdict**

In `internal/storage/verdict.go`, add import for `config` package (it already imports `strings`):

```go
import (
    "database/sql"
    "strings"

    "github.com/roborev/internal/config"
)
```

In `ParseVerdict` (line 56-77), after the `hasSeverityLabel` gate (line 59-61), add:

```go
	// Marker signals pass ONLY when no severity labels are present.
	if strings.Contains(output, config.SeverityThresholdMarker) {
		return verdictPass
	}
```

The full function becomes:
```go
func ParseVerdict(output string) string {
    if hasSeverityLabel(output) {
        return verdictFail
    }

    if strings.Contains(output, config.SeverityThresholdMarker) {
        return verdictPass
    }

    for line := range strings.SplitSeq(output, "\n") {
        // ... existing pass-phrase loop unchanged
    }
    return verdictFail
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop -c go test ./internal/storage/ -run TestParseVerdict -v`
Expected: PASS — all cases including new marker cases.

---

### Task 8: Prompt — Build/BuildDirty severity injection

**Files:**
- Modify: `internal/prompt/prompt.go:166-171` — Build signature
- Modify: `internal/prompt/prompt.go:176` — BuildDirty signature + injection
- Modify: `internal/prompt/prompt.go:450` — buildSinglePrompt signature + injection
- Modify: `internal/prompt/prompt.go:567` — buildRangePrompt signature + injection
- Test: `internal/prompt/prompt_test.go` — injection tests

- [ ] **Step 1: Write the failing tests**

Add to `internal/prompt/prompt_test.go`:

```go
func TestBuildInjectsSeverityInstruction(t *testing.T) {
	assert := assert.New(t)
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	builder := NewBuilder(nil)

	tests := []struct {
		name        string
		reviewType  string
		minSeverity string
		wantContain string
		wantAbsent  string
	}{
		{"standard with medium", "", "medium", "Severity filter:", ""},
		{"security with high", "security", "high", "Severity filter:", ""},
		{"design with critical", "design", "critical", "Severity filter:", ""},
		{"empty severity skips injection", "", "", "", "Severity filter:"},
		{"low severity skips injection", "", "low", "", "Severity filter:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, err := builder.Build(repoPath, targetSHA, 0, 0, "test", tt.reviewType, tt.minSeverity)
			require.NoError(t, err)
			if tt.wantContain != "" {
				assert.Contains(prompt, tt.wantContain)
			}
			if tt.wantAbsent != "" {
				assert.NotContains(prompt, tt.wantAbsent)
			}
		})
	}
}

func TestBuildDirtyInjectsSeverityInstruction(t *testing.T) {
	assert := assert.New(t)
	repoPath, _ := setupTestRepo(t)
	builder := NewBuilder(nil)

	diff := "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n"

	prompt, err := builder.BuildDirty(repoPath, diff, 0, 0, "test", "", "high")
	require.NoError(t, err)
	assert.Contains(prompt, "Severity filter:")
	assert.Contains(prompt, "SEVERITY_THRESHOLD_MET")

	// Empty severity: no injection
	prompt2, err := builder.BuildDirty(repoPath, diff, 0, 0, "test", "", "")
	require.NoError(t, err)
	assert.NotContains(prompt2, "Severity filter:")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop -c go test ./internal/prompt/ -run TestBuild.*SeverityInstruction -v`
Expected: Compilation error — `Build` and `BuildDirty` have wrong number of arguments.

- [ ] **Step 3: Update Build signature**

In `internal/prompt/prompt.go`, update `Build` (line 166):

```go
func (b *Builder) Build(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType, minSeverity string) (string, error) {
	if git.IsRange(gitRef) {
		return b.buildRangePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, minSeverity)
	}
	return b.buildSinglePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, minSeverity)
}
```

- [ ] **Step 4: Update BuildDirty signature and inject severity**

In `internal/prompt/prompt.go`, update `BuildDirty` (line 176):

```go
func (b *Builder) BuildDirty(repoPath, diff string, repoID int64, contextCount int, agentName, reviewType, minSeverity string) (string, error) {
```

After `requiredPrefix` construction (line 185), inject severity:

```go
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"
	if inst := config.SeverityInstruction(minSeverity); inst != "" {
		requiredPrefix += inst + "\n"
	}
```

- [ ] **Step 5: Update buildSinglePrompt signature and inject severity**

In `internal/prompt/prompt.go`, update `buildSinglePrompt` (line 450):

```go
func (b *Builder) buildSinglePrompt(repoPath, sha string, repoID int64, contextCount int, agentName, reviewType, minSeverity string) (string, error) {
```

After `requiredPrefix` construction (line 459), inject severity:

```go
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"
	if inst := config.SeverityInstruction(minSeverity); inst != "" {
		requiredPrefix += inst + "\n"
	}
```

- [ ] **Step 6: Update buildRangePrompt signature and inject severity**

In `internal/prompt/prompt.go`, update `buildRangePrompt` (line 567):

```go
func (b *Builder) buildRangePrompt(repoPath, rangeRef string, repoID int64, contextCount int, agentName, reviewType, minSeverity string) (string, error) {
```

After `requiredPrefix` construction (line 576), inject severity:

```go
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"
	if inst := config.SeverityInstruction(minSeverity); inst != "" {
		requiredPrefix += inst + "\n"
	}
```

- [ ] **Step 7: Fix all callers of Build/BuildDirty**

Every existing call to `Build` or `BuildDirty` now needs the trailing `minSeverity` parameter. Pass `""` at sites that get proper values in later tasks.

Run to enumerate all call sites:
```bash
nix develop -c rg '\.Build\(|\.BuildDirty\(' internal/prompt/ internal/daemon/ internal/review/ cmd/roborev/ --no-heading -n -g '*.go' -g '!*_test.go'
```

Known production callers (update each with trailing `, ""`):

| File | Line | Call | Proper value added in |
|------|------|------|----------------------|
| `internal/daemon/worker.go` | 398 | `pb.BuildDirty(...)` | Task 10 |
| `internal/daemon/worker.go` | 401 | `pb.Build(...)` | Task 10 |
| `cmd/roborev/review.go` | ~431 | `pb.BuildDirty(...)` | Task 12 |
| `cmd/roborev/review.go` | ~433 | `pb.Build(...)` | Task 12 |
| `internal/review/batch.go` | 152 | `builder.Build(...)` | Task 12.5 |

Also update any test callers of `Build`/`BuildDirty` in `internal/prompt/prompt_test.go` (existing tests, not the new ones from Step 1) to add `, ""`. The new tests from Step 1 already include the parameter.

After updating, verify the full list is covered:
```bash
nix develop -c go build ./...
```
A compilation error means a caller was missed — fix it by adding `, ""`.

- [ ] **Step 8: Run tests to verify they pass**

Run: `nix develop -c go test ./internal/prompt/ -run TestBuild.*SeverityInstruction -v`
Expected: PASS

Run: `nix develop -c go build ./...`
Expected: Build succeeds — all callers compile.

---

### Task 9: Daemon — EnqueueRequest and handleEnqueue

**Files:**
- Modify: `internal/daemon/server.go:673-689` — add MinSeverity to EnqueueRequest
- Modify: `internal/daemon/server.go:860-875` — validate MinSeverity in handleEnqueue
- Modify: `internal/daemon/server.go:955-971,979-993,1056-1069,1108-1123` — pass MinSeverity in EnqueueOpts

- [ ] **Step 1: Add MinSeverity to EnqueueRequest**

In `internal/daemon/server.go`, add to the `EnqueueRequest` struct (after `Provider`, line 688):

```go
	MinSeverity  string `json:"min_severity,omitempty"`  // Minimum severity filter: critical, high, medium, low
```

- [ ] **Step 2: Add validation in handleEnqueue**

In `internal/daemon/server.go`, after the reasoning resolution block (lines 871-875), add:

```go
	// Validate min_severity if provided
	var normalizedMinSev string
	if strings.TrimSpace(req.MinSeverity) != "" {
		normalizedMinSev, err = config.NormalizeMinSeverity(req.MinSeverity)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
```

- [ ] **Step 3: Thread MinSeverity into all EnqueueOpts literals**

Add `MinSeverity: normalizedMinSev` to each of the four `EnqueueOpts` literals:

1. Custom prompt job (line ~955): add after `WorktreePath`
2. Dirty review (line ~979): add after `WorktreePath`
3. Range job (line ~1056): add after `WorktreePath`
4. Single commit (line ~1108): add after `WorktreePath`

- [ ] **Step 4: Write handleEnqueue tests**

Add to `internal/daemon/server_test.go`:

```go
func TestHandleEnqueueMinSeverity(t *testing.T) {
	t.Run("valid min_severity stored as canonical lowercase", func(t *testing.T) {
		s, db, tmpDir := newTestServer(t)
		testutil.InitTestGitRepo(t, tmpDir)
		repo, err := db.GetOrCreateRepo(tmpDir)
		require.NoError(t, err)

		body := fmt.Sprintf(`{"repo_path":%q,"git_ref":"HEAD","min_severity":"HIGH"}`, tmpDir)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		jobs, err := db.ListJobs(storage.ListJobsOpts{RepoID: repo.ID})
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		assert.Equal(t, "high", jobs[0].MinSeverity)
	})

	t.Run("invalid min_severity returns 400", func(t *testing.T) {
		s, _, tmpDir := newTestServer(t)
		testutil.InitTestGitRepo(t, tmpDir)

		body := fmt.Sprintf(`{"repo_path":%q,"git_ref":"HEAD","min_severity":"bogus"}`, tmpDir)
		req := httptest.NewRequest(http.MethodPost, "/api/enqueue", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "invalid min_severity")
	})
}
```

(Adjust helper names to match the actual test patterns — the existing `newTestServer` or `newTestServerWithDir` function. The key assertions are: valid mixed-case normalizes to lowercase on the stored job; invalid value returns 400.)

- [ ] **Step 5: Run tests**

Run: `nix develop -c go test ./internal/daemon/ -run TestHandleEnqueueMinSeverity -v -timeout=120s`
Expected: PASS.

Run: `nix develop -c go test ./internal/daemon/ -v -count=1 -timeout=120s`
Expected: PASS — all existing tests still pass.

---

### Task 10: Daemon — Worker prompt resolution

**Files:**
- Modify: `internal/daemon/worker.go:396-402` — resolve min-severity and thread into Build/BuildDirty

- [ ] **Step 1: Add severity resolution in worker**

In `internal/daemon/worker.go`, replace the dirty/normal prompt building block (lines 396-402):

```go
	} else if job.DiffContent != nil {
		// Dirty job - use pre-captured diff
		reviewPrompt, err = pb.BuildDirty(effectiveRepoPath, *job.DiffContent, job.RepoID, cfg.ReviewContextCount, job.Agent, job.ReviewType)
	} else {
		// Normal job - build prompt from git ref
		reviewPrompt, err = pb.Build(effectiveRepoPath, job.GitRef, job.RepoID, cfg.ReviewContextCount, job.Agent, job.ReviewType)
	}
```

with:

```go
	} else {
		// Resolve effective min-severity: job-level override wins, then cascade.
		minSev := job.MinSeverity
		if minSev == "" {
			resolved, resErr := config.ResolveReviewMinSeverity("", effectiveRepoPath, cfg)
			if resErr != nil {
				log.Printf("[%s] Error resolving min-severity: %v", workerID, resErr)
				wp.failOrRetry(workerID, job, job.Agent, fmt.Sprintf("resolve min-severity: %v", resErr))
				return
			}
			minSev = resolved
		}

		if job.DiffContent != nil {
			// Dirty job - use pre-captured diff
			reviewPrompt, err = pb.BuildDirty(effectiveRepoPath, *job.DiffContent, job.RepoID, cfg.ReviewContextCount, job.Agent, job.ReviewType, minSev)
		} else {
			// Normal job - build prompt from git ref
			reviewPrompt, err = pb.Build(effectiveRepoPath, job.GitRef, job.RepoID, cfg.ReviewContextCount, job.Agent, job.ReviewType, minSev)
		}
	}
```

Ensure `config` is imported (it likely already is via other usages in this file).

- [ ] **Step 2: Write worker min-severity tests**

Add to `internal/daemon/worker_test.go`:

```go
func TestProcessJob_MinSeverityCascade(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	var capturedPrompt string
	agentName := "min-sev-cascade-capture"
	agent.Register(&agent.FakeAgent{
		NameStr: agentName,
		ReviewFn: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
			capturedPrompt = prompt
			return "No issues found.", nil
		},
	})
	t.Cleanup(func() { agent.Unregister(agentName) })

	// Set global config with ReviewMinSeverity
	cfg := config.DefaultConfig()
	cfg.ReviewMinSeverity = "medium"
	tc.Pool = NewWorkerPool(tc.DB, NewStaticConfig(cfg), 1, tc.Broadcaster, nil, nil)

	job := tc.createJobWithAgent(t, sha, agentName)
	claimed, err := tc.DB.ClaimJob("test-worker")
	require.NoError(t, err)
	require.Equal(t, job.ID, claimed.ID)

	tc.Pool.processJob("test-worker", claimed)

	tc.assertJobStatus(t, job.ID, storage.JobStatusDone)
	assert.Contains(t, capturedPrompt, "Severity filter:")
	assert.Contains(t, capturedPrompt, "SEVERITY_THRESHOLD_MET")
}

func TestProcessJob_MinSeverityJobOverrideWins(t *testing.T) {
	tc := newWorkerTestContext(t, 1)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)

	var capturedPrompt string
	agentName := "min-sev-override-capture"
	agent.Register(&agent.FakeAgent{
		NameStr: agentName,
		ReviewFn: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
			capturedPrompt = prompt
			return "No issues found.", nil
		},
	})
	t.Cleanup(func() { agent.Unregister(agentName) })

	// Global says "medium" but job says "critical"
	cfg := config.DefaultConfig()
	cfg.ReviewMinSeverity = "medium"
	tc.Pool = NewWorkerPool(tc.DB, NewStaticConfig(cfg), 1, tc.Broadcaster, nil, nil)

	commit, err := tc.DB.GetOrCreateCommit(tc.Repo.ID, sha, "Author", "Subject", time.Now())
	require.NoError(t, err)
	job, err := tc.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID:      tc.Repo.ID,
		CommitID:    commit.ID,
		GitRef:      sha,
		Agent:       agentName,
		MinSeverity: "critical",
	})
	require.NoError(t, err)

	claimed, err := tc.DB.ClaimJob("test-worker")
	require.NoError(t, err)
	require.Equal(t, job.ID, claimed.ID)

	tc.Pool.processJob("test-worker", claimed)

	tc.assertJobStatus(t, job.ID, storage.JobStatusDone)
	assert.Contains(t, capturedPrompt, "Only include Critical findings.")
}
```

- [ ] **Step 3: Run tests**

Run: `nix develop -c go test ./internal/daemon/ -run TestProcessJob_MinSeverity -v -count=1 -timeout=120s`
Expected: PASS.

Run: `nix develop -c go test ./internal/daemon/ -v -count=1 -timeout=120s`
Expected: PASS — all existing worker tests still pass.

---

### Task 11: Daemon — handleFixJob severity baking

**Files:**
- Modify: `internal/daemon/server.go:2346-2508` — handleFixJob
- Modify: `internal/daemon/server.go:2708-2732` — buildFixPrompt and buildFixPromptWithInstructions signatures

- [ ] **Step 1: Update buildFixPrompt and buildFixPromptWithInstructions**

In `internal/daemon/server.go`, update `buildFixPrompt` (line 2708):

```go
func buildFixPrompt(reviewOutput, minSeverity string) string {
	return buildFixPromptWithInstructions(reviewOutput, "", minSeverity)
}
```

Update `buildFixPromptWithInstructions` (line 2714):

```go
func buildFixPromptWithInstructions(reviewOutput, userInstructions, minSeverity string) string {
	prompt := "# Fix Request\n\n" +
		"An analysis was performed and produced the following findings:\n\n"
	if inst := config.SeverityInstruction(minSeverity); inst != "" {
		prompt += inst + "\n"
	}
	prompt += "## Analysis Findings\n\n" +
		reviewOutput + "\n\n"
	if userInstructions != "" {
		prompt += "## Additional Instructions\n\n" +
			userInstructions + "\n\n"
	}
	prompt += "## Instructions\n\n" +
		"Please apply the suggested changes from the analysis above. " +
		"Make the necessary edits to address each finding. " +
		"Focus on the highest priority items first.\n\n" +
		"After making changes:\n" +
		"1. Verify the code still compiles/passes linting\n" +
		"2. Run any relevant tests to ensure nothing is broken\n" +
		"3. Stage the changes with git add but do NOT commit — the changes will be captured as a patch\n"
	return prompt
}
```

`buildRebasePrompt` is NOT changed — it stays threshold-free.

- [ ] **Step 2: Add severity resolution in handleFixJob**

In `internal/daemon/server.go` `handleFixJob`, after `fixPrompt == ""` check (line 2415), before the review output fetching, add severity resolution:

```go
	// Resolve fix min-severity (skip for task/insights parents — their
	// free-form output has no severity-labeled findings to filter).
	var fixMinSev string
	if !parentJob.IsTaskJob() {
		effectivePath := parentJob.RepoPath
		if parentJob.WorktreePath != "" &&
			gitpkg.ValidateWorktreeForRepo(parentJob.WorktreePath, parentJob.RepoPath) {
			effectivePath = parentJob.WorktreePath
		}
		cfg := s.configWatcher.Config()
		resolved, resolveErr := config.ResolveFixMinSeverity("", effectivePath, cfg)
		if resolveErr != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("resolve fix min-severity: %v", resolveErr))
			return
		}
		fixMinSev = resolved
	}
```

Note: This block must be placed AFTER the `buildRebasePrompt` branch (which returns early when `fixPrompt` is set from a rebase) and BEFORE the review-output branch. The cleanest insertion point is to restructure slightly:

Replace the fix prompt building block (lines 2415-2426):

```go
	if fixPrompt == "" {
		// Resolve fix min-severity (skip for task/insights parents)
		var fixMinSev string
		if !parentJob.IsTaskJob() {
			effectivePath := parentJob.RepoPath
			if parentJob.WorktreePath != "" &&
				gitpkg.ValidateWorktreeForRepo(parentJob.WorktreePath, parentJob.RepoPath) {
				effectivePath = parentJob.WorktreePath
			}
			cfg := s.configWatcher.Config()
			resolved, resolveErr := config.ResolveFixMinSeverity("", effectivePath, cfg)
			if resolveErr != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("resolve fix min-severity: %v", resolveErr))
				return
			}
			fixMinSev = resolved
		}

		// Fetch the review output for the parent job
		review, err := s.db.GetReviewByJobID(req.ParentJobID)
		if err != nil || review == nil {
			writeError(w, http.StatusBadRequest, "parent job has no review to fix")
			return
		}
		if req.Prompt != "" {
			fixPrompt = buildFixPromptWithInstructions(review.Output, req.Prompt, fixMinSev)
		} else {
			fixPrompt = buildFixPrompt(review.Output, fixMinSev)
		}
	}
```

Ensure `gitpkg` alias is imported (check existing imports — it's likely `gitpkg "github.com/roborev/internal/git"` or just `"github.com/roborev/internal/git"`).

- [ ] **Step 3: Fix callers of buildFixPrompt/buildFixPromptWithInstructions outside handleFixJob**

Search for other callers of `buildFixPrompt`:

```bash
nix develop -c rg 'buildFixPrompt\(' internal/daemon/server.go --no-heading -n
```

If there are any other callers (e.g., in tests or batch processing), update them to pass `""` as the trailing `minSeverity` argument.

- [ ] **Step 4: Write handleFixJob tests**

Add to `internal/daemon/server_test.go`:

```go
func TestHandleFixJobMinSeverity(t *testing.T) {
	t.Run("bakes severity instruction into stored prompt", func(t *testing.T) {
		s, db, tmpDir := newTestServer(t)
		testutil.InitTestGitRepo(t, tmpDir)

		// Write a .roborev.toml with fix_min_severity = "high"
		os.WriteFile(filepath.Join(tmpDir, ".roborev.toml"), []byte(`fix_min_severity = "high"`), 0644)

		// Create parent review job with output
		parentJob := createTestJob(t, db, tmpDir, testutil.GetHeadSHA(t, tmpDir), "test")
		setJobStatus(t, db, parentJob.ID, storage.JobStatusDone)
		_, err := db.CreateReview(parentJob.ID, "test", "prompt", "- High — some bug\nDetails here")
		require.NoError(t, err)

		body := fmt.Sprintf(`{"parent_job_id":%d}`, parentJob.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/job/fix", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		// Verify the fix job's stored prompt contains severity instruction
		var fixJob storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&fixJob)
		got, err := db.GetJobByID(fixJob.ID)
		require.NoError(t, err)
		assert.Contains(t, got.Prompt, "Severity filter:")
		assert.Contains(t, got.Prompt, "SEVERITY_THRESHOLD_MET")
	})

	t.Run("skips severity for task parent", func(t *testing.T) {
		s, db, tmpDir := newTestServer(t)
		testutil.InitTestGitRepo(t, tmpDir)

		// Write a .roborev.toml with fix_min_severity
		os.WriteFile(filepath.Join(tmpDir, ".roborev.toml"), []byte(`fix_min_severity = "high"`), 0644)

		// Create a task parent (has prompt, JobType=task)
		repo, err := db.GetOrCreateRepo(tmpDir)
		require.NoError(t, err)
		parentJob, err := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:  repo.ID,
			Agent:   "test",
			Prompt:  "analyze the code",
			JobType: storage.JobTypeTask,
		})
		require.NoError(t, err)
		setJobStatus(t, db, parentJob.ID, storage.JobStatusDone)
		_, err = db.CreateReview(parentJob.ID, "test", "prompt", "Found some issues")
		require.NoError(t, err)

		body := fmt.Sprintf(`{"parent_job_id":%d}`, parentJob.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/job/fix", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var fixJob storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&fixJob)
		got, err := db.GetJobByID(fixJob.ID)
		require.NoError(t, err)
		assert.NotContains(t, got.Prompt, "Severity filter:")
	})

	t.Run("rebase prompt has no severity instruction", func(t *testing.T) {
		s, db, tmpDir := newTestServer(t)
		testutil.InitTestGitRepo(t, tmpDir)

		// Write a .roborev.toml with fix_min_severity
		os.WriteFile(filepath.Join(tmpDir, ".roborev.toml"), []byte(`fix_min_severity = "high"`), 0644)

		// Create parent + stale fix job with patch
		parentJob := createTestJob(t, db, tmpDir, testutil.GetHeadSHA(t, tmpDir), "test")
		setJobStatus(t, db, parentJob.ID, storage.JobStatusDone)
		_, err := db.CreateReview(parentJob.ID, "test", "prompt", "- High — bug")
		require.NoError(t, err)

		patch := "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n"
		staleJob, err := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      parentJob.RepoID,
			GitRef:      "HEAD",
			Agent:       "test",
			Prompt:      "fix stuff",
			JobType:     storage.JobTypeFix,
			ParentJobID: parentJob.ID,
		})
		require.NoError(t, err)
		setJobStatus(t, db, staleJob.ID, storage.JobStatusDone)
		// Store patch on the stale job
		db.Exec(`UPDATE review_jobs SET patch = ? WHERE id = ?`, patch, staleJob.ID)

		body := fmt.Sprintf(`{"parent_job_id":%d,"stale_job_id":%d}`, parentJob.ID, staleJob.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/job/fix", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var fixJob storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&fixJob)
		got, err := db.GetJobByID(fixJob.ID)
		require.NoError(t, err)
		assert.NotContains(t, got.Prompt, "Severity filter:")
		assert.Contains(t, got.Prompt, "Rebase Fix Request")
	})

	t.Run("worktree config overrides repo config", func(t *testing.T) {
		s, db, tmpDir := newTestServer(t)
		testutil.InitTestGitRepo(t, tmpDir)

		// Main repo: fix_min_severity = "medium"
		os.WriteFile(filepath.Join(tmpDir, ".roborev.toml"), []byte(`fix_min_severity = "medium"`), 0644)

		// Create a worktree dir with a different config
		wtDir := t.TempDir()
		os.WriteFile(filepath.Join(wtDir, ".roborev.toml"), []byte(`fix_min_severity = "critical"`), 0644)

		// Create parent with WorktreePath set to the worktree dir
		repo, err := db.GetOrCreateRepo(tmpDir)
		require.NoError(t, err)
		sha := testutil.GetHeadSHA(t, tmpDir)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "A", "S", time.Now())
		require.NoError(t, err)
		parentJob, err := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:       repo.ID,
			CommitID:     commit.ID,
			GitRef:       sha,
			Agent:        "test",
			WorktreePath: wtDir,
		})
		require.NoError(t, err)
		setJobStatus(t, db, parentJob.ID, storage.JobStatusDone)
		_, err = db.CreateReview(parentJob.ID, "test", "prompt", "- High — bug")
		require.NoError(t, err)

		body := fmt.Sprintf(`{"parent_job_id":%d}`, parentJob.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/job/fix", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var fixJob storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&fixJob)
		got, err := db.GetJobByID(fixJob.ID)
		require.NoError(t, err)
		// Should use worktree's "critical", not main repo's "medium"
		assert.Contains(t, got.Prompt, "Critical")
	})

	t.Run("legacy task parent with empty job_type skips severity", func(t *testing.T) {
		s, db, tmpDir := newTestServer(t)
		testutil.InitTestGitRepo(t, tmpDir)
		os.WriteFile(filepath.Join(tmpDir, ".roborev.toml"), []byte(`fix_min_severity = "high"`), 0644)

		// Create a legacy-style task parent: empty JobType, has Prompt, no CommitID
		repo, err := db.GetOrCreateRepo(tmpDir)
		require.NoError(t, err)
		parentJob, err := db.EnqueueJob(storage.EnqueueOpts{
			RepoID: repo.ID,
			Agent:  "test",
			Prompt: "analyze the code",
			// JobType intentionally left empty — legacy shape
		})
		require.NoError(t, err)
		setJobStatus(t, db, parentJob.ID, storage.JobStatusDone)
		_, err = db.CreateReview(parentJob.ID, "test", "prompt", "Found issues")
		require.NoError(t, err)

		body := fmt.Sprintf(`{"parent_job_id":%d}`, parentJob.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/job/fix", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var fixJob storage.ReviewJob
		json.NewDecoder(w.Body).Decode(&fixJob)
		got, err := db.GetJobByID(fixJob.ID)
		require.NoError(t, err)
		// IsTaskJob() should return true for legacy shape — no severity injection
		assert.NotContains(t, got.Prompt, "Severity filter:")
	})
}
```

(Adjust helper names to match actual test patterns. `createTestJob`, `setJobStatus`, `db.CreateReview` — verify these exist or use their actual names.)

- [ ] **Step 5: Run tests**

Run: `nix develop -c go test ./internal/daemon/ -run TestHandleFixJobMinSeverity -v -timeout=120s`
Expected: PASS.

Run: `nix develop -c go test ./internal/daemon/ -v -count=1 -timeout=120s`
Expected: PASS.

---

### Task 12: CLI — review --min-severity flag

**Files:**
- Modify: `cmd/roborev/review.go:28-44` — add flag variable
- Modify: `cmd/roborev/review.go:277-292` — set field on EnqueueRequest
- Modify: `cmd/roborev/review.go:368` — thread into runLocalReview
- Test: `cmd/roborev/review_test.go` — flag tests

- [ ] **Step 1: Add the flag variable**

In `cmd/roborev/review.go`, add to the `var` block (line 28-44):

```go
	minSeverity string
```

Register the flag on the cobra command (find the flag registration section):

```go
cmd.Flags().StringVar(&minSeverity, "min-severity", "", "Minimum severity: critical, high, medium, low")
```

- [ ] **Step 2: Add CLI-side validation**

In the review command's `RunE` function, before building the request, validate:

```go
	// Validate and normalize min-severity flag
	var normalizedMinSev string
	if strings.TrimSpace(minSeverity) != "" {
		var err error
		normalizedMinSev, err = config.NormalizeMinSeverity(minSeverity)
		if err != nil {
			return err
		}
	}
```

- [ ] **Step 3: Set MinSeverity on EnqueueRequest**

In `cmd/roborev/review.go`, at the `EnqueueRequest` construction (line 277-292), add:

```go
	MinSeverity: normalizedMinSev,
```

- [ ] **Step 4: Thread into runLocalReview**

Update the `runLocalReview` function signature to accept `minSeverity string`:

```go
func runLocalReview(cmd *cobra.Command, repoPath, gitRef, diffContent, agentName, model, provider, reasoning, reviewType, minSeverity string, quiet bool) error {
```

Inside `runLocalReview`, resolve the severity cascade before building prompts:

```go
	// Resolve min-severity via cascade
	minSev, err := config.ResolveReviewMinSeverity(minSeverity, repoPath, cfg)
	if err != nil {
		return fmt.Errorf("resolve min-severity: %w", err)
	}
```

Update the `pb.Build` and `pb.BuildDirty` calls to pass `minSev` as the trailing argument:

```go
	if diffContent != "" {
		reviewPrompt, err = pb.BuildDirty(repoPath, diffContent, 0, cfg.ReviewContextCount, a.Name(), reviewType, minSev)
	} else {
		reviewPrompt, err = pb.Build(repoPath, gitRef, 0, cfg.ReviewContextCount, a.Name(), reviewType, minSev)
	}
```

Update the call site that invokes `runLocalReview` to pass `normalizedMinSev`:

```go
	return runLocalReview(cmd, repoPath, gitRef, diffContent, agent, model, provider, reasoning, reviewType, normalizedMinSev, quiet)
```

- [ ] **Step 5: Write tests**

First, add `MinSeverity string` to the `capturedEnqueue` struct in `cmd/roborev/review_test.go` (matching the JSON tag `min_severity`):

```go
type capturedEnqueue struct {
	// ... existing fields ...
	MinSeverity string `json:"min_severity"`
}
```

Then add the test cases:

```go
func TestReviewMinSeverityFlag(t *testing.T) {
	t.Run("valid flag stores canonical lowercase", func(t *testing.T) {
		assert := assert.New(t)
		repo, mux := setupTestEnvironment(t)
		ch := mockEnqueue(t, mux)

		go executeReviewCmd("--repo", repo.Dir, "--min-severity", "HIGH", repo.HeadSHA)
		captured := <-ch
		assert.Equal("high", captured.MinSeverity)
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		assert := assert.New(t)
		repo, mux := setupTestEnvironment(t)
		ch := mockEnqueue(t, mux)

		go executeReviewCmd("--repo", repo.Dir, "--min-severity", " high ", repo.HeadSHA)
		captured := <-ch
		assert.Equal("high", captured.MinSeverity)
	})

	t.Run("invalid value errors before enqueue", func(t *testing.T) {
		repo, _ := setupTestEnvironment(t)

		_, stderr, err := executeReviewCmd("--repo", repo.Dir, "--min-severity", "bogus", repo.HeadSHA)
		assert.Error(t, err)
		assert.Contains(t, stderr, "invalid min_severity")
	})
}
```

(Adjust helper names to match the actual test patterns in `review_test.go`.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `nix develop -c go test ./cmd/roborev/ -run TestReviewMinSeverity -v`
Expected: PASS

Run: `nix develop -c go build ./...`
Expected: Build succeeds.

---

### Task 12.5: CI batch review — thread min-severity through BatchConfig

**Files:**
- Modify: `internal/review/batch.go:14-31` — add MinSeverity to BatchConfig
- Modify: `internal/review/batch.go:152` — pass MinSeverity to builder.Build
- Modify: `cmd/roborev/ci.go:~210` — resolve and set BatchConfig.MinSeverity
- Test: `internal/review/batch_test.go` — verify injection

`roborev ci` runs reviews without the daemon/worker, calling `internal/review.RunBatch` → `runSingle` → `builder.Build` directly. Without this task, the `minSeverity` parameter added in Task 8 would get `""` here, silently disabling severity filtering for all CI reviews.

- [ ] **Step 1: Add MinSeverity to BatchConfig**

In `internal/review/batch.go`, add to `BatchConfig` (after `GlobalConfig`):

```go
	// MinSeverity is the effective review-level minimum severity filter.
	// Resolved by the caller via config.ResolveReviewMinSeverity.
	MinSeverity string
```

- [ ] **Step 2: Thread into runSingle's builder.Build call**

In `internal/review/batch.go` at line 152, update the `builder.Build` call:

```go
	reviewPrompt, err := builder.Build(
		cfg.RepoPath, cfg.GitRef, 0, cfg.ContextCount,
		resolvedAgent.Name(), promptReviewType, cfg.MinSeverity)
```

- [ ] **Step 3: Resolve in cmd/roborev/ci.go**

In `cmd/roborev/ci.go`, before building `BatchConfig` (~line 210), resolve:

```go
	reviewMinSev, err := config.ResolveReviewMinSeverity("", repoPath, cfg)
	if err != nil {
		return fmt.Errorf("resolve review min-severity: %w", err)
	}
```

Then set `MinSeverity: reviewMinSev` on the `BatchConfig` literal.

- [ ] **Step 4: Write test**

Add to `internal/review/batch_test.go`:

```go
func TestBatchMinSeverityInjection(t *testing.T) {
	assert := assert.New(t)
	repoPath := testutil.NewTestRepoWithCommit(t)
	sha := testutil.GetHeadSHA(t, repoPath)

	var capturedPrompt string
	mockAgent := &agent.FakeAgent{
		NameStr: "batch-sev-test",
		ReviewFn: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
			capturedPrompt = prompt
			return "No issues found.", nil
		},
	}

	results := RunBatch(context.Background(), BatchConfig{
		RepoPath:      repoPath,
		GitRef:        sha,
		Agents:        []string{"batch-sev-test"},
		ReviewTypes:   []string{"review"},
		MinSeverity:   "high",
		AgentRegistry: map[string]agent.Agent{"batch-sev-test": mockAgent},
	})
	require.Len(t, results, 1)
	assert.Equal(ResultDone, results[0].Status)
	assert.Contains(capturedPrompt, "Severity filter:")
	assert.Contains(capturedPrompt, "SEVERITY_THRESHOLD_MET")

	// Empty severity: no injection
	capturedPrompt = ""
	results2 := RunBatch(context.Background(), BatchConfig{
		RepoPath:      repoPath,
		GitRef:        sha,
		Agents:        []string{"batch-sev-test"},
		ReviewTypes:   []string{"review"},
		AgentRegistry: map[string]agent.Agent{"batch-sev-test": mockAgent},
	})
	require.Len(t, results2, 1)
	assert.NotContains(capturedPrompt, "Severity filter:")
}
```

- [ ] **Step 5: Run tests**

Run: `nix develop -c go test ./internal/review/ -run TestBatchMinSeverity -v`
Expected: PASS.

Run: `nix develop -c go build ./...`
Expected: Build succeeds.

---

### Task 13: Full build and test sweep

**Files:** None (verification only)

- [ ] **Step 1: Format and vet**

Run: `nix develop -c go fmt ./... && nix develop -c go vet ./...`
Expected: No errors, no formatting changes (or fix any that appear).

- [ ] **Step 2: Full test suite**

Run: `nix develop -c go test ./... -count=1 -timeout=300s`
Expected: PASS — all tests pass.

- [ ] **Step 3: Lint**

Run: `nix develop -c golangci-lint run`
Expected: No new warnings.

- [ ] **Step 4: Fix any issues**

If any tests or lint issues arise, fix them.
