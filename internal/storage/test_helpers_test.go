package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrationTestEnv manages the environment for schema migration tests.
type MigrationTestEnv struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
}

func NewMigrationTestEnv(t *testing.T) *MigrationTestEnv {
	t.Helper()
	connString := getTestPostgresURL(t)
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	return &MigrationTestEnv{
		t:    t,
		ctx:  t.Context(),
		pool: p,
	}
}

func (e *MigrationTestEnv) DropSchema(schema string) {
	e.t.Helper()
	_, err := e.pool.Exec(e.ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	if err != nil {
		e.t.Fatalf("Failed to drop schema %s: %v", schema, err)
	}
}

func (e *MigrationTestEnv) DropTable(schema, table string) {
	e.t.Helper()
	_, err := e.pool.Exec(e.ctx, "DROP TABLE IF EXISTS "+pgx.Identifier{schema, table}.Sanitize())
	if err != nil {
		e.t.Fatalf("Failed to drop table %s.%s: %v", schema, table, err)
	}
}

func (e *MigrationTestEnv) CleanupDropSchema(schema string) {
	e.t.Helper()
	e.t.Cleanup(func() {
		// Use background context: t.Context() is canceled by
		// the time Cleanup runs.
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_, err := e.pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		if err != nil {
			e.t.Errorf("Failed to cleanup schema %s: %v", schema, err)
		}
	})
}

func (e *MigrationTestEnv) CleanupDropTable(schema, table string) {
	e.t.Helper()
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_, err := e.pool.Exec(ctx, "DROP TABLE IF EXISTS "+pgx.Identifier{schema, table}.Sanitize())
		if err != nil {
			e.t.Errorf("Failed to cleanup table %s.%s: %v", schema, table, err)
		}
	})
}

func (e *MigrationTestEnv) SkipIfTableInSchema(schema, table string) {
	e.t.Helper()
	var exists bool
	err := e.pool.QueryRow(e.ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = $1 AND table_name = $2
		)
	`, schema, table).Scan(&exists)
	if err != nil {
		e.t.Fatalf("Failed to check %s.%s: %v", schema, table, err)
	}
	if exists {
		e.t.Skipf("Skipping: %s.%s already exists", schema, table)
	}
}

func (e *MigrationTestEnv) Exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(e.ctx, sql, args...)
	if err != nil {
		e.t.Fatalf("Failed to execute SQL: %v\nSQL: %s", err, sql)
	}
}

func (e *MigrationTestEnv) QueryRow(sql string, args ...any) pgx.Row {
	e.t.Helper()
	return e.pool.QueryRow(e.ctx, sql, args...)
}

// Data Factory Helpers

func createTestRepo(t *testing.T, pool *PgPool, identity string) int64 {
	t.Helper()
	id, err := pool.GetOrCreateRepo(t.Context(), identity)
	if err != nil {
		t.Fatalf("Failed to create repo %s: %v", identity, err)
	}
	return id
}

func createTestCommit(t *testing.T, pool *PgPool, repoID int64, sha string) int64 {
	t.Helper()
	id, err := pool.GetOrCreateCommit(t.Context(), repoID, sha, "Test Author", "Test Subject", time.Now())
	if err != nil {
		t.Fatalf("Failed to create commit %s: %v", sha, err)
	}
	return id
}

type TestJobOpts struct {
	UUID            string
	RepoID          int64
	CommitID        int64
	GitRef          string
	Agent           string
	Status          string
	SourceMachineID string
	EnqueuedAt      time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func createTestJob(t *testing.T, pool *PgPool, opts TestJobOpts) {
	t.Helper()
	if opts.UUID == "" {
		opts.UUID = uuid.NewString()
	}
	if opts.GitRef == "" {
		opts.GitRef = "HEAD"
	}
	if opts.Agent == "" {
		opts.Agent = "test"
	}
	if opts.Status == "" {
		opts.Status = "done"
	}
	if opts.SourceMachineID == "" {
		opts.SourceMachineID = uuid.NewString()
	}
	if opts.EnqueuedAt.IsZero() {
		opts.EnqueuedAt = time.Now()
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now()
	}
	if opts.UpdatedAt.IsZero() {
		opts.UpdatedAt = time.Now()
	}

	_, err := pool.pool.Exec(t.Context(), `
		INSERT INTO review_jobs (uuid, repo_id, commit_id, git_ref, agent, status, source_machine_id, enqueued_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, opts.UUID, opts.RepoID, opts.CommitID, opts.GitRef, opts.Agent, opts.Status, opts.SourceMachineID, opts.EnqueuedAt, opts.CreatedAt, opts.UpdatedAt)
	if err != nil {
		t.Fatalf("Failed to create job %s: %v", opts.UUID, err)
	}
}

type TestReviewOpts struct {
	UUID               string
	JobUUID            string
	Agent              string
	Prompt             string
	Output             string
	Addressed          bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	UpdatedByMachineID string
}

func createTestReview(t *testing.T, pool *PgPool, opts TestReviewOpts) {
	t.Helper()
	if opts.UUID == "" {
		opts.UUID = uuid.NewString()
	}
	if opts.Agent == "" {
		opts.Agent = "test"
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now()
	}
	if opts.UpdatedAt.IsZero() {
		opts.UpdatedAt = time.Now()
	}
	if opts.UpdatedByMachineID == "" {
		opts.UpdatedByMachineID = uuid.NewString()
	}

	_, err := pool.pool.Exec(t.Context(), `
		INSERT INTO reviews (uuid, job_uuid, agent, prompt, output, addressed, created_at, updated_at, updated_by_machine_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, opts.UUID, opts.JobUUID, opts.Agent, opts.Prompt, opts.Output, opts.Addressed, opts.CreatedAt, opts.UpdatedAt, opts.UpdatedByMachineID)
	if err != nil {
		t.Fatalf("Failed to create review %s: %v", opts.UUID, err)
	}
}

func parseSQLStatements(sql string) []string {
	var stmts []string
	for stmt := range strings.SplitSeq(sql, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		// Skip statements that are only comments
		hasCode := false
		for line := range strings.SplitSeq(stmt, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "--") {
				hasCode = true
				break
			}
		}
		if hasCode {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}
