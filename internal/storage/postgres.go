package storage

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQL schema version - increment when schema changes
const pgSchemaVersion = 3

// pgSchemaName is the PostgreSQL schema used to isolate roborev tables
const pgSchemaName = "roborev"

//go:embed schemas/postgres_v3.sql
var pgSchemaSQL string

// pgSchemaStatements returns the individual DDL statements for schema creation.
// Parsed from the embedded SQL file.
func pgSchemaStatements() []string {
	var stmts []string
	for _, stmt := range strings.Split(pgSchemaSQL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		// Skip pure comment lines
		lines := strings.Split(stmt, "\n")
		hasCode := false
		for _, line := range lines {
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

// PgPool wraps a pgx connection pool with reconnection logic
type PgPool struct {
	pool       *pgxpool.Pool
	connString string
	config     PgPoolConfig
}

// PgPoolConfig configures the PostgreSQL connection pool
type PgPoolConfig struct {
	// ConnectTimeout is the timeout for initial connection (default: 5s)
	ConnectTimeout time.Duration
	// MaxConns is the maximum number of connections (default: 4)
	MaxConns int32
	// MinConns is the minimum number of connections (default: 0)
	MinConns int32
	// MaxConnLifetime is the maximum lifetime of a connection (default: 1h)
	MaxConnLifetime time.Duration
	// MaxConnIdleTime is the maximum idle time before closing (default: 30m)
	MaxConnIdleTime time.Duration
}

// DefaultPgPoolConfig returns sensible defaults for the connection pool
func DefaultPgPoolConfig() PgPoolConfig {
	return PgPoolConfig{
		ConnectTimeout:  5 * time.Second,
		MaxConns:        4,
		MinConns:        0,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	}
}

// NewPgPool creates a new PostgreSQL connection pool.
// The connection string should be a PostgreSQL URL like:
// postgres://user:pass@host:port/dbname?sslmode=disable
func NewPgPool(ctx context.Context, connString string, cfg PgPoolConfig) (*PgPool, error) {
	poolCfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse connection string: %w", err)
	}

	// Apply configuration
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	// Set search_path to roborev schema on each connection.
	// Try setting search_path first; if schema doesn't exist, create it.
	// This avoids requiring CREATE privilege when schema already exists.
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+pgSchemaName)
		if err != nil {
			// Schema doesn't exist - create it and retry
			if _, createErr := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgSchemaName); createErr != nil {
				return createErr
			}
			_, err = conn.Exec(ctx, "SET search_path TO "+pgSchemaName)
		}
		return err
	}

	// Create context with timeout for initial connection
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(connectCtx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	// Verify connection
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &PgPool{
		pool:       pool,
		connString: connString,
		config:     cfg,
	}, nil
}

// Close closes the connection pool
func (p *PgPool) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}

// Pool returns the underlying pgxpool.Pool for direct access
func (p *PgPool) Pool() *pgxpool.Pool {
	return p.pool
}

// EnsureSchema creates the schema if it doesn't exist and checks version.
// If legacy tables exist in the public schema, they are migrated to roborev.
func (p *PgPool) EnsureSchema(ctx context.Context) error {
	// Migrate legacy tables from public schema if they exist
	if err := p.migrateLegacyTables(ctx); err != nil {
		return fmt.Errorf("migrate legacy tables: %w", err)
	}

	// Execute each schema statement individually since pgx prepared
	// statement mode doesn't support multi-statement execution
	for _, stmt := range pgSchemaStatements() {
		if _, err := p.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}

	// Check/insert schema version using ON CONFLICT to handle concurrent initializers
	var currentVersion int
	err := p.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("check schema version: %w", err)
	}

	if currentVersion == 0 {
		// First time - insert version with ON CONFLICT to handle races
		_, err = p.pool.Exec(ctx, `INSERT INTO schema_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, pgSchemaVersion)
		if err != nil {
			return fmt.Errorf("insert schema version: %w", err)
		}
		// Create indexes not in base schema (to support upgrades from older versions)
		_, err = p.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_review_jobs_branch ON review_jobs(branch)`)
		if err != nil {
			return fmt.Errorf("create branch index: %w", err)
		}
	} else if currentVersion > pgSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", currentVersion, pgSchemaVersion)
	} else if currentVersion < pgSchemaVersion {
		// Run migrations
		if currentVersion < 2 {
			// Migration 1->2: Add model column to review_jobs
			_, err = p.pool.Exec(ctx, `ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS model TEXT`)
			if err != nil {
				return fmt.Errorf("migrate to v2 (add model column): %w", err)
			}
		}
		if currentVersion < 3 {
			// Migration 2->3: Add branch column to review_jobs
			_, err = p.pool.Exec(ctx, `ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS branch TEXT`)
			if err != nil {
				return fmt.Errorf("migrate to v3 (add branch column): %w", err)
			}
			// Add index for branch filtering
			_, err = p.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_review_jobs_branch ON review_jobs(branch)`)
			if err != nil {
				return fmt.Errorf("migrate to v3 (add branch index): %w", err)
			}
		}
		// Update version
		_, err = p.pool.Exec(ctx, `INSERT INTO schema_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, pgSchemaVersion)
		if err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
	}

	return nil
}

// GetDatabaseID returns the unique ID for this Postgres database.
// Creates one if it doesn't exist. This ID is used to detect when
// a client is syncing to a different database than before.
func (p *PgPool) GetDatabaseID(ctx context.Context) (string, error) {
	var id string
	err := p.pool.QueryRow(ctx, `SELECT value FROM sync_metadata WHERE key = 'database_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("query database_id: %w", err)
	}

	// Generate new ID - use ON CONFLICT to handle concurrent creation
	newID := GenerateUUID()
	_, err = p.pool.Exec(ctx, `
		INSERT INTO sync_metadata (key, value) VALUES ('database_id', $1)
		ON CONFLICT (key) DO NOTHING
	`, newID)
	if err != nil {
		return "", fmt.Errorf("insert database_id: %w", err)
	}

	// Re-read in case another process inserted first
	err = p.pool.QueryRow(ctx, `SELECT value FROM sync_metadata WHERE key = 'database_id'`).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("re-read database_id: %w", err)
	}
	return id, nil
}

// pgLegacyTables lists tables that may exist in public schema from older installations
var pgLegacyTables = []string{
	"responses",
	"reviews",
	"review_jobs",
	"commits",
	"repos",
	"machines",
	"schema_version",
}

// migrateLegacyTables moves roborev tables from public schema to roborev schema.
// Handles concurrent execution and partial migration states gracefully.
func (p *PgPool) migrateLegacyTables(ctx context.Context) error {
	// Check if any legacy tables exist in public schema
	var hasLegacy bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_version'
		)
	`).Scan(&hasLegacy)
	if err != nil {
		return fmt.Errorf("check legacy tables: %w", err)
	}

	if !hasLegacy {
		return nil
	}

	// Migrate tables in dependency order (reverse of pgLegacyTables)
	for _, table := range pgLegacyTables {
		// Check if table exists in public and not in roborev
		var existsInPublic, existsInRoborev bool
		err := p.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)
		`, table).Scan(&existsInPublic)
		if err != nil {
			return fmt.Errorf("check table %s in public: %w", table, err)
		}
		if !existsInPublic {
			continue
		}

		err = p.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = $1 AND table_name = $2
			)
		`, pgSchemaName, table).Scan(&existsInRoborev)
		if err != nil {
			return fmt.Errorf("check table %s in roborev: %w", table, err)
		}

		if existsInRoborev {
			// Table exists in both schemas - this could mean data loss if rows remain in public
			var publicCount, roborevCount int64
			if err := p.pool.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM public.%s`, table)).Scan(&publicCount); err != nil {
				// Handle concurrent drop - treat as empty/gone
				if pgErr, ok := isPgError(err); ok && pgErr == "42P01" {
					continue
				}
				return fmt.Errorf("count rows in public.%s: %w", table, err)
			}
			if err := p.pool.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s.%s`, pgSchemaName, table)).Scan(&roborevCount); err != nil {
				// roborev table disappeared - if public still has data, try to move it
				if pgErr, ok := isPgError(err); ok && pgErr == "42P01" {
					if publicCount > 0 {
						// Fall through to move logic below by not continuing
						existsInRoborev = false
					} else {
						// public is empty, roborev gone - nothing to do
						continue
					}
				} else {
					return fmt.Errorf("count rows in %s.%s: %w", pgSchemaName, table, err)
				}
			}
			if existsInRoborev {
				if publicCount > 0 {
					return fmt.Errorf("table %s exists in both public (%d rows) and %s (%d rows) schemas; "+
						"manual reconciliation required - migrate data from public.%s to %s.%s then DROP TABLE public.%s",
						table, publicCount, pgSchemaName, roborevCount, table, pgSchemaName, table, table)
				}
				// public table is empty, safe to drop it
				if _, err := p.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE public.%s`, table)); err != nil {
					// Ignore if already dropped by concurrent process
					if pgErr, ok := isPgError(err); ok && pgErr == "42P01" {
						continue
					}
					return fmt.Errorf("drop empty public.%s: %w", table, err)
				}
				continue
			}
		}

		// Move table to roborev schema
		_, err = p.pool.Exec(ctx, fmt.Sprintf(
			`ALTER TABLE public.%s SET SCHEMA %s`,
			table, pgSchemaName,
		))
		if err != nil {
			// Ignore "relation does not exist" (42P01) - table was moved by concurrent process
			// Ignore "relation already exists" (42P07) - table appeared in roborev concurrently
			if pgErr, ok := isPgError(err); ok && (pgErr == "42P01" || pgErr == "42P07") {
				continue
			}
			return fmt.Errorf("migrate table %s: %w", table, err)
		}
	}

	return nil
}

// isPgError checks if err is a PostgreSQL error and returns its SQLSTATE code.
// Uses errors.As to unwrap wrapped errors.
func isPgError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, true
	}
	return "", false
}

// Ping checks if the connection is alive
func (p *PgPool) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

// RegisterMachine registers or updates this machine in the machines table
func (p *PgPool) RegisterMachine(ctx context.Context, machineID, name string) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO machines (machine_id, name, last_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (machine_id) DO UPDATE SET
			name = COALESCE(EXCLUDED.name, machines.name),
			last_seen_at = NOW()
	`, machineID, name)
	if err != nil {
		return fmt.Errorf("register machine: %w", err)
	}
	return nil
}

// GetOrCreateRepo finds or creates a repo by identity, returns the PostgreSQL ID
func (p *PgPool) GetOrCreateRepo(ctx context.Context, identity string) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO repos (identity)
		VALUES ($1)
		ON CONFLICT (identity) DO UPDATE SET identity = EXCLUDED.identity
		RETURNING id
	`, identity).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get or create repo: %w", err)
	}
	return id, nil
}

// GetOrCreateCommit finds or creates a commit, returns the PostgreSQL ID
func (p *PgPool) GetOrCreateCommit(ctx context.Context, repoID int64, sha, author, subject string, timestamp time.Time) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO commits (repo_id, sha, author, subject, timestamp)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (repo_id, sha) DO UPDATE SET sha = EXCLUDED.sha
		RETURNING id
	`, repoID, sha, author, subject, timestamp).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get or create commit: %w", err)
	}
	return id, nil
}

// Tx runs a function within a transaction
func (p *PgPool) Tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// UpsertJob inserts or updates a job in PostgreSQL
func (p *PgPool) UpsertJob(ctx context.Context, j SyncableJob, pgRepoID int64, pgCommitID *int64) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO review_jobs (
			uuid, repo_id, commit_id, git_ref, agent, model, reasoning, status, agentic,
			enqueued_at, started_at, finished_at, prompt, diff_content, error,
			source_machine_id, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, NOW())
		ON CONFLICT (uuid) DO UPDATE SET
			status = EXCLUDED.status,
			finished_at = EXCLUDED.finished_at,
			error = EXCLUDED.error,
			model = COALESCE(EXCLUDED.model, review_jobs.model),
			updated_at = NOW()
	`, j.UUID, pgRepoID, pgCommitID, j.GitRef, j.Agent, nullString(j.Model), nullString(j.Reasoning),
		j.Status, j.Agentic, j.EnqueuedAt, j.StartedAt, j.FinishedAt,
		nullString(j.Prompt), j.DiffContent, nullString(j.Error), j.SourceMachineID)
	return err
}

// UpsertReview inserts or updates a review in PostgreSQL
func (p *PgPool) UpsertReview(ctx context.Context, r SyncableReview) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO reviews (
			uuid, job_uuid, agent, prompt, output, addressed,
			updated_by_machine_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (uuid) DO UPDATE SET
			addressed = EXCLUDED.addressed,
			updated_by_machine_id = EXCLUDED.updated_by_machine_id,
			updated_at = NOW()
	`, r.UUID, r.JobUUID, r.Agent, r.Prompt, r.Output, r.Addressed,
		r.UpdatedByMachineID, r.CreatedAt)
	return err
}

// InsertResponse inserts a response in PostgreSQL (append-only, no updates)
func (p *PgPool) InsertResponse(ctx context.Context, r SyncableResponse) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO responses (
			uuid, job_uuid, responder, response, source_machine_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (uuid) DO NOTHING
	`, r.UUID, r.JobUUID, r.Responder, r.Response, r.SourceMachineID, r.CreatedAt)
	return err
}

// PulledJob represents a job pulled from PostgreSQL
type PulledJob struct {
	UUID            string
	RepoIdentity    string
	CommitSHA       string
	CommitAuthor    string
	CommitSubject   string
	CommitTimestamp time.Time
	GitRef          string
	Agent           string
	Model           string
	Reasoning       string
	Status          string
	Agentic         bool
	EnqueuedAt      time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	Prompt          string
	DiffContent     *string
	Error           string
	SourceMachineID string
	UpdatedAt       time.Time
}

// PullJobs fetches jobs from PostgreSQL updated after the given cursor.
// Cursor format: "updated_at id" (space-separated) or empty for first pull.
// Returns jobs not from the given machineID (to avoid echo).
func (p *PgPool) PullJobs(ctx context.Context, excludeMachineID string, cursor string, limit int) ([]PulledJob, string, error) {
	var cursorTime time.Time
	var cursorID int64

	if cursor != "" {
		var ts string
		_, err := fmt.Sscanf(cursor, "%s %d", &ts, &cursorID)
		if err == nil {
			cursorTime, _ = time.Parse(time.RFC3339Nano, ts)
		}
	}

	rows, err := p.pool.Query(ctx, `
		SELECT
			j.uuid, r.identity, COALESCE(c.sha, ''), COALESCE(c.author, ''), COALESCE(c.subject, ''), COALESCE(c.timestamp, '1970-01-01'::timestamptz),
			j.git_ref, j.agent, COALESCE(j.model, ''), COALESCE(j.reasoning, ''), j.status, j.agentic,
			j.enqueued_at, j.started_at, j.finished_at,
			COALESCE(j.prompt, ''), j.diff_content, COALESCE(j.error, ''),
			j.source_machine_id, j.updated_at, j.id
		FROM review_jobs j
		JOIN repos r ON j.repo_id = r.id
		LEFT JOIN commits c ON j.commit_id = c.id
		WHERE (j.source_machine_id IS NULL OR j.source_machine_id != $1)
		AND (j.updated_at > $2 OR (j.updated_at = $2 AND j.id > $3))
		ORDER BY j.updated_at, j.id
		LIMIT $4
	`, excludeMachineID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, cursor, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []PulledJob
	var lastUpdatedAt time.Time
	var lastID int64

	for rows.Next() {
		var j PulledJob
		var diffContent *string

		err := rows.Scan(
			&j.UUID, &j.RepoIdentity, &j.CommitSHA, &j.CommitAuthor, &j.CommitSubject, &j.CommitTimestamp,
			&j.GitRef, &j.Agent, &j.Model, &j.Reasoning, &j.Status, &j.Agentic,
			&j.EnqueuedAt, &j.StartedAt, &j.FinishedAt,
			&j.Prompt, &diffContent, &j.Error,
			&j.SourceMachineID, &j.UpdatedAt, &lastID,
		)
		if err != nil {
			return nil, cursor, fmt.Errorf("scan job: %w", err)
		}

		j.DiffContent = diffContent
		lastUpdatedAt = j.UpdatedAt
		jobs = append(jobs, j)
	}

	if err := rows.Err(); err != nil {
		return nil, cursor, fmt.Errorf("rows error: %w", err)
	}

	// Update cursor if we got results
	newCursor := cursor
	if len(jobs) > 0 {
		newCursor = fmt.Sprintf("%s %d", lastUpdatedAt.Format(time.RFC3339Nano), lastID)
	}

	return jobs, newCursor, nil
}

// PulledReview represents a review pulled from PostgreSQL
type PulledReview struct {
	UUID               string
	JobUUID            string
	Agent              string
	Prompt             string
	Output             string
	Addressed          bool
	UpdatedByMachineID string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// PullReviews fetches reviews from PostgreSQL updated after the given cursor.
// Only fetches reviews for jobs in knownJobUUIDs to avoid cursor advancement past unknown jobs.
func (p *PgPool) PullReviews(ctx context.Context, excludeMachineID string, knownJobUUIDs []string, cursor string, limit int) ([]PulledReview, string, error) {
	var cursorTime time.Time
	var cursorID int64

	if cursor != "" {
		var ts string
		_, err := fmt.Sscanf(cursor, "%s %d", &ts, &cursorID)
		if err == nil {
			cursorTime, _ = time.Parse(time.RFC3339Nano, ts)
		}
	}

	// If no known jobs, return empty (no reviews can match)
	if len(knownJobUUIDs) == 0 {
		return nil, cursor, nil
	}

	rows, err := p.pool.Query(ctx, `
		SELECT
			r.uuid, r.job_uuid, r.agent, r.prompt, r.output, r.addressed,
			r.updated_by_machine_id, r.created_at, r.updated_at, r.id
		FROM reviews r
		WHERE (r.updated_by_machine_id IS NULL OR r.updated_by_machine_id != $1)
		AND r.job_uuid = ANY($2)
		AND (r.updated_at > $3 OR (r.updated_at = $3 AND r.id > $4))
		ORDER BY r.updated_at, r.id
		LIMIT $5
	`, excludeMachineID, knownJobUUIDs, cursorTime, cursorID, limit)
	if err != nil {
		return nil, cursor, fmt.Errorf("query reviews: %w", err)
	}
	defer rows.Close()

	var reviews []PulledReview
	var lastUpdatedAt time.Time
	var lastID int64

	for rows.Next() {
		var r PulledReview

		err := rows.Scan(
			&r.UUID, &r.JobUUID, &r.Agent, &r.Prompt, &r.Output, &r.Addressed,
			&r.UpdatedByMachineID, &r.CreatedAt, &r.UpdatedAt, &lastID,
		)
		if err != nil {
			return nil, cursor, fmt.Errorf("scan review: %w", err)
		}

		lastUpdatedAt = r.UpdatedAt
		reviews = append(reviews, r)
	}

	if err := rows.Err(); err != nil {
		return nil, cursor, fmt.Errorf("rows error: %w", err)
	}

	newCursor := cursor
	if len(reviews) > 0 {
		newCursor = fmt.Sprintf("%s %d", lastUpdatedAt.Format(time.RFC3339Nano), lastID)
	}

	return reviews, newCursor, nil
}

// PulledResponse represents a response pulled from PostgreSQL
type PulledResponse struct {
	UUID            string
	JobUUID         string
	Responder       string
	Response        string
	SourceMachineID string
	CreatedAt       time.Time
}

// PullResponses fetches responses from PostgreSQL created after the given ID cursor.
func (p *PgPool) PullResponses(ctx context.Context, excludeMachineID string, afterID int64, limit int) ([]PulledResponse, int64, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT
			r.uuid, r.job_uuid, r.responder, r.response, r.source_machine_id, r.created_at, r.id
		FROM responses r
		WHERE (r.source_machine_id IS NULL OR r.source_machine_id != $1)
		AND r.id > $2
		ORDER BY r.id
		LIMIT $3
	`, excludeMachineID, afterID, limit)
	if err != nil {
		return nil, afterID, fmt.Errorf("query responses: %w", err)
	}
	defer rows.Close()

	var responses []PulledResponse
	var lastID int64 = afterID

	for rows.Next() {
		var r PulledResponse

		err := rows.Scan(
			&r.UUID, &r.JobUUID, &r.Responder, &r.Response, &r.SourceMachineID, &r.CreatedAt, &lastID,
		)
		if err != nil {
			return nil, afterID, fmt.Errorf("scan response: %w", err)
		}

		responses = append(responses, r)
	}

	if err := rows.Err(); err != nil {
		return nil, afterID, fmt.Errorf("rows error: %w", err)
	}

	return responses, lastID, nil
}

// nullString returns nil if s is empty, otherwise returns s
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// BatchUpsertReviews inserts or updates multiple reviews in a single batch operation.
// Returns a boolean slice indicating success/failure for each item at the corresponding index.
func (p *PgPool) BatchUpsertReviews(ctx context.Context, reviews []SyncableReview) ([]bool, error) {
	if len(reviews) == 0 {
		return nil, nil
	}

	batch := &pgx.Batch{}
	for _, r := range reviews {
		batch.Queue(`
			INSERT INTO reviews (
				uuid, job_uuid, agent, prompt, output, addressed,
				updated_by_machine_id, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
			ON CONFLICT (uuid) DO UPDATE SET
				addressed = EXCLUDED.addressed,
				updated_by_machine_id = EXCLUDED.updated_by_machine_id,
				updated_at = NOW()
		`, r.UUID, r.JobUUID, r.Agent, r.Prompt, r.Output, r.Addressed,
			r.UpdatedByMachineID, r.CreatedAt)
	}

	br := p.pool.SendBatch(ctx, batch)
	defer br.Close()

	success := make([]bool, len(reviews))
	var firstErr error
	for i := range reviews {
		_, err := br.Exec()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success[i] = true
	}

	return success, firstErr
}

// BatchInsertResponses inserts multiple responses in a single batch operation.
// Returns a boolean slice indicating success/failure for each item at the corresponding index.
func (p *PgPool) BatchInsertResponses(ctx context.Context, responses []SyncableResponse) ([]bool, error) {
	if len(responses) == 0 {
		return nil, nil
	}

	batch := &pgx.Batch{}
	for _, r := range responses {
		batch.Queue(`
			INSERT INTO responses (
				uuid, job_uuid, responder, response, source_machine_id, created_at
			) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (uuid) DO NOTHING
		`, r.UUID, r.JobUUID, r.Responder, r.Response, r.SourceMachineID, r.CreatedAt)
	}

	br := p.pool.SendBatch(ctx, batch)
	defer br.Close()

	success := make([]bool, len(responses))
	var firstErr error
	for i := range responses {
		_, err := br.Exec()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success[i] = true
	}

	return success, firstErr
}

// JobWithPgIDs represents a job with its resolved PostgreSQL repo and commit IDs
type JobWithPgIDs struct {
	Job        SyncableJob
	PgRepoID   int64
	PgCommitID *int64
}

// BatchUpsertJobs inserts or updates multiple jobs in a single batch operation.
// The jobs must have their PgRepoID and PgCommitID already resolved.
// Returns a boolean slice indicating success/failure for each item at the corresponding index.
func (p *PgPool) BatchUpsertJobs(ctx context.Context, jobs []JobWithPgIDs) ([]bool, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	batch := &pgx.Batch{}
	for _, jw := range jobs {
		j := jw.Job
		batch.Queue(`
			INSERT INTO review_jobs (
				uuid, repo_id, commit_id, git_ref, agent, reasoning, status, agentic,
				enqueued_at, started_at, finished_at, prompt, diff_content, error,
				source_machine_id, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW())
			ON CONFLICT (uuid) DO UPDATE SET
				status = EXCLUDED.status,
				finished_at = EXCLUDED.finished_at,
				error = EXCLUDED.error,
				updated_at = NOW()
		`, j.UUID, jw.PgRepoID, jw.PgCommitID, j.GitRef, j.Agent, nullString(j.Reasoning),
			j.Status, j.Agentic, j.EnqueuedAt, j.StartedAt, j.FinishedAt,
			nullString(j.Prompt), j.DiffContent, nullString(j.Error), j.SourceMachineID)
	}

	br := p.pool.SendBatch(ctx, batch)
	defer br.Close()

	success := make([]bool, len(jobs))
	var firstErr error
	for i := range jobs {
		_, err := br.Exec()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success[i] = true
	}

	return success, firstErr
}
