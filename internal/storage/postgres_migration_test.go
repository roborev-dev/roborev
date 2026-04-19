//go:build postgres

package storage

import (
	"context"
	_ "embed"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

//go:embed schemas/postgres_v11.sql
var postgresV11Schema string

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
	prevVersion := pgSchemaVersion - 1

	oldPool := openTestPgPoolRawAtVersion(t, prevVersion)
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
