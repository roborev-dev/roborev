package storage

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schemas/postgres_v1.sql
var postgresV1Schema string

func TestDefaultPgPoolConfig(t *testing.T) {
	cfg := DefaultPgPoolConfig()

	if cfg.ConnectTimeout != 5*time.Second {
		t.Errorf("Expected ConnectTimeout 5s, got %v", cfg.ConnectTimeout)
	}
	if cfg.MaxConns != 4 {
		t.Errorf("Expected MaxConns 4, got %d", cfg.MaxConns)
	}
	if cfg.MinConns != 0 {
		t.Errorf("Expected MinConns 0, got %d", cfg.MinConns)
	}
	if cfg.MaxConnLifetime != time.Hour {
		t.Errorf("Expected MaxConnLifetime 1h, got %v", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != 30*time.Minute {
		t.Errorf("Expected MaxConnIdleTime 30m, got %v", cfg.MaxConnIdleTime)
	}
}

func TestPgSchemaStatementsContainsRequiredTables(t *testing.T) {
	requiredStatements := []string{
		"CREATE SCHEMA IF NOT EXISTS roborev",
		"CREATE TABLE IF NOT EXISTS roborev.schema_version",
		"CREATE TABLE IF NOT EXISTS roborev.machines",
		"CREATE TABLE IF NOT EXISTS roborev.repos",
		"CREATE TABLE IF NOT EXISTS roborev.commits",
		"CREATE TABLE IF NOT EXISTS roborev.review_jobs",
		"CREATE TABLE IF NOT EXISTS roborev.reviews",
		"CREATE TABLE IF NOT EXISTS roborev.responses",
	}

	// Join all statements to search across the actual executed schema
	allStatements := strings.Join(pgSchemaStatements(), "\n")

	for _, required := range requiredStatements {
		if !strings.Contains(allStatements, required) {
			t.Errorf("Schema missing: %s", required)
		}
	}
}

func TestPgSchemaStatementsContainsRequiredIndexes(t *testing.T) {
	requiredIndexes := []string{
		"idx_review_jobs_source",
		"idx_review_jobs_updated",
		"idx_reviews_job_uuid",
		"idx_reviews_updated",
		"idx_responses_job_uuid",
		"idx_responses_id",
	}

	// Join all statements to search across the actual executed schema
	allStatements := strings.Join(pgSchemaStatements(), "\n")

	for _, idx := range requiredIndexes {
		if !strings.Contains(allStatements, idx) {
			t.Errorf("Schema missing index: %s", idx)
		}
	}
}

// Integration tests require a live PostgreSQL instance.
// Run with: TEST_POSTGRES_URL=postgres://... go test -run Integration

func TestIntegration_PullReviewsFiltersByKnownJobs(t *testing.T) {
	connString := getTestPostgresURL(t)

	ctx := t.Context()
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Clean up test data - use valid UUIDs
	machineID := uuid.NewString()
	otherMachineID := uuid.NewString()
	jobUUID1 := uuid.NewString()
	jobUUID2 := uuid.NewString()
	reviewUUID1 := uuid.NewString()
	reviewUUID2 := uuid.NewString()

	defer cleanupTestData(t, pool, machineID, otherMachineID, []string{jobUUID1, jobUUID2})

	// Register both machines
	if err := pool.RegisterMachine(ctx, machineID, "test"); err != nil {
		t.Fatalf("RegisterMachine failed: %v", err)
	}
	if err := pool.RegisterMachine(ctx, otherMachineID, "other"); err != nil {
		t.Fatalf("RegisterMachine (other) failed: %v", err)
	}

	// Create a repo using the helper
	repoIdentity := "test-repo-" + time.Now().Format("20060102150405")
	repoID, err := pool.GetOrCreateRepo(ctx, repoIdentity)
	if err != nil {
		t.Fatalf("Failed to create repo: %v", err)
	}

	// Create a commit using the helper
	commitID, err := pool.GetOrCreateCommit(ctx, repoID, "abc123", "Test Author", "Test Subject", time.Now())
	if err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}

	// Create two jobs with different UUIDs using correct schema
	for _, jobUUID := range []string{jobUUID1, jobUUID2} {
		_, err = pool.pool.Exec(ctx, `
			INSERT INTO review_jobs (uuid, repo_id, commit_id, git_ref, agent, status, source_machine_id, enqueued_at, created_at, updated_at)
			VALUES ($1, $2, $3, 'HEAD', 'test', 'done', $4, NOW(), NOW(), NOW())
		`, jobUUID, repoID, commitID, machineID)
		if err != nil {
			t.Fatalf("Failed to create job %s: %v", jobUUID, err)
		}
	}

	_, err = pool.pool.Exec(ctx, `
		INSERT INTO reviews (uuid, job_uuid, agent, prompt, output, addressed, created_at, updated_at, updated_by_machine_id)
		VALUES ($1, $2, 'test', 'prompt1', 'output1', false, NOW(), NOW(), $3)
	`, reviewUUID1, jobUUID1, otherMachineID)
	if err != nil {
		t.Fatalf("Failed to create review1: %v", err)
	}

	// Sleep briefly to ensure different timestamps
	time.Sleep(10 * time.Millisecond)

	_, err = pool.pool.Exec(ctx, `
		INSERT INTO reviews (uuid, job_uuid, agent, prompt, output, addressed, created_at, updated_at, updated_by_machine_id)
		VALUES ($1, $2, 'test', 'prompt2', 'output2', false, NOW(), NOW(), $3)
	`, reviewUUID2, jobUUID2, otherMachineID)
	if err != nil {
		t.Fatalf("Failed to create review2: %v", err)
	}

	t.Run("empty knownJobUUIDs returns empty and preserves cursor", func(t *testing.T) {
		reviews, newCursor, err := pool.PullReviews(ctx, machineID, []string{}, "", 100)
		if err != nil {
			t.Fatalf("PullReviews failed: %v", err)
		}
		if len(reviews) != 0 {
			t.Errorf("Expected 0 reviews, got %d", len(reviews))
		}
		if newCursor != "" {
			t.Errorf("Expected empty cursor, got %q", newCursor)
		}
	})

	t.Run("filters to only known job UUIDs", func(t *testing.T) {
		// Only request reviews for job1
		reviews, _, err := pool.PullReviews(ctx, machineID, []string{jobUUID1}, "", 100)
		if err != nil {
			t.Fatalf("PullReviews failed: %v", err)
		}
		if len(reviews) != 1 {
			t.Fatalf("Expected 1 review, got %d", len(reviews))
		}
		if reviews[0].JobUUID != jobUUID1 {
			t.Errorf("Expected job UUID %s, got %s", jobUUID1, reviews[0].JobUUID)
		}
	})

	t.Run("cursor does not skip reviews for later-known jobs", func(t *testing.T) {
		// First pull with only job1 known - gets review1, advances cursor
		reviews1, cursor1, err := pool.PullReviews(ctx, machineID, []string{jobUUID1}, "", 100)
		if err != nil {
			t.Fatalf("First PullReviews failed: %v", err)
		}
		if len(reviews1) != 1 {
			t.Fatalf("Expected 1 review in first pull, got %d", len(reviews1))
		}

		// Second pull with both jobs known - should still get review2
		// even though cursor advanced past review1's timestamp
		reviews2, _, err := pool.PullReviews(ctx, machineID, []string{jobUUID1, jobUUID2}, cursor1, 100)
		if err != nil {
			t.Fatalf("Second PullReviews failed: %v", err)
		}
		if len(reviews2) != 1 {
			t.Fatalf("Expected 1 review in second pull, got %d", len(reviews2))
		}
		if reviews2[0].JobUUID != jobUUID2 {
			t.Errorf("Expected job UUID %s, got %s", jobUUID2, reviews2[0].JobUUID)
		}
	})
}

func getTestPostgresURL(t *testing.T) string {
	t.Helper()
	connString := ""
	// Check common env vars
	for _, envVar := range []string{"TEST_POSTGRES_URL", "POSTGRES_URL", "DATABASE_URL"} {
		if v := lookupEnv(envVar); v != "" {
			connString = v
			break
		}
	}
	if connString == "" {
		t.Skip("No PostgreSQL URL set (TEST_POSTGRES_URL, POSTGRES_URL, or DATABASE_URL)")
	}
	return connString
}

func lookupEnv(key string) string {
	return os.Getenv(key)
}

func cleanupTestData(t *testing.T, pool *PgPool, machineID, otherMachineID string, jobUUIDs []string) {
	t.Helper()
	ctx := t.Context()
	// Clean up in reverse dependency order using tracked UUIDs
	// Delete responses and reviews by job_uuid since that's tracked
	for _, jobUUID := range jobUUIDs {
		pool.pool.Exec(ctx, `DELETE FROM responses WHERE job_uuid = $1`, jobUUID)
		pool.pool.Exec(ctx, `DELETE FROM reviews WHERE job_uuid = $1`, jobUUID)
	}
	pool.pool.Exec(ctx, `DELETE FROM review_jobs WHERE source_machine_id = $1`, machineID)
	pool.pool.Exec(ctx, `DELETE FROM commits WHERE repo_id IN (SELECT id FROM repos WHERE identity LIKE 'test-repo-%')`)
	pool.pool.Exec(ctx, `DELETE FROM repos WHERE identity LIKE 'test-repo-%'`)
	pool.pool.Exec(ctx, `DELETE FROM machines WHERE machine_id = $1`, machineID)
	pool.pool.Exec(ctx, `DELETE FROM machines WHERE machine_id = $1`, otherMachineID)
}

func TestIntegration_EnsureSchema_AutoInitializesVersion(t *testing.T) {
	// This test verifies that EnsureSchema auto-initializes when schema_version table is empty
	connString := getTestPostgresURL(t)

	ctx := t.Context()
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	// Clear schema_version to simulate empty table
	_, _ = pool.pool.Exec(ctx, `DELETE FROM schema_version`)

	// EnsureSchema should succeed and auto-initialize
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify version was inserted
	var version int
	err = pool.pool.QueryRow(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("Failed to query version: %v", err)
	}
	if version != pgSchemaVersion {
		t.Errorf("Expected schema version %d, got %d", pgSchemaVersion, version)
	}
}

func TestIntegration_EnsureSchema_RejectsNewerVersion(t *testing.T) {
	// This test verifies that EnsureSchema returns error when schema version is newer than supported
	connString := getTestPostgresURL(t)

	ctx := t.Context()
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	// First ensure schema exists
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("Initial EnsureSchema failed: %v", err)
	}

	// Insert a newer version
	futureVersion := pgSchemaVersion + 10
	_, err = pool.pool.Exec(ctx, `INSERT INTO schema_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, futureVersion)
	if err != nil {
		t.Fatalf("Failed to insert future version: %v", err)
	}
	defer func() {
		// Clean up - remove future version
		pool.pool.Exec(ctx, `DELETE FROM schema_version WHERE version = $1`, futureVersion)
	}()

	// EnsureSchema should fail with clear error
	err = pool.EnsureSchema(ctx)
	if err == nil {
		t.Fatal("Expected error for newer schema version, but got nil")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Errorf("Expected 'newer than supported' error, got: %v", err)
	}
}

func TestIntegration_EnsureSchema_FreshDatabase(t *testing.T) {
	// This test verifies that a fresh database (no roborev schema) can be initialized
	connString := getTestPostgresURL(t)

	ctx := t.Context()

	// First, drop the roborev schema if it exists to simulate a fresh database
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	tempPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create temp pool: %v", err)
	}

	// Save existing data by checking if schema exists and has data
	var schemaExists bool
	tempPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = 'roborev')`).Scan(&schemaExists)

	if schemaExists {
		// Don't actually drop if it has data - just verify the bootstrap works
		tempPool.Close()

		pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
		if err != nil {
			t.Fatalf("Failed to connect with schema bootstrap: %v", err)
		}
		defer pool.Close()

		if err := pool.EnsureSchema(ctx); err != nil {
			t.Fatalf("EnsureSchema failed on existing schema: %v", err)
		}

		// Verify schema_version table is accessible
		var version int
		err = pool.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
		if err != nil {
			t.Fatalf("Failed to query schema_version: %v", err)
		}
		t.Logf("Schema version: %d", version)

		// Verify branch index exists (created by migration or fresh install)
		var indexExists bool
		err = pool.pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM pg_indexes
				WHERE schemaname = 'roborev' AND indexname = 'idx_review_jobs_branch'
			)
		`).Scan(&indexExists)
		if err != nil {
			t.Fatalf("Failed to check branch index: %v", err)
		}
		if !indexExists {
			t.Errorf("Expected idx_review_jobs_branch to exist after EnsureSchema")
		}
	} else {
		tempPool.Close()

		// Fresh database - NewPgPool should succeed with AfterConnect bootstrap
		pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
		if err != nil {
			t.Fatalf("Failed to connect on fresh database: %v", err)
		}
		defer func() {
			pool.Close()
			// Clean up: drop the schema we created
			cfg, _ := pgxpool.ParseConfig(connString)
			cleanupPool, _ := pgxpool.NewWithConfig(ctx, cfg)
			if cleanupPool != nil {
				cleanupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
				cleanupPool.Close()
			}
		}()

		if err := pool.EnsureSchema(ctx); err != nil {
			t.Fatalf("EnsureSchema failed on fresh database: %v", err)
		}

		// Verify tables were created in roborev schema
		var tableCount int
		err = pool.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema = 'roborev'
		`).Scan(&tableCount)
		if err != nil {
			t.Fatalf("Failed to count tables: %v", err)
		}
		if tableCount < 5 {
			t.Errorf("Expected at least 5 tables in roborev schema, got %d", tableCount)
		}

		// Verify branch index was created for fresh install
		var indexExists bool
		err = pool.pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM pg_indexes
				WHERE schemaname = 'roborev' AND indexname = 'idx_review_jobs_branch'
			)
		`).Scan(&indexExists)
		if err != nil {
			t.Fatalf("Failed to check branch index: %v", err)
		}
		if !indexExists {
			t.Errorf("Expected idx_review_jobs_branch to exist on fresh install")
		}
	}
}

func TestIntegration_EnsureSchema_MigratesLegacyTables(t *testing.T) {
	// This test verifies that tables in public schema are migrated to roborev
	connString := getTestPostgresURL(t)

	ctx := t.Context()

	// Create a pool without AfterConnect to set up legacy tables in public
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	setupPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create setup pool: %v", err)
	}

	// Check if roborev schema already has tables (skip test if so to avoid data loss)
	var roborevHasTables bool
	setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'roborev' AND table_name = 'schema_version'
		)
	`).Scan(&roborevHasTables)

	if roborevHasTables {
		setupPool.Close()
		t.Skip("Skipping migration test: roborev schema already has tables")
	}

	// Check if public already has legacy roborev tables
	var publicHasLegacy bool
	setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_version'
		)
	`).Scan(&publicHasLegacy)

	if publicHasLegacy {
		setupPool.Close()
		t.Skip("Skipping migration test: public schema has legacy tables that may contain real data")
	}

	// Create legacy table in public schema
	_, err = setupPool.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_version (version INTEGER PRIMARY KEY)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create legacy table: %v", err)
	}
	_, err = setupPool.Exec(ctx, `INSERT INTO public.schema_version (version) VALUES (1) ON CONFLICT DO NOTHING`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert legacy data: %v", err)
	}
	setupPool.Close()

	// Now connect with the normal pool and run EnsureSchema
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer func() {
		pool.Close()
		// Cleanup
		cfg, _ := pgxpool.ParseConfig(connString)
		cleanupPool, _ := pgxpool.NewWithConfig(ctx, cfg)
		if cleanupPool != nil {
			cleanupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
			cleanupPool.Close()
		}
	}()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify legacy table was migrated (no longer in public)
	var stillInPublic bool
	pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_version'
		)
	`).Scan(&stillInPublic)

	if stillInPublic {
		t.Error("Legacy table still exists in public schema after migration")
	}

	// Verify data is accessible in roborev schema
	var version int
	err = pool.pool.QueryRow(ctx, `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("Failed to query migrated data: %v", err)
	}
	if version != 1 {
		t.Errorf("Expected migrated version 1, got %d", version)
	}
}

func TestIntegration_EnsureSchema_MigratesMultipleTablesAndMixedState(t *testing.T) {
	// This test verifies migration with multiple tables in public and mixed state
	// (some tables already in roborev, some in public)
	connString := getTestPostgresURL(t)

	ctx := t.Context()

	// Create a pool without AfterConnect to set up test state
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	setupPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create setup pool: %v", err)
	}

	// Register cleanup immediately to ensure test tables are removed even if setup fails
	t.Cleanup(func() {
		cleanupCfg, _ := pgxpool.ParseConfig(connString)
		cleanupPool, _ := pgxpool.NewWithConfig(ctx, cleanupCfg)
		if cleanupPool != nil {
			cleanupPool.Exec(ctx, "DROP TABLE IF EXISTS public.schema_version")
			cleanupPool.Exec(ctx, "DROP TABLE IF EXISTS public.repos")
			cleanupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
			cleanupPool.Close()
		}
	})

	// Check if roborev schema already has tables (skip test if so to avoid data loss)
	var roborevHasTables bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'roborev' AND table_name = 'schema_version'
		)
	`).Scan(&roborevHasTables)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check roborev schema: %v", err)
	}

	if roborevHasTables {
		setupPool.Close()
		t.Skip("Skipping migration test: roborev schema already has tables")
	}

	// Check if public already has legacy roborev tables
	var publicHasLegacy bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_version'
		)
	`).Scan(&publicHasLegacy)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check public schema: %v", err)
	}

	if publicHasLegacy {
		setupPool.Close()
		t.Skip("Skipping migration test: public schema has legacy tables that may contain real data")
	}

	// Create roborev schema for mixed state test
	_, err = setupPool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS roborev`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create roborev schema: %v", err)
	}

	// Create legacy tables in public schema (simulating old installation)
	_, err = setupPool.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_version (version INTEGER PRIMARY KEY)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create legacy schema_version: %v", err)
	}
	_, err = setupPool.Exec(ctx, `INSERT INTO public.schema_version (version) VALUES (1) ON CONFLICT DO NOTHING`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert legacy version: %v", err)
	}

	// Create repos table in public (second legacy table)
	_, err = setupPool.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create legacy repos: %v", err)
	}
	_, err = setupPool.Exec(ctx, `INSERT INTO public.repos (identity) VALUES ('test-repo-legacy') ON CONFLICT DO NOTHING`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert legacy repo: %v", err)
	}

	// Create machines table directly in roborev (simulating partial migration)
	_, err = setupPool.Exec(ctx, `CREATE TABLE IF NOT EXISTS roborev.machines (id SERIAL PRIMARY KEY, machine_id UUID UNIQUE NOT NULL, name TEXT)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create machines in roborev: %v", err)
	}

	setupPool.Close()

	// Now connect with the normal pool and run EnsureSchema
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify schema_version was migrated from public
	var svInPublic bool
	err = pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_version'
		)
	`).Scan(&svInPublic)
	if err != nil {
		t.Fatalf("Failed to check schema_version location: %v", err)
	}
	if svInPublic {
		t.Error("schema_version still exists in public schema")
	}

	// Verify repos was migrated from public
	var reposInPublic bool
	err = pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'repos'
		)
	`).Scan(&reposInPublic)
	if err != nil {
		t.Fatalf("Failed to check repos location: %v", err)
	}
	if reposInPublic {
		t.Error("repos still exists in public schema")
	}

	// Verify machines exists in roborev (was already there)
	var machinesInRoborev bool
	err = pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'roborev' AND table_name = 'machines'
		)
	`).Scan(&machinesInRoborev)
	if err != nil {
		t.Fatalf("Failed to check machines location: %v", err)
	}
	if !machinesInRoborev {
		t.Error("machines should exist in roborev schema")
	}

	// Verify data is accessible
	var version int
	err = pool.pool.QueryRow(ctx, `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("Failed to query migrated schema_version: %v", err)
	}
	if version != 1 {
		t.Errorf("Expected migrated version 1, got %d", version)
	}

	var repoIdentity string
	err = pool.pool.QueryRow(ctx, `SELECT identity FROM repos WHERE identity = 'test-repo-legacy'`).Scan(&repoIdentity)
	if err != nil {
		t.Fatalf("Failed to query migrated repo: %v", err)
	}
	if repoIdentity != "test-repo-legacy" {
		t.Errorf("Expected repo identity 'test-repo-legacy', got %q", repoIdentity)
	}
}

func TestIntegration_EnsureSchema_DualSchemaWithDataErrors(t *testing.T) {
	// This test verifies that having a table in both schemas with data in public
	// causes an error requiring manual reconciliation.
	connString := getTestPostgresURL(t)
	ctx := t.Context()

	// Create a pool without AfterConnect to set up test state
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	setupPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create setup pool: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		cleanupCfg, _ := pgxpool.ParseConfig(connString)
		cleanupPool, _ := pgxpool.NewWithConfig(ctx, cleanupCfg)
		if cleanupPool != nil {
			cleanupPool.Exec(ctx, "DROP TABLE IF EXISTS public.repos")
			cleanupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
			cleanupPool.Close()
		}
	})

	// Check if roborev schema already has tables
	var roborevHasTables bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'roborev' AND table_name = 'repos'
		)
	`).Scan(&roborevHasTables)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check roborev schema: %v", err)
	}
	if roborevHasTables {
		setupPool.Close()
		t.Skip("Skipping test: roborev.repos already exists")
	}

	// Check if public already has repos table
	var publicHasRepos bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'repos'
		)
	`).Scan(&publicHasRepos)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check public schema: %v", err)
	}
	if publicHasRepos {
		setupPool.Close()
		t.Skip("Skipping test: public.repos already exists")
	}

	// Create roborev schema with repos table
	_, err = setupPool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS roborev`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create roborev schema: %v", err)
	}
	_, err = setupPool.Exec(ctx, `CREATE TABLE roborev.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create roborev.repos: %v", err)
	}

	// Create public.repos table with data
	_, err = setupPool.Exec(ctx, `CREATE TABLE public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create public.repos: %v", err)
	}
	_, err = setupPool.Exec(ctx, `INSERT INTO public.repos (identity) VALUES ('legacy-repo')`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert into public.repos: %v", err)
	}

	setupPool.Close()

	// Now connect and try EnsureSchema - should fail
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	err = pool.EnsureSchema(ctx)
	if err == nil {
		t.Fatal("Expected EnsureSchema to fail with dual-schema data, but it succeeded")
	}
	if !strings.Contains(err.Error(), "manual reconciliation required") {
		t.Errorf("Expected error about manual reconciliation, got: %v", err)
	}
}

func TestIntegration_EnsureSchema_EmptyPublicTableDropped(t *testing.T) {
	// This test verifies that an empty table in public is dropped when the same
	// table exists in roborev schema.
	connString := getTestPostgresURL(t)
	ctx := t.Context()

	// Create a pool without AfterConnect to set up test state
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	setupPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create setup pool: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		cleanupCfg, _ := pgxpool.ParseConfig(connString)
		cleanupPool, _ := pgxpool.NewWithConfig(ctx, cleanupCfg)
		if cleanupPool != nil {
			cleanupPool.Exec(ctx, "DROP TABLE IF EXISTS public.repos")
			cleanupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
			cleanupPool.Close()
		}
	})

	// Check if roborev schema already has tables
	var roborevHasTables bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'roborev' AND table_name = 'repos'
		)
	`).Scan(&roborevHasTables)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check roborev schema: %v", err)
	}
	if roborevHasTables {
		setupPool.Close()
		t.Skip("Skipping test: roborev.repos already exists")
	}

	// Check if public already has repos table
	var publicHasRepos bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'repos'
		)
	`).Scan(&publicHasRepos)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check public schema: %v", err)
	}
	if publicHasRepos {
		setupPool.Close()
		t.Skip("Skipping test: public.repos already exists")
	}

	// Create roborev schema with repos table containing data
	_, err = setupPool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS roborev`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create roborev schema: %v", err)
	}
	_, err = setupPool.Exec(ctx, `CREATE TABLE roborev.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create roborev.repos: %v", err)
	}
	_, err = setupPool.Exec(ctx, `INSERT INTO roborev.repos (identity) VALUES ('new-repo')`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert into roborev.repos: %v", err)
	}

	// Create empty public.repos table
	_, err = setupPool.Exec(ctx, `CREATE TABLE public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create public.repos: %v", err)
	}
	// Note: no data inserted - empty table

	setupPool.Close()

	// Now connect and run EnsureSchema - should succeed and drop empty public.repos
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	err = pool.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify public.repos was dropped
	var publicReposExists bool
	err = pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'repos'
		)
	`).Scan(&publicReposExists)
	if err != nil {
		t.Fatalf("Failed to check public.repos: %v", err)
	}
	if publicReposExists {
		t.Error("Empty public.repos should have been dropped")
	}

	// Verify roborev.repos still exists with data
	var repoIdentity string
	err = pool.pool.QueryRow(ctx, `SELECT identity FROM roborev.repos`).Scan(&repoIdentity)
	if err != nil {
		t.Fatalf("Failed to query roborev.repos: %v", err)
	}
	if repoIdentity != "new-repo" {
		t.Errorf("Expected identity 'new-repo', got %q", repoIdentity)
	}
}

func TestIntegration_EnsureSchema_MigratesPublicTableWithData(t *testing.T) {
	// This test verifies that a public table with data is properly migrated
	// to roborev schema when roborev doesn't have that table yet.
	// This is the normal migration path and also what the 42P01 fallback uses.
	connString := getTestPostgresURL(t)
	ctx := t.Context()

	// Create a pool without AfterConnect to set up test state
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	setupPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create setup pool: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		cleanupCfg, _ := pgxpool.ParseConfig(connString)
		cleanupPool, _ := pgxpool.NewWithConfig(ctx, cleanupCfg)
		if cleanupPool != nil {
			cleanupPool.Exec(ctx, "DROP TABLE IF EXISTS public.repos")
			cleanupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
			cleanupPool.Close()
		}
	})

	// Check if roborev.repos already exists
	var roborevHasRepos bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'roborev' AND table_name = 'repos'
		)
	`).Scan(&roborevHasRepos)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check roborev schema: %v", err)
	}
	if roborevHasRepos {
		setupPool.Close()
		t.Skip("Skipping test: roborev.repos already exists")
	}

	// Check if public.repos already exists
	var publicHasRepos bool
	err = setupPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'repos'
		)
	`).Scan(&publicHasRepos)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to check public schema: %v", err)
	}
	if publicHasRepos {
		setupPool.Close()
		t.Skip("Skipping test: public.repos already exists")
	}

	// Create roborev schema but NOT the repos table
	_, err = setupPool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS roborev`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create roborev schema: %v", err)
	}

	// Create public.repos table with data
	_, err = setupPool.Exec(ctx, `CREATE TABLE public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to create public.repos: %v", err)
	}
	_, err = setupPool.Exec(ctx, `INSERT INTO public.repos (identity) VALUES ('migrated-repo-1'), ('migrated-repo-2')`)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert into public.repos: %v", err)
	}

	setupPool.Close()

	// Now connect and run EnsureSchema - should migrate public.repos to roborev.repos
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	err = pool.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify public.repos was moved (no longer in public)
	var publicReposExists bool
	err = pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'repos'
		)
	`).Scan(&publicReposExists)
	if err != nil {
		t.Fatalf("Failed to check public.repos: %v", err)
	}
	if publicReposExists {
		t.Error("public.repos should have been moved to roborev schema")
	}

	// Verify data is accessible in roborev.repos
	var count int
	err = pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM roborev.repos WHERE identity IN ('migrated-repo-1', 'migrated-repo-2')`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count migrated repos: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 migrated repos, got %d", count)
	}
}

func TestIntegration_GetDatabaseID_GeneratesAndPersists(t *testing.T) {
	connString := getTestPostgresURL(t)

	ctx := t.Context()
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Get the database ID - should create one if it doesn't exist
	dbID1, err := pool.GetDatabaseID(ctx)
	if err != nil {
		t.Fatalf("GetDatabaseID failed: %v", err)
	}
	if dbID1 == "" {
		t.Fatal("Expected non-empty database ID")
	}

	// Get it again - should return the same ID
	dbID2, err := pool.GetDatabaseID(ctx)
	if err != nil {
		t.Fatalf("GetDatabaseID (second call) failed: %v", err)
	}
	if dbID2 != dbID1 {
		t.Errorf("Expected same database ID on second call, got %s vs %s", dbID1, dbID2)
	}

	// Verify it's stored in sync_metadata
	var storedID string
	err = pool.pool.QueryRow(ctx, `SELECT value FROM sync_metadata WHERE key = 'database_id'`).Scan(&storedID)
	if err != nil {
		t.Fatalf("Failed to query sync_metadata: %v", err)
	}
	if storedID != dbID1 {
		t.Errorf("Stored ID %s doesn't match returned ID %s", storedID, dbID1)
	}

	t.Logf("Database ID: %s", dbID1)
}

func TestIntegration_NewDatabaseClearsSyncedAt(t *testing.T) {
	// This test verifies that when connecting to a different Postgres database
	// (different database_id), the SQLite synced_at timestamps get cleared.
	connString := getTestPostgresURL(t)

	ctx := t.Context()
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Create a test SQLite database
	sqliteDB, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open SQLite: %v", err)
	}
	defer sqliteDB.Close()

	// Create test data with synced_at already set
	repo, err := sqliteDB.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := sqliteDB.GetOrCreateCommit(repo.ID, "test-sha", "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := sqliteDB.EnqueueJob(repo.ID, commit.ID, "test-sha", "", "test", "", "thorough")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	_, err = sqliteDB.ClaimJob("worker")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	err = sqliteDB.CompleteJob(job.ID, "test", "prompt", "output")
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Mark everything as synced to simulate previous sync
	err = sqliteDB.MarkJobSynced(job.ID)
	if err != nil {
		t.Fatalf("MarkJobSynced failed: %v", err)
	}
	review, err := sqliteDB.GetReviewByJobID(job.ID)
	if err != nil {
		t.Fatalf("GetReviewByJobID failed: %v", err)
	}
	err = sqliteDB.MarkReviewSynced(review.ID)
	if err != nil {
		t.Fatalf("MarkReviewSynced failed: %v", err)
	}

	// Set a fake old sync target ID (simulating we synced to a different database before)
	oldTargetID := "old-database-" + uuid.NewString()
	err = sqliteDB.SetSyncState(SyncStateSyncTargetID, oldTargetID)
	if err != nil {
		t.Fatalf("SetSyncState failed: %v", err)
	}

	// Verify job is currently synced
	machineID, _ := sqliteDB.GetMachineID()
	jobsToSync, _ := sqliteDB.GetJobsToSync(machineID, 100)
	if len(jobsToSync) != 0 {
		t.Errorf("Expected 0 jobs to sync (all synced), got %d", len(jobsToSync))
	}

	// Now get the database ID from the actual Postgres (which is different from oldTargetID)
	dbID, err := pool.GetDatabaseID(ctx)
	if err != nil {
		t.Fatalf("GetDatabaseID failed: %v", err)
	}

	// Simulate what connect() does: detect new database and clear synced_at
	lastTargetID, _ := sqliteDB.GetSyncState(SyncStateSyncTargetID)
	if lastTargetID != "" && lastTargetID != dbID {
		// This is what the sync worker does
		t.Logf("Detected new database (was %s..., now %s...), clearing synced_at", lastTargetID[:8], dbID[:8])
		err = sqliteDB.ClearAllSyncedAt()
		if err != nil {
			t.Fatalf("ClearAllSyncedAt failed: %v", err)
		}
	}
	err = sqliteDB.SetSyncState(SyncStateSyncTargetID, dbID)
	if err != nil {
		t.Fatalf("SetSyncState (new target) failed: %v", err)
	}

	// Now the job should be returned for sync again
	jobsToSync, err = sqliteDB.GetJobsToSync(machineID, 100)
	if err != nil {
		t.Fatalf("GetJobsToSync failed: %v", err)
	}
	if len(jobsToSync) != 1 {
		t.Errorf("Expected 1 job to sync after clear, got %d", len(jobsToSync))
	}

	// Verify sync target was updated
	newTargetID, _ := sqliteDB.GetSyncState(SyncStateSyncTargetID)
	if newTargetID != dbID {
		t.Errorf("Expected sync target ID to be %s, got %s", dbID, newTargetID)
	}
}

func TestIntegration_BatchOperations(t *testing.T) {
	connString := getTestPostgresURL(t)

	ctx := t.Context()
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Create a test repo
	repoID, err := pool.GetOrCreateRepo(ctx, "https://github.com/test/batch-test.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	t.Run("BatchUpsertJobs", func(t *testing.T) {
		// Create multiple jobs with prepared IDs
		var jobs []JobWithPgIDs
		for i := 0; i < 5; i++ {
			commitID, err := pool.GetOrCreateCommit(ctx, repoID, fmt.Sprintf("batch-test-sha-%d", i), "Author", "Subject", time.Now())
			if err != nil {
				t.Fatalf("GetOrCreateCommit failed: %v", err)
			}
			jobs = append(jobs, JobWithPgIDs{
				Job: SyncableJob{
					UUID:            uuid.NewString(),
					RepoIdentity:    "https://github.com/test/batch-test.git",
					CommitSHA:       fmt.Sprintf("batch-test-sha-%d", i),
					GitRef:          "test-ref",
					Agent:           "test",
					Status:          "done",
					SourceMachineID: "11111111-1111-1111-1111-111111111111",
					EnqueuedAt:      time.Now(),
				},
				PgRepoID:   repoID,
				PgCommitID: &commitID,
			})
		}

		success, err := pool.BatchUpsertJobs(ctx, jobs)
		if err != nil {
			t.Fatalf("BatchUpsertJobs failed: %v", err)
		}
		successCount := 0
		for _, ok := range success {
			if ok {
				successCount++
			}
		}
		if successCount != 5 {
			t.Errorf("Expected 5 jobs upserted, got %d", successCount)
		}

		// Verify jobs exist
		var jobCount int
		err = pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM review_jobs WHERE source_machine_id = '11111111-1111-1111-1111-111111111111'`).Scan(&jobCount)
		if err != nil {
			t.Fatalf("Count query failed: %v", err)
		}
		if jobCount < 5 {
			t.Errorf("Expected at least 5 jobs in database, got %d", jobCount)
		}
	})

	t.Run("BatchUpsertReviews", func(t *testing.T) {
		// Get a job UUID to reference
		var jobUUID string
		err := pool.pool.QueryRow(ctx, `SELECT uuid FROM review_jobs WHERE source_machine_id = '11111111-1111-1111-1111-111111111111' LIMIT 1`).Scan(&jobUUID)
		if err != nil {
			t.Fatalf("Failed to get job UUID: %v", err)
		}

		reviews := []SyncableReview{
			{
				UUID:               uuid.NewString(),
				JobUUID:            jobUUID,
				Agent:              "test",
				Prompt:             "test prompt 1",
				Output:             "test output 1",
				Addressed:          false,
				UpdatedByMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:          time.Now(),
			},
			{
				UUID:               uuid.NewString(),
				JobUUID:            jobUUID,
				Agent:              "test",
				Prompt:             "test prompt 2",
				Output:             "test output 2",
				Addressed:          true,
				UpdatedByMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:          time.Now(),
			},
		}

		success, err := pool.BatchUpsertReviews(ctx, reviews)
		if err != nil {
			t.Fatalf("BatchUpsertReviews failed: %v", err)
		}
		successCount := 0
		for _, ok := range success {
			if ok {
				successCount++
			}
		}
		if successCount != 2 {
			t.Errorf("Expected 2 reviews upserted, got %d", successCount)
		}
	})

	t.Run("BatchInsertResponses", func(t *testing.T) {
		// Get a job UUID to reference
		var jobUUID string
		err := pool.pool.QueryRow(ctx, `SELECT uuid FROM review_jobs WHERE source_machine_id = '11111111-1111-1111-1111-111111111111' LIMIT 1`).Scan(&jobUUID)
		if err != nil {
			t.Fatalf("Failed to get job UUID: %v", err)
		}

		responses := []SyncableResponse{
			{
				UUID:            uuid.NewString(),
				JobUUID:         jobUUID,
				Responder:       "user1",
				Response:        "response 1",
				SourceMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:       time.Now(),
			},
			{
				UUID:            uuid.NewString(),
				JobUUID:         jobUUID,
				Responder:       "user2",
				Response:        "response 2",
				SourceMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:       time.Now(),
			},
			{
				UUID:            uuid.NewString(),
				JobUUID:         jobUUID,
				Responder:       "agent",
				Response:        "response 3",
				SourceMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:       time.Now(),
			},
		}

		success, err := pool.BatchInsertResponses(ctx, responses)
		if err != nil {
			t.Fatalf("BatchInsertResponses failed: %v", err)
		}
		successCount := 0
		for _, ok := range success {
			if ok {
				successCount++
			}
		}
		if successCount != 3 {
			t.Errorf("Expected 3 responses inserted, got %d", successCount)
		}
	})

	t.Run("empty batches are no-op", func(t *testing.T) {
		success, err := pool.BatchUpsertJobs(ctx, []JobWithPgIDs{})
		if err != nil {
			t.Errorf("BatchUpsertJobs with empty slice failed: %v", err)
		}
		if success != nil {
			t.Errorf("Expected nil for empty batch, got %v", success)
		}

		success, err = pool.BatchUpsertReviews(ctx, []SyncableReview{})
		if err != nil {
			t.Errorf("BatchUpsertReviews with empty slice failed: %v", err)
		}
		if success != nil {
			t.Errorf("Expected nil for empty batch, got %v", success)
		}

		success, err = pool.BatchInsertResponses(ctx, []SyncableResponse{})
		if err != nil {
			t.Errorf("BatchInsertResponses with empty slice failed: %v", err)
		}
		if success != nil {
			t.Errorf("Expected nil for empty batch, got %v", success)
		}
	})

	t.Run("BatchUpsertReviews partial failure with invalid FK", func(t *testing.T) {
		// Get a valid job UUID
		var validJobUUID string
		err := pool.pool.QueryRow(ctx, `SELECT uuid FROM review_jobs WHERE source_machine_id = '11111111-1111-1111-1111-111111111111' LIMIT 1`).Scan(&validJobUUID)
		if err != nil {
			t.Fatalf("Failed to get job UUID: %v", err)
		}

		// Create a batch with one valid and one invalid review (bad FK)
		// Note: PostgreSQL transactions are atomic - if any statement fails due to FK violation,
		// the entire batch transaction is rolled back. The success slice indicates which
		// individual statements executed without error, but actual persistence depends on
		// the entire batch succeeding.
		validReviewUUID := uuid.NewString()
		reviews := []SyncableReview{
			{
				UUID:               validReviewUUID,
				JobUUID:            validJobUUID, // Valid FK
				Agent:              "test",
				Prompt:             "valid review",
				Output:             "output",
				UpdatedByMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:          time.Now(),
			},
			{
				UUID:               uuid.NewString(),
				JobUUID:            "00000000-0000-0000-0000-000000000000", // Invalid FK - will fail
				Agent:              "test",
				Prompt:             "invalid review",
				Output:             "output",
				UpdatedByMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:          time.Now(),
			},
		}

		success, err := pool.BatchUpsertReviews(ctx, reviews)

		// Should return an error (from the failed row)
		if err == nil {
			t.Error("Expected error from batch with invalid FK, got nil")
		}

		// Should have correct success flags - note these are informational only
		// and don't guarantee persistence due to transaction rollback
		if len(success) != 2 {
			t.Fatalf("Expected success slice length 2, got %d", len(success))
		}

		// First statement executes without error, second fails
		if !success[0] {
			t.Error("Expected success[0]=true (valid FK)")
		}
		if success[1] {
			t.Error("Expected success[1]=false (invalid FK)")
		}

		// Verify DB state: due to PostgreSQL transaction semantics, the FK violation
		// causes the entire batch to be rolled back, so no reviews should be persisted
		var count int
		err = pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM reviews WHERE uuid = $1`, validReviewUUID).Scan(&count)
		if err != nil {
			t.Fatalf("Failed to query review: %v", err)
		}
		// The valid review was rolled back along with the failed one
		if count != 0 {
			t.Errorf("Expected 0 reviews (batch rolled back due to FK failure), got %d", count)
		}
	})

	t.Run("BatchInsertResponses partial failure with invalid FK", func(t *testing.T) {
		// Get a valid job UUID
		var validJobUUID string
		err := pool.pool.QueryRow(ctx, `SELECT uuid FROM review_jobs WHERE source_machine_id = '11111111-1111-1111-1111-111111111111' LIMIT 1`).Scan(&validJobUUID)
		if err != nil {
			t.Fatalf("Failed to get job UUID: %v", err)
		}

		responses := []SyncableResponse{
			{
				UUID:            uuid.NewString(),
				JobUUID:         validJobUUID, // Valid FK
				Responder:       "user",
				Response:        "valid response",
				SourceMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:       time.Now(),
			},
			{
				UUID:            uuid.NewString(),
				JobUUID:         "00000000-0000-0000-0000-000000000000", // Invalid FK
				Responder:       "user",
				Response:        "invalid response",
				SourceMachineID: "11111111-1111-1111-1111-111111111111",
				CreatedAt:       time.Now(),
			},
		}

		success, err := pool.BatchInsertResponses(ctx, responses)

		// Should return an error
		if err == nil {
			t.Error("Expected error from batch with invalid FK, got nil")
		}

		// Check success flags
		if len(success) != 2 {
			t.Fatalf("Expected success slice length 2, got %d", len(success))
		}
		if !success[0] {
			t.Error("Expected success[0]=true (valid FK)")
		}
		if success[1] {
			t.Error("Expected success[1]=false (invalid FK)")
		}
	})
}

func TestIntegration_EnsureSchema_MigratesV1ToV2(t *testing.T) {
	// This test verifies that a v1 schema (without model column) gets migrated to v2
	connString := getTestPostgresURL(t)
	ctx := t.Context()

	// Create a pool without AfterConnect to set up test state
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	setupPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Failed to create setup pool: %v", err)
	}

	// Drop any existing schema to start fresh - this test needs to verify v1→v2 migration
	_, err = setupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to drop existing schema: %v", err)
	}

	// Cleanup after test
	t.Cleanup(func() {
		cleanupCfg, _ := pgxpool.ParseConfig(connString)
		cleanupPool, _ := pgxpool.NewWithConfig(ctx, cleanupCfg)
		if cleanupPool != nil {
			cleanupPool.Exec(ctx, "DROP SCHEMA IF EXISTS roborev CASCADE")
			cleanupPool.Close()
		}
	})

	// Load and execute v1 schema from embedded SQL file
	// Use same parsing logic as pgSchemaStatements() to handle comments correctly
	for _, stmt := range strings.Split(postgresV1Schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		// Skip statements that are only comments
		hasCode := false
		for _, line := range strings.Split(stmt, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "--") {
				hasCode = true
				break
			}
		}
		if !hasCode {
			continue
		}
		if _, err := setupPool.Exec(ctx, stmt); err != nil {
			setupPool.Close()
			t.Fatalf("Failed to execute v1 schema statement: %v\nStatement: %s", err, stmt)
		}
	}

	// Insert a test job to verify data survives migration
	testJobUUID := uuid.NewString()
	var repoID int64
	err = setupPool.QueryRow(ctx, `
		INSERT INTO roborev.repos (identity) VALUES ('test-repo-v1-migration') RETURNING id
	`).Scan(&repoID)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert test repo: %v", err)
	}
	_, err = setupPool.Exec(ctx, `
		INSERT INTO roborev.review_jobs (uuid, repo_id, git_ref, agent, status, source_machine_id, enqueued_at)
		VALUES ($1, $2, 'HEAD', 'test-agent', 'done', '00000000-0000-0000-0000-000000000001', NOW())
	`, testJobUUID, repoID)
	if err != nil {
		setupPool.Close()
		t.Fatalf("Failed to insert test job: %v", err)
	}

	setupPool.Close()

	// Now connect with the normal pool and run EnsureSchema - should migrate v1→v2
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	err = pool.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify schema version advanced to 2
	var version int
	err = pool.pool.QueryRow(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("Failed to query schema version: %v", err)
	}
	if version != pgSchemaVersion {
		t.Errorf("Expected schema version %d, got %d", pgSchemaVersion, version)
	}

	// Verify model column was added
	var hasModelColumn bool
	err = pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'roborev' AND table_name = 'review_jobs' AND column_name = 'model'
		)
	`).Scan(&hasModelColumn)
	if err != nil {
		t.Fatalf("Failed to check for model column: %v", err)
	}
	if !hasModelColumn {
		t.Error("Expected model column to exist after v1→v2 migration")
	}

	// Verify pre-existing job survived migration with model=NULL
	var jobAgent string
	var jobModel *string
	err = pool.pool.QueryRow(ctx, `SELECT agent, model FROM review_jobs WHERE uuid = $1`, testJobUUID).Scan(&jobAgent, &jobModel)
	if err != nil {
		t.Fatalf("Failed to query test job after migration: %v", err)
	}
	if jobAgent != "test-agent" {
		t.Errorf("Expected agent 'test-agent', got %q", jobAgent)
	}
	if jobModel != nil {
		t.Errorf("Expected model to be NULL for pre-migration job, got %q", *jobModel)
	}
}

func TestIntegration_UpsertJob_BackfillsModel(t *testing.T) {
	// This test verifies that upserting a job with a model value backfills
	// an existing job that has NULL model (COALESCE behavior)
	connString := getTestPostgresURL(t)
	ctx := t.Context()

	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Create test data
	machineID := uuid.NewString()
	jobUUID := uuid.NewString()
	repoIdentity := "test-repo-backfill-" + time.Now().Format("20060102150405")

	defer func() {
		// Cleanup
		pool.pool.Exec(ctx, `DELETE FROM review_jobs WHERE uuid = $1`, jobUUID)
		pool.pool.Exec(ctx, `DELETE FROM repos WHERE identity = $1`, repoIdentity)
		pool.pool.Exec(ctx, `DELETE FROM machines WHERE machine_id = $1`, machineID)
	}()

	// Register machine and create repo
	if err := pool.RegisterMachine(ctx, machineID, "test"); err != nil {
		t.Fatalf("RegisterMachine failed: %v", err)
	}
	repoID, err := pool.GetOrCreateRepo(ctx, repoIdentity)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Insert job with NULL model directly
	_, err = pool.pool.Exec(ctx, `
		INSERT INTO review_jobs (uuid, repo_id, git_ref, agent, status, source_machine_id, enqueued_at, created_at, updated_at)
		VALUES ($1, $2, 'HEAD', 'test-agent', 'done', $3, NOW(), NOW(), NOW())
	`, jobUUID, repoID, machineID)
	if err != nil {
		t.Fatalf("Failed to insert job with NULL model: %v", err)
	}

	// Verify model is NULL
	var modelBefore *string
	err = pool.pool.QueryRow(ctx, `SELECT model FROM review_jobs WHERE uuid = $1`, jobUUID).Scan(&modelBefore)
	if err != nil {
		t.Fatalf("Failed to query model before: %v", err)
	}
	if modelBefore != nil {
		t.Fatalf("Expected model to be NULL before upsert, got %q", *modelBefore)
	}

	// Upsert with a model value - should backfill
	job := SyncableJob{
		UUID:            jobUUID,
		RepoIdentity:    repoIdentity,
		GitRef:          "HEAD",
		Agent:           "test-agent",
		Model:           "gpt-4", // Now providing a model
		Status:          "done",
		SourceMachineID: machineID,
		EnqueuedAt:      time.Now(),
	}
	err = pool.UpsertJob(ctx, job, repoID, nil)
	if err != nil {
		t.Fatalf("UpsertJob failed: %v", err)
	}

	// Verify model was backfilled
	var modelAfter *string
	err = pool.pool.QueryRow(ctx, `SELECT model FROM review_jobs WHERE uuid = $1`, jobUUID).Scan(&modelAfter)
	if err != nil {
		t.Fatalf("Failed to query model after: %v", err)
	}
	if modelAfter == nil {
		t.Error("Expected model to be backfilled, but it's still NULL")
	} else if *modelAfter != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got %q", *modelAfter)
	}

	// Also verify that upserting with empty model doesn't clear existing model
	job.Model = "" // Empty model
	err = pool.UpsertJob(ctx, job, repoID, nil)
	if err != nil {
		t.Fatalf("UpsertJob (empty model) failed: %v", err)
	}

	var modelPreserved *string
	err = pool.pool.QueryRow(ctx, `SELECT model FROM review_jobs WHERE uuid = $1`, jobUUID).Scan(&modelPreserved)
	if err != nil {
		t.Fatalf("Failed to query model preserved: %v", err)
	}
	if modelPreserved == nil || *modelPreserved != "gpt-4" {
		t.Errorf("Expected model to be preserved as 'gpt-4' when upserting with empty model, got %v", modelPreserved)
	}
}
