//go:build postgres

package storage

import (
	"context"
	_ "embed"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

//go:embed schemas/postgres_v11.sql
var postgresV11Schema string

//go:embed schemas/postgres_v12.sql
var postgresV12Schema string

// openTestPgPoolRawAtVersion bootstraps a fresh Postgres test pool at the
// given older schema version by running only the corresponding embedded
// schema file and seeding schema_version. It deliberately does NOT call
// EnsureSchema, so a subsequent openTestPgPool(t) call triggers the upgrade
// path under test.
func openTestPgPoolRawAtVersion(t *testing.T, version int) *PgPool {
	t.Helper()
	connString := getTestPostgresURL(t)
	ctx := t.Context()

	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	require.NoError(t, err, "Failed to connect: %v")
	t.Cleanup(func() { pool.Close() })

	// Wipe any pre-existing roborev schema so we start clean.
	_, err = pool.pool.Exec(ctx, `DROP SCHEMA IF EXISTS roborev CASCADE`)
	require.NoError(t, err, "drop existing roborev schema")

	var schemaSQL string
	switch version {
	case 11:
		schemaSQL = postgresV11Schema
	case 12:
		schemaSQL = postgresV12Schema
	default:
		t.Fatalf("openTestPgPoolRawAtVersion: no embedded schema for version %d", version)
	}

	for _, stmt := range strings.Split(schemaSQL, ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		// Skip pure comment-only statements
		isComment := true
		for _, line := range strings.Split(s, "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "--") {
				isComment = false
				break
			}
		}
		if isComment {
			continue
		}
		_, err = pool.pool.Exec(ctx, s)
		require.NoError(t, err, "execute statement: %s", s)
	}

	_, err = pool.pool.Exec(ctx,
		`INSERT INTO roborev.schema_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, version)
	require.NoError(t, err, "seed schema_version")

	return pool
}

// pgxPool returns the underlying pgxpool.Pool for low-level access in tests.
func pgxPool(p *PgPool) *pgxpool.Pool { return p.pool }

func TestPostgresMigration_SkipReasonAndClassify(t *testing.T) {
	// Pinned to v11: validates the v11 → v12 migration step (skip_reason
	// column, classify support, auto_design dedup indexes). Cannot use
	// pgSchemaVersion - 1, because once newer migrations exist the helper
	// would start at the wrong version.
	oldPool := openTestPgPoolRawAtVersion(t, 11)
	ctx := context.Background()

	var repoID int
	require.NoError(t, pgxPool(oldPool).QueryRow(ctx,
		`INSERT INTO roborev.repos (identity) VALUES ($1) RETURNING id`,
		"git@example.com:owner/test-repo.git").Scan(&repoID))
	_, err := pgxPool(oldPool).Exec(ctx, `
		INSERT INTO roborev.review_jobs
		  (uuid, repo_id, git_ref, agent, status, enqueued_at, source_machine_id)
		VALUES ($1, $2, 'abc', 'test', 'done', NOW(), $3)
	`, uuid.New().String(), repoID, uuid.New().String())
	require.NoError(t, err)

	pg := openTestPgPool(t)
	defer pg.Close()

	var n int
	require.NoError(t, pgxPool(pg).QueryRow(ctx,
		`SELECT COUNT(*) FROM roborev.review_jobs WHERE git_ref = 'abc'`).Scan(&n))
	require.Equal(t, 1, n)

	machineID := uuid.New().String()
	_, err = pgxPool(pg).Exec(ctx, `
		INSERT INTO roborev.review_jobs
		  (uuid, repo_id, git_ref, agent, job_type, review_type, status, enqueued_at,
		   source_machine_id, skip_reason, source)
		VALUES ($1, $2, 'after', 'test', 'review', 'design', 'skipped', NOW(), $3, 'trivial', 'auto_design')
	`, uuid.New().String(), repoID, machineID)
	require.NoError(t, err)
}

// TestPostgresMigration_FindingCounts validates the v12 → v13 migration:
// pre-existing reviews get the three nullable count columns added with NULL
// values, new UpsertReview calls persist explicit ints, and PullReviews
// returns 0 (via COALESCE) for the still-NULL legacy rows.
func TestPostgresMigration_FindingCounts(t *testing.T) {
	oldPool := openTestPgPoolRawAtVersion(t, 12)
	ctx := context.Background()

	var repoID int
	require.NoError(t, pgxPool(oldPool).QueryRow(ctx,
		`INSERT INTO roborev.repos (identity) VALUES ($1) RETURNING id`,
		"git@example.com:owner/findings-repo.git").Scan(&repoID))
	machineID := uuid.New().String()
	jobUUID := uuid.New().String()
	legacyReviewUUID := uuid.New().String()

	_, err := pgxPool(oldPool).Exec(ctx, `
		INSERT INTO roborev.review_jobs
		  (uuid, repo_id, git_ref, agent, status, enqueued_at, source_machine_id)
		VALUES ($1, $2, 'legacy-sha', 'test', 'done', NOW(), $3)
	`, jobUUID, repoID, machineID)
	require.NoError(t, err)
	_, err = pgxPool(oldPool).Exec(ctx, `
		INSERT INTO roborev.reviews
		  (uuid, job_uuid, agent, prompt, output, updated_by_machine_id)
		VALUES ($1, $2, 'test', 'p', 'legacy output (no count cols)', $3)
	`, legacyReviewUUID, jobUUID, machineID)
	require.NoError(t, err)

	// Run EnsureSchema by opening a fresh pool, which advances v12 → v13.
	pg := openTestPgPool(t)
	defer pg.Close()

	// Legacy row: columns exist now, but they're NULL because the v13
	// migration adds them as nullable (NULL == "not yet parsed").
	var hi, me, lo *int
	require.NoError(t, pgxPool(pg).QueryRow(ctx,
		`SELECT high_count, medium_count, low_count
		 FROM roborev.reviews WHERE uuid = $1`, legacyReviewUUID,
	).Scan(&hi, &me, &lo))
	require.Nil(t, hi, "legacy review should have NULL high_count after migration")
	require.Nil(t, me, "legacy review should have NULL medium_count")
	require.Nil(t, lo, "legacy review should have NULL low_count")

	// New write goes in with explicit non-NULL counts.
	freshUUID := uuid.New().String()
	require.NoError(t, pg.UpsertReview(ctx, SyncableReview{
		UUID:               freshUUID,
		JobUUID:            jobUUID,
		Agent:              "test",
		Prompt:             "p",
		Output:             "fresh review",
		Closed:             false,
		UpdatedByMachineID: machineID,
		CreatedAt:          mustNow(),
		HighCount:          3,
		MediumCount:        2,
		LowCount:           5,
	}))
	var hh, mm, ll int
	require.NoError(t, pgxPool(pg).QueryRow(ctx,
		`SELECT high_count, medium_count, low_count
		 FROM roborev.reviews WHERE uuid = $1`, freshUUID,
	).Scan(&hh, &mm, &ll))
	require.Equal(t, 3, hh)
	require.Equal(t, 2, mm)
	require.Equal(t, 5, ll)

	// PullReviews should COALESCE the legacy NULLs to 0 and return the
	// fresh row's explicit values. excludeMachineID must be a valid UUID
	// (the type comparison happens server-side); use a fresh one not
	// equal to machineID so both rows are returned.
	pulled, _, err := pg.PullReviews(ctx, uuid.New().String(),
		[]string{jobUUID}, "", 100)
	require.NoError(t, err)
	byUUID := map[string]PulledReview{}
	for _, r := range pulled {
		byUUID[r.UUID] = r
	}
	legacy, ok := byUUID[legacyReviewUUID]
	require.True(t, ok, "legacy review missing from PullReviews")
	require.Equal(t, 0, legacy.HighCount)
	require.Equal(t, 0, legacy.MediumCount)
	require.Equal(t, 0, legacy.LowCount)
	fresh, ok := byUUID[freshUUID]
	require.True(t, ok, "fresh review missing from PullReviews")
	require.Equal(t, 3, fresh.HighCount)
	require.Equal(t, 2, fresh.MediumCount)
	require.Equal(t, 5, fresh.LowCount)
}

func mustNow() time.Time { return time.Now().UTC() }
